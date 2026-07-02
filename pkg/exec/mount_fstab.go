package exec

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const (
	// ProcMountsPath is the kernel's active mount list.
	ProcMountsPath = "/proc/mounts"
	// FstabPath is the system filesystem table.
	FstabPath = "/etc/fstab"
)

// FstabProvider implements MountExec using mount(8)/umount(8) and /etc/fstab.
type FstabProvider struct {
	cmd  CommandExec
	file FileExec
}

// NewFstabProvider creates a FstabProvider with the given providers.
func NewFstabProvider(cmd CommandExec, file FileExec) *FstabProvider {
	return &FstabProvider{cmd: cmd, file: file}
}

// IsMounted reports whether mountPoint appears in /proc/mounts.
func (p *FstabProvider) IsMounted(ctx context.Context, mountPoint string) (bool, error) {
	data, err := p.file.ReadFile(ctx, ProcMountsPath)
	if err != nil {
		return false, fmt.Errorf("exec: mount: read /proc/mounts: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == mountPoint {
			return true, nil
		}
	}
	return false, nil
}

// Mount mounts the filesystem described by entry.
func (p *FstabProvider) Mount(ctx context.Context, entry MountEntry) error {
	args := []string{"-t", entry.FSType}
	if entry.Options != "" && entry.Options != "defaults" {
		args = append(args, "-o", entry.Options)
	}
	args = append(args, entry.Device, entry.MountPoint)

	_, err := p.cmd.Run(ctx, CommandOpts{Command: "mount", Args: args})
	if err != nil {
		return fmt.Errorf("exec: mount: mount %s -> %s: %w", entry.Device, entry.MountPoint, err)
	}
	return nil
}

// Unmount unmounts the given mount point.
func (p *FstabProvider) Unmount(ctx context.Context, mountPoint string) error {
	_, err := p.cmd.Run(ctx, CommandOpts{
		Command: "umount",
		Args:    []string{mountPoint},
	})
	if err != nil {
		return fmt.Errorf("exec: mount: umount %s: %w", mountPoint, err)
	}
	return nil
}

// GetFstab returns the fstab entry for mountPoint, or nil if not found.
func (p *FstabProvider) GetFstab(ctx context.Context, mountPoint string) (*MountEntry, error) {
	entries, err := p.readFstab(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.MountPoint == mountPoint {
			cp := e
			return &cp, nil
		}
	}
	return nil, nil
}

// SetFstab adds or replaces the fstab entry for entry.MountPoint.
func (p *FstabProvider) SetFstab(ctx context.Context, entry MountEntry) error {
	data, err := p.file.ReadFile(ctx, FstabPath)
	if err != nil {
		data = nil // file may not exist yet
	}

	line := fstabLine(entry)
	var out []string
	replaced := false

	for _, l := range strings.Split(string(data), "\n") {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[1] == entry.MountPoint && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			if !replaced {
				out = append(out, line)
				replaced = true
			}
		} else {
			out = append(out, l)
		}
	}
	if !replaced {
		out = append(out, line)
	}

	// Trim trailing blanks.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	content := strings.Join(out, "\n") + "\n"

	if err := p.file.WriteFile(ctx, FstabPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("exec: mount: write fstab for %s: %w", entry.MountPoint, err)
	}
	return nil
}

// RemoveFstab removes the fstab entry for mountPoint.
func (p *FstabProvider) RemoveFstab(ctx context.Context, mountPoint string) error {
	data, err := p.file.ReadFile(ctx, FstabPath)
	if err != nil {
		return nil // nothing to remove
	}

	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[1] == mountPoint && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		out = append(out, l)
	}

	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	content := strings.Join(out, "\n") + "\n"

	if err := p.file.WriteFile(ctx, FstabPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("exec: mount: remove fstab for %s: %w", mountPoint, err)
	}
	return nil
}

// readFstab parses /etc/fstab into MountEntry values.
func (p *FstabProvider) readFstab(ctx context.Context) ([]MountEntry, error) {
	data, err := p.file.ReadFile(ctx, FstabPath)
	if err != nil {
		return nil, fmt.Errorf("exec: mount: read fstab: %w", err)
	}
	return parseFstab(string(data)), nil
}

// parseFstab parses fstab-format text.
func parseFstab(content string) []MountEntry {
	var entries []MountEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		dump, pass := 0, 0
		if len(fields) >= 5 {
			dump, _ = strconv.Atoi(fields[4])
		}
		if len(fields) >= 6 {
			pass, _ = strconv.Atoi(fields[5])
		}
		entries = append(entries, MountEntry{
			Device:     fields[0],
			MountPoint: fields[1],
			FSType:     fields[2],
			Options:    fields[3],
			Dump:       dump,
			Pass:       pass,
		})
	}
	return entries
}

// fstabLine formats a MountEntry as a single fstab line.
func fstabLine(e MountEntry) string {
	opts := e.Options
	if opts == "" {
		opts = "defaults"
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%d %d",
		e.Device, e.MountPoint, e.FSType, opts, e.Dump, e.Pass)
}
