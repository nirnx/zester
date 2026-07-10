package exec

import (
	"context"
	"fmt"
	"strings"
)

// DnfProvider implements PackageExec for Fedora/RHEL 8+ systems using dnf.
type DnfProvider struct {
	cmd CommandExec
}

func NewDnfProvider(cmd CommandExec) *DnfProvider {
	return &DnfProvider{cmd: cmd}
}

func (d *DnfProvider) Name() string { return "dnf" }

func (d *DnfProvider) IsInstalled(ctx context.Context, pkg string) (bool, error) {
	_, err := d.cmd.Run(ctx, CommandOpts{
		Command: "rpm",
		Args:    []string{"-q", pkg},
	})
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (d *DnfProvider) Install(ctx context.Context, pkg string, version string) error {
	target := pkg
	if version != "" {
		target = pkg + "-" + version
	}
	_, err := d.cmd.Run(ctx, CommandOpts{
		Command: "dnf",
		Args:    []string{"install", "-y", target},
	})
	if err != nil {
		return fmt.Errorf("dnf install %s: %w", target, err)
	}
	return nil
}

func (d *DnfProvider) Remove(ctx context.Context, pkg string) error {
	_, err := d.cmd.Run(ctx, CommandOpts{
		Command: "dnf",
		Args:    []string{"remove", "-y", pkg},
	})
	if err != nil {
		return fmt.Errorf("dnf remove %s: %w", pkg, err)
	}
	return nil
}

func (d *DnfProvider) Refresh(ctx context.Context) error {
	_, err := d.cmd.Run(ctx, CommandOpts{
		Command: "dnf",
		Args:    []string{"makecache"},
	})
	if err != nil {
		return fmt.Errorf("dnf makecache: %w", err)
	}
	return nil
}

func (d *DnfProvider) InstalledVersion(ctx context.Context, pkg string) (string, error) {
	res, err := d.cmd.Run(ctx, CommandOpts{
		Command: "rpm",
		Args:    []string{"-q", "--qf", "%{VERSION}-%{RELEASE}", pkg},
	})
	if err != nil || res == nil {
		return "", nil // not installed
	}
	return strings.TrimSpace(res.Stdout), nil
}
