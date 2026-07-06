package target

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/facts"
)

// DefaultResolveQueue is the queue group masters join when serving the
// target-resolution service. All masters share the group, so each request is
// handled by exactly one master.
const DefaultResolveQueue = "zester-target-resolvers"

// defaultResolveTimeout bounds both client requests (when no explicit timeout
// is configured) and server-side resolve execution.
const defaultResolveTimeout = 5 * time.Second

// ResolveRequest asks the master-side resolution service to expand a target
// expression into the matching peel IDs (and optionally their facts).
type ResolveRequest struct {
	// Expr is the target expression, e.g. "web-*" or "G@os.name:linux".
	Expr string `msgpack:"expr"`
	// Type is the target type name ("glob", "pcre", "fact", "settings",
	// "compound", "list"). Empty means auto-detect via DetectType.
	Type string `msgpack:"type,omitempty"`
	// WantFacts requests the matched peels' facts in the response.
	WantFacts bool `msgpack:"want_facts,omitempty"`
}

// ResolveResponse carries the resolution result.
type ResolveResponse struct {
	// Peels is the sorted-or-matcher-ordered list of matching peel IDs.
	Peels []string `msgpack:"peels,omitempty"`
	// Facts maps matched peel IDs to their nested facts. Only populated
	// when the request set WantFacts.
	Facts map[string]map[string]any `msgpack:"facts,omitempty"`
	// Err is a non-empty resolution error message.
	Err string `msgpack:"err,omitempty"`
}

// ResolveFunc performs a target resolution on the server side. targetType is
// the string form of a TargetType ("" means auto-detect). factsByPeel maps
// each matched peel to its nested facts; implementations may return a nil map
// when facts are unavailable.
type ResolveFunc func(ctx context.Context, expr, targetType string) (peels []string, factsByPeel map[string]map[string]any, err error)

// ParseType converts a target type name (as produced by TargetType.String())
// back into a TargetType.
func ParseType(s string) (TargetType, error) {
	switch strings.ToLower(s) {
	case "glob":
		return Glob, nil
	case "pcre":
		return PCRE, nil
	case "fact":
		return Fact, nil
	case "settings":
		return Settings, nil
	case "compound":
		return Compound, nil
	case "list":
		return List, nil
	default:
		return Glob, fmt.Errorf("target: unknown target type %q", s)
	}
}

