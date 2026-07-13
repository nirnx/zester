package modules

import (
	"context"
	"strings"
	"testing"
)

func TestTestNop(t *testing.T) {
	ctx := context.Background()
	s, err := NewTestNop("noop", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "test.nop:noop" {
		t.Errorf("Name: got %q", s.Name())
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("test.nop Check must report no change")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("test.nop Apply must succeed: %v", err)
	}
	if ar.Changed {
		t.Error("test.nop Apply must report no change")
	}
	if ar.Details["result"] != "true" {
		t.Errorf("Details result: got %v", ar.Details)
	}

	rr, err := s.Revert(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Error("test.nop Revert must report no change")
	}
}

func TestTestNopRequisites(t *testing.T) {
	s, err := NewTestNop("noop", map[string]any{
		"require": []any{"pkg.installed:nginx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
}

func TestTestFailWithoutChanges(t *testing.T) {
	ctx := context.Background()
	s, err := NewTestFailWithoutChanges("boom", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "test.fail_without_changes:boom" {
		t.Errorf("Name: got %q", s.Name())
	}

	// Check forces Apply so the failure is reported.
	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected Check to force Apply")
	}

	ar, err := s.Apply(ctx)
	if err == nil {
		t.Fatal("test.fail_without_changes Apply must fail")
	}
	if ar.Changed {
		t.Error("test.fail_without_changes must report changed=false")
	}
	if ar.Details["result"] != "false" {
		t.Errorf("Details result: got %v", ar.Details)
	}
}

func TestTestFailWithoutChangesComment(t *testing.T) {
	s, err := NewTestFailWithoutChanges("boom", map[string]any{"comment": "custom failure"})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "custom failure") {
		t.Errorf("error should contain comment: %v", err)
	}
	if ar.Details["comment"] != "custom failure" {
		t.Errorf("Details comment: got %v", ar.Details)
	}
}

func TestTestSucceedWithChanges(t *testing.T) {
	ctx := context.Background()
	s, err := NewTestSucceedWithChanges("ok", map[string]any{"comment": "pretend change"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "test.succeed_with_changes:ok" {
		t.Errorf("Name: got %q", s.Name())
	}

	cr, err := s.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected Check to force Apply")
	}

	ar, err := s.Apply(ctx)
	if err != nil {
		t.Fatalf("test.succeed_with_changes Apply must succeed: %v", err)
	}
	if !ar.Changed {
		t.Error("test.succeed_with_changes must report changed=true")
	}
	if ar.Details["result"] != "true" || ar.Details["comment"] != "pretend change" {
		t.Errorf("Details: got %v", ar.Details)
	}
}

func TestTestConfigurableTestState(t *testing.T) {
	ctx := context.Background()

	t.Run("defaults succeed with changes", func(t *testing.T) {
		s, err := NewTestConfigurableTestState("cfg", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		ar, err := s.Apply(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ar.Changed {
			t.Error("expected changed=true by default")
		}
	})

	t.Run("configured failure without changes", func(t *testing.T) {
		s, err := NewTestConfigurableTestState("cfg", map[string]any{
			"result":  false,
			"changes": false,
			"comment": "nope",
		})
		if err != nil {
			t.Fatal(err)
		}
		ar, err := s.Apply(ctx)
		if err == nil {
			t.Fatal("expected configured failure")
		}
		if ar.Changed {
			t.Error("expected changed=false")
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("error should contain comment: %v", err)
		}
	})
}
