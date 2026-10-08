package archivemod

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/internal/famshared"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
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
//
// Integrity: a declared source_hash is ALSO VERIFIED against the archive
// bytes (local file or the downloaded temp file) before anything is
// extracted — `sha256=<hex>`, `sha256:<hex>`, `sha1=`, `sha512=`, `md5=`, or
// a bare hex digest whose algorithm is inferred from its length. A mismatch
// fails Apply with nothing extracted and no marker written. A remote source
// over plain `http://` or `ftp://` has no transport integrity, so it is
// REFUSED at build time unless it carries a verifiable source_hash or the
// operator sets `skip_verify: true`; `https://` without a hash stays allowed.
// `skip_verify` disables the byte verification (Salt semantics) and lets an
// otherwise-unparseable source_hash serve as an opaque version marker only.
//
// The guard is evaluated in BOTH Check and Apply (watch-forced applies
// bypass Check): an already-extracted archive is a clean Apply no-op.
//
// ArchiveExtracted is also its own schema proto: the tagged exported fields
// ARE the module's parameter declaration (one schema declaration per module) —
// all six are primitives (four strings and two bools), no semantic types are
// needed. `source` is `required`; `archive_format` carries an EAGER
// `default=auto`, reproducing the legacy construction-time default. Under the
// uniform decoder a numeric `name`/`source`/`archive_format`/`if_missing`/
// `source_hash` coerces to its string form and a composite is rejected
// (BD-6); `makedirs`/`skip_verify` honor an integer 1/0 (BD-7) and a
// truthy/falsy string (BD-2) where the legacy `.(bool)` assertion silently
// dropped them. The `source_hash` TrimSpace + digest parsing and the
// insecure-transport gate stay in the builder tail (a decoder never trims,
// and cross-field rules are module logic). The derived HashAlgo/HashHex
// facets are exported for test/contract projection but UNTAGGED, so the
// schema compiler skips them. The unexported runtime fields (id, reqs, cmd,
// file, revert memos) are untagged too.
type ArchiveExtracted struct {
	id   string
	reqs state.Requisites

	// Dir is the target directory the archive is extracted into; it defaults
	// to the state ID.
	Dir string `zester:"name,primary" usage:"target directory the archive is extracted into; defaults to the state ID"`

	// Source is a local file path or an http/https/ftp URL to the archive.
	Source string `zester:"source,required" usage:"local file path, or an http/https/ftp URL to the archive; required (a plain http:// or ftp:// source needs source_hash or skip_verify)"`

	// Format is the archive format: "tar", "zip", or "auto" (default).
	Format string `zester:"archive_format,default=auto" usage:"archive format: tar, zip, or auto (inferred from the source name); defaults to auto"`

	// IfMissing is a path whose existence means the archive is already
	// extracted. When set, it replaces the weak target-dir existence check.
	IfMissing string `zester:"if_missing" usage:"path whose existence means the archive is already extracted; when set, it is the sole idempotency check (besides source_hash)"`

	// SourceHash is the declared digest of the source archive —
	// `sha256=<hex>`, `sha256:<hex>`, `sha1=`, `sha512=`, `md5=`, or a bare
	// hex digest (algorithm inferred from its length). It is VERIFIED
	// against the archive bytes before extraction (unless skip_verify), and
	// recorded in a marker file after a successful extraction so that a
	// changed declaration triggers re-extraction. TrimSpace'd in the builder
	// tail (a decoder never trims).
	SourceHash string `zester:"source_hash" usage:"digest of the source archive (sha256=<hex>, sha256:<hex>, sha1=, sha512=, md5=, or a bare hex digest); verified against the archive bytes before extraction and recorded as the version marker; required for a plain http:// or ftp:// source unless skip_verify is set"`

	// MakeDirs creates the target directory (and parents) before extracting.
	MakeDirs bool `zester:"makedirs" usage:"create the target directory (and parents, mode 0755) before extracting; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// SkipVerify disables the source_hash byte verification and allows a
	// plain http:// or ftp:// source without a hash (Salt name/semantics).
	// With it set, source_hash is still recorded as an opaque version marker.
	SkipVerify bool `zester:"skip_verify" usage:"skip verifying source_hash against the archive bytes and allow a plain http:// or ftp:// source without one; source_hash is then only an opaque version marker; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// HashAlgo/HashHex are the parsed form of SourceHash ("sha256", lowercase
	// hex) when it is verifiable; both empty when no hash is declared or
	// skip_verify is set. Derived in the builder tail by resolveSourceHash.
	HashAlgo string
	HashHex  string

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
// drift-corrected against the live Check/Apply/Revert behavior — notably
// that `source_hash` is now VERIFIED against the archive bytes and that an
// unverified plain-http/ftp download is refused without `skip_verify`.
var archiveExtractedSpec = regdef.MustSpec("archive.extracted", modschema.KindState, ArchiveExtracted{}, modschema.Doc{
	Summary: "Extract a tar or zip archive (from a local path or URL) into a target directory.",
	Description: "`archive.extracted` extracts `source` (a local file path or an http/https/ftp URL) into " +
		"`name` (the target directory, defaulting to the state ID). Idempotency has three independent " +
		"strengths, weakest to strongest: without `if_missing` or `source_hash`, a PRE-EXISTING target " +
		"directory alone counts as \"already extracted\" (declare one of them for anything beyond " +
		"throwaway use); `if_missing` names a path whose existence is the sole extraction marker (Salt " +
		"parity), replacing the weak directory check; `source_hash` records the declared value in a " +
		"marker file after a successful extraction and re-extracts when the declaration no longer " +
		"matches.\n\n" +
		"`source_hash` is also an INTEGRITY check: before anything is extracted, the archive bytes (the " +
		"local file, or the downloaded temp file) are hashed and compared with the declaration — " +
		"`sha256=<hex>`, `sha256:<hex>`, `sha1=…`, `sha512=…`, `md5=…`, or a bare hex digest whose " +
		"algorithm is inferred from its length (32/40/64/128). A mismatch fails the state with nothing " +
		"extracted. A remote `source` over plain `http://` or `ftp://` carries no transport integrity, so " +
		"it is refused at build time unless it declares a verifiable `source_hash` or sets " +
		"`skip_verify: true`; `https://` without a hash is allowed (TLS provides transport integrity) and " +
		"with one it is verified like any other source.",
	Effects: modschema.Effects{
		Check: "With `if_missing` set: reports a change unless that path exists (the target-directory " +
			"fallback is not consulted). Without it: reports a change unless the target directory exists — " +
			"and a directory this run's `makedirs` created ahead of a FAILED extraction attempt does not " +
			"count (a retried Apply proceeds rather than latching on its own directory). When `source_hash` " +
			"is declared, ALSO requires a marker file recording that exact value to exist inside the target " +
			"— an unset or mismatched marker reports a change even if the directory/`if_missing` guard " +
			"passed, so bumping `source`/`source_hash` re-extracts. Check never downloads or hashes the " +
			"archive.",
		Apply: "Re-evaluates the same guard as Check (a watch-forced apply bypasses Check entirely, and an " +
			"already-extracted archive must be a clean no-op — never a re-download/re-extract over local " +
			"modifications). When extraction is needed: creates the target directory (mode 0755) when " +
			"`makedirs` is set and it does not already exist; downloads a remote `source` (http/https/ftp) " +
			"to a temp file via `curl -fSL`, falling back to `wget` when curl is unavailable; when " +
			"`source_hash` is declared (and `skip_verify` is not set), streams the archive through the " +
			"declared digest and FAILS — extracting nothing and writing no marker — on a mismatch; extracts " +
			"with `unzip -o <archive> -d <dir>` (zip) or `tar -xf <archive> -C <dir>` (tar), inferring the " +
			"format from the source name when `archive_format` is `auto` (`.zip` → zip; `.tgz`/`.tbz2`/" +
			"`.txz`/anything containing `.tar` → tar; anything else defaults to tar); a non-zero exit fails " +
			"the state. Only AFTER a successful extraction does it write the `source_hash` marker (a failed " +
			"download, verification, or extraction never latches the state as done). Reports the source, " +
			"target, and resolved format in its details.",
		Revert: "If Apply created the target directory (via `makedirs`), removes it recursively. Otherwise " +
			"an explicit no-op (`Changed: false`) — individual extracted files are not tracked, so there is " +
			"nothing else safe to remove.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Extract a release tarball once",
			Kind:        "state",
			Explanation: "if_missing points at a file the archive actually creates, the real extraction marker; https needs no hash.",
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
			Title:       "Verify the download and re-extract on a version bump",
			Kind:        "state",
			Explanation: "source_hash is checked against the archive bytes before extraction, and a changed declaration (with its new source) triggers re-extraction on the next run.",
			Code: "/opt/myapp:\n  archive.extracted:\n    - source: https://releases.example.com/myapp-2.0.0.tar.gz\n" +
				"    - source_hash: sha256=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08\n" +
				"    - makedirs: true\n",
		},
		{
			Title:       "Fetch over plain http",
			Kind:        "state",
			Explanation: "A plain http:// (or ftp://) source is refused without a source_hash; skip_verify: true accepts the unverified download explicitly.",
			Code: "/opt/legacy-tool:\n  archive.extracted:\n    - source: http://mirror.internal/legacy-tool.tar.gz\n" +
				"    - skip_verify: true\n    - if_missing: /opt/legacy-tool/bin/tool\n",
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
			Title: "source_hash is verified against the archive bytes",
			Body: "A declared `source_hash` is checked against the actual archive (local file or downloaded " +
				"temp file) BEFORE extraction; a mismatch fails the state with nothing extracted and no " +
				"marker written. Accepted forms: `sha256=<hex>`, `sha256:<hex>`, `sha1=`, `sha512=`, `md5=`, " +
				"or a bare hex digest (algorithm inferred from its length). A value that is none of these " +
				"fails the build — unless `skip_verify: true`, which disables the byte check and lets any " +
				"string act purely as the \"declared version changed\" marker (the pre-verification " +
				"behavior). The marker still records the declaration verbatim, so existing markers from " +
				"earlier runs remain valid.",
		},
		{
			Level: "warn",
			Title: "Plain http:// and ftp:// sources need a hash or skip_verify",
			Body: "An unencrypted download can be tampered with in transit, so a plain `http://` or `ftp://` " +
				"`source` without a verifiable `source_hash` is refused when the state is built. Declare " +
				"the hash, move to `https://`, or set `skip_verify: true` to accept the risk explicitly. " +
				"`https://` sources need no hash.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "Idempotency is purely path/marker-based (`if_missing`, the `source_hash` marker, or " +
				"target-directory existence, per the note above) — Salt's `enforce_toplevel`, `options`, " +
				"`user`/`group`, `clean`, and `trim_output` parameters are not supported, and `source_hash` " +
				"must be an inline digest (a URL to a checksum file is not fetched). Extraction requires " +
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
		// (a decoder never trims — same convention as ssh_auth.present's
		// name), parse it into its verifiable form, then apply the
		// cross-field insecure-transport gate.
		a.SourceHash = strings.TrimSpace(a.SourceHash)
		if err := a.resolveSourceHash(); err != nil {
			return nil, fmt.Errorf("archive.extracted: %w", err)
		}
		if scheme, insecure := insecureRemoteScheme(a.Source); insecure && a.HashAlgo == "" && !a.SkipVerify {
			return nil, fmt.Errorf("archive.extracted: %s: source %q is fetched over plain %s with no verifiable source_hash — declare source_hash (e.g. sha256=<hex>), use https://, or set skip_verify: true to accept an unverified download", id, a.Source, scheme)
		}
		a.id = id
		a.cmd = mctx.Command
		a.file = mctx.File
		a.reqs = state.ParseRequisites(config)
		return a, nil
	}
}

// resolveSourceHash derives the HashAlgo/HashHex facets from the declared
// SourceHash. No declaration, or skip_verify, leaves both empty (nothing is
// verified; a declaration still serves as the opaque version marker). An
// unparseable declaration without skip_verify is a typed value_invalid
// FieldError — a hash that cannot be checked must not pass as one. Mirrored
// by the contract decode wrapper so the projected facets match the builder.
func (a *ArchiveExtracted) resolveSourceHash() error {
	a.HashAlgo, a.HashHex = "", ""
	if a.SourceHash == "" || a.SkipVerify {
		return nil
	}
	algo, digest, err := parseSourceHash(a.SourceHash)
	if err != nil {
		return &modschema.FieldError{
			Module: "archive.extracted",
			Param:  "source_hash",
			Key:    "source_hash",
			Kind:   modschema.ErrValueInvalid,
			Value:  a.SourceHash,
			Err:    fmt.Errorf("%w (want sha256=<hex>, sha256:<hex>, sha1=, sha512=, md5=, or a bare hex digest; set skip_verify: true to use it as an opaque version marker only)", err),
		}
	}
	a.HashAlgo, a.HashHex = algo, digest
	return nil
}

// hashDigestLengths maps a supported algorithm to its hex digest length; it
// doubles as the length→algorithm table for bare digests.
var hashDigestLengths = map[string]int{
	"md5":    32,
	"sha1":   40,
	"sha256": 64,
	"sha512": 128,
}

// parseSourceHash accepts the Salt-style forms `<algo>=<hex>` and
// `<algo>:<hex>` plus a bare hex digest (algorithm inferred from its
// length: 32 md5, 40 sha1, 64 sha256, 128 sha512). Algorithm and hex are
// case-insensitive; the returned digest is lowercase.
func parseSourceHash(s string) (algo, digest string, err error) {
	digest = s
	if i := strings.IndexAny(s, "=:"); i >= 0 {
		algo = strings.ToLower(strings.TrimSpace(s[:i]))
		digest = strings.TrimSpace(s[i+1:])
		if algo == "" {
			return "", "", fmt.Errorf("source_hash %q: empty algorithm before the separator", s)
		}
	}
	digest = strings.ToLower(digest)
	if digest == "" {
		return "", "", fmt.Errorf("source_hash %q has no digest", s)
	}
	if _, decErr := hex.DecodeString(digest); decErr != nil || len(digest)%2 != 0 {
		return "", "", fmt.Errorf("source_hash %q: digest is not hexadecimal", s)
	}
	if algo == "" {
		for name, n := range hashDigestLengths {
			if len(digest) == n {
				return name, digest, nil
			}
		}
		return "", "", fmt.Errorf("source_hash %q: bare digest length %d matches no supported algorithm", s, len(digest))
	}
	want, ok := hashDigestLengths[algo]
	if !ok {
		return "", "", fmt.Errorf("source_hash %q: unsupported algorithm %q", s, algo)
	}
	if len(digest) != want {
		return "", "", fmt.Errorf("source_hash %q: %s digest must be %d hex characters, got %d", s, algo, want, len(digest))
	}
	return algo, digest, nil
}

// newDigest returns a fresh hash for a parsed algorithm name. md5 and sha1
// are accepted for Salt parity with existing states; they verify integrity
// against a declared value, not authenticity.
func newDigest(algo string) (hash.Hash, error) {
	switch algo {
	case "md5":
		return md5.New(), nil //nolint:gosec // Salt-compatible digest form
	case "sha1":
		return sha1.New(), nil //nolint:gosec // Salt-compatible digest form
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	}
	return nil, fmt.Errorf("unsupported hash algorithm %q", algo)
}

// verifySourceHash streams the archive at path through the declared digest
// (via the injected file provider, so no os.* call and no whole-file read)
// and fails on a mismatch. Called only when HashAlgo is set.
func (a *ArchiveExtracted) verifySourceHash(ctx context.Context, path string) error {
	h, err := newDigest(a.HashAlgo)
	if err != nil {
		return fmt.Errorf("archive.extracted: %w", err)
	}
	rc, err := a.file.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("archive.extracted: open %s for %s verification: %w", path, a.HashAlgo, err)
	}
	defer rc.Close()
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("archive.extracted: hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != a.HashHex {
		return fmt.Errorf("archive.extracted: source_hash mismatch for %s: declared %s=%s, actual %s=%s — refusing to extract", a.Source, a.HashAlgo, a.HashHex, a.HashAlgo, got)
	}
	return nil
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
		recorded, existed, err := famshared.ReadManagedFile(ctx, a.file, a.markerPath())
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

	// Integrity gate: verify the declared digest against the bytes we are
	// about to extract — BEFORE tar/unzip touches the target and before any
	// marker is written, so a tampered or corrupted archive changes nothing.
	if a.HashAlgo != "" {
		if err := a.verifySourceHash(ctx, archivePath); err != nil {
			return state.ApplyResult{}, err
		}
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
	// a failed download/verification/extract never latches the state as done.
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

// insecureRemoteScheme reports whether source is fetched over a transport
// with no integrity protection (plain http or ftp), returning the scheme
// for the diagnostic. https is not insecure; a local path is not remote.
func insecureRemoteScheme(source string) (string, bool) {
	s := strings.ToLower(source)
	switch {
	case strings.HasPrefix(s, "http://"):
		return "http", true
	case strings.HasPrefix(s, "ftp://"):
		return "ftp", true
	}
	return "", false
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
