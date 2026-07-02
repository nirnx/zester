package migrate

import (
	"os"
	"path/filepath"
	"strings"
)

// Change records one automated transformation applied to a line.
type Change struct {
	Line   int
	Before string // original line content
	After  string // transformed line content
	Rule   string // rule name (e.g., "salt-functions", "mlist")
}

// Warning records something that needs manual review.
type Warning struct {
	Line    int
	Message string
	Context string // the line content for display
}

// Result holds the transformation result for a single file.
type Result struct {
	Path     string // original file path
	NewPath  string // output path (.sls → .zy, .jinja stays)
	Original string // original content
	Content  string // transformed content
	Changes  []Change
	Warnings []Warning
}

// Options configures the migration.
type Options struct {
	RenameVars bool // also rename grains→facts, pillar→settings
}

// Migrate processes a single file's content and returns the transformation result.
func Migrate(path string, content string, opts Options) *Result {
	r := &Result{
		Path:     path,
		NewPath:  outputPath(path),
		Original: content,
	}

	lines := strings.Split(content, "\n")

	// Apply rules in order. Each rule may modify lines and append changes/warnings.
	saltFunctions(lines, r)
	unknownSaltCalls(lines, r)
	mlistDetection(lines, r)
	octalModes(lines, r)
	requisiteWarnings(lines, r)
	if opts.RenameVars {
		variableRename(lines, r)
	}

	r.Content = strings.Join(lines, "\n")
	return r
}

// MigrateDir walks a directory tree and migrates all .sls and .jinja files.
func MigrateDir(dir string, opts Options) ([]*Result, error) {
	var results []*Result

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		if ext != ".sls" && ext != ".jinja" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		results = append(results, Migrate(path, string(data), opts))
		return nil
	})

	return results, err
}

// outputPath computes the target file path: .sls → .zy, .jinja stays unchanged.
func outputPath(path string) string {
	if strings.HasSuffix(path, ".sls") {
		return strings.TrimSuffix(path, ".sls") + ".zy"
	}
	return path // .jinja files keep their extension
}
