package peeld

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nirnx/zester/pkg/facts/collectors"
	"github.com/nirnx/zester/pkg/proto"
	"github.com/nirnx/zester/pkg/starmod"
	"github.com/nirnx/zester/pkg/state"
	"github.com/nirnx/zester/pkg/state/compiler"
	"github.com/nirnx/zester/pkg/template"
	"gopkg.in/yaml.v3"
)

const (
	// bakedStatesDirDefault is the baked-in states fallback used when the KV
	// cache dir is empty (overridable per Agent for tests).
	bakedStatesDirDefault = "/data/states"

	// statesDirRetryAttempts/statesDirRetryDelay bound the ReadDir retries in
	// resolveStatesDir: the state-file cache's atomic dir swap (two
	// sequential os.Rename calls in statefiles.Cache) has a brief window
	// where the cache dir does not exist. A single ReadDir landing in that
	// window must not silently switch the whole run to the baked fallback
	// (C2), so an ENOENT/empty result is retried before concluding the cache
	// is genuinely empty.
	statesDirRetryAttempts = 5
	statesDirRetryDelay    = 25 * time.Millisecond

	// statesEngineUnavailableMsg is the ExecResponse error for state
	// compilation on a peel that has no states directory at all yet (KV-only
	// deployment before the first state-file sync, C4).
	statesEngineUnavailableMsg = "states engine unavailable: no states directory exists yet (state files not yet synced from KV and no baked states dir); retry after state files are published"
)

// resolveStatesDir returns the KV cache dir if populated, otherwise the
// baked-in states fallback. Re-evaluated before every execution (see
// execModule) so a peel that boots before state files reach KV switches to
// the cache once it fills. An empty/missing cache dir is re-checked a few
// times before falling back, so the cache's atomic dir swap is never observed
// as "no cache" (C2); after the retries, falling back is a real event and is
// logged at Warn.
func (a *Agent) resolveStatesDir() string {
	dir, fellBack := resolveStatesDirWith(a.cfg.StatesCache, a.bakedStatesDir,
		statesDirRetryAttempts, statesDirRetryDelay, os.ReadDir, time.Sleep)
	if fellBack {
		a.logger.Warn("state-file cache dir empty or missing after retries, falling back to baked states dir",
			"cache_dir", a.cfg.StatesCache, "baked_dir", a.bakedStatesDir)
	}
	return dir
}

// resolveStatesDirWith is the injectable core of resolveStatesDir: it returns
// cacheDir as soon as one readDir attempt shows content, sleeping delay
// between attempts, and returns (bakedDir, true) only after all attempts saw
// an error or an empty directory.
func resolveStatesDirWith(cacheDir, bakedDir string, attempts int, delay time.Duration,
	readDir func(string) ([]os.DirEntry, error), sleep func(time.Duration)) (string, bool) {
	for i := 0; i < attempts; i++ {
		if i > 0 {
			sleep(delay)
		}
		if entries, err := readDir(cacheDir); err == nil && len(entries) > 0 {
			return cacheDir, false
		}
	}
	return bakedDir, true
}

// setupStatesEngine performs the boot-time states-engine construction. A
// missing/unbuildable states directory — a KV-only deployment with no baked
// states dir, before the first connected-phase cache sync — is NOT fatal
// (C4, offline-first): statesEng stays nil and effectiveStatesDir stays "",
// so execModule re-attempts the build before every execution and returns
// statesEngineUnavailableMsg for state compilation until it succeeds.
func (a *Agent) setupStatesEngine() {
	dir := a.resolveStatesDir()
	if dir != a.cfg.StatesCache {
		a.logger.Info("no cached state files, using local states dir", "dir", dir)
	}
	eng, err := a.newStatesEngine(dir)
	if err != nil {
		a.logger.Warn("states engine unavailable at boot; will build when state files appear",
			"dir", dir, "error", err)
		return
	}
	a.statesEng = eng
	a.effectiveStatesDir = dir
}

