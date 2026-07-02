package schedule

import (
	"context"
	"hash/fnv"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// ExecResult holds the outcome of a scheduled module execution.
type ExecResult struct {
	Success    bool
	ResultData any    // opaque result payload (e.g. proto.ExecResponse)
	Error      string // error message, empty on success
	Duration   time.Duration
}

// ExecFn is the function signature the scheduler calls to execute a module.
// It returns an ExecResult so return_job entries can capture the full outcome.
type ExecFn func(ctx context.Context, module string, args map[string]any) ExecResult

// ReturnFn is called after execution when an entry has ReturnJob: true.
// It receives the full Entry (for module/args/name) and the ExecResult.
// The implementation should create a trackable job in the job system.
type ReturnFn func(entry Entry, result ExecResult)

// RunnerConfig configures a new Runner.
type RunnerConfig struct {
	PeelID   string
	ExecFn   ExecFn
	ReturnFn ReturnFn // called for return_job entries; nil = no-op
	Logger   *slog.Logger
	Now      func() time.Time // injectable clock for testing
}

// entryState holds runtime state for a single schedule entry.
type entryState struct {
	entry   Entry
	nextRun time.Time
	running atomic.Int32
	splayer *rand.Rand
}

// Runner evaluates schedule entries and fires executions at the right time.
type Runner struct {
	cfg     RunnerConfig
	mu      sync.RWMutex
	entries map[string]*entryState           // effective entries keyed by name
	sources map[EntrySource]map[string]Entry // raw entries per source/name
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	now     func() time.Time
	logger  *slog.Logger
}

// NewRunner creates a new Runner.
func NewRunner(cfg RunnerConfig) *Runner {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		cfg:     cfg,
		entries: make(map[string]*entryState),
		sources: make(map[EntrySource]map[string]Entry),
		now:     nowFn,
		logger:  logger,
	}
}

// Load adds entries and computes their initial nextRun times.
func (r *Runner) Load(entries []Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Treat Load as upsert into per-source maps, then rebuild effective view.
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if _, ok := r.sources[e.Source]; !ok {
			r.sources[e.Source] = make(map[string]Entry)
		}
		r.sources[e.Source][e.Name] = e
	}
	r.rebuildEffectiveLocked(r.now())
}

// Reload merges new entries (from the given source) into the runner.
// Entries from other sources are left untouched. Within the same source:
// new entries are added, changed entries are updated, missing entries are removed.
func (r *Runner) Reload(entries []Entry, source EntrySource) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Replace this source snapshot with the newly provided entries.
	incoming := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if e.Enabled {
			incoming[e.Name] = e
		}
	}
	r.sources[source] = incoming
	r.rebuildEffectiveLocked(r.now())
}

func (r *Runner) rebuildEffectiveLocked(now time.Time) {
	// Build effective set with explicit precedence:
	// settings > config > all others.
	effective := make(map[string]Entry)
	choose := func(src EntrySource) {
		srcMap, ok := r.sources[src]
		if !ok {
			return
		}
		for name, e := range srcMap {
			effective[name] = e
		}
	}
	// Lower precedence first, higher precedence last.
	for src, srcMap := range r.sources {
		if src == SourceConfig || src == SourceSettings {
			continue
		}
		for name, e := range srcMap {
			effective[name] = e
		}
	}
	choose(SourceConfig)
	choose(SourceSettings)

	// Remove effective entries no longer present.
	for name, es := range r.entries {
		if _, ok := effective[name]; !ok {
			delete(r.entries, name)
			r.logger.Info("schedule entry removed", "name", name, "source", es.entry.Source)
			continue
		}
	}

	// Add/update effective entries.
	for name, e := range effective {
		existing, ok := r.entries[name]
		if ok && entriesEqual(existing.entry, e) {
			// Entry unchanged — preserve runtime state, just update config fields.
			existing.entry = e
			continue
		}

		// New or changed — compute fresh nextRun.
		es := r.makeEntryState(e, now)
		r.entries[name] = es
		r.logger.Info("schedule entry loaded", "name", e.Name, "module", e.Module, "source", e.Source)
	}
}

func entriesEqual(a, b Entry) bool {
	return a.Name == b.Name &&
		a.Module == b.Module &&
		a.Interval == b.Interval &&
		a.Cron == b.Cron &&
		a.Splay == b.Splay &&
		a.MaxRunning == b.MaxRunning &&
		a.RunOnStart == b.RunOnStart &&
		a.ReturnJob == b.ReturnJob &&
		a.Enabled == b.Enabled &&
		a.Source == b.Source
}

func (r *Runner) makeEntryState(e Entry, now time.Time) *entryState {
	es := &entryState{
		entry:   e,
		splayer: newSplayer(r.cfg.PeelID, e.Name),
	}

	if e.RunOnStart {
		es.nextRun = now
	} else {
		es.nextRun = r.computeNext(e, es.splayer, now)
	}

	return es
}

// Start begins the scheduler ticker loop. Call Stop() to terminate.
func (r *Runner) Start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(1)
	go r.loop(ctx)
}

// Stop cancels the scheduler and waits for all in-flight executions to complete.
func (r *Runner) Stop() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}

func (r *Runner) loop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	r.mu.RLock()
	now := r.now()
	var toFire []*entryState
	for _, es := range r.entries {
		if !es.nextRun.IsZero() && !now.Before(es.nextRun) {
			if int(es.running.Load()) < es.entry.MaxRunning {
				toFire = append(toFire, es)
			}
		}
	}
	r.mu.RUnlock()

	for _, es := range toFire {
		es.running.Add(1)
		// Compute next run time immediately so we don't double-fire.
		r.mu.Lock()
		es.nextRun = r.computeNext(es.entry, es.splayer, now)
		r.mu.Unlock()

		r.wg.Add(1)
		go func(es *entryState) {
			defer r.wg.Done()
			defer es.running.Add(-1)

			r.logger.Info("schedule firing", "name", es.entry.Name, "module", es.entry.Module)

			start := r.now()
			result := r.cfg.ExecFn(ctx, es.entry.Module, es.entry.Args)
			result.Duration = r.now().Sub(start)

			if !result.Success {
				r.logger.Error("schedule exec failed", "name", es.entry.Name, "error", result.Error)
			} else {
				r.logger.Info("schedule exec complete", "name", es.entry.Name)
			}

			if es.entry.ReturnJob && r.cfg.ReturnFn != nil {
				r.cfg.ReturnFn(es.entry, result)
			}
		}(es)
	}
}

func (r *Runner) computeNext(e Entry, splayer *rand.Rand, after time.Time) time.Time {
	var next time.Time

	if e.Cron != "" {
		sched, err := ParseCron(e.Cron)
		if err != nil {
			r.logger.Error("schedule: bad cron", "name", e.Name, "error", err)
			return time.Time{} // disable entry
		}
		next = sched.Next(after)
	} else {
		next = after.Add(e.Interval)
	}

	if e.Splay > 0 {
		splayNs := splayer.Int63n(int64(e.Splay))
		next = next.Add(time.Duration(splayNs))
	}

	return next
}

// Entries returns the current set of entry names (for testing/logging).
func (r *Runner) Entries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	return names
}

func newSplayer(peelID, entryName string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(peelID))
	h.Write([]byte(entryName))
	return rand.New(rand.NewSource(int64(h.Sum64())))
}
