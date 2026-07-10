package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// FileLine implements the file.line state.
// It manages a single line within a file: ensuring, replacing, inserting,
// or deleting it. Modeled after Salt's file.line module.
type FileLine struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit.
	Path string

	// Content is the desired line content.
	Content string

	// Action selects the operation: ensure (default), replace, insert, delete.
	// "absent" is accepted as a synonym for "delete".
	Action string

	// Match is a regex/substring identifying the target line. When empty,
	// the Content is matched exactly.
	Match string

	// Before is a regex/substring; the line is inserted before the first
	// matching line.
	Before string

	// After is a regex/substring; the line is inserted after the first
	// matching line.
	After string

	file exec.FileExec

	backup    []byte
	backupSet bool
	created   bool
}

// NewFileLineBuilder returns a state.Builder that creates FileLine states.
func NewFileLineBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.line: no file provider available")
		}
		return newFileLine(id, config, mctx.File)
	}
}

func newFileLine(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileLine{id: id, file: file}

	f.Path = fsxResolvePath(config, id)
	f.Content, _ = config["content"].(string)
	f.Match, _ = config["match"].(string)
	f.Before, _ = config["before"].(string)
	f.After, _ = config["after"].(string)

	f.Action, _ = config["mode"].(string)
	if f.Action == "" {
		f.Action = "ensure"
	}
	f.Action = strings.ToLower(f.Action)

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileLine) Name() string           { return "file.line:" + f.id }
func (f *FileLine) Reqs() state.Requisites { return f.reqs }

// createsOnMissing reports whether the action would create the file when it
// does not exist yet (ensure/insert add content; replace/delete do not).
func (f *FileLine) createsOnMissing() bool {
	switch f.Action {
	case "delete", "absent", "replace":
		return false
	default:
		return true
	}
}

// matches reports whether a line is the target of this state.
func (f *FileLine) matches(line string) bool {
	if f.Match != "" {
		return fsxLineMatches(line, f.Match)
	}
	return line == f.Content
}

// compute returns the new set of lines and whether anything changed.
func (f *FileLine) compute(lines []string) ([]string, bool) {
	switch f.Action {
	case "delete", "absent":
		return f.computeDelete(lines)
	case "replace":
		return f.computeReplace(lines)
	case "insert":
		return f.computeInsert(lines)
	default:
		return f.computeEnsure(lines)
	}
}

func (f *FileLine) computeDelete(lines []string) ([]string, bool) {
	out := make([]string, 0, len(lines))
	changed := false
	for _, l := range lines {
		if f.matches(l) {
			changed = true
			continue
		}
		out = append(out, l)
	}
	return out, changed
}

func (f *FileLine) computeReplace(lines []string) ([]string, bool) {
	out := append([]string(nil), lines...)
	changed := false
	for i, l := range out {
		if f.matches(l) && l != f.Content {
			out[i] = f.Content
			changed = true
		}
	}
	return out, changed
}

func (f *FileLine) computeEnsure(lines []string) ([]string, bool) {
	for i, l := range lines {
		if f.matches(l) {
			if l == f.Content {
				return lines, false
			}
			out := append([]string(nil), lines...)
			out[i] = f.Content
			return out, true
		}
	}
	return f.insert(lines), true
}

func (f *FileLine) computeInsert(lines []string) ([]string, bool) {
	for _, l := range lines {
		if l == f.Content {
			return lines, false
		}
	}
	return f.insert(lines), true
}

func (f *FileLine) insert(lines []string) []string {
	if f.After != "" {
		for i, l := range lines {
			if fsxLineMatches(l, f.After) {
				return fsxInsertAt(lines, i+1, f.Content)
			}
		}
	}
	if f.Before != "" {
		for i, l := range lines {
			if fsxLineMatches(l, f.Before) {
				return fsxInsertAt(lines, i, f.Content)
			}
		}
	}
	return append(append([]string(nil), lines...), f.Content)
}

func (f *FileLine) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.line: read %s: %w", f.Path, err)
		}
		if f.createsOnMissing() {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("file %s does not exist", f.Path),
			}, nil
		}
		return state.CheckResult{NeedsChange: false}, nil
	}

	lines, _ := fsxSplitLines(string(data))
	_, changed := f.compute(lines)
	if !changed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("line %q (%s) differs in %s", f.Content, f.Action, f.Path),
	}, nil
}

