package exec

import (
	"context"
	"fmt"
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
