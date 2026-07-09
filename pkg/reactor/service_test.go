package reactor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// plainPubSub deliberately lacks request/reply capabilities.
type plainPubSub struct{}

func (plainPubSub) Publish(string, []byte) error { return nil }
func (plainPubSub) Subscribe(string, func(*bus.Msg)) (bus.Subscription, error) {
	return nil, nil
}

func testServiceEngine(t *testing.T) *Engine {
	t.Helper()
	top := `
reactor:
  - 'web-*/deploy/*':
      - reactor.notify
  - '_master/enroll/pending/*':
      - reactor.broken
`
	rs := mustRuleSet(t, top, map[string]string{
		"reactor.notify": "say:\n  log:\n    message: deploy on {{ event.peel }} of {{ data.get('service', '?') }}\n",
		// Missing require_peel: surfaces as a per-rule error in the dry run.
		"reactor.broken": "approve:\n  enroll.approve:\n    - id: enr-1\n",
	})
	return testEngine(t, rs, &seamRecorder{resolved: []string{"web-01"}}, nil, nil)
}

func requestTest(t *testing.T, ps *bustest.FakePubSub, req TestRequest) TestResponse {
	t.Helper()
	data, err := bus.Encode(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg, err := ps.Request(ctx, bus.SubjectReactorTest, data)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var resp TestResponse
	if err := bus.Decode(msg.Data, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestTestServiceRoundTrip(t *testing.T) {
	e := testServiceEngine(t)
	ps := bustest.NewFakePubSub()
	stop, err := e.StartTestService(ps)
	if err != nil {
		t.Fatalf("StartTestService: %v", err)
	}
	defer stop()

	resp := requestTest(t, ps, TestRequest{
		Key:  "web-01/deploy/finished",
		Data: map[string]any{"service": "nginx"},
	})
	if resp.Err != "" {
		t.Fatalf("Err: %s", resp.Err)
	}
	if len(resp.Matched) != 1 {
		t.Fatalf("matched: %+v", resp.Matched)
	}
	mr := resp.Matched[0]
	if mr.Rule != "reactor.notify" {
		t.Errorf("rule: %q", mr.Rule)
	}
	if len(mr.Errors) != 0 {
		t.Errorf("errors: %v", mr.Errors)
	}
	if len(mr.Actions) != 1 || !strings.Contains(mr.Actions[0], "deploy on web-01 of nginx") {
		t.Errorf("actions: %v", mr.Actions)
	}
}

func TestTestServiceDottedOriginAndImpossibleTag(t *testing.T) {
	top := `
reactor:
  - 'web01.pl/deploy/*':
      - reactor.notify
`
	rs := mustRuleSet(t, top, map[string]string{
		"reactor.notify": "say:\n  log:\n    message: hi\n",
	})
	e := testEngine(t, rs, &seamRecorder{}, nil, nil)
	ps := bustest.NewFakePubSub()
	stop, err := e.StartTestService(ps)
	if err != nil {
		t.Fatalf("StartTestService: %v", err)
	}
	defer stop()

	// A dotted-hostname ORIGIN in the test key is encoded to its wire form
	// and matches the (equally dotted) rule.
	resp := requestTest(t, ps, TestRequest{Key: "web01.pl/deploy/finished"})
	if resp.Err != "" {
		t.Fatalf("dotted origin: Err %q", resp.Err)
	}
	if len(resp.Matched) != 1 {
		t.Fatalf("dotted origin: matched %+v", resp.Matched)
	}

	// A key whose TAG cannot exist on the wire (dots in tag territory) is an
	// ERROR, never a match — a dry run must not green-light a rule no live
	// event can reach.
	resp = requestTest(t, ps, TestRequest{Key: "web01.pl/beacon/db01.example.com/service"})
	if resp.Err == "" {
		t.Fatal("impossible dotted tag: want an error, got a dry-run result")
	}
	if !strings.Contains(resp.Err, "cannot occur on the wire") {
		t.Errorf("impossible-tag error should explain the wire form, got: %s", resp.Err)
	}
}

func TestTestServiceReportsRuleErrorsWithoutExecuting(t *testing.T) {
	e := testServiceEngine(t)
	ps := bustest.NewFakePubSub()
	stop, err := e.StartTestService(ps)
	if err != nil {
		t.Fatalf("StartTestService: %v", err)
	}
	defer stop()

	resp := requestTest(t, ps, TestRequest{Key: "_master/enroll/pending/enr-1"})
	if len(resp.Matched) != 1 || resp.Matched[0].Rule != "reactor.broken" {
		t.Fatalf("matched: %+v", resp.Matched)
	}
	if len(resp.Matched[0].Errors) != 1 || !strings.Contains(resp.Matched[0].Errors[0], "require_peel") {
		t.Errorf("errors: %v", resp.Matched[0].Errors)
	}
}

func TestTestServiceNoMatchAndBadKey(t *testing.T) {
	e := testServiceEngine(t)
	ps := bustest.NewFakePubSub()
	stop, err := e.StartTestService(ps)
	if err != nil {
		t.Fatalf("StartTestService: %v", err)
	}
	defer stop()

	if resp := requestTest(t, ps, TestRequest{Key: "db-01/other/tag"}); len(resp.Matched) != 0 || resp.Err != "" {
		t.Errorf("no-match response: %+v", resp)
	}
	for _, bad := range []string{"", "nokey", "/leading", "trailing/"} {
		if resp := requestTest(t, ps, TestRequest{Key: bad}); resp.Err == "" {
			t.Errorf("key %q must yield a request-level error", bad)
		}
	}
}

func TestTestServiceRequiresRequestPubSub(t *testing.T) {
	e := testServiceEngine(t)
	if _, err := e.StartTestService(plainPubSub{}); err == nil {
		t.Fatal("plain PubSub must be rejected")
	}
}
