package statefiles

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// KeyManifest is the well-known KV key holding the published file-set
// manifest. The publisher writes it after all file keys and before bumping
// _revision, so a watcher that reacts to the revision bump always finds a
// manifest describing the complete batch. Like _revision, it is never
// written to peel disk caches and never deleted by the stale-key sweep.
const KeyManifest = "_manifest"

// ManifestFile describes one published state file: its KV key (forward-slash
// relative path) and the SHA-256 hex digest of its content.
type ManifestFile struct {
	Key    string `msgpack:"key"`
	SHA256 string `msgpack:"sha256"`
}

// Manifest is the authoritative description of the published file set.
// Entries are sorted by Key for deterministic encoding. Peels fetch exactly
// this file set (verifying digests) and prune local files not listed; a
// bucket holding file keys without a manifest fails the sync (torn or
// tampered publish).
type Manifest struct {
	Files []ManifestFile `msgpack:"files"`
}

// Keys returns the set of file keys in the manifest.
func (m Manifest) Keys() map[string]struct{} {
	set := make(map[string]struct{}, len(m.Files))
	for _, f := range m.Files {
		set[f.Key] = struct{}{}
	}
	return set
}

// BuildManifest constructs a Manifest from a published file map, sorted by
// key, with an unkeyed SHA-256 per file.
func BuildManifest(files map[string][]byte) Manifest {
	return BuildManifestWith(files, HashFile)
}

// BuildManifestWith builds a manifest hashing each file with hashFn. The
// default (BuildManifest → HashFile) is an unkeyed SHA-256, fine for the
// non-secret state/reactor trees. The sealed master-settings replica passes
// a KEYED hash (HMAC under the account seed): the manifest then verifies
// only for account-key holders (all masters), closing the offline
// brute-force oracle an unkeyed plaintext hash would hand any $KV.> reader
// over a secret-bearing settings file. A keyed hash is still deterministic
// (same key + same plaintext → same digest), so the publishers' byte-equal
// hash-gate is unaffected.
func BuildManifestWith(files map[string][]byte, hashFn func([]byte) string) Manifest {
	m := Manifest{Files: make([]ManifestFile, 0, len(files))}
	for key, data := range files {
		m.Files = append(m.Files, ManifestFile{Key: key, SHA256: hashFn(data)})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Key < m.Files[j].Key })
	return m
}

// HashFile returns the SHA-256 hex digest used in manifest entries.
func HashFile(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
