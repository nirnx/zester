package filemod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/modschema/paramtypes"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// FileDirectory implements the file.directory state.
// It ensures a directory exists with the specified permissions and ownership.
//
// FileDirectory is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Mode is a
// paramtypes.FileMode declared `lazy,default=0755,aliases=dir_mode` — the mode is
// never materialized into the struct; the module applies the documented 0755
// default at use time via Mode.Resolve(0755), and dir_mode is accepted as a
// fallback source (mode wins when both are set, exactly like the legacy
// mode-then-dir_mode-then-0755 chain). FileMode honors an octal-int mode of ANY
// integer kind, so a reactor-dispatched `mode: 0700` (delivered as a msgpack-sized
// uint16) now applies 0700 rather than silently dropping to the 0755 default
// (BD-1, the same reproduced reactor bug as file.managed). The unexported runtime
// fields (id, reqs, injected provider, revert memo) are untagged, so the schema
// compiler skips them.
type FileDirectory struct {
	id   string
	reqs state.Requisites

	// Path is the absolute path to the directory; it defaults to the state ID.
	Path string `zester:"name,primary,aliases=path" usage:"absolute path to the target directory (defaults to the state ID); the path alias is accepted"`
	// Mode is the directory permission mode; it defaults to 0755 (applied lazily).
	// Mode: file.* family component (member-supplied default 0755, see mustSpec).
	fileModeParam
	// DirMode is the Salt-compat fallback SOURCE for mode (mode wins when both
	// are set; an explicit dir_mode fills in when mode is undeclared — builder
	// tail). Member-declared: dir_mode's meaning is member-specific within the
	// family (here a mode fallback; on file.recurse a creation mode), with one
	// shared contract signature.
	DirMode paramtypes.FileMode `zester:"dir_mode,lazy,default=0755" usage:"fallback source for mode (Salt compatibility): applied as the directory's permission mode when mode is not declared; defaults to 0755"`
	// User/Group: file.* family ownership component.
	fileOwnershipParam
	// MakeDirs: file.* family component. FIXED to the canonical contract in
	// this migration (previously accepted-but-inert — see the CHANGELOG BD).
	fileMakeDirsParam

	// file is the injected file execution provider.
	file exec.FileExec

	// wasCreated records that Apply created the directory (it did not pre-exist),
	// so a same-instance Revert removes it. With the memo unset, Revert is a
	// clean no-op.
	wasCreated bool
}

// fileDirectorySpec is the compiled schema + documentation for file.directory. It
// is compiled once at package init and executed by every decode path (the builder
// below, and Registry.Parse). The prose is drift-corrected against the live
// Check/Apply/Revert behavior — notably the canonical makedirs contract (parents fail when missing unless
// makedirs is set) and that the mode is set (Chmod) BEFORE ownership is converged
// (Chown), the order the legacy code has always used.
var fileDirectorySpec = regdef.MustSpec("file.directory", modschema.KindState, FileDirectory{}, fileDirectoryDoc,
	modschema.WithDefault("mode", "0755"))

