package pkgrepomod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/modules/internal/famshared"
	"github.com/nirnx/zester/pkg/state/modules/regdef"
)

// PkgrepoManaged implements the pkgrepo.managed state.
// It manages an apt or yum/dnf repository definition file. On Debian it writes
// /etc/apt/sources.list.d/<name>.list (optionally fetching a signing key and
// refreshing the cache), and on RedHat it writes /etc/yum.repos.d/<name>.repo.
//
// PkgrepoManaged is also its own schema proto: the tagged exported fields ARE
// the module's parameter declaration (one schema declaration per module). This
// is a parameter-decode migration ONLY: Check/Apply/Revert (including the
// signing-key convergence marker and the DEFERRED in-place key-rotation
// detection — the audit's open item) are unchanged. `humanname` is a DERIVED
// default: it is declared `lazy` (documented, never materialized by the
// decoder) and the builder tail assigns it from `name`, reproducing the legacy
// `if HumanName == "" { HumanName = RepoName }`. `enabled`/`gpgcheck`/`refresh`
// carry an eager `default=true`, so under the uniform decoder an integer
// (`1`/`0`) or truthy/falsy string now coerces to the flag (BD-2/BD-7) where
// the legacy `config[...].(bool)` assertion silently dropped it. Nothing here is
// sensitive (a signing-KEY URL points at a PUBLIC key). The unexported runtime
// fields (id, reqs, family, mgr, providers) are untagged, so the schema
// compiler skips them.
type PkgrepoManaged struct {
	id   string
	reqs state.Requisites

	// RepoName is the repository identifier (used for the default filename and
	// the yum section header). Defaults to the state ID.
	RepoName string `zester:"name,primary" usage:"repository identifier, used for the default filename and the yum section header; defaults to the state ID"`

	// HumanName is a descriptive display name (yum "name=" field / apt comment).
	// It is a lazy, derived default: absent, the builder tail sets it to RepoName.
	HumanName string `zester:"humanname,lazy" usage:"descriptive display name (yum name= field / apt comment line); defaults to the repository name"`

	// BaseURL is the repository line. For apt this is the full "deb ..." line;
	// for yum it is the baseurl= value.
	BaseURL string `zester:"baseurl" usage:"the repository line — on apt the full \"deb ...\" source line, on yum the baseurl= value"`

	// PPA is an apt PPA reference (e.g. "ppa:user/name"). When set (and BaseURL
	// is empty) the repo is added via add-apt-repository.
	PPA string `zester:"ppa" usage:"apt PPA reference (e.g. \"ppa:user/name\"); when set and baseurl is empty the repo is added via add-apt-repository, and it forces the Debian code path when the OS family cannot be detected"`

	// File overrides the default repository definition path.
	File string `zester:"file" usage:"override the default repository definition file path"`

	// KeyURL is an optional signing-key URL. On apt the key is fetched to a
	// persistent keyring file (see aptKeyringPath) and imported; Check
	// verifies that file exists — a repo whose .list matches but whose key
	// was never imported (or was deleted) is NOT converged. On yum it is
	// written as the gpgkey= field, which the compared file content already
	// covers.
	KeyURL string `zester:"key_url" usage:"signing-key URL; on apt it is fetched to /etc/apt/keyrings/zester-<name>.gpg and imported via apt-key add, on yum it becomes the gpgkey= field"`

	// Enabled controls the yum "enabled=" field. Defaults to true.
	Enabled bool `zester:"enabled,default=true" usage:"the yum enabled= field (1/0), ignored on apt; defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// GPGCheck controls the yum "gpgcheck=" field. Defaults to true.
	GPGCheck bool `zester:"gpgcheck,default=true" usage:"the yum gpgcheck= field (1/0), ignored on apt; defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// Refresh runs the package-cache refresh after writing the repo. Defaults to true.
	Refresh bool `zester:"refresh,default=true" usage:"refresh the package cache after writing the repo (apt-get update / <mgr> makecache); defaults to true; a boolean that also accepts the integers 1 (true) and 0 (false)"`

	// family is the detected OS family ("debian", "redhat", "").
	family string

	// mgr is the package manager CLI ("apt-get", "dnf", "yum").
	mgr string

	file exec.FileExec
	cmd  exec.CommandExec
}

