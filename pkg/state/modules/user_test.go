package modules

import (
	"context"
	"fmt"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})
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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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

func TestUserPresentRevertAfterNoOpApplyNoOp(t *testing.T) {
	// Round-2 regression: the memo was armed BEFORE drift detection, so a
	// fully converged Apply (Changed=false) still triggered a full usermod +
	// Changed=true on Revert. The memo must arm only when drift is applied.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	fakeUser.ModifyErr = fmt.Errorf("Modify must not be called on a converged user")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell": "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Fatal("expected converged Apply to be a no-op")
	}
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Error("expected Changed=false on Revert after a no-op Apply")
	}
	if rr.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", rr.Diff)
	}
}

func TestUserPresentRevertRestoresPassword(t *testing.T) {
	// Round-2 regression: the password facet was read in Apply but never
	// memoized, so Revert claimed "reverted to original state" while leaving
	// the Apply-set hash in the shadow — the ONE facet Apply actually changed
	// survived the revert.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	fakeUser.SetPasswordHash("deploy", "$6$oldhash")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell":    "/bin/bash",
		"password": "$6$newhash",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Fatal("expected Changed=true on password drift")
	}
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rr.Changed {
		t.Error("expected Changed=true when restoring the password")
	}
	hash, _ := fakeUser.PasswordHash(context.Background(), "deploy")
	if hash != "$6$oldhash" {
		t.Errorf("hash after Revert = %q, want the original %q restored", hash, "$6$oldhash")
	}
}

func TestUserPresentRevertNoDriftReportsNoChange(t *testing.T) {
	// Revert diffs the CURRENT user against the memoized original (mirrors
	// group.present): when they already match, no usermod runs and
	// Changed=false is reported honestly.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/sh"})
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell": "/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The user is externally restored before the revert runs.
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/sh"})
	fakeUser.ModifyErr = fmt.Errorf("Modify must not be called when nothing drifts from original")
	rr, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rr.Changed {
		t.Errorf("expected Changed=false when current already matches original, diff: %s", rr.Diff)
	}
}

func TestUserPresentConvergencePassword(t *testing.T) {
	// Check → Apply → Check for the password facet.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	fakeUser.SetPasswordHash("deploy", "$6$oldhash")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell":    "/bin/bash",
		"password": "$6$newhash",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Fatal("expected NeedsChange=true before Apply")
	}
	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	cr, err = s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected converged after Apply, diff: %s", cr.Diff)
	}
}

// --- password convergence ---

func TestUserPresentCheckPasswordDrift(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	fakeUser.SetPasswordHash("deploy", "$6$oldhash")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell":    "/bin/bash",
		"password": "$6$newhash",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when shadow hash differs from declared password")
	}
}

func TestUserPresentCheckPasswordMatchesNoChange(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	fakeUser.SetPasswordHash("deploy", "$6$samehash")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell":    "/bin/bash",
		"password": "$6$samehash",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when hash matches, diff: %s", cr.Diff)
	}
}

func TestUserPresentCheckPasswordUnverifiable(t *testing.T) {
	// "" from the provider (no hash / cannot verify) with a declared
	// password is drift — Apply's usermod -p is idempotent.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy"})
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"password": "$6$declared",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when the provider reports no hash for a declared password")
	}
}

func TestUserPresentApplyPasswordInSyncIsNoOp(t *testing.T) {
	// Watch-forced Apply with everything (incl. password) in sync must not
	// report a change — the old code always set Password and lied Changed=true.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	fakeUser.SetPasswordHash("deploy", "$6$samehash")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell":    "/bin/bash",
		"password": "$6$samehash",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Changed=false when password and all attributes are in sync")
	}
}

func TestUserPresentApplyPasswordDrift(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy"})
	fakeUser.SetPasswordHash("deploy", "$6$oldhash")
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"password": "$6$newhash",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true on password drift")
	}
	hash, _ := fakeUser.PasswordHash(context.Background(), "deploy")
	if hash != "$6$newhash" {
		t.Errorf("hash after Apply = %q, want %q", hash, "$6$newhash")
	}
}

// --- name-based primary group convergence ---

func TestUserPresentCheckPrimaryGroupDrift(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", GID: 100})
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "docker", GID: 999})
	mctx := testUserMctx(fakeUser)
	mctx.Group = fakeGroup
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"gid": "docker", // string form maps to PrimaryGroup (Salt compat)
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when primary group differs from declared name")
	}
}

func TestUserPresentCheckPrimaryGroupMatchesNoChange(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", GID: 999})
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "docker", GID: 999})
	mctx := testUserMctx(fakeUser)
	mctx.Group = fakeGroup
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"primary_group": "docker",
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected no change when primary group matches, diff: %s", cr.Diff)
	}
}

func TestUserPresentApplyPrimaryGroup(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", GID: 100})
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "docker", GID: 999})
	mctx := testUserMctx(fakeUser)
	mctx.Group = fakeGroup
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"gid": "docker",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true on primary group drift")
	}
	if got := fakeUser.PrimaryGroupOf("deploy"); got != "docker" {
		t.Errorf("PrimaryGroupOf = %q, want %q (name-based usermod -g)", got, "docker")
	}
}

func TestUserPresentApplyPrimaryGroupMissingGroupFails(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", GID: 100})
	mctx := testUserMctx(fakeUser) // FakeGroupExec has no "docker"
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"primary_group": "docker",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected Apply error when the declared primary group does not exist")
	}
}

func TestUserPresentPrimaryGroupRequiresGroupProvider(t *testing.T) {
	mctx := testUserMctx(exectest.NewFakeUserExec())
	mctx.Group = nil
	_, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"primary_group": "docker",
	})
	if err == nil {
		t.Error("expected builder error: primary group declared but no group provider")
	}
}

// --- revert contract ---

func TestUserPresentRevertFreshInstanceNoOp(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "deploy", Shell: "/bin/bash"})
	mctx := testUserMctx(fakeUser)
	s, err := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})("deploy", map[string]any{
		"shell": "/bin/sh",
	})
	if err != nil {
		t.Fatal(err)
	}
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected Changed=false on fresh-instance Revert")
	}
	if ar.Diff != "nothing to revert (no apply recorded in this run)" {
		t.Errorf("Diff = %q, want the explicit no-op explanation", ar.Diff)
	}
	u, _ := fakeUser.GetUser("deploy")
	if u.Shell != "/bin/bash" {
		t.Error("fresh-instance Revert must not touch the user")
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
	builder := NewUserPresentBuilder(mctx, modschema.DecodeOptions{})

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

func TestUserAbsentApplyAlreadyAbsentNoOp(t *testing.T) {
	// Watch-forced Apply bypasses Check: an already-absent user must be a
	// clean no-op, never a userdel failure. DeleteErr proves Delete is
	// not even attempted.
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.DeleteErr = fmt.Errorf("userdel: user does not exist (exit 6)")
	mctx := testUserMctx(fakeUser)
	builder := NewUserAbsentBuilder(mctx)

	s, err := builder("ghost", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("expected clean no-op for absent user, got error: %v", err)
	}
	if ar.Changed {
		t.Error("expected Changed=false for already-absent user")
	}
}

func TestUserAbsentApplyError(t *testing.T) {
	fakeUser := exectest.NewFakeUserExec()
	fakeUser.PreCreate(&exec.UserInfo{Name: "olduser"}) // exists, so Delete IS attempted
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
