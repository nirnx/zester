package reactor

import (
	"fmt"
	"strings"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/event"
)

// TestServiceQueue is the queue group every reactor-enabled master joins to
// answer `zester reactor test` requests (one master answers each request).
const TestServiceQueue = "zester-reactor-testers"

// TestRequest is the wire request for the reactor test service: given a
// match key ("<origin>/<slashTag>") and optional event data, report which
// rules match and what their rendered, validated actions would be — WITHOUT
// executing anything. Msgpack additive.
type TestRequest struct {
	// Key is the match key, e.g. "_master/enroll/pending/enr-1" or
	// "web-01/myco/deploy/finished".
	Key string `msgpack:"key"`

	// Data is the synthetic event payload made available to the reaction
	// templates.
	Data map[string]any `msgpack:"data,omitempty"`
}

// MatchedRule describes one matched rule in a test response.
type MatchedRule struct {
	// Rule is the dotted reaction ref.
	Rule string `msgpack:"rule"`

	// Actions are human-readable summaries of the normalized actions.
	Actions []string `msgpack:"actions,omitempty"`

	// Errors are render/normalize/validation errors for this rule.
	Errors []string `msgpack:"errors,omitempty"`
}

// TestResponse is the wire response for the reactor test service. Msgpack
// additive.
type TestResponse struct {
	// Matched lists every matching rule in file order.
	Matched []MatchedRule `msgpack:"matched,omitempty"`

	// Err is a non-empty request-level error message.
	Err string `msgpack:"err,omitempty"`
}

// StartTestService subscribes the engine to bus.SubjectReactorTest in the
// TestServiceQueue queue group, answering dry-run requests by matching +
// rendering + validating against the live rule snapshot without executing.
// ps must provide request/reply capabilities (bus.RequestPubSub); returns a
// stop function.
func (e *Engine) StartTestService(ps bus.PubSub) (func(), error) {
	rps, ok := ps.(bus.RequestPubSub)
	if !ok {
		return nil, fmt.Errorf("reactor: test service requires a request/reply-capable PubSub (bus.RequestPubSub)")
	}

	sub, err := rps.QueueSubscribe(bus.SubjectReactorTest, TestServiceQueue, func(msg *bus.Msg) {
		var req TestRequest
		resp := TestResponse{}
		if err := bus.Decode(msg.Data, &req); err != nil {
			resp.Err = fmt.Sprintf("decode request: %v", err)
		} else {
			resp = e.testMatch(req)
		}
		data, err := bus.Encode(resp)
		if err != nil {
			e.logger.Warn("reactor: test service: encode response failed", "error", err)
			return
		}
		if err := msg.Respond(data); err != nil {
			e.logger.Warn("reactor: test service: respond failed", "error", err)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("reactor: subscribe %s: %w", bus.SubjectReactorTest, err)
	}

	e.logger.Info("reactor: test service started", "subject", bus.SubjectReactorTest, "queue", TestServiceQueue)
	return func() { _ = sub.Unsubscribe() }, nil
}

// testMatch performs the dry run: match the key, then render + normalize +
// validate each matched rule with a synthetic event. Nothing executes.
func (e *Engine) testMatch(req TestRequest) TestResponse {
	origin, tag, err := splitMatchKey(req.Key)
	if err != nil {
		return TestResponse{Err: err.Error()}
	}

	rs := e.ruleSet()
	matched := rs.Match(req.Key)
	if len(matched) == 0 {
		return TestResponse{}
	}

	// Synthetic event: fresh ID/TS, origin-appropriate depth 0. The origin
	// token drives event.peel/origin_facts exactly like a live event.
	ev := event.NewEvent(tag, req.Data)

	out := make([]MatchedRule, 0, len(matched))
	for _, rule := range matched {
		mr := MatchedRule{Rule: rule.Ref}
		src, ok := rs.File(rule.Ref)
		if !ok {
			mr.Errors = []string{fmt.Sprintf("reaction file %s missing from snapshot", RefPath(rule.Ref))}
			out = append(out, mr)
			continue
		}
		rr, err := e.renderer.RenderRule(rule.Ref, src, ev, origin, NormalizeOptions{EnableChaining: e.chaining})
		if err != nil {
			mr.Errors = []string{err.Error()}
			out = append(out, mr)
			continue
		}
		mr.Errors = rr.Errors
		for _, act := range rr.Actions {
			mr.Actions = append(mr.Actions, act.Summary())
		}
		out = append(out, mr)
	}
	return TestResponse{Matched: out}
}

// splitMatchKey splits "<origin>/<slashTag>" into its parts.
func splitMatchKey(key string) (origin, tag string, err error) {
	idx := strings.Index(key, "/")
	if idx <= 0 || idx == len(key)-1 {
		return "", "", fmt.Errorf("reactor: match key %q must be \"<origin>/<tag>\" (e.g. \"web-01/myco/deploy/finished\")", key)
	}
	return key[:idx], key[idx+1:], nil
}
