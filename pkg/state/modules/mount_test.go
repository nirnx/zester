package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func testMountMctx(mount *exectest.FakeMountExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Mount:   mount,
			Package: exectest.NewFakePackageExec("apt"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

func TestMountMountedName(t *testing.T) {
	mctx := testMountMctx(exectest.NewFakeMountExec())
	s, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "mount.mounted:/mnt/data" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestMountMountedDefaultMountPoint(t *testing.T) {
	mctx := testMountMctx(exectest.NewFakeMountExec())
	s, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	if err != nil {
		t.Fatal(err)
	}
	mm := s.(*MountMounted)
	if mm.MountPoint != "/mnt/data" {
		t.Errorf("MountPoint = %q", mm.MountPoint)
	}
}

func TestMountMountedDeviceRequired(t *testing.T) {
	mctx := testMountMctx(exectest.NewFakeMountExec())
	_, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{})
	if err == nil {
		t.Fatal("expected error when device is missing")
	}
}

func TestMountMountedCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeMountExec() // nothing mounted
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when not mounted")
	}
}

func TestMountMountedCheckNoChange(t *testing.T) {
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected NeedsChange=false when mounted and fstab matches, diff: %s", cr.Diff)
	}
}

func TestMountMountedCheckDoesNotCompareLiveConfig(t *testing.T) {
	// BINDING (round-2): the live mount's device/fstype/options are NOT
	// compared — fstab-language options (nofail/_netdev) and negotiated
	// fstypes (nfs→nfs4) never appear verbatim in the kernel view, so a
	// live-config comparison would perma-churn and escalate to production
	// unmounts. Presence at the mount point is enough.
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "10.0.0.5:/vol", MountPoint: "/mnt/data", FSType: "nfs4", Options: "rw,relatime"})
	fake.PreFstab(exec.MountEntry{Device: "10.0.0.5:/vol", MountPoint: "/mnt/data", FSType: "nfs", Options: "defaults,nofail,_netdev"})
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "10.0.0.5:/vol",
		"fstype": "nfs",
		"opts":   "defaults,nofail,_netdev",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected NeedsChange=false: live config is not a compared facet, diff: %s", cr.Diff)
	}
}

func TestMountMountedCheckFstabPassDrift(t *testing.T) {
	// Dump/Pass are part of the fstab comparison.
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults", Pass: 0})
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
		"pass":   2,
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when fstab pass differs")
	}
}

func TestMountMountedApply(t *testing.T) {
	fake := exectest.NewFakeMountExec() // nothing mounted
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
		"fstype": "xfs",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true")
	}
	if !fake.IsMountedSync("/mnt/data") {
		t.Error("expected /mnt/data to be mounted")
	}
	_, inFstab := fake.GetFstabSync("/mnt/data")
	if !inFstab {
		t.Error("expected /mnt/data in fstab")
	}
}

func TestMountMountedApplyAlreadyMountedAddsFstab(t *testing.T) {
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4"})
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true (fstab was missing)")
	}
	_, inFstab := fake.GetFstabSync("/mnt/data")
	if !inFstab {
		t.Error("expected fstab entry added")
	}
	// The diff must not claim a mount that never happened.
	if strings.Contains(ar.Diff, "mounted /dev") {
		t.Errorf("Diff %q claims a mount, but only fstab was updated", ar.Diff)
	}
}

func TestMountMountedApplyNeverUnmountsExistingMount(t *testing.T) {
	// BINDING (round-2): a mount already serving the mount point — even one
	// that would look "wrong" under a naive live comparison — is left alone.
	// Apply mounts only when nothing is mounted there.
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "/dev/sdc1", MountPoint: "/mnt/data", FSType: "xfs"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	// Any Mount/Unmount attempt would error the Apply — a clean pass proves
	// the existing mount was left completely untouched.
	fake.MountErr = errors.New("Mount must not be called on an occupied mount point")
	fake.UnmountErr = errors.New("Unmount must never be called from Apply")
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Errorf("expected Changed=false (mounted + fstab matches), diff: %s", ar.Diff)
	}
	if !fake.IsMountedSync("/mnt/data") {
		t.Error("expected /mnt/data to still be mounted")
	}
}

func TestMountMountedApplyConvergedNoOp(t *testing.T) {
	// Watch-forced Apply on a fully converged mount: clean no-op.
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar.Changed {
		t.Errorf("expected Changed=false when converged, diff: %s", ar.Diff)
	}
}

func TestMountMountedConvergence(t *testing.T) {
	// Check → Apply → Check must report converged for every compared facet
	// (mounted presence + fstab entry incl. dump/pass).
	fake := exectest.NewFakeMountExec() // nothing mounted, empty fstab
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
		"fstype": "xfs",
		"opts":   "defaults,noatime",
		"pass":   2,
	})
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

func TestMountMountedRevert(t *testing.T) {
	fake := exectest.NewFakeMountExec() // nothing mounted
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	_, _ = s.Apply(context.Background())
	ar, err := s.Revert(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true on Revert")
	}
	if fake.IsMountedSync("/mnt/data") {
		t.Error("expected /mnt/data unmounted after Revert")
	}
	_, inFstab := fake.GetFstabSync("/mnt/data")
	if inFstab {
		t.Error("expected fstab entry removed after Revert")
	}
}

func TestMountMountedRevertFreshInstanceNoOp(t *testing.T) {
	// A fresh instance (standalone ModeRevert): explicit clean no-op with an
	// honest diff — the old code claimed "unmounted ... and removed fstab
	// entry" while doing neither.
	fake := exectest.NewFakeMountExec()
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
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
	if !fake.IsMountedSync("/mnt/data") {
		t.Error("fresh-instance Revert must not unmount")
	}
	if _, inFstab := fake.GetFstabSync("/mnt/data"); !inFstab {
		t.Error("fresh-instance Revert must not touch fstab")
	}
}

func TestMountMountedNoProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Mount: nil}}
	_, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{"device": "/dev/sdb1"})
	if err == nil {
		t.Fatal("expected error when mount provider is nil")
	}
}

func TestMountMountedRequisites(t *testing.T) {
	mctx := testMountMctx(exectest.NewFakeMountExec())
	s, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device":  "/dev/sdb1",
		"require": []any{"pkg.installed:nfs-utils"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := s.Reqs()
	if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nfs-utils" {
		t.Errorf("Require = %v", reqs.Require)
	}
}
