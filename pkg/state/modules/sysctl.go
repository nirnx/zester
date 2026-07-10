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

// SysctlPresent implements the sysctl.present state.
// It ensures a kernel parameter is set to the desired value at runtime
// and optionally persisted across reboots. With persist (the default) the
// drop-in file entry is verified too — a runtime-only match (e.g. a manual
// `sysctl -w`) is drift, since the value would not survive a reboot.
type SysctlPresent struct {
	id   string
	reqs state.Requisites

	Key     string
	Value   string
	Persist bool

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

// NewSysctlPresentBuilder returns a state.Builder that creates SysctlPresent states.
func NewSysctlPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Sysctl == nil {
			return nil, fmt.Errorf("sysctl.present: no sysctl provider available")
		}
		return newSysctlPresent(id, config, mctx.Sysctl, mctx.File)
	}
}

func newSysctlPresent(id string, config map[string]any, sysctl exec.SysctlExec, file exec.FileExec) (state.State, error) {
	s := &SysctlPresent{id: id, sysctl: sysctl, file: file}

	s.Key, _ = config["name"].(string)
	if s.Key == "" {
		s.Key = id
	}

	s.Value, _ = config["value"].(string)
	if s.Value == "" {
		return nil, fmt.Errorf("sysctl.present: %s: value is required", id)
	}

	s.Persist = true // default true
	if v, ok := config["persist"].(bool); ok {
		s.Persist = v
	}

	if s.Persist && file == nil {
		return nil, fmt.Errorf("sysctl.present: %s: no file provider available (required to verify %s)", id, exec.SysctlConfPath)
	}

	s.reqs = state.ParseRequisites(config)
	return s, nil
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
