package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/state"
)

// SSHAuthAbsent implements the ssh_auth.absent state.
// It ensures an SSH public key line is not present in authorized_keys.
//
// SSHAuthAbsent is also its own schema proto: the tagged exported fields ARE the
// module's parameter declaration (one schema declaration per module). Under the
// uniform decoder a numeric `name`/`user`/`config` coerces to its string form and
// a composite is rejected (BD-6). The `name` primary is TrimSpace'd and the
// require-`user`-OR-`config` cross-field rule is enforced in the builder tail
// (both are module logic, not schema). The unexported runtime fields (id, reqs,
// file, revert memos) are untagged, so the schema compiler skips them.
type SSHAuthAbsent struct {
	id   string
	reqs state.Requisites

	// Key is the public key blob to remove; it defaults to the state ID and is
	// TrimSpace'd in the builder.
	Key string `zester:"name,primary" usage:"public key blob (or full key line) to remove; matched on the blob; defaults to the state ID"`
	// User/Config: ssh_auth.* family target component.
	sshAuthTargetParam

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence;
	// unset memos make Revert an explicit clean no-op (see revertHostsFile).
	backup    []byte
	backupSet bool
}

// sshAuthAbsentSpec is the compiled schema + documentation for ssh_auth.absent.
var sshAuthAbsentSpec = mustSpec("ssh_auth.absent", modschema.KindState, SSHAuthAbsent{}, modschema.Doc{
	Summary: "Ensure an SSH public key is absent from a user's authorized_keys.",
	Description: "`ssh_auth.absent` ensures an SSH public key line is NOT present in a user's " +
		"`authorized_keys`. The key is given as `name` (defaulting to the state ID) and matched on its " +
		"BLOB (the base64 body), so a bare blob or a full key line both identify the same authorized " +
		"line. The managed file is resolved from `config` when set or the `user`'s home directory — one " +
		"of `user` or `config` is required.",
	Effects: modschema.Effects{
		Check: "Resolves the authorized_keys path (from `config`, or the `user`'s home) and reads it (a " +
			"non-not-exist read error fails the phase). Reports no change when the file does not exist or " +
			"no line carries the key blob; otherwise reports a change.",
		Apply: "Reads authorized_keys and, when a line carries the key blob, memoizes the prior content " +
			"for revert and rewrites the file with every matching line removed. A missing file or an " +
			"already-absent key is a clean no-op. Reports the path and user in its details.",
		Revert: "Restores the authorized_keys content this run's Apply captured before removing the key " +
			"(with 0600 mode). A fresh instance (a standalone revert) recorded nothing and is an explicit " +
			"clean no-op — it never rewrites a user's authorized_keys it did not touch.",
	},
	Examples: []modschema.Example{
		{
			Title:       "Revoke a key for a user",
			Kind:        "state",
			Explanation: "name is the key blob to remove; matched on the blob.",
			Code:        "AAAAB3NzaC1yc2EAAAADAQABAAAB...:\n  ssh_auth.absent:\n    - user: deploy\n",
		},
		{
			Title:       "Revoke a key ad hoc",
			Kind:        "cli",
			Explanation: "The bare positional argument is the key blob; user is a key=value.",
			Code:        "zester 'web*' ssh_auth.absent AAAAB3Nza... user=deploy",
		},
	},
	Notes: []modschema.Note{
		{
			Level: "info",
			Title: "Comment and blank lines are preserved",
			Body: "The `authorized_keys` file is rewritten in memory with existing comment (`#`) and blank " +
				"lines kept verbatim; every line carrying the key blob is removed.",
		},
		{
			Level: "info",
			Title: "Divergences from Salt",
			Body: "`config` is used as the LITERAL `authorized_keys` path (Salt's `config` is relative to the " +
				"user's home directory). The `user` is resolved via the peel's OS user database. Salt's " +
				"`options` and `source` parameters are not supported.",
		},
	},
	Divergences: []string{"BD-6"},
	SeeAlso:     []string{"ssh_auth.present"},
})

