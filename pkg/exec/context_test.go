package exec

import (
	"sync"
	"testing"
)

func TestFactStringNested(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{
			"family": "debian",
			"name":   "Ubuntu",
		},
	}

	if got := factString(facts, "os", "family"); got != "debian" {
		t.Errorf("factString(os, family): got %q, want %q", got, "debian")
	}
	if got := factString(facts, "os", "name"); got != "Ubuntu" {
		t.Errorf("factString(os, name): got %q, want %q", got, "Ubuntu")
	}
}

func TestFactStringTopLevel(t *testing.T) {
	facts := map[string]any{
		"hostname": "web-01",
	}
	if got := factString(facts, "hostname"); got != "web-01" {
		t.Errorf("factString(hostname): got %q, want %q", got, "web-01")
	}
}

func TestFactStringMissing(t *testing.T) {
	facts := map[string]any{
		"os": map[string]any{"family": "debian"},
	}

	if got := factString(facts, "os", "missing"); got != "" {
		t.Errorf("factString(os, missing): got %q, want empty", got)
	}
	if got := factString(facts, "missing"); got != "" {
		t.Errorf("factString(missing): got %q, want empty", got)
	}
	if got := factString(facts, "os", "family", "deep"); got != "" {
		t.Errorf("factString(os, family, deep): got %q, want empty", got)
	}
}

func TestFactStringNilFacts(t *testing.T) {
	if got := factString(nil, "os"); got != "" {
		t.Errorf("factString(nil): got %q, want empty", got)
	}
}

func TestFactStringNonStringValue(t *testing.T) {
	facts := map[string]any{
		"count": 42,
	}
	if got := factString(facts, "count"); got != "" {
		t.Errorf("factString(count=42): got %q, want empty (not a string)", got)
	}
}

func TestNewModuleContextNilLogger(t *testing.T) {
	ps := &ProviderSet{
		Command: &OSCommandExec{},
		File:    &OSFileExec{},
	}
	mctx := NewModuleContext(ps, nil, nil, nil)
	if mctx.Logger == nil {
		t.Fatal("Logger should default to slog.Default()")
	}
	if mctx.Command == nil {
		t.Fatal("Command should be set from ProviderSet")
	}
	if mctx.File == nil {
		t.Fatal("File should be set from ProviderSet")
	}
}

func TestNewModuleContextCopiesProviders(t *testing.T) {
	cmd := &OSCommandExec{}
	file := &OSFileExec{}
	ps := &ProviderSet{
		Command: cmd,
		File:    file,
	}
	facts := map[string]any{"key": "val"}
	settings := map[string]any{"s": "v"}

	mctx := NewModuleContext(ps, facts, settings, nil)

	if mctx.Package != nil {
		t.Error("Package should be nil when not in ProviderSet")
	}
	if mctx.Service != nil {
		t.Error("Service should be nil when not in ProviderSet")
	}
	if mctx.Facts["key"] != "val" {
		t.Error("Facts not passed through")
	}
	if mctx.Settings["s"] != "v" {
		t.Error("Settings not passed through")
	}
}

func TestWithFactsSettingsSharesProviders(t *testing.T) {
	cmd := &OSCommandExec{}
	file := &OSFileExec{}
	ps := &ProviderSet{
		Command: cmd,
		File:    file,
	}
	mctx := NewModuleContext(ps, map[string]any{"old": true}, nil, nil)
	mctx.RenderTemplate = func(name, source string, extra map[string]any) (string, error) {
		return "rendered", nil
	}

	derived := mctx.WithFactsSettings(map[string]any{"new": true}, map[string]any{"s": 1})

	if derived == mctx {
		t.Fatal("WithFactsSettings should return a new ModuleContext, not the receiver")
	}
	if derived.Command != CommandExec(cmd) {
		t.Error("Command provider should be shared with the receiver")
	}
	if derived.File != FileExec(file) {
		t.Error("File provider should be shared with the receiver")
	}
	if derived.Logger != mctx.Logger {
		t.Error("Logger should be shared with the receiver")
	}
	if derived.RenderTemplate == nil {
		t.Fatal("RenderTemplate should be carried over to the derived context")
	}
	if out, err := derived.RenderTemplate("n", "s", nil); err != nil || out != "rendered" {
		t.Errorf("RenderTemplate: got (%q, %v), want (rendered, nil)", out, err)
	}
}

func TestWithFactsSettingsIsolatesFactsSettings(t *testing.T) {
	ps := &ProviderSet{Command: &OSCommandExec{}}
	origFacts := map[string]any{"host": "a"}
	origSettings := map[string]any{"k": "v"}
	mctx := NewModuleContext(ps, origFacts, origSettings, nil)

	newFacts := map[string]any{"host": "b"}
	newSettings := map[string]any{"k": "w"}
	derived := mctx.WithFactsSettings(newFacts, newSettings)

	if derived.Facts["host"] != "b" || derived.Settings["k"] != "w" {
		t.Error("derived context should carry the new facts/settings")
	}
	if mctx.Facts["host"] != "a" || mctx.Settings["k"] != "v" {
		t.Error("receiver facts/settings must not be modified")
	}

	// A second derivation must not affect the first.
	derived2 := mctx.WithFactsSettings(map[string]any{"host": "c"}, nil)
	if derived.Facts["host"] != "b" {
		t.Error("earlier derived context mutated by later derivation")
	}
	if derived2.Settings != nil {
		t.Error("nil settings should stay nil on the derived context")
	}
}

func TestWithFactsSettingsConcurrent(t *testing.T) {
	ps := &ProviderSet{Command: &OSCommandExec{}, File: &OSFileExec{}}
	mctx := NewModuleContext(ps, map[string]any{"base": true}, map[string]any{"base": true}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := mctx.WithFactsSettings(map[string]any{"i": i}, map[string]any{"i": i})
			if d.Command == nil || d.File == nil {
				t.Error("derived context lost providers")
			}
			if d.Facts["i"] != i {
				t.Errorf("derived facts: got %v, want %d", d.Facts["i"], i)
			}
			// Read-only access to the receiver must be race-free.
			if d2 := d.WithFactsSettings(nil, nil); d2.Facts != nil {
				t.Error("nil facts should stay nil")
			}
		}(i)
	}
	wg.Wait()
}

func TestDetectProvidersAlwaysSetsCommandAndFile(t *testing.T) {
	ps := DetectProviders(nil, nil)
	if ps.Command == nil {
		t.Fatal("Command should always be set")
	}
	if ps.File == nil {
		t.Fatal("File should always be set")
	}
}

func TestDetectProvidersServiceMatchesEnvironment(t *testing.T) {
	// Service detection is PATH-based: a systemd provider is set exactly
	// when systemctl is available (Linux CI runners have it, macOS dev
	// machines don't) — assert the contract, not a fixed platform.
	ps := DetectProviders(nil, nil)
	if commandExists("systemctl") {
		if ps.Service == nil {
			t.Error("systemctl on PATH: Service should be the systemd provider")
		}
	} else if ps.Service != nil {
		t.Error("no systemctl on PATH: Service should be nil")
	}
}