// newStatesEngine creates a states-specific template engine rooted at the
// effective states directory. The settings engine is rooted at /data for
// settings templates. State files use {% from %} imports that must resolve
// relative to the states directory, not /data.
func (a *Agent) newStatesEngine(dir string) (*template.Engine, error) {
	return template.NewEngine(template.EngineConfig{
		BasePath: dir,
		ModuleFn: a.moduleDispatch,
		BasketFn: makeBasketFunc(a.client.JetStream(), a.ps, a.logger, a.basketScope),
	})
}

// renderStateTemplate is wired into mctx.RenderTemplate to enable template
// rendering for state modules (file.managed source templates). It reads
// statesEng, which execModule swaps under execMu; render calls happen inside
// module execution, i.e. under the same mutex.
func (a *Agent) renderStateTemplate(name, source string, extra map[string]any) (string, error) {
	if a.statesEng == nil {
		return "", fmt.Errorf("peeld: states template engine unavailable (no states directory exists yet)")
	}
	return a.statesEng.RenderString(name, source, template.RenderContext{
		Facts:    a.mctx.Facts,
		Settings: a.mctx.Settings,
		Extra:    extra,
	})
}

// resolveExecSettings resolves settings for a state execution, failing
// closed on nil settings (finding 29): on Resolve error it falls back to
// the last-known-good cachedSettings (guarded by basketScopeMu, same as
// the settings.* query path) with a Warn; when the peel has never
// resolved settings, it returns an error so the execution fails instead
// of rendering templates against nil settings. A nil resolver (peel-side
// rendering disabled at startup) keeps the pre-existing nil-settings
// behavior — that is a permanent configuration state, not a transient
// failure. Successful resolves also refresh the on-disk last-known-good
// snapshot (hash-gated, so unchanged settings never touch disk).
func (a *Agent) resolveExecSettings(execCtx context.Context, module string, currentFacts map[string]any) (map[string]any, error) {
	if a.resolver == nil {
		return nil, nil
	}
	fresh, resolveErr := a.resolver.Resolve(execCtx, currentFacts)
	a.basketScopeMu.RLock()
	cached := a.cachedSettings
	a.basketScopeMu.RUnlock()
	cs, stale, err := settingsForExec(fresh, resolveErr, cached)
	if err != nil {
		return nil, err
	}
	if stale {
		a.logger.Warn("peel-side settings resolution failed, using last-known-good cached settings",
			"module", module, "error", resolveErr)
	} else {
		a.persistSettingsSnapshot(cs)
	}
	return cs, nil
}

// readOnlyModule reports whether module belongs to the fixed read-only set
// that executes outside execMu (finding 32): facts.* EXCEPT facts.set,
// settings.*/pillar.*, test.ping, sys.list_functions, and grains.*. The
// imperative-registry names (grains.*, sys.list_functions) additionally
// mirror execModule's dispatch-order check, so unknown names and
// state-module-shadowed names keep flowing through the serialized path and
// produce the same errors as before.
func (a *Agent) readOnlyModule(module string) bool {
	switch {
	case module == "test.ping":
		return true
	case module == "facts.set":
		// facts.set mutates the custom-facts file via a non-atomic
		// read-modify-write; it must stay on the execMu-serialized worker
		// path or concurrent sets would clobber each other's keys (C7).
		return false
	case strings.HasPrefix(module, "facts."),
		strings.HasPrefix(module, "settings."),
		strings.HasPrefix(module, "pillar."):
		return true
	case module == "sys.list_functions" || strings.HasPrefix(module, "grains."):
		return a.execReg.Has(module) && !a.registry.Has(module)
	}
	return false
}

