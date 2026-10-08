package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/enroll"
)

// hasControl reports whether s contains any byte a terminal would interpret
// (ESC, CR, LF, TAB, BEL, ...) other than the line separators the renderer
// itself emits — callers strip those first.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// TestPrintEnrollList_SanitizesAndShowsSource pins the two operator-facing
// defenses of `zester enroll list`: requester-supplied hostnames are rendered
// through displayString (an ANSI/CR payload cannot rewrite the table), and the
// SOURCE column shows the request's peer address so a squat from an unexpected
// network stands out next to the hostname it claims.
func TestPrintEnrollList_SanitizesAndShowsSource(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	// A hostname crafted to clear the line and fake an "approved" row.
	evil := "web-01\x1b[2K\rweb-01  10.0.0.1  approved\x07"
	records := []*enroll.Record{
		{ID: "enr-1", PeelID: "web-01", Hostname: evil, RemoteAddr: "203.0.113.9", State: enroll.StatePending, CreatedAt: now},
		{ID: "enr-2", PeelID: "db_internal", State: enroll.StateActive, TrustChecked: true, CreatedAt: now},
		{ID: "enr-3", PeelID: "lb-01", Hostname: "lb-01.example.com", RemoteAddr: "10.0.0.5", State: enroll.StatePending, TrustMismatch: true, CreatedAt: now},
	}

	var buf bytes.Buffer
	printEnrollList(&buf, records)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 3 rows, got %d lines:\n%s", len(lines), out)
	}

	header := strings.Fields(lines[0])
	wantHeader := []string{"ID", "PEEL", "ID", "HOSTNAME", "SOURCE", "STATE", "TRUST", "CREATED"}
	if strings.Join(header, " ") != strings.Join(wantHeader, " ") {
		t.Errorf("header = %q, want %q", lines[0], strings.Join(wantHeader, " "))
	}

	// Row 1: no control bytes survive, the peer address is shown.
	if hasControl(lines[1]) {
		t.Errorf("row 1 still contains control bytes: %q", lines[1])
	}
	if !strings.Contains(lines[1], "203.0.113.9") {
		t.Errorf("row 1 missing SOURCE address: %q", lines[1])
	}
	if !strings.Contains(lines[1], "web-01?[2K?web-01  10.0.0.1  approved?") {
		t.Errorf("row 1 should neutralize ESC/CR/BEL to '?' and keep printable bytes; got %q", lines[1])
	}
	if !strings.HasPrefix(lines[1], "enr-1") || !strings.Contains(lines[1], "pending") {
		t.Errorf("row 1 identity/state mangled: %q", lines[1])
	}

	// Row 2: empty hostname/source render as "-", peel id shown in dotted form.
	f := strings.Fields(lines[2])
	if len(f) < 6 || f[0] != "enr-2" || f[1] != "db.internal" || f[2] != "-" || f[3] != "-" || f[4] != "active" || f[5] != "ok" {
		t.Errorf("row 2 fields = %v, want enr-2 db.internal - - active ok ...", f)
	}

	// Row 3: TRUST mismatch label with source.
	f = strings.Fields(lines[3])
	if len(f) < 6 || f[2] != "lb-01.example.com" || f[3] != "10.0.0.5" || f[5] != "MISMATCH!" {
		t.Errorf("row 3 fields = %v", f)
	}
}

// TestPrintEnrollRecord_SanitizesFreeText: `zester enroll show` renders every
// externally supplied string (hostname, metadata keys and values, reported CA,
// reject reason, decided-by, peer address) through displayString, prints
// metadata with sorted keys, and never lets an embedded newline fake a field.
func TestPrintEnrollRecord_SanitizesFreeText(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	decided := now.Add(time.Minute)
	rec := &enroll.Record{
		ID:            "enr-9",
		PeelID:        "web_01",
		State:         enroll.StateRejected,
		PublicKey:     "UABC",
		Hostname:      "host\x1b[31mRED\x1b[0m",
		Metadata:      map[string]string{"zone\n": "eu\x07", "arch": "amd64", "os": "Linux \u202eevil"},
		DecidedBy:     "ops\radmin",
		DecidedAt:     &decided,
		RejectReason:  "squat attempt\nTrust:          ok",
		RemoteAddr:    "203.0.113.9",
		TrustedCASPKI: "sha256:abc\x1b[2J",
		TrustMismatch: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	var buf bytes.Buffer
	printEnrollRecord(&buf, rec)
	out := buf.String()

	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if hasControl(line) {
			t.Errorf("line %d contains control bytes: %q", i, line)
		}
	}

	for _, want := range []string{
		"Peel ID:        web.01",
		"Hostname:       host?[31mRED?[0m",
		"Metadata:",
		"    arch: amd64",
		"    os: Linux ?evil",
		"    zone?: eu?",
		"Trust:          MISMATCH! peel reported CA sha256:abc?[2J",
		"Decided By:     ops?admin",
		"Reject Reason:  squat attempt?Trust:          ok",
		"Remote Addr:    203.0.113.9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Metadata keys are sorted (arch, os, zone?) for deterministic output.
	ai, oi, zi := strings.Index(out, "    arch:"), strings.Index(out, "    os:"), strings.Index(out, "    zone?:")
	if !(ai < oi && oi < zi) {
		t.Errorf("metadata keys not sorted: arch@%d os@%d zone@%d", ai, oi, zi)
	}

	// The injected newline in the reason produced exactly ONE "Trust:" line.
	if n := strings.Count(out, "\nTrust:"); n != 1 {
		t.Errorf("want exactly one Trust: line, got %d:\n%s", n, out)
	}
}
