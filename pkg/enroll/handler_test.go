package enroll_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
)

func testHandlerSetup(t *testing.T) (*enroll.Handler, *enroll.Store, *enroll.ChallengeStore, context.Context) {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	store, err := enroll.NewStore(ctx, enroll.StoreConfig{JS: js})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	challenges, err := enroll.NewChallengeStore(ctx, js, nil)
	if err != nil {
		t.Fatalf("NewChallengeStore: %v", err)
	}

	accountKB, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		t.Fatalf("GenerateKeyBundle account: %v", err)
	}

	issuer, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: accountKB,
	})
	if err != nil {
		t.Fatalf("NewCredentialIssuer: %v", err)
	}

	handler := enroll.NewHandler(enroll.HandlerConfig{
		Store:      store,
		Challenges: challenges,
		Issuer:     issuer,
	})

	return handler, store, challenges, ctx
}

func TestHandleNonce_ValidRequest(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Generate a valid user key.
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/enroll/nonce?peel_id=test-peel&public_key="+userKB.PublicKey, nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp enroll.NonceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode response: %v", err)
	}

	if resp.ChallengeID == "" {
		t.Error("ChallengeID is empty")
	}
	if len(resp.Challenge) != enroll.ChallengeSize {
		t.Errorf("Challenge length = %d, want %d", len(resp.Challenge), enroll.ChallengeSize)
	}
	if resp.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero")
	}
}

func TestHandleNonce_MissingPeelID(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/enroll/nonce?public_key="+userKB.PublicKey, nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleNonce_InvalidPublicKey(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/enroll/nonce?peel_id=test-peel&public_key=invalid-key", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleEnroll_FullFlow(t *testing.T) {
	handler, _, challenges, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Generate user key.
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Step 1: Get a challenge nonce.
	challenge, err := challenges.Issue(ctx, "test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue challenge: %v", err)
	}

	// Step 2: Sign the challenge with curve key binding.
	signature, err := enroll.SignChallenge(userKB.Seed, challenge.Challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Step 3: Submit enrollment.
	enrollReq := enroll.EnrollRequest{
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		Hostname:       "server.example.com",
		ChallengeID:    challenge.ChallengeID,
		Signature:      signature,
		Metadata:       map[string]string{"os": "linux"},
	}

	body, _ := json.Marshal(enrollReq)
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("Status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp enroll.EnrollResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode response: %v", err)
	}

	if resp.ID == "" {
		t.Error("Enrollment ID is empty")
	}
	if resp.PeelID != "test-peel" {
		t.Errorf("PeelID = %q, want %q", resp.PeelID, "test-peel")
	}
	if resp.State != enroll.StatePending {
		t.Errorf("State = %q, want %q", resp.State, enroll.StatePending)
	}
}

func TestHandleEnroll_DuplicatePeel(t *testing.T) {
	handler, store, challenges, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Create an existing pending enrollment.
	now := time.Now().UTC()
	existing := &enroll.Record{
		ID:             "enr-existing",
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("Create existing: %v", err)
	}

	// Get a new challenge.
	challenge, err := challenges.Issue(ctx, "test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue challenge: %v", err)
	}

	signature, err := enroll.SignChallenge(userKB.Seed, challenge.Challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Submit enrollment again (should be idempotent for pending).
	enrollReq := enroll.EnrollRequest{
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		ChallengeID:    challenge.ChallengeID,
		Signature:      signature,
	}

	body, _ := json.Marshal(enrollReq)
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	// Should return 200 OK with existing record.
	if rec.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp enroll.EnrollResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode response: %v", err)
	}

	if resp.ID != existing.ID {
		t.Errorf("Returned ID = %q, want %q (existing)", resp.ID, existing.ID)
	}
}

