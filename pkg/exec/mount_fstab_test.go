package exec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func TestFstabProviderIsMounted(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.ProcMountsPath, []byte("/dev/sdb1 /mnt/data ext4 rw 0 0\n"), 0444)
	p := exec.NewFstabProvider(cmd, file)

	ok, err := p.IsMounted(context.Background(), "/mnt/data")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected /mnt/data to be mounted")
	}

	ok, err = p.IsMounted(context.Background(), "/mnt/other")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected /mnt/other to be not mounted")
	}
}

func TestFstabProviderGetFstab(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.FstabPath, []byte("/dev/sdb1\t/mnt/data\text4\tdefaults\t0 0\n"), 0644)
	p := exec.NewFstabProvider(cmd, file)

	entry, err := p.GetFstab(context.Background(), "/mnt/data")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Device != "/dev/sdb1" || entry.FSType != "ext4" {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

func TestFstabProviderGetFstabMissing(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.FstabPath, []byte(""), 0644)
	p := exec.NewFstabProvider(cmd, file)

	entry, err := p.GetFstab(context.Background(), "/mnt/data")
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Error("expected nil for missing entry")
	}
}

func TestFstabProviderSetFstabNew(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.FstabPath, []byte(""), 0644)
	p := exec.NewFstabProvider(cmd, file)

	err := p.SetFstab(context.Background(), exec.MountEntry{
		Device: "/dev/sdb1", MountPoint: "/mnt/data", FSType: "ext4", Options: "defaults",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := file.GetFile(exec.FstabPath)
	if !strings.Contains(string(data), "/dev/sdb1") {
		t.Errorf("expected entry in fstab, got: %s", string(data))
	}
}

func TestFstabProviderSetFstabReplace(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.FstabPath, []byte("/dev/sdb1\t/mnt/data\text4\tdefaults\t0 0\n"), 0644)
	p := exec.NewFstabProvider(cmd, file)

	err := p.SetFstab(context.Background(), exec.MountEntry{
		Device: "/dev/sdc1", MountPoint: "/mnt/data", FSType: "xfs", Options: "defaults",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := file.GetFile(exec.FstabPath)
	content := string(data)
	if strings.Contains(content, "/dev/sdb1") {
		t.Errorf("old device should be replaced, got: %s", content)
	}
	if !strings.Contains(content, "/dev/sdc1") {
		t.Errorf("new device not found, got: %s", content)
	}
}

func TestFstabProviderRemoveFstab(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.FstabPath, []byte("/dev/sdb1\t/mnt/data\text4\tdefaults\t0 0\n"), 0644)
	p := exec.NewFstabProvider(cmd, file)

	if err := p.RemoveFstab(context.Background(), "/mnt/data"); err != nil {
		t.Fatal(err)
	}
	data, _ := file.GetFile(exec.FstabPath)
	if strings.Contains(string(data), "/mnt/data") {
		t.Errorf("expected entry removed, got: %s", string(data))
	}
}
