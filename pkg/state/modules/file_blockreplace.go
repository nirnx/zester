package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

const (
	defaultMarkerStart = "# START managed zone"
	defaultMarkerEnd   = "# END managed zone"
)

// FileBlockReplace implements the file.blockreplace state.
// It manages content between marker lines in a file.
//
// FileBlockReplace is also its own schema proto: the tagged exported fields ARE
// the module's parameter declaration (one schema declaration per module). The
// primary `name` has NO `path` alias — the legacy constructor read `name` only
// (unlike the fsxResolvePath-based file modules), so one is deliberately not
// added here (parity). `marker_start`/`marker_end` carry eager defaults matching
// the legacy `defaultMarkerStart`/`defaultMarkerEnd` constants. The unexported
// runtime fields (id, reqs, injected provider, revert memos) are untagged, so
// the compiler skips them.
type FileBlockReplace struct {
	id   string
	reqs state.Requisites

	// Path is the file path to manage; it defaults to the state ID.
	Path string `zester:"name,primary" usage:"absolute path to the target file; defaults to the state ID"`

	// Content is the desired content between the markers.
	Content string `zester:"content" usage:"desired content placed between the marker lines"`

	// MarkerStart is the line that starts the managed block.
	MarkerStart string `zester:"marker_start,default=# START managed zone" usage:"line that starts the managed block"`

	// MarkerEnd is the line that ends the managed block.
	MarkerEnd string `zester:"marker_end,default=# END managed zone" usage:"line that ends the managed block"`

	// AppendIfNotFound appends the block if the markers are not found.
	AppendIfNotFound bool `zester:"append_if_not_found" usage:"append (or create) the managed block when the markers are absent; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// AppendNewline adds a trailing newline after the content block.
	AppendNewline bool `zester:"append_newline" usage:"add a trailing newline after an appended block; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	file exec.FileExec

	// backup stores original content for revert; created records that Apply
	// created the file in this instance.
	backup    []byte
	backupSet bool
	created   bool
}

// fileBlockReplaceSpec is the compiled schema + documentation for
// file.blockreplace. It is compiled once at package init and executed by every
// decode path (the builder below, and Registry.Parse). The prose is verified
// against the live Check/Apply/Revert code (notably: a missing block with
// append_if_not_found off is a clean no-op, and existence is re-derived per
// Apply invocation).
var fileBlockReplaceSpec = mustSpec("file.blockreplace", modschema.KindState, FileBlockReplace{}, modschema.Doc{
	Summary: "Manage a block of content between marker lines in a file.",
	Description: "`file.blockreplace` keeps the content between a start and end marker line in sync with " +
		"`content`. The path defaults to the state ID (this module has no `path` alias). The markers " +
		"default to `# START managed zone` / `# END managed zone` and can be overridden with " +
		"`marker_start`/`marker_end`. When the markers are absent, `append_if_not_found` appends the whole " +
		"block (creating the file if it does not exist); without it, a file or block that is missing is a " +
		"clean no-op.",
	Effects: modschema.Effects{
		Check: "Reads the file and locates the marker block. When the block is present, it reports a " +
			"change only if the content between the markers differs from `content` (normalized to a trailing " +
			"newline). When the file or block is absent, a change is needed only if `append_if_not_found` is " +
			"set. Any read error other than not-exist fails the check.",
		Apply: "Re-derives existence from a fresh read every invocation (never from a stale instance flag). " +
			"On a missing file it creates the block when `append_if_not_found` is set (else no-op). On a " +
			"present file it replaces the content between the markers, or — when the markers are absent and " +
			"`append_if_not_found` is set — appends the block (adding a trailing newline when " +
			"`append_newline` is set). Captures the original for revert on the first write and writes with " +
			"mode 0644.",
		Revert: "Restores what this run's Apply changed: a file that pre-existed is rewritten with its " +
			"captured prior content; a file this instance created is removed (tolerating an already-missing " +
			"file). A fresh instance (a standalone revert) recorded nothing and is an explicit clean no-op.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Manage a marked block in a config file",
			Kind:        "state",
			Explanation: "The default markers delimit the managed zone; append_if_not_found seeds the block on first run.",
			Code: "/etc/hosts:\n  file.blockreplace:\n    - content: |\n        10.0.0.1 app\n" +
				"        10.0.0.2 db\n    - append_if_not_found: true\n",
		},
		{
			Title:       "Custom markers",
			Kind:        "state",
			Explanation: "marker_start/marker_end override the default managed-zone markers.",
			Code: "/etc/nginx/nginx.conf:\n  file.blockreplace:\n    - content: \"    include /etc/nginx/managed/*.conf;\"\n" +
				"    - marker_start: \"# BEGIN zester\"\n    - marker_end: \"# END zester\"\n" +
				"    - append_if_not_found: true\n",
		},
		{
			Title:       "Manage a block ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional is the path; content and flags are key=value args.",
			Code:        "zester 'web*' file.blockreplace /etc/hosts content='10.0.0.1 app' append_if_not_found=true",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Only the marked block is modified",
			Body: "Lines before the start marker and after the end marker are never modified — only the " +
				"content between the markers (or an appended block when the markers are absent) is written.",
		},
		{
			Level: "info",
			Title: "Markers must match exactly",
			Body: "A marker line is matched exactly (after trimming a trailing carriage return). Whitespace " +
				"or comment-character differences mean the block is treated as absent, so keep the markers " +
				"identical across runs.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"file.line", "file.replace", "file.managed"},
})

// NewFileBlockReplaceBuilder returns a state.Builder that creates
// FileBlockReplace states. Decode policy (unknown-key handling, reserved keys)
// is threaded via opts; the peel supplies it through modules.RegisterAll.
func NewFileBlockReplaceBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.blockreplace: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		f := &FileBlockReplace{}
		if _, err := fileBlockReplaceSpec.Decode(id, config, f, opts); err != nil {
			return nil, fmt.Errorf("file.blockreplace: %w", err)
		}
		f.id = id
		f.file = mctx.File
		f.reqs = state.ParseRequisites(config)
		return f, nil
	}
}

