package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// PkgrepoManaged implements the pkgrepo.managed state.
// It manages an apt or yum/dnf repository definition file. On Debian it writes
// /etc/apt/sources.list.d/<name>.list (optionally fetching a signing key and
// refreshing the cache), and on RedHat it writes /etc/yum.repos.d/<name>.repo.
type PkgrepoManaged struct {
	id   string
	reqs state.Requisites

	// RepoName is the repository identifier (used for the default filename and
	// the yum section header). Defaults to the state ID.
	RepoName string

	// HumanName is a descriptive display name (yum "name=" field / apt comment).
	HumanName string

	// BaseURL is the repository line. For apt this is the full "deb ..." line;
	// for yum it is the baseurl= value.
	BaseURL string

	// PPA is an apt PPA reference (e.g. "ppa:user/name"). When set (and BaseURL
	// is empty) the repo is added via add-apt-repository.
	PPA string

	// File overrides the default repository definition path.
	File string

	// KeyURL is an optional signing-key URL. On apt the key is fetched to a
	// persistent keyring file (see aptKeyringPath) and imported; Check
	// verifies that file exists — a repo whose .list matches but whose key
	// was never imported (or was deleted) is NOT converged. On yum it is
	// written as the gpgkey= field, which the compared file content already
	// covers.
	KeyURL string

	// Enabled controls the yum "enabled=" field. Defaults to true.
	Enabled bool

	// GPGCheck controls the yum "gpgcheck=" field. Defaults to true.
	GPGCheck bool

	// Refresh runs the package-cache refresh after writing the repo. Defaults to true.
	Refresh bool

	// family is the detected OS family ("debian", "redhat", "").
	family string

	// mgr is the package manager CLI ("apt-get", "dnf", "yum").
	mgr string

	file exec.FileExec
	cmd  exec.CommandExec
}

// NewPkgrepoManagedBuilder returns a state.Builder that creates PkgrepoManaged
// states using the given ModuleContext's file and command providers.
func NewPkgrepoManagedBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("pkgrepo.managed: no file provider available")
		}
		if mctx.Command == nil {
			return nil, fmt.Errorf("pkgrepo.managed: no command provider available")
		}
		return newPkgrepoManaged(id, config, mctx.File, mctx.Command, mctx.Package, mctx.Facts)
	}
}

func newPkgrepoManaged(id string, config map[string]any, file exec.FileExec,
	cmd exec.CommandExec, pkg exec.PackageExec, facts map[string]any) (state.State, error) {

	r := &PkgrepoManaged{id: id, file: file, cmd: cmd}

	r.RepoName, _ = config["name"].(string)
	if r.RepoName == "" {
		r.RepoName = id
	}

	r.HumanName, _ = config["humanname"].(string)
	if r.HumanName == "" {
		r.HumanName = r.RepoName
	}

	r.BaseURL, _ = config["baseurl"].(string)
	r.PPA, _ = config["ppa"].(string)
	r.File, _ = config["file"].(string)
	r.KeyURL, _ = config["key_url"].(string)

	// enabled / gpgcheck / refresh default to true; explicit bools override.
	r.Enabled = true
	if v, ok := config["enabled"].(bool); ok {
		r.Enabled = v
	}
	r.GPGCheck = true
	if v, ok := config["gpgcheck"].(bool); ok {
		r.GPGCheck = v
	}
	r.Refresh = true
	if v, ok := config["refresh"].(bool); ok {
		r.Refresh = v
	}

	providerName := ""
	if pkg != nil {
		providerName = pkg.Name()
	}
	r.family, r.mgr = detectPkgSystem(facts, providerName)

	// A PPA is unambiguously an apt/Debian construct.
	if r.family == "" && r.PPA != "" {
		r.family, r.mgr = "debian", "apt-get"
	}

	r.reqs = state.ParseRequisites(config)

	return r, nil
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

// importAptKey fetches an apt signing key to the persistent keyring path
// (Check's convergence marker — a /tmp download would vanish on reboot) and
// imports it into the apt trust store.
func (r *PkgrepoManaged) importAptKey(ctx context.Context) error {
	res, err := r.cmd.Run(ctx, exec.CommandOpts{
		Command: "curl",
		Args:    []string{"-fsSL", r.KeyURL},
	})
	if err != nil || res == nil {
		return fmt.Errorf("pkgrepo.managed: fetch key %s: %w", r.KeyURL, err)
	}

	keyfile := r.aptKeyringPath()
	if dir := path.Dir(keyfile); dir != "" && dir != "." {
		if err := r.file.MkdirAll(ctx, dir, 0755); err != nil {
			return fmt.Errorf("pkgrepo.managed: mkdir %s: %w", dir, err)
		}
	}
	if err := r.file.WriteFile(ctx, keyfile, []byte(res.Stdout), 0644); err != nil {
		return fmt.Errorf("pkgrepo.managed: write keyring %s: %w", keyfile, err)
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
