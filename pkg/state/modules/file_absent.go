package modules

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// FileAbsent implements the file.absent state.
// It ensures a file or directory does not exist.
//
// FileAbsent is also its own schema proto: the tagged exported Path field IS
// the module's parameter declaration (one schema declaration per module). The
// unexported runtime fields (id, reqs, file) are untagged, so the schema
// compiler skips them.
type FileAbsent struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path to the file or directory to remove; it
	// defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the file or directory to remove (defaults to the state ID); the path alias is accepted"`

	// file is the injected file execution provider.
	file exec.FileExec
}

// fileAbsentSpec is the compiled schema + documentation for file.absent. It is
// compiled once at package init and executed by every decode path (the
// builder below, and Registry.Parse). The prose is verified against the live
// Check/Apply/Revert code.
var fileAbsentSpec = mustSpec("file.absent", modschema.KindState, FileAbsent{}, modschema.Doc{
	Summary: "Ensure a file or directory does not exist.",
	Description: "`file.absent` ensures nothing exists at the given path, removing a file or an " +
		"entire directory tree. The path defaults to the state ID, so a bare `file.absent` under an " +
		"`/opt/old-app:` key removes `/opt/old-app`.",
	Effects: modschema.Effects{
		Check: "Stats the path. A stat error (the common case: the path does not exist) means no " +
			"change is needed; any successful stat — file or directory — needs a change.",
		Apply: "Removes the path via `RemoveAll`, which recursively deletes a directory tree. " +
			"Reports the removed path in its details.",
		Revert: "Cannot restore the removed content: nothing is captured before removal, so Revert " +
			"is an explicit no-op rather than a guessed re-creation.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Remove a file by state ID",
			Kind:        "state",
			Explanation: "The state ID is the path to remove; no parameters are needed.",
			Code:        "/etc/nginx/sites-enabled/default:\n  file.absent: []\n",
		},
		{
			Title:       "Remove a named path after stopping a dependent",
			Kind:        "state",
			Explanation: "name overrides the path; require ensures the dependent service is stopped first.",
			Code: "remove_old_app:\n  file.absent:\n    - name: /opt/old-app\n    - require:\n" +
				"      - \"cmd.run:stop_old_app\"\n",
		},
		{
			Title:       "Remove a path ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the path.",
			Code:        "zester 'web-01' file.absent /tmp/old-config.txt",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Revert does not restore removed content",
			Body: "Because nothing is captured before removal, Revert cannot be Apply's inverse. It " +
				"is an honest no-op.",
		},
		{
			Title: "Removes directories recursively",
			Body:  "A path that is a directory is removed with all of its contents, equivalent to `rm -rf`.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"file.managed", "file.touch"},
})

// NewFileAbsentBuilder returns a state.Builder that creates FileAbsent states
// using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewFileAbsentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		f := &FileAbsent{}
		if _, err := fileAbsentSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.absent: %w", err)
		}
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileAbsent) Name() string           { return "file.absent:" + f.id }
func (f *FileAbsent) Reqs() state.Requisites { return f.reqs }

func (f *FileAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	_, err := f.file.Stat(ctx, f.Path)
	if err != nil {
		// File doesn't exist — no change needed.
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("%s exists and should be absent", f.Path),
	}, nil
}

func (f *FileAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	if err := f.file.RemoveAll(ctx, f.Path); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.absent: remove %s: %w", f.Path, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %s", f.Path),
		Details: map[string]string{"path": f.Path},
	}, nil
}

func (f *FileAbsent) Revert(_ context.Context) (state.ApplyResult, error) {
	return state.ApplyResult{
		Changed: false,
		Diff:    "cannot revert file removal",
	}, nil
}
