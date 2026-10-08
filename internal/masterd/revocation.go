package masterd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/update"
)

// Credential revocation (security review 2026-10, HIGH-1).
//
// `zester enroll revoke` used to flip the enrollment record to `revoked` and
// nothing else: the peel's user JWT (valid 180 days) kept authenticating, the
// master kept re-encrypting secrets for it, and its events/returns were still
// honored. NATS invalidates an issued user JWT only through the ACCOUNT JWT's
// revocation list, so the master now owns that list:
//
//   - revocationSyncer rebuilds the full list from the enrollments bucket
//     (every record in StateRevoked → its user public key, revoked at the
//     decision time), re-signs the bootstrap account JWT with the operator
//     SIGNING key (auth.FileOperatorSigningSeed) and pushes it to the running
//     nats-server over the system account (auth.FileSysCreds) on
//     $SYS.REQ.ACCOUNT.<acct>.CLAIMS.UPDATE. The server closes live
//     connections of revoked users ("User Authentication Revoked") and
//     refuses their reconnects.
//   - The push is idempotent and repeated: immediately on `enroll revoke`
//     (the CLI reply carries a warning if it failed), on every sys-connection
//     (re)connect — the MEMORY resolver does not persist pushed updates, so a
//     nats-server restart reverts to the preloaded list until a master
//     re-pushes — and periodically (RevocationSyncInterval, default 5m),
//     which also converges --direct-kv revokes and other masters' decisions.
//   - Soft revocation is the second layer, for the window before the push
//     lands (sys connection down, nats-server mid-restart): the revoked
//     peel's facts, secrets, heartbeat, basket and update-status keys are
//     purged (it drops out of targeting and presence), the facts watcher
//     refuses to re-publish its secrets, and the reactor drops its events
//     (reason "revoked").
//
// The push material (account.jwt, operator-signing.seed, sys.creds — all
// written by `zester nats-auth init`) is REQUIRED: a master without it
// refuses to start rather than running with a revoke that does not revoke.

const (
	// defaultRevocationSyncInterval is the periodic full re-sync cadence
	// when the config knob is 0.
	defaultRevocationSyncInterval = 5 * time.Minute

	// revocationPushTimeout bounds one account-claims update request (the
	// first reply).
	revocationPushTimeout = 5 * time.Second

	// revocationReplyGrace is how long after the first reply the pusher keeps
	// collecting replies from the other servers of a NATS cluster — every
	// server answers (applied or skipped), and the tally is what the log and
	// readiness report.
	revocationReplyGrace = 750 * time.Millisecond

	// revocationConnectDebounce coalesces account connect events into one
	// re-push: a NATS restart reconnects the whole fleet within seconds.
	revocationConnectDebounce = 2 * time.Second
)

// revocationPushFn pushes a signed account JWT to the nats-server(s) and
// returns every raw reply collected (one per server in a cluster). Injected
// for tests.
type revocationPushFn func(ctx context.Context, subject string, accountJWT []byte) ([][]byte, error)

// revocationSyncer owns the account JWT revocation list and the soft-revoke
// state derived from the enrollments bucket.
type revocationSyncer struct {
	logger *slog.Logger
	store  *enroll.Store
	js     bus.JetStreamAPI
	now    func() time.Time

	// NATS-level push material (loaded from the auth dir at construction).
	accountPub  string
	baseJWT     string
	signing     *auth.KeyBundle
	push        revocationPushFn
	pushHealthy func() bool // sys connection health; nil = assume healthy
	interval    time.Duration

	mu          sync.Mutex
	lastPushed  map[string]int64 // user pubkey → revoked-at unix, as last accepted by the server
	lastErr     error
	lastPushAt  time.Time
	lastApplied []string // server names that applied the last push
	lastSkipped []string // server names without the account loaded at the last push
	pushes      int

	// connectTimer debounces account connect events into one re-push.
	connectMu    sync.Mutex
	connectTimer *time.Timer

	// revokedPeels is the soft-revoke cache: peel IDs whose CURRENT
	// enrollment record is revoked. Swapped atomically on every sync.
	revokedPeels atomic.Pointer[map[string]struct{}]
}

