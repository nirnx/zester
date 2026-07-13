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
	"github.com/nirnx/zester/pkg/modschema"
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
//
// ArchiveExtracted is also its own schema proto: the tagged exported fields
// ARE the module's parameter declaration (one schema declaration per module) —
// all five are primitives (four strings and a bool), no semantic types are
// needed. `source` is `required`; `archive_format` carries an EAGER
// `default=auto`, reproducing the legacy construction-time default. Under the
// uniform decoder a numeric `name`/`source`/`archive_format`/`if_missing`/
// `source_hash` coerces to its string form and a composite is rejected
// (BD-6); `makedirs` honors an integer 1/0 (BD-7) and a truthy/falsy string
// (BD-2) where the legacy `.(bool)` assertion silently dropped them. The
// `source_hash` TrimSpace stays in the builder tail (a decoder never trims —
// same convention as ssh_auth.present's name). The unexported runtime fields
// (id, reqs, cmd, file, revert memos) are untagged, so the schema compiler
// skips them.
type ArchiveExtracted struct {
	id   string
	reqs state.Requisites

	// Dir is the target directory the archive is extracted into; it defaults
	// to the state ID.
	Dir string `zester:"name,primary" usage:"target directory the archive is extracted into; defaults to the state ID"`

	// Source is a local file path or an http/https/ftp URL to the archive.
	Source string `zester:"source,required" usage:"local file path, or an http/https/ftp URL to the archive; required"`

	// Format is the archive format: "tar", "zip", or "auto" (default).
	Format string `zester:"archive_format,default=auto" usage:"archive format: tar, zip, or auto (inferred from the source name); defaults to auto"`

	// IfMissing is a path whose existence means the archive is already
	// extracted. When set, it replaces the weak target-dir existence check.
	IfMissing string `zester:"if_missing" usage:"path whose existence means the archive is already extracted; when set, it is the sole idempotency check (besides source_hash)"`

	// SourceHash is the declared hash of the source archive (any stable
	// string, e.g. "sha256=<hex>"). When set, it is recorded in a marker
	// file after a successful extraction and Check requires the recorded
	// value to match — changing the declaration triggers re-extraction.
	// Compared as an opaque string, never verified against archive bytes.
	// TrimSpace'd in the builder tail (a decoder never trims).
	SourceHash string `zester:"source_hash" usage:"declared hash of the source archive (e.g. sha256=<hex>); recorded after extraction and compared as an opaque string on later runs — NOT verified against the archive bytes"`

	// MakeDirs creates the target directory (and parents) before extracting.
	MakeDirs bool `zester:"makedirs" usage:"create the target directory (and parents, mode 0755) before extracting; a boolean that also accepts the integers 1 (true) and 0 (false)"`

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

// archiveExtractedSpec is the compiled schema + documentation for
// archive.extracted. It is compiled once at package init and executed by
// every decode path (the builder below, and Registry.Parse). The prose is
// drift-corrected against the live Check/Apply/Revert behavior — notably that
// `source_hash` verification IS supported (the hand page predated it).
var archiveExtractedSpec = mustSpec("archive.extracted", modschema.KindState, ArchiveExtracted{}, modschema.Doc{
	Summary: "Extract a tar or zip archive (from a local path or URL) into a target directory.",
	Description: "`archive.extracted` extracts `source` (a local file path or an http/https/ftp URL) into " +
		"`name` (the target directory, defaulting to the state ID). Idempotency has three independent " +
		"strengths, weakest to strongest: without `if_missing` or `source_hash`, a PRE-EXISTING target " +
		"directory alone counts as \"already extracted\" (declare one of them for anything beyond " +
		"throwaway use); `if_missing` names a path whose existence is the sole extraction marker (Salt " +
		"parity), replacing the weak directory check; `source_hash` records the declared value in a " +
		"marker file after a successful extraction and re-extracts when the declaration no longer matches " +
		"— it is an OPAQUE string comparison, never a checksum verified against the archive bytes.",
	Effects: modschema.Effects{
		Check: "With `if_missing` set: reports a change unless that path exists (the target-directory " +
			"fallback is not consulted). Without it: reports a change unless the target directory exists — " +
			"and a directory this run's `makedirs` created ahead of a FAILED extraction attempt does not " +
			"count (a retried Apply proceeds rather than latching on its own directory). When `source_hash` " +
			"is declared, ALSO requires a marker file recording that exact value to exist inside the target " +
			"— an unset or mismatched marker reports a change even if the directory/`if_missing` guard " +
			"passed, so bumping `source`/`source_hash` re-extracts.",
		Apply: "Re-evaluates the same guard as Check (a watch-forced apply bypasses Check entirely, and an " +
			"already-extracted archive must be a clean no-op — never a re-download/re-extract over local " +
			"modifications). When extraction is needed: creates the target directory (mode 0755) when " +
			"`makedirs` is set and it does not already exist; downloads a remote `source` (http/https/ftp) " +
			"to a temp file via `curl -fSL`, falling back to `wget` when curl is unavailable; extracts with " +
			"`unzip -o <archive> -d <dir>` (zip) or `tar -xf <archive> -C <dir>` (tar), inferring the " +
			"format from the source name when `archive_format` is `auto` (`.zip` → zip; `.tgz`/`.tbz2`/" +
			"`.txz`/anything containing `.tar` → tar; anything else defaults to tar); a non-zero exit fails " +
			"the state. Only AFTER a successful extraction does it write the `source_hash` marker (a failed " +
			"attempt never latches the state as done). Reports the source, target, and resolved format in " +
			"its details.",
		Revert: "If Apply created the target directory (via `makedirs`), removes it recursively. Otherwise " +
			"an explicit no-op (`Changed: false`) — individual extracted files are not tracked, so there is " +
			"nothing else safe to remove.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Extract a release tarball once",
			Kind:        "state",
			Explanation: "if_missing points at a file the archive actually creates, the real extraction marker.",
			Code: "/opt/prometheus:\n  archive.extracted:\n" +
				"    - source: https://github.com/prometheus/prometheus/releases/download/v2.53.0/prometheus-2.53.0.linux-amd64.tar.gz\n" +
				"    - if_missing: /opt/prometheus/prometheus-2.53.0.linux-amd64\n    - makedirs: true\n",
		},
		{
			Title:       "Extract a local zip archive",
			Kind:        "state",
			Explanation: "archive_format overrides auto-detection; if_missing avoids re-extracting.",
			Code: "/srv/webapp:\n  archive.extracted:\n    - source: /tmp/webapp-release.zip\n" +
				"    - archive_format: zip\n    - if_missing: /srv/webapp/index.html\n",
		},
		{
			Title:       "Re-extract on a version bump with source_hash",
			Kind:        "state",
			Explanation: "Changing source_hash (or source) triggers re-extraction on the next run; the value is an opaque declaration, not a verified checksum.",
			Code: "/opt/myapp:\n  archive.extracted:\n    - source: /srv/releases/myapp-2.0.0.tar.gz\n" +
				"    - source_hash: \"sha256=<hex-of-2.0.0>\"\n    - makedirs: true\n",
		},
		{
			Title:       "Extract an archive ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the target directory; source is a key=value.",
			Code:        "zester '*' archive.extracted /opt/tool source=/tmp/tool.tar.gz",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "warn",
			Title: "Set if_missing or source_hash",
			Body: "Without `if_missing` or `source_hash`, an existing target directory is considered " +
				"\"already extracted\" — even if it is empty. Point `if_missing` at a file the archive " +
				"actually creates, or declare `source_hash` to force re-extraction on a version bump.",
		},
		{
			Level: "info",
			Title: "source_hash is an opaque declaration, not a checksum",
			Body: "`source_hash` is compared as a plain string against a marker recorded after the LAST " +
				"successful extraction — it is never verified against the actual archive bytes. It exists " +
				"purely to detect \"the declared version changed since last time\", not to authenticate the " +
				"download.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "Idempotency is purely path/marker-based (`if_missing`, the `source_hash` marker, or " +
				"target-directory existence, per the note above) — Salt's `enforce_toplevel`, `options`, " +
				"`user`/`group`, `clean`, and `trim_output` parameters are not supported. Extraction requires " +
				"`tar`/`unzip` (and `curl` or `wget` for remote sources) to be present on the peel.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
})

// NewArchiveExtractedBuilder returns a state.Builder that creates
// ArchiveExtracted states using the ModuleContext's command and file
// providers. Decode policy (unknown-key handling, reserved keys) is threaded
// via opts; the peel supplies it through modules.RegisterAll.
func NewArchiveExtractedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.Command == nil {
			return nil, fmt.Errorf("archive.extracted: no command provider available")
		}
		if mctx.File == nil {
			return nil, fmt.Errorf("archive.extracted: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		a := &ArchiveExtracted{}
		if _, err := archiveExtractedSpec.Decode(id, config, a, opts); err != nil {
			return nil, fmt.Errorf("archive.extracted: %w", err)
		}
		// Builder-tail module logic (not schema): TrimSpace the declared hash
		// (a decoder never trims — same convention as ssh_auth.present's name).
		a.SourceHash = strings.TrimSpace(a.SourceHash)
		a.id = id
		a.cmd = mctx.Command
		a.file = mctx.File
		a.reqs = state.ParseRequisites(config)
		return a, nil
	}
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