// pkgrepoManagedSpec is the compiled schema + documentation for pkgrepo.managed.
// Its Doc is drift-corrected against the live Check/Apply/Revert behavior and
// preserves the hand page's keyring semantics, apt-key deprecation, and
// unsupported-Salt-parameter notes.
var pkgrepoManagedSpec = regdef.MustSpec("pkgrepo.managed", modschema.KindState, PkgrepoManaged{}, modschema.Doc{
	Summary: "Manage an apt or yum/dnf package repository definition.",
	Description: "`pkgrepo.managed` manages an apt or yum/dnf package repository definition file. On the " +
		"Debian family it writes `/etc/apt/sources.list.d/<name>.list` (optionally fetching a signing key " +
		"and refreshing the cache); on the RedHat family it writes `/etc/yum.repos.d/<name>.repo`. A PPA " +
		"(`ppa:user/name`) is handled via `add-apt-repository`. The repository identifier defaults to the " +
		"state ID and is used for both the default filename and the yum section header; `humanname` " +
		"defaults to it. `enabled`, `gpgcheck`, and `refresh` default to `true`. Setting `ppa` also forces " +
		"the Debian code path when the OS family cannot otherwise be detected.\n\n" +
		"The rendered repository file body is:\n\n" +
		"Debian (`.list`):\n\n" +
		"```\n# Managed by Zester: <humanname>\n<baseurl>\n```\n\n" +
		"RedHat (`.repo`):\n\n" +
		"```ini\n[<name>]\nname=<humanname>\nbaseurl=<baseurl>\nenabled=1\ngpgcheck=1\n" +
		"gpgkey=<key_url>          # only when key_url is set\n```",
	Effects: modschema.Effects{
		Check: "Errors when the OS family cannot be determined. A **PPA** repo always reports a change — a " +
			"PPA cannot be verified from a single file, so the idempotent `add-apt-repository` is re-run on " +
			"every Apply. Otherwise it renders the desired file content and compares it byte-for-byte with " +
			"the current repo file; a missing or differing file needs a change, and a read failure other " +
			"than \"file does not exist\" fails the check (it is never treated as a missing file, so an " +
			"unreadable file is never blindly overwritten). On Debian with `key_url` declared it ALSO " +
			"verifies the imported keyring file `/etc/apt/keyrings/zester-<name>.gpg` exists — the key URL " +
			"appears nowhere in the compared `.list` bytes, so a matching repo file with a never-imported " +
			"(or deleted) signing key still reports a change. This is presence-only: a key rotated in place " +
			"at the same URL is not detected. RedHat needs no keyring probe — `gpgkey=` is part of the " +
			"compared content and dnf fetches it at transaction time.",
		Apply: "On **Debian**: imports the signing key when `key_url` is set (`curl` writing DIRECTLY to the " +
			"persistent keyring file `/etc/apt/keyrings/zester-<name>.gpg`, then `apt-key add`), then either " +
			"runs `add-apt-repository -y <ppa>` or writes the `.list` file (creating the parent directory), " +
			"then runs `apt-get update` when `refresh` is true. On **RedHat**: writes the `.repo` file, then " +
			"runs `<mgr> makecache` when `refresh` is true. Reports the repo name, file, and family in its " +
			"details.",
		Revert: "A PPA repo is removed via `add-apt-repository -r -y <ppa>`. A file-based repo has its " +
			"definition file deleted; on Debian with `key_url` declared the keyring file " +
			"`/etc/apt/keyrings/zester-<name>.gpg` is removed too (an already-absent keyring is fine). " +
			"Revert removes the whole repo file rather than restoring any prior content.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Apt repository",
			Kind:        "state",
			Explanation: "On apt, baseurl holds the full deb line; key_url is fetched and imported.",
			Code: "docker:\n  pkgrepo.managed:\n    - humanname: Docker CE\n" +
				"    - baseurl: \"deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable\"\n" +
				"    - key_url: https://download.docker.com/linux/ubuntu/gpg\n",
		},
		{
			Title:       "Yum repository",
			Kind:        "state",
			Explanation: "On yum, baseurl is the baseurl= value; enabled/gpgcheck default to true.",
			Code: "epel:\n  pkgrepo.managed:\n    - humanname: Extra Packages for Enterprise Linux\n" +
				"    - baseurl: \"https://download.fedoraproject.org/pub/epel/9/Everything/x86_64/\"\n" +
				"    - gpgcheck: false\n",
		},
		{
			Title:       "PPA",
			Kind:        "state",
			Explanation: "A PPA is added via add-apt-repository; setting ppa forces the Debian path.",
			Code:        "deadsnakes:\n  pkgrepo.managed:\n    - ppa: \"ppa:deadsnakes/ppa\"\n",
		},
		{
			Title:       "Add an apt repository ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the repository name; baseurl is a key=value.",
			Code:        "zester 'web*' pkgrepo.managed docker baseurl='deb https://download.docker.com/linux/ubuntu jammy stable'",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "warning",
			Title: "On apt, baseurl holds the full deb line",
			Body: "On apt, `baseurl` holds the ENTIRE `deb ...` source line. In Salt the deb line goes in " +
				"`name:`; Zester keeps `name` as the repo identifier and reuses `baseurl` for both families.",
		},
		{
			Level: "info",
			Title: "Signing-key convergence is presence-only",
			Body: "On Debian a declared `key_url` is fetched to `/etc/apt/keyrings/zester-<name>.gpg` and " +
				"imported; Check verifies that keyring file EXISTS (the key URL is invisible in the compared " +
				"`.list` bytes, so a matching repo file with a never-imported key still needs a change). " +
				"Detection is presence-only: a key rotated in place at the SAME URL is not re-detected — " +
				"delete the keyring file to force a re-import. RedHat needs no keyring probe: `gpgkey=` is " +
				"part of the compared `.repo` content and dnf fetches it at transaction time.",
		},
		{
			Level: "warning",
			Title: "Apt keys use the deprecated apt-key add",
			Body: "Apt keys are imported with the deprecated `apt-key add`; Salt supports `signed-by` / " +
				"keyring-file placement. The key bytes are written direct-to-disk with `curl -o` (never " +
				"routed through captured stdout, which would corrupt a binary non-armored `.gpg` key).",
		},
		{
			Level: "info",
			Title: "Unsupported Salt parameters",
			Body: "Salt's `disabled`, `mirrorlist`, `gpgautoimport`, `comps`, and `architectures` parameters " +
				"are not supported. Reverting removes the whole repo file rather than restoring prior content.",
		},
		{
			Level: "info",
			Title: "enabled/gpgcheck/refresh default to true; integer values honored",
			Body: "`enabled`, `gpgcheck`, and `refresh` default to `true`; set one `false` to override. Under " +
				"the uniform decoder an integer (`1`/`0`) or a truthy/falsy string is honored for each of " +
				"them, where the legacy `.(bool)` assertion silently dropped a non-bool value to the default.",
		},
	},
	Divergences: []string{"BD-2", "BD-6", "BD-7"},
	SeeAlso:     []string{"pkg.installed"},
})

