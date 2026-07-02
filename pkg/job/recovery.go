package job

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

const (
	// OrphanScanInterval is how often the scanner checks for orphaned jobs.
	OrphanScanInterval = 20 * time.Second

	// DefaultMissThreshold is the number of consecutive scan cycles a
	// job's owner must be absent from the live-master set before its
	// jobs are reclaimed. A single missed cycle (e.g. a heartbeat-bucket
	// blip that expired all heartbeats at once) must not trigger a mass
	// reclaim of jobs whose owners are actually alive.
	DefaultMissThreshold = 2
)

// OrphanScanner detects jobs owned by dead masters and reclaims them.
// It runs on every master instance; reclaims are CAS-protected to
// prevent split-brain double-reclaim.
//
// The scanner is not safe for concurrent use: scan cycles are serialized
// by the Run loop (miss-count state is mutated without locking).
type OrphanScanner struct {
	masterID  string
	js        bus.JetStreamAPI
	logger    *slog.Logger
	reclaimFn func(ctx context.Context, j *Job) // callback when a job is reclaimed

	// MissThreshold is the number of consecutive scan cycles an owner
	// must be observed missing before its jobs are reclaimed. Defaults
	// to DefaultMissThreshold; values <= 0 fall back to the default.
	// Set before calling Run; tests may lower it to 1.
	MissThreshold int

	// OnReclaim, if set, is invoked once for each job this scanner
	// successfully reclaims (after the CAS ownership update succeeds).
	// Optional metric hook: nil means no-op. Set before calling Run.
	OnReclaim func()

	// missCounts tracks, per absent master ID, how many consecutive
	// scan cycles it has been missing from the live-master set. Reset
	// whenever the master is seen alive again (or stops owning jobs).
	missCounts map[string]int
}

// NewOrphanScanner creates an orphan scanner for the given master.
// reclaimFn is called for each job that is successfully reclaimed.
func NewOrphanScanner(masterID string, js bus.JetStreamAPI, logger *slog.Logger, reclaimFn func(ctx context.Context, j *Job)) *OrphanScanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &OrphanScanner{
		masterID:      masterID,
		js:            js,
		logger:        logger,
		reclaimFn:     reclaimFn,
		MissThreshold: DefaultMissThreshold,
		missCounts:    make(map[string]int),
	}
}

