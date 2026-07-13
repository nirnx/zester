package compiler

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/template"
	"gopkg.in/yaml.v3"
)

// Compiler compiles state files with include/extend directives
type Compiler struct {
	config CompilerConfig
}

// StarModLoader loads Starlark modules from a directory's _modules/ subdirectory.
// Implemented by starmod.Loader.
type StarModLoader interface {
	LoadDir(dir string, registry *state.Registry) (int, error)
}

// CompilerConfig holds the configuration for the compiler
type CompilerConfig struct {
	StatesDir  string
	Engine     *template.Engine
	Registry   *state.Registry
	Facts      map[string]any
	Settings   map[string]any
	StarLoader StarModLoader     // Starlark module loader (nil = disabled)
	Guards     state.GuardRunner // onlyif/unless command runner (nil = guards always satisfied)
	Logger     *slog.Logger
}

// CompileResult contains the compiled states and source files
type CompileResult struct {
	States  []state.State
	Sources []string // all .zy files loaded (for logging)
}

// parsedFile holds the parsed directives and states from a single file
type parsedFile struct {
	ref      StateRef
	includes []StateRef
	extends  map[string]map[string][]map[string]any
	states   map[string]map[string][]map[string]any
}

// NewCompiler creates a new state compiler
func NewCompiler(cfg CompilerConfig) *Compiler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Compiler{config: cfg}
}

// Compile compiles a single state reference
func (c *Compiler) Compile(ref StateRef) (*CompileResult, error) {
	return c.CompileMultiple([]StateRef{ref})
}

// CompileMultiple compiles multiple state references and merges them.
// When multiple files define the same state ID, their args are deep-merged
// (same semantics as settings): requisite lists are appended, other keys
// are replaced by the later file. Files are processed in load order
// (depth-first include resolution) for deterministic results.
func (c *Compiler) CompileMultiple(refs []StateRef) (*CompileResult, error) {
	loaded := make(map[StateRef]*parsedFile)
	var loadStack []StateRef
	var sources []string

	// Load all refs and their includes (depth-first)
	for _, ref := range refs {
		if err := c.loadRecursive(ref, loaded, loadStack, &sources); err != nil {
			return nil, err
		}
	}

	// Build merged state map with extends applied.
	// Process files in load order (sources) for deterministic merge behavior.
	mergedStates := make(map[string]map[string][]map[string]any)

	// Build a ref-to-parsed lookup so we can iterate in sources order.
	refByPath := make(map[string]*parsedFile)
	for _, parsed := range loaded {
		path := parsed.ref.ResolveToPath(c.config.StatesDir)
		refByPath[path] = parsed
	}

	// First pass: collect and merge states from included files (in load order)
	for _, srcPath := range sources {
		parsed, ok := refByPath[srcPath]
		if !ok {
			continue
		}
		for stateID, stateData := range parsed.states {
			existing, exists := mergedStates[stateID]
			if !exists {
				mergedStates[stateID] = stateData
				continue
			}

			// Deep-merge: same module → merge args, new module → add it
			for module, newArgs := range stateData {
				if baseArgs, ok := existing[module]; ok {
					existing[module] = mergeArgsList(baseArgs, newArgs)
				} else {
					existing[module] = newArgs
				}
			}
			c.config.Logger.Info("compiler: merged duplicate state ID",
				"state_id", stateID,
				"from_file", parsed.ref)
		}
	}

	// Second pass: apply extends (in load order)
	for _, srcPath := range sources {
		parsed, ok := refByPath[srcPath]
		if !ok {
			continue
		}
		for stateID, extendData := range parsed.extends {
			baseState, exists := mergedStates[stateID]
			if !exists {
				return nil, fmt.Errorf("compiler: extend: state %q not found in included files", stateID)
			}

			// Merge extend data into base state
			for module, extendArgs := range extendData {
				if baseArgs, ok := baseState[module]; ok {
					baseState[module] = mergeArgsList(baseArgs, extendArgs)
				} else {
					baseState[module] = extendArgs
				}
			}
		}
	}

	// Rewrite inverse ("_in") requisites and prereq ordering across the full
	// state set before building (Salt parity). Must run after merge/extend.
	if err := transformRequisites(mergedStates); err != nil {
		return nil, err
	}

	// Discover and load Starlark modules from _modules/ directories
	// adjacent to the loaded state files.
	if c.config.StarLoader != nil {
		dirs := uniqueParentDirs(sources, c.config.StatesDir)
		for _, dir := range dirs {
			if n, err := c.config.StarLoader.LoadDir(dir, c.config.Registry); err != nil {
				c.config.Logger.Warn("compiler: starlark module loading error",
					"dir", dir, "error", err)
			} else if n > 0 {
				c.config.Logger.Info("compiler: loaded Starlark modules",
					"dir", dir, "count", n)
			}
		}
	}

	// Build final state list. Each module under a state ID becomes a
	// separate State in the DAG (names are "module:stateID", so they're unique).
	// A `names:` list expands into one state per name (Salt parity), each using
	// the name as its state ID (so requisites can target it by name).
	var states []state.State
	for stateID, stateData := range mergedStates {
		for module, args := range stateData {
			cfg := flattenArgsList(args)

			if names, ok := expandNames(cfg); ok {
				// SCHEMA-AWARE injection (M1/M2, keystone spec final fix pass): the
				// expanded name goes into the module's PRIMARY parameter's canonical
				// key when it has one (file.managed → name, cmd.run → command), or
				// the historical literal "name" otherwise (module.run, legacy/
				// Starlark, and a no-primary spec-carrying module like test.* — all
				// reserved-tolerant of "name" now per M1's ExtraReserved fix). See
				// namesInjectKey. Injection is UNCONDITIONAL — Salt semantics: the
				// per-instance names entry wins over an explicit value at the inject
				// key (Salt's compiler sets each expanded chunk's name from the
				// names list; legacy zester likewise injected name unconditionally).
				// For cmd.run this rides BD-8's Salt-parity acceptance: names +
				// explicit command now runs each instance's own name, as Salt does.
				injectKey, injectAliases := c.namesInjectKey(module)
				for _, name := range names {
					inst := cloneConfig(cfg)
					delete(inst, namesKey)
					// Salt semantics: names wins — drop any explicit value at the
					// inject key or its aliases, then set the per-instance name.
					delete(inst, injectKey)
					for _, a := range injectAliases {
						delete(inst, a)
					}
					inst[injectKey] = name
					s, err := c.buildOne(module, name, inst)
					if err != nil {
						return nil, fmt.Errorf("compiler: build state %q (%s, name=%s): %w", stateID, module, name, err)
					}
					states = append(states, s)
				}
				continue
			}

			s, err := c.buildOne(module, stateID, cfg)
			if err != nil {
				return nil, fmt.Errorf("compiler: build state %q (%s): %w", stateID, module, err)
			}
			states = append(states, s)
		}
	}

	return &CompileResult{
		States:  states,
		Sources: sources,
	}, nil
}

