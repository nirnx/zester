package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/nirnx/zester/pkg/bus"
)

// Rollout states.
const (
	RolloutCreated   = "created"
	RolloutPreparing = "preparing"
	RolloutRolling   = "rolling"
	RolloutSoaking   = "soaking"
	RolloutCompleted = "completed"
	RolloutAborting  = "aborting"
	RolloutAborted   = "aborted"
)

// Rollout driver tuning.
const (
	// DefaultRolloutStaleAfter is how old a rollout's DriverHeartbeat must be
	// before ResumeOrphaned considers the rollout orphaned and adopts it.
	DefaultRolloutStaleAfter = 60 * time.Second

	// DefaultRolloutResumeInterval is how often RunResumeLoop scans the
	// rollout store for orphaned rollouts.
	DefaultRolloutResumeInterval = 60 * time.Second

	// rolloutHeartbeatInterval is how often the driving goroutine refreshes
	// the persisted DriverHeartbeat while a rollout is in flight (during
	// batch commands, soak waits, and batch pauses alike).
	rolloutHeartbeatInterval = 10 * time.Second

	// rolloutSaveRetries and rolloutSaveRetryBackoff govern how rollout
	// state persistence failures are retried before the rollout is aborted.
	rolloutSaveRetries      = 3
	rolloutSaveRetryBackoff = 200 * time.Millisecond
)

// Sentinel errors classified by the driver's persistence path. Unexported:
// they only steer the driver loop, never escape the package API.
var (
	errRolloutAbortedExternally = errors.New("update: rollout aborted externally")
	errRolloutSuperseded        = errors.New("update: rollout adopted by another driver")
)

// RolloutConfig holds the configuration for a fleet rollout.
type RolloutConfig struct {
	Version      string        `msgpack:"version"`
	Component    string        `msgpack:"component"`
	Target       string        `msgpack:"target"`        // target expression (default: "*")
	BatchSize    int           `msgpack:"batch_size"`    // nodes per batch (default: 1)
	BatchPercent int           `msgpack:"batch_percent"` // alternative: percentage per batch
	SoakTime     time.Duration `msgpack:"soak_time"`     // per-node soak (default: 60s)
	BatchPause   time.Duration `msgpack:"batch_pause"`   // pause between batches (default: 30s)
	MaxFailed    int           `msgpack:"max_failed"`    // abort after N failures (default: 1)
	DryRun       bool          `msgpack:"dry_run"`

	// RolloutID, when set, overrides the generated KSUID id. Auto-rollout
	// uses deterministic ids (rol-auto-<component>-<version>) so a
	// concurrently-triggered duplicate CAS-conflicts on Create instead of
	// double-rolling the fleet. Additive.
	RolloutID string `msgpack:"rollout_id,omitempty"`
}

// RolloutStartRequest is sent by the CLI to the master to initiate a rollout.
type RolloutStartRequest struct {
	Config RolloutConfig `msgpack:"config"`
}

// RolloutStartResponse is returned by the master after creating a rollout.
type RolloutStartResponse struct {
	ID      string        `msgpack:"id,omitempty"`
	Batches int           `msgpack:"batches"`
	Nodes   int           `msgpack:"nodes"`
	Error   string        `msgpack:"error,omitempty"`
	State   *RolloutState `msgpack:"state,omitempty"` // included for dry-run
}

// RolloutAbortRequest is sent by the CLI to the master to abort a rollout.
type RolloutAbortRequest struct {
	RolloutID string `msgpack:"rollout_id"`
}

// RolloutAbortResponse is returned by the master after aborting.
type RolloutAbortResponse struct {
	Error string `msgpack:"error,omitempty"`
}

// NodeResult records the outcome of an update for a single node.
type NodeResult struct {
	ID      string    `msgpack:"id"`
	Status  string    `msgpack:"status"` // prepared, applied, confirmed, failed, rolled_back
	Error   string    `msgpack:"error,omitempty"`
	Updated time.Time `msgpack:"updated"`
}

