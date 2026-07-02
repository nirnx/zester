package statefiles_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/statefiles"
)

func TestRepoName(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
	}{
		{"ssh with .git", "git@github.com:org/nginx-formula.git", "nginx-formula"},
		{"ssh without .git", "git@github.com:org/states", "states"},
		{"https with .git", "https://github.com/org/states.git", "states"},
		{"https without .git", "https://github.com/org/myrepo", "myrepo"},
		{"simple name", "my-states.git", "my-states"},
		{"bare path", "/path/to/repo.git", "repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statefiles.RepoName(tt.remote)
			if got != tt.want {
				t.Errorf("RepoName(%q): got %q, want %q", tt.remote, got, tt.want)
			}
		})
	}
}

func TestGitFSClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	// Create a bare repo.
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare", bareDir)

	// Clone the bare repo, add a commit, push.
	workDir := t.TempDir()
	runGit(t, workDir, "clone", bareDir, workDir)
	runGit(t, workDir, "-C", workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "-C", workDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "init.zy"), []byte("state: test"), 0644)
	runGit(t, workDir, "-C", workDir, "add", ".")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "add init.zy")
	runGit(t, workDir, "-C", workDir, "push")

	// Set up GitFS.
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	statesDir := t.TempDir()
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        kv,
	})

	gitfs := statefiles.NewGitFS(statefiles.GitFSConfig{
		Remotes:   []string{bareDir},
		StatesDir: statesDir,
		Interval:  1 * time.Hour, // won't tick in this test
	}, pub)

	// Run with a generous timeout so initial clone can complete.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- gitfs.Run(runCtx)
	}()

	// Wait for the cloned file to appear, then cancel.
	repoName := statefiles.RepoName(bareDir)
	clonedFile := filepath.Join(statesDir, repoName, "init.zy")
	waitForFile(t, clonedFile, 10*time.Second)
	cancel()
	<-done

	// Verify file content.
	data, err := os.ReadFile(clonedFile)
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if got := string(data); got != "state: test" {
		t.Errorf("cloned file content: got %q, want %q", got, "state: test")
	}

	// Verify the file was published to KV.
	entry, err := kv.Get(ctx, repoName+"/init.zy")
	if err != nil {
		t.Fatalf("get from kv: %v", err)
	}
	if got := string(entry.Value()); got != "state: test" {
		t.Errorf("kv value: got %q, want %q", got, "state: test")
	}
}

func TestGitFSPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	// Create a bare repo with one commit.
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare", bareDir)

	workDir := t.TempDir()
	runGit(t, workDir, "clone", bareDir, workDir)
	runGit(t, workDir, "-C", workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "-C", workDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "init.zy"), []byte("v1"), 0644)
	runGit(t, workDir, "-C", workDir, "add", ".")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "v1")
	runGit(t, workDir, "-C", workDir, "push")

	// Set up GitFS and do initial clone.
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	statesDir := t.TempDir()
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        kv,
	})

	repoName := statefiles.RepoName(bareDir)

	gitfs := statefiles.NewGitFS(statefiles.GitFSConfig{
		Remotes:   []string{bareDir},
		StatesDir: statesDir,
		Interval:  1 * time.Hour,
	}, pub)

	// First run: clone. Wait for file to appear.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- gitfs.Run(runCtx)
	}()
	waitForFile(t, filepath.Join(statesDir, repoName, "init.zy"), 10*time.Second)
	cancel()
	<-done

	// Push a new commit.
	os.WriteFile(filepath.Join(workDir, "config.zy"), []byte("new-file"), 0644)
	runGit(t, workDir, "-C", workDir, "add", ".")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "v2")
	runGit(t, workDir, "-C", workDir, "push")

	// Second run: should pull and publish the new file.
	runCtx2, cancel2 := context.WithTimeout(ctx, 30*time.Second)
	done2 := make(chan error, 1)
	go func() {
		done2 <- gitfs.Run(runCtx2)
	}()
	waitForFile(t, filepath.Join(statesDir, repoName, "config.zy"), 10*time.Second)
	cancel2()
	<-done2

	// Verify new file was pulled.
	newFile := filepath.Join(statesDir, repoName, "config.zy")
	data, err := os.ReadFile(newFile)
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if got := string(data); got != "new-file" {
		t.Errorf("new file content: got %q, want %q", got, "new-file")
	}

	// Verify KV has the new file.
	entry, err := kv.Get(ctx, repoName+"/config.zy")
	if err != nil {
		t.Fatalf("get from kv: %v", err)
	}
	if got := string(entry.Value()); got != "new-file" {
		t.Errorf("kv value: got %q, want %q", got, "new-file")
	}
}

