// Package modules provides built-in state modules for Zester.
package modules

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os/user"
	"strconv"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// FileManaged implements the file.managed state.
// It ensures a file exists with the specified content, permissions, and ownership.
type FileManaged struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path to the file.
	Path string

	// Content is the desired file content. Mutually exclusive with Source.
	Content string

	// Source is a local file path to copy from. Mutually exclusive with Content.
	Source string

	// Template enables Jinja2 rendering of source/content before writing.
	Template bool

	// Context provides extra variables to the template (overrides Defaults).
	Context map[string]any

	// Defaults provides fallback variables for the template.
	Defaults map[string]any

	// Mode is the file permission mode (e.g., "0644").
	Mode string

	// User is the file owner username.
	User string

	// Group is the file group name.
	Group string

	// MakeDirs creates parent directories if true.
	MakeDirs bool

	// file is the injected file execution provider.
	file exec.FileExec

	// render is the injected template rendering function.
	render func(name, source string, extra map[string]any) (string, error)

	// backup stores original content (and its mode) for revert.
	backup     []byte
	backupMode fs.FileMode
	backupSet  bool

	// wasCreated records that Apply created the file (it did not pre-exist),
	// so a same-instance Revert removes it. With neither memo set, Revert is
	// a clean no-op.
	wasCreated bool
}

// NewFileManagedBuilder returns a state.Builder that creates FileManaged
// states using the given ModuleContext's file and render providers.
func NewFileManagedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileManaged(id, config, mctx.File, mctx.RenderTemplate)
	}
}

func newFileManaged(id string, config map[string]any, file exec.FileExec,
	render func(string, string, map[string]any) (string, error)) (state.State, error) {

	f := &FileManaged{id: id, file: file, render: render}

	// Salt convention: "name" is the primary param, "path" is an alias.
	path, _ := config["name"].(string)
	if path == "" {
		path, _ = config["path"].(string)
	}
	if path == "" {
		path = id
	}
	f.Path = path

	f.Content, _ = config["content"].(string)
	f.Source, _ = config["source"].(string)
	f.Mode = modeConfigToString(config["mode"])
	f.User, _ = config["user"].(string)
	f.Group, _ = config["group"].(string)
	f.MakeDirs, _ = config["makedirs"].(bool)

	// Template: accept bool (true) or string ("jinja").
	switch v := config["template"].(type) {
	case bool:
		f.Template = v
	case string:
		f.Template = v == "jinja"
	}

	if ctx, ok := config["context"].(map[string]any); ok {
		f.Context = ctx
	}
	if defs, ok := config["defaults"].(map[string]any); ok {
		f.Defaults = defs
	}

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileManaged) Name() string           { return "file.managed:" + f.id }
func (f *FileManaged) Reqs() state.Requisites { return f.reqs }

func (f *FileManaged) desiredContent(ctx context.Context) ([]byte, error) {
	var raw string
	if f.Source != "" {
		data, err := f.file.ReadFile(ctx, f.Source)
		if err != nil {
			return nil, err
		}
		raw = string(data)
	} else {
		raw = f.Content
	}

	if f.Template && f.render != nil {
		extra := mergeDefaults(f.Defaults, f.Context)
		rendered, err := f.render(f.id, raw, extra)
		if err != nil {
			return nil, fmt.Errorf("render template: %w", err)
		}
		return []byte(rendered), nil
	}

	return []byte(raw), nil
}

// mergeDefaults merges defaults and context maps, with context taking precedence.
func mergeDefaults(defaults, context map[string]any) map[string]any {
	if defaults == nil && context == nil {
		return nil
	}
	merged := make(map[string]any)
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range context {
		merged[k] = v
	}
	return merged
}

func (f *FileManaged) desiredMode() (fs.FileMode, error) {
	if f.Mode == "" {
		return 0644, nil
	}
	m, err := strconv.ParseUint(f.Mode, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("file.managed: invalid mode %q: %w", f.Mode, err)
	}
	return fs.FileMode(m), nil
}