// RolloutState is the persisted state of an active or completed rollout.
type RolloutState struct {
	ID           string                 `msgpack:"id"`
	Config       RolloutConfig          `msgpack:"config"`
	State        string                 `msgpack:"state"`
	Batches      [][]string             `msgpack:"batches"`
	NodeResults  map[string]*NodeResult `msgpack:"node_results"`
	CurrentBatch int                    `msgpack:"current_batch"`
	StartedAt    time.Time              `msgpack:"started_at"`
	FinishedAt   time.Time              `msgpack:"finished_at,omitempty"`
	FailedCount  int                    `msgpack:"failed_count"`
	Error        string                 `msgpack:"error,omitempty"`

	// DriverID identifies the controller instance currently driving this
	// rollout; DriverHeartbeat is refreshed by that driver on every batch
	// step and at least every rolloutHeartbeatInterval while waiting or
	// soaking. A non-terminal rollout whose heartbeat is older than the
	// staleness window is considered orphaned and eligible for adoption by
	// ResumeOrphaned. Both fields are msgpack-additive: records written by
	// older masters decode with zero values (a zero heartbeat reads as
	// maximally stale, so legacy in-flight records are adopted immediately).
	DriverID        string    `msgpack:"driver_id,omitempty"`
	DriverHeartbeat time.Time `msgpack:"driver_heartbeat,omitempty"`

	// Revision is the KV revision for CAS updates. Not serialized — set from
	// KV entry metadata on Get, updated on Save. Zero means first write (Create).
	Revision uint64 `msgpack:"-"`
}

// snapshot returns a copy safe to hand to callers while the driver goroutine
// keeps mutating the original. Batches are shared (never mutated after
// creation); NodeResults entries are deep-copied.
func (s *RolloutState) snapshot() *RolloutState {
	cp := *s
	cp.NodeResults = make(map[string]*NodeResult, len(s.NodeResults))
	for k, v := range s.NodeResults {
		nr := *v
		cp.NodeResults[k] = &nr
	}
	return &cp
}

// RequestFunc sends an update command to a node and returns the response.
// The master wires this using the raw NATS connection's Request method.
type RequestFunc func(ctx context.Context, subject string, req *UpdateCommand) (*UpdateResponse, error)

// RolloutStore persists RolloutState to a NATS KV bucket.
type RolloutStore struct {
	kv bus.KV
}

// NewRolloutStore wraps a KV bucket for rollout state persistence.
func NewRolloutStore(kv bus.KV) *RolloutStore {
	return &RolloutStore{kv: kv}
}

// Save encodes and writes the rollout state to KV with CAS protection.
// First write uses Create (revision=0); subsequent writes use Update with
// the stored revision to prevent concurrent overwrites.
func (s *RolloutStore) Save(ctx context.Context, r *RolloutState) error {
	data, err := bus.Encode(r)
	if err != nil {
		return fmt.Errorf("update: rollout encode %s: %w", r.ID, err)
	}

	var rev uint64
	if r.Revision == 0 {
		rev, err = s.kv.Create(ctx, r.ID, data)
	} else {
		rev, err = s.kv.Update(ctx, r.ID, data, r.Revision)
	}
	if err != nil {
		return fmt.Errorf("update: rollout save %s: %w", r.ID, err)
	}
	r.Revision = rev
	return nil
}

// Get retrieves and decodes a rollout state by ID, including its KV revision.
func (s *RolloutStore) Get(ctx context.Context, id string) (*RolloutState, error) {
	entry, err := s.kv.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("update: rollout get %s: %w", id, err)
	}
	var r RolloutState
	if err := bus.Decode(entry.Value(), &r); err != nil {
		return nil, fmt.Errorf("update: rollout decode %s: %w", id, err)
	}
	r.Revision = entry.Revision()
	return &r, nil
}

// List returns all rollout states stored in the KV bucket.
func (s *RolloutStore) List(ctx context.Context) ([]*RolloutState, error) {
	keys, err := s.kv.Keys(ctx)
	if err != nil {
		if err == bus.ErrNoKeysFound {
			return nil, nil
		}
		return nil, fmt.Errorf("update: rollout list keys: %w", err)
	}

	var results []*RolloutState
	for _, key := range keys {
		entry, err := s.kv.Get(ctx, key)
		if err != nil {
			continue
		}
		var r RolloutState
		if err := bus.Decode(entry.Value(), &r); err != nil {
			continue
		}
		r.Revision = entry.Revision()
		results = append(results, &r)
	}
	return results, nil
}

// RolloutController orchestrates fleet-wide updates on the master.
type RolloutController struct {
	// DriverID identifies this controller instance in persisted rollout
	// state (RolloutState.DriverID). NewRolloutController generates a
	// default; set it to the master ID before starting or resuming rollouts
	// for meaningful status output. Must not change while rollouts run.
	DriverID string

	// StaleAfter is the DriverHeartbeat staleness window after which
	// ResumeOrphaned adopts a rollout. Zero means DefaultRolloutStaleAfter.
	// Set before RunResumeLoop/ResumeOrphaned; not safe to change after.
	StaleAfter time.Duration

	// ResumeInterval is the RunResumeLoop scan period. Zero means
	// DefaultRolloutResumeInterval. Set before RunResumeLoop.
	ResumeInterval time.Duration

	baseCtx  context.Context
	request  RequestFunc
	store    *RolloutStore
	manifest *ManifestStore
	statusKV bus.KV
	logger   *slog.Logger

	mu      sync.Mutex
	active  map[string]*RolloutState
	abortCh map[string]chan struct{}
}