func TestGitFSRepublishManifestAndDeletions(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	// Bare repo with two files.
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare", bareDir)

	workDir := t.TempDir()
	runGit(t, workDir, "clone", bareDir, workDir)
	runGit(t, workDir, "-C", workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "-C", workDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "keep.zy"), []byte("keep"), 0644)
	os.WriteFile(filepath.Join(workDir, "removed.zy"), []byte("old"), 0644)
	runGit(t, workDir, "-C", workDir, "add", ".")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "v1")
	runGit(t, workDir, "-C", workDir, "push")

	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	statesDir := t.TempDir()
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        kv,
	})
	repoName := statefiles.RepoName(bareDir)

	// runOnce runs GitFS until the given condition holds (the initial
	// clone/pull + publish inside Run is synchronous, so cancelling too
	// early would abort the publish).
	runOnce := func(cond func() bool) {
		t.Helper()
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		gitfs := statefiles.NewGitFS(statefiles.GitFSConfig{
			Remotes:   []string{bareDir},
			StatesDir: statesDir,
			Interval:  1 * time.Hour,
		}, pub)
		done := make(chan error, 1)
		go func() { done <- gitfs.Run(runCtx) }()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !cond() {
			t.Fatal("gitfs run condition not met within deadline")
		}
		cancel()
		<-done
	}

	// First run: clone + publish. The manifest must cover both files.
	runOnce(func() bool {
		_, err := kv.Get(ctx, statefiles.KeyManifest)
		return err == nil
	})
	entry, err := kv.Get(ctx, statefiles.KeyManifest)
	if err != nil {
		t.Fatalf("get manifest after initial publish: %v", err)
	}
	var m statefiles.Manifest
	if err := bus.Decode(entry.Value(), &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	keys := m.Keys()
	if _, ok := keys[repoName+"/keep.zy"]; !ok {
		t.Errorf("manifest missing %s/keep.zy: %+v", repoName, m.Files)
	}
	if _, ok := keys[repoName+"/removed.zy"]; !ok {
		t.Errorf("manifest missing %s/removed.zy: %+v", repoName, m.Files)
	}

	// Delete removed.zy upstream and republish via a second GitFS run: the
	// manifest must shrink and the stale KV key must be deleted.
	runGit(t, workDir, "-C", workDir, "rm", "removed.zy")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "v2 remove")
	runGit(t, workDir, "-C", workDir, "push")

	runOnce(func() bool {
		_, err := kv.Get(ctx, repoName+"/removed.zy")
		return err != nil // stale key deleted by the republish
	})

	entry, err = kv.Get(ctx, statefiles.KeyManifest)
	if err != nil {
		t.Fatalf("get manifest after republish: %v", err)
	}
	m = statefiles.Manifest{}
	if err := bus.Decode(entry.Value(), &m); err != nil {
		t.Fatalf("decode manifest 2: %v", err)
	}
	keys = m.Keys()
	if _, ok := keys[repoName+"/removed.zy"]; ok {
		t.Errorf("manifest still lists removed file: %+v", m.Files)
	}
	if _, ok := keys[repoName+"/keep.zy"]; !ok {
		t.Errorf("manifest lost keep.zy: %+v", m.Files)
	}
	if _, err := kv.Get(ctx, repoName+"/removed.zy"); err == nil {
		t.Error("stale KV key for removed file should be deleted on republish")
	}
	if _, err := kv.Get(ctx, repoName+"/keep.zy"); err != nil {
		t.Errorf("keep.zy KV key should survive: %v", err)
	}
}

