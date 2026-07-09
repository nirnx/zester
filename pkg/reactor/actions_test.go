package reactor

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/proto"
)

func TestDeriveJIDDeterministicAndSensitive(t *testing.T) {
	jid := DeriveJID("web-01", "ev-1", "reactor.deploy", "restart-nginx")
	if jid != DeriveJID("web-01", "ev-1", "reactor.deploy", "restart-nginx") {
		t.Fatal("DeriveJID must be deterministic")
	}
	if !strings.HasPrefix(jid, "rxn-") {
		t.Errorf("prefix: %q", jid)
	}
	if len(jid) != len("rxn-")+32 {
		t.Errorf("length: %d (%q)", len(jid), jid)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(jid, "rxn-")); err != nil {
		t.Errorf("digest must be hex: %v", err)
	}
	// JID charset invariant: usable as a KV key and subject token.
	if strings.ContainsAny(jid, ". *>") {
		t.Errorf("JID %q contains forbidden characters", jid)
	}

	// Every input perturbs the digest — including the ORIGIN, so a peel
	// replaying another origin's event IDs cannot suppress its reactions.
	variants := []string{
		DeriveJID("web-02", "ev-1", "reactor.deploy", "restart-nginx"),
		DeriveJID("web-01", "ev-2", "reactor.deploy", "restart-nginx"),
		DeriveJID("web-01", "ev-1", "reactor.other", "restart-nginx"),
		DeriveJID("web-01", "ev-1", "reactor.deploy", "other-block"),
	}
	seen := map[string]bool{jid: true}
	for _, v := range variants {
		if seen[v] {
			t.Errorf("JID collision: %q", v)
		}
		seen[v] = true
	}

	// The 0x00 separator prevents boundary-shift collisions.
	if DeriveJID("a", "bc", "d", "e") == DeriveJID("ab", "c", "d", "e") {
		t.Error("separator must prevent boundary-shift collisions")
	}
}

func TestDeriveChainID(t *testing.T) {
	id := DeriveChainID("parent-1", "reactor.chain", "hop")
	if id != DeriveChainID("parent-1", "reactor.chain", "hop") {
		t.Fatal("DeriveChainID must be deterministic")
	}
	if len(id) != 64 {
		t.Errorf("length: %d", len(id))
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Errorf("must be hex: %v", err)
	}
	if id == DeriveChainID("parent-2", "reactor.chain", "hop") ||
		id == DeriveChainID("parent-1", "reactor.other", "hop") ||
		id == DeriveChainID("parent-1", "reactor.chain", "hop2") {
		t.Error("inputs must perturb the ID")
	}
}

func TestMatchRequirePeel(t *testing.T) {
	tests := []struct {
		glob, peel string
		want       bool
	}{
		{"web-*", "web-01", true},
		{"web-*", "db-01", false},
		{"web-0?", "web-01", true},
		{"*", "anything", true},
		{"", "web-01", false},   // fails closed
		{"web-*", "", false},    // fails closed
		{"[z-a]", "web", false}, // invalid glob fails closed
		// A dotted-hostname glob matches the sanitized peel id ('.' -> '_'),
		// consistent with CLI glob targeting.
		{"web*.pl", "web01_pl", true},
		{"devops-hetzner.oxm", "devops-hetzner_oxm", true},
		{"web01.pl", "web01-pl", false}, // never crosses to the hyphenated host
	}
	for _, tt := range tests {
		if got := MatchRequirePeel(tt.glob, tt.peel); got != tt.want {
			t.Errorf("MatchRequirePeel(%q, %q) = %v, want %v", tt.glob, tt.peel, got, tt.want)
		}
	}
}

// seamRecorder captures executor seam calls.
type seamRecorder struct {
	mu sync.Mutex

	jobs        []*job.Job
	dispatchErr error

	resolved   []string
	resolveErr error

	enrollCalls [][4]string // op, id, requirePeel, reason
	enrollErr   error

	emits   []emitRecord
	emitErr error
}

type emitRecord struct {
	subject string
	ev      event.Event
	msgID   string
}

func (s *seamRecorder) dispatch(_ context.Context, j *job.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispatchErr != nil {
		return s.dispatchErr
	}
	s.jobs = append(s.jobs, j)
	return nil
}

func (s *seamRecorder) resolve(_ context.Context, expr string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return s.resolved, nil
}

func (s *seamRecorder) enroll(_ context.Context, op, id, requirePeel, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enrollCalls = append(s.enrollCalls, [4]string{op, id, requirePeel, reason})
	return s.enrollErr
}