// newRevocationSyncer loads the push material from authDir. A missing or
// unreadable file is an error: the caller fails master startup, pointing at
// `zester nats-auth init`.
func newRevocationSyncer(logger *slog.Logger, store *enroll.Store, js bus.JetStreamAPI, authDir string, accountPub string, interval time.Duration) (*revocationSyncer, error) {
	if interval == 0 {
		interval = defaultRevocationSyncInterval
	}
	s := &revocationSyncer{
		logger:     logger,
		store:      store,
		js:         js,
		now:        time.Now,
		accountPub: accountPub,
		interval:   interval,
	}
	empty := map[string]struct{}{}
	s.revokedPeels.Store(&empty)

	baseJWT, err := os.ReadFile(filepath.Join(authDir, auth.FileAccountJWT))
	if err != nil {
		return nil, fmt.Errorf("credential revocation: read %s: %w (run 'zester nats-auth init')", auth.FileAccountJWT, err)
	}
	signing, err := auth.LoadKeyBundleFromFile(auth.RoleOperator, filepath.Join(authDir, auth.FileOperatorSigningSeed))
	if err != nil {
		return nil, fmt.Errorf("credential revocation: load %s: %w (run 'zester nats-auth init')", auth.FileOperatorSigningSeed, err)
	}
	if _, err := os.Stat(filepath.Join(authDir, auth.FileSysCreds)); err != nil {
		return nil, fmt.Errorf("credential revocation: %s: %w (run 'zester nats-auth init')", auth.FileSysCreds, err)
	}
	s.baseJWT = string(baseJWT)
	s.signing = signing
	return s, nil
}

// IsRevoked reports whether peelID's current enrollment is revoked (soft
// revocation gate for the facts watcher and the reactor).
func (s *revocationSyncer) IsRevoked(peelID string) bool {
	if s == nil {
		return false
	}
	set := s.revokedPeels.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[peelID]
	return ok
}

// markRevoked adds peelID to the soft-revoke cache immediately (the next
// Sync rebuilds the set from the store).
func (s *revocationSyncer) markRevoked(peelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.revokedPeels.Load()
	next := make(map[string]struct{}, len(*old)+1)
	maps.Copy(next, *old)
	next[peelID] = struct{}{}
	s.revokedPeels.Store(&next)
}

// revocationState is one pass over the enrollments bucket.
type revocationState struct {
	// keys maps revoked user public keys to the revocation time.
	keys map[string]time.Time
	// peels is the set of peel IDs whose current record is revoked.
	peels map[string]struct{}
}

// load derives the revocation state from every enrollment record: a user
// key is revoked when ANY record holding it is revoked (later decision wins
// — a re-enrolled identity gets a NEW JWT with a later iat, which the
// timestamped entry does not affect); a peel ID is soft-revoked when its most
// recently updated record is revoked.
func (s *revocationSyncer) load(ctx context.Context) (*revocationState, error) {
	recs, err := s.store.List(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list enrollments: %w", err)
	}
	st := &revocationState{keys: map[string]time.Time{}, peels: map[string]struct{}{}}
	latest := map[string]*enroll.Record{}
	for _, rec := range recs {
		if cur, ok := latest[rec.PeelID]; !ok || rec.UpdatedAt.After(cur.UpdatedAt) {
			latest[rec.PeelID] = rec
		}
		if rec.State != enroll.StateRevoked || rec.PublicKey == "" {
			continue
		}
		if auth.ValidatePublicKey(rec.PublicKey, auth.RoleUser) != nil {
			continue // test fixtures / corrupt records never reach the account JWT
		}
		at := rec.UpdatedAt
		if rec.DecidedAt != nil {
			at = *rec.DecidedAt
		}
		if at.IsZero() {
			at = s.now()
		}
		if prev, ok := st.keys[rec.PublicKey]; !ok || at.After(prev) {
			st.keys[rec.PublicKey] = at
		}
	}
	for peelID, rec := range latest {
		if rec.State == enroll.StateRevoked {
			st.peels[peelID] = struct{}{}
		}
	}
	return st, nil
}