// StartResolveService subscribes on bus.SubjectTargetResolve as a member of
// the given queue group (DefaultResolveQueue when empty) and answers
// ResolveRequests using the provided resolve function. Masters wire resolve
// to IndexResolveFunc over their facts.Index.
//
// ps must implement bus.RequestPubSub (NATSPubSub and bustest.FakePubSub do);
// otherwise an error is returned. Call the returned cancel function to stop
// serving.
func StartResolveService(ctx context.Context, ps bus.PubSub, queue string, resolve ResolveFunc, logger *slog.Logger) (context.CancelFunc, error) {
	if resolve == nil {
		return nil, fmt.Errorf("target: resolve service: resolve func is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if queue == "" {
		queue = DefaultResolveQueue
	}
	rps, ok := ps.(bus.RequestPubSub)
	if !ok {
		return nil, fmt.Errorf("target: resolve service: pubsub implementation %T does not support queue subscriptions", ps)
	}

	svcCtx, cancel := context.WithCancel(ctx)

	sub, err := rps.QueueSubscribe(bus.SubjectTargetResolve, queue, func(msg *bus.Msg) {
		resp := handleResolve(svcCtx, resolve, msg.Data, logger)
		data, err := bus.Encode(resp)
		if err != nil {
			logger.Warn("target: resolve service: encode response", "error", err)
			return
		}
		if err := msg.Respond(data); err != nil {
			logger.Warn("target: resolve service: respond", "error", err)
		}
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("target: resolve service: subscribe: %w", err)
	}

	go func() {
		<-svcCtx.Done()
		_ = sub.Unsubscribe()
	}()

	return cancel, nil
}

func handleResolve(ctx context.Context, resolve ResolveFunc, data []byte, logger *slog.Logger) *ResolveResponse {
	var req ResolveRequest
	if err := bus.Decode(data, &req); err != nil {
		return &ResolveResponse{Err: fmt.Sprintf("decode request: %v", err)}
	}

	rctx, cancel := context.WithTimeout(ctx, defaultResolveTimeout)
	defer cancel()

	peels, factsByPeel, err := resolve(rctx, req.Expr, req.Type)
	if err != nil {
		logger.Debug("target: resolve service: resolve failed",
			"expr", req.Expr, "type", req.Type, "error", err)
		return &ResolveResponse{Err: err.Error()}
	}

	resp := &ResolveResponse{Peels: peels}
	if req.WantFacts {
		resp.Facts = factsByPeel
	}
	return resp
}

// --- IndexLister: PeelLister backed by an in-memory facts.Index ---

// IndexLister adapts a *facts.Index to the PeelLister and BulkPeelLister
// interfaces, so the existing Resolve pipeline (glob/PCRE/fact/compound/list
// matching) runs against the in-memory index instead of draining the facts
// KV bucket. This is the master-side backend for the resolve service.
type IndexLister struct {
	idx *facts.Index
}

// NewIndexLister creates an IndexLister over the given index.
func NewIndexLister(idx *facts.Index) *IndexLister {
	return &IndexLister{idx: idx}
}

// ListPeels returns all indexed peel IDs.
func (l *IndexLister) ListPeels(_ context.Context) ([]string, error) {
	return l.idx.PeelIDs(), nil
}

// GetFacts returns the nested facts snapshot for a peel. The returned map is
// shared with the index and must be treated as read-only.
func (l *IndexLister) GetFacts(_ context.Context, peelID string) (map[string]any, error) {
	f := l.idx.RawFacts(peelID)
	if f == nil {
		return nil, fmt.Errorf("target: index lister: no facts for peel %q", peelID)
	}
	return f, nil
}

// ListPeelsWithFacts returns all indexed peels with their nested facts.
// Inner maps are shared with the index and must be treated as read-only.
func (l *IndexLister) ListPeelsWithFacts(_ context.Context) (map[string]map[string]any, error) {
	return l.idx.AllRawFacts(), nil
}

// IndexResolveFunc builds a ResolveFunc backed by a facts.Index: expressions
// are resolved with the standard target matchers over an IndexLister, and the
// matched peels' facts come straight from the index. Intended as the resolve
// argument to StartResolveService on the master.
func IndexResolveFunc(idx *facts.Index) ResolveFunc {
	lister := NewIndexLister(idx)
	return func(ctx context.Context, expr, targetType string) ([]string, map[string]map[string]any, error) {
		var tt TargetType
		if targetType == "" {
			tt = DetectType(expr)
		} else {
			var err error
			tt, err = ParseType(targetType)
			if err != nil {
				return nil, nil, err
			}
		}

		peels, err := Resolve(ctx, expr, tt, lister)
		if err != nil {
			return nil, nil, err
		}

		factsByPeel := make(map[string]map[string]any, len(peels))
		for _, id := range peels {
			if f := idx.RawFacts(id); f != nil {
				factsByPeel[id] = f
			}
		}
		return peels, factsByPeel, nil
	}
}

// --- ServiceLister: client-side lister that delegates to the service ---

// ServiceLister resolves target expressions via the master-side resolve
// service (request/reply on bus.SubjectTargetResolve). On any failure —
// timeout, no responders (no master serving the subject, e.g. in
// mixed-version fleets), transport or remote resolution errors — it logs a
// warning and falls back to the configured fallback lister (typically
// KVPeelLister), so callers keep working exactly as before the service
// existed. With a nil fallback the error is returned instead.
//
// ServiceLister implements ExprResolver, so Resolve() delegates whole
// expressions to the service — the master performs the matching and only the
// matched peel IDs cross the wire. Do not pass another ServiceLister as the
// fallback.
type ServiceLister struct {
	ps       bus.PubSub
	timeout  time.Duration
	fallback PeelLister
	logger   *slog.Logger
}

// NewServiceLister creates a ServiceLister. timeout <= 0 defaults to 5s;
// a nil logger defaults to slog.Default().
func NewServiceLister(ps bus.PubSub, timeout time.Duration, fallback PeelLister, logger *slog.Logger) *ServiceLister {
	if timeout <= 0 {
		timeout = defaultResolveTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ServiceLister{ps: ps, timeout: timeout, fallback: fallback, logger: logger}
}

var (
	_ PeelLister     = (*ServiceLister)(nil)
	_ BulkPeelLister = (*ServiceLister)(nil)
	_ ExprResolver   = (*ServiceLister)(nil)
)

// request performs one request/reply round trip against the resolve service.
func (s *ServiceLister) request(ctx context.Context, req ResolveRequest) (*ResolveResponse, error) {
	rps, ok := s.ps.(bus.RequestPubSub)
	if !ok {
		return nil, fmt.Errorf("target: service lister: pubsub implementation %T does not support request/reply", s.ps)
	}

	data, err := bus.Encode(req)
	if err != nil {
		return nil, fmt.Errorf("target: service lister: encode request: %w", err)
	}

	rctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	msg, err := rps.Request(rctx, bus.SubjectTargetResolve, data)
	if err != nil {
		return nil, fmt.Errorf("target: service lister: request: %w", err)
	}

	var resp ResolveResponse
	if err := bus.Decode(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("target: service lister: decode response: %w", err)
	}
	if resp.Err != "" {
		return nil, fmt.Errorf("target: service lister: remote resolve: %s", resp.Err)
	}
	return &resp, nil
}

// ResolveExpr resolves a full target expression via the service; Resolve()
// calls this automatically when given a ServiceLister.
func (s *ServiceLister) ResolveExpr(ctx context.Context, expr string, tt TargetType) ([]string, error) {
	resp, err := s.request(ctx, ResolveRequest{Expr: expr, Type: tt.String()})
	if err == nil {
		return resp.Peels, nil
	}
	if s.fallback == nil {
		return nil, err
	}
	s.logger.Warn("target: resolve service unavailable, falling back to facts KV scan",
		"expr", expr, "error", err)
	return Resolve(ctx, expr, tt, s.fallback)
}

// ListPeels returns all known peel IDs via the service ("*" glob), falling
// back to the fallback lister on failure.
func (s *ServiceLister) ListPeels(ctx context.Context) ([]string, error) {
	resp, err := s.request(ctx, ResolveRequest{Expr: "*", Type: Glob.String()})
	if err == nil {
		return resp.Peels, nil
	}
	if s.fallback == nil {
		return nil, err
	}
	s.logger.Warn("target: resolve service unavailable, falling back to facts KV scan", "error", err)
	return s.fallback.ListPeels(ctx)
}

// GetFacts returns the facts for one peel via the service, falling back to
// the fallback lister on failure.
func (s *ServiceLister) GetFacts(ctx context.Context, peelID string) (map[string]any, error) {
	resp, err := s.request(ctx, ResolveRequest{Expr: peelID, Type: List.String(), WantFacts: true})
	if err == nil {
		if f, ok := resp.Facts[peelID]; ok {
			return f, nil
		}
		return nil, fmt.Errorf("target: service lister: no facts for peel %q", peelID)
	}
	if s.fallback == nil {
		return nil, err
	}
	s.logger.Warn("target: resolve service unavailable, falling back to facts KV scan",
		"peel_id", peelID, "error", err)
	return s.fallback.GetFacts(ctx, peelID)
}

// ListPeelsWithFacts returns all peels and their facts via the service,
// falling back to the fallback lister on failure. Note: prefer ResolveExpr
// (via Resolve) where possible — this call transfers every peel's facts.
func (s *ServiceLister) ListPeelsWithFacts(ctx context.Context) (map[string]map[string]any, error) {
	resp, err := s.request(ctx, ResolveRequest{Expr: "*", Type: Glob.String(), WantFacts: true})
	if err == nil {
		return resp.Facts, nil
	}
	if s.fallback == nil {
		return nil, err
	}
	s.logger.Warn("target: resolve service unavailable, falling back to facts KV scan", "error", err)

	if bulk, ok := s.fallback.(BulkPeelLister); ok {
		return bulk.ListPeelsWithFacts(ctx)
	}
	ids, err := s.fallback.ListPeels(ctx)
	if err != nil {
		return nil, fmt.Errorf("target: service lister: fallback list peels: %w", err)
	}
	all := make(map[string]map[string]any, len(ids))
	for _, id := range ids {
		f, err := s.fallback.GetFacts(ctx, id)
		if err != nil {
			continue // skip peels with unavailable facts, mirroring Resolve
		}
		all[id] = f
	}
	return all, nil
}

// ServiceBasketQuery resolves a basket target expression via the resolve
// service and returns the matched peels with their facts. The target type is
// auto-detected from the expression (basket targets are usually compound
// once the basket_scope is ANDed in). Every matched peel is guaranteed a key
// in the returned map, even if the server did not supply facts for it, so
// callers that only need the matched IDs can range over the keys.
//
// There is no built-in fallback: callers (the peel's makeBasketFunc) should
// fall back to their existing KV-scan path when this returns an error —
// including nats.ErrNoResponders when no master serves the subject.
func ServiceBasketQuery(ctx context.Context, ps bus.PubSub, expr string, timeout time.Duration) (map[string]map[string]any, error) {
	sl := NewServiceLister(ps, timeout, nil, nil)
	tt := DetectType(expr)

	resp, err := sl.request(ctx, ResolveRequest{Expr: expr, Type: tt.String(), WantFacts: true})
	if err != nil {
		return nil, err
	}

	out := resp.Facts
	if out == nil {
		out = make(map[string]map[string]any, len(resp.Peels))
	}
	for _, id := range resp.Peels {
		if _, ok := out[id]; !ok {
			out[id] = nil
		}
	}
	return out, nil
}
