package exec_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
)

func TestGroupaddCreate(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)

	err := p.Create(context.Background(), exec.GroupCreateOpts{
		Name:   "devops",
		GID:    2000,
		System: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "groupadd" {
		t.Errorf("command: got %q, want %q", c.Command, "groupadd")
	}
	assertContainsPair(t, c.Args, "-g", "2000")
	assertContainsFlag(t, c.Args, "-r")
	if c.Args[len(c.Args)-1] != "devops" {
		t.Errorf("last arg: got %q, want %q", c.Args[len(c.Args)-1], "devops")
	}
}

func TestGroupaddCreateMinimal(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)

	err := p.Create(context.Background(), exec.GroupCreateOpts{
		Name: "minimal",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	calls := cmd.Calls()
	c := calls[0]
	if len(c.Args) != 1 || c.Args[0] != "minimal" {
		t.Errorf("args: got %v, want [minimal]", c.Args)
	}
}

func TestGroupaddModifyGID(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)

	gid := 3000
	err := p.Modify(context.Background(), "devops", exec.GroupModifyOpts{
		GID: &gid,
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "groupmod" {
		t.Errorf("command: got %q, want %q", c.Command, "groupmod")
	}
	assertContainsPair(t, c.Args, "-g", "3000")
	if c.Args[len(c.Args)-1] != "devops" {
		t.Errorf("last arg: got %q, want %q", c.Args[len(c.Args)-1], "devops")
	}
}

func TestGroupaddModifyAddMembers(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)

	err := p.Modify(context.Background(), "devops", exec.GroupModifyOpts{
		AddMembers: []string{"alice", "bob"},
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 gpasswd -a calls, got %d", len(calls))
	}

	// First call: gpasswd -a alice devops
	c0 := calls[0]
	if c0.Command != "gpasswd" {
		t.Errorf("call 0 command: got %q, want %q", c0.Command, "gpasswd")
	}
	wantArgs0 := []string{"-a", "alice", "devops"}
	if !argsEqual(c0.Args, wantArgs0) {
		t.Errorf("call 0 args: got %v, want %v", c0.Args, wantArgs0)
	}

	// Second call: gpasswd -a bob devops
	c1 := calls[1]
	wantArgs1 := []string{"-a", "bob", "devops"}
	if !argsEqual(c1.Args, wantArgs1) {
		t.Errorf("call 1 args: got %v, want %v", c1.Args, wantArgs1)
	}
}

func TestGroupaddModifyDelMembers(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)

	err := p.Modify(context.Background(), "devops", exec.GroupModifyOpts{
		DelMembers: []string{"charlie"},
	})
	if err != nil {
		t.Fatalf("Modify: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "gpasswd" {
		t.Errorf("command: got %q, want %q", c.Command, "gpasswd")
	}
	wantArgs := []string{"-d", "charlie", "devops"}
	if !argsEqual(c.Args, wantArgs) {
		t.Errorf("args: got %v, want %v", c.Args, wantArgs)
	}
}

func TestGroupaddDelete(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)

	err := p.Delete(context.Background(), "oldgroup")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}

	c := calls[0]
	if c.Command != "groupdel" {
		t.Errorf("command: got %q, want %q", c.Command, "groupdel")
	}
	if len(c.Args) != 1 || c.Args[0] != "oldgroup" {
		t.Errorf("args: got %v, want [oldgroup]", c.Args)
	}
}

func TestGroupaddCreateError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("groupadd", fmt.Errorf("permission denied"))
	p := exec.NewGroupaddProvider(cmd)

	err := p.Create(context.Background(), exec.GroupCreateOpts{Name: "fail"})
	if err == nil {
		t.Error("expected error from failed groupadd")
	}
}

func TestGroupaddModifyGIDError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("groupmod", fmt.Errorf("permission denied"))
	p := exec.NewGroupaddProvider(cmd)

	gid := 500
	err := p.Modify(context.Background(), "fail", exec.GroupModifyOpts{GID: &gid})
	if err == nil {
		t.Error("expected error from failed groupmod")
	}
}

func TestGroupaddModifyAddMemberError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("gpasswd", fmt.Errorf("user not found"))
	p := exec.NewGroupaddProvider(cmd)

	err := p.Modify(context.Background(), "devops", exec.GroupModifyOpts{
		AddMembers: []string{"ghost"},
	})
	if err == nil {
		t.Error("expected error from failed gpasswd -a")
	}
}

func TestGroupaddDeleteError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("groupdel", fmt.Errorf("group in use"))
	p := exec.NewGroupaddProvider(cmd)

	err := p.Delete(context.Background(), "fail")
	if err == nil {
		t.Error("expected error from failed groupdel")
	}
}

func TestGroupaddLookupNonexistent(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewGroupaddProvider(cmd)
	info, err := p.Lookup(context.Background(), "zester_nonexistent_group_99999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil for nonexistent group, got %+v", info)
	}
}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
