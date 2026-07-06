// Multi-URL failover and nonce re-request tests for the enrollment client.
// Internal test package — exercises unexported rotation helpers directly
// and the exported Enroll flow against httptest servers.
package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/auth"
)

// enrollFlowServer is an httptest server implementing the full enrollment
// flow (nonce → submit → SSE approval → creds) with canned responses.
type enrollFlowServer struct {
	srv *httptest.Server

	// rejectSubmits is the number of initial enrollment submissions to
	// reject with the unknown-challenge error before accepting. Set
	// before the first request; not mutated afterwards.
	rejectSubmits int

	mu         sync.Mutex
	nonceHits  int
	submitHits int
	credsHits  int
}

func newEnrollFlowServer(t *testing.T, rejectSubmits int) *enrollFlowServer {
	t.Helper()
	s := &enrollFlowServer{rejectSubmits: rejectSubmits}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/enroll/nonce", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.nonceHits++
		n := s.nonceHits
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(NonceResponse{
			ChallengeID: fmt.Sprintf("chl-%d", n),
			Challenge:   []byte("challenge-bytes"),
			ExpiresAt:   time.Now().Add(5 * time.Minute),
		})
	})
	mux.HandleFunc("/api/v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.submitHits++
		hit := s.submitHits
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if hit <= s.rejectSubmits {
			// Mirrors Handler.handleEnroll's unknown/expired challenge
			// rejection (writeError → {"error": message}).
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "challenge verification failed"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(EnrollResponse{ID: "enr-1", PeelID: "peel-1", State: StatePending})
	})
	mux.HandleFunc("/api/v1/enroll/enr-1/stream", func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateApproved))
	})
	mux.HandleFunc("/api/v1/enroll/enr-1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StatusResponse{ID: "enr-1", PeelID: "peel-1", State: StateApproved})
	})
	mux.HandleFunc("/api/v1/enroll/enr-1/creds", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.credsHits++
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CredsResponse{
			PeelID:    "peel-1",
			CredsData: EncodeJWTForTransport("fake-jwt"),
			ExpiresAt: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *enrollFlowServer) hits() (nonce, submit, creds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonceHits, s.submitHits, s.credsHits
}

// deadURL returns a URL that refuses connections: it briefly starts a
// server to allocate a port, then closes it so nothing is listening.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u := srv.URL
	srv.Close()
	return u
}

func testKeyBundle(t *testing.T) *auth.KeyBundle {
	t.Helper()
	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		t.Fatalf("GenerateKeyBundle: %v", err)
	}
	return kb
}