func (s *seamRecorder) emit(_ context.Context, subject string, ev event.Event, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emitErr != nil {
		return s.emitErr
	}
	s.emits = append(s.emits, emitRecord{subject: subject, ev: ev, msgID: msgID})
	return nil
}

func newExecutor(s *seamRecorder) *Executor {
	return &Executor{
		Dispatch:      s.dispatch,
		Resolve:       s.resolve,
		Enroll:        s.enroll,
		Emit:          s.emit,
		MaxChainDepth: 3,
	}
}

func TestExecuteDispatchBuildsProvenancedJob(t *testing.T) {
	seams := &seamRecorder{resolved: []string{"web-01", "web-02"}}
	x := newExecutor(seams)
	ev := testEvent("myco/deploy/finished", map[string]any{"service": "nginx"})

	act := Action{
		BlockID: "restart-nginx", Type: ActionDispatch,
		Function: "service.restart", Target: "web-*",
		Args: map[string]any{"name": "nginx"}, StateID: "nginx",
	}
	result, err := x.Execute(context.Background(), ev, "web-01", "reactor.deploy", act)
	if err != nil || result != ResultDispatched {
		t.Fatalf("Execute: %s, %v", result, err)
	}
	if len(seams.jobs) != 1 {
		t.Fatalf("jobs: %d", len(seams.jobs))
	}
	j := seams.jobs[0]

	if want := DeriveJID("web-01", ev.ID, "reactor.deploy", "restart-nginx"); j.JID != want {
		t.Errorf("JID: got %q, want %q", j.JID, want)
	}
	if j.User != "reactor:reactor.deploy" {
		t.Errorf("User: %q", j.User)
	}
	if j.TargetExpr != "web-*" {
		t.Errorf("TargetExpr: %q", j.TargetExpr)
	}
	if j.StateID != "nginx" {
		t.Errorf("StateID: %q", j.StateID)
	}
	if len(j.Targets) != 2 {
		t.Errorf("Targets: %v", j.Targets)
	}
	if j.Timeout != 60*time.Second {
		t.Errorf("default timeout: %s", j.Timeout)
	}
	wantMeta := map[string]string{
		"source":                 "reactor",
		"rule":                   "reactor.deploy",
		"event_id":               ev.ID,
		"event_tag":              "myco/deploy/finished",
		job.MetadataReactorDepth: "1",
	}
	for k, v := range wantMeta {
		if j.Metadata[k] != v {
			t.Errorf("Metadata[%s]: got %q, want %q", k, j.Metadata[k], v)
		}
	}
}

func TestExecuteDispatchTimeoutDefaults(t *testing.T) {
	tests := []struct {
		function string
		explicit time.Duration
		want     time.Duration
	}{
		{"cmd.run", 0, 60 * time.Second},
		{"state.apply", 0, 5 * time.Minute},
		{"state.highstate", 0, 10 * time.Minute},
		{"state.apply", 30 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		seams := &seamRecorder{resolved: []string{"web-01"}}
		x := newExecutor(seams)
		act := Action{BlockID: "b", Type: ActionDispatch, Function: tt.function, Target: "*", Timeout: tt.explicit}
		if _, err := x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act); err != nil {
			t.Fatalf("%s: %v", tt.function, err)
		}
		if got := seams.jobs[0].Timeout; got != tt.want {
			t.Errorf("%s timeout: got %s, want %s", tt.function, got, tt.want)
		}
	}
}

func TestExecuteDispatchDuplicateViaJIDConflict(t *testing.T) {
	seams := &seamRecorder{
		resolved:    []string{"web-01"},
		dispatchErr: fmt.Errorf("job: jid x conflict: existing job differs (targets): %w", job.ErrJIDConflict),
	}
	x := newExecutor(seams)
	act := Action{BlockID: "b", Type: ActionDispatch, Function: "test.ping", Target: "*"}

	result, err := x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act)
	if err != nil {
		t.Fatalf("conflict must be duplicate-success, got error: %v", err)
	}
	if result != ResultDuplicate {
		t.Errorf("result: %s", result)
	}
}

