package filemod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// FileAppend implements the file.append state.
// It ensures that specific lines are present in a file.
//
// FileAppend is also its own schema proto: the tagged exported fields ARE
// the module's parameter declaration (one schema declaration per module).
// Text is a paramtypes.StringList (a scalar list element is rendered to a
// string rather than silently dropped, and a bare-string value manages one
// line instead of none — BD-5, the same class as user.present's groups /
// optional_groups). The unexported runtime fields are untagged, so the
// schema compiler skips them.
type FileAppend struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path to the target file; it defaults to the
	// state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target file (defaults to the state ID); the path alias is accepted"`

	// Text is the set of lines that must be present in the file. Each line
	// is checked independently; only missing lines are appended.
	Text paramtypes.StringList `zester:"text" usage:"lines that must be present in the file; each line is checked independently and only missing lines are appended"`

	file exec.FileExec

	// backup stores original content for revert; created records that Apply
	// created the file in this instance.
	backup    []byte
	backupSet bool
	created   bool
}

// fileAppendSpec is the compiled schema + documentation for file.append. It
// is compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code.
var fileAppendSpec = regdef.MustSpec("file.append", modschema.KindState, FileAppend{}, modschema.Doc{
	Summary: "Ensure specific lines are present in a file, appending any that are missing.",
	Description: "`file.append` ensures every line listed in `text` is present somewhere in the " +
		"file at the given path (defaulting to the state ID). Each line is checked independently by " +
		"substring match; only the lines that are missing are appended, in declared order, each " +
		"followed by a newline.",
	Effects: modschema.Effects{
		Check: "A missing file needs a change (it will be created and populated on apply). For an " +
			"existing file, each declared line is checked for presence as a substring of the file's " +
			"content; any missing line needs a change.",
		Apply: "Reads the current content (capturing it as the revert backup on first capture, if the " +
			"file exists). For each declared line not already present, appends it followed by a " +
			"newline — inserting a separating newline first if the existing content does not already " +
			"end with one. A run where every line was already present is a no-op. Reports the count " +
			"of appended lines in its details.",
		Revert: "A file this run's Apply created is removed (tolerating an already-missing file); a " +
			"file that pre-existed is restored to its captured original content. A fresh instance (a " +
			"standalone revert) recorded nothing and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Add entries to /etc/hosts",
			Kind:        "state",
			Explanation: "Each listed line is appended only if not already present.",
			Code: "/etc/hosts:\n  file.append:\n    - text:\n" +
				"      - \"192.168.1.10 db-master\"\n      - \"192.168.1.11 db-replica\"\n",
		},
		{
			Title:       "Append configuration after package install",
			Kind:        "state",
			Explanation: "require ensures the package is installed before the config file is touched.",
			Code: "/etc/security/limits.conf:\n  file.append:\n    - text:\n" +
				"      - \"* soft nofile 65535\"\n      - \"* hard nofile 65535\"\n" +
				"    - require:\n      - \"pkg.installed:base-packages\"\n",
		},
		{
			Title: "Append a line ad hoc",
			Kind:  "cli",
			Explanation: "The bare positional argument is the path. The CLI leg passes `text` as a " +
				"plain string — one line — so a single line is given directly; a multi-line list is " +
				"YAML-only (a bracketed value on the CLI is taken literally, not parsed as a list).",
			Code: "zester 'web-01' file.append /etc/hosts text='192.168.1.10 db-master'",
		},
	},
	Notes: []modschema.Note{
		{
			Title: "Lines are matched by substring",
			Body:  "A declared line is considered present if it occurs anywhere in the file's content, not only as a whole line.",
		},
		{
			Title: "text accepts a single line too",
			Body:  "A bare string (`text: \"some line\"`, or a CLI `text=\"some line\"`) manages exactly that one line, equivalent to a one-element list.",
		},
	},
	Divergences: []string{"BD-5", "BD-6"},
	SeeAlso:     []string{"file.managed", "file.line"},
})

// NewFileAppendBuilder returns a state.Builder that creates FileAppend
// states. Decode policy (unknown-key handling, reserved keys) is threaded via
// opts; the peel supplies it through modules.RegisterAll.
func NewFileAppendBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		f := &FileAppend{}
		if _, err := fileAppendSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.append: %w", err)
		}
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileAppend) Name() string           { return "file.append:" + f.id }
func (f *FileAppend) Reqs() state.Requisites { return f.reqs }

func (f *FileAppend) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.append: read %s: %w", f.Path, err)
		}
		// File doesn't exist — need to create and append.
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("file %s does not exist", f.Path),
		}, nil
	}

	content := string(data)
	var missing []string
	for _, line := range f.Text {
		if !strings.Contains(content, line) {
			missing = append(missing, line)
		}
	}

	if len(missing) > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%d lines missing from %s", len(missing), f.Path),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (f *FileAppend) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Read current content (may not exist).
	existing, err := f.file.ReadFile(ctx, f.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state.ApplyResult{}, fmt.Errorf("file.append: read %s: %w", f.Path, err)
	}
	existed := err == nil
	if existed && !f.backupSet {
		f.backup = existing
		f.backupSet = true
	}

	content := string(existing)
	var appended int

	for _, line := range f.Text {
		if !strings.Contains(content, line) {
			if len(content) > 0 && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			content += line + "\n"
			appended++
		}
	}

	if appended == 0 {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := f.file.WriteFile(ctx, f.Path, []byte(content), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.append: write %s: %w", f.Path, err)
	}
	if !existed {
		f.created = true
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("appended %d lines to %s", appended, f.Path),
		Details: map[string]string{
			"path":  f.Path,
			"lines": fmt.Sprintf("%d", appended),
		},
	}, nil
}

func (f *FileAppend) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevertWithCreate(ctx, f.file, f.Path, f.backup, f.backupSet, f.created, "file.append")
}
