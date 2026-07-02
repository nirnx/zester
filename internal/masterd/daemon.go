// Package masterd implements the zester-master daemon runtime. It was
// extracted verbatim from cmd/zester-master's former run() function
// (architecture review finding 39): the Daemon struct owns the state that
// used to live in run()'s local variables and closures, and each former
// inline subsystem-wiring block is a named method that Run calls in the
// pre-extraction order. Startup order, log messages, endpoints, and
// shutdown/defer semantics are preserved 1:1 with the pre-extraction binary.
package masterd

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/segmentio/ksuid"

	"github.com/ptorbus/zester/internal/config"
	"github.com/ptorbus/zester/internal/health"
	"github.com/ptorbus/zester/internal/metrics"
	"github.com/ptorbus/zester/internal/version"
	"github.com/ptorbus/zester/pkg/auth"
	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/job"
	"github.com/ptorbus/zester/pkg/settings"
	"github.com/ptorbus/zester/pkg/statefiles"
	"github.com/ptorbus/zester/pkg/update"
)

const (
	// masterQueueGroup is the NATS queue group for dispatch load balancing.
	masterQueueGroup = "zester.masters"
)

// Daemon is the zester-master daemon runtime. New constructs it from a fully
// resolved config and logger; Run starts every subsystem in the exact order
// the former run() did and blocks until the passed context is cancelled.
//
// Mutable fields are assigned during Run in startup order and are stable
// afterwards unless a comment below says otherwise; the comments document
// the concurrency guarantee each field carries (the same guarantees the
// former function-scoped variables had).
type Daemon struct {
	cfg    *config.MasterDaemonConfig
	logger *slog.Logger // annotated with master_id in New
	// masterID is the unique master instance ID (KSUID for time-ordered
	// uniqueness), generated in New so every log line carries it.
	masterID string

	// runCtx is Run's internal lifecycle context, handed to every subsystem
	// (gitfs, job manager subscriptions, heartbeater, scanner, watchers) and
	// read by the NATS message handlers (handleDispatch, handleRolloutStart,
	// handleFactsUpdate) — formerly run()'s ctx closure capture. It is
	// cancelled by Run's first-registered defer, i.e. only AFTER all other
	// shutdown defers have completed, preserving the pre-extraction ordering
	// (subsystems are stopped via their Stop/cancel/Unsubscribe functions
	// first; the shared context dies last).
	runCtx context.Context

	reg     *metrics.Registry
	checker *health.Checker

	// busClient is read by the "nats" readiness check (natsCheck), which is
	// registered up-front against this atomic pointer: it stays nil until
	// the NATS client is created, so /readyz reports Down (503) from process
	// start until NATS is actually connected.
	busClient atomic.Pointer[bus.Client]

	client *bus.Client // set once in connectNATS; stable afterwards
	nc     *nats.Conn  // raw connection for queue subscriptions and request/reply

	// js is the JetStream API handed to the subsystems added after the
	// masterd extraction (leader leases, target-resolution service,
	// connected-peels gauge). Run sets it to d.client.JetStream() right
	// after connecting; tests inject a bustest.FakeJS.
	js bus.JetStreamAPI

	// accountKP is the account key for settings encryption, loaded in
	// startSettingsPublisher and reused by the credential issuer in
	// startEnrollment.
	accountKP *auth.KeyBundle

	// Settings-publishing state shared with the facts watcher callback
	// (handleFactsUpdate): assigned once in startSettingsPublisher before
	// the watcher starts, read-only afterwards from the watcher goroutine.
	publisher  *settings.Publisher
	allSecrets settings.FileSecrets
	topFile    *settings.TopFile

	// rawSettingsFiles holds the loaded .zy settings files (relative path
	// → content). Loading runs on EVERY master (handleFactsUpdate
	// re-encrypts secrets from d.allSecrets/d.topFile), but the sanitized
	// files are published to KV only by the publisher-lease holder
	// (publishSettingsFiles). Assigned once in startSettingsPublisher,
	// read-only afterwards.
	rawSettingsFiles map[string][]byte

	statePublisher *statefiles.Publisher

	// gitfs is the GitFS syncer, constructed in startGitFS when remotes
	// are configured. Its sync loop runs only while this master holds the
	// publisher lease (runLeaderPublish).
	gitfs *statefiles.GitFS

	// pubMu guards pubCancel: the cancel function for the current
	// publisher-lease acquisition sub-context (nil while not leader).
	// OnAcquired stores it, OnLost cancels it — both run on the lease's
	// Run goroutine, but shutdown-time cancellation can race with them.
	pubMu     sync.Mutex
	pubCancel context.CancelFunc

	// secretsLease gates per-peel secrets publishing in handleFactsUpdate
	// (roadmap B6): only the "facts-secrets" lease holder re-encrypts and
	// publishes. nil (unit tests) means always publish.
	secretsLease *bus.LeaderLease

	// leaseTTL / leaseRenewInterval override the LeaderLease timings for
	// both daemon leases. Zero (production) means the LeaderLease defaults,
	// which match the leases bucket TTL. Tests set short values.
	leaseTTL           time.Duration
	leaseRenewInterval time.Duration

	// GitFS health state (only populated when GitFS is configured):
	// gitfsInterval is the effective sync interval (config value, floored to
	// 5m when unset), gitfsLastSync is unix nanos of the last
	// fully-successful sync (written by the OnSyncSuccess callback, read by
	// gitfsCheck), and gitfsExit holds an error message (string) if the
	// syncer goroutine ever exits while the master is still running.
	gitfsInterval time.Duration
	gitfsLastSync atomic.Int64
	gitfsExit     atomic.Value

	jobMgr *job.Manager

	// schedState holds the current 'sched-consumer' readiness result
	// (health.CheckResult). It starts as the boot outcome; when the boot
	// start failed, the background retry loop (retrySchedConsumer) flips it
	// to OK once the consumer finally starts.
	schedState atomic.Value

	// schedStopMu guards schedStop and schedStopped: the stop function of
	// the currently running scheduled-result consumer (replaced by the
	// retry loop) and whether shutdown already ran (so a late retry success
	// stops its own consumer instead of leaking it).
	schedStopMu  sync.Mutex
	schedStop    func()
	schedStopped bool

	// schedStart is a test seam for starting the scheduled-result
	// consumer. nil (production) means job.StartScheduledResultConsumer
	// against d.client's JetStream.
	schedStart func(ctx context.Context) (func(), error)

	// schedRetryInterval paces the sched-consumer background retry loop.
	// Zero means schedConsumerRetryInterval (60s); tests set short values.
	schedRetryInterval time.Duration

	enrollStore *enroll.Store

	// enrollState tracks the enrollment server goroutine's state
	// (health.CheckResult) for the 'enroll-server' readiness check: OK is
	// stored just before Start blocks; if Start returns with a real error
	// (bad cert, port taken), the goroutine flips it to Down.
	enrollState atomic.Value

	rolloutCtrl *update.RolloutController
	statusKV    bus.KV // update-status bucket, read by handleRolloutStart

	// targetState tracks the 'target-service' readiness result
	// (health.CheckResult): OK once the resolve service is serving, Down
	// when it failed to start.
	targetState atomic.Value
}

