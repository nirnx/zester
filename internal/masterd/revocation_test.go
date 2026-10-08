package masterd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/internal/metrics"
	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
)

// fakePusher captures pushed account JWTs and replies like a nats-server.
type fakePusher struct {
	mu      sync.Mutex
	pushed  []string
	subject string
	reply   []byte
	err     error
}

func (f *fakePusher) push(_ context.Context, subject string, accountJWT []byte) ([][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subject = subject
	f.pushed = append(f.pushed, string(accountJWT))
	if f.err != nil {
		return nil, f.err
	}
	if f.reply != nil {
		return [][]byte{f.reply}, nil
	}
	// A 3-node cluster: one server applies, two have no client of the
	// account loaded and skip.
	return [][]byte{
		[]byte(`{"server":{"name":"n1"},"data":{"account":"A","code":200,"message":"jwt updated"}}`),
		[]byte(`{"server":{"name":"n2"},"data":{"account":"A","code":200,"message":"jwt update skipped"}}`),
		[]byte(`{"server":{"name":"n3"},"data":{"account":"A","code":200,"message":"jwt update skipped"}}`),
	}, nil
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushed)
}

func (f *fakePusher) last(t *testing.T) *jwt.AccountClaims {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushed) == 0 {
		t.Fatal("nothing pushed")
	}
	ac, err := jwt.DecodeAccountClaims(f.pushed[len(f.pushed)-1])
	if err != nil {
		t.Fatalf("decode pushed account JWT: %v", err)
	}
	return ac
}

