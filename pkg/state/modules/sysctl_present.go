package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// SysctlPresent implements the sysctl.present state.
// It ensures a kernel parameter is set to the desired value at runtime
// and optionally persisted across reboots. With persist (the default) the
// drop-in file entry is verified too — a runtime-only match (e.g. a manual
// `sysctl -w`) is drift, since the value would not survive a reboot.
//
// SysctlPresent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `value` is
// `required` and `persist` carries an eager `default=true`, reproducing the legacy
// construction-time behavior — but through the uniform decoder, so a numeric
// `value`/`name` coerces to its string form (BD-6) and an integer/truthy-string
// `persist` is honored (BD-2/BD-7) where the legacy `.(string)`/`.(bool)`
// assertions silently dropped them. The require-file-provider-when-persist rule
// stays in the builder tail (it is cross-field module logic, not schema). The
// unexported runtime fields (id, reqs, sysctl, file, revert memos) are untagged,
// so the schema compiler skips them.
type SysctlPresent struct {
	id   string
	reqs state.Requisites

	// Key is the kernel parameter name; it defaults to the state ID.
	Key string `zester:"name,primary" usage:"kernel parameter name (e.g. net.ipv4.ip_forward); defaults to the state ID"`
	// Value is the desired parameter value; required.
	Value string `zester:"value,required" usage:"desired parameter value; required"`
	// Persist records the value in a sysctl drop-in so it survives a reboot.
	Persist bool `zester:"persist,default=true" usage:"persist the value in a sysctl drop-in so it survives a reboot; defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	sysctl exec.SysctlExec
	// file verifies the persist drop-in (exec.SysctlConfPath): SysctlExec
	// has no persist read-back, so the file is read via the file provider.
	// Required only when Persist is enabled.
	file exec.FileExec

	// Revert memos — armed ONLY for the facets Apply actually changed, so
	// Revert undoes exactly those and nothing else (a persist-only Apply must
	// not touch the runtime value on revert, and vice versa).
	appliedSet bool   // Apply changed the runtime value
	original   string // pre-Apply runtime value (valid when appliedSet)

	appliedPersist  bool   // Apply wrote the drop-in entry
	persistHadEntry bool   // the drop-in had an entry for the key before Apply
	persistOriginal string // its value (valid when persistHadEntry)
}

// sysctlPresentSpec is the compiled schema + documentation for sysctl.present. Its
// Doc is drift-corrected against the live Check/Apply/Revert behavior — notably
// the persist facet (a runtime-only match is drift) and the exact revert memoing.
var sysctlPresentSpec = mustSpec("sysctl.present", modschema.KindState, SysctlPresent{}, modschema.Doc{
	Summary: "Ensure a kernel parameter is set to a value at runtime and, by default, persisted.",
	Description: "`sysctl.present` ensures a kernel parameter (`name`, defaulting to the state ID) is set " +
		"to the desired `value` at runtime and, with `persist` (the default), that the same value is " +
		"recorded in the Zester sysctl drop-in (`/etc/sysctl.d/99-zester.conf`) so it survives a reboot. `value` is required. With `persist` " +
		"enabled a runtime-only match — for example a manual `sysctl -w` — counts as drift, because the " +
		"value would not survive a reboot.",
	Effects: modschema.Effects{
		Check: "Reads the current runtime value (via `sysctl -n`) and reports a change when it differs from " +
			"`value`. With `persist` (the default) it ALSO reads the Zester sysctl drop-in " +
			"(`/etc/sysctl.d/99-zester.conf`) and reports a change when the " +
			"key is absent from it or persisted with a different value — so a runtime-only match is drift. " +
			"A non-not-exist read error on the drop-in fails the phase rather than being treated as \"no " +
			"entry\".",
		Apply: "Re-reads the runtime value and, with `persist`, the drop-in (a self-contained flow: a " +
			"watch-forced Apply bypasses Check). It sets the runtime value (via `sysctl -w key=value`) only when it differs and writes " +
			"the drop-in entry (`/etc/sysctl.d/99-zester.conf`) only when it is missing or wrong — each " +
			"facet is memoized for revert " +
			"independently. A fully converged key is a clean no-op. Reports the key, value, previous " +
			"runtime value, and persist flag in its details.",
		Revert: "Undoes only the facets this run's Apply actually changed. A runtime value Apply set is " +
			"restored to its pre-Apply value; a drop-in entry Apply changed is restored to its prior value " +
			"(or removed outright when Apply introduced it) — never re-persisting the old RUNTIME value. A " +
			"fresh instance (a standalone revert) recorded nothing and is an explicit clean no-op; it never " +
			"writes the zero value or invents a persist entry.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Enable IP forwarding persistently",
			Kind:        "state",
			Explanation: "The parameter name defaults to the state ID; persist (default true) writes the drop-in entry.",
			Code:        "net.ipv4.ip_forward:\n  sysctl.present:\n    - value: \"1\"\n",
		},
		{
			Title:       "Tune swappiness at runtime only",
			Kind:        "state",
			Explanation: "persist: false sets the runtime value without recording a drop-in entry.",
			Code:        "vm.swappiness:\n  sysctl.present:\n    - value: \"10\"\n    - persist: false\n",
		},
		{
			Title:       "Set a kernel parameter ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the parameter name; value is a key=value.",
			Code:        "zester '*' sysctl.present net.ipv4.ip_forward value=1",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "persist defaults to true and verifies the drop-in",
			Body: "`persist` defaults to true: the value is written to the Zester sysctl drop-in (`/etc/sysctl.d/99-zester.conf`) AND that " +
				"entry is verified during Check, so a runtime-only change (a manual `sysctl -w`) reads as " +
				"drift because it would not survive a reboot. Set `persist: false` to manage only the " +
				"runtime value. Under the uniform decoder an integer or truthy-string `persist` (`1`, " +
				"`\"true\"`) is honored where the legacy `.(bool)` assertion dropped it.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
})

