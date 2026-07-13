package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// FileLine implements the file.line state.
// It manages a single line within a file: ensuring, replacing, inserting,
// or deleting it. Modeled after Salt's file.line module.
//
// FileLine is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). The
// `mode` parameter is an ACTION ENUM (a plain string — ensure/replace/insert/
// delete, absent as a delete synonym), NOT a permission mode, so it is a plain
// string field with an eager `default=ensure`; the builder lowercases the
// resolved action to reproduce the legacy `strings.ToLower` normalization. The
// unexported runtime fields (id, reqs, injected provider, revert memos) are
// untagged, so the schema compiler skips them.
type FileLine struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit; it defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target file; the path alias is accepted for Salt compatibility; defaults to the state ID"`

	// Content is the desired line content.
	Content string `zester:"content" usage:"the line content to ensure, replace, insert, or delete"`

	// Action selects the operation: ensure (default), replace, insert, delete
	// (absent is accepted as a synonym for delete). It is lowercased before use.
	Action string `zester:"mode,default=ensure" usage:"operation to perform: ensure (default), replace, insert, or delete (absent = delete); case-insensitive"`

	// Match is a regex/substring identifying the target line. When empty,
	// Content is matched exactly.
	Match string `zester:"match" usage:"regular expression (or literal substring fallback) identifying the target line; when empty, the exact Content line is matched"`

	// Before is a regex/substring; the line is inserted before the first match.
	Before string `zester:"before" usage:"regular expression (or substring); on insert/ensure the new line is placed before the first matching line"`

	// After is a regex/substring; the line is inserted after the first match.
	After string `zester:"after" usage:"regular expression (or substring); on insert/ensure the new line is placed after the first matching line (takes precedence over before)"`

	file exec.FileExec

	backup    []byte
	backupSet bool
	created   bool
}

// fileLineSpec is the compiled schema + documentation for file.line. It is
// compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code (notably: `mode` is an action selector, not a
// permission mode; a missing file is created only by the content-adding actions;
// and matching falls back from regex to a literal substring test).
var fileLineSpec = mustSpec("file.line", modschema.KindState, FileLine{}, modschema.Doc{
	Summary: "Ensure, replace, insert, or delete a single line in a file.",
	Description: "`file.line` manages one line within a file. The path defaults to the state ID (the " +
		"`path` alias is accepted for Salt compatibility). The `mode` parameter selects the action — " +
		"`ensure` (the default: the line exists — it replaces the first matched line, or, when nothing " +
		"matches, inserts the line positioned by `before`/`after`, appending it at the end when neither " +
		"anchor matches), `replace` " +
		"(rewrite a matched line, never adding one), `insert` (add the line if absent, positioned by " +
		"`before`/`after`), or `delete` (remove every matching line; `absent` is a synonym). `match` is a " +
		"regular expression identifying the target line (falling back to a literal substring test when it " +
		"is not a valid regex); with no `match`, the exact `content` line is the target.",
	Effects: modschema.Effects{
		Check: "Reads the file and computes the resulting lines in memory. A change is needed only when " +
			"the computed content differs. When the file does not exist, a change is needed only for the " +
			"content-adding actions (`ensure`/`insert`); `replace`/`delete` on a missing file need no change. " +
			"Any read error other than not-exist fails the check rather than risking a blind write.",
		Apply: "Reads the file (capturing the original for revert on the first write). A missing file is " +
			"created with the single line for the content-adding actions, and is a no-op for `replace`/" +
			"`delete`. Otherwise it applies the action — `ensure` replaces the first matched line, or, when " +
			"no line matches, inserts `content` after the first `after` match, else before the first " +
			"`before` match, else appends it at the end; `replace` rewrites matched lines in place; `insert` " +
			"adds `content` after the first " +
			"`after` match, else before the first `before` match, else at end; `delete` drops every matching " +
			"line — and writes the result with mode 0644, preserving the trailing newline.",
		Revert: "Restores what this run's Apply changed: a file that pre-existed is rewritten with its " +
			"captured prior content; a file this instance created is removed (tolerating an already-missing " +
			"file). A fresh instance (a standalone revert) recorded nothing and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Ensure a directive is present",
			Kind:        "state",
			Explanation: "With no match, the exact content line is ensured — appended if it is not already present.",
			Code:        "/etc/sysctl.conf:\n  file.line:\n    - content: \"net.ipv4.ip_forward=1\"\n",
		},
		{
			Title:       "Replace a matched line",
			Kind:        "state",
			Explanation: "match selects the line to rewrite; mode: replace never adds a line when the match is absent.",
			Code: "/etc/ssh/sshd_config:\n  file.line:\n    - content: \"PermitRootLogin no\"\n" +
				"    - match: \"^PermitRootLogin\"\n    - mode: replace\n",
		},
		{
			Title:       "Insert a line after an anchor",
			Kind:        "state",
			Explanation: "mode: insert places the line after the first line matching after (before is the fallback anchor).",
			Code: "/etc/hosts:\n  file.line:\n    - content: \"10.0.0.5 db\"\n" +
				"    - after: \"^127\\\\.0\\\\.0\\\\.1\"\n    - mode: insert\n",
		},
		{
			Title:       "Delete a matching line ad hoc",
			Kind:        "cli",
			Explanation: "mode=delete removes every line matching match; the bare positional is the path.",
			Code:        "zester 'web*' file.line /etc/fstab mode=delete match='\\sswap\\s'",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "mode is an action, not a permission mode",
			Body: "Unlike file.managed, file.line's `mode` selects the edit operation " +
				"(ensure/replace/insert/delete). It is not a file permission; file.line never changes " +
				"permissions and always writes with 0644 when it creates a file.",
		},
		{
			Level: "info",
			Title: "match is a regex with a substring fallback",
			Body: "`match`, `before`, and `after` are compiled as Go (RE2) regular expressions; if a value " +
				"is not a valid regex it falls back to a literal substring test rather than erroring.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "Unlike Salt — which requires `match` to be a valid Python regular expression — Zester " +
				"falls back to a plain substring test when `match` is not a valid Go regex. Salt's `location`, " +
				"`quiet`, `indent`, and `backup` parameters are not supported.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"file.replace", "file.blockreplace", "file.managed"},
})

// NewFileLineBuilder returns a state.Builder that creates FileLine states.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts;
// the peel supplies it through modules.RegisterAll.
func NewFileLineBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.line: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		f := &FileLine{}
		if _, err := fileLineSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.line: %w", err)
		}
		// Reproduce the legacy strings.ToLower(mode) normalization: the action
		// selector is case-insensitive.
		f.Action = strings.ToLower(f.Action)
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
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
	if slices.Contains(lines, f.Content) {
		return lines, false
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
