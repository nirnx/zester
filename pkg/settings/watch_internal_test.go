package settings

import (
	"testing"
	"time"
)

// TestWatchReconnectJitter_Deterministic verifies the jitter offset is a
// stable function of the identity, bounded by the spread, and disabled when
// the spread is zero.
func TestWatchReconnectJitter_Deterministic(t *testing.T) {
	SetWatchJitter("", DefaultWatchReconnectJitter)
	t.Cleanup(func() { SetWatchJitter("", DefaultWatchReconnectJitter) })

	a1 := watchReconnectJitter("peel-01")
	a2 := watchReconnectJitter("peel-01")
	if a1 != a2 {
		t.Errorf("jitter not deterministic: %v vs %v", a1, a2)
	}
	if a1 < 0 || a1 >= DefaultWatchReconnectJitter {
		t.Errorf("jitter %v out of range [0, %v)", a1, DefaultWatchReconnectJitter)
	}

	// Different identities should (for these fixed inputs) land on
	// different offsets — that is the whole point of the spread.
	b := watchReconnectJitter("peel-02")
	if a1 == b {
		t.Errorf("expected different offsets for peel-01 (%v) and peel-02 (%v)", a1, b)
	}

	// Zero spread disables jitter entirely.
	SetWatchJitter("ignored", 0)
	if got := watchReconnectJitter("peel-01"); got != 0 {
		t.Errorf("jitter with zero spread = %v, want 0", got)
	}

	// Explicit ID falls back to the package-level ID when empty.
	SetWatchJitter("fallback-id", time.Minute)
	if got, want := watchReconnectJitter(""), watchReconnectJitter("fallback-id"); got != want {
		t.Errorf("empty-id jitter = %v, want package fallback %v", got, want)
	}
}

// TestSecretsFingerprint_Canonical verifies the fingerprint is independent
// of map iteration order, sensitive to every component, and unambiguous
// under key/value boundary shifts.
func TestSecretsFingerprint_Canonical(t *testing.T) {
	base := secretsFingerprint("sender", "recipient", map[string]string{"a": "1", "b": "2"})

	if got := secretsFingerprint("sender", "recipient", map[string]string{"b": "2", "a": "1"}); got != base {
		t.Error("fingerprint depends on map insertion order")
	}
	if got := secretsFingerprint("sender2", "recipient", map[string]string{"a": "1", "b": "2"}); got == base {
		t.Error("fingerprint ignores sender key")
	}
	if got := secretsFingerprint("sender", "recipient2", map[string]string{"a": "1", "b": "2"}); got == base {
		t.Error("fingerprint ignores recipient key")
	}
	if got := secretsFingerprint("sender", "recipient", map[string]string{"a": "1", "b": "3"}); got == base {
		t.Error("fingerprint ignores secret values")
	}
	// Boundary ambiguity: {"a": "1b", ...} must differ from {"a1": "b", ...}.
	x := secretsFingerprint("s", "r", map[string]string{"a": "1b"})
	y := secretsFingerprint("s", "r", map[string]string{"a1": "b"})
	if x == y {
		t.Error("fingerprint is ambiguous under key/value boundary shifts")
	}
}
