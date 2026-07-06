package enroll

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nirnx/zester/pkg/auth"
)

// maxResponseBody is the maximum response body size the client will read
// from the enrollment API. This prevents a malicious server from sending
// an unbounded payload to exhaust client memory.
const maxResponseBody = 1 << 20 // 1 MB

// maxChallengeRetries bounds the transparent nonce re-requests performed
// when the server reports an unknown/expired challenge nonce (e.g. after
// a NATS restart wiped the in-memory enroll-challenges bucket). The
// initial attempt plus maxChallengeRetries re-requests are made before
// the error is surfaced, so a genuinely broken server still errors out.
const maxChallengeRetries = 2

// unknownChallengeMessage is the error body the enrollment handler
// returns (with HTTP 401) when a challenge nonce is unknown, expired,
// or already consumed. See Handler.handleEnroll in handler.go.
const unknownChallengeMessage = "challenge verification failed"

// ClientConfig configures the peel-side enrollment client.
type ClientConfig struct {
	// MasterURL is the base URL of the master enrollment API (e.g., "https://master:8443").
	// Retained for backward compatibility; prefer MasterURLs for
	// multi-master fallback.
	MasterURL string

	// MasterURLs is an ordered list of master enrollment API base URLs.
	// The client sends every request to the current URL and rotates to
	// the next one on connection-level failures (dial errors, timeouts,
	// 5xx responses). Successful responses pin the current URL. If
	// empty, []string{MasterURL} is used.
	MasterURLs []string

	// PeelID is the peel's identifier.
	PeelID string

	// CAFile is the path to the CA certificate for server TLS verification.
	// If empty, the system CA pool is used.
	CAFile string

	// PollInterval is the base interval for polling enrollment status.
	// Defaults to 10 seconds.
	PollInterval time.Duration

	// MaxPollInterval is the maximum poll interval with exponential backoff.
	// Defaults to 5 minutes.
	MaxPollInterval time.Duration

	// Logger is the structured logger.
	Logger *slog.Logger
}

// Client is the peel-side enrollment HTTP client.
type Client struct {
	httpClient *http.Client
	masterURLs []string
	peelID     string
	pollBase   time.Duration
	pollMax    time.Duration
	logger     *slog.Logger

	urlMu  sync.Mutex
	urlIdx int
}

// NewClient creates a peel-side enrollment client.
func NewClient(cfg ClientConfig) (*Client, error) {
	urls := make([]string, 0, len(cfg.MasterURLs)+1)
	for _, u := range cfg.MasterURLs {
		if u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 && cfg.MasterURL != "" {
		urls = []string{cfg.MasterURL}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("enroll: master URL is required")
	}
	// Fail fast on IDs the master would reject at submit anyway (subject-token
	// charset rules — see ValidatePeelID).
	if err := ValidatePeelID(cfg.PeelID); err != nil {
		return nil, err
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Second
	}
	if cfg.MaxPollInterval == 0 {
		cfg.MaxPollInterval = 5 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	if cfg.CAFile != "" {
		caPEM, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("enroll: read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("enroll: failed to parse CA certificate")
		}
		tlsCfg.RootCAs = pool
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: tlsCfg,
			},
		},
		masterURLs: urls,
		peelID:     cfg.PeelID,
		pollBase:   cfg.PollInterval,
		pollMax:    cfg.MaxPollInterval,
		logger:     cfg.Logger,
	}, nil
}

// apiError is a non-2xx response from the enrollment API.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("HTTP %d", e.status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

// newAPIError builds an apiError from a non-2xx response, reading a
// bounded amount of the body.
func newAPIError(resp *http.Response) *apiError {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return &apiError{status: resp.StatusCode}
	}
	return &apiError{status: resp.StatusCode, body: string(body)}
}

// connError wraps a connection-level failure (dial error, timeout, or a
// 5xx response) — the class of error that triggers rotation to the next
// master URL and is safe to retry.
type connError struct{ err error }

func (e *connError) Error() string { return e.err.Error() }
func (e *connError) Unwrap() error { return e.err }

// isConnError reports whether err (or anything it wraps) is a
// connection-level failure.
func isConnError(err error) bool {
	var ce *connError
	return errors.As(err, &ce)
}

// isUnknownChallenge reports whether err is the enrollment API's
// unknown/expired-challenge rejection (HTTP 401 with the handler's
// "challenge verification failed" body).
func isUnknownChallenge(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.status == http.StatusUnauthorized &&
		strings.Contains(ae.body, unknownChallengeMessage)
}