// NewPkgrepoManagedBuilder returns a state.Builder that creates PkgrepoManaged
// states using the given ModuleContext's file and command providers. Decode
// policy (unknown-key handling, reserved keys) is threaded via opts; the peel
// supplies it through modules.RegisterAll.
func NewPkgrepoManagedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("pkgrepo.managed: no file provider available")
		}
		if mctx.Command == nil {
			return nil, fmt.Errorf("pkgrepo.managed: no command provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected providers, id,
		// requisites, the derived humanname, and the OS-family detection MUST be
		// assigned AFTER it — assigning them before would be overwritten by the
		// committed scratch value.
		r := &PkgrepoManaged{}
		if _, err := pkgrepoManagedSpec.Decode(id, config, r, opts); err != nil {
			return nil, fmt.Errorf("pkgrepo.managed: %w", err)
		}
		r.id = id
		r.file = mctx.File
		r.cmd = mctx.Command
		r.deriveHumanName()

		providerName := ""
		if mctx.Package != nil {
			providerName = mctx.Package.Name()
		}
		r.family, r.mgr = famshared.DetectPkgSystem(mctx.Facts, providerName)

		// A PPA is unambiguously an apt/Debian construct.
		if r.family == "" && r.PPA != "" {
			r.family, r.mgr = "debian", "apt-get"
		}

		r.reqs = state.ParseRequisites(config)
		return r, nil
	}
}

// deriveHumanName applies the documented `humanname` default: absent, it
// defaults to the repository name, reproducing the legacy construction-time
// `if HumanName == "" { HumanName = RepoName }`. `humanname` is declared `lazy`
// (the decoder never materializes the default), so this is applied by the
// builder tail and mirrored by the contract decode wrapper (like user.present's
// resolveGroupFacets) so the projected facet matches what the builder computes.
func (r *PkgrepoManaged) deriveHumanName() {
	if r.HumanName == "" {
		r.HumanName = r.RepoName
	}
}

func (r *PkgrepoManaged) Name() string           { return "pkgrepo.managed:" + r.id }
func (r *PkgrepoManaged) Reqs() state.Requisites { return r.reqs }

