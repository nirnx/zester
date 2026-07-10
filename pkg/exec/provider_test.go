package exec_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
)

// --- AptProvider ---

func TestAptProviderName(t *testing.T) {
	p := exec.NewAptProvider(exectest.NewFakeCommandExec())
	if p.Name() != "apt" {
		t.Errorf("Name: got %q, want %q", p.Name(), "apt")
	}
}

func TestAptProviderIsInstalled(t *testing.T) {
	// The probe is STATUS-aware: only a fully installed package counts.
	// `dpkg -s`-style exit-code probing counted 'rc' state (removed,
	// conffiles remain) as installed — dpkg exits 0 for those — which made
	// pkg.installed unable to ever reinstall such a package.
	for name, tc := range map[string]struct {
		stdout string
		want   bool
	}{
		"installed": {"installed", true},
		"rc state":  {"config-files", false},
		"half":      {"half-configured", false},
	} {
		cmd := exectest.NewFakeCommandExec()
		cmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: tc.stdout, ExitCode: 0}, nil)
		p := exec.NewAptProvider(cmd)
		installed, err := p.IsInstalled(context.Background(), "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if installed != tc.want {
			t.Errorf("%s: installed = %v, want %v", name, installed, tc.want)
		}
	}
}

func TestAptProviderInstalledVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout string
		want   string
	}{
		"installed": {"installed 1.24.0-1", "1.24.0-1"},
		"rc state":  {"config-files 1.20.0-1", ""},
	} {
		cmd := exectest.NewFakeCommandExec()
		cmd.SetResult("dpkg-query", &exec.CommandResult{Stdout: tc.stdout, ExitCode: 0}, nil)
		p := exec.NewAptProvider(cmd)
		v, err := p.InstalledVersion(context.Background(), "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if v != tc.want {
			t.Errorf("%s: version = %q, want %q", name, v, tc.want)
		}
	}
}

func TestAptProviderIsInstalledNotFound(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("dpkg", fmt.Errorf("not installed"))
	p := exec.NewAptProvider(cmd)

	installed, err := p.IsInstalled(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Error("expected not installed when dpkg fails")
	}
}

func TestAptProviderInstall(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewAptProvider(cmd)

	if err := p.Install(context.Background(), "nginx", ""); err != nil {
		t.Fatal(err)
	}

	calls := cmd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.Command != "apt-get" {
		t.Errorf("command: got %q, want %q", c.Command, "apt-get")
	}
	if c.Args[0] != "install" || c.Args[1] != "-y" {
		t.Errorf("args prefix: got %v, want [install -y ...]", c.Args)
	}
	if last := c.Args[len(c.Args)-1]; last != "nginx" {
		t.Errorf("package arg: got %q, want %q", last, "nginx")
	}
	// Fully non-interactive: no debconf prompts (no TTY on the exec worker),
	// and modified conffiles resolve without prompting.
	if c.Env["DEBIAN_FRONTEND"] != "noninteractive" {
		t.Errorf("DEBIAN_FRONTEND: got %q, want noninteractive", c.Env["DEBIAN_FRONTEND"])
	}
	if !argsContain(c.Args, "Dpkg::Options::=--force-confold") ||
		!argsContain(c.Args, "Dpkg::Options::=--force-confdef") {
		t.Errorf("missing dpkg conf-force options: %v", c.Args)
	}
}

