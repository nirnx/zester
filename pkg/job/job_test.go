package job

import (
	"testing"
	"time"
)

func TestNewJID(t *testing.T) {
	jid1 := NewJID()
	jid2 := NewJID()

	if jid1 == "" {
		t.Error("JID should not be empty")
	}
	if jid1 == jid2 {
		t.Error("JIDs should be unique")
	}
	// KSUID strings are 27 characters.
	if len(jid1) != 27 {
		t.Errorf("JID length: got %d, want 27", len(jid1))
	}
}

func TestNewJob(t *testing.T) {
	j := NewJob("cmd.run", map[string]any{"cmd": "uptime"}, []string{"web-01", "web-02"}, 30*time.Second)

	if j.JID == "" {
		t.Error("JID should not be empty")
	}
	if j.Function != "cmd.run" {
		t.Errorf("Function: got %q, want %q", j.Function, "cmd.run")
	}
	if j.Status != StatusPending {
		t.Errorf("Status: got %q, want %q", j.Status, StatusPending)
	}
	if j.TargetCount() != 2 {
		t.Errorf("TargetCount: got %d, want 2", j.TargetCount())
	}
	if j.Timeout != 30*time.Second {
		t.Errorf("Timeout: got %v, want 30s", j.Timeout)
	}
	if j.Created.IsZero() {
		t.Error("Created should be set")
	}
	if j.Updated.IsZero() {
		t.Error("Updated should be set")
	}
	if want := j.Created.Add(30 * time.Second); !j.Deadline.Equal(want) {
		t.Errorf("Deadline: got %v, want Created+Timeout (%v)", j.Deadline, want)
	}
	if j.ReclaimCount != 0 {
		t.Errorf("ReclaimCount: got %d, want 0", j.ReclaimCount)
	}
}

// TestNewJobZeroTimeoutDeadline verifies that a job without an explicit
// timeout still gets a deadline based on the default timeout.
func TestNewJobZeroTimeoutDeadline(t *testing.T) {
	j := NewJob("cmd.run", nil, []string{"web-01"}, 0)

	if want := j.Created.Add(defaultJobTimeout); !j.Deadline.Equal(want) {
		t.Errorf("Deadline: got %v, want Created+default (%v)", j.Deadline, want)
	}
}

func TestJobIsTerminal(t *testing.T) {
	tests := []struct {
		status   Status
		terminal bool
	}{
		{StatusPending, false},
		{StatusRunning, false},
		{StatusCanceled, true},
		{StatusComplete, true},
		{StatusPartial, true},
		{StatusTimeout, true},
		{StatusFailed, true},
	}

	for _, tt := range tests {
		j := &Job{Status: tt.status}
		if got := j.IsTerminal(); got != tt.terminal {
			t.Errorf("IsTerminal(%s): got %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestJobTargetCount(t *testing.T) {
	j := &Job{Targets: []string{"a", "b", "c"}}
	if j.TargetCount() != 3 {
		t.Errorf("TargetCount: got %d, want 3", j.TargetCount())
	}

	j = &Job{}
	if j.TargetCount() != 0 {
		t.Errorf("TargetCount empty: got %d, want 0", j.TargetCount())
	}
}

func TestReturnStruct(t *testing.T) {
	r := Return{
		JID:        "test-jid",
		PeelID:     "web-01",
		Success:    true,
		ReturnData: map[string]string{"output": "hello"},
		Duration:   500 * time.Millisecond,
		Timestamp:  time.Now().UTC(),
	}

	if r.JID != "test-jid" {
		t.Errorf("JID: got %q", r.JID)
	}
	if !r.Success {
		t.Error("expected Success")
	}
}

func TestAckStruct(t *testing.T) {
	a := Ack{
		JID:       "test-jid",
		PeelID:    "web-01",
		Timestamp: time.Now().UTC(),
	}

	if a.JID != "test-jid" {
		t.Errorf("JID: got %q", a.JID)
	}
	if a.PeelID != "web-01" {
		t.Errorf("PeelID: got %q", a.PeelID)
	}
}

func TestEventConstants(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"dispatched", EventDispatched, "dispatched"},
		{"acked", EventAcked, "acked"},
		{"returned", EventReturned, "returned"},
		{"completed", EventCompleted, "completed"},
		{"canceled", EventCanceled, "canceled"},
		{"timeout", EventTimeout, "timeout"},
		{"failed", EventFailed, "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

func TestJIDOrdering(t *testing.T) {
	// KSUIDs generated later should sort after earlier ones.
	// KSUID has 1-second timestamp resolution, so we need >1s gap.
	jid1 := NewJID()
	time.Sleep(1100 * time.Millisecond)
	jid2 := NewJID()

	if jid1 >= jid2 {
		t.Errorf("expected jid1 < jid2 for time ordering: %s >= %s", jid1, jid2)
	}
}
