package modules

import (
	"context"
	"fmt"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testUserMctx(fakeUser *exectest.FakeUserExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			User:    fakeUser,
			Group:   exectest.NewFakeGroupExec(),
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

// --- user.present tests ---

func TestUserPresentName(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserPresentBuilder(mctx)
	s, err := builder("deploy", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "user.present:deploy" {
		t.Errorf("Name: got %q, want %q", s.Name(), "user.present:deploy")
	}
}

func TestUserPresentNameFromConfig(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserPresentBuilder(mctx)
	s, err := builder("web-server-user", map[string]any{
		"name": "www-data",
	})
	if err != nil {
		t.Fatal(err)
	}
	u := s.(*UserPresent)
	if u.UserName != "www-data" {
		t.Errorf("UserName: got %q, want %q", u.UserName, "www-data")
	}
}

func TestUserPresentRequisites(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserPresentBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require":   []any{"group.present:docker"},
		"watch":     []any{"file.managed:/etc/passwd"},
		"onchanges": []any{"cmd.run:setup"},
		"onfail":    []any{"cmd.run:fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "group.present:docker" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/passwd" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:setup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:fallback" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestUserPresentCheckUserDoesNotExist(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for nonexistent user")
	}
}

func TestUserPresentCheckUserExistsNoChange(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{
		Name:  "deploy",
		Shell: "/bin/bash",
		Home:  "/home/deploy",
	})
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{
		"shell": "/bin/bash",
		"home":  "/home/deploy",
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change, got diff: %s", cr.Diff)
	}
}

func TestUserPresentCheckShellDiffers(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{
		Name:  "deploy",
		Shell: "/bin/sh",
	})
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{
		"shell": "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when shell differs")
	}
}

func TestUserPresentGIDString(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{
		"gid": "developers",
	})
	if err != nil {
		t.Fatal(err)
	}
	u := s.(*UserPresent)
	if u.PrimaryGroup != "developers" {
		t.Errorf("PrimaryGroup: got %q, want %q", u.PrimaryGroup, "developers")
	}
	if u.GID != 0 {
		t.Errorf("GID: got %d, want 0 (should be unset when gid is a string)", u.GID)
	}
}

func TestUserPresentGIDStringDoesNotOverridePrimaryGroup(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	// When gid is a string, it should take precedence over primary_group.
	s, err := builder("deploy", map[string]any{
		"gid":           "developers",
		"primary_group": "ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	u := s.(*UserPresent)
	if u.PrimaryGroup != "developers" {
		t.Errorf("PrimaryGroup: got %q, want %q (gid string should take precedence)", u.PrimaryGroup, "developers")
	}
}

func TestUserPresentApplyCreate(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{
		"shell": "/bin/bash",
		"home":  "/home/deploy",
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating user")
	}
	if ar.Details["action"] != "created" {
		t.Errorf("action: got %q, want %q", ar.Details["action"], "created")
	}

	u, ok := fakeUser.GetUser("deploy")
	if !ok {
		t.Fatal("expected user deploy to exist in fake")
	}
	if u.Shell != "/bin/bash" {
		t.Errorf("shell: got %q, want %q", u.Shell, "/bin/bash")
	}
}

func TestUserPresentApplyModify(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{
		Name:  "deploy",
		Shell: "/bin/sh",
	})
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{
		"shell": "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after modifying user")
	}
	if ar.Details["action"] != "modified" {
		t.Errorf("action: got %q, want %q", ar.Details["action"], "modified")
	}

	u, _ := fakeUser.GetUser("deploy")
	if u.Shell != "/bin/bash" {
		t.Errorf("shell: got %q, want %q", u.Shell, "/bin/bash")
	}
}

func TestUserPresentApplyError(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.CreateErr = fmt.Errorf("permission denied")
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed create")
	}
}

func TestUserPresentRevertCreated(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	// Apply first to set wasCreated.
	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of created user")
	}

	_, ok := fakeUser.GetUser("deploy")
	if ok {
		t.Error("expected user to be deleted after revert")
	}
}

func TestUserPresentRevertModified(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{
		Name:  "deploy",
		Shell: "/bin/sh",
	})
	mctx := testUserMctx(fakeUser)
	builder := NewUserPresentBuilder(mctx)

	s, err := builder("deploy", map[string]any{
		"shell": "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Apply first to set original.
	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of modified user")
	}

	u, _ := fakeUser.GetUser("deploy")
	if u.Shell != "/bin/sh" {
		t.Errorf("shell after revert: got %q, want %q", u.Shell, "/bin/sh")
	}
}

func TestUserPresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Group:   exectest.NewFakeGroupExec(),
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
	builder := NewUserPresentBuilder(mctx)

	_, err := builder("deploy", map[string]any{})
	if err == nil {
		t.Error("expected error when no user provider is set")
	}
}

// --- user.absent tests ---

func TestUserAbsentName(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserAbsentBuilder(mctx)
	s, err := builder("olduser", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "user.absent:olduser" {
		t.Errorf("Name: got %q, want %q", s.Name(), "user.absent:olduser")
	}
}

func TestUserAbsentCheckNotExists(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	mctx := testUserMctx(fakeUser)
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("nobody", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change for nonexistent user")
	}
}

func TestUserAbsentCheckExists(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "olduser"})
	mctx := testUserMctx(fakeUser)
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("olduser", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for existing user")
	}
}

func TestUserAbsentApply(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "olduser"})
	mctx := testUserMctx(fakeUser)
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("olduser", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after deleting user")
	}

	_, ok := fakeUser.GetUser("olduser")
	if ok {
		t.Error("expected user to be deleted")
	}
}

func TestUserAbsentApplyWithPurge(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "olduser"})
	mctx := testUserMctx(fakeUser)
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("olduser", map[string]any{
		"purge": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after deleting user with purge")
	}
	if ar.Details["purge"] != "true" {
		t.Errorf("purge detail: got %q, want %q", ar.Details["purge"], "true")
	}
}

func TestUserAbsentNameFromConfig(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserAbsentBuilder(mctx)
	s, err := builder("remove-legacy", map[string]any{
		"name": "legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	u := s.(*UserAbsent)
	if u.UserName != "legacy" {
		t.Errorf("UserName: got %q, want %q", u.UserName, "legacy")
	}
}

func TestUserAbsentRequisites(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserAbsentBuilder(mctx)
	s, err := builder("test", map[string]any{
		"require":   []any{"cmd.run:stop-service"},
		"watch":     []any{"file.managed:/etc/cron.d/olduser"},
		"onchanges": []any{"cmd.run:cleanup"},
		"onfail":    []any{"cmd.run:force-cleanup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "cmd.run:stop-service" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/cron.d/olduser" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:cleanup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:force-cleanup" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestUserAbsentApplyError(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.DeleteErr = fmt.Errorf("user is logged in")
	mctx := testUserMctx(fakeUser)
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("olduser", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed delete")
	}
}

func TestUserAbsentRevert(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("olduser", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change from revert (cannot revert user deletion)")
	}
}

func TestUserAbsentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Group:   exectest.NewFakeGroupExec(),
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
	builder := NewUserAbsentBuilder(mctx)

	_, err := builder("olduser", map[string]any{})
	if err == nil {
		t.Error("expected error when no user provider is set")
	}
}
