package masterd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/internal/metrics"
	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/facts"
	"github.com/nirnx/zester/pkg/settings"
	"github.com/nirnx/zester/pkg/statefiles"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sharedTestEncryptor mimics production account.seed sharing: multi-daemon
// tests pass ONE encryptor to every daemon so the sealed master-settings
// replica is openable across them.
func sharedTestEncryptor(t *testing.T) *auth.Encryptor {
	t.Helper()
	kb, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("generate key bundle: %v", err)
	}
	enc, err := auth.NewEncryptor(kb)
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}
	return enc
}

// newLeaseTestDaemon builds a Daemon with just enough wiring for the
// publisher-lease seams: a settings publisher, a statefiles publisher, and
// the sealed master-settings publisher over the shared fake JS, plus
// per-daemon raw settings files so the tests can tell which daemon
// published. enc nil generates a fresh (per-daemon) encryptor; multi-daemon
// tests must pass sharedTestEncryptor's result to every daemon.
func newLeaseTestDaemon(t *testing.T, js bus.JetStreamAPI, id, statesDir, settingsFile string, enc *auth.Encryptor) *Daemon {
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
	masterSettingsKV, err := bus.GetBucket(ctx, js, bus.BucketMasterSettings)
	if err != nil {
		t.Fatalf("get master-settings bucket: %v", err)
	}

	if enc == nil {
		enc = sharedTestEncryptor(t)
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

	// publishSettingsFiles re-reads the on-disk tree (that is what makes
	// edits publishable without a restart), so the test's settings file
	// lives in a real dir wired through cfg. The fsnotify watcher is off
	// (determinism); a fast republish ticker stands in for it. Reactor is
	// dirless — the reactor mirror stays unconfigured.
	settingsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(settingsDir, settingsFile), []byte("greeting: hello\n"), 0o644); err != nil {
		t.Fatalf("write settings file: %v", err)
	}
	cfg := config.MasterDaemonDefaults()
	cfg.SettingsDir = settingsDir
	cfg.StatesDir = statesDir
	cfg.Reactor.Dir = ""
	cfg.FilesWatch = false
	cfg.FilesRepublishInterval = config.Duration(50 * time.Millisecond)
	// Never write the real /run/zester/publisher-status from a unit test;
	// each daemon gets its own tempdir path.
	cfg.PublisherStatusFile = filepath.Join(t.TempDir(), "publisher-status")

	d := &Daemon{
		cfg:      &cfg,
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
	d.masterEnc = enc
	d.masterSettingsPublisher = statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: settingsDir,
		KV:        masterSettingsKV,
		Logger:    discardLogger(),
		EncodeValue: func(_ string, plaintext []byte) ([]byte, error) {
			return enc.Seal(plaintext, enc.PublicKey())
		},
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

// TestPublisherLeaseGatesPublishes verifies the single-publisher invariant
// AND the mirror-backed takeover: the lease holder publishes settings and
// state files, the standby does not (its dirs instead converge to published
// truth via the KV mirror), and a failover publishes NOTHING — the fleet
// keeps the previous holder's tree, never reverting to the standby's stale
// files. New edits on the new holder then publish normally.
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

	// One shared encryptor = production account.seed sharing; the sealed
	// settings replica must be openable by both masters.
	enc := sharedTestEncryptor(t)
	d1 := newLeaseTestDaemon(t, js, "master-1", statesDir1, "m1.zy", enc)
	d2 := newLeaseTestDaemon(t, js, "master-2", statesDir2, "m2.zy", enc)

	// d1 also carries an !encrypted secret — the sealed master-settings
	// replica must round-trip its PLAINTEXT to the standby's dir while the
	// buckets never store it in the clear.
	const secretPlain = "hunter2-lease-test"
	if err := os.WriteFile(filepath.Join(d1.cfg.SettingsDir, "sec.zy"),
		[]byte("db_password: !encrypted \""+secretPlain+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	// Its standby mirror (armed after ~2 lease TTLs) instead pulls the
	// PUBLISHED tree into its own dirs.
	if err := d2.startPublisherLease(ctx2); err != nil {
		t.Fatalf("start publisher lease d2: %v", err)
	}
	waitFor(t, 5*time.Second, "standby mirror convergence on master-2", func() bool {
		if _, err := os.Stat(filepath.Join(statesDir2, "web1.zy")); err != nil {
			return false
		}
		_, err := os.Stat(filepath.Join(d2.cfg.SettingsDir, "sec.zy"))
		return err == nil
	})
	if kvHasKey(t, js, bus.BucketSettingsFiles, "m2.zy") {
		t.Fatal("standby master published settings files while not holding the lease")
	}
	if kvHasKey(t, js, bus.BucketStateFiles, "web2.zy") {
		t.Fatal("standby master published state files while not holding the lease")
	}
	// The mirror pruned the standby's stale local file (it is not part of
	// published truth).
	if _, err := os.Stat(filepath.Join(statesDir2, "web2.zy")); !os.IsNotExist(err) {
		t.Error("standby's stale local state file survived the mirror sync")
	}

	// SEALED SETTINGS: the standby's dir now holds d1's raw tree — secret
	// plaintext included — while neither bucket ever stored it in the clear.
	secOnStandby, err := os.ReadFile(filepath.Join(d2.cfg.SettingsDir, "sec.zy"))
	if err != nil {
		t.Fatalf("standby settings mirror missing sec.zy: %v", err)
	}
	if !strings.Contains(string(secOnStandby), secretPlain) {
		t.Error("standby's mirrored settings lost the !encrypted plaintext")
	}
	sanKV, err := bus.GetBucket(ctx, js, bus.BucketSettingsFiles)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := sanKV.Get(ctx, "sec.zy"); err != nil {
		t.Fatalf("sanitized sec.zy missing: %v", err)
	} else if strings.Contains(string(e.Value()), secretPlain) {
		t.Fatal("plaintext secret leaked into the sanitized settings-files bucket")
	}
	sealedKV, err := bus.GetBucket(ctx, js, bus.BucketMasterSettings)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := sealedKV.Get(ctx, "sec.zy"); err != nil {
		t.Fatalf("sealed sec.zy missing: %v", err)
	} else if strings.Contains(string(e.Value()), secretPlain) {
		t.Fatal("plaintext secret leaked into the sealed master-settings bucket")
	}
	// The OnSynced refresh re-extracted secrets on the standby: a
	// facts-secrets-lease-holding standby must encrypt CURRENT values. It
	// fires just after the file swap, so poll rather than assert once.
	waitFor(t, 3*time.Second, "standby in-memory secrets refresh", func() bool {
		d2.settingsMu.RLock()
		defer d2.settingsMu.RUnlock()
		return d2.allSecrets["sec.zy"]["db_password"] == secretPlain
	})

	stateKV, err := bus.GetBucket(ctx, js, bus.BucketStateFiles)
	if err != nil {
		t.Fatal(err)
	}
	revBefore := bus.GetRevision(ctx, stateKV)
	settingsRevBefore := bus.GetRevision(ctx, sanKV)

	// FAILOVER: stopping the leader hands the lease to master-2. The
	// takeover publish is hash-gated against the mirrored (identical) tree —
	// the fleet keeps master-1's files and the revision does not bump.
	cancel1()
	waitFor(t, 5*time.Second, "master-2 takeover", d2.publisherLeader)
	time.Sleep(300 * time.Millisecond) // let the takeover publish (a no-op) run
	if kvHasKey(t, js, bus.BucketStateFiles, "web2.zy") {
		t.Fatal("takeover reverted the fleet to the standby's stale state file")
	}
	if !kvHasKey(t, js, bus.BucketStateFiles, "web1.zy") {
		t.Fatal("published truth lost across the takeover")
	}
	if rev := bus.GetRevision(ctx, stateKV); rev != revBefore {
		t.Errorf("takeover bumped the state-files revision %d -> %d; a converged takeover must be a no-op", revBefore, rev)
	}
	// Settings converge through the sealed replica, so the takeover is a
	// no-op for the sanitized settings bucket too.
	if rev := bus.GetRevision(ctx, sanKV); rev != settingsRevBefore {
		t.Errorf("takeover bumped the settings-files revision %d -> %d; converged settings must be a no-op", settingsRevBefore, rev)
	}
	if kvHasKey(t, js, bus.BucketSettingsFiles, "m2.zy") {
		t.Fatal("takeover reverted the fleet to the standby's stale settings file")
	}

	// A NEW edit on the new holder publishes normally (via its ticker or
	// watcher — the lease-test daemon config keeps both defaults).
	if err := os.WriteFile(filepath.Join(statesDir2, "post-takeover.zy"), []byte("x:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "post-takeover edit publish", func() bool {
		return kvHasKey(t, js, bus.BucketStateFiles, "post-takeover.zy")
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

	d := newLeaseTestDaemon(t, js, "master-1", t.TempDir(), "app.zy", nil)
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
