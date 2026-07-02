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

	"github.com/ptorbus/zester/pkg/bus"
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