var fileDirectoryDoc = modschema.Doc{
	Summary: "Ensure a directory exists with the desired permissions and ownership.",
	Description: "`file.directory` ensures a directory exists at its path with the desired permission " +
		"mode and ownership. The path defaults to the state ID. The `mode` parameter sets the " +
		"directory's permission bits and defaults to `0755`; `dir_mode` is a standalone fallback " +
		"source for the same mode, with `mode` winning when both are given. It honors an " +
		"octal string or an octal integer of any kind, so a reactor-dispatched `mode: 0700` applies " +
		"0700. `user`/`group` converge ownership only when declared. `makedirs` follows the file.* " +
		"family's canonical contract: it governs missing PARENTS of the managed directory (created at " +
		"0755 when true); when false — the default — a missing parent fails the apply instead of " +
		"being silently created, while a dry run reports it as a would-change (a deliberate " +
		"compatibility fix; see the changelog).",
	Effects: modschema.Effects{
		Check: "Reports a would-change first — naming the missing parent and the remedy — when a parent " +
			"directory is missing and `makedirs` is unset (the canonical file.* contract: an earlier state " +
			"in the run may create the parent, so dry runs of ordered trees stay valid; the strict failure " +
			"happens at apply). " +
			"Then compares, in order: path existence (a genuine not-exist means the directory must be " +
			"created, while any other stat error fails the check rather than reporting phantom drift); " +
			"that the path is a directory (an existing non-directory needs a change); the permission " +
			"mode (the managed facets — permission bits plus setuid/setgid/sticky) against the desired " +
			"mode, defaulting to 0755; and, only when `user`/`group` are declared, ownership drift. " +
			"Reports a change on the first mismatch.",
		Apply: "Ensures parents per the canonical makedirs contract (missing parents are created at " +
			"0755 only when `makedirs` is true; otherwise a missing parent fails before anything is " +
			"created). Probes existence for the revert memo — a non-not-exist stat error fails the apply " +
			"rather than poisoning the memo, since a pre-existing tree must never be recorded as " +
			"created (a same-instance Revert would RemoveAll it). Creates the directory itself, records " +
			"the created memo only when it did not pre-exist, sets the mode via Chmod, and — when " +
			"`user`/`group` is declared — converges ownership via Chown. Reports the applied mode. " +
			"Revert removes only the managed directory, never parents makedirs created.",
		Revert: "Removes a directory this run's Apply created (RemoveAll). A directory that pre-existed " +
			"(only its mode or ownership changed) is left untouched, and a fresh instance (a standalone " +
			"revert) recorded nothing and is a no-op — it never removes a directory it did not create.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Create a directory with ownership",
			Kind:        "state",
			Explanation: "The state ID is the target path; mode and ownership are set inline.",
			Code:        "/opt/myapp:\n  file.directory:\n    - mode: \"0755\"\n    - user: root\n    - group: root\n",
		},
		{
			Title:       "Private application data directory",
			Kind:        "state",
			Explanation: "A restrictive 0700 mode with a service account owner; the require pulls in the user first.",
			Code: "/var/lib/prometheus/data:\n  file.directory:\n    - mode: \"0700\"\n" +
				"    - user: prometheus\n    - group: prometheus\n    - require:\n      - \"user.present:prometheus\"\n",
		},
		{
			Title:       "Create a directory ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the path; mode and ownership are key=value args.",
			Code:        "zester 'web*' file.directory /var/log/myapp mode=0750 user=appuser group=appuser",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "warning",
			Title: "makedirs now follows the canonical contract (compatibility fix)",
			Body: "Before 0.7.0 makedirs was accepted but inert — file.directory always created the full " +
				"parent chain, silently masking typos in deep paths. It now follows the file.* family " +
				"contract: a missing parent without `makedirs: true` fails Apply (Check reports a " +
				"would-change naming the parent, keeping dry runs of ordered trees valid) — BD-9.",
		},
		{
			Title: "Mode defaults to 0755 and honors integer modes",
			Body: "An omitted mode applies 0755. The dir_mode alias supplies the same mode as a fallback " +
				"(mode wins if both are given). A mode given as an octal integer (for example `mode: 0700`) " +
				"is honored across every delivery path, including a reactor dispatch where msgpack encodes " +
				"it as a sized integer; setuid/setgid/sticky bits are preserved.",
		},
		{
			Level: "info",
			Title: "Ownership requires privilege",
			Body: "`user` and `group` are resolved via `os/user.Lookup` and `os/user.LookupGroup`; the " +
				"peel must run with sufficient privileges to set ownership.",
		},
	},
	Divergences: []string{"BD-1", "BD-2", "BD-6", "BD-7", "BD-9"},
	SeeAlso:     []string{"file.managed", "file.recurse", "file.absent"},
}

// NewFileDirectoryBuilder returns a state.Builder that creates FileDirectory
// states using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewFileDirectoryBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("file.directory: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it — assigning them before would be
		// overwritten by the committed scratch value.
		d := &FileDirectory{}
		if _, err := fileDirectorySpec.Decode(id, config, d, opts); err != nil {
			return nil, fmt.Errorf("file.directory: %w", err)
		}
		d.id = id
		d.file = mctx.File
		d.reqs = state.ParseRequisites(config)
		// Salt-compat fallback (builder tail, not schema): an explicit
		// dir_mode fills in when mode is undeclared; mode wins when both are
		// set — byte-identical to the pre-A1 alias behavior.
		if !d.Mode.Declared() && d.DirMode.Declared() {
			d.Mode = d.DirMode
		}
		return d, nil
	}
}

func (d *FileDirectory) Name() string           { return "file.directory:" + d.id }
func (d *FileDirectory) Reqs() state.Requisites { return d.reqs }

// desiredMode resolves the managed permission mode, supplying the documented 0755
// default for the lazy, unmaterialized Mode field. FileMode parsed the value at
// decode time (through the mode name or its dir_mode alias), so resolution here
// cannot fail.
func (d *FileDirectory) desiredMode() fs.FileMode {
	return d.Mode.Resolve(0755)
}

