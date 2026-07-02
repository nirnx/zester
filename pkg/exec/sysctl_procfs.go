package exec

import (
	"context"
	"fmt"
	"strings"
)

// SysctlConfPath is the drop-in sysctl configuration file managed by Zester.
const SysctlConfPath = "/etc/sysctl.d/99-zester.conf"

// ProcfsProvider implements SysctlExec using the sysctl(8) command and
// a drop-in file under /etc/sysctl.d/ for persistence.
type ProcfsProvider struct {
	cmd  CommandExec
	file FileExec
}

// NewProcfsProvider creates a ProcfsProvider with the given providers.
func NewProcfsProvider(cmd CommandExec, file FileExec) *ProcfsProvider {
	return &ProcfsProvider{cmd: cmd, file: file}
}

// Get returns the current runtime value of a sysctl key.
func (p *ProcfsProvider) Get(ctx context.Context, key string) (string, error) {
	res, err := p.cmd.Run(ctx, CommandOpts{
		Command: "sysctl",
		Args:    []string{"-n", key},
	})
	if err != nil {
		return "", fmt.Errorf("exec: sysctl: get %q: %w", key, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Set applies key=value at runtime via `sysctl -w`.
func (p *ProcfsProvider) Set(ctx context.Context, key, value string) error {
	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "sysctl",
		Args:    []string{"-w", key + "=" + value},
	})
	if err != nil {
		return fmt.Errorf("exec: sysctl: set %q=%q: %w", key, value, err)
	}
	return nil
}

// Persist writes key=value to the Zester sysctl drop-in file so the setting
// survives reboots. Existing occurrences of the key are replaced in-place.
func (p *ProcfsProvider) Persist(ctx context.Context, key, value string) error {
	existing, err := p.file.ReadFile(ctx, SysctlConfPath)
	if err != nil {
		// File does not exist yet — start with empty content.
		existing = nil
	}

	line := key + " = " + value
	var out []string
	replaced := false

	for _, l := range strings.Split(string(existing), "\n") {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, key+"=") || strings.HasPrefix(trimmed, key+" =") ||
			strings.HasPrefix(trimmed, key+"\t=") {
			if !replaced {
				out = append(out, line)
				replaced = true
			}
			// skip duplicate lines
		} else {
			out = append(out, l)
		}
	}
	if !replaced {
		out = append(out, line)
	}

	// Trim trailing blank lines then add a single newline.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	content := strings.Join(out, "\n") + "\n"

	if err := p.file.WriteFile(ctx, SysctlConfPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("exec: sysctl: persist %q: %w", key, err)
	}
	return nil
}
