package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// FileReplace implements the file.replace state.
// It performs a regular-expression search-and-replace within a file, with
// optional append/prepend when the pattern is not found. Modeled after Salt's
// file.replace module.
type FileReplace struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit.
	Path string

	// Pattern is the compiled search regular expression.
	Pattern string

	// Repl is the replacement text. Backreferences ($1, ${name}) are expanded.
	Repl string

	// Count limits the number of replacements (0 = replace all).
	Count int

	// AppendIfNotFound appends NotFoundContent when the pattern is absent.
	AppendIfNotFound bool

	// PrependIfNotFound prepends NotFoundContent when the pattern is absent.
	PrependIfNotFound bool

	// NotFoundContent is the text used for append/prepend. Defaults to Repl.
	NotFoundContent string

	re   *regexp.Regexp
	file exec.FileExec

	backup    []byte
	backupSet bool
	created   bool
}

// NewFileReplaceBuilder returns a state.Builder that creates FileReplace states.
func NewFileReplaceBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.replace: no file provider available")
		}
		return newFileReplace(id, config, mctx.File)
	}
}

func newFileReplace(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileReplace{id: id, file: file}

	f.Path = fsxResolvePath(config, id)
	f.Pattern, _ = config["pattern"].(string)
	f.Repl, _ = config["repl"].(string)
	f.Count = fsxToInt(config["count"])
	f.AppendIfNotFound, _ = config["append_if_not_found"].(bool)
	f.PrependIfNotFound, _ = config["prepend_if_not_found"].(bool)
	f.NotFoundContent, _ = config["not_found_content"].(string)

	if f.Pattern == "" {
		return nil, fmt.Errorf("file.replace: pattern is required")
	}
	// Salt's file.replace defaults to re.MULTILINE (flags=8), so ^ and $
	// anchor per line rather than to the whole file. Match that behavior.
	re, err := regexp.Compile("(?m)" + f.Pattern)
	if err != nil {
		return nil, fmt.Errorf("file.replace: invalid pattern %q: %w", f.Pattern, err)
	}
	f.re = re

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileReplace) Name() string           { return "file.replace:" + f.id }
func (f *FileReplace) Reqs() state.Requisites { return f.reqs }

func (f *FileReplace) notFound() string {
	if f.NotFoundContent != "" {
		return f.NotFoundContent
	}
	return f.Repl
}

// transform returns the new file content and whether anything changed.
// existed indicates whether the file was present.
func (f *FileReplace) transform(content string, existed bool) (string, bool) {
	if !existed {
		if f.AppendIfNotFound || f.PrependIfNotFound {
			nc := f.notFound()
			if !strings.HasSuffix(nc, "\n") {
				nc += "\n"
			}
			return nc, true
		}
		return content, false
	}

	if f.re.MatchString(content) {
		out := fsxRegexpReplace(f.re, content, f.Repl, f.Count)
		return out, out != content
	}

	nc := f.notFound()
	if nc == "" {
		return content, false
	}
	if f.AppendIfNotFound {
		if strings.Contains(content, nc) {
			return content, false
		}
		out := content
		if len(out) > 0 && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += nc
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out, true
	}
	if f.PrependIfNotFound {
		if strings.Contains(content, nc) {
			return content, false
		}
		prefix := nc
		if !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		return prefix + content, true
	}
	return content, false
}

func (f *FileReplace) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state.CheckResult{}, fmt.Errorf("file.replace: read %s: %w", f.Path, err)
	}
	existed := err == nil
	content := ""
	if existed {
		content = string(data)
	}

	_, changed := f.transform(content, existed)
	if !changed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("pattern %q replacement pending in %s", f.Pattern, f.Path),
	}, nil
}

func (f *FileReplace) Apply(ctx context.Context) (state.ApplyResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state.ApplyResult{}, fmt.Errorf("file.replace: read %s: %w", f.Path, err)
	}
	existed := err == nil
	content := ""
	if existed {
		content = string(data)
		if !f.backupSet {
			f.backup = data
			f.backupSet = true
		}
	}

	out, changed := f.transform(content, existed)
	if !changed {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := f.file.WriteFile(ctx, f.Path, []byte(out), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.replace: write %s: %w", f.Path, err)
	}
	if !existed {
		f.created = true
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("applied replacement of %q in %s", f.Pattern, f.Path),
		Details: map[string]string{"path": f.Path, "pattern": f.Pattern},
	}, nil
}

func (f *FileReplace) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevertWithCreate(ctx, f.file, f.Path, f.backup, f.backupSet, f.created, "file.replace")
}

// fsxRegexpReplace replaces matches of re in src with repl, expanding
// backreferences. When count > 0, at most count replacements are performed.
func fsxRegexpReplace(re *regexp.Regexp, src, repl string, count int) string {
	matches := re.FindAllStringSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return src
	}
	var sb strings.Builder
	last := 0
	n := 0
	for _, m := range matches {
		if count > 0 && n >= count {
			break
		}
		sb.WriteString(src[last:m[0]])
		sb.Write(re.ExpandString(nil, repl, src, m))
		last = m[1]
		n++
	}
	sb.WriteString(src[last:])
	return sb.String()
}