func (f *FileManaged) Check(ctx context.Context) (state.CheckResult, error) {
	desired, err := f.desiredContent(ctx)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.managed: %w", err)
	}

	current, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		// Only a genuine not-exist means "file absent"; any other read error
		// (permissions, I/O) fails the check rather than risking a blind
		// overwrite in Apply.
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.managed: read %s: %w", f.Path, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("file %s does not exist", f.Path),
		}, nil
	}

	if hashBytes(current) != hashBytes(desired) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("content differs for %s", f.Path),
		}, nil
	}

	mode, err := f.desiredMode()
	if err != nil {
		return state.CheckResult{}, err
	}
	info, err := f.file.Stat(ctx, f.Path)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.managed: stat %s: %w", f.Path, err)
	}
	if info.Mode().Perm() != mode {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("mode %o != %o for %s", info.Mode().Perm(), mode, f.Path),
		}, nil
	}

	diff, drift, err := checkOwnershipDrift(ctx, f.file, "file.managed", f.Path, f.User, f.Group)
	if err != nil {
		return state.CheckResult{}, err
	}
	if drift {
		return state.CheckResult{NeedsChange: true, Diff: diff}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (f *FileManaged) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Probe prior state for revert. Only a genuine not-exist means "will
	// create"; any other read error fails the apply rather than overwriting
	// a file whose current content could not be captured.
	preExisted := false
	var priorContent []byte
	var priorMode fs.FileMode
	existing, err := f.file.ReadFile(ctx, f.Path)
	switch {
	case err == nil:
		preExisted = true
		priorContent = existing
		// Capture the prior mode too, so Revert can restore it (not the
		// desired mode, which is what Apply sets).
		info, statErr := f.file.Stat(ctx, f.Path)
		if statErr != nil {
			return state.ApplyResult{}, fmt.Errorf("file.managed: stat %s: %w", f.Path, statErr)
		}
		priorMode = info.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist):
		// Will create.
	default:
		return state.ApplyResult{}, fmt.Errorf("file.managed: read %s: %w", f.Path, err)
	}

	desired, err := f.desiredContent(ctx)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: %w", err)
	}

	mode, err := f.desiredMode()
	if err != nil {
		return state.ApplyResult{}, err
	}

	if f.MakeDirs {
		dir := f.Path[:len(f.Path)-len(f.Path[lastSlash(f.Path):])]
		if dir != "" {
			if err := f.file.MkdirAll(ctx, dir, 0755); err != nil {
				return state.ApplyResult{}, fmt.Errorf("file.managed: mkdir %s: %w", dir, err)
			}
		}
	}

	if err := f.file.WriteFile(ctx, f.Path, desired, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: write %s: %w", f.Path, err)
	}

	// Record revert memos only after the write actually changed the system.
	// First capture wins: a re-Apply on the same instance (retry:, watch-
	// forced runs) must not clobber the original backup with already-applied
	// content — and a file this instance CREATED stays wasCreated, or Revert
	// would rewrite content into a file that never pre-existed instead of
	// removing it.
	if preExisted {
		if !f.backupSet && !f.wasCreated {
			f.backup = priorContent
			f.backupMode = priorMode
			f.backupSet = true
		}
	} else {
		f.wasCreated = true
	}

	// os.WriteFile applies perm at creation only — enforce the desired mode
	// on pre-existing files too, or the mode drift Check reports would never
	// converge (Apply would rewrite identical bytes forever).
	if err := f.file.Chmod(ctx, f.Path, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: chmod %s: %w", f.Path, err)
	}

	if err := f.setOwnership(ctx); err != nil {
		return state.ApplyResult{}, err
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("wrote %d bytes to %s (mode %o)", len(desired), f.Path, mode),
		Details: map[string]string{
			"path":  f.Path,
			"bytes": strconv.Itoa(len(desired)),
			"mode":  fmt.Sprintf("%04o", mode),
		},
	}, nil
}

