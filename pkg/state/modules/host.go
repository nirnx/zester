package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// defaultHostsPath is the default path to the hosts file.
const defaultHostsPath = "/etc/hosts"

// HostPresent implements the host.present state.
// It ensures a hostname is mapped to a given IP address in /etc/hosts,
// adding it to an existing line for that IP or creating a new line, and
// removing the hostname from lines that map it to a different IP.
type HostPresent struct {
	id   string
	reqs state.Requisites

	// Hostname is the host name to manage.
	Hostname string

	// IP is the IP address the hostname should map to.
	IP string

	// Path is the hosts file path (default /etc/hosts).
	Path string

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence.
	// A fresh instance (the runner builds states fresh per execution, so
	// ModeRevert always is one) leaves them unset and Revert is an explicit
	// clean no-op — see revertHostsFile.
	backup    []byte
	backupSet bool
	created   bool
}

// NewHostPresentBuilder returns a state.Builder that creates HostPresent states.
func NewHostPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("host.present: no file provider available")
		}
		return newHostPresent(id, config, mctx.File)
	}
}

func newHostPresent(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	h := &HostPresent{id: id, file: file}

	h.Hostname, _ = config["name"].(string)
	if h.Hostname == "" {
		h.Hostname = id
	}

	h.IP, _ = config["ip"].(string)
	if h.IP == "" {
		return nil, fmt.Errorf("host.present: %s: ip is required", id)
	}

	h.Path = hostsPath(config)

	h.reqs = state.ParseRequisites(config)

	return h, nil
}

func (h *HostPresent) Name() string           { return "host.present:" + h.id }
func (h *HostPresent) Reqs() state.Requisites { return h.reqs }

func (h *HostPresent) Check(ctx context.Context) (state.CheckResult, error) {
	current, _, err := readManagedFile(ctx, h.file, h.Path)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("host.present: read %s: %w", h.Path, err)
	}
	desired := renderHostPresent(current, h.IP, h.Hostname)
	if desired == current {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s should map to %s in %s", h.Hostname, h.IP, h.Path),
	}, nil
}

func (h *HostPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	current, existed, err := readManagedFile(ctx, h.file, h.Path)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("host.present: read %s: %w", h.Path, err)
	}
	h.backup = []byte(current)
	h.backupSet = existed
	h.created = !existed

	desired := renderHostPresent(current, h.IP, h.Hostname)
	if desired == current {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := h.file.WriteFile(ctx, h.Path, []byte(desired), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("host.present: write %s: %w", h.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("mapped %s -> %s in %s", h.Hostname, h.IP, h.Path),
		Details: map[string]string{
			"hostname": h.Hostname,
			"ip":       h.IP,
			"path":     h.Path,
		},
	}, nil
}

func (h *HostPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	return revertHostsFile(ctx, h.file, h.Path, h.backup, h.backupSet, h.created, 0644, "host.present")
}

// HostAbsent implements the host.absent state.
// It ensures a hostname is not present in /etc/hosts.
type HostAbsent struct {
	id   string
	reqs state.Requisites

	Hostname string
	Path     string

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence;
	// unset memos make Revert an explicit clean no-op (see revertHostsFile).
	backup    []byte
	backupSet bool
}

// NewHostAbsentBuilder returns a state.Builder that creates HostAbsent states.
func NewHostAbsentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("host.absent: no file provider available")
		}
		return newHostAbsent(id, config, mctx.File)
	}
}

func newHostAbsent(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	h := &HostAbsent{id: id, file: file}

	h.Hostname, _ = config["name"].(string)
	if h.Hostname == "" {
		h.Hostname = id
	}

	h.Path = hostsPath(config)

	h.reqs = state.ParseRequisites(config)

	return h, nil
}

func (h *HostAbsent) Name() string           { return "host.absent:" + h.id }
func (h *HostAbsent) Reqs() state.Requisites { return h.reqs }

func (h *HostAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	current, existed, err := readManagedFile(ctx, h.file, h.Path)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("host.absent: read %s: %w", h.Path, err)
	}
	if !existed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	desired := renderHostAbsent(current, h.Hostname)
	if desired == current {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s should be absent from %s", h.Hostname, h.Path),
	}, nil
}

