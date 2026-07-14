package peeld

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nirnx/zester/internal/config"
	"github.com/nirnx/zester/pkg/proto"
)

// Startup-states retry pacing: startupRetryBase doubles per attempt up to
// startupRetryCap. Fields on Agent so tests can shrink them.
const (
	defaultStartupRetryBase = 5 * time.Second
	defaultStartupRetryCap  = time.Minute

	// defaultStartupSyncGrace bounds how long the startup run waits for the
	// FIRST state-file KV sync before proceeding with whatever local tree
	// exists (baked fallback or a previous cache). Online boots proceed the
	// moment the sync lands (typically seconds); a peel booting during a NATS
	// outage proceeds after the grace and enforces offline-first.
	defaultStartupSyncGrace = time.Minute

	// startupRetryWarnAfter escalates the per-attempt not-ready log from Info
	// to Warn: a peel that never becomes ready (states never published,
	// broken grants) must surface at Warn+ monitoring instead of Info-looping
	// forever in silence. The retry itself stays unbounded — it is what makes
	// fresh-peel autonomous convergence work.
	startupRetryWarnAfter = 10
)

// settingsResolveFailedPrefix prefixes dispatchStateModule's fail-closed
// settings error (exec.go). startupNotReady keys its retry classification on
// it, so the two sites MUST share the constant — rewording the message would
// otherwise silently turn a not-ready bootstrap response into a terminal
// answer and skip the boot-time apply (TestStartupNotReady pins this).
const settingsResolveFailedPrefix = "settings resolution failed: "

// startupStatesRequests validates the startup_states configuration and expands
// it into the exec requests to run once at boot. It is called EARLY in
// Agent.Run so a typo'd value fails startup loudly (the same convention as
// enroll_trust / log_level) instead of being discovered hours later when the
// silent boot-time apply never happened.
func startupStatesRequests(cfg *config.PeelConfig) ([]proto.ExecRequest, error) {
	switch cfg.StartupStates {
	case "":
		if len(cfg.StartupSLSList) > 0 {
			return nil, fmt.Errorf("startup_sls_list is set but startup_states is not \"sls\" (set startup_states: sls or remove the list)")
		}
		return nil, nil
	case "highstate":
		if len(cfg.StartupSLSList) > 0 {
			return nil, fmt.Errorf("startup_states \"highstate\" does not take startup_sls_list (use startup_states: sls to apply a list)")
		}
		return []proto.ExecRequest{{Module: "state.highstate"}}, nil
	case "sls":
		if len(cfg.StartupSLSList) == 0 {
			return nil, fmt.Errorf("startup_states \"sls\" requires startup_sls_list")
		}
		reqs := make([]proto.ExecRequest, 0, len(cfg.StartupSLSList))
		for _, ref := range cfg.StartupSLSList {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				return nil, fmt.Errorf("startup_sls_list contains an empty state ref")
			}
			reqs = append(reqs, proto.ExecRequest{
				Module: "state.apply",
				Args:   map[string]any{"state": ref},
			})
		}
		return reqs, nil
	default:
		return nil, fmt.Errorf("invalid startup_states %q: must be \"highstate\", \"sls\", or empty", cfg.StartupStates)
	}
}

// markStatesSynced signals (once) that a state-file KV sync has completed
// this process — the peel's local tree reflects published truth. Fired from
// the connected phase's initial explicit Sync (an empty bucket is a clean,
// current no-op) and from the cache's OnSynced hook (covers an initial-sync
// failure healed later by the watch loop). Gates the startup-states run.
func (a *Agent) markStatesSynced() {
	a.statesSyncedOnce.Do(func() { close(a.statesSynced) })
}

// startupNotReady reports whether an execution response failed because the
// peel's state infrastructure is still coming up — the states engine has no
// directory yet (first boot, before the KV state-file sync lands) or settings
// have never resolved (no snapshot, master not yet reachable). Those are
// retried; anything else is an ANSWER (compile errors, no top.zy match,
// per-state failures) and is reported once, never retried.
func startupNotReady(resp proto.ExecResponse) bool {
	return resp.Error == statesEngineUnavailableMsg ||
		strings.HasPrefix(resp.Error, settingsResolveFailedPrefix)
}

// runStartupStates runs the configured startup states ONCE per process start,
// sequentially, through the same serialized execModule path as remote and
// scheduled executions (execMu, beacon busy-skip). Not-ready responses retry
// with capped exponential backoff — the primary use case is a freshly
// provisioned peel converging autonomously right after enrollment, before any
// operator dispatch. Returns the final response per request (consumed by
// tests; the production caller runs it in a goroutine and relies on the logs).
func (a *Agent) runStartupStates(ctx context.Context, reqs []proto.ExecRequest) []proto.ExecResponse {
	logger := a.logger
	out := make([]proto.ExecResponse, 0, len(reqs))

	// Wait (bounded) for the first state-file sync before the first attempt.
	// With a populated BAKED fallback tree the states engine builds
	// immediately, and compiling against it pre-sync would either
	// terminal-fail a ref that exists only in the not-yet-synced KV tree or
	// silently apply a stale baked copy (the one-shot then never re-applies
	// the fresh tree). Online boots proceed the moment the sync lands;
	// offline boots proceed with the local tree after the grace —
	// offline-first enforcement is preserved.
	select {
	case <-a.statesSynced:
	case <-time.After(a.startupSyncGrace):
		logger.Info("startup states: no state-file sync yet, proceeding with the local tree",
			"waited", a.startupSyncGrace)
	case <-ctx.Done():
		return out
	}
	for _, req := range reqs {
		ref, _ := req.Args["state"].(string)
		delay := a.startupRetryBase
		attempt := 0
		for {
			resp, err := a.execModule(ctx, req)
			if err != nil {
				// execModule reports execution failures inside the response;
				// a non-nil error is internal (context death on shutdown).
				logger.Warn("startup states: execution aborted",
					"module", req.Module, "state", ref, "error", err)
				return out
			}
			if startupNotReady(resp) {
				attempt++
				// Escalate a persistent not-ready loop to Warn: after this
				// many attempts the peel is not "still booting", something is
				// wrong upstream (states never published, broken grants) and
				// Info-only logging would hide it from monitoring forever.
				logFn := logger.Info
				if attempt >= startupRetryWarnAfter {
					logFn = logger.Warn
				}
				logFn("startup states: peel not ready yet, will retry",
					"module", req.Module, "state", ref, "reason", resp.Error,
					"attempt", attempt, "retry_in", delay)
				select {
				case <-ctx.Done():
					return out
				case <-time.After(delay):
				}
				if delay *= 2; delay > a.startupRetryCap {
					delay = a.startupRetryCap
				}
				continue
			}

			out = append(out, resp)
			changed, failed := 0, 0
			for _, sr := range resp.Results {
				if sr.Changed {
					changed++
				}
				if sr.Error != "" {
					failed++
				}
			}
			switch {
			case resp.Error != "":
				logger.Warn("startup states: run failed",
					"module", req.Module, "state", ref, "error", resp.Error)
			case !resp.Success:
				logger.Warn("startup states: applied with failures",
					"module", req.Module, "state", ref,
					"states", len(resp.Results), "changed", changed, "failed", failed)
			default:
				logger.Info("startup states: applied",
					"module", req.Module, "state", ref,
					"states", len(resp.Results), "changed", changed)
			}
			break
		}
	}
	return out
}
