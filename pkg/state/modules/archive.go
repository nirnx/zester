package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// ArchiveExtracted implements the archive.extracted state.
// It extracts a tar or zip archive (from a local path or URL) into a
// target directory. Idempotency is provided by the if_missing path: when
// that path exists the extraction is considered already done.
type ArchiveExtracted struct {
	id   string
	reqs state.Requisites

	// Dir is the target directory the archive is extracted into.
	Dir string

	// Source is a local file path or an http/https/ftp URL to the archive.
	Source string

	// Format is the archive format: "tar", "zip", or "auto" (default).
	Format string

	// IfMissing is a path whose existence means the archive is already
	// extracted. When set, it is the sole idempotency check.
	IfMissing string

	// MakeDirs creates the target directory (and parents) before extracting.
	MakeDirs bool

	cmd  exec.CommandExec
	file exec.FileExec

	// createdByApply tracks whether Apply created the target directory,
	// so Revert knows whether it is safe to remove it.
	createdByApply bool
}

// NewArchiveExtractedBuilder returns a state.Builder that creates
// ArchiveExtracted states using the ModuleContext's command and file providers.
func NewArchiveExtractedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("archive.extracted: no command provider available")
		}
		if mctx.File == nil {
			return nil, fmt.Errorf("archive.extracted: no file provider available")
		}
		return newArchiveExtracted(id, config, mctx.Command, mctx.File)
	}
}

func newArchiveExtracted(id string, config map[string]any, cmd exec.CommandExec, file exec.FileExec) (state.State, error) {
	a := &ArchiveExtracted{id: id, cmd: cmd, file: file}

	a.Dir, _ = config["name"].(string)
	if a.Dir == "" {
		a.Dir = id
	}

	a.Source, _ = config["source"].(string)
	if a.Source == "" {
		return nil, fmt.Errorf("archive.extracted: %s: source is required", id)
	}

	a.Format, _ = config["archive_format"].(string)
	if a.Format == "" {
		a.Format = "auto"
	}
	a.IfMissing, _ = config["if_missing"].(string)
	a.MakeDirs, _ = config["makedirs"].(bool)

	a.reqs = state.ParseRequisites(config)

	return a, nil
}

func (a *ArchiveExtracted) Name() string           { return "archive.extracted:" + a.id }
func (a *ArchiveExtracted) Reqs() state.Requisites { return a.reqs }

func (a *ArchiveExtracted) Check(ctx context.Context) (state.CheckResult, error) {
	// Preferred idempotency: if_missing path.
	if a.IfMissing != "" {
		if _, err := a.file.Stat(ctx, a.IfMissing); err == nil {
			return state.CheckResult{
				NeedsChange: false,
				Diff:        fmt.Sprintf("if_missing path %s exists; already extracted", a.IfMissing),
			}, nil
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("if_missing path %s does not exist", a.IfMissing),
		}, nil
	}

	// Weak fallback: consider extraction done when the target dir exists.
	if _, err := a.file.Stat(ctx, a.Dir); err == nil {
		return state.CheckResult{
			NeedsChange: false,
			Diff:        fmt.Sprintf("target %s exists (no if_missing set)", a.Dir),
		}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("target %s does not exist", a.Dir),
	}, nil
}

func (a *ArchiveExtracted) Apply(ctx context.Context) (state.ApplyResult, error) {
	if a.MakeDirs {
		if _, err := a.file.Stat(ctx, a.Dir); err != nil {
			if err := a.file.MkdirAll(ctx, a.Dir, 0755); err != nil {
				return state.ApplyResult{}, fmt.Errorf("archive.extracted: mkdir %s: %w", a.Dir, err)
			}
			a.createdByApply = true
		}
	}

	archivePath := a.Source
	if isRemoteSource(a.Source) {
		tmp := filepath.Join(os.TempDir(), "zester-archive-"+archiveBaseName(a.Source))
		if err := a.download(ctx, a.Source, tmp); err != nil {
			return state.ApplyResult{}, err
		}
		archivePath = tmp
	}

	format := a.Format
	if format == "" || format == "auto" {
		format = detectArchiveFormat(a.Source)
	}

	var opts exec.CommandOpts
	switch format {
	case "zip":
		opts = exec.CommandOpts{Command: "unzip", Args: []string{"-o", archivePath, "-d", a.Dir}}
	case "tar":
		opts = exec.CommandOpts{Command: "tar", Args: []string{"-xf", archivePath, "-C", a.Dir}}
	default:
		return state.ApplyResult{}, fmt.Errorf("archive.extracted: unsupported format %q", format)
	}

	res, err := a.cmd.Run(ctx, opts)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("archive.extracted: extract %s: %w", a.Source, err)
	}
	if res != nil && res.ExitCode != 0 {
		return state.ApplyResult{}, fmt.Errorf("archive.extracted: extract %s: exit %d: %s", a.Source, res.ExitCode, res.Stderr)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("extracted %s into %s (%s)", a.Source, a.Dir, format),
		Details: map[string]string{
			"source": a.Source,
			"target": a.Dir,
			"format": format,
		},
	}, nil
}

func (a *ArchiveExtracted) Revert(ctx context.Context) (state.ApplyResult, error) {
	if !a.createdByApply {
		return state.ApplyResult{
			Changed: false,
			Diff:    "archive.extracted: target was not created by this apply; skipping removal",
		}, nil
	}
	if err := a.file.RemoveAll(ctx, a.Dir); err != nil {
		return state.ApplyResult{}, fmt.Errorf("archive.extracted: remove %s: %w", a.Dir, err)
	}
	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed extracted directory %s", a.Dir),
	}, nil
}

// download fetches url into dst using curl, falling back to wget.
func (a *ArchiveExtracted) download(ctx context.Context, url, dst string) error {
	cmd := fmt.Sprintf("command -v curl >/dev/null 2>&1 && curl -fSL -o %s %s || wget -O %s %s",
		shQuote(dst), shQuote(url), shQuote(dst), shQuote(url))
	res, err := a.cmd.Run(ctx, exec.CommandOpts{Command: cmd, Shell: true})
	if err != nil {
		return fmt.Errorf("archive.extracted: download %s: %w", url, err)
	}
	if res != nil && res.ExitCode != 0 {
		return fmt.Errorf("archive.extracted: download %s: exit %d: %s", url, res.ExitCode, res.Stderr)
	}
	return nil
}

// isRemoteSource reports whether source is a downloadable URL.
func isRemoteSource(source string) bool {
	s := strings.ToLower(source)
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "ftp://")
}

// detectArchiveFormat infers "zip" or "tar" from a filename/URL.
func detectArchiveFormat(source string) string {
	s := strings.ToLower(source)
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	switch {
	case strings.HasSuffix(s, ".zip"):
		return "zip"
	case strings.HasSuffix(s, ".tgz"),
		strings.HasSuffix(s, ".tbz2"),
		strings.HasSuffix(s, ".txz"),
		strings.Contains(s, ".tar"):
		return "tar"
	default:
		return "tar"
	}
}

// archiveBaseName returns a sanitized base filename for a source URL/path.
func archiveBaseName(source string) string {
	if i := strings.IndexByte(source, '?'); i >= 0 {
		source = source[:i]
	}
	base := filepath.Base(source)
	if base == "" || base == "." || base == "/" {
		return "download"
	}
	return base
}

// shQuote wraps s in single quotes for safe use in an sh -c command.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