// Sync rebuilds the revocation state from the enrollments bucket, refreshes
// the soft-revoke cache, and pushes the account JWT when the revoked key set
// changed since the last accepted push (or when force is set — used after a
// (re)connect, because the server may have restarted with the preloaded,
// revocation-free account JWT). Returns errRevocationDisabled when the push
// material is missing.
func (s *revocationSyncer) Sync(ctx context.Context, reason string, force bool) error {
	st, err := s.load(ctx)
	if err != nil {
		s.setErr(err)
		return err
	}
	s.revokedPeels.Store(&st.peels)

	want := make(map[string]int64, len(st.keys))
	for pub, at := range st.keys {
		want[pub] = at.Unix()
	}
	s.mu.Lock()
	unchanged := s.lastPushed != nil && maps.Equal(want, s.lastPushed)
	s.mu.Unlock()
	if unchanged && !force {
		return nil
	}

	token, err := auth.AccountJWTWithRevocations(s.baseJWT, s.signing, st.keys)
	if err != nil {
		s.setErr(err)
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, revocationPushTimeout)
	defer cancel()
	replies, err := s.push(pctx, auth.AccountClaimsUpdateSubject(s.accountPub), []byte(token))
	if err != nil {
		err = fmt.Errorf("push account JWT to nats-server: %w", err)
		s.setErr(err)
		s.logger.Warn("credential revocation list NOT applied at the nats-server (will retry)",
			"reason", reason, "revoked_keys", len(want), "error", err)
		return err
	}
	applied, skipped, err := tallyClaimsReplies(replies)
	if err != nil {
		s.setErr(err)
		s.logger.Warn("credential revocation list NOT applied at the nats-server (will retry)",
			"reason", reason, "revoked_keys", len(want), "applied_on", applied, "skipped_on", skipped, "error", err)
		return err
	}

	s.mu.Lock()
	s.lastPushed = want
	s.lastErr = nil
	s.lastPushAt = s.now()
	s.lastApplied, s.lastSkipped = applied, skipped
	s.pushes++
	s.mu.Unlock()
	// "skipped" servers have no client of the zester account connected
	// since they started, so there is nothing to revoke there right now;
	// the account connect event re-push covers the moment one shows up.
	s.logger.Info("account JWT revocation list pushed to nats-server",
		"reason", reason, "revoked_keys", len(want), "revoked_peels", sortedKeys(st.peels),
		"applied_on", applied, "skipped_on", skipped)
	return nil
}

// tallyClaimsReplies classifies every server's reply. A rejection by ANY
// server (bad signature, wrong subject) is an error — the fleet must not end
// up with a split revocation state; no reply at all is an error too.
func tallyClaimsReplies(replies [][]byte) (applied, skipped []string, err error) {
	if len(replies) == 0 {
		return nil, nil, errors.New("no nats-server answered the account claims update")
	}
	var errs []error
	for _, data := range replies {
		r, perr := auth.ParseClaimsUpdateReply(data)
		if perr != nil {
			errs = append(errs, perr)
			continue
		}
		name := r.Server
		if name == "" {
			name = "?"
		}
		switch r.Outcome {
		case auth.ClaimsUpdateApplied:
			applied = append(applied, name)
		case auth.ClaimsUpdateSkipped:
			skipped = append(skipped, name)
		}
	}
	sort.Strings(applied)
	sort.Strings(skipped)
	if len(errs) > 0 {
		return applied, skipped, errors.Join(errs...)
	}
	return applied, skipped, nil
}

