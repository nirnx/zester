package testmod

import (
	"context"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// buildPlain constructs a test-helper (BuildPlain) state with a default decode
// policy, failing the test on a build/decode error.
func buildPlain(t *testing.T, f regdef.PlainBuildFunc, id string, config map[string]any) state.State {
	t.Helper()
	s, err := f(modschema.DecodeOptions{})(id, config)
	if err != nil {
		t.Fatalf("build %s: %v", id, err)
	}
	return s
}

func TestTestNop(t *testing.T) {
	ctx := context.Background()
	s := buildPlain(t, NewTestNopBuilder, "noop", map[string]any{})
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
	s := buildPlain(t, NewTestNopBuilder, "noop", map[string]any{
		"require": []any{"pkg.installed:nginx"},
	})
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
		t.Errorf("Require: got %v", reqs.Require)
	}
}

func TestTestFailWithoutChanges(t *testing.T) {
	ctx := context.Background()
	s := buildPlain(t, NewTestFailWithoutChangesBuilder, "boom", map[string]any{})
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
	if ar.Details["comment"] != "failure without changes" {
		t.Errorf("default comment: got %v", ar.Details)
	}
}

func TestTestFailWithoutChangesComment(t *testing.T) {
	s := buildPlain(t, NewTestFailWithoutChangesBuilder, "boom", map[string]any{"comment": "custom failure"})
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

// TestTestFailWithoutChangesNumericComment pins the BD-6 coercion: a numeric
// comment is coerced to its string form (the legacy .(string) assertion dropped
// it to "").
func TestTestFailWithoutChangesNumericComment(t *testing.T) {
	s := buildPlain(t, NewTestFailWithoutChangesBuilder, "boom", map[string]any{"comment": 123})
	ar, err := s.Apply(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if ar.Details["comment"] != "123" {
		t.Errorf("numeric comment should coerce to \"123\": got %v", ar.Details)
	}
}

func TestTestSucceedWithChanges(t *testing.T) {
	ctx := context.Background()
	s := buildPlain(t, NewTestSucceedWithChangesBuilder, "ok", map[string]any{"comment": "pretend change"})
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
		s := buildPlain(t, NewTestConfigurableTestStateBuilder, "cfg", map[string]any{})
		ar, err := s.Apply(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ar.Changed {
			t.Error("expected changed=true by default")
		}
		if ar.Details["result"] != "true" {
			t.Errorf("expected result=true by default: got %v", ar.Details)
		}
	})

	t.Run("configured failure without changes", func(t *testing.T) {
		s := buildPlain(t, NewTestConfigurableTestStateBuilder, "cfg", map[string]any{
			"result":  false,
			"changes": false,
			"comment": "nope",
		})
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

	// BD-2: a truthy/falsy STRING result/changes is honored (the legacy .(bool)
	// assertion silently kept the default true).
	t.Run("string booleans honored (BD-2)", func(t *testing.T) {
		s := buildPlain(t, NewTestConfigurableTestStateBuilder, "cfg", map[string]any{
			"result":  "no",
			"changes": "false",
		})
		ar, err := s.Apply(ctx)
		if err == nil {
			t.Fatal("expected failure from result=\"no\"")
		}
		if ar.Changed {
			t.Error("expected changed=false from changes=\"false\"")
		}
	})

	// BD-7: an integer result/changes is 1=true / 0=false; any other integer a
	// typed decode error.
	t.Run("integer booleans honored (BD-7)", func(t *testing.T) {
		s := buildPlain(t, NewTestConfigurableTestStateBuilder, "cfg", map[string]any{
			"result":  0,
			"changes": 1,
		})
		ar, err := s.Apply(ctx)
		if err == nil {
			t.Fatal("expected failure from result=0")
		}
		if !ar.Changed {
			t.Error("expected changed=true from changes=1")
		}
	})

	t.Run("invalid integer boolean is a decode error (BD-7)", func(t *testing.T) {
		_, err := NewTestConfigurableTestStateBuilder(modschema.DecodeOptions{})("cfg", map[string]any{"result": 2})
		if err == nil {
			t.Fatal("expected a decode error for result=2")
		}
	})
}
