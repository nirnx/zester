//go:build integration

package integration

import (
	"testing"
)

// --- File Extension Tests ---

func TestFileSymlink_Direct(t *testing.T) {
	results := execCLI(t, "web-01", "state.apply", "files_extended")
	if len(results) == 0 {
		t.Fatal("no results returned")
	}
	r := results[0]
	r.requireSuccess(t, "state.apply files_extended")

	// Find the symlink state result.
	found := false
	for _, sr := range r.Results {
		if sr.Name == "file.symlink:/tmp/zester-symlink-test" {
			found = true
			if !sr.Changed {
				t.Error("expected symlink to report changed on first apply")
			}
		}
	}
	if !found {
		t.Error("file.symlink state not found in results")
	}

	// Second apply should be idempotent.
	results2 := execCLI(t, "web-01", "state.apply", "files_extended")
	if len(results2) == 0 {
		t.Fatal("no results on second apply")
	}
	for _, sr := range results2[0].Results {
		if sr.Name == "file.symlink:/tmp/zester-symlink-test" && sr.Changed {
			t.Error("expected symlink to be idempotent on second apply")
		}
	}
}

func TestFileBlockreplace_Direct(t *testing.T) {
	results := execCLI(t, "web-01", "state.apply", "files_extended")
	if len(results) == 0 {
		t.Fatal("no results returned")
	}
	r := results[0]
	r.requireSuccess(t, "state.apply files_extended")

	found := false
	for _, sr := range r.Results {
		if sr.Name == "file.blockreplace:blockreplace_test" {
			found = true
			if !sr.Changed {
				t.Error("expected blockreplace to report changed on first apply")
			}
		}
	}
	if !found {
		t.Error("file.blockreplace state not found in results")
	}
}

// --- Service Tests ---

func TestServiceRunning_Direct(t *testing.T) {
	// Apply the services state which installs nginx and starts it.
	results := execCLI(t, "web-02", "state.apply", "services")
	if len(results) == 0 {
		t.Fatal("no results returned")
	}
	r := results[0]
	r.requireSuccess(t, "state.apply services")

	// Verify service.running was applied.
	for _, sr := range r.Results {
		if sr.Name == "service.running:nginx_running" {
			if sr.Error != "" {
				t.Errorf("service.running error: %s", sr.Error)
			}
			return
		}
	}
	t.Error("service.running state not found in results")
}

func TestServiceDead_Direct(t *testing.T) {
	// First ensure nginx is installed and running.
	execCLI(t, "web-03", "state.apply", "services")

	// Now stop it directly.
	results := execCLI(t, "web-03", "service.dead", "name=nginx")
	if len(results) == 0 {
		t.Fatal("no results returned")
	}
	r := results[0]
	if r.Error != "" {
		t.Fatalf("service.dead failed: %s", r.Error)
	}
}

// --- Package Tests ---

func TestPkgRemoved_Direct(t *testing.T) {
	// Install a small package first.
	execCLI(t, "db-01", "pkg.installed", "name=toilet")

	// Remove it.
	results := execCLI(t, "db-01", "pkg.removed", "name=toilet")
	if len(results) == 0 {
		t.Fatal("no results returned")
	}
	r := results[0]
	if r.Error != "" {
		t.Fatalf("pkg.removed failed: %s", r.Error)
	}

	// Second remove should be idempotent.
	results2 := execCLI(t, "db-01", "pkg.removed", "name=toilet")
	if len(results2) == 0 {
		t.Fatal("no results on second remove")
	}
}
