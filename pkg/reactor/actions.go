package reactor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/proto"
)

// Default job timeouts for reaction dispatches, mirroring the CLI defaults.
const (
	defaultDispatchTimeout = 60 * time.Second
	stateApplyTimeout      = 5 * time.Minute
	stateHighstateTimeout  = 10 * time.Minute
	fnStateApply           = "state.apply"
	fnStateHighstate       = "state.highstate"
	reactionOriginPrefix   = "reaction:"
	reactionUserPrefix     = "reactor:"
	reactionSubjectPrefix  = "reaction"
	metadataSourceReactor  = "reactor"
	metadataKeySource      = "source"
	metadataKeyRule        = "rule"
	metadataKeyEventID     = "event_id"
	metadataKeyEventTag    = "event_tag"
)

// Reaction result labels (zester_reactor_reactions_total{rule,result}).
const (
	ResultDispatched    = "dispatched"
	ResultDuplicate     = "duplicate"
	ResultThrottled     = "throttled"
	ResultBreakerOpen   = "breaker_open"
	ResultRenderError   = "render_error"
	ResultValidateError = "validate_error"
	ResultAborted       = "aborted"
	ResultRefused       = "refused"
	ResultNoTargets     = "no_targets"
	ResultError         = "error"
)

// Sentinel errors the masterd EnrollFn implementation wraps so the executor
// can classify enrollment outcomes without string matching.
var (
	// ErrEnrollRefused marks a PERMANENT enrollment refusal: the
	// require_peel glob did not match the enrollment record's peel ID, the
	// record does not exist, or the transition is not allowed for a reason
	// that will not heal on redelivery. Classified as result "refused"
	// (Error log, Ack).
	ErrEnrollRefused = errors.New("reactor: enroll action refused")

	// ErrEnrollDuplicate marks an idempotent replay: the record already
	// underwent this transition (e.g. approving an already-approved
	// enrollment). Classified as duplicate-success (Debug log, Ack).
	ErrEnrollDuplicate = errors.New("reactor: enroll action already applied")
)

// DispatchFn dispatches a reaction job (masterd wires jobMgr.Dispatch bound
// to the daemon's run context so watchers survive the per-message context).
// Must wrap JID conflicts with job.ErrJIDConflict.
type DispatchFn func(ctx context.Context, j *job.Job) error

// ResolveFn resolves a canonical target expression (type auto-detected) to
// peel IDs (masterd wires target.Resolve over the retained facts.Index).
type ResolveFn func(ctx context.Context, expr string) ([]string, error)

// EnrollFn performs the enrollment lookup + require_peel gate + state
// transition master-locally. Implementations MUST enforce that the
// enrollment record's peel ID matches requirePeelGlob (fnmatch semantics —
// use MatchRequirePeel) BEFORE transitioning, returning an error wrapping
// ErrEnrollRefused on mismatch and ErrEnrollDuplicate for already-applied
// transitions. op is "approve", "reject", or "revoke". The executor
// additionally enforces the _master-origin gate before ever calling this.
type EnrollFn func(ctx context.Context, op, enrollmentID, requirePeelGlob, reason string) error

// EmitFn publishes a derived event (masterd wires a JetStream publish with
// jetstream.WithMsgID(msgID) so redelivered emissions collapse in-stream
// within the duplicate window).
type EmitFn func(ctx context.Context, subject string, ev event.Event, msgID string) error

// MatchRequirePeel reports whether an enrollment's peel ID satisfies a
// require_peel glob (fnmatch semantics, same compiler as rule globs).
// Invalid globs and empty inputs never match — the gate fails closed.
//
// A glob written with the original hostname dots (require_peel: 'web*.pl')
// also matches the sanitized peel ID (web01_pl) via the same '.' -> '_'
// normalization the CLI's glob targeting applies. Peel IDs can never contain
// '.', so the normalized fallback only ever ADDS the matches the rule author
// wrote the dotted form for. (Rule match-key globs normalize only their
// ORIGIN segment — see NormalizeMatchKeyOrigin; require_peel globs are pure
// peel-ID patterns with no '/', so the whole pattern normalizes here.)
func MatchRequirePeel(glob, peelID string) bool {
	if glob == "" || peelID == "" {
		return false
	}
	if MatchGlob(glob, peelID) {
		return true
	}
	if norm := enroll.SanitizeGlobDots(glob); norm != glob {
		return MatchGlob(norm, peelID)
	}
	return false
}

// DeriveJID returns the deterministic, content-addressed JID for a reaction
// dispatch: "rxn-" + hex(sha256(origin||0x00||eventID||0x00||ruleRef||0x00||blockID))[:32].
// Including the origin means an attacker replaying its own event IDs can
// only suppress reactions to its OWN events; the hex digest guarantees the
// JID charset invariant (never contains '.', ' ', '*', or '>').
func DeriveJID(origin, eventID, ruleRef, blockID string) string {
	return "rxn-" + deriveHex(origin, eventID, ruleRef, blockID)[:32]
}