func TestHandleEnroll_WrongSignature(t *testing.T) {
	handler, _, challenges, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	challenge, err := challenges.Issue(ctx, "test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue challenge: %v", err)
	}

	// Create a wrong signature (sign something else).
	wrongSignature, err := enroll.SignChallenge(userKB.Seed, []byte("wrong-data"), curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	enrollReq := enroll.EnrollRequest{
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		ChallengeID:    challenge.ChallengeID,
		Signature:      wrongSignature,
	}

	body, _ := json.Marshal(enrollReq)
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleEnroll_ChallengeBindingMismatch(t *testing.T) {
	handler, _, challenges, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Issue challenge for one peel.
	challenge, err := challenges.Issue(ctx, "peel-A", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue challenge: %v", err)
	}

	signature, err := enroll.SignChallenge(userKB.Seed, challenge.Challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Submit enrollment for a different peel (binding mismatch).
	enrollReq := enroll.EnrollRequest{
		PeelID:         "peel-B", // Different!
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		ChallengeID:    challenge.ChallengeID,
		Signature:      signature,
	}

	body, _ := json.Marshal(enrollReq)
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleStatus_Found(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create an enrollment.
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             "enr-status-test",
		PeelID:         "test-peel",
		PublicKey:      "UABC123",
		CurvePublicKey: "XDEF456",
		State:          enroll.StatePending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/enroll/"+rec.ID+"/status", nil)
	rec2 := httptest.NewRecorder()

	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", rec2.Code, http.StatusOK)
	}

	var resp enroll.StatusResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode response: %v", err)
	}

	if resp.ID != rec.ID {
		t.Errorf("ID = %q, want %q", resp.ID, rec.ID)
	}
	if resp.State != enroll.StatePending {
		t.Errorf("State = %q, want %q", resp.State, enroll.StatePending)
	}
}

func TestHandleStatus_NotFound(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/enroll/enr-nonexistent/status", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleCreds_FullFlow(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Create an approved enrollment.
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             "enr-creds-test",
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		State:          enroll.StateApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Sign the enrollment ID.
	sigB64, err := enroll.SignEnrollmentID(userKB.Seed, rec.ID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	authHeader := "Nkey " + userKB.PublicKey + ":" + sigB64

	req := httptest.NewRequest("GET", "/api/v1/enroll/"+rec.ID+"/creds", nil)
	req.Header.Set("Authorization", authHeader)
	rec2 := httptest.NewRecorder()

	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d (body: %s)", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	var resp enroll.CredsResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode response: %v", err)
	}

	if resp.PeelID != "test-peel" {
		t.Errorf("PeelID = %q, want %q", resp.PeelID, "test-peel")
	}
	if resp.CredsData == "" {
		t.Error("CredsData is empty")
	}
	if resp.ExpiresAt == "" {
		t.Error("ExpiresAt is empty")
	}

	// Verify the enrollment transitioned to Issued.
	updated, err := store.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get updated record: %v", err)
	}
	if updated.State != enroll.StateIssued {
		t.Errorf("Updated state = %q, want %q", updated.State, enroll.StateIssued)
	}
}

func TestHandleCreds_NotApproved(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Create a pending (not approved) enrollment.
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             "enr-not-approved",
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		State:          enroll.StatePending, // Not approved!
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sigB64, err := enroll.SignEnrollmentID(userKB.Seed, rec.ID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	authHeader := "Nkey " + userKB.PublicKey + ":" + sigB64

	req := httptest.NewRequest("GET", "/api/v1/enroll/"+rec.ID+"/creds", nil)
	req.Header.Set("Authorization", authHeader)
	rec2 := httptest.NewRecorder()

	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", rec2.Code, http.StatusForbidden)
	}
}

func TestHandleCreds_WrongAuthHeader(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             "enr-wrong-auth",
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		State:          enroll.StateApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Use a wrong auth header.
	req := httptest.NewRequest("GET", "/api/v1/enroll/"+rec.ID+"/creds", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec2 := httptest.NewRecorder()

	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", rec2.Code, http.StatusUnauthorized)
	}
}

