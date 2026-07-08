package settings

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
)

// MasterCurvePubKey is the well-known KV key where the master's curve public key
// is stored, so peels can read it for secret decryption.
const MasterCurvePubKey = "_master_curve_pub"

// ClusterInfoKey is the well-known KV key (secrets bucket) where the master
// publishes the JSON bootstrap document (CA trust bundle + fleet NATS
// endpoints) for the peel discovery/refresh channel. Published by every
// master, not lease-gated, idempotent — the MasterCurvePubKey pattern.
const ClusterInfoKey = "_cluster_info"

// ManifestKey is the well-known KV key holding the file manifest for the
// current publish batch: a MessagePack-encoded, key-sorted []ManifestEntry.
// It is written after all file keys and before the _revision bump, so a
// resolver that sees a given revision can verify the files it loads belong
// to the batch that produced that revision (torn-read detection) and so
// publishers can delete keys that are no longer part of the source tree.
const ManifestKey = "_manifest"

// ManifestEntry describes one published file in a distribution batch.
type ManifestEntry struct {
	// Key is the KV key of the file (e.g., "common/base.zy").
	Key string `msgpack:"key"`

	// SHA256 is the hex-encoded SHA-256 of the file content exactly as
	// stored in KV (i.e., the sanitized content for settings files).
	SHA256 string `msgpack:"sha256"`
}

// SecretPlaceholderPrefix is the prefix used for secret placeholders in sanitized
// .zy templates. When the master pre-processes templates for shared KV storage,
// plaintext !encrypted values are replaced with these placeholders. The peel
// replaces them with decrypted values from its per-peel secrets entry.
const SecretPlaceholderPrefix = "__ZESTER_SECRET:"

// SecretPlaceholderSuffix closes a secret placeholder.
const SecretPlaceholderSuffix = "__"

// Publisher handles the master-side operations for peel-side rendering:
// sanitizing .zy templates, storing them in shared KV, and publishing
// per-peel encrypted secrets.
type Publisher struct {
	settingsDir string
	filesKV     bus.KV
	secretsKV   bus.KV
	masterEnc   *auth.Encryptor
	logger      *slog.Logger

	// secretsMu guards secretsHash. PublishSecrets may be called from the
	// facts-watch callback and startup paths concurrently.
	secretsMu sync.Mutex

	// secretsHash caches, per peel, the fingerprint of the last
	// successfully published secrets payload: sha256 over the master
	// (sender) curve public key, the recipient curve public key, and a
	// canonical encoding of the plaintext secret map. Because the sealed
	// box is randomized, re-encrypting identical inputs yields a new
	// ciphertext every time — this cache is what lets PublishSecrets skip
	// the pointless re-encrypt + KV write when nothing changed.
	secretsHash map[string]string
}

// PublisherConfig configures the settings publisher.
type PublisherConfig struct {
	// SettingsDir is the root directory for .zy files on the master.
	SettingsDir string

	// FilesKV is the shared KV bucket for sanitized template files.
	FilesKV bus.KV

	// SecretsKV is the per-peel KV bucket for encrypted secrets.
	SecretsKV bus.KV

	// MasterEncryptor encrypts secret values for peels. Its curve public
	// key is part of the secrets skip-cache fingerprint, so constructing a
	// Publisher with a rotated master key never skips a republish.
	MasterEncryptor *auth.Encryptor

	// Logger is the structured logger. Defaults to slog.Default().
	Logger *slog.Logger
}

// NewPublisher creates a settings publisher for the master.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.FilesKV == nil {
		return nil, fmt.Errorf("settings: FilesKV is required")
	}
	if cfg.SecretsKV == nil {
		return nil, fmt.Errorf("settings: SecretsKV is required")
	}
	if cfg.MasterEncryptor == nil {
		return nil, fmt.Errorf("settings: MasterEncryptor is required")
	}
	if cfg.SettingsDir == "" {
		cfg.SettingsDir = DefaultSettingsDir
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Publisher{
		settingsDir: cfg.SettingsDir,
		filesKV:     cfg.FilesKV,
		secretsKV:   cfg.SecretsKV,
		masterEnc:   cfg.MasterEncryptor,
		logger:      cfg.Logger,
		secretsHash: make(map[string]string),
	}, nil
}

