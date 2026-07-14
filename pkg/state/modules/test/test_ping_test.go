package testmod

import (
	"context"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

var _ state.State = (*TestPing)(nil)

func TestTestPing(t *testing.T) {
	ctx := context.Background()
	s := buildPlain(t, NewTestPingBuilder, "web-01", map[string]any{})
	if s.Name() != "test.ping:web-01" {
		t.Errorf("Name: got %q", s.Name())
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("test.ping Check must force Apply (NeedsChange: true)")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("test.ping Apply must succeed: %v", err)
	}
	if ar.Changed {
		t.Error("test.ping Apply must report no change")
	}
	if ar.Details["result"] != "true" {
		t.Errorf("Details result: got %v", ar.Details)
	}

	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Error("test.ping Revert must report no change")
	}
}

func TestTestPingRequisites(t *testing.T) {
	s := buildPlain(t, NewTestPingBuilder, "web-01", map[string]any{
		"require": []any{"pkg.installed:nginx"},
		"watch":   []any{"file.managed:/etc/nginx/nginx.conf"},
	})
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/nginx/nginx.conf" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
}

// TestTestPingReservedKeysNotUnknownUnderPolicyError verifies that a reserved
// requisite key is not flagged as an unknown parameter even under the strict
// (gate-closed) unknown-key policy, when the reserved set is threaded — a
// no-parameter module must still tolerate every requisite/attribute directive.
func TestTestPingReservedKeysNotUnknownUnderPolicyError(t *testing.T) {
	opts := modschema.DecodeOptions{
		Unknown:  modschema.PolicyError,
		Reserved: state.ReservedKeySet(),
	}
	_, err := NewTestPingBuilder(opts)("web-01", map[string]any{
		"require": []any{"pkg.installed:nginx"},
		"order":   "last",
	})
	if err != nil {
		t.Fatalf("reserved keys must not be flagged unknown: %v", err)
	}

	// A genuinely unknown key IS an error under PolicyError.
	if _, err := NewTestPingBuilder(opts)("web-01", map[string]any{"bogus": 1}); err == nil {
		t.Error("expected an unknown-key error for a non-reserved, non-parameter key under PolicyError")
	}
}
