package cmd

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/job"
)

// ---------- fetchJobReturns ----------

// TestFetchJobReturns_PerPeelKeys verifies `zester job show` reads the
// per-peel "<jid>.<peelID>" keys — the only format returns are stored in —
// and that stray keys never leak into the listing.
func TestFetchJobReturns_PerPeelKeys(t *testing.T) {
	ctx := context.Background()
	kv := bustest.NewFakeKV("job-returns", 0)
	jid := "2abcJID1"

	for _, r := range []job.Return{
		{JID: jid, PeelID: "web-01", Success: true, Duration: time.Second},
		{JID: jid, PeelID: "web-02", Success: false, Error: "boom"},
	} {
		if _, err := bus.KVPut(ctx, kv, jid+"."+r.PeelID, r); err != nil {
			t.Fatalf("put per-peel return: %v", err)
		}
	}
	// Decoys: a stray value under the bare JID (nothing writes one; it must
	// not be surfaced) and another job's per-peel return (must not leak in
	// via the prefix listing).
	if _, err := bus.KVPut(ctx, kv, jid, []job.Return{{JID: jid, PeelID: "stray-bare-jid"}}); err != nil {
		t.Fatalf("put stray bare-JID value: %v", err)
	}
	if _, err := bus.KVPut(ctx, kv, "2abcJID2.web-01", job.Return{JID: "2abcJID2", PeelID: "web-01"}); err != nil {
		t.Fatalf("put other job return: %v", err)
	}

	got := fetchJobReturns(ctx, kv, jid)
	if len(got) != 2 {
		t.Fatalf("fetchJobReturns = %d entries, want 2 (per-peel keys)", len(got))
	}
	// ListKeysWithPrefix sorts keys, so ordering is deterministic.
	if p := mapStr(got[0], "peel_id"); p != "web-01" {
		t.Errorf("returns[0] peel_id = %q, want web-01", p)
	}
	if p := mapStr(got[1], "peel_id"); p != "web-02" {
		t.Errorf("returns[1] peel_id = %q, want web-02", p)
	}
	if e := mapStr(got[1], "error"); e != "boom" {
		t.Errorf("returns[1] error = %q, want boom", e)
	}
}

// TestFetchJobReturns_ScheduledResult verifies `zester job show` surfaces a
// scheduler-synthesized result: the scheduled-result consumer writes the
// same per-peel key format as the dispatch watcher.
func TestFetchJobReturns_ScheduledResult(t *testing.T) {
	ctx := context.Background()
	kv := bustest.NewFakeKV("job-returns", 0)
	jid := "2abcJID3"

	ret := job.Return{JID: jid, PeelID: "db-01", Success: true, Duration: time.Second}
	if _, err := bus.KVPut(ctx, kv, jid+"."+ret.PeelID, ret); err != nil {
		t.Fatalf("put scheduled per-peel return: %v", err)
	}

	got := fetchJobReturns(ctx, kv, jid)
	if len(got) != 1 {
		t.Fatalf("fetchJobReturns = %d entries, want 1", len(got))
	}
	if p := mapStr(got[0], "peel_id"); p != "db-01" {
		t.Errorf("peel_id = %q, want db-01", p)
	}
}

func TestFetchJobReturns_NoReturns(t *testing.T) {
	ctx := context.Background()
	kv := bustest.NewFakeKV("job-returns", 0)
	if got := fetchJobReturns(ctx, kv, "2missingJID"); len(got) != 0 {
		t.Errorf("fetchJobReturns(missing) = %v, want empty", got)
	}
}

// ---------- filterJobRecordKeys ----------

func TestFilterJobRecordKeys_DropsActiveIndexKeys(t *testing.T) {
	keys := []string{
		"2abcJID1",
		job.ActiveJobKey("2abcJID1"),
		"2abcJID2",
		"active.2abcJID3",
	}
	got := filterJobRecordKeys(keys)
	want := []string{"2abcJID1", "2abcJID2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterJobRecordKeys = %v, want %v", got, want)
	}
}

func TestFilterJobRecordKeys_Empty(t *testing.T) {
	if got := filterJobRecordKeys(nil); len(got) != 0 {
		t.Errorf("filterJobRecordKeys(nil) = %v, want empty", got)
	}
	onlyIndex := []string{job.ActiveJobKey("x"), job.ActiveJobKey("y")}
	if got := filterJobRecordKeys(onlyIndex); len(got) != 0 {
		t.Errorf("filterJobRecordKeys(index-only) = %v, want empty", got)
	}
}

// ---------- jobStatusActive ----------

func TestJobStatusActive(t *testing.T) {
	cases := map[string]bool{
		"running":   true,
		"pending":   true,
		"claimed":   true,
		"completed": false,
		"failed":    false,
		"partial":   false,
		"":          false,
		"-":         false,
	}
	for status, want := range cases {
		if got := jobStatusActive(status); got != want {
			t.Errorf("jobStatusActive(%q) = %v, want %v", status, got, want)
		}
	}
}
