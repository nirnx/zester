package modules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

// testMountMctx wires the mount fake plus a file fake the module reads the
// kernel mount table from. seedProcMounts populates that table.
func testMountMctx(mount *exectest.FakeMountExec, file *exectest.FakeFileExec) *exec.ModuleContext {
	return &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Mount:   mount,
			Package: exectest.NewFakePackageExec("apt"),
			File:    file,
			Command: exectest.NewFakeCommandExec(),
		},
	}
}

// seedProcMounts writes /proc/mounts-format lines into the fake filesystem.
// Pass no lines for an empty (nothing mounted) table.
func seedProcMounts(file *exectest.FakeFileExec, lines ...string) {
	file.PreCreate(exec.ProcMountsPath, []byte(strings.Join(lines, "\n")+"\n"), 0444)
}

func TestMountMountedName(t *testing.T) {
	file := exectest.NewFakeFileExec()
	mctx := testMountMctx(exectest.NewFakeMountExec(), file)
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
	file := exectest.NewFakeFileExec()
	mctx := testMountMctx(exectest.NewFakeMountExec(), file)
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
	file := exectest.NewFakeFileExec()
	mctx := testMountMctx(exectest.NewFakeMountExec(), file)
	_, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{})
	if err == nil {
		t.Fatal("expected error when device is missing")
	}
}

func TestMountMountedCheckNeedsChange(t *testing.T) {
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file) // nothing mounted
	mctx := testMountMctx(fake, file)
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
	file := exectest.NewFakeFileExec()
	// Kernel shows its own default options; declared "defaults" imposes none.
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake, file)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected NeedsChange=false when live mount and fstab match, diff: %s", cr.Diff)
	}
}

func TestMountMountedCheckLiveDeviceMismatch(t *testing.T) {
	// The wrong device serving the mountpoint was reported compliant before
	// — fstab matched and IsMounted only proved *something* was mounted.
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdc1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake, file)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when a different device is live-mounted")
	}
}

func TestMountMountedCheckLiveOptionsSubset(t *testing.T) {
	// Declared options are a SUBSET check: kernel-added defaults never churn.
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,noatime,relatime 0 0")
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults,noatime"})
	mctx := testMountMctx(fake, file)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
		"opts":   "defaults,noatime",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cr.NeedsChange {
		t.Errorf("expected NeedsChange=false: declared options are active, diff: %s", cr.Diff)
	}
}

func TestMountMountedCheckLiveOptionMissing(t *testing.T) {
	// Declared noatime not active on the live mount: the opts-drift case
	// that previously never converged after the first fstab-only Apply.
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults,noatime"})
	mctx := testMountMctx(fake, file)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
		"opts":   "defaults,noatime",
	})
	cr, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !cr.NeedsChange {
		t.Error("expected NeedsChange=true when a declared option is not active")
	}
}

func TestMountMountedCheckFstabPassDrift(t *testing.T) {
	// Dump/Pass are part of the fstab comparison now.
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults", Pass: 0})
	mctx := testMountMctx(fake, file)
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

func TestMountMountedCheckReadErrorFails(t *testing.T) {
	// A non-not-exist read error on the mount table fails the phase — it is
	// never treated as "not mounted".
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	file.SetReadError(exec.ProcMountsPath, errors.New("permission denied"))
	mctx := testMountMctx(fake, file)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	if _, err := s.Check(context.Background()); err == nil {
		t.Error("expected Check error when the mount table is unreadable")
	}
	if _, err := s.Apply(context.Background()); err == nil {
		t.Error("expected Apply error when the mount table is unreadable")
	}
}

func TestMountMountedApply(t *testing.T) {
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file) // nothing mounted
	mctx := testMountMctx(fake, file)
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
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,relatime 0 0")
	mctx := testMountMctx(fake, file)
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

func TestMountMountedApplyRemountsOnLiveMismatch(t *testing.T) {
	// The live mount serves the wrong device: Apply must remount with the
	// declared config instead of skipping Mount because "something" is there.
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdc1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreMount(exec.MountEntry{Device: "/dev/sdc1", MountPoint: "/mnt/data", FSType: "ext4"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake, file)
	s, _ := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{
		"device": "/dev/sdb1",
	})
	ar, err := s.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ar.Changed {
		t.Error("expected Changed=true")
	}
	if !strings.Contains(ar.Diff, "remounted") {
		t.Errorf("Diff = %q, want a remount", ar.Diff)
	}
	if !fake.IsMountedSync("/mnt/data") {
		t.Error("expected /mnt/data mounted after remount")
	}
}

func TestMountMountedApplyConvergedNoOp(t *testing.T) {
	// Watch-forced Apply on a fully converged mount: clean no-op.
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake, file)
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

func TestMountMountedRevert(t *testing.T) {
	fake := exectest.NewFakeMountExec()
	file := exectest.NewFakeFileExec()
	seedProcMounts(file) // nothing mounted
	mctx := testMountMctx(fake, file)
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
	file := exectest.NewFakeFileExec()
	seedProcMounts(file, "/dev/sdb1 /mnt/data ext4 rw,relatime 0 0")
	fake.PreMount(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4"})
	fake.PreFstab(exec.MountEntry{Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults"})
	mctx := testMountMctx(fake, file)
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

func TestMountMountedNoFileProvider(t *testing.T) {
	mctx := &exec.ModuleContext{ProviderSet: exec.ProviderSet{Mount: exectest.NewFakeMountExec()}}
	_, err := NewMountMountedBuilder(mctx)("/mnt/data", map[string]any{"device": "/dev/sdb1"})
	if err == nil {
		t.Fatal("expected error when file provider is nil (mount table unreadable)")
	}
}

func TestMountMountedRequisites(t *testing.T) {
	file := exectest.NewFakeFileExec()
	mctx := testMountMctx(exectest.NewFakeMountExec(), file)
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
