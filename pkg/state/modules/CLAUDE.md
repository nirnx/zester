# State Module Development Guide

How to build new state modules following Zester's two-layer architecture.

## Architecture: Execution + State

Modules follow a two-layer split:

- **Execution layer** (`pkg/exec/`): Stateless, imperative interfaces for system operations (`PackageExec`, `FileExec`, `CommandExec`, `ServiceExec`, `UserExec`, `GroupExec`). Platform-specific implementations live here.
- **State layer** (`pkg/state/modules/`): Thin idempotent wrappers that implement `Check → Apply → Revert` by delegating to the execution layer.

State modules never call `os.Exec`, `os.WriteFile`, etc. directly. They call the injected exec providers, which enables testing with fakes.

## Module Struct Template

Every module follows this structure:

```go
package modules

import (
    "context"
    "fmt"

    "github.com/nirnx/zester/pkg/exec"
    "github.com/nirnx/zester/pkg/state"
)

// SvcRunning implements the service.running state.
type SvcRunning struct {
    id   string                // state ID from YAML key
    reqs state.Requisites      // parsed requisites (require, watch, onchanges, onfail)

    // Config fields — populated from the YAML config map.
    Service string
    Enable  bool

    // Injected providers — never construct these yourself.
    svc exec.ServiceExec
}
```

Key rules:
- `id` and `reqs` are always present
- Config fields are exported (for test assertions)
- Injected providers are unexported (internal plumbing)

## Builder Pattern

Modules use closure-based builders that capture `ModuleContext`:

```go
func NewSvcRunningBuilder(mctx *exec.ModuleContext) state.Builder {
    return func(id string, config map[string]any) (state.State, error) {
        if mctx.Service == nil {
            return nil, fmt.Errorf("service.running: no service provider available")
        }
        return newSvcRunning(id, config, mctx.Service)
    }
}
```

The inner constructor receives only the specific providers it needs (not the whole `ModuleContext`):

```go
func newSvcRunning(id string, config map[string]any, svc exec.ServiceExec) (state.State, error) {
    s := &SvcRunning{id: id, svc: svc}

    // Primary param defaults to the state ID.
    s.Service, _ = config["name"].(string)
    if s.Service == "" {
        s.Service = id
    }

    s.Enable, _ = config["enable"].(bool)

    // Always parse requisites last.
    s.reqs = state.ParseRequisites(config)

    return s, nil
}
```

**Exception**: Modules that don't need exec providers (like `test.ping`) export a plain `func(id string, config map[string]any) (state.State, error)` that matches `state.Builder` directly.

## Config Parsing Conventions

- **Primary param defaults to ID**: The main identifier (`name`, `command`, `path`) falls back to the state ID if not specified. This lets users write `/etc/nginx/nginx.conf:` as the YAML key and omit the explicit `path:` parameter.
- **Type assertions with comma-ok**: Use `val, _ = config["key"].(type)` — don't error on missing optional fields.
- **Requisites**: Always call `state.ParseRequisites(config)` at the end. This handles `require`, `watch`, `onchanges`, `onfail` — in both string form (`"pkg.installed:nginx"`) and Salt dict shorthand (`{"pkg": "nginx"}`). That's all a module needs: `listen` and the inverse `_in` forms (`require_in`, `watch_in`, `onchanges_in`, `onfail_in`, `listen_in`, `prereq_in`) are rewritten into forward requisites at compile time by `pkg/state/compiler/requisites.go`, so modules never see those keys.
- **String lists**: For `[]any` → `[]string` conversions, iterate and type-assert each element (see `CmdRun.Args` parsing in `cmd.go`).
- **Maps**: For `map[string]any` → `map[string]string`, iterate and use `fmt.Sprintf("%v", v)` (see `CmdRun.Env` parsing).

## Generic Attributes — Never Parse These in a Module

The Salt-parity per-state attributes are module-independent and handled *outside* the module:

- `onlyif` / `unless` (shell guards), `order`, `retry`, `failhard`, `prereq` — parsed by `state.ParseStateAttributes(config)` and applied via `state.WrapAttributes(inner, attrs, guards)`, a decorator around the built state. The compiler wraps every compiled state (`buildOne` in `pkg/state/compiler/compiler.go`), and the peel exec handler wraps ad-hoc single-module runs. The wrapper embeds the inner State, so `Name()` and `Reqs()` pass through unchanged.
- `names` — expanded by the compiler *before* the builder is called: one state per name, with the name substituted as both the state ID and the `name` param.
- The `_in` requisite forms and `listen` — rewritten at compile time (see Requisites bullet above).