// New constructs the daemon from a fully-resolved config and logger. It
// generates the unique master instance ID (KSUID for time-ordered
// uniqueness) up front so every log line carries it.
func New(cfg *config.MasterDaemonConfig, logger *slog.Logger) *Daemon {
	masterID := "master-" + ksuid.New().String()
	return &Daemon{
		cfg:      cfg,
		logger:   logger.With("master_id", masterID),
		masterID: masterID,
	}
}

// Logger returns the daemon's logger, annotated with the generated
// master_id. main uses it so the shutdown-signal log line carries the same
// attributes as before the masterd extraction.
func (d *Daemon) Logger() *slog.Logger { return d.logger }

// Run starts every master subsystem in the exact pre-extraction order and
// blocks until the passed context is cancelled (main cancels it on
// SIGINT/SIGTERM). The deferred teardown mirrors the former run()'s defer
// stack 1:1.
func (d *Daemon) Run(ctx context.Context) error {
	// runCtx deliberately derives from Background, not from ctx: the
	// deferred cancel below is registered first, so runCtx is cancelled only
	// after all other shutdown defers have run — preserving the
	// pre-extraction run() ordering where subsystems are stopped via their
	// Stop/cancel/Unsubscribe functions first and the shared context dies
	// last (client.Shutdown and enrollSrv.Shutdown run with a live context).
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.runCtx = runCtx

	d.logger.Info("master instance ID generated")

	// Prometheus registry and readiness checker, both served on the local
	// health listener: /healthz stays a static liveness probe, /readyz
	// aggregates per-subsystem checks (503 unless all are OK), /metrics
	// serves the Prometheus scrape endpoint.
	d.reg = metrics.NewMasterRegistry()
	d.checker = health.New(version.Version, 2*time.Second)

	// The 'nats' check is registered before the connection exists via the
	// atomic client pointer: until the client connects, /readyz reports
	// Down (503), which is accurate during startup.
	d.checker.Register("nats", d.natsCheck)

	stopHealth, err := startLocalHealthServer(d.logger, "master", d.cfg.HealthAddr, d.checker.Handler(), d.reg.Handler())
	if err != nil {
		return fmt.Errorf("start local health server: %w", err)
	}
	defer stopHealth()

	if err := d.connectNATS(); err != nil {
		return err
	}
	defer d.client.Shutdown(runCtx)
	d.js = d.client.JetStream()
	d.busClient.Store(d.client) // 'nats' readiness check goes live
	d.logger.Info("NATS connected", "url", d.cfg.NatsURL)

	if err := d.initStorage(runCtx); err != nil {
		return err
	}

	if err := d.startSettingsPublisher(runCtx); err != nil {
		return err
	}

	if err := d.startStatefilesPublisher(runCtx); err != nil {
		return err
	}

	d.startGitFS()

	// Single-publisher lease (roadmap B7): the settings-files publish, the
	// state-files publish, and the GitFS sync loop all run on the lease
	// holder only. In a single-master deployment the lease is acquired
	// immediately (storage init above created the bucket), so behavior is
	// unchanged; standby masters start publishing when they acquire it.
	if err := d.startPublisherLease(runCtx); err != nil {
		return err
	}

	d.startJobManager()
	defer d.jobMgr.Shutdown()

	stopSchedConsumer := d.startSchedConsumer(runCtx)
	defer stopSchedConsumer()

	unsubDispatch, err := d.subscribeDispatch()
	if err != nil {
		return err
	}
	defer unsubDispatch()

	unsubCancel, err := d.subscribeCancel()
	if err != nil {
		return err
	}
	defer unsubCancel()
	d.logger.Info("job system initialized", "master_id", d.masterID)

	d.startHeartbeatAndOrphanScanner(runCtx)

	// The enrollment store is created before the facts watcher so the
	// watcher callback (which runs on a watcher goroutine) never observes a
	// half-assigned pointer.
	d.enrollStore, err = enroll.NewStore(runCtx, enroll.StoreConfig{
		JS:     d.client.JetStream(),
		Logger: d.logger,
	})
	if err != nil {
		return fmt.Errorf("create enrollment store: %w", err)
	}

	// Facts→secrets single-owner lease (roadmap B6): only the holder
	// publishes per-peel secrets from handleFactsUpdate. Started before the
	// facts watcher so the callback never observes a half-assigned lease.
	if err := d.startSecretsLease(runCtx); err != nil {
		return err
	}

	cancelWatch, err := d.startFactsWatcher(runCtx)
	if err != nil {
		return err
	}
	defer cancelWatch()
	d.logger.Info("facts watcher started")

	stopEnroll, err := d.startEnrollment(runCtx)
	if err != nil {
		return err
	}
	defer stopEnroll()
	d.logger.Info("enrollment system initialized", "addr", d.cfg.Enroll.Addr)

	stopAdmin, err := d.startAdminService(bus.NewNATSPubSub(d.nc))
	if err != nil {
		return err
	}
	defer stopAdmin()

	unsubRollout, err := d.startRolloutController(runCtx)
	if err != nil {
		return err
	}
	defer unsubRollout()
	// Resume loop: adopt rollouts orphaned by a dead master (finding 26).
	go d.rolloutCtrl.RunResumeLoop(runCtx)
	d.logger.Info("rollout controller initialized")

	// Target-resolution service (roadmap C2): every master joins the queue
	// group; readiness check registered after the pre-existing ones.
	stopTargetService := d.startTargetService(runCtx)
	defer stopTargetService()

	d.startConnectedPeelsGauge(runCtx)

	d.logger.Info("zester-master ready", "nats", d.cfg.NatsURL, "enroll_addr", d.cfg.Enroll.Addr)

	// Block until main cancels the context (shutdown signal); the deferred
	// teardown above then runs in the pre-extraction order.
	<-ctx.Done()
	return nil
}
