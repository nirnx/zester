# Changelog

All notable changes to Zester are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/) (0.x — APIs may still change between minors).

## [0.6.5] - 2026-07-14

### Added
- **Family parameter components (Amendment A1, spec §13).** The `file.*`
  family's canonical parameters are now declared ONCE in embeddable family
  components — `makedirs`, `mode`, `user`/`group` ownership, and `source` —
  and members embed them; the schema declaration, canonical usage text, and
  runtime behavior contract are all shared, per the approved family-scoped
  contract model. The framework gains member-supplied dimensions
  (`memberdefault`/`memberrequired` tag options + `modschema.WithDefault`/
  `WithRequired`): the mode component fixes name/type/semantics while
  `file.managed` supplies 0644 and `file.directory` 0755; the source
  component fixes everything but requiredness (`file.copy` requires it).
  Every field is stamped with its declaring type, and the vocabulary gate
  gains a COMPONENT RATCHET: once a family component exists for a key, a
  private redeclaration in any member — even byte-identical — fails CI.
- **Paired-family components (A1 step 7).** `ssh_auth.*` (the user/config
  target pair), `host.*` (the config/path hosts-file selector), `cron.*`
  (the user/command entry identity), and `pkg.*` (`refresh`, with
  member-supplied defaults: `pkg.installed` false, `pkg.latest` true —
  behavior unchanged) now declare their canonical parameters once in family
  components. `git.*`'s shared keys (branch/rev/force) stay member-declared:
  their per-operation semantics genuinely differ, and the gate keeps their
  contract signatures aligned. The pkg refresh vocabulary-ratchet entry is
  retired — the in-family exception table is down to the single
  maintainer-blessed permanent (`file.line`'s action-selector `mode`).
- **Canonical `file.*` `makedirs` runtime contract, enforced by a shared
  behavior suite.** All six members (`managed`, `copy`, `directory`,
  `recurse`, `symlink`, `touch`) now implement ONE contract: `makedirs`
  governs missing PARENTS of the target only (created at 0755 when true);
  when false, a missing parent fails BOTH Check and Apply with an error
  naming the parent and the `makedirs: true` remedy — no partial creation,
  and Revert never removes created parents. A four-case behavior suite
  (false/true × parents present/missing) runs against every embedding member.

- **`path` alias unified across the whole file family.** All 13 `file.*`
  modules now accept `path` as an alias of their primary — previously 7 did
  and 6 (absent, append, blockreplace, directory, recurse, symlink) rejected
  it under strict params (or, on blockreplace, deliberately ignored it), so
  `- path: /srv/www` worked on `file.managed` but failed on `file.directory`.
  Pinned by three-universe contract fixtures per module; blockreplace's old
  "path is not an alias" pin is retired by this change.

- **Family form for the doc surfaces (Salt parity).** `zester '<target>'
  sys.doc ssh_auth` and the offline `zester doc ssh_auth` now render every
  documented `ssh_auth.*` module as one document (sorted, shared
  `RenderTextAll` shape on both surfaces) instead of erroring
  `no documentation for "ssh_auth"`. Works for any family — `file`, `pkg`,
  `facts` (dispatch specials included); `--json` returns an array for a
  family. Family names also join `zester doc`'s did-you-mean suggestions and
  shell completion.
- **Parameter vocabulary gate.** A conformance test
  (`TestParameterVocabularyConsistency`) now compares every parameter key —
  canonical names and aliases — across ALL state and exec modules and fails
  when the same key carries different schemas ("`mode` must always behave the
  same"). The fleet passes with exactly three documented Salt-parity
  exceptions (`mode` in file.line = action selector; `gid` in group.present =
  numeric-only create id; `text` in test.echo = scalar echo string), each
  justified in the exception table, which also refuses stale entries. The
  developing guide documents the rule. REWORKED to the family-scoped
  two-tier form per the approved Amendment A1 (spec §13): the contract
  boundary is (module kind, family, parameter name) — within one family the
  full contract is compared (shape, aliases, primary, requiredness,
  default), across families only the value shape (the same spelling may
  legitimately mean different things in unrelated families). Alias keys are
  first-class: the gate caught `dir_mode` meaning an alias-of-mode on
  file.directory but a standalone parameter on file.recurse. In-family
  divergences are pinned as migration-ratchet entries the family-component
  tranches delete. Exceptions are participant-pinned:
  the exception covers only the known divergence, so a NEW module reusing an
  excepted key (e.g. a third `mode` shape) still fails until the pin is
  deliberately updated. Dual-surface modules (`cmd.run`) now carry their
  "also reachable as an execution module" header in the OFFLINE docs too —
  the embedded docdata gains the same AlsoExecmod overlay live sys.doc
  renders, and the docdata↔live parity test covers it.

### Changed
- **Family-oriented package layout (A1 step 8).** `pkg/state/modules` is now
  one package per module family (`modules/file/`, `modules/cron/`, …; package
  names `filemod`, `cronmod`, …), each holding its members, its parameter
  components (`components.go`), its behavior suites, its contract fixtures,
  and its registration rows; shared plumbing lives in `modules/regdef`
  (registration types + `MustSpec`) and `modules/internal/famshared`
  (cross-family helpers). The aggregator `pkg/state/modules` keeps its import
  path and public API (`RegisterAll`, the dispatch specials) unchanged — no
  consumer changes; file history follows the moves.

### Fixed
- **`makedirs: true` no longer accepts a regular file at the parent path, and
  non-ENOENT `Stat` failures are surfaced instead of masked.** The Apply-side
  parent-creation arm returned success whenever `Stat(parent)` succeeded —
  without checking it was a directory — and fell through to `MkdirAll` on ANY
  stat error, hiding permission/I-O/provider failures behind a creation
  attempt. It now succeeds only for an existing directory, reports a clear
  not-a-directory error for a file, creates only on a genuine not-exist, and
  wraps every other stat error untouched. Two new arms in the shared family
  behavior suite pin both cases per member (regular-file parent; injected
  non-ENOENT stat failure via the fake's new fault injection).

- **`makedirs` Check semantics re-ruled (Salt-aligned).** A missing parent
  with `makedirs` unset no longer fails Check: dry runs do not materialize
  changes from earlier required states, so a correctly ordered tree (a
  directory state creating the parent, a file state writing into it with
  `require`) must dry-run clean — Check now reports a would-change whose
  detail names the missing parent and the remedy. Apply retains the strict
  canonical contract: a parent still missing when the operation actually runs
  fails without partial creation. Pinned by the ordered-tree dry-run,
  ordered-tree apply, and standalone strict-apply cases in the family
  behavior suite; BD-9's wording updated. (Also fixed en route: the exectest
  fake's Chmod wiped file-TYPE bits, silently un-directorying entries.)

- **A1 final-verification round.** `file.directory`'s documentation was
  drift-corrected (it still described the pre-fix inert `makedirs`; the
  published page contradicted its own parameter table) and the five other
  members' Check effects now document the missing-parent failure; the fix is
  minted as **BD-9** in the spec and each member's Divergences. The makedirs
  helpers now `Clean` the target path first (a trailing-slash target no
  longer gates on itself) and report a parent that exists as a regular file
  with a clear not-a-directory error. `pkg.*`'s `refresh` was
  UN-componentized: its two members genuinely differ in phase coverage and
  error semantics (installed: Apply-only, fatal; latest: Check+Apply,
  warn-only), so per §13 it is member-declared with a permanent pinned
  exception — the git.* precedent. Gate hardening: the component ratchet now
  fires for single-embedder components (a member defecting from a 2-member
  component was previously invisible); member-supplied dimensions are only
  legal on embedded component fields; untagged pointer-to-struct embeds are
  compile errors (previously silently dropped the whole component); the
  declaring-type stamp gained direct tests; same-signature keys with
  per-member runtime meanings (`dir_mode`) are declared in a participant-
  pinned meaning-variance registry. The behavior suite now asserts the 0755
  creation mode, that errors name the missing parent, revert-never-removes-
  parents, trailing-slash handling, and pins suite completeness against the
  live embedder set.

- **`file.directory` `makedirs` was accepted but inert — deliberate
  compatibility fix (A1).** The module previously always created the full
  parent chain (`MkdirAll`), silently masking typos in deep paths, and
  ignored the flag. It now follows the canonical contract: a missing parent
  without `makedirs: true` fails Check and Apply. Behavioral difference from
  0.6.0, maintainer-ruled; pinned by the shared behavior suite and the
  makedirs contract fixtures across YAML/CLI/msgpack.
- **The other five `makedirs` members failed missing-parent cases with raw
  OS errors and reported phantom applicable changes in Check.** All now fail
  both phases with the canonical contract error. `file.recurse` previously
  created missing parents with `dir_mode` instead of the canonical 0755.
- **`file.directory`'s `dir_mode` is now a standalone parameter** (was an
  alias of `mode`): decode lands on its own field and the mode fallback
  (mode wins; `dir_mode` fills in when `mode` is undeclared) moved to the
  builder — behavior is byte-identical to the alias era, pinned by
  `TestFileDirectoryDirModeFallback` and updated contract fixtures; its
  contract signature now matches `file.recurse`'s `dir_mode`, retiring that
  vocabulary-ratchet entry (along with the `source` one).

## [0.6.0] - 2026-07-13

### Added
<!-- track-B1: runtime documentation surface (DispatchSpecials + sys.doc) -->
- **`sys.doc` — on-node module documentation.** `zester '<target>' sys.doc`
  returns a unified index of every callable surface (state modules, execution
  functions, and the peel dispatch specials `state.apply`/`state.highstate`/
  `facts.*`/`settings.*`/`pillar.*`/`event.send`); `sys.doc <module>` renders
  that module's documentation through the same `modschema.RenderText` used by
  `zester doc` and the generated pages, mirroring dispatch precedence
  (dispatch specials → state registry → execution registry). A module that is
  both a state and an execution surface (e.g. `cmd.run`) is annotated as also
  reachable from templates via `salt['<module>']`. `sys.doc` runs on the peel's
  read-only fast path, so it answers even during a long highstate; its result
  rides the existing `ExecResponse` result field (no wire change).
- **`sys.list_functions` now lists every callable surface on the peel** — state
  modules and dispatch specials in addition to execution functions (it
  previously listed only execution functions).

- **Self-documenting execution modules + the first execution-module reference
  page.** Every built-in remote-execution function (`test.echo`/`version`/`true`/
  `false`, `pkg.version`/`list_pkgs`, `service.status`/`start`/`stop`/`restart`,
  `disk.usage`, `cmd.run`, `grains.item`/`items`, `sys.list_functions`,
  `sys.doc`) now carries a `modschema.Spec` (Kind `exec`), registered via
  `Registry.RegisterSpec` — so `sys.doc <function>`, `zester doc`, and the
  generated docs all describe it with drift-corrected parameter and behavior
  metadata. Parameter schemas are lifted from each function's actual argument
  alias sets (e.g. `pkg.version` accepts `name`/`package`/`pkg`, defaulting to
  the request ID) **in the SAME precedence order the runtime `argStr` helper
  checks them** — `cmd.run` as an execution module (`salt['cmd.run'](...)`,
  `zester '<target>' cmd.run ...`) declares `cmd` as its canonical primary
  (aliases `command`, `name`, argStr's exact check order), not `command` —
  a declared canonical name is checked before its aliases, so a spec whose
  order didn't match the runtime's own precedence would silently accept the
  wrong value when a caller set more than one of them; pinned by a permanent
  contract fixture where both `cmd` and `command` are set. (This deliberately
  differs from the `cmd.run` STATE module, whose primary is `command` — a
  different surface with its own precedence, see BD-8 below.) A new combined
  reference page,
  [Execution Modules](/docs/guides/execution-modules), documents them all
  (generated by `zester-docgen`; `cmd.run` stays on its dual-surface state
  page). A closed execmod doc-coverage conformance gate now requires every
  registered execution function to carry a spec, and permanent contract fixtures
  pin the argument-alias decoding across YAML/CLI/msgpack.

- **`zester doc [module] [--json]` — offline operator documentation for
  self-documenting modules (Track B2).** Renders the embedded module docs
  (`pkg/moduledoc`) through the SAME `modschema.RenderText` a connected daemon
  uses for `sys.doc`, so the offline CLI answer is byte-identical to the
  peel-side one (pinned by `TestDocdataMatchesLive`). Fully offline — reads only
  the embedded docdata, never contacts a master, and works on a peel-only box
  with no config. Bare `zester doc` lists documented modules grouped by family;
  `zester doc <module>` prints the full parameter/effects/examples/notes render;
  an unknown module reports a clear error with nearest-match suggestions (edit
  distance ≤ 2); `--json` emits the structured `ModuleInfo`. Cobra shell
  completion now suggests documented module names for both `zester doc <TAB>`
  and the exec form `zester '<target>' <TAB>` (sourced from `moduledoc.All()`).
  The CLI's streamlined-output set gains `sys.doc` so a peel-side `sys.doc`
  result prints as plain rendered text. Internally, `parseModuleArgs` now binds
  a bare positional to a self-documenting module's declared primary parameter
  looked up from the embedded docdata (replacing the hand-maintained per-module
  positional table); every previously hard-coded module resolves identically,
  and `file.managed`'s positional now binds to its canonical primary `name`
  (decode-identical to the former `path`, which is a registered alias, with the
  ID carrying the same value). Modules absent from docdata keep the legacy
  fallback (mixed-fleet honesty).

- **Self-documenting Starlark custom modules.** The `.star` module loader now
  captures each module's documentation at load time — the function docstring
  (Google-style summary/description + `Args:` section, via the pinned
  `go.starlark.net` `Function.Doc()`), the source location (`Function.Position()`),
  and an optional per-function `<fn>_params` or module-global `PARAMS` dict — and
  registers a `modschema.Spec` so Starlark modules appear in `sys.doc` / `zester
  doc` through the state Registry's `Describe` exactly like built-in Go modules.
  A module is registered with `OpenParams` (accepts arbitrary keys) unless it
  declares a `PARAMS`/`<fn>_params` dict; a declared dict opts the module into
  unknown-key validation, which surfaces unknown config keys through the peel's
  configured decode policy (`LoaderConfig.DecodeOptions`) while leaving the
  requisite/attribute/compiler directives and `name` reserved. Documentation is
  re-captured on hot-reload. Authoring convention documented in the hand-owned
  `guides/modules/starlark.mdx`.

- **Generated JSON Schema artifact for module parameters**
  (`website/public/schema/zester-modules.schema.json`, JSON Schema draft
  2020-12) — machine-readable parameter schemas for every state module that
  has a migrated self-documenting schema (`file.managed`, `file.directory`,
  `file.absent`, `file.append`, `file.copy`, `file.recurse`, `file.symlink`,
  `file.touch`, `file.line`, `file.replace`, `file.comment`, `file.uncomment`,
  `file.keyvalue`, `file.blockreplace`, `pkg.installed`, `pkg.latest`,
  `pkg.purged`, `pkg.removed`, `service.running`, `service.dead`, and
  `user.present`) — `service.running`/`service.dead` contribute the shared
  `TriState` semantic-type `$def`, `file.managed` the `TemplateFlag` and
  `FileMode` `$defs`, `user.present` the `GroupRef` and `StringList` `$defs`,
  `file.append` is the second module to contribute to the `StringList` `$def`,
  `file.keyvalue` contributes the `StringMap` `$def` (its `key_values` and
  `entries` parameters — the type's first migrated consumer), and
  `file.directory`/`file.recurse` are further `FileMode` `$def` contributors
  (`file.directory`'s `mode` with a `dir_mode` fallback alias; `file.recurse`'s
  DECLARED-ONLY `dir_mode` and lazy `file_mode`); a module without a schema
  is left unconstrained, so the artifact
  grows automatically as more modules migrate, never breaking existing
  consumers. Generated deterministically by the new `cmd/zester-docgen` tool
  from the live module registries.

- **New architecture page: [Self-Documenting Modules](/docs/architecture/self-documenting-modules).**
  A hand-owned overview, for contributors and operators, of the module-schema
  framework introduced above: how one compiled schema declaration per module
  (a tagged struct plus a `Doc` block) drives parsing, the strict unknown-
  parameter policy, `sys.doc`/`zester doc`, the generated reference pages, and
  the JSON Schema export from a single source of truth; the sealed six-type
  semantic vocabulary; permanent contract fixtures as behavioral regression
  guards across YAML/CLI/msgpack; and the concrete gates (the docgen
  freshness diff, the closed doc-coverage ratchet, `TestDocdataMatchesLive`,
  the package-boundary architecture test) that make documentation drift a
  build failure instead of a silent fact of life. Registered in the
  Architecture nav (`architecture/meta.json`).

- **New guide: Developing a Built-in Module** (`guides/modules/developing`) —
  the step-by-step howto for adding a state module with a self-documenting
  schema: the proto struct and `zester` tag grammar, semantic types, the
  drift-corrected `Doc`, builder/lifecycle contract, registration and the
  coverage gates, contract fixtures across the three input universes, docgen
  regeneration, and the exec-module variant. Complements the operator-facing
  Starlark guide and the architecture page.

### Changed
- **Starlark hot-reload now UNREGISTERS removed modules.** A function removed
  from a reloaded `.star` file — and every module of a deleted `.star` file or
  a removed `_modules/` directory — is unregistered from the state registry on
  the next load pass, so it is neither callable nor documented (previously the
  stale builder and docs survived until a peel restart). When another loaded
  file still provides the same module name (a deleted formula override with a
  surviving global definition), that surviving file is re-executed so ITS
  builder becomes live again. New `state.Registry.Unregister` seam backs this;
  a conformance test pins that built-ins are never unregistered.
- **A `.star` module can no longer shadow a built-in module name.** The
  Starlark loader refuses to register a module whose name is already held by a
  non-Starlark registration (e.g. a `pkg.star` defining `installed` colliding
  with the built-in `pkg.installed`) and logs an error naming the file —
  previously the `.star` silently hijacked the built-in fleet-wide with no way
  to restore it until restart. Starlark-over-Starlark overrides (formula
  overrides global, hot-reload) are unaffected.

- **A typo'd state-module parameter now FAILS the build instead of being silently
  ignored (`strict_params`, default on).** The peel gains a `strict_params` knob
  (peel.yaml `strict_params:` / `--strict-params`, default **true**). With every
  built-in state module now decoding through a compiled schema, an unrecognized
  parameter on a migrated module is a hard error carrying the module name, the
  offending key, and a did-you-mean suggestion (edit distance ≤ 2) — for example
  `pkg.removed` with `nmae:` fails with `unknown parameter "nmae" (did you mean
  "name"?)` rather than applying the state with a defaulted name. This is a
  behavior change from the previous warn-and-continue default; set
  `strict_params: false` to restore the old behavior (a warning is logged through
  the peel logger and the state still builds). The policy is enforced uniformly
  across every decode path — compiled highstates, ad-hoc `zester '<target>'
  <module> …` runs, `module.run` forwarding, reactor `dispatch.module` actions,
  and Starlark custom modules that declared `PARAMS` — and never false-positives
  on reserved directives (`require`/`watch`/`onlyif`/`order`/…), the exec-layer
  `test=True` flag, `module.run`'s `name` selector, or the Salt universal
  state-identifier idiom — an explicit `name:` on a module that declares no
  `name` parameter of its own (`test.nop: - name: anchor` is valid Salt even
  though `test.nop` has no `name` field) is excused fleet-wide, not just for
  `module.run`. A module that DOES declare its own `name` parameter or alias
  (e.g. `file.managed`) still decodes an explicit `name:` into that field as
  normal — the reserved-key excuse only ever applies to a key no declared
  parameter claimed. The compiler's `names:` expansion is schema-aware: each
  expanded name is injected into the module's PRIMARY parameter's canonical key
  when it has one (`cmd.run`'s primary is `command`, not a bare `name`), or the
  historical literal `name` key otherwise (`module.run`, legacy/Starlark
  modules without a declared schema, and parameter-less modules like `test.*`
  — all now reserved-tolerant of "name", so this no longer trips a false
  strict failure). The injection never overwrites an EXPLICIT value already
  present at the primary key or one of its aliases — `names: [...]` alongside
  an explicit `command:` (or its `name:` alias) keeps that command for every
  expanded instance instead of clobbering it with the per-instance name.
  Reserved keys carried into a build (they are still present in the config map
  when the compiler builds a compiled state) are always excused; only genuine
  unknown parameters fail.
- **`cmd.run` now RUNS the `name:` command (Salt parity — BD-8).** `cmd.run`'s
  primary `command` parameter gains the `name` alias, so the Salt idiom
  `cmd.run: - name: apt-get update` now executes `apt-get update` exactly as Salt
  does. Previously Zester read only `command` and silently ran the STATE ID
  instead — quietly wrong, and a latent bug in `zester-migrate`'s output (it
  passes Salt's `name:` through unchanged). This changes what executes for that
  idiom, from quietly-wrong to Salt-correct. Precedence is command-beats-name (a
  declared `command` wins over `name`), and an empty-string `command` falls
  through to `name` before the state-ID fallback. Pinned by permanent contract
  fixtures across YAML/CLI/msgpack.
- **`cmd.run` and `service.enabled` migrated — the doc-coverage ratchet reaches
  ZERO and the gate is CLOSED.** With these two, EVERY built-in state module now
  decodes through a single compiled schema (`modschema.Spec`) plus registered
  documentation metadata; there is no longer an `unmigratedAllowlist` (it was
  deleted) and the coverage conformance test is in its final form —
  `TestDocCoverage_EveryModuleHasSpec` asserts every registered module carries a
  Spec with no exemptions, and a Spec-less registration fails the build. The dead
  `providerBuild` legacy adapter (its last two callers were these modules) was
  removed. Highlights:
  - `cmd.run`: `command` is the primary (defaults to the state ID) and now
    accepts the `name` alias (see the BD-8 entry above — the Salt idiom
    `cmd.run: - name: <command>` runs the named command);
    `args` is a `paramtypes.StringList` and `env` a `paramtypes.StringMap`; `cwd`
    and `creates` are plain strings. The require-file-provider-when-`creates` rule
    stays in the builder tail (cross-field module logic, not schema). Check/Apply/
    Revert — the `creates` guard gating both phases, the shell-vs-direct execution
    split on `args`, the captured `command`/`stdout`/`stderr`/`exitcode` details,
    and the non-revertible Revert — are unchanged. `cmd.go` → `cmd_run.go`.
  - `service.enabled`: `name` is the sole parameter (primary, defaults to the
    state ID); Check/Apply/Revert (including the already-enabled Apply no-op that
    does not arm the revert memo, and the standalone-revert clean no-op) are
    unchanged. `service.go` → `service_enabled.go`.
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack) except for the flagged behavioral differences below. The
  reference pages (`cmd-run.mdx`, `service-enabled.mdx`) are now generated from
  the registered schema + documentation metadata rather than hand-maintained —
  all prior content is preserved (parameter tables, the `creates` idempotency
  mechanism, the shell-vs-direct `sh -c` behavior, the returned-details table now
  carried as a Note, `env` merged with the process environment, every example,
  and `cmd.run`'s dual-surface note that it is also an execution module reachable
  from templates via `salt['cmd.run']`) and reorganized under the shared anatomy,
  drift-corrected against the live code (notably `service.enabled`'s Apply/Revert
  no-op semantics, which the hand page omitted). The permanent differential
  contracts at `pkg/state/modules/testdata/contract/{cmd.run,service.enabled}.yaml`
  guard the decode behavior across all three universes. The doc-coverage ratchet
  shrinks by 2 (2 → 0 unmigrated modules — the ratchet is now empty).

  Alongside the gate-close, several documentation-surface fixes landed: the
  `sys.doc` unified index is now pinned to cover every registered state module by
  a permanent peel test (`TestPeelDocSourceCoversAllStateModules`) rather than an
  empirical observation; zero-parameter generated pages (`test.ping`, `test.nop`,
  `module.run`) now render the auto requisites boilerplate with an
  "no parameters of its own" note instead of silently omitting the Parameters
  section; the N:1 page-group renderer no longer silently falls back to
  per-member rendering on a shared-parameter mismatch — distinct-parameter groups
  (`host`, `ssh-auth`, `test-helpers`) now opt in explicitly via a group-table
  flag, so a genuinely mismatched shared-proto group fails generation loudly;
  `module.run`'s two Salt-divergence facts moved from `Doc.Divergences`
  (BD-IDs only, per convention) into Notes; and `guides/modules/index.mdx`'s
  Source column was corrected for every renamed/split module file.

  <!-- BD-5 -->
  **Behavioral difference (BD-5, approved 2026-07-13 under the maintainer standing
  proceed-without-sign-off grant).** `cmd.run`'s `args` (a `paramtypes.StringList`)
  and `env` (a `paramtypes.StringMap`) now surface the values the legacy parsers
  silently dropped: a scalar `args` list element is rendered to its string form
  (the legacy element-wise `.(string)` assertion dropped any non-string element),
  a nested `args` element is rejected with a typed error (was dropped), a
  bare-string `args` value decodes as a single-element list — which, downstream,
  switches execution to the direct (non-shell) path — where the legacy
  `config["args"].([]any)` assertion failed entirely and left the command on the
  shell path, and a composite `env` value is rejected with a typed error where the
  legacy `fmt.Sprintf("%v", v)` sprint'd it into Go syntax (a scalar `env` value
  stays parity — both render it to a string). Pinned per-param by the
  `args-scalar-sprint-*`, `args-nested-rejected-*`, `args-bare-string-*`, and
  `env-composite-value-rejected-*` contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** The migrated string
  parameters are now compiled plain strings: a *numeric* `cmd.run` `command`,
  `cwd`, or `creates`, or a numeric `service.enabled` `name`, is coerced to its
  string form (was a silent drop — for the primary `command`/`name`, a silent fall
  back to the state ID), and a *composite* value (a list/map) is rejected with a
  typed error rather than silently zeroing. The CLI already delivered a numeric
  token as a string (PARITY). Pinned per-param by the `numeric-command-coerced-*`
  / `composite-command-rejected-*`, `numeric-cwd-coerced-*` /
  `composite-cwd-rejected-*`, `numeric-creates-coerced-*` /
  `composite-creates-rejected-*` (cmd.run) and `numeric-name-coerced-*` /
  `composite-name-rejected-*` (service.enabled) contract fixtures.

- **The final `test.*` helpers and `module.run` migrated to the self-documenting
  module-schema framework — the doc-coverage ratchet reaches 2.** Every built-in
  state module except `cmd.run` and `service.enabled` now decodes through a single
  compiled schema (`modschema.Spec`) plus registered documentation metadata,
  replacing the hand-written `config[...].(type)` extractions. The multi-module
  `test_extra.go` was split into per-module files (`test_nop.go`,
  `test_fail_without_changes.go`, `test_succeed_with_changes.go`,
  `test_configurable_test_state.go`) alongside the existing `test_ping.go`,
  matching the per-module file convention so each generated **Source** line
  resolves. The `Registration.BuildPlain` shape gained the decode policy
  (`func(modschema.DecodeOptions) state.Builder`) so the provider-less test
  helpers thread reserved keys + the fleet unknown-key policy into their decode
  exactly like the provider-carrying modules. Highlights:
  - `test.ping` / `test.nop` declare **no parameters**; the decode still runs so
    reserved requisite/attribute keys are honored and unknown keys follow the
    fleet policy.
  - `test.fail_without_changes` / `test.succeed_with_changes` carry a single
    `comment` string (no default; the fail helper substitutes its built-in
    `failure without changes` message at apply time).
  - `test.configurable_test_state` carries eager `default=true` `result` and
    `changes` bools plus a `comment` string; Check/Apply are unchanged.
  - `module.run` is an **OpenParams passthrough**: it declares no fixed
    parameters (its schema has zero fields, marked `OpenParams` so unknown-key
    validation is skipped and it is documented as accepting arbitrary parameters
    forwarded to the target module). Its dynamic `name:` / dotted-`<module.func>:`
    target resolution and the reserved-key filter (`state.ReservedKeySet()` plus
    its own local `name`) are unchanged.
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack) except for the flagged behavioral differences below. The
  reference pages (`test-ping.mdx`, `test-helpers.mdx` — the N:1 group covering
  the four helpers, `module-run.mdx`) are now generated from the registered
  schema + documentation metadata rather than hand-maintained — all prior content
  is preserved (parameter tables, Check/Apply/Revert behavior, every example, the
  `onfail`/`onchanges` chain snippets now carried as Notes, the unimplemented
  Salt `test.show_notification`/`test.mod_watch` note, and module.run's two-form
  usage + its Divergences-from-Salt facts: state-registry targets only, and
  idempotent-as-its-target unlike Salt's always-changes `module.run`) and
  reorganized under the shared anatomy, drift-corrected against the live code. The
  permanent differential contracts at `pkg/state/modules/testdata/contract/{test.
  fail_without_changes,test.succeed_with_changes,test.configurable_test_state}.yaml`
  guard the decode behavior across all three universes (`test.ping`/`test.nop`
  declare no parameters, so they have no contract fixture). The doc-coverage
  ratchet shrinks by 6 (8 → 2 unmigrated modules — only `cmd.run` and
  `service.enabled` remain, migrated by their own closeout).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** String-form values that
  the legacy `.(bool)` assertion dropped are now coerced, origin-independently (a
  CLI `key=value` and a YAML-quoted value alike): `test.configurable_test_state`'s
  `result`/`changes` given as a truthy/falsy string (`"true"`, `"yes"`, `"on"`,
  `"false"`, `"no"`, `"off"`) is honored where the legacy assertion silently kept
  the default `true`. Pinned by the `{result,changes}-falsy-string-{cli,yaml}`
  contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** Each migrated module's
  string parameters are now compiled plain strings: a *numeric* `comment` on
  `test.fail_without_changes`, `test.succeed_with_changes`, or
  `test.configurable_test_state` is coerced to its string form (was a silent drop
  to `""`), and a *composite* `comment` (a list/map) is rejected with a typed
  error. `test.configurable_test_state`'s `result`/`changes` bools likewise reject
  a composite or a float (a float is not a boolean) with a typed error rather than
  silently keeping the default. The CLI already delivered a numeric comment as a
  string (PARITY). Pinned by the `comment-numeric-coerced-*` /
  `comment-composite-rejected-*` and `{result,changes}-float-rejected-yaml` /
  `-composite-rejected-yaml` contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** The `result` and
  `changes` bools of `test.configurable_test_state` now accept the integers `1`
  and `0` (`1` → true, `0` → false) and reject any other integer with a typed
  error, per the approved §2.3 coercion table and the §11 SCOPE ruling that BD-7
  covers ALL boolean-typed parameters (each pinned per-param). The legacy `.(bool)`
  assertion dropped an integer entirely (a silent fall to the default `true`).
  Pinned by the `{result,changes}-int-{one,zero}-{yaml,msgpack}` and
  `-invalid-int-{yaml,msgpack}` contract fixtures.

- **`pkgrepo.managed` and `user.absent` migrated to the self-documenting
  module-schema framework.** Each constructor now decodes through a single
  compiled schema (`modschema.Spec`) plus registered documentation metadata,
  replacing the hand-written `config[...].(type)` extractions. The single-module
  `pkgrepo.go` was renamed to `pkgrepo_managed.go` and `UserAbsent` was split out
  of `user.go` into `user_absent.go` (leaving `user.go` as the shared slice
  helpers `containsString`/`stringSliceEqual` used across the user/group/host
  modules), matching the per-module file convention. Highlights:
  - `pkgrepo.managed` is a **parameter-decode-only** migration: Check/Apply/Revert
    and the DEFERRED in-place signing-key-rotation detection (Check is
    presence-only — a key rotated at the same URL is not re-detected; delete the
    keyring file to force a re-import) are unchanged. `name` is the primary
    (default state ID); `humanname` is a **lazy DERIVED default** — the decoder
    never materializes it and the builder tail assigns it from `name`, reproducing
    the legacy `if HumanName == "" { HumanName = RepoName }`; `baseurl`/`ppa`/
    `file`/`key_url` are plain strings; `enabled`/`gpgcheck`/`refresh` carry an
    eager `default=true`. Nothing is `sensitive` (a signing-KEY URL points at a
    PUBLIC key — a per-module sensitivity pass).
  - `user.absent` is an all-primitives migration: `name` primary (default state
    ID); `purge`/`force` plain bools. Nothing is `sensitive`. `force` remains
    accepted for Salt compatibility but is not yet wired into the execution layer
    (documented, no effect).
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack) except for the flagged behavioral differences below. The
  reference pages (`pkgrepo-managed.mdx`, `user-absent.mdx`) are now generated
  from the registered schema + documentation metadata rather than hand-maintained
  — all prior content is preserved (parameter tables, Check/Apply/Revert behavior,
  every example, the rendered `.list`/`.repo` file-format blocks, and pkgrepo's
  Divergences-from-Salt facts: `baseurl` holding the full `deb` line, the
  deprecated `apt-key add`, the unsupported `disabled`/`mirrorlist`/
  `gpgautoimport`/`comps`/`architectures` parameters, and the presence-only
  keyring-convergence caveat) and reorganized under the shared anatomy,
  drift-corrected against the live code. The permanent differential contracts at
  `pkg/state/modules/testdata/contract/{pkgrepo.managed,user.absent}.yaml` guard
  the decode behavior across all three universes. The doc-coverage ratchet shrinks
  by 2 (10 → 8 unmigrated modules).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** String-form values that
  the legacy `.(bool)` assertion dropped are now coerced, origin-independently (a
  CLI `key=value` and a YAML-quoted value alike): a `pkgrepo.managed`
  `enabled`/`gpgcheck`/`refresh` or a `user.absent` `purge`/`force` given as a
  truthy/falsy string (`"false"`, `"no"`, `"yes"`, `"on"`) is honored (the three
  pkgrepo bools were silently dropped to their default `true`, the two user.absent
  bools to `false`). Pinned by the `{enabled,gpgcheck,refresh}-falsy-string-{cli,
  yaml}` and `{purge,force}-truthy-string-{cli,yaml}` contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with every prior
  migration, each module's primary `name` parameter is now a compiled plain
  string: a *non-string* `name` (for example `name: 123`) is coerced to its string
  form (was a silent fallback to the state ID), and a *composite* `name` (a
  list/map) is rejected with a typed error. The same acceptance/rejection class
  covers `pkgrepo.managed`'s remaining string params — a numeric
  `humanname`/`baseurl`/`ppa`/`file`/`key_url` is coerced to its string form and a
  composite value for any of them is rejected — instead of the legacy silent drop.
  The CLI already delivered a numeric name as a string (PARITY). Pinned by the
  `numeric-name-coerced-*` / `composite-name-rejected-*` (both modules) and the
  `{humanname,baseurl,ppa,file,key_url}-numeric-coerced-*` / `-composite-rejected-*`
  contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** The
  `enabled`/`gpgcheck`/`refresh` bools of `pkgrepo.managed` and the `purge`/`force`
  bools of `user.absent` now accept the integers `1` and `0` (`1` → true, `0` →
  false) and reject any other integer with a typed error, per the approved §2.3
  coercion table and the §11 SCOPE ruling that BD-7 covers ALL boolean-typed
  parameters (each pinned per-param). The legacy `.(bool)` assertion dropped an
  integer entirely (a silent fall to the pkgrepo default `true` / the user.absent
  `false`). Pinned by the `{enabled,gpgcheck,refresh,purge,force}-int-{one,zero}-
  {yaml,msgpack}` and `-invalid-int-{yaml,msgpack}` contract fixtures.

- **`git.cloned`, `git.latest`, `pip.installed`, `archive.extracted`,
  `locale.present`, and `timezone.system` migrated to the self-documenting
  module-schema framework** (the tooling wave). Each constructor now decodes
  through a single compiled schema (`modschema.Spec`) plus registered
  documentation metadata, replacing the hand-written `config[...].(type)`
  extractions. The legacy `git.go` was split into `git_cloned.go` (the
  `GitCloned` module) plus `git.go` kept as the shared rev-comparison helpers
  (`isHexRevPrefix`/`isFullHexSHA`/`resolveRevCommit`/`revAtHead`) used by both
  `git.cloned` and `git.latest`; the single-module `mount.go`-style renames
  (`locale.go` → `locale_present.go`, `timezone.go` → `timezone_system.go`,
  `pip.go` → `pip_installed.go`, `archive.go` → `archive_extracted.go`) match
  the per-module file convention. Every field in all six modules is a
  primitive (string/int/bool) — no semantic types are needed anywhere in this
  wave. Highlights:
  - `git.cloned`'s `name` (defaulting to the state ID) is the clone **PATH**;
    `git.latest`'s `name` (defaulting to the state ID) is instead the remote
    **URL**, with the clone path in its own `target` parameter. This DIFFERENT
    primary meaning between the two modules — the most common authoring
    mistake between them — is called out unmissably in both generated pages
    (a dedicated warning Note on each, plus the Description prose). `url`
    (`git.cloned`) and `target` (`git.latest`) are `required`; `depth`
    (`git.cloned`) is a plain int; `force` (both) is a plain bool.
    `git.latest`'s `name` is `primary`, not `required` — it legitimately
    falls back to the state ID — but the builder tail restores the legacy
    `git.latest: <id>: url (name) is required` error for the case where
    BOTH are empty (parity restoration, not a BD; pinned by the
    `TestGitLatestMissingURL` unit test, following the same builder-tail-
    logic-is-unit-tested-not-contract-tested convention as `ssh_auth`'s
    user-or-config rule and `sysctl.present`'s persist-needs-file-provider
    rule — the equivalent `git.cloned` gap doesn't exist because its
    primary is `name`/path and `url` is independently `required`). Their
    Doc's Check/Revert prose is **drift-corrected**: a pinned tag/symbolic
    rev converges via a local `git rev-parse --verify <rev>^{commit}`
    commit-id comparison, not merely a sha-prefix match as the old
    `git.latest` hand page's "Divergences from Salt" section claimed.
  - `pip.installed`'s `bin` carries an eager `default=pip3`, reproducing the
    legacy construction-time default.
  - `archive.extracted`'s `source` is `required`; `archive_format` carries an
    eager `default=auto`; `source_hash` is `TrimSpace`'d in the builder tail
    (a decoder never trims — same convention as `ssh_auth.present`'s `name`).
    Its Doc is **drift-corrected**: the hand page claimed Salt's `source_hash`
    verification "is not supported", but the module already records a
    `source_hash` marker after extraction and re-extracts on a mismatch — it
    is an opaque string comparison, never a byte-verified checksum, which the
    new Doc states plainly instead of omitting the feature. The hand page's
    "Divergences from Salt" facts — the unsupported `enforce_toplevel`/
    `options`/`user`/`group`/`clean`/`trim_output` parameter list, and the
    `tar`/`unzip` (plus `curl` or `wget` for remote sources) binary
    requirement — are carried forward verbatim (verified still accurate)
    into the new Doc's own "Divergences from Salt" Note, alongside the
    corrected `source_hash` fact above.
  - `locale.present` has a single parameter (`name`, the locale string) — the
    single-primary-param exemplar. Its Doc is **drift-corrected**: the hand
    page claimed Apply creates `/etc/locale.gen` when missing; the module in
    fact never creates that file on a system that lacks it.
  - `timezone.system`'s `utc` is a plain bool.
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack) except for the flagged behavioral differences below.
  The reference pages (`git-cloned.mdx`, `git-latest.mdx`, `pip-installed.mdx`,
  `archive-extracted.mdx`, `locale-present.mdx`, `timezone-system.mdx`) are now
  generated from the registered schema + documentation metadata rather than
  hand-maintained — all prior content is preserved (parameter tables,
  Check/Apply/Revert behavior, every example, the per-manager/per-format
  tables, the Divergences-from-Salt notes) and reorganized under the shared
  anatomy, drift-corrected against the live code where the hand pages had
  fallen behind. The permanent differential contracts at
  `pkg/state/modules/testdata/contract/{git.cloned,git.latest,pip.installed,
  archive.extracted,locale.present,timezone.system}.yaml` guard the decode
  behavior across all three universes. The doc-coverage ratchet shrinks by 6
  (16 → 10 unmigrated modules).

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer
  standing proceed-without-sign-off grant).** A `git.cloned` `depth` given as
  an INTEGER and delivered over msgpack is now applied. The legacy
  `config["depth"].(int)` assertion never matched a msgpack sized kind (msgpack
  v5 encodes a small int as a sized `int8`/`uint`), so a reactor-dispatched
  `depth: 1` silently fell to `0` (full clone) — the same reproduced sized-int
  class as `file.managed`'s BD-1. The uniform decoder honors the sized int.
  Pinned by the `depth-msgpack-sized-int` contract fixture. **Presented for
  sign-off in this PR** (keystone spec §11).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** String-form values
  that the legacy `.(bool)`/`.(int)` assertions dropped are now coerced,
  origin-independently (a CLI `key=value` and a YAML-quoted value alike): a
  `git.cloned`/`git.latest` `force`, an `archive.extracted` `makedirs`, and a
  `timezone.system` `utc` given as a truthy/falsy string (`"true"`, `"yes"`,
  `"false"`, `"no"`) is now honored (was silently dropped to its default
  `false`); a `git.cloned` `depth` given as a numeric string (`"1"` — the
  CLI's ONLY delivery form for an int) is now parsed base-10 (was silently
  dropped to `0` by the legacy `config["depth"].(int)` assertion, which never
  matches a string). Pinned by the `{force,makedirs,utc}-{truthy,falsy}-
  string-{cli,yaml}` and `depth-numeric-string-{cli,yaml}` contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with every prior
  migration, each module's primary parameter is now a compiled plain string: a
  *non-string* value (for example `name: 123`) is coerced to its string form
  (was a silent fallback to the state ID), and a *composite* value (a
  list/map) is rejected with a typed error. Further wrong-typed→typed-handling
  changes land under BD-6's approved acceptance/rejection class in this wave:
  - **`git.cloned` `url` and `git.latest` `target` — ERROR→ACCEPT flips on
    REQUIRED parameters (read deliberately).** A non-string value for either
    (for example `url: 123`) is now coerced to its string form and
    **accepted**, where the legacy `.(string)` assertion missed the non-string
    and raised the module's own "is required" error. `archive.extracted`'s
    `source` gets the identical flip.
  - **String coercion / composite rejection on the remaining string params.**
    A numeric `branch`/`rev` (both git modules), `version`/`requirements`/`bin`
    (`pip.installed`), `archive_format`/`if_missing`/`source_hash`
    (`archive.extracted`) is coerced to its string form, and a composite value
    for any of them is rejected with a typed error, instead of the legacy
    silent drop.
  - **`git.cloned` FLOAT `depth`.** A finite-integral float (`depth: 1.0`) is
    coerced to the integer, where the legacy `.(int)` assertion missed a
    float64 and left `Depth=0`.
  The CLI already delivered a numeric primary as a string (PARITY). Pinned by
  the `numeric-name-coerced-*`/`composite-name-rejected-*` (all six modules),
  the `{url,target,source}-numeric-accepted-*`/`-composite-rejected-*`, the
  `{branch,rev,version,requirements,bin,archive_format,if_missing,source_hash}
  -numeric-coerced-*`/`-composite-rejected-*`, and the `depth-float-*` contract
  fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** The boolean
  `force` (`git.cloned`/`git.latest`), `makedirs` (`archive.extracted`), and
  `utc` (`timezone.system`) parameters now accept the integers `1` and `0`
  (`1` → true, `0` → false) and reject any other integer with a typed error,
  per the approved §2.3 coercion table and the §11 SCOPE ruling that BD-7
  covers ALL boolean-typed parameters (each pinned per-param). The legacy
  `.(bool)` assertion dropped an integer entirely (a silent fall to the
  default `false`). Pinned by the
  `{force,makedirs,utc}-int-{one,zero,invalid}-{yaml,msgpack}` contract
  fixtures.

- **`mount.mounted`, `sysctl.present`, `host.present`/`host.absent`, and
  `ssh_auth.present`/`ssh_auth.absent` migrated to the self-documenting
  module-schema framework** (the system wave). Each constructor now decodes
  through a single compiled schema (`modschema.Spec`) plus registered
  documentation metadata, replacing the hand-written `config[...].(type)`
  extractions. The multi-module `host.go`/`ssh_auth.go` were split into per-module
  files (`host_present.go`, `host_absent.go`, `ssh_auth_present.go`,
  `ssh_auth_absent.go`), with the shared line-managed-file helpers moved to
  `linemanaged.go`; the single-module `mount.go`/`sysctl.go` were renamed to
  `mount_mounted.go`/`sysctl_present.go` to match the per-module file convention.
  Highlights of the wave:
  - `mount.mounted` is a **parameter-decode-only** migration: Check/Apply/Revert
    and the deliberate blindness to the LIVE mount's device/fstype/options (the
    audit's open live-facet-normalization item) are unchanged. `device` is
    `required`; `fstype`/`opts` carry eager `default=ext4`/`default=defaults`;
    `dump`/`pass` are plain ints; `persist` is an eager `default=true` bool.
  - `sysctl.present`'s `value` is `required` and `persist` an eager `default=true`
    bool. The require-file-provider-when-`persist` rule stays in the builder tail —
    cross-field module logic, not schema.
  - `host.present`/`host.absent` are the **per-field alias exemplar**: the
    hosts-file path binds the `config` key with a `path` alias and an eager
    `default=/etc/hosts`, reproducing the legacy `config` > `path` > `/etc/hosts`
    precedence (the standalone `hostsPath` helper is gone). `host.present`'s `ip`
    is `required`.
  - `ssh_auth.present`/`ssh_auth.absent`: `enc` carries an eager `default=ssh-rsa`.
    The `name` primary is `TrimSpace`'d and the require-`user`-OR-`config`
    cross-field rule are enforced in the builder tail (a decoder never trims;
    cross-field validation is module logic, not schema). The key material is a
    PUBLIC key, so — per a per-module sensitivity pass — no parameter is
    `sensitive`.
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack) except for the flagged behavioral differences below. The
  reference pages (`mount-mounted.mdx`, `sysctl-present.mdx`, `host.mdx`,
  `ssh-auth.mdx`) are now generated from the registered schema + documentation
  metadata rather than hand-maintained — all prior content is preserved and
  reorganized under the shared anatomy, drift-corrected against the live code.
  `host` and `ssh-auth` are the first N:1 page groups whose members carry
  **distinct** parameter surfaces (e.g. `host.present` has `ip`, `host.absent`
  does not), so `zester-docgen` now renders each member's own `**Source**` line
  and Parameters table under a per-module banner (extending the shared-proto
  page-group renderer). The permanent differential contracts at
  `pkg/state/modules/testdata/contract/{mount.mounted,sysctl.present,host.present,host.absent,ssh_auth.present,ssh_auth.absent}.yaml`
  guard the decode behavior across all three universes. The doc-coverage ratchet
  shrinks by 6 (22 → 16 unmigrated modules).

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).**
  A `mount.mounted` `dump`/`pass` given as an INTEGER and delivered over msgpack is
  now applied. The legacy `config["dump"].(int)` / `config["pass"].(int)`
  assertions never matched a msgpack sized kind (msgpack v5 encodes a small int as
  a sized `int8`/`uint`), so a reactor-dispatched `pass: 2` silently fell to `0` —
  the same reproduced sized-int class as `file.managed`'s BD-1. The uniform decoder
  honors the sized int. Pinned by the `dump-int-msgpack` and `pass-int-msgpack`
  contract fixtures. **Presented for sign-off in this PR** (keystone spec §11).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** String-form values that
  the legacy `.(int)`/`.(bool)` assertions dropped are now coerced, origin-
  independently (a CLI `key=value` and a YAML-quoted value alike): a `mount.mounted`
  `dump`/`pass` given as a numeric string (`"1"`/`"2"`) is parsed base-10, and a
  `mount.mounted`/`sysctl.present` `persist` given as a truthy/falsy string
  (`"false"`, `"no"`) is honored (was silently dropped to the default `true`).
  Pinned by the `dump-string-cli`, `dump-numeric-string-yaml`, `pass-string-cli`,
  `pass-numeric-string-yaml`, and `persist-falsy-string-{cli,yaml}` (mount + sysctl)
  contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with every prior
  migration, each module's primary `name` parameter is now a compiled plain
  string: a *non-string* `name` (for example `name: 123`) is coerced to its string
  form (was a silent fallback to the state ID), and a *composite* `name` (a
  list/map) is rejected with a typed error. Further wrong-typed→typed-handling
  changes land under BD-6's approved acceptance/rejection class in this wave:
  - **`mount.mounted` `device`, `sysctl.present` `value`, and `host.present` `ip` —
    ERROR→ACCEPT flips on REQUIRED parameters (read deliberately).** A non-string
    value for any of these required params (for example `ip: 123`) is now coerced
    to its string form and **accepted**, where the legacy `.(string)` assertion
    missed the non-string and raised the module's "X is required" error.
  - **String coercion / composite rejection on the remaining string params.** A
    numeric `fstype`/`opts` (mount), `config`/`path` (host), and
    `user`/`enc`/`comment`/`config` (ssh_auth) is coerced to its string form, and a
    composite value for any of them is rejected with a typed error, instead of the
    legacy silent drop.
  - **`mount.mounted` FLOAT `dump`/`pass` and composite `dump`/`pass`.** A
    finite-integral float (`dump: 1.0`) is coerced to the integer, and a composite
    `dump`/`pass` is rejected with a typed error, where the legacy `.(int)`
    assertion left `0`. (BD-2 remains strictly string-coercion; a non-string
    wrong-typed value such as a float is BD-6's class.)
  The CLI already delivered a numeric name as a string (PARITY). Pinned by the
  `numeric-name-coerced-*` / `composite-name-rejected-*` (all six modules), the
  `{device,value,ip}-numeric-accepted-*` / `{device,value,ip}-composite-rejected-*`,
  the `{fstype,opts,config,user,enc,comment}-numeric-coerced-*` /
  `-composite-rejected-*`, and the `{dump,pass}-float-*` / `-composite-rejected-*`
  contract fixtures. Because coercion/rejection is origin-independent, `host`'s
  hosts-file path is pinned through BOTH its canonical `config` key and its `path`
  ALIAS source — the `path-alias-numeric-coerced-*` (yaml + msgpack) and
  `path-alias-composite-rejected-*` fixtures (host.present and host.absent) prove a
  numeric/composite value delivered via the alias coerces/rejects exactly as
  through `config`.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** The `persist` boolean of
  BOTH `mount.mounted` and `sysctl.present` now accepts the integers `1` and `0`
  (`1` → true, `0` → false) and rejects any other integer with a typed error, per
  the approved §2.3 coercion table and the §11 SCOPE ruling that BD-7 covers ALL
  boolean-typed parameters (each pinned per-param). The legacy `.(bool)` assertion
  dropped an integer entirely (a silent fall to the default `true`). Pinned by the
  `persist-int-{one,zero,invalid}-{yaml,msgpack}` contract fixtures (mount +
  sysctl).

- **`cron.present`, `cron.absent`, `group.present`, and `group.absent` migrated
  to the self-documenting module-schema framework** (the cron/group wave). Each
  constructor now decodes through a single compiled schema (`modschema.Spec`)
  plus registered documentation metadata, replacing the hand-written
  `config[...].(type)` extractions (and, for the group modules, the legacy
  `parseAnyStringList` list parser). The legacy multi-module `cron.go`/`group.go`
  were split into per-module files (`cron_present.go`, `cron_absent.go`,
  `group_present.go`, `group_absent.go`). Highlights of the wave:
  - `cron.present`/`cron.absent`'s `command` is `required` (a missing/empty
    `command` fails at decode with a typed `MissingRequired` error, where legacy
    raised its own explicit "command is required" error — both reject, PARITY);
    `user` carries an eager `default=root`; and `cron.present`'s five schedule
    fields (`minute`/`hour`/`daymonth`/`month`/`dayweek`) carry eager
    `default=*`, reproducing the legacy construction-time defaults.
  - `group.present`'s `gid` is a **plain `int`** — group.present never resolves a
    group NAME, so (unlike `user.present`) it is NOT a `paramtypes.GroupRef` and
    there is no BD-4: a negative gid is accepted, not rejected. Its
    `members`/`addusers`/`delusers` are `paramtypes.StringList` and `system` is a
    plain bool. The Revert prose is **drift-corrected**: the hand page wrongly
    claimed membership changes are not reverted, but Revert diffs the current
    group against the memoized original and restores GID **and** membership.
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack) except for the flagged behavioral differences below. The
  reference pages (`cron-present.mdx`, `cron-absent.mdx`, `group-present.mdx`,
  `group-absent.mdx`) are now generated from the registered schema + documentation
  metadata rather than hand-maintained — all prior content is preserved
  (parameter tables, Check/Apply/Revert behavior, every example) and reorganized
  under the shared anatomy, drift-corrected against the live code.
  `group.present`'s `members`/`addusers`/`delusers` are further `StringList`
  `$def` contributors to the combined JSON Schema artifact. The permanent
  differential contracts at
  `pkg/state/modules/testdata/contract/cron.{present,absent}.yaml` and
  `.../group.{present,absent}.yaml` guard the decode behavior across all three
  universes. The doc-coverage ratchet shrinks by 4 (26 → 22 unmigrated modules).

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** A
  `group.present` `gid` given as an INTEGER and delivered over msgpack is now
  applied. The legacy `config["gid"].(int)` assertion never matched a msgpack
  sized kind (msgpack v5 encodes `999` as a `uint16`), so a reactor-dispatched
  `gid: 999` silently fell to `0` (auto-assign / not compared) — the same
  reproduced sized-int class as `file.managed`'s BD-1. The uniform decoder honors
  the sized int as a numeric GID. Pinned by the `gid-int-msgpack` contract
  fixture. **Presented for sign-off in this PR** (keystone spec §11).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A `group.present`
  string-form `gid` or `system` is now coerced instead of dropped: a CLI
  `gid=999` (the string `"999"`) is parsed base-10 to the numeric GID, and a CLI
  `system=true` (the string `"true"`) enables the system flag, where the legacy
  `.(int)`/`.(bool)` assertions dropped the string (GID `0` / system `false`).
  Origin-independent: a YAML-quoted `gid: "999"` and `system: "yes"` are honored
  the same way. Pinned by the `gid-string-cli`, `gid-numeric-string-yaml`,
  `system-truthy-string-cli`, and `system-truthy-string-yaml` contract fixtures.

  <!-- BD-3 -->
  **⚠️ Behavioral difference (BD-3, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant) — THIS CHANGES
  REAL SCHEDULES; READ BEFORE APPROVING.** A `cron.present` schedule value given
  as a YAML or msgpack INTEGER is now coerced to its string form. This applies to
  ALL FIVE schedule fields (`minute`/`hour`/`daymonth`/`month`/`dayweek`); the
  canonical case: `minute: 5`.
  - **OLD behavior (the every-minute bug):** the legacy
    `config["minute"].(string)` assertion did not match a non-string, so
    `minute: 5` fell to the empty string `""`, which the constructor then
    defaulted to `"*"`. The job ran **every minute** — never at minute 5.
  - **NEW behavior:** the uniform string decoder renders the integer via
    `fmt.Sprint`, so `minute: 5` decodes to `"5"` and the job runs at **minute 5**,
    as written.
  This alters the actual cron schedule of any state that passed an *unquoted
  integer* schedule field through a reactor/msgpack path. Operators who relied on
  the old accidental every-minute behavior (unlikely, but possible) must quote the
  value or adjust. The CLI already delivered `"5"` as a string, so it is
  unaffected (PARITY). This integer-coercion arm is the **ONLY** change on the
  BD-3 sign-off sheet — the composite-schedule REJECTION (a
  wrong-typed→typed-error) is the APPROVED BD-6's class and is documented under
  BD-6 below, not here. Pinned per-field by the
  `{minute,hour,daymonth,month,dayweek}-int-{yaml,msgpack}` contract fixtures
  (`minute-int-cli` pins the CLI parity). **Presented for sign-off in this PR**
  (keystone spec §11).

  <!-- BD-5 -->
  **Behavioral difference (BD-5, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).**
  `group.present`'s `members`/`addusers`/`delusers` are now `paramtypes.StringList`,
  which handles the three arms the legacy `parseAnyStringList` silently dropped:
  a **scalar list element** is rendered to a string (`members: [alice, 1000]` →
  `[alice, "1000"]`, where legacy dropped the `1000`); a **nested** list/map
  element is rejected with a typed error (was silently dropped); and a
  **bare-string** value decodes as a single-element list that ACTIVATES membership
  management (`members: alice` → `[alice]`, where legacy ignored a non-list value
  entirely, managing no members — this is also what makes a CLI `members=alice`
  work). Pinned per-param by the `members-*`, `addusers-*`, and `delusers-*`
  (scalar-sprint / nested-rejected / bare-string) contract fixtures. **Presented
  for sign-off in this PR** (keystone spec §11).

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with every prior
  migration, each module's primary `name` parameter is now a compiled plain
  string: a *non-string* `name` (for example `name: 123` in YAML/msgpack) is
  coerced to its string form (was a silent fallback to the state ID), and a
  *composite* `name` (a list/map) is rejected with a typed error. Three further
  wrong-typed→typed-handling changes land under BD-6's approved acceptance/
  rejection class in this wave:
  - **`cron.present`/`cron.absent` numeric `command` — an ERROR→ACCEPT flip on a
    REQUIRED parameter (read deliberately).** A non-string `command` (for example
    `command: 123` in YAML, or over msgpack where the sized-int arm applies) is
    now coerced to its string form `"123"` and **accepted**, where the legacy
    `config["command"].(string)` assertion missed the non-string and raised
    `command is required`. This is an accept-direction change on a *required*
    parameter. A non-string `user` is likewise coerced to its string form instead
    of the legacy silent fallback to `root`.
  - **`group.present` composite `gid` and FLOAT `gid`.** A *composite* `gid` (a
    list/map) is rejected with a typed `WrongType` error instead of the legacy
    silent `0`; and a finite-integral FLOAT `gid` (for example `gid: 999.0`) is
    now coerced to the integer `999`, where the legacy `config["gid"].(int)`
    assertion missed a `float64` and left `GID` at `0`. (BD-2 remains strictly
    string-coercion; a non-string wrong-typed value such as a float is BD-6's
    class.)
  - **`cron.present` composite schedule value (refiled from BD-3).** A *composite*
    schedule value (a list/map for `minute`/`hour`/…) is rejected up front with a
    typed error instead of silently falling to `"*"`. This wrong-typed→typed-error
    rejection belongs to the APPROVED BD-6 class, NOT the BD-3 sign-off sheet
    (which carries only the schedule integer-coercion arm).
  The CLI already delivered a numeric name/command as a string (PARITY). Pinned by
  the `numeric-name-coerced-*` and `composite-name-rejected-*` (all four modules),
  `command-numeric-accepted-{yaml,msgpack}` and
  `user-numeric-accepted-{yaml,msgpack}` (cron.present + cron.absent),
  `gid-composite-rejected-*` and `gid-float-{yaml,msgpack}` (group.present), and
  `minute-composite-rejected-{yaml,msgpack}` (cron.present) contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** `group.present`'s
  `system` boolean now accepts the integers `1` and `0` (`1` → true, `0` → false)
  and rejects any other integer with a typed error, per the approved §2.3
  coercion table and the §11 SCOPE ruling that BD-7 covers ALL boolean-typed
  parameters (each pinned per-param). The legacy `.(bool)` assertion dropped an
  integer entirely (a silent false). Pinned by the
  `system-int-{one,zero,invalid}-{yaml,msgpack,cli}` contract fixtures across all
  three universes.