func (d *FileDirectory) Check(ctx context.Context) (state.CheckResult, error) {
	// Canonical makedirs contract (§13, Check re-ruled 2026-07-14): a
	// missing parent with makedirs unset is a WOULD-CHANGE — an earlier
	// state in the run may create it, so dry runs of ordered trees stay
	// valid; Apply stays strict.
	if detail, err := checkParentDirs(ctx, d.file, "file.directory", d.Path, d.MakeDirs); err != nil {
		return state.CheckResult{}, err
	} else if detail != "" {
		return state.CheckResult{NeedsChange: true, Diff: "file.directory: " + detail}, nil
	}
	info, err := d.file.Stat(ctx, d.Path)
	if err != nil {
		// Only a genuine not-exist means "directory absent"; any other stat
		// error (permissions, I/O) fails the check rather than reporting
		// phantom drift and letting Apply proceed blind.
		if !errors.Is(err, fs.ErrNotExist) {
			return state.CheckResult{}, fmt.Errorf("file.directory: stat %s: %w", d.Path, err)
		}
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("directory %s does not exist", d.Path),
		}, nil
	}

	if !info.IsDir() {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("%s exists but is not a directory", d.Path),
		}, nil
	}

	mode := d.desiredMode()
	if !modesEqual(info.Mode(), mode) {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("mode %04o != %04o for %s", info.Mode().Perm(), mode, d.Path),
		}, nil
	}

	diff, drift, err := checkOwnershipDrift(ctx, d.file, "file.directory", d.Path, d.User, d.Group)
	if err != nil {
		return state.CheckResult{}, err
	}
	if drift {
		return state.CheckResult{NeedsChange: true, Diff: diff}, nil
	}

	return state.CheckResult{NeedsChange: false}, nil
}

func (d *FileDirectory) Apply(ctx context.Context) (state.ApplyResult, error) {
	mode := d.desiredMode()

	// Canonical makedirs contract (§13): parents are created only when
	// makedirs is true (mode 0755); a missing parent without it fails before
	// anything is created. The managed directory ITSELF is created below
	// regardless — creating the target is this module's job, not makedirs'.
	if err := ensureParentDirs(ctx, d.file, "file.directory", d.Path, d.MakeDirs); err != nil {
		return state.ApplyResult{}, err
	}

	// Probe existence for the revert memo. Only a genuine not-exist can mark
	// the directory as created; any other stat error fails the apply — a
	// pre-existing tree must never be memoized as created (a same-instance
	// Revert would RemoveAll it).
	preExisted := false
	if _, statErr := d.file.Stat(ctx, d.Path); statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("file.directory: stat %s: %w", d.Path, statErr)
		}
	} else {
		preExisted = true
	}

	if err := d.file.MkdirAll(ctx, d.Path, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.directory: mkdir %s: %w", d.Path, err)
	}

	// Record the revert memo only after the directory was actually created.
	if !preExisted {
		d.wasCreated = true
	}

	if err := d.file.Chmod(ctx, d.Path, mode); err != nil {
		return state.ApplyResult{}, fmt.Errorf("file.directory: chmod %s: %w", d.Path, err)
	}

	if err := d.setOwnership(ctx); err != nil {
		return state.ApplyResult{}, err
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("ensured directory %s (mode %04o)", d.Path, mode),
		Details: map[string]string{
			"path": d.Path,
			"mode": fmt.Sprintf("%04o", mode),
		},
	}, nil
}

func (d *FileDirectory) Revert(ctx context.Context) (state.ApplyResult, error) {
	if d.wasCreated {
		if err := d.file.RemoveAll(ctx, d.Path); err != nil {
			return state.ApplyResult{}, fmt.Errorf("file.directory: revert remove %s: %w", d.Path, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed directory %s (revert create)", d.Path),
		}, nil
	}

	return state.ApplyResult{Changed: false}, nil
}

func (d *FileDirectory) setOwnership(ctx context.Context) error {
	if d.User == "" && d.Group == "" {
		return nil
	}

	uid, gid, err := resolveOwnerIDs("file.directory", d.User, d.Group)
	if err != nil {
		return err
	}

	if err := d.file.Chown(ctx, d.Path, uid, gid); err != nil {
		return fmt.Errorf("file.directory: chown %s: %w", d.Path, err)
	}
	return nil
}
