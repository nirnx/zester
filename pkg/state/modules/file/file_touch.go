package filemod

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// FileTouch implements the file.touch state.
// It creates an empty file when missing, or updates the modification time of
// an existing file. Modeled after Salt's file.touch module.
//
// FileTouch is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Path is
// a plain string declared `name,primary,aliases=path` (the file_managed.go
// alias tag is the exemplar — `path` is accepted for Salt compatibility, same
// as the legacy fsxResolvePath helper). The unexported runtime fields are
// untagged, so the schema compiler skips them.
type FileTouch struct {
	id   string
	reqs state.Requisites

	// Path is the file to touch; it defaults to the state ID. The path alias
	// is accepted for Salt compatibility.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the file to touch; the path alias is accepted for Salt compatibility (defaults to the state ID)"`

	// MakeDirs: file.* family component (canonical parent-creation contract).
	fileMakeDirsParam

	file exec.FileExec
	cmd  exec.CommandExec

	createdNew bool
}

// fileTouchSpec is the compiled schema + documentation for file.touch. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code.
var fileTouchSpec = regdef.MustSpec("file.touch", modschema.KindState, FileTouch{}, modschema.Doc{
	Summary: "Create an empty file when missing, or update its modification time.",
	Description: "`file.touch` ensures a file exists, creating it empty when missing. The path " +
		"defaults to the state ID; `path` is accepted as an alias for Salt compatibility. If the file " +
		"already exists, Check reports no change is needed regardless of its timestamp — the " +
		"modification-time update only runs when the state is force-applied (for example by a " +
		"`watch` requisite).",
	Effects: modschema.Effects{
		Check: "Stats the path. An existing file (any content) needs no change; a missing path " +
			"needs a change.",
		Apply: "Creates missing parent directories first when `makedirs` is set. If the file is " +
			"missing, creates it empty with the default file mode. If the file already exists (only " +
			"reached on a force-apply), runs the system `touch` command to update its modification " +
			"time — a no-op (`Changed: false`) when no command provider is available.",
		Revert: "A file this run's Apply created is removed. A file that already existed (Apply only " +
			"updated its mtime) is left alone — the previous timestamp is not restored, so Revert is " +
			"a clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Create a marker file",
			Kind:        "state",
			Explanation: "makedirs creates any missing parent directories before the empty file is created.",
			Code:        "/var/lib/myapp/.provisioned:\n  file.touch:\n    - makedirs: true\n",
		},
		{
			Title:       "Bump a reload trigger whenever config changes",
			Kind:        "state",
			Explanation: "A watch requisite force-applies the state, which updates the file's mtime via the system touch command.",
			Code: "/etc/myapp/reload-trigger:\n  file.touch:\n    - watch:\n" +
				"      - \"file.managed:/etc/myapp/config.yaml\"\n",
		},
		{
			Title:       "Touch a file ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the path.",
			Code:        "zester 'web-01' file.touch /var/run/myapp/.initialized",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "The mtime is not refreshed on a normal run when the file already exists",
			Body: "Salt's `file.touch` updates atime/mtime every time it runs; Zester's Check reports " +
				"an existing file as satisfied, so the timestamp-update path in Apply only executes " +
				"when the state is force-applied (for example by a `watch` requisite).",
		},
		{
			Title: "Explicit timestamps are not supported",
			Body:  "Salt's `atime`/`mtime` parameters (setting explicit timestamps) have no equivalent here.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"file.managed", "file.absent"},
})

// NewFileTouchBuilder returns a state.Builder that creates FileTouch states.
// Decode policy (unknown-key handling, reserved keys) is threaded via opts;
// the peel supplies it through modules.RegisterAll.
func NewFileTouchBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.touch: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		f := &FileTouch{}
		if _, err := fileTouchSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.touch: %w", err)
		}
		f.id = id
		f.file = mctx.File
		f.cmd = mctx.Command
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileTouch) Name() string           { return "file.touch:" + f.id }
func (f *FileTouch) Reqs() state.Requisites { return f.reqs }

func (f *FileTouch) exists(ctx context.Context) bool {
	_, err := f.file.Stat(ctx, f.Path)
	return err == nil
}

func (f *FileTouch) Check(ctx context.Context) (state.CheckResult, error) {
	// Canonical makedirs contract (§13): a missing parent with makedirs
	// unset fails Check too — never reported as an applicable change.
	if err := requireParentDirs(ctx, f.file, "file.touch", f.Path, f.MakeDirs); err != nil {
		return state.CheckResult{}, err
	}
	if f.exists(ctx) {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("file %s does not exist", f.Path),
	}, nil
}

func (f *FileTouch) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := ensureParentDirs(ctx, f.file, "file.touch", f.Path, f.MakeDirs); err != nil {
		return state.ApplyResult{}, err
	}

	if !f.exists(ctx) {
		if err := f.file.WriteFile(ctx, f.Path, []byte{}, fsModeDefault); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.touch: create %s: %w", f.Path, err)
		}
		f.createdNew = true
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("created %s", f.Path),
			Details: map[string]string{"path": f.Path, "action": "created"},
		}, nil
	}

	// File exists: update its modification time via `touch` when possible.
	if f.cmd != nil {
		if _, err := f.cmd.Run(ctx, exec.CommandOpts{
			Command: "touch",
			Args:    []string{f.Path},
		}); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.touch: touch %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("updated mtime of %s", f.Path),
			Details: map[string]string{"path": f.Path, "action": "mtime"},
		}, nil
	}

	return state.ApplyResult{Changed: false}, nil
}

func (f *FileTouch) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !f.createdNew {
		return state.ApplyResult{
			Changed: false,
			Diff:    "file.touch on existing file cannot be reverted",
		}, nil
	}
	if err := f.file.Remove(ctx, f.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.touch: revert remove %s: %w", f.Path, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s (revert touch)", f.Path),
	}, nil
}