// buildOne builds a single state and wraps it with the generic Salt-parity
// attributes (onlyif/unless/order/retry/failhard/prereq) parsed from cfg.
func (c *Compiler) buildOne(module, id string, cfg map[string]any) (state.State, error) {
	s, err := c.config.Registry.Build(module, id, cfg)
	if err != nil {
		return nil, err
	}
	attrs := state.ParseStateAttributes(cfg)
	return state.WrapAttributes(s, attrs, c.config.Guards), nil
}

// namesInjectKey decides, for a `names:` expansion, which config key each
// expanded name is written into, and that key's declared aliases (so the
// caller — Compile's expansion loop — can clear any explicit value at the key
// OR an alias before injecting; injection is UNCONDITIONAL, Salt semantics —
// each expanded instance runs its own names entry, see the loop's comment;
// the earlier M2 skip-injection behavior was superseded by BD-8). The
// compiler holds the Registry, so it consults the module's registered schema
// (Describe):
//
//   - spec-carrying module WITH a primary parameter → (primary.Name,
//     primary.Aliases): inject into that canonical key. file.managed's primary
//     is `name` (so this is byte-identical to the historical literal "name"
//     injection); cmd.run's primary is `command` (aliases `name`, `cmd`), so
//     the expansion clears all three before writing each per-instance name
//     into `command`.
//   - every other case — OpenParams passthrough (module.run), no registered
//     schema at all (legacy / Starlark without a PARAMS declaration), AND a
//     spec-carrying module with NO primary (the test.* family) — → ("name",
//     nil): the historical literal-"name" injection, restored in full (M2:
//     "full parity") now that ANY module tolerates an injected "name" it does
//     not itself declare (M1 added "name" to internal/peeld's ExtraReserved
//     alongside the exec-layer "test"). module.run resolves its target from
//     config["name"] and skips unknown-key validation regardless; a no-schema
//     builder reads config["name"] directly; a no-primary spec-carrying module
//     (test.ping, test.nop, …) simply carries the per-instance state ID as
//     before and ignores the extra key, which strict now excuses rather than
//     rejects.
func (c *Compiler) namesInjectKey(module string) (key string, aliases []string) {
	info, ok := c.config.Registry.Describe(module)
	if ok && !info.OpenParams {
		for _, f := range info.Params {
			if f.Primary {
				return f.Name, f.Aliases
			}
		}
	}
	return "name", nil
}

// namesKey is the `names:` expansion directive. It is DERIVED from
// state.CompilerKeys() — the sole compiler directive that is neither an "_in"
// inverse form nor a same-state alias source — so the key string lives only in
// pkg/state/reserved.go. Pinned to "names" by TestCompilerConsumedKeysAreReserved.
var namesKey = deriveNamesKey()

func deriveNamesKey() string {
	for _, k := range state.CompilerKeys() {
		if strings.HasSuffix(k, inSuffix) {
			continue
		}
		if _, isAlias := sameStateAlias[k]; isAlias {
			continue
		}
		return k
	}
	return ""
}

