package starmod_test

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/exec"
	"github.com/nirnx/zester/pkg/exec/exectest"
	"github.com/nirnx/zester/pkg/modschema"
	"github.com/nirnx/zester/pkg/starmod"
	"github.com/nirnx/zester/pkg/state"
)

// warnSink collects formatted warnings for validation-parity assertions.
type warnSink struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnSink) Warnf(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, fmt.Sprintf(format, args...))
}

func (w *warnSink) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

func specSummary(spec *modschema.Spec) string {
	if spec == nil {
		return "<nil>"
	}
	return spec.Doc.Summary
}

func loaderWithOpts(t *testing.T, statesDir string, opts modschema.DecodeOptions) *starmod.Loader {
	t.Helper()
	mctx := &exec.ModuleContext{
		ProviderSet: exec.ProviderSet{
			Package: exectest.NewFakePackageExec("test"),
			File:    exectest.NewFakeFileExec(),
			Command: exectest.NewFakeCommandExec(),
		},
		Facts:  map[string]any{"os": "linux"},
		Logger: slog.Default(),
	}
	return starmod.NewLoader(starmod.LoaderConfig{
		StatesDir:     statesDir,
		ModuleContext: mctx,
		Logger:        slog.Default(),
		DecodeOptions: opts,
	})
}

const nginxWithDocsAndParams = `
PARAMS = {
    "path": {"type": "str", "required": True, "usage": "config file path"},
    "mode": {"type": "str", "default": "0644", "usage": "file mode"},
}

def configured(id, config):
    """Manage the nginx config file.

    Renders the configuration and reloads the daemon on change.
    """
    return {"changed": True}
`

func TestLoader_CapturesDocstringAndParams_ViaRegistry(t *testing.T) {
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "nginx.star", nginxWithDocsAndParams)

	loader := loaderWithOpts(t, dir, modschema.DecodeOptions{})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	// sys.doc reaches Starlark modules through the state Registry's Describe.
	info, ok := registry.Describe("nginx.configured")
	if !ok {
		t.Fatal("Describe(nginx.configured) not found via registry")
	}
	if info.Doc.Summary != "Manage the nginx config file." {
		t.Errorf("summary = %q", info.Doc.Summary)
	}
	if info.Kind != modschema.KindState {
		t.Errorf("kind = %q", info.Kind)
	}
	fields := map[string]modschema.Field{}
	for _, f := range info.Params {
		fields[f.Name] = f
	}
	if !fields["path"].Required {
		t.Error("path should be required")
	}
	if fields["mode"].Default != "0644" {
		t.Errorf("mode default = %q", fields["mode"].Default)
	}

	// RenderText (the shared sys.doc / `zester doc` currency) shows the docs.
	rendered := modschema.RenderText(info)
	for _, want := range []string{
		"nginx.configured (state)",
		"Manage the nginx config file.",
		"Renders the configuration and reloads the daemon on change.",
		"path", "mode", "Source",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("RenderText missing %q\n%s", want, rendered)
		}
	}
}

func TestLoader_LoadedSpec_HotReloadRecapturesDocs(t *testing.T) {
	dir := t.TempDir()
	modulesDir := filepath.Join(dir, "_modules")
	writeStarFile(t, modulesDir, "svc.star", `
def running(id, config):
    """Version one summary."""
    return {"changed": True}
`)

	loader := loaderWithOpts(t, dir, modschema.DecodeOptions{})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	spec, ok := loader.LoadedSpec("svc.running")
	if !ok || spec.Doc.Summary != "Version one summary." {
		t.Fatalf("initial LoadedSpec summary = %q (ok=%v)", specSummary(spec), ok)
	}

	// Rewrite the docstring and bump mtime so the loader re-parses.
	time.Sleep(10 * time.Millisecond)
	path := filepath.Join(modulesDir, "svc.star")
	if err := os.WriteFile(path, []byte(`
def running(id, config):
    """Version two summary, revised."""
    return {"changed": True}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(path, future, future)

	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	spec2, ok := loader.LoadedSpec("svc.running")
	if !ok {
		t.Fatal("LoadedSpec missing after reload")
	}
	if spec2.Doc.Summary != "Version two summary, revised." {
		t.Errorf("re-captured summary = %q, want v2", spec2.Doc.Summary)
	}
}

func TestLoader_ValidationParity_UnknownKeyWarns(t *testing.T) {
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "nginx.star", nginxWithDocsAndParams)

	sink := &warnSink{}
	loader := loaderWithOpts(t, dir, modschema.DecodeOptions{
		Unknown: modschema.PolicyWarn,
		Warnf:   sink.Warnf,
	})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	// A known param, a reserved directive, and an unknown key.
	_, err := registry.Build("nginx.configured", "web", map[string]any{
		"path":    "/etc/nginx/nginx.conf",
		"require": []any{"pkg.installed:nginx"},
		"bogus":   "value",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	msgs := sink.all()
	if len(msgs) != 1 {
		t.Fatalf("warnings = %v, want exactly one (the unknown key)", msgs)
	}
	if !strings.Contains(msgs[0], "bogus") {
		t.Errorf("warning %q does not mention the unknown key", msgs[0])
	}
	// Neither the known param nor the reserved requisite warns.
	if strings.Contains(msgs[0], "path") || strings.Contains(msgs[0], "require") {
		t.Errorf("known/reserved key wrongly warned: %q", msgs[0])
	}
}

func TestLoader_OpenParams_NoUnknownKeyWarnings(t *testing.T) {
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "free.star", `
def form(id, config):
    """A module with no declared parameters."""
    return {"changed": True}
`)

	sink := &warnSink{}
	loader := loaderWithOpts(t, dir, modschema.DecodeOptions{
		Unknown: modschema.PolicyWarn,
		Warnf:   sink.Warnf,
	})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Build("free.form", "x", map[string]any{"anything": 1, "goes": 2}); err != nil {
		t.Fatal(err)
	}
	if msgs := sink.all(); len(msgs) != 0 {
		t.Errorf("open-params module should not warn on unknown keys, got %v", msgs)
	}
	// It is still described as open-params.
	info, ok := registry.Describe("free.form")
	if !ok {
		t.Fatal("Describe(free.form) missing")
	}
	if info.Doc.Summary != "A module with no declared parameters." {
		t.Errorf("summary = %q", info.Doc.Summary)
	}
}

func TestLoader_RegistryParse_NonNilNewParams(t *testing.T) {
	dir := t.TempDir()
	writeStarFile(t, filepath.Join(dir, "_modules"), "nginx.star", nginxWithDocsAndParams)

	loader := loaderWithOpts(t, dir, modschema.DecodeOptions{})
	registry := state.NewRegistry()
	if _, err := loader.LoadGlobal(registry); err != nil {
		t.Fatal(err)
	}

	// Registry.Parse allocates through spec.NewParams(); a nil there would fail
	// with "dst must be a non-nil pointer". A synthetic proto makes it work.
	report, err := registry.Parse("nginx.configured", "web", map[string]any{
		"path": "/etc/nginx/nginx.conf",
		"junk": true,
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(report.UnknownKeys) != 1 || report.UnknownKeys[0] != "junk" {
		t.Errorf("UnknownKeys = %v, want [junk]", report.UnknownKeys)
	}
}
