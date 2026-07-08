package peeld

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/internal/config"
)

func newBootstrapAgent(t *testing.T, cfg *config.PeelConfig) *Agent {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	return &Agent{cfg: cfg, logger: slog.New(slog.NewTextHandler(nopWriter{}, nil))}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestResolveNATSURLs_Precedence(t *testing.T) {
	// Explicit nats_url wins over everything.
	a := newBootstrapAgent(t, &config.PeelConfig{NatsURL: "tls://explicit:4222", DataDir: t.TempDir()})
	if got := a.resolveNATSURLs(); len(got) != 1 || got[0] != "tls://explicit:4222" {
		t.Errorf("explicit = %v, want [tls://explicit:4222]", got)
	}

	// No explicit, no cache, master configured -> builtin tail (with warn).
	a = newBootstrapAgent(t, &config.PeelConfig{MasterURLs: []string{"https://m:8443"}, DataDir: t.TempDir()})
	if got := a.resolveNATSURLs(); len(got) != 1 || got[0] != builtinNATSTail {
		t.Errorf("no-cache = %v, want builtin tail", got)
	}

	// Cache present -> cache wins over builtin tail.
	dir := t.TempDir()
	a = newBootstrapAgent(t, &config.PeelConfig{MasterURLs: []string{"https://m:8443"}, DataDir: dir})
	a.writeBootstrapCache([]string{"tls://cached:4222"}, "test", "")
	if got := a.resolveNATSURLs(); len(got) != 1 || got[0] != "tls://cached:4222" {
		t.Errorf("cached = %v, want [tls://cached:4222]", got)
	}
}

func TestBootstrapCache_ValidationAndIdentity(t *testing.T) {
	dir := t.TempDir()
	a := newBootstrapAgent(t, &config.PeelConfig{MasterURLs: []string{"https://m:8443"}, EnrollCAPin: []string{"sha256:aa"}, DataDir: dir})

	// Loopback / non-tls entries are dropped on write.
	a.writeBootstrapCache([]string{"tls://good:4222", "tls://localhost:4222", "nats://bad:4222"}, "test", "")
	got := a.loadBootstrapCache()
	if len(got) != 1 || got[0] != "tls://good:4222" {
		t.Fatalf("loaded cache = %v, want [tls://good:4222]", got)
	}

	// Changing the identity (pins) invalidates the cache.
	a.cfg.EnrollCAPin = []string{"sha256:bb"}
	if got := a.loadBootstrapCache(); got != nil {
		t.Errorf("identity change should invalidate cache, got %v", got)
	}
}

func TestBootstrapCache_MissingFileIsNil(t *testing.T) {
	a := newBootstrapAgent(t, &config.PeelConfig{DataDir: t.TempDir()})
	if got := a.loadBootstrapCache(); got != nil {
		t.Errorf("missing cache = %v, want nil", got)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.DataDir, bootstrapCacheFileName)); !os.IsNotExist(err) {
		t.Error("loadBootstrapCache must not create the file")
	}
}