// NewRolloutController creates a new rollout controller.
func NewRolloutController(baseCtx context.Context, reqFn RequestFunc, store *RolloutStore, manifest *ManifestStore, statusKV bus.KV, logger *slog.Logger) *RolloutController {
	if logger == nil {
		logger = slog.Default()
	}
	return &RolloutController{
		DriverID: "drv-" + ksuid.New().String(),
		baseCtx:  baseCtx,
		request:  reqFn,
		store:    store,
		manifest: manifest,
		statusKV: statusKV,
		logger:   logger,
		active:   make(map[string]*RolloutState),
		abortCh:  make(map[string]chan struct{}),
	}
}

func (r *RolloutController) driverID() string {
	if r.DriverID == "" {
		return "drv-unknown"
	}
	return r.DriverID
}

func (r *RolloutController) staleAfter() time.Duration {
	if r.StaleAfter > 0 {
		return r.StaleAfter
	}
	return DefaultRolloutStaleAfter
}

func (r *RolloutController) resumeInterval() time.Duration {
	if r.ResumeInterval > 0 {
		return r.ResumeInterval
	}
	return DefaultRolloutResumeInterval
}

// ActiveRollouts returns the IDs of rollouts this controller instance is
// currently driving, sorted.
func (r *RolloutController) ActiveRollouts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.active))
	for id := range r.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// StartRollout creates and launches a new fleet rollout.
//
// Target eligibility is checked against the update-status bucket before any
// state is persisted: degraded nodes are excluded (with an Info log listing
// them), and if the target manifest declares MinProtocol > 0, the rollout is
// refused outright when any remaining node reports a lower protocol.
func (r *RolloutController) StartRollout(ctx context.Context, config RolloutConfig, nodeIDs []string) (*RolloutState, error) {
	// Apply defaults.
	if config.Target == "" {
		config.Target = "*"
	}
	if config.BatchSize == 0 && config.BatchPercent == 0 {
		config.BatchSize = 1
	}
	if config.SoakTime == 0 {
		config.SoakTime = 60 * time.Second
	}
	if config.BatchPause == 0 {
		config.BatchPause = 30 * time.Second
	}
	if config.MaxFailed == 0 {
		config.MaxFailed = 1
	}

	eligible, err := r.filterTargets(ctx, config, nodeIDs)
	if err != nil {
		return nil, err
	}

	// Sort node IDs for deterministic batch assignment.
	sorted := make([]string, len(eligible))
	copy(sorted, eligible)
	sort.Strings(sorted)

	id := config.RolloutID
	if id == "" {
		id = "rol-" + ksuid.New().String()
	}

	state := &RolloutState{
		ID:          id,
		Config:      config,
		State:       RolloutCreated,
		Batches:     computeBatches(sorted, config.BatchSize, config.BatchPercent),
		NodeResults: make(map[string]*NodeResult),
		StartedAt:   time.Now(),
	}

	// state is not shared with any goroutine yet, so no lock is needed;
	// saveStateLocked stamps DriverID + DriverHeartbeat so a freshly created
	// rollout is never mistaken for an orphan.
	if err := r.saveStateLocked(ctx, state); err != nil {
		return nil, fmt.Errorf("update: start rollout: %w", err)
	}

	if config.DryRun {
		r.logger.Info("rollout dry-run complete", "id", id, "batches", len(state.Batches), "nodes", len(sorted))
		return state, nil
	}

	abort := make(chan struct{})

	r.mu.Lock()
	r.active[id] = state
	r.abortCh[id] = abort
	r.mu.Unlock()

	// Snapshot before launching the driver goroutine: the goroutine (and its
	// heartbeat loop) keep mutating state, so callers get a stable copy.
	snap := state.snapshot()

	go r.executeRollout(r.baseCtx, state, abort)

	return snap, nil
}

