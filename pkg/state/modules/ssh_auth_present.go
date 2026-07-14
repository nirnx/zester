package modules

import (
	"context"
	"fmt"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// SSHAuthPresent implements the ssh_auth.present state.
// It ensures an SSH public key line is present in a user's authorized_keys
// file, creating the ~/.ssh directory (0700) and authorized_keys (0600) as
// needed. Idempotency is based on the key blob field.
//
// SSHAuthPresent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). `enc`
// carries an EAGER `default=ssh-rsa`. Under the uniform decoder a numeric
// `name`/`user`/`enc`/`comment`/`config` coerces to its string form and a
// composite is rejected (BD-6). Two pieces of module logic stay in the builder
// tail, NOT the schema: the `name` primary is TrimSpace'd (a decoder never trims),
// and the require-`user`-OR-`config` cross-field rule (cross-field validation is
// module logic). The key material is PUBLIC key content, so no parameter is
// `sensitive`. The unexported runtime fields (id, reqs, file, revert memos) are
// untagged, so the schema compiler skips them.
type SSHAuthPresent struct {
	id   string
	reqs state.Requisites

	// Key is the public key blob (the base64 body), or a full key line; it
	// defaults to the state ID and is TrimSpace'd in the builder.
	Key string `zester:"name,primary" usage:"public key blob (the base64 body) or a full key line; defaults to the state ID"`
	// User/Config: ssh_auth.* family target component.
	sshAuthTargetParam
	// Enc is the key encoding/type (e.g., "ssh-rsa"). Default "ssh-rsa".
	Enc string `zester:"enc,default=ssh-rsa" usage:"key encoding/type (ssh-rsa, ssh-ed25519, …); defaults to ssh-rsa; ignored when name is a full key line"`
	// Comment is an optional trailing comment on the key line.
	Comment string `zester:"comment" usage:"optional trailing comment on the key line"`

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence;
	// unset memos make Revert an explicit clean no-op (see revertHostsFile).
	backup    []byte
	backupSet bool
	created   bool
}

// sshAuthPresentSpec is the compiled schema + documentation for ssh_auth.present.
// Its Doc is drift-corrected against the live Check/Apply/Revert behavior — notably
// that a full key line is used verbatim and idempotency keys on the key blob.
var sshAuthPresentSpec = mustSpec("ssh_auth.present", modschema.KindState, SSHAuthPresent{}, modschema.Doc{
	Summary: "Ensure an SSH public key is present in a user's authorized_keys.",
	Description: "`ssh_auth.present` ensures an SSH public key line is present in a user's " +
		"`authorized_keys`, creating `~/.ssh` (0700) and `authorized_keys` (0600) as needed. The key is " +
		"given as `name` (defaulting to the state ID): a bare base64 blob is combined with `enc` " +
		"(default `ssh-rsa`) and an optional `comment`, while a full `\"ssh-… AAAA… comment\"` line is " +
		"used verbatim. Idempotency keys on the key BLOB, so re-running with a changed comment or " +
		"encoding replaces the matching line rather than duplicating it. The managed file is the target " +
		"user's `authorized_keys`, resolved from `config` when set or the account's home directory " +
		"otherwise — one of `user` or `config` is required.",
	Effects: modschema.Effects{
		Check: "Resolves the authorized_keys path (from `config`, or the `user`'s home) and reads it (a " +
			"non-not-exist read error fails the phase). Reports a change when no line carries the desired " +
			"key blob, or when the matching line differs from the desired line (a changed comment or " +
			"encoding).",
		Apply: "Ensures `~/.ssh` exists (0700), reads authorized_keys, memoizes its prior content (or " +
			"that it did not exist) for revert, and rewrites it so a single line carries the key blob — " +
			"replacing any existing line for the same blob and deduplicating, or writing the file (0600) " +
			"when absent. An already-present identical line is a clean no-op. Reports the path and user in " +
			"its details.",
		Revert: "Restores what this run's Apply changed: an authorized_keys that pre-existed is rewritten " +
			"with its captured prior content and 0600 mode; a file this instance created is removed. A " +
			"fresh instance (a standalone revert) recorded nothing and is an explicit clean no-op — it " +
			"never rewrites a user's authorized_keys it did not touch.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Authorize a key for a user",
			Kind:        "state",
			Explanation: "name is the key blob; enc defaults to ssh-rsa; comment labels the line.",
			Code: "AAAAB3NzaC1yc2EAAAADAQABAAAB...:\n  ssh_auth.present:\n    - user: deploy\n" +
				"    - comment: deploy@ci\n",
		},
		{
			Title:       "Authorize a full ed25519 key line",
			Kind:        "state",
			Explanation: "A full \"ssh-… AAAA… comment\" line is used verbatim; enc/comment are ignored.",
			Code: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA... alice@laptop:\n  ssh_auth.present:\n" +
				"    - user: alice\n",
		},
		{
			Title:       "Authorize a key ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the key blob; user/enc/comment are key=values.",
			Code:        "zester 'web*' ssh_auth.present AAAAB3Nza... user=deploy enc=ssh-rsa",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Identity is the key blob",
			Body: "The managed line's identity is the key BLOB (its base64 body). Re-running with a " +
				"different `comment` or `enc` replaces the matching line in place rather than adding a " +
				"duplicate. Provide either a bare blob (combined with `enc`/`comment`) or a full key line " +
				"(used verbatim).",
		},
		{
			Level: "info",
			Title: "user or config selects the file",
			Body: "The managed file is resolved from `config` (an explicit authorized_keys path) when set, " +
				"otherwise from the `user`'s home directory (`~user/.ssh/authorized_keys`). One of the two " +
				"is required.",
		},
		{
			Level: "info",
			Title: "Comment and blank lines are preserved",
			Body: "The `authorized_keys` file is rewritten in memory: existing comment (`#`) and blank lines " +
				"are kept verbatim, and only the line carrying the key blob is added, replaced in place, or " +
				"deduplicated.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "`config` is used as the LITERAL `authorized_keys` path (so it must be absolute to be " +
				"meaningful), whereas Salt's `config` is relative to the user's home directory (default " +
				"`.ssh/authorized_keys`). Salt's `options` (key options such as `no-pty`) and `source` " +
				"(key-file URL) parameters are not supported. The `user` is resolved via the peel's OS user " +
				"database, and the `.ssh` directory and file are written by the peel's file provider — " +
				"ownership is not changed to the target user.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"ssh_auth.absent"},
})

// NewSSHAuthPresentBuilder returns a state.Builder that creates SSHAuthPresent
// states using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewSSHAuthPresentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("ssh_auth.present: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		s := &SSHAuthPresent{}
		if _, err := sshAuthPresentSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("ssh_auth.present: %w", err)
		}
		// Builder-tail module logic (not schema): trim the key primary (a decoder
		// never trims), then enforce the require-user-OR-config cross-field rule.
		s.Key = strings.TrimSpace(s.Key)
		if s.Config == "" && s.User == "" {
			return nil, fmt.Errorf("ssh_auth.present: %s: user or config path is required", id)
		}
		s.id = id
		s.file = mctx.File
		s.reqs = state.ParseRequisites(config)
		return s, nil
	}
}

