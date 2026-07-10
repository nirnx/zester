package modules

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// ArchiveExtracted implements the archive.extracted state.
// It extracts a tar or zip archive (from a local path or URL) into a
// target directory.
//
// Idempotency semantics (weakest to strongest):
//   - Without if_missing or source_hash, the state is considered already
//     extracted when the target dir exists — a PRE-EXISTING target (or a dir
//     left behind by makedirs after a failed extraction) therefore counts as
//     done and the archive is never (re-)extracted. Declare if_missing and/or
//     source_hash for anything beyond throwaway use.
//   - if_missing: the existence of that path is the extraction marker
//     (Salt parity); the target-dir fallback is not consulted.
//   - source_hash: the declared value is recorded in a marker file inside
//     the target dir after a successful extraction; Check re-extracts when
//     the declaration no longer matches the recorded value (version bump).
//     The value is an opaque declaration (e.g. "sha256=<hex>") compared as a
//     string — it is NOT verified against the archive bytes.
//
// The guard is evaluated in BOTH Check and Apply (watch-forced applies
// bypass Check): an already-extracted archive is a clean Apply no-op.
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
	// extracted. When set, it replaces the weak target-dir existence check.
	IfMissing string

	// SourceHash is the declared hash of the source archive (any stable
	// string, e.g. "sha256=<hex>"). When set, it is recorded in a marker
	// file after a successful extraction and Check requires the recorded
	// value to match — changing the declaration triggers re-extraction.
	// Compared as an opaque string, never verified against archive bytes.
	SourceHash string

	// MakeDirs creates the target directory (and parents) before extracting.
	MakeDirs bool

	cmd  exec.CommandExec
	file exec.FileExec

	// createdByApply tracks whether Apply created the target directory,
	// so Revert knows whether it is safe to remove it.
	createdByApply bool

	// extractedByApply tracks whether a same-instance Apply completed a
	// successful extraction. It distinguishes "the target dir exists because
	// extraction succeeded" from "the target dir exists only because this
	// instance's makedirs created it before a failed extraction" — a retried
	// Apply must proceed instead of latching on its own directory.
	extractedByApply bool
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
	sh, _ := config["source_hash"].(string)
	a.SourceHash = strings.TrimSpace(sh)

	a.reqs = state.ParseRequisites(config)

	return a, nil
}

func (a *ArchiveExtracted) Name() string           { return "archive.extracted:" + a.id }
func (a *ArchiveExtracted) Reqs() state.Requisites { return a.reqs }

// markerPath returns the per-state marker file recording the source_hash of
// the last successful extraction. It is keyed on the state id (stable across
// source version bumps) and lives inside the target dir, so removing the
// tree also resets the marker.
func (a *ArchiveExtracted) markerPath() string {
	sum := sha256.Sum256([]byte(a.id))
	return filepath.Join(a.Dir, fmt.Sprintf(".zester-archive-%x.hash", sum[:8]))
}

// alreadyExtracted evaluates the extraction guard — the if_missing path
// (preferred, Salt parity) or the weak target-dir fallback, plus the
// source_hash marker — and reports whether the archive is already extracted,
// with the reason. Only fs.ErrNotExist counts as absent; any other stat/read
// error fails the phase rather than triggering a re-extraction. The guard is
// evaluated independently in BOTH Check and Apply: watch-forced applies
// bypass Check entirely, and a converged archive must not be re-downloaded
// and re-extracted (clobbering post-extraction local modifications
// if_missing exists to protect) just because a watched dependency changed —
// Salt's archive.extracted checks if_missing in the state function itself.
func (a *ArchiveExtracted) alreadyExtracted(ctx context.Context) (bool, string, error) {
	if a.IfMissing != "" {
		if _, err := a.file.Stat(ctx, a.IfMissing); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return false, "", fmt.Errorf("archive.extracted: stat %s: %w", a.IfMissing, err)
			}
			return false, fmt.Sprintf("if_missing path %s does not exist", a.IfMissing), nil
		}
	} else {
		if _, err := a.file.Stat(ctx, a.Dir); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return false, "", fmt.Errorf("archive.extracted: stat %s: %w", a.Dir, err)
			}
			return false, fmt.Sprintf("target %s does not exist", a.Dir), nil
		}
		if a.createdByApply && !a.extractedByApply {
			// The dir exists only because this instance's makedirs created
			// it before a failed extraction — no evidence of prior success;
			// a retried Apply must proceed instead of no-oping.
			return false, fmt.Sprintf("target %s was created by this run's failed attempt", a.Dir), nil
		}
	}

	// Declared source_hash: the recorded marker must match the declaration,
	// so bumping source/source_hash re-extracts instead of no-oping forever.
	if a.SourceHash != "" {
		recorded, existed, err := readManagedFile(ctx, a.file, a.markerPath())
		if err != nil {
			return false, "", fmt.Errorf("archive.extracted: read marker %s: %w", a.markerPath(), err)
		}
		if !existed {
			return false, fmt.Sprintf("source_hash declared but no extraction marker at %s", a.markerPath()), nil
		}
		if strings.TrimSpace(recorded) != a.SourceHash {
			return false, fmt.Sprintf("source_hash changed (recorded %q, declared %q)", strings.TrimSpace(recorded), a.SourceHash), nil
		}
	}

	if a.IfMissing != "" {
		return true, fmt.Sprintf("if_missing path %s exists; already extracted", a.IfMissing), nil
	}
	return true, fmt.Sprintf("target %s exists (no if_missing set)", a.Dir), nil
}

func (a *ArchiveExtracted) Check(ctx context.Context) (state.CheckResult, error) {
	done, reason, err := a.alreadyExtracted(ctx)
	if err != nil {
		return state.CheckResult{}, err
	}
	return state.CheckResult{
		NeedsChange: !done,
		Diff:        reason,
	}, nil
}

func (a *ArchiveExtracted) Apply(ctx context.Context) (state.ApplyResult, error) {
	// Re-evaluate the guard here, not just in Check: a watch-forced apply
	// skips Check, and an already-extracted archive must be a clean no-op —
	// never a re-download/re-extract over local modifications.
	done, reason, err := a.alreadyExtracted(ctx)
	if err != nil {
		return state.ApplyResult{}, err
	}
	if done {
		return state.ApplyResult{
			Changed: false,
			Diff:    reason + "; nothing to do",
		}, nil
	}

	if a.MakeDirs {
		if _, err := a.file.Stat(ctx, a.Dir); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return state.ApplyResult{}, fmt.Errorf("archive.extracted: stat %s: %w", a.Dir, err)
			}
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

	// Record the declared source_hash only AFTER a successful extraction, so
	// a failed download/extract never latches the state as done.
	if a.SourceHash != "" {
		if err := a.file.WriteFile(ctx, a.markerPath(), []byte(a.SourceHash+"\n"), 0644); err != nil {
			return state.ApplyResult{}, fmt.Errorf("archive.extracted: write marker %s: %w", a.markerPath(), err)
		}
	}
	a.extractedByApply = true

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
