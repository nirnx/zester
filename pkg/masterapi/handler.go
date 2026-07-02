package masterapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/segmentio/ksuid"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/job"
	"github.com/ptorbus/zester/pkg/target"
)

type contextKey string

const (
	ctxKeyUsername  contextKey = "masterapi.username"
	ctxKeyRequestID contextKey = "masterapi.request_id"
)

const defaultTimeout = 60 * time.Second

// TokenEntry maps one API username to a token file.
type TokenEntry struct {
	Username  string
	TokenFile string
}

// HandlerConfig configures the master REST API handler.
type HandlerConfig struct {
	JobManager  *job.Manager
	JS          bus.JetStreamAPI
	Lister      target.PeelLister
	EnrollStore *enroll.Store
	Tokens      []TokenEntry
	DocsEnabled bool
	Logger      *slog.Logger
}

// Handler exposes REST routes for jobs and enrollment admin.
type Handler struct {
	jobMgr      *job.Manager
	js          bus.JetStreamAPI
	lister      target.PeelLister
	enrollStore *enroll.Store
	tokens      []TokenEntry
	docsEnabled bool
	logger      *slog.Logger
}

// NewHandler creates a master API handler.
func NewHandler(cfg HandlerConfig) *Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	for _, t := range cfg.Tokens {
		if t.TokenFile == "" {
			continue
		}
		info, err := os.Stat(t.TokenFile)
		switch {
		case err != nil:
			logger.Warn("api token file not readable at startup",
				"username", t.Username, "path", t.TokenFile, "error", err)
		case info.Mode().Perm()&0o077 != 0:
			logger.Warn("api token file is accessible by group/others; tighten to 0600",
				"username", t.Username, "path", t.TokenFile, "mode", info.Mode().Perm().String())
		}
	}
	return &Handler{
		jobMgr:      cfg.JobManager,
		js:          cfg.JS,
		lister:      cfg.Lister,
		enrollStore: cfg.EnrollStore,
		tokens:      cfg.Tokens,
		docsEnabled: cfg.DocsEnabled,
		logger:      logger,
	}
}

// RegisterRoutes registers docs/spec routes and protected API routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	if h.docsEnabled {
		mux.Handle("GET /api/v1/docs", h.wrapPublic(http.HandlerFunc(h.handleSwaggerUI)))
		subFS, _ := fs.Sub(swaggerUIFS, "swagger-ui")
		mux.Handle("GET /api/v1/docs/{file...}", h.wrapPublic(
			http.StripPrefix("/api/v1/docs/", http.FileServerFS(subFS)),
		))
		mux.Handle("GET /api/v1/openapi.yaml", h.wrapPublic(http.HandlerFunc(h.handleOpenAPISpecYAML)))
		mux.Handle("GET /api/v1/openapi.json", h.wrapPublic(http.HandlerFunc(h.handleOpenAPISpecJSON)))
	}

	if len(h.tokens) == 0 {
		return
	}

	mux.Handle("POST /api/v1/jobs", h.wrapProtected(http.HandlerFunc(h.handleDispatch)))
	mux.Handle("GET /api/v1/jobs/{jid}", h.wrapProtected(http.HandlerFunc(h.handleGetJob)))

	mux.Handle("GET /api/v1/enrollments", h.wrapProtected(http.HandlerFunc(h.handleListEnrollments)))
	mux.Handle("POST /api/v1/enrollments/{id}/approve", h.wrapProtected(http.HandlerFunc(h.handleApproveEnrollment)))
	mux.Handle("POST /api/v1/enrollments/{id}/reject", h.wrapProtected(http.HandlerFunc(h.handleRejectEnrollment)))
	mux.Handle("POST /api/v1/enrollments/{id}/revoke", h.wrapProtected(http.HandlerFunc(h.handleRevokeEnrollment)))
}

func (h *Handler) wrapPublic(next http.Handler) http.Handler {
	return requestIDMiddleware(h.logger)(requestLoggingMiddleware(h.logger)(next))
}

func (h *Handler) wrapProtected(next http.Handler) http.Handler {
	return requestIDMiddleware(h.logger)(h.tokenAuthMiddleware(requestLoggingMiddleware(h.logger)(next)))
}