func (f *FileBlockReplace) Name() string           { return "file.blockreplace:" + f.id }
func (f *FileBlockReplace) Reqs() state.Requisites { return f.reqs }

// findBlock searches for the managed block in lines, returning start index (of marker),
// end index (of end marker), and the current block content between markers.
// Returns (-1, -1, "") if the block is not found.
func findBlock(lines []string, markerStart, markerEnd string) (startIdx, endIdx int, current string) {
	startIdx = -1
	endIdx = -1

	for i, line := range lines {
		if strings.TrimRight(line, "\r") == markerStart {
			startIdx = i
		} else if startIdx != -1 && strings.TrimRight(line, "\r") == markerEnd {
			endIdx = i
			// Collect lines between markers.
			between := lines[startIdx+1 : endIdx]
			current = strings.Join(between, "\n")
			if len(between) > 0 {
				current += "\n"
			}
			return startIdx, endIdx, current
		}
	}

	return -1, -1, ""
}

func (f *FileBlockReplace) Check(ctx context.Context) (state.CheckResult, error) {
	data, err := f.file.ReadFile(ctx, f.Path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.blockreplace: read %s: %w", f.Path, err)
		}
		if f.AppendIfNotFound {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("file %s does not exist", f.Path),
			}, nil
		}
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("file %s does not exist and append_if_not_found is false", f.Path),
		}, nil
	}

	lines := strings.Split(string(data), "\n")
	startIdx, endIdx, current := findBlock(lines, f.MarkerStart, f.MarkerEnd)

	if startIdx == -1 || endIdx == -1 {
		if f.AppendIfNotFound {
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("managed block not found in %s", f.Path),
			}, nil
		}
		return state.CheckResult{NeedsChange: false}, nil
	}

	desiredContent := f.Content
	if !strings.HasSuffix(desiredContent, "\n") {
		desiredContent += "\n"
	}

	if current == desiredContent {
		return state.CheckResult{NeedsChange: false}, nil
	}

	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("managed block content differs in %s", f.Path),
	}, nil
}

func (f *FileBlockReplace) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Fresh read: existence is decided per invocation from this read alone,
	// never from instance fields a previous invocation may have set.
	existing, err := f.file.ReadFile(ctx, f.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state.ApplyResult{}, fmt.Errorf("file.blockreplace: read %s: %w", f.Path, err)
	}
	existed := err == nil
	if existed && !f.backupSet {
		// Save backup for revert.
		f.backup = existing
		f.backupSet = true
	}

	desiredContent := f.Content
	if !strings.HasSuffix(desiredContent, "\n") {
		desiredContent += "\n"
	}

	if !existed {
		// File doesn't exist — create it with the block if append_if_not_found.
		if !f.AppendIfNotFound {
			return state.ApplyResult{Changed: false}, nil
		}
		newContent := f.MarkerStart + "\n" + desiredContent + f.MarkerEnd + "\n"
		if err := f.file.WriteFile(ctx, f.Path, []byte(newContent), 0644); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.blockreplace: write %s: %w", f.Path, err)
		}
		f.created = true
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("created %s with managed block", f.Path),
			Details: map[string]string{"path": f.Path, "action": "created"},
		}, nil
	}

	lines := strings.Split(string(existing), "\n")
	startIdx, endIdx, _ := findBlock(lines, f.MarkerStart, f.MarkerEnd)

	if startIdx == -1 || endIdx == -1 {
		// Markers not found — append if configured.
		if !f.AppendIfNotFound {
			return state.ApplyResult{Changed: false}, nil
		}
		content := string(existing)
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += f.MarkerStart + "\n" + desiredContent + f.MarkerEnd + "\n"
		if f.AppendNewline {
			content += "\n"
		}
		if err := f.file.WriteFile(ctx, f.Path, []byte(content), 0644); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.blockreplace: write %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("appended managed block to %s", f.Path),
			Details: map[string]string{"path": f.Path, "action": "appended"},
		}, nil
	}

	// Replace content between markers.
	var newLines []string
	newLines = append(newLines, lines[:startIdx+1]...)
	contentLines := strings.Split(strings.TrimSuffix(desiredContent, "\n"), "\n")
	newLines = append(newLines, contentLines...)
	newLines = append(newLines, lines[endIdx:]...)

	newContent := strings.Join(newLines, "\n")
	if err := f.file.WriteFile(ctx, f.Path, []byte(newContent), 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.blockreplace: write %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("replaced managed block content in %s", f.Path),
		Details: map[string]string{"path": f.Path, "action": "replaced"},
	}, nil
}

func (f *FileBlockReplace) Revert(ctx context.Context) (state.ApplyResult, error) {
	return fsxRevertWithCreate(ctx, f.file, f.Path, f.backup, f.backupSet, f.created, "file.blockreplace")
}
