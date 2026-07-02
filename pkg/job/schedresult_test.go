package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

func TestHandleScheduledResultCreatesJobAndReturns(t *testing.T) {
	ps, js := testSetup(t)
	ctx := context.Background()

	res := ScheduledResult{
		JID:        NewJID(),
		Entry:      "nightly-highstate",
		Module:     "state.highstate",
		Args:       map[string]any{"test": true},
		Success:    true,
		ReturnData: "ok",
		Duration:   2 * time.Second,
		Timestamp:  time.Now().UTC(),
	}

	if err := HandleScheduledResult(ctx, js, "peel-01", res, nil); err != nil {
		t.Fatalf("HandleScheduledResult: %v", err)
	}

	// Synthetic job record created.
	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	var j Job
	if err := bus.KVGet(ctx, jobsBucket, res.JID, &j); err != nil {
		t.Fatalf("job record not created: %v", err)
	}
	if j.Function != "state.highstate" {
		t.Errorf("Function = %q, want state.highstate", j.Function)
	}
	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}
	if len(j.Targets) != 1 || j.Targets[0] != "peel-01" {
		t.Errorf("Targets = %v, want [peel-01]", j.Targets)
	}
	if j.Metadata["source"] != "schedule" || j.Metadata["schedule"] != "nightly-highstate" {
		t.Errorf("Metadata = %v, want source=schedule schedule=nightly-highstate", j.Metadata)
	}

	// The return is persisted ONLY under its per-peel key — the bare-JID
	// aggregate format does not exist.
	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var perPeel Return
	if err := bus.KVGet(ctx, returnsBucket, res.JID+".peel-01", &perPeel); err != nil {
		t.Fatalf("per-peel return not persisted: %v", err)
	}
	if perPeel.PeelID != "peel-01" || !perPeel.Success {
		t.Errorf("per-peel return = %+v, want PeelID=peel-01 Success=true", perPeel)
	}

	var agg []Return
	if err := bus.KVGet(ctx, returnsBucket, res.JID, &agg); err == nil {
		t.Errorf("scheduled result must not write the bare-JID key, got %+v", agg)
	}

	// The manager read path (used by `zester job show` and the REST API)
	// surfaces the scheduled return from the per-peel key.
	mgr := NewManager(ps, js, "test-master", nil)
	returns, err := mgr.GetReturns(ctx, res.JID)
	if err != nil {
		t.Fatalf("GetReturns: %v", err)
	}
	if len(returns) != 1 || returns[0].PeelID != "peel-01" || !returns[0].Success {
		t.Errorf("GetReturns = %+v, want single successful peel-01 return", returns)
	}
}

func TestHandleScheduledResultFailureBecomesFailedStatus(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	res := ScheduledResult{
		JID:     NewJID(),
		Entry:   "flaky-check",
		Module:  "cmd.run",
		Success: false,
		Error:   "exit status 1",
		// Timestamp deliberately zero: handler must default it.
	}

	if err := HandleScheduledResult(ctx, js, "peel-02", res, nil); err != nil {
		t.Fatalf("HandleScheduledResult: %v", err)
	}

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	var j Job
	if err := bus.KVGet(ctx, jobsBucket, res.JID, &j); err != nil {
		t.Fatalf("job record not created: %v", err)
	}
	if j.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", j.Status, StatusFailed)
	}
	if j.Created.IsZero() || j.Updated.IsZero() {
		t.Error("Created/Updated should default when payload timestamp is zero")
	}

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var perPeel Return
	if err := bus.KVGet(ctx, returnsBucket, res.JID+".peel-02", &perPeel); err != nil {
		t.Fatalf("per-peel return not persisted: %v", err)
	}
	if perPeel.Success || perPeel.Error != "exit status 1" {
		t.Errorf("per-peel return = %+v, want Success=false Error='exit status 1'", perPeel)
	}
}