func (h *HostAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	current, existed, err := readManagedFile(ctx, h.file, h.Path)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("host.absent: read %s: %w", h.Path, err)
	}
	if !existed {
		return state.ApplyResult{Changed: false}, nil
	}
	h.backup = []byte(current)
	h.backupSet = true

	desired := renderHostAbsent(current, h.Hostname)
	if desired == current {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := h.file.WriteFile(ctx, h.Path, []byte(desired), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("host.absent: write %s: %w", h.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s from %s", h.Hostname, h.Path),
		Details: map[string]string{
			"hostname": h.Hostname,
			"path":     h.Path,
		},
	}, nil
}

func (h *HostAbsent) Revert(ctx context.Context) (state.ApplyResult, error) {
	return revertHostsFile(ctx, h.file, h.Path, h.backup, h.backupSet, false, 0644, "host.absent")
}

// hostsPath resolves the hosts file path from config, defaulting to /etc/hosts.
// Accepts "config" or "path" as overrides (primarily for testing).
func hostsPath(config map[string]any) string {
	if p, ok := config["config"].(string); ok && p != "" {
		return p
	}
	if p, ok := config["path"].(string); ok && p != "" {
		return p
	}
	return defaultHostsPath
}

// renderHostPresent returns the hosts-file content with hostname mapped to ip.
// The hostname is added to an existing line for ip, or a new line is appended.
// Any occurrence of hostname on a line for a different ip is removed.
func renderHostPresent(content, ip, hostname string) string {
	lines := splitHostLines(content)
	found := false
	out := make([]string, 0, len(lines)+1)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(trimmed)
		lineIP := fields[0]
		names := fields[1:]

		if lineIP == ip {
			if containsString(names, hostname) {
				found = true
				out = append(out, line)
			} else {
				names = append(names, hostname)
				found = true
				out = append(out, lineIP+"\t"+strings.Join(names, " "))
			}
			continue
		}

		// Different IP: drop hostname if present here.
		newNames := dropString(names, hostname)
		if len(newNames) == 0 {
			continue
		}
		if len(newNames) != len(names) {
			out = append(out, lineIP+"\t"+strings.Join(newNames, " "))
		} else {
			out = append(out, line)
		}
	}

	if !found {
		out = append(out, ip+"\t"+hostname)
	}
	return joinHostLines(out)
}

// renderHostAbsent returns the hosts-file content with hostname removed.
func renderHostAbsent(content, hostname string) string {
	lines := splitHostLines(content)
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(trimmed)
		lineIP := fields[0]
		names := fields[1:]

		newNames := dropString(names, hostname)
		if len(newNames) == 0 {
			continue
		}
		if len(newNames) != len(names) {
			out = append(out, lineIP+"\t"+strings.Join(newNames, " "))
		} else {
			out = append(out, line)
		}
	}
	return joinHostLines(out)
}

// splitHostLines splits content into lines, dropping a single trailing newline.
func splitHostLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// joinHostLines joins lines with newlines and ensures a trailing newline.
func joinHostLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// readManagedFile reads a line-managed file, distinguishing a genuinely
// absent file from a failed read: fs.ErrNotExist is the ONLY absent signal
// (returned as "", false, nil); any other read error is returned to the
// caller so the phase FAILS instead of treating unreadable content as empty.
// Conflating the two would let a transient read failure (EIO, ESTALE, EACCES)
// followed by a successful write truncate /etc/hosts or authorized_keys down
// to just the managed line.
func readManagedFile(ctx context.Context, file exec.FileExec, path string) (content string, existed bool, err error) {
	data, err := file.ReadFile(ctx, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// revertHostsFile undoes a same-instance Apply on a line-managed file using
// the memos Apply recorded: remove the file when Apply created it, restore
// the backup (with the module's canonical permissions) when Apply captured
// one. When NO memo is set — Revert on a fresh instance where Apply never
// ran (the runner builds states fresh per execution, so ModeRevert always
// hits this) or Apply failed before reading — it is an explicit clean no-op:
// deleting or rewriting a shared file like /etc/hosts or authorized_keys to
// "revert" an unrecorded apply is never safe.
func revertHostsFile(ctx context.Context, file exec.FileExec, path string, backup []byte, backupSet, created bool, perm fs.FileMode, mod string) (state.ApplyResult, error) {
	switch {
	case created:
		if err := file.Remove(ctx, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("%s: revert remove %s: %w", mod, path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert; created by this apply)", path),
		}, nil
	case backupSet:
		if err := file.WriteFile(ctx, path, backup, perm); err != nil {
			return state.ApplyResult{}, fmt.Errorf("%s: revert %s: %w", mod, path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted %s to previous content", path),
		}, nil
	default:
		return state.ApplyResult{
			Changed: false,
			Diff:    mod + ": nothing to revert (no apply recorded in this run)",
		}, nil
	}
}

// dropString returns ss with all occurrences of s removed.
func dropString(ss []string, s string) []string {
	out := make([]string, 0, len(ss))
	for _, v := range ss {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