func TestEnroll_FallsBackToSecondURL(t *testing.T) {
	flow := newEnrollFlowServer(t, 0)
	dead := deadURL(t)

	c, err := NewClient(ClientConfig{
		MasterURLs:   []string{dead, flow.srv.URL},
		PeelID:       "peel-1",
		PollInterval: 10 * time.Millisecond,
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := c.Enroll(ctx, testKeyBundle(t))
	if err != nil {
		t.Fatalf("Enroll returned error: %v", err)
	}
	if res.EnrollmentID != "enr-1" {
		t.Errorf("EnrollmentID = %q, want %q", res.EnrollmentID, "enr-1")
	}
	if res.JWT != "fake-jwt" {
		t.Errorf("JWT = %q, want %q", res.JWT, "fake-jwt")
	}

	// After the rotation the client must be pinned to the healthy URL.
	if got := c.currentURL(); got != flow.srv.URL {
		t.Errorf("currentURL = %q, want %q", got, flow.srv.URL)
	}
}

func TestEnroll_RerequestsNonceOnUnknownChallenge(t *testing.T) {
	// First submission is rejected with the handler's unknown-challenge
	// error; the second is accepted.
	flow := newEnrollFlowServer(t, 1)

	c, err := NewClient(ClientConfig{
		MasterURLs:   []string{flow.srv.URL},
		PeelID:       "peel-1",
		PollInterval: 10 * time.Millisecond,
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := c.Enroll(ctx, testKeyBundle(t))
	if err != nil {
		t.Fatalf("Enroll returned error: %v", err)
	}
	if res.EnrollmentID != "enr-1" {
		t.Errorf("EnrollmentID = %q, want %q", res.EnrollmentID, "enr-1")
	}

	nonce, submit, _ := flow.hits()
	if nonce != 2 {
		t.Errorf("nonce endpoint hit %d times, want 2 (fresh nonce per submission)", nonce)
	}
	if submit != 2 {
		t.Errorf("enroll endpoint hit %d times, want 2", submit)
	}
}

func TestTryEnroll_UnknownChallengeBounded(t *testing.T) {
	// Server always rejects with unknown-challenge: tryEnroll must give
	// up after the initial attempt + maxChallengeRetries re-requests.
	flow := newEnrollFlowServer(t, 1<<30)

	kb := testKeyBundle(t)
	curveKey, err := auth.CurvePublicKeyFromSeed(kb.Seed)
	if err != nil {
		t.Fatalf("CurvePublicKeyFromSeed: %v", err)
	}

	c := &Client{
		httpClient: flow.srv.Client(),
		masterURLs: []string{flow.srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	_, err = c.tryEnroll(context.Background(), kb, curveKey, "host-1")
	if err == nil {
		t.Fatal("expected error from tryEnroll, got nil")
	}

	wantAttempts := maxChallengeRetries + 1
	nonce, submit, _ := flow.hits()
	if nonce != wantAttempts {
		t.Errorf("nonce endpoint hit %d times, want %d", nonce, wantAttempts)
	}
	if submit != wantAttempts {
		t.Errorf("enroll endpoint hit %d times, want %d", submit, wantAttempts)
	}
}

func TestEnroll_AllURLsDown(t *testing.T) {
	c, err := NewClient(ClientConfig{
		MasterURLs:   []string{deadURL(t), deadURL(t)},
		PeelID:       "peel-1",
		PollInterval: 10 * time.Millisecond,
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// The enroll loop retries with backoff until the context expires —
	// a short deadline keeps the test bounded and fast.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err = c.Enroll(ctx, testKeyBundle(t))
	if err == nil {
		t.Fatal("expected error when all master URLs are down, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestEnroll_BackCompatSingleMasterURL(t *testing.T) {
	flow := newEnrollFlowServer(t, 0)

	c, err := NewClient(ClientConfig{
		MasterURL:    flow.srv.URL, // legacy single-URL field only
		PeelID:       "peel-1",
		PollInterval: 10 * time.Millisecond,
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := c.Enroll(ctx, testKeyBundle(t))
	if err != nil {
		t.Fatalf("Enroll returned error: %v", err)
	}
	if res.EnrollmentID != "enr-1" {
		t.Errorf("EnrollmentID = %q, want %q", res.EnrollmentID, "enr-1")
	}
	if res.JWT != "fake-jwt" {
		t.Errorf("JWT = %q, want %q", res.JWT, "fake-jwt")
	}
}

func TestNewClient_RequiresSomeURL(t *testing.T) {
	_, err := NewClient(ClientConfig{PeelID: "peel-1"})
	if err == nil {
		t.Fatal("expected error when both MasterURL and MasterURLs are empty")
	}
}

func TestNewClient_SkipsEmptyURLEntries(t *testing.T) {
	c, err := NewClient(ClientConfig{
		MasterURLs: []string{"", "https://master-1:8443", ""},
		PeelID:     "peel-1",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := c.currentURL(); got != "https://master-1:8443" {
		t.Errorf("currentURL = %q, want %q", got, "https://master-1:8443")
	}
	if len(c.masterURLs) != 1 {
		t.Errorf("masterURLs = %v, want 1 entry", c.masterURLs)
	}
}

func TestRotateURL(t *testing.T) {
	c := &Client{
		masterURLs: []string{"https://a", "https://b"},
		logger:     slog.Default(),
	}

	if got := c.currentURL(); got != "https://a" {
		t.Fatalf("currentURL = %q, want %q", got, "https://a")
	}

	c.rotateURL("https://a")
	if got := c.currentURL(); got != "https://b" {
		t.Errorf("after rotation currentURL = %q, want %q", got, "https://b")
	}

	// A stale rotation request for a URL the client already rotated away
	// from must be a no-op (successful responses pin the current URL).
	c.rotateURL("https://a")
	if got := c.currentURL(); got != "https://b" {
		t.Errorf("stale rotation moved currentURL to %q, want %q", got, "https://b")
	}

	// Rotation wraps around.
	c.rotateURL("https://b")
	if got := c.currentURL(); got != "https://a" {
		t.Errorf("after wrap-around currentURL = %q, want %q", got, "https://a")
	}
}

func TestRotateURL_SingleURLNoop(t *testing.T) {
	c := &Client{
		masterURLs: []string{"https://only"},
		logger:     slog.Default(),
	}
	c.rotateURL("https://only")
	if got := c.currentURL(); got != "https://only" {
		t.Errorf("currentURL = %q, want %q", got, "https://only")
	}
}

func TestPollUntilApproved_RotatesOn5xx(t *testing.T) {
	var mu sync.Mutex
	var hits1, hits2 int

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits1++
		mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits2++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StatusResponse{ID: "enr-1", PeelID: "peel-1", State: StateApproved})
	}))
	defer srv2.Close()

	c := &Client{
		httpClient: srv1.Client(),
		masterURLs: []string{srv1.URL, srv2.URL},
		peelID:     "peel-1",
		pollBase:   10 * time.Millisecond,
		pollMax:    50 * time.Millisecond,
		logger:     slog.Default(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.pollUntilApproved(ctx, "enr-1"); err != nil {
		t.Fatalf("pollUntilApproved returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits1 != 1 {
		t.Errorf("failing server hit %d times, want 1 (rotate after first 5xx)", hits1)
	}
	if hits2 != 1 {
		t.Errorf("healthy server hit %d times, want 1", hits2)
	}
}
