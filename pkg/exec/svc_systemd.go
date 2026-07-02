package exec

import (
	"context"
	"fmt"
)

// SystemdProvider implements ServiceExec for Linux systems using systemctl.
type SystemdProvider struct {
	cmd CommandExec
}

// NewSystemdProvider creates a SystemdProvider using the given CommandExec.
func NewSystemdProvider(cmd CommandExec) *SystemdProvider {
	return &SystemdProvider{cmd: cmd}
}

func (s *SystemdProvider) Name() string { return "systemd" }

func (s *SystemdProvider) IsRunning(ctx context.Context, service string) (bool, error) {
	result, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"is-active", "--quiet", service},
	})
	// systemctl is-active uses exit codes: 0=active, non-zero=inactive.
	// OSCommandExec returns an error for non-zero exits, but the exit code
	// in the result is the meaningful signal here.
	if result != nil {
		return result.ExitCode == 0, nil
	}
	return false, fmt.Errorf("exec: systemd: is-active %s: %w", service, err)
}

func (s *SystemdProvider) IsEnabled(ctx context.Context, service string) (bool, error) {
	result, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"is-enabled", "--quiet", service},
	})
	// systemctl is-enabled uses exit codes: 0=enabled, non-zero=disabled.
	if result != nil {
		return result.ExitCode == 0, nil
	}
	return false, fmt.Errorf("exec: systemd: is-enabled %s: %w", service, err)
}

func (s *SystemdProvider) Start(ctx context.Context, service string) error {
	_, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"start", service},
	})
	if err != nil {
		return fmt.Errorf("exec: systemd: start %s: %w", service, err)
	}
	return nil
}

func (s *SystemdProvider) Stop(ctx context.Context, service string) error {
	_, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"stop", service},
	})
	if err != nil {
		return fmt.Errorf("exec: systemd: stop %s: %w", service, err)
	}
	return nil
}

func (s *SystemdProvider) Restart(ctx context.Context, service string) error {
	_, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"restart", service},
	})
	if err != nil {
		return fmt.Errorf("exec: systemd: restart %s: %w", service, err)
	}
	return nil
}

func (s *SystemdProvider) Enable(ctx context.Context, service string) error {
	_, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"enable", service},
	})
	if err != nil {
		return fmt.Errorf("exec: systemd: enable %s: %w", service, err)
	}
	return nil
}

func (s *SystemdProvider) Disable(ctx context.Context, service string) error {
	_, err := s.cmd.Run(ctx, CommandOpts{
		Command: "systemctl",
		Args:    []string{"disable", service},
	})
	if err != nil {
		return fmt.Errorf("exec: systemd: disable %s: %w", service, err)
	}
	return nil
}
