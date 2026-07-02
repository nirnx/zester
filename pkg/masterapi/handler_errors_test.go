package masterapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
	"github.com/ptorbus/zester/pkg/bus/bustest"
	"github.com/ptorbus/zester/pkg/enroll"
	"github.com/ptorbus/zester/pkg/job"
	"github.com/ptorbus/zester/pkg/target"
)

// --- dispatch error branches -------------------------------------------------

func TestDispatch_InvalidJSONBody(t *testing.T) {
	env := setupTestEnv(t)

	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/jobs",
		strings.NewReader("{not valid json"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "invalid JSON body" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestDispatch_MissingFunction(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target": "web-*",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "function is required" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestDispatch_MissingTarget(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"function": "test.ping",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "target is required" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}

	// Whitespace-only target is treated as missing.
	resp = env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target":   "   ",
		"function": "test.ping",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for blank target, got %d", resp.StatusCode)
	}
}

func TestDispatch_UnresolvableTarget(t *testing.T) {
	env := setupTestEnv(t)

	// Invalid PCRE expression fails matcher construction inside target.Resolve.
	resp := env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target":   "E@[invalid",
		"function": "test.ping",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "failed to resolve target" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestDispatch_NoPeelsMatched(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target":   "nomatch-*",
		"function": "test.ping",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "no peels matched target" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

// conflictJobsKV forces job.Manager.Dispatch down the conflict path: Create
// always fails as if the key existed, and Get returns a pre-seeded job whose
// dispatch intent differs from any new request.
type conflictJobsKV struct {
	bus.KV
	seedKey string
}

func (c *conflictJobsKV) Create(context.Context, string, []byte) (uint64, error) {
	return 0, errors.New("nats: key exists")
}

func (c *conflictJobsKV) Get(ctx context.Context, _ string) (bus.KVEntry, error) {
	return c.KV.Get(ctx, c.seedKey)
}

type conflictJS struct {
	bus.JetStreamAPI
	jobsKV bus.KV
}

func (c *conflictJS) KeyValue(ctx context.Context, bucket string) (bus.KV, error) {
	if bucket == bus.BucketJobs {
		return c.jobsKV, nil
	}
	return c.JetStreamAPI.KeyValue(ctx, bucket)
}

func TestDispatch_Conflict(t *testing.T) {
	ctx := context.Background()

	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	factsKV, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		t.Fatalf("get facts bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, factsKV, "web-01", map[string]any{"os": "ubuntu"}); err != nil {
		t.Fatalf("seed facts: %v", err)
	}

	jobsKV, err := bus.GetBucket(ctx, js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	existing := job.NewJob("other.function", nil, []string{"web-01"}, time.Second)
	if _, err := bus.KVPut(ctx, jobsKV, "conflict-seed", existing); err != nil {
		t.Fatalf("seed conflicting job: %v", err)
	}

	wrapped := &conflictJS{
		JetStreamAPI: js,
		jobsKV:       &conflictJobsKV{KV: jobsKV, seedKey: "conflict-seed"},
	}
	jobMgr := job.NewManager(bustest.NewFakePubSub(), wrapped, "test-master", nil)
	t.Cleanup(jobMgr.Shutdown)

	tokenFile := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(tokenFile, []byte("test-secret"), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	h := NewHandler(HandlerConfig{
		JobManager: jobMgr,
		JS:         wrapped,
		Lister:     &target.KVPeelLister{JS: js},
		Tokens:     []TokenEntry{{Username: "tester", TokenFile: tokenFile}},
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/jobs",
		strings.NewReader(`{"target":"web-*","function":"test.ping"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if !strings.Contains(out["error"], "conflict") {
		t.Fatalf("expected conflict in error message, got %q", out["error"])
	}
}

func TestDispatch_NotConfigured(t *testing.T) {
	h := NewHandler(HandlerConfig{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader("{}"))
	h.handleDispatch(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "job API not configured") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.handleGetJob(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for get job, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.handleListEnrollments(rec, httptest.NewRequest(http.MethodGet, "/api/v1/enrollments", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for list enrollments, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.handleApproveEnrollment(rec, httptest.NewRequest(http.MethodPost, "/api/v1/enrollments/x/approve", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for approve, got %d", rec.Code)
	}
}

// --- handleGetJob 400/500 branches -------------------------------------------

func TestGetJob_MissingJID(t *testing.T) {
	env := setupTestEnv(t)

	h := NewHandler(HandlerConfig{JobManager: env.jobMgr, JS: env.js})
	rec := httptest.NewRecorder()
	// Direct call bypasses the mux, so PathValue("jid") is empty.
	h.handleGetJob(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "jid is required") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestGetJob_CorruptRecord(t *testing.T) {
	env := setupTestEnv(t)

	jobsKV, err := bus.GetBucket(env.ctx, env.js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	// A msgpack-encoded string cannot decode into a Job struct, so GetJob
	// fails with a non-not-found error.
	garbage, err := bus.Encode("not a job")
	if err != nil {
		t.Fatalf("encode garbage: %v", err)
	}
	if _, err := jobsKV.Put(env.ctx, "corrupt-jid", garbage); err != nil {
		t.Fatalf("put garbage: %v", err)
	}

	resp := env.doReq(t, http.MethodGet, "/api/v1/jobs/corrupt-jid", nil, "test-secret")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "failed to load job" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestGetJob_ReturnsBucketMissing(t *testing.T) {
	env := setupTestEnv(t)

	j := job.NewJob("test.ping", nil, []string{"web-01"}, 5*time.Second)
	jobsKV, err := bus.GetBucket(env.ctx, env.js, bus.BucketJobs)
	if err != nil {
		t.Fatalf("get jobs bucket: %v", err)
	}
	if _, err := bus.KVPut(env.ctx, jobsKV, j.JID, j); err != nil {
		t.Fatalf("put job: %v", err)
	}

	// Deleting the returns bucket makes GetReturns fail after GetJob succeeds.
	if err := env.js.DeleteKeyValue(env.ctx, bus.BucketJobReturns); err != nil {
		t.Fatalf("delete returns bucket: %v", err)
	}

	resp := env.doReq(t, http.MethodGet, "/api/v1/jobs/"+j.JID, nil, "test-secret")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "failed to load returns" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

// --- handleApproveEnrollment error branches -----------------------------------

func TestApproveEnrollment_NotFound(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/enr-missing/approve", nil, "test-secret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "enrollment not found" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestApproveEnrollment_WrongState(t *testing.T) {
	env := setupTestEnv(t)

	now := time.Now().UTC()
	active := &enroll.Record{
		ID:             "enr-already-active",
		PeelID:         "web-01",
		PublicKey:      "UABC",
		CurvePublicKey: "XABC",
		State:          enroll.StateActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := env.enroll.Create(env.ctx, active); err != nil {
		t.Fatalf("create active record: %v", err)
	}

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/"+active.ID+"/approve", nil, "test-secret")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if !strings.Contains(out["error"], "cannot approve") {
		t.Fatalf("expected 'cannot approve' in error, got %q", out["error"])
	}
}

func TestApproveEnrollment_MissingID(t *testing.T) {
	env := setupTestEnv(t)

	h := NewHandler(HandlerConfig{EnrollStore: env.enroll})
	rec := httptest.NewRecorder()
	// Direct call bypasses the mux, so PathValue("id") is empty.
	h.handleApproveEnrollment(rec, httptest.NewRequest(http.MethodPost, "/api/v1/enrollments//approve", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "enrollment id is required") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// --- matchToken degenerate cases ----------------------------------------------

func TestMatchToken(t *testing.T) {
	dir := t.TempDir()
	writeToken := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	goodFile := writeToken("good.token", "sekret\n")
	emptyFile := writeToken("empty.token", "")
	blankFile := writeToken("blank.token", " \t\n")

	h := NewHandler(HandlerConfig{
		Tokens: []TokenEntry{
			{Username: "", TokenFile: goodFile},                              // blank username: skipped
			{Username: "nofile", TokenFile: ""},                              // blank token file: skipped
			{Username: "empty", TokenFile: emptyFile},                        // empty file: fails closed
			{Username: "blank", TokenFile: blankFile},                        // whitespace-only file: fails closed
			{Username: "gone", TokenFile: filepath.Join(dir, "missing.txt")}, // unreadable: skipped
			{Username: "alice", TokenFile: goodFile},
		},
	})

	if user, ok := h.matchToken("wrong-token"); ok {
		t.Fatalf("expected mismatch, got user %q", user)
	}
	if user, ok := h.matchToken(""); ok {
		t.Fatalf("expected empty token to fail closed, got user %q", user)
	}
	// Content is trimmed; the blank-username entry with the same file must
	// be skipped, so the match attributes to alice.
	user, ok := h.matchToken("sekret")
	if !ok || user != "alice" {
		t.Fatalf("expected alice match, got %q ok=%v", user, ok)
	}
}

func TestMatchToken_EmptyFileFailsClosed(t *testing.T) {
	emptyFile := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(emptyFile, nil, 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	h := NewHandler(HandlerConfig{
		Tokens: []TokenEntry{{Username: "u", TokenFile: emptyFile}},
	})
	// Even a token that "matches" the empty content must be rejected.
	if user, ok := h.matchToken(""); ok {
		t.Fatalf("empty token file must fail closed, got user %q", user)
	}
	if user, ok := h.matchToken("anything"); ok {
		t.Fatalf("expected no match against empty token file, got user %q", user)
	}
}

// --- startup permission warnings ----------------------------------------------

func newWarnCapture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), &buf
}

func TestNewHandler_TokenFilePermissionWarning(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	if err := os.Chmod(tokenFile, 0644); err != nil {
		t.Fatalf("chmod token file: %v", err)
	}

	logger, buf := newWarnCapture()
	h := NewHandler(HandlerConfig{
		Logger: logger,
		Tokens: []TokenEntry{{Username: "u", TokenFile: tokenFile}},
	})
	if h == nil {
		t.Fatal("expected handler")
	}
	if !strings.Contains(buf.String(), "accessible by group/others") {
		t.Fatalf("expected permission warning, got: %s", buf.String())
	}
}

func TestNewHandler_TokenFileUnreadableWarning(t *testing.T) {
	logger, buf := newWarnCapture()
	NewHandler(HandlerConfig{
		Logger: logger,
		Tokens: []TokenEntry{{Username: "u", TokenFile: filepath.Join(t.TempDir(), "missing.token")}},
	})
	if !strings.Contains(buf.String(), "not readable at startup") {
		t.Fatalf("expected unreadable warning, got: %s", buf.String())
	}
}

func TestNewHandler_TokenFileTightPermsNoWarning(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	if err := os.Chmod(tokenFile, 0600); err != nil {
		t.Fatalf("chmod token file: %v", err)
	}

	logger, buf := newWarnCapture()
	NewHandler(HandlerConfig{
		Logger: logger,
		Tokens: []TokenEntry{{Username: "u", TokenFile: tokenFile}},
	})
	if strings.Contains(buf.String(), "api token file") {
		t.Fatalf("unexpected warning for 0600 file: %s", buf.String())
	}
}