// TestGitFSBrokenCloneRecovered verifies self-healing: a repo directory that
// exists but is not a valid git repository (e.g. left behind by an
// interrupted clone) is removed and re-cloned from scratch instead of being
// pulled-and-failed forever.
func TestGitFSBrokenCloneRecovered(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare", bareDir)

	workDir := t.TempDir()
	runGit(t, workDir, "clone", bareDir, workDir)
	runGit(t, workDir, "-C", workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "-C", workDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "init.zy"), []byte("state: healed"), 0644)
	runGit(t, workDir, "-C", workDir, "add", ".")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "add init.zy")
	runGit(t, workDir, "-C", workDir, "push")

	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	statesDir := t.TempDir()
	repoName := statefiles.RepoName(bareDir)

	// Simulate an interrupted clone from an older version: the repo dir
	// exists but is not a git repository (no .git, no worktree files).
	brokenDir := filepath.Join(statesDir, repoName)
	if err := os.MkdirAll(brokenDir, 0755); err != nil {
		t.Fatalf("mkdir broken dir: %v", err)
	}
	os.WriteFile(filepath.Join(brokenDir, "junk.txt"), []byte("partial"), 0644)

	// Plus a stale hidden clone temp dir from an interrupted temp-clone;
	// it must be swept before the fresh clone.
	staleTmp := filepath.Join(statesDir, "."+repoName+".clone-stale")
	if err := os.MkdirAll(staleTmp, 0755); err != nil {
		t.Fatalf("mkdir stale tmp: %v", err)
	}

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        kv,
	})
	gitfs := statefiles.NewGitFS(statefiles.GitFSConfig{
		Remotes:   []string{bareDir},
		StatesDir: statesDir,
		Interval:  1 * time.Hour,
	}, pub)

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gitfs.Run(runCtx) }()

	clonedFile := filepath.Join(statesDir, repoName, "init.zy")
	waitForFile(t, clonedFile, 10*time.Second)
	cancel()
	<-done

	data, err := os.ReadFile(clonedFile)
	if err != nil {
		t.Fatalf("read re-cloned file: %v", err)
	}
	if got := string(data); got != "state: healed" {
		t.Errorf("re-cloned file content: got %q, want %q", got, "state: healed")
	}
	if _, err := os.Stat(filepath.Join(statesDir, repoName, "junk.txt")); !os.IsNotExist(err) {
		t.Errorf("junk from the broken dir should be gone after re-clone, stat err=%v", err)
	}
	if _, err := os.Stat(staleTmp); !os.IsNotExist(err) {
		t.Errorf("stale clone temp dir should be swept, stat err=%v", err)
	}

	// And the healed content must be published.
	entry, err := kv.Get(ctx, repoName+"/init.zy")
	if err != nil {
		t.Fatalf("get from kv: %v", err)
	}
	if got := string(entry.Value()); got != "state: healed" {
		t.Errorf("kv value: got %q, want %q", got, "state: healed")
	}
}

// TestGitFSBrokenCloneUnreachableRemoteSkipsPublish verifies publish safety:
// an invalid (partial) clone directory does NOT count as a last-known-good
// clone, so when the remote is also unreachable the sync must skip publishing
// entirely — publishing would emit a manifest without the repo's files and
// propagate their deletion fleet-wide.
func TestGitFSBrokenCloneUnreachableRemoteSkipsPublish(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	// Unreachable remote: a path that does not exist.
	missingRemote := filepath.Join(t.TempDir(), "gone.git")
	repoName := statefiles.RepoName(missingRemote)

	statesDir := t.TempDir()
	brokenDir := filepath.Join(statesDir, repoName)
	if err := os.MkdirAll(brokenDir, 0755); err != nil {
		t.Fatalf("mkdir broken dir: %v", err)
	}
	os.WriteFile(filepath.Join(brokenDir, "junk.txt"), []byte("partial"), 0644)

	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        kv,
	})
	gitfs := statefiles.NewGitFS(statefiles.GitFSConfig{
		Remotes:   []string{missingRemote},
		StatesDir: statesDir,
		Interval:  1 * time.Hour,
	}, pub)

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gitfs.Run(runCtx) }()

	// The invalid dir is removed before the (failing) re-clone attempt —
	// a deterministic marker that the initial sync cycle ran.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(brokenDir); os.IsNotExist(err) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(brokenDir); !os.IsNotExist(err) {
		t.Fatalf("invalid repo dir should be removed for re-clone, stat err=%v", err)
	}
	cancel()
	<-done

	// No publish must have happened: no manifest, no revision.
	if _, err := kv.Get(ctx, statefiles.KeyManifest); err == nil {
		t.Error("publish ran despite no valid clone for the failed remote (manifest written)")
	}
	if rev := bus.GetRevision(ctx, kv); rev != 0 {
		t.Errorf("revision = %d, want 0 (publish must be skipped)", rev)
	}
}

