package exec_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func TestUseraddLookupNonexistent(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)
	// Lookup uses os/user.Lookup which reads real /etc/passwd.
	// We can only verify it returns nil for a user that doesn't exist.
	info, err := p.Lookup(context.Background(), "zester_nonexistent_user_12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for nonexistent user, got %+v", info)
	}
}

func TestUseraddCreateAllOpts(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	err := p.Create(context.Background(), exec.UserCreateOpts{
		Name:         "deploy",
		UID:          1001,
		PrimaryGroup: "staff",
		Groups:       []string{"docker", "sudo"},
		Home:         "/home/deploy",
		Shell:        "/bin/bash",
		CreateHome:   true,
		System:       true,
		Password:     "$6$hashed",
		FullName:     "Deploy User",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "useradd" {
		t.Errorf("command: got %q, want %q", c.Command, "useradd")
	}

	args := c.Args
	assertContainsPair(t, args, "-u", "1001")
	assertContainsPair(t, args, "-g", "staff")
	assertContainsPair(t, args, "-G", "docker,sudo")
	assertContainsPair(t, args, "-d", "/home/deploy")
	assertContainsPair(t, args, "-s", "/bin/bash")
	assertContainsFlag(t, args, "-m")
	assertContainsFlag(t, args, "-r")
	assertContainsPair(t, args, "-p", "$6$hashed")
	assertContainsPair(t, args, "-c", "Deploy User")
	// Name should be the last argument.
	if args[len(args)-1] != "deploy" {
		t.Errorf("last arg: got %q, want %q", args[len(args)-1], "deploy")
	}
}

func TestUseraddCreateMinimal(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	err := p.Create(context.Background(), exec.UserCreateOpts{
		Name: "minimal",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "useradd" {
		t.Errorf("command: got %q, want %q", c.Command, "useradd")
	}
	// Only the name should be present.
	if len(c.Args) != 1 {
		t.Errorf("args: got %v (len %d), want [minimal]", c.Args, len(c.Args))
	}
	if c.Args[0] != "minimal" {
		t.Errorf("arg[0]: got %q, want %q", c.Args[0], "minimal")
	}
}

func TestUseraddCreateGIDFallback(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	// When PrimaryGroup is empty, GID should be used.
	err := p.Create(context.Background(), exec.UserCreateOpts{
		Name: "giduser",
		GID:  500,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	calls := cmd.Calls()
	c := calls[0]
	assertContainsPair(t, c.Args, "-g", "500")
}

func TestUseraddModify(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	shell := "/bin/zsh"
	home := "/opt/deploy"
	err := p.Modify(context.Background(), "deploy", exec.UserModifyOpts{
		Shell: &shell,
		Home:  &home,
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "usermod" {
		t.Errorf("command: got %q, want %q", c.Command, "usermod")
	}
	assertContainsPair(t, c.Args, "-s", "/bin/zsh")
	assertContainsPair(t, c.Args, "-d", "/opt/deploy")
	// Name should be the last argument.
	if c.Args[len(c.Args)-1] != "deploy" {
		t.Errorf("last arg: got %q, want %q", c.Args[len(c.Args)-1], "deploy")
	}
}

func TestUseraddModifyEmpty(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	// All-nil opts: no command should be executed.
	err := p.Modify(context.Background(), "deploy", exec.UserModifyOpts{})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}

	if cmd.CallCount() != 0 {
		t.Errorf("expected 0 calls for empty modify, got %d", cmd.CallCount())
	}
}

func TestUseraddDelete(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	err := p.Delete(context.Background(), "olduser", false)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	calls := cmd.Calls()
	c := calls[0]
	if c.Command != "userdel" {
		t.Errorf("command: got %q, want %q", c.Command, "userdel")
	}
	if len(c.Args) != 1 || c.Args[0] != "olduser" {
		t.Errorf("args: got %v, want [olduser]", c.Args)
	}
}

func TestUseraddDeleteWithRemoveHome(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewUseraddProvider(cmd)

	err := p.Delete(context.Background(), "olduser", true)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	calls := cmd.Calls()
	c := calls[0]
	if c.Command != "userdel" {
		t.Errorf("command: got %q, want %q", c.Command, "userdel")
	}
	assertContainsFlag(t, c.Args, "-r")
	if c.Args[len(c.Args)-1] != "olduser" {
		t.Errorf("last arg: got %q, want %q", c.Args[len(c.Args)-1], "olduser")
	}
}

func TestUseraddCreateError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("useradd", fmt.Errorf("permission denied"))
	p := exec.NewUseraddProvider(cmd)

	err := p.Create(context.Background(), exec.UserCreateOpts{Name: "fail"})
	if err == nil {
		t.Error("expected error from failed useradd")
	}
}

func TestUseraddModifyError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("usermod", fmt.Errorf("permission denied"))
	p := exec.NewUseraddProvider(cmd)

	shell := "/bin/bash"
	err := p.Modify(context.Background(), "fail", exec.UserModifyOpts{Shell: &shell})
	if err == nil {
		t.Error("expected error from failed usermod")
	}
}

func TestUseraddDeleteError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("userdel", fmt.Errorf("user is logged in"))
	p := exec.NewUseraddProvider(cmd)

	err := p.Delete(context.Background(), "fail", false)
	if err == nil {
		t.Error("expected error from failed userdel")
	}
}

// assertContainsPair checks that args contains "-flag value" adjacent pair.
func assertContainsPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Errorf("args %v missing pair %s %s", args, flag, value)
}

// assertContainsFlag checks that args contains the given flag.
func assertContainsFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("args %v missing flag %s", args, flag)
}
