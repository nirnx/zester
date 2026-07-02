package exec

import (
	"context"
	"fmt"
)

// BrewProvider implements PackageExec for macOS systems using Homebrew.
type BrewProvider struct {
	cmd CommandExec
}

func NewBrewProvider(cmd CommandExec) *BrewProvider {
	return &BrewProvider{cmd: cmd}
}

func (b *BrewProvider) Name() string { return "brew" }

func (b *BrewProvider) IsInstalled(ctx context.Context, pkg string) (bool, error) {
	_, err := b.cmd.Run(ctx, CommandOpts{
		Command: "brew",
		Args:    []string{"list", "--formula", pkg},
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (b *BrewProvider) Install(ctx context.Context, pkg string, version string) error {
	target := pkg
	if version != "" {
		target = pkg + "@" + version
	}
	_, err := b.cmd.Run(ctx, CommandOpts{
		Command: "brew",
		Args:    []string{"install", target},
	})
	if err != nil {
		return fmt.Errorf("brew install %s: %w", target, err)
	}
	return nil
}

func (b *BrewProvider) Remove(ctx context.Context, pkg string) error {
	_, err := b.cmd.Run(ctx, CommandOpts{
		Command: "brew",
		Args:    []string{"uninstall", pkg},
	})
	if err != nil {
		return fmt.Errorf("brew uninstall %s: %w", pkg, err)
	}
	return nil
}

func (b *BrewProvider) Refresh(ctx context.Context) error {
	_, err := b.cmd.Run(ctx, CommandOpts{
		Command: "brew",
		Args:    []string{"update"},
	})
	if err != nil {
		return fmt.Errorf("brew update: %w", err)
	}
	return nil
}
