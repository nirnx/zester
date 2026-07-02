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

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
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

	// backup stores original content for revert.
	backup    []byte
	backupSet bool
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
		// Treat any read error on the target as "file doesn't exist".
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

	return state.CheckResult{NeedsChange: false}, nil
}

func (f *FileManaged) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Save backup for revert.
	if existing, err := f.file.ReadFile(ctx, f.Path); err == nil {
		f.backup = existing
		f.backupSet = true
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
	if !f.backupSet {
		if err := f.file.Remove(ctx, f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("file.managed: remove %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (no prior state)", f.Path),
		}, nil
	}

	mode, _ := f.desiredMode()
	if err := f.file.WriteFile(ctx, f.Path, f.backup, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.managed: revert %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("reverted %s to previous content", f.Path),
	}, nil
}

func (f *FileManaged) setOwnership(ctx context.Context) error {
	if f.User == "" && f.Group == "" {
		return nil
	}

	uid := -1
	gid := -1

	if f.User != "" {
		u, err := user.Lookup(f.User)
		if err != nil {
			return fmt.Errorf("file.managed: lookup user %q: %w", f.User, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if f.Group != "" {
		g, err := user.LookupGroup(f.Group)
		if err != nil {
			return fmt.Errorf("file.managed: lookup group %q: %w", f.Group, err)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}

	if err := f.file.Chown(ctx, f.Path, uid, gid); err != nil {
		return fmt.Errorf("file.managed: chown %s: %w", f.Path, err)
	}
	return nil
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
