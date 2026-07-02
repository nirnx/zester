package masterd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ptorbus/zester/internal/metrics"
	"github.com/ptorbus/zester/pkg/auth"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/facts"
	"github.com/ptorbus/zester/pkg/settings"
	"github.com/ptorbus/zester/pkg/statefiles"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newLeaseTestDaemon builds a Daemon with just enough wiring for the
// publisher-lease seams: a settings publisher and a statefiles publisher
// over the shared fake JS, plus per-daemon raw settings files so the tests
// can tell which daemon published.
func newLeaseTestDaemon(t *testing.T, js bus.JetStreamAPI, id, statesDir, settingsFile string) *Daemon {
	t.Helper()
	ctx := context.Background()

	filesKV, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
	if err != nil {
		t.Fatalf("get settings-files bucket: %v", err)
	}
	secretsKV, err := bus.GetBucket(ctx, js, bus.BucketSecrets)
	if err != nil {
		t.Fatalf("get secrets bucket: %v", err)
	}
	stateKV, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get state-files bucket: %v", err)
	}

	kb, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("generate key bundle: %v", err)
	}
	enc, err := auth.NewEncryptor(kb)
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	pub, err := settings.NewPublisher(settings.PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: enc,
		Logger:          discardLogger(),
	})
	if err != nil {
		t.Fatalf("new settings publisher: %v", err)
	}

	d := &Daemon{
		logger:   discardLogger(),
		masterID: id,
		js:       js,
		// Short lease timings so handoff happens within the test budget.
		leaseTTL:           150 * time.Millisecond,
		leaseRenewInterval: 50 * time.Millisecond,
	}
	d.publisher = pub
	d.rawSettingsFiles = map[string][]byte{settingsFile: []byte("greeting: hello\n")}
	d.allSecrets = extractFileSecrets(d.rawSettingsFiles, d.logger)
	d.statePublisher = statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        stateKV,
		Logger:    discardLogger(),
	})
	return d
}

func kvHasKey(t *testing.T, js bus.JetStreamAPI, bucket, key string) bool {
	t.Helper()
	ctx := context.Background()
	kv, err := bus.GetBucket(ctx, js, bucket)
	if err != nil {
		t.Fatalf("get bucket %s: %v", bucket, err)
	}
	_, err = kv.Get(ctx, key)
	return err == nil
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestPublisherLeaseGatesPublishes verifies the single-publisher invariant:
// the lease holder publishes settings and state files, the standby does
// not, and losing the lease hands publishing over to the standby.
func TestPublisherLeaseGatesPublishes(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	statesDir1 := t.TempDir()
	if err := os.WriteFile(filepath.Join(statesDir1, "web1.zy"), []byte("test-ping:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	statesDir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(statesDir2, "web2.zy"), []byte("test-ping:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	d1 := newLeaseTestDaemon(t, js, "master-1", statesDir1, "m1.zy")
	d2 := newLeaseTestDaemon(t, js, "master-2", statesDir2, "m2.zy")

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	if err := d1.startPublisherLease(ctx1); err != nil {
		t.Fatalf("start publisher lease d1: %v", err)
	}
	// d1 acquires immediately (single candidate) and publishes.
	waitFor(t, 2*time.Second, "leader publish from master-1", func() bool {
		return kvHasKey(t, js, bus.BucketSettingsFiles, "m1.zy") &&
			kvHasKey(t, js, bus.BucketStateFiles, "web1.zy")
	})

	// d2 starts as standby: it must NOT publish while d1 holds the lease.
	if err := d2.startPublisherLease(ctx2); err != nil {
		t.Fatalf("start publisher lease d2: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // several renew intervals
	if kvHasKey(t, js, bus.BucketSettingsFiles, "m2.zy") {
		t.Fatal("standby master published settings files while not holding the lease")
	}
	if kvHasKey(t, js, bus.BucketStateFiles, "web2.zy") {
		t.Fatal("standby master published state files while not holding the lease")
	}

	// Stopping the leader releases the lease; the standby acquires it and
	// starts publishing.
	cancel1()
	waitFor(t, 5*time.Second, "standby takeover publish from master-2", func() bool {
		return kvHasKey(t, js, bus.BucketSettingsFiles, "m2.zy") &&
			kvHasKey(t, js, bus.BucketStateFiles, "web2.zy")
	})
}

// TestHandleFactsUpdateSecretsGate verifies that only the facts-secrets
// lease holder publishes per-peel secrets from the facts watcher callback,
// while the enrollment lookup path stays ungated.
func TestHandleFactsUpdateSecretsGate(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	store, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("new enroll store: %v", err)
	}

	d := newLeaseTestDaemon(t, js, "master-1", t.TempDir(), "app.zy")
	d.runCtx = ctx
	d.reg = metrics.NewMasterRegistry()
	d.enrollStore = store
	d.allSecrets = settings.FileSecrets{"app.zy": {"app.token": "hunter2"}}
	topFile, err := settings.ParseTopFile([]byte("base:\n  '*':\n    - app\n"))
	if err != nil {
		t.Fatalf("parse top file: %v", err)
	}
	d.topFile = topFile

	// The peel's curve public key must be a real X25519 key so the seal
	// operation succeeds when the leader path runs.
	peelKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("generate peel key bundle: %v", err)
	}
	curvePub, err := peelKB.CurvePublicKey()
	if err != nil {
		t.Fatalf("peel curve public key: %v", err)
	}
	peelFacts := facts.Facts{"_curve_public_key": curvePub, "os": map[string]any{"name": "linux"}}

	// Not the lease holder: a constructed-but-never-run lease reports
	// IsLeader() == false, so no secrets may be published.
	notLeader, err := bus.NewLeaderLease(bus.LeaderLeaseConfig{
		JS: js, Key: "facts-secrets", HolderID: "master-1", Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("new lease: %v", err)
	}
	d.secretsLease = notLeader
	d.handleFactsUpdate("web-01", peelFacts)
	if kvHasKey(t, js, bus.BucketSecrets, "web-01") {
		t.Fatal("non-leader master published per-peel secrets")
	}

	// Acquire the lease for real: secrets are published.
	leaseCtx, cancelLease := context.WithCancel(context.Background())
	defer cancelLease()
	go d.runLease(leaseCtx, notLeader, "facts-secrets")
	waitFor(t, 2*time.Second, "facts-secrets lease acquisition", notLeader.IsLeader)

	d.handleFactsUpdate("web-01", peelFacts)
	if !kvHasKey(t, js, bus.BucketSecrets, "web-01") {
		t.Fatal("lease holder did not publish per-peel secrets")
	}
}

// TestSecretsLeaderNilLease documents the unit-test escape hatch: without a
// lease the gate is open.
func TestSecretsLeaderNilLease(t *testing.T) {
	d := &Daemon{}
	if !d.secretsLeader() {
		t.Fatal("nil secrets lease must leave the gate open")
	}
}
