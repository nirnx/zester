package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nirnx/zester/pkg/bus"
)

// Manifest describes a published binary for a specific component and platform.
type Manifest struct {
	Version   string    `msgpack:"version"`
	Component string    `msgpack:"component"` // "peel" or "master"
	GOOS      string    `msgpack:"goos"`
	GOARCH    string    `msgpack:"goarch"`
	SHA256    string    `msgpack:"sha256"`
	Size      int64     `msgpack:"size"`
	ObjectKey string    `msgpack:"object_key"`
	Published time.Time `msgpack:"published"`
	Publisher string    `msgpack:"publisher"`
	// MinProtocol is the minimum NodeStatus.Protocol a target node must
	// report to receive this binary; the rollout controller refuses to
	// start a rollout that includes any node below it. Zero disables the
	// check entirely. Nodes reporting 0 (legacy watchdogs) or with no
	// status record fail the check only when MinProtocol > 0. Additive:
	// manifests published by older CLIs decode with MinProtocol 0.
	MinProtocol int `msgpack:"min_protocol,omitempty"`

	// Promoted marks this version as a promoted release: it never expires
	// (the master GC skips it regardless of ExpiresAtUnix) and it is a
	// candidate for auto-rollout on masters with update.auto_rollout
	// enabled. Additive: pre-promotion manifests decode as not promoted.
	Promoted bool `msgpack:"promoted,omitempty"`

	// ExpiresAtUnix is when the master GC may delete this version (unix
	// seconds). Sentinels: 0 = legacy manifest published before per-version
	// TTLs — treated as Published + DefaultBinaryTTL (the old bucket-level
	// TTL, so upgrades change nothing); TTLNever (-1) = never expires.
	// Ignored while Promoted. Additive.
	ExpiresAtUnix int64 `msgpack:"expires_at,omitempty"`
}

// DefaultBinaryTTL is the default lifetime of a published (non-promoted)
// binary — the same 30 days the object-store bucket TTL used to enforce
// before expiry moved to the manifest-driven master GC.
const DefaultBinaryTTL = 30 * 24 * time.Hour

// TTLNever is the ExpiresAtUnix sentinel for "never expires".
const TTLNever int64 = -1

// Expiry returns when this version expires, and false when it never does
// (promoted, or explicit TTLNever).
func (m *Manifest) Expiry() (time.Time, bool) {
	if m.Promoted || m.ExpiresAtUnix == TTLNever {
		return time.Time{}, false
	}
	if m.ExpiresAtUnix == 0 {
		return m.Published.Add(DefaultBinaryTTL), true // legacy fallback
	}
	return time.Unix(m.ExpiresAtUnix, 0), true
}

// Expired reports whether the version is eligible for GC at the given time.
func (m *Manifest) Expired(now time.Time) bool {
	exp, expires := m.Expiry()
	return expires && now.After(exp)
}

// ManifestKey returns the KV key for this manifest.
// Format: <component>.<goos>.<goarch>.<version>
func (m *Manifest) ManifestKey() string {
	return fmt.Sprintf("%s.%s.%s.%s", m.Component, m.GOOS, m.GOARCH, m.Version)
}

// ObjectKeyFor returns the Object Store key for a binary.
// Format: <component>/<goos>/<goarch>/<version>
func ObjectKeyFor(component, goos, goarch, version string) string {
	return fmt.Sprintf("%s/%s/%s/%s", component, goos, goarch, version)
}

// ManifestStore provides CRUD operations for update manifests via NATS KV.
type ManifestStore struct {
	kv bus.KV
}

// NewManifestStore wraps a KV bucket for manifest storage.
func NewManifestStore(kv bus.KV) *ManifestStore {
	return &ManifestStore{kv: kv}
}

// Publish stores a manifest in KV.
func (s *ManifestStore) Publish(ctx context.Context, m *Manifest) error {
	_, err := bus.KVPut(ctx, s.kv, m.ManifestKey(), m)
	if err != nil {
		return fmt.Errorf("update: publish manifest: %w", err)
	}
	return nil
}

// Get retrieves a manifest by its component, platform, and version.
func (s *ManifestStore) Get(ctx context.Context, component, goos, goarch, version string) (*Manifest, error) {
	key := fmt.Sprintf("%s.%s.%s.%s", component, goos, goarch, version)
	var m Manifest
	if err := bus.KVGet(ctx, s.kv, key, &m); err != nil {
		return nil, fmt.Errorf("update: get manifest %q: %w", key, err)
	}
	return &m, nil
}