// SanitizedFile represents a .zy template with !encrypted values replaced
// by placeholders.
type SanitizedFile struct {
	// Content is the sanitized template content (no plaintext secrets).
	Content []byte

	// Secrets maps dot-path keys to their plaintext values.
	// These must be encrypted per-peel and stored separately.
	Secrets map[string]string
}

// SanitizeFile reads a .zy file and replaces all !encrypted values with
// placeholders. Returns the sanitized content and the extracted secrets.
func SanitizeFile(data []byte) (*SanitizedFile, error) {
	secrets := make(map[string]string)

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return &SanitizedFile{Content: data, Secrets: secrets}, nil
	}

	sanitizeNodes(&doc, secrets, "")

	sanitized, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("settings: re-marshal sanitized YAML: %w", err)
	}

	return &SanitizedFile{
		Content: sanitized,
		Secrets: secrets,
	}, nil
}

// sanitizeNodes recursively walks the YAML AST and replaces !encrypted
// tagged values with placeholder references.
func sanitizeNodes(node *yaml.Node, secrets map[string]string, prefix string) {
	if node == nil {
		return
	}

	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]
			fullKey := prefix + keyNode.Value
			if valNode.Tag == EncryptedTag {
				secrets[fullKey] = valNode.Value
				valNode.Tag = ""
				valNode.Value = SecretPlaceholderPrefix + fullKey + SecretPlaceholderSuffix
				valNode.Style = yaml.DoubleQuotedStyle
			}
			// Ensure values containing template expressions stay quoted
			// so YAML doesn't parse {{ }} as a mapping literal.
			if valNode.Kind == yaml.ScalarNode && strings.Contains(valNode.Value, "{{") {
				valNode.Style = yaml.DoubleQuotedStyle
			}
			sanitizeNodes(valNode, secrets, fullKey+".")
		}
	}

	if node.Kind == yaml.SequenceNode || node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			sanitizeNodes(child, secrets, prefix)
		}
	}
}

// FileSecrets maps file keys to their extracted secrets (dot-path → plaintext).
// This preserves per-file grouping so secrets can be filtered by which files
// target a given peel, preventing over-sharing of secrets across peels.
type FileSecrets map[string]map[string]string

// ForRefs returns only secrets from files matching the given settings refs.
// Refs are converted to KV keys via resolveRefToKVKey (e.g., "app" → "app.zy").
func (fs FileSecrets) ForRefs(refs []string) map[string]string {
	result := make(map[string]string)
	for _, ref := range refs {
		filePath := resolveRefToKVKey(ref)
		if secrets, ok := fs[filePath]; ok {
			for k, v := range secrets {
				result[k] = v
			}
		}
	}
	return result
}

// Flat returns all secrets merged into a single map (for backward compat).
func (fs FileSecrets) Flat() map[string]string {
	result := make(map[string]string)
	for _, secrets := range fs {
		for k, v := range secrets {
			result[k] = v
		}
	}
	return result
}

