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

// FileComment implements BOTH the file.comment and file.uncomment states from a
// single struct. It comments (or uncomments) lines matching a regular expression
// by prefixing/removing a comment character. Modeled after Salt's file.comment
// and file.uncomment modules.
//
// FileComment is the N:1 exemplar: ONE proto/struct backs TWO registered module
// names, so there are two Specs — fileCommentSpec ("file.comment") and
// fileUncommentSpec ("file.uncomment") — each carrying its own documentation
// metadata but compiling the SAME tagged parameter surface (name, char, regex).
// Which behavior a built state performs is selected by the `uncomment` runtime
// field, set by the respective builder (not a user parameter, so untagged). The
// raw `regex` string is decoded into the tagged Regex field and compiled into
// the unexported `re` in the builder tail (a compile failure stays a
// construction error — parity). The remaining unexported runtime fields are
// untagged, so the schema compiler skips them.
type FileComment struct {
	id   string
	reqs state.Requisites

	// Path is the file to edit; it defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target file; the path alias is accepted for Salt compatibility; defaults to the state ID"`

	// Char is the comment character (default "#").
	Char string `zester:"char,default=#" usage:"comment prefix added (file.comment) or stripped (file.uncomment); defaults to #"`

	// Regex is the required matching expression (matched against the
	// UNCOMMENTED line content).
	Regex string `zester:"regex,required" usage:"Go regular expression matched against the uncommented line content"`

	// uncomment selects uncomment mode when true. Set by the builder, not a
	// user parameter — so it is untagged and the schema compiler skips it.
	uncomment bool

	re   *regexp.Regexp
	file exec.FileExec

	backup    []byte
	backupSet bool
}

// commentDoc / uncommentDoc are the drift-corrected documentation blocks for the
// two registered names. They compile the SAME FileComment proto (identical
// parameter surface) but carry name-specific summaries, effects, and examples.
var (
	fileCommentSpec = mustSpec("file.comment", modschema.KindState, FileComment{}, modschema.Doc{
		Summary: "Comment out lines matching a regular expression.",
		Description: "`file.comment` prefixes every line matching `regex` with a comment character (`#` by " +
			"default), skipping lines that are already commented. The path defaults to the state ID (the " +
			"`path` alias is accepted for Salt compatibility). `regex` is matched against the UNCOMMENTED " +
			"line content and is required. A missing file is a clean no-op.",
		Effects: modschema.Effects{
			Check: "Reads the file (a missing file needs no change; any other read error fails the check) " +
				"and computes the result in memory: a change is needed when any uncommented line matching " +
				"`regex` would be prefixed with the comment character.",
			Apply: "Reads the file (capturing the original for revert). Prefixes every uncommented line " +
				"matching `regex` with `char`, leaving already-commented lines untouched, and writes the " +
				"result with mode 0644, preserving the trailing newline. A missing file is a no-op.",
			Revert: "Restores the file's captured prior content from this run's Apply. A fresh instance (a " +
				"standalone revert) recorded nothing and is an explicit clean no-op.",
		},
		Examples: []modschema.Example{
			{
				Title:       "Disable a directive by commenting it",
				Kind:        "state",
				Explanation: "Lines matching regex are prefixed with the comment character.",
				Code:        "/etc/ssh/sshd_config:\n  file.comment:\n    - regex: \"^PermitRootLogin\"\n",
			},
			{
				Title:       "Comment with a custom character",
				Kind:        "state",
				Explanation: "char overrides the default # prefix (here a semicolon for an INI file).",
				Code:        "/etc/php.ini:\n  file.comment:\n    - regex: \"^expose_php\"\n    - char: \";\"\n",
			},
			{
				Title:       "Comment a line ad hoc",
				Kind:        "cli",
				Explanation: "The bare positional is the path; regex is a key=value arg.",
				Code:        "zester 'web*' file.comment /etc/fstab regex='\\sswap\\s'",
			},
		},
		Notes:       fileCommentNotes,
		Divergences: []string{"BD-6"},
		SeeAlso:     []string{"file.uncomment", "file.line", "file.replace"},
	})

	fileUncommentSpec = mustSpec("file.uncomment", modschema.KindState, FileComment{}, modschema.Doc{
		Summary: "Uncomment lines matching a regular expression.",
		Description: "`file.uncomment` strips a single leading comment character (`#` by default, after " +
			"optional indentation) from every commented line whose UNCOMMENTED content matches `regex`. The " +
			"path defaults to the state ID (the `path` alias is accepted for Salt compatibility). `regex` is " +
			"required and is matched against the line WITHOUT its comment prefix. A missing file is a clean " +
			"no-op.",
		Effects: modschema.Effects{
			Check: "Reads the file (a missing file needs no change; any other read error fails the check) " +
				"and computes the result in memory: a change is needed when a commented line, once its " +
				"comment character is stripped, matches `regex`.",
			Apply: "Reads the file (capturing the original for revert). Removes a single leading `char` " +
				"(after optional whitespace, preserving indentation) from every commented line whose " +
				"uncommented text matches `regex`, and writes the result with mode 0644, preserving the " +
				"trailing newline. A missing file is a no-op.",
			Revert: "Restores the file's captured prior content from this run's Apply. A fresh instance (a " +
				"standalone revert) recorded nothing and is an explicit clean no-op.",
		},
		Examples: []modschema.Example{
			{
				Title:       "Re-enable a commented directive",
				Kind:        "state",
				Explanation: "regex is matched against the uncommented text, so the leading # is stripped from the matching line.",
				Code:        "/etc/sysctl.conf:\n  file.uncomment:\n    - regex: \"net.ipv4.ip_forward\"\n",
			},
			{
				Title:       "Uncomment with a custom character",
				Kind:        "state",
				Explanation: "char selects which comment prefix to strip (a semicolon here).",
				Code:        "/etc/samba/smb.conf:\n  file.uncomment:\n    - regex: \"load printers\"\n    - char: \";\"\n",
			},
			{
				Title:       "Uncomment a line ad hoc",
				Kind:        "cli",
				Explanation: "The bare positional is the path; regex is a key=value arg.",
				Code:        "zester 'web*' file.uncomment /etc/apt/sources.list regex='deb-src'",
			},
		},
		Notes:       fileCommentNotes,
		Divergences: []string{"BD-6"},
		SeeAlso:     []string{"file.comment", "file.line", "file.replace"},
	})
)