// onAccountConnect is the $SYS.ACCOUNT.<acct>.CONNECT handler: a client
// connecting into the zester account on a server that may hold the
// pre-revocation account JWT (fresh after a nats-server restart, or one that
// "skipped" the last push) — including a revoked peel's own reconnect
// attempt. Debounced; re-pushes only while something is revoked.
func (s *revocationSyncer) onAccountConnect(ctx context.Context) {
	s.mu.Lock()
	pending := len(s.lastPushed)
	s.mu.Unlock()
	if pending == 0 {
		return
	}
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	if s.connectTimer != nil {
		s.connectTimer.Stop()
	}
	s.connectTimer = time.AfterFunc(revocationConnectDebounce, func() {
		if ctx.Err() != nil {
			return
		}
		_ = s.Sync(ctx, "account-connect", true)
	})
}

func (s *revocationSyncer) setErr(err error) {
	s.mu.Lock()
	s.lastErr = err
	s.mu.Unlock()
}

// onRevoked is the hook `zester enroll revoke` runs after the record
// transition: soft-revoke immediately, purge the peel's KV footprint, and
// push the updated account JWT. The returned warning (empty on full success)
// is relayed to the operator in the AdminResponse.
func (s *revocationSyncer) onRevoked(ctx context.Context, rec *enroll.Record) string {
	s.markRevoked(rec.PeelID)
	s.purgePeel(ctx, rec.PeelID)
	if err := s.Sync(ctx, "revoke", true); err != nil {
		return fmt.Sprintf("record revoked, but the NATS account revocation list was not applied yet (%v); the master retries on reconnect and every %s", err, s.interval)
	}
	return ""
}

// purgePeel deletes a revoked peel's KV footprint: facts (drops it from
// targeting and the facts index), per-peel secrets, heartbeat (presence),
// basket data and update-status. Best-effort: failures Warn.
func (s *revocationSyncer) purgePeel(ctx context.Context, peelID string) {
	del := func(bucket, key string) {
		kv, err := bus.GetBucket(ctx, s.js, bucket)
		if err != nil {
			s.logger.Warn("revocation: purge: bucket unavailable", "bucket", bucket, "peel", peelID, "error", err)
			return
		}
		if err := kv.Delete(ctx, key); err != nil && !errors.Is(err, bus.ErrKeyNotFound) {
			s.logger.Warn("revocation: purge: delete failed", "bucket", bucket, "key", key, "error", err)
		}
	}
	del(bus.BucketFacts, peelID)
	del(bus.BucketSecrets, peelID)
	del(bus.BucketPeelHeartbeat, peelID)
	del(bus.BucketUpdateStatus, update.StatusKey("peel", peelID))

	if kv, err := bus.GetBucket(ctx, s.js, bus.BucketBasket); err == nil {
		keys, err := bus.ListKeysWithPrefix(ctx, kv, peelID)
		if err != nil {
			s.logger.Warn("revocation: purge: list basket keys", "peel", peelID, "error", err)
		}
		for _, k := range keys {
			if err := kv.Delete(ctx, k); err != nil && !errors.Is(err, bus.ErrKeyNotFound) {
				s.logger.Warn("revocation: purge: delete basket key failed", "key", k, "error", err)
			}
		}
	}
	s.logger.Info("revoked peel purged from facts/secrets/heartbeat/basket/update-status", "peel", peelID)
}

// run drives the periodic sync until ctx ends. The first pass runs
// immediately (seeding the soft-revoke cache and pushing the current list).
func (s *revocationSyncer) run(ctx context.Context) {
	_ = s.Sync(ctx, "startup", true)
	if s.interval < 0 {
		return
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Sync(ctx, "periodic", false)
		}
	}
}