// isPPA reports whether this repo should be managed via add-apt-repository.
func (r *PkgrepoManaged) isPPA() bool {
	return r.family == "debian" && r.PPA != "" && r.BaseURL == ""
}

// aptKeyringPath returns the persistent path the fetched apt signing key is
// stored at. It is both the imported artifact and Check's convergence marker
// for the key dimension (the KeyURL appears nowhere in the .list content, so
// the content compare alone is blind to a never-imported key).
func (r *PkgrepoManaged) aptKeyringPath() string {
	return "/etc/apt/keyrings/zester-" + r.RepoName + ".gpg"
}

// filePath returns the repository definition file path.
func (r *PkgrepoManaged) filePath() string {
	if r.File != "" {
		return r.File
	}
	switch r.family {
	case "debian":
		return "/etc/apt/sources.list.d/" + r.RepoName + ".list"
	case "redhat":
		return "/etc/yum.repos.d/" + r.RepoName + ".repo"
	}
	return ""
}

// desiredContent renders the repository definition file body.
func (r *PkgrepoManaged) desiredContent() (string, error) {
	switch r.family {
	case "debian":
		return fmt.Sprintf("# Managed by Zester: %s\n%s\n", r.HumanName, strings.TrimSpace(r.BaseURL)), nil
	case "redhat":
		var b strings.Builder
		fmt.Fprintf(&b, "[%s]\n", r.RepoName)
		fmt.Fprintf(&b, "name=%s\n", r.HumanName)
		fmt.Fprintf(&b, "baseurl=%s\n", r.BaseURL)
		fmt.Fprintf(&b, "enabled=%s\n", repoBool01(r.Enabled))
		fmt.Fprintf(&b, "gpgcheck=%s\n", repoBool01(r.GPGCheck))
		if r.KeyURL != "" {
			fmt.Fprintf(&b, "gpgkey=%s\n", r.KeyURL)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("unsupported os family %q", r.family)
}

func (r *PkgrepoManaged) Check(ctx context.Context) (state.CheckResult, error) {
	if r.family == "" {
		return state.CheckResult{}, fmt.Errorf("pkgrepo.managed: cannot determine os family for %s", r.RepoName)
	}

	// PPA-managed repos cannot be verified from a single file; always ensure.
	if r.isPPA() {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("ppa %s will be ensured", r.PPA),
		}, nil
	}

	desired, err := r.desiredContent()
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("pkgrepo.managed: %w", err)
	}

	p := r.filePath()
	current, err := r.file.ReadFile(ctx, p)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("repo file %s does not exist", p),
		}, nil
	default:
		// Any non-not-exist read failure (permissions, I/O) is an answer we
		// don't have, not "missing" — failing the phase beats an Apply that
		// overwrites a file we couldn't read.
		return state.CheckResult{}, fmt.Errorf("pkgrepo.managed: read %s: %w", p, err)
	}

	if string(current) != desired {
		return state.CheckResult{
			NeedsChange: true,
			Diff:        fmt.Sprintf("repo file %s content differs", p),
		}, nil
	}

	// On Debian the signing key is part of the desired state but invisible
	// in the compared .list bytes — verify the imported keyring artifact
	// exists. Only when key_url is declared: an undeclared key must never
	// churn the state.
	if r.family == "debian" && r.KeyURL != "" {
		kp := r.aptKeyringPath()
		_, err := r.file.ReadFile(ctx, kp)
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			return state.CheckResult{
				NeedsChange: true,
				Diff:        fmt.Sprintf("signing key for repo %s is not imported (%s missing)", r.RepoName, kp),
			}, nil
		default:
			return state.CheckResult{}, fmt.Errorf("pkgrepo.managed: read keyring %s: %w", kp, err)
		}
	}

	return state.CheckResult{
		NeedsChange: false,
		Diff:        fmt.Sprintf("repo %s is up to date", r.RepoName),
	}, nil
}

