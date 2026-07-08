package enroll

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/nirnx/zester/pkg/bus"
)

// Handler provides HTTP handlers for the enrollment API.
type Handler struct {
	store             *Store
	challenges        *ChallengeStore
	issuer            *CredentialIssuer
	logger            *slog.Logger
	maxStreamDuration time.Duration
	onPending         func(Record)
	bootstrap         BootstrapProvider
	caRootPin         func() string
}

// HandlerConfig configures the enrollment HTTP handler.
type HandlerConfig struct {
	Store      *Store
	Challenges *ChallengeStore
	Issuer     *CredentialIssuer
	Logger     *slog.Logger

	// OnPending, when non-nil, is called synchronously with a copy of every
	// NEWLY created (pending) enrollment record, after the store Create
	// succeeds. Idempotent resubmits of an already-pending enrollment do NOT
	// fire it. The master wires it to emit the
	// zester.event._master.enroll.pending.<id> reactor event; implementations
	// must be fast and must never fail the enrollment.
	OnPending func(Record)

	// Bootstrap, when non-nil, enables GET /api/v1/enroll/ca serving the CA
	// trust bundle + fleet NATS endpoints (embedded-CA / discovery mode).
	Bootstrap BootstrapProvider

	// CARootPin, when non-nil, returns the master's own CA root SPKI pin.
	// handleEnroll compares each peel's reported TrustedCASPKI against it to
	// flag first-contact MITM (TrustMismatch). Nil in external mode (no
	// embedded CA to compare against).
	CARootPin func() string
}

// NewHandler creates enrollment HTTP handlers.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handler{
		store:      cfg.Store,
		challenges: cfg.Challenges,
		issuer:     cfg.Issuer,
		logger:     cfg.Logger,
		onPending:  cfg.OnPending,
		bootstrap:  cfg.Bootstrap,
		caRootPin:  cfg.CARootPin,
	}
}

// RegisterRoutes registers peel-facing enrollment endpoints on the given mux.
// Admin operations (approve, reject, revoke, list) are NOT exposed over HTTP.
// They are performed by the CLI directly via NATS KV, which is already
// authenticated by the admin's nkey/JWT credentials.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/enroll/nonce", h.handleNonce)
	mux.HandleFunc("POST /api/v1/enroll", h.handleEnroll)
	mux.HandleFunc("GET /api/v1/enroll/{id}/status", h.handleStatus)
	mux.HandleFunc("GET /api/v1/enroll/{id}/stream", h.handleStream)
	mux.HandleFunc("GET /api/v1/enroll/{id}/creds", h.handleCreds)
	h.registerBootstrapRoute(mux)
}

// --- Request/Response types ---

// EnrollRequest is the request body for POST /api/v1/enroll.
type EnrollRequest struct {
	PeelID         string            `json:"peel_id"`
	PublicKey      string            `json:"public_key"`
	CurvePublicKey string            `json:"curve_public_key"`
	Hostname       string            `json:"hostname"`
	ChallengeID    string            `json:"challenge_id"`
	Signature      []byte            `json:"signature"`
	Metadata       map[string]string `json:"metadata,omitempty"`

	// TrustedCASPKI + TrustSignature bind the CA the peel trusted for this
	// TLS connection to its Ed25519 key (additive; pre-feature peels omit
	// them). The signature covers the challenge and a capability marker, so
	// a relay MITM cannot strip the fields and masquerade as a legacy peel.
	TrustedCASPKI  string `json:"trusted_ca_spki,omitempty"`
	TrustSignature []byte `json:"trust_signature,omitempty"`
}

// EnrollResponse is the response body for POST /api/v1/enroll.
type EnrollResponse struct {
	ID      string `json:"id"`
	PeelID  string `json:"peel_id"`
	State   State  `json:"state"`
	Message string `json:"message"`
}