// currentURL returns the master URL the client is currently pinned to.
func (c *Client) currentURL() string {
	c.urlMu.Lock()
	defer c.urlMu.Unlock()
	return c.masterURLs[c.urlIdx]
}

// rotateURL advances to the next master URL after a connection-level
// failure against from. Rotation is skipped when only one URL is
// configured or when the client already rotated away from from —
// successful responses pin the current URL, so the client never
// rotates needlessly.
func (c *Client) rotateURL(from string) {
	c.urlMu.Lock()
	defer c.urlMu.Unlock()
	if len(c.masterURLs) < 2 || c.masterURLs[c.urlIdx] != from {
		return
	}
	c.urlIdx = (c.urlIdx + 1) % len(c.masterURLs)
	c.logger.Info("enroll: rotating to next master URL",
		"peel_id", c.peelID,
		"from", from,
		"to", c.masterURLs[c.urlIdx],
	)
}

// failConn classifies err as a connection-level failure: it rotates to
// the next master URL (unless ctx is already canceled) and wraps err so
// callers can identify it via isConnError. The actual retry is left to
// the caller's existing backoff machinery.
func (c *Client) failConn(ctx context.Context, base string, err error) error {
	if ctx.Err() == nil {
		c.rotateURL(base)
	}
	return &connError{err: err}
}

// EnrollResult holds the result of a successful enrollment flow.
type EnrollResult struct {
	EnrollmentID string
	JWT          string
	ExpiresAt    time.Time
}

// Enroll performs the complete enrollment flow:
// 1. Request a challenge nonce (retries with backoff if master not ready)
// 2. Sign the challenge with the peel's nkey
// 3. Submit the enrollment request
// 4. Poll until approved
// 5. Download credentials
func (c *Client) Enroll(ctx context.Context, keyBundle *auth.KeyBundle) (*EnrollResult, error) {
	curveKey, err := auth.CurvePublicKeyFromSeed(keyBundle.Seed)
	if err != nil {
		return nil, fmt.Errorf("enroll: derive curve key: %w", err)
	}

	hostname, _ := os.Hostname()

	// Steps 1-3 are retried indefinitely with backoff (max 30s) — the master's
	// enrollment HTTP API may not be ready yet (e.g., JetStream cluster RAFT
	// leader election can take 30s+). Only context cancellation stops the loop.
	// Connection-level failures rotate through the configured master URLs
	// (rotation happens inside the HTTP helpers), so each retry may hit a
	// different master.
	const maxConnBackoff = 30 * time.Second
	var enrollResp *EnrollResponse
	interval := c.pollBase
	for {
		enrollResp, err = c.tryEnroll(ctx, keyBundle, curveKey, hostname)
		if err == nil {
			break
		}

		c.logger.Warn("enrollment attempt failed, retrying",
			"peel_id", c.peelID, "error", err, "retry_in", interval)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("enroll: %w (last error: %v)", ctx.Err(), err)
		case <-time.After(interval):
		}
		interval = min(interval*2, maxConnBackoff)
	}

	c.logger.Info("enrollment submitted, awaiting approval",
		"enrollment_id", enrollResp.ID,
		"peel_id", c.peelID,
		"state", enrollResp.State,
	)

	// Step 4: Wait for approval (SSE stream with polling fallback).
	if err := c.waitForApproval(ctx, enrollResp.ID); err != nil {
		return nil, fmt.Errorf("enroll: wait for approval: %w", err)
	}

	// Step 5: Download credentials. Connection-level failures (the master
	// that served us may have gone down while we waited for approval)
	// rotate to the next master URL and retry with backoff, bounded so a
	// persistent outage still surfaces an error.
	c.logger.Info("enrollment approved, downloading credentials",
		"enrollment_id", enrollResp.ID,
	)
	maxCredsAttempts := 2 * len(c.masterURLs)
	var creds *CredsResponse
	interval = c.pollBase
	for attempt := 1; ; attempt++ {
		creds, err = c.downloadCreds(ctx, enrollResp.ID, keyBundle)
		if err == nil {
			break
		}
		if !isConnError(err) || attempt >= maxCredsAttempts {
			return nil, fmt.Errorf("enroll: download credentials: %w", err)
		}

		c.logger.Warn("credential download failed, retrying",
			"enrollment_id", enrollResp.ID, "error", err, "retry_in", interval)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("enroll: %w (last error: %v)", ctx.Err(), err)
		case <-time.After(interval):
		}
		interval = min(interval*2, maxConnBackoff)
	}

	jwt, err := DecodeJWTFromTransport(creds.CredsData)
	if err != nil {
		return nil, fmt.Errorf("enroll: decode credentials: %w", err)
	}

	expiresAt, _ := time.Parse(time.RFC3339, creds.ExpiresAt)

	c.logger.Info("enrollment complete",
		"enrollment_id", enrollResp.ID,
		"peel_id", c.peelID,
		"expires_at", expiresAt,
	)

	return &EnrollResult{
		EnrollmentID: enrollResp.ID,
		JWT:          jwt,
		ExpiresAt:    expiresAt,
	}, nil
}