// PublishRawFiles reads all .zy files from the settings directory, sanitizes
// them (replacing !encrypted values with placeholders), and stores the
// sanitized templates in the shared settings-files KV bucket. Returns
// per-file secrets so callers can filter by peel targeting.
//
// After all file keys are written, a _manifest key (sorted list of
// {key, sha256} entries) is published, stale bucket keys absent from the
// manifest are deleted (deletion propagation), and finally _revision is
// bumped. Well-known keys (_revision, _manifest, the master curve public
// key) are never deleted.
func (p *Publisher) PublishRawFiles(ctx context.Context, files map[string][]byte) (FileSecrets, error) {
	allSecrets := make(FileSecrets)
	manifest := make([]ManifestEntry, 0, len(files))

	for key, data := range files {
		sanitized, err := SanitizeFile(data)
		if err != nil {
			return nil, fmt.Errorf("settings: sanitize %s: %w", key, err)
		}

		if _, err := p.filesKV.Put(ctx, key, sanitized.Content); err != nil {
			return nil, fmt.Errorf("settings: publish file %s to KV: %w", key, err)
		}

		sum := sha256.Sum256(sanitized.Content)
		manifest = append(manifest, ManifestEntry{
			Key:    key,
			SHA256: hex.EncodeToString(sum[:]),
		})

		if len(sanitized.Secrets) > 0 {
			allSecrets[key] = sanitized.Secrets
		}
	}

	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Key < manifest[j].Key })

	if _, err := bus.KVPut(ctx, p.filesKV, ManifestKey, manifest); err != nil {
		return nil, fmt.Errorf("settings: publish manifest: %w", err)
	}

	// Prune is best-effort garbage collection and must never block the
	// revision bump: files and _manifest are already committed, and a
	// batch whose _revision never bumps stalls every warm peel on the old
	// generation until the next publisher-lease acquisition. Stale keys
	// are inert — the resolver only loads top.zy-referenced files and
	// verifies every load against the manifest — and the next successful
	// publish re-prunes them.
	p.pruneStaleFiles(ctx, manifest)

	if err := bus.BumpRevision(ctx, p.filesKV); err != nil {
		return nil, fmt.Errorf("settings: bump revision: %w", err)
	}

	return allSecrets, nil
}

// pruneStaleFiles deletes settings-files KV keys that are not part of the
// given manifest. Well-known keys (_revision, _manifest, and the master
// curve public key) are always preserved.
//
// Pruning is best-effort garbage collection (matching statefiles'
// deleteStaleKeys): a transient ListKeys error or failed Delete is logged at
// Warn and never propagated, so it can never abort the publish before the
// _revision bump. Keys that survive a failed prune are inert for resolvers
// (manifest-verified selective loading) and are re-pruned on the next
// successful publish.
func (p *Publisher) pruneStaleFiles(ctx context.Context, manifest []ManifestEntry) {
	keep := make(map[string]struct{}, len(manifest))
	for _, e := range manifest {
		keep[e.Key] = struct{}{}
	}

	lister, err := p.filesKV.ListKeys(ctx)
	if err != nil {
		if !errors.Is(err, bus.ErrNoKeysFound) {
			p.logger.Warn("failed to list settings-files keys for stale-key cleanup", "error", err)
		}
		return
	}
	defer lister.Stop()

	var stale []string
	for key := range lister.Keys() {
		if isProtectedFileKey(key) {
			continue
		}
		if _, ok := keep[key]; ok {
			continue
		}
		stale = append(stale, key)
	}
	sort.Strings(stale)

	var failed []string
	for _, key := range stale {
		if err := p.filesKV.Delete(ctx, key); err != nil {
			failed = append(failed, key)
			p.logger.Warn("failed to prune stale settings file from KV", "key", key, "error", err)
			continue
		}
		p.logger.Info("pruned stale settings file from KV", "key", key)
	}
	if len(failed) > 0 {
		p.logger.Warn("stale settings files left in KV; next successful publish will re-prune",
			"keys", strings.Join(failed, ","))
	}
}

// isProtectedFileKey reports whether a settings-files bucket key must never
// be deleted by manifest-based pruning.
func isProtectedFileKey(key string) bool {
	return key == bus.KeyRevision || key == ManifestKey || key == MasterCurvePubKey
}

