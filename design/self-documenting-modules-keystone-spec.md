# Keystone Spec v2 — Self-Documenting Modules (implementation contract)

Status: **architecture FINAL** (v2, 2026-07-12 — revised after external design review).
Behavioral differences **BD-1…BD-6 are PENDING maintainer sign-off** (§11); each activates
only in the PR that ships it, with its CHANGELOG entry and pinning test. Where this
document disagrees with any earlier spec section or v1, THIS document wins.

## Normative principle (maintainer-locked)

> **Module parameter architecture.** One shared decoding framework, one schema
> declaration per module, and explicit named semantic types for polymorphic parameters.
> Semantic types live in a controlled package, cannot be defined inline, and must provide
> decoding, validation, semantic documentation, and JSON Schema metadata from the same
> implementation.
>
> The framework carries no module- or type-specific semantics. It only walks declarations
> and dispatches to primitive or named semantic types.
>
> Every semantic type and every migrated module must be covered by differential tests
> across all supported input representations, including YAML values, CLI `key=value`
> strings, and msgpack round-trips, before the legacy parser is removed. Any intentional
> behavioral difference must be documented in the CHANGELOG and pinned by its own test as
> a deliberate behavioral fix.
>
> A module's schema declaration includes both its parameter structure and its registered
> documentation metadata, including its summary, behavioral description, effects, and
> examples.

Interpretation (maintainer-flagged, unvetoed): "validation" = Decode's rejection
contract, pinned by `WantErr`/`WantErrKind` fixtures and mirrored by JSON Schema
constraints, with mechanical fixture↔schema agreement tests (§9.4). No ceremonial
`Validate()` method.

## Empirical foundation (reproduced)

`bus.Encode/Decode` (msgpack v5) returns sized int kinds by magnitude: `420 → uint16`,
`-5 → int8`, `70000 → uint32`. Legacy `.(int)`/`.(string)` match none of the unsigned
kinds ⇒ a reactor-dispatched `file.managed` `mode: 0755` silently applies **0644** today.
The CLI (`parseKeyValues`) delivers every value as a raw string ⇒ `enable=true`,
`makedirs=true`, `template=true` are silently ignored today. The uniform compiled decoder
fixes both classes (BD-1, BD-2).

## 1. Package layout & import graph

```
pkg/modschema              framework: tag→plan compiler, plan-executing Decode, Doc/Spec/
                           ModuleInfo/RenderText, ValidateUnknownKeys. Imports stdlib +
                           pkg/modschema/paramtypes ONLY. Never pkg/state, never internal/*.
pkg/modschema/paramtypes   sealed semantic types. Stdlib only. Never imports modschema.
pkg/modschema/schematest   test harness (fixture types, ingress adapters, Equivalence,
                           contract-fixture runner). Imports modschema, paramtypes,
                           pkg/bus, pkg/cliargs, yaml.v3. Production code never imports it.
pkg/cliargs                NEUTRAL home of the CLI key=value parser (moved verbatim from
                           cmd/zester/cmd; the CLI re-imports it). Enables the harness to
                           exercise the REAL CLI ingress; later serves parseModuleArgs
                           unification (Phase 4).
pkg/state                  imports modschema (RegisterSpec). Exports the reserved-key
                           lists (§6); the compiler DERIVES its rewrite maps from them.
pkg/state/modules          protos import paramtypes (field types) + modschema (Decode).
                           Owns RegisterAll, DispatchSpecials, PageGroups/MetaFamily.
pkg/execmod                imports modschema (RegisterSpec, DocSource seam).
pkg/moduledoc              leaf; go:embed docdata.json; imports modschema. CLI-side docs.
cmd/zester-docgen          generator; imports state, modules, execmod, modschema, moduledoc.
```

Architecture test pins every "never imports" via `go list -deps`.

## 2. Framework — pkg/modschema

### 2.1 Tag grammar (single interpretation — parsed ONCE, at compile)

```go
`zester:"<name>[,opt]..." usage:"<help text, commas fine>"`
```