// ListByComponent returns all manifests for a given component.
func (s *ManifestStore) ListByComponent(ctx context.Context, component string) ([]*Manifest, error) {
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		if err == bus.ErrNoKeysFound {
			return nil, nil
		}
		return nil, fmt.Errorf("update: list manifest keys: %w", err)
	}

	var manifests []*Manifest
	prefix := component + "."
	for _, key := range keys {
		if strings.HasPrefix(key, "_") { // meta keys (_auto-rollout) are not manifests
			continue
		}
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		var m Manifest
		if err := bus.KVGet(ctx, s.kv, key, &m); err != nil {
			continue // skip unreadable entries
		}
		manifests = append(manifests, &m)
	}
	return manifests, nil
}

// BinaryStore provides upload/download operations for binaries via NATS Object Store.
type BinaryStore struct {
	os jetstream.ObjectStore
}

// NewBinaryStore wraps a NATS Object Store for binary storage.
func NewBinaryStore(os jetstream.ObjectStore) *BinaryStore {
	return &BinaryStore{os: os}
}

// Upload stores a binary in the Object Store and returns its SHA-256 digest.
func (s *BinaryStore) Upload(ctx context.Context, key string, data []byte) (string, error) {
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])

	_, err := s.os.Put(ctx, jetstream.ObjectMeta{Name: key}, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("update: upload binary %q: %w", key, err)
	}
	return digest, nil
}

// Download retrieves a binary from the Object Store and verifies its SHA-256 digest.
func (s *BinaryStore) Download(ctx context.Context, key string, expectedHash string) ([]byte, error) {
	result, err := s.os.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("update: download binary %q: %w", key, err)
	}
	defer result.Close()

	data, err := io.ReadAll(result)
	if err != nil {
		return nil, fmt.Errorf("update: read binary %q: %w", key, err)
	}

	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	if digest != expectedHash {
		return nil, fmt.Errorf("update: binary %q hash mismatch: got %s, want %s", key, digest, expectedHash)
	}

	return data, nil
}

// Info retrieves metadata about a stored binary.
func (s *BinaryStore) Info(ctx context.Context, key string) (*jetstream.ObjectInfo, error) {
	info, err := s.os.GetInfo(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("update: get binary info %q: %w", key, err)
	}
	return info, nil
}

// Delete removes a binary from the Object Store.
func (s *BinaryStore) Delete(ctx context.Context, key string) error {
	if err := s.os.Delete(ctx, key); err != nil {
		return fmt.Errorf("update: delete binary %q: %w", key, err)
	}
	return nil
}

// List returns metadata for all binaries in the Object Store.
func (s *BinaryStore) List(ctx context.Context) ([]*jetstream.ObjectInfo, error) {
	infos, err := s.os.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("update: list binaries: %w", err)
	}
	return infos, nil
}

// List returns every manifest in the store, across components and platforms.
func (s *ManifestStore) List(ctx context.Context) ([]*Manifest, error) {
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		if err == bus.ErrNoKeysFound {
			return nil, nil
		}
		return nil, fmt.Errorf("update: list manifest keys: %w", err)
	}
	var manifests []*Manifest
	for _, key := range keys {
		if strings.HasPrefix(key, "_") { // meta keys (_auto-rollout) are not manifests
			continue
		}
		var m Manifest
		if err := bus.KVGet(ctx, s.kv, key, &m); err != nil {
			continue // skip unreadable entries
		}
		manifests = append(manifests, &m)
	}
	return manifests, nil
}

// Delete removes a manifest record.
func (s *ManifestStore) Delete(ctx context.Context, component, goos, goarch, version string) error {
	key := fmt.Sprintf("%s.%s.%s.%s", component, goos, goarch, version)
	if err := s.kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("update: delete manifest %q: %w", key, err)
	}
	return nil
}

// Mutate loads the manifest for the given coordinates, applies fn, and saves
// it back. Used by promote/demote/set-ttl.
func (s *ManifestStore) Mutate(ctx context.Context, component, goos, goarch, version string, fn func(*Manifest)) (*Manifest, error) {
	m, err := s.Get(ctx, component, goos, goarch, version)
	if err != nil {
		return nil, err
	}
	fn(m)
	if err := s.Publish(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}