// Run starts the orphan scan loop. It blocks until ctx is cancelled.
func (s *OrphanScanner) Run(ctx context.Context) {
	ticker := time.NewTicker(OrphanScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

func (s *OrphanScanner) scan(ctx context.Context) {
	liveMasters, err := ListLiveMasters(ctx, s.js)
	if err != nil {
		// A liveness-read failure must NOT be interpreted as "all
		// masters dead": abort this scan cycle entirely (no reclaims,
		// no miss-count bumps) and retry on the next tick.
		s.logger.Warn("orphan scan: list live masters failed, aborting scan cycle", "error", err)
		return
	}

	jobsKV, err := bus.GetBucket(ctx, s.js, bus.BucketJobs)
	if err != nil {
		s.logger.Error("orphan scan: get jobs bucket", "error", err)
		return
	}

	// Enumerate only the active-jobs index ("active.<jid>"), not the whole
	// 7-day jobs bucket: scan cost is O(active jobs), independent of the
	// retained history and of scheduler-synthetic jobs (which are terminal
	// at creation and never get index entries). Every claimed job gets an
	// index entry at claim time; the write is best-effort, so a job whose
	// index write failed is invisible to the scanner and the bucket TTL
	// ages it out. Stale entries (index key without a live job record)
	// are self-healed below.
	activeKeys, err := bus.ListKeysWithPrefix(ctx, jobsKV, ActiveJobKeyPrefix+bus.WildcardMany)
	if err != nil {
		s.logger.Warn("orphan scan: list active jobs failed, aborting scan cycle", "error", err)
		return
	}
	if len(activeKeys) == 0 {
		// No active jobs: nothing to scan.
		return
	}

	threshold := s.MissThreshold
	if threshold <= 0 {
		threshold = DefaultMissThreshold
	}
	if s.missCounts == nil {
		s.missCounts = make(map[string]int)
	}
	// missedThisCycle ensures each absent owner's consecutive-miss count
	// is bumped at most once per scan, however many jobs it owns.
	missedThisCycle := make(map[string]bool)
	defer func() {
		// Drop counts for masters not observed missing this cycle: they
		// are either live again or no longer own any non-terminal jobs.
		for id := range s.missCounts {
			if !missedThisCycle[id] {
				delete(s.missCounts, id)
			}
		}
	}()

	for _, activeKey := range activeKeys {
		jid := strings.TrimPrefix(activeKey, ActiveJobKeyPrefix)

		entry, err := jobsKV.Get(ctx, jid)
		if err != nil {
			if errors.Is(err, bus.ErrKeyNotFound) {
				// Self-heal: index entry references a job record that no
				// longer exists (TTL expiry, manual purge).
				s.logger.Warn("orphan scan: active key without job record, removing", "jid", jid)
				clearActiveKey(ctx, jobsKV, jid, s.logger)
			}
			continue
		}

		var j Job
		if err := bus.Decode(entry.Value(), &j); err != nil {
			continue
		}

		// Self-heal: the finalize-path delete of the index entry failed
		// (best-effort, see clearActiveKey) or the index was edited by
		// hand; drop the stale entry now and skip.
		if j.IsTerminal() {
			clearActiveKey(ctx, jobsKV, jid, s.logger)
			continue
		}

		// Skip jobs with no owner (pending, not yet claimed).
		if j.Owner == "" {
			continue
		}

		// Skip jobs owned by this master (we handle our own).
		if j.Owner == s.masterID {
			continue
		}

		// Check if the owning master is still alive.
		if _, alive := liveMasters[j.Owner]; alive {
			continue
		}

		// Consecutive-miss grace: only declare the owner dead after it
		// has been absent from the live-master set for at least
		// `threshold` consecutive scan cycles.
		if !missedThisCycle[j.Owner] {
			missedThisCycle[j.Owner] = true
			s.missCounts[j.Owner]++
		}
		if s.missCounts[j.Owner] < threshold {
			s.logger.Info("orphan scan: owner missing, within grace period",
				"jid", j.JID, "owner", j.Owner,
				"misses", s.missCounts[j.Owner], "threshold", threshold)
			continue
		}

		// The owner is dead. Attempt to reclaim via CAS.
		s.logger.Info("orphan scan: dead master detected, reclaiming job",
			"jid", j.JID, "dead_owner", j.Owner, "new_owner", s.masterID)

		j.Owner = s.masterID
		j.Epoch = 0 // Will be set from CAS result.
		j.Updated = time.Now().UTC()
		j.ReclaimCount++ // persisted with the CAS ownership update below

		data, err := bus.Encode(j)
		if err != nil {
			s.logger.Error("orphan scan: encode job", "jid", j.JID, "error", err)
			continue
		}

		// CAS update: only succeed if no one else reclaimed first.
		rev, err := jobsKV.Update(ctx, jid, data, entry.Revision())
		if err != nil {
			s.logger.Warn("orphan scan: CAS failed (another master reclaimed)",
				"jid", j.JID, "error", err)
			continue
		}

		j.Epoch = rev
		s.logger.Info("orphan scan: job reclaimed", "jid", j.JID, "epoch", rev)

		// Ownership changed: rewrite the index entry with the new owner.
		writeActiveKey(ctx, jobsKV, jid, s.masterID, false, s.logger)

		if s.OnReclaim != nil {
			s.OnReclaim()
		}
		if s.reclaimFn != nil {
			s.reclaimFn(ctx, &j)
		}
	}
}