Options: `primary` (falls back to the state ID; max one; eager; Origin: Primary),
`aliases=a|b` (pipe-separated), `required`. SOURCE RESOLUTION (amended 2026-07-12 after
the file.managed gate): sources are consulted in order — declared name, then each alias
left-to-right — and an EMPTY-STRING value at one source falls THROUGH to the next source
exactly like absence (legacy `if x == "" { x = next }` chains: `name: ""` alongside a
non-empty `path:` uses path, NOT the ID). Only when every source is absent/empty does a
primary receive the state ID (`name: "{{ var }}"` rendering empty with no alias set
still falls back to the ID). A nil value (YAML null — the `key:` trailing-colon shape)
is treated as ABSENT for every param (legacy comma-ok parity); it never reaches coercion.
Remaining options:
`default=LIT` (EAGER; decoded through the field's own decoder at COMPILE time — an
invalid default fails compilation, not runtime), `lazy` (default documented, never
materialized; field stays zero; module applies at use time), `sensitive` (§2.5).
`zester:"-"` skips; untagged structs recurse; untagged non-structs skipped.
Verified characterizations: `file.managed` mode `lazy,default=0644` (genuinely lazy —
`desiredMode`); `file.recurse` dir_mode DECLARED-ONLY lazy; cron fields **eager**
`default=*`, user eager `default=root` (defaults are applied at construction today).

### 2.2 Compile once, decode from the plan

```go
func Compile(proto any, doc Doc) (*CompiledSchema, error)
```

The ONLY tag-parsing pass. Produces the **BindingPlan** — per field: index path,
canonical name, aliases, kind (primitive | semantic-type ref), pre-decoded eager default
value, required/lazy/primary/sensitive flags, JSON Schema fragment — and the derived
`ModuleSchema` (docs/JSON-Schema/sys.doc view). Every consumer (Decode, docgen, sys.doc,
JSON Schema export) reads the SAME compiled plan; a second tag interpretation cannot
exist. Compile errors: duplicate names, >1 primary, unregistered non-primitive field
type, invalid default literal, `lazy` on a required field, alias collisions (an alias
equal to any canonical name or another field's alias — ambiguous multi-binding is
unrepresentable), unknown tag options, a zester tag on an unexported non-embedded field.

```go
func (cs *CompiledSchema) Decode(id string, config map[string]any, dst any, opts DecodeOptions) (*DecodeReport, error)
```

**Transactional (normative):** decoding runs against a scratch instance; `dst` is written
only if every field succeeds. On error `dst` is untouched — partially decoded data is
unrepresentable, retries against the same destination are safe.

Per-field semantics (plan order): resolve source key (name → aliases in order; a nil OR
empty-string value at a source falls through to the next source like absence; record
which source supplied the value) → present ⇒ coerce (primitive table | semantic dispatch
with `Input{Origin: Explicit|Alias, Key, ...}`) → primary AND every source absent/empty
⇒ id (`Origin: Primary`) → absent + eager default ⇒ pre-decoded default value
(`Origin: Default`) → absent + required ⇒ `FieldError{ErrMissingRequired}`. `lazy` never materializes. Errors joined; typed
`FieldError{Module, Param, Key, Kind, Type, Value /* redacted when sensitive */, Err}`
(kinds MissingRequired | WrongType | ValueInvalid), `UnknownKeyError{Module, Key,
Suggestion /* inline edit-distance ≤ 2 */, Known}`.

`DecodeOptions{Unknown Policy /* Ignore(default)|Warn|Error */, Warnf, Reserved
map[string]struct{} /* injected: state.ReservedKeySet() */, ExtraReserved []string
/* e.g. exec-layer "test" */}` · `DecodeReport{UnknownKeys, LazyDefaults}`.

### 2.3 Uniform primitive coercion (tightened; origin-independent)

| target | accepts |
|---|---|
| string | string as-is; other scalars via fmt.Sprint (documented, deliberate) |
| bool | bool; truthy/falsy string set {true/yes/1/on ∥ false/no/0/off, case-insensitive}; ints **0/1 only** (other numbers error); floats rejected |
| int | every int/uint kind via reflect Int()/Uint(), range-checked; finite integral floats; strings **base-10 only** (octal semantics belong exclusively to FileMode) |
| float64 | any finite numeric kind; strings via ParseFloat; NaN/Inf rejected |
| map[string]any, []any | passthrough |