// NonceResponse is the response body for GET /api/v1/enroll/nonce.
type NonceResponse struct {
	ChallengeID string    `json:"challenge_id"`
	Challenge   []byte    `json:"challenge"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// StatusResponse is the response body for GET /api/v1/enroll/{id}/status.
type StatusResponse struct {
	ID      string `json:"id"`
	PeelID  string `json:"peel_id"`
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
}

// CredsResponse is the response body for GET /api/v1/enroll/{id}/creds.
type CredsResponse struct {
	PeelID    string `json:"peel_id"`
	CredsData string `json:"creds_data"`
	ExpiresAt string `json:"expires_at"`
}

// --- Handlers ---

// handleNonce issues a new challenge nonce.
// GET /api/v1/enroll/nonce?peel_id=<id>&public_key=<key>
func (h *Handler) handleNonce(w http.ResponseWriter, r *http.Request) {
	peelID := r.URL.Query().Get("peel_id")
	publicKey := r.URL.Query().Get("public_key")

	if peelID == "" || publicKey == "" {
		h.writeError(w, http.StatusBadRequest, "peel_id and public_key are required")
		return
	}

	if err := ValidatePeelID(peelID); err != nil {
		h.writeError(w, http.StatusBadRequest, peelIDErrorMessage(err))
		return
	}
	if err := ValidatePublicKey(publicKey); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid public_key")
		return
	}

	rec, err := h.challenges.Issue(r.Context(), peelID, publicKey)
	if err != nil {
		h.logger.Error("enroll: issue challenge", "error", err)
		h.writeError(w, http.StatusInternalServerError, "failed to issue challenge")
		return
	}

	h.writeJSON(w, http.StatusOK, NonceResponse{
		ChallengeID: rec.ChallengeID,
		Challenge:   rec.Challenge,
		ExpiresAt:   rec.ExpiresAt,
	})
}

// handleEnroll processes a new enrollment request.
// POST /api/v1/enroll
func (h *Handler) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req EnrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate fields. The peel ID check is the enforcement point for the
	// subject-token charset rules (see ValidatePeelID): a Record is only
	// ever created from a request here, so nothing violating them can enter
	// the fleet.
	if err := ValidatePeelID(req.PeelID); err != nil {
		h.writeError(w, http.StatusBadRequest, peelIDErrorMessage(err))
		return
	}
	if err := ValidatePublicKey(req.PublicKey); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid public_key")
		return
	}
	if err := ValidateCurvePublicKey(req.CurvePublicKey); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid curve_public_key")
		return
	}
	if req.ChallengeID == "" {
		h.writeError(w, http.StatusBadRequest, "challenge_id is required")
		return
	}
	if len(req.Signature) == 0 {
		h.writeError(w, http.StatusBadRequest, "signature is required")
		return
	}

	// Consume and verify challenge.
	challenge, err := h.challenges.Consume(r.Context(), req.ChallengeID)
	if err != nil {
		h.logger.Warn("enroll: challenge verification failed",
			"peel_id", req.PeelID,
			"source_ip", remoteIP(r),
			"error", err,
		)
		h.writeError(w, http.StatusUnauthorized, "challenge verification failed")
		return
	}

	// Verify the challenge was issued for this peel and key.
	if challenge.PeelID != req.PeelID || challenge.PublicKey != req.PublicKey {
		h.logger.Warn("enroll: challenge binding mismatch",
			"peel_id", req.PeelID,
			"source_ip", remoteIP(r),
		)
		h.writeError(w, http.StatusBadRequest, "challenge binding mismatch")
		return
	}

	// Verify the signature (binds both public_key and curve_public_key to the proof).
	if err := VerifyEnrollSignature(req.PublicKey, challenge.Challenge, req.Signature, req.CurvePublicKey); err != nil {
		h.logger.Warn("enroll: signature verification failed",
			"peel_id", req.PeelID,
			"source_ip", remoteIP(r),
		)
		h.writeError(w, http.StatusUnauthorized, "signature verification failed")
		return
	}

	// Trust binding. On an embedded-CA master (a non-public root), the
	// binding is REQUIRED: every legitimate peel that completes the TLS
	// handshake resolved a non-empty trusted-CA SPKI and sends it, so a
	// missing binding means a relay MITM stripped the fields to masquerade as
	// a legacy peel — reject it. A present binding must verify (a
	// present-but-forged binding is an integrity failure). When the master
	// has a root to compare against, the comparison result is recorded
	// (TrustChecked) and mismatches are flagged for the approval gate.
	masterPin := ""
	if h.caRootPin != nil {
		masterPin = h.caRootPin()
	}
	trustMismatch := false
	trustChecked := false
	if req.TrustedCASPKI == "" {
		if masterPin != "" {
			h.logger.Warn("enroll: missing trust binding on an embedded-CA master (possible relay MITM strip)",
				"peel_id", req.PeelID, "source_ip", remoteIP(r))
			h.writeError(w, http.StatusUnauthorized, "trust binding required")
			return
		}
		// External-CA master: no root to compare against; nothing to check.
	} else {
		if err := VerifyTrustBinding(req.PublicKey, challenge.Challenge, req.TrustedCASPKI, req.TrustSignature); err != nil {
			h.logger.Warn("enroll: trust-binding verification failed",
				"peel_id", req.PeelID, "source_ip", remoteIP(r))
			h.writeError(w, http.StatusUnauthorized, "trust binding verification failed")
			return
		}
		if masterPin != "" {
			trustChecked = true
			if !strings.EqualFold(masterPin, req.TrustedCASPKI) {
				trustMismatch = true
				h.logger.Warn("enroll: TRUST MISMATCH — peel reported a CA that is not this master's root (possible first-contact MITM)",
					"peel_id", req.PeelID, "source_ip", remoteIP(r),
					"reported_ca", req.TrustedCASPKI, "master_ca", masterPin)
			}
		}
	}

	// Check for existing enrollment.
	existing, err := h.store.FindByPeelID(r.Context(), req.PeelID)
	if err != nil {
		h.logger.Error("enroll: find existing", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing != nil {
		switch existing.State {
		case StatePending:
			// Already pending -- return existing record (idempotent).
			h.writeJSON(w, http.StatusOK, EnrollResponse{
				ID:      existing.ID,
				PeelID:  existing.PeelID,
				State:   existing.State,
				Message: "Enrollment already pending.",
			})
			return
		case StateApproved, StateIssued, StateActive:
			h.writeError(w, http.StatusConflict, "peel already has an active enrollment")
			return
		case StateRejected, StateRevoked:
			// Allow re-enrollment: release the old peel index so the new
			// record can atomically claim it via Store.Create.
			if err := h.store.ReleaseIndex(r.Context(), req.PeelID); err != nil {
				h.logger.Warn("enroll: failed to release old peel index for re-enrollment",
					"peel_id", req.PeelID, "error", err)
				// Proceed anyway — Store.Create will fail if index still exists,
				// which is the safe outcome.
			}
		}
	}

	// Create enrollment record.
	now := time.Now().UTC()
	rec := &Record{
		ID:             "enr-" + ksuid.New().String(),
		PeelID:         req.PeelID,
		PublicKey:      req.PublicKey,
		CurvePublicKey: req.CurvePublicKey,
		State:          StatePending,
		Hostname:       req.Hostname,
		Metadata:       req.Metadata,
		CreatedAt:      now,
		UpdatedAt:      now,
		RemoteAddr:     remoteIP(r),
		TrustedCASPKI:  req.TrustedCASPKI,
		TrustMismatch:  trustMismatch,
		TrustChecked:   trustChecked,
	}

	if err := h.store.Create(r.Context(), rec); err != nil {
		h.logger.Error("enroll: create record", "error", err)
		h.writeError(w, http.StatusInternalServerError, "failed to create enrollment")
		return
	}

	// Notify observers of the new pending enrollment (e.g. the master's
	// enroll/pending reactor event). Nil-safe and best-effort by contract.
	if h.onPending != nil {
		h.onPending(*rec)
	}

	h.logger.Info("enrollment.verify.success",
		"enrollment_id", rec.ID,
		"peel_id", rec.PeelID,
		"source_ip", remoteIP(r),
	)

	h.writeJSON(w, http.StatusCreated, EnrollResponse{
		ID:      rec.ID,
		PeelID:  rec.PeelID,
		State:   rec.State,
		Message: "Enrollment submitted. Awaiting operator approval.",
	})
}

// handleStatus returns the current state of an enrollment.
// GET /api/v1/enroll/{id}/status
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		h.writeError(w, http.StatusBadRequest, "enrollment id required")
		return
	}

	rec, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.writeError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	h.writeJSON(w, http.StatusOK, StatusResponse{
		ID:     rec.ID,
		PeelID: rec.PeelID,
		State:  rec.State,
	})
}

// handleStream provides a Server-Sent Events (SSE) stream of enrollment
// state changes. The client receives the current state immediately, then
// live updates as the enrollment state changes (e.g., pending → approved).
// The stream closes when a terminal state is reached, the max duration is
// exceeded, or the client disconnects.
//
// GET /api/v1/enroll/{id}/stream
func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		h.writeError(w, http.StatusBadRequest, "enrollment id required")
		return
	}

	// Verify enrollment exists.
	rec, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.writeError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	// Check that the ResponseWriter supports flushing (required for SSE).
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Start KV watcher before writing headers to avoid missing updates
	// between the initial Get and the watcher start.
	watcher, err := h.store.WatchRecord(r.Context(), id)
	if err != nil {
		h.logger.Error("enroll: start watcher for SSE stream", "enrollment_id", id, "error", err)
		h.writeError(w, http.StatusInternalServerError, "failed to start stream")
		return
	}
	defer watcher.Stop()

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Use ResponseController to extend the write deadline per-flush.
	// The server's default WriteTimeout (10s) would kill SSE connections.
	rc := http.NewResponseController(w)

	// Send the initial state.
	h.writeSSEState(w, rc, flusher, rec)
	lastState := rec.State

	// If already in a terminal state, close immediately.
	if isTerminalState(rec.State) {
		return
	}

	maxDuration := h.maxStreamDuration
	if maxDuration == 0 {
		maxDuration = 30 * time.Minute
	}

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	deadline := time.NewTimer(maxDuration)
	defer deadline.Stop()

	for {
		select {
		case <-r.Context().Done():
			// Client disconnected.
			return

		case <-deadline.C:
			// Max stream duration exceeded.
			rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
			fmt.Fprintf(w, "event: timeout\ndata: {\"message\":\"stream duration exceeded\"}\n\n")
			flusher.Flush()
			return

		case <-heartbeat.C:
			// SSE comment — keeps connection alive through NAT/proxies.
			rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()

		case entry, ok := <-watcher.Updates():
			if !ok {
				// Watcher channel closed.
				return
			}
			if entry == nil {
				// Nil sentinel after initial replay — skip.
				continue
			}

			var updated Record
			if err := bus.Decode(entry.Value(), &updated); err != nil {
				h.logger.Warn("enroll: decode SSE update", "enrollment_id", id, "error", err)
				continue
			}

			// WatchRecord replays the current value before live updates.
			// Suppress duplicate state events to keep SSE consumers deterministic.
			if updated.State == lastState {
				continue
			}

			h.writeSSEState(w, rc, flusher, &updated)
			lastState = updated.State

			if isTerminalState(updated.State) {
				return
			}
		}
	}
}

// writeSSEState writes a single SSE state event.
func (h *Handler) writeSSEState(w http.ResponseWriter, rc *http.ResponseController, flusher http.Flusher, rec *Record) {
	rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
	data, _ := json.Marshal(StatusResponse{
		ID:     rec.ID,
		PeelID: rec.PeelID,
		State:  rec.State,
	})
	fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
	flusher.Flush()
}

// isTerminalState returns true for states where the SSE stream should close.
func isTerminalState(s State) bool {
	switch s {
	case StateApproved, StateRejected, StateRevoked, StateIssued, StateActive:
		return true
	default:
		return false
	}
}

// handleCreds issues and returns credentials for an approved enrollment.
// The peel must prove key ownership by signing the enrollment ID.
// Credentials are single-use: the enrollment transitions to Issued via CAS
// BEFORE the JWT is returned. A concurrent request will fail the CAS and
// receive a 409 Conflict.
// GET /api/v1/enroll/{id}/creds
func (h *Handler) handleCreds(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		h.writeError(w, http.StatusBadRequest, "enrollment id required")
		return
	}

	rec, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.writeError(w, http.StatusNotFound, "enrollment not found")
		return
	}

	if rec.State != StateApproved {
		h.writeError(w, http.StatusForbidden, "enrollment not in approved state")
		return
	}

	// Verify the peel proves key ownership.
	authHeader := r.Header.Get("Authorization")
	if err := VerifyCredsSignature(authHeader, id, rec.PublicKey); err != nil {
		h.logger.Warn("enroll: creds auth failed",
			"enrollment_id", id,
			"source_ip", remoteIP(r),
		)
		h.writeError(w, http.StatusUnauthorized, "authentication failed")
		return
	}

	// Issue credentials.
	creds, err := h.issuer.Issue(rec.PeelID, rec.PublicKey)
	if err != nil {
		h.logger.Error("enroll: issue credentials", "error", err)
		h.writeError(w, http.StatusInternalServerError, "failed to issue credentials")
		return
	}

	// Transition to Issued BEFORE returning credentials (single-use enforcement).
	// The CAS update ensures only one concurrent request succeeds. If this
	// fails (concurrent download or state already changed), reject the request.
	if _, err := h.store.MarkIssued(r.Context(), id, creds.ExpiresAt); err != nil {
		h.logger.Warn("enroll: credential delivery contention",
			"enrollment_id", id,
			"source_ip", remoteIP(r),
			"error", err,
		)
		h.writeError(w, http.StatusConflict, "credentials already issued")
		return
	}

	h.logger.Info("enrollment.credential.downloaded",
		"enrollment_id", id,
		"peel_id", rec.PeelID,
		"source_ip", remoteIP(r),
	)

	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, http.StatusOK, CredsResponse{
		PeelID:    rec.PeelID,
		CredsData: EncodeJWTForTransport(creds.JWT),
		ExpiresAt: creds.ExpiresAt.Format(time.RFC3339),
	})
}

// --- Helpers ---

func (h *Handler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, code int, message string) {
	h.writeJSON(w, code, map[string]string{"error": message})
}

// peelIDErrorMessage formats a ValidatePeelID error for an HTTP response:
// the specific reason (dots, wildcards, leading underscore, length, charset)
// is surfaced so operators can fix the ID without reading server logs.
func peelIDErrorMessage(err error) string {
	return "invalid peel_id: " + strings.TrimPrefix(err.Error(), "enroll: ")
}

// remoteIP extracts the client IP from the request.
// It only uses the TCP connection's remote address. X-Forwarded-For is
// NOT trusted because the enrollment API cannot verify the connecting
// IP is a trusted proxy. Trusting X-Forwarded-For would allow attackers
// to spoof their IP to bypass per-IP rate limiting.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// securityHeaders is HTTP middleware that adds security headers to
// all enrollment responses per the security specification.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// RateLimitConfig tunes the per-IP token bucket used by
// RateLimitMiddlewareWithConfig.
type RateLimitConfig struct {
	// Capacity is the bucket size (burst allowance).
	Capacity float64

	// RefillPerSec is the sustained tokens-per-second refill rate.
	RefillPerSec float64
}

// RateLimitMiddleware provides per-IP token bucket rate limiting with the
// strict enrollment defaults (burst 10, 1 request per 10 seconds). Suitable
// only for the unauthenticated enrollment endpoints; API routes need a much
// higher budget (see RateLimitMiddlewareWithConfig).
func RateLimitMiddleware(next http.Handler, logger *slog.Logger) http.Handler {
	return RateLimitMiddlewareWithConfig(next, logger, RateLimitConfig{
		Capacity:     10,
		RefillPerSec: 1.0 / 10.0,
	})
}

// RateLimitMiddlewareWithConfig provides per-IP token bucket rate limiting
// with configurable capacity and refill rate. The bucket map is protected by
// a mutex for concurrent access safety.
func RateLimitMiddlewareWithConfig(next http.Handler, logger *slog.Logger, cfg RateLimitConfig) http.Handler {
	type bucket struct {
		tokens   float64
		lastFill time.Time
	}

	var (
		mu      sync.Mutex
		buckets = make(map[string]*bucket)
	)

	capacity := cfg.Capacity
	if capacity <= 0 {
		capacity = 10
	}
	refillRate := cfg.RefillPerSec
	if refillRate <= 0 {
		refillRate = 1.0 / 10.0
	}

	const (
		sweepSize  = 5000 // trigger sweep when map exceeds this size
		staleAfter = 5 * time.Minute
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)

		mu.Lock()
		now := time.Now()

		// Evict stale entries when the map grows beyond sweepSize.
		if len(buckets) > sweepSize {
			cutoff := now.Add(-staleAfter)
			for k, v := range buckets {
				if v.lastFill.Before(cutoff) {
					delete(buckets, k)
				}
			}
		}

		b, ok := buckets[ip]
		if !ok {
			b = &bucket{tokens: capacity, lastFill: now}
			buckets[ip] = b
		}

		// Refill tokens based on elapsed time.
		elapsed := now.Sub(b.lastFill).Seconds()
		b.tokens += elapsed * refillRate
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.lastFill = now

		if b.tokens < 1 {
			mu.Unlock()
			if logger != nil {
				logger.Warn("enrollment.ratelimit.exceeded",
					"source_ip", ip,
				)
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(1.0/refillRate)))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}

		b.tokens--
		mu.Unlock()

		next.ServeHTTP(w, r)
	})
}