// execReadOnly executes one of the read-only modules WITHOUT taking execMu:
// these never mutate the shared ModuleContext or the managed system, so a
// long-running state apply no longer blocks liveness probes (test.ping) and
// data queries. Imperative-registry calls run on an immutable per-request
// context derived from mctxTemplate with fresh facts and the cached settings
// snapshot.
func (a *Agent) execReadOnly(execCtx context.Context, req proto.ExecRequest) (proto.ExecResponse, error) {
	id, module, args := req.ID, req.Module, req.Args
	if args == nil {
		args = map[string]any{}
	}

	switch {
	case strings.HasPrefix(module, "facts."):
		return a.execFactsModule(module, args), nil

	case strings.HasPrefix(module, "settings."), strings.HasPrefix(module, "pillar."):
		return a.execSettingsModule(module, args), nil

	case module == "test.ping":
		// Built from the thread-safe registry; the test.ping state touches
		// no providers, so running it concurrently with the worker is safe.
		s, err := a.registry.Build(module, id, args)
		if err != nil {
			return proto.ExecResponse{PeelID: a.peelID, Error: "build state: " + err.Error()}, nil
		}
		s = state.WrapAttributes(s, state.ParseStateAttributes(args), a.guardRunner)
		return a.runExecStates(execCtx, []state.State{s}, args)

	default:
		// grains.*, sys.list_functions — imperative registry functions on a
		// derived immutable context (fresh facts + cached settings), never
		// the shared mutable mctx.
		a.basketScopeMu.RLock()
		cached := a.cachedSettings
		a.basketScopeMu.RUnlock()
		derived := a.mctxTemplate.WithFactsSettings(a.mgr.GetFacts(), cached)

		if id != "" {
			if _, ok := args["name"]; !ok {
				args["name"] = id
			}
		}
		out, err := a.execReg.Call(execCtx, module, derived, args)
		if err != nil {
			return proto.ExecResponse{PeelID: a.peelID, Error: module + ": " + err.Error()}, nil
		}
		return proto.ExecResponse{
			PeelID:  a.peelID,
			Success: true,
			Results: []proto.StateResult{{
				Name:    module,
				Changed: false,
				Details: map[string]string{"result": out},
			}},
		}, nil
	}
}

