package execmod_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ptorbus/zester/internal/version"
	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
	"github.com/ptorbus/zester/pkg/execmod"
)

// testMctx builds a ModuleContext wired with in-memory fakes. Individual
// tests tweak the returned providers/facts before calling.
func testMctx() (*exec.ModuleContext, *exectest.FakeCommandExec, *exectest.FakePackageExec, *exectest.FakeServiceExec) {
	cmd := exectest.NewFakeCommandExec()
	pkg := exectest.NewFakePackageExec("apt")
	svc := exectest.NewFakeServiceExec("systemd")
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Command: cmd,
			Package: pkg,
			Service: svc,
			File:    exectest.NewFakeFileExec(),
		},
		Facts: map[string]any{
			"id": "web-01",
			"os": map[string]any{
				"family": "debian",
				"name":   "Ubuntu",
			},
			"num_cpus": 4,
		},
	}
	return mctx, cmd, pkg, svc
}

func call(t *testing.T, r *execmod.Registry, name string, mctx *exec.ModuleContext, args map[string]any) (string, error) {
	t.Helper()
	return r.Call(context.Background(), name, mctx, args)
}

func TestTestFunctions(t *testing.T) {
	r := execmod.DefaultRegistry()
	mctx, _, _, _ := testMctx()

	tests := []struct {
		name string
		fn   string
		args map[string]any
		want string
	}{
		{"echo text", "test.echo", map[string]any{"text": "hello"}, "hello"},
		{"echo via id", "test.echo", map[string]any{"__id__": "from-id"}, "from-id"},
		{"echo empty", "test.echo", nil, ""},
		{"version", "test.version", nil, version.Version},
		{"true", "test.true", nil, "true"},
		{"false", "test.false", nil, "false"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := call(t, r, tc.fn, mctx, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if out != tc.want {
				t.Fatalf("got %q want %q", out, tc.want)
			}
		})
	}
}