func (h *Handler) tokenAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			h.logAuthFailure(r, "missing bearer token")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		provided := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		if provided == "" {
			h.logAuthFailure(r, "empty bearer token")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		username, ok := h.matchToken(provided)
		if !ok {
			h.logAuthFailure(r, "invalid token")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeyUsername, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) matchToken(provided string) (string, bool) {
	for _, t := range h.tokens {
		if t.Username == "" || t.TokenFile == "" {
			continue
		}
		data, err := os.ReadFile(t.TokenFile)
		if err != nil {
			continue
		}
		expected := strings.TrimSpace(string(data))
		if expected == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1 {
			return t.Username, true
		}
	}
	return "", false
}

func (h *Handler) logAuthFailure(r *http.Request, reason string) {
	h.logger.Warn("http.request.unauthorized",
		"request_id", requestIDFromContext(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"remote_ip", remoteIP(r),
		"user_agent", r.UserAgent(),
		"reason", reason,
	)
}

type DispatchRequest struct {
	Target   string            `json:"target,omitempty"`
	Targets  []string          `json:"targets,omitempty"`
	Function string            `json:"function"`
	Args     map[string]any    `json:"args,omitempty"`
	Timeout  string            `json:"timeout,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DispatchResponse struct {
	JID     string   `json:"jid"`
	Targets []string `json:"targets"`
	Status  string   `json:"status"`
}

type ReturnItem struct {
	PeelID     string        `json:"peel_id"`
	Success    bool          `json:"success"`
	ReturnData any           `json:"return_data,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration"`
	Timestamp  time.Time     `json:"timestamp"`
}

type JobStatusResponse struct {
	JID      string       `json:"jid"`
	Function string       `json:"function"`
	Targets  []string     `json:"targets"`
	Status   string       `json:"status"`
	Created  time.Time    `json:"created"`
	Updated  time.Time    `json:"updated"`
	Returns  []ReturnItem `json:"returns"`
	Complete bool         `json:"complete"`
}

type EnrollmentItem struct {
	ID        string            `json:"id"`
	PeelID    string            `json:"peel_id"`
	Hostname  string            `json:"hostname,omitempty"`
	State     string            `json:"state"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func (h *Handler) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if h.jobMgr == nil || h.lister == nil {
		writeError(w, http.StatusInternalServerError, "job API not configured")
		return
	}

	var req DispatchRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Function == "" {
		writeError(w, http.StatusBadRequest, "function is required")
		return
	}

	timeout := defaultTimeout
	if req.Timeout != "" {
		d, err := time.ParseDuration(req.Timeout)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid timeout")
			return
		}
		timeout = d
	}

	targets := req.Targets
	if len(targets) == 0 {
		if strings.TrimSpace(req.Target) == "" {
			writeError(w, http.StatusBadRequest, "target is required")
			return
		}
		tt := target.DetectType(req.Target)
		resolved, err := target.Resolve(r.Context(), req.Target, tt, h.lister)
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to resolve target")
			return
		}
		targets = resolved
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "no peels matched target")
		return
	}

	j := job.NewJob(req.Function, req.Args, targets, timeout)
	j.Metadata = req.Metadata
	j.User = usernameFromContext(r.Context())

	if err := h.jobMgr.Dispatch(r.Context(), j); err != nil {
		if strings.Contains(err.Error(), "conflict") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("X-Job-ID", j.JID)
	writeJSON(w, http.StatusAccepted, DispatchResponse{
		JID:     j.JID,
		Targets: targets,
		Status:  string(j.Status),
	})
}

func (h *Handler) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if h.jobMgr == nil {
		writeError(w, http.StatusInternalServerError, "job API not configured")
		return
	}
	jid := r.PathValue("jid")
	if jid == "" {
		writeError(w, http.StatusBadRequest, "jid is required")
		return
	}

	w.Header().Set("X-Job-ID", jid)

	j, err := h.jobMgr.GetJob(r.Context(), jid)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load job")
		return
	}

	rets, err := h.jobMgr.GetReturns(r.Context(), jid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load returns")
		return
	}

	items := make([]ReturnItem, 0, len(rets))
	for _, ret := range rets {
		items = append(items, ReturnItem{
			PeelID:     ret.PeelID,
			Success:    ret.Success,
			ReturnData: ret.ReturnData,
			Error:      ret.Error,
			Duration:   ret.Duration,
			Timestamp:  ret.Timestamp,
		})
	}

	writeJSON(w, http.StatusOK, JobStatusResponse{
		JID:      j.JID,
		Function: j.Function,
		Targets:  j.Targets,
		Status:   string(j.Status),
		Created:  j.Created,
		Updated:  j.Updated,
		Returns:  items,
		Complete: j.IsTerminal(),
	})
}

