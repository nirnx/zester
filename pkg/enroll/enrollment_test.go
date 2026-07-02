package enroll_test

import (
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/enroll"
)

func TestParseState(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   enroll.State
		wantOK bool
	}{
		{"pending", "pending", enroll.StatePending, true},
		{"approved", "approved", enroll.StateApproved, true},
		{"rejected", "rejected", enroll.StateRejected, true},
		{"issued", "issued", enroll.StateIssued, true},
		{"active", "active", enroll.StateActive, true},
		{"revoked", "revoked", enroll.StateRevoked, true},
		{"unknown", "unknown", "", false},
		{"empty", "", "", false},
		{"invalid", "invalid-state", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := enroll.ParseState(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParseState(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("ParseState(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRecordCanTransitionTo_ValidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		from   enroll.State
		to     enroll.State
		wantOK bool
	}{
		// Valid transitions from pending
		{"pending->approved", enroll.StatePending, enroll.StateApproved, true},
		{"pending->rejected", enroll.StatePending, enroll.StateRejected, true},

		// Valid transitions from approved
		{"approved->issued", enroll.StateApproved, enroll.StateIssued, true},
		{"approved->revoked", enroll.StateApproved, enroll.StateRevoked, true},

		// Valid transitions from issued
		{"issued->active", enroll.StateIssued, enroll.StateActive, true},
		{"issued->revoked", enroll.StateIssued, enroll.StateRevoked, true},

		// Valid transitions from active
		{"active->revoked", enroll.StateActive, enroll.StateRevoked, true},

		// No valid transitions from rejected or revoked (terminal states)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &enroll.Record{State: tt.from}
			got := rec.CanTransitionTo(tt.to)
			if got != tt.wantOK {
				t.Errorf("CanTransitionTo(%q -> %q) = %v, want %v", tt.from, tt.to, got, tt.wantOK)
			}
		})
	}
}

func TestRecordCanTransitionTo_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from enroll.State
		to   enroll.State
	}{
		// Invalid transitions from pending
		{"pending->issued", enroll.StatePending, enroll.StateIssued},
		{"pending->active", enroll.StatePending, enroll.StateActive},
		{"pending->revoked", enroll.StatePending, enroll.StateRevoked},

		// Invalid transitions from approved
		{"approved->pending", enroll.StateApproved, enroll.StatePending},
		{"approved->rejected", enroll.StateApproved, enroll.StateRejected},
		{"approved->active", enroll.StateApproved, enroll.StateActive},

		// Invalid transitions from rejected (terminal state)
		{"rejected->pending", enroll.StateRejected, enroll.StatePending},
		{"rejected->approved", enroll.StateRejected, enroll.StateApproved},
		{"rejected->issued", enroll.StateRejected, enroll.StateIssued},
		{"rejected->active", enroll.StateRejected, enroll.StateActive},
		{"rejected->revoked", enroll.StateRejected, enroll.StateRevoked},

		// Invalid transitions from issued
		{"issued->pending", enroll.StateIssued, enroll.StatePending},
		{"issued->approved", enroll.StateIssued, enroll.StateApproved},
		{"issued->rejected", enroll.StateIssued, enroll.StateRejected},

		// Invalid transitions from active
		{"active->pending", enroll.StateActive, enroll.StatePending},
		{"active->approved", enroll.StateActive, enroll.StateApproved},
		{"active->rejected", enroll.StateActive, enroll.StateRejected},
		{"active->issued", enroll.StateActive, enroll.StateIssued},

		// Invalid transitions from revoked (terminal state)
		{"revoked->pending", enroll.StateRevoked, enroll.StatePending},
		{"revoked->approved", enroll.StateRevoked, enroll.StateApproved},
		{"revoked->rejected", enroll.StateRevoked, enroll.StateRejected},
		{"revoked->issued", enroll.StateRevoked, enroll.StateIssued},
		{"revoked->active", enroll.StateRevoked, enroll.StateActive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &enroll.Record{State: tt.from}
			got := rec.CanTransitionTo(tt.to)
			if got {
				t.Errorf("CanTransitionTo(%q -> %q) = true, want false (invalid transition)", tt.from, tt.to)
			}
		})
	}
}

func TestRecordSummary(t *testing.T) {
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:        "enr-test123",
		PeelID:    "web-01",
		PublicKey: "UABC123...",
		Hostname:  "server.example.com",
		State:     enroll.StatePending,
		CreatedAt: now,
		Metadata: map[string]string{
			"os":   "linux",
			"arch": "amd64",
		},
	}

	summary := rec.Summary()

	if summary.ID != rec.ID {
		t.Errorf("Summary.ID = %q, want %q", summary.ID, rec.ID)
	}
	if summary.PeelID != rec.PeelID {
		t.Errorf("Summary.PeelID = %q, want %q", summary.PeelID, rec.PeelID)
	}
	if summary.PublicKey != rec.PublicKey {
		t.Errorf("Summary.PublicKey = %q, want %q", summary.PublicKey, rec.PublicKey)
	}
	if summary.Hostname != rec.Hostname {
		t.Errorf("Summary.Hostname = %q, want %q", summary.Hostname, rec.Hostname)
	}
	if summary.State != rec.State {
		t.Errorf("Summary.State = %q, want %q", summary.State, rec.State)
	}
	if !summary.CreatedAt.Equal(rec.CreatedAt) {
		t.Errorf("Summary.CreatedAt = %v, want %v", summary.CreatedAt, rec.CreatedAt)
	}
	if len(summary.Metadata) != 2 || summary.Metadata["os"] != "linux" {
		t.Errorf("Summary.Metadata = %v, want %v", summary.Metadata, rec.Metadata)
	}
}
