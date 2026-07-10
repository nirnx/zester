package modules

import (
	"context"
	"fmt"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/state"
)

// SSHAuthPresent implements the ssh_auth.present state.
// It ensures an SSH public key line is present in a user's authorized_keys
// file, creating the ~/.ssh directory (0700) and authorized_keys (0600) as
// needed. Idempotency is based on the key blob field.
type SSHAuthPresent struct {
	id   string
	reqs state.Requisites

	// Key is the public key blob (the base64 body), or a full key line.
	Key string

	// User is the account whose authorized_keys is managed.
	User string

	// Enc is the key encoding/type (e.g., "ssh-rsa"). Default "ssh-rsa".
	Enc string

	// Comment is an optional trailing comment on the key line.
	Comment string

	// Config overrides the authorized_keys path (default ~user/.ssh/authorized_keys).
	Config string

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence;
	// unset memos make Revert an explicit clean no-op (see revertHostsFile).
	backup    []byte
	backupSet bool
	created   bool
}

// NewSSHAuthPresentBuilder returns a state.Builder that creates SSHAuthPresent states.
func NewSSHAuthPresentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("ssh_auth.present: no file provider available")
		}
		return newSSHAuthPresent(id, config, mctx.File)
	}
}

func newSSHAuthPresent(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	s := &SSHAuthPresent{id: id, file: file}

	s.Key, _ = config["name"].(string)
	if s.Key == "" {
		s.Key = id
	}
	s.Key = strings.TrimSpace(s.Key)

	s.User, _ = config["user"].(string)
	s.Enc, _ = config["enc"].(string)
	if s.Enc == "" {
		s.Enc = "ssh-rsa"
	}
	s.Comment, _ = config["comment"].(string)
	s.Config, _ = config["config"].(string)

	if s.Config == "" && s.User == "" {
		return nil, fmt.Errorf("ssh_auth.present: %s: user or config path is required", id)
	}

	s.reqs = state.ParseRequisites(config)

	return s, nil
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

// SSHAuthAbsent implements the ssh_auth.absent state.
// It ensures an SSH public key line is not present in authorized_keys.
type SSHAuthAbsent struct {
	id   string
	reqs state.Requisites

	Key    string
	User   string
	Config string

	file exec.FileExec

	// Revert memos, valid only for a same-instance Apply→Revert sequence;
	// unset memos make Revert an explicit clean no-op (see revertHostsFile).
	backup    []byte
	backupSet bool
}

// NewSSHAuthAbsentBuilder returns a state.Builder that creates SSHAuthAbsent states.
func NewSSHAuthAbsentBuilder(mctx *exec.ModuleContext) state.Builder {
	return func(id string, config map[string]any) (state.State, error) {
		if mctx.File == nil {
			return nil, fmt.Errorf("ssh_auth.absent: no file provider available")
		}
		return newSSHAuthAbsent(id, config, mctx.File)
	}
}

func newSSHAuthAbsent(id string, config map[string]any, file exec.FileExec) (state.State, error) {
	s := &SSHAuthAbsent{id: id, file: file}

	s.Key, _ = config["name"].(string)
	if s.Key == "" {
		s.Key = id
	}
	s.Key = strings.TrimSpace(s.Key)

	s.User, _ = config["user"].(string)
	s.Config, _ = config["config"].(string)

	if s.Config == "" && s.User == "" {
		return nil, fmt.Errorf("ssh_auth.absent: %s: user or config path is required", id)
	}

	s.reqs = state.ParseRequisites(config)

	return s, nil
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

// resolveAuthKeysPath returns the authorized_keys path from an explicit config
// override or by resolving the user's home directory.
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

// lineHasKey reports whether an authorized_keys line contains the given key
// blob as one of its whitespace-separated fields.
func lineHasKey(line, keyBlob string) bool {
	if keyBlob == "" {
		return false
	}
	for _, f := range strings.Fields(line) {
		if f == keyBlob {
			return true
		}
	}
	return false
}
