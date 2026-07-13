package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// FileReplace implements the file.replace state.
// It performs a regular-expression search-and-replace within a file, with
// optional append/prepend when the pattern is not found. Modeled after Salt's
// file.replace module.
//
// FileReplace is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `pattern`
// is `required` — a missing/empty pattern is a typed MissingRequired error,
// matching the legacy explicit check. `count` is a plain int, so a msgpack-
// delivered sized integer (a reactor dispatch encodes `count: 2` as an int8) is
// now honored where the legacy `fsxToInt` switch dropped every non-int/int64/
// float64 kind to 0 (BD-1), and a CLI `count=2` string is parsed (BD-2). The
// compiled regex lives in the unexported `re` field, built in the builder tail
// (a compile failure stays a construction error, as in the legacy constructor —
// parity). The unexported runtime fields are untagged, so the compiler skips
// them.
type FileReplace struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit; it defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target file; the path alias is accepted for Salt compatibility; defaults to the state ID"`

	// Pattern is the search regular expression (required).
	Pattern string `zester:"pattern,required" usage:"search regular expression (RE2); anchored per-line (Salt's re.MULTILINE default)"`

	// Repl is the replacement text. Backreferences ($1, ${name}) are expanded.
	Repl string `zester:"repl" usage:"replacement text; $1/${name} backreferences are expanded"`

	// Count limits the number of replacements (0 = replace all).
	Count int `zester:"count" usage:"maximum number of replacements to perform; 0 (the default) replaces every match"`

	// AppendIfNotFound appends NotFoundContent when the pattern is absent.
	AppendIfNotFound bool `zester:"append_if_not_found" usage:"when the pattern is not found, append not_found_content (or repl); a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// PrependIfNotFound prepends NotFoundContent when the pattern is absent.
	PrependIfNotFound bool `zester:"prepend_if_not_found" usage:"when the pattern is not found, prepend not_found_content (or repl); a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// NotFoundContent is the text used for append/prepend. Defaults to Repl.
	NotFoundContent string `zester:"not_found_content" usage:"text used for append_if_not_found/prepend_if_not_found; defaults to repl when empty"`

	re   *regexp.Regexp
	file exec.FileExec

	backup    []byte
	backupSet bool
	created   bool
}

// fileReplaceSpec is the compiled schema + documentation for file.replace. It
// is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code (notably: the pattern is anchored per-line; the
// search-and-replace is inherently non-idempotent when the replacement still
// matches; and append/prepend fall back to `repl` when `not_found_content` is
// empty).
var fileReplaceSpec = mustSpec("file.replace", modschema.KindState, FileReplace{}, modschema.Doc{
	Summary: "Regular-expression search-and-replace within a file.",
	Description: "`file.replace` rewrites every match of a regular expression in a file, expanding " +
		"backreferences in the replacement. The path defaults to the state ID (the `path` alias is " +
		"accepted for Salt compatibility). The pattern is anchored per line (Salt's `re.MULTILINE` " +
		"default), so `^`/`$` bind to line boundaries. `count` caps the number of replacements (0 replaces " +
		"all). When the pattern is not found, `append_if_not_found` or `prepend_if_not_found` adds " +
		"`not_found_content` (falling back to `repl`) — and, on a missing file, creates it with that " +
		"content.",
	Effects: modschema.Effects{
		Check: "Reads the file (a not-exist read is treated as empty content; any other read error fails " +
			"the check) and computes the transformed content in memory: it reports a change when a match " +
			"would be rewritten, or when the pattern is absent and an append/prepend would add content that " +
			"is not already present.",
		Apply: "Reads the file (capturing the original for revert on the first write). If the pattern " +
			"matches, it performs up to `count` replacements. If the pattern is absent, it appends or " +
			"prepends `not_found_content`/`repl` (creating a missing file with that content when " +
			"append/prepend is enabled). Writes the result with mode 0644. Note the replacement is not " +
			"guaranteed idempotent: if the replacement text still matches the pattern, a re-run replaces again.",
		Revert: "Restores what this run's Apply changed: a file that pre-existed is rewritten with its " +
			"captured prior content; a file this instance created (via append/prepend on a missing file) is " +
			"removed (tolerating an already-missing file). A fresh instance (a standalone revert) recorded " +
			"nothing and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Rewrite a config value",
			Kind:        "state",
			Explanation: "The pattern matches the whole line; repl replaces it.",
			Code: "/etc/rsyslog.conf:\n  file.replace:\n    - pattern: \"^\\\\$ModLoad imudp\"\n" +
				"    - repl: \"#$ModLoad imudp\"\n",
		},
		{
			Title:       "Ensure a setting exists, appending if absent",
			Kind:        "state",
			Explanation: "append_if_not_found adds the line when the pattern matches nothing; not_found_content is the text to add.",
			Code: "/etc/security/limits.conf:\n  file.replace:\n    - pattern: \"^\\\\* soft nofile\"\n" +
				"    - repl: \"* soft nofile 65535\"\n    - append_if_not_found: true\n" +
				"    - not_found_content: \"* soft nofile 65535\"\n",
		},
		{
			Title:       "Replace only the first match",
			Kind:        "state",
			Explanation: "count caps the number of replacements; here only the first occurrence is rewritten.",
			Code: "/etc/hosts:\n  file.replace:\n    - pattern: \"localhost\"\n" +
				"    - repl: \"localhost.localdomain\"\n    - count: 1\n",
		},
		{
			Title:       "Search-and-replace ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional is the path; pattern/repl are key=value args.",
			Code:        "zester 'web*' file.replace /etc/motd pattern='old text' repl='new text'",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Pattern is anchored per line",
			Body: "Zester wraps the pattern with `(?m)` to match Salt's `re.MULTILINE` default, so `^` and " +
				"`$` anchor to line boundaries rather than the whole file.",
		},
		{
			Level: "info",
			Title: "Replacement is not guaranteed idempotent",
			Body: "If the replacement text still matches the pattern (for example replacing `x` with `xy`), " +
				"a subsequent run replaces again. This mirrors Salt's file.replace; anchor the pattern or use " +
				"a replacement that no longer matches to converge.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "Backreferences in `repl` use Go's `$1` / `${name}` syntax, not Python's `\\1` / " +
				"`\\g<name>`. The pattern is a Go (RE2) regular expression, so lookaheads and lookbehinds are " +
				"unavailable. Salt's `flags`, `ignore_if_missing`, `backup`, and `dry_run` parameters are not " +
				"supported; the MULTILINE flag is always on (which is also Salt's default).",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"file.line", "file.blockreplace", "file.managed"},
})

// NewFileReplaceBuilder returns a state.Builder that creates FileReplace states.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts;
// the peel supplies it through modules.RegisterAll.
func NewFileReplaceBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.replace: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the compiled regex, injected
		// provider, id, and requisites MUST be assigned AFTER it.
		f := &FileReplace{}
		if _, err := fileReplaceSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.replace: %w", err)
		}
		// Salt's file.replace defaults to re.MULTILINE (flags=8), so ^ and $
		// anchor per line rather than to the whole file. Match that behavior.
		// Compilation stays a CONSTRUCTION-time error (as in the legacy
		// constructor) — decode validated the pattern is present, not that it
		// compiles.
		re, err := regexp.Compile("(?m)" + f.Pattern)
		if err != nil {
			return nil, fmt.Errorf("file.replace: invalid pattern %q: %w", f.Pattern, err)
		}
		f.re = re
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
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