// filterTargets applies eligibility checks against the update-status bucket:
//
//   - Degraded nodes (NodeStatus.Degraded) are excluded from the rollout with
//     an Info log listing the exclusions. If exclusion empties a non-empty
//     target set, an error is returned.
//   - Min-protocol (finding 36 / B12): if the manifest for a node's platform
//     declares MinProtocol > 0 and the node reports Protocol < MinProtocol,
//     the rollout is refused with an error naming the incompatible nodes.
//     Nodes reporting Protocol 0 (legacy watchdogs that predate the field)
//     and nodes with no status record fail the check only when MinProtocol
//     is set (> 0); a manifest with MinProtocol 0 never refuses anyone.
//
// With no status KV wired, all checks are skipped (legacy behavior).
func (r *RolloutController) filterTargets(ctx context.Context, config RolloutConfig, nodeIDs []string) ([]string, error) {
	if r.statusKV == nil || len(nodeIDs) == 0 {
		return nodeIDs, nil
	}

	var (
		eligible     []string
		degraded     []string
		incompatible []string
	)
	manifestCache := map[string]*Manifest{}

	for _, nodeID := range nodeIDs {
		ns, err := GetNodeStatus(ctx, r.statusKV, config.Component, nodeID)
		if err != nil {
			ns = nil // no status record: unknown platform and protocol
		}

		if ns != nil && ns.Degraded {
			degraded = append(degraded, nodeID)
			continue
		}

		goos, goarch := "linux", "amd64"
		protocol := 0
		if ns != nil {
			goos, goarch = ns.GOOS, ns.GOARCH
			protocol = ns.Protocol
		}

		cacheKey := goos + "/" + goarch
		m, cached := manifestCache[cacheKey]
		if !cached {
			m, err = r.manifest.Get(ctx, config.Component, goos, goarch, config.Version)
			if err != nil {
				m = nil // manifest missing: the batch phase surfaces the error per node
			}
			manifestCache[cacheKey] = m
		}
		if m != nil && m.MinProtocol > 0 && protocol < m.MinProtocol {
			incompatible = append(incompatible, fmt.Sprintf("%s (protocol %d < %d)", nodeID, protocol, m.MinProtocol))
			continue
		}

		eligible = append(eligible, nodeID)
	}

	if len(incompatible) > 0 {
		return nil, fmt.Errorf("update: start rollout: manifest %s/%s requires a newer node protocol; incompatible nodes: %s",
			config.Component, config.Version, strings.Join(incompatible, ", "))
	}
	if len(degraded) > 0 {
		r.logger.Info("excluding degraded nodes from rollout",
			"component", config.Component, "version", config.Version, "excluded", degraded)
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("update: start rollout: no eligible nodes (%d degraded excluded)", len(degraded))
	}
	return eligible, nil
}

// AbortRollout aborts a rollout. When this controller instance is driving it,
// the abort channel is closed and the driver goroutine performs the state
// transition (avoiding a data race — the goroutine is the sole owner of
// RolloutState mutations). When it is not locally active (multi-master: the
// queue-group-routed abort landed on a non-owning master, or the driver died),
// the abort is CAS-written directly to KV so the owning driver's poll /
// save-conflict detection fires and ResumeOrphaned never re-adopts it.
func (r *RolloutController) AbortRollout(ctx context.Context, rolloutID string) error {
	r.mu.Lock()
	_, isActive := r.active[rolloutID]
	abort, hasAbort := r.abortCh[rolloutID]
	if isActive && hasAbort {
		close(abort)
		// Remove the channel so a repeated abort cannot double-close it.
		delete(r.abortCh, rolloutID)
	}
	r.mu.Unlock()

	if isActive {
		r.logger.Info("rollout abort signaled", "id", rolloutID)
		return nil
	}

	// Not locally active: write the abort straight to KV with CAS retries
	// (the live driver may be bumping the revision concurrently).
	for attempt := 0; attempt < rolloutSaveRetries; attempt++ {
		persisted, err := r.store.Get(ctx, rolloutID)
		if err != nil {
			return fmt.Errorf("update: abort rollout %s: %w", rolloutID, err)
		}
		switch persisted.State {
		case RolloutAborted:
			return nil // already aborted
		case RolloutCompleted:
			return fmt.Errorf("update: abort rollout %s: already completed", rolloutID)
		}
		persisted.State = RolloutAborted
		persisted.FinishedAt = time.Now()
		if persisted.Error == "" {
			persisted.Error = "aborted by operator"
		}
		if err := r.store.Save(ctx, persisted); err == nil {
			r.logger.Info("rollout abort written to KV", "id", rolloutID, "locally_active", false)
			return nil
		}
		// CAS conflict — re-read and retry.
	}
	return fmt.Errorf("update: abort rollout %s: could not write abort after %d attempts", rolloutID, rolloutSaveRetries)
}

