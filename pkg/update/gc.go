package update

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// GCResult summarizes one garbage-collection pass.
type GCResult struct {
	Deleted []string // "<component>/<goos>/<goarch>/<version>" object keys removed
	Orphans []string // manifest-less objects removed
	Skipped []string // expired versions kept because an active rollout references them
}

// BinaryGC deletes expired published versions. Expiry moved from the object
// store's bucket TTL to the manifest (per-version, promoted = never), so the
// bucket no longer ages anything out — this GC is the only reaper.
type BinaryGC struct {
	Manifests *ManifestStore
	Binaries  *BinaryStore
	Rollouts  *RolloutStore
	Logger    *slog.Logger
}

// Run performs one GC pass at the given time. Per version: skip promoted and
// unexpired; skip versions referenced by a non-terminal rollout (an in-flight
// prepare must be able to download); otherwise delete the OBJECT first, the
// MANIFEST last — a crash in between leaves a manifest whose object is gone
// (fetch fails loudly, next pass finishes the job) rather than an invisible
// orphan. Finally, objects with no manifest at all are swept once they are
// old enough that they cannot be a publish-in-progress (upload happens before
// the manifest write).
func (g *BinaryGC) Run(ctx context.Context, now time.Time) (GCResult, error) {
	var res GCResult
	log := g.Logger
	if log == nil {
		log = slog.Default()
	}

	manifests, err := g.Manifests.List(ctx)
	if err != nil {
		return res, err
	}

	activeVersions, err := g.activeRolloutVersions(ctx)
	if err != nil {
		// Refuse to reap anything when rollout state is unreadable: deleting
		// a binary an in-flight rollout needs is worse than keeping garbage.
		return res, err
	}

	live := make(map[string]bool, len(manifests))
	for _, m := range manifests {
		live[m.ObjectKey] = true
		if !m.Expired(now) {
			continue
		}
		if activeVersions[m.Component+"/"+m.Version] {
			res.Skipped = append(res.Skipped, m.ObjectKey)
			continue
		}
		if err := g.Binaries.Delete(ctx, m.ObjectKey); err != nil &&
			!strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "no message") {
			log.Warn("update gc: delete binary failed; keeping manifest for the next pass",
				"key", m.ObjectKey, "error", err)
			continue
		}
		if err := g.Manifests.Delete(ctx, m.Component, m.GOOS, m.GOARCH, m.Version); err != nil {
			log.Warn("update gc: delete manifest failed", "key", m.ManifestKey(), "error", err)
			continue
		}
		res.Deleted = append(res.Deleted, m.ObjectKey)
		log.Info("update gc: expired version removed",
			"component", m.Component, "version", m.Version, "expired_at", expiryString(m))
	}

	// Orphan sweep: objects without a manifest (e.g. a manifest deleted by
	// hand, or the object half of an interrupted unpublish). The age gate
	// keeps a publish-in-progress (object uploaded, manifest not yet
	// written) safe.
	infos, err := g.Binaries.List(ctx)
	if err != nil {
		return res, nil //nolint:nilerr // orphan sweep is best-effort; the primary pass succeeded
	}
	const orphanMinAge = time.Hour
	for _, info := range infos {
		if info == nil || live[info.Name] {
			continue
		}
		if now.Sub(info.ModTime) < orphanMinAge {
			continue
		}
		if err := g.Binaries.Delete(ctx, info.Name); err != nil {
			log.Warn("update gc: delete orphan object failed", "key", info.Name, "error", err)
			continue
		}
		res.Orphans = append(res.Orphans, info.Name)
		log.Info("update gc: manifest-less object removed", "key", info.Name)
	}

	return res, nil
}

// activeRolloutVersions returns "<component>/<version>" keys for every
// non-terminal rollout.
func (g *BinaryGC) activeRolloutVersions(ctx context.Context) (map[string]bool, error) {
	states, err := g.Rollouts.List(ctx)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for _, s := range states {
		if s == nil || RolloutTerminal(s.State) {
			continue
		}
		active[s.Config.Component+"/"+s.Config.Version] = true
	}
	return active, nil
}

func expiryString(m *Manifest) string {
	exp, expires := m.Expiry()
	if !expires {
		return "never"
	}
	return exp.UTC().Format(time.RFC3339)
}
