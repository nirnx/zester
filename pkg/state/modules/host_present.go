package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// HostPresent implements the host.present state.
// It ensures a hostname is mapped to a given IP address in /etc/hosts,
// adding it to an existing line for that IP or creating a new line, and
// removing the hostname from lines that map it to a different IP.
//
// HostPresent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `ip` is
// `required`; `Path` is the per-field ALIAS exemplar — the hosts-file path binds
// the `config` key with a `path` alias and an EAGER `default=/etc/hosts`, so
// `config` wins over `path` and either wins over the default, reproducing the
// legacy `config`>`path`>`/etc/hosts` precedence exactly. Under the uniform
// decoder a numeric `name`/`ip` coerces to its string form and a composite is
// rejected (BD-6) where the legacy `.(string)` assertions silently dropped them.
// The unexported runtime fields (id, reqs, file, revert memos) are untagged, so
// the schema compiler skips them.
type HostPresent struct {
	id   string
	reqs state.Requisites

	// Hostname is the host name to manage; it defaults to the state ID.
	Hostname string `zester:"name,primary" usage:"host name to manage; defaults to the state ID"`
	// IP is the IP address the hostname should map to; required.
	IP string `zester:"ip,required" usage:"IP address the hostname should map to; required"`
	// Path: host.* family hosts-file component.
	hostFileParam

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence.
	// A fresh instance (the runner builds states fresh per execution, so
	// ModeRevert always is one) leaves them unset and Revert is an explicit
	// clean no-op — see revertHostsFile.
	backup    []byte
	backupSet bool
	created   bool
}

// hostPresentSpec is the compiled schema + documentation for host.present. Its
// Doc is drift-corrected against the live Check/Apply/Revert behavior — notably
// that a hostname is MOVED off any other IP's line (not just added).
var hostPresentSpec = mustSpec("host.present", modschema.KindState, HostPresent{}, modschema.Doc{
	Summary: "Ensure a hostname maps to an IP address in the hosts file.",
	Description: "`host.present` ensures a hostname (`name`, defaulting to the state ID) is mapped to `ip` " +
		"in the hosts file (`/etc/hosts` by default; override with `config` or its `path` alias). It adds " +
		"the hostname to an existing line for that IP or appends a new line, and REMOVES the hostname " +
		"from any line that maps it to a different IP — so a host is never listed under two addresses. " +
		"`ip` is required.",
	Effects: modschema.Effects{
		Check: "Reads the hosts file (a non-not-exist read error fails the phase rather than risking a " +
			"blind overwrite) and computes the desired content. Reports a change when the desired mapping " +
			"is not already present — the hostname is absent, mapped to a different IP, or on a line that " +
			"would be rewritten.",
		Apply: "Reads the hosts file, memoizes its prior content (or that it did not exist) for revert, " +
			"and rewrites it so the hostname maps to `ip`: the hostname is added to the existing line for " +
			"that IP or a new line is appended, and it is dropped from any line for a different IP (a line " +
			"left with no names is removed). An already-correct mapping is a clean no-op. Reports the " +
			"hostname, IP, and path in its details.",
		Revert: "Restores what this run's Apply changed: a hosts file that pre-existed is rewritten with " +
			"its captured prior content; a file this instance created is removed. A fresh instance (a " +
			"standalone revert) recorded nothing and is an explicit clean no-op — it never rewrites or " +
			"deletes a hosts file it did not touch.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Map a hostname to an IP",
			Kind:        "state",
			Explanation: "The hostname defaults to the state ID; ip is required.",
			Code:        "web1:\n  host.present:\n    - ip: 10.0.0.5\n",
		},
		{
			Title:       "Manage an alternate hosts file",
			Kind:        "state",
			Explanation: "config (or its path alias) overrides the default /etc/hosts.",
			Code:        "db-primary:\n  host.present:\n    - ip: 10.0.0.9\n    - config: /etc/hosts\n",
		},
		{
			Title:       "Add a host mapping ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the hostname; ip is a key=value.",
			Code:        "zester '*' host.present web1 ip=10.0.0.5",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "A hostname is moved, not duplicated",
			Body: "If the hostname already appears on a line for a DIFFERENT IP, host.present removes it " +
				"there and maps it to the declared IP, so a name is never resolvable to two addresses at " +
				"once. A line left with no remaining names is dropped.",
		},
		{
			Level: "info",
			Title: "The hosts-file path: config, path, or the default",
			Body: "The managed file defaults to `/etc/hosts`. Override it with `config`; the legacy `path` " +
				"alias is also accepted. `config` takes precedence over `path`, and either takes precedence " +
				"over the `/etc/hosts` default.",
		},
		{
			Level: "info",
			Title: "Comment and blank lines are preserved",
			Body: "The hosts file is rewritten in memory: existing comment (`#`) and blank lines are kept " +
				"verbatim. The hostname is added to the existing line for its IP or appended as a new " +
				"tab-separated line for that IP, and a missing hosts file is created.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "`host.present` enforces exactly ONE IP per hostname — any line mapping the hostname to a " +
				"different IP has the name removed — whereas Salt's `host.present` accepts a LIST of IPs and " +
				"only cleans up other mappings under `clean: True`. Salt's per-entry `comment` parameter is " +
				"not supported.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"host.absent"},
})

// NewHostPresentBuilder returns a state.Builder that creates HostPresent states
// using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewHostPresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("host.present: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		h := &HostPresent{}
		if _, err := hostPresentSpec.Decode(id, config, h, opts); err != nil {
			return nil, fmt.Errorf("host.present: %w", err)
		}
		h.id = id
		h.file = mctx.File
		h.reqs = state.ParseRequisites(config)
		return h, nil
	}
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