// execModule is the core execution function shared between the exec worker
// and the scheduler. It handles all module types: state.apply, state.highstate,
// facts.*, settings.*, event.send, and single-module execution. req.ID is the
// request's state ID (used as the primary-param default for single-module
// runs); req.ReactorDepth threads the reactor chain depth into event.send.
func (a *Agent) execModule(execCtx context.Context, req proto.ExecRequest) (proto.ExecResponse, error) {
	a.execMu.Lock()
	defer a.execMu.Unlock()

	// Mark the exec worker busy for the duration of this mutating execution.
	// The beacon manager's BusyFn reads it lock-free and skips polls while
	// set (disable_during_state_run, reactor amendment 22), so a
	// reaction-triggered state run cannot re-trip the beacon that caused it.
	a.execBusy.Store(true)
	defer a.execBusy.Store(false)

	id, module, args := req.ID, req.Module, req.Args

	peelID := a.peelID
	logger := a.logger

	// Requests from older clients or the REST API may omit args
	// entirely; downstream code writes into the map (e.g. the
	// positional-name default), so a nil map would panic.
	if args == nil {
		args = map[string]any{}
	}

	// Switch to the KV cache dir once it has content (peel may have booted
	// before state files reached KV). This is also the lazy-build path for a
	// peel whose boot-time engine construction failed (statesEng nil,
	// effectiveStatesDir "" — see setupStatesEngine, C4): the dirs always
	// differ, so the build is re-attempted before every execution.
	if d := a.resolveStatesDir(); d != a.effectiveStatesDir {
		if eng2, engErr := a.newStatesEngine(d); engErr != nil {
			if a.statesEng == nil {
				logger.Warn("states engine build failed, no engine available yet",
					"dir", d, "error", engErr)
			} else {
				logger.Warn("states engine rebuild failed, keeping previous dir",
					"dir", d, "error", engErr)
			}
		} else {
			a.statesEng = eng2
			a.effectiveStatesDir = d
			a.starLoader = starmod.NewLoader(starmod.LoaderConfig{
				StatesDir:     d,
				ModuleContext: a.mctx,
				Logger:        logger,
			})
			if _, lErr := a.starLoader.LoadGlobal(a.registry); lErr != nil {
				logger.Warn("starlark global module reload had errors", "error", lErr)
			}
			logger.Info("states directory switched", "dir", d)
		}
	}

	// Refresh execution context with latest facts.
	a.mctx.Facts = a.mgr.GetFacts()

	// A peel that has no states directory at all yet (KV-only deployment
	// before the first state-file sync) has no engine; state compilation
	// cannot proceed. Return a clear error instead of crashing — the lazy
	// (re)build above succeeds once state files appear (C4). Non-compiling
	// paths (facts./settings./execmod/registry-built single modules) do not
	// require the engine.
	if a.statesEng == nil && (module == "state.apply" || module == "state.highstate") {
		return proto.ExecResponse{PeelID: peelID, Error: statesEngineUnavailableMsg}, nil
	}

	var states []state.State

	if module == "state.apply" {
		stateName, _ := args["state"].(string)
		if stateName == "" {
			return proto.ExecResponse{PeelID: peelID, Error: "state.apply requires 'state' arg"}, nil
		}

		currentFacts := a.mgr.GetFacts()
		cs, csErr := a.resolveExecSettings(execCtx, module, currentFacts)
		if csErr != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "settings resolution failed: " + csErr.Error()}, nil
		}
		a.mctx.Settings = cs

		comp := compiler.NewCompiler(compiler.CompilerConfig{
			StatesDir:  a.effectiveStatesDir,
			Engine:     a.statesEng,
			Registry:   a.registry,
			Facts:      currentFacts,
			Settings:   cs,
			StarLoader: a.starLoader,
			Guards:     a.guardRunner,
			Logger:     logger,
		})
		result, err := comp.Compile(compiler.StateRef(stateName))
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "compile state: " + err.Error()}, nil
		}
		states = result.States
		logger.Info("state compiled", "state", stateName, "sources", result.Sources, "state_count", len(result.States))

	} else if module == "state.highstate" {
		currentFacts := a.mgr.GetFacts()
		cs, csErr := a.resolveExecSettings(execCtx, module, currentFacts)
		if csErr != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "settings resolution failed: " + csErr.Error()}, nil
		}
		a.mctx.Settings = cs

		comp := compiler.NewCompiler(compiler.CompilerConfig{
			StatesDir:  a.effectiveStatesDir,
			Engine:     a.statesEng,
			Registry:   a.registry,
			Facts:      currentFacts,
			Settings:   cs,
			StarLoader: a.starLoader,
			Guards:     a.guardRunner,
			Logger:     logger,
		})
		result, err := comp.Highstate(peelID)
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "highstate: " + err.Error()}, nil
		}
		if len(result.States) == 0 {
			return proto.ExecResponse{PeelID: peelID, Error: "highstate: no states matched in top.zy"}, nil
		}
		states = result.States
		logger.Info("highstate compiled", "peel", peelID, "sources", result.Sources, "state_count", len(result.States))

	} else if strings.HasPrefix(module, "facts.") {
		return a.execFactsModule(module, args), nil

	} else if strings.HasPrefix(module, "settings.") || strings.HasPrefix(module, "pillar.") {
		return a.execSettingsModule(module, args), nil

	} else if module == "event.send" {
		// Custom peel event (reactor amendment 20): needs the bus, so it is
		// special-cased here — like the facts./settings. query modules and
		// BEFORE the pkg/execmod fallback (execmod functions have no bus
		// access). Deliberately on the mutating (execMu-serialized) worker
		// path: publishing an event is a side effect, and serialization
		// keeps per-peel event order deterministic.
		return a.execEventSend(id, args, req.ReactorDepth), nil

	} else if a.execReg.Has(module) && !a.registry.Has(module) {
		// Imperative remote-execution function (Salt-style ad-hoc ops:
		// pkg.version, service.restart, disk.usage, ...). Not a state —
		// no Check/Apply lifecycle, just run and return output. A bare
		// positional argument arrives as the request ID; expose it as the
		// conventional "name" arg (e.g. `zester '*' pkg.version nginx`).
		if id != "" {
			if _, ok := args["name"]; !ok {
				args["name"] = id
			}
		}
		out, err := a.execReg.Call(execCtx, module, a.mctx, args)
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: module + ": " + err.Error()}, nil
		}
		return proto.ExecResponse{
			PeelID:  peelID,
			Success: true,
			Results: []proto.StateResult{{
				Name:    module,
				Changed: false,
				Details: map[string]string{"result": out},
			}},
		}, nil
	} else {
		s, err := a.registry.Build(module, id, args)
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "build state: " + err.Error()}, nil
		}
		// Honor generic attributes (onlyif/unless/order/retry/...) on
		// ad-hoc single-module runs too.
		s = state.WrapAttributes(s, state.ParseStateAttributes(args), a.guardRunner)
		states = append(states, s)
	}

	if len(states) == 0 {
		return proto.ExecResponse{PeelID: peelID, Error: "no states built from request"}, nil
	}

	return a.runExecStates(execCtx, states, args)
}