// DeriveChainID returns the deterministic ID for a derived (event.send)
// event: hex(sha256(parentID||0x00||ruleRef||0x00||blockID)). Redelivered
// emissions produce the same ID, so they collapse via the stream duplicate
// window and dedup downstream at dispatch.
func DeriveChainID(parentID, ruleRef, blockID string) string {
	return deriveHex(parentID, ruleRef, blockID)
}

func deriveHex(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Executor runs normalized actions against injected seams. All side effects
// are idempotent per (event, rule, block): job dispatch dedups on the
// deterministic JID, enrollment transitions dedup in the state machine, and
// derived events dedup on the deterministic chain ID.
type Executor struct {
	Dispatch DispatchFn
	Resolve  ResolveFn
	Enroll   EnrollFn
	Emit     EmitFn
	Logger   *slog.Logger

	// MaxChainDepth caps reaction chains: event.send refuses to emit when
	// the derived depth would reach the cap.
	MaxChainDepth int

	// Now is the injectable clock (nil = time.Now).
	Now func() time.Time
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Executor) logger() *slog.Logger {
	if x.Logger != nil {
		return x.Logger
	}
	return slog.Default()
}

// Execute runs one action for one (event, rule) pair. The returned result is
// the metric label; a non-nil error marks a TRANSIENT failure (connectivity)
// that the engine answers with NakWithDelay — permanent outcomes (refused,
// aborted, duplicate, no_targets) return a nil error so the message acks.
func (x *Executor) Execute(ctx context.Context, ev event.Event, origin, ruleRef string, act Action) (string, error) {
	switch act.Type {
	case ActionDispatch:
		return x.executeDispatch(ctx, ev, origin, ruleRef, act)
	case ActionEnroll:
		return x.executeEnroll(ctx, ev, origin, ruleRef, act)
	case ActionEventSend:
		return x.executeEventSend(ctx, ev, ruleRef, act)
	case ActionLog:
		x.logger().Info(act.Message, "rule", ruleRef, "event_id", ev.ID, "event_tag", ev.Tag, "origin", origin)
		return ResultDispatched, nil
	default:
		x.logger().Error("reactor: unknown action type", "rule", ruleRef, "type", string(act.Type))
		return ResultValidateError, nil
	}
}

func (x *Executor) executeDispatch(ctx context.Context, ev event.Event, origin, ruleRef string, act Action) (string, error) {
	if x.Dispatch == nil || x.Resolve == nil {
		x.logger().Error("reactor: dispatch action unavailable (no dispatch/resolve seam configured)", "rule", ruleRef)
		return ResultRefused, nil
	}

	targets, err := x.Resolve(ctx, act.Target)
	if err != nil {
		return ResultError, fmt.Errorf("reactor: resolve targets %q: %w", act.Target, err)
	}
	if len(targets) == 0 {
		x.logger().Info("reactor: reaction matched no targets", "rule", ruleRef, "target", act.Target, "event_id", ev.ID)
		return ResultNoTargets, nil
	}
	if act.MaxTargets > 0 && len(targets) > act.MaxTargets {
		x.logger().Error("reactor: reaction aborted: resolved targets exceed max_targets",
			"rule", ruleRef, "target", act.Target, "resolved", len(targets), "max_targets", act.MaxTargets, "event_id", ev.ID)
		return ResultAborted, nil
	}

	timeout := act.Timeout
	if timeout == 0 {
		switch act.Function {
		case fnStateApply:
			timeout = stateApplyTimeout
		case fnStateHighstate:
			timeout = stateHighstateTimeout
		default:
			timeout = defaultDispatchTimeout
		}
	}

	j := job.NewJob(act.Function, act.Args, targets, timeout)
	j.JID = DeriveJID(origin, ev.ID, ruleRef, act.BlockID)
	j.User = reactionUserPrefix + ruleRef
	j.TargetExpr = act.Target
	j.StateID = act.StateID
	j.Metadata = map[string]string{
		metadataKeySource:        metadataSourceReactor,
		metadataKeyRule:          ruleRef,
		metadataKeyEventID:       ev.ID,
		metadataKeyEventTag:      ev.Tag,
		job.MetadataReactorDepth: strconv.Itoa(ev.Depth + 1),
	}

	if err := x.Dispatch(ctx, j); err != nil {
		if errors.Is(err, job.ErrJIDConflict) {
			// The rxn- keyspace is reactor-exclusive and the JID is
			// content-addressed, so an existing job with this JID IS this
			// reaction (facts drift can make resolved targets differ across
			// redeliveries; the conflict is still the same reaction).
			x.logger().Debug("reactor: reaction already dispatched (duplicate suppressed)",
				"jid", j.JID, "rule", ruleRef, "event_id", ev.ID)
			return ResultDuplicate, nil
		}
		// job.ErrJobClaimPending (another master's claimed-never-published
		// record) deliberately falls through as TRANSIENT: the reaction is
		// not dispatched yet, so the event must redeliver until the claim is
		// resumed or reclaimed.
		return ResultError, fmt.Errorf("reactor: dispatch reaction job %s: %w", j.JID, err)
	}

	x.logger().Info("reactor: reaction job dispatched",
		"jid", j.JID, "rule", ruleRef, "user", j.User, "function", act.Function,
		"targets", len(targets), "event_id", ev.ID, "event_tag", ev.Tag, "depth", ev.Depth+1)
	return ResultDispatched, nil
}

func (x *Executor) executeEnroll(ctx context.Context, ev event.Event, origin, ruleRef string, act Action) (string, error) {
	// Trusted-origin gate: enroll.* actions fire ONLY from _master-origin
	// events, enforced here in the executor — rule authoring mistakes (or a
	// peel-matched glob) can never turn peel events into enrollment
	// transitions.
	if origin != bus.OriginMaster {
		x.logger().Error("reactor: enroll action refused: event origin is not _master",
			"rule", ruleRef, "origin", origin, "op", act.EnrollOp, "enrollment_id", act.EnrollID, "event_id", ev.ID)
		return ResultRefused, nil
	}
	if act.RequirePeel == "" {
		// Normalization guarantees this; fail closed regardless.
		x.logger().Error("reactor: enroll action refused: missing require_peel", "rule", ruleRef, "op", act.EnrollOp)
		return ResultRefused, nil
	}
	if x.Enroll == nil {
		x.logger().Error("reactor: enroll action unavailable (no enroll seam configured)", "rule", ruleRef, "op", act.EnrollOp)
		return ResultRefused, nil
	}

	if err := x.Enroll(ctx, act.EnrollOp, act.EnrollID, act.RequirePeel, act.Reason); err != nil {
		switch {
		case errors.Is(err, ErrEnrollDuplicate):
			x.logger().Debug("reactor: enroll action already applied (duplicate suppressed)",
				"rule", ruleRef, "op", act.EnrollOp, "enrollment_id", act.EnrollID)
			return ResultDuplicate, nil
		case errors.Is(err, ErrEnrollRefused):
			x.logger().Error("reactor: enroll action refused",
				"rule", ruleRef, "op", act.EnrollOp, "enrollment_id", act.EnrollID, "require_peel", act.RequirePeel, "error", err)
			return ResultRefused, nil
		default:
			return ResultError, fmt.Errorf("reactor: enroll %s %s: %w", act.EnrollOp, act.EnrollID, err)
		}
	}

	x.logger().Info("reactor: enrollment transition applied",
		"rule", ruleRef, "op", act.EnrollOp, "enrollment_id", act.EnrollID,
		"require_peel", act.RequirePeel, "operator", reactionUserPrefix+ruleRef, "event_id", ev.ID)
	return ResultDispatched, nil
}

func (x *Executor) executeEventSend(ctx context.Context, ev event.Event, ruleRef string, act Action) (string, error) {
	if x.Emit == nil {
		x.logger().Error("reactor: event.send unavailable (no emit seam configured)", "rule", ruleRef)
		return ResultRefused, nil
	}

	depth := ev.Depth + 1
	if x.MaxChainDepth > 0 && depth >= x.MaxChainDepth {
		// Emitting at the cap is pointless (the consume gate drops
		// depth >= cap) and hides the loop; refuse loudly instead.
		x.logger().Warn("reactor: event.send refused at chain depth cap",
			"rule", ruleRef, "tag", act.Tag, "depth", depth, "max_chain_depth", x.MaxChainDepth, "parent_event_id", ev.ID)
		return ResultRefused, nil
	}

	derivedID := DeriveChainID(ev.ID, ruleRef, act.BlockID)
	// Derived events publish under the "reaction" sub-token of the _master
	// origin, so the subject-derived tag is "reaction/<tag>". The wire Tag
	// MUST carry the same value: the consuming engine's anti-spoof gate
	// drops any event whose payload tag disagrees with its subject tag.
	derived := event.Event{
		ID:     derivedID,
		Tag:    reactionSubjectPrefix + "/" + act.Tag,
		Data:   act.Data,
		TS:     x.now().UTC(),
		V:      proto.ProtocolVersion,
		Origin: reactionOriginPrefix + ruleRef,
		Depth:  depth,
	}
	subject := bus.MasterEventSubject(reactionSubjectPrefix + "." + event.DottedTag(act.Tag))

	if err := x.Emit(ctx, subject, derived, derivedID); err != nil {
		return ResultError, fmt.Errorf("reactor: emit derived event %s: %w", act.Tag, err)
	}

	x.logger().Info("reactor: derived event emitted",
		"rule", ruleRef, "tag", act.Tag, "subject", subject, "event_id", derivedID,
		"parent_event_id", ev.ID, "depth", depth)
	return ResultDispatched, nil
}
