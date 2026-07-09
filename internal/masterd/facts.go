package masterd

import (
	"context"
	"fmt"

	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/facts"
	"github.com/nirnx/zester/pkg/settings"
)

// startFactsWatcher watches for peel facts and reacts to each update via
// handleFactsUpdate. Both legacy (master-compiled) and new (peel-side
// rendering secrets) paths run. The returned cancel function stops the
// watcher.
func (d *Daemon) startFactsWatcher(ctx context.Context) (context.CancelFunc, error) {
	d.logger.Info("starting facts watcher")
	cancelWatch, err := facts.Watch(ctx, d.client.JetStream(), d.handleFactsUpdate, d.logger)
	if err != nil {
		return nil, fmt.Errorf("start facts watcher: %w", err)
	}
	return cancelWatch, nil
}

// handleFactsUpdate is the facts watcher callback (formerly an inline
// closure), running on the watcher goroutine for every facts update. It
// transitions enrollments from issued to active on first fact publish and
// publishes per-peel secrets for peel-side rendering. It reads
// d.enrollStore, d.allSecrets, d.topFile, and d.publisher — all assigned
// before the watcher starts and stable afterwards — and uses d.runCtx as the
// operation context, exactly as the closure captured run()'s ctx.
func (d *Daemon) handleFactsUpdate(peelID string, f facts.Facts) {
	ctx := d.runCtx
	d.reg.FactsSyncTotal.Inc()
	d.logger.Info("peel facts received", "peel", peelID)

	// Transition enrollment from issued → active on first fact publish.
	if rec, err := d.enrollStore.FindByPeelID(ctx, peelID); err != nil {
		d.logger.Warn("enrollment lookup failed", "peel", peelID, "error", err)
	} else if rec != nil && rec.State == enroll.StateIssued {
		if _, err := d.enrollStore.MarkActive(ctx, rec.ID); err != nil {
			d.logger.Warn("failed to mark enrollment active", "peel", peelID, "error", err)
		}
	}

	// Publish only secrets from files that target this peel. Gated behind
	// the "facts-secrets" lease (roadmap B6) so exactly one master
	// re-encrypts per facts update instead of every master doing N× the
	// work; PublishSecrets itself stays hash-gated, so the brief
	// advisory-lease overlap at worst duplicates one publish.
	// Snapshot the secret-encryption inputs: publishSettingsFiles refreshes
	// them when the on-disk tree changes (ticker / watcher / fileserver
	// update), and this callback runs on the facts watcher goroutine.
	d.settingsMu.RLock()
	allSecrets, topFile := d.allSecrets, d.topFile
	d.settingsMu.RUnlock()
	if len(allSecrets) > 0 && topFile != nil {
		if !d.secretsLeader() {
			d.logger.Debug("skipping secrets publish; not facts-secrets lease holder", "peel", peelID)
			return
		}
		d.publishSecretsForPeel(ctx, peelID, map[string]any(f), allSecrets, topFile)
	}
}

// publishSecretsForPeel encrypts and publishes one peel's targeted secrets
// (hash-gated inside PublishSecrets). Shared by the facts watcher callback
// and the live-settings fleet republish.
func (d *Daemon) publishSecretsForPeel(ctx context.Context, peelID string, f map[string]any, allSecrets settings.FileSecrets, topFile *settings.TopFile) {
	curveKey := ""
	if ck, ok := f["_curve_public_key"]; ok {
		curveKey = fmt.Sprintf("%v", ck)
	}
	if curveKey == "" {
		return
	}
	matcher := &settings.SimpleTargetMatcher{}
	refs := topFile.ResolveForPeel(peelID, f, matcher)
	peelSecrets := allSecrets.ForRefs(refs)
	if len(peelSecrets) > 0 {
		if err := d.publisher.PublishSecrets(ctx, peelID, peelSecrets, curveKey); err != nil {
			d.logger.Error("failed to publish secrets", "peel", peelID, "error", err)
		}
	}
}

// republishSecretsToFleet re-encrypts targeted secrets for EVERY indexed
// peel. Called when a live settings publish changed the extracted secrets:
// stable peels hash-skip their periodic facts publishes, so without this a
// rotated !encrypted VALUE (which is even hash-gate-invisible — the
// sanitized placeholder derives from the key alone) would reach a peel only
// on its next facts change. Before live publishing, rotation implied a
// master restart, whose facts-watch replay re-encrypted everyone; this is
// that replay's equivalent. Per-peel PublishSecrets stays hash-gated, so
// unchanged peels cost one fingerprint check.
func (d *Daemon) republishSecretsToFleet(ctx context.Context) {
	if !d.secretsLeader() {
		return
	}
	idx := d.factsIndex.Load()
	if idx == nil {
		return
	}
	d.settingsMu.RLock()
	allSecrets, topFile := d.allSecrets, d.topFile
	d.settingsMu.RUnlock()
	if len(allSecrets) == 0 || topFile == nil {
		return
	}
	all := idx.AllRawFacts()
	d.logger.Info("settings secrets changed; re-encrypting for the fleet", "peels", len(all))
	for peelID, f := range all {
		if ctx.Err() != nil {
			return
		}
		d.publishSecretsForPeel(ctx, peelID, f, allSecrets, topFile)
	}
}
