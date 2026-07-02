// Package template provides a Gonja (Jinja2-compatible) template engine
// for rendering Zester .zy files. It wraps the gonja/v2 library with
// custom filters and functions specific to the Zester settings/state system.
package template

import (
	"fmt"
	"io"
	"strings"

	"github.com/nikolalohinski/gonja/v2/builtins"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
	"github.com/nikolalohinski/gonja/v2/parser"
)

// lookupFactKey traverses a nested map using dot-separated keys.
// For example, "os.family" looks up map["os"].(map[string]any)["family"].
func lookupFactKey(m map[string]any, key string) any {
	parts := strings.Split(key, ".")
	var current any = m
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[part]
		if !ok {
			return nil
		}
	}
	return current
}

// BasketFunc queries basket data from other peels.
// Parameters: target pattern, function name.
// Returns a slice of basket results.
type BasketFunc func(target, function string) []map[string]any

// EngineConfig configures the template engine.
type EngineConfig struct {
	// BasePath is the root directory for resolving template includes.
	// Defaults to /srv/zester.
	BasePath string

	// BasketFn is the function used to resolve basket() calls in templates.
	// If nil, basket() returns an empty list.
	BasketFn BasketFunc

	// ModuleFn dispatches execution-module calls made from templates via the
	// Salt-style salt accessor:
	//
	//	{{ salt['pkg.version']('nginx') }}
	//	{{ salt['grains.get']('os') }}
	//	{{ salt_call('pkg.version', 'nginx') }}   (fallback for dynamic names)
	//
	// If nil, any use of the salt accessor (or salt_call) raises a render
	// error with a clear message stating that module calls are unavailable.
	ModuleFn ModuleFunc
}

func (c *EngineConfig) defaults() {
	if c.BasePath == "" {
		c.BasePath = "/srv/zester"
	}
}

// Engine wraps the gonja template environment with Zester-specific
// filters and functions.
type Engine struct {
	config  EngineConfig
	env     *exec.Environment
	gonjaFg *config.Config
	loader  loaders.Loader
}

// NewEngine creates a template engine with custom Zester filters and functions.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	cfg.defaults()

	loader, err := loaders.NewFileSystemLoader(cfg.BasePath)
	if err != nil {
		return nil, fmt.Errorf("template: create loader for %s: %w", cfg.BasePath, err)
	}

	filters := exec.NewFilterSet(map[string]exec.FilterFunction{})
	filters.Update(builtins.Filters)
	registerFilters(filters)

	basketFn := cfg.BasketFn
	globalCtx := exec.EmptyContext().Update(builtins.GlobalFunctions).Update(builtins.GlobalVariables)
	registerGlobalFunctions(globalCtx, basketFn, cfg.ModuleFn)

	controlStructures := exec.NewControlStructureSet(map[string]parser.ControlStructureParser{})
	controlStructures.Update(builtins.ControlStructures)
	controlStructures.Replace("from", zesterFromParser)
	controlStructures.Register("import_yaml", importYAMLParser)
	controlStructures.Register("do", doParser)

	methods := builtins.Methods
	methods.Dict = zesterDictMethods()

	env := &exec.Environment{
		Context:           globalCtx,
		Filters:           filters,
		Tests:             builtins.Tests,
		ControlStructures: controlStructures,
		Methods:           methods,
	}

	gonjaCfg := config.New()

	return &Engine{
		config:  cfg,
		env:     env,
		gonjaFg: gonjaCfg,
		loader:  loader,
	}, nil
}

// RenderContext holds the data available to templates during rendering.
type RenderContext struct {
	// Facts is the peel's collected system facts.
	Facts map[string]any

	// Settings is the current settings being rendered (for cross-references).
	Settings map[string]any

	// Extra allows passing additional data into the template context.
	Extra map[string]any
}

// RenderString renders a template from a raw string with the given context.
func (e *Engine) RenderString(name, source string, ctx RenderContext) (string, error) {
	tpl, err := exec.NewTemplate(name, e.gonjaFg, loaders.MustNewShiftedLoader(
		name,
		strings.NewReader(source),
		e.loader,
	), e.env)
	if err != nil {
		return "", fmt.Errorf("template: parse %q: %w", name, err)
	}

	data := e.buildContext(ctx, source)
	result, err := tpl.ExecuteToString(data)
	if err != nil {
		return "", fmt.Errorf("template: render %q: %w", name, err)
	}
	return result, nil
}

// RenderFile renders a template from a file path relative to BasePath.
func (e *Engine) RenderFile(path string, ctx RenderContext) (string, error) {
	tpl, err := exec.NewTemplate(path, e.gonjaFg, e.loader, e.env)
	if err != nil {
		return "", fmt.Errorf("template: parse file %q: %w", path, err)
	}

	// Re-read the source so the salt accessor can be seeded from the
	// literal salt[...] subscripts it contains (see module_accessor.go).
	var source string
	if r, rerr := e.loader.Read(path); rerr == nil {
		if b, berr := io.ReadAll(r); berr == nil {
			source = string(b)
		}
	}

	data := e.buildContext(ctx, source)
	result, err := tpl.ExecuteToString(data)
	if err != nil {
		return "", fmt.Errorf("template: render file %q: %w", path, err)
	}
	return result, nil
}

func (e *Engine) buildContext(ctx RenderContext, source string) *exec.Context {
	m := map[string]any{}

	if ctx.Facts != nil {
		m["facts"] = ctx.Facts
	} else {
		m["facts"] = map[string]any{}
	}

	if ctx.Settings != nil {
		m["settings"] = ctx.Settings
	} else {
		m["settings"] = map[string]any{}
	}

	facts := m["facts"].(map[string]any)
	stgs := m["settings"].(map[string]any)

	m["facts_get"] = func(key string, args ...any) any {
		val := lookupFactKey(facts, key)
		if val != nil {
			return val
		}
		if len(args) > 0 {
			return args[0]
		}
		return nil
	}

	// Salt compatibility aliases
	m["grains"] = m["facts"]
	m["pillar"] = m["settings"]

	// Salt compatibility functions (need per-render facts/settings)
	registerSaltFunctions(m, facts, stgs)

	// Salt-style module accessor: salt['pkg.version']('nginx').
	// Seeded from the literal salt[...] subscripts in the template source.
	m["salt"] = buildSaltAccessor(source, e.config.ModuleFn)

	for k, v := range ctx.Extra {
		m[k] = v
	}

	return exec.NewContext(m)
}
