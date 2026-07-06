package masterapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/job"
	"github.com/nirnx/zester/pkg/target"
)

type testEnv struct {
	ctx       context.Context
	js        *bustest.FakeJS
	ps        *bustest.FakePubSub
	jobMgr    *job.Manager
	enroll    *enroll.Store
	tokenFile string
	server    *httptest.Server
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	js := bustest.NewFakeJS()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("init storage: %v", err)
	}

	ps := bustest.NewFakePubSub()
	jobMgr := job.NewManager(ps, js, "test-master", nil)
	t.Cleanup(jobMgr.Shutdown)

	enrollStore, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js})
	if err != nil {
		t.Fatalf("new enroll store: %v", err)
	}

	factsKV, err := bus.GetBucket(ctx, js, bus.BucketFacts)
	if err != nil {
		t.Fatalf("get facts bucket: %v", err)
	}
	if _, err := bus.KVPut(ctx, factsKV, "web-01", map[string]any{"os": "ubuntu"}); err != nil {
		t.Fatalf("seed facts web-01: %v", err)
	}
	if _, err := bus.KVPut(ctx, factsKV, "db-01", map[string]any{"os": "debian"}); err != nil {
		t.Fatalf("seed facts db-01: %v", err)
	}

	tokenFile := filepath.Join(t.TempDir(), "api.token")
	if err := os.WriteFile(tokenFile, []byte("test-secret"), 0600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	h := NewHandler(HandlerConfig{
		JobManager:  jobMgr,
		JS:          js,
		Lister:      &target.KVPeelLister{JS: js},
		EnrollStore: enrollStore,
		Tokens: []TokenEntry{
			{Username: "tester", TokenFile: tokenFile},
		},
		DocsEnabled: true,
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &testEnv{
		ctx:       ctx,
		js:        js,
		ps:        ps,
		jobMgr:    jobMgr,
		enroll:    enrollStore,
		tokenFile: tokenFile,
		server:    srv,
	}
}

func (e *testEnv) doReq(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeResp[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestAuthAndTokenRotation(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodGet, "/api/v1/enrollments", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	resp = env.doReq(t, http.MethodGet, "/api/v1/enrollments", nil, "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	resp = env.doReq(t, http.MethodGet, "/api/v1/enrollments", nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if err := os.WriteFile(env.tokenFile, []byte("new-secret"), 0600); err != nil {
		t.Fatalf("rotate token: %v", err)
	}

	resp = env.doReq(t, http.MethodGet, "/api/v1/enrollments", nil, "test-secret")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with old token, got %d", resp.StatusCode)
	}
	resp = env.doReq(t, http.MethodGet, "/api/v1/enrollments", nil, "new-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with new token, got %d", resp.StatusCode)
	}
}

func TestDispatchAndGetJob(t *testing.T) {
	env := setupTestEnv(t)

	dispatch := map[string]any{
		"target":   "web-*",
		"function": "test.ping",
		"timeout":  "10ms",
	}
	resp := env.doReq(t, http.MethodPost, "/api/v1/jobs", dispatch, "test-secret")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	out := decodeResp[DispatchResponse](t, resp)
	if out.JID == "" {
		t.Fatal("expected jid")
	}
	if len(out.Targets) != 1 || out.Targets[0] != "web-01" {
		t.Fatalf("unexpected targets: %v", out.Targets)
	}

	time.Sleep(20 * time.Millisecond)

	resp = env.doReq(t, http.MethodGet, "/api/v1/jobs/"+out.JID, nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	jobResp := decodeResp[JobStatusResponse](t, resp)
	if jobResp.JID != out.JID {
		t.Fatalf("jid mismatch: %s vs %s", jobResp.JID, out.JID)
	}
}

func TestDispatchValidation(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target": "*",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	resp = env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target":   "nomatch-*",
		"function": "test.ping",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	resp = env.doReq(t, http.MethodPost, "/api/v1/jobs", map[string]any{
		"target":   "*",
		"function": "test.ping",
		"timeout":  "bad",
	}, "test-secret")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetJobReturnsFromKV(t *testing.T) {
	env := setupTestEnv(t)

	j := job.NewJob("test.ping", nil, []string{"web-01"}, 5*time.Second)
	j.Status = job.StatusComplete
	jobsKV, _ := bus.GetBucket(env.ctx, env.js, bus.BucketJobs)
	if _, err := bus.KVPut(env.ctx, jobsKV, j.JID, j); err != nil {
		t.Fatalf("put job: %v", err)
	}

	// Returns are stored under per-peel keys ("<jid>.<peelID>") — the only
	// format the master ever writes.
	retsKV, _ := bus.GetBucket(env.ctx, env.js, bus.BucketJobReturns)
	ret := job.Return{
		JID:       j.JID,
		PeelID:    "web-01",
		Success:   true,
		Duration:  100 * time.Millisecond,
		Timestamp: time.Now().UTC(),
	}
	if _, err := bus.KVPut(env.ctx, retsKV, j.JID+"."+ret.PeelID, ret); err != nil {
		t.Fatalf("put return: %v", err)
	}

	resp := env.doReq(t, http.MethodGet, "/api/v1/jobs/"+j.JID, nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	out := decodeResp[JobStatusResponse](t, resp)
	if !out.Complete {
		t.Fatal("expected complete=true")
	}
	if len(out.Returns) != 1 || out.Returns[0].PeelID != "web-01" {
		t.Fatalf("unexpected returns: %+v", out.Returns)
	}
}

func TestEnrollmentListAndApprove(t *testing.T) {
	env := setupTestEnv(t)

	now := time.Now().UTC()
	pending := &enroll.Record{
		ID:             "enr-pending",
		PeelID:         "web-01",
		PublicKey:      "UABC",
		CurvePublicKey: "XABC",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	active := &enroll.Record{
		ID:             "enr-active",
		PeelID:         "db-01",
		PublicKey:      "UDEF",
		CurvePublicKey: "XDEF",
		State:          enroll.StateActive,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := env.enroll.Create(env.ctx, pending); err != nil {
		t.Fatalf("create pending: %v", err)
	}
	if err := env.enroll.Create(env.ctx, active); err != nil {
		t.Fatalf("create active: %v", err)
	}

	resp := env.doReq(t, http.MethodGet, "/api/v1/enrollments", nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	list := decodeResp[[]EnrollmentItem](t, resp)
	if len(list) != 1 || list[0].ID != pending.ID {
		t.Fatalf("expected only pending, got %+v", list)
	}

	resp = env.doReq(t, http.MethodGet, "/api/v1/enrollments?state=all", nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	all := decodeResp[[]EnrollmentItem](t, resp)
	if len(all) != 2 {
		t.Fatalf("expected 2 enrollments, got %d", len(all))
	}

	resp = env.doReq(t, http.MethodPost, "/api/v1/enrollments/"+pending.ID+"/approve", nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	updated := decodeResp[EnrollmentItem](t, resp)
	if updated.State != string(enroll.StateApproved) {
		t.Fatalf("expected approved state, got %s", updated.State)
	}

	rec, err := env.enroll.Get(env.ctx, pending.ID)
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if rec.DecidedBy != "tester" {
		t.Fatalf("expected decided_by tester, got %q", rec.DecidedBy)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodGet, "/api/v1/jobs/nonexistent-jid-12345", nil, "test-secret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "job not found" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestOpenAPIDocsEndpoints(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodGet, "/api/v1/docs", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var b bytes.Buffer
	if _, err := b.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read docs body: %v", err)
	}
	if !strings.Contains(b.String(), "swagger-ui") {
		t.Fatalf("expected swagger-ui in body: %s", b.String())
	}

	// Verify HTML references local paths, not CDN.
	html := b.String()
	if strings.Contains(html, "unpkg.com") || strings.Contains(html, "cdn.") {
		t.Fatal("swagger-ui HTML must not reference CDN assets")
	}
	if !strings.Contains(html, "/api/v1/docs/swagger-ui-bundle.js") {
		t.Fatal("swagger-ui HTML must reference local JS bundle")
	}

	// Verify embedded JS asset is served.
	resp = env.doReq(t, http.MethodGet, "/api/v1/docs/swagger-ui-bundle.js", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for embedded JS, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "javascript") {
		t.Fatalf("expected javascript content-type, got %q", ct)
	}

	// Verify embedded CSS asset is served.
	resp = env.doReq(t, http.MethodGet, "/api/v1/docs/swagger-ui.css", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for embedded CSS, got %d", resp.StatusCode)
	}

	resp = env.doReq(t, http.MethodGet, "/api/v1/openapi.yaml", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var yb bytes.Buffer
	_, _ = yb.ReadFrom(resp.Body)
	if !strings.Contains(yb.String(), "/jobs") {
		t.Fatalf("expected /jobs path in openapi yaml")
	}

	resp = env.doReq(t, http.MethodGet, "/api/v1/openapi.json", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode openapi json: %v", err)
	}
}
