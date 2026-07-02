package masterd

import (
	"context"
	"fmt"

	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/facts"
	"github.com/ptorbus/zester/pkg/settings"
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
	curveKey := ""
	if ck, ok := f["_curve_public_key"]; ok {
		curveKey = fmt.Sprintf("%v", ck)
	}
	if len(d.allSecrets) > 0 && curveKey != "" && d.topFile != nil {
		if !d.secretsLeader() {
			d.logger.Debug("skipping secrets publish; not facts-secrets lease holder", "peel", peelID)
			return
		}
		matcher := &settings.SimpleTargetMatcher{}
		refs := d.topFile.ResolveForPeel(peelID, map[string]any(f), matcher)
		peelSecrets := d.allSecrets.ForRefs(refs)
		if len(peelSecrets) > 0 {
			if err := d.publisher.PublishSecrets(ctx, peelID, peelSecrets, curveKey); err != nil {
				d.logger.Error("failed to publish secrets", "peel", peelID, "error", err)
			}
		}
	}
}