NOT primitives: `[]string`, `map[string]string` (StringList/StringMap own them),
durations (unused by modules), anything from `internal/` (unimportable from pkg/).
Declared leniency: string→bool/int/float coercion trims surrounding ASCII whitespace
before parsing.

### 2.4 Semantic dispatch

By field Go type via `paramtypes.ForGoType` — **`ptype` is removed**; every semantic type
has a distinct named Go type (uniqueness pinned by conformance test). Unregistered
non-primitive field type = Compile error (seal wall b).

### 2.5 Sensitive parameters (before ANY migration)

`zester:"password,sensitive"`: value redacted across the ENTIRE error chain — the
FieldError, the rendered text of every wrapped cause, and the `Unwrap()` chain must not
carry the raw value or any substring of it — plus warnings/DecodeReport; JSON Schema
`writeOnly: true`; defaults never rendered; excluded from generated examples;
`Field.Sensitive` carried in ModuleSchema/ModuleInfo so every renderer (MDX, sys.doc,
docdata) redacts. Inventory: `user.present.password` at minimum; migration checklist
requires a sensitivity pass per module.

## 3. Semantic types — pkg/modschema/paramtypes (sealed)

```go
type SemanticType interface {
    Name() string                 // stable id: JSON Schema $defs key, docs anchor
    GoType() reflect.Type         // dispatch key; unique per type; Decode's return type
    Decode(in Input) (any, error) // pure; value receivers; never applies module defaults
    Doc() string                  // semantic doc string (MDX, sys.doc, schema description)
    JSONSchema() map[string]any   // accepted representations; constraints = validation surface
    sealed()                      // unexported: only paramtypes can implement
}

type InputOrigin uint8 // OriginExplicit | OriginAlias | OriginPrimary | OriginDefault
type Input struct{ Raw any; Origin InputOrigin; Key string; StateID string }
```

Absent fields NEVER reach a semantic Decode — the zero value IS undeclared (`Declared()
== false`). EMPTY-STRING RULE (added 2026-07-12 after the user.present gate): a semantic
type receiving an empty string decodes to its undeclared zero value — legacy comma-ok
parity (`gid: ""` behaves as absent and falls through to `primary_group`), mirroring
§2.1's empty-primary ID fallback. FileMode and TriState already conform; every semantic
type must. The framework decides when a default applies; `Origin` tells the type whether
the value was explicit, alias-supplied, id-synthesized, or defaulted (so declared-ness
semantics of e.g. TriState under a future `default=` are well-defined: OriginDefault may
be recorded distinctly if a module ever needs it).

**No `Fixtures()` in the production interface.** Fixture topology (maintainer-directed):
- Fixture TYPES live in `schematest`: `TypeFixture{Label, YAML any, CLI string, CLISkip
  bool, CLISkipReason string, Want any, WantErr bool, WantErrKind modschema.ErrorKind}`.
- Fixture DATA lives in ONE external-test file, `paramtypes/fixtures_test.go`:
  `var typeFixtures = map[string][]schematest.TypeFixture{...}`.
- Completeness is test-enforced: the paramtypes conformance test asserts
  `keys(typeFixtures) == names(paramtypes.All())` — a new type without fixtures fails.
- `schematest.RunTypeFixtures(t, st, fixtures)` executes the matrix; the msgpack leg is
  ALWAYS auto-derived (`bus.Encode(YAML) → bus.Decode → Decode`) — hand-written msgpack
  fixtures are banned (an int64 literal never exercises the uint16 case).
- Production binaries carry zero fixture data.

Sealing walls: (a) unexported `sealed()` — inline/foreign implementations cannot compile;
(b) Compile errors on unregistered non-primitive field types; (c) conformance test:
unique names, unique GoTypes, pinned count, complete fixture coverage. Test hook:
paramtypes may export `RegisterTestType` for framework tests ONLY, guarded by
`testing.Testing()` — it panics outside `go test`, so it is inert in production binaries.