func (h *Handler) handleListEnrollments(w http.ResponseWriter, r *http.Request) {
	if h.enrollStore == nil {
		writeError(w, http.StatusInternalServerError, "enrollment API not configured")
		return
	}

	stateParam := strings.TrimSpace(r.URL.Query().Get("state"))
	if stateParam == "" {
		stateParam = string(enroll.StatePending)
	}

	var filter *enroll.State
	if stateParam != "all" {
		st, ok := enroll.ParseState(stateParam)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid state")
			return
		}
		filter = &st
	}

	records, err := h.enrollStore.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list enrollments")
		return
	}

	out := make([]EnrollmentItem, 0, len(records))
	for _, rec := range records {
		out = append(out, EnrollmentItem{
			ID:        rec.ID,
			PeelID:    rec.PeelID,
			Hostname:  rec.Hostname,
			State:     string(rec.State),
			CreatedAt: rec.CreatedAt,
			Metadata:  rec.Metadata,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleApproveEnrollment(w http.ResponseWriter, r *http.Request) {
	if h.enrollStore == nil {
		writeError(w, http.StatusInternalServerError, "enrollment API not configured")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "enrollment id is required")
		return
	}

	w.Header().Set("X-Enrollment-ID", id)

	username := usernameFromContext(r.Context())
	rec, err := h.enrollStore.Approve(r.Context(), id, username)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "enrollment not found")
			return
		}
		if strings.Contains(err.Error(), "cannot approve") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to approve enrollment")
		return
	}

	writeJSON(w, http.StatusOK, EnrollmentItem{
		ID:        rec.ID,
		PeelID:    rec.PeelID,
		Hostname:  rec.Hostname,
		State:     string(rec.State),
		CreatedAt: rec.CreatedAt,
		Metadata:  rec.Metadata,
	})
}

// EnrollmentDecisionRequest is the optional JSON body for the reject and
// revoke endpoints, carrying a human-readable reason recorded on the
// enrollment record.
type EnrollmentDecisionRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (h *Handler) handleRejectEnrollment(w http.ResponseWriter, r *http.Request) {
	h.handleEnrollmentDecision(w, r, "reject", func(ctx context.Context, id, username, reason string) (*enroll.Record, error) {
		return h.enrollStore.Reject(ctx, id, username, reason)
	})
}

func (h *Handler) handleRevokeEnrollment(w http.ResponseWriter, r *http.Request) {
	h.handleEnrollmentDecision(w, r, "revoke", func(ctx context.Context, id, username, reason string) (*enroll.Record, error) {
		return h.enrollStore.Revoke(ctx, id, username, reason)
	})
}

// handleEnrollmentDecision implements the shared reject/revoke flow,
// mirroring handleApproveEnrollment (same auth via the route wrapper, same
// error mapping) plus an optional JSON request body carrying a reason. The
// authenticated API username is recorded as the decider.
func (h *Handler) handleEnrollmentDecision(w http.ResponseWriter, r *http.Request, verb string, op func(ctx context.Context, id, username, reason string) (*enroll.Record, error)) {
	if h.enrollStore == nil {
		writeError(w, http.StatusInternalServerError, "enrollment API not configured")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "enrollment id is required")
		return
	}

	w.Header().Set("X-Enrollment-ID", id)

	// The body is optional: an empty body means no reason.
	var req EnrollmentDecisionRequest
	if err := decodeJSON(r.Body, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	username := usernameFromContext(r.Context())
	rec, err := op(r.Context(), id, username, req.Reason)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "enrollment not found")
			return
		}
		if strings.Contains(err.Error(), "cannot "+verb) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to "+verb+" enrollment")
		return
	}

	writeJSON(w, http.StatusOK, EnrollmentItem{
		ID:        rec.ID,
		PeelID:    rec.PeelID,
		Hostname:  rec.Hostname,
		State:     string(rec.State),
		CreatedAt: rec.CreatedAt,
		Metadata:  rec.Metadata,
	})
}

func (h *Handler) handleOpenAPISpecYAML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiYAML)
}

func (h *Handler) handleOpenAPISpecJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openapiJSON)
}

func (h *Handler) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(swaggerHTML)
}

func requestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

func usernameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUsername).(string)
	return v
}

func remoteIP(r *http.Request) string {
	return r.RemoteAddr
}

func decodeJSON(body io.ReadCloser, v any) error {
	defer body.Close()
	dec := json.NewDecoder(io.LimitReader(body, 1<<20))
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "key not found")
}

func requestIDMiddleware(_ *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
			if reqID == "" {
				reqID = ksuid.New().String()
			}

			w.Header().Set("X-Request-ID", reqID)
			ctx := context.WithValue(r.Context(), ctxKeyRequestID, reqID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func requestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusCapturingWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)

			attrs := []any{
				"request_id", requestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes_out", sw.bytes,
				"remote_ip", remoteIP(r),
				"user_agent", r.UserAgent(),
				"auth_username", usernameFromContext(r.Context()),
			}
			if jid := sw.Header().Get("X-Job-ID"); jid != "" {
				attrs = append(attrs, "jid", jid)
			}
			if eid := sw.Header().Get("X-Enrollment-ID"); eid != "" {
				attrs = append(attrs, "enrollment_id", eid)
			}
			logger.Info("http.request.complete", attrs...)
		})
	}
}