// runExecStates runs built states through the runner and assembles the
// ExecResponse — the shared tail of execModule and the read-only test.ping
// path. The Runner is stateless, so concurrent use is safe.
func (a *Agent) runExecStates(execCtx context.Context, states []state.State, args map[string]any) (proto.ExecResponse, error) {
	// test=True (Salt parity): report what would change without applying.
	mode := state.ModeApply
	testMode := isTestArg(args["test"])
	if testMode {
		mode = state.ModeTest
	}

	result, err := a.runner.Run(execCtx, states, mode)
	if err != nil {
		return proto.ExecResponse{PeelID: a.peelID, Error: "run failed: " + err.Error()}, nil
	}
	success := result.Success()

	resp := proto.ExecResponse{
		PeelID:  a.peelID,
		Success: success,
		Test:    testMode,
	}
	if result.Canceled {
		resp.Error = "execution canceled"
	}
	for name, sr := range result.States {
		resp.Results = append(resp.Results, proto.StateResult{
			Name:       name,
			Changed:    sr.Changed,
			Diff:       sr.Diff,
			Duration:   sr.Duration,
			Details:    sr.Details,
			Error:      sr.Error,
			Skipped:    sr.Skipped,
			SkipReason: sr.SkipReason,
		})
	}

	return resp, nil
}

// execFactsModule handles the facts.* query functions. It only touches the
// facts manager (internally locked) and the custom-facts file, never the
// shared ModuleContext, so it is used by both the serialized worker path and
// the read-only fast path — except facts.set, which readOnlyModule routes to
// the worker only: its custom-facts-file read-modify-write is not atomic and
// relies on execMu for serialization (C7).
func (a *Agent) execFactsModule(module string, args map[string]any) proto.ExecResponse {
	peelID := a.peelID
	var resultValue string

	switch module {
	case "facts.set":
		key, _ := args["key"].(string)
		rawValue, _ := args["value"].(string)
		if key == "" {
			return proto.ExecResponse{PeelID: peelID, Error: "facts.set requires key and value arguments"}
		}
		var value any
		if err := yaml.Unmarshal([]byte(rawValue), &value); err != nil {
			value = rawValue
		}
		factsPath := collectors.DefaultCustomFactsPath
		existing := make(map[string]any)
		if data, err := os.ReadFile(factsPath); err == nil && len(data) > 0 {
			yaml.Unmarshal(data, &existing) //nolint:errcheck
		}
		if existing == nil {
			existing = make(map[string]any)
		}
		existing[key] = value
		out, err := yaml.Marshal(existing)
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "marshal facts file: " + err.Error()}
		}
		if err := os.MkdirAll(filepath.Dir(factsPath), 0755); err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "create facts dir: " + err.Error()}
		}
		if err := os.WriteFile(factsPath, out, 0644); err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "write facts file: " + err.Error()}
		}
		a.mgr.SetFact(key, value)
		resultValue = fmt.Sprintf("%v", value)

	case "facts.items":
		currentFacts := a.mgr.GetFacts()
		data, err := yaml.Marshal(currentFacts)
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "marshal facts: " + err.Error()}
		}
		resultValue = strings.TrimSpace(string(data))

	case "facts.get":
		currentFacts := a.mgr.GetFacts()
		key, _ := args["key"].(string)
		if key == "" {
			return proto.ExecResponse{PeelID: peelID, Error: "facts.get requires a key argument"}
		}
		val := lookupNestedKey(currentFacts, key)
		if val == nil {
			if def, ok := args["default"]; ok {
				resultValue = fmt.Sprintf("%v", def)
			}
		} else {
			switch v := val.(type) {
			case string:
				resultValue = v
			default:
				data, err := yaml.Marshal(val)
				if err != nil {
					resultValue = fmt.Sprintf("%v", val)
				} else {
					resultValue = strings.TrimSpace(string(data))
				}
			}
		}

	case "facts.keys":
		currentFacts := a.mgr.GetFacts()
		keys := make([]string, 0, len(currentFacts))
		for k := range currentFacts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		resultValue = strings.Join(keys, "\n")

	default:
		return proto.ExecResponse{PeelID: peelID, Error: "unknown facts function: " + module}
	}

	return proto.ExecResponse{
		PeelID:  peelID,
		Success: true,
		Results: []proto.StateResult{{
			Name:    module,
			Changed: false,
			Details: map[string]string{"result": resultValue},
		}},
	}
}