// ResumeOrphaned scans the rollout store for non-terminal rollouts whose
// DriverHeartbeat is older than StaleAfter, CAS-adopts each (writing this
// controller's DriverID and a fresh heartbeat), and resumes its driver loop
// from the persisted batch index. It returns the number of rollouts adopted.
//
// Adoption is advisory, not fenced: if the previous driver is merely slow
// rather than dead, both may briefly drive the same rollout. That is
// tolerable by design — the watchdog handler rejects duplicate prepare/apply
// commands, and the superseded driver detects the lost CAS on its next save
// and stops. A resumed rollout re-issues commands to its current batch from
// the prepare step; nodes that already progressed (staged/soaking) reject the
// duplicate command and are recorded as failed, bounded by MaxFailed and
// backstopped by the watchdog's confirm-deadline auto-rollback.
func (r *RolloutController) ResumeOrphaned(ctx context.Context) (int, error) {
	states, err := r.store.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("update: resume orphaned: %w", err)
	}

	adopted := 0
	for _, st := range states {
		if st.State == RolloutCompleted || st.State == RolloutAborted {
			continue // terminal
		}
		if st.Config.DryRun {
			continue // dry-run records are never driven
		}

		r.mu.Lock()
		_, isLocal := r.active[st.ID]
		r.mu.Unlock()
		if isLocal {
			continue // we are already driving it
		}

		// A zero DriverHeartbeat (record written by an older master) reads
		// as maximally stale and is adopted immediately.
		if time.Since(st.DriverHeartbeat) < r.staleAfter() {
			continue // fresh heartbeat: another driver is on it
		}

		// CAS-adopt: losing the race to another master is fine — skip.
		st.DriverID = r.driverID()
		st.DriverHeartbeat = time.Now()
		if err := r.store.Save(ctx, st); err != nil {
			r.logger.Debug("rollout adoption lost CAS race", "id", st.ID, "error", err)
			continue
		}

		abort := make(chan struct{})
		r.mu.Lock()
		r.active[st.ID] = st
		r.abortCh[st.ID] = abort
		r.mu.Unlock()

		r.logger.Warn("adopting orphaned rollout",
			"id", st.ID, "state", st.State, "batch", st.CurrentBatch, "driver", r.driverID())

		go r.executeRollout(r.baseCtx, st, abort)
		adopted++
	}
	return adopted, nil
}

// RunResumeLoop scans for and adopts orphaned rollouts (see ResumeOrphaned)
// once immediately and then every ResumeInterval, until ctx is cancelled.
// The master calls this once at startup.
func (r *RolloutController) RunResumeLoop(ctx context.Context) {
	scan := func() {
		n, err := r.ResumeOrphaned(ctx)
		if err != nil {
			r.logger.Warn("orphaned rollout scan failed", "error", err)
			return
		}
		if n > 0 {
			r.logger.Info("resumed orphaned rollouts", "count", n)
		}
	}

	scan()
	ticker := time.NewTicker(r.resumeInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan()
		}
	}
}

// saveStateLocked persists state, stamping this controller's driver identity
// and a fresh DriverHeartbeat. Transient failures are retried a few times
// with short backoff. A failed CAS is classified by re-reading KV:
//
//   - the persisted record is RolloutAborted → errRolloutAbortedExternally
//     (do not overwrite; the abort wins),
//   - a different DriverID holds the record → errRolloutSuperseded (another
//     controller adopted the rollout; stop driving without writing),
//   - otherwise the latest revision is adopted and the save retried.
//
// Callers must hold r.mu whenever state is shared with a running driver
// goroutine (the heartbeat loop encodes the whole struct concurrently).
func (r *RolloutController) saveStateLocked(ctx context.Context, state *RolloutState) error {
	state.DriverID = r.driverID()

	var lastErr error
	for attempt := 0; attempt < rolloutSaveRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("update: rollout save %s: %w", state.ID, ctx.Err())
			case <-time.After(rolloutSaveRetryBackoff):
			}
		}

		state.DriverHeartbeat = time.Now()
		err := r.store.Save(ctx, state)
		if err == nil {
			return nil
		}
		lastErr = err

		// The failure may be a CAS conflict caused by an external writer —
		// re-read to classify.
		persisted, gerr := r.store.Get(ctx, state.ID)
		if gerr != nil {
			continue
		}
		if persisted.State == RolloutAborted {
			return errRolloutAbortedExternally
		}
		if persisted.DriverID != "" && persisted.DriverID != r.driverID() {
			return errRolloutSuperseded
		}
		// Our own record at a newer revision (e.g. a previous incarnation of
		// this driver): adopt the revision and retry.
		state.Revision = persisted.Revision
	}
	return lastErr
}