Do **not** read `onlyif`, `unless`, `order`, `retry`, `failhard`, `prereq`, `names`, or any `_in` key inside a module builder — modules simply ignore unknown keys and let the wrapper handle them. Guard-not-met is reported by the wrapper as a no-op (`Diff: "skipped: guard condition not met"`), retry only applies in apply/revert modes, and `retry:` interval is in seconds (default 10s).

## State Interface Methods

### Name()

Format: `"module.function:id"` — e.g., `"service.running:nginx"`.

```go
func (s *SvcRunning) Name() string           { return "service.running:" + s.id }
func (s *SvcRunning) Reqs() state.Requisites { return s.reqs }
```

### Check(ctx)

Returns whether the system already matches desired state. Must not modify the **managed** state — but MAY run read-only-in-spirit prep actions needed for an accurate answer (e.g. `pkg.latest` refreshes the package index before probing upgradability: that mutates the manager's metadata cache, never the configuration under management).

**Check and Apply are independent, self-contained full flows — never share state between them.** If both phases need the same prep (like a cache refresh), each runs it itself; idempotent prep repeating is fine, cross-phase "already done" flags are not (the runner's call patterns vary — watch-forced applies bypass Check entirely). Don't add provider-level memos to dedupe repeated idempotent work either.

```go
func (s *SvcRunning) Check(ctx context.Context) (state.CheckResult, error) {
    running, err := s.svc.IsRunning(ctx, s.Service)
    if err != nil {
        return state.CheckResult{}, fmt.Errorf("service.running: check %s: %w", s.Service, err)
    }
    if running {
        return state.CheckResult{NeedsChange: false}, nil
    }
    return state.CheckResult{
        NeedsChange: true,
        Diff:        fmt.Sprintf("%s is not running", s.Service),
    }, nil
}
```

### Apply(ctx)

Makes the system match desired state. Should be idempotent. Always returns `Changed: true` when it performs work.

```go
func (s *SvcRunning) Apply(ctx context.Context) (state.ApplyResult, error) {
    if err := s.svc.Start(ctx, s.Service); err != nil {
        return state.ApplyResult{}, fmt.Errorf("service.running: start %s: %w", s.Service, err)
    }
    return state.ApplyResult{
        Changed: true,
        Diff:    fmt.Sprintf("started %s", s.Service),
        Details: map[string]string{
            "service": s.Service,
            "manager": s.svc.Name(),
        },
    }, nil
}
```

### Revert(ctx)

Undoes Apply. Some modules can't revert (like `cmd.run`) — return `Changed: false` with an explanation.

**Standalone-Revert contract**: in-instance memos (backups, `wasCreated`, saved originals) are valid ONLY for a same-instance Apply→Revert; the runner builds FRESH instances for `ModeRevert`, so the unset-memo path MUST be an explicit clean no-op — `Changed: false`, diff `nothing to revert (no apply recorded in this run)`. It must NEVER be destructive (inferring "file was new" from a missing backup memo deleted pre-existing files — the 2026-07 audit's worst class) and never report a lying diff. Do not re-derive prior state to make standalone revert "work".

```go
func (s *SvcRunning) Revert(ctx context.Context) (state.ApplyResult, error) {
    if err := s.svc.Stop(ctx, s.Service); err != nil {
        return state.ApplyResult{}, fmt.Errorf("service.running: stop %s: %w", s.Service, err)
    }
    return state.ApplyResult{
        Changed: true,
        Diff:    fmt.Sprintf("stopped %s", s.Service),
    }, nil
}
```

## Error Wrapping

Always use the pattern: `fmt.Errorf("module.function: operation: %w", err)`

```go
fmt.Errorf("service.running: start %s: %w", s.Service, err)
fmt.Errorf("pkg.installed: install %s: %w", p.Package, err)
fmt.Errorf("file.managed: write %s: %w", f.Path, err)
```

## Registration

Register the module builder in `cmd/zester-peel/main.go` alongside the existing modules:

```go
registry.Register("service.running", modules.NewSvcRunningBuilder(mctx))
```

## Testing

### Test Setup

Create a helper that builds the `exec.ModuleContext` with the fakes your module needs:

```go
func testSvcMctx(fakeSvc *exectest.FakeServiceExec) *exec.ModuleContext {
    return &exec.ModuleContext{
        Service: fakeSvc,
        Package: exectest.NewFakePackageExec("apt"),
        File:    exectest.NewFakeFileExec(),
        Command: exectest.NewFakeCommandExec(),
    }
}
```

### Available Fakes (pkg/exec/exectest/)

| Fake | Constructor | Key Methods |
|------|-------------|-------------|
| `FakePackageExec` | `NewFakePackageExec(name)` | `PreInstall()`, `IsInstalledSync()`, `RefreshCount()`, `InstallErr` |
| `FakeFileExec` | `NewFakeFileExec()` | `PreCreate()`, `GetFile()` |
| `FakeCommandExec` | `NewFakeCommandExec()` | `SetResult()`, `SetError()`, `Calls()` |

### When to Use Real OS vs Fakes

- **File tests**: Use `exec.OSFileExec{}` with `t.TempDir()` — real filesystem operations are fast and self-cleaning.
- **Package tests**: Always use `exectest.FakePackageExec` — never install real packages in tests.
- **Command tests**: Use `exec.OSCommandExec{}` for simple commands (`echo`, `true`, `false`, `pwd`). Use `exectest.FakeCommandExec` when you need to control output or simulate failures.

### Required Test Cases

Every module should have tests for:

1. **Name()** — verify the `"module.function:id"` format
2. **Primary param default** — verify ID is used when the primary config key is absent
3. **Requisites** — verify `Reqs()` returns correct `Requisites` for all 4 types
4. **Check (needs change)** — system doesn't match desired state
5. **Check (no change)** — system already matches
6. **Apply** — verify `Changed: true` and correct `Details`
7. **Apply error** — verify error propagation from exec provider
8. **Revert** — verify undo behavior
9. **Provider missing** — verify builder returns error when required provider is nil

### Test Pattern Example

```go
func TestSvcRunningRequisites(t *testing.T) {
    mctx := testSvcMctx(exectest.NewFakeServiceExec("systemd"))
    builder := NewSvcRunningBuilder(mctx)
    s, err := builder("test", map[string]any{
        "require":  []any{"pkg.installed:nginx"},
        "watch":     []any{"file.managed:/etc/nginx/nginx.conf"},
        "onchanges": []any{"cmd.run:build"},
        "onfail":    []any{"cmd.run:primary"},
    })
    if err != nil {
        t.Fatal(err)
    }
    reqs := s.Reqs()
    if len(reqs.Require) != 1 || reqs.Require[0] != "pkg.installed:nginx" {
        t.Errorf("Require: got %v", reqs.Require)
    }
    // ... check Watch, OnChanges, OnFail similarly
}
```

## Adding a New Exec Provider

If your module needs a new provider interface (beyond Package/File/Command/Service/User/Group):

1. Define the interface in `pkg/exec/exec.go`
2. Add the OS implementation in `pkg/exec/` (prefix naming: `svc_systemd.go`, not `svc_linux.go`)
3. Add a field to `ModuleContext` and `ProviderSet` in `pkg/exec/context.go`
4. Wire detection in `DetectProviders()`
5. Create a fake in `pkg/exec/exectest/` following the existing pattern

## File Naming

**Do not** use OS-suffix naming (`_linux.go`, `_darwin.go`). Go treats these as implicit build tags. Use prefix naming instead: `pkg_apt.go`, `pkg_dnf.go`, `svc_systemd.go`.

## YAML Usage in State Files

```yaml
# The state ID is the YAML key. For file paths, the ID IS the path:
/etc/nginx/nginx.conf:
  file.managed:
    - content: "worker_processes auto;"
    - mode: "0644"
    - require:
      - "pkg.installed:nginx"
    - watch:
      - "file.managed:/etc/ssl/cert.pem"

# For named states, use the name parameter to specify the actual target:
web-server:
  pkg.installed:
    - name: nginx
    - refresh: true

# Requisites reference other states by "module.function:id":
restart-nginx:
  cmd.run:
    - command: systemctl restart nginx
    - watch:
      - "file.managed:/etc/nginx/nginx.conf"
    - onchanges:
      - "pkg.installed:nginx"
```

## Checklist for a New Module

- [ ] Identify which `exec` providers you need (or add a new one)
- [ ] Create the struct with `id`, `reqs`, config fields, and injected providers
- [ ] Implement the builder: `NewXxxBuilder(mctx) state.Builder`
- [ ] Implement `Name()`, `Reqs()`, `Check()`, `Apply()`, `Revert()`
- [ ] Parse config with primary-param-defaults-to-ID and `ParseRequisites()`
- [ ] Do NOT parse `onlyif`/`unless`/`order`/`retry`/`failhard`/`prereq`/`names` or `_in` keys — the compiler wrapper handles them
- [ ] Wrap errors with `fmt.Errorf("module.function: op: %w", err)`
- [ ] Register in `cmd/zester-peel/main.go`
- [ ] Write tests covering: name, requisites, check, apply, revert, errors, missing provider
- [ ] Add a reference page under `website/content/docs/` (Fumadocs) with a parameter table and register it in the relevant `meta.json` nav
