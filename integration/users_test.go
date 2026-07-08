//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// cleanupUsers registers a t.Cleanup that removes test users and groups from
// a container. Best-effort — errors are ignored so cleanup never fails tests.
func cleanupUsers(t *testing.T, service string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		container, err := stack.ServiceContainer(ctx, service)
		if err != nil {
			return
		}
		for _, user := range []string{"deploy", "testuser"} {
			//nolint:errcheck
			container.Exec(ctx, []string{"userdel", "-r", user}, tcexec.Multiplexed())
		}
		for _, group := range []string{"developers", "ops", "deploy", "testuser"} {
			//nolint:errcheck
			container.Exec(ctx, []string{"groupdel", group}, tcexec.Multiplexed())
		}
		//nolint:errcheck
		container.Exec(ctx, []string{"rm", "-f", "/etc/sudoers.d/deploy"}, tcexec.Multiplexed())
		//nolint:errcheck
		container.Exec(ctx, []string{"rm", "-f", "/etc/sudoers.d/testuser"}, tcexec.Multiplexed())
	})
}

// ---------------------------------------------------------------------------
// Users Formula Tests
// ---------------------------------------------------------------------------

func TestUsersFormula_GroupCreation(t *testing.T) {
	cleanupUsers(t, "web-01")

	results := execCLI(t, "web-01", "state.apply", "users")
	requireSuccess(t, results, "web-01")

	// Verify the developers group was created with GID 2000.
	devGroup := execInContainer(t, "web-01", []string{"getent", "group", "developers"})
	if !strings.Contains(devGroup, "2000") {
		t.Errorf("expected developers group to contain GID 2000, got %q", devGroup)
	}

	// Verify the ops group was created with GID 2001.
	opsGroup := execInContainer(t, "web-01", []string{"getent", "group", "ops"})
	if !strings.Contains(opsGroup, "2001") {
		t.Errorf("expected ops group to contain GID 2001, got %q", opsGroup)
	}
}

func TestUsersFormula_UserCreation(t *testing.T) {
	cleanupUsers(t, "web-01")

	results := execCLI(t, "web-01", "state.apply", "users")
	requireSuccess(t, results, "web-01")

	// Verify deploy user: UID 4000, shell /bin/bash, home /home/deploy.
	deployPasswd := execInContainer(t, "web-01", []string{"getent", "passwd", "deploy"})
	if !strings.Contains(deployPasswd, "4000") {
		t.Errorf("expected deploy passwd to contain UID 4000, got %q", deployPasswd)
	}
	if !strings.Contains(deployPasswd, "/bin/bash") {
		t.Errorf("expected deploy passwd to contain /bin/bash, got %q", deployPasswd)
	}
	if !strings.Contains(deployPasswd, "/home/deploy") {
		t.Errorf("expected deploy passwd to contain /home/deploy, got %q", deployPasswd)
	}

	// Verify testuser: UID 4001.
	testPasswd := execInContainer(t, "web-01", []string{"getent", "passwd", "testuser"})
	if !strings.Contains(testPasswd, "4001") {
		t.Errorf("expected testuser passwd to contain UID 4001, got %q", testPasswd)
	}

	// Verify deploy belongs to both developers and ops groups.
	deployGroups := execInContainer(t, "web-01", []string{"id", "-Gn", "deploy"})
	if !strings.Contains(deployGroups, "developers") {
		t.Errorf("expected deploy groups to include developers, got %q", deployGroups)
	}
	if !strings.Contains(deployGroups, "ops") {
		t.Errorf("expected deploy groups to include ops, got %q", deployGroups)
	}
}

func TestUsersFormula_HomeDirectory(t *testing.T) {
	cleanupUsers(t, "web-01")

	results := execCLI(t, "web-01", "state.apply", "users")
	requireSuccess(t, results, "web-01")

	// Verify deploy home directory: mode 750, owned by deploy.
	deployStat := execInContainer(t, "web-01", []string{"stat", "-c", "%a %U %G", "/home/deploy"})
	if !strings.Contains(deployStat, "750") {
		t.Errorf("expected deploy home mode 750, got %q", deployStat)
	}
	if !strings.Contains(deployStat, "deploy") {
		t.Errorf("expected deploy home owned by deploy, got %q", deployStat)
	}

	// Verify testuser home directory exists.
	execInContainer(t, "web-01", []string{"test", "-d", "/home/testuser"})
}

func TestUsersFormula_Sudoers(t *testing.T) {
	cleanupUsers(t, "web-01")

	results := execCLI(t, "web-01", "state.apply", "users")
	requireSuccess(t, results, "web-01")

	// Verify deploy sudoer file exists.
	execInContainer(t, "web-01", []string{"test", "-f", "/etc/sudoers.d/deploy"})

	// Verify deploy sudoer file contains the expected rule.
	deploySudo := execInContainer(t, "web-01", []string{"cat", "/etc/sudoers.d/deploy"})
	if !strings.Contains(deploySudo, "deploy ALL=(ALL) NOPASSWD:ALL") {
		t.Errorf("expected deploy sudoer rule, got %q", deploySudo)
	}

	// Verify no sudoer file exists for testuser (testuser is not a sudouser).
	// execInContainer calls t.Fatalf on nonzero exit, so use raw container.Exec.
	ctx := context.Background()
	container, err := stack.ServiceContainer(ctx, "web-01")
	if err != nil {
		t.Fatalf("get container web-01: %v", err)
	}
	exitCode, _, _ := container.Exec(ctx, []string{"test", "-f", "/etc/sudoers.d/testuser"}, tcexec.Multiplexed())
	if exitCode == 0 {
		t.Error("expected no sudoer file for testuser, but /etc/sudoers.d/testuser exists")
	}
}

func TestUsersFormula_Idempotency(t *testing.T) {
	cleanupUsers(t, "web-01")

	// First apply: creates users, groups, home dirs, sudoers.
	results := execCLI(t, "web-01", "state.apply", "users")
	requireSuccess(t, results, "web-01")

	// Second apply: everything should already be in the desired state.
	results2 := execCLI(t, "web-01", "state.apply", "users")
	r := requireSuccess(t, results2, "web-01")

	for _, sr := range r.Results {
		if sr.Changed {
			t.Errorf("expected no changes on second apply, but %s changed", sr.Name)
		}
	}
}

func TestUsersFormula_AllPeels(t *testing.T) {
	for _, peel := range allPeels {
		cleanupUsers(t, peel)
	}

	// Apply per-peel over the Ubuntu peels: the users formula needs real
	// useradd/group tooling, so it deliberately excludes wd-01 (Alpine,
	// watchdog-supervised). A '*' fan-out would include wd-01.
	for _, peel := range allPeels {
		results := execCLI(t, peel, "state.apply", "users")
		r := requireSuccess(t, results, peel)
		r.checkSuccess(t, fmt.Sprintf("peel %s user state", peel))
	}

	// Spot-check: verify deploy user exists on db-01.
	execInContainer(t, "db-01", []string{"id", "deploy"})
}