func (f *FileLine) Apply(ctx context.Context) (state.ApplyResult, error) {
	existing, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("file.line: read %s: %w", f.Path, err)
		}
		if !f.createsOnMissing() {
			return state.ApplyResult{Changed: false}, nil
		}
		content := f.Content + "\n"
		if err := f.file.WriteFile(ctx, f.Path, []byte(content), 0644); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.line: write %s: %w", f.Path, err)
		}
		f.created = true
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("created %s with line %q", f.Path, f.Content),
			Details: map[string]string{"path": f.Path, "action": "created"},
		}, nil
	}

	if !f.backupSet {
		f.backup = existing
		f.backupSet = true
	}

	lines, trailingNL := fsxSplitLines(string(existing))
	newLines, changed := f.compute(lines)
	if !changed {
		return state.ApplyResult{Changed: false}, nil
	}

	out := fsxJoinLines(newLines, trailingNL)
	if err := f.file.WriteFile(ctx, f.Path, []byte(out), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.line: write %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s line %q in %s", f.Action, f.Content, f.Path),
		Details: map[string]string{"path": f.Path, "action": f.Action},
	}, nil
}

func (f *FileLine) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevertWithCreate(ctx, f.file, f.Path, f.backup, f.backupSet, f.created, "file.line")
}

// ---- shared file-surgery helpers (fsx*) ----

// fsxResolvePath extracts the target file path from config using the Salt
// convention: "name" is primary, "path" is an alias, and the state ID is the
// final fallback.
func fsxResolvePath(config map[string]any, id string) string {
	p, _ := config["name"].(string)
	if p == "" {
		p, _ = config["path"].(string)
	}
	if p == "" {
		p = id
	}
	return p
}

// fsxLineMatches reports whether line matches expr. expr is treated as a Go
// regular expression; if it fails to compile it falls back to a substring test.
func fsxLineMatches(line, expr string) bool {
	if expr == "" {
		return false
	}
	if re, err := regexp.Compile(expr); err == nil {
		return re.MatchString(line)
	}
	return strings.Contains(line, expr)
}

// fsxSplitLines splits file content into lines, reporting whether the content
// ended with a trailing newline so it can be preserved on write.
func fsxSplitLines(s string) (lines []string, trailingNL bool) {
	if s == "" {
		return nil, false
	}
	trailingNL = strings.HasSuffix(s, "\n")
	if trailingNL {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n"), trailingNL
}

// fsxJoinLines joins lines back into file content, restoring the trailing
// newline when trailingNL is true.
func fsxJoinLines(lines []string, trailingNL bool) string {
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return out
}

// fsxInsertAt returns a new slice with val inserted at idx.
func fsxInsertAt(lines []string, idx int, val string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:idx]...)
	out = append(out, val)
	out = append(out, lines[idx:]...)
	return out
}

// fsxMakeParent creates the parent directory of path when needed.
func fsxMakeParent(ctx context.Context, file exec.FileExec, path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." || dir == "/" {
		return nil
	}
	return file.MkdirAll(ctx, dir, 0755)
}

// fsxToInt best-effort converts a YAML-decoded numeric value to an int.
func fsxToInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// fsxNothingToRevert is the diff reported when Revert runs on an instance
// whose Apply never recorded a backup — a fresh instance must be a clean
// no-op, never destructive.
const fsxNothingToRevert = "nothing to revert (no apply recorded in this run)"

// fsxRevert restores a file from a backup captured by a same-instance Apply.
// When no backup was recorded (backupSet false — Apply never ran in this
// instance, or ran without touching an existing file), it is an explicit
// clean no-op: the flag cannot distinguish "file was absent before Apply"
// from "Apply never ran here", so removing the file would destroy
// pre-existing state on a fresh instance. Modules whose Apply may CREATE the
// file track that separately and use fsxRevertWithCreate.
func fsxRevert(ctx context.Context, file exec.FileExec, path string, backup []byte, backupSet bool, module string) (state.ApplyResult, error) {
	if !backupSet {
		return state.ApplyResult{
			Changed: false,
			Diff:    fsxNothingToRevert,
		}, nil
	}
	if err := file.WriteFile(ctx, path, backup, 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("%s: revert %s: %w", module, path, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("reverted %s to previous content", path),
	}, nil
}

// fsxRevertWithCreate is fsxRevert for modules whose Apply may create the
// managed file: when this instance's Apply created it (created true) and no
// pre-existing content was captured, removing the file is the exact inverse.
// A captured backup takes precedence — it is genuine pre-apply content.
// An already-missing created file is tolerated (fs.ErrNotExist): file-absent
// IS the reverted state — aligned with FileCopy.Revert and FileManaged.Revert.
func fsxRevertWithCreate(ctx context.Context, file exec.FileExec, path string, backup []byte, backupSet, created bool, module string) (state.ApplyResult, error) {
	if !backupSet && created {
		if err := file.Remove(ctx, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("%s: revert remove %s: %w", module, path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert to nonexistent)", path),
		}, nil
	}
	return fsxRevert(ctx, file, path, backup, backupSet, module)
}