func argsContain(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestAptProviderInstallWithVersion(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewAptProvider(cmd)

	if err := p.Install(context.Background(), "nginx", "1.18"); err != nil {
		t.Fatal(err)
	}

	calls := cmd.Calls()
	lastArg := calls[0].Args[len(calls[0].Args)-1]
	if lastArg != "nginx=1.18" {
		t.Errorf("version arg: got %q, want %q", lastArg, "nginx=1.18")
	}
}

func TestAptProviderRemove(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewAptProvider(cmd)

	if err := p.Remove(context.Background(), "nginx"); err != nil {
		t.Fatal(err)
	}

	calls := cmd.Calls()
	if calls[0].Command != "apt-get" {
		t.Errorf("command: got %q, want %q", calls[0].Command, "apt-get")
	}
	if calls[0].Args[0] != "remove" {
		t.Errorf("args[0]: got %q, want %q", calls[0].Args[0], "remove")
	}
	if calls[0].Env["DEBIAN_FRONTEND"] != "noninteractive" {
		t.Errorf("DEBIAN_FRONTEND: got %q, want noninteractive", calls[0].Env["DEBIAN_FRONTEND"])
	}
}

func TestAptProviderRefresh(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewAptProvider(cmd)

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := cmd.Calls()
	if calls[0].Command != "apt-get" || calls[0].Args[0] != "update" {
		t.Errorf("expected 'apt-get update', got %q %v", calls[0].Command, calls[0].Args)
	}
}

// --- DnfProvider ---

func TestDnfProviderName(t *testing.T) {
	p := exec.NewDnfProvider(exectest.NewFakeCommandExec())
	if p.Name() != "dnf" {
		t.Errorf("Name: got %q, want %q", p.Name(), "dnf")
	}
}

func TestDnfProviderIsInstalled(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewDnfProvider(cmd)

	installed, _ := p.IsInstalled(context.Background(), "httpd")
	if !installed {
		t.Error("expected installed (rpm -q succeeds by default)")
	}
	if cmd.Calls()[0].Command != "rpm" {
		t.Errorf("command: got %q, want %q", cmd.Calls()[0].Command, "rpm")
	}
}

func TestDnfProviderInstallWithVersion(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewDnfProvider(cmd)

	p.Install(context.Background(), "httpd", "2.4")

	lastArg := cmd.Calls()[0].Args[len(cmd.Calls()[0].Args)-1]
	if lastArg != "httpd-2.4" {
		t.Errorf("version arg: got %q, want %q", lastArg, "httpd-2.4")
	}
}

func TestDnfProviderRefresh(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewDnfProvider(cmd)
	p.Refresh(context.Background())

	if cmd.Calls()[0].Args[0] != "makecache" {
		t.Errorf("expected 'makecache', got %v", cmd.Calls()[0].Args)
	}
}

// --- YumProvider ---

func TestYumProviderName(t *testing.T) {
	p := exec.NewYumProvider(exectest.NewFakeCommandExec())
	if p.Name() != "yum" {
		t.Errorf("Name: got %q, want %q", p.Name(), "yum")
	}
}

func TestYumProviderInstallWithVersion(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewYumProvider(cmd)

	p.Install(context.Background(), "nginx", "1.20")

	lastArg := cmd.Calls()[0].Args[len(cmd.Calls()[0].Args)-1]
	if lastArg != "nginx-1.20" {
		t.Errorf("version arg: got %q, want %q", lastArg, "nginx-1.20")
	}
}

func TestYumProviderRemove(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewYumProvider(cmd)
	p.Remove(context.Background(), "nginx")

	c := cmd.Calls()[0]
	if c.Command != "yum" || c.Args[0] != "remove" {
		t.Errorf("expected 'yum remove', got %q %v", c.Command, c.Args)
	}
}

// --- BrewProvider ---

func TestBrewProviderName(t *testing.T) {
	p := exec.NewBrewProvider(exectest.NewFakeCommandExec())
	if p.Name() != "brew" {
		t.Errorf("Name: got %q, want %q", p.Name(), "brew")
	}
}

func TestBrewProviderIsInstalled(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewBrewProvider(cmd)

	installed, _ := p.IsInstalled(context.Background(), "wget")
	if !installed {
		t.Error("expected installed")
	}
	c := cmd.Calls()[0]
	if c.Command != "brew" {
		t.Errorf("command: got %q, want %q", c.Command, "brew")
	}
	expected := []string{"list", "--formula", "wget"}
	for i, a := range expected {
		if c.Args[i] != a {
			t.Errorf("args[%d]: got %q, want %q", i, c.Args[i], a)
		}
	}
}

func TestBrewProviderInstallWithVersion(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewBrewProvider(cmd)

	p.Install(context.Background(), "python", "3.11")

	lastArg := cmd.Calls()[0].Args[len(cmd.Calls()[0].Args)-1]
	if lastArg != "python@3.11" {
		t.Errorf("version arg: got %q, want %q", lastArg, "python@3.11")
	}
}

func TestBrewProviderRemove(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewBrewProvider(cmd)
	p.Remove(context.Background(), "wget")

	c := cmd.Calls()[0]
	if c.Command != "brew" || c.Args[0] != "uninstall" {
		t.Errorf("expected 'brew uninstall', got %q %v", c.Command, c.Args)
	}
}

func TestBrewProviderRefresh(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	p := exec.NewBrewProvider(cmd)
	p.Refresh(context.Background())

	c := cmd.Calls()[0]
	if c.Command != "brew" || c.Args[0] != "update" {
		t.Errorf("expected 'brew update', got %q %v", c.Command, c.Args)
	}
}

// --- Install/Remove error propagation ---

func TestProviderInstallError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("apt-get", fmt.Errorf("network unreachable"))
	p := exec.NewAptProvider(cmd)

	err := p.Install(context.Background(), "nginx", "")
	if err == nil {
		t.Fatal("expected error from failed install")
	}
}

func TestProviderRemoveError(t *testing.T) {
	cmd := exectest.NewFakeCommandExec()
	cmd.SetError("dnf", fmt.Errorf("permission denied"))
	p := exec.NewDnfProvider(cmd)

	err := p.Remove(context.Background(), "httpd")
	if err == nil {
		t.Fatal("expected error from failed remove")
	}
}
