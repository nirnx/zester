# State Module Development Guide

How to build new state modules following Zester's **schema-first**, two-layer
architecture. Every built-in state module is migrated to a single compiled
schema (`modschema.Spec`); the doc-coverage gate is CLOSED, so a Spec-less
registration fails the build (`TestDocCoverage_EveryModuleHasSpec`). A new module
is added the same way — proto + Spec + builder + contract fixtures + generated
page — never the legacy hand-rolled parser.

> Keystone spec: `design/self-documenting-modules-keystone-spec.md`. Read it for
> the framework contract; this guide is the module-author's how-to.

## Architecture: Execution + State

Modules follow a two-layer split:

- **Execution layer** (`pkg/exec/`): stateless, imperative interfaces for system
  operations (`PackageExec`, `FileExec`, `CommandExec`, `ServiceExec`,
  `UserExec`, `GroupExec`, `CronExec`, `MountExec`, `SysctlExec`).
  Platform-specific implementations live here.
- **State layer** (`pkg/state/modules/`): thin idempotent wrappers that implement
  `Check → Apply → Revert` by delegating to the execution layer.

State modules never call `os.Exec`, `os.WriteFile`, etc. directly. They call the
injected exec providers, which enables testing with fakes.

## Package layout (family-oriented, keystone spec §13)

One directory per module FAMILY (family = the module name's first dotted
segment); the directory name is the family verbatim, the Go package name is the
family + `mod` (`file/` → `filemod`, `ssh_auth/` → `sshauthmod`, …):

- **`<family>/`** — the family's member sources (`file_managed.go`), its
  parameter components (`components.go`, the former `family_<fam>.go`), member
  + behavior tests, contract fixtures (`<family>/testdata/contract/<module>.yaml`),
  and `register.go` exporting the family's registration rows as
  `var Rows = []regdef.Registration{...}`.
- **`regdef/`** — the registration row shape shared by every family:
  `Registration`, `BuildFunc`/`PlainBuildFunc`, and `MustSpec`.
- **`internal/famshared/`** — helpers used by MORE THAN ONE family package
  (`ContainsString`, `ReadManagedFile`/`RevertHostsFile`/`SplitHostLines`/
  `JoinHostLines`, `DetectPkgSystem`, …). A helper used by a single family
  stays unexported inside that family.
- **`pkg/state/modules` (the aggregator)** — concatenates the family `Rows`
  into the full table (historical family order, `module.run` LAST), owns
  `RegisterAll`, the dispatch-surface docs (`dispatch_docs.go`), and the
  doc-coverage/registration conformance tests. Its public API is unchanged by
  the split; the peel and docgen import only the aggregator.

## Schema-first: the proto IS the module

A migrated module's struct is BOTH the runtime state and its own **schema proto**.
The tagged, exported fields ARE the parameter declaration (one schema declaration
per module); the compiler reads them once. Untagged fields (the `id`, `reqs`,
injected providers, derived runtime facets, revert memos) are skipped by the
compiler.

```go
// pkg_removed.go — the minimal pilot (zero semantic types, zero behavior change).
type PkgRemoved struct {
    id   string
    reqs state.Requisites

    // Tagged exported field == the module's ONE parameter declaration.
    Package string `zester:"name,primary" usage:"package to remove (defaults to the state ID)"`

    pkg exec.PackageExec // injected provider (untagged)
}
```

### Tag grammar (`pkg/modschema`)

```go
`zester:"<name>[,opt]..." usage:"<help text, commas are fine>"`
```

Options (parsed ONCE, at `Compile` time):

- `primary` — falls back to the state ID when every source is absent/empty; at
  most one per module.
- `aliases=a|b` — pipe-separated alternate source keys (e.g.
  `zester:"name,primary,aliases=path"` on file modules for Salt compatibility;
  the canonical name wins if both are set).
- `required` — a missing value is a typed `FieldError{MissingRequired}`.
- `default=LIT` — **eager**: decoded through the field's own decoder at compile
  time and assigned when the key is absent (an invalid literal fails
  compilation, not runtime). Verified examples: cron fields `default=*`, user
  `default=root`, `sysctl.present`/`mount.mounted` persist `default=true`.
