package update

import (
	"context"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

func setupStatusTest(t *testing.T) bus.KV {
	t.Helper()
	ctx := context.Background()
	js := bustest.NewFakeJS()
	bus.InitializeStorage(ctx, js)
	kv, err := bus.GetBucket(ctx, js, bus.BucketUpdateStatus)
	if err != nil {
		t.Fatal(err)
	}
	return kv
}

func TestStatusKey(t *testing.T) {
	if got := StatusKey("peel", "web-01"); got != "peel.web-01" {
		t.Errorf("got %q, want %q", got, "peel.web-01")
	}
	if got := StatusKey("master", "m-01"); got != "master.m-01" {
		t.Errorf("got %q, want %q", got, "master.m-01")
	}
}

func TestGetNodeStatus(t *testing.T) {
	ctx := context.Background()
	kv := setupStatusTest(t)

	want := &NodeStatus{
		ID:          "web-01",
		Component:   "peel",
		Version:     "v1.2.3",
		State:       "running",
		GOOS:        "linux",
		GOARCH:      "amd64",
		ChildPID:    42,
		ChildUptime: "1m0s",
		UpdatedAt:   time.Now().Round(time.Second),
	}
	if _, err := bus.KVPut(ctx, kv, StatusKey("peel", "web-01"), want); err != nil {
		t.Fatal(err)
	}

	got, err := GetNodeStatus(ctx, kv, "peel", "web-01")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	if got.Component != want.Component {
		t.Errorf("Component: got %q, want %q", got.Component, want.Component)
	}
	if got.Version != want.Version {
		t.Errorf("Version: got %q, want %q", got.Version, want.Version)
	}
	if got.State != want.State {
		t.Errorf("State: got %q, want %q", got.State, want.State)
	}
	if got.ChildPID != want.ChildPID {
		t.Errorf("ChildPID: got %d, want %d", got.ChildPID, want.ChildPID)
	}
}

func TestGetNodeStatus_NotFound(t *testing.T) {
	ctx := context.Background()
	kv := setupStatusTest(t)

	_, err := GetNodeStatus(ctx, kv, "peel", "nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent node, got nil")
	}
}

func TestListNodeStatuses(t *testing.T) {
	ctx := context.Background()
	kv := setupStatusTest(t)

	statuses := []*NodeStatus{
		{ID: "web-01", Component: "peel", Version: "v1.0.0", State: "running"},
		{ID: "web-02", Component: "peel", Version: "v1.0.0", State: "running"},
		{ID: "m-01", Component: "master", Version: "v1.0.0", State: "running"},
	}
	for _, s := range statuses {
		if _, err := bus.KVPut(ctx, kv, StatusKey(s.Component, s.ID), s); err != nil {
			t.Fatal(err)
		}
	}

	peels, err := ListNodeStatuses(ctx, kv, "peel")
	if err != nil {
		t.Fatal(err)
	}
	if len(peels) != 2 {
		t.Errorf("peel count: got %d, want 2", len(peels))
	}

	masters, err := ListNodeStatuses(ctx, kv, "master")
	if err != nil {
		t.Fatal(err)
	}
	if len(masters) != 1 {
		t.Errorf("master count: got %d, want 1", len(masters))
	}
}

func TestReporter_WritesStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kv := setupStatusTest(t)

	cfg := ReporterConfig{
		KV:        kv,
		ID:        "web-01",
		Component: "peel",
		Interval:  50 * time.Millisecond,
	}

	reporter := NewReporter(
		cfg,
		func() string { return "v1.0.0" },
		func() string { return "running" },
		func() int { return 12345 },
		func() time.Duration { return 30 * time.Second },
	)

	done := make(chan struct{})
	go func() {
		reporter.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	got, err := GetNodeStatus(context.Background(), kv, "peel", "web-01")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1.0.0" {
		t.Errorf("Version: got %q, want %q", got.Version, "v1.0.0")
	}
	if got.State != "running" {
		t.Errorf("State: got %q, want %q", got.State, "running")
	}
	if got.ChildPID != 12345 {
		t.Errorf("ChildPID: got %d, want 12345", got.ChildPID)
	}
	// No DegradedFn wired: Degraded must default to false.
	if got.Degraded {
		t.Error("Degraded: got true, want false when DegradedFn is nil")
	}
}

