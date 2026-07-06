package compiler_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/compiler"
	"github.com/nirnx/zester/pkg/state/modules"
	"github.com/nirnx/zester/pkg/template"
)

// compileStates writes a single init.zy under a formula dir and compiles it,
// returning the built states keyed by Name().
func compileStates(t *testing.T, formula, content string) map[string]state.State {
	t.Helper()
	tmpDir := t.TempDir()
	registry := setupTestRegistry()
	// module.run captures the registry so it can dispatch to the test modules.
	registry.Register("module.run", modules.NewModuleRunBuilder(registry))

	engine, err := template.NewEngine(template.EngineConfig{BasePath: tmpDir})
	if err != nil {
		t.Fatal(err)
	}
	c := compiler.NewCompiler(compiler.CompilerConfig{
		StatesDir: tmpDir,
		Engine:    engine,
		Registry:  registry,
		Facts:     map[string]any{"os": "linux"},
	})

	if err := os.MkdirAll(filepath.Join(tmpDir, formula), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, formula, "init.zy"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := c.Compile(compiler.StateRef(formula))
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	out := make(map[string]state.State, len(result.States))
	for _, s := range result.States {
		out[s.Name()] = s
	}
	return out
}

func hasRef(list []string, ref string) bool {
	for _, r := range list {
		if r == ref {
			return true
		}
	}
	return false
}

func TestRequireIn(t *testing.T) {
	states := compileStates(t, "app", `
apt-cache:
  cmd.run:
    - command: apt-get update
    - require_in:
      - cmd.run: install-app
install-app:
  cmd.run:
    - command: apt-get install -y app
`)
	install := states["cmd.run:install-app"]
	if install == nil {
		t.Fatal("install-app not built")
	}
	if !hasRef(install.Reqs().Require, "cmd.run:apt-cache") {
		t.Errorf("install-app.Require = %v, want cmd.run:apt-cache", install.Reqs().Require)
	}
}

func TestWatchInAndOnchangesIn(t *testing.T) {
	states := compileStates(t, "app", `
conf:
  file.managed:
    - name: /etc/app.conf
    - watch_in:
      - cmd.run: reload
    - onchanges_in:
      - cmd.run: notify
reload:
  cmd.run:
    - command: systemctl reload app
notify:
  cmd.run:
    - command: echo changed
`)
	if r := states["cmd.run:reload"].Reqs(); !hasRef(r.Watch, "file.managed:conf") {
		t.Errorf("reload.Watch = %v", r.Watch)
	}
	if r := states["cmd.run:notify"].Reqs(); !hasRef(r.OnChanges, "file.managed:conf") {
		t.Errorf("notify.OnChanges = %v", r.OnChanges)
	}
}

func TestListenAliasesToWatch(t *testing.T) {
	states := compileStates(t, "app", `
conf:
  file.managed:
    - name: /etc/app.conf
    - listen:
      - cmd.run: svc
svc:
  cmd.run:
    - command: true
`)
	// listen on conf -> watch on conf.
	if r := states["file.managed:conf"].Reqs(); !hasRef(r.Watch, "cmd.run:svc") {
		t.Errorf("conf.Watch (from listen) = %v", r.Watch)
	}
}

func TestPrereqInjectsOrderingAndGate(t *testing.T) {
	states := compileStates(t, "app", `
build:
  cmd.run:
    - command: make
    - prereq:
      - cmd.run: deploy
deploy:
  cmd.run:
    - command: ./deploy.sh
`)
	// Ordering: deploy must require build (build runs first).
	if r := states["cmd.run:deploy"].Reqs(); !hasRef(r.Require, "cmd.run:build") {
		t.Errorf("deploy.Require (from prereq) = %v", r.Require)
	}
	// Gate: build is wrapped as a Prereqer targeting deploy.
	pr, ok := states["cmd.run:build"].(state.Prereqer)
	if !ok {
		t.Fatal("build should implement Prereqer")
	}
	if !hasRef(pr.PrereqTargets(), "cmd.run:deploy") {
		t.Errorf("build.PrereqTargets = %v", pr.PrereqTargets())
	}
}

func TestNamesExpansion(t *testing.T) {
	states := compileStates(t, "pkgs", `
install:
  pkg.installed:
    - names:
      - nginx
      - curl
      - git
`)
	for _, name := range []string{"nginx", "curl", "git"} {
		if states["pkg.installed:"+name] == nil {
			t.Errorf("expected expanded state pkg.installed:%s", name)
		}
	}
	if len(states) != 3 {
		t.Errorf("expected 3 expanded states, got %d", len(states))
	}
}

func TestModuleRunDispatches(t *testing.T) {
	states := compileStates(t, "mod", `
do-it:
  module.run:
    - name: cmd.run
    - command: echo hello
`)
	s := states["module.run:do-it"]
	if s == nil {
		t.Fatal("module.run:do-it not built")
	}
	// It should behave (Check/Apply) without error via the inner cmd.run.
	if _, err := s.Check(context.Background()); err != nil {
		t.Errorf("module.run Check: %v", err)
	}
}