- `lazy` — the default is documented but never materialized; the field stays
  zero and the **module** applies it at use time (see file.managed `mode`,
  `lazy,default=0644`). Cannot combine with `required`.
- `sensitive` — the value is redacted across the ENTIRE decode-error chain,
  warnings, JSON Schema (`writeOnly`), and every rendered surface; never appears
  in defaults or generated examples (e.g. `user.present.password`).
- `zester:"-"` skips a field; untagged structs recurse; untagged non-structs are
  skipped.

The framework resolves **primary-defaults-to-ID** and the legacy
`if x == "" { x = next }` empty-string fall-through itself (spec §2.1). A module
must NOT write `if s.Foo == "" { s.Foo = id }` — that logic moved into the plan.

## Semantic types — `pkg/modschema/paramtypes` (sealed; never inline)

Polymorphic parameters use a NAMED semantic type as their Go field type. The
vocabulary is a sealed set of six (`VocabularySize`; a conformance test pins it);
you cannot define a semantic type inline — an unregistered non-primitive field
type is a compile error.

| Type | Use for | Key accessors |
|------|---------|---------------|
| `FileMode` | octal/string modes (`"0755"`, `0644`, setuid/sticky) | `Declared()`, `Mode()`, `Resolve(def)`, `Octal()`, `Equal(m)` |
| `TriState` | three-valued bool (unset ≠ false) | `Declared()`, `Value()`, `ValueOr(def)` |
| `GroupRef` | gid as numeric GID OR group name | `Declared()`, `IsGID()`, `GID()`, `Name()` |
| `TemplateFlag` | `template:` (bool, `"jinja"`, or truthy string) | `Declared()`, `Enabled()` |
| `StringList` (`[]string`) | `string \| []string \| []any` (scalars sprint'd) | it IS a `[]string` |
| `StringMap` (`map[string]string`) | `map` (scalar values sprint'd; composite values error) | it IS a `map[string]string` |

Pick the type that matches the parameter's accepted representations, then read
its `Declared()` bit in Check/Apply so an UNSET parameter is distinct from a
zero/false one (this is what replaced the old `Enable bool + hasEnable bool`
pairs — see `service.running`/`service.dead` on `TriState`). Absent fields never
reach a semantic `Decode`; the zero value IS undeclared, and an empty-string
source decodes to the undeclared zero value (legacy comma-ok parity).

Adding a NEW semantic type is a `paramtypes` change, not a module change: implement
the sealed `SemanticType` interface, `register(...)` it in `vocabulary.go`, bump
`VocabularySize`, and add its fixtures in `paramtypes/fixtures_test.go`
(completeness is test-enforced). Do this only when a real parameter needs it.

## Authoring the Spec (`regdef.MustSpec` + `Doc`)

`regdef.MustSpec(module, kind, proto, doc)` compiles the schema once at package
init and panics on a bad declaration (a programming error). It lives in
`pkg/state/modules/regdef`. Declare the spec next to the module:

```go
var pkgRemovedSpec = regdef.MustSpec("pkg.removed", modschema.KindState, PkgRemoved{}, modschema.Doc{
    Summary:     "Ensure a system package is not installed.",
    Description: "`pkg.removed` ensures the named package is absent ...", // CommonMark; no JSX
    Effects: modschema.Effects{
        Check:  "Queries the provider whether the package is installed ...",
        Apply:  "Removes the package through the detected manager ...",
        Revert: "Cannot restore the package: the removed version is not recorded ...",
    },
    Examples: []modschema.Example{
        {Title: "Remove a package by name", Kind: "state", Explanation: "...", Code: "remove-telnet:\n  pkg.removed:\n    - name: telnet\n"},
        {Title: "Remove a package ad hoc",  Kind: "cli",   Explanation: "...", Code: "zester '*' pkg.removed nginx"},
    },
    Notes:       []modschema.Note{{Level: "info", Title: "Revert does not reinstall", Body: "..."}},
    Divergences: []string{"BD-6"}, // stable BD-IDs for flagged behavioral differences
    SeeAlso:     []string{"pkg.installed"},
})
```

**Effects are required by Kind** (`TestDocCoverage_EffectsByKind`,
`modschema.ValidateEffects`): a `KindState` module MUST fill `Check` + `Apply`
(`Revert` optional — an explicit "cannot revert" is encouraged); `KindExec` /
`KindDispatch` fill `Execution`. Prose is CommonMark only (no `<Tabs>`/
`<Callout>`), and it MUST be drift-corrected against the live Check/Apply/Revert
code — the generated page is derived from it, so a lie here becomes a published
lie. `Divergences` carries the BD-IDs a module activates (see the BD pinning
standard below); `SeeAlso` targets must resolve or docgen fails.

The one `Doc` currency (Summary/Description/Effects/Examples/Notes/Divergences/
SeeAlso) feeds `sys.doc`, `zester doc`, the generated MDX page, and the embedded
`docdata.json` through a single `modschema.RenderText` — author it once.

## Builder pattern (decode first, assign after)

Builders are closures that capture the `ModuleContext` (providers) and the peel's
decode policy (`opts modschema.DecodeOptions`). Three shapes exist, keyed in the
`registrations` table:

- `Build BuildFunc` — `func(mctx, opts) state.Builder`; needs providers (most
  modules).
- `BuildPlain PlainBuildFunc` — `func(opts) state.Builder`; no providers, still
  threads the policy (the `test.*` helpers).
- `BuildWithRegistry` — `func(reg *state.Registry) state.Builder`; needs the
  registry itself (`module.run`, registered LAST).

```go
func NewPkgRemovedBuilder(mctx *exec.ModuleContext, opts modschema.DecodeOptions) state.Builder {
    return func(id string, config map[string]any) (state.State, error) {
        if mctx.Package == nil { // provider-nil guard FIRST
            return nil, fmt.Errorf("pkg.removed: no package provider available")
        }
        // Decode is TRANSACTIONAL: it commits by replacing the whole struct, so
        // the injected provider, id, requisites, and any derived facets MUST be
        // assigned AFTER Decode — assigning them before would be overwritten.
        p := &PkgRemoved{}
        if _, err := pkgRemovedSpec.Decode(id, config, p, opts); err != nil {
            return nil, fmt.Errorf("pkg.removed: %w", err) // wraps the typed FieldError/UnknownKeyError
        }
        p.id = id
        p.pkg = mctx.Package
        p.reqs = state.ParseRequisites(config)
        return p, nil
    }
}
```

**Builder-tail rules** (everything that is module logic, not schema):

- **Post-decode assignment** — `id`, injected providers, `state.ParseRequisites(config)`,
  and any DERIVED runtime facets are set after `Decode` (transactional commit).
- **Cross-field validation** — rules spanning two parameters live in the tail,
  not the schema (e.g. `cmd.run` requires a file provider only when `creates` is
  set; `user.present` errors when a name-based primary group is declared but no
  group provider is available; `ssh_auth.present` needs `user` OR `config`).
- **Derived facets** — compute exported-but-untagged runtime fields from decoded
  parameters (e.g. `user.present.resolveGroupFacets()` turns `GroupRef` +
  `primary_group` into `GID`/`PrimaryGroup`; `service.dead` sets
  `DisableOnApply = Enable.Declared() && !Enable.Value()`).
- **Lazy defaults** — a `lazy` parameter is materialized by the MODULE at use
  time via the semantic type's resolver (`FileMode.Resolve(0644)`,
  `TriState.ValueOr(def)`), never in the builder.

The peel threads the policy via `modules.RegisterAll(reg, mctx, opts)`; the knob
is `strict_params` (default on → `PolicyError`, so a typo'd parameter fails the
build with a suggestion; `false` → `PolicyWarn`). Modules never inspect the
policy — they just call `spec.Decode(id, config, dst, opts)` and wrap its error.

## Reserved keys — never parse these in a module

The Salt-parity directives are module-independent and consumed OUTSIDE the module
(`pkg/state/reserved.go` is the single source of truth, wired into the decode
policy's `Reserved` set so they never count as unknown parameters):

- **Requisites** `require`/`watch`/`onchanges`/`onfail` — `state.ParseRequisites`,
  which accepts BOTH the string form (`"pkg.installed:nginx"`) and the Salt dict
  shorthand (`{pkg: nginx}` → `pkg.installed:nginx`, via `saltShorthandMap`).
- **Generic attributes** `onlyif`/`unless`/`order`/`retry`/`failhard`/`prereq` —
  `state.ParseStateAttributes` + `state.WrapAttributes` (the compiler wraps every
  compiled state in `buildOne`; the peel wraps ad-hoc runs). Guard-not-met
  (`onlyif`/`unless`) is a wrapper-reported no-op — diff `skipped: guard condition
  not met`, never an error; `retry` applies only in apply/revert modes and its
  interval is in SECONDS (default 10s) — all handled by the wrapper, invisible to
  the module.
- **Compiler directives** `names`, `listen`, and the `_in` inverses
  (`require_in`/`watch_in`/`onchanges_in`/`onfail_in`/`listen_in`/`prereq_in`) —
  rewritten in `pkg/state/compiler/requisites.go` before the builder runs. `names`
  is expanded (one state per name) BEFORE the builder is called: each name becomes
  the expanded state's ID AND is injected into the module's PRIMARY parameter's
  canonical key when it has one (schema-aware — `command` for cmd.run, `name` for
  file.managed). Injection is UNCONDITIONAL (Salt semantics, rides BD-8): any
  explicit value at the inject key or its aliases is cleared first, so every
  expanded instance runs its own names entry — `names: [...]` alongside an
  explicit `command:` runs each name, exactly as Salt does. A module with
  NO primary parameter (the `test.*` family) gets the historical literal `name`
  key injected instead — safe because `name` is reserved-tolerated everywhere a
  module declares no such field of its own (see the `name` bullet below); the
  expanded state ID still carries the name regardless of the injected key.
- **The Salt universal state-identifier idiom** `name` — Salt lets ANY state
  declare `name:` as a label independent of what it manages
  (`test.nop: - name: anchor` is valid Salt even though `test.nop` declares no
  `name` parameter at all). `name` is therefore reserved fleet-wide
  (`DecodeOptions.ExtraReserved`), exactly like the exec-layer `test` flag below —
  but ONLY as a fallback: a module that DOES declare its own `name` parameter or
  alias (file.managed, pkg.removed, most of the fleet) still decodes an explicit
  `name:` into that field first; the reserved-key excuse is consulted only for a
  key no declared parameter claimed, so it can never mask a genuine typo of a
  parameter a module actually has.

These keys are STILL present in the config map at Build time (the compiler
consumes them later), so under strict decoding they are excused, not flagged. Do
NOT read them in a module. The exec-layer `test` dry-run flag is likewise reserved
(`DecodeOptions.ExtraReserved`).

## State interface methods

### Name() / Reqs()

Format: `"module.function:id"` — e.g. `"service.running:nginx"`.

```go
func (s *SvcRunning) Name() string           { return "service.running:" + s.id }
func (s *SvcRunning) Reqs() state.Requisites { return s.reqs }
```

### Check(ctx)

Returns whether the system already matches desired state. Must not modify the
**managed** state — but MAY run read-only-in-spirit prep (e.g. `pkg.latest`
refreshes the index before probing). **Check and Apply are independent,
self-contained full flows** — never share state or memoize "already done" across
them (watch-forced applies bypass Check entirely). Read a semantic type's
`Declared()` before comparing a facet, so an unset parameter never churns state
it does not manage.

### Apply(ctx)

Makes the system match. Idempotent; returns `Changed: true` when it performs work,
with a `Diff` and `Details`. Record revert memos here (see below).

### Revert(ctx)

Undoes Apply. **Standalone-Revert contract**: in-instance memos (backups,
`wasCreated`, saved originals) are valid ONLY for a same-instance Apply→Revert;
the runner builds FRESH instances for `ModeRevert`, so the unset-memo path MUST be
an explicit clean no-op — `Changed: false`, diff
`nothing to revert (no apply recorded in this run)`. It must NEVER be destructive
(inferring "file was new" from a missing backup memo — the 2026-07 audit's worst
class) and never report a lying diff. Some modules genuinely cannot revert
(`cmd.run`, `pkg.removed`) — return `Changed: false` with an honest explanation.

## Error wrapping

Always: `fmt.Errorf("module.function: operation: %w", err)`.

```go
fmt.Errorf("service.running: start %s: %w", s.Service, err)
fmt.Errorf("pkg.removed: %w", err) // wraps the typed Decode error unchanged
```

## Registration

Register in the family's ordered `Rows` table in
**`pkg/state/modules/<family>/register.go`** (NOT `cmd/zester-peel/main.go` —
that is the old, wrong path). Every row carries a `Spec` and exactly one
builder shape:

```go
var Rows = []regdef.Registration{
    // ...
    {Name: "pkg.removed", Spec: pkgRemovedSpec, Build: NewPkgRemovedBuilder},
}
```

The aggregator's `registrations` table concatenates every family's `Rows`
(historical family order; the `module` family — `module.run` — stays LAST so it
registers after every target it may dispatch to). A brand-NEW family also gets
its `Rows` added to the aggregator's `concatRows` call in
`pkg/state/modules/register.go`, plus the registration-order pin in
`register_test.go`. `RegisterAll` `RegisterSpec`s each row (so
`Describe`/`SpecNames`/`Parse` see it), panicking on a name/spec mismatch or
missing builder. The count is pinned to 47 in
`TestDocCoverage_EveryModuleHasSpec` — bump it deliberately when you add a module.

**N:1 (one proto, several registered names)**: `file.comment`/`file.uncomment`
share one `FileComment` proto but declare two Specs (one per name), each with its
own documentation; docgen's `PageGroups`/`MetaFamily` preserve their page slugs.

## Testing

Every module needs BOTH unit tests and a permanent contract fixture.

### Unit tests (fakes from `pkg/exec/exectest/`)

| Fake | Constructor | Key methods |
|------|-------------|-------------|
| `FakePackageExec` | `NewFakePackageExec(name)` | `PreInstall()`, `IsInstalledSync()`, `RefreshCount()`, `InstallErr` |
| `FakeFileExec` | `NewFakeFileExec()` | `PreCreate()`, `GetFile()` |
| `FakeCommandExec` | `NewFakeCommandExec()` | `SetResult()`, `SetError()`, `Calls()` |

Cover: `Name()`, primary-defaults-to-ID, requisites, Check (change / no change),
Apply (`Changed: true` + `Details`), Apply error propagation, Revert (incl. the
standalone clean no-op), provider-missing builder error. **Convergence is
mandatory**: what Check compares MUST equal what Apply produces — add a
Check→Apply→Check test that ends `NeedsChange: false`. File tests use
`exec.OSFileExec{}` with `t.TempDir()`; package tests ALWAYS use the fake (never
install real packages). For command tests, use the real `exec.OSCommandExec{}`
for simple, safe commands (`echo`, `true`, `false`, `pwd`) and
`exectest.NewFakeCommandExec()` (`SetResult`/`SetError`) when you need to control
output or simulate a failure.

### Permanent contract fixtures (`schematest.RunContract`)

Every migrated module has `testdata/contract/<module>.yaml` — cases of
`{universe (yaml|cli|msgpack), input, want <field projection> | want_error <kind>,
bd}`. They were approved by the legacy-vs-new differential while the legacy
constructor still existed; after its deletion `RunContract` replays them forever
as the decode regression guard. The test is one function per module:

```go
func TestPkgRemovedContract(t *testing.T) {
    decode := func(id string, config map[string]any) (any, error) {
        var p PkgRemoved
        if _, err := pkgRemovedSpec.Decode(id, config, &p, modschema.DecodeOptions{}); err != nil {
            return nil, err
        }
        return &p, nil // project a derived facet here too, if the builder computes one
    }
    schematest.RunContract(t, decode, "testdata/contract/pkg.removed.yaml")
}
```

`want_error` kinds: `missing_required`, `wrong_type`, `value_invalid`. The msgpack
leg is auto-derived (`bus.Encode(yaml) → bus.Decode → Decode`) — it is what
catches the sized-int class (a `mode: 0755` reactor-dispatched as `uint16`).

### Per-param BD pinning standard

Behavioral differences have STABLE IDs (BD-1…BD-8, spec §11) and pin **per
parameter**, not per representative. EVERY boolean-typed parameter (primitive
`bool` AND `TriState`) of a migrated module gets its own `int-one`/`int-zero`/
`invalid-int` fixtures across yaml AND msgpack (BD-7: `1`=true, `0`=false, any
other integer a typed `value_invalid`). A comment saying another param "follows
the same pattern" is NOT a pin. Likewise the numeric-scalar→string acceptance
(a non-string primary coerced, a composite rejected) files under BD-6 per param.
Add the module's activated BD-IDs to the Spec's `Divergences`, and a CHANGELOG
entry for the user-visible activation.

## Docgen — regenerate LAST

After your FINAL source edit (proto, Spec, builder, tests all done), regenerate
the doc artifacts from the live registries:

```
go run ./cmd/zester-docgen        # from the repo root
```

It emits the marker-guarded MDX page per Spec, the modules `meta.json` (all 47),
the combined JSON Schema, and `pkg/moduledoc/docdata.json`. CI gate 1 runs it bare
and requires a clean tree:

```
go run ./cmd/zester-docgen && git add -A && git diff --cached --exit-code website/ pkg/moduledoc
```

A brand-new module whose page was never hand-written generates cleanly; a module
ADOPTING a previously hand-written page needs a one-time `--claim=<module>`. When
you claim/replace a page, first diff your `Doc` against `git show HEAD:<page>` and
fold every hand-page fact in — **code wins on accuracy**, but no fact is dropped.

## File naming

**Do not** use OS/arch-suffix naming (`_linux.go`, `_darwin.go`, `_amd64.go`,
`_js.go`) — Go treats those as implicit build constraints (`fake_js.go` was
silently excluded on darwin/arm64). Use prefix naming: `pkg_apt.go`,
`svc_systemd.go`, `pkg_removed.go`.

## Adding a new exec provider

If your module needs a provider beyond the nine existing interfaces (Package/File/Command/Service/User/Group/Cron/Mount/Sysctl):

1. Define the interface in `pkg/exec/exec.go`.
2. Add the OS implementation in `pkg/exec/` (prefix naming).
3. Add a field to `ModuleContext`/`ProviderSet` in `pkg/exec/context.go`.
4. Wire detection in `DetectProviders()`.
5. Create a fake in `pkg/exec/exectest/`.

## Checklist for a new module

- [ ] Pick the `exec` providers you need (or add one).
- [ ] Define the struct: untagged `id`/`reqs`/providers/memos + tagged exported
      parameter fields (semantic types from `paramtypes` where polymorphic —
      never inline).
- [ ] Write the `Spec` via `regdef.MustSpec` with a drift-corrected `Doc`
      (Summary/Description/Effects-by-kind/Examples/Notes/Divergences/SeeAlso);
      run a `sensitive` pass on any secret-bearing parameter.
- [ ] Implement the builder (provider-nil guard → `spec.Decode` → assign
      `id`/providers/`ParseRequisites`/derived facets AFTER → tail cross-field
      validation).
- [ ] Implement `Name()`/`Reqs()`/`Check()`/`Apply()`/`Revert()`; honor the
      standalone-revert clean no-op; make Check's comparison equal Apply's
      product.
- [ ] Do NOT parse reserved keys (requisites/attributes/`names`/`listen`/`_in`/
      `test`) — the runner, compiler, and wrapper own them.
- [ ] Wrap errors `fmt.Errorf("module.function: op: %w", err)`.
- [ ] Register in the family's `Rows` table
      (`pkg/state/modules/<family>/register.go`; a new family also joins the
      aggregator's `concatRows` — `module` stays last); bump the count pin.
- [ ] Parameter names pass the PARAMETER-VOCABULARY gate
      (`TestParameterVocabularyConsistency` in `cmd/zester-docgen`): the same
      key — canonical name or alias — must carry the SAME schema in every
      module that uses it. On a collision: reuse the existing semantic type,
      pick a non-colliding name, or add a justified, participant-pinned entry
      to the exception table. (Distinct from the SEMANTIC-TYPE vocabulary in
      `paramtypes/vocabulary.go` — that one registers types; this one governs
      parameter KEY reuse across modules.)
- [ ] Unit tests (name, primary-default, requisites, check both ways, apply,
      apply-error, revert, provider-missing, convergence).
- [ ] Permanent contract fixture `<family>/testdata/contract/<module>.yaml` +
      `RunContract` test; per-param BD fixtures across yaml+msgpack for every
      bool/TriState and numeric-coercion parameter; CHANGELOG entry for any BD.
- [ ] `go run ./cmd/zester-docgen` LAST; verify the tree is clean and the website
      builds.
