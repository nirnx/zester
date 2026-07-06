package exec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

func TestProcfsProviderGet(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetResult("sysctl", &exec.CommandResult{Stdout: "1\n"}, nil)
	file := exectest.NewFakeFileExec()
	p := exec.NewProcfsProvider(cmd, file)

	v, err := p.Get(context.Background(), "net.ipv4.ip_forward")
	if err != nil {
		t.Fatal(err)
	}
	if v != "1" {
		t.Errorf("Get() = %q, want 1", v)
	}
}

func TestProcfsProviderSet(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetResult("sysctl", &exec.CommandResult{}, nil)
	file := exectest.NewFakeFileExec()
	p := exec.NewProcfsProvider(cmd, file)

	if err := p.Set(context.Background(), "net.ipv4.ip_forward", "1"); err != nil {
		t.Fatal(err)
	}
	calls := cmd.Calls()
	if len(calls) == 0 {
		t.Fatal("expected sysctl call")
	}
}

func TestProcfsProviderPersistNewFile(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	p := exec.NewProcfsProvider(cmd, file)

	if err := p.Persist(context.Background(), "net.ipv4.ip_forward", "1"); err != nil {
		t.Fatal(err)
	}
	data, _ := file.GetFile(exec.SysctlConfPath)
	if !strings.Contains(string(data), "net.ipv4.ip_forward = 1") {
		t.Errorf("expected key in conf, got: %s", string(data))
	}
}

func TestProcfsProviderPersistReplaces(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	file := exectest.NewFakeFileExec()
	file.PreCreate(exec.SysctlConfPath, []byte("net.ipv4.ip_forward = 0\n"), 0644)
	p := exec.NewProcfsProvider(cmd, file)

	if err := p.Persist(context.Background(), "net.ipv4.ip_forward", "1"); err != nil {
		t.Fatal(err)
	}
	data, _ := file.GetFile(exec.SysctlConfPath)
	content := string(data)
	if strings.Count(content, "net.ipv4.ip_forward") != 1 {
		t.Errorf("expected exactly one occurrence, got: %s", content)
	}
	if !strings.Contains(content, "net.ipv4.ip_forward = 1") {
		t.Errorf("expected replaced value, got: %s", content)
	}
}