// TestGitFSPullFailureAfterSuccessStillPublishes verifies the last-known-good
// semantics survive the validity hardening: once a remote has synced
// successfully and its clone is a valid repository, a later pull failure
// (remote outage) must NOT block publishing.
func TestGitFSPullFailureAfterSuccessStillPublishes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare", bareDir)

	workDir := t.TempDir()
	runGit(t, workDir, "clone", bareDir, workDir)
	runGit(t, workDir, "-C", workDir, "config", "user.email", "test@test.com")
	runGit(t, workDir, "-C", workDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "init.zy"), []byte("state: lkg"), 0644)
	runGit(t, workDir, "-C", workDir, "add", ".")
	runGit(t, workDir, "-C", workDir, "commit", "-m", "v1")
	runGit(t, workDir, "-C", workDir, "push")

	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}
	kv, err := js.KeyValue(ctx, bus.BucketStateFiles)
	if err != nil {
		t.Fatalf("get kv: %v", err)
	}

	statesDir := t.TempDir()
	pub := statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: statesDir,
		KV:        kv,
	})
	gitfs := statefiles.NewGitFS(statefiles.GitFSConfig{
		Remotes:   []string{bareDir},
		StatesDir: statesDir,
		Interval:  200 * time.Millisecond,
	}, pub)

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gitfs.Run(runCtx) }()

	// Wait for the initial successful sync + publish.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := kv.Get(ctx, statefiles.KeyManifest); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := kv.Get(ctx, statefiles.KeyManifest); err != nil {
		t.Fatalf("initial publish never happened: %v", err)
	}

	// Break the remote so subsequent pulls fail, let any in-flight cycle
	// drain, then drop the manifest: only a post-outage publish (from a
	// cycle whose pull FAILED) can rewrite it.
	if err := os.RemoveAll(bareDir); err != nil {
		t.Fatalf("remove bare repo: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := kv.Delete(ctx, statefiles.KeyManifest); err != nil {
		t.Fatalf("delete manifest: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	republished := false
	for time.Now().Before(deadline) {
		if _, err := kv.Get(ctx, statefiles.KeyManifest); err == nil {
			republished = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	if !republished {
		t.Fatal("pull failure of a previously-synced valid clone must not block publishing (last known good)")
	}
	entry, err := kv.Get(ctx, statefiles.RepoName(bareDir)+"/init.zy")
	if err != nil {
		t.Fatalf("last-known-good file missing from kv: %v", err)
	}
	if got := string(entry.Value()); got != "state: lkg" {
		t.Errorf("kv value: got %q, want %q", got, "state: lkg")
	}
}

func TestGitFSSSHCommand(t *testing.T) {
	// Verify that RepoName works correctly with SSH-style URLs
	// (indirect test of the SSH handling logic).
	tests := []struct {
		remote string
		want   string
	}{
		{"git@github.com:org/repo.git", "repo"},
		{"git@gitlab.com:team/project.git", "project"},
	}
	for _, tt := range tests {
		got := statefiles.RepoName(tt.remote)
		if got != tt.want {
			t.Errorf("RepoName(%q): got %q, want %q", tt.remote, got, tt.want)
		}
	}
}

// waitForFile polls until a file exists or the timeout expires.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear within %s", path, timeout)
}

// runGit runs a git command and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
}
