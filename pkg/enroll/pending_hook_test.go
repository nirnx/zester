package enroll_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
)

// pendingHookSetup builds a handler with the OnPending hook wired to record
// every notified record.
func pendingHookSetup(t *testing.T) (*enroll.Handler, *enroll.ChallengeStore, context.Context, *[]enroll.Record) {
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
	issuer, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{AccountKP: accountKB})
	if err != nil {
		t.Fatalf("NewCredentialIssuer: %v", err)
	}

	var pending []enroll.Record
	handler := enroll.NewHandler(enroll.HandlerConfig{
		Store:      store,
		Challenges: challenges,
		Issuer:     issuer,
		OnPending:  func(rec enroll.Record) { pending = append(pending, rec) },
	})
	return handler, challenges, ctx, &pending
}

// submitEnrollment runs one challenge + signed submit round trip for peelID
// under a freshly generated key (a brand-new host).
func submitEnrollment(t *testing.T, handler *enroll.Handler, challenges *enroll.ChallengeStore, ctx context.Context, peelID string) *httptest.ResponseRecorder {
	t.Helper()
	userKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}
	return submitEnrollmentWithKey(t, handler, challenges, ctx, peelID, userKB)
}

// submitEnrollmentWithKey runs one challenge + signed submit round trip for
// peelID under the given identity key (the same host submitting again).
func submitEnrollmentWithKey(t *testing.T, handler *enroll.Handler, challenges *enroll.ChallengeStore, ctx context.Context, peelID string, userKB *auth.KeyBundle) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	curveKey, err := auth.CurvePublicKeyFromSeed(userKB.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}
	challenge, err := challenges.Issue(ctx, peelID, userKB.PublicKey)
	if err != nil {
		t.Fatalf("Issue challenge: %v", err)
	}
	signature, err := enroll.SignChallenge(userKB.Seed, challenge.Challenge, curveKey)
	if err != nil {
		t.Fatalf("SignChallenge: %v", err)
	}

	body, _ := json.Marshal(enroll.EnrollRequest{
		PeelID:         peelID,
		PublicKey:      userKB.PublicKey,
		CurvePublicKey: curveKey,
		ChallengeID:    challenge.ChallengeID,
		Signature:      signature,
	})
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHandleEnroll_OnPendingHook verifies that the OnPending hook fires
// exactly once per NEWLY created enrollment record — and not for the
// idempotent same-key resubmit of an already-pending enrollment, nor for a
// refused different-key resubmit (peel-ID squat → 409).
func TestHandleEnroll_OnPendingHook(t *testing.T) {
	handler, challenges, ctx, pending := pendingHookSetup(t)

	hostKB, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}
	if rec := submitEnrollmentWithKey(t, handler, challenges, ctx, "hook-peel", hostKB); rec.Code != http.StatusCreated {
		t.Fatalf("submit status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}

	if len(*pending) != 1 {
		t.Fatalf("OnPending calls = %d, want 1", len(*pending))
	}
	got := (*pending)[0]
	if got.PeelID != "hook-peel" {
		t.Errorf("OnPending record PeelID = %q, want hook-peel", got.PeelID)
	}
	if got.ID == "" || got.State != enroll.StatePending {
		t.Errorf("OnPending record = %+v, want a pending record with an ID", got)
	}

	// The SAME host resubmitting while pending is idempotent (200, existing
	// record) and must NOT re-fire the hook.
	if rec := submitEnrollmentWithKey(t, handler, challenges, ctx, "hook-peel", hostKB); rec.Code != http.StatusOK {
		t.Fatalf("resubmit status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(*pending) != 1 {
		t.Fatalf("OnPending calls after idempotent resubmit = %d, want 1", len(*pending))
	}

	// A DIFFERENT key under the same pending peel id is a squat attempt:
	// refused with 409, no record created, hook not fired.
	if rec := submitEnrollment(t, handler, challenges, ctx, "hook-peel"); rec.Code != http.StatusConflict {
		t.Fatalf("different-key resubmit status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if len(*pending) != 1 {
		t.Fatalf("OnPending calls after refused squat = %d, want 1", len(*pending))
	}
}