func TestHandleCreds_DoubleDownload(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             "enr-double-download",
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		State:          enroll.StateApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sigB64, err := enroll.SignEnrollmentID(userKB.Seed, rec.ID)
	if err != nil {
		t.Fatalf("SignEnrollmentID: %v", err)
	}

	authHeader := "Nkey " + userKB.PublicKey + ":" + sigB64

	// First download.
	req1 := httptest.NewRequest("GET", "/api/v1/enroll/"+rec.ID+"/creds", nil)
	req1.Header.Set("Authorization", authHeader)
	rec1 := httptest.NewRecorder()

	mux.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Errorf("First download status = %d, want %d", rec1.Code, http.StatusOK)
	}

	// Second download (should fail with 403 Forbidden because state is now Issued, not Approved).
	req2 := httptest.NewRequest("GET", "/api/v1/enroll/"+rec.ID+"/creds", nil)
	req2.Header.Set("Authorization", authHeader)
	rec2 := httptest.NewRecorder()

	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Errorf("Second download status = %d, want %d", rec2.Code, http.StatusForbidden)
	}
}

func TestHandleEnroll_ReEnrollmentAfterRejection(t *testing.T) {
	handler, store, challenges, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	// Create a rejected enrollment.
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:             "enr-rejected",
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		State:          enroll.StateRejected,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get a new challenge.
	challenge, err := challenges.Issue(ctx, "test-peel", userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue challenge: %v", err)
	}

	signature, err := enroll.SignChallenge(userKB.Seed, challenge.Challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	// Re-enroll (should succeed after releasing the index).
	enrollReq := enroll.EnrollRequest{
		PeelID:         "test-peel",
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		ChallengeID:    challenge.ChallengeID,
		Signature:      signature,
	}

	body, _ := json.Marshal(enrollReq)
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()

	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusCreated {
		t.Errorf("Re-enrollment status = %d, want %d (body: %s)", rec2.Code, http.StatusCreated, rec2.Body.String())
	}

	var resp enroll.EnrollResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("Decode response: %v", err)
	}

	if resp.ID == rec.ID {
		t.Error("Re-enrollment returned same ID as rejected enrollment")
	}
	if resp.State != enroll.StatePending {
		t.Errorf("Re-enrollment state = %q, want %q", resp.State, enroll.StatePending)
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	// Create a simple handler that just returns 200.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with rate limiter.
	handler := enroll.RateLimitMiddleware(inner, nil)

	// Make requests up to the capacity (10).
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.0.2.1:12345" // Same IP
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: status = %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}

	// The 11th request should be rate limited.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("Request 11: status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/enroll/nonce?peel_id=test&public_key="+userKB.PublicKey, nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	// Check for security headers.
	headers := []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Content-Security-Policy",
	}

	for _, h := range headers {
		if rec.Header().Get(h) == "" {
			t.Errorf("Missing security header: %s", h)
		}
	}
}

// --- SSE Stream Handler Tests ---

func TestHandleStream_InitialStatePending(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Create a pending enrollment.
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:        "enr-stream-pending",
		PeelID:    "test-peel",
		PublicKey: "UABC123",
		State:     enroll.StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Open SSE stream with a short timeout so we don't hang forever.
	streamCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/api/v1/enroll/"+rec.ID+"/stream", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}

	// Read the initial state event.
	event := readSSEEvent(t, resp.Body)
	if event.eventType != "state" {
		t.Errorf("event type = %q, want %q", event.eventType, "state")
	}

	var status enroll.StatusResponse
	if err := json.Unmarshal([]byte(event.data), &status); err != nil {
		t.Fatalf("decode SSE data: %v", err)
	}
	if status.State != enroll.StatePending {
		t.Errorf("initial state = %q, want %q", status.State, enroll.StatePending)
	}
	if status.ID != rec.ID {
		t.Errorf("ID = %q, want %q", status.ID, rec.ID)
	}
}