// NewSSHAuthAbsentBuilder returns a state.Builder that creates SSHAuthAbsent
// states using the given ModuleContext's file provider. Decode policy (unknown-key
// handling, reserved keys) is threaded via opts; the peel supplies it through
// modules.RegisterAll.
func NewSSHAuthAbsentBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("ssh_auth.absent: no file provider available")
		}
		// Decode the typed parameters first. Decode is transactional and commits
		// by replacing the whole struct, so the injected provider, id, and
		// requisites MUST be assigned AFTER it.
		s := &SSHAuthAbsent{}
		if _, err := sshAuthAbsentSpec.Decode(id, config, s, opts); err != nil {
			return nil, fmt.Errorf("ssh_auth.absent: %w", err)
		}
		// Builder-tail module logic (not schema): trim the key primary (a decoder
		// never trims), then enforce the require-user-OR-config cross-field rule.
		s.Key = strings.TrimSpace(s.Key)
		if s.Config == "" && s.User == "" {
			return nil, fmt.Errorf("ssh_auth.absent: %s: user or config path is required", id)
		}
		s.id = id
		s.file = mctx.File
		s.reqs = state.ParseRequisites(config)
		return s, nil
	}
}

func (s *SSHAuthAbsent) Name() string           { return "ssh_auth.absent:" + s.id }
func (s *SSHAuthAbsent) Reqs() state.Requisites { return s.reqs }

func (s *SSHAuthAbsent) keyBlob() string {
	fields := strings.Fields(s.Key)
	switch len(fields) {
	case 0:
		return s.Key
	case 1:
		return fields[0]
	default:
		return fields[1]
	}
}

func (s *SSHAuthAbsent) Check(ctx context.Context) (state.CheckResult, error) {
	path, err := s.authKeysPath()
	if err != nil {
		return state.CheckResult{}, err
	}
	current, existed, err := readManagedFile(ctx, s.file, path)
	if err != nil {
		return state.CheckResult{}, fmt.Errorf("ssh_auth.absent: read %s: %w", path, err)
	}
	if !existed {
		return state.CheckResult{NeedsChange: false}, nil
	}
	desired := renderSSHAuthAbsent(current, s.keyBlob())
	if desired == current {
		return state.CheckResult{NeedsChange: false}, nil
	}
	return state.CheckResult{
		NeedsChange: true,
		Diff:        fmt.Sprintf("ssh key should be absent from %s", path),
	}, nil
}

func (s *SSHAuthAbsent) Apply(ctx context.Context) (state.ApplyResult, error) {
	path, err := s.authKeysPath()
	if err != nil {
		return state.ApplyResult{}, err
	}
	current, existed, err := readManagedFile(ctx, s.file, path)
	if err != nil {
		return state.ApplyResult{}, fmt.Errorf("ssh_auth.absent: read %s: %w", path, err)
	}
	if !existed {
		return state.ApplyResult{Changed: false}, nil
	}
	s.backup = []byte(current)
	s.backupSet = true

	desired := renderSSHAuthAbsent(current, s.keyBlob())
	if desired == current {
		return state.ApplyResult{Changed: false}, nil
	}

	if err := s.file.WriteFile(ctx, path, []byte(desired), 0600); err != nil {
		return state.ApplyResult{}, fmt.Errorf("ssh_auth.absent: write %s: %w", path, err)
	}

	return state.ApplyResult{
		Changed: true,
		Diff:    fmt.Sprintf("removed ssh key from %s", path),
		Details: map[string]string{
			"path": path,
			"user": s.User,
		},
	}, nil
}

func (s *SSHAuthAbsent) Revert(ctx context.Context) (state.ApplyResult, error) {
	path, err := s.authKeysPath()
	if err != nil {
		return state.ApplyResult{}, err
	}
	return revertHostsFile(ctx, s.file, path, s.backup, s.backupSet, false, 0600, "ssh_auth.absent")
}

func (s *SSHAuthAbsent) authKeysPath() (string, error) {
	return resolveAuthKeysPath(s.Config, s.User)
}

// renderSSHAuthAbsent removes any line whose key field matches keyBlob.
func renderSSHAuthAbsent(content, keyBlob string) string {
	existing := splitHostLines(content)
	out := make([]string, 0, len(existing))
	for _, l := range existing {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, l)
			continue
		}
		if lineHasKey(trimmed, keyBlob) {
			continue
		}
		out = append(out, l)
	}
	return joinHostLines(out)
}
