package enroll_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/segmentio/ksuid"
)

func testStoreSetup(t *testing.T) (*enroll.Store, context.Context) {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	store, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return store, ctx
}

func TestStoreCreate(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-test123",
		PeelID:         "web-01",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		Hostname:       "server.example.com",
		Metadata:       map[string]string{"os": "linux"},
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the record was created with a revision.
	if rec.Revision == 0 {
		t.Error("Create did not set Revision")
	}

	// Verify we can retrieve it.
	got, err := store.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != rec.ID {
		t.Errorf("ID = %q, want %q", got.ID, rec.ID)
	}
	if got.PeelID != rec.PeelID {
		t.Errorf("PeelID = %q, want %q", got.PeelID, rec.PeelID)
	}
	if got.PublicKey != rec.PublicKey {
		t.Errorf("PublicKey = %q, want %q", got.PublicKey, rec.PublicKey)
	}
	if got.CurvePublicKey != rec.CurvePublicKey {
		t.Errorf("CurvePublicKey = %q, want %q", got.CurvePublicKey, rec.CurvePublicKey)
	}
	if got.State != rec.State {
		t.Errorf("State = %q, want %q", got.State, rec.State)
	}
}

func TestStoreSupersede(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	old := &enroll.Record{
		ID: "enr-old", PeelID: "web-01", PublicKey: "UKEY", CurvePublicKey: "XKEY",
		State: enroll.StateActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Create(ctx, old); err != nil {
		t.Fatalf("Create old: %v", err)
	}
	// Carry the current revision, as the handler's FindByPeelID path does.
	loadedOld, err := store.FindByPeelID(ctx, "web-01")
	if err != nil {
		t.Fatalf("FindByPeelID old: %v", err)
	}

	newRec := &enroll.Record{
		ID: "enr-new", PeelID: "web-01", PublicKey: "UKEY", CurvePublicKey: "XKEY",
		State: enroll.StatePending, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Supersede(ctx, newRec, loadedOld); err != nil {
		t.Fatalf("Supersede: %v", err)
	}

	// The index now resolves to the new record.
	got, err := store.FindByPeelID(ctx, "web-01")
	if err != nil {
		t.Fatalf("FindByPeelID new: %v", err)
	}
	if got == nil || got.ID != "enr-new" {
		t.Fatalf("index not repointed to the new record: %+v", got)
	}
	if got.State != enroll.StatePending {
		t.Errorf("new record state = %q, want pending", got.State)
	}

	// The old record is retired to revoked — no lingering live/active ghost.
	oldGot, err := store.Get(ctx, "enr-old")
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if oldGot.State != enroll.StateRevoked {
		t.Errorf("old record state = %q, want revoked (superseded)", oldGot.State)
	}

	// The index was never released, so the uniqueness guard is intact: a fresh
	// Create for the same peel ID (a different key attempting to claim the
	// identity) still fails — the hijack window is closed.
	attacker := &enroll.Record{
		ID: "enr-atk", PeelID: "web-01", PublicKey: "UEVIL",
		State: enroll.StatePending, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Create(ctx, attacker); err == nil {
		t.Error("Create succeeded for an already-indexed peel ID (uniqueness guard bypassed)")
	}
}

func TestStoreCreateDuplicatePeelID(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec1 := &enroll.Record{
		ID:             "enr-first",
		PeelID:         "web-01",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec1); err != nil {
		t.Fatalf("Create first: %v", err)
	}

	// Attempt to create second enrollment with same peel ID but different enrollment ID.
	rec2 := &enroll.Record{
		ID:             "enr-second",
		PeelID:         "web-01", // Same peel ID!
		PublicKey:      "UGHI789",
		CurvePublicKey: "XJKL012",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	err := store.Create(ctx, rec2)
	if err == nil {
		t.Error("Create with duplicate peel ID should fail, got nil error")
	}
}

func TestStoreCreateDifferentPeelIDs(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec1 := &enroll.Record{
		ID:             "enr-test1",
		PeelID:         "web-01",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	rec2 := &enroll.Record{
		ID:             "enr-test2",
		PeelID:         "web-02",
		PublicKey:      "UGHI789",
		CurvePublicKey: "XJKL012",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec1); err != nil {
		t.Fatalf("Create rec1: %v", err)
	}
	if err := store.Create(ctx, rec2); err != nil {
		t.Fatalf("Create rec2: %v", err)
	}

	// Both should be retrievable.
	got1, err := store.Get(ctx, rec1.ID)
	if err != nil || got1.PeelID != "web-01" {
		t.Errorf("Get rec1 failed: %v", err)
	}

	got2, err := store.Get(ctx, rec2.ID)
	if err != nil || got2.PeelID != "web-02" {
		t.Errorf("Get rec2 failed: %v", err)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	store, ctx := testStoreSetup(t)

	_, err := store.Get(ctx, "enr-nonexistent")
	if err == nil {
		t.Error("Get with nonexistent ID should return error, got nil")
	}
}

func TestStoreFindByPeelID(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-test456",
		PeelID:         "db-01",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Find by peel ID.
	got, err := store.FindByPeelID(ctx, "db-01")
	if err != nil {
		t.Fatalf("FindByPeelID: %v", err)
	}
	if got == nil {
		t.Fatal("FindByPeelID returned nil")
	}
	if got.ID != rec.ID {
		t.Errorf("ID = %q, want %q", got.ID, rec.ID)
	}
	if got.PeelID != rec.PeelID {
		t.Errorf("PeelID = %q, want %q", got.PeelID, rec.PeelID)
	}
}

func TestStoreFindByPeelIDNotFound(t *testing.T) {
	store, ctx := testStoreSetup(t)

	got, err := store.FindByPeelID(ctx, "unknown-peel")
	if err != nil {
		t.Fatalf("FindByPeelID should not error on not found: %v", err)
	}
	if got != nil {
		t.Errorf("FindByPeelID returned %v, want nil", got)
	}
}

func TestStoreApprove(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-approve-test",
		PeelID:         "web-03",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Approve the enrollment.
	approved, err := store.Approve(ctx, rec.ID, "admin-01")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if approved.State != enroll.StateApproved {
		t.Errorf("State = %q, want %q", approved.State, enroll.StateApproved)
	}
	if approved.DecidedBy != "admin-01" {
		t.Errorf("DecidedBy = %q, want %q", approved.DecidedBy, "admin-01")
	}
	if approved.DecidedAt == nil {
		t.Error("DecidedAt is nil, want timestamp")
	}
}

func TestStoreApproveNonPending(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-already-approved",
		PeelID:         "web-04",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateApproved, // Already approved!
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attempting to approve again should fail.
	_, err := store.Approve(ctx, rec.ID, "admin-02")
	if err == nil {
		t.Error("Approve non-pending record should fail, got nil error")
	}
}

func TestStoreReject(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-reject-test",
		PeelID:         "web-05",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Reject the enrollment.
	rejected, err := store.Reject(ctx, rec.ID, "admin-01", "suspicious activity")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}

	if rejected.State != enroll.StateRejected {
		t.Errorf("State = %q, want %q", rejected.State, enroll.StateRejected)
	}
	if rejected.DecidedBy != "admin-01" {
		t.Errorf("DecidedBy = %q, want %q", rejected.DecidedBy, "admin-01")
	}
	if rejected.RejectReason != "suspicious activity" {
		t.Errorf("RejectReason = %q, want %q", rejected.RejectReason, "suspicious activity")
	}
	if rejected.DecidedAt == nil {
		t.Error("DecidedAt is nil, want timestamp")
	}
}

func TestStoreRejectNonPending(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-already-issued",
		PeelID:         "web-06",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateIssued, // Already issued!
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attempting to reject an issued record should fail.
	_, err := store.Reject(ctx, rec.ID, "admin-02", "too late")
	if err == nil {
		t.Error("Reject non-pending record should fail, got nil error")
	}
}

func TestStoreMarkIssued(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-issue-test",
		PeelID:         "web-07",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	expiresAt := now.Add(180 * 24 * time.Hour)
	issued, err := store.MarkIssued(ctx, rec.ID, expiresAt)
	if err != nil {
		t.Fatalf("MarkIssued: %v", err)
	}

	if issued.State != enroll.StateIssued {
		t.Errorf("State = %q, want %q", issued.State, enroll.StateIssued)
	}
	if issued.IssuedAt == nil {
		t.Error("IssuedAt is nil, want timestamp")
	}
	if issued.ExpiresAt == nil || !issued.ExpiresAt.Equal(expiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", issued.ExpiresAt, expiresAt)
	}
}

func TestStoreMarkIssuedNonApproved(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-pending-issue",
		PeelID:         "web-08",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending, // Not approved!
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	expiresAt := now.Add(180 * 24 * time.Hour)
	_, err := store.MarkIssued(ctx, rec.ID, expiresAt)
	if err == nil {
		t.Error("MarkIssued on pending record should fail, got nil error")
	}
}

func TestStoreMarkActive(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-active-test",
		PeelID:         "web-09",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateIssued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	active, err := store.MarkActive(ctx, rec.ID)
	if err != nil {
		t.Fatalf("MarkActive: %v", err)
	}

	if active.State != enroll.StateActive {
		t.Errorf("State = %q, want %q", active.State, enroll.StateActive)
	}
}

func TestStoreRevokeFromApproved(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-revoke-approved",
		PeelID:         "web-10",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revoked, err := store.Revoke(ctx, rec.ID, "admin-01", "policy violation")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if revoked.State != enroll.StateRevoked {
		t.Errorf("State = %q, want %q", revoked.State, enroll.StateRevoked)
	}
	if revoked.DecidedBy != "admin-01" {
		t.Errorf("DecidedBy = %q, want %q", revoked.DecidedBy, "admin-01")
	}
	if revoked.RejectReason != "policy violation" {
		t.Errorf("RejectReason = %q, want %q", revoked.RejectReason, "policy violation")
	}
}

func TestStoreRevokeFromIssued(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-revoke-issued",
		PeelID:         "web-11",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateIssued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revoked, err := store.Revoke(ctx, rec.ID, "admin-01", "compromised key")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if revoked.State != enroll.StateRevoked {
		t.Errorf("State = %q, want %q", revoked.State, enroll.StateRevoked)
	}
}

func TestStoreRevokeFromActive(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-revoke-active",
		PeelID:         "web-12",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	revoked, err := store.Revoke(ctx, rec.ID, "admin-01", "security incident")
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if revoked.State != enroll.StateRevoked {
		t.Errorf("State = %q, want %q", revoked.State, enroll.StateRevoked)
	}
}

func TestStoreRevokeFromPending(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-revoke-pending",
		PeelID:         "web-13",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := store.Revoke(ctx, rec.ID, "admin-01", "changed mind")
	if err == nil {
		t.Error("Revoke from pending should fail, got nil error")
	}
}

func TestStoreRevokeFromRejected(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-revoke-rejected",
		PeelID:         "web-14",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateRejected,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := store.Revoke(ctx, rec.ID, "admin-01", "redundant")
	if err == nil {
		t.Error("Revoke from rejected should fail, got nil error")
	}
}

func TestStoreListEmpty(t *testing.T) {
	store, ctx := testStoreSetup(t)

	records, err := store.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if records != nil {
		t.Errorf("List on empty bucket should return nil, got %d records", len(records))
	}
}

func TestStoreListAll(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	// Create multiple enrollments.
	for i := 0; i < 3; i++ {
		rec := &enroll.Record{
			ID:             "enr-list-" + ksuid.New().String(),
			PeelID:         "peel-" + string(rune('a'+i)),
			PublicKey:      "UABC" + string(rune('0'+i)),
			CurvePublicKey: "XDEF" + string(rune('0'+i)),
			State:          enroll.StatePending,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := store.Create(ctx, rec); err != nil {
			t.Fatalf("Create rec %d: %v", i, err)
		}
	}

	records, err := store.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("List returned %d records, want 3", len(records))
	}
}

func TestStoreListFilteredByState(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	// Create enrollments in different states.
	pending := &enroll.Record{
		ID:             "enr-filter-pending",
		PeelID:         "peel-pending",
		PublicKey:      "UABC1",
		CurvePublicKey: "XDEF1",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	approved := &enroll.Record{
		ID:             "enr-filter-approved",
		PeelID:         "peel-approved",
		PublicKey:      "UABC2",
		CurvePublicKey: "XDEF2",
		State:          enroll.StateApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, pending); err != nil {
		t.Fatalf("Create pending: %v", err)
	}
	if err := store.Create(ctx, approved); err != nil {
		t.Fatalf("Create approved: %v", err)
	}

	// Filter by pending state.
	pendingState := enroll.StatePending
	records, err := store.List(ctx, &pendingState)
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("List filtered returned %d records, want 1", len(records))
	}
	if len(records) > 0 && records[0].State != enroll.StatePending {
		t.Errorf("Filtered record state = %q, want %q", records[0].State, enroll.StatePending)
	}
}

func TestStoreDelete(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-delete-test",
		PeelID:         "peel-delete",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Delete the record.
	if err := store.Delete(ctx, rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify it's gone.
	_, err := store.Get(ctx, rec.ID)
	if err == nil {
		t.Error("Get after Delete should return error, got nil")
	}

	// Verify the peel index is also gone.
	found, err := store.FindByPeelID(ctx, rec.PeelID)
	if err != nil {
		t.Fatalf("FindByPeelID: %v", err)
	}
	if found != nil {
		t.Error("FindByPeelID after Delete should return nil, got record")
	}
}

func TestStoreReleaseIndex(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-release-test",
		PeelID:         "peel-release",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StateRejected,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Release the peel index to allow re-enrollment.
	if err := store.ReleaseIndex(ctx, rec.PeelID); err != nil {
		t.Fatalf("ReleaseIndex: %v", err)
	}

	// Now we should be able to create a new enrollment with the same peel ID.
	rec2 := &enroll.Record{
		ID:             "enr-release-test-2",
		PeelID:         "peel-release", // Same peel ID!
		PublicKey:      "UGHI789",
		CurvePublicKey: "XJKL012",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec2); err != nil {
		t.Fatalf("Create after ReleaseIndex: %v", err)
	}

	// Verify the new record is findable by peel ID.
	found, err := store.FindByPeelID(ctx, rec.PeelID)
	if err != nil {
		t.Fatalf("FindByPeelID: %v", err)
	}
	if found == nil {
		t.Fatal("FindByPeelID returned nil after re-enrollment")
	}
	if found.ID != rec2.ID {
		t.Errorf("FindByPeelID returned old record ID %q, want new ID %q", found.ID, rec2.ID)
	}
}

func TestStoreConcurrentApprove(t *testing.T) {
	store, ctx := testStoreSetup(t)
	now := time.Now().UTC()

	rec := &enroll.Record{
		ID:             "enr-concurrent-approve",
		PeelID:         "peel-concurrent",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Launch two concurrent approval attempts.
	var wg sync.WaitGroup
	errors := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		adminID := "admin-" + string(rune('0'+i))
		go func(admin string) {
			defer wg.Done()
			_, err := store.Approve(ctx, rec.ID, admin)
			errors <- err
		}(adminID)
	}

	wg.Wait()
	close(errors)

	// Collect results.
	var successCount, failureCount int
	for err := range errors {
		if err == nil {
			successCount++
		} else {
			failureCount++
		}
	}

	// Only one should succeed due to CAS.
	if successCount != 1 {
		t.Errorf("Concurrent approves: %d succeeded, want exactly 1", successCount)
	}
	if failureCount != 1 {
		t.Errorf("Concurrent approves: %d failed, want exactly 1", failureCount)
	}
}