func TestHandleStream_TerminalStateClosesImmediately(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Create an already-approved enrollment (terminal state).
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:        "enr-stream-approved",
		PeelID:    "test-peel-term",
		PublicKey: "UABC123",
		State:     enroll.StateApproved,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/enroll/"+rec.ID+"/stream", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Read the initial state event.
	event := readSSEEvent(t, resp.Body)
	if event.eventType != "state" {
		t.Errorf("event type = %q, want %q", event.eventType, "state")
	}

	var status enroll.StatusResponse
	if err := json.Unmarshal([]byte(event.data), &status); err != nil {
		t.Fatalf("decode SSE data: %v", err)
	}
	if status.State != enroll.StateApproved {
		t.Errorf("state = %q, want %q", status.State, enroll.StateApproved)
	}

	// The stream should close immediately after a terminal state.
	// Try to read more — should get EOF.
	next := readSSEEvent(t, resp.Body)
	if next.eventType != "" && next.data != "" {
		t.Errorf("expected stream to close after terminal state, got event: %+v", next)
	}
}

func TestHandleStream_WaitsForApproval(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Create a pending enrollment.
	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:        "enr-stream-wait",
		PeelID:    "test-peel-wait",
		PublicKey: "UABC123",
		State:     enroll.StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/api/v1/enroll/"+rec.ID+"/stream", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()

	// Read the initial "pending" event.
	initial := readSSEEvent(t, resp.Body)
	var initialStatus enroll.StatusResponse
	if err := json.Unmarshal([]byte(initial.data), &initialStatus); err != nil {
		t.Fatalf("decode initial SSE data: %v", err)
	}
	if initialStatus.State != enroll.StatePending {
		t.Fatalf("initial state = %q, want %q", initialStatus.State, enroll.StatePending)
	}

	// Approve the enrollment in a goroutine — this triggers a KV update
	// that the handler's watcher picks up and streams to the client.
	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := store.Approve(ctx, rec.ID, "admin"); err != nil {
			t.Errorf("Approve: %v", err)
		}
	}()

	// Read the approval event.
	approved := readSSEEvent(t, resp.Body)
	if approved.eventType != "state" {
		t.Errorf("event type = %q, want %q", approved.eventType, "state")
	}

	var approvedStatus enroll.StatusResponse
	if err := json.Unmarshal([]byte(approved.data), &approvedStatus); err != nil {
		t.Fatalf("decode approved SSE data: %v", err)
	}
	if approvedStatus.State != enroll.StateApproved {
		t.Errorf("state = %q, want %q", approvedStatus.State, enroll.StateApproved)
	}
}

func TestHandleStream_NotFound(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/enroll/enr-nonexistent/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandleStream_RejectionClosesStream(t *testing.T) {
	handler, store, _, ctx := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	now := time.Now().UTC()
	rec := &enroll.Record{
		ID:        "enr-stream-reject",
		PeelID:    "test-peel-reject",
		PublicKey: "UABC123",
		State:     enroll.StatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/api/v1/enroll/"+rec.ID+"/stream", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()

	// Read initial pending event.
	readSSEEvent(t, resp.Body)

	// Reject in background.
	go func() {
		time.Sleep(100 * time.Millisecond)
		if _, err := store.Reject(ctx, rec.ID, "admin", "denied"); err != nil {
			t.Errorf("Reject: %v", err)
		}
	}()

	// Read the rejection event.
	rejected := readSSEEvent(t, resp.Body)
	var status enroll.StatusResponse
	if err := json.Unmarshal([]byte(rejected.data), &status); err != nil {
		t.Fatalf("decode rejected SSE data: %v", err)
	}
	if status.State != enroll.StateRejected {
		t.Errorf("state = %q, want %q", status.State, enroll.StateRejected)
	}

	// Stream should close after terminal state.
	next := readSSEEvent(t, resp.Body)
	if next.eventType != "" && next.data != "" {
		t.Errorf("expected stream to close after rejection, got event: %+v", next)
	}
}

// sseEvent holds a parsed SSE event.
type sseEvent struct {
	eventType string
	data      string
}

// readSSEEvent reads the next SSE event from a stream.
// An SSE event is: "event: <type>\ndata: <data>\n\n"
// Heartbeats (": heartbeat\n\n") are skipped.
func readSSEEvent(t *testing.T, r io.Reader) sseEvent {
	t.Helper()
	scanner := bufio.NewScanner(r)
	var ev sseEvent

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event: "):
			ev.eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ":"):
			// Comment (heartbeat), skip.
			continue
		case line == "":
			// End of event. If we captured something, return it.
			if ev.eventType != "" || ev.data != "" {
				return ev
			}
		}
	}
	return ev
}