func TestExecuteDispatchTransientErrors(t *testing.T) {
	// Non-conflict dispatch errors are transient.
	seams := &seamRecorder{resolved: []string{"web-01"}, dispatchErr: errors.New("nats down")}
	x := newExecutor(seams)
	act := Action{BlockID: "b", Type: ActionDispatch, Function: "test.ping", Target: "*"}
	result, err := x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act)
	if err == nil || result != ResultError {
		t.Errorf("dispatch failure: %s, %v", result, err)
	}

	// A claim-pending collision (another master's claimed-never-published
	// record) is transient too — NOT duplicate-suppressed: the reaction has
	// not been dispatched yet, so the event must redeliver until the claim
	// is resumed or reclaimed.
	seams = &seamRecorder{
		resolved:    []string{"web-01"},
		dispatchErr: fmt.Errorf("job: jid x claimed by master-b but not yet published: %w", job.ErrJobClaimPending),
	}
	x = newExecutor(seams)
	result, err = x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act)
	if err == nil || result != ResultError {
		t.Errorf("claim pending: %s, %v (want transient error)", result, err)
	}
	if !errors.Is(err, job.ErrJobClaimPending) {
		t.Errorf("claim-pending sentinel lost: %v", err)
	}

	// Resolve errors are transient too.
	seams = &seamRecorder{resolveErr: errors.New("resolve service down")}
	x = newExecutor(seams)
	result, err = x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act)
	if err == nil || result != ResultError {
		t.Errorf("resolve failure: %s, %v", result, err)
	}
	if len(seams.jobs) != 0 {
		t.Error("no job may be dispatched when resolution fails")
	}
}

func TestExecuteDispatchNoTargetsAndMaxTargets(t *testing.T) {
	seams := &seamRecorder{resolved: nil}
	x := newExecutor(seams)
	act := Action{BlockID: "b", Type: ActionDispatch, Function: "test.ping", Target: "nope*"}
	result, err := x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act)
	if err != nil || result != ResultNoTargets {
		t.Errorf("no targets: %s, %v", result, err)
	}

	seams = &seamRecorder{resolved: []string{"a", "b", "c"}}
	x = newExecutor(seams)
	act.MaxTargets = 2
	result, err = x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.x", act)
	if err != nil || result != ResultAborted {
		t.Errorf("max_targets: %s, %v", result, err)
	}
	if len(seams.jobs) != 0 {
		t.Error("aborted action must not dispatch")
	}
}

func TestExecuteEnrollOriginGate(t *testing.T) {
	seams := &seamRecorder{}
	x := newExecutor(seams)
	act := Action{BlockID: "b", Type: ActionEnroll, EnrollOp: "approve", EnrollID: "enr-1", RequirePeel: "web-*"}

	// Non-master origins are refused at EXECUTION time, before the seam.
	for _, origin := range []string{"web-01", "evil", bus.OriginAdmin} {
		result, err := x.Execute(context.Background(), testEvent("enroll/pending/enr-1", nil), origin, "r.approve", act)
		if err != nil || result != ResultRefused {
			t.Errorf("origin %q: %s, %v", origin, result, err)
		}
	}
	if len(seams.enrollCalls) != 0 {
		t.Fatal("EnrollFn must never be called for non-master origins")
	}

	// _master origin passes the gate and forwards all fields.
	act.Reason = "auto"
	result, err := x.Execute(context.Background(), testEvent("enroll/pending/enr-1", nil), bus.OriginMaster, "r.approve", act)
	if err != nil || result != ResultDispatched {
		t.Fatalf("master origin: %s, %v", result, err)
	}
	if len(seams.enrollCalls) != 1 {
		t.Fatalf("enroll calls: %d", len(seams.enrollCalls))
	}
	if got := seams.enrollCalls[0]; got != [4]string{"approve", "enr-1", "web-*", "auto"} {
		t.Errorf("enroll args: %v", got)
	}
}

func TestExecuteEnrollErrorClassification(t *testing.T) {
	ev := testEvent("enroll/pending/enr-1", nil)
	act := Action{BlockID: "b", Type: ActionEnroll, EnrollOp: "approve", EnrollID: "enr-1", RequirePeel: "web-*"}

	// require_peel mismatch (wrapped ErrEnrollRefused) is a permanent refusal.
	seams := &seamRecorder{enrollErr: fmt.Errorf("peel id %q does not match require_peel: %w", "db-9", ErrEnrollRefused)}
	result, err := newExecutor(seams).Execute(context.Background(), ev, bus.OriginMaster, "r.a", act)
	if err != nil || result != ResultRefused {
		t.Errorf("refused: %s, %v", result, err)
	}

	// Already-applied transition (wrapped ErrEnrollDuplicate) is duplicate-success.
	seams = &seamRecorder{enrollErr: fmt.Errorf("already approved: %w", ErrEnrollDuplicate)}
	result, err = newExecutor(seams).Execute(context.Background(), ev, bus.OriginMaster, "r.a", act)
	if err != nil || result != ResultDuplicate {
		t.Errorf("duplicate: %s, %v", result, err)
	}

	// Anything else is transient.
	seams = &seamRecorder{enrollErr: errors.New("kv timeout")}
	result, err = newExecutor(seams).Execute(context.Background(), ev, bus.OriginMaster, "r.a", act)
	if err == nil || result != ResultError {
		t.Errorf("transient: %s, %v", result, err)
	}

	// Missing seam fails closed.
	x := newExecutor(&seamRecorder{})
	x.Enroll = nil
	result, err = x.Execute(context.Background(), ev, bus.OriginMaster, "r.a", act)
	if err != nil || result != ResultRefused {
		t.Errorf("nil seam: %s, %v", result, err)
	}
}