// TestHandleScheduledResultIdempotent verifies that redeliveries (or a
// multi-master race) are safe: the second call succeeds and leaves exactly
// one job record and one per-peel return.
func TestHandleScheduledResultIdempotent(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	res := ScheduledResult{
		JID:       NewJID(),
		Entry:     "hourly",
		Module:    "cmd.run",
		Success:   true,
		Timestamp: time.Now().UTC(),
	}

	if err := HandleScheduledResult(ctx, js, "peel-01", res, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := HandleScheduledResult(ctx, js, "peel-01", res, nil); err != nil {
		t.Fatalf("second call must be idempotent, got: %v", err)
	}

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := bus.ListKeysWithPrefix(ctx, returnsBucket, res.JID)
	if err != nil {
		t.Fatalf("list per-peel keys: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("per-peel keys after redelivery = %d, want 1", len(keys))
	}

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	var j Job
	if err := bus.KVGet(ctx, jobsBucket, res.JID, &j); err != nil {
		t.Fatalf("job record: %v", err)
	}
	if j.Status != StatusComplete {
		t.Errorf("Status = %q, want %q", j.Status, StatusComplete)
	}
}

// TestHandleScheduledResultForcesTargetToSubjectPeel verifies the peel
// identity comes from the (permission-enforced) subject token, never from
// the payload: the payload carries no peel field, and both the job's
// Targets and the return's PeelID are forced to the passed peelID.
func TestHandleScheduledResultForcesTargetToSubjectPeel(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	res := ScheduledResult{
		JID:       NewJID(),
		Entry:     "spoof-attempt",
		Module:    "cmd.run",
		Args:      map[string]any{"peel_id": "peel-victim", "targets": []string{"peel-victim"}},
		Success:   true,
		Timestamp: time.Now().UTC(),
	}

	if err := HandleScheduledResult(ctx, js, "peel-real", res, nil); err != nil {
		t.Fatalf("HandleScheduledResult: %v", err)
	}

	jobsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatal(err)
	}
	var j Job
	if err := bus.KVGet(ctx, jobsBucket, res.JID, &j); err != nil {
		t.Fatalf("job record: %v", err)
	}
	if len(j.Targets) != 1 || j.Targets[0] != "peel-real" {
		t.Errorf("Targets = %v, want [peel-real] (subject peel, not payload)", j.Targets)
	}

	returnsBucket, err := bus.GetBucket(ctx, js, bus.BucketJobReturns)
	if err != nil {
		t.Fatal(err)
	}
	var perPeel Return
	if err := bus.KVGet(ctx, returnsBucket, res.JID+".peel-real", &perPeel); err != nil {
		t.Fatalf("per-peel return keyed by subject peel: %v", err)
	}
	if perPeel.PeelID != "peel-real" {
		t.Errorf("PeelID = %q, want peel-real", perPeel.PeelID)
	}
}

func TestHandleScheduledResultRejectsInvalid(t *testing.T) {
	_, js := testSetup(t)
	ctx := context.Background()

	cases := []struct {
		name string
		res  ScheduledResult
	}{
		{"empty jid", ScheduledResult{JID: "", Module: "cmd.run"}},
		{"jid with dot", ScheduledResult{JID: "abc.def", Module: "cmd.run"}},
		{"jid with star", ScheduledResult{JID: "abc*", Module: "cmd.run"}},
		{"jid with gt", ScheduledResult{JID: "abc>", Module: "cmd.run"}},
		{"jid with space", ScheduledResult{JID: "abc def", Module: "cmd.run"}},
		{"empty module", ScheduledResult{JID: NewJID(), Module: ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := HandleScheduledResult(ctx, js, "peel-01", tc.res, nil)
			if !errors.Is(err, ErrInvalidScheduledResult) {
				t.Errorf("err = %v, want ErrInvalidScheduledResult", err)
			}
		})
	}
}

// TestPublishScheduledResult verifies the peel-side publish helper emits a
// decodable payload on the peel-scoped schedule subject.
func TestPublishScheduledResult(t *testing.T) {
	ps, _ := testSetup(t)

	res := ScheduledResult{
		JID:       NewJID(),
		Entry:     "hourly",
		Module:    "cmd.run",
		Success:   true,
		Timestamp: time.Now().UTC(),
	}

	received := make(chan ScheduledResult, 1)
	sub, err := ps.Subscribe(bus.JobScheduleSubject(res.JID, "peel-01"), func(msg *bus.Msg) {
		var got ScheduledResult
		if err := bus.Decode(msg.Data, &got); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		received <- got
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	if err := PublishScheduledResult(ps, "peel-01", res); err != nil {
		t.Fatalf("PublishScheduledResult: %v", err)
	}

	select {
	case got := <-received:
		if got.JID != res.JID || got.Module != "cmd.run" || got.Entry != "hourly" {
			t.Errorf("received = %+v, want JID/Module/Entry to round-trip", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled result not received on schedule subject")
	}
}