// expandNames returns the `names:` list from a config map, if present.
func expandNames(cfg map[string]any) ([]string, bool) {
	v, ok := cfg[namesKey]
	if !ok {
		return nil, false
	}
	names := state.ParseNameList(v)
	if len(names) == 0 {
		return nil, false
	}
	return names, true
}

// cloneConfig makes a shallow copy of a config map so per-name expansion does
// not mutate the shared map.
func cloneConfig(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	maps.Copy(out, cfg)
	return out
}

// loadRecursive loads a state file and all its includes recursively
func (c *Compiler) loadRecursive(ref StateRef, loaded map[StateRef]*parsedFile, loadStack []StateRef, sources *[]string) error {
	// Check for cycles (must happen before "already loaded" check)
	if slices.Contains(loadStack, ref) {
		chain := append(loadStack, ref)
		return fmt.Errorf("compiler: include cycle detected: %v", chain)
	}

	// Check if already loaded
	if _, ok := loaded[ref]; ok {
		return nil
	}

	// Load and parse file
	parsed, err := c.loadAndParse(ref)
	if err != nil {
		return err
	}

	*sources = append(*sources, ref.ResolveToPath(c.config.StatesDir))
	loaded[ref] = parsed

	// Push onto stack and recurse into includes
	loadStack = append(loadStack, ref)
	for _, includeRef := range parsed.includes {
		if err := c.loadRecursive(includeRef, loaded, loadStack, sources); err != nil {
			return err
		}
	}

	return nil
}

// loadAndParse loads a single state file, renders it, and parses directives
func (c *Compiler) loadAndParse(ref StateRef) (*parsedFile, error) {
	path := ref.ResolveToPath(c.config.StatesDir)

	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compiler: read %s: %w", path, err)
	}

	// Render template
	rendered, err := c.config.Engine.RenderString(path, string(content), template.RenderContext{
		Facts:    c.config.Facts,
		Settings: c.config.Settings,
	})
	if err != nil {
		return nil, fmt.Errorf("compiler: render %s: %w", path, err)
	}

	// Parse YAML
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &doc); err != nil {
		return nil, fmt.Errorf("compiler: parse %s: %w", path, err)
	}

	// Extract directives
	includes, extends, states, err := parseDirectives(doc)
	if err != nil {
		return nil, fmt.Errorf("compiler: parse directives %s: %w", path, err)
	}

	// Convert string includes to StateRefs
	var includeRefs []StateRef
	for _, inc := range includes {
		includeRefs = append(includeRefs, StateRef(inc))
	}

	return &parsedFile{
		ref:      ref,
		includes: includeRefs,
		extends:  extends,
		states:   states,
	}, nil
}

// parseDirectives extracts include/extend/state declarations from parsed YAML
func parseDirectives(doc map[string]any) (
	includes []string,
	extends map[string]map[string][]map[string]any,
	states map[string]map[string][]map[string]any,
	err error,
) {
	extends = make(map[string]map[string][]map[string]any)
	states = make(map[string]map[string][]map[string]any)

	for key, value := range doc {
		switch key {
		case "include":
			// Parse include list
			if includeList, ok := value.([]any); ok {
				for _, item := range includeList {
					if str, ok := item.(string); ok {
						includes = append(includes, str)
					}
				}
			}

		case "extend":
			// Parse extend map
			if extendMap, ok := value.(map[string]any); ok {
				for stateID, stateData := range extendMap {
					parsedState, parseErr := parseStateData(stateData)
					if parseErr != nil {
						err = fmt.Errorf("compiler: parse extend state %q: %w", stateID, parseErr)
						return
					}
					extends[stateID] = parsedState
				}
			}

		default:
			// Regular state declaration
			parsedState, parseErr := parseStateData(value)
			if parseErr != nil {
				err = fmt.Errorf("compiler: parse state %q: %w", key, parseErr)
				return
			}
			states[key] = parsedState
		}
	}

	return
}

// parseStateData parses a single state's data (module -> args list)
func parseStateData(data any) (map[string][]map[string]any, error) {
	stateMap, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compiler: expected map, got %T", data)
	}

	result := make(map[string][]map[string]any)

	for module, argsData := range stateMap {
		// Args should be a list
		argsList, ok := argsData.([]any)
		if !ok {
			return nil, fmt.Errorf("compiler: module %q: expected list, got %T", module, argsData)
		}

		var args []map[string]any
		for i, item := range argsList {
			argMap, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("compiler: module %q: arg %d: expected map, got %T", module, i, item)
			}
			args = append(args, argMap)
		}

		result[module] = args
	}

	return result, nil
}

// uniqueParentDirs extracts unique parent directories from source file paths.
func uniqueParentDirs(sources []string, statesDir string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, src := range sources {
		var dir string
		if filepath.IsAbs(src) {
			dir = filepath.Dir(src)
		} else {
			dir = filepath.Dir(filepath.Join(statesDir, src))
		}
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	if !seen[statesDir] {
		dirs = append(dirs, statesDir)
	}
	return dirs
}
