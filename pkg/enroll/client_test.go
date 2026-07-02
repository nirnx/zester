// Package enroll (internal test) — needs access to unexported
// streamUntilApproved and waitForApproval methods.
package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// writeSSE is a test helper that writes a single SSE event.
func writeSSE(w http.ResponseWriter, f http.Flusher, eventType, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	f.Flush()
}

func statusJSON(id, peelID string, state State) string {
	b, _ := json.Marshal(StatusResponse{ID: id, PeelID: peelID, State: state})
	return string(b)
}

func TestStreamUntilApproved_ImmediateApproval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateApproved))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err != nil {
		t.Fatalf("streamUntilApproved returned error: %v", err)
	}
}

func TestStreamUntilApproved_PendingThenApproved(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send pending, then approved after a short delay.
		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StatePending))
		time.Sleep(50 * time.Millisecond)
		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateApproved))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err != nil {
		t.Fatalf("streamUntilApproved returned error: %v", err)
	}
}

func TestStreamUntilApproved_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StatePending))
		time.Sleep(50 * time.Millisecond)
		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateRejected))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err == nil {
		t.Fatal("expected error for rejection, got nil")
	}
	if got := err.Error(); got != "enrollment rejected" {
		t.Errorf("error = %q, want %q", got, "enrollment rejected")
	}
}

func TestStreamUntilApproved_Revoked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateRevoked))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err == nil {
		t.Fatal("expected error for revocation, got nil")
	}
	if got := err.Error(); got != "enrollment revoked" {
		t.Errorf("error = %q, want %q", got, "enrollment revoked")
	}
}

func TestStreamUntilApproved_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send a timeout event.
		writeSSE(w, f, "timeout", `{"message":"stream duration exceeded"}`)
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err == nil {
		t.Fatal("expected error for timeout, got nil")
	}
	if got := err.Error(); got != "SSE stream timeout" {
		t.Errorf("error = %q, want %q", got, "SSE stream timeout")
	}
}

func TestStreamUntilApproved_NonSSEContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return JSON instead of SSE — triggers fallback.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"error":"not supported"}`))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err == nil {
		t.Fatal("expected error for non-SSE content type, got nil")
	}
	if got := err.Error(); got != `unexpected Content-Type "application/json", expected text/event-stream` {
		t.Errorf("error = %q", got)
	}
}

func TestStreamUntilApproved_Heartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send heartbeat, then pending, then heartbeat, then approved.
		fmt.Fprintf(w, ": heartbeat\n\n")
		f.Flush()
		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StatePending))
		fmt.Fprintf(w, ": heartbeat\n\n")
		f.Flush()
		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateApproved))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	err := c.streamUntilApproved(context.Background(), "enr-1")
	if err != nil {
		t.Fatalf("streamUntilApproved returned error: %v", err)
	}
}

func TestWaitForApproval_SSESuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StateApproved))
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		pollBase:   10 * time.Millisecond,
		pollMax:    50 * time.Millisecond,
		logger:     slog.Default(),
	}

	err := c.waitForApproval(context.Background(), "enr-1")
	if err != nil {
		t.Fatalf("waitForApproval returned error: %v", err)
	}
}

func TestWaitForApproval_SSEFallsBackToPolling(t *testing.T) {
	// Track request counts per path.
	var streamHit, statusHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/enroll/enr-1/stream":
			streamHit++
			// Return non-SSE content type to trigger fallback.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"error":"sse not available"}`))

		case r.URL.Path == "/api/v1/enroll/enr-1/status":
			statusHits++
			// First poll: pending. Second poll: approved.
			state := StatePending
			if statusHits >= 2 {
				state = StateApproved
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(StatusResponse{
				ID:     "enr-1",
				PeelID: "peel-1",
				State:  state,
			})
		}
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		pollBase:   10 * time.Millisecond,
		pollMax:    50 * time.Millisecond,
		logger:     slog.Default(),
	}

	err := c.waitForApproval(context.Background(), "enr-1")
	if err != nil {
		t.Fatalf("waitForApproval returned error: %v", err)
	}

	if streamHit != 1 {
		t.Errorf("stream endpoint hit %d times, want 1", streamHit)
	}
	if statusHits < 2 {
		t.Errorf("status endpoint hit %d times, want >= 2", statusHits)
	}
}

func TestStreamUntilApproved_ContextCanceled(t *testing.T) {
	// Server that blocks until context is canceled.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		writeSSE(w, f, "state", statusJSON("enr-1", "peel-1", StatePending))

		// Block until client disconnects.
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := &Client{
		httpClient: srv.Client(),
		masterURLs: []string{srv.URL},
		peelID:     "peel-1",
		logger:     slog.Default(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := c.streamUntilApproved(ctx, "enr-1")
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}
