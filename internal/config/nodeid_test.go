package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/enroll"
)

func TestResolveNodeID_ExplicitSanitized(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	// An explicit id with dots is sanitized into a valid subject token
	// ('.' -> '_' so it stays collision-free with hyphenated hostnames).
	id, err := ResolveNodeID("web01.example.com", "", log)
	if err != nil {
		t.Fatalf("ResolveNodeID: %v", err)
	}
	if id != "web01_example_com" {
		t.Errorf("id = %q, want web01_example_com", id)
	}

	// An already-valid id is unchanged.
	if id, _ := ResolveNodeID("web-01", "", log); id != "web-01" {
		t.Errorf("id = %q, want web-01", id)
	}
}

func TestDerivedNodeID(t *testing.T) {
	// Normal FQDN derives the encoded token.
	id, err := derivedNodeID("web01.example.com")
	if err != nil {
		t.Fatalf("derivedNodeID: %v", err)
	}
	if id != "web01_example_com" {
		t.Errorf("id = %q, want web01_example_com", id)
	}

	// The '_' reservation covers the DERIVED path too: a non-RFC underscore
	// hostname is refused — sanitizing it would mint an id whose dotted
	// display names a host that does not exist and that pre-collides with the
	// genuine dotted host.
	if _, err := derivedNodeID("db_primary.example.com"); err == nil {
		t.Error("underscore hostname must be refused on the derived path")
	}

	// Localhost placeholders refused; empty refused.
	if _, err := derivedNodeID("localhost.localdomain"); err == nil {
		t.Error("localhost placeholder must be refused")
	}
	if _, err := derivedNodeID(""); err == nil {
		t.Error("empty hostname must be refused")
	}
}

func TestResolveNodeID_ExplicitUnderscoreReserved(t *testing.T) {
	// '_' is the wire encoding of '.' (enroll.DisplayPeelID decodes it), so a
	// configured id must spell the dot — a raw '_' would make the display
	// decode ambiguous and is refused with guidance.
	_, err := ResolveNodeID("web_01", "", slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("explicit id with '_' must be refused (reserved as the '.' encoding)")
	}
	if !strings.Contains(err.Error(), "web.01") {
		t.Errorf("error should suggest the dotted form, got: %v", err)
	}
}

func TestResolveNodeID_ExplicitNeverPinned(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveNodeID("web-01", dir, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("ResolveNodeID: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, nodeIDFileName)); !os.IsNotExist(err) {
		t.Error("an explicit id must not write an identity pin (config is the source of truth)")
	}
}

func TestResolveNodeID_HostnameFallbackAndPin(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.DiscardHandler)

	// Empty explicit id -> derived from hostname; must be non-empty and valid.
	id, err := ResolveNodeID("", dir, log)
	if err != nil {
		t.Fatalf("ResolveNodeID(empty): %v", err)
	}
	if id == "" {
		t.Fatal("expected a hostname-derived id, got empty")
	}
	if err := enroll.ValidatePeelID(id); err != nil {
		t.Errorf("hostname-derived id %q is not a valid peel id: %v", id, err)
	}

	// The derived id is pinned so a later DNS-degraded `hostname -f` cannot
	// re-identify the node.
	data, err := os.ReadFile(filepath.Join(dir, nodeIDFileName))
	if err != nil {
		t.Fatalf("identity pin not written: %v", err)
	}
	if got := string(data); got != id+"\n" {
		t.Errorf("pin content = %q, want %q", got, id+"\n")
	}

	// A second resolve returns the pinned identity.
	again, err := ResolveNodeID("", dir, log)
	if err != nil {
		t.Fatalf("ResolveNodeID(pinned): %v", err)
	}
	if again != id {
		t.Errorf("second resolve = %q, want pinned %q", again, id)
	}
}

func TestResolveNodeID_PinWins(t *testing.T) {
	// A pre-existing pin wins over the live hostname — the whole point of
	// pinning is boot-to-boot identity stability.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nodeIDFileName), []byte("pinned-node-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := ResolveNodeID("", dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("ResolveNodeID: %v", err)
	}
	if id != "pinned-node-01" {
		t.Errorf("id = %q, want the pinned pinned-node-01", id)
	}
}

func TestResolveNodeID_InvalidPinIgnored(t *testing.T) {
	// A corrupt pin (invalid peel id) is ignored and overwritten by a fresh
	// derivation instead of bricking startup.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, nodeIDFileName), []byte("_bad.pin!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := ResolveNodeID("", dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("ResolveNodeID: %v", err)
	}
	if err := enroll.ValidatePeelID(id); err != nil {
		t.Errorf("re-derived id %q invalid: %v", id, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, nodeIDFileName))
	if got := string(data); got != id+"\n" {
		t.Errorf("pin not overwritten: content %q, want %q", got, id+"\n")
	}
}

func TestResolveNodeID_ExplicitLocalhostAllowed(t *testing.T) {
	// The localhost guard applies only to DERIVED ids; an operator explicitly
	// configuring "localhost" (e.g. a scratch VM) is their call.
	id, err := ResolveNodeID("localhost", "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("explicit localhost refused: %v", err)
	}
	if id != "localhost" {
		t.Errorf("id = %q, want localhost", id)
	}
}

func TestIsLocalhostID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"localhost", true},
		{"localhost_localdomain", true}, // sanitized localhost.localdomain
		{"localhost4", true},
		{"localhost4_localdomain4", true},
		{"localhost6", true},
		{"LOCALHOST", true},
		{"localhost-dev", false}, // a real (if unwise) hostname
		{"web-01", false},
		{"localhostile", false},
	}
	for _, c := range cases {
		if got := isLocalhostID(c.id); got != c.want {
			t.Errorf("isLocalhostID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
