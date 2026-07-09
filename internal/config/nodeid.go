package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/enroll"
)

// nodeIDFileName is the identity pin written under the auth dir when the node
// id is derived from the hostname (see ResolveNodeID).
const nodeIDFileName = "node-id"

// ResolveNodeID resolves a component's stable node identity — the peel/master
// id used for enrollment credentials (<auth_dir>/<id>.creds), NATS subjects
// (zester.cmd.<id>, zester.update.cmd.<id>), and update-status. Precedence: an
// explicit configured id, else a previously pinned identity, else the machine
// hostname (`hostname -f`). The value is sanitized into a valid peel ID
// because it becomes a NATS subject token ('.' -> '_', other invalid chars ->
// '-'; see enroll.SanitizePeelID) — so an FQDN like "web01.example.com"
// resolves to "web01_example_com".
//
// A HOSTNAME-DERIVED id is pinned to <cacheDir>/node-id (Salt's minion_id
// semantics): `hostname -f` depends on DNS/NSS and silently degrades to the
// short kernel name when the resolver is down or slow, so without the pin a
// reboot during a DNS outage would re-identify the node (new enrollment, old
// creds orphaned). The pin also makes the peel and its watchdog converge on
// one identity even if they resolve at different times. An EXPLICIT id is
// never pinned (the config stays the source of truth), and an empty cacheDir
// disables pinning. Break-glass re-identify: delete <cacheDir>/node-id.
//
// A derived id that is a localhost placeholder (localhost,
// localhost.localdomain, localhost4/6, ...) is refused: it would collide
// across every misconfigured host in the fleet. Set an explicit id to
// override.
//
// Both zester-peel and zester-watchdog call this with the same inputs, so they
// always agree on the node's identity (there is no separate copy to drift). A
// one-line note is logged when the id is derived, pinned, or altered by
// sanitization.
func ResolveNodeID(explicit, cacheDir string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if raw := strings.TrimSpace(explicit); raw != "" {
		id := enroll.SanitizePeelID(raw)
		if id == "" {
			return "", fmt.Errorf("configured node id %q sanitizes to nothing: set a valid 'id'", raw)
		}
		if err := enroll.ValidatePeelID(id); err != nil {
			// Defensive: SanitizePeelID's contract guarantees a valid
			// non-empty result, so this should be unreachable.
			return "", fmt.Errorf("resolved node id %q is invalid: %w", id, err)
		}
		if id != raw {
			logger.Warn("node id sanitized to a valid NATS subject token ('.' -> '_', other invalid chars -> '-')",
				"configured", raw, "id", id)
		}
		return id, nil
	}

	pinPath := ""
	if cacheDir != "" {
		pinPath = filepath.Join(cacheDir, nodeIDFileName)
		if id, ok := readPinnedNodeID(pinPath, logger); ok {
			logger.Debug("node id loaded from identity pin", "id", id, "path", pinPath)
			return id, nil
		}
	}

	raw := hostnameFQDN()
	id := enroll.SanitizePeelID(raw)
	if id == "" {
		return "", fmt.Errorf("cannot resolve a node id from hostname %q: set a valid 'id' in the config, or ensure the host has a usable hostname", raw)
	}
	if err := enroll.ValidatePeelID(id); err != nil {
		return "", fmt.Errorf("resolved node id %q is invalid: %w", id, err)
	}
	if isLocalhostID(id) {
		return "", fmt.Errorf("hostname resolves to %q, a localhost placeholder that would collide across the fleet: set an explicit 'id' in the config or fix the hostname", id)
	}
	logger.Info("node id derived from hostname", "id", id, "hostname", raw)
	if pinPath != "" {
		if err := writePinnedNodeID(pinPath, id); err != nil {
			logger.Warn("could not pin node id (identity may change if the hostname/DNS changes)", "path", pinPath, "error", err)
		} else {
			logger.Info("node id pinned (delete this file to re-derive from the hostname)", "path", pinPath)
		}
	}
	return id, nil
}

// readPinnedNodeID loads a previously pinned identity. An unreadable file is
// treated as absent; an INVALID pin is ignored with a warning (and later
// overwritten by the fresh derivation) rather than bricking startup.
func readPinnedNodeID(path string, logger *slog.Logger) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	if err := enroll.ValidatePeelID(id); err != nil {
		logger.Warn("ignoring invalid node-id pin; re-deriving from hostname", "path", path, "error", err)
		return "", false
	}
	return id, true
}

// writePinnedNodeID persists the identity pin atomically (temp + rename).
func writePinnedNodeID(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), nodeIDFileName+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// isLocalhostID reports whether a sanitized hostname-derived id is a localhost
// placeholder (localhost, localhost.localdomain -> localhost_localdomain,
// localhost4/6 variants). Real hostnames like "localhost-dev" pass ('-', not
// '_', separates them from the sanitized-dot forms).
func isLocalhostID(id string) bool {
	base, _, _ := strings.Cut(id, "_")
	switch strings.ToLower(base) {
	case "localhost", "localhost4", "localhost6":
		return true
	}
	return false
}

// hostnameFQDN returns the fully-qualified hostname (`hostname -f`), falling
// back to the kernel hostname (os.Hostname) when the FQDN lookup is
// unavailable or times out (it can block on DNS, so it is bounded). The result
// is sanitized by the caller.
func hostnameFQDN() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "hostname", "-f").Output(); err == nil {
		if h := strings.TrimSpace(string(out)); h != "" {
			return h
		}
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}
