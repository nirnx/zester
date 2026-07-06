package reactor

import (
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/event"
)

func testEvent(tag string, data map[string]any) event.Event {
	// NewEvent stamps TS=now, which keeps engine tests (real clock, default
	// 1h staleness gate) from classifying fixtures as stale.
	return event.NewEvent(tag, data)
}

func mustRenderer(t *testing.T, factsFn FactsFn) *Renderer {
	t.Helper()
	r, err := NewRenderer(factsFn)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return r
}

func TestRenderEventContext(t *testing.T) {
	factsFn := func(peelID string) map[string]any {
		if peelID == "web-01" {
			return map[string]any{"role": "web", "os": map[string]any{"family": "debian"}}
		}
		return nil
	}
	r := mustRenderer(t, factsFn)
	ev := testEvent("myco/deploy/finished", map[string]any{"service": "nginx"})

	src := []byte(`{{ event.id }}|{{ event.tag }}|{{ event.peel }}|{{ event.origin }}|{{ event.depth }}|{{ tag }}|{{ data.get('service', '') }}|{{ origin_facts.role }}|{{ data | to_json }}`)
	out, err := r.Render("reactor.test", src, ev, "web-01")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := ev.ID + `|myco/deploy/finished|web-01|web-01|0|myco/deploy/finished|nginx|web|{"service":"nginx"}`
	if out != want {
		t.Errorf("rendered:\n  got:  %s\n  want: %s", out, want)
	}
}

func TestRenderMasterOriginHasNoPeel(t *testing.T) {
	called := false
	r := mustRenderer(t, func(string) map[string]any { called = true; return map[string]any{"x": 1} })
	ev := testEvent("enroll/pending/enr-1", nil)

	out, err := r.Render("reactor.test", []byte(`[{{ event.peel }}]|{{ event.origin }}|{{ origin_facts | to_json }}`), ev, bus.OriginMaster)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "[]|_master|{}" {
		t.Errorf("rendered: %s", out)
	}
	if called {
		t.Error("FactsFn must not be consulted for trusted origins")
	}
}

func TestRenderNilFactsFnAndNilData(t *testing.T) {
	r := mustRenderer(t, nil)
	ev := testEvent("a/b", nil)
	out, err := r.Render("reactor.test", []byte(`{{ origin_facts | to_json }}|{{ data.get('missing', 'dflt') }}`), ev, "web-01")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "{}|dflt" {
		t.Errorf("rendered: %s", out)
	}
}

func TestRenderSaltAccessorIsAClearError(t *testing.T) {
	r := mustRenderer(t, nil)
	ev := testEvent("a/b", nil)
	_, err := r.Render("reactor.test", []byte(`{{ salt['pkg.version']('nginx') }}`), ev, "web-01")
	if err == nil {
		t.Fatal("salt[...] must fail with ModuleFn nil")
	}
}

