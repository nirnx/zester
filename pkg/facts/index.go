package facts

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Index is a radix-tree-style index that maps flattened fact key-value pairs
// to peel IDs for fast targeting. The master side builds and queries this index.
//
// Keys are stored as "fact.key\x00value" -> set of peel IDs.
// This enables glob matching like "G@os.name:linux" or "G@network.hostname:web-*".
type Index struct {
	mu      sync.RWMutex
	entries map[string]map[string]struct{} // "key\x00value" -> set{peelID}
	peels   map[string]map[string]string   // peelID -> flattened facts snapshot
	raw     map[string]map[string]any      // peelID -> nested facts snapshot

	// seeded flips once the initial KV replay has been fully applied
	// (WatchIntoIndex marks it at the end-of-replay sentinel).
	seeded atomic.Bool
}

const keySep = "\x00"

// NewIndex creates an empty facts index.
func NewIndex() *Index {
	return &Index{
		entries: make(map[string]map[string]struct{}),
		peels:   make(map[string]map[string]string),
		raw:     make(map[string]map[string]any),
	}
}

// Update indexes the facts for a peel, removing any stale entries first.
// The facts map is retained by reference as the peel's nested snapshot
// (see RawFacts); callers must not mutate it after passing it in.
func (idx *Index) Update(peelID string, facts Facts) {
	flat := flattenFacts(map[string]any(facts), "")

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove old entries for this peel.
	if old, ok := idx.peels[peelID]; ok {
		for k, v := range old {
			compositeKey := k + keySep + v
			if set, exists := idx.entries[compositeKey]; exists {
				delete(set, peelID)
				if len(set) == 0 {
					delete(idx.entries, compositeKey)
				}
			}
		}
	}

	// Insert new entries.
	for k, v := range flat {
		compositeKey := k + keySep + v
		if idx.entries[compositeKey] == nil {
			idx.entries[compositeKey] = make(map[string]struct{})
		}
		idx.entries[compositeKey][peelID] = struct{}{}
	}

	idx.peels[peelID] = flat
	idx.raw[peelID] = map[string]any(facts)
}

// MarkSeeded records that the index has been seeded with a complete replay
// of the facts bucket (WatchIntoIndex calls it at the initial end-of-replay
// sentinel).
func (idx *Index) MarkSeeded() { idx.seeded.Store(true) }

// Seeded reports whether the initial replay completed. Consumers that must
// not act on a partially-populated index — the reactor's in-process target
// resolution, which would otherwise silently no-target boot-replay reactions
// — gate on it. The request/reply resolve service deliberately does not: its
// CLI/peel callers carry a facts-KV-scan fallback.
func (idx *Index) Seeded() bool { return idx.seeded.Load() }

// Remove deletes all index entries for a peel.
func (idx *Index) Remove(peelID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if old, ok := idx.peels[peelID]; ok {
		for k, v := range old {
			compositeKey := k + keySep + v
			if set, exists := idx.entries[compositeKey]; exists {
				delete(set, peelID)
				if len(set) == 0 {
					delete(idx.entries, compositeKey)
				}
			}
		}
		delete(idx.peels, peelID)
	}
	delete(idx.raw, peelID)
}

// Match returns all peel IDs where the given fact key matches the glob pattern
// against the fact value. This implements "G@key:pattern" targeting.
//
// Both key and pattern support glob wildcards:
//   - "*" matches any sequence of characters within a segment
//   - "?" matches any single character
//
// Examples:
//
//	Match("os.name", "linux")          -> exact match
//	Match("os.name", "*")             -> all peels with os.name set
//	Match("network.hostname", "web-*") -> hostname starts with "web-"
func (idx *Index) Match(key, pattern string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	result := make(map[string]struct{})

	for compositeKey, peelSet := range idx.entries {
		sepIdx := strings.Index(compositeKey, keySep)
		if sepIdx < 0 {
			continue
		}
		entryKey := compositeKey[:sepIdx]
		entryVal := compositeKey[sepIdx+1:]

		if entryKey != key {
			continue
		}

		if globMatch(pattern, entryVal) {
			for peelID := range peelSet {
				result[peelID] = struct{}{}
			}
		}
	}

	return sortedKeys(result)
}

// MatchKeyGlob returns all peel IDs where any fact key matching keyPattern
// has a value matching valuePattern. This supports "G@network.*:web-*".
func (idx *Index) MatchKeyGlob(keyPattern, valuePattern string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	result := make(map[string]struct{})

	for compositeKey, peelSet := range idx.entries {
		sepIdx := strings.Index(compositeKey, keySep)
		if sepIdx < 0 {
			continue
		}
		entryKey := compositeKey[:sepIdx]
		entryVal := compositeKey[sepIdx+1:]

		if !globMatch(keyPattern, entryKey) {
			continue
		}
		if !globMatch(valuePattern, entryVal) {
			continue
		}

		for peelID := range peelSet {
			result[peelID] = struct{}{}
		}
	}

	return sortedKeys(result)
}

// GetPeelFacts returns the flattened facts for a peel.
func (idx *Index) GetPeelFacts(peelID string) map[string]string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if facts, ok := idx.peels[peelID]; ok {
		cp := make(map[string]string, len(facts))
		for k, v := range facts {
			cp[k] = v
		}
		return cp
	}
	return nil
}

// RawFacts returns the nested (non-flattened) facts snapshot for a peel, or
// nil when the peel is not indexed. The returned map is shared with the
// index for efficiency: treat it as READ-ONLY. Updates replace (never mutate)
// the stored snapshot, so holders of a previously returned map are unaffected
// by concurrent Update/Remove calls.
func (idx *Index) RawFacts(peelID string) map[string]any {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.raw[peelID]
}

// AllRawFacts returns every indexed peel mapped to its nested facts snapshot.
// The outer map is a fresh copy; the inner maps are shared with the index and
// must be treated as READ-ONLY (see RawFacts).
func (idx *Index) AllRawFacts() map[string]map[string]any {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	cp := make(map[string]map[string]any, len(idx.raw))
	for id, f := range idx.raw {
		cp[id] = f
	}
	return cp
}

// PeelIDs returns all indexed peel IDs.
func (idx *Index) PeelIDs() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	ids := make([]string, 0, len(idx.peels))
	for id := range idx.peels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// globMatch matches a pattern against a string using glob-style wildcards.
// Supports "*" (match any sequence) and "?" (match single character).
func globMatch(pattern, str string) bool {
	return globMatchRec(pattern, str, 0, 0)
}

func globMatchRec(pattern, str string, pi, si int) bool {
	for pi < len(pattern) && si < len(str) {
		switch pattern[pi] {
		case '*':
			// Skip consecutive stars.
			for pi < len(pattern) && pattern[pi] == '*' {
				pi++
			}
			if pi == len(pattern) {
				return true
			}
			// Try matching the rest from every position.
			for si <= len(str) {
				if globMatchRec(pattern, str, pi, si) {
					return true
				}
				si++
			}
			return false
		case '?':
			pi++
			si++
		default:
			if pattern[pi] != str[si] {
				return false
			}
			pi++
			si++
		}
	}

	// Consume trailing stars.
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}

	return pi == len(pattern) && si == len(str)
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