// PublishSecrets encrypts secret values for a specific peel and stores them
// in the per-peel secrets KV bucket. The secrets map keys are dot-paths
// and values are plaintext that will be encrypted with the peel's curve key.
//
// Publishing is hash-gated: the sealed box is randomized, so re-encrypting
// identical inputs would produce a new ciphertext (and a KV write, and a
// secrets-watch wakeup on the peel) every call. When the fingerprint of
// (master curve pub, recipient curve pub, secret map) matches the last
// successful publish for this peel, the call is a no-op. A failed encrypt
// or KV put never updates the cache, so the next call retries. Rotating
// either curve key changes the fingerprint and forces a republish.
func (p *Publisher) PublishSecrets(ctx context.Context, peelID string, secrets map[string]string, curvePub string) error {
	if len(secrets) == 0 {
		return nil
	}

	if curvePub == "" {
		return fmt.Errorf("settings: peel %s has secrets but no curve public key", peelID)
	}

	fp := secretsFingerprint(p.masterEnc.PublicKey(), curvePub, secrets)

	p.secretsMu.Lock()
	if p.secretsHash[peelID] == fp {
		p.secretsMu.Unlock()
		p.logger.Debug("secrets unchanged, skipping publish", "peel", peelID)
		return nil
	}
	p.secretsMu.Unlock()

	encrypted := make(map[string]string, len(secrets))
	for dotPath, plaintext := range secrets {
		enc, err := p.masterEnc.SealSettingsValue([]byte(plaintext), curvePub)
		if err != nil {
			return fmt.Errorf("settings: encrypt secret %q for peel %s: %w", dotPath, peelID, err)
		}
		encrypted[dotPath] = enc
	}

	if _, err := bus.KVPut(ctx, p.secretsKV, peelID, encrypted); err != nil {
		return fmt.Errorf("settings: publish secrets for peel %s: %w", peelID, err)
	}

	// Only a fully successful publish updates the skip-cache.
	p.secretsMu.Lock()
	p.secretsHash[peelID] = fp
	p.secretsMu.Unlock()

	return nil
}

// InvalidateSecretsCache drops the publish skip-cache fingerprints for the
// given peels, forcing the next PublishSecrets call for them to re-encrypt
// and re-publish even when inputs are unchanged. Called with no arguments it
// clears the entire cache (e.g., after an out-of-band secrets-bucket wipe).
// Master curve key rotation does NOT require this: the sender key is part of
// the fingerprint, so a Publisher built with a rotated key never skips.
func (p *Publisher) InvalidateSecretsCache(peelIDs ...string) {
	p.secretsMu.Lock()
	defer p.secretsMu.Unlock()
	if len(peelIDs) == 0 {
		p.secretsHash = make(map[string]string)
		return
	}
	for _, id := range peelIDs {
		delete(p.secretsHash, id)
	}
}

// secretsFingerprint computes a canonical fingerprint of a per-peel secrets
// publish: sha256 over the sender (master) curve public key, the recipient
// curve public key, and the secret map with keys in sorted order. Every
// component is length-prefixed so distinct inputs can never collide by
// concatenation ambiguity.
func secretsFingerprint(senderPub, recipientPub string, secrets map[string]string) string {
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	writeLenPrefixed(h, senderPub)
	writeLenPrefixed(h, recipientPub)
	for _, k := range keys {
		writeLenPrefixed(h, k)
		writeLenPrefixed(h, secrets[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeLenPrefixed writes an 8-byte big-endian length followed by the raw
// bytes of s, giving the fingerprint hash an unambiguous canonical encoding.
func writeLenPrefixed(w io.Writer, s string) {
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
	w.Write(lenBuf[:])   //nolint:errcheck // hash.Hash never errors
	io.WriteString(w, s) //nolint:errcheck // hash.Hash never errors
}

// IsSecretPlaceholder checks if a string value is a secret placeholder.
func IsSecretPlaceholder(s string) bool {
	return strings.HasPrefix(s, SecretPlaceholderPrefix) && strings.HasSuffix(s, SecretPlaceholderSuffix)
}

// ExtractSecretKey extracts the dot-path key from a secret placeholder.
func ExtractSecretKey(placeholder string) string {
	if !IsSecretPlaceholder(placeholder) {
		return ""
	}
	return placeholder[len(SecretPlaceholderPrefix) : len(placeholder)-len(SecretPlaceholderSuffix)]
}