- **`file.directory` and `file.recurse` migrated to the self-documenting
  module-schema framework** (the declared-facet wave). Each constructor now
  decodes through a single compiled schema (`modschema.Spec`) plus registered
  documentation metadata, replacing the hand-written `config[...].(type)`
  extractions and the shared `modeConfigToString` helper (now removed — its only
  two callers were these modules). Both modules move their mode parameters onto
  `paramtypes.FileMode`:
  - `file.directory`'s `mode` is a `paramtypes.FileMode` declared
    `lazy,default=0755` with `dir_mode` as a **fallback alias** — the mode is
    resolved at use time via `Mode.Resolve(0755)`, and the legacy
    mode-then-dir_mode-then-0755 chain is reproduced exactly by the alias source
    precedence (name `mode` wins over the `dir_mode` alias; an empty-string
    `mode` falls THROUGH to `dir_mode`, never to the state ID — §2.1 source
    fall-through, PARITY). `makedirs` stays a plain bool whose documented no-op
    behavior (MkdirAll is unconditional) is now stated honestly on the generated
    page. Check/Apply/Revert are unchanged except for reading the typed mode
    (`Mode.Resolve`); the chmod-before-chown Apply order is preserved verbatim.
  - `file.recurse`'s `file_mode` is a `paramtypes.FileMode` declared
    `lazy,default=0644` (always enforced), and `dir_mode` is a
    `paramtypes.FileMode` declared `lazy,default=0755` whose **`Declared()` bit
    is load-bearing**: it is a DECLARED-ONLY facet — undeclared, existing
    directory modes are neither compared (Check) nor rewritten (Apply), and the
    0755 default is used only as the MkdirAll creation perm. The module gates on
    `r.DirMode.Declared()` in both phases, reproducing the legacy
    `DirMode != ""` guard. `source` stays a plain string whose emptiness is a
    run-time "source is required" error (legacy parity, NOT a decode-time
    `required`); `clean`/`makedirs` are plain bools. Behavior is unchanged for
    every realistic YAML input.
  Each module moved to its own file already (`file_directory.go`,
  `file_recurse.go`); the shared file-module helpers stay in `file.go`. Their
  reference pages (`file-directory.mdx`, `file-recurse.mdx`) are now generated
  from the registered schema + documentation metadata rather than
  hand-maintained: all prior content is preserved (parameter tables, the
  Check/Apply/Revert behavior, the deploy/private-directory examples, the
  declared-only-dir_mode and clean-removes-regular-files-only caveats) and
  reorganized under the shared
  Source/Parameters/Parameter-Types/Effects/Examples/Notes/Divergences/See-Also
  anatomy, **drift-corrected** against the live code (notably: `file.directory`'s
  `makedirs` is documented as inert rather than functional; `file.recurse`'s
  declared-only `dir_mode` semantics are stated in both phases). Both modules are
  further `FileMode` `$def` contributors to the combined JSON Schema artifact.
  The permanent differential contracts at
  `pkg/state/modules/testdata/contract/file.directory.yaml` and
  `.../file.recurse.yaml` guard the decode behavior across all three universes
  (YAML, CLI, msgpack). The doc-coverage ratchet shrinks by 2 (28 → 26
  unmigrated modules).

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** A mode given as
  an octal INTEGER and delivered over msgpack is now applied, for
  `file.directory`'s `mode` (and its `dir_mode` alias) and `file.recurse`'s
  `file_mode`/`dir_mode`. The legacy `modeConfigToString` switch handled only
  `int`/`int64`/`float64`, so a reactor-dispatched `mode: 0700` (encoded by
  msgpack v5 as a sized `uint16`) fell through to the empty string and silently
  applied the module's default — the same reproduced `0755→0644`-class bug that
  motivated `file.managed`'s BD-1. `paramtypes.FileMode` interprets the octal
  value from every integer kind, so the requested mode now survives a reactor
  dispatch; the setuid/setgid/sticky bits survive too. For `file.recurse`'s
  `dir_mode` the fix additionally restores the DECLARED-ONLY facet: over msgpack
  the legacy value fell through to `""`, which not only lost the mode but also
  DISABLED the facet — the new decoder keeps `dir_mode` declared. Pinned by the
  `mode-0700-msgpack`/`mode-setgid-msgpack`/`dir-mode-alias-0700-msgpack`
  (file.directory) and `file-mode-0640-msgpack`/`dir-mode-0750-msgpack`
  (file.recurse) contract fixtures. **Presented for sign-off in this PR**
  (keystone spec §11).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A CLI `makedirs=true`
  string (and `file.recurse`'s `clean=true`) now applies, where the legacy
  `.(bool)` assertion dropped the string and left the flag false. The same rule
  honors a YAML-quoted boolean string. Pinned by the `makedirs-cli-truthy-string`
  (both modules) and `clean-cli-truthy-string` (file.recurse) contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** A wrong-typed value is
  now handled deterministically instead of a silent zero/fallback: a *non-string*
  `name`/`source` is coerced to its string form (was a fallback to the state ID /
  the empty string), and a *composite* value (a list/map) into `name`, or into a
  mode parameter, is rejected with a typed error. Mode VALIDATION also moves to
  DECODE time: a **float** mode (which the legacy `modeConfigToString` `%04o`-
  converted and applied) and an **invalid-octal string** mode (which the legacy
  path carried through and only rejected at apply) are both rejected up front by
  `paramtypes.FileMode`. Pinned by the `numeric-name-*`/`numeric-source-coerced-*`,
  `composite-name-rejected-*`, `composite-mode-rejected-*`/`composite-dir-mode-
  rejected-*`, `float-mode-rejected-*`/`float-file-mode-rejected-*`/
  `float-dir-mode-rejected-*`, and `invalid-octal-string-*-mode-rejected-*`
  contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** Every boolean-typed
  parameter — `file.directory`'s `makedirs`, and `file.recurse`'s `clean` AND
  `makedirs` — now accepts the integers `1` and `0` (`1` → true, `0` → false)
  and rejects any other integer with a typed error, per the approved §2.3
  coercion table and the §11 SCOPE ruling that BD-7 covers ALL boolean-typed
  parameters (each pinned per-param, not "same as the other"). The legacy
  `.(bool)` assertion dropped an integer entirely (a silent false). Pinned by the
  `makedirs-int-{one,zero,invalid}-*` (file.directory) and the
  `clean-int-{one,zero,invalid}-*` + `makedirs-int-{one,zero,invalid}-*`
  (file.recurse) contract fixtures across the YAML and msgpack universes (the CLI
  delivers a string, covered by BD-2).

- **`file.line`, `file.replace`, `file.comment`, `file.uncomment`,
  `file.keyvalue`, and `file.blockreplace` migrated to the self-documenting
  module-schema framework** (the file-surgery wave). Each constructor now
  decodes through a single compiled schema (`modschema.Spec`) plus registered
  documentation metadata, replacing the hand-written `config[...].(type)`
  extractions and the shared `fsxResolvePath`/`fsxToInt` helpers (now removed).
  Highlights of the wave:
  - `file.line`'s `mode` parameter is an ACTION ENUM (ensure/replace/insert/
    delete), NOT a permission mode — it is a plain string with an eager
    `default=ensure`, and the builder lowercases the resolved action to
    reproduce the legacy `strings.ToLower` normalization.
  - `file.replace`'s `pattern` is `required` (a missing/empty pattern fails at
    decode with a typed `MissingRequired` error, where legacy raised its own
    explicit "pattern is required" error — both reject, PARITY), and the
    `(?m)`-anchored regex compile stays a construction-time error in the builder
    tail; `count` is a plain int.
  - `file.comment`/`file.uncomment` are the **N:1 exemplar**: ONE `FileComment`
    proto backs TWO registered names, so there are two `modschema.Spec`s (one
    per name), each with its own documentation, compiling the same tagged
    parameter surface (`name`, `char` default `#`, required `regex`). Which
    behavior a built state performs is selected by an untagged runtime field set
    by the builder. Their single reference page (`file-comment.mdx`) is now a
    generated N:1 combined page — one shared Parameters section plus a per-name
    Effects/Examples/Notes/Divergences/See-Also section.
  - `file.keyvalue`'s `key_values` and `entries` are TWO separate
    `paramtypes.StringMap` parameters — NOT a single name-wins alias. The legacy
    constructor UNIONED both maps (`entries` winning a per-key collision, since it
    merged second), so a name-wins alias would have silently discarded `entries`
    whenever both were supplied; the union merge is reproduced in the module tail
    (both maps, then the single `key`/`value` injection last). The single
    `key`/`value` convenience form keeps its legacy module-local injection as a
    post-decode merge (`key` requires a present, non-nil `value`); `separator`
    defaults to `=`. Pinned by the `key-values-and-entries-union-*` and
    `key-values-entries-collision-entries-wins-*` contract fixtures (PARITY, not a
    BD).
  - `file.blockreplace` keeps its `name`-only primary (its legacy constructor
    never accepted a `path` alias, so one is deliberately NOT added — parity,
    not a new divergence), with eager `marker_start`/`marker_end` defaults.
  Behavior is unchanged for every realistic input across all three universes
  (YAML, CLI, msgpack). The reference pages (`file-line.mdx`, `file-replace.mdx`,
  `file-comment.mdx`, `file-keyvalue.mdx`, `file-blockreplace.mdx`) are now
  generated from the registered schema + documentation metadata rather than
  hand-maintained — all prior content is preserved (parameter tables,
  Check/Apply/Revert behavior, every example and note, including the "Divergences
  from Salt" material folded into Notes) and reorganized under the shared
  anatomy, drift-corrected against the live code. The permanent differential
  contracts at `pkg/state/modules/testdata/contract/file.{line,replace,comment,
  uncomment,keyvalue,blockreplace}.yaml` guard the decode behavior; the
  differential harness (`pkg/modschema/schematest`) gained a `StringMap` matcher
  (its first migrated consumer). The doc-coverage ratchet shrinks by 6
  (34 → 28 unmigrated modules).

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with every prior
  migration, each module's primary `name` parameter is now a compiled plain
  string: a *non-string* value (for example `name: 123` in YAML/msgpack) is
  coerced to its string form instead of silently falling back to the state ID,
  and a *composite* value (a list/map) is now rejected with a typed `wrong_type`
  error instead of being silently ignored. Pinned by the `numeric-name-*` and
  `composite-name-*` contract fixtures across all six modules. Additionally,
  `file.replace`'s `count` (now a compiled plain `int`) rejects a NON-integral
  float (`count: 2.9`) with a typed `value_invalid` error at decode time, where
  the legacy `fsxToInt` silently TRUNCATED it (`int(2.9)` = 2); an integral float
  still coerces, so only a non-integral value diverges (the same class as the
  `file.managed` float-mode fixture). Pinned by the `float-count-rejected-{yaml,
  msgpack}` contract fixtures. Additionally, EVERY non-primary string parameter
  that the legacy constructors read through a silent `.(string)` assertion now
  sprints a numeric scalar to its string form (the `pkg.installed` `version`
  precedent), pinned per-param across YAML and msgpack by
  `numeric-<param>-coerced-{yaml,msgpack}` fixtures: `file.line`'s
  `content`/`match`/`before`/`after`/`mode`, `file.replace`'s
  `pattern`/`repl`/`not_found_content`, `file.comment`/`file.uncomment`'s
  `regex`/`char`, `file.keyvalue`'s `separator`/`key`, and `file.blockreplace`'s
  `content`/`marker_start`/`marker_end`. For the REQUIRED params (`file.replace`'s
  `pattern`, `file.comment`/`file.uncomment`'s `regex`) and for `file.keyvalue`'s
  `key` this is a reject-to-accept flip — legacy zeroed the value and then hit the
  required/"no entries" check, where it now decodes to the coerced string.
  (`file.keyvalue`'s SCALAR `value` is NOT in this coercion set: the legacy
  constructor already sprint'd a scalar with `fmt.Sprintf`, so a numeric single
  `value` is parity. A COMPOSITE single `value` — a nested map/list with `key`
  set — IS a BD-6 rejection, though: the legacy `fmt.Sprintf("%v", …)` wrote
  Go-syntax garbage into the file, where the `value` string field now rejects it
  with a typed `wrong_type` error at decode; pinned by the
  `composite-single-value-rejected-{yaml,msgpack}` fixtures.)

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A CLI
  `<bool-param>=<truthy/falsy string>` is now honored on every boolean
  parameter (`file.replace`'s `append_if_not_found`/`prepend_if_not_found`,
  `file.blockreplace`'s `append_if_not_found`/`append_newline`), and a CLI
  numeric-string `count=2` is parsed for `file.replace`'s `count`, where the
  legacy `.(bool)` / `fsxToInt` paths silently dropped a string. Pinned by the
  `*-cli-truthy-string` and `count-cli-numeric-string` contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** Each boolean parameter
  accepts the INTEGERS 1 and 0 (1 = true, 0 = false, across every signed/unsigned
  integer kind, so a msgpack-delivered bool — which arrives as a sized kind such
  as `int8` — is honored) and rejects any other integer with a typed
  `value_invalid` error, where the legacy `.(bool)` assertion dropped an int
  entirely. EVERY boolean parameter's integer arm is pinned explicitly (not just
  a representative): `file.replace`'s `append_if_not_found`/`prepend_if_not_found`
  and `file.blockreplace`'s `append_if_not_found`/`append_newline`, each across
  YAML and msgpack by the `*-int-one-*`/`*-int-zero-*`/`*-int-invalid-*` contract
  fixtures, per the §11 SCOPE ruling that BD-7 covers ALL boolean-typed
  parameters.

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** `file.replace`'s
  `count` now honors a msgpack-delivered sized integer. msgpack v5 encodes a
  small integer into the smallest kind by magnitude (`count: 2` → an `int8`),
  and the legacy `fsxToInt` switch handled only `int`/`int64`/`float64` — so a
  reactor-dispatched `file.replace` with `count: 2` fell through to 0
  (replace-all) instead of limiting the replacement (the same sized-int class as
  the reproduced `file.managed` `0755 → 0644` bug). `paramtypes`-free primitive
  int coercion interprets every integer kind, so the requested count now survives
  a reactor dispatch. Pinned by the `count-msgpack-sized-int` contract fixture.
  **Presented for sign-off in this PR** (keystone spec §11).

  <!-- BD-5 -->
  **Behavioral difference (BD-5, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** `file.keyvalue`'s
  `key_values` (a `paramtypes.StringMap`) rejects a COMPOSITE value — a nested
  map or list — with a typed `value_invalid` error, where the legacy
  `fmt.Sprintf("%v", v)` sprint'd it into Go syntax and wrote that garbage into
  the file. A scalar value is unchanged (rendered to its string form by both).
  Pinned by the `composite-value-rejected-*` contract fixtures (yaml/msgpack; a
  nested map has no CLI spelling). **Presented for sign-off in this PR**
  (keystone spec §11).

- **`file.absent`, `file.touch`, `file.copy`, `file.symlink`, and
  `file.append` migrated to the self-documenting module-schema framework**
  (an all-primitives wave plus a second `StringList` consumer). Each
  constructor now decodes through a single compiled schema
  (`modschema.Spec`) plus registered documentation metadata, replacing the
  hand-written `config[...].(type)` extractions. `file.touch` and
  `file.copy` gain the `path` alias on their primary (`name,primary,
  aliases=path`, the `file_managed.go` exemplar) — matching what their
  legacy `fsxResolvePath`-based constructors already accepted, so this is
  parity, not a new divergence; `file.absent`, `file.symlink`, and
  `file.append` keep their legacy `name`-only primary (their pre-migration
  constructors never recognized a `path` alias, so one is deliberately NOT
  added here — parity, not a new divergence). `file.copy`'s `source` is the first
  `required` primitive-string parameter to reach a migrated module: a
  missing or empty `source` fails at decode with a typed `MissingRequired`
  error, where legacy raised its own explicit "source is required" error —
  both reject, so this is parity under the differential harness, not a BD.
  `file.append`'s `text` moves onto `paramtypes.StringList` (the same
  semantic type `user.present`'s `groups`/`optional_groups` use), replacing
  the legacy `parseAnyStringList` helper (which stays in `user.go` for the
  still-unmigrated `group.*` modules). Behavior is unchanged for every
  realistic input across all three universes (YAML, CLI, msgpack). Each
  module moved fully self-contained (they already had their own files); their
  reference pages (`file-absent.mdx`, `file-touch.mdx`, `file-copy.mdx`,
  `file-symlink.mdx`, `file-append.mdx`) are now generated from the
  registered schema + documentation metadata rather than hand-maintained —
  all prior content is preserved (parameter tables, Check/Apply/Revert
  behavior, every example and note, including the "Divergences from Salt"
  material folded into Notes) and reorganized under the shared
  Source/Parameters/Effects/Examples/Notes/Divergences/See-Also anatomy. The
  permanent differential contracts at
  `pkg/state/modules/testdata/contract/file.{absent,touch,copy,symlink,
  append}.yaml` guard the decode behavior; the doc-coverage ratchet shrinks
  by 5 (39 → 34 unmigrated modules).

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with every prior
  migration, each module's primary `name` parameter (and the non-primary
  string params `file.symlink`'s `target` and `file.copy`'s `source`) is now a
  compiled plain string: a *non-string* value (for example `name: 123` in
  YAML/msgpack) is coerced to its string form instead of silently falling back
  to the state ID (or, for `target`, staying empty; for the REQUIRED `source`,
  being zeroed and then failing the required check — so a numeric `source` is a
  reject-to-accept flip), and a *composite* value (a list/map) is now rejected
  with a typed `wrong_type` error instead of being silently ignored. Pinned
  per-param across YAML and msgpack by the `numeric-name-*`, `composite-name-*`,
  `numeric-target-*`, `composite-target-*`, and `numeric-source-*` contract
  fixtures across all five modules. (A MISSING `source` stays parity — both
  legacy and decoded reject — pinned by `source-missing-rejected`.)

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A CLI
  `<bool-param>=<truthy/falsy string>` is now honored on every boolean
  parameter across the five modules (`file.touch`'s/`file.copy`'s/
  `file.symlink`'s `makedirs`, `file.copy`'s `force`/`preserve`,
  `file.symlink`'s `force`) instead of being silently dropped by the legacy
  `.(bool)` assertion. Pinned by the `*-cli-truthy-string` contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** Each module's
  boolean parameters accept the INTEGERS 1 and 0 (1 = true, 0 = false, across
  every signed/unsigned integer kind, so a msgpack-delivered bool — which
  arrives as a sized kind such as `int8` — is honored) and reject any other
  integer with a typed `value_invalid` error, where the legacy `.(bool)`
  assertion silently dropped an int entirely. EVERY boolean parameter's integer
  arm is pinned explicitly (per the §11 per-param pinning standard, not by a
  representative): `file.touch`'s `makedirs`, `file.copy`'s
  `force`/`preserve`/`makedirs`, and `file.symlink`'s `force`/`makedirs`, each
  across YAML and msgpack by the `<param>-int-one-*`/`<param>-int-zero-*`/
  `<param>-int-invalid-*` contract fixtures.

  <!-- BD-5 -->
  **Behavioral difference (BD-5, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** `file.append`'s
  `text` list element that is a scalar (for example `text: [line1, 2]`) is now
  rendered to its string form (`"2"`) instead of being silently DROPPED by the
  legacy `parseAnyStringList` (which — stricter than `user.present`'s
  `groups`, which at least kept every already-string element — only ever
  appended an element that type-asserted directly as a Go `string`), and a
  NESTED element (a list or map inside the list) is now rejected with a typed
  `value_invalid` error instead of being silently dropped. A plain list of
  strings is unchanged. Additionally, a BARE-STRING value (for example `text:
  someline`, or a CLI `text=someline`) now decodes as a single-element list
  and ACTIVATES line management, where the legacy `config["text"].([]any)`
  type assertion failed entirely on a non-list value (so `text: someline`
  silently managed NO lines); this is also what makes the CLI scalar spelling
  work. Pinned by the `text-scalar-sprint-*`, `text-nested-rejected-*`, and
  `text-bare-string-*` contract fixtures (the scalar/nested-element arms have
  no CLI spelling for a mixed/nested list; the bare-string arm is pinned
  across all three universes). **Presented for sign-off in this PR** (keystone
  spec §11).

- **`file.managed` migrated to the self-documenting module-schema framework
  (the BD-1 flagship).** Its parameter declaration now decodes through a single
  compiled schema (`modschema.Spec`) plus registered documentation metadata,
  replacing the hand-written `config[...].(type)` extractions. Two parameters
  move onto named semantic types: `template` is a `paramtypes.TemplateFlag` (a
  bool, the string `"jinja"`, or any truthy/falsy string) and `mode` is a
  `paramtypes.FileMode` declared `lazy,default=0644` — the mode is never
  materialized into the struct; the module applies the documented `0644` default
  at use time via `Mode.Resolve(0644)`, and `FileMode` honors an octal string OR
  an octal integer of any kind. `name`/`path` (with `path` as the primary's
  alias), `content`, `source`, `user`, `group` are plain strings, `makedirs` a
  plain bool, and `context`/`defaults` are `map[string]any` passthroughs. The
  Check/Apply/Revert code paths are unchanged except for reading the two typed
  fields (`Mode.Resolve`, `Template.Enabled`). Behavior is unchanged for every
  realistic input — including the compiled decoder's source resolution, which
  now falls THROUGH an empty-string source exactly like an absent one (keystone
  spec §2.1/§2.2 amendment): an empty `name` alongside a non-empty `path` alias
  resolves to the path, never the state ID, matching the legacy
  `if name == "" { name = path }` chain (pinned by the `name-empty-path-wins`
  contract fixtures; the framework fix and its unit tests live in
  `pkg/modschema`). The module moved to its own file (`file_managed.go`; the
  shared file-module helpers — `resolveOwnerIDs`, `checkOwnershipDrift`,
  `modeConfigToString`, `hashBytes` — stay in `file.go`) per the per-module
  file-naming convention the docgen `Source:` line relies on. Its reference page
  (`file-managed.mdx`) is now generated from the registered schema +
  documentation metadata rather than hand-maintained: all prior content is
  preserved (the full parameter table, the Check/Apply/Revert behavior, the
  inline/source/template examples INCLUDING the "Create directories on demand"
  example, the worked `nginx.conf.jinja` source-template render walkthrough, the
  explicit template-namespace list — `facts.*`/`settings.*`/`context`/`defaults`
  with their precedence, restored as its own Note section — and the opt-in /
  double-render template caveat) and reorganized under the shared
  Source/Parameters/Parameter-Types/Effects/Examples/Notes/Divergences/See-Also
  anatomy, **drift-corrected** against the live code: the hand page claimed a
  parse-time content/source mutual-exclusion that does not exist (source simply
  wins when both are set) and omitted the ownership-drift Check facet, the
  chown-before-chmod ordering, and the pre-existing-file mode enforcement — all
  now documented honestly. `file.managed` is the first module to contribute the
  `TemplateFlag` and `FileMode` semantic-type `$defs` to the combined JSON
  Schema artifact. The permanent differential contract at
  `pkg/state/modules/testdata/contract/file.managed.yaml` guards the decode
  behavior across all three universes (YAML, CLI, msgpack); the differential
  harness (`pkg/modschema/schematest`) gained `TemplateFlag` and `FileMode`
  matchers (as it gained a `StringList` matcher for `user.present`). The
  doc-coverage ratchet shrinks by 1 (40 → 39 unmigrated modules).

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** A mode given as
  an octal INTEGER and delivered over msgpack is now applied. msgpack v5 encodes
  a small integer into the smallest kind by magnitude (`0755` → the octal int
  493 → `uint16`), and the legacy `modeConfigToString` switch handled only
  `int`/`int64`/`float64` — so a reactor-dispatched `file.managed` with
  `mode: 0755` fell through to the empty string and silently applied the `0644`
  default (the exact reproduced `0755 → 0644` bug that motivated this whole
  effort). `paramtypes.FileMode` interprets the octal value from every integer
  kind, so the requested mode now survives a reactor dispatch; the setuid/
  setgid/sticky special bits survive the same path (`0o4755` → `uint16`). Pinned
  by the `mode-0755-msgpack` and `mode-setuid-msgpack` contract fixtures.
  **Presented for sign-off in this PR** (keystone spec §11).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A CLI `template=<truthy
  string>` now enables Jinja rendering, where the legacy `v == "jinja"` check
  ignored every other string (so `zester '*' file.managed … template=true` was
  silently a no-op), and a CLI `makedirs=true` string now applies where the
  legacy `.(bool)` assertion dropped it. The SAME rule honors a YAML-quoted
  `template: "yes"` (a string, not a native bool). Pinned by the
  `template-cli-truthy-string` and `makedirs-cli-truthy-string` contract
  fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** A wrong-typed value is
  now handled deterministically instead of a silent zero/fallback: a *non-string*
  `name`/`content` is coerced to its string form (was a silent fallback to the
  state ID / the empty string), and a *composite* value (a list/map) — into
  `name`, or into `mode` (where the legacy `modeConfigToString` returned `""` and
  the module silently applied `0644`) — is rejected with a typed error.
  Relatedly, `mode` VALIDATION now happens at DECODE time rather than at apply
  time: a **float** `mode` (for example `mode: 493.0`, which the legacy
  `modeConfigToString` `%04o`-converted to `0755`) and an **invalid-octal string**
  `mode` (for example `mode: "banana"`, which the legacy path carried through and
  only rejected later at apply) are both rejected up front by `paramtypes.FileMode`;
  and a `template` value that `TemplateFlag` does not accept — it takes only a
  bool, `"jinja"`, or a truthy/falsy string — is now rejected with a typed error,
  whether it is a bare **integer** (`template: 1`) or an arbitrary **string** such
  as `template: mako` (the legacy `v == "jinja"` string check silently left
  rendering off for everything but `"jinja"`, so `mako` was a no-op). The string
  case is pinned across all three universes because a string survives every
  ingress unchanged (the CLI delivers `mako` too, unlike the integer case where it
  delivers a truthy `"1"`). Pinned by the `numeric-name-*`,
  `numeric-content-coerced-*`, `composite-name-rejected-*`,
  `composite-mode-rejected-*`, `float-mode-rejected-*`,
  `invalid-octal-string-mode-rejected-*`, `template-int-rejected-*`, and
  `template-invalid-string-rejected-*` contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** The boolean-typed
  `makedirs` now accepts the integers `1` and `0` (`1` → true, `0` → false) and
  rejects any other integer with a typed error, per the approved §2.3 coercion
  table and the §11 SCOPE ruling that BD-7 covers ALL boolean-typed parameters.
  The legacy `makedirs, _ = config["makedirs"].(bool)` assertion dropped an
  integer entirely (a silent false). This is documented on the generated page
  (the `makedirs` row) and pinned by the `makedirs-int-one-*`,
  `makedirs-int-zero-*`, and `makedirs-int-invalid-*` contract fixtures across
  the YAML and msgpack universes (the CLI delivers a string, covered by BD-2).

- **`user.present` migrated to the self-documenting module-schema framework
  (the semantic-type-heavy surface).** Its 14-parameter, previously
  zero-doc-comment declaration now decodes through a single compiled schema
  (`modschema.Spec`) plus registered documentation metadata, replacing the
  hand-written `config[...].(type)` extractions. Three parameters move onto
  named semantic types: `gid` is a `paramtypes.GroupRef` (a numeric GID or a
  group name), and `groups` / `optional_groups` are `paramtypes.StringList`.
  `password` is now marked **sensitive**: its value is redacted across the
  entire decode-error chain and never rendered into docs, defaults, or
  examples (verified by a redaction unit test). The `gid`/`primary_group`
  precedence is reproduced exactly in the builder's `resolveGroupFacets` (a
  name-form `gid` wins over `primary_group`; a numeric `gid` sets the GID).
  Behavior is unchanged for every realistic YAML input (string/int params, a
  name-form `gid`, a list of `groups`); `user.absent` stays on its legacy
  constructor (a later wave). The module moved to its own file
  (`user_present.go`, `user.absent` staying in `user.go`) per the per-module
  file-naming convention the docgen `Source:` line relies on, and its
  reference page (`user-present.mdx`) is now generated from the registered
  schema + documentation metadata — **drift-corrected** against the live
  Check/Apply/Revert: the hand page claimed `password` was "always applied on
  modify (not compared)" when the code actually converges by comparing the
  shadow hash, and described a string `gid` as always a group name (see BD-4).
  All prior page content is preserved (the full parameter table, the
  optional_groups/remove_groups semantics, the Check ordering, the example
  set) and reorganized under the shared
  Source/Parameters/Effects/Examples/Notes/Divergences/See Also anatomy. The
  permanent differential contract at
  `pkg/state/modules/testdata/contract/user.present.yaml` guards the decode
  behavior across all three universes (YAML, CLI, msgpack); the doc-coverage
  ratchet shrinks by 1 (41 → 40 unmigrated modules). `paramtypes.StringList`
  is the first list-valued semantic type to reach a module contract, so the
  differential harness (`pkg/modschema/schematest`) gained a `StringList`
  matcher alongside the existing `TriState` one.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with the earlier
  migrations, a wrong-typed value is now handled deterministically instead of
  a silent zero/fallback: a *non-string* `name` is coerced to its string form
  (was a silent fallback to the state ID), and a *composite* value (a list/map)
  into any scalar parameter — `name`, `uid`, or the sensitive `password` — is
  rejected with a typed error (was silently ignored). Pinned by the
  `numeric-name-*`, `composite-name-*`, `uid-composite-rejected-*`, and
  `password-composite-rejected-*` contract fixtures (the sensitive password's
  error value is redacted).

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12; origin-independent string
  coercion per the approved §2.3 table).** A string-form value for a typed
  parameter is now coerced no matter which universe delivered it, where the
  legacy `.(int)`/`.(bool)` assertions silently dropped it. The CLI delivers
  every value as a string, so `zester '*' user.present deploy uid=1500` now sets
  `uid` (the CLI `"1500"`, coerced base-10) and a CLI
  `createhome=true`/`system=true` bool-string is applied; the SAME rule coerces a
  YAML-quoted `createhome: "yes"` (a string, not a native bool). Pinned by the
  `uid-cli`, `createhome-true-cli`, and `createhome-truthy-string-yaml` contract
  fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12; scope extended to all
  boolean-typed parameters).** An INTEGER given to a boolean parameter
  (`createhome`, `system`, `remove_groups`) is now coerced narrowly — `1` is
  true and `0` is false, across every signed/unsigned integer kind (so a
  msgpack-delivered bool, which arrives as a sized kind such as `int8`, is
  honored rather than dropped by the legacy `.(bool)` assertion) — and ANY
  other integer (for example `2`) is rejected with a typed `value_invalid`
  error. Pinned across all three universes (YAML, CLI, msgpack) by the
  `createhome-int-one-*`, `createhome-int-zero-*`, and `createhome-invalid-int-*`
  contract fixtures. (This is the same rule already documented on the
  `TriState` type; here it applies to `user.present`'s primitive `bool`
  parameters.)

  <!-- BD-1 -->
  **Behavioral difference (BD-1, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** A msgpack-
  delivered `uid` or numeric `gid` is now honored. msgpack v5 encodes an
  integer into the smallest kind by magnitude (`1500 → uint16`, `1 → int8`),
  none of which the legacy `config["uid"].(int)` / `config["gid"].(int)`
  assertions matched — so a reactor-dispatched `user.present` with `uid: 1500`
  silently left `uid` at `0` (the exact reproduced `0755 → 0644` bug class).
  The uniform compiled decoder honors every integer kind, so the value now
  survives a reactor dispatch. Pinned by the `uid-msgpack` and `gid-int-msgpack`
  contract fixtures. **Presented for sign-off in this PR** (keystone spec §11).

  <!-- BD-4 -->
  **Behavioral difference (BD-4, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** An all-digit
  string `gid` (for example `gid: "1000"`, or a CLI `gid=1000`, or a native int
  arriving as the string `"1500"` over the CLI) is now resolved as a numeric
  GID, matching how the OS treats a numeric group and how the integer form
  already behaved. The legacy string branch treated ANY string `gid` — digits
  included — as a group NAME and stored it into `primary_group`, so `gid:
  "1000"` mis-configured a group named "1000" instead of GID 1000. A non-digit
  string `gid` is still a group name (unchanged). Additionally, a NEGATIVE `gid`
  (an integer `gid: -5`) is now rejected up front with a typed `value_invalid`
  error, where the legacy path forwarded it and only failed later at the group
  provider (a non-digit string such as `"-5"` over the CLI remains a group name,
  so this arm is YAML/msgpack). Pinned by the `gid-alldigit-string-*` and
  `gid-negative-rejected-*` contract fixtures. **Presented for sign-off in this
  PR** (keystone spec §11).

  <!-- BD-5 -->
  **Behavioral difference (BD-5, approved 2026-07-13 under the maintainer standing proceed-without-sign-off grant).** A `groups` /
  `optional_groups` list element that is a scalar (for example `groups: [docker,
  1000]`) is now rendered to its string form (`"1000"`) instead of being
  silently dropped by the legacy `parseAnyStringList`, and a NESTED element (a
  list or map inside the list) is now rejected with a typed `value_invalid`
  error instead of being silently dropped. A plain list of strings is unchanged.
  Additionally, a BARE-STRING value (for example `groups: docker`, or a CLI
  `groups=docker`) now decodes as a single-element list and ACTIVATES group
  management, where the legacy `parseAnyStringList` ignored any non-list value
  entirely (so `groups: docker` silently managed no groups); this is also what
  makes the CLI scalar spelling work. Pinned by the `groups-scalar-sprint-*`,
  `groups-nested-rejected-*`, and `groups-bare-string-*` contract fixtures.
  **Presented for sign-off in this PR** (keystone spec §11).

- **`pkg.installed`, `pkg.latest`, and `pkg.purged` migrated to the
  self-documenting module-schema framework** (all-primitives wave — no
  semantic types needed; `name`/`version`/`refresh` are all plain
  string/bool parameters). Each constructor now decodes through a single
  compiled schema (`modschema.Spec`) plus registered documentation metadata,
  instead of hand-written `config[...].(type)` extractions. `pkg.latest`'s
  `refresh` (which defaults to true, Salt-parity) is now an EAGER compiled
  default (`default=true`) rather than a construction-time `if ok` override,
  with the same observable default. `pkg.installed`'s file moved to
  `pkg_installed.go` (from `pkg.go`) to match the per-module file-naming
  convention the other migrated modules and the docgen `Source:` line both
  rely on. Behavior is unchanged for every realistic input (a string
  `name`/`version`, a native bool `refresh`, or none of the above — falling
  back to the state ID / the field's default). Their reference pages
  (`pkg-installed.mdx`, `pkg-latest.mdx`, `pkg-purged.mdx`) are now generated
  from the registered schema + documentation metadata rather than
  hand-maintained — all prior content is preserved (the apt/dnf/yum/brew
  per-manager command tables, the version-pinning format table, the dpkg
  `rc`-state notes, the yum-downgrade and apt `--allow-downgrades` details,
  the Salt divergence notes) and reorganized under the shared
  Source/Parameters/Effects/Examples/Notes/Divergences/See Also page anatomy,
  corrected against the live provider implementations
  (`pkg/exec/pkg_apt.go`, `pkg_dnf.go`, `pkg_yum.go`, `pkg_brew.go`) where the
  hand pages had drifted (missing the apt noninteractive/conffile flags and
  the yum-downgrade fallback entirely). The permanent differential contracts
  at `pkg/state/modules/testdata/contract/pkg.installed.yaml`,
  `pkg.latest.yaml`, and `pkg.purged.yaml` guard the decode behavior; the
  doc-coverage ratchet shrinks by 3 (44 → 41 unmigrated modules).

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with
  `pkg.removed`, each module's `name` parameter (and `pkg.installed`'s
  `version`) is now a compiled primary/plain string: a *non-string* value
  (for example `name: 123` or `version: 124` in YAML/msgpack) is coerced to
  its string form instead of silently falling back to the state ID / the
  empty string, and a *composite* value (a list/map) is now rejected with a
  typed `wrong_type` error instead of being silently ignored. This activates
  the §11 BD-6 class for `pkg.installed`'s `name`/`version` and for
  `pkg.latest`'s and `pkg.purged`'s `name`; pinned by the `numeric-name-*`,
  `numeric-version-*`, `composite-name-*`, and `composite-version-*` contract
  fixtures.

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A CLI
  `refresh=<truthy/falsy string>` is now honored instead of silently dropped
  or ignored. The CLI delivers every value as a string, so the legacy
  `config["refresh"].(bool)` assertion always failed: on `pkg.installed`,
  `zester '*' pkg.installed nginx refresh=true` left `refresh` at its false
  zero value; on `pkg.latest`, `zester '*' pkg.latest nginx refresh=false`
  left `refresh` at its eager `true` default regardless of the operator's
  intent (the construction-time `if r, ok := config["refresh"].(bool); ok`
  override never fired against a CLI string). Under the compiled decoder the
  string is coerced (the same true/yes/1/on ∥ false/no/0/off set the
  framework uses), so the operator's intent is now applied on both modules.
  This activates the §11 BD-2 class for `pkg.installed`'s and `pkg.latest`'s
  `refresh`; pinned by the `cli-refresh-*` contract fixtures.

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED conditionally 2026-07-12; scope
  amended 2026-07-12 to cover every boolean-typed parameter, primitive
  `bool` included, not only `TriState`).** `pkg.installed`'s and
  `pkg.latest`'s `refresh` — a plain `bool`, not `TriState` — given as an
  INTEGER is now coerced explicitly: `1` is true and `0` is false, across
  every signed/unsigned integer kind, so a msgpack-delivered `refresh`
  (which arrives as a sized kind such as `int8`) is honored instead of being
  silently ignored by the legacy `.(bool)` assertion (on `pkg.latest`, that
  silent drop left `refresh` at its eager `true` default regardless of an
  integer override). ANY other integer (for example `2`) is rejected with a
  typed `value_invalid` error rather than being dropped. Pinned across YAML
  and msgpack by the `refresh-int-one-*` / `refresh-int-zero-*` /
  `refresh-invalid-int-rejected-*` contract fixtures on both modules (the
  CLI leg is already covered by the `cli-refresh-*` BD-2 fixtures above — the
  CLI never delivers a raw integer). This activates the §11 BD-7 class,
  whose scope the orchestrator ruled (2026-07-12) extends to ALL
  boolean-typed parameters under the maintainer's existing TriState 1/0
  approval and coercion table, not only `TriState` fields.

- **The peel now warns on an unknown state-module parameter** (keystone spec
  §5, Phase-1 activation). A migrated (schema-carrying) module given a config
  key that matches no parameter and no reserved key — a typo like `nmae:` for
  `name:` — logs a `Warn` line through the peel's logger
  (`modschema: <module>: unknown parameter "<key>"`) and the state STILL
  builds and applies. The check is warn-only, never fatal: a stray key can no
  longer be silently absorbed, but it also can't break an apply. The
  fleet-wide reserved keys (requisites, generic attributes, compiler
  directives) and the exec-layer `test=True` dry-run flag are known control
  keys and never warned. Only migrated modules participate today; the warning
  surface grows as more modules gain schemas.

- **`service.running` and `service.dead` migrated to the self-documenting
  module-schema framework (semantic-type pilot).** Their `enable` parameter is
  now a named semantic type, `paramtypes.TriState` — a three-valued
  unset/true/false whose "declared" bit replaces the ad-hoc `Enable bool +
  hasEnable bool` pair. Behavior is unchanged for every realistic YAML/msgpack
  input: an omitted `enable` still leaves boot enablement untouched, `enable:
  true` enables (and, on `service.running`, converges an enable-only drift
  WITHOUT restarting a healthy service), and `enable: false` disables (the
  inverted default that `service.dead` keys off). Both modules moved to their
  own files (`service_running.go`, `service_dead.go`) per the per-module
  file-naming convention, and their reference pages
  (`service-running.mdx`, `service-dead.mdx`) are now generated from the
  registered schema + documentation metadata — drift-corrected against the
  live tri-state Check/Apply/Revert behavior. The permanent differential
  contracts at `pkg/state/modules/testdata/contract/service.running.yaml` and
  `service.dead.yaml` guard the decode behavior.

  <!-- BD-2 -->
  **Behavioral difference (BD-2, APPROVED 2026-07-12).** A CLI
  `enable=<truthy/falsy string>` is now honored instead of silently dropped.
  The CLI delivers every value as a string, so the legacy
  `config["enable"].(bool)` assertion failed and left `enable` UNDECLARED —
  `zester '*' service.running nginx enable=true` did not manage boot
  enablement at all, and `zester '*' service.dead nginx enable=false` did not
  disable the unit. Under TriState the string is coerced (the same
  true/yes/1/on ∥ false/no/0/off set the framework uses), so the operator's
  intent is now applied. This activates the §11 BD-2 class for both modules'
  `enable`; pinned by the `enable-*-cli` contract fixtures.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** As with
  `pkg.removed`, the `name` parameter is now a compiled primary string: a
  *non-string* `name` (for example `name: 123` in YAML/msgpack) is coerced to
  its string form instead of silently falling back to the state ID, and a
  *composite* `name` (a list/map) is now rejected with a typed error instead
  of being silently ignored. This activates the §11 BD-6 class for both
  modules; pinned by the `numeric-name-*` and `composite-name-*` contract
  fixtures. (A wrong-typed `enable` integer belongs to BD-7 below.)

  <!-- BD-7 -->
  **Behavioral difference (BD-7, APPROVED 2026-07-12).** A `service.running` /
  `service.dead` `enable` given as an INTEGER is now coerced explicitly: `1` is
  declared-true and `0` is declared-false — across every signed/unsigned integer
  kind, so a msgpack-delivered `enable` (which arrives as a sized kind such as
  `int8`) is honored rather than silently ignored by the legacy `.(bool)`
  assertion. ANY other integer (for example `2`) is rejected with a typed
  `value_invalid` error rather than being dropped. The rule is documented on the
  `TriState` semantic type — its `Doc()` (and thus the generated Parameter Types
  section) states the 1/0 semantics explicitly, and its JSON Schema constrains the
  integer form to `enum: [0, 1]` — and pinned across all three universes (YAML,
  CLI, msgpack) at both the type level (the `TriState` `int-one` / `int-zero` /
  `reject-two` type fixtures) and the module level (the `enable-int-one-*` /
  `enable-int-zero-*` contract fixtures for `service.running` and
  `service.dead`). Approved conditionally by the maintainer on 2026-07-12
  (keystone spec §11).

- **`pkg.removed`'s reference page is now generated**
  (`website/content/docs/guides/modules/pkg-removed.mdx`), from its
  registered schema and documentation metadata rather than hand-maintained.
  All prior content is preserved (the apt/dnf/yum/brew package-manager list,
  the dpkg `rc`-state convergence note) and reorganized under the new
  Source/Parameters/Effects/Examples/Notes/Divergences page anatomy shared by
  every future self-documenting module page.

  This tranche (documentation infrastructure only) activates no new
  behavioral differences itself — it introduces no Decode/coercion changes.
  Sign-off status (keystone spec §11, 2026-07-12): **BD-2, BD-6, and BD-7 are
  APPROVED**; BD-1/BD-3/BD-4/BD-5 remain pending and are presented for
  sign-off in the Phase-1 PRs that activate them.

- **`pkg.removed` migrated to the self-documenting module-schema framework
  (pilot #1).** Its constructor now decodes through a single compiled schema
  (`modschema.Spec`) plus registered documentation metadata, instead of a
  hand-written `config["name"].(string)` extraction. Behavior is unchanged for
  every realistic input (a string `name`, or none — falling back to the state
  ID). The permanent differential contract at
  `pkg/state/modules/testdata/contract/pkg.removed.yaml` guards this.

  <!-- BD-6 -->
  **Behavioral difference (BD-6, APPROVED 2026-07-12).** A *non-string*
  `name` value is no longer silently dropped in favor of the state ID. Under the
  uniform coercion framework a numeric `name` (for example `name: 123` in YAML or
  over msgpack) is now coerced to its string form (`"123"`), and a *composite*
  `name` (a list/map) is now rejected with a typed `wrong_type` error instead of
  falling back to the ID. This activates the §11 BD-6 class ("wrong-typed values
  are handled deterministically instead of a silent zero") for `pkg.removed`'s
  `name` parameter; it is pinned by the contract fixtures (approved 2026-07-12).


### Fixed
- **Starlark modules survive a states-directory switch (review round 5).** When
  the peel switches its states dir (baked tree → KV cache, or a lazy engine
  rebuild) it replaces the Starlark loader but keeps the registry; the fresh
  loader's empty ownership ledger then treated every previously loaded Starlark
  name as non-Starlark and shadow-refused all reloads — freezing custom modules
  (no hot-reload, no removal, no override) until restart. The old loader now
  purges its registrations first (`starmod.Loader.UnloadAll`; built-ins are
  untouched), and the new tree re-registers cleanly via LoadGlobal/LoadDir.
- **`zester '<target>' cmd.run cmd=<command>` now works end to end.** The CLI
  parsed `cmd=` as key=value form but dispatch routes cmd.run through the STATE
  module, whose strict schema only accepted `command`/`name` — so the accepted
  spelling was then rejected as an unknown parameter. The state schema now
  accepts `cmd` as a second alias (the execution-module spelling); source
  resolution order is `command` > `name` > `cmd`, pinned by contract fixtures
  across YAML/CLI/msgpack and a Docker salt-compat test. The same class was
  closed for the working directory: `cwd` gains the `dir` alias (the
  execution-module spelling `salt['cmd.run'](dir=...)`), so `zester '<target>'
  cmd.run 'make' dir=/opt/src` no longer fails under strict params.
- **`key=value` first tokens now parse as key=value for EVERY self-documenting
  module on the CLI, not just cmd.run.** `zester '*' pkg.installed name=nginx`
  previously bound the literal string `"name=nginx"` as the package name (the
  round-4 fix covered only the bespoke cmd.run arm). A first token assigning to
  one of the module's DECLARED parameter keys (canonical name or alias) now
  switches the whole invocation to key=value form — `file.managed path=/etc/motd
  contents=hi` works, and a default-less primary missing from the assignments is
  a usage error. Assignments to undeclared keys stay positional values, so
  exotic positional values containing `=` keep working.
- **`zester '<target>' pillar.get <key>` (and `pillar.items`/`pillar.keys`) now
  work from the CLI.** pillar.* is the peel's Salt-compat alias of settings.*,
  answered by a handler that reads only `args["key"]` — but the CLI had no
  pillar.* argument arms, so the key fell into the request ID and every keyed
  invocation errored `requires a key argument`. The pillar.* spellings now bind
  identically to their settings.* counterparts.
- **Documentation served from long-lived caches is now cloned on egress.**
  `moduledoc.Lookup`/`All` (the embedded offline docs) and
  `modules.DispatchInfo` (the dispatch-specials table) handed out ModuleInfo
  values whose doc slices and schema fragments aliased process-wide state,
  violating the returned-views-are-safe-to-vandalize contract the rest of the
  framework pins; new exported `Doc.Clone`/`ModuleInfo.Clone` seal them.
- **A `.star` file that fails to load is retried on the next load pass.** The
  loader recorded the file's mtime before executing it, so a failed load was
  skipped until the mtime changed; after a states-directory switch (old
  registrations purged) that left the module missing — not stale-but-callable —
  until a republish or restart. The mtime record now rolls back on failure.
- **`modschema` input boundary sealed (review round 5).** `Compile`/`NewSpec`
  now detach the caller's `Doc` (a registrant retaining and later mutating its
  doc slices could taint rendered docs), and `Spec.Params` is a deep-copied
  snapshot instead of an alias of the compiled plan's internal schema — the
  round-4 fix had sealed the output side (`Schema()`/`Info()`) only.

- **`zester '<target>' cmd.run name=<cmd>` no longer executes the literal
  assignment string.** The CLI treated a leading `name=`/`command=`/`cmd=`
  token as the positional command, so the Salt-parity form ran e.g.
  `name=echo hi` (exit 127). A leading explicit command-key assignment now
  parses as key=value form; a positional command merely containing `=`
  (env-prefix style, `FOO=bar env`) still runs verbatim.
- **JSON Schema artifact tightened to match the runtime decoder exactly**
  (review findings): `FileMode` string values now carry the octal pattern
  (`"999"`/`"banana"` are schema-rejected, matching decode); float strings
  follow the exact `strconv.ParseFloat` grammar (underscores only between
  digits, hex floats incl. `0x_1p2`/`0x.8p1`; `1__0`/`10_`/`0xp1` rejected);
  required-parameter checks reject `""`/`null` stand-ins across array-of-maps
  items. Known, documented limitation: when the same parameter key appears in
  multiple list items the runtime merges last-occurrence-wins, which JSON
  Schema cannot express — the schema validates items independently and the
  decoder stays authoritative (docgen refuses to GENERATE examples with
  duplicate keys).
- **`modschema` documentation views are now deep copies.** `CompiledSchema.
  Schema()` and `Spec.Info()` returned internally shared maps/slices; a
  consumer mutating a returned view (fields, aliases, JSON-Schema fragments,
  doc slices, semantic-type fragments) could corrupt later docs/schema output
  or race concurrent readers. Both now return fully detached values.

## [0.5.0] - 2026-07-10

### Added
- **Release promotion + auto-rollout.** `zester update promote` marks a
  published version as a promoted release: it **never expires**, and masters
  automatically roll the fleet to the latest promoted version through the
  full existing rollout machinery (batches, soak, auto-rollback, failure
  budget, degraded-node exclusion) — promotion is now the one-command
  release act. Auto-rollout never downgrades (nodes at or ahead of the
  promoted version are untouched), starts at most one rollout per component
  at a time, and uses deterministic generation ids
  (`rol-auto-<component>-<version>-r<N>`) so racing masters CAS-conflict
  instead of double-rolling. A COMPLETED run never blocks convergence: a
  node that was offline during the rollout (or enrolled later) still lags
  and gets the next generation, targeting exactly the laggards; an
  operator-ABORTED auto-rollout, by contrast, is never retried
  automatically for that version. The fleet switch is stored in NATS and **on by default** —
  safe, because nothing rolls until something is explicitly promoted — and
  flippable at runtime with `zester update auto on|off|status` (no master
  restart). Per-master participation and tuning via `update_auto_rollout`
  (default true), `update_auto_components` (default peel),
  `update_auto_batch_size` (5), `update_auto_soak_time` (60s),
  `update_auto_max_failed` (1), `update_auto_interval` (1m).
- **Per-version TTLs replace the object-store bucket TTL.** Every published
  version now carries its own expiry in the manifest: `update publish
  --ttl <d>` (default 30 days, `0` = never), `update set-ttl` adjusts it
  later, `update demote` resumes expiry for a formerly promoted version.
  A lease-gated hourly GC on the master reaps expired versions (object +
  manifest) — skipping anything a non-terminal rollout still references —
  and sweeps manifest-less orphan objects. The `update-binaries` bucket TTL
  is removed (it would have deleted promoted binaries behind the GC's
  back); versions published by older CLIs keep their original
  `published + 30d` lifetime.
- **`zester update rollouts`** lists rollout history (id, component,
  version, state, batch progress, failure budget, timestamps; newest
  first) — rollout ids no longer live only in the start command's output.
- **`zester update unpublish`** removes a published version (binary +
  manifest); refused while a non-terminal rollout references it, and
  promoted versions require `--force`. Node-side rollback is unaffected
  (nodes revert from their local previous-binary slot).
- **`zester update versions`** now shows PROMOTED and EXPIRES columns and
  sorts newest-first.

### Changed
- **`zester update status --rollout` Node Results are stable-sorted** by
  batch, then node name, with a new BATCH column — consecutive invocations
  line up row-for-row instead of shuffling with map iteration order.

## [0.4.2] - 2026-07-10

**Fleet-wide state-module convergence audit.** After the `pkg.latest`
stale-index bug shipped in the field, every state module (44 states, 36
files) was audited against the Check/Apply contract; 48 adversarially
verified defects across 25 states are fixed in this release. Three defect
classes dominated: Check blind to a facet only Apply enforces (drift never
converges, reported compliant forever), destructive or dishonest Revert
paths, and read errors conflated with "file absent" (data-loss paths).

### Fixed
- **`pkg.latest` refreshes the package cache BEFORE checking upgradability**
  (Salt parity). Refresh (default on) ran only in Apply, but Check consulted
  the stale index and short-circuited "already at latest" — so Apply, and
  with it the refresh, never executed: any release published after the box's
  last cache refresh was invisible (field symptom: the first fleet-wide
  `pkg.latest zester-peel` after the 0.4.1 repo publish was a silent
  `changed: 0` no-op on every apt host). Check and Apply now each run their
  own refresh independently. A FAILED refresh (one rotted third-party repo
  fails `apt-get update` while reachable repos still updated) warns and
  proceeds in both phases instead of failing the state; `--test` dry runs
  refresh the index too (metadata-only).
- **`pkg.installed` honors a declared `version:` pin in Check.** Any
  installed version used to satisfy a pinned state, so version drift was
  compliant forever. Check now compares the installed version against the
  pin (got/want diff). Pinned downgrades converge on every provider: apt
  passes `--allow-downgrades`, yum verifies the pin landed and falls back
  to `yum downgrade` (plain `yum install pkg-<older>` silently no-ops),
  dnf handles explicit version downgrades natively. Undeclared `version:`
  is unchanged.
- **`pkg.purged` treats a removed-but-not-purged Debian package (dpkg `rc`
  state) as needing a purge** — converged only when no package record
  exists at all; previously `rc` reported converged and leftover conffiles
  were never removed. The apt probes also fail loudly when the probe never
  ran (spawn failure/context death) instead of reporting "not installed",
  and parse multi-arch dpkg output correctly.
- **`cron.present`, `sysctl.present`, and `mount.mounted` now actually work
  on real peels.** Their exec providers (crontab/procfs/fstab) were never
  wired into provider detection — every real-peel run failed with "no
  cron/sysctl/mount provider available" (unit tests wired providers
  manually, masking it).
- **`file.managed` enforces the `mode:` facet on pre-existing files.**
  Apply wrote content via a create-time-perm-only write and never chmodded,
  so mode drift on an existing file was reported by Check forever but never
  fixed — permanent churn firing `watch` dependents every highstate. Apply
  now chmods after writing; Revert restores the CAPTURED prior mode, not
  the desired one. Octal modes with setuid/setgid/sticky digits
  ("4755", "1777") are translated to the Go FileMode flags — previously the
  special bit was silently dropped by chmod and the state re-applied forever
  without ever setting it — and ownership is applied BEFORE mode (chown on
  an executable clears setuid). Applies to file.managed, file.directory,
  and file.recurse modes.
- **Debian `rc`-state packages (removed, conffiles remain) no longer count
  as installed.** The apt probe requires dpkg status `installed`
  (`dpkg-query -W -f='${db:Status-Status}'`) instead of the `dpkg -s` exit
  code — `pkg.installed` could never reinstall a previously-removed
  conffile-bearing package, and `pkg.removed` re-applied (and fired `watch`
  dependents) on every highstate after its own successful removal.
- **`pkgrepo.managed` verifies the Debian signing key.** The `key_url` key
  now lands persistently at `/etc/apt/keyrings/zester-<name>.gpg` (was a
  volatile `/tmp` download) and Check reports drift when it is missing —
  previously a never-imported/deleted key left `apt-get update` failing
  NO_PUBKEY while the state reported converged. Presence-only (in-place key
  rotation at the same URL is not detected); Revert removes the artifact.
- **File states detect ownership drift.** `file.managed`, `file.directory`,
  and `file.recurse` compare on-disk owner/group against declared
  `user:`/`group:` in Check (declared facets only — undeclared ownership
  never churns). `file.recurse` Check also flags `clean: true` extra files
  (previously the clean feature could never fire once the managed set
  converged) and `dir_mode` drift on every managed dir, and all its
  filesystem walks now go through the injected file provider
  (new `FileExec.Walk`).
- **`service.running`/`service.dead` compare a declared `enable:` facet in
  Check** — running-but-disabled (`enable: true`) and stopped-but-enabled
  (`enable: false`, resurrects at reboot) were compliant forever.
  `service.running` with `enable: false` now actually disables; an Apply
  reached only for enable drift never restarts the running service.
- **`user.present` converges password and name-based primary group.** A
  declared `password:` hash is compared against the shadow hash (new
  `UserExec.PasswordHash`); a name-based `gid:`/`primary_group:` is compared
  and enforced on existing users via `usermod -g` — both facets were
  silently unenforced outside user creation.
- **`mount.mounted` fstab comparison now includes `dump`/`pass`**, Apply
  no-ops honestly when converged, and Revert no longer claims an unmount it
  never performed. (Comparing the LIVE mount — a wrong device/options
  serving the mountpoint — is deferred: it needs Salt-style
  option/fstype/device normalization to avoid remount churn on
  kernel-normalized values; that audit finding stays open on the backlog.)
- **`sysctl.present` with `persist: true` verifies the drop-in file entry**,
  not just the runtime value — a manual `sysctl -w` match silently died at
  the next reboot.
- **`cron.present` entries are keyed on the label** (identifier comment,
  Salt semantics) instead of the exact command string — editing a state's
  command replaces the old line instead of orphaning it to run forever.
  Managed entries carry a `# ZESTER_CRON_ID: <label>` marker; pre-existing
  same-command lines are adopted (a human descriptive comment neither
  blocks adoption nor duplicates the job) and stamped on the next apply.
  Converged Apply is a no-op, so watch-forced runs no longer rewrite the
  crontab. Crontab edits are LINE-PRESERVING: Set/Remove splice only
  the targeted entry and its marker — MAILTO=/PATH= environment lines,
  human comments, blank lines, and @reboot/@daily nickname entries survive
  untouched (the previous whole-crontab reconstruction silently destroyed
  them), and a Remove that matches nothing does not rewrite the crontab at
  all. Commands are compared whitespace-normalized, so a declared command
  with consecutive spaces converges instead of rewriting every run.
- **`git.cloned`/`git.latest` with a symbolic `rev:` (tag) converge** —
  the rev was compared against the HEAD sha by string prefix, which never
  matches a tag name, so every run re-applied (needless fetches, `watch`
  cascades, service restarts every highstate). Symbolic revs resolve locally
  via `git rev-parse --verify <rev>^{commit}`; sha prefixes still match
  directly.
- **`locale.present` verifies the `/etc/locale.gen` enabling line** that
  Apply writes, not just `locale -a` membership — an out-of-band-generated
  locale was compliant until the next `locales` package upgrade silently
  dropped it. The facet applies only where `/etc/locale.gen` exists
  (Debian-family); RHEL/musl systems stay satisfied by `locale -a`, no
  stray `locale.gen` is created, and charmap spellings are normalized
  (`en_US.utf8` == `en_US.UTF-8`).
- **Read errors are no longer conflated with "file absent" anywhere.**
  `host.present`, `ssh_auth.present`, every text-editing file state
  (`file.line`/`append`/`blockreplace`/`comment`/`keyvalue`/`replace`),
  `file.managed`/`copy`/`recurse`, `cmd.run`'s `creates` probe,
  `locale.present`, and the archive marker all treated ANY read error as
  "missing" — a transient EIO/EACCES/ESTALE followed by a successful write
  could truncate `/etc/hosts` or a user's `authorized_keys` down to the one
  managed line, or clobber file content that was never captured. Only
  `fs.ErrNotExist` selects the absent path now; anything else fails the
  phase without writing.
- **Revert contract: a fresh instance never destroys state.** Revert
  consuming in-instance memos (backups, created flags, saved originals)
  treated "memo unset" as "file was new" and DELETED the target — and the
  runner builds FRESH instances for revert, so a revert run would have wiped
  `/etc/hosts`, `authorized_keys`, or any managed file. Fresh-instance
  Revert is now an explicit clean no-op across ALL modules
  ("nothing to revert (no apply recorded in this run)"); same-instance
  Apply→Revert still restores backups (with canonical permissions —
  `authorized_keys` restores 0600) and still removes files the same
  instance created. (tolerating an already-externally-removed file). Related honesty fixes: `sysctl.present` Revert no
  longer writes an EMPTY value into the kernel and persist file;
  `mount.mounted` Revert no longer claims an unmount it never performed;
  `group.present` Revert really restores membership (it only restored the
  GID); `service.running` Revert reports enable-reverts.
- **Watch-forced applies are safe on guarded/absent states.** `cmd.run`'s
  `creates` guard now also gates Apply (a watch trigger used to re-run
  creates-guarded one-shots like `initdb`); `user.absent`/`group.absent`
  Apply no-op cleanly when already absent instead of failing `userdel`/
  `groupdel`; `service.dead`/`mount.mounted`/`sysctl.present`/`user.present`
  Apply can now honestly report `changed: false` when converged.
  `archive.extracted` Apply re-evaluates its `if_missing`/`source_hash`
  guard itself, so a watch trigger no longer re-downloads and re-extracts a
  converged archive.

### Added
- **`archive.extracted` gains `source_hash`**: recorded in a marker after a
  successful extraction and compared by Check, so bumping
  `source`/`source_hash` re-extracts instead of no-oping forever. Opaque
  string comparison (not byte verification); the marker is only written
  after success, so a failed extract no longer latches a `makedirs`-created
  dir as done. Without `source_hash`, existing marker semantics are
  unchanged and now documented.
- **Exec-layer convergence probes** (internal API): status-aware apt
  installed-probe, `PackageExec.InstalledVersion`, `FileExec.Owner`/`Walk`,
  `UserExec.PasswordHash`, `UserModifyOpts.PrimaryGroup`; test fakes wrap
  `fs.ErrNotExist` for missing files and support read-error injection.

### Changed
- **`pkg.removed` Revert is an explicit clean no-op** — it previously
  reinstalled the repo's latest candidate driven by a never-populated memo.
  Reinstall explicitly with `pkg.installed`.
- **`pkgrepo.managed` Debian key artifact moved** from
  `/tmp/zester-repo-<name>.gpg` to `/etc/apt/keyrings/zester-<name>.gpg`;
  existing fleets show ONE pending change per keyed repo on the next
  highstate (idempotent re-import).

## [0.4.1] - 2026-07-09

### Fixed
- **`onlyif`/`unless` guards: a non-zero guard exit is the guard's answer,
  not an error.** The peel's guard runner passed the command provider's
  exit-status error through, so any failing `onlyif` reported
  `error: onlyif ...: exit status 1` instead of the documented clean skip,
  and `unless` was unusable — its normal run-the-state path IS a non-zero
  exit, so every such state failed instead of running (found during 0.4.0
  fleet operations). The exit code is now extracted; only guards with no
  meaningful answer error (killed by context deadline, or spawn failure).

### Added
- **`--version` on every binary.** `zester-peel`, `zester-master`, and
  `zester-watchdog` gain a `--version` flag (version, commit, build date;
  print and exit), and `zester --version` works via the CLI root. The
  daemons additionally now REFUSE unexpected positional arguments (exit 2)
  instead of silently starting: probing a peel binary with
  `zester-peel version` used to boot — and enroll — a peel.

## [0.4.0] - 2026-07-09

Live file distribution: on-disk edits reach the fleet in seconds with no
master restart, multi-master dirs converge through KV (settings via a sealed
masters-only channel), failover can no longer revert the fleet, and operators
can always tell which master is the publisher. Plus embedded-CA rotation
tooling and tests. One behavior change: out-of-band writes to the file
buckets are now healed back to disk truth.

### Added
- **Embedded-CA rotation is now implemented and tested, not just documented.**
  New `pkg/ca` API: `Authority.RotateIntermediate` mints a fresh signing
  intermediate under the same root (refused in root-offline mode), and
  `Authority.SaveIntermediate` is its on-disk half — `Save` deliberately
  refuses a dir that already holds a root key, so the runbook's "replace
  intermediate.crt/key in ca.dir" step previously had no programmatic path.
  Test coverage now pins the properties both runbook procedures rely on:
  intermediate rotation is fleet-invisible (old and new chains verify against
  the original root anchor; the SPKI pin is unchanged; a restarted master
  self-issues through the new intermediate — `pkg/ca` + `internal/masterd`),
  and root rollover works as a two-phase overlap-bundle file drop on the
  enrollment plane (persisted-anchor peels verify either root during overlap,
  refuse the retired root after, and never re-TOFU — `pkg/enroll` over real
  TLS handshakes). The tests also pinned a runbook constraint: during
  overlap, `enroll_ca_pin` must still cover the anchor bundle's FIRST root —
  retiring the old pin before the old root leaves the bundle fails loudly.
- **Docs: the embedded-CA operations guide gained a Multi-Master section**
  (CA-dir file replication like `account.seed`, root key may stay offline,
  per-master self-issued leaves, split-brain divergence warning and its
  fail-closed peel behavior), and the rotation runbook now spells out the
  multi-master steps (replicate the rotated intermediate to every master;
  roll masters one at a time — the divergence warning during a root-rollover
  overlap window is expected and clears when the last master switches).

- **On-disk file edits reach the fleet without a master restart.** The
  publisher-lease holder previously walked the settings/states/reactor trees
  ONCE per lease acquisition — adding a state file meant restarting the
  master. Three mechanisms replace that: an fsnotify **file watcher**
  (`files_watch`, default on — inotify on Linux; publishes ~1s after an
  edit), a **republish interval** as the correctness backstop
  (`files_republish_interval`, default 30s; covers NFS/remote mounts and
  missed events; 0 disables), and **`zester fileserver update [--force]`**
  (Salt `fileserver.update` parity) — a request answered only by the
  publisher-lease holder, reporting per-set files/changed; `--force`
  rewrites every key (heals a tampered or torn bucket). Settings edits also
  refresh the per-peel secret-encryption inputs (extracted secrets +
  top.zy) live.

- **Multi-master file convergence: standby masters mirror published truth.**
  Standby masters (holding no publisher lease ~2 lease TTLs after boot or
  after a loss) now sync the **states and reactor** file sets from KV into
  their local source dirs (`files_mirror`, default on) using the same
  manifest-verified atomic-swap machinery peels use. On failover the new
  lease holder stops its mirror, runs one catch-up sync, and its initial
  publish hash-gates to a **no-op** — a takeover can no longer revert the
  fleet to a stale tree (previously the new holder unconditionally
  republished whatever its disk contained). A fresh boot that wins the lease
  immediately skips the catch-up, so the single-master offline-edit workflow
  keeps today's disk-wins semantics; a lease loss re-arms the standby timer
  rather than mirroring instantly, so a transient NATS blip can't race the
  re-acquisition. Local edits on a standby are overwritten by the mirror
  with a loud warning naming the files; mirrored dirs are managed trees.
  **Settings converge through a dedicated sealed channel**: the peel-facing
  sanitized bucket is never mirrored (that would destroy `!encrypted`
  plaintext); instead the lease holder replicates the raw settings tree into
  the new masters-only `master-settings` bucket with every file sealed to
  the shared account curve key (NaCl box) — any master opens it (they all
  hold `account.seed`), peel credentials have no grant for the bucket, and
  JetStream at-rest/backups never contain plaintext. The manifest hashes are
  **HMAC-keyed under the account seed** (not raw SHA-256), so a `$KV.>`
  reader without `account.seed` cannot use them as an offline brute-force
  oracle on secret values — while staying deterministic, so the hash-gate is
  unaffected. The sealed replica publishes FIRST (before the peel-facing
  sanitized publish, from the same file set), so an interruption rolls
  forward rather than reverting settings on the next failover. Standbys
  decrypt on sync and refresh their in-memory secret-extraction state (a
  `facts-secrets`-lease-holding standby always encrypts current values, on
  own-disk and shared-volume topologies alike). Auto-excluded for
  GitFS-sourced states (masters converge through git); disable
  `files_mirror` when masters share one filesystem for these dirs.
- **GitFS exclusively owns states publishing when configured.** The
  watcher/interval/`fileserver update` paths skip the states set under GitFS
  (`skipped: gitfs-managed` in the update reply) instead of walking the dir
  directly — bypassing GitFS's clone-validity gate could have published a
  half-cloned tree and propagated fleet-wide state deletions during a
  self-healing re-clone.
- **The hash-gate verifies publish integrity, not just manifest equality.**
  A publish interrupted between the `_manifest` write and the `_revision`
  bump — or a manifest-listed key deleted by a dual-leader prune — is now
  detected at the next gated publish and repaired automatically, instead of
  being pinned forever by the byte-equality check.
- **Secret VALUE rotations reach the fleet without a restart.** A rotated
  `!encrypted` value changes neither the sanitized bytes (hash-gate) nor
  stable peels' facts (their publishes hash-skip), so previously only a
  master restart's facts replay delivered it. The live settings publish now
  fingerprints the extracted secrets and, on change, re-encrypts for every
  indexed peel (per-peel hash-gated).
- **Behavior change: out-of-band writes to the file buckets are healed.**
  The master's on-disk trees are the source of truth; anything writing the
  settings/state/reactor KV buckets directly (scripts, manual `nats kv put`)
  is reverted to disk truth within one republish interval. Edit the files on
  the lease holder (or use GitFS) instead.
- **Operators can always tell which master is the publisher.** New
  `zester fileserver status` names the lease holder (hostname + hold time;
  answered only by the holder). The master maintains
  `/run/zester/publisher-status` (`publisher_status_file`) on every lease
  transition, and the master `.deb` ships an `/etc/update-motd.d` snippet
  that warns at SSH login when the host is a standby ("edits here are not
  published"). `/readyz` gains an informational `publisher-lease` entry and
  `/metrics` a `zester_master_publisher_leader` gauge.

### Changed
- **All three file publishers are hash-gated.** A publish whose manifest is
  byte-identical to the bucket's writes nothing — no file puts, no
  `_revision` bump, no peel resyncs — so the watcher, the ticker, and
  repeated `fileserver update` runs are free when nothing changed. GitFS
  republishes get the same gate.
- `statefiles.Cache` is generalized (bucket, key prefix, ignore keys,
  manifest decoder, local-edit warnings) so the same cache implementation
  backs both the peel state-file cache and the master standby mirrors.

## [0.3.8] - 2026-07-09

Human identity everywhere: the CLI displays and accepts dotted hostnames
(`devops-hetzner.oxm`); the underscore form is now purely a NATS wire
encoding, formally reserved and enforced on every id-minting path.

### Changed
- **The CLI speaks hostnames; only the wire speaks tokens.** Every place the
  CLI shows a peel id — job returns and targeting echo, `peel list`,
  `enroll list/show/approve/reject/revoke`, `job show`, `update status`,
  `update rollback`, `event watch` origins, JSON/YAML output included — now
  displays the human form: interior `_` decodes back to `.`
  (`enroll.DisplayPeelID`), so `zester 'devops-hetzner.oxm' test.ping` answers
  as `devops-hetzner.oxm`, not the NATS subject token `devops-hetzner_oxm`.
  The decode is lossless because `_` in a peel id is now RESERVED as the wire
  encoding of `.`: a configured `id` may not contain a raw underscore (write
  the dot — it round-trips), and hostnames can never contain one. The token
  form remains valid input everywhere.
- **Reactor rules accept dotted-hostname origins.** The ORIGIN segment of a
  match-key glob (before the first `/`) is dot-normalized at compile time, so
  `'web01.pl/service/*'` matches the wire key `web01_pl/service/nginx`;
  `zester reactor test` accepts the dotted form too. Tag segments are
  deliberately never rewritten — tags may legitimately contain `_`, and a
  dotted tag is probably a mistyped `/`. Two guards keep that boundary
  honest: `zester reactor test` REFUSES a key whose tag cannot exist on the
  wire (dots in tag territory — previously it would green-light a rule no
  live event could ever reach), and the rule loader warns on any match glob
  with a `.` after the first `/` (dead pattern; a peel id inside a beacon
  tag uses its `_` wire form).
- **The `_` reservation covers hostname-derived ids too.** A non-RFC
  underscore hostname (`db_primary.example.com` — Linux permits it) is now
  refused at id derivation with guidance to set an explicit `id`: sanitizing
  it would mint an id whose dotted display names a host that does not exist
  and which pre-collides with the genuine `db.primary.example.com`.
- **Dotted-form input works in every command, not just targeting**: `zester
  kv fact get`, `zester basket list`, and `--direct` exact/glob targets now
  normalize dots like job-mode targeting (direct mode shares
  `target.GlobMatcher` outright); `job list`/`job active` TARGET columns,
  `update rollout` dry-run batches, and the `basket get` PEEL column joined
  the display decode.

## [0.3.7] - 2026-07-09

Zero-config node identity: `zester-watchdog --component peel` is a complete
invocation, the peel id defaults to the (sanitized, pinned) hostname, and
dotted-hostname targets match everywhere. Plus the fixes from this release's
adversarial review — most notably the colocated master+peel rollout-command
cross-execution.

### Changed
- **The watchdog is now component-aware: a packaged unit is just
  `zester-watchdog --component peel` (or `--component master`).** Everything
  else — child binary, child config, node id, creds path, CA path,
  bootstrap-cache, health/ready URLs — is derived from `--component` and the
  child's config; explicit flags still override. This removes the
  hand-maintained flag list and the per-host `ZESTER_PEEL_ID` systemd drop-in
  that had to be kept in sync with `peel.yaml` (the source of an entire class
  of "the drop-in and the config disagree" failures). The peel and the watchdog
  now resolve the node id from the same source, so they can never diverge on
  identity or the `<id>.creds` filename. Deriving `--health-url` from the
  child's `health_addr` also fixes the footgun where a master watchdog had to
  remember to override the default `:9090` to `:9091`.
- **The peel id now defaults to the machine hostname.** With no `id` in
  `peel.yaml`, `zester-peel` derives it from `hostname -f`, sanitized into a
  valid NATS subject token: `.` → `_`, other invalid chars → `-`, e.g.
  `web01.example.com` → `web01_example_com`. Mapping dots to `_` (which a valid
  hostname never contains) keeps the transform collision-free — the distinct
  hosts `web01.pl` and `web01-pl` map to the distinct ids `web01_pl` and
  `web01-pl` instead of colliding. An explicit `id` is sanitized the same way
  rather than rejected — so a dotted FQDN just works instead of failing
  enrollment with a cryptic "create enrollment client" error. New
  `enroll.SanitizePeelID` / `enroll.SanitizeGlobDots` and `config.ResolveNodeID`.
- **A hostname-derived node id is pinned to `<auth_dir>/node-id`** (Salt
  `minion_id` semantics): `hostname -f` depends on DNS, and a reboot during a
  resolver outage would silently re-identify the node (fresh enrollment,
  orphaned creds) without the pin. The pin also guarantees the peel and its
  watchdog converge on one identity even when they resolve at different times.
  An explicit `id` is never pinned; delete the file to re-derive. A derived id
  that is a localhost placeholder (`localhost`, `localhost.localdomain`, …) is
  refused at startup — it would collide across every misconfigured host — with
  a message asking for an explicit `id`.
- **Dotted-hostname targets match everywhere, not just globs.** The `.` → `_`
  normalization that lets `zester 'web01.pl' test.ping` match the sanitized id
  now also applies to list targets (`L@web01.pl,web02.pl`), the reactor's
  `require_peel` gate, and `zester enroll approve --peel web01.pl`. It is
  applied only where the pattern matches peel IDs (never to reactor match-key
  globs, where a `.` may be a mistyped dotted tag), and never inside glob
  `[...]` character classes, where rewriting a `.` range endpoint could widen
  the class to unintended peels.

### Fixed
- **A colocated master + peel watchdog pair no longer executes each other's
  rollout commands.** Both watchdogs on one host resolve the same node id and
  therefore subscribe the same id-only command subject
  (`zester.update.cmd.<id>`) — a peel rollout could swap the MASTER binary
  (staged peel binary renamed over `/usr/local/bin/zester-master`, master
  restarted as the wrong binary until the confirm-deadline rollback). The
  update handler now drops — without replying, so the matching watchdog's
  reply is never raced — any command whose `component` doesn't match its own,
  and every sender stamps it (`zester update rollback` previously didn't).
- **An explicit watchdog `--id` is sanitized like every other id source.** A
  dotted `--id web01.example.com` previously stayed raw, deriving an
  `<auth_dir>/web01.example.com.creds` path no 0.3.7 peel can ever write — the
  watchdog waited for creds forever and the node's self-update plane was
  silently dead. Both id paths now flow through `config.ResolveNodeID`.
- **The watchdog honors child flag overrides in `--child-args`.** The child
  applies its CLI flags over its config (flag > YAML), so `--id`, `--auth-dir`,
  `--data-dir`, `--health-addr`, and `--nats-ca` inside `--child-args` now
  overlay the derivation too — previously the watchdog derived creds paths and
  health URLs from config values the child wasn't actually using.
- **A config-less box no longer crash-loops the child.** The derived
  child-args pass `--config /etc/zester/{peel,master}.yaml` only when the file
  exists: the daemons treat an explicitly passed missing config as fatal,
  while with no flag they run on built-in defaults.

## [0.3.6] - 2026-07-09

Make "zester manages zester" via the pkg module safe: package upgrades no
longer stop the running service, and apt runs non-interactively.

### Fixed
- **A `zester-peel`/`zester-master` package upgrade no longer stops the running
  service** (which made managing zester with zester's own `pkg` module fatal).
  The `.deb` `prerm` stopped and disabled the unit unconditionally, but dpkg
  runs `prerm` on **upgrades** too (`prerm upgrade`), so upgrading the peel from
  inside its own cgroup (e.g. `pkg.latest zester-peel`) killed watchdog + peel +
  apt + dpkg mid-unpack, leaving dpkg half-configured and the service
  down+disabled. `prerm` now acts only on a real removal (`prerm remove`); an
  upgrade leaves the running service untouched (postinst only no-op *starts*).
- **The apt package provider now runs fully non-interactively.** `apt-get
  install/remove` set `DEBIAN_FRONTEND=noninteractive` and
  `--force-confdef --force-confold`, so a modified conffile (e.g. an
  operator-edited `/etc/zester/peel.yaml`) can no longer trigger a prompt that
  hangs the peel's serialized exec worker and blocks every mutating job behind
  it.

### Added
- **Docs: [Upgrading Zester](operations/upgrading)** — the two upgrade channels
  (self-update plane for the running binary; apt / the `pkg` module for
  package + config alignment) and the rule that a node must never synchronously
  restart its own service from a job.

## [0.3.5] - 2026-07-09

Fix the 0.3.4 release-blocker (peel creds too large for NATS default
max_control_line), grant-weight linting, CLI trust-without-config, and the
`auth` -> `nats-auth` command rename.

### Fixed
- **Peels with 0.3.4-issued creds could not connect at all (release-blocker for
  0.3.4).** The fattened peel JWT (the new flow-control grants) serializes past
  NATS's default `max_control_line` of 4096 bytes, so the server rejects the
  connection **before authentication** with `maximum control line exceeded` —
  near-silent on the client (readyz just flips to down). `zester nats-auth init`
  now emits `max_control_line: 16384` in the generated `nats-server.conf`.
  **If you run your own `nats-server` you MUST set `max_control_line: 16384`
  yourself** — this is required on every NATS server the fleet connects to, or
  re-enrolled peels brick. (The grants are correct least-privilege; the fix is a
  bigger control line, not stripping grants.)
- **`zester update fetch` no longer hangs for minutes under peel creds.** The
  `--component/--version` manifest lookup opens the manifest bucket, which peel
  creds cannot read, so it blocked until timeout; it now has a short deadline
  and points you at the direct `--object-key <k> --sha256 <h>` path (which is
  what the watchdog uses and works under peel creds).

### Added
- **`zester nats-auth lint` now flags oversized JWTs**, not just grant gaps: it
  warns when a creds' CONNECT line would exceed NATS's default `max_control_line`
  (4096) — the "grant weight" drift that bricked 0.3.4 peels — and tells you to
  raise `max_control_line`.
- **The operator CLI resolves NATS trust without a config file.** New global
  `--nats-ca` flag, and `NATS_CA_FILE` is now honored; combined with `--creds`
  and `--master`/`NATS_URL`, the CLI runs on a peel-only box with no
  `~/.zester/config.yaml`.
- **`zester update fetch --object-key <key> --sha256 <hash>`** downloads a
  binary directly (skipping the manifest), mirroring the watchdog's own path so
  it works under least-privilege peel creds.

### Changed
- **Renamed `zester auth init` → `zester nats-auth init` and `zester auth lint`
  → `zester nats-auth lint`.** The group name now says what it manages (the
  external NATS server's JWT auth), distinct from peel enrollment and API-token
  auth. It stays a flat top-level group (not nested under `nats`), alongside
  `zester ca`. Update any provisioning scripts.

## [0.3.4] - 2026-07-09

Self-update permission fixes and tooling to catch the whole grant-drift class.

### Security / Fixed
- **Self-update binary downloads no longer stall with a permissions violation.**
  The peel JWT granted the object-store read APIs but not the JetStream
  flow-control publish subject `$JS.FC.OBJ_update-binaries.>` the ordered
  download consumer needs, so every watchdog self-update on peel credentials
  died with `Permissions Violation for Publish to "$JS.FC.OBJ_update-binaries.…"`
  and never completed. The grant is now issued. The same latent gap is closed
  for the peel's KV watches (`$JS.FC.KV_{settings-files,state-files,secrets,basket,facts}.>`),
  which could stall under backpressure. **Re-issue peel creds (re-enroll) to
  pick up the new grants.**
- **Reactor event acks are no longer denied.** The master/admin creds were
  scoped to `$JS.API.>`, which does not cover the durable reactor/schedule
  consumers' ack subjects (`$JS.ACK.<stream>.>`) or flow control
  (`$JS.FC.<stream>.>`); reactions still fired (dedup masked it) but events
  were never acked, causing redelivery churn until MaxDeliver. These trusted
  control-plane creds now carry `$JS.>`. **Re-issue master/admin creds** (from
  `zester auth init`) to pick it up. Surfaced by the new permissions sentinel.

### Added
- **`zester auth lint <creds-file>...`** (offline): decodes a NATS `.creds`
  file and flags JetStream access-pattern gaps — the class of bug where a
  component connects fine but a specific operation is silently denied at
  runtime. It keys off the grants the creds carry (a consumer-create on a
  stream) and reports the companion flow-control / inbox subjects that access
  pattern requires; an object-store gap is an error, a KV-watch gap a warning.
- **`zester update fetch --component <c> --version <v> [--out <path>]`**:
  downloads a published binary from the object store and verifies its SHA-256 —
  the non-mutating counterpart to a rollout's internal download. With
  `--creds <peel.creds>` it checks that peel credentials can complete the
  flow-controlled object-store download self-update depends on.
- **Integration coverage for the whole permissions class**: a sentinel that
  fails the suite on any `permissions violation` across all component logs, and
  a test that downloads a real (multi-MB) binary under peel credentials
  (`update fetch`), exercising the object-store flow-control path end-to-end.

## [0.3.3] - 2026-07-08

Deployment papercuts surfaced by the first production install.

### Added
- **`zester enroll approve --peel <peel-id>`** approves a peel's current
  enrollment without copying the enrollment KSUID, and **`--all-pending`**
  approves every pending record at once. Exactly one of {enrollment ID,
  `--peel`, `--all-pending`} must be given.

### Changed
- **`zester ca issue nats-server` now also writes `nats-ca.crt`** (the CA root)
  next to the issued cert/key, so the material peels anchor on is produced in
  the same step — no separate "export the root" seam in the manual bootstrap,
  and it lands at the path the packaged peel config expects.

### Fixed
- **Docs: the APT setup no longer fails with `NO_PUBKEY`.** The published
  signing key is ASCII-armored, so the install guide now pipes it through
  `gpg --dearmor` into the binary keyring apt expects instead of writing the
  armored bytes straight to `zester.gpg`.
- The self-update watchdog no longer logs `update-status KV bucket unavailable`
  every second during a fresh-boot startup window (before the master finishes
  storage init): it warns once, then drops the retries to Debug and logs when
  the buckets become ready.

## [0.3.2] - 2026-07-08

Correctness and security fixes from an adversarial review of the 0.3.0/0.3.1
embedded-CA and zero-config-bootstrap code.

### Security
- **Enrollment same-key recovery no longer opens a hijack race.** Recovery
  released the peer index (KV delete) before re-creating it; in that window an
  attacker submitting a different key (the endpoint is unauthenticated and the
  challenge only proves ownership of the submitted key, with a valid trust
  binding freely derivable from the public CA) could claim the identity, which
  reactor auto-approval — gating only on trust-mismatch — would then
  credential. Recovery now atomically repoints the index via CAS
  (`Store.Supersede`) and never releases it, so the different-key guard is
  never bypassable.
- **Peel discovery no longer writes an unvalidated CA bundle.** The
  recovery-loop and `_cluster_info` watch path wrote the raw discovery
  `ca_bundle_pem` verbatim to `nats-ca.crt`; it now writes only the
  pin/anchor-verified single root (mirroring the enrollment path), dropping any
  extra certificate in the bundle and refusing to introduce a brand-new,
  never-anchored root (root rotation stays a deliberate operator file drop).

### Fixed
- **Same-key credential recovery retires the old enrollment record** (transition
  to revoked, "superseded") instead of leaving a live "active" ghost that made
  `zester enroll list` show two records and `enroll revoke` act on the wrong one.
- **`ca.FirstCARoot` now selects the self-signed root anywhere in a bundle**, not
  the first CA certificate: a chain-ordered `enroll_ca` bundle (intermediate
  first) plus a root `enroll_ca_pin` no longer fails with a spurious fatal pin
  mismatch, and no-pin enrollments no longer bind the intermediate SPKI and get
  flagged trust-mismatched.
- **Peel discovery no longer clobbers an operator-provisioned NATS CA.** In a
  split-CA / external deployment where the NATS root is pre-dropped at the
  conventional `nats-ca.crt` path and `nats_ca` is unset, enrollment/discovery
  used to overwrite it with the enrollment anchor (breaking NATS TLS at the next
  reconnect); it now leaves a NATS CA that does not already trust the enrollment
  anchor untouched.
- **Zero-config convention-master peels now get NATS discovery.** The
  `https://zester:8443` fallback was applied only inside the enrollment path, so
  a peel with an empty config enrolled but never discovered endpoints and had no
  self-heal loop; master-URL resolution is now unified so the boot fetch,
  recovery loop, and cluster-info watch all target the convention master too.
- **Runtime NATS re-pointing no longer forces spurious reconnects.**
  `bus.Client.SetServers` compared the connected server against the new pool by
  raw string, but nats.go normalizes URLs (adds the default `:4222`), so a
  port-less advertised endpoint never matched and every discovery apply
  force-reconnected a healthy connection; comparison is now on normalized
  (scheme, host, port) form. The peel also no-ops a content-identical
  bootstrap-doc re-delivery (the cluster-info watch replays on every boot).
- **`_cluster_info` watch survives consumer loss.** It exited permanently when
  the JetStream watcher channel closed, leaving a healthy peel deaf to endpoint
  migrations and CA rotations until restart; it now re-establishes with capped
  backoff like the settings watchers.
- **The self-update watchdog no longer wedges when the bootstrap cache never
  appears.** With `--bootstrap-cache` it polled forever and the whole
  self-update plane went silently dark for all-in-one peels (explicit
  `nats_url`) or masters advertising no endpoints; it now starts on `--nats-url`
  and follows the cache, repointing its NATS pool if/when the cache appears or
  changes (endpoint-migration self-heal) instead of reading it once at startup.
- **The persisted enrollment trust anchor is written atomically** (temp +
  rename); a crash or `ENOSPC` mid-write previously left a truncated
  `enroll-ca.crt` that the strict read path treats as fatal, wedging the peel
  until manual deletion.

## [0.3.1] - 2026-07-08

Field-testing and CI follow-ups to the 0.3.0 embedded-CA release.

### Added
- **`zester auth init`** (offline, no NATS): generates the NATS auth
  hierarchy — operator/account/system JWTs, `account.seed` (the fleet trust
  root), `master.creds`, `admin.creds` — plus a `nats-server.conf` that
  trusts them (operator mode, MEMORY resolver, JetStream). Paired with
  `zester ca` this makes a bare-metal bootstrap fully CLI-driven; the
  operator still installs and runs their own external `nats-server`.

### Fixed
- **Peel JWT update-status grant now matches the reporter's actual KV key**
  (`$KV.update-status.peel.<id>`, was `$KV.update-status.<id>`): a
  zester-watchdog connecting with peel credentials had every status heartbeat
  rejected as a NATS permissions violation, so the node never appeared in
  `zester update status`. Re-issue peel creds (re-enroll) to pick up the
  corrected grant.
- **Credential-loss recovery is no longer a dead-end.** A peel that lost its
  credentials but still holds its identity key would re-enroll and hit a
  permanent 409 ("already has an active enrollment"). The enrollment handler
  now allows a **same-key** re-enrollment from approved/issued/active states
  (the challenge-response already proved key ownership, so a matching key is
  provably the same peel recovering); a *different* key still gets 409 — the
  peel-ID uniqueness guard blocks impersonation. The 409 message now names the
  `revoke`-to-recover path.
- **A never-connected discovery peel no longer wedges.** The re-discovery
  recovery loop was gated behind a successful NATS connection, so an
  already-enrolled peel booting with no bootstrap cache and a stale/
  unresolvable endpoint stayed offline forever. The recovery loop now runs
  from the local phase (fires even when NATS never connects), and boot-time
  resolution attempts one verified discovery fetch before falling back to the
  builtin `tls://nats:4222` tail.
- **Packaged peels no longer auto-start unconfigured.** `postinstall` enables
  but only starts `zester-peel` when `master_urls` is set — an unconfigured
  peel could otherwise fall to the convention master `https://zester:8443` and
  silently TOFU-enroll into a half-state during provisioning.
- **Operator CLI on an all-in-one box now derives its connection from the
  daemon config.** The CLI's default search hits `/etc/zester/master.yaml`
  (the master DAEMON config, which has no `master:` block) first, so on the
  master host it silently got no creds/CA and failed NATS TLS with a bare cert
  error. When there is no `master:` block the CLI now derives its NATS URL,
  CA, and admin credentials from the daemon's `nats_url`, `nats_ca`, and
  `auth_dir`/admin.creds.
- Docs: the `enroll_ca_pin` examples used the scalar form
  (`enroll_ca_pin: "sha256:…"`), which fails strict-YAML parsing against the
  `[]string` field; corrected to the list form.

## [0.3.0] - 2026-07-08

### Added
- **Embedded certificate authority + zero-config peel bootstrap.** A peel can
  now be provisioned with just `master_urls` (+ an optional `enroll_ca_pin`);
  the NATS endpoint list and CA trust bundle are discovered at enrollment and
  cached locally.
  - **`zester ca`** (offline, no NATS): `init` generates a root +
    signing-intermediate hierarchy (stdlib `crypto/x509`, ECDSA P-256) and
    prints the root SPKI pin; `fingerprint` prints the pin; `print` shows
    details + bundle; `issue nats-server|enroll` issues server certs.
  - **Master embedded-CA mode** (`ca.mode: auto|embedded|external`, default
    auto = embedded iff `<auth_dir>/ca/root.crt` exists): self-issues and
    hot-renews its enrollment HTTPS certificate in-process; new `ca` readiness
    check; config `ca.dir`, `ca.enroll_cert_validity` (90d), `ca.enroll_sans`.
  - **Discovery**: master `nats_advertise_urls` (tls:// only; loopback /
    unspecified / link-local / `0.0.0.0/8` / numeric-loopback rejected so a
    master's own `tls://localhost` view never leaks) served at unauthenticated
    `GET /api/v1/enroll/ca` (JSON bootstrap doc) and republished to the
    `_cluster_info` secrets-KV key for live refresh (new peel JWT read grant).
  - **Peel trust ladder**: `enroll_ca` file > `enroll_ca_pin` (SPKI pin) >
    persisted anchor (`<auth_dir>/enroll-ca.crt`) > system trust > TOFU.
    `enroll_trust: tofu` (default) | `strict`. Pins anchor the handshake root
    only (bundle-poisoning resistant); a mismatched pin/anchor is fatal, never
    a downgrade; TOFU fires only on genuine first contact and persists the
    anchor (Salt `minion_master.pub` semantics; break-glass `rm`). Config is
    parsed strictly (unknown keys fail startup — a typo'd pin can't silently
    fall to TOFU).
  - **Hardened TOFU binding**: the peel signs the trusted-CA SPKI into the
    enrollment submission (domain-separated, challenge- and capability-bound,
    strip-resistant); the master flags a `TrustMismatch`; `zester enroll
    list`/`show` surface it (TRUST column); `enroll approve` refuses a
    mismatched record without `--force`; reactor auto-approval refuses
    unconditionally.
  - **Peel bootstrap cache** (`<data_dir>/nats-bootstrap.msgpack`) + boot
    precedence (explicit `nats_url` > cache > discovery > builtin
    `tls://nats:4222` tail; `nats_url` default is now the empty discovery
    sentinel); offline-first boot from cache; a recovery loop re-discovers via
    the enrollment channel and repoints the live NATS pool
    (`SetServerPool`/`ForceReconnect`) without a restart when every endpoint
    goes dead; a `_cluster_info` KV watch applies live updates.
  - **Convention master**: with no `master_urls` configured, the peel tries
    `https://zester:8443` (Salt-style), enrollment path only.
  - Packaged `zester-peel.service` runs the watchdog with `--bootstrap-cache`
    (follows the peel's discovered endpoints); the packaged `peel.yaml` is now
    the 2-line zero-config form; `master.yaml` documents the `ca` and
    `nats_advertise_urls` blocks. `playground-init` issues one CA via `pkg/ca`
    (enroll + NATS share the root, satisfying ENROLL-TLS-3).
  - Security hardening (adversarial review): the peel persists only the
    pin/anchor-VERIFIED CA (never the raw bundle from the insecure first
    fetch) and re-fetches NATS endpoints over verified TLS, so a correct
    `enroll_ca_pin` protects the discovery payload too; on an embedded-CA
    master the trust binding is REQUIRED (a stripped binding is rejected,
    closing the relay-MITM masquerade), and a `TrustChecked` record field
    distinguishes a verified `ok` from an unverified external-mode report;
    the CA bundle is published to `_cluster_info` after the CA loads (the
    rotation refresh channel now carries it); embedded-leaf renewal retries
    on a short cadence after a failure; issued leaves are clamped to the
    intermediate's validity and the `ca` readiness check degrades/downs on
    approaching CA-chain expiry; the loopback advertise-gate also rejects
    trailing-dot FQDNs and hex `inet_aton` forms; multi-master CA divergence
    is warned on publish.
- The compose playground/integration stack gains **wd-01**, a peel supervised
  by `zester-watchdog` with the peel's own credentials — the packaged
  topology, previously untested anywhere. Integration tests now assert wd-01
  reports into `zester update status` (regression test for the update-plane
  JWT grants) and that a full **CA rotation is a pure file drop**: new CA +
  new NATS server cert, rolling NATS restarts, old CA dropped — every daemon
  (masters, peels, watchdog) reconnects under the new trust without a single
  process restart.
- Peel config knobs `auth_dir` (default `/var/lib/zester/auth`) and `data_dir`
  (default `/var/lib/zester`) — the credentials/trust directory and the
  runtime-state directory (settings snapshot, job dedup state, baked states
  fallback, template base path) are now configurable instead of hardcoded.

### Changed
- **Default filesystem layout moved from `/data` to FHS-proper locations.**
  Binary defaults are now `/var/lib/zester/...` for state (master:
  `auth_dir`, `states_dir`, `settings_dir`, `reactor.dir`, enrollment
  cert/key; peel: `auth_dir`, `data_dir`) and `/var/cache/zester/states` for
  the peel's regenerable state-file cache (`states_cache`); the conventional
  NATS CA fallback (`bus.DefaultNATSCAPath`) is now
  `/var/lib/zester/auth/nats-ca.crt`. Packaged configs already used these
  paths. The compose/playground stack keeps its volumes at `/data` by
  pinning the layout explicitly (playground-init's generated `master.yaml`
  and a baked `/etc/zester/peel.yaml` in `Dockerfile.peel`). Note: `/var/cache`
  may be purged by the OS; a peel with a wiped states cache re-syncs from KV
  on reconnect (the offline-critical settings snapshot lives in `/var/lib`).
- **NATS CA trust is now re-read on every (re)connect attempt** instead of
  being frozen into the TLS config at process start: `bus.ClientConfig` gains
  `CAFile`/`CAFileOptional` fields wired to a per-connect root-CA callback,
  and `bus.NATSClientTLS` returns the base TLS config plus the resolved CA
  path (resolution order: `--nats-ca`/YAML → `NATS_CA_FILE` →
  `<auth_dir>/nats-ca.crt` → system trust store). CA rotation becomes a file
  drop — master, peel, and watchdog pick up new trust at the next reconnect
  without a restart, and a CA dropped at the conventional `<auth_dir>` path
  after boot now takes effect without a restart too. Missing conventional
  file = system trust store for that attempt; explicit `nats_ca`/
  `NATS_CA_FILE` paths stay strict — a missing explicit file no longer fails
  startup (previously a hard error on master/peel and a fatal exit on the
  watchdog before the supervised child ever started) but fails the attempt
  and retries, never silently downgrading to system trust. Peel and watchdog
  retry indefinitely; the master retries through its bounded storage-init
  window (~3 minutes) and then exits with an error naming the likely NATS/CA
  cause, for its supervisor to restart.

- **APT repository moved to a self-hosted server** at `https://repo.zester.cc`
  (suite `noble`, component `main`). Each `v*` release now uploads its `.deb`s
  to the repo server, which signs and rebuilds the index on ingest; publishing
  is authenticated with the `REPO_PUBLISH_TOKEN` Actions secret. Replaces the
  GitHub Pages `/repo` approach — the `apt-repo` branch, the Pages overlay in
  the docs workflow, and `packaging/apt/*` (apt-ftparchive + GPG signing in CI)
  are removed. Install with the key at
  `https://repo.zester.cc/debian/repository.key` and
  `deb [signed-by=/usr/share/keyrings/zester.gpg] https://repo.zester.cc/debian noble main`.
- The release workflow's GitHub Release step is now idempotent — re-running an
  existing tag refreshes its assets (and re-publishes to the APT repo) instead
  of failing on "release already exists".

### Fixed
- **Packaged watchdog could never join the update plane.** Three stacked
  defects on `.deb` installs: (1) `zester-peel.service` pointed the watchdog
  at `/var/lib/zester/auth/peel.creds`, a file the peel never writes (it
  saves credentials as `<id>.creds`, previously only under the hardcoded
  `/data/auth`); (2) even with the right file, peel JWTs carried no
  update-plane grants, so a watchdog connecting with them hit NATS
  permission violations on every update operation; (3) FQDN hostnames
  produced peel IDs with dots, which enrollment rejects
  (`enroll.ValidatePeelID`), crash-looping the peel. Now: postinstall derives
  a sanitized short-hostname peel ID, writes it to both `peel.yaml` and a
  systemd drop-in (`ZESTER_PEEL_ID`) that drives the unit's `--id` and
  `--nats-creds <id>.creds` (immune to later hostname changes), and peel
  JWTs gain own-id-scoped update-plane grants (`zester.update.cmd.<id>`
  subscribe, `$KV.update-status.<id>` put, read-only `update-binaries`
  object-store download) for the colocated watchdog. Peels enrolled before
  this change need re-issued credentials for their watchdog to use the
  update plane.

## [0.2.0] - 2026-07-07

### Added
- **APT repository** (Debian/Ubuntu) — the `zester`, `zester-master`,
  `zester-peel`, and `zester-watchdog` packages (amd64) are now installable
  via `apt` from a signed repository. (Initially served from GitHub Pages;
  moved to `https://repo.zester.cc` — see Unreleased.)

### Changed
- Go module path renamed from `github.com/ptorbus/zester` to
  `github.com/nirnx/zester` following the repository move; `go install`
  commands, clone URLs, and docs updated accordingly.

## [0.1.0] - 2026-07-05

Initial release.

### Added
- **Five binaries**: `zester-master` (job dispatch, settings compilation,
  enrollment + REST API, rollout coordination), `zester-peel` (offline-first
  agent: facts, states engine, local settings resolution, scheduler),
  `zester` (operator CLI with Salt-style `'<target>' module.function`
  execution, `--test` dry runs, `--direct` mode), `zester-watchdog`
  (self-update process supervisor with atomic binary slots and auto-rollback),
  `zester-migrate` (Salt `.sls` → Zester `.zy` converter).
- **State engine**: Salt-compatible requisites (`require`/`watch`/`onchanges`/
  `onfail`/`prereq`/`listen` + `_in` inverses), `onlyif`/`unless` guards,
  `order`/`retry`/`failhard`/`names`, include/extend/highstate composition,
  25+ built-in state modules, Starlark custom modules (`_modules/*.star`).
- **Settings pipeline**: two-phase rendering with per-peel Curve25519 secret
  encryption, manifest-verified distribution, snapshot warm-start for offline
  boots.
- **State file distribution** over NATS KV with atomic local caching and
  optional GitFS sources.
- **Enrollment**: challenge-response TLS flow with manual approval, NATS
  request/reply admin service, bearer-token REST API.
- **Self-update**: fleet-wide batched rollouts with soak, readiness-gated
  health checks, confirm-deadline auto-rollback, protocol-version gating.
- **Reactor** (event-driven automation): durable `events` stream, origin-scoped
  rule matching, Jinja-rendered reactions (job dispatch, gated enrollment
  auto-actions, depth-capped chaining), service beacon, exactly-once side
  effects, loop guards (throttle/storm breaker), `zester event send|watch` and
  `zester reactor test` CLI.
- **Multi-master**: leader leases for publishing, shared durable consumers,
  orphan-job reclaim with CAS fencing.
- **Security**: TLS-only NATS, least-privilege peel JWTs, subject-token
  identity (payload identities never trusted), Ed25519 challenge-response
  enrollment.
- **Observability**: Prometheus metrics on both daemons, `/healthz` +
  `/readyz`, JSON structured logging.
- Docs site (Fumadocs) at https://zester.cc; Docker Compose playground;
  Docker-based integration suite (82 tests).
- Apache-2.0 license.

[Unreleased]: https://github.com/nirnx/zester/compare/v0.3.6...HEAD
[0.3.6]: https://github.com/nirnx/zester/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/nirnx/zester/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/nirnx/zester/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/nirnx/zester/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/nirnx/zester/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/nirnx/zester/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nirnx/zester/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/nirnx/zester/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nirnx/zester/releases/tag/v0.1.0