**Vocabulary (6):** FileMode (string|octal-int any-kind; `Declared()/Resolve(def)`;
absorbs mode.go parse/compare logic; setuid/sticky honored) · TriState
(`Declared()/Value()/ValueOr(def)`; replaces Enable+hasEnable; service.dead computes
`DisableOnApply = Declared() && !Value()` in the module) · GroupRef (int-kinds=GID |
string=name; all-digit string numeric = BD-4) · TemplateFlag (bool|"jinja"|truthy = BD-2)
· StringList (string|[]string|[]any; scalars sprint'd, nested elements error = BD-5) ·
StringMap (map→map[string]string; scalar values sprint'd, composite values ERROR; CLI-
inexpressible ⇒ CLISkip + reason). `file.line` mode is an ACTION enum (plain string),
never FileMode. cmd.run: args→StringList, env→StringMap, command primary.

## 4. Doc metadata & Spec

`Doc{Summary, Description /* CommonMark; rendered between Source line and Parameters */,
Effects{Check, Apply, Revert, Execution}, Examples []Example{Title, Kind(cli|state),
Explanation, Code}, Notes []Note{Level, Title, Body}, Divergences []string /* carries
BD-IDs */, SeeAlso []string}`.

**Effects requirements by Kind** (coverage-test enforced): state ⇒ Check+Apply required,
Revert optional (explicit "cannot revert" prose encouraged); exec ⇒ Execution required;
dispatch ⇒ Execution required. No fake Check/Apply prose for phase-less surfaces.

```go
func NewSpec(module string, kind Kind, proto any, doc Doc) (*Spec, error) // compiles once
type Spec struct {
    Module string; Kind Kind; Doc Doc
    Params    *ModuleSchema   // derived view (docs/schema/sys.doc)
    NewParams func() any      // fresh proto factory (Registry.Parse, harness)
    OpenParams bool           // module.run passthrough: unknown-key validation skipped,
                              // surfaced in sys.doc as "accepts arbitrary parameters"
    // unexported: plan *CompiledSchema — Decode and Parse both execute THIS plan.
}
func (s *Spec) Decode(id string, config map[string]any, dst any, opts DecodeOptions) (*DecodeReport, error)
```

`ModuleInfo{Module, Kind, Doc, Params []Field /* incl. Sensitive */, SemTypes
[]SemanticTypeInfo{Name, Doc, JSONSchema}, HasSpec, AlsoExecmod}` — the one currency of
sys.doc, `zester doc`, docgen, docdata; ONE `RenderText`. Prose is CommonMark only
(`assertNoJSX`); `<Tabs>` content migrates as sequential fenced blocks; `<Callout>` →
Note blockquotes.

## 5. Registries & registration

Both registries: `RegisterSpec(spec *modschema.Spec, b Builder)` (name = spec.Module;
empty/duplicate = error) · `Describe(name) (ModuleInfo, bool)` · `SpecNames() []string`;
state registry also `Parse(name, id string, raw map[string]any) (*DecodeReport, error)` =
`spec.NewParams()` + `spec.Decode(...)` — the SAME compiled plan constructors execute.
`Register` stays for legacy/Starlark. **Constructors decode themselves** into typed
protos (no map-normalizing wrappers); policy threads via
`modules.RegisterAll(reg, mctx, opts modschema.DecodeOptions)` — peel wires
`Reserved: state.ReservedKeySet(), Unknown: PolicyWarn, Warnf: logger`; gate close flips
one knob to PolicyError. `Registration{Name, Spec, Build|BuildPlain|BuildWithRegistry}`
(exactly one; ordered slice; module.run LAST). internal/peeld/modules.go → one call.

## 6. Reserved-key union — pkg/state/reserved.go

`attributeKeys` (onlyif unless order retry failhard prereq — ParseStateAttributes
consumes THIS slice), `requisiteKeys` (require watch onchanges onfail — ParseRequisites
likewise), `compilerKeys` (names listen listen_in + the `_in` forms). The compiler
**derives** its rewrite maps (inverseForward, listen alias, names expansion key) FROM the
state-exported lists — no duplicated literals — and a compiler-package conformance test
pins consumed ⊆ exported as the belt. `ReservedKeys() []string` + `ReservedKeySet()`.
Membership EXCLUDES `name` (module.run adds its own local `{"name"}`); exec-layer `test`
injected via ExtraReserved, never promoted.

## 7. Peel dispatch & sys.doc

One `modules.DispatchSpecials` table drives BOTH dispatch sites (`readOnlyModule`'s
prefix set is derived from it; `execModule`'s if/else chain replaced by lookup).
**Structural precedence: exact match → longest matching prefix → table order as final
deterministic tiebreak.** Family catch-alls (facts.*, settings.*, pillar.*) preserve
in-handler unknown-subfunction errors. `TestDispatchTableBound` pins handler↔table 1:1
across both sites.

sys.doc: DocSource seam (execmod never imports pkg/state); lookup mirrors dispatch
precedence; cmd.run dual-surface (state primary + "also an execution module — reachable
from templates via salt['cmd.run']" appendix); bare `sys.doc` = unified index (also fixes
sys.list_functions, re-registered merged). Result string rides
**`ExecResponse.Results[0].Details["result"]`** (ExecResponse has NO Details field).
sys.doc joins BOTH `readOnlyModule` (answers during highstates; handler_test pin) and
`isStreamlined`. Offline: `zester doc` renders pkg/moduledoc (embedded docdata) through
the same RenderText; `TestDocdataMatchesLive` pins embedded == live.

## 8. Docgen — cmd/zester-docgen

Emits: marker-guarded module MDX (refuses markerless overwrite), `guides/modules/meta.json`
(wholesale, FULLY-enumerated MetaFamily, byte-exact live separators, **`index` NOT
listed**), ONE combined JSON Schema artifact (`website/public/schema/zester-modules.
schema.json`, draft 2020-12, `$defs` per module + shared semantic-type `$defs`, state-map
oneOf), `pkg/moduledoc/docdata.json`. Page anatomy: frontmatter → marker → `**Source**:`
→ Description → Parameters table → auto requisites boilerplate → Parameter Types →
Effects sections (by kind) → Examples → Notes → Divergences → See Also. N:1 PageGroups
preserve slugs (file-comment, host, ssh-auth, test-helpers, query ← generated from
DispatchSpecials, cmd-run dual-surface). state.apply/highstate/event.send Docs feed
sys.doc+docdata only in v1. **Validation:** every generated example parses as YAML
against the module's JSON Schema; self-contained non-templated state examples must
construct via Registry.Build (others recorded-skipped, never silently); SeeAlso targets
and PageGroup membership resolve or generation fails. Sensitive params never appear in
examples or rendered defaults. Deterministic output.

## 9. CI gates (PR-visible test job)

1. `go run ./cmd/zester-docgen && git add -A && git diff --cached --exit-code website/
   pkg/moduledoc` (catches untracked new artifacts; leaves index clean on failure).
2. Website build on PR (pnpm, exact action versions from docs.yml).
3. Conformance suite in `go test ./...`: doc-coverage shrink-only ratchet (policyMsg;
   Effects-by-kind validation), paramtypes conformance (seal walls, GoType uniqueness,
   fixture completeness, TypeFixture matrix incl. auto-msgpack), compiler reserved-keys
   pin, TestDispatchTableBound, TestDocdataMatchesLive, architecture import test.
4. Fixture↔JSON-Schema agreement: every accepting TypeFixture validates against the
   type's emitted schema; every rejecting fixture fails it where the representation is
   expressible. Test-only dependency: santhosh-tekuri/jsonschema (maintained standard;
   maintainer-approved 2026-07-12).

## 10. Differential harness & permanent contract fixtures — schematest

**Real ingress (normative):** YAML leg = a real YAML snippet through yaml.v3 Unmarshal
(the production loader); CLI leg = real `key=value` token slices through
`pkg/cliargs.ParseKeyValues` (the production parser, relocated); msgpack leg =
`bus.Encode → bus.Decode` of the YAML-parsed map. Synthesized fmt.Sprint legs are banned.

`Equivalence{Module, ID, YAMLSource string, CLITokens []string /* optional; derived when
omitted, non-scalars recorded-skipped */, Legacy, Decoded func(id string, config
map[string]any) (any, error) /* any: works for state AND exec migrations */, Compare
func(legacy, decoded any) error, Diffs []IntentionalDiff{BD string /* stable ID */,
Universe, Param, Reason, Changelog, Expect}}` — runs Legacy vs Decoded per universe,
fails on undeclared divergence, asserts Expect + non-empty Changelog per declared diff.

**Permanent contract fixtures (survive legacy deletion):** migration produces
`testdata/contract/<module>.yaml` — cases of {universe, input, expected params projection
| expected error kind, BD-ID} — APPROVED by the legacy comparison while it still exists.
After the legacy constructor is deleted, `schematest.RunContract(t, decode, file)`
(caller wraps `spec.Decode` into the Spec-agnostic `schematest.Decoder` shape) replays
them against the new decoder forever. The differential proof becomes a permanent
regression guard, without keeping dead production code.

## 11. Behavioral differences (STABLE IDs; PENDING maintainer sign-off)

BD-1 msgpack sized-int values honored (fixes reproduced mode 0755→0644 reactor bug) ·
BD-2 CLI string coercions work (enable=true, makedirs=true, template=true/truthy, numeric
strings) · BD-3 `minute: 5` = minute 5, not `*` · BD-4 all-digit string gid = numeric
GID · BD-5 StringList sprints scalars / errors on nested (was silent drop) · BD-6
wrong-typed values produce typed errors (was silent zero) · BD-7 boolean integer
coercion: `1` = true, `0` = false, any other integer a typed error (was: ints silently
ignored). SCOPE (orchestrator ruling 2026-07-12, under the maintainer's TriState 1/0
approval + the approved coercion table): applies to ALL boolean-typed parameters —
primitive `bool` and TriState alike — one consistent rule. PINNING STANDARD (ruling
2026-07-13): per-PARAM, not representative — EVERY boolean parameter of a migrated
module gets its own int-one/int-zero/invalid-int fixtures across yaml+msgpack; a comment
saying another param "follows the same pattern" is not a pin. Likewise BD-6 instances
pin per-param (the numeric-scalar→string acceptance direction files under BD-6, per the
pkg.installed `version` precedent). Each activates only in the PR that migrates the affected
surface, with CHANGELOG entry + pinned contract fixture.

SIGN-OFF STATUS (maintainer, 2026-07-12): **BD-2 APPROVED** · **BD-6 APPROVED** ·
**BD-7 APPROVED conditionally** — the behavior must be explicitly documented (TriState
Doc + generated pages) and covered by contract fixtures across ALL THREE universes
(YAML, CLI, msgpack) at both the type level (TypeFixtures) and the module level
(service.* contract files). BD-1/3/4/5 remain PENDING — each is presented for sign-off
in the Phase-1 PR that activates it.

## 12. Implementation tranches

- **prep** (independent): reserved-keys (§6 — partial output from the stopped run is on
  disk; gate-review, add derive-in-compiler, finish). Dispatch-table refactor (§7) stays
  scheduled with Phase 3 — not a Phase 0 dependency.
- **0A — schema compiler**: Compile/BindingPlan, transactional Decode, coercion v2, typed
  errors, sensitive support, unknown-key policies; unit + fuzz tests (fuzz Decode against
  arbitrary map shapes — must never panic, never partially write dst).
- **0B — semantic types + conformance**: paramtypes (interface v2, seal, registry, 6
  types), fixture topology per §3, pkg/cliargs relocation, real ingress adapters,
  TypeFixture matrix, fixture↔schema agreement (gate 9.4).
- **0C — registry vertical slice**: NewSpec/Spec, RegisterSpec/Describe/Parse,
  RegisterAll relocation (behavior-identical), **pilot #1: `pkg.removed`** migrated with
  exact parity + permanent contract fixtures (proves the vertical with zero semantic
  types, zero behavior change).
- **0D — documentation infrastructure**: ModuleInfo/RenderText, pkg/moduledoc, docgen
  skeleton (marker guard, deterministic no-op on empty), meta/schema/docdata emitters
  behind spec-presence, coverage ratchet (full 47-name allowlist), CI gates 1–3.
- **0E — semantic-type pilot (maintainer-directed, gates broad migration)**:
  `service.running` + `service.dead` on TriState — the Enable+hasEnable → TriState
  conversion through Check/Apply/Revert (the audit's tri-state class), full differential
  + permanent fixtures (BD-2 activates here for `enable`), drift-corrected Doc (the
  measured service-running.mdx gaps), generated pages replace the hand pages. Exit: the
  hardest conversion pattern proven end-to-end; only then do Phase-1 family waves start.

Every tranche: tests mandatory, full suite green, CHANGELOG only for user-visible
activations, my gate review before the next tranche builds on it.