// tryEnroll attempts steps 1-3 of the enrollment flow (nonce → sign → submit).
// Returns the enrollment response on success, or an error if any step fails.
// When the server reports an unknown/expired challenge (e.g. its volatile
// challenge store was wiped between nonce issue and submit), a fresh nonce
// is transparently requested and the enrollment resubmitted, bounded to
// maxChallengeRetries re-requests.
func (c *Client) tryEnroll(ctx context.Context, keyBundle *auth.KeyBundle, curveKey, hostname string) (*EnrollResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= maxChallengeRetries; attempt++ {
		if attempt > 0 {
			c.logger.Info("challenge unknown or expired, re-requesting nonce",
				"peel_id", c.peelID, "attempt", attempt)
		}

		// Step 1: Get challenge nonce.
		c.logger.Info("requesting enrollment challenge", "peel_id", c.peelID)
		nonce, err := c.requestNonce(ctx, keyBundle.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("request nonce: %w", err)
		}

		// Step 2: Sign the challenge (includes curve key for cryptographic binding).
		sig, err := SignChallenge(keyBundle.Seed, nonce.Challenge, curveKey)
		if err != nil {
			return nil, fmt.Errorf("sign challenge: %w", err)
		}

		// Step 3: Submit enrollment.
		c.logger.Info("submitting enrollment request", "peel_id", c.peelID)
		resp, err := c.submitEnrollment(ctx, EnrollRequest{
			PeelID:         c.peelID,
			PublicKey:      keyBundle.PublicKey,
			CurvePublicKey: curveKey,
			Hostname:       hostname,
			ChallengeID:    nonce.ChallengeID,
			Signature:      sig,
		})
		if err == nil {
			return resp, nil
		}
		if !isUnknownChallenge(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("challenge rejected after %d nonce re-requests: %w", maxChallengeRetries, lastErr)
}

func (c *Client) requestNonce(ctx context.Context, publicKey string) (*NonceResponse, error) {
	base := c.currentURL()
	u := fmt.Sprintf("%s/api/v1/enroll/nonce?peel_id=%s&public_key=%s",
		base, url.QueryEscape(c.peelID), url.QueryEscape(publicKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.failConn(ctx, base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var reqErr error = newAPIError(resp)
		if resp.StatusCode >= 500 {
			reqErr = c.failConn(ctx, base, reqErr)
		}
		return nil, fmt.Errorf("nonce request failed: %w", reqErr)
	}

	var nonce NonceResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&nonce); err != nil {
		return nil, fmt.Errorf("decode nonce response: %w", err)
	}
	return &nonce, nil
}

func (c *Client) submitEnrollment(ctx context.Context, enrollReq EnrollRequest) (*EnrollResponse, error) {
	body, err := json.Marshal(enrollReq)
	if err != nil {
		return nil, err
	}

	base := c.currentURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.failConn(ctx, base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var reqErr error = newAPIError(resp)
		if resp.StatusCode >= 500 {
			reqErr = c.failConn(ctx, base, reqErr)
		}
		return nil, fmt.Errorf("enrollment request failed: %w", reqErr)
	}

	var enrollResp EnrollResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&enrollResp); err != nil {
		return nil, fmt.Errorf("decode enrollment response: %w", err)
	}
	return &enrollResp, nil
}

func (c *Client) pollUntilApproved(ctx context.Context, enrollmentID string) error {
	interval := c.pollBase

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}

		status, err := c.getStatus(ctx, enrollmentID)
		if err != nil {
			c.logger.Warn("enroll: status poll failed, retrying",
				"enrollment_id", enrollmentID, "error", err)
			// Increase backoff on error.
			interval = min(interval*2, c.pollMax)
			continue
		}

		switch status.State {
		case StateApproved:
			return nil
		case StateRejected:
			return fmt.Errorf("enrollment rejected")
		case StatePending:
			c.logger.Debug("enrollment still pending",
				"enrollment_id", enrollmentID)
			interval = min(interval*2, c.pollMax)
		default:
			return fmt.Errorf("unexpected enrollment state: %s", status.State)
		}
	}
}