func (s *SSHAuthPresent) Name() string           { return "ssh_auth.present:" + s.id }
func (s *SSHAuthPresent) Reqs() state.Requisites { return s.reqs }

// keyBlob returns the raw key body used for matching. If Key was given as a
// full line ("ssh-rsa AAAA... comment"), the middle field is used.
func (s *SSHAuthPresent) keyBlob() string {
	fields := strings.Fields(s.Key)
	switch len(fields) {
	case 0:
		return s.Key
	case 1:
		return fields[0]
	default:
		// Assume "enc key [comment]" form: the blob is the second field.
		return fields[1]
	}
}

// desiredLine returns the authorized_keys line to write.
func (s *SSHAuthPresent) desiredLine() string {
	// If Key already looks like a full line, use it verbatim.
	if len(strings.Fields(s.Key)) >= 2 {
		return strings.TrimSpace(s.Key)
	}
	parts := []string{s.Enc, s.Key}
	if s.Comment != "" {
		parts = append(parts, s.Comment)
	}
	return strings.Join(parts, " ")
}

func (s *SSHAuthPresent) Check(ctx context.Context) (state.CheckResult, error) {
	path, err := s.authKeysPath()
	if err != nil {
		return state.CheckResult{}, err
	}
	current, _, err := readManagedFile(ctx, s.file, path)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("ssh_auth.present: read %s: %w", path, err)
	}
	desired := renderSSHAuthPresent(current, s.keyBlob(), s.desiredLine())
	if desired == current {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("ssh key should be present in %s", path),
	}, nil
}

