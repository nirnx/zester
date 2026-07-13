package peeld

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/state"
)

// capturingHandler records Warn-level log messages so a test can assert whether
// the peel's unknown-key decode policy (spec §5, PolicyWarn) emitted a warning.
type capturingHandler struct {
	mu    sync.Mutex
	warns []string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.mu.Lock()
		h.warns = append(h.warns, r.Message)
		h.mu.Unlock()
	}
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.warns...)
}

// registerForTest wires the peel's real registration path (registerStateModules)
// with a capturing logger and the decode policy the strict_params knob would
// produce (strict=true → PolicyError, strict=false → PolicyWarn), returning the
// registry and the handler so a test can build a module and inspect the errors
// or warnings the decode emitted.
func registerForTest(t *testing.T, strict bool) (*state.Registry, *capturingHandler) {
	t.Helper()
	h := &capturingHandler{}
	logger := slog.New(h)
	registry := state.NewRegistry()
	mctx := exec.NewModuleContext(
		&exec.ProviderSet{Package: exectest.NewFakePackageExec("apt")},
		map[string]any{}, nil, logger,
	)
	registerStateModules(registry, mctx, decodeOptions(strict, logger))
	return registry, h
}

// TestRegisterStateModules_UnknownKeyWarns asserts the relaxed policy
// (strict_params: false → PolicyWarn): a typo'd parameter on a migrated
// (Spec-carrying) module logs a Warn through the peel's slog logger and the
// state STILL builds — warn-only, never fatal.
func TestRegisterStateModules_UnknownKeyWarns(t *testing.T) {
	registry, h := registerForTest(t, false)

	// pkg.removed is a migrated module (carries a Spec). "nmae" is a typo of the
	// real "name" parameter and matches no reserved key.
	s, err := registry.Build("pkg.removed", "telnet", map[string]any{
		"nmae": "telnet",
	})
	if err != nil {
		t.Fatalf("build must not error under PolicyWarn: %v", err)
	}
	if s == nil {
		t.Fatal("state must still build despite the unknown key")
	}
	// The primary param falls back to the state ID, so the state is usable.
	if got := s.Name(); got != "pkg.removed:telnet" {
		t.Errorf("Name() = %q, want pkg.removed:telnet", got)
	}

	warns := h.messages()
	if len(warns) != 1 {
		t.Fatalf("want exactly 1 warning, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0], "pkg.removed") || !strings.Contains(warns[0], "nmae") {
		t.Errorf("warning %q must name the module and the unknown key", warns[0])
	}
	if !strings.Contains(warns[0], "unknown parameter") {
		t.Errorf("warning %q must describe the unknown parameter", warns[0])
	}
}

// TestRegisterStateModules_ReservedKeysNoWarn asserts that the fleet-wide
// reserved keys (a requisite, a generic attribute) and the exec-layer "test"
// dry-run flag are known-not-parameters under BOTH policies: none produce an
// unknown-key warning (relaxed) OR error (strict), and the state builds. This is
// the core audit invariant behind the strict flip — reserved/injected keys must
// never false-positive even when unknown parameters are fatal.
func TestRegisterStateModules_ReservedKeysNoWarn(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := "relaxed"
		if strict {
			name = "strict"
		}
		t.Run(name, func(t *testing.T) {
			registry, h := registerForTest(t, strict)

			s, err := registry.Build("pkg.removed", "telnet", map[string]any{
				"name":    "telnet",
				"require": []any{"cmd.run:stop-telnet"}, // requisite (Reserved)
				"onlyif":  "true",                       // generic attribute (Reserved)
				"test":    true,                         // exec-layer flag (ExtraReserved)
			})
			if err != nil {
				t.Fatalf("build must not error under strict=%v: %v", strict, err)
			}
			if s == nil {
				t.Fatal("state must build")
			}

			if warns := h.messages(); len(warns) != 0 {
				t.Fatalf("reserved keys must not warn, got %d: %v", len(warns), warns)
			}
		})
	}
}