// execSettingsModule handles the settings.* query functions (and their
// Salt-compat pillar.* aliases) against the cached settings snapshot,
// reading under basketScopeMu — safe from both the worker and the read-only
// fast path.
func (a *Agent) execSettingsModule(module string, args map[string]any) proto.ExecResponse {
	peelID := a.peelID

	a.basketScopeMu.RLock()
	compiled := a.cachedSettings
	a.basketScopeMu.RUnlock()

	if compiled == nil {
		return proto.ExecResponse{PeelID: peelID, Error: "settings not yet resolved on this peel"}
	}

	// pillar.* is a Salt-compat alias for settings.* (matching the template
	// layer, where pillar maps to settings).
	fn := strings.TrimPrefix(strings.TrimPrefix(module, "settings."), "pillar.")

	var resultValue string

	switch fn {
	case "items":
		data, err := yaml.Marshal(compiled)
		if err != nil {
			return proto.ExecResponse{PeelID: peelID, Error: "marshal settings: " + err.Error()}
		}
		resultValue = strings.TrimSpace(string(data))

	case "get":
		key, _ := args["key"].(string)
		if key == "" {
			return proto.ExecResponse{PeelID: peelID, Error: "settings.get requires a key argument"}
		}
		val := lookupNestedKey(compiled, key)
		if val == nil {
			if def, ok := args["default"]; ok {
				resultValue = fmt.Sprintf("%v", def)
			}
		} else {
			switch v := val.(type) {
			case string:
				resultValue = v
			default:
				data, err := yaml.Marshal(val)
				if err != nil {
					resultValue = fmt.Sprintf("%v", val)
				} else {
					resultValue = strings.TrimSpace(string(data))
				}
			}
		}

	case "keys":
		keys := make([]string, 0, len(compiled))
		for k := range compiled {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		resultValue = strings.Join(keys, "\n")

	default:
		return proto.ExecResponse{PeelID: peelID, Error: "unknown settings function: " + module}
	}

	return proto.ExecResponse{
		PeelID:  peelID,
		Success: true,
		Results: []proto.StateResult{{
			Name:    module,
			Changed: false,
			Details: map[string]string{"result": resultValue},
		}},
	}
}

// settingsForExec decides which settings map a state execution should use.
// fresh/resolveErr are the outcome of a just-attempted resolver.Resolve;
// cached is the last successfully resolved settings map (nil when the peel
// has never resolved settings). On resolve failure it falls back to cached
// (stale=true); with no cache available it returns an error — callers must
// fail the execution rather than proceed with nil settings, which would
// silently render templates with empty values (finding 29).
func settingsForExec(fresh map[string]any, resolveErr error, cached map[string]any) (map[string]any, bool, error) {
	if resolveErr == nil {
		return fresh, false, nil
	}
	if cached != nil {
		return cached, true, nil
	}
	return nil, false, fmt.Errorf("resolve settings: %w (no cached settings available on this peel)", resolveErr)
}

// isTestArg reports whether a "test" argument requests a dry run. Accepts
// bool true or common truthy strings (test=True Salt-style).
func isTestArg(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1", "on":
			return true
		}
	}
	return false
}
