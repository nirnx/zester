package masterd

import (
	"context"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
)

func newAdminTestDaemon(t *testing.T) (*Daemon, *bustest.FakePubSub, *enroll.Store) {
	t.Helper()
	ctx := context.Background()

	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	store, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new enroll store: %v", err)
	}

	d := &Daemon{
		logger:      discardLogger(),
		masterID:    "master-test",
		runCtx:      ctx,
		enrollStore: store,
	}

	ps := bustest.NewFakePubSub()
	stop, err := d.startAdminService(ps)
	if err != nil {
		t.Fatalf("start admin service: %v", err)
	}
	t.Cleanup(stop)
	return d, ps, store
}

func createEnrollment(t *testing.T, store *enroll.Store, id, peelID string, state enroll.State) *enroll.Record {
	t.Helper()
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             id,
		PeelID:         peelID,
		PublicKey:      "UABC",
		CurvePublicKey: "XABC",
		State:          state,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("create enrollment %s: %v", id, err)
	}
	return rec
}

func adminRoundTrip(t *testing.T, ps *bustest.FakePubSub, subject string, req enroll.AdminRequest) enroll.AdminResponse {
	t.Helper()
	data, err := bus.Encode(&req)
	if err != nil {
		t.Fatalf("encode admin request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg, err := ps.Request(ctx, subject, data)
	if err != nil {
		t.Fatalf("admin request on %s: %v", subject, err)
	}
	var resp enroll.AdminResponse
	if err := bus.Decode(msg.Data, &resp); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	return resp
}

func TestAdminServiceApprove(t *testing.T) {
	_, ps, store := newAdminTestDaemon(t)
	createEnrollment(t, store, "enr-1", "web-01", enroll.StatePending)

	resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollApprove, enroll.AdminRequest{ID: "enr-1", Operator: "alice"})
	if resp.Err != "" {
		t.Fatalf("unexpected error: %s", resp.Err)
	}
	if resp.Record == nil || resp.Record.State != enroll.StateApproved {
		t.Fatalf("expected approved record, got %+v", resp.Record)
	}
	if resp.Record.DecidedBy != "alice" {
		t.Fatalf("expected decided_by alice, got %q", resp.Record.DecidedBy)
	}

	rec, err := store.Get(context.Background(), "enr-1")
	if err != nil {
		t.Fatalf("get record: %v", err)
	}
	if rec.State != enroll.StateApproved || rec.DecidedBy != "alice" {
		t.Fatalf("persisted record mismatch: %+v", rec)
	}
}

func TestAdminServiceRejectWithReason(t *testing.T) {
	_, ps, store := newAdminTestDaemon(t)
	createEnrollment(t, store, "enr-2", "web-02", enroll.StatePending)

	resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollReject,
		enroll.AdminRequest{ID: "enr-2", Operator: "bob", Reason: "untrusted host"})
	if resp.Err != "" {
		t.Fatalf("unexpected error: %s", resp.Err)
	}
	if resp.Record == nil || resp.Record.State != enroll.StateRejected {
		t.Fatalf("expected rejected record, got %+v", resp.Record)
	}
	if resp.Record.RejectReason != "untrusted host" || resp.Record.DecidedBy != "bob" {
		t.Fatalf("reason/operator not recorded: %+v", resp.Record)
	}
}

func TestAdminServiceRevoke(t *testing.T) {
	_, ps, store := newAdminTestDaemon(t)
	createEnrollment(t, store, "enr-3", "web-03", enroll.StateActive)

	resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollRevoke,
		enroll.AdminRequest{ID: "enr-3", Operator: "carol", Reason: "decommissioned"})
	if resp.Err != "" {
		t.Fatalf("unexpected error: %s", resp.Err)
	}
	if resp.Record == nil || resp.Record.State != enroll.StateRevoked {
		t.Fatalf("expected revoked record, got %+v", resp.Record)
	}
	if resp.Record.RejectReason != "decommissioned" {
		t.Fatalf("reason not recorded: %+v", resp.Record)
	}
}

func TestAdminServiceErrorReplies(t *testing.T) {
	_, ps, store := newAdminTestDaemon(t)
	createEnrollment(t, store, "enr-4", "web-04", enroll.StateActive)

	t.Run("unknown id", func(t *testing.T) {
		resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollApprove,
			enroll.AdminRequest{ID: "enr-missing", Operator: "alice"})
		if resp.Err == "" {
			t.Fatal("expected error reply for unknown enrollment")
		}
		if resp.Record != nil {
			t.Fatalf("error reply must not carry a record: %+v", resp.Record)
		}
	})

	t.Run("invalid transition", func(t *testing.T) {
		resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollApprove,
			enroll.AdminRequest{ID: "enr-4", Operator: "alice"})
		if resp.Err == "" || resp.AsError() == nil {
			t.Fatal("expected error reply for active->approved transition")
		}
	})

	t.Run("missing operator", func(t *testing.T) {
		resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollRevoke,
			enroll.AdminRequest{ID: "enr-4"})
		if resp.Err == "" {
			t.Fatal("expected validation error for missing operator")
		}
	})

	t.Run("malformed payload", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		msg, err := ps.Request(ctx, bus.SubjectAdminEnrollReject, []byte{0xc1}) // invalid msgpack
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		var resp enroll.AdminResponse
		if err := bus.Decode(msg.Data, &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Err == "" {
			t.Fatal("expected decode error reply")
		}
	})
}
