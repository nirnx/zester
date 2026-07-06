package modules

import (
	"context"
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
	fake := exectest.NewFakeMountExec()
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
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
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
		t.Error("expected NeedsChange=false when mounted and fstab matches")
	}
}

func TestMountMountedApply(t *testing.T) {
	fake := exectest.NewFakeMountExec()
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
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
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
}

func TestMountMountedRevert(t *testing.T) {
	fake := exectest.NewFakeMountExec()
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