// handlePersistError reacts to a final saveStateLocked failure. External
// aborts and supersedes just stop the local driver. A cancelled context means
// the driver is shutting down — the rollout record is left as-is for another
// master's ResumeOrphaned to adopt. Any other persistence failure aborts the
// rollout loudly: a rollout whose persisted state cannot be trusted must not
// keep driving (finding 26). The caller must stop driving in all cases.
func (r *RolloutController) handlePersistError(ctx context.Context, state *RolloutState, step string, err error) {
	switch {
	case errors.Is(err, errRolloutAbortedExternally):
		r.logger.Info("rollout aborted externally", "id", state.ID, "step", step)
	case errors.Is(err, errRolloutSuperseded):
		r.logger.Warn("rollout adopted by another driver, stopping local driver", "id", state.ID, "step", step)
	case ctx.Err() != nil:
		r.logger.Warn("rollout driver stopping (context cancelled), leaving rollout for adoption",
			"id", state.ID, "step", step)
	default:
		r.logger.Error("rollout state persistence failed, aborting rollout",
			"id", state.ID, "step", step, "error", err)
		r.abortForPersistFailure(ctx, state, err)
	}
}

// abortForPersistFailure best-effort marks the rollout aborted after its
// persistence path proved untrustworthy, and logs loudly either way.
func (r *RolloutController) abortForPersistFailure(ctx context.Context, state *RolloutState, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state.State = RolloutAborted
	state.FinishedAt = time.Now()
	state.Error = fmt.Sprintf("aborted: rollout state persistence failed: %v", cause)
	if err := r.saveStateLocked(ctx, state); err != nil &&
		!errors.Is(err, errRolloutAbortedExternally) && !errors.Is(err, errRolloutSuperseded) {
		r.logger.Error("failed to persist rollout abort after persistence failure; KV record is stale",
			"id", state.ID, "error", err)
	}
}

// persistProgress persists rollout progress from the driver goroutine.
// It returns false when the driver must stop (see handlePersistError).
func (r *RolloutController) persistProgress(ctx context.Context, state *RolloutState, step string) bool {
	r.mu.Lock()
	err := r.saveStateLocked(ctx, state)
	r.mu.Unlock()
	if err == nil {
		return true
	}
	r.handlePersistError(ctx, state, step, err)
	return false
}

// heartbeatLoop refreshes the persisted DriverHeartbeat every
// rolloutHeartbeatInterval while the driver goroutine runs, so other masters'
// ResumeOrphaned scans can tell a driven rollout from an orphaned one even
// during long batch commands, soaks, and pauses. Heartbeat write failures are
// tolerated (logged at Warn): the worst case is that another master adopts
// the rollout, and a brief double-drive is safe because the watchdog handler
// rejects duplicate prepare/apply commands.
func (r *RolloutController) heartbeatLoop(ctx context.Context, state *RolloutState) {
	ticker := time.NewTicker(rolloutHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			err := r.saveStateLocked(ctx, state)
			r.mu.Unlock()
			if err != nil && !errors.Is(err, errRolloutAbortedExternally) && !errors.Is(err, errRolloutSuperseded) {
				r.logger.Warn("rollout driver heartbeat write failed", "id", state.ID, "error", err)
			}
		}
	}
}

// batchOutcome tells executeRollout how to proceed after a batch phase.
type batchOutcome int

const (
	batchOK    batchOutcome = iota // continue the rollout
	batchAbort                     // MaxFailed exceeded → finish as aborted
	batchStop                      // stop driving immediately (already handled)
)