// check is the `revocation` readiness entry: degraded (never down) while the
// system-account connection is down or the last push failed — the control
// plane works, but a revoke would not cut NATS access until the retry lands.
func (s *revocationSyncer) check(context.Context) health.CheckResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastErr != nil {
		return health.CheckResult{Status: health.StatusDegraded, Message: "last revocation push failed: " + s.lastErr.Error()}
	}
	if s.pushHealthy != nil && !s.pushHealthy() {
		return health.CheckResult{Status: health.StatusDegraded, Message: "system-account NATS connection down"}
	}
	if s.lastPushed == nil {
		return health.CheckResult{Status: health.StatusDegraded, Message: "revocation list not pushed yet"}
	}
	return health.CheckResult{Status: health.StatusOK,
		Message: fmt.Sprintf("%d revoked key(s); applied on %d server(s), skipped on %d", len(s.lastPushed), len(s.lastApplied), len(s.lastSkipped))}
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// startRevocationSync constructs the syncer, opens the system-account NATS
// connection, registers the readiness check and starts the periodic loop.
// Missing push material fails master startup.
func (d *Daemon) startRevocationSync(ctx context.Context) error {
	s, err := newRevocationSyncer(d.logger, d.enrollStore, d.js, d.cfg.AuthDir, d.accountKP.PublicKey,
		time.Duration(d.cfg.RevocationSyncInterval))
	if err != nil {
		return err
	}
	d.revocation = s
	d.checker.Register("revocation", s.check)

	natsURLs := bus.NormalizeNATSURLs([]string{d.cfg.NatsURL})
	natsTLS, natsCA, caOptional := bus.NATSClientTLS(natsURLs, d.cfg.NatsCA, d.cfg.AuthDir)
	sys, err := bus.NewClient(bus.ClientConfig{
		URLs:           natsURLs,
		Name:           "zester-master-sys",
		CredsFile:      filepath.Join(d.cfg.AuthDir, auth.FileSysCreds),
		TLS:            natsTLS,
		CAFile:         natsCA,
		CAFileOptional: caOptional,
		Logger:         d.logger.With("conn", "sys"),
		RetryConnect:   true,
		// A (re)connect may follow a nats-server restart that reloaded the
		// preloaded (revocation-free) account JWT: force a re-push.
		OnReconnect: func() { go func() { _ = s.Sync(ctx, "reconnect", true) }() },
	})
	if err != nil {
		return fmt.Errorf("credential revocation: system-account NATS connection: %w", err)
	}
	s.push = func(pctx context.Context, subject string, accountJWT []byte) ([][]byte, error) {
		return pushCollectReplies(pctx, sys.Conn(), subject, accountJWT)
	}
	s.pushHealthy = sys.IsHealthy
	// Re-push trigger: any client connecting into the zester account.
	if _, err := sys.Conn().Subscribe(auth.AccountConnectEventSubject(s.accountPub), func(*nats.Msg) {
		s.onAccountConnect(ctx)
	}); err != nil {
		d.logger.Warn("revocation: subscribe account connect events failed; relying on periodic re-push", "error", err)
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sys.Shutdown(sctx)
	}()
	// A deferred initial connect (RetryConnect) fires OnReconnect when it
	// lands, which re-syncs; the loop's startup pass covers the connected
	// case and seeds the soft-revoke cache either way.
	go s.run(ctx)
	d.logger.Info("credential revocation sync started", "interval", s.interval)
	return nil
}

// pushCollectReplies publishes the account JWT as a request and collects
// every server's reply: the first within ctx, then whatever else arrives
// within revocationReplyGrace (a cluster answers once per server).
func pushCollectReplies(ctx context.Context, nc *nats.Conn, subject string, accountJWT []byte) ([][]byte, error) {
	inbox := nats.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe reply inbox: %w", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck
	if err := nc.PublishRequest(subject, inbox, accountJWT); err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	first, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	replies := [][]byte{first.Data}
	deadline := time.Now().Add(revocationReplyGrace)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return replies, nil
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			return replies, nil // timeout: the cluster has answered
		}
		replies = append(replies, msg.Data)
	}
}
