package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// HostAbsent implements the host.absent state.
// It ensures a hostname is not present in /etc/hosts.
//
// HostAbsent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `Path` is
// the per-field ALIAS exemplar (the `config` key with a `path` alias and an
// eager `default=/etc/hosts`, reproducing the legacy `config`>`path`>`/etc/hosts`
// precedence). Under the uniform decoder a numeric `name` coerces to its string
// form and a composite is rejected (BD-6). The unexported runtime fields (id,
// reqs, file, revert memos) are untagged, so the schema compiler skips them.
type HostAbsent struct {
	id   string
	reqs state.Requisites

	// Hostname is the host name to remove; it defaults to the state ID.
	Hostname string `zester:"name,primary" usage:"host name to remove from the hosts file; defaults to the state ID"`
	// Path is the hosts file path; the config key with a path alias, default /etc/hosts.
	Path string `zester:"config,aliases=path,default=/etc/hosts" usage:"hosts file path; the path alias is also accepted; defaults to /etc/hosts"`

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence;
	// unset memos make Revert an explicit clean no-op (see revertHostsFile).
	backup    []byte
	backupSet bool
}

// hostAbsentSpec is the compiled schema + documentation for host.absent.
var hostAbsentSpec = mustSpec("host.absent", modschema.KindState, HostAbsent{}, modschema.Doc{
	Summary: "Ensure a hostname is absent from the hosts file.",
	Description: "`host.absent` ensures a hostname (`name`, defaulting to the state ID) does not appear in " +
		"the hosts file (`/etc/hosts` by default; override with `config` or its `path` alias). The " +
		"hostname is removed from every line it appears on; a line left with no remaining names is " +
		"dropped.",
	Effects: modschema.Effects{
		Check: "Reads the hosts file (a non-not-exist read error fails the phase). Reports no change when " +
			"the file does not exist or the hostname is already absent; otherwise reports a change.",
		Apply: "Reads the hosts file and, when the hostname is present, memoizes the prior content for " +
			"revert and rewrites the file with the hostname removed from every line (dropping any line " +
			"left with no names). A missing file or an already-absent hostname is a clean no-op. Reports " +
			"the hostname and path in its details.",
		Revert: "Restores the hosts file to the content this run's Apply captured before removing the " +
			"hostname. A fresh instance (a standalone revert) recorded nothing and is an explicit clean " +
			"no-op — it never rewrites a hosts file it did not touch.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Remove a hostname",
			Kind:        "state",
			Explanation: "The hostname defaults to the state ID.",
			Code:        "old-host:\n  host.absent: []\n",
		},
		{
			Title:       "Remove a host mapping ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the hostname.",
			Code:        "zester '*' host.absent old-host",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Comment and blank lines are preserved",
			Body: "The hosts file is rewritten in memory with existing comment (`#`) and blank lines kept " +
				"verbatim; the hostname is removed from every line it appears on and a line left with no " +
				"remaining names is dropped. A missing hosts file is a clean no-op.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"host.present"},
})

// NewHostAbsentBuilder returns a state.Builder that creates HostAbsent states
// using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewHostAbsentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("host.absent: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		h := &HostAbsent{}
		if _, err := hostAbsentSpec.Decode(id, config, h, opts); err != nil {
			return nil, fmt.Errorf("host.absent: %w", err)
		}
		h.id = id
		h.file = mctx.File
		h.reqs = state.ParseRequisites(config)
		return h, nil
	}
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
