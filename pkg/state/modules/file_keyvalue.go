package modules

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/ptorbus/zester/pkg/exec"
	"github.com/ptorbus/zester/pkg/state"
)

// FileKeyValue implements the file.keyvalue state.
// It ensures one or more "key<separator>value" lines are present in a file,
// updating the value in place when the key already exists (e.g. sysctl.conf,
// os-release, environment files).
type FileKeyValue struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit.
	Path string

	// Separator is the literal token written between key and value (default "=").
	Separator string

	// Entries maps keys to their desired values.
	Entries map[string]string

	file exec.FileExec

	backup    []byte
	backupSet bool
}

// NewFileKeyValueBuilder returns a state.Builder that creates FileKeyValue states.
func NewFileKeyValueBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.keyvalue: no file provider available")
		}
		return newFileKeyValue(id, config, mctx.File)
	}
}

func newFileKeyValue(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileKeyValue{id: id, file: file, Entries: map[string]string{}}

	f.Path = fsxResolvePath(config, id)

	f.Separator, _ = config["separator"].(string)
	if f.Separator == "" {
		f.Separator = "="
	}

	// Salt calls the entries dict "key_values"; "entries" is kept as an alias.
	if entries, ok := config["key_values"].(map[string]any); ok {
		for k, v := range entries {
			f.Entries[k] = fmt.Sprintf("%v", v)
		}
	}
	if entries, ok := config["entries"].(map[string]any); ok {
		for k, v := range entries {
			f.Entries[k] = fmt.Sprintf("%v", v)
		}
	}

	if key, ok := config["key"].(string); ok && key != "" {
		v, ok := config["value"]
		if !ok || v == nil {
			return nil, fmt.Errorf("file.keyvalue: key %q requires a value", key)
		}
		f.Entries[key] = fmt.Sprintf("%v", v)
	}

	if len(f.Entries) == 0 {
		return nil, fmt.Errorf("file.keyvalue: no entries (use key/value or key_values)")
	}

	f.reqs = state.ParseRequisites(config)

	return f, nil
}

func (f *FileKeyValue) Name() string           { return "file.keyvalue:" + f.id }
func (f *FileKeyValue) Reqs() state.Requisites { return f.reqs }

func (f *FileKeyValue) sortedKeys() []string {
	keys := make([]string, 0, len(f.Entries))
	for k := range f.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// keyRegex builds a matcher for an existing "key <sep> value" line, capturing
// the leading whitespace (group 1) and current value (group 2). The separator
// is matched trimmed so surrounding spaces are tolerated.
func fsxKeyValueRegex(key, sep string) *regexp.Regexp {
	s := trimSpaceASCII(sep)
	if s == "" {
		s = "="
	}
	return regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*` + regexp.QuoteMeta(s) + `\s*(.*?)\s*$`)
}

// trimSpaceASCII trims spaces and tabs from both ends of s.
func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// compute returns the new lines and whether anything changed.
func (f *FileKeyValue) compute(lines []string) ([]string, bool) {
	out := append([]string(nil), lines...)
	changed := false

	for _, key := range f.sortedKeys() {
		val := f.Entries[key]
		re := fsxKeyValueRegex(key, f.Separator)
		found := false
		for i, l := range out {
			m := re.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			found = true
			newLine := m[1] + key + f.Separator + val
			if out[i] != newLine {
				out[i] = newLine
				changed = true
			}
			break
		}
		if !found {
			out = append(out, key+f.Separator+val)
			changed = true
		}
	}

	return out, changed
}

func (f *FileKeyValue) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("file %s does not exist", f.Path),
		}, nil
	}

	lines, _ := fsxSplitLines(string(data))
	_, changed := f.compute(lines)
	if !changed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%d key/value entries pending in %s", len(f.Entries), f.Path),
	}, nil
}

func (f *FileKeyValue) Apply(ctx context.Context) (state.ApplyResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	var lines []string
	trailingNL := true
	if err == nil {
		f.backup = data
		f.backupSet = true
		lines, trailingNL = fsxSplitLines(string(data))
	}

	newLines, changed := f.compute(lines)
	if !changed {
		return state.ApplyResult{Changed: false}, nil
	}

	out := fsxJoinLines(newLines, trailingNL)
	if err := f.file.WriteFile(ctx, f.Path, []byte(out), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.keyvalue: write %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("ensured %d key/value entries in %s", len(f.Entries), f.Path),
		Details: map[string]string{"path": f.Path, "entries": fmt.Sprintf("%d", len(f.Entries))},
	}, nil
}

func (f *FileKeyValue) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevert(ctx, f.file, f.Path, f.backup, f.backupSet, "file.keyvalue")
}