// executeRollout drives a rollout from state.CurrentBatch to completion. For
// a freshly started rollout CurrentBatch is 0; for one adopted by
// ResumeOrphaned it resumes from the persisted batch index.
func (r *RolloutController) executeRollout(ctx context.Context, state *RolloutState, abort <-chan struct{}) {
	defer func() {
		r.mu.Lock()
		delete(r.active, state.ID)
		delete(r.abortCh, state.ID)
		r.mu.Unlock()
	}()

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go r.heartbeatLoop(hbCtx, state)

	// Adopted mid-abort: just finish the abort, don't re-run batches.
	if state.State == RolloutAborting {
		r.logger.Info("finishing abort of adopted rollout", "id", state.ID)
		r.finishAborted(ctx, state)
		return
	}

	for batchIdx := state.CurrentBatch; batchIdx < len(state.Batches); batchIdx++ {
		batch := state.Batches[batchIdx]

		// Check both in-process abort channel and KV state (external abort).
		select {
		case <-abort:
			r.mu.Lock()
			state.State = RolloutAborted
			state.FinishedAt = time.Now()
			err := r.saveStateLocked(ctx, state)
			r.mu.Unlock()
			if err != nil && !errors.Is(err, errRolloutAbortedExternally) {
				r.logger.Error("failed to persist rollout abort", "id", state.ID, "error", err)
			}
			r.logger.Info("rollout aborted mid-run", "id", state.ID, "batch", batchIdx)
			return
		default:
		}
		if r.isAbortedInKV(ctx, state.ID) {
			// KV already holds the aborted record (written by another
			// master's AbortRollout) — nothing to write, just stop.
			r.logger.Info("rollout aborted externally", "id", state.ID, "batch", batchIdx)
			return
		}

		r.mu.Lock()
		state.State = RolloutRolling
		state.CurrentBatch = batchIdx
		r.mu.Unlock()
		if !r.persistProgress(ctx, state, "batch start") {
			return
		}

		r.logger.Info("rollout batch start", "id", state.ID, "batch", batchIdx, "nodes", len(batch))

		// Prepare all nodes in batch in parallel.
		switch r.runBatchCommand(ctx, state, batch, CmdPrepare, "prepared", abort) {
		case batchAbort:
			r.finishAborted(ctx, state)
			return
		case batchStop:
			return
		}

		// Apply all nodes in batch in parallel.
		switch r.runBatchCommand(ctx, state, batch, CmdApply, "applied", abort) {
		case batchAbort:
			r.finishAborted(ctx, state)
			return
		case batchStop:
			return
		}

		// Wait soak time then confirm.
		if r.waitOrAbort(ctx, state, abort, state.Config.SoakTime) {
			return
		}

		switch r.runBatchCommand(ctx, state, batch, CmdConfirm, "confirmed", abort) {
		case batchAbort:
			r.finishAborted(ctx, state)
			return
		case batchStop:
			return
		}

		// Pause between batches (skip after last batch).
		if batchIdx < len(state.Batches)-1 {
			if r.waitOrAbort(ctx, state, abort, state.Config.BatchPause) {
				return
			}
		}
	}

	r.mu.Lock()
	state.State = RolloutCompleted
	state.FinishedAt = time.Now()
	err := r.saveStateLocked(ctx, state)
	r.mu.Unlock()
	if err != nil {
		// Terminal step: nothing is left to drive, and a completed rollout
		// must not be overwritten with an abort — log loudly instead.
		switch {
		case errors.Is(err, errRolloutAbortedExternally):
			r.logger.Info("rollout aborted externally at completion", "id", state.ID)
		case errors.Is(err, errRolloutSuperseded):
			r.logger.Warn("rollout adopted by another driver at completion", "id", state.ID)
		default:
			r.logger.Error("failed to persist rollout completion; KV record is stale",
				"id", state.ID, "error", err)
		}
		return
	}
	r.logger.Info("rollout completed", "id", state.ID)
}

// runBatchCommand sends cmd to all nodes in batch in parallel and collects
// results. The returned outcome tells the caller whether to continue, finish
// as aborted (MaxFailed exceeded), or stop driving immediately.
func (r *RolloutController) runBatchCommand(ctx context.Context, state *RolloutState, batch []string, cmd string, successStatus string, abort <-chan struct{}) batchOutcome {
	type nodeErr struct {
		nodeID string
		err    error
	}

	results := make(chan nodeErr, len(batch))

	// Cache manifests by "goos/goarch" — component and version are constant per rollout.
	var manifestMu sync.Mutex
	manifestCache := map[string]*Manifest{}

	for _, nodeID := range batch {
		nodeID := nodeID
		go func() {
			select {
			case <-abort:
				results <- nodeErr{nodeID: nodeID, err: fmt.Errorf("aborted")}
				return
			default:
			}

			opCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()

			// Look up the node's platform from its status heartbeat.
			goos, goarch := "linux", "amd64"
			if r.statusKV != nil {
				if ns, err := GetNodeStatus(opCtx, r.statusKV, state.Config.Component, nodeID); err == nil {
					goos, goarch = ns.GOOS, ns.GOARCH
				}
			}

			cacheKey := goos + "/" + goarch
			manifestMu.Lock()
			m, cached := manifestCache[cacheKey]
			manifestMu.Unlock()
			if !cached {
				var err error
				m, err = r.manifest.Get(opCtx, state.Config.Component, goos, goarch, state.Config.Version)
				if err != nil {
					results <- nodeErr{nodeID: nodeID, err: fmt.Errorf("manifest: %w", err)}
					return
				}
				manifestMu.Lock()
				manifestCache[cacheKey] = m
				manifestMu.Unlock()
			}

			resp, err := r.request(opCtx, bus.UpdateCmdSubject(nodeID), &UpdateCommand{
				Command:   cmd,
				Version:   state.Config.Version,
				Component: state.Config.Component,
				SHA256:    m.SHA256,
				ObjectKey: m.ObjectKey,
			})
			if err != nil {
				results <- nodeErr{nodeID: nodeID, err: fmt.Errorf("request: %w", err)}
				return
			}
			if resp.Status == "error" {
				results <- nodeErr{nodeID: nodeID, err: fmt.Errorf("node error: %s", resp.Error)}
				return
			}
			results <- nodeErr{nodeID: nodeID}
		}()
	}

	for range batch {
		res := <-results
		now := time.Now()

		r.mu.Lock()
		nr, exists := state.NodeResults[res.nodeID]
		if !exists {
			nr = &NodeResult{ID: res.nodeID}
			state.NodeResults[res.nodeID] = nr
		}
		if res.err != nil {
			nr.Status = "failed"
			nr.Error = res.err.Error()
			nr.Updated = now
			state.FailedCount++
			r.logger.Error("node update failed", "id", state.ID, "node", res.nodeID, "cmd", cmd, "error", res.err)
		} else {
			nr.Status = successStatus
			nr.Error = ""
			nr.Updated = now
		}
		failed := state.FailedCount
		err := r.saveStateLocked(ctx, state)
		r.mu.Unlock()

		if err != nil {
			r.handlePersistError(ctx, state, "node result", err)
			return batchStop
		}

		if failed >= state.Config.MaxFailed {
			r.logger.Error("max failures exceeded, aborting rollout", "id", state.ID, "failed", failed)
			return batchAbort
		}
	}

	return batchOK
}

