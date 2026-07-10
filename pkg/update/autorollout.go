package update

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/bus"
)

// AutoSwitchKey is the well-known manifest-bucket key holding the fleet-wide
// auto-rollout switch. Underscore-prefixed meta keys are excluded from
// manifest listings.
const AutoSwitchKey = "_auto-rollout"

// AutoSwitch is the fleet-wide runtime toggle for promoted-version
// auto-rollouts, stored in the update-manifests bucket and flipped with
// `zester update auto on|off` — no master restart. ABSENT MEANS ENABLED:
// auto-rollout is on by default, which is safe because it only ever acts on
// explicitly PROMOTED versions (a fleet with nothing promoted never
// auto-rolls anything).
type AutoSwitch struct {
	Enabled       bool   `msgpack:"enabled"`
	UpdatedBy     string `msgpack:"updated_by,omitempty"`
	UpdatedAtUnix int64  `msgpack:"updated_at,omitempty"`
}

// LoadAutoSwitch reads the fleet switch; a missing key is the enabled
// default.
func LoadAutoSwitch(ctx context.Context, kv bus.KV) (AutoSwitch, error) {
	var s AutoSwitch
	if err := bus.KVGet(ctx, kv, AutoSwitchKey, &s); err != nil {
		if errors.Is(err, bus.ErrKeyNotFound) {
			return AutoSwitch{Enabled: true}, nil
		}
		return AutoSwitch{}, fmt.Errorf("update: load auto-rollout switch: %w", err)
	}
	return s, nil
}

// SaveAutoSwitch writes the fleet switch.
func SaveAutoSwitch(ctx context.Context, kv bus.KV, s AutoSwitch) error {
	if _, err := bus.KVPut(ctx, kv, AutoSwitchKey, &s); err != nil {
		return fmt.Errorf("update: save auto-rollout switch: %w", err)
	}
	return nil
}

// AutoRolloutPlan is a decided-but-not-started auto-rollout.
type AutoRolloutPlan struct {
	Component string
	Version   string
	// Generation numbers this convergence run (see AutoRolloutID).
	Generation int
	// NodeIDs are the nodes actually below the promoted version (nodes
	// already at or above it are never touched — auto-rollout must not
	// restart current nodes or downgrade ahead-of-promoted ones).
	NodeIDs []string
}

// rolloutIDSanitize maps a version string into the id-safe charset.
var rolloutIDSanitize = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// autoRolloutBase is the deterministic id prefix for a component+version.
func autoRolloutBase(component, version string) string {
	return "rol-auto-" + component + "-" + rolloutIDSanitize.ReplaceAllString(version, "-")
}

// AutoRolloutID is the deterministic rollout id for the Nth convergence run
// of a component+version ("rol-auto-peel-0-5-0-r1"). Determinism is the
// same-instant dedup: two masters racing the same tick compute the same
// generation from the same store listing, so the second StartRollout
// CAS-conflicts on Create instead of double-rolling; skewed ticks are
// covered by the one-active-rollout-per-component guard. Generations exist
// because a COMPLETED run must not block convergence forever: a node that
// was offline during generation N (its status TTL'd out, so it was never in
// the plan) or enrolled later still lags — generation N+1 picks it up.
func AutoRolloutID(component, version string, generation int) string {
	return fmt.Sprintf("%s-r%d", autoRolloutBase(component, version), generation)
}

// PickAutoRollout decides whether a promoted version warrants an
// auto-rollout for the component. Pure function — fed by the master's tick.
// Returns nil when there is nothing to do:
//   - no promoted manifests for the component;
//   - a non-terminal rollout for the component already exists (one at a
//     time, whatever its origin);
//   - a rollout for this exact target already ran (terminal record under the
//     deterministic id — covers "it completed/aborted, do not restart it");
//   - every live node is already at or above the latest promoted version.
//
// Degraded nodes are excluded here for clarity of the plan's node list; the
// controller excludes them again on start.
func PickAutoRollout(component string, manifests []*Manifest, statuses []*NodeStatus, rollouts []*RolloutState) *AutoRolloutPlan {
	var target *Manifest
	for _, m := range manifests {
		if m == nil || !m.Promoted || m.Component != component {
			continue
		}
		if target == nil || CompareVersions(m.Version, target.Version) > 0 {
			target = m
		}
	}
	if target == nil {
		return nil
	}

	// One rollout per component at a time; an operator ABORT of any prior
	// auto-rollout generation for this version blocks it permanently (until
	// a newer version is promoted) — an abort is a human "stop", never to be
	// auto-retried. COMPLETED generations do NOT block: nodes that were
	// offline during (or enrolled after) a completed run still lag and get a
	// fresh generation.
	base := autoRolloutBase(component, target.Version)
	generation := 1
	for _, r := range rollouts {
		if r == nil {
			continue
		}
		if r.Config.Component == component && !RolloutTerminal(r.State) && !r.Config.DryRun {
			return nil // something is already rolling this component
		}
		if strings.HasPrefix(r.ID, base+"-r") || r.ID == base {
			if r.State == RolloutAborted {
				return nil // operator aborted this version's auto-rollout
			}
			generation++
		}
	}

	var nodes []string
	for _, s := range statuses {
		if s == nil || s.Degraded {
			continue
		}
		if s.Version != "" && CompareVersions(s.Version, target.Version) >= 0 {
			continue // already there (or ahead — never downgrade)
		}
		nodes = append(nodes, s.ID)
	}
	if len(nodes) == 0 {
		return nil
	}

	return &AutoRolloutPlan{Component: component, Version: target.Version, Generation: generation, NodeIDs: nodes}
}

// AutoRolloutConfig builds the RolloutConfig for a plan with the given
// batch/soak tuning, stamping the deterministic id.
func (p *AutoRolloutPlan) RolloutConfig(batchSize int, soak time.Duration, maxFailed int) RolloutConfig {
	return RolloutConfig{
		Version:   p.Version,
		Component: p.Component,
		Target:    "*",
		BatchSize: batchSize,
		SoakTime:  soak,
		MaxFailed: maxFailed,
		RolloutID: AutoRolloutID(p.Component, p.Version, p.Generation),
	}
}