func (s *SSHAuthPresent) Apply(ctx context.Context) (state.ApplyResult, error) {
	path, err := s.authKeysPath()
	if err != nil {
		return state.ApplyResult{}, err
	}

	// Ensure the .ssh directory exists with 0700 perms.
	dir := filepath.Dir(path)
	if err := s.file.MkdirAll(ctx, dir, 0700); err != nil {
		return state.ApplyResult{}, fmt.Errorf("ssh_auth.present: mkdir %s: %w", dir, err)
	}

	current, existed, err := readManagedFile(ctx, s.file, path)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("ssh_auth.present: read %s: %w", path, err)
	}
	s.backup = []byte(current)
	s.backupSet = existed
	s.created = !existed

	desired := renderSSHAuthPresent(current, s.keyBlob(), s.desiredLine())
	if desired == current {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := s.file.WriteFile(ctx, path, []byte(desired), 0600); err != nil {
		return state.ApplyResult{}, fmt.Errorf("ssh_auth.present: write %s: %w", path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("added ssh key to %s", path),
		Details: map[string]string{
			"path": path,
			"user": s.User,
		},
	}, nil
}

func (s *SSHAuthPresent) Revert(ctx context.Context) (state.ApplyResult, error) {
	path, err := s.authKeysPath()
	if err != nil {
		return state.ApplyResult{}, err
	}
	return revertHostsFile(ctx, s.file, path, s.backup, s.backupSet, s.created, 0600, "ssh_auth.present")
}

func (s *SSHAuthPresent) authKeysPath() (string, error) {
	return resolveAuthKeysPath(s.Config, s.User)
}

// resolveAuthKeysPath returns the authorized_keys path from an explicit config
// override or by resolving the user's home directory. Shared by ssh_auth.present
// and ssh_auth.absent.
func resolveAuthKeysPath(config, username string) (string, error) {
	if config != "" {
		return config, nil
	}
	if username == "" {
		return "", fmt.Errorf("ssh_auth: user or config path is required")
	}
	u, err := user.Lookup(username)
	if err != nil {
		return "", fmt.Errorf("ssh_auth: lookup user %q: %w", username, err)
	}
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), nil
}

// renderSSHAuthPresent ensures a single line for keyBlob (equal to line) is
// present in the authorized_keys content, replacing any existing line whose
// key field matches keyBlob and deduplicating.
func renderSSHAuthPresent(content, keyBlob, line string) string {
	existing := splitHostLines(content)
	out := make([]string, 0, len(existing)+1)
	found := false

	for _, l := range existing {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, l)
			continue
		}
		if lineHasKey(trimmed, keyBlob) {
			if !found {
				out = append(out, line)
				found = true
			}
			// Duplicate key line — drop it.
			continue
		}
		out = append(out, l)
	}

	if !found {
		out = append(out, line)
	}
	return joinHostLines(out)
}

// lineHasKey reports whether an authorized_keys line contains the given key
// blob as one of its whitespace-separated fields. Shared by ssh_auth.present
// and ssh_auth.absent.
func lineHasKey(line, keyBlob string) bool {
	if keyBlob == "" {
		return false
	}
	return slices.Contains(strings.Fields(line), keyBlob)
}