// NewSysctlPresentBuilder returns a state.Builder that creates SysctlPresent states
// using the given ModuleContext's sysctl (and, when persist is enabled, file)
// provider. Decode policy (unknown-key handling, reserved keys) is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewSysctlPresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Sysctl == nil {
			return nil, fmt.Errorf("sysctl.present: no sysctl provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it.
		s := &SysctlPresent{}
		if _, err := sysctlPresentSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("sysctl.present: %w", err)
		}
		s.id = id
		s.sysctl = mctx.Sysctl
		s.file = mctx.File
		s.reqs = state.ParseRequisites(config)

		// Cross-field module logic (not schema): the persist facet reads the
		// drop-in via the file provider, so a nil file provider is only fatal
		// when persist is enabled.
		if s.Persist && s.file == nil {
			return nil, fmt.Errorf("sysctl.present: %s: no file provider available (required to verify %s)", id, exec.SysctlConfPath)
		}

		return s, nil
	}
}

func (s *SysctlPresent) Name() string           { return "sysctl.present:" + s.id }
func (s *SysctlPresent) Reqs() state.Requisites { return s.reqs }

// persistedValue returns the value recorded for the key in the Zester sysctl
// drop-in (exec.SysctlConfPath) and whether an entry exists. A missing file
// means "no entry" (fs.ErrNotExist is the ONLY absent signal); any other
// read error fails the phase. The last matching line wins, matching how the
// kernel applies the file.
func (s *SysctlPresent) persistedValue(ctx context.Context) (string, bool, error) {
	data, err := s.file.ReadFile(ctx, exec.SysctlConfPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("sysctl.present: read %s: %w", exec.SysctlConfPath, err)
	}

	val, found := "", false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == s.Key {
			val, found = strings.TrimSpace(v), true
		}
	}
	return val, found, nil
}

func (s *SysctlPresent) Check(ctx context.Context) (state.CheckResult, error) {
	current, err := s.sysctl.Get(ctx, s.Key)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("sysctl.present: get %s: %w", s.Key, err)
	}

	var diffs []string
	if current != s.Value {
		diffs = append(diffs, fmt.Sprintf("%s: %q != %q", s.Key, current, s.Value))
	}

	if s.Persist {
		pv, found, err := s.persistedValue(ctx)
		if err != nil {
			return state.CheckResult{}, err
		}
		if !found {
			diffs = append(diffs, fmt.Sprintf("%s: not persisted in %s", s.Key, exec.SysctlConfPath))
		} else if pv != s.Value {
			diffs = append(diffs, fmt.Sprintf("%s: persisted %q != %q", s.Key, pv, s.Value))
		}
	}

	if len(diffs) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        strings.Join(diffs, "; "),
		}, nil
	}
	return state.CheckResult{NeedsChange: false}, nil
}