// waitForApproval tries SSE streaming first for instant notification,
// falling back to polling if SSE is unavailable (proxy, old server, etc.).
func (c *Client) waitForApproval(ctx context.Context, enrollmentID string) error {
	err := c.streamUntilApproved(ctx, enrollmentID)
	if err == nil {
		return nil
	}
	c.logger.Warn("SSE stream unavailable, falling back to polling", "error", err)
	return c.pollUntilApproved(ctx, enrollmentID)
}

// streamUntilApproved opens an SSE stream to receive real-time enrollment
// state changes. Returns nil on approval, an error on rejection/revocation,
// or a non-nil error to signal the caller to fall back to polling.
func (c *Client) streamUntilApproved(ctx context.Context, enrollmentID string) error {
	base := c.currentURL()
	u := fmt.Sprintf("%s/api/v1/enroll/%s/stream", base, enrollmentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// Use a separate client with no timeout — SSE connections are long-lived.
	sseClient := *c.httpClient
	sseClient.Timeout = 0

	resp, err := sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", c.failConn(ctx, base, err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("SSE connect: %w", c.failConn(ctx, base, newAPIError(resp)))
	}

	// Verify the server is sending SSE. If not, fall back to polling.
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		return fmt.Errorf("unexpected Content-Type %q, expected text/event-stream", ct)
	}

	c.logger.Info("SSE stream connected", "enrollment_id", enrollmentID)

	scanner := bufio.NewScanner(resp.Body)
	var eventType string

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")

		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")

			if eventType == "timeout" {
				return fmt.Errorf("SSE stream timeout")
			}

			if eventType == "state" {
				var status StatusResponse
				if err := json.Unmarshal([]byte(data), &status); err != nil {
					c.logger.Warn("enroll: SSE decode error", "error", err)
					continue
				}

				c.logger.Debug("SSE state update",
					"enrollment_id", enrollmentID, "state", status.State)

				switch status.State {
				case StateApproved:
					return nil
				case StateRejected:
					return fmt.Errorf("enrollment rejected")
				case StateRevoked:
					return fmt.Errorf("enrollment revoked")
				case StatePending:
					// Still waiting, continue.
				default:
					return fmt.Errorf("unexpected enrollment state: %s", status.State)
				}
			}

		case strings.HasPrefix(line, ":"):
			// SSE comment (heartbeat), ignore.

		case line == "":
			// Empty line marks end of an event, reset event type.
			eventType = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE read: %w", err)
	}

	// Stream ended (EOF) without reaching approval — fall back to polling.
	return fmt.Errorf("SSE stream closed unexpectedly")
}

func (c *Client) getStatus(ctx context.Context, enrollmentID string) (*StatusResponse, error) {
	base := c.currentURL()
	u := fmt.Sprintf("%s/api/v1/enroll/%s/status", base, enrollmentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.failConn(ctx, base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var reqErr error = newAPIError(resp)
		if resp.StatusCode >= 500 {
			reqErr = c.failConn(ctx, base, reqErr)
		}
		return nil, fmt.Errorf("status request failed: %w", reqErr)
	}

	var status StatusResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return &status, nil
}

func (c *Client) downloadCreds(ctx context.Context, enrollmentID string, keyBundle *auth.KeyBundle) (*CredsResponse, error) {
	sigB64, err := SignEnrollmentID(keyBundle.Seed, enrollmentID)
	if err != nil {
		return nil, err
	}

	base := c.currentURL()
	u := fmt.Sprintf("%s/api/v1/enroll/%s/creds", base, enrollmentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Nkey %s:%s", keyBundle.PublicKey, sigB64))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.failConn(ctx, base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var reqErr error = newAPIError(resp)
		if resp.StatusCode >= 500 {
			reqErr = c.failConn(ctx, base, reqErr)
		}
		return nil, fmt.Errorf("credential download failed: %w", reqErr)
	}

	var creds CredsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&creds); err != nil {
		return nil, fmt.Errorf("decode credentials response: %w", err)
	}
	return &creds, nil
}