func (r *PkgrepoManaged) Apply(ctx context.Context) (state.ApplyResult, error) {
	if r.family == "" {
		return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: cannot determine os family for %s", r.RepoName)
	}

	switch r.family {
	case "debian":
		if r.KeyURL != "" {
			if err := r.importAptKey(ctx); err != nil {
				return state.ApplyResult{}, err
			}
		}
		if r.isPPA() {
			if _, err := r.cmd.Run(ctx, exec.CommandOpts{
				Command: "add-apt-repository",
				Args:    []string{"-y", r.PPA},
			}); err != nil {
				return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: add ppa %s: %w", r.PPA, err)
			}
		} else {
			if err := r.writeRepoFile(ctx); err != nil {
				return state.ApplyResult{}, err
			}
		}
		if r.Refresh {
			if _, err := r.cmd.Run(ctx, exec.CommandOpts{
				Command: "apt-get",
				Args:    []string{"update"},
			}); err != nil {
				return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: apt-get update: %w", err)
			}
		}

	case "redhat":
		if err := r.writeRepoFile(ctx); err != nil {
			return state.ApplyResult{}, err
		}
		if r.Refresh {
			if _, err := r.cmd.Run(ctx, exec.CommandOpts{
				Command: r.mgr,
				Args:    []string{"makecache"},
			}); err != nil {
				return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: %s makecache: %w", r.mgr, err)
			}
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("managed repo %s", r.RepoName),
		Details: map[string]string{
			"name":   r.RepoName,
			"file":   r.filePath(),
			"family": r.family,
		},
	}, nil
}

func (r *PkgrepoManaged) Revert(ctx context.Context) (state.ApplyResult, error) {
	if r.isPPA() {
		if _, err := r.cmd.Run(ctx, exec.CommandOpts{
			Command: "add-apt-repository",
			Args:    []string{"-r", "-y", r.PPA},
		}); err != nil {
			return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: remove ppa %s: %w", r.PPA, err)
		}
		return state.ApplyResult{
			Changed: true,
			Diff:    fmt.Sprintf("removed ppa %s", r.PPA),
		}, nil
	}

	p := r.filePath()
	if p == "" {
		return state.ApplyResult{
			Changed: false,
			Diff:    "pkgrepo.managed: nothing to revert",
		}, nil
	}

	if err := r.file.Remove(ctx, p); err != nil {
		return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: remove %s: %w", p, err)
	}

	// Clean up the keyring artifact Apply created; an already-absent file is
	// fine (Apply may never have imported a key in this lifetime).
	if r.family == "debian" && r.KeyURL != "" {
		kp := r.aptKeyringPath()
		if err := r.file.Remove(ctx, kp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return state.ApplyResult{}, fmt.Errorf("pkgrepo.managed: remove keyring %s: %w", kp, err)
		}
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed repo file %s", p),
	}, nil
}

// writeRepoFile renders and writes the repository definition file.
func (r *PkgrepoManaged) writeRepoFile(ctx context.Context) error {
	content, err := r.desiredContent()
	if err != nil {
		return fmt.Errorf("pkgrepo.managed: %w", err)
	}

	p := r.filePath()
	if dir := path.Dir(p); dir != "" && dir != "." {
		if err := r.file.MkdirAll(ctx, dir, 0755); err != nil {
			return fmt.Errorf("pkgrepo.managed: mkdir %s: %w", dir, err)
		}
	}

	if err := r.file.WriteFile(ctx, p, []byte(content), 0644); err != nil {
		return fmt.Errorf("pkgrepo.managed: write %s: %w", p, err)
	}
	return nil
}

// importAptKey fetches an apt signing key with curl writing DIRECTLY to the
// persistent keyring path (Check's convergence marker — a /tmp download would
// vanish on reboot) and imports it into the apt trust store. `curl -o` is
// deliberate: routing the key bytes through captured stdout corrupts binary
// (non-armored) .gpg keys — CommandResult.Stdout is TrimSpace'd, so a key
// whose final byte happens to be whitespace-class loses it and gpg rejects
// the mangled key.
func (r *PkgrepoManaged) importAptKey(ctx context.Context) error {
	keyfile := r.aptKeyringPath()
	if dir := path.Dir(keyfile); dir != "" && dir != "." {
		if err := r.file.MkdirAll(ctx, dir, 0755); err != nil {
			return fmt.Errorf("pkgrepo.managed: mkdir %s: %w", dir, err)
		}
	}

	if _, err := r.cmd.Run(ctx, exec.CommandOpts{
		Command: "curl",
		Args:    []string{"-fsSL", "-o", keyfile, r.KeyURL},
	}); err != nil {
		return fmt.Errorf("pkgrepo.managed: fetch key %s: %w", r.KeyURL, err)
	}

	if _, err := r.cmd.Run(ctx, exec.CommandOpts{
		Command: "apt-key",
		Args:    []string{"add", keyfile},
	}); err != nil {
		return fmt.Errorf("pkgrepo.managed: import key: %w", err)
	}
	return nil
}

// repoBool01 renders a bool as "1" or "0" for repo definition files.
func repoBool01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
