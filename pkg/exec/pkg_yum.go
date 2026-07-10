package exec

import (
	"context"
	"fmt"
	"strings"
)

// YumProvider implements PackageExec for RHEL/CentOS 7 systems using yum.
type YumProvider struct {
	cmd CommandExec
}

func NewYumProvider(cmd CommandExec) *YumProvider {
	return &YumProvider{cmd: cmd}
}

func (y *YumProvider) Name() string { return "yum" }

func (y *YumProvider) IsInstalled(ctx context.Context, pkg string) (bool, error) {
	_, err := y.cmd.Run(ctx, CommandOpts{
		Command: "rpm",
		Args:    []string{"-q", pkg},
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (y *YumProvider) Install(ctx context.Context, pkg string, version string) error {
	target := pkg
	if version != "" {
		target = pkg + "-" + version
	}
	_, err := y.cmd.Run(ctx, CommandOpts{
		Command: "yum",
		Args:    []string{"install", "-y", target},
	})
	if err != nil {
		return fmt.Errorf("yum install %s: %w", target, err)
	}
	if version != "" {
		return y.installPinned(ctx, pkg, version, target)
	}
	return nil
}

// installPinned handles yum's downgrade blindness: `yum install -y pkg-<old>`
// on a host running a NEWER version prints "Nothing to do" and exits 0 — a
// silent no-op that would make a version-pinned state perma-churn (Check
// keeps reporting drift, Apply keeps "succeeding"). After the install
// attempt, verify the pin landed; if not, it is a downgrade — run
// `yum downgrade` explicitly. (dnf converges explicit NEVR downgrades on
// its own, so DnfProvider needs no equivalent.)
func (y *YumProvider) installPinned(ctx context.Context, pkg, version, target string) error {
	got, err := y.InstalledVersion(ctx, pkg)
	if err == nil && got == version {
		return nil
	}
	if _, err := y.cmd.Run(ctx, CommandOpts{
		Command: "yum",
		Args:    []string{"downgrade", "-y", target},
	}); err != nil {
		return fmt.Errorf("yum downgrade %s: %w", target, err)
	}
	return nil
}

func (y *YumProvider) Remove(ctx context.Context, pkg string) error {
	_, err := y.cmd.Run(ctx, CommandOpts{
		Command: "yum",
		Args:    []string{"remove", "-y", pkg},
	})
	if err != nil {
		return fmt.Errorf("yum remove %s: %w", pkg, err)
	}
	return nil
}

func (y *YumProvider) Refresh(ctx context.Context) error {
	_, err := y.cmd.Run(ctx, CommandOpts{
		Command: "yum",
		Args:    []string{"makecache"},
	})
	if err != nil {
		return fmt.Errorf("yum makecache: %w", err)
	}
	return nil
}

func (y *YumProvider) InstalledVersion(ctx context.Context, pkg string) (string, error) {
	res, err := y.cmd.Run(ctx, CommandOpts{
		Command: "rpm",
		Args:    []string{"-q", "--qf", "%{VERSION}-%{RELEASE}", pkg},
	})
	if err != nil || res == nil {
		return "", nil // not installed
	}
	return strings.TrimSpace(res.Stdout), nil
}
