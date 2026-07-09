package masterd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/fileserver"
)

// requestUpdate round-trips one fileserver.UpdateRequest over the fake bus.
func requestUpdate(t *testing.T, ps *bustest.FakePubSub, force bool, timeout time.Duration) (*fileserver.UpdateResponse, error) {
	t.Helper()
	data, err := bus.Encode(fileserver.UpdateRequest{Force: force})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	msg, err := ps.Request(ctx, bus.SubjectAdminFileserverUpdate, data)
	if err != nil {
		return nil, err
	}
	var resp fileserver.UpdateResponse
	if err := bus.Decode(msg.Data, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &resp, nil
}

func setResult(t *testing.T, resp *fileserver.UpdateResponse, name string) fileserver.SetResult {
	t.Helper()
	for _, s := range resp.Sets {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("response has no %q set: %+v", name, resp.Sets)
	return fileserver.SetResult{}
}

// TestFileserverService_LeaderRepliesStandbySilent pins the service's
// leader-only reply contract on a two-master fleet: the publisher-lease
// holder answers `zester fileserver update`, the standby stays silent (a
// silent subscriber must never race the holder's reply), and the response
// reflects the hash-gate (unchanged=false, edited=true, forced=true).
func TestFileserverService_LeaderRepliesStandbySilent(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	statesDir1 := t.TempDir()
	if err := os.WriteFile(filepath.Join(statesDir1, "web1.zy"), []byte("a:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := sharedTestEncryptor(t)
	d1 := newLeaseTestDaemon(t, js, "master-1", statesDir1, "m1.zy", enc)
	d2 := newLeaseTestDaemon(t, js, "master-2", t.TempDir(), "m2.zy", enc)
	// This test asserts exact changed=true/false transitions per REQUEST, so
	// the background ticker must not race the requests; and the teardown
	// OnLost (lease cancel) flips d1 into standby mirroring — give it a
	// runCtx that dies with the test and stop the mirrors explicitly.
	runCtx, cancelRun := context.WithCancel(ctx)
	for _, d := range []*Daemon{d1, d2} {
		d.cfg.FilesRepublishInterval = 0
		d.runCtx = runCtx
	}
	t.Cleanup(func() {
		cancelRun()
		d1.stopFilesMirrors()
		d2.stopFilesMirrors()
	})

	ps := bustest.NewFakePubSub()
	for _, d := range []*Daemon{d1, d2} {
		if _, err := d.startFileserverService(ps); err != nil {
			t.Fatalf("start fileserver service: %v", err)
		}
	}

	// No lease holder yet: both masters stay silent, the request times out
	// (production surfaces this as the "no lease holder" hint).
	if _, err := requestUpdate(t, ps, false, 150*time.Millisecond); err == nil {
		t.Fatal("request with no lease holder must time out, got a reply")
	}

	// d1 acquires the lease (single candidate) and runs the initial publish.
	leaseCtx, cancelLease := context.WithCancel(context.Background())
	defer cancelLease()
	if err := d1.startPublisherLease(leaseCtx); err != nil {
		t.Fatalf("start publisher lease: %v", err)
	}
	waitFor(t, 2*time.Second, "initial leader publish", func() bool {
		return kvHasKey(t, js, bus.BucketStateFiles, "web1.zy")
	})

	// Unforced update right after the initial publish: everything unchanged.
	resp, err := requestUpdate(t, ps, false, 2*time.Second)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resp.Master != "master-1" {
		t.Errorf("answering master = %q, want the lease holder master-1", resp.Master)
	}
	if s := setResult(t, resp, "states"); s.Changed || s.Files != 1 || s.Err != "" {
		t.Errorf("states after no edits = %+v, want unchanged 1-file success", s)
	}

	// Edit the tree: the next update publishes it.
	if err := os.WriteFile(filepath.Join(statesDir1, "new.zy"), []byte("b:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err = requestUpdate(t, ps, false, 2*time.Second)
	if err != nil {
		t.Fatalf("update after edit: %v", err)
	}
	if s := setResult(t, resp, "states"); !s.Changed || s.Files != 2 {
		t.Errorf("states after edit = %+v, want changed 2-file publish", s)
	}
	if !kvHasKey(t, js, bus.BucketStateFiles, "new.zy") {
		t.Error("edited state file did not reach KV")
	}

	// Force bypasses the gate even with no edits.
	resp, err = requestUpdate(t, ps, true, 2*time.Second)
	if err != nil {
		t.Fatalf("forced update: %v", err)
	}
	if s := setResult(t, resp, "states"); !s.Changed {
		t.Errorf("forced update = %+v, want changed", s)
	}
}

// TestFileserverStatusService: the status request identifies the lease
// holder by hostname; with no holder it times out (silent non-holders).
func TestFileserverStatusService(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	d := newLeaseTestDaemon(t, js, "master-1", t.TempDir(), "m1.zy", nil)
	runCtx, cancelRun := context.WithCancel(ctx)
	d.runCtx = runCtx
	statusFile := filepath.Join(t.TempDir(), "publisher-status")
	d.cfg.PublisherStatusFile = statusFile
	t.Cleanup(func() {
		cancelRun()
		d.stopFilesMirrors()
	})

	ps := bustest.NewFakePubSub()
	if _, err := d.startFileserverService(ps); err != nil {
		t.Fatal(err)
	}

	request := func(timeout time.Duration) (*fileserver.StatusResponse, error) {
		data, err := bus.Encode(fileserver.StatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		msg, err := ps.Request(rctx, bus.SubjectAdminFileserverStatus, data)
		if err != nil {
			return nil, err
		}
		var resp fileserver.StatusResponse
		if err := bus.Decode(msg.Data, &resp); err != nil {
			t.Fatal(err)
		}
		return &resp, nil
	}

	// No lease holder: silent (request times out).
	if _, err := request(150 * time.Millisecond); err == nil {
		t.Fatal("status with no lease holder must time out")
	}

	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	if err := d.startPublisherLease(leaseCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "lease acquisition", d.publisherLeader)

	resp, err := request(2 * time.Second)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	hostname, _ := os.Hostname()
	if resp.Master != "master-1" || resp.Hostname != hostname {
		t.Errorf("status = %+v, want master-1 on %s", resp, hostname)
	}
	if resp.SinceUnix == 0 {
		t.Error("status missing the acquisition time")
	}

	// The status FILE reflects the transition too (the MOTD source).
	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("publisher status file: %v", err)
	}
	if !strings.Contains(string(data), "role=leader") {
		t.Errorf("status file = %q, want role=leader", data)
	}
}

// TestFilesRepublishTickerPublishesEdits: the interval loop picks up on-disk
// edits without any external trigger.
func TestFilesRepublishTickerPublishesEdits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	statesDir := t.TempDir()
	d := newLeaseTestDaemon(t, js, "master-1", statesDir, "m1.zy", nil)

	go d.runFilesRepublish(ctx, 20*time.Millisecond)

	if err := os.WriteFile(filepath.Join(statesDir, "ticker.zy"), []byte("a:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, "ticker republish of the new file", func() bool {
		return kvHasKey(t, js, bus.BucketStateFiles, "ticker.zy")
	})
}

// TestFilesWatcherPublishesEdits: the fsnotify watcher publishes an on-disk
// edit (after its debounce) without waiting for the republish interval.
func TestFilesWatcherPublishesEdits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	statesDir := t.TempDir()
	d := newLeaseTestDaemon(t, js, "master-1", statesDir, "m1.zy", nil)

	go d.runFilesWatcher(ctx)
	time.Sleep(100 * time.Millisecond) // let the watches attach

	if err := os.WriteFile(filepath.Join(statesDir, "watched.zy"), []byte("a:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Debounce is 1s; allow generous slack for slow CI filesystems.
	waitFor(t, 5*time.Second, "watcher publish of the new file", func() bool {
		return kvHasKey(t, js, bus.BucketStateFiles, "watched.zy")
	})

	// A file in a NEWLY CREATED subdirectory is picked up too (the watcher
	// adds watches for created dirs).
	sub := filepath.Join(statesDir, "web")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the new-dir watch attach
	if err := os.WriteFile(filepath.Join(sub, "init.zy"), []byte("b:\n  test.ping: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "watcher publish from a new subdir", func() bool {
		return kvHasKey(t, js, bus.BucketStateFiles, "web/init.zy")
	})
}

// TestPublishStateFiles_GitFSManagedSkips: with GitFS configured, states
// publishing belongs to the GitFS sync loop (clone-validity gate) — the
// watcher/ticker/update paths must not walk the dir directly.
func TestPublishStateFiles_GitFSManagedSkips(t *testing.T) {
	ctx := context.Background()
	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatal(err)
	}
	d := newLeaseTestDaemon(t, js, "master-1", t.TempDir(), "m1.zy", nil)
	d.cfg.GitFS.Remotes = []string{"git@example.com:states.git"}

	res := d.publishStateFiles(ctx, false)
	if res.Skipped != "gitfs-managed" {
		t.Errorf("states under GitFS = %+v, want Skipped=gitfs-managed", res)
	}
	res = d.publishStateFiles(ctx, true) // force does not bypass ownership
	if res.Skipped != "gitfs-managed" {
		t.Errorf("forced states under GitFS = %+v, want Skipped=gitfs-managed", res)
	}
}

// TestSkipWatchPath: hidden segments BELOW a watched root are filtered;
// a root living under a dot-directory ancestor is not.
func TestSkipWatchPath(t *testing.T) {
	roots := []string{"/home/op/.config/zester/states", "/data/settings"}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/op/.config/zester/states/web/init.zy", false}, // dotted ANCESTOR: keep
		{"/home/op/.config/zester/states/.git/HEAD", true},    // hidden below root: skip
		{"/data/settings/app.zy", false},
		{"/data/settings/.app.zy.swp", true}, // editor swap file: skip
		{"/data/settings/sub/.hidden/x.zy", true},
	}
	for _, c := range cases {
		if got := skipWatchPath(roots, c.path); got != c.want {
			t.Errorf("skipWatchPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