func BenchmarkHandleNonce(b *testing.B) {
	handler, _, _, _ := testHandlerSetup(&testing.T{})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, _ := auth.GenerateKeyBundle(auth.RoleUser)
	url := fmt.Sprintf("/api/v1/enroll/nonce?peel_id=bench-peel&public_key=%s", userKB.PublicKey)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", url, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}
}

// invalidPeelIDCases are peel IDs the enrollment endpoints must reject with
// HTTP 400: peel IDs become NATS subject tokens in fixed positions, so dots,
// wildcards, leading underscores (reserved _master/_admin origins), and
// non-ASCII characters are banned at the submit path.
var invalidPeelIDCases = []struct {
	name   string
	peelID string
}{
	{"empty", ""},
	{"dotted", "web.01"},
	{"dotted fqdn", "web01.example.com"},
	{"leading underscore", "_web01"},
	{"reserved master origin", "_master"},
	{"reserved admin origin", "_admin"},
	{"wildcard star", "web*"},
	{"wildcard gt", "web>"},
	{"only star", "*"},
	{"only gt", ">"},
	{"unicode", "wéb-01"},
	{"spaces", "web 01"},
	{"too long", strings.Repeat("a", 129)},
}

func TestHandleNonce_PeelIDValidation(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}

	for _, tt := range invalidPeelIDCases {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			target := "/api/v1/enroll/nonce?peel_id=" + url.QueryEscape(tt.peelID) +
				"&public_key=" + userKB.PublicKey
			req := httptest.NewRequest("GET", target, nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("Status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "peel_id") {
				t.Errorf("error body %q does not mention peel_id", rec.Body.String())
			}
		})
	}

	// Sanity: a conforming ID gets a nonce.
	t.Run("valid/simple", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/enroll/nonce?peel_id=web-01&public_key="+userKB.PublicKey, nil)
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
	})
}

func TestHandleEnroll_PeelIDValidation(t *testing.T) {
	handler, _, _, _ := testHandlerSetup(t)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}
	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	submit := func(t *testing.T, peelID string) *httptest.ResponseRecorder {
		t.Helper()
		enrollReq := enroll.EnrollRequest{
			PeelID:         peelID,
			PublicKey:      userKB.PublicKey,
			CurvePublicKey: curveKey,
			Hostname:       "host.example.com",
			ChallengeID:    "chl-nonexistent",
			Signature:      []byte("sig"),
		}
		body, _ := json.Marshal(enrollReq)
		req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for _, tt := range invalidPeelIDCases {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			rec := submit(t, tt.peelID)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("Status = %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "peel_id") {
				t.Errorf("error body %q does not mention peel_id", rec.Body.String())
			}
		})
	}

	// Valid IDs must pass the peel-ID gate: with a bogus challenge the request
	// proceeds to challenge verification and fails 401 there — NOT 400.
	validIDs := []struct {
		name   string
		peelID string
	}{
		{"simple", "web-01"},
		{"underscores", "web_server_01"},
		{"single char", "w"},
		{"max length", strings.Repeat("a", 128)},
	}
	for _, tt := range validIDs {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			rec := submit(t, tt.peelID)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("Status = %d, want %d — a valid peel ID must clear validation and fail at the challenge step (body: %s)",
					rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}
