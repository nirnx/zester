package modules

import (
	"context"
	"fmt"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
)

func testGroupMctx(fakeGroup *exectest.FakeGroupExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Group:   fakeGroup,
			User:    exectest.NewFakeUserExec(),
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

// --- group.present tests ---

func TestGroupPresentName(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("docker", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "group.present:docker" {
		t.Errorf("Name: got %q, want %q", s.Name(), "group.present:docker")
	}
}

func TestGroupPresentNameFromConfig(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("container-group", map[string]any{
		"name": "docker",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := s.(*GroupPresent)
	if g.GroupName != "docker" {
		t.Errorf("GroupName: got %q, want %q", g.GroupName, "docker")
	}
}

func TestGroupPresentRequisites(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"require":   []any{"pkg.installed:docker"},
		"watch":     []any{"file.managed:/etc/group"},
		"onchanges": []any{"cmd.run:setup"},
		"onfail":    []any{"cmd.run:fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:docker" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/group" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:setup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:fallback" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestGroupPresentCheckDoesNotExist(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for nonexistent group")
	}
}

func TestGroupPresentCheckExistsNoChange(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{
		Name: "docker",
		GID:  999,
	})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{
		"gid": 999,
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

func TestGroupPresentCheckGIDDiffers(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{
		Name: "docker",
		GID:  998,
	})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{
		"gid": 999,
	})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange when GID differs")
	}
}

func TestGroupPresentApplyCreate(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{
		"gid":    999,
		"system": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after creating group")
	}
	if ar.Details["action"] != "created" {
		t.Errorf("action: got %q, want %q", ar.Details["action"], "created")
	}

	g, ok := fakeGroup.GetGroup("docker")
	if !ok {
		t.Fatal("expected group docker to exist in fake")
	}
	if g.GID != 999 {
		t.Errorf("GID: got %d, want %d", g.GID, 999)
	}
}

func TestGroupPresentApplyModify(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{
		Name: "docker",
		GID:  998,
	})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{
		"gid": 999,
	})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after modifying group")
	}
	if ar.Details["action"] != "modified" {
		t.Errorf("action: got %q, want %q", ar.Details["action"], "modified")
	}

	g, _ := fakeGroup.GetGroup("docker")
	if g.GID != 999 {
		t.Errorf("GID: got %d, want %d", g.GID, 999)
	}
}

func TestGroupPresentApplyError(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.CreateErr = fmt.Errorf("permission denied")
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed create")
	}
}

func TestGroupPresentRevertCreated(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of created group")
	}

	_, ok := fakeGroup.GetGroup("docker")
	if ok {
		t.Error("expected group to be deleted after revert")
	}
}

func TestGroupPresentRevertModified(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{
		Name: "docker",
		GID:  998,
	})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{
		"gid": 999,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed on revert of modified group")
	}

	g, _ := fakeGroup.GetGroup("docker")
	if g.GID != 998 {
		t.Errorf("GID after revert: got %d, want %d", g.GID, 998)
	}
}

func TestGroupPresentRevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert) has no apply memo: Revert
	// must be an explicit clean no-op, never touching the group.
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "docker", GID: 998})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{"gid": 999})
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
	g, _ := fakeGroup.GetGroup("docker")
	if g.GID != 998 {
		t.Error("fresh-instance Revert must not touch the group")
	}
}

func TestGroupPresentRevertRestoresMembership(t *testing.T) {
	// Apply added a member; Revert must restore the ORIGINAL membership,
	// not just the GID (the old code stored original.Members but never used
	// it while claiming "reverted to original state").
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "docker", GID: 999, Members: []string{"alice"}})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{
		"addusers": []any{"bob"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	g, _ := fakeGroup.GetGroup("docker")
	if !containsString(g.Members, "bob") {
		t.Fatal("expected bob added by Apply")
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true on membership revert")
	}
	g, _ = fakeGroup.GetGroup("docker")
	if containsString(g.Members, "bob") {
		t.Error("expected bob removed by Revert")
	}
	if !containsString(g.Members, "alice") {
		t.Error("expected alice preserved by Revert")
	}
}

func TestGroupPresentRevertNoDriftReportsNoChange(t *testing.T) {
	// If the group already matches the memoized original, Revert must not
	// claim Changed=true (the old code did whenever original was set).
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "docker", GID: 999})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("docker", map[string]any{"gid": 999})
	if err != nil {
		t.Fatal(err)
	}

	// Apply memoizes original but changes nothing (already converged).
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
		t.Errorf("expected Changed=false when nothing drifted from original, diff: %s", rr.Diff)
	}
}

func TestGroupPresentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			User:    exectest.NewFakeUserExec(),
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
	builder := NewGroupPresentBuilder(mctx, modschema.DecodeOptions{})

	_, err := builder("docker", map[string]any{})
	if err == nil {
		t.Error("expected error when no group provider is set")
	}
}

// --- group.absent tests ---

func TestGroupAbsentName(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("oldgroup", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "group.absent:oldgroup" {
		t.Errorf("Name: got %q, want %q", s.Name(), "group.absent:oldgroup")
	}
}

func TestGroupAbsentCheckNotExists(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("ghost", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Error("expected no change for nonexistent group")
	}
}

func TestGroupAbsentCheckExists(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "oldgroup"})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("oldgroup", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange for existing group")
	}
}

func TestGroupAbsentApply(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "oldgroup"})
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("oldgroup", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed after deleting group")
	}

	_, ok := fakeGroup.GetGroup("oldgroup")
	if ok {
		t.Error("expected group to be deleted")
	}
}

func TestGroupAbsentNameFromConfig(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("remove-legacy", map[string]any{
		"name": "legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	g := s.(*GroupAbsent)
	if g.GroupName != "legacy" {
		t.Errorf("GroupName: got %q, want %q", g.GroupName, "legacy")
	}
}

func TestGroupAbsentRequisites(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})
	s, err := builder("test", map[string]any{
		"require":   []any{"user.absent:olduser"},
		"watch":     []any{"file.managed:/etc/group"},
		"onchanges": []any{"cmd.run:cleanup"},
		"onfail":    []any{"cmd.run:force-cleanup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "user.absent:olduser" {
		t.Errorf("Require: got %v", reqs.Require)
	}
	if len(reqs.Watch) != 1 || reqs.Watch[0] != "file.managed:/etc/group" {
		t.Errorf("Watch: got %v", reqs.Watch)
	}
	if len(reqs.OnChanges) != 1 || reqs.OnChanges[0] != "cmd.run:cleanup" {
		t.Errorf("OnChanges: got %v", reqs.OnChanges)
	}
	if len(reqs.OnFail) != 1 || reqs.OnFail[0] != "cmd.run:force-cleanup" {
		t.Errorf("OnFail: got %v", reqs.OnFail)
	}
}

func TestGroupAbsentApplyAlreadyAbsentNoOp(t *testing.T) {
	// Watch-forced Apply bypasses Check: an already-absent group must be a
	// clean no-op, never a groupdel failure. DeleteErr proves Delete is
	// not even attempted.
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.DeleteErr = fmt.Errorf("groupdel: group does not exist (exit 6)")
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("ghost", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatalf("expected clean no-op for absent group, got error: %v", err)
	}
	if ar.Changed {
		t.Error("expected Changed=false for already-absent group")
	}
}

func TestGroupAbsentApplyError(t *testing.T) {
	fakeGroup := exectest.NewFakeGroupExec()
	fakeGroup.PreCreate(&exec.GroupInfo{Name: "oldgroup"}) // exists, so Delete IS attempted
	fakeGroup.DeleteErr = fmt.Errorf("group in use")
	mctx := testGroupMctx(fakeGroup)
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("oldgroup", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(context.Background())
	if err == nil {
		t.Error("expected error from failed delete")
	}
}

func TestGroupAbsentRevert(t *testing.T) {
	mctx := testGroupMctx(exectest.NewFakeGroupExec())
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	s, err := builder("oldgroup", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Error("expected no change from revert (cannot revert group deletion)")
	}
}

func TestGroupAbsentNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			User:    exectest.NewFakeUserExec(),
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
	builder := NewGroupAbsentBuilder(mctx, modschema.DecodeOptions{})

	_, err := builder("oldgroup", map[string]any{})
	if err == nil {
		t.Error("expected error when no group provider is set")
	}
}