func (f *FileManaged) Revert(ctx context.Context) (state.ApplyResult, error) {
	switch {
	case f.backupSet:
		if err := f.file.WriteFile(ctx, f.Path, f.backup, f.backupMode); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.managed: revert %s: %w", f.Path, err)
		}
		// os.WriteFile sets perm at creation only; restore the CAPTURED
		// prior mode explicitly (never the desired mode — that is what
		// Apply set).
		if err := f.file.Chmod(ctx, f.Path, f.backupMode); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.managed: revert chmod %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("reverted %s to previous content", f.Path),
		}, nil

	case f.wasCreated:
		if err := f.file.Remove(ctx, f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("file.managed: remove %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert create)", f.Path),
		}, nil

	default:
		// No apply recorded on this instance: never destroy a file we did
		// not touch.
		return state.ApplyResult{
			Changed: false,
			Diff:    "nothing to revert (no apply recorded in this run)",
		}, nil
	}
}

func (f *FileManaged) setOwnership(ctx context.Context) error {
	if f.User == "" && f.Group == "" {
		return nil
	}

	uid, gid, err := resolveOwnerIDs("file.managed", f.User, f.Group)
	if err != nil {
		return err
	}

	if err := f.file.Chown(ctx, f.Path, uid, gid); err != nil {
		return fmt.Errorf("file.managed: chown %s: %w", f.Path, err)
	}
	return nil
}

// resolveOwnerIDs resolves declared user/group names to numeric ids, the same
// way the Apply-side ownership setters do. An undeclared (empty) name maps to
// -1 ("don't care", matching os.Chown semantics).
func resolveOwnerIDs(module, userName, groupName string) (uid, gid int, err error) {
	uid, gid = -1, -1

	if userName != "" {
		u, err := user.Lookup(userName)
		if err != nil {
			return -1, -1, fmt.Errorf("%s: lookup user %q: %w", module, userName, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if groupName != "" {
		g, err := user.LookupGroup(groupName)
		if err != nil {
			return -1, -1, fmt.Errorf("%s: lookup group %q: %w", module, groupName, err)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}

	return uid, gid, nil
}

// checkOwnershipDrift compares path's current ownership against the declared
// user/group names. The facet fires only for declared config: with neither
// user nor group declared it never reports drift, and with only one declared
// only that half is compared.
func checkOwnershipDrift(ctx context.Context, file exec.FileExec, module, path, userName, groupName string) (diff string, drift bool, err error) {
	if userName == "" && groupName == "" {
		return "", false, nil
	}

	wantUID, wantGID, err := resolveOwnerIDs(module, userName, groupName)
	if err != nil {
		return "", false, err
	}

	uid, gid, err := file.Owner(ctx, path)
	if err != nil {
		return "", false, fmt.Errorf("%s: owner %s: %w", module, path, err)
	}

	if wantUID != -1 && uid != wantUID {
		return fmt.Sprintf("owner uid %d != %d (%s) for %s", uid, wantUID, userName, path), true, nil
	}
	if wantGID != -1 && gid != wantGID {
		return fmt.Sprintf("group gid %d != %d (%s) for %s", gid, wantGID, groupName, path), true, nil
	}
	return "", false, nil
}

// modeConfigToString converts a YAML-parsed mode value to a string.
// YAML parsers interpret "0750" (leading zero) as an octal integer,
// so mode may arrive as int (488) instead of string ("0750").
func modeConfigToString(v any) string {
	switch m := v.(type) {
	case string:
		return m
	case int:
		return fmt.Sprintf("%04o", m)
	case int64:
		return fmt.Sprintf("%04o", m)
	case float64:
		return fmt.Sprintf("%04o", int(m))
	}
	return ""
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return 0
}
