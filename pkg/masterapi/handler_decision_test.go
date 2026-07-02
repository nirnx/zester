package masterapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ptorbus/zester/pkg/enroll"
)

func createTestEnrollment(t *testing.T, env *testEnv, id, peelID string, state enroll.State) *enroll.Record {
	t.Helper()
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             id,
		PeelID:         peelID,
		PublicKey:      "UABC",
		CurvePublicKey: "XABC",
		State:          state,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := env.enroll.Create(env.ctx, rec); err != nil {
		t.Fatalf("create enrollment %s: %v", id, err)
	}
	return rec
}

func TestEnrollmentRejectWithReason(t *testing.T) {
	env := setupTestEnv(t)
	pending := createTestEnrollment(t, env, "enr-reject", "web-01", enroll.StatePending)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/"+pending.ID+"/reject",
		map[string]string{"reason": "untrusted host"}, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	updated := decodeResp[EnrollmentItem](t, resp)
	if updated.State != string(enroll.StateRejected) {
		t.Fatalf("expected rejected state, got %s", updated.State)
	}

	rec, err := env.enroll.Get(env.ctx, pending.ID)
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if rec.State != enroll.StateRejected {
		t.Fatalf("expected persisted rejected state, got %s", rec.State)
	}
	if rec.RejectReason != "untrusted host" {
		t.Fatalf("expected reason recorded, got %q", rec.RejectReason)
	}
	if rec.DecidedBy != "tester" {
		t.Fatalf("expected decided_by tester, got %q", rec.DecidedBy)
	}
}

func TestEnrollmentRevokeWithoutBody(t *testing.T) {
	env := setupTestEnv(t)
	active := createTestEnrollment(t, env, "enr-revoke", "web-01", enroll.StateActive)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/"+active.ID+"/revoke", nil, "test-secret")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	updated := decodeResp[EnrollmentItem](t, resp)
	if updated.State != string(enroll.StateRevoked) {
		t.Fatalf("expected revoked state, got %s", updated.State)
	}

	rec, err := env.enroll.Get(env.ctx, active.ID)
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if rec.State != enroll.StateRevoked || rec.DecidedBy != "tester" {
		t.Fatalf("persisted record mismatch: state=%s decided_by=%q", rec.State, rec.DecidedBy)
	}
}

func TestRejectEnrollment_WrongState(t *testing.T) {
	env := setupTestEnv(t)
	active := createTestEnrollment(t, env, "enr-active-rej", "web-01", enroll.StateActive)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/"+active.ID+"/reject", nil, "test-secret")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if !strings.Contains(out["error"], "cannot reject") {
		t.Fatalf("expected 'cannot reject' in error, got %q", out["error"])
	}
}

func TestRevokeEnrollment_WrongState(t *testing.T) {
	env := setupTestEnv(t)
	pending := createTestEnrollment(t, env, "enr-pending-rev", "web-01", enroll.StatePending)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/"+pending.ID+"/revoke", nil, "test-secret")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if !strings.Contains(out["error"], "cannot revoke") {
		t.Fatalf("expected 'cannot revoke' in error, got %q", out["error"])
	}
}

func TestRevokeEnrollment_NotFound(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/enr-missing/revoke", nil, "test-secret")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	out := decodeResp[map[string]string](t, resp)
	if out["error"] != "enrollment not found" {
		t.Fatalf("unexpected error message: %q", out["error"])
	}
}

func TestRejectEnrollment_InvalidBody(t *testing.T) {
	env := setupTestEnv(t)
	createTestEnrollment(t, env, "enr-badbody", "web-01", enroll.StatePending)

	req, err := http.NewRequest(http.MethodPost,
		env.server.URL+"/api/v1/enrollments/enr-badbody/reject", strings.NewReader("{not json"))
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
}

func TestRejectEnrollment_Unauthorized(t *testing.T) {
	env := setupTestEnv(t)

	resp := env.doReq(t, http.MethodPost, "/api/v1/enrollments/enr-x/reject", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
