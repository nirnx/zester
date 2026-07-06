package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

const (
	defaultMarkerStart = "# START managed zone"
	defaultMarkerEnd   = "# END managed zone"
)

// FileBlockReplace implements the file.blockreplace state.
// It manages content between marker lines in a file.
type FileBlockReplace struct {
	id   string
	reqs state.Requisites

	// Path is the file path to manage.
	Path string

	// Content is the desired content between the markers.
	Content string

	// MarkerStart is the line that starts the managed block.
	MarkerStart string

	// MarkerEnd is the line that ends the managed block.
	MarkerEnd string

	// AppendIfNotFound appends the block if the markers are not found.
	AppendIfNotFound bool

	// AppendNewline adds a trailing newline after the content block.
	AppendNewline bool

	file exec.FileExec

	// backup stores original content for revert.
	backup    []byte
	backupSet bool
}

// NewFileBlockReplaceBuilder returns a state.Builder that creates FileBlockReplace states.
func NewFileBlockReplaceBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		return newFileBlockReplace(id, config, mctx.File)
	}
}

func newFileBlockReplace(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	f := &FileBlockReplace{id: id, file: file}

	f.Path, _ = config["name"].(string)
	if f.Path == "" {
		f.Path = id
	}

	f.Content, _ = config["content"].(string)

	f.MarkerStart, _ = config["marker_start"].(string)
	if f.MarkerStart == "" {
		f.MarkerStart = defaultMarkerStart
	}

	f.MarkerEnd, _ = config["marker_end"].(string)
	if f.MarkerEnd == "" {
		f.MarkerEnd = defaultMarkerEnd
	}

	f.AppendIfNotFound, _ = config["append_if_not_found"].(bool)
	f.AppendNewline, _ = config["append_newline"].(bool)

	f.reqs = state.ParseRequisites(config)

	return f, nil
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
	// Save backup for revert.
	existing, err := f.file.ReadFile(ctx, f.Path)
	if err == nil {
		f.backup = existing
		f.backupSet = true
	}

	desiredContent := f.Content
	if !strings.HasSuffix(desiredContent, "\n") {
		desiredContent += "\n"
	}

	if !f.backupSet {
		// File doesn't exist — create it with the block if append_if_not_found.
		if !f.AppendIfNotFound {
			return state.ApplyResult{Changed: false}, nil
		}
		newContent := f.MarkerStart + "\n" + desiredContent + f.MarkerEnd + "\n"
		if err := f.file.WriteFile(ctx, f.Path, []byte(newContent), 0644); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.blockreplace: write %s: %w", f.Path, err)
		}
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
	if !f.backupSet {
		if err := f.file.Remove(ctx, f.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.blockreplace: revert remove %s: %w", f.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed %s (revert block replace to new file)", f.Path),
		}, nil
	}

	if err := f.file.WriteFile(ctx, f.Path, f.backup, 0644); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.blockreplace: revert %s: %w", f.Path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("reverted %s to previous content", f.Path),
	}, nil
}
