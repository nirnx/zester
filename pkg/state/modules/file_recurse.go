package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
)

// errRecurseDestMissing is an internal walk sentinel: the clean-mode dest
// walk found no destination directory at all (nothing extra to detect).
var errRecurseDestMissing = errors.New("file.recurse: dest missing")

// FileRecurse implements the file.recurse state.
// It recursively copies a source directory to a destination directory.
//
// FileRecurse is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Two of them
// are named semantic types. FileMode is a paramtypes.FileMode declared
// `lazy,default=0644` — always enforced on copied files via FileMode.Resolve(0644).
// DirMode is a paramtypes.FileMode declared `lazy,default=0755` and is a
// DECLARED-ONLY facet: its Declared() bit gates the module — when dir_mode is
// undeclared, existing directory modes are neither compared (Check) nor rewritten
// (Apply), and the 0755 default is used ONLY as the creation perm for MkdirAll.
// Both honor an octal-int mode of ANY integer kind, so a reactor-dispatched
// `file_mode: 0640` / `dir_mode: 0750` (delivered as a msgpack-sized uint16) now
// applies rather than silently dropping to the default (BD-1). Source stays a
// plain string whose emptiness is a run-time "source is required" error (parity
// with the legacy Check/Apply guard, not a decode-time required). The unexported
// runtime fields are untagged, so the schema compiler skips them.
type FileRecurse struct {
	id   string
	reqs state.Requisites

	// Dest is the destination directory path; it defaults to the state ID.
	Dest string `zester:"name,primary,aliases=path" usage:"destination directory path (defaults to the state ID); the path alias is accepted"`

	// Source is the source directory path to copy from. An empty source is a
	// run-time error (kept as a plain string for legacy parity), not a
	// Source: file.* family component (requiredness member-supplied —
	// optional at decode; validated at run time).
	fileSourceParam

	// DirMode is the mode for created directories. It is a DECLARED-ONLY facet:
	// undeclared, existing directory modes are never compared or rewritten, and
	// the 0755 default is used only as the MkdirAll creation perm.
	DirMode paramtypes.FileMode `zester:"dir_mode,lazy,default=0755" usage:"directory permission mode for managed directories (octal); DECLARED-ONLY — undeclared, existing directory modes are left alone (used only as the creation mode for new directories, defaulting to 0755)"`

	// FileMode is the mode for copied files; it defaults to 0644 and is always
	// enforced on managed files.
	FileMode paramtypes.FileMode `zester:"file_mode,lazy,default=0644" usage:"permission mode for copied files in octal (\"0644\"); always enforced; defaults to 0644"`

	// User/Group: file.* family ownership component.
	fileOwnershipParam

	// Clean removes files in Dest that are not in Source.
	Clean bool `zester:"clean" usage:"remove files in the destination that are not present in the source (regular files only; directories are never removed); a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// MakeDirs: file.* family component (canonical parent-creation contract).
	fileMakeDirsParam

	// file is the injected file execution provider.
	file exec.FileExec

	// createdFiles tracks files created by Apply for Revert.
	createdFiles []string
}

// fileRecurseSpec is the compiled schema + documentation for file.recurse. It is
// compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is drift-corrected against the live
// Check/Apply/Revert behavior — notably the DECLARED-ONLY dir_mode facet (existing
// directory modes are left alone unless dir_mode is declared, in BOTH phases) and
// the clean semantics (regular files only; directories are never removed).
var fileRecurseSpec = mustSpec("file.recurse", modschema.KindState, FileRecurse{}, fileRecurseDoc,
	modschema.WithRequired("source", false))

var fileRecurseDoc = modschema.Doc{
	Summary: "Recursively copy a source directory tree to a destination directory.",
	Description: "`file.recurse` mirrors a `source` directory tree onto the destination, creating " +
		"directories and copying files by SHA-256 content. The destination path defaults to the state " +
		"ID. `file_mode` (default `0644`) is enforced on every copied file. `dir_mode` (default " +
		"`0755`) is DECLARED-ONLY: when it is omitted, existing directory modes are left alone (it is " +
		"used only as the creation mode for directories the copy makes); when it is set, managed " +
		"directory modes are compared and enforced. `user`/`group` set ownership on copied files only. " +
		"`clean` removes destination files absent from the source, and `makedirs` creates the " +
		"destination's parent chain. Both mode parameters honor an octal string or an octal integer " +
		"of any kind.",
	Effects: modschema.Effects{
		Check: "Requires a source (an empty source is an error). Walks the source tree: a missing " +
			"destination entry, a non-directory where a directory is expected, a content-hash " +
			"difference, a file-mode difference against `file_mode`, and — only when `user`/`group` are " +
			"declared — a file-ownership difference each count as drift. Directory modes are compared " +
			"ONLY when `dir_mode` is declared (an undeclared dir_mode never churns an operator-set mode, " +
			"including a pre-existing dest root). With `clean`, extra regular files in the destination " +
			"(never directories, matching Apply) also count as drift. Reports a change when any drift is " +
			"found.",
		Apply: "Requires a source. When `makedirs` is set, creates the destination's parent chain. " +
			"Walks the source: creates each directory with MkdirAll (chmod'ing it to `dir_mode` ONLY " +
			"when dir_mode is declared — an undeclared dir_mode never rewrites a pre-existing " +
			"directory's mode), and writes each file with `file_mode`, recording newly created files " +
			"for revert (a non-not-exist stat error fails the apply rather than mis-memoizing a " +
			"pre-existing file), chmod'ing it to `file_mode`, and chowning it when ownership is " +
			"declared. With `clean`, removes destination regular files not present in the source " +
			"(directories are never removed). Reports the copied and cleaned counts.",
		Revert: "Removes only the files this run's Apply created (best-effort; files that already " +
			"existed before Apply are left untouched). A fresh instance (a standalone revert, e.g. after " +
			"a peel restart) recorded nothing and is a clean no-op — it never removes a file it did not " +
			"create.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Deploy a config directory tree",
			Kind:        "state",
			Explanation: "Copies a whole tree with explicit modes and ownership; clean prunes stray destination files; makedirs creates the parent chain.",
			Code: "/etc/app:\n  file.recurse:\n    - source: /srv/config/app\n" +
				"    - file_mode: \"0640\"\n    - dir_mode: \"0750\"\n    - user: appuser\n    - group: appgroup\n" +
				"    - clean: true\n    - makedirs: true\n    - require:\n      - \"pkg.installed:myapp\"\n",
		},
		{
			Title:       "Mirror a tree, leaving directory modes alone",
			Kind:        "state",
			Explanation: "With no dir_mode declared, existing directory modes are never touched — only file content and file_mode converge.",
			Code:        "/opt/app/static:\n  file.recurse:\n    - source: /srv/static\n    - file_mode: \"0644\"\n",
		},
		{
			Title:       "Copy a tree ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the destination; source is a key=value arg.",
			Code:        "zester 'web*' file.recurse /etc/app source=/srv/config/app clean=true",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "dir_mode is a declared-only facet",
			Body: "When dir_mode is omitted, existing directory modes are neither compared (Check) nor " +
				"rewritten (Apply) — an operator-set mode on a pre-existing directory (for example a " +
				"private 0700 tree) is preserved, and the 0755 default is used only as the mode for " +
				"directories the copy creates. Declare dir_mode to actively manage directory modes.",
		},
		{
			Level: "info",
			Title: "clean removes regular files only",
			Body: "clean: true removes destination regular files that are absent from the source. Empty " +
				"directories left behind after a clean are not removed (matching the Apply behavior), so " +
				"a stray empty directory never churns Check.",
		},
		{
			Level: "info",
			Title: "Ownership requires privilege",
			Body: "`user` and `group` are resolved via `os/user.Lookup` and `os/user.LookupGroup`; the " +
				"peel must run with sufficient privileges to set ownership.",
		},
		{
			Title: "Revert loses its memo across instances",
			Body: "Revert removes only the files created by the most recent Apply on the same instance. " +
				"If the module is re-instantiated (for example after a peel restart), that memo is lost " +
				"and a standalone revert is a clean no-op.",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"file.managed", "file.directory", "file.copy"},
}

// NewFileRecurseBuilder returns a state.Builder that creates FileRecurse states
// using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewFileRecurseBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.recurse: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		r := &FileRecurse{}
		if _, err := fileRecurseSpec.Decode(id, config, r, opts); err != nil {
			return nil, fmt.Errorf("file.recurse: %w", err)
		}
		r.id = id
		r.file = mctx.File
		r.reqs = state.ParseRequisites(config)
		return r, nil
	}
}

func (r *FileRecurse) Name() string           { return "file.recurse:" + r.id }
func (r *FileRecurse) Reqs() state.Requisites { return r.reqs }

// desiredFileMode resolves the always-enforced file mode, supplying the documented
// 0644 default for the lazy, unmaterialized FileMode field. FileMode parsed the
// value at decode time, so resolution here cannot fail.
func (r *FileRecurse) desiredFileMode() fs.FileMode {
	return r.FileMode.Resolve(0644)
}

// desiredDirMode resolves the directory mode, supplying the 0755 default. The
// default applies only when Apply CREATES a directory — dir_mode is a declared-only
// facet, so existing directory modes are neither compared (Check) nor rewritten
// (Apply) unless dir_mode is declared (r.DirMode.Declared()).
func (r *FileRecurse) desiredDirMode() fs.FileMode {
	return r.DirMode.Resolve(0755)
}

func (r *FileRecurse) Check(ctx context.Context) (state.CheckResult, error) {
	// Canonical makedirs contract (§13): a missing parent with makedirs
	// unset fails Check too — never reported as an applicable change.
	if err := requireParentDirs(ctx, r.file, "file.recurse", r.Dest, r.MakeDirs); err != nil {
		return state.CheckResult{}, err
	}
	if r.Source == "" {
		return state.CheckResult{}, fmt.Errorf("file.recurse: source is required")
	}

	fileMode := r.desiredFileMode()

	// Resolve declared ownership up front; the facet fires only when
	// user/group is declared (Apply chowns files only, so Check compares
	// files only — dir ownership is never enforced).
	wantUID, wantGID := -1, -1
	if r.User != "" || r.Group != "" {
		var err error
		wantUID, wantGID, err = r.resolveOwnership()
		if err != nil {
			return state.CheckResult{}, err
		}
	}

	// Managed dest paths, for clean-mode extra detection.
	managed := make(map[string]struct{})

	var diffCount int
	err := r.file.Walk(ctx, r.Source, func(srcPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(r.Source, srcPath)
		if relErr != nil {
			return relErr
		}
		destPath := filepath.Join(r.Dest, rel)
		managed[destPath] = struct{}{}

		if d.IsDir() {
			info, statErr := r.file.Stat(ctx, destPath)
			if statErr != nil {
				if !errors.Is(statErr, fs.ErrNotExist) {
					return statErr
				}
				diffCount++
				return nil
			}
			if !info.IsDir() {
				diffCount++
				return nil
			}
			// dir_mode is a declared-only facet: with no dir_mode declared,
			// existing directory modes (including a pre-existing dest root)
			// are left alone, so Check must not compare them — Apply doesn't
			// chmod undeclared dirs either.
			if r.DirMode.Declared() && !r.DirMode.Equal(info.Mode()) {
				diffCount++
			}
			return nil
		}

		// Compare file.
		destData, readErr := r.file.ReadFile(ctx, destPath)
		if readErr != nil {
			if !errors.Is(readErr, fs.ErrNotExist) {
				return readErr
			}
			diffCount++
			return nil
		}

		srcData, srcReadErr := r.file.ReadFile(ctx, srcPath)
		if srcReadErr != nil {
			return srcReadErr
		}

		if hashBytes(srcData) != hashBytes(destData) {
			diffCount++
			return nil
		}

		info, statErr := r.file.Stat(ctx, destPath)
		if statErr != nil {
			if !errors.Is(statErr, fs.ErrNotExist) {
				return statErr
			}
			diffCount++
			return nil
		}
		if !modesEqual(info.Mode(), fileMode) {
			diffCount++
			return nil
		}

		if wantUID != -1 || wantGID != -1 {
			uid, gid, ownErr := r.file.Owner(ctx, destPath)
			if ownErr != nil {
				return ownErr
			}
			if (wantUID != -1 && uid != wantUID) || (wantGID != -1 && gid != wantGID) {
				diffCount++
			}
		}

		return nil
	})
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("file.recurse: walk source %s: %w", r.Source, err)
	}

	// Clean: extra (non-managed) files in dest count as drift — they are
	// what Apply's cleanDestination would remove. Extra directories are
	// skipped, matching cleanDestination, which never removes directories.
	if r.Clean {
		err = r.file.Walk(ctx, r.Dest, func(destPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// A missing dest is not "extras": the source walk already
				// counted every missing entry.
				if destPath == r.Dest && errors.Is(walkErr, fs.ErrNotExist) {
					return errRecurseDestMissing
				}
				return walkErr
			}
			if destPath == r.Dest || d.IsDir() {
				return nil
			}
			if _, ok := managed[destPath]; !ok {
				diffCount++
			}
			return nil
		})
		if err != nil && !errors.Is(err, errRecurseDestMissing) {
			return state.CheckResult{}, fmt.Errorf("file.recurse: walk dest %s: %w", r.Dest, err)
		}
	}

	if diffCount > 0 {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%d files differ between %s and %s", diffCount, r.Source, r.Dest),
		}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (r *FileRecurse) Apply(ctx context.Context) (state.ApplyResult, error) {
	if r.Source == "" {
		return state.ApplyResult{}, fmt.Errorf("file.recurse: source is required")
	}

	fileMode := r.desiredFileMode()
	dirMode := r.desiredDirMode()

	uid, gid, err := r.resolveOwnership()
	if err != nil {
		return state.ApplyResult{}, err
	}

	if err := ensureParentDirs(ctx, r.file, "file.recurse", r.Dest, r.MakeDirs); err != nil {
		return state.ApplyResult{}, err
	}

	// Build set of source-relative paths for clean mode.
	sourceFiles := make(map[string]struct{})

	var copiedFiles, copiedDirs int
	r.createdFiles = r.createdFiles[:0]

	err = r.file.Walk(ctx, r.Source, func(srcPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(r.Source, srcPath)
		if relErr != nil {
			return relErr
		}
		destPath := filepath.Join(r.Dest, rel)
		sourceFiles[destPath] = struct{}{}

		if d.IsDir() {
			if err := r.file.MkdirAll(ctx, destPath, dirMode); err != nil {
				return fmt.Errorf("mkdir %s: %w", destPath, err)
			}
			// Declared-only facet, mirroring Check: chmod managed dirs only
			// when dir_mode is declared — an undeclared dir_mode must never
			// rewrite an operator-set mode on a pre-existing directory.
			if r.DirMode.Declared() {
				if err := r.file.Chmod(ctx, destPath, dirMode); err != nil {
					return fmt.Errorf("chmod %s: %w", destPath, err)
				}
			}
			copiedDirs++
			return nil
		}

		data, readErr := r.file.ReadFile(ctx, srcPath)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", srcPath, readErr)
		}

		// Track whether this is a new file for revert. Only a genuine
		// not-exist marks it as created; any other stat error fails the
		// apply (a pre-existing file must never be memoized as created).
		if _, statErr := r.file.Stat(ctx, destPath); statErr != nil {
			if !errors.Is(statErr, fs.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", destPath, statErr)
			}
			r.createdFiles = append(r.createdFiles, destPath)
		}

		if err := r.file.WriteFile(ctx, destPath, data, fileMode); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}

		if err := r.file.Chmod(ctx, destPath, fileMode); err != nil {
			return fmt.Errorf("chmod %s: %w", destPath, err)
		}

		if uid != -1 || gid != -1 {
			if err := r.file.Chown(ctx, destPath, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", destPath, err)
			}
		}

		copiedFiles++
		return nil
	})
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.recurse: apply: %w", err)
	}

	// Clean: remove dest files not in source.
	var cleaned int
	if r.Clean {
		cleaned, err = r.cleanDestination(ctx, sourceFiles)
		if err != nil {
			return state.ApplyResult{}, err
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("copied %d files, %d dirs from %s to %s (cleaned %d)", copiedFiles, copiedDirs, r.Source, r.Dest, cleaned),
		Details: map[string]string{
			"source":  r.Source,
			"dest":    r.Dest,
			"files":   strconv.Itoa(copiedFiles),
			"dirs":    strconv.Itoa(copiedDirs),
			"cleaned": strconv.Itoa(cleaned),
		},
	}, nil
}

// cleanDestination removes files in dest not present in the source file set.
func (r *FileRecurse) cleanDestination(ctx context.Context, sourceFiles map[string]struct{}) (int, error) {
	var removed int
	err := r.file.Walk(ctx, r.Dest, func(destPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if destPath == r.Dest {
			return nil
		}
		if _, ok := sourceFiles[destPath]; !ok {
			if d.IsDir() {
				// Only remove if empty after children are removed.
				return nil
			}
			if err := r.file.Remove(ctx, destPath); err != nil {
				return fmt.Errorf("clean remove %s: %w", destPath, err)
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("file.recurse: clean walk: %w", err)
	}
	return removed, nil
}

func (r *FileRecurse) Revert(ctx context.Context) (state.ApplyResult, error) {
	var removed int
	for _, f := range r.createdFiles {
		if err := r.file.Remove(ctx, f); err != nil {
			// Best-effort removal.
			continue
		}
		removed++
	}

	if removed == 0 && len(r.createdFiles) == 0 {
		return state.ApplyResult{Changed: false}, nil
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed %d files created by file.recurse (revert)", removed),
	}, nil
}

func (r *FileRecurse) resolveOwnership() (uid, gid int, err error) {
	return resolveOwnerIDs("file.recurse", r.User, r.Group)
}