// newRevocationFixture bootstraps a real hierarchy into a temp auth dir and a
// fake-JS enrollment store, returning an enabled syncer with a fake pusher.
func newRevocationFixture(t *testing.T) (*revocationSyncer, *enroll.Store, *bustest.FakeJS, *fakePusher, *auth.Hierarchy) {
	t.Helper()
	ctx := context.Background()
	h, err := auth.GenerateHierarchy(auth.HierarchyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(t.TempDir(), "auth")
	if err := h.WriteFiles(authDir); err != nil {
		t.Fatal(err)
	}
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	store, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	s := newRevocationSyncer(discardLogger(), store, js, authDir, h.Account.PublicKey, 0)
	if !s.enabled() {
		t.Fatalf("syncer unexpectedly disabled: %s", s.disabledReason)
	}
	fp := &fakePusher{}
	s.push = fp.push
	return s, store, js, fp, h
}

func newUserRecord(t *testing.T, store *enroll.Store, id, peelID string, state enroll.State, updated time.Time) *enroll.Record {
	t.Helper()
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	rec := &enroll.Record{
		ID: id, PeelID: peelID, PublicKey: kb.PublicKey, CurvePublicKey: "XABC",
		State: state, CreatedAt: updated, UpdatedAt: updated,
	}
	if err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	return rec
}

func TestNewRevocationSyncer_DisabledWithoutMaterial(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	store, _ := enroll.NewStore(ctx, enroll.StoreConfig{JS: js, Logger: discardLogger()})

	s := newRevocationSyncer(discardLogger(), store, js, t.TempDir(), "ACCT", 0)
	if s.enabled() {
		t.Fatal("expected disabled syncer")
	}
	if !strings.Contains(s.disabledReason, auth.FileAccountJWT) {
		t.Errorf("reason should name the missing file: %q", s.disabledReason)
	}
	if s.interval != defaultRevocationSyncInterval {
		t.Errorf("interval default = %s", s.interval)
	}
	err := s.Sync(ctx, "test", true)
	if !errors.Is(err, errRevocationDisabled) {
		t.Fatalf("Sync on a disabled syncer: %v", err)
	}
	if res := s.check(ctx); res.Status != health.StatusDegraded {
		t.Errorf("disabled check = %+v, want degraded", res)
	}
	// Soft revocation still works without the push material.
	rec := newUserRecord(t, store, "enr-1", "web-01", enroll.StateActive, time.Now())
	if _, err := store.Revoke(ctx, rec.ID, "op", "gone"); err != nil {
		t.Fatal(err)
	}
	warn := s.onRevoked(ctx, rec)
	if !strings.Contains(warn, "nats-auth init") {
		t.Errorf("disabled revoke must tell the operator how to enable the push: %q", warn)
	}
	if !s.IsRevoked("web-01") {
		t.Error("peel must be soft-revoked")
	}
}

func TestRevocationSyncer_SyncPushesRevokedKeysOnly(t *testing.T) {
	s, store, _, fp, h := newRevocationFixture(t)
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	active := newUserRecord(t, store, "enr-a", "web-01", enroll.StateActive, t0)
	revoked := newUserRecord(t, store, "enr-b", "web-02", enroll.StateActive, t0)
	if _, err := store.Revoke(ctx, revoked.ID, "op", "compromised"); err != nil {
		t.Fatal(err)
	}
	pending := newUserRecord(t, store, "enr-c", "db-01", enroll.StatePending, t0)
	// Fixture-style record with a non-nkey public key must never reach the JWT.
	bad := &enroll.Record{ID: "enr-d", PeelID: "db-02", PublicKey: "UABC", CurvePublicKey: "X",
		State: enroll.StateIssued, CreatedAt: t0, UpdatedAt: t0}
	if err := store.Create(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Revoke(ctx, bad.ID, "op", "x"); err != nil {
		t.Fatal(err)
	}

	if err := s.Sync(ctx, "test", false); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if fp.count() != 1 {
		t.Fatalf("pushes = %d, want 1", fp.count())
	}
	if fp.subject != auth.AccountClaimsUpdateSubject(h.Account.PublicKey) {
		t.Errorf("push subject = %q", fp.subject)
	}
	ac := fp.last(t)
	if ac.Issuer != h.OperatorSigning.PublicKey {
		t.Errorf("pushed account JWT must be signed by the operator signing key")
	}
	if len(ac.Revocations) != 1 {
		t.Fatalf("revocations = %v, want exactly the revoked peel's key", ac.Revocations)
	}
	if !ac.Revocations.IsRevoked(revoked.PublicKey, t0) {
		t.Errorf("revoked peel's JWT (issued at %s) must be revoked", t0)
	}
	if ac.Revocations.IsRevoked(active.PublicKey, t0) || ac.Revocations.IsRevoked(pending.PublicKey, t0) {
		t.Errorf("active/pending keys must not be revoked")
	}
	// A JWT the re-enrolled identity gets LATER stays valid.
	if ac.Revocations.IsRevoked(revoked.PublicKey, time.Now().Add(time.Hour)) {
		t.Errorf("revocation must be timestamped, not permanent for the key")
	}

	// Soft-revoke cache.
	if !s.IsRevoked("web-02") || s.IsRevoked("web-01") || s.IsRevoked("db-01") {
		t.Errorf("soft-revoke cache wrong: web-02=%v web-01=%v db-01=%v",
			s.IsRevoked("web-02"), s.IsRevoked("web-01"), s.IsRevoked("db-01"))
	}
	if res := s.check(ctx); res.Status != health.StatusOK || !strings.Contains(res.Message, "applied on 1 server(s), skipped on 2") {
		t.Errorf("check after successful push = %+v", res)
	}

	// Unchanged state: no push unless forced (reconnect).
	if err := s.Sync(ctx, "periodic", false); err != nil {
		t.Fatal(err)
	}
	if fp.count() != 1 {
		t.Errorf("unchanged state must not re-push, got %d pushes", fp.count())
	}
	if err := s.Sync(ctx, "reconnect", true); err != nil {
		t.Fatal(err)
	}
	if fp.count() != 2 {
		t.Errorf("forced sync must re-push, got %d pushes", fp.count())
	}

	// Revoking another peel changes the set → push.
	if _, err := store.Revoke(ctx, active.ID, "op", "decommissioned"); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx, "periodic", false); err != nil {
		t.Fatal(err)
	}
	if fp.count() != 3 || len(fp.last(t).Revocations) != 2 {
		t.Errorf("pushes=%d revocations=%v", fp.count(), fp.last(t).Revocations)
	}
}

func TestRevocationSyncer_ReenrolledPeelIsNotSoftRevoked(t *testing.T) {
	s, store, _, _, _ := newRevocationFixture(t)
	ctx := context.Background()
	t0 := time.Now().Add(-2 * time.Hour)

	old := newUserRecord(t, store, "enr-old", "web-01", enroll.StateActive, t0)
	if _, err := store.Revoke(ctx, old.ID, "op", "lost laptop"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseIndex(ctx, "web-01"); err != nil {
		t.Fatal(err)
	}
	// Re-enrollment under the same peel ID with a new key, later.
	fresh := newUserRecord(t, store, "enr-new", "web-01", enroll.StatePending, time.Now())
	if _, err := store.Approve(ctx, fresh.ID, "op"); err != nil {
		t.Fatal(err)
	}

	if err := s.Sync(ctx, "test", false); err != nil {
		t.Fatal(err)
	}
	if s.IsRevoked("web-01") {
		t.Error("a re-enrolled peel (latest record approved) must not be soft-revoked")
	}
}

func TestRevocationSyncer_OnRevokedPurgesAndPushes(t *testing.T) {
	s, store, js, fp, _ := newRevocationFixture(t)
	ctx := context.Background()
	rec := newUserRecord(t, store, "enr-1", "web-01", enroll.StateActive, time.Now())

	put := func(bucket, key string) {
		kv, err := bus.GetBucket(ctx, js, bucket)
		if err != nil {
			t.Fatalf("bucket %s: %v", bucket, err)
		}
		if _, err := kv.Put(ctx, key, []byte("x")); err != nil {
			t.Fatalf("put %s/%s: %v", bucket, key, err)
		}
	}
	put(bus.BucketFacts, "web-01")
	put(bus.BucketFacts, "web-02")
	put(bus.BucketSecrets, "web-01")
	put(bus.BucketPeelHeartbeat, "web-01")
	put(bus.BucketBasket, "web-01.network")
	put(bus.BucketBasket, "web-01.disk")
	put(bus.BucketBasket, "web-02.network")
	put(bus.BucketUpdateStatus, "peel.web-01")

	if _, err := store.Revoke(ctx, rec.ID, "op", "bye"); err != nil {
		t.Fatal(err)
	}
	if warn := s.onRevoked(ctx, rec); warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	if fp.count() != 1 {
		t.Fatalf("pushes = %d", fp.count())
	}
	if !s.IsRevoked("web-01") {
		t.Error("soft-revoke cache must include the peel immediately")
	}

	gone := func(bucket, key string) {
		kv, _ := bus.GetBucket(ctx, js, bucket)
		if _, err := kv.Get(ctx, key); !errors.Is(err, bus.ErrKeyNotFound) {
			t.Errorf("%s/%s should be purged, got err=%v", bucket, key, err)
		}
	}
	kept := func(bucket, key string) {
		kv, _ := bus.GetBucket(ctx, js, bucket)
		if _, err := kv.Get(ctx, key); err != nil {
			t.Errorf("%s/%s should be kept: %v", bucket, key, err)
		}
	}
	gone(bus.BucketFacts, "web-01")
	gone(bus.BucketSecrets, "web-01")
	gone(bus.BucketPeelHeartbeat, "web-01")
	gone(bus.BucketBasket, "web-01.network")
	gone(bus.BucketBasket, "web-01.disk")
	gone(bus.BucketUpdateStatus, "peel.web-01")
	kept(bus.BucketFacts, "web-02")
	kept(bus.BucketBasket, "web-02.network")
}

func TestRevocationSyncer_PushFailureIsWarnedAndDegraded(t *testing.T) {
	s, store, _, fp, _ := newRevocationFixture(t)
	ctx := context.Background()
	rec := newUserRecord(t, store, "enr-1", "web-01", enroll.StateActive, time.Now())
	if _, err := store.Revoke(ctx, rec.ID, "op", "bye"); err != nil {
		t.Fatal(err)
	}

	fp.err = errors.New("nats: no responders")
	warn := s.onRevoked(ctx, rec)
	if !strings.Contains(warn, "not applied yet") || !strings.Contains(warn, "no responders") {
		t.Errorf("warning should explain the failed push: %q", warn)
	}
	if res := s.check(ctx); res.Status != health.StatusDegraded || !strings.Contains(res.Message, "no responders") {
		t.Errorf("check = %+v, want degraded with the push error", res)
	}
	if !s.IsRevoked("web-01") {
		t.Error("soft revocation must hold even when the push fails")
	}

	// Server-side rejection surfaces too.
	fp.err = nil
	fp.reply = []byte(`{"server":{},"error":{"code":500,"description":"jwt update resulted in error: bad issuer"}}`)
	if err := s.Sync(ctx, "retry", true); err == nil || !strings.Contains(err.Error(), "bad issuer") {
		t.Errorf("server rejection must surface: %v", err)
	}

	// Recovery: the next successful push clears the degraded state.
	fp.reply = nil
	if err := s.Sync(ctx, "retry", true); err != nil {
		t.Fatal(err)
	}
	if res := s.check(ctx); res.Status != health.StatusOK {
		t.Errorf("check after recovery = %+v", res)
	}
}

func TestAdminRevoke_ResponseCarriesRevocationWarning(t *testing.T) {
	d, ps, store := newAdminTestDaemon(t)
	s, _, _, fp, _ := newRevocationFixture(t)
	// Point the syncer at the admin daemon's store so both see the record.
	s.store = store
	d.revocation = s

	rec := newUserRecord(t, store, "enr-1", "web-01", enroll.StateActive, time.Now())
	resp := adminRoundTrip(t, ps, bus.SubjectAdminEnrollRevoke, enroll.AdminRequest{ID: rec.ID, Operator: "alice", Reason: "bye"})
	if resp.Err != "" {
		t.Fatalf("revoke failed: %s", resp.Err)
	}
	if resp.Record == nil || resp.Record.State != enroll.StateRevoked {
		t.Fatalf("record = %+v", resp.Record)
	}
	if resp.Warning != "" {
		t.Errorf("successful push must not warn: %q", resp.Warning)
	}
	if fp.count() != 1 {
		t.Errorf("revoke must push the account JWT once, got %d", fp.count())
	}

	// Without a syncer the response says so.
	d.revocation = nil
	rec2 := newUserRecord(t, store, "enr-2", "web-02", enroll.StateActive, time.Now())
	resp2 := adminRoundTrip(t, ps, bus.SubjectAdminEnrollRevoke, enroll.AdminRequest{ID: rec2.ID, Operator: "alice"})
	if resp2.Err != "" || !strings.Contains(resp2.Warning, "not running") {
		t.Errorf("resp2 = err=%q warning=%q", resp2.Err, resp2.Warning)
	}
}

func TestHandleFactsUpdate_RevokedPeelIsPurgedNotServed(t *testing.T) {
	s, store, js, _, _ := newRevocationFixture(t)
	ctx := context.Background()
	d := &Daemon{logger: discardLogger(), runCtx: ctx, enrollStore: store, revocation: s}
	d.reg = metrics.NewMasterRegistry()

	rec := newUserRecord(t, store, "enr-1", "web-01", enroll.StateActive, time.Now())
	if _, err := store.Revoke(ctx, rec.ID, "op", "bye"); err != nil {
		t.Fatal(err)
	}
	kv, _ := bus.GetBucket(ctx, js, bus.BucketFacts)
	if _, err := kv.Put(ctx, "web-01", []byte("x")); err != nil {
		t.Fatal(err)
	}

	d.handleFactsUpdate("web-01", map[string]any{"_curve_public_key": "XK", "role": "db"})

	if _, err := kv.Get(ctx, "web-01"); !errors.Is(err, bus.ErrKeyNotFound) {
		t.Errorf("facts of a revoked peel must be purged, got %v", err)
	}
}

func TestTallyClaimsReplies(t *testing.T) {
	ok := []byte(`{"server":{"name":"a"},"data":{"account":"A","code":200,"message":"jwt updated"}}`)
	skip := []byte(`{"server":{"name":"b"},"data":{"account":"A","code":200,"message":"jwt update skipped"}}`)
	bad := []byte(`{"server":{"name":"c"},"error":{"code":500,"description":"jwt update resulted in error: bad issuer"}}`)

	applied, skipped, err := tallyClaimsReplies([][]byte{skip, ok})
	if err != nil || len(applied) != 1 || applied[0] != "a" || len(skipped) != 1 || skipped[0] != "b" {
		t.Errorf("applied=%v skipped=%v err=%v", applied, skipped, err)
	}
	if _, _, err := tallyClaimsReplies(nil); err == nil {
		t.Error("no replies must be an error")
	}
	if _, _, err := tallyClaimsReplies([][]byte{ok, bad}); err == nil || !strings.Contains(err.Error(), "bad issuer") {
		t.Errorf("a rejecting server must fail the push: %v", err)
	}
	// All skipped: nothing to revoke live anywhere — not an error.
	if _, skipped, err := tallyClaimsReplies([][]byte{skip}); err != nil || len(skipped) != 1 {
		t.Errorf("all-skipped: skipped=%v err=%v", skipped, err)
	}
}

func TestRevocationSyncer_AccountConnectRepushesOnlyWhenRevoked(t *testing.T) {
	s, store, _, fp, _ := newRevocationFixture(t)
	ctx := context.Background()

	// Nothing revoked and nothing pushed yet: a connect event is a no-op.
	s.onAccountConnect(ctx)
	time.Sleep(revocationConnectDebounce + 300*time.Millisecond)
	if fp.count() != 0 {
		t.Fatalf("connect event must not push before anything is revoked, got %d", fp.count())
	}

	rec := newUserRecord(t, store, "enr-1", "web-01", enroll.StateActive, time.Now())
	if _, err := store.Revoke(ctx, rec.ID, "op", "bye"); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(ctx, "revoke", true); err != nil {
		t.Fatal(err)
	}
	if fp.count() != 1 {
		t.Fatalf("pushes = %d", fp.count())
	}

	// A burst of connect events coalesces into ONE forced re-push.
	for i := 0; i < 5; i++ {
		s.onAccountConnect(ctx)
	}
	time.Sleep(revocationConnectDebounce + 500*time.Millisecond)
	if fp.count() != 2 {
		t.Errorf("connect burst must re-push exactly once, got %d pushes", fp.count())
	}
}