func normalizeOne(t *testing.T, doc string) Action {
	t.Helper()
	actions, errs, err := NormalizeActions(doc, NormalizeOptions{EnableChaining: true})
	if err != nil {
		t.Fatalf("NormalizeActions: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("normalize errors: %v", errs)
	}
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	return actions[0]
}

func normalizeErr(t *testing.T, doc string, errPart string) {
	t.Helper()
	_, errs, err := NormalizeActions(doc, NormalizeOptions{EnableChaining: true})
	if err != nil {
		t.Fatalf("document-level error, want block error: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("want 1 block error containing %q, got %v", errPart, errs)
	}
	if !strings.Contains(errs[0], errPart) {
		t.Errorf("error %q does not contain %q", errs[0], errPart)
	}
}

func TestNormalizeDispatchModuleListForm(t *testing.T) {
	act := normalizeOne(t, `
restart-nginx:
  dispatch.module:
    - target: web-01
    - function: service.restart
    - args:
        name: nginx
    - timeout: 60
`)
	if act.Type != ActionDispatch || act.BlockID != "restart-nginx" {
		t.Fatalf("action: %+v", act)
	}
	if act.Function != "service.restart" || act.Target != "web-01" {
		t.Errorf("function/target: %q %q", act.Function, act.Target)
	}
	if act.Args["name"] != "nginx" {
		t.Errorf("args: %v", act.Args)
	}
	if act.Timeout != 60*time.Second {
		t.Errorf("timeout: %s", act.Timeout)
	}
}

func TestNormalizeDispatchModuleMapForm(t *testing.T) {
	act := normalizeOne(t, `
ping-all:
  dispatch.module:
    target: 'G@os:ubuntu'
    function: test.ping
    state_id: web
    timeout: 90s
    max_targets: 10
`)
	if act.Target != "G@os:ubuntu" || act.Function != "test.ping" {
		t.Errorf("target/function: %q %q", act.Target, act.Function)
	}
	if act.StateID != "web" || act.Timeout != 90*time.Second || act.MaxTargets != 10 {
		t.Errorf("state_id/timeout/max_targets: %q %s %d", act.StateID, act.Timeout, act.MaxTargets)
	}
}

func TestNormalizeDispatchState(t *testing.T) {
	act := normalizeOne(t, `
apply-web:
  dispatch.state:
    - target: web-*
    - sls: webserver
`)
	if act.Function != "state.apply" {
		t.Errorf("function: %q", act.Function)
	}
	if act.Args["mods"] != "webserver" {
		t.Errorf("args: %v", act.Args)
	}

	act = normalizeOne(t, `
highstate-all:
  dispatch.state:
    target: '*'
    highstate: true
`)
	if act.Function != "state.highstate" {
		t.Errorf("function: %q", act.Function)
	}

	normalizeErr(t, `
bad:
  dispatch.state:
    target: '*'
    sls: x
    highstate: true
`, "mutually exclusive")
	normalizeErr(t, `
bad:
  dispatch.state:
    target: '*'
`, "one of sls or highstate")
}

func TestNormalizeLocalSugar(t *testing.T) {
	act := normalizeOne(t, `
restart:
  local.service.restart:
    - tgt: 'web\d+'
    - tgt_type: pcre
    - arg:
        - nginx
    - kwarg:
        force: true
    - timeout: 2m
`)
	if act.Type != ActionDispatch || act.Function != "service.restart" {
		t.Fatalf("action: %+v", act)
	}
	if act.Target != `E@web\d+` {
		t.Errorf("tgt_type pcre must canonicalize to E@ prefix, got %q", act.Target)
	}
	if act.StateID != "nginx" {
		t.Errorf("arg[0] must map to StateID, got %q", act.StateID)
	}
	if act.Args["force"] != true {
		t.Errorf("kwarg: %v", act.Args)
	}
	if act.Timeout != 2*time.Minute {
		t.Errorf("timeout: %s", act.Timeout)
	}

	normalizeErr(t, `
bad:
  local.cmd.run:
    - tgt: '*'
    - arg: [one, two]
`, "only one positional argument")
}

func TestNormalizeEnroll(t *testing.T) {
	act := normalizeOne(t, `
approve:
  enroll.approve:
    - id: enr-123
    - require_peel: 'web-*'
    - reason: autoapproved
`)
	if act.Type != ActionEnroll || act.EnrollOp != "approve" {
		t.Fatalf("action: %+v", act)
	}
	if act.EnrollID != "enr-123" || act.RequirePeel != "web-*" || act.Reason != "autoapproved" {
		t.Errorf("fields: %+v", act)
	}

	// Missing require_peel is a COMPILE error (amendment 10), not a default.
	normalizeErr(t, `
approve:
  enroll.reject:
    - id: enr-123
`, "require_peel")
}

func TestNormalizeEventSend(t *testing.T) {
	act := normalizeOne(t, `
chain:
  event.send:
    tag: chain/next
    data:
      hop: one
`)
	if act.Type != ActionEventSend || act.Tag != "chain/next" || act.Data["hop"] != "one" {
		t.Fatalf("action: %+v", act)
	}

	// Refused at normalize time when chaining is disabled.
	_, errs, err := NormalizeActions(`
chain:
  event.send:
    tag: chain/next
`, NormalizeOptions{EnableChaining: false})
	if err != nil {
		t.Fatalf("NormalizeActions: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "chaining is disabled") {
		t.Fatalf("want chaining-disabled refusal, got %v", errs)
	}

	normalizeErr(t, `
chain:
  event.send:
    tag: 'bad.tag'
`, "invalid character")
}

func TestNormalizeLogForms(t *testing.T) {
	act := normalizeOne(t, "note:\n  log:\n    message: hello world\n")
	if act.Type != ActionLog || act.Message != "hello world" {
		t.Fatalf("action: %+v", act)
	}
	act = normalizeOne(t, "note:\n  log: bare scalar\n")
	if act.Message != "bare scalar" {
		t.Errorf("scalar log: %+v", act)
	}
	normalizeErr(t, "note:\n  log:\n    message: ''\n", "message is required")
}

func TestNormalizeValidationErrors(t *testing.T) {
	tests := []struct {
		name, doc, errPart string
	}{
		{"bad function", "b:\n  dispatch.module:\n    target: '*'\n    function: Not-Valid\n", "does not match"},
		{"missing function", "b:\n  dispatch.module:\n    target: '*'\n", "function is required"},
		{"missing target", "b:\n  dispatch.module:\n    function: test.ping\n", "target is required"},
		{"unparseable compound target", "b:\n  dispatch.module:\n    target: 'web* and ('\n    function: test.ping\n", "does not parse"},
		{"bad target type", "b:\n  dispatch.module:\n    target: x\n    target_type: bogus\n    function: test.ping\n", "unknown target type"},
		{"bad max_targets", "b:\n  dispatch.module:\n    target: '*'\n    function: test.ping\n    max_targets: 0\n", "max_targets"},
		{"unknown field", "b:\n  dispatch.module:\n    target: '*'\n    function: test.ping\n    bogus: 1\n", "unknown field"},
		{"unknown action", "b:\n  runner.orchestrate:\n    target: '*'\n", "unknown action"},
		{"two action keys", "b:\n  log: x\n  dispatch.module:\n    target: '*'\n    function: test.ping\n", "exactly one action key"},
		{"scalar block body", "b: just-a-string\n", "must be a mapping"},
		{"negative timeout", "b:\n  dispatch.module:\n    target: '*'\n    function: test.ping\n    timeout: -5s\n", "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizeErr(t, tt.doc, tt.errPart)
		})
	}
}

func TestNormalizeDocumentLevel(t *testing.T) {
	// Empty document: no actions, no errors.
	actions, errs, err := NormalizeActions("", NormalizeOptions{EnableChaining: true})
	if err != nil || len(actions) != 0 || len(errs) != 0 {
		t.Errorf("empty doc: %v %v %v", actions, errs, err)
	}
	// Comment-only document (a fully conditional template).
	actions, errs, err = NormalizeActions("# nothing\n", NormalizeOptions{EnableChaining: true})
	if err != nil || len(actions) != 0 || len(errs) != 0 {
		t.Errorf("comment doc: %v %v %v", actions, errs, err)
	}
	// Non-mapping document is a document-level error.
	if _, _, err := NormalizeActions("- a\n- b\n", NormalizeOptions{EnableChaining: true}); err == nil {
		t.Error("sequence document must be a document-level error")
	}
	// Broken YAML is a document-level error.
	if _, _, err := NormalizeActions("a: [\n", NormalizeOptions{EnableChaining: true}); err == nil {
		t.Error("broken YAML must be a document-level error")
	}
}

func TestNormalizeOrderAndPartialErrors(t *testing.T) {
	doc := `
first:
  log: one
broken:
  dispatch.module:
    target: '*'
    function: BAD
second:
  log: two
`
	actions, errs, err := NormalizeActions(doc, NormalizeOptions{EnableChaining: true})
	if err != nil {
		t.Fatalf("NormalizeActions: %v", err)
	}
	if len(actions) != 2 || actions[0].BlockID != "first" || actions[1].BlockID != "second" {
		t.Errorf("actions in document order: %+v", actions)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], `block "broken"`) {
		t.Errorf("errors: %v", errs)
	}
}

func TestRenderRuleEndToEnd(t *testing.T) {
	r := mustRenderer(t, nil)
	ev := testEvent("beacon/web-01/service/nginx", map[string]any{"service": "nginx"})

	src := []byte(`{% set svc = event.data.get('service', '') %}
restart-{{ svc }}:
  dispatch.module:
    - target: {{ event.peel }}
    - function: service.restart
    - args:
        name: {{ svc }}
    - timeout: 60
`)
	rr, err := r.RenderRule("reactor.restart_service", src, ev, "web-01", NormalizeOptions{EnableChaining: true})
	if err != nil {
		t.Fatalf("RenderRule: %v", err)
	}
	if len(rr.Errors) > 0 {
		t.Fatalf("errors: %v", rr.Errors)
	}
	if len(rr.Actions) != 1 {
		t.Fatalf("actions: %+v", rr.Actions)
	}
	act := rr.Actions[0]
	if act.BlockID != "restart-nginx" || act.Target != "web-01" || act.Args["name"] != "nginx" {
		t.Errorf("action: %+v", act)
	}
}

func TestCanonicalTarget(t *testing.T) {
	tests := []struct {
		expr, tt, want string
	}{
		{"web*", "", "web*"},
		{"web*", "glob", "web*"},
		{`web\d+`, "pcre", `E@web\d+`},
		{`E@web\d+`, "pcre", `E@web\d+`}, // already prefixed
		{"os:ubuntu", "fact", "G@os:ubuntu"},
		{"os:ubuntu", "grain", "G@os:ubuntu"},
		{"role:web", "settings", "I@role:web"},
		{"role:web", "pillar", "I@role:web"},
		{"a,b,c", "list", "L@a,b,c"},
		{"a and b", "compound", "a and b"},
	}
	for _, tt := range tests {
		got, err := canonicalTarget(tt.expr, tt.tt)
		if err != nil {
			t.Errorf("canonicalTarget(%q, %q): %v", tt.expr, tt.tt, err)
			continue
		}
		if got != tt.want {
			t.Errorf("canonicalTarget(%q, %q) = %q, want %q", tt.expr, tt.tt, got, tt.want)
		}
	}
	if _, err := canonicalTarget("", ""); err == nil {
		t.Error("empty target must error")
	}
	if _, err := canonicalTarget("x", "bogus"); err == nil {
		t.Error("unknown target type must error")
	}
}

func TestActionSummary(t *testing.T) {
	act := Action{
		Type: ActionDispatch, Function: "service.restart", Target: "web-01",
		Timeout: time.Minute, Args: map[string]any{"name": "nginx"},
	}
	s := act.Summary()
	for _, part := range []string{"service.restart", `"web-01"`, "name:nginx"} {
		if !strings.Contains(s, part) {
			t.Errorf("summary %q missing %q", s, part)
		}
	}
	if s2 := (Action{Type: ActionEnroll, EnrollOp: "approve", EnrollID: "enr-1", RequirePeel: "web-*"}).Summary(); !strings.Contains(s2, "enroll.approve") {
		t.Errorf("enroll summary: %s", s2)
	}
}