// fileCommentNotes are the notes shared by both file.comment and file.uncomment
// (identical parameter semantics and Salt divergences).
var fileCommentNotes = []modschema.Note{
	{
		Level: "info",
		Title: "regex matches the uncommented line",
		Body: "`regex` is always matched against the line WITHOUT its comment character, so the same " +
			"expression selects a line whether or not it is currently commented.",
	},
	{
		Level: "info",
		Title: "Divergences from Salt",
		Body: "`regex` must be a valid Go (RE2) regular expression (the builder fails otherwise). Unlike " +
			"Salt — which strips a leading `^` and trailing `$` before wrapping the pattern — Zester uses " +
			"the expression as-is (an unanchored substring match unless you anchor it). Salt's `backup` " +
			"parameter is not supported; Revert uses an in-memory backup instead.",
	},
}

// NewFileCommentBuilder returns a state.Builder that comments matching lines.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts;
// the peel supplies it through modules.RegisterAll.
func NewFileCommentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return newFileCommentBuilder(mctx, opts, fileCommentSpec, false)
}

// NewFileUncommentBuilder returns a state.Builder that uncomments matching lines.
func NewFileUncommentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return newFileCommentBuilder(mctx, opts, fileUncommentSpec, true)
}

// newFileCommentBuilder is the shared builder factory for the two registered
// names: it decodes through the given spec, sets the uncomment mode, and
// compiles the regex — the two behaviors differ only in the uncomment flag.
func newFileCommentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions, spec *modschema.Spec, uncomment bool) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("%s: no file provider available", spec.Module)
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the mode flag, compiled regex,
		// injected provider, id, and requisites MUST be assigned AFTER it.
		f := &FileComment{}
		if _, err := spec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("%s: %w", spec.Module, err)
		}
		f.uncomment = uncomment
		// Compilation stays a CONSTRUCTION-time error (as in the legacy
		// constructor) — decode validated that regex is present, not that it
		// compiles.
		re, err := regexp.Compile(f.Regex)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid regex %q: %w", spec.Module, f.Regex, err)
		}
		f.re = re
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
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
		if errors.Is(err, fs.ErrNotExist) {
			// No file — nothing to comment/uncomment.
			return state.CheckResult{NeedsChange: false}, nil
		}
		return state.CheckResult{}, fmt.Errorf("%s: read %s: %w", f.module(), f.Path, err)
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
		if errors.Is(err, fs.ErrNotExist) {
			// No file — nothing to comment/uncomment.
			return state.ApplyResult{Changed: false}, nil
		}
		return state.ApplyResult{}, fmt.Errorf("%s: read %s: %w", f.module(), f.Path, err)
	}

	if !f.backupSet {
		f.backup = data
		f.backupSet = true
	}

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