// waitOrAbort waits for the given duration while checking both the in-process
// abort channel and KV state (external abort) every 5 seconds. Returns true
// if aborted (caller should return).
func (r *RolloutController) waitOrAbort(ctx context.Context, state *RolloutState, abort <-chan struct{}, d time.Duration) bool {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()

	for {
		select {
		case <-abort:
			r.finishAborted(ctx, state)
			return true
		case <-deadline.C:
			return false
		case <-poll.C:
			if r.isAbortedInKV(ctx, state.ID) {
				// KV already holds the aborted record — nothing to write.
				r.logger.Info("rollout aborted externally during wait", "id", state.ID)
				return true
			}
		}
	}
}

// isAbortedInKV re-reads rollout state from KV to detect external abort
// (e.g., operator ran "zester update abort" against a different master).
func (r *RolloutController) isAbortedInKV(ctx context.Context, rolloutID string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	persisted, err := r.store.Get(checkCtx, rolloutID)
	if err != nil {
		return false
	}
	return persisted.State == RolloutAborted
}

func (r *RolloutController) finishAborted(ctx context.Context, state *RolloutState) {
	r.mu.Lock()
	state.State = RolloutAborting
	if err := r.saveStateLocked(ctx, state); err != nil &&
		!errors.Is(err, errRolloutAbortedExternally) && !errors.Is(err, errRolloutSuperseded) {
		r.logger.Error("failed to persist aborting state", "id", state.ID, "error", err)
	}
	state.State = RolloutAborted
	state.FinishedAt = time.Now()
	err := r.saveStateLocked(ctx, state)
	r.mu.Unlock()
	if err != nil && !errors.Is(err, errRolloutAbortedExternally) {
		r.logger.Error("failed to persist aborted state; KV record is stale", "id", state.ID, "error", err)
	}
	r.logger.Info("rollout finished as aborted", "id", state.ID)
}

// computeBatches splits nodeIDs into chunks. If batchPercent > 0, it takes precedence.
func computeBatches(nodeIDs []string, batchSize int, batchPercent int) [][]string {
	if len(nodeIDs) == 0 {
		return nil
	}

	if batchPercent > 0 {
		computed := len(nodeIDs) * batchPercent / 100
		if computed < 1 {
			computed = 1
		}
		batchSize = computed
	}

	if batchSize <= 0 {
		batchSize = 1
	}

	var batches [][]string
	for i := 0; i < len(nodeIDs); i += batchSize {
		end := i + batchSize
		if end > len(nodeIDs) {
			end = len(nodeIDs)
		}
		chunk := make([]string, end-i)
		copy(chunk, nodeIDs[i:end])
		batches = append(batches, chunk)
	}
	return batches
}

// RolloutTerminal reports whether a rollout state is final (no driver will
// touch it again). Dry-run records are also effectively terminal.
func RolloutTerminal(state string) bool {
	return state == RolloutCompleted || state == RolloutAborted
}
