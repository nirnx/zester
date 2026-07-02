package exec

import (
	"context"
	"fmt"
)

// AptProvider implements PackageExec for Debian/Ubuntu systems using apt-get.
type AptProvider struct {
	cmd CommandExec
}

func NewAptProvider(cmd CommandExec) *AptProvider {
	return &AptProvider{cmd: cmd}
}

func (a *AptProvider) Name() string { return "apt" }

func (a *AptProvider) IsInstalled(ctx context.Context, pkg string) (bool, error) {
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "dpkg",
		Args:    []string{"-s", pkg},
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (a *AptProvider) Install(ctx context.Context, pkg string, version string) error {
	target := pkg
	if version != "" {
		target = pkg + "=" + version
	}
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "apt-get",
		Args:    []string{"install", "-y", target},
	})
	if err != nil {
		return fmt.Errorf("apt install %s: %w", target, err)
	}
	return nil
}

func (a *AptProvider) Remove(ctx context.Context, pkg string) error {
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "apt-get",
		Args:    []string{"remove", "-y", pkg},
	})
	if err != nil {
		return fmt.Errorf("apt remove %s: %w", pkg, err)
	}
	return nil
}

func (a *AptProvider) Refresh(ctx context.Context) error {
	_, err := a.cmd.Run(ctx, CommandOpts{
		Command: "apt-get",
		Args:    []string{"update"},
	})
	if err != nil {
		return fmt.Errorf("apt update: %w", err)
	}
	return nil
}
