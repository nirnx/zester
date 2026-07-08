package ca

import (
	"strings"
	"testing"
)

func TestNormalizePin(t *testing.T) {
	a := testAuthority(t)
	pin := a.RootSPKIPin()

	// Canonical round-trip, case-insensitivity, whitespace tolerance.
	for _, in := range []string{pin, strings.ToUpper(pin[:7]) + pin[7:], "  " + pin + "\n"} {
		got, err := NormalizePin(in)
		if err != nil || got != pin {
			t.Errorf("NormalizePin(%q) = (%q, %v), want (%q, nil)", in, got, err, pin)
		}
	}

	for _, bad := range []string{"", "sha256:", "sha256:zz", "sha1:" + pin[7:], pin[7:], "sha256:" + pin[7:20]} {
		if _, err := NormalizePin(bad); err == nil {
			t.Errorf("NormalizePin(%q) succeeded, want error", bad)
		}
	}
}

func TestFindPinnedRoot_TrustsExactlyThePinnedCert(t *testing.T) {
	real := testAuthority(t)
	attacker := testAuthority(t)

	pin := real.RootSPKIPin()

	// The bundle-poisoning scenario: a served bundle carrying the real
	// (public) root PLUS an attacker CA. Only the pinned cert may become
	// the anchor.
	poisoned := append(append([]byte{}, attacker.Bundle()...), real.Bundle()...)
	got, err := FindPinnedRoot(poisoned, []string{pin})
	if err != nil {
		t.Fatalf("FindPinnedRoot: %v", err)
	}
	if SPKIPin(got) != pin {
		t.Fatalf("FindPinnedRoot returned an unpinned certificate")
	}
	if got.Subject.CommonName != real.Root.Subject.CommonName {
		t.Errorf("wrong cert selected: %s", got.Subject.CommonName)
	}

	// A bundle with only the attacker CA must never match.
	if _, err := FindPinnedRoot(attacker.Bundle(), []string{pin}); err == nil {
		t.Fatal("attacker-only bundle matched the pin")
	}

	// Multi-pin (rotation window): either pin selects its root.
	if _, err := FindPinnedRoot(poisoned, []string{attacker.RootSPKIPin(), pin}); err != nil {
		t.Fatalf("multi-pin: %v", err)
	}
}

func TestSPKIPin_StableAcrossReissue(t *testing.T) {
	a := testAuthority(t)
	// Re-encoding / re-parsing the same cert yields the same pin; and the
	// pin differs from the DER fingerprint concept.
	if SPKIPin(a.Root) != a.RootSPKIPin() {
		t.Error("pin not stable")
	}
	if a.RootSPKIPin() == a.Fingerprint() {
		t.Error("SPKI pin and DER fingerprint should differ")
	}
	if !MatchPin(a.Root, []string{a.RootSPKIPin()}) {
		t.Error("MatchPin rejects the root's own pin")
	}
	if MatchPin(a.Root, []string{"sha256:" + strings.Repeat("00", 32)}) {
		t.Error("MatchPin accepted a wrong pin")
	}
}