func TestExecuteEventSend(t *testing.T) {
	seams := &seamRecorder{}
	x := newExecutor(seams)
	parent := testEvent("chain/start", nil)
	parent.Depth = 1
	act := Action{BlockID: "hop", Type: ActionEventSend, Tag: "chain/next", Data: map[string]any{"n": 2}}

	result, err := x.Execute(context.Background(), parent, "web-01", "reactor.chain", act)
	if err != nil || result != ResultDispatched {
		t.Fatalf("Execute: %s, %v", result, err)
	}
	if len(seams.emits) != 1 {
		t.Fatalf("emits: %d", len(seams.emits))
	}
	em := seams.emits[0]

	wantID := DeriveChainID(parent.ID, "reactor.chain", "hop")
	if em.ev.ID != wantID {
		t.Errorf("derived ID: got %q, want %q", em.ev.ID, wantID)
	}
	if em.msgID != wantID {
		t.Errorf("msgID must equal the derived ID for JetStream dedup, got %q", em.msgID)
	}
	if em.subject != "zester.event._master.reaction.chain.next" {
		t.Errorf("subject: %q", em.subject)
	}
	if em.ev.Origin != "reaction:reactor.chain" {
		t.Errorf("Origin: %q", em.ev.Origin)
	}
	if em.ev.Depth != 2 {
		t.Errorf("Depth: %d", em.ev.Depth)
	}
	// The wire tag must equal the subject-derived tag ("reaction/<tag>") or
	// the consuming engine's anti-spoof gate drops the derived event.
	if em.ev.Tag != "reaction/chain/next" || em.ev.Data["n"] != 2 {
		t.Errorf("tag/data: %q %v", em.ev.Tag, em.ev.Data)
	}
	if em.ev.V != proto.ProtocolVersion {
		t.Errorf("V: %d", em.ev.V)
	}
}

func TestExecuteEventSendRefusedAtDepthCap(t *testing.T) {
	seams := &seamRecorder{}
	x := newExecutor(seams) // MaxChainDepth 3
	parent := testEvent("chain/start", nil)
	parent.Depth = 2 // derived depth 3 >= cap 3
	act := Action{BlockID: "hop", Type: ActionEventSend, Tag: "chain/next"}

	result, err := x.Execute(context.Background(), parent, "web-01", "reactor.chain", act)
	if err != nil || result != ResultRefused {
		t.Fatalf("depth cap: %s, %v", result, err)
	}
	if len(seams.emits) != 0 {
		t.Fatal("emission at the depth cap must be refused")
	}
}

func TestExecuteEventSendTransientEmitError(t *testing.T) {
	seams := &seamRecorder{emitErr: errors.New("stream unavailable")}
	x := newExecutor(seams)
	act := Action{BlockID: "hop", Type: ActionEventSend, Tag: "chain/next"}
	result, err := x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.c", act)
	if err == nil || result != ResultError {
		t.Errorf("emit failure: %s, %v", result, err)
	}
}

func TestExecuteLog(t *testing.T) {
	logger, buf := testLogger()
	x := newExecutor(&seamRecorder{})
	x.Logger = logger
	act := Action{BlockID: "note", Type: ActionLog, Message: "deploy finished on web-01"}
	result, err := x.Execute(context.Background(), testEvent("a/b", nil), "web-01", "r.log", act)
	if err != nil || result != ResultDispatched {
		t.Fatalf("log: %s, %v", result, err)
	}
	if !strings.Contains(buf.String(), "deploy finished on web-01") {
		t.Errorf("log output missing message:\n%s", buf.String())
	}
}