func (s *SysctlPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	current, err := s.sysctl.Get(ctx, s.Key)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("sysctl.present: get %s: %w", s.Key, err)
	}

	setNeeded := current != s.Value
	persistNeeded := false
	persistedVal, persistedFound := "", false
	if s.Persist {
		pv, found, err := s.persistedValue(ctx)
		if err != nil {
			return state.ApplyResult{}, err
		}
		persistedVal, persistedFound = pv, found
		persistNeeded = !found || pv != s.Value
	}

	if !setNeeded && !persistNeeded {
		// Self-contained no-op: a watch-forced Apply on a converged key must
		// not rewrite anything or report a change.
		return state.ApplyResult{
			Changed: false,
			Diff:    fmt.Sprintf("%s already %q", s.Key, s.Value),
		}, nil
	}

	if setNeeded {
		if err := s.sysctl.Set(ctx, s.Key, s.Value); err != nil {
			return state.ApplyResult{}, fmt.Errorf("sysctl.present: set %s: %w", s.Key, err)
		}
		s.original = current
		s.appliedSet = true
	}

	if persistNeeded {
		if err := s.sysctl.Persist(ctx, s.Key, s.Value); err != nil {
			return state.ApplyResult{}, fmt.Errorf("sysctl.present: persist %s: %w", s.Key, err)
		}
		s.persistOriginal = persistedVal
		s.persistHadEntry = persistedFound
		s.appliedPersist = true
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s: %q -> %q", s.Key, current, s.Value),
		Details: map[string]string{
			"key":       s.Key,
			"value":     s.Value,
			"previous":  current,
			"persisted": fmt.Sprintf("%v", s.Persist),
		},
	}, nil
}

// removePersistEntry deletes the key's lines from the Zester drop-in,
// mirroring ProcfsProvider.Persist's key matching. A missing file or an
// absent entry means there is nothing to remove.
func (s *SysctlPresent) removePersistEntry(ctx context.Context) error {
	data, err := s.file.ReadFile(ctx, exec.SysctlConfPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sysctl.present: read %s: %w", exec.SysctlConfPath, err)
	}

	var out []string
	removed := false
	for _, l := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, s.Key+"=") || strings.HasPrefix(trimmed, s.Key+" =") ||
			strings.HasPrefix(trimmed, s.Key+"\t=") {
			removed = true
			continue
		}
		out = append(out, l)
	}
	if !removed {
		return nil
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	content := ""
	if len(out) > 0 {
		content = strings.Join(out, "\n") + "\n"
	}
	if err := s.file.WriteFile(ctx, exec.SysctlConfPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("sysctl.present: rewrite %s: %w", exec.SysctlConfPath, err)
	}
	return nil
}

func (s *SysctlPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !s.appliedSet && !s.appliedPersist {
		// Fresh instance or converged Apply: nothing was changed this run —
		// never write the zero value or invent a persist entry.
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}

	var acts []string
	if s.appliedSet {
		if err := s.sysctl.Set(ctx, s.Key, s.original); err != nil {
			return state.ApplyResult{}, fmt.Errorf("sysctl.present: revert set %s: %w", s.Key, err)
		}
		acts = append(acts, fmt.Sprintf("runtime reverted to %q", s.original))
	}
	if s.appliedPersist {
		// Undo the persist facet Apply changed — never re-persist the old
		// RUNTIME value: a pre-existing entry is restored to its prior value,
		// and an entry Apply introduced is removed outright.
		if s.persistHadEntry {
			if err := s.sysctl.Persist(ctx, s.Key, s.persistOriginal); err != nil {
				return state.ApplyResult{}, fmt.Errorf("sysctl.present: revert persist %s: %w", s.Key, err)
			}
			acts = append(acts, fmt.Sprintf("persist entry restored to %q", s.persistOriginal))
		} else {
			if err := s.removePersistEntry(ctx); err != nil {
				return state.ApplyResult{}, err
			}
			acts = append(acts, "persist entry removed")
		}
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s: %s (revert)", s.Key, strings.Join(acts, "; ")),
	}, nil
}