func TestPkgVersion(t *testing.T) {
	r := execmod.DefaultRegistry()

	t.Run("apt query", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		cmd.DefaultResult = &exec.CommandResult{Stdout: "1.18.0-6ubuntu14\n", ExitCode: 0}
		out, err := call(t, r, "pkg.version", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "1.18.0-6ubuntu14" {
			t.Fatalf("got %q", out)
		}
		// Verify it shelled out to dpkg-query for an apt system.
		calls := cmd.Calls()
		if len(calls) != 1 || !strings.Contains(calls[0].Command, "dpkg-query") {
			t.Fatalf("expected dpkg-query call, got %+v", calls)
		}
		if !strings.Contains(calls[0].Command, "'nginx'") {
			t.Fatalf("package name not quoted into command: %q", calls[0].Command)
		}
	})

	t.Run("rpm query for dnf", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		mctx.Package = exectest.NewFakePackageExec("dnf")
		cmd.DefaultResult = &exec.CommandResult{Stdout: "1.20.1-9.el9", ExitCode: 0}
		out, err := call(t, r, "pkg.version", mctx, map[string]any{"__id__": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "1.20.1-9.el9" {
			t.Fatalf("got %q", out)
		}
		if calls := cmd.Calls(); len(calls) != 1 || !strings.Contains(calls[0].Command, "rpm -q") {
			t.Fatalf("expected rpm query, got %+v", calls)
		}
	})

	t.Run("not installed", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		cmd.DefaultResult = &exec.CommandResult{Stdout: "", ExitCode: 1}
		_, err := call(t, r, "pkg.version", mctx, map[string]any{"name": "ghost"})
		if err == nil {
			t.Fatal("expected error for missing package")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		if _, err := call(t, r, "pkg.version", mctx, nil); err == nil {
			t.Fatal("expected error when name missing")
		}
	})
}

func TestPkgListPkgs(t *testing.T) {
	r := execmod.DefaultRegistry()
	mctx, cmd, _, _ := testMctx()
	cmd.DefaultResult = &exec.CommandResult{Stdout: "nginx 1.18.0\nbash 5.1\n", ExitCode: 0}
	out, err := call(t, r, "pkg.list_pkgs", mctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "nginx 1.18.0\nbash 5.1" {
		t.Fatalf("got %q", out)
	}
}

func TestServiceStatus(t *testing.T) {
	r := execmod.DefaultRegistry()

	t.Run("running via provider", func(t *testing.T) {
		mctx, _, _, svc := testMctx()
		svc.PreAdd("nginx", true, true)
		out, err := call(t, r, "service.status", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "running" {
			t.Fatalf("got %q want running", out)
		}
	})

	t.Run("stopped via provider", func(t *testing.T) {
		mctx, _, _, svc := testMctx()
		svc.PreAdd("nginx", false, false)
		out, err := call(t, r, "service.status", mctx, map[string]any{"__id__": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "stopped" {
			t.Fatalf("got %q want stopped", out)
		}
	})

	t.Run("systemctl fallback when no service provider", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		mctx.Service = nil
		cmd.DefaultResult = &exec.CommandResult{Stdout: "active\n", ExitCode: 0}
		out, err := call(t, r, "service.status", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "active" {
			t.Fatalf("got %q want active", out)
		}
		if calls := cmd.Calls(); len(calls) != 1 || !strings.Contains(calls[0].Command, "systemctl is-active") {
			t.Fatalf("expected systemctl is-active, got %+v", calls)
		}
	})

	t.Run("missing name", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		if _, err := call(t, r, "service.status", mctx, nil); err == nil {
			t.Fatal("expected error when name missing")
		}
	})
}

func TestServiceActions(t *testing.T) {
	r := execmod.DefaultRegistry()

	t.Run("start via provider", func(t *testing.T) {
		mctx, _, _, svc := testMctx()
		out, err := call(t, r, "service.start", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "started nginx" {
			t.Fatalf("got %q", out)
		}
		if !svc.IsRunningSync("nginx") {
			t.Fatal("service should be running after start")
		}
	})

	t.Run("stop via provider", func(t *testing.T) {
		mctx, _, _, svc := testMctx()
		svc.PreAdd("nginx", true, true)
		out, err := call(t, r, "service.stop", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "stopped nginx" {
			t.Fatalf("got %q", out)
		}
		if svc.IsRunningSync("nginx") {
			t.Fatal("service should be stopped")
		}
	})

	t.Run("restart via provider", func(t *testing.T) {
		mctx, _, _, svc := testMctx()
		out, err := call(t, r, "service.restart", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "restarted nginx" {
			t.Fatalf("got %q", out)
		}
		if !svc.IsRunningSync("nginx") {
			t.Fatal("service should be running after restart")
		}
	})

	t.Run("start error propagates", func(t *testing.T) {
		mctx, _, _, svc := testMctx()
		svc.StartErr = context.DeadlineExceeded
		if _, err := call(t, r, "service.start", mctx, map[string]any{"name": "nginx"}); err == nil {
			t.Fatal("expected error from provider")
		}
	})

	t.Run("systemctl fallback", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		mctx.Service = nil
		cmd.DefaultResult = &exec.CommandResult{ExitCode: 0}
		out, err := call(t, r, "service.restart", mctx, map[string]any{"name": "nginx"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "restarted nginx" {
			t.Fatalf("got %q", out)
		}
		if calls := cmd.Calls(); len(calls) != 1 || !strings.Contains(calls[0].Command, "systemctl restart") {
			t.Fatalf("expected systemctl restart, got %+v", calls)
		}
	})
}

func TestDiskUsage(t *testing.T) {
	r := execmod.DefaultRegistry()
	mctx, cmd, _, _ := testMctx()
	cmd.DefaultResult = &exec.CommandResult{Stdout: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 100 40 60 40% /\n", ExitCode: 0}
	out, err := call(t, r, "disk.usage", mctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/dev/sda1") {
		t.Fatalf("got %q", out)
	}
	if calls := cmd.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0].Command, "df -P") {
		t.Fatalf("expected df -P, got %+v", calls)
	}
}

func TestCmdRun(t *testing.T) {
	r := execmod.DefaultRegistry()

	t.Run("returns stdout", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		cmd.DefaultResult = &exec.CommandResult{Stdout: "web-01\n", ExitCode: 0}
		out, err := call(t, r, "cmd.run", mctx, map[string]any{"cmd": "hostname"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "web-01" {
			t.Fatalf("got %q", out)
		}
		if calls := cmd.Calls(); len(calls) != 1 || calls[0].Command != "hostname" || !calls[0].Shell {
			t.Fatalf("expected shell hostname, got %+v", calls)
		}
	})

	t.Run("command via id and cwd", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		cmd.DefaultResult = &exec.CommandResult{Stdout: "ok", ExitCode: 0}
		_, err := call(t, r, "cmd.run", mctx, map[string]any{"__id__": "ls", "cwd": "/tmp"})
		if err != nil {
			t.Fatal(err)
		}
		if calls := cmd.Calls(); len(calls) != 1 || calls[0].Command != "ls" || calls[0].Dir != "/tmp" {
			t.Fatalf("expected ls in /tmp, got %+v", calls)
		}
	})

	t.Run("error propagates", func(t *testing.T) {
		mctx, cmd, _, _ := testMctx()
		cmd.SetError("boom", context.DeadlineExceeded)
		if _, err := call(t, r, "cmd.run", mctx, map[string]any{"cmd": "boom"}); err == nil {
			t.Fatal("expected error from command")
		}
	})

	t.Run("missing command", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		if _, err := call(t, r, "cmd.run", mctx, nil); err == nil {
			t.Fatal("expected error when command missing")
		}
	})
}

func TestGrains(t *testing.T) {
	r := execmod.DefaultRegistry()

	t.Run("nested item", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		out, err := call(t, r, "grains.item", mctx, map[string]any{"key": "os.family"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "debian" {
			t.Fatalf("got %q want debian", out)
		}
	})

	t.Run("scalar via id", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		out, err := call(t, r, "grains.item", mctx, map[string]any{"__id__": "num_cpus"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "4" {
			t.Fatalf("got %q want 4", out)
		}
	})

	t.Run("missing grain returns empty", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		out, err := call(t, r, "grains.item", mctx, map[string]any{"key": "nope.here"})
		if err != nil {
			t.Fatal(err)
		}
		if out != "" {
			t.Fatalf("got %q want empty", out)
		}
	})

	t.Run("missing key arg errors", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		if _, err := call(t, r, "grains.item", mctx, nil); err == nil {
			t.Fatal("expected error when key missing")
		}
	})

	t.Run("items yaml", func(t *testing.T) {
		mctx, _, _, _ := testMctx()
		out, err := call(t, r, "grains.items", mctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "family: debian") || !strings.Contains(out, "id: web-01") {
			t.Fatalf("grains.items output missing facts: %q", out)
		}
	})
}

func TestSysListFunctions(t *testing.T) {
	r := execmod.DefaultRegistry()
	out, err := call(t, r, "sys.list_functions", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Split(out, "\n")
	found := false
	for _, n := range names {
		if n == "sys.list_functions" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sys.list_functions should include itself: %q", out)
	}
	// Newly registered functions should appear.
	r.Register("custom.fn", func(context.Context, *exec.ModuleContext, map[string]any) (string, error) {
		return "", nil
	})
	out2, _ := call(t, r, "sys.list_functions", nil, nil)
	if !strings.Contains(out2, "custom.fn") {
		t.Fatalf("expected custom.fn in listing, got %q", out2)
	}
}

func TestNoCommandProvider(t *testing.T) {
	r := execmod.DefaultRegistry()
	// A context with no command provider should surface a clear error for
	// command-backed functions rather than panic.
	mctx := &exec.ModuleContext{}
	for _, fn := range []string{"pkg.version", "pkg.list_pkgs", "disk.usage", "cmd.run"} {
		args := map[string]any{"name": "x", "cmd": "x"}
		if _, err := call(t, r, fn, mctx, args); err == nil {
			t.Errorf("%s: expected error with no command provider", fn)
		}
	}
}
