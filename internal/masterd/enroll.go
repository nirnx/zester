package masterd

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nirnx/zester/internal/health"
	"github.com/nirnx/zester/pkg/enroll"
	"github.com/nirnx/zester/pkg/masterapi"
	"github.com/nirnx/zester/pkg/target"
)

// startEnrollment wires the enrollment TLS server (challenge store,
// credential issuer, HTTP handler) plus the optional token-authenticated
// master REST API on the same listener, registers the 'enroll-server'
// readiness check, and starts the server goroutine. The returned stop
// function shuts the server down with the daemon lifecycle context, matching
// the pre-extraction `defer enrollSrv.Shutdown(ctx)`.
func (d *Daemon) startEnrollment(ctx context.Context) (func(), error) {
	challengeStore, err := enroll.NewChallengeStore(ctx, d.client.JetStream(), d.logger)
	if err != nil {
		return nil, fmt.Errorf("create challenge store: %w", err)
	}

	credIssuer, err := enroll.NewCredentialIssuer(enroll.CredentialIssuerConfig{
		AccountKP: d.accountKP,
	})
	if err != nil {
		return nil, fmt.Errorf("create credential issuer: %w", err)
	}

	// The embedded CA (if any) was started earlier in Run (before the
	// settings publisher, so cluster-info carries the CA bundle); reuse its
	// GetCertificate callback. External mode leaves it nil and the server
	// falls back to the operator-provided cert/key files.
	getCert := d.enrollGetCert

	handlerCfg := enroll.HandlerConfig{
		Store:      d.enrollStore,
		Challenges: challengeStore,
		Issuer:     credIssuer,
		Logger:     d.logger,
		// Emit zester.event._master.enroll.pending.<id> for every newly
		// created enrollment so reactor rules (e.g. auto-approve) can react.
		// Best-effort by contract: emit failures are Debug-logged.
		OnPending: d.emitEnrollPendingEvent,
	}
	if d.bootstrapEnabled() {
		handlerCfg.Bootstrap = d.bootstrapDoc
		d.logger.Info("enrollment discovery enabled (GET /api/v1/enroll/ca)",
			"advertise_urls", len(d.advertiseURLs()), "embedded_ca", d.caManager != nil)
	}
	if d.caManager != nil {
		// Compare peel-reported trusted-CA fingerprints against our root to
		// flag first-contact MITM (surfaced at the approval gate).
		handlerCfg.CARootPin = d.caManager.rootPin
	}
	enrollHandler := enroll.NewHandler(handlerCfg)

	enrollServerCfg := enroll.ServerConfig{
		ListenAddr:     d.cfg.Enroll.Addr,
		TLSCert:        d.cfg.Enroll.TLSCert,
		TLSKey:         d.cfg.Enroll.TLSKey,
		GetCertificate: getCert,
		Handler:        enrollHandler,
		Logger:         d.logger,
	}

	apiTokens := make([]masterapi.TokenEntry, 0, len(d.cfg.API.Tokens))
	for _, t := range d.cfg.API.Tokens {
		if t.Username == "" || t.TokenFile == "" {
			continue
		}
		apiTokens = append(apiTokens, masterapi.TokenEntry{
			Username:  t.Username,
			TokenFile: t.TokenFile,
		})
	}
	if d.cfg.API.DocsEnabled || len(apiTokens) > 0 {
		apiHandler := masterapi.NewHandler(masterapi.HandlerConfig{
			JobManager:  d.jobMgr,
			JS:          d.client.JetStream(),
			Lister:      &target.KVPeelLister{JS: d.client.JetStream()},
			EnrollStore: d.enrollStore,
			Tokens:      apiTokens,
			DocsEnabled: d.cfg.API.DocsEnabled,
			Logger:      d.logger.With("component", "masterapi"),
		})
		enrollServerCfg.ExtraRoutes = apiHandler.RegisterRoutes
		d.logger.Info("master API routes configured", "token_count", len(apiTokens), "docs_enabled", d.cfg.API.DocsEnabled)
	}

	enrollSrv, err := enroll.NewServer(enrollServerCfg)
	if err != nil {
		return nil, fmt.Errorf("create enrollment server: %w", err)
	}

	// The 'enroll-server' readiness check tracks the server goroutine's
	// state: OK is stored just before Start blocks; if Start returns with
	// a real error (bad cert, port taken), the check flips to Down with
	// that error so a dead enrollment API is visible on /readyz instead
	// of only as a single startup log line.
	d.enrollState.Store(health.CheckResult{Status: health.StatusDown, Message: "starting"})
	d.checker.Register("enroll-server", d.enrollServerCheck)

	go func() {
		d.enrollState.Store(health.CheckResult{Status: health.StatusOK})
		if err := enrollSrv.Start(); err != nil && err != http.ErrServerClosed {
			d.enrollState.Store(health.CheckResult{Status: health.StatusDown, Message: err.Error()})
			d.logger.Error("enrollment server error", "error", err)
		}
	}()
	return func() { enrollSrv.Shutdown(d.runCtx) }, nil
}

// enrollServerCheck is the 'enroll-server' readiness check; it returns the
// state last stored in d.enrollState by the server goroutine.
func (d *Daemon) enrollServerCheck(context.Context) health.CheckResult {
	return d.enrollState.Load().(health.CheckResult)
}
