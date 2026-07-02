package modules

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// FileComment implements the file.comment and file.uncomment states.
// It comments (or uncomments) lines matching a regular expression by
// prefixing/removing a comment character. Modeled after Salt's file.comment
// and file.uncomment modules.
type FileComment struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit.
	Path string

	// Char is the comment character (default "#").
	Char string

	// uncomment selects uncomment mode when true.
	uncomment bool

	re   *regexp.Regexp
	file exec.FileExec

	backup    []byte
	backupSet bool
}

// NewFileCommentBuilder returns a state.Builder that comments matching lines.
func NewFileCommentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.comment: no file provider available")
		}
		return newFileComment(id, config, mctx.File, false)
	}
}

// NewFileUncommentBuilder returns a state.Builder that uncomments matching lines.
func NewFileUncommentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.uncomment: no file provider available")
		}
		return newFileComment(id, config, mctx.File, true)
	}
}

func newFileComment(id string, config map[string]any, file exec.FileExec, uncomment bool) (state.State, error) {
	f := &FileComment{id: id, file: file, uncomment: uncomment}

	f.Path = fsxResolvePath(config, id)

	f.Char, _ = config["char"].(string)
	if f.Char == "" {
		f.Char = "#"
	}

	pattern, _ := config["regex"].(string)
	if pattern == "" {
		return nil, fmt.Errorf("%s: regex is required", f.module())
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid regex %q: %w", f.module(), pattern, err)
	}
	f.re = re

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileComment) module() string {
	if f.uncomment {
		return "file.uncomment"
	}
	return "file.comment"
}

func (f *FileComment) Name() string           { return f.module() + ":" + f.id }
func (f *FileComment) Reqs() state.Requisites { return f.reqs }

// fsxUncommentLine strips a single leading comment char (after optional
// whitespace) from line, reporting whether the line was commented.
func fsxUncommentLine(line, char string) (string, bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if strings.HasPrefix(line[i:], char) {
		return line[:i] + line[i+len(char):], true
	}
	return line, false
}

// compute returns the new lines and whether anything changed.
func (f *FileComment) compute(lines []string) ([]string, bool) {
	out := append([]string(nil), lines...)
	changed := false

	for i, l := range out {
		if f.uncomment {
			unc, wasCommented := fsxUncommentLine(l, f.Char)
			if wasCommented && f.re.MatchString(unc) {
				out[i] = unc
				changed = true
			}
			continue
		}

		// comment: skip lines already commented.
		if _, commented := fsxUncommentLine(l, f.Char); commented {
			continue
		}
		if f.re.MatchString(l) {
			out[i] = f.Char + l
			changed = true
		}
	}

	return out, changed
}

func (f *FileComment) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		return state.CheckResult{NeedsChange: false}, nil
	}

	lines, _ := fsxSplitLines(string(data))
	_, changed := f.compute(lines)
	if !changed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s lines matching %q in %s", f.module(), f.re.String(), f.Path),
	}, nil
}

func (f *FileComment) Apply(ctx context.Context) (state.ApplyResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		return state.ApplyResult{Changed: false}, nil
	}

	f.backup = data
	f.backupSet = true

	lines, trailingNL := fsxSplitLines(string(data))
	newLines, changed := f.compute(lines)
	if !changed {
		return state.ApplyResult{Changed: false}, nil
	}

	out := fsxJoinLines(newLines, trailingNL)
	if err := f.file.WriteFile(ctx, f.Path, []byte(out), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("%s: write %s: %w", f.module(), f.Path, err)
	}

	action := "commented"
	if f.uncomment {
		action = "uncommented"
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("%s lines matching %q in %s", action, f.re.String(), f.Path),
		Details: map[string]string{"path": f.Path, "action": action},
	}, nil
}

func (f *FileComment) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevert(ctx, f.file, f.Path, f.backup, f.backupSet, f.module())
}
