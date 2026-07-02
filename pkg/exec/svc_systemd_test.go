package exec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/exec/exectest"
)

func TestSystemdProvider_Name(t *testing.T) {
	p := exec.NewSystemdProvider(exectest.NewFakeCommandExec())
	if p.Name() != "systemd" {
		t.Errorf("Name() = %q, want %q", p.Name(), "systemd")
	}
}

func TestSystemdProvider_IsRunning(t *testing.T) {
	ctx := context.Background()

	t.Run("running when exit code 0", func(t *testing.T) {
		fake := exectest.NewFakeCommandExec()
		fake.SetResult("systemctl", &exec.CommandResult{ExitCode: 0}, nil)
		p := exec.NewSystemdProvider(fake)

		running, err := p.IsRunning(ctx, "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if !running {
			t.Error("expected running=true for exit code 0")
		}

		calls := fake.Calls()
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Command != "systemctl" {
			t.Errorf("command = %q, want %q", calls[0].Command, "systemctl")
		}
		wantArgs := []string{"is-active", "--quiet", "nginx"}
		for i, arg := range wantArgs {
			if calls[0].Args[i] != arg {
				t.Errorf("arg[%d] = %q, want %q", i, calls[0].Args[i], arg)
			}
		}
	})

	t.Run("not running when exit code non-zero", func(t *testing.T) {
		fake := exectest.NewFakeCommandExec()
		fake.SetResult("systemctl", &exec.CommandResult{ExitCode: 3}, nil)
		p := exec.NewSystemdProvider(fake)

		running, err := p.IsRunning(ctx, "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if running {
			t.Error("expected running=false for exit code 3")
		}
	})

	t.Run("error propagation", func(t *testing.T) {
		fake := exectest.NewFakeCommandExec()
		fake.DefaultErr = errors.New("command failed")
		fake.DefaultResult = nil
		p := exec.NewSystemdProvider(fake)

		_, err := p.IsRunning(ctx, "nginx")
		if err == nil {
			t.Error("expected error, got nil")
		}
	})
}

func TestSystemdProvider_IsEnabled(t *testing.T) {
	ctx := context.Background()

	t.Run("enabled when exit code 0", func(t *testing.T) {
		fake := exectest.NewFakeCommandExec()
		fake.SetResult("systemctl", &exec.CommandResult{ExitCode: 0}, nil)
		p := exec.NewSystemdProvider(fake)

		enabled, err := p.IsEnabled(ctx, "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if !enabled {
			t.Error("expected enabled=true for exit code 0")
		}

		calls := fake.Calls()
		wantArgs := []string{"is-enabled", "--quiet", "nginx"}
		for i, arg := range wantArgs {
			if calls[0].Args[i] != arg {
				t.Errorf("arg[%d] = %q, want %q", i, calls[0].Args[i], arg)
			}
		}
	})

	t.Run("not enabled when exit code 1", func(t *testing.T) {
		fake := exectest.NewFakeCommandExec()
		fake.SetResult("systemctl", &exec.CommandResult{ExitCode: 1}, nil)
		p := exec.NewSystemdProvider(fake)

		enabled, err := p.IsEnabled(ctx, "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if enabled {
			t.Error("expected enabled=false for exit code 1")
		}
	})
}

func TestSystemdProvider_Start(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeCommandExec()
	p := exec.NewSystemdProvider(fake)

	if err := p.Start(ctx, "nginx"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "start" || calls[0].Args[1] != "nginx" {
		t.Errorf("unexpected calls: %v", calls)
	}
}

func TestSystemdProvider_Stop(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeCommandExec()
	p := exec.NewSystemdProvider(fake)

	if err := p.Stop(ctx, "nginx"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "stop" || calls[0].Args[1] != "nginx" {
		t.Errorf("unexpected calls: %v", calls)
	}
}

func TestSystemdProvider_Restart(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeCommandExec()
	p := exec.NewSystemdProvider(fake)

	if err := p.Restart(ctx, "nginx"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "restart" || calls[0].Args[1] != "nginx" {
		t.Errorf("unexpected calls: %v", calls)
	}
}

func TestSystemdProvider_Enable(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeCommandExec()
	p := exec.NewSystemdProvider(fake)

	if err := p.Enable(ctx, "nginx"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "enable" || calls[0].Args[1] != "nginx" {
		t.Errorf("unexpected calls: %v", calls)
	}
}

func TestSystemdProvider_Disable(t *testing.T) {
	ctx := context.Background()
	fake := exectest.NewFakeCommandExec()
	p := exec.NewSystemdProvider(fake)

	if err := p.Disable(ctx, "nginx"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Args[0] != "disable" || calls[0].Args[1] != "nginx" {
		t.Errorf("unexpected calls: %v", calls)
	}
}

func TestSystemdProvider_ErrorWrapping(t *testing.T) {
	ctx := context.Background()

	ops := []struct {
		name string
		fn   func(p *exec.SystemdProvider) error
	}{
		{"Start", func(p *exec.SystemdProvider) error { return p.Start(ctx, "x") }},
		{"Stop", func(p *exec.SystemdProvider) error { return p.Stop(ctx, "x") }},
		{"Restart", func(p *exec.SystemdProvider) error { return p.Restart(ctx, "x") }},
		{"Enable", func(p *exec.SystemdProvider) error { return p.Enable(ctx, "x") }},
		{"Disable", func(p *exec.SystemdProvider) error { return p.Disable(ctx, "x") }},
	}

	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			fake := exectest.NewFakeCommandExec()
			fake.DefaultErr = errors.New("underlying error")
			fake.DefaultResult = nil
			p := exec.NewSystemdProvider(fake)
			err := op.fn(p)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