func TestReporter_ReportsDegraded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kv := setupStatusTest(t)

	cfg := ReporterConfig{
		KV:         kv,
		ID:         "web-02",
		Component:  "peel",
		Interval:   50 * time.Millisecond,
		DegradedFn: func() bool { return true },
	}

	reporter := NewReporter(
		cfg,
		func() string { return "v1.0.0" },
		func() string { return "degraded" },
		func() int { return 0 },
		func() time.Duration { return 0 },
	)

	done := make(chan struct{})
	go func() {
		reporter.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	got, err := GetNodeStatus(context.Background(), kv, "peel", "web-02")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Degraded {
		t.Error("Degraded: got false, want true when DegradedFn reports true")
	}
}

func TestNodeStatus_MsgpackRoundTrip(t *testing.T) {
	original := NodeStatus{
		ID:          "web-01",
		Component:   "peel",
		Version:     "v2.0.0",
		State:       "updating",
		GOOS:        "linux",
		GOARCH:      "arm64",
		ChildPID:    9999,
		ChildUptime: "5m0s",
		Degraded:    true,
		UpdatedAt:   time.Now().UTC().Round(time.Millisecond),
	}

	data, err := bus.Encode(&original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded NodeStatus
	if err := bus.Decode(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Component != original.Component {
		t.Errorf("Component: got %q, want %q", decoded.Component, original.Component)
	}
	if decoded.Version != original.Version {
		t.Errorf("Version: got %q, want %q", decoded.Version, original.Version)
	}
	if decoded.State != original.State {
		t.Errorf("State: got %q, want %q", decoded.State, original.State)
	}
	if decoded.GOOS != original.GOOS {
		t.Errorf("GOOS: got %q, want %q", decoded.GOOS, original.GOOS)
	}
	if decoded.GOARCH != original.GOARCH {
		t.Errorf("GOARCH: got %q, want %q", decoded.GOARCH, original.GOARCH)
	}
	if decoded.ChildPID != original.ChildPID {
		t.Errorf("ChildPID: got %d, want %d", decoded.ChildPID, original.ChildPID)
	}
	if decoded.ChildUptime != original.ChildUptime {
		t.Errorf("ChildUptime: got %q, want %q", decoded.ChildUptime, original.ChildUptime)
	}
	if decoded.Degraded != original.Degraded {
		t.Errorf("Degraded: got %v, want %v", decoded.Degraded, original.Degraded)
	}
}

// TestReporter_ReportsProtocol verifies ReporterConfig.Protocol is stamped
// into every report, and that unset means 0 (legacy).
func TestReporter_ReportsProtocol(t *testing.T) {
	ctx := context.Background()
	kv := setupStatusTest(t)

	reporter := NewReporter(
		ReporterConfig{KV: kv, ID: "web-03", Component: "peel", Protocol: 7},
		func() string { return "v1.0.0" },
		func() string { return "running" },
		func() int { return 0 },
		func() time.Duration { return 0 },
	)
	reporter.report(ctx)

	got, err := GetNodeStatus(ctx, kv, "peel", "web-03")
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != 7 {
		t.Errorf("Protocol: got %d, want 7", got.Protocol)
	}

	legacy := NewReporter(
		ReporterConfig{KV: kv, ID: "web-04", Component: "peel"},
		func() string { return "v1.0.0" },
		func() string { return "running" },
		func() int { return 0 },
		func() time.Duration { return 0 },
	)
	legacy.report(ctx)

	got, err = GetNodeStatus(ctx, kv, "peel", "web-04")
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != 0 {
		t.Errorf("legacy Protocol: got %d, want 0", got.Protocol)
	}
}

func TestNodeStatus_MsgpackRoundTrip_DegradedFalseOmitted(t *testing.T) {
	original := NodeStatus{ID: "web-01", Component: "peel"}

	data, err := bus.Encode(&original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded NodeStatus
	if err := bus.Decode(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Degraded {
		t.Error("Degraded: got true, want false (omitempty zero value)")
	}
}
