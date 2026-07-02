# Zester

Go-based infrastructure automation system (SaltStack alternative). Module path: `github.com/ptorbus/zester`.

> ## ⚠️ SUPER IMPORTANT — Model policy (read first)
>
> This project runs on **Claude Fable 5**. Every subagent, Workflow `agent()` call,
> and Agent-tool spawn **MUST be pinned to Fable** (`model: 'fable'`). Do **not**
> rely on model inheritance — in this environment unpinned subagents fall back to
> the harness default (**Opus**), which the maintainer does not want.
>
> **If anything triggers a switch from Fable to Opus** (an unpinned agent, a
> Workflow run started before the pin was added, a harness fallback, or a session
> default reset — anything you observe running on Opus):
> 1. **STOP** the offending run immediately (`TaskStop`) rather than letting it finish.
> 2. **FLAG** it to the maintainer: say what switched, the root cause, and which
>    run/agent was affected.
> 3. **SUGGEST the operating pattern** that prevents a recurrence (e.g. "pin
>    `model: 'fable'` on all N `agent()` call sites in the script") and apply it to
>    the script/config.
> 4. **NOTIFY and hand back** — do NOT silently relaunch. Let the maintainer
>    restart the run with that rule in place.
>
> Belt-and-suspenders: `teammateDefaultModel` should be `claude-fable-5` (set via
> `/config`) so subagents default to Fable even if a pin is missed — but the
> explicit per-call pin remains mandatory, not optional.

## Architecture

Five binaries in `cmd/`. Both daemon `main.go`s own ONLY flag parsing, config loading, logging setup, and signal handling (~90 lines each); the runtimes live in `internal/masterd` (`Daemon`) and `internal/peeld` (`Agent`), each `New(cfg, logger)` + `Run(ctx)`.

- **zester-master** — Dispatches jobs, compiles settings, watches facts, runs heartbeat + orphan scanner + the `schedule-results` consumer (a boot failure spawns a 60s retry loop that flips the readiness check to OK on success), the target-resolution service, the enrollment admin service, and the rollout resume loop. Multi-master coordination via two advisory `bus.LeaderLease`s (see Multi-Master Coordination below): `"publisher"` gates settings-files/state-files publish + GitFS sync, `"facts-secrets"` gates secrets re-encryption. Supports `--config` flag (default: `/etc/zester/master.yaml`). Serves `/healthz` (liveness), `/readyz` (readiness — checks: `nats`, `enroll-server`, `sched-consumer`, `target-service`, `gitfs` when enabled), and `/metrics` (Prometheus) on `--health-addr` / `health_addr` YAML (default `127.0.0.1:9091`). `--log-level` / `--log-format` (JSON default) via `internal/logging`. Config structs in `internal/config/master_daemon.go`. **Flags are generated from the config struct's `flag:"..." usage:"..."` tags via `config.BindFlags`, and user-set flags are re-applied after YAML load via `config.ApplyVisited` (`internal/config/bind.go`) — a new knob is ONE tagged struct field. Precedence unchanged: flag > YAML > struct default.**
- **zester-peel** — Agent running on managed nodes; runtime in `internal/peeld`. **Offline-first startup**: `Agent.Run` is split into a LOCAL phase (health server, enrollment, NATS client with `RetryConnect`, local fact collection via `facts.Manager.Collect`, provider detection, `ModuleContext`, states engine, settings warm-start from the last-known-good snapshot `/data/settings-snapshot.msgpack`, scheduler, exec+cancel core-NATS subscriptions) and a CONNECTED-phase goroutine (facts publishing, master curve pub, initial settings resolve, basket publisher, settings watchers, statefiles sync/watch, peel heartbeat) that starts once NATS is healthy — a peel booted during a NATS outage enforces from local state and self-heals when the connection arrives (the bus client's `nats.ConnectHandler` flips `IsHealthy` on a deferred initial connect). Only genuinely local errors (bad config, missing creds + no master URL, bad TLS) are fatal. **Execution model**: mutating executions go through a bounded 64-slot queue consumed by one worker holding `execMu` (queue-full replies immediately with `"peel busy: execution queue full"`; queued jobs are cancelable); the read-only set — `facts.*` EXCEPT `facts.set`, `settings.*`/`pillar.*`, `test.ping`, `grains.*`, `sys.list_functions` — runs concurrently OUTSIDE `execMu` on immutable contexts derived via `mctxTemplate.WithFactsSettings(...)`, so `test.ping`/fact queries answer during long highstates. Job dispatches are **acked on accept** (`job.Ack` on `bus.JobAckSubject`, after fencing/dedup, before execution) and **deduped** via persisted jid→epoch state (`/data/peel-dedup.msgpack`, 0600, 4096-entry cap; `ObserveDurable` flushes synchronously before an accepted job executes; epoch ≤ stored is rejected — `pkg/job` CAS-bumps `Epoch` on every legitimate redispatch, so failover is unaffected). Exec dispatch order (`execModule` in `internal/peeld/exec.go`) unchanged: nil-`Args` guard → `state.apply` / `state.highstate` → `facts.*` → `settings.*` (+ `pillar.*` alias) → `pkg/execmod` function (only when the name is **not** a registered state module — checked live via `registry.Has`) → state-module build via the Registry, with the request's state ID as the primary-param default; ad-hoc runs wrapped with `state.WrapAttributes(state.ParseStateAttributes(args), guardRunner)`. Publishes facts `zester_version` and `protocol_version`. Supports `--config` flag (default: `/etc/zester/peel.yaml`). Serves `/healthz`, `/readyz` (checks: `nats`, `kv`), and `/metrics` on `--health-addr` / `health_addr` YAML (default `127.0.0.1:9090`; distinct from master's `:9091` so both can colocate). `--master-urls` / `master_urls` YAML: ordered enrollment-URL failover list, takes precedence over the single-URL `master_url` when non-empty (`--master-urls ""` clears a YAML list). Config structs in `internal/config/peel.go`. **Same binder convention as the master.**
- **zester** — CLI for operators. Supports Salt-style module execution (`zester '<target>' <module.function> [args...]`) with `--format text|json|yaml`, `--no-color`, `--test` (dry run: sets `test=True` in the module args; a literal `test=True` arg works too — the peel runs states in `ModeTest`), and `--direct` (bypass master, send directly to peels via NATS request/reply). Job mode resolves targets via the master target-resolution service (`target.NewServiceLister` over `zester.target.resolve`, automatic facts-KV-scan fallback with a stderr Warn) and stamps `Job.TargetExpr` (audit); `--direct` keeps the pure KV lister. `zester job list` skips `active.<jid>` index keys; `job active` enumerates the active-jobs index (no full-bucket scan); `job show` reads the per-peel return keys. `zester peel list` shows `PEEL ID / OS / ARCH / ONLINE / LAST-SEEN` from the `peel-heartbeat` bucket (absent key = offline; missing bucket = `-`; 60s freshness backstop). `zester update status` has trailing DEGRADED and PROTO columns (`-` = protocol 0, i.e. never reported). `zester enroll approve|reject|revoke` go over NATS request/reply to the masters; `--direct-kv` is the break-glass direct-KV path.
- **zester-watchdog** — Dedicated process supervisor for self-updates. Wraps zester-peel or zester-master, manages binary slots (current/previous/staging) with atomic `os.Rename`, health-monitors child via HTTP `/healthz` (liveness — a NATS outage never restarts a healthy child); the update soak phase instead polls the child's `/readyz` (`--ready-url`, derived from `--health-url` by default). Handles update commands via NATS, auto-restarts on crash with exponential backoff; after 10 consecutive failures it enters a slow-retry tier (`DegradedRetryInterval`, default 10m) instead of giving up. Stamps `NodeStatus.Protocol = proto.ProtocolVersion` into every update-status report. Uses `flag` package (not Cobra). Enrollment-aware: starts child immediately, connects to NATS when creds file appears. Validates `tls://` NATS URLs before starting the child; retries NATS/bucket setup with backoff instead of exiting (an exit used to orphan the child) and stops the child on every exit path.
- **zester-migrate** — Salt→Zester migration tool: `zester-migrate [--write] [--rename-vars] <path>` converts `.sls` files to `.zy` via `pkg/migrate`, prints a change/warning report, exits 1 when warnings need manual review. Uses `flag` package (not Cobra).

Dev CLI in `playground/`:
- **zester-cli** — Dev-only CLI (`zester-cli <target> <module> [args...]`) for direct peel access; prefer `zester --direct`.

### Package Layout

| Package | Purpose |
|---------|---------|
| `pkg/bus` | NATS JetStream client, KV helpers, codec (MessagePack), TLS, subject constants. `BumpRevision` is a CAS retry loop (Create when missing / Update with revision, ~10 attempts with jitter). `ClientConfig.RetryConnect` makes an unreachable NATS at boot non-fatal (daemons set it; the operator CLI leaves it false and fails fast); a registered `nats.ConnectHandler` flips `IsHealthy`, fires `OnReconnect`, and signals waiters when a deferred initial connect succeeds (offline-first boots self-heal). `OnReconnect`/`OnDisconnect`/`OnSlowConsumer` hooks feed the NATS transport metrics. **Replicas tiering**: `EffectiveReplicas(explicit, clusterSize)` — explicit wins, else `min(3, max(1, clusterSize))`; every bucket/stream/object-store config carries a `DurabilityTier` (`TierCritical`/`TierEphemeral` — the tier controls only warning behavior: under-replicated critical assets get a loud per-asset warning, and a failed replica upgrade falls back to R1 with a `nats stream edit` migration hint instead of aborting startup). `LeaderLease` (`lease.go`): advisory leader election over a TTL'd entry in the `leases` bucket (15s TTL / 5s renew — bucket TTL must match). `ListKeysWithPrefix(ctx, kv, prefix)`: server-side filtered key listing (`"jid"` → `"jid.>"`; a `">"`-suffixed prefix passes verbatim; empty result = `(nil, nil)`). `RequestPubSub` capability interface (see Key Interfaces). |
| `pkg/bus/bustest` | In-memory test fakes for JetStreamAPI, KeyValue, PubSub. `FakePubSub` implements `bus.RequestPubSub` (queue groups with round-robin, synchronous request/reply, `nats.ErrNoResponders` when nothing matches); `FakeKV.WatchFiltered` really filters with NATS wildcard semantics. |
| `pkg/job` | Job dispatch, watcher (ack/return collection), heartbeat, orphan recovery, active-jobs index, `job-events` stream replay. `Job` carries `Deadline` (always re-anchored by the master in `Manager.Dispatch`), `ReclaimCount` (capped at 3), `StateID` (CLI bare positional → `ExecRequest.ID`), `User` (operator identity), `TargetExpr` (operator's original targeting expression, audit-only), `V` (protocol version), and `ReturnCount`/`SuccessCount` (set at finalize). Details under Job System below. |
| `pkg/facts` | Fact collectors (OS, CPU, Memory, Disk, Network, Custom, DefaultIP), KV publish, Watch for master-side indexing. `Manager.Collect(ctx)` (local-only collection) / `StartPublishing(ctx)` (bucket + publish + interval collectors) split backs the peel's offline-first boot; `Start` equals both. `facts.Heartbeat` (`heartbeat.go`, msgpack `{ts, version, protocol}`, additive-only) is the peel-heartbeat value type. `Index` (+ `RawFacts`/`AllRawFacts`, read-only shared maps) is kept current by `WatchIntoIndex` (WatchAll replay-seeded, handles KV delete/purge, reconnects with backoff) and backs the master's target-resolution service. Custom facts read from `/etc/zester/facts` every 30s. DefaultIP uses UDP dial trick to detect outbound IPv4. |
| `pkg/basket` | Basket publisher: publishes peel facts to shared KV for cross-peel queries. `ParseFunctions()` reads settings, `Publisher` runs periodic KV writes with a per-key hash-skip (SHA-256 of sorted-key msgpack, same pattern as facts) — unchanged values are not re-put; the hash is cached only after a successful put, so a failed write retries next tick. Query-side filtering via `basket_scope` setting (compound-ANDed with every `basket()` target in `internal/peeld/basket.go`); basket target resolution goes through the master resolve service (IDs-only) with automatic KV-scan fallback. |
| `pkg/settings` | Top file parsing, template rendering, secret encryption, peel-side resolver. **Deletion manifest**: `PublishRawFiles` puts files → `_manifest` (`settings.ManifestKey`, sorted msgpack `[]ManifestEntry{key, sha256}` of the stored bytes) → best-effort prune of stale keys (never `_revision`/`_manifest`/`_master_curve_pub`; prune failures Warn and never block) → `_revision` bump; the Resolver verifies every file it loads (incl. top.zy) against the manifest — listed-but-missing, hash mismatch, and loaded-but-unlisted all fail the resolve (torn-read protection, never cached); a bucket holding settings content without a `_manifest` is a torn or tampered publish and fails the resolve too — an absent manifest is legitimate only before the master's very first publish (resolves to empty). **Secrets hash-gate**: `PublishSecrets` skips the randomized re-encrypt + put when the per-peel fingerprint (sender curve pub ‖ recipient curve pub ‖ sorted secrets) is unchanged; cache updates only after a successful put (`InvalidateSecretsCache` drops entries). `WatchSecretsAndCurve` covers the per-peel secrets key and `_master_curve_pub` with ONE `WatchFiltered` consumer; all settings watchers add deterministic per-id reconnect jitter (`SetWatchJitter`, default spread 60s, hostname fallback seed) to the first reconnect after a consumer loss. Resolver hardening under Settings Pipeline below. |
| `pkg/exec` | Execution layer: provider interfaces (`PackageExec`, `FileExec`, `CommandExec`, `ServiceExec`, `UserExec`, `GroupExec`), OS implementations, `ModuleContext` (embeds `ProviderSet`; `WithFactsSettings` derives immutable per-request copies), `DetectProviders()`. Providers: `AptProvider`, `DnfProvider`, `YumProvider`, `BrewProvider`. Files use prefix naming (`pkg_apt.go`, not OS-suffix). |
| `pkg/exec/exectest` | In-memory test fakes for execution interfaces (`FakePackageExec`, `FakeFileExec`, `FakeCommandExec`). Follows `pkg/bus/bustest` pattern. |
| `pkg/state` | State module execution engine (DAG, Runner, Registry). Requisites: `require`, `watch`, `onchanges`, `onfail`, `prereq`, `listen` and their `_in` inverses (inverse forms + prereq ordering rewritten at compile time in `pkg/state/compiler/requisites.go`). Generic per-state attributes (`pkg/state/attributes.go`, applied via `WrapAttributes`): `onlyif`/`unless` guards, `order`, `retry`, `failhard`, plus `names` expansion (compiler). `ModeTest`/`ModeCheck` back `test=True` dry runs. Built-in modules in `pkg/state/modules/`: file (`managed`/`directory`/`absent`/`append`/`symlink`/`recurse`/`blockreplace`/`line`/`replace`/`comment`/`uncomment`/`keyvalue`/`copy`/`touch`), pkg (`installed`/`removed`/`latest`/`purged`), `pkgrepo.managed`, service (`running`/`dead`/`enabled`), user/group (`present`/`absent`), cron (`present`/`absent`), `mount.mounted`, `sysctl.present`, `locale.present`, `timezone.system`, `pip.installed`, `archive.extracted`, `host.present`/`host.absent`, `ssh_auth.present`/`ssh_auth.absent`, `git.cloned`/`git.latest`, `module.run` (both `name:` and dotted-key target forms), and `test.*` (`ping`/`nop`/`fail_without_changes`/`succeed_with_changes`/`configurable_test_state`). Query modules (special-cased in peel exec handler): `facts.*`, `settings.*` |
| `pkg/execmod` | Imperative remote-execution registry (Salt-style ad-hoc ops distinct from idempotent states): `test.echo/version/true/false`, `pkg.version/list_pkgs`, `service.status/start/stop/restart`, `disk.usage`, `cmd.run`, `grains.item/items`, `sys.list_functions`. Functions take `(ctx, *exec.ModuleContext, args map[string]any)` and return a string; the bare positional arg arrives as `name` (the peel maps the request ID / `arg[0]` to it). `DefaultRegistry()`; dispatched by the peel exec handler only when the module name isn't a registered state module (state modules take precedence). |
| `pkg/state/compiler` | State compilation pipeline: include resolution (depth-first, cycle detection), state merging (duplicate state IDs deep-merged: requisites appended, other keys replaced, multi-module preserved), extend directives, multi-file state loading (dot-notation → path), state top file parsing (`top.zy`), and highstate compilation. Sits between template rendering and state building, producing a flat `[]state.State` for the existing DAG/Runner. |
| `pkg/starmod` | Starlark custom module support: users drop `.star` files into `_modules/` directories within states. `Loader` discovers and registers modules at peel startup (global `_modules/`) and at compile time (formula-specific `_modules/`). `StarlarkState` implements `state.State` (Check/Apply/Revert). Builtins bridge Starlark to `pkg/exec` providers (cmd_run, file_*, pkg_*) plus HTTP, JSON, base64, hashing, and logging. |
| `pkg/statefiles` | State file distribution: master publishes `.zy` and `.star` files to `state-files` KV bucket (with a `_manifest` for deletion propagation + integrity), peels watch KV and cache to local disk via atomic temp-dir swaps. GitFS support with validity-gated publish. Details under State File Distribution below. |
| `pkg/schedule` | Peel-side scheduler: runs modules at configured intervals or cron times. Supports splay, maxrunning, run_on_start, return_job. Config from peel.yaml (static) or settings pipeline (dynamic, hot-reloaded; snapshot-warmed at offline boot). `return_job` entries invoke `ReturnFn`, which publishes a `job.ScheduledResult` (see Scheduler Returns under Job System). |
| `pkg/update` | Self-update system: `SlotManager` (atomic binary swap), `Supervisor` (child process lifecycle; degraded is a slow-retry tier, not terminal), `Handler` (NATS update command state machine; soak polls readiness via `Supervisor.CheckReady`; soak confirm-deadline auto-rollback), `Manifest`/`BinaryStore` (KV + Object Store; `Manifest.MinProtocol` gates rollouts), `Reporter` (status heartbeat incl. `NodeStatus.Degraded` + `NodeStatus.Protocol`), `RolloutController` (fleet-wide orchestration with batches, soak, abort, driver-heartbeat resume). |
| `pkg/auth` | NKey/Curve25519 key management, encryption, JWT grants. Peel JWTs are least-privilege (`PeelUserJWTOptions`): job ack/return/schedule publish, own facts/basket/heartbeat writes, settings/secrets/state-files reads, plus publish on `zester.target.resolve`; admin JWTs (`AdminUserJWTOptions`) grant `zester.admin.>` and `zester.target.resolve`. Heartbeat puts and basket target resolution both ride these grants: a peel whose credentials lack them looks offline in presence views (heartbeat puts fail at Debug, retried every tick) and falls back to facts-KV scans for basket resolution (degraded, not broken). |
| `pkg/template` | Jinja2-style `.zy` template engine. Salt-compat `salt['mod.func'](...)` accessor (`module_accessor.go`) dispatches execution-module calls during rendering via `EngineConfig.ModuleFn`. It is source-scan based: a regex pre-scans the template for **literal** `salt[...]` subscripts (gonja has no dynamic-subscript hook), so dynamic names (`salt[var]`) and `salt[...]` inside `{% include %}`d files need the `salt_call('mod.func', args..., kw=...)` fallback function. The peel wires `ModuleFn` to `moduleDispatch` in `internal/peeld/dispatch.go`: `grains.get/item/items` resolve against local facts, `pillar.*`/`settings.*` against cached settings, everything else routes to `pkg/execmod` (positional `arg[0]` best-effort mapped to `name`), bounded by the 60s `moduleDispatchTimeout`. |
| `pkg/proto` | Shared message types (ExecRequest/ExecResponse, StateResult) + wire versioning: `ProtocolVersion = 1` (`version.go`); `ExecRequest.V`/`ExecResponse.V` (msgpack `v,omitempty`) stamped by senders — decoded `V == 0` means the field was never set and MUST be treated as compatible. The **additive-only policy** is normative in `doc.go`: never rename/retype/remove a msgpack field (retired keys stay reserved forever), new fields are `,omitempty` with safe zero values; bump `ProtocolVersion` only for semantically breaking changes and gate rollouts via `update.Manifest.MinProtocol`. Key-set conformance tests pin the exact encoded keys of the wire structs. |
| `pkg/target` | Target matching (glob, pcre, fact, settings, compound, list) + the master-side resolution service (`service.go`): `StartResolveService` (QueueSubscribe on `zester.target.resolve`, default queue `DefaultResolveQueue` = `"zester-target-resolvers"`), `IndexResolveFunc`/`IndexLister` over `facts.Index`, `ServiceLister` (client-side `PeelLister` + `BulkPeelLister` + `ExprResolver`; ANY failure — no responders, timeout, decode/remote error — Warn-logs and retries via the wrapped fallback lister), `ServiceBasketQuery` (WantFacts variant, no built-in fallback), `ParseType`. `target.Resolve` delegates whole expressions to listers implementing `ExprResolver` first, so call sites upgrade with a one-line lister swap and only matched peel IDs cross the wire. |
| `pkg/enroll` | Peel enrollment: state machine, KV-backed store, credential issuance. `AdminRequest{id, operator, reason}`/`AdminResponse{record, err}` (`admin.go`, msgpack) are the request/reply wire types for the masters' enrollment admin service. `ClientConfig.MasterURLs` gives ordered multi-master failover: rotate on connection-level failures/5xx across all operations (nonce, submit, status poll/SSE, creds download), pin the URL on success, never rotate on 4xx; a 401 "challenge verification failed" transparently re-requests a fresh nonce and resubmits (3 total attempts). |
| `pkg/masterapi` | Master REST API: bearer-token auth from `api.tokens` files (startup warning unless file perms are 0600). Routes: `POST /api/v1/jobs`, `GET /api/v1/jobs/{jid}`, `GET /api/v1/enrollments`, `POST /api/v1/enrollments/{id}/approve`, `POST /api/v1/enrollments/{id}/reject`, `POST /api/v1/enrollments/{id}/revoke` (reject/revoke take an optional JSON body `{"reason"}`); optional unauthenticated Swagger UI / OpenAPI docs (`api.docs_enabled`, default **false**; `--api-docs` flag). Mounted on the enrollment TLS listener via `enroll.ServerConfig.ExtraRoutes`; protected routes are only registered when at least one token entry exists. |
| `pkg/migrate` | Salt `.sls` → Zester `.zy` conversion used by `zester-migrate`: rule-based rewrite (salt function mapping, mlist detection, octal modes, requisite warnings, optional grains→facts / pillar→settings rename via `Options.RenameVars`) plus report formatting. |
| `internal/peeld` | Peel daemon runtime (`Agent`; `New(cfg *config.PeelConfig, logger)` + `Run(ctx)`; `Run`'s internal `runCtx` derives from Background and outlives shutdown defers). Files: `agent.go` (two-phase Run), `connected.go` (connected phase + heartbeat), `handler.go` (subscriptions, exec queue, ack/dedup gate), `exec.go` (`execModule` + read-only fast path), `dispatch.go` (template `moduleDispatch`), `snapshot.go` (settings snapshot), `dedup.go` (persisted JID dedup), `basket.go`, `schedule.go`, `modules.go`, `health.go`. |
| `internal/masterd` | Master daemon runtime (`Daemon`; `New(cfg *config.MasterDaemonConfig, logger)` + `Run(ctx)`). Files: `daemon.go` (composition), `nats.go` (bounded connect/storage-init retries), `lease.go` (`"publisher"`/`"facts-secrets"` leases), `jobs.go` (dispatch subscription, orphan scanner, sched-consumer + boot retry), `facts.go` (facts watch → index + lease-gated secrets), `settings.go`/`statefiles.go` (lease-gated publish, GitFS), `target.go` (resolve service + connected-peels gauge), `admin.go` (enroll admin queue service), `enroll.go`, `rollout.go` (+ resume loop start in daemon.go), `health.go`. |
| `internal/health` | HTTP health-check library backing both daemons' `GET /readyz`: `Checker` runs named checks in parallel (2s per-check timeout) and serves a JSON summary. HTTP 503 ONLY when a check is `down`; `degraded` means working-but-impaired and stays 200. Peel checks: `nats`, `kv` (JetStream bucket-info round-trip). Master checks: `nats`, `enroll-server`, `sched-consumer` (boot failure is retried every 60s and flips back to OK), `target-service` (Down until the resolve service starts; startup failure is non-fatal — targeting falls back to KV scans), `gitfs` (only when GitFS enabled: degraded before first sync or when stale — standby masters stay degraded until they hold the publisher lease — down if the syncer exits). `GET /healthz` remains a minimal inline liveness handler returning `{"status":"ok","component":"<c>","version":"<v>"}`. |
| `internal/metrics` | Prometheus registries (`NewMasterRegistry`, `NewPeelRegistry`); `Handler()` is mounted at `GET /metrics` on both daemons' `health_addr`. Master (wired): `zester_jobs_total{status}` + `zester_job_duration_seconds{function}` (via `OnJobFinalized`), `zester_job_reclaims_total` (via `OrphanScanner.OnReclaim`), `zester_facts_sync_total`, `zester_connected_peels` (gauge fed every 15s from `peel-heartbeat` bucket key counts), `zester_nats_reconnects_total`/`zester_nats_disconnects_total`/`zester_nats_slow_consumers_total` (via `bus.ClientConfig` hooks). Peel (wired): `zester_peel_state_apply_total{state,result}` + `zester_peel_state_apply_duration_seconds`, `zester_peel_connected` gauge (honest: 0 until NATS actually connected), `zester_nats_reconnects_total`, `zester_nats_slow_consumers_total`, `zester_peel_uptime_seconds`. |
| `internal/logging` | Shared slog bootstrap: `Setup(w, component, level, format)` — JSON default, `--log-level`/`--log-format` on all three daemons, base attrs `component` + `version` on every line (daemons add `master_id`/`peel_id` via `logger.With` once known). Invalid level/format values fail startup with an error listing valid options. |
| `internal/config` | Daemon config structs (`master_daemon.go`, `peel.go`) plus the flag binder (`bind.go`): `BindFlags(fs, cfg)` registers flags from `flag:"name" usage:"..."` struct tags (defaults from current field values); `ApplyVisited(fs, cfg)` writes back only user-set flags after YAML load. Supported types: string, bool, int, int64, `time.Duration`, float64, `[]string` (comma-separated). |
| `internal/version` | Build-time version info (`Version`, `GitCommit`, `BuildDate`) injected via ldflags. |

## Key Interfaces (bus package)

Production code uses narrow interfaces defined in `pkg/bus/jsapi.go` and `pkg/bus/kv_iface.go`:

- **`bus.JetStreamAPI`** — 5 methods (CreateOrUpdateKeyValue, KeyValue, DeleteKeyValue, CreateStream, Publish); the KV methods return `bus.KV`. Satisfied by the `bus.JS` adapter in production, `bustest.FakeJS` in tests.
- **`bus.KV`** — Bus-owned narrow KV interface (Get/Put/Create/Update/Delete/Keys/ListKeys/ListKeysFiltered/Watch/WatchAll/WatchFiltered) with bus-owned `KVEntry` (Key/Value/Revision/Operation), `KeyWatcher` (nil-sentinel end-of-replay, channel closes on consumer loss), `KeyLister`, op consts (`KVOpPut`/`KVOpDelete`/`KVOpPurge`), and error aliases `bus.ErrKeyNotFound`/`ErrKeyExists`/`ErrNoKeysFound` (aliases of the jetstream sentinels, so `errors.Is` works with either). Domain packages hold no nats.go KV types; `jetstream.KeyValue` is adapted once in `pkg/bus/kv_jetstream.go`.
- **`bus.PubSub`** — Publish + Subscribe. Satisfied by `bus.NATSPubSub` (wraps `*nats.Conn`) in production, `bustest.FakePubSub` in tests.
- **`bus.RequestPubSub`** — capability interface: `PubSub` + `Request(ctx, subject, data)` + `QueueSubscribe(subject, queue, handler)`. `bus.PubSub` itself is deliberately unchanged (adding methods would break third-party implementers) — consumers accept `bus.PubSub` and type-assert. `NATSPubSub` wraps errors so `errors.Is(err, nats.ErrNoResponders)` works; `bustest.FakePubSub` implements it too.
- **`bus.Msg`** — Simple struct with Subject + Data fields (replaces `*nats.Msg`); `bus.NewMsg` wires a responder for request/reply fakes.
- **`bus.ConsumerAPI`** — `CreateOrUpdateConsumer` only (defined in `pkg/bus/kv.go`); used for durable stream consumers (`schedule-results`) and the job-events return replayer. Kept separate from `JetStreamAPI` to avoid breaking existing test fakes.

At the production boundary, `Client.JetStream()` returns `*bus.JS`, an adapter over the raw `jetstream.JetStream` that implements `JetStreamAPI`, `ConsumerAPI`, and `ObjectStoreAPI` (call `Unwrap()` for the raw context; `bus.NewJS` wraps a self-built one):
```go
jobMgr := job.NewManager(bus.NewNATSPubSub(client.Conn()), client.JetStream(), masterID, logger)
```

## Two-Layer Module Architecture (Execution + State)

State modules follow a two-layer design modeled after Salt's execution/state split:

- **Execution layer** (`pkg/exec/`): Stateless, imperative provider interfaces + platform-specific implementations. Reusable across multiple state modules.
- **State layer** (`pkg/state/modules/`): Thin idempotent wrappers (Check → Apply → Revert) that delegate system calls to the execution layer.

### ModuleContext

`exec.ModuleContext` is the Go equivalent of Salt's `__salt__` + `__grains__` + `__pillar__`. It **embeds `ProviderSet`** (field promotion keeps `mctx.Package`-style selectors compiling; composite literals must nest: `exec.ModuleContext{ProviderSet: exec.ProviderSet{File: f}}` — adding a provider now touches only ProviderSet/DetectProviders/interface/fake). The peel builds one immutable template (`Agent.mctxTemplate`, frozen after `RenderTemplate` is set) at startup and derives per-request copies via `WithFactsSettings(facts, settings)` — a shallow copy that shares the ProviderSet/Logger/RenderTemplate and never mutates the receiver. Mutating module executions are serialized on the single exec worker behind `execMu`; the read-only module set runs concurrently on derived contexts (see the zester-peel bullet).

### Provider Detection

`exec.DetectProviders(facts, logger)` selects providers based on `os.family` from locally collected facts. Called once during the peel's local startup phase (`internal/peeld/agent.go`).

### Dependency Injection

State modules receive executors via closure-based builder factories:

```go
// NewPkgInstalledBuilder captures the ModuleContext and returns a state.Builder
func NewPkgInstalledBuilder(mctx *exec.ModuleContext) state.Builder {
    return func(id string, config map[string]any) (state.State, error) {
        // ... uses mctx.Package internally
    }
}
```

The `Builder` type, `State` interface, `Registry`, `Runner`, and `DAG` are unchanged.

### Provider Constructors

Package providers receive `CommandExec` in their constructor (e.g., `NewAptProvider(cmdExec)`) — they shell out to package manager CLIs via the injected `CommandExec`. No circular dependencies.

## Requisites & Generic State Attributes

States declare dependencies via `Reqs() Requisites`. The `Requisites` struct has four fields: `Require`, `Watch`, `OnChanges`, `OnFail` (all `[]string`). `AllDeps()` returns the deduplicated union of all lists — used by the DAG for ordering edges.

`ParseRequisites(config map[string]any)` extracts the four requisite types from a state config map, replacing manual `config["require"].([]any)` boilerplate. Supports both string format (`"pkg.installed:nginx"`) and Salt-style dict format (`{"pkg": "nginx"}` — shorthand module names resolve via `saltShorthandMap`: pkg→pkg.installed, file→file.managed, service→service.running, cmd→cmd.run, user→user.present, group→group.present; unknown modules pass through as-is).

The Runner's `resolveRequisites()` evaluates requisite semantics per-state:
- **Require**: any failed dep → skip (`require_failed`)
- **Watch**: any failed dep → skip (also `require_failed`); any changed dep → `forceApply=true` (skips Check, goes straight to Apply)
- **OnChanges**: any failed dep → skip; none changed → skip (`onchanges_not_met`)
- **OnFail**: none failed → skip (`onfail_not_met`)

In ModeRevert, only require-failure propagation applies (no watch/onchanges/onfail/prereq semantics).

### Compile-Time Rewrites (`pkg/state/compiler/requisites.go`)

`transformRequisites()` runs after merge/extend and before state building:
- **`listen`** is aliased to `watch` on the same state; `listen_in` injects `watch` onto its target. Zester runs watch-triggered applies during the run rather than deferring them to the end; the apply-on-change effect matches Salt, only the timing differs.
- **`_in` inverses** — `require_in`, `watch_in`, `onchanges_in`, `onfail_in`, `listen_in`, `prereq_in`: state A declaring `<req>_in: [B]` has the key dropped and the corresponding *forward* requisite (referencing A as `module:id`) injected onto B.
- **`prereq`** — A declaring `prereq: [B]` gets `require: [A]` injected onto each target B (so A is ordered before B); A keeps `prereq` for the runtime gate.

### prereq Runtime Gate

At run time (ModeApply/ModeCheck), a state with prereq targets (via the `Prereqer` interface) calls each target's `Check`: if at least one target would change, the state runs with `forceApply=true`; if none would change, it is skipped with SkipReason `prereq_not_met`. Missing targets or Check errors are logged and count as "would not change".

### Generic Attributes (`pkg/state/attributes.go`)

`ParseStateAttributes(config)` recognizes `onlyif`, `unless`, `order`, `retry`, `failhard`, `prereq`; `WrapAttributes(inner, attrs, guards)` decorates a state with them (returns `inner` unchanged when attrs are zero; embeds the inner State so `Name()`/`Reqs()` pass through). The compiler applies this in `buildOne()` to every compiled state, and the peel exec handler applies it to ad-hoc single-module runs — **individual state modules never parse these keys**.

- **onlyif / unless** — shell guards run via `GuardRunner` (peel wires its `CommandExec`; nil runner = guards always satisfied). `onlyif`: ALL commands must exit 0 for the state to run. `unless`: state is skipped if ANY command exits 0. Guard-not-met is a **no-op**, not an error: Check/Apply report no change with diff `"skipped: guard condition not met"`. Guards are evaluated before both Check and Apply (Apply too, because watch-forced applies bypass Check).
- **order** — int, or `"first"` (−1000000) / `"last"` (1000000). Sorts states *within* a DAG level (lower first, name as tiebreak); states without an order sort as 0. Never overrides requisite edges.
- **retry** — bare int (`retry: 3`) or Salt map form (`retry: {attempts: 3, interval: 10}` — interval in **seconds**, default 10s). Retries the state on error; applies only in apply/revert modes, never in check/test mode.
- **failhard** — truthy value; when the state fails, all remaining DAG **levels** are skipped with SkipReason `failhard_abort` (states in the same level still complete).
- **names** — `ParseNameList()`; the compiler expands a `names:` list into one state per name, each using the name as its state ID and `name` param (so requisites can target it by name).

### Dry Runs (ModeTest)

`state.ModeTest` is an alias for `ModeCheck` (`pkg/state/runner.go`): a read-only run that reports which states would change. Triggered by the CLI `--test` flag or a `test=True` module arg (`isTestArg` accepts bool `true` or truthy strings: true/yes/1/on, case-insensitive); the `ExecResponse` carries `Test: true`.

## Testing

### No Embedded NATS

Tests use in-memory fakes from `pkg/bus/bustest`. There is NO embedded NATS server dependency.

- `bustest.NewFakeJS()` — In-memory JetStreamAPI with KV buckets and stream stubs
- `bustest.NewFakeKV(name, ttl)` — In-memory `bus.KV` implementation with TTL, CAS, Watch/WatchAll/WatchFiltered (real wildcard filtering)
- `bustest.NewFakePubSub()` — Synchronous pub/sub with NATS wildcard matching (`*`, `>`), queue groups (round-robin, one member per group per message), and request/reply (`bus.RequestPubSub`; returns `nats.ErrNoResponders` when nothing matches)

### Execution Layer Fakes

State module tests use in-memory fakes from `pkg/exec/exectest`:

- `exectest.NewFakePackageExec(name)` — Tracks installed packages, records operations, supports `PreInstall()` for setup
- `exectest.NewFakeFileExec()` — In-memory filesystem with `PreCreate()` for setup, `GetFile()` for assertions
- `exectest.NewFakeCommandExec()` — Records calls, returns configurable results via `SetResult()`/`SetError()`

State module tests that need real OS interaction (file tests) use `exec.OSFileExec{}` directly with `t.TempDir()`.

### Test Setup Pattern

```go
// For packages that need both pub/sub and KV (e.g., pkg/job):
func testSetup(t *testing.T) (bus.PubSub, bus.JetStreamAPI) {
    js := bustest.NewFakeJS()
    bus.InitializeStorage(ctx, js) // creates default buckets + streams
    return bustest.NewFakePubSub(), js
}

// For packages that only need KV (e.g., pkg/facts, pkg/settings):
js := bustest.NewFakeJS()
bus.InitializeStorage(ctx, js)
```

### bus package tests

`bus_test.go` uses `package bus_test` (external test) to avoid import cycle (`bus` -> `bustest` -> `bus`). KV tests use an inline `testJS` helper that delegates to `bustest.FakeKV`. Other packages (facts, job, settings) can import bustest directly since there's no cycle.

### Running Tests

```bash
go test -count=1 ./...          # full suite
go test -count=1 ./pkg/job/     # single package
```

FakeKV checks TTL on access (no background purge), so heartbeat expiry tests use short sleeps: `time.Sleep(ttl + 100*time.Millisecond)`.

### Integration Tests

Docker-based end-to-end tests using testcontainers-go. Located in `integration/`. Start the full compose stack (NATS, master, 5 peels, admin) and exercise real module execution via `zester --format json`.

Run: `go test -v -tags integration -count=1 -timeout 5m ./integration/`
Requires: Docker daemon running.

**When adding new features, add corresponding integration tests** to verify end-to-end behavior in the Docker playground.

### CI

`.github/workflows/ci.yml` runs gofmt, `go vet`, a `go mod tidy` check, `go build ./...`, unit tests, and the race detector on core packages.

## Go Filename Convention

**Do not** name files `*_js.go`, `*_linux.go`, `*_darwin.go`, `*_amd64.go`, etc. unless you intend a build constraint. Go treats `_GOOS` and `_GOARCH` suffixes as implicit build tags. Example: `fake_js.go` was silently excluded on `darwin/arm64` because Go interpreted `_js` as `GOOS=js` (WebAssembly).

## Codec & Protocol Versioning

All NATS messages use MessagePack encoding via `bus.Encode()`/`bus.Decode()`. Struct tags: `msgpack:"field_name"`.

Wire evolution is **additive-only** (normative policy in `pkg/proto/doc.go`): never rename/retype/remove a msgpack field name (retired keys stay reserved forever); new fields MUST be `,omitempty` with zero values safe for old AND new readers. `pkg/proto.ProtocolVersion` (currently 1) is the wire-protocol generation — senders stamp it into the `V` fields on `ExecRequest`/`ExecResponse`/`Job`; decoded `V == 0` means the field was never set and is always compatible. Bump `ProtocolVersion` only for semantically breaking changes; rollouts gate on it via `update.Manifest.MinProtocol` vs the fleet's reported `NodeStatus.Protocol`. Peels also publish a `protocol_version` fact.

## NATS TLS

`tls://` URLs are mandatory everywhere: `bus.ValidateTLSNATSURLs` rejects non-`tls://` URLs on master, peel, and watchdog. Defaults are `tls://nats:4222` (master/peel) and `tls://localhost:4222` (watchdog, packaged configs). CA certificate resolution order in `bus.NATSClientTLS`: explicit `--nats-ca` flag / `nats_ca` YAML → `NATS_CA_FILE` env var → `/data/auth/nats-ca.crt` if present → system trust store.

## Multi-Master Coordination (Leader Leases)

`bus.LeaderLease` is advisory leader election over a TTL'd KV entry in the `leases` bucket (15s TTL / 5s renew — the bucket TTL must match; `IsLeader` may be stale up to one TTL, so holders must tolerate brief dual ownership). Masters run two leases:

- **`"publisher"`** — gates settings-files publish, state-files publish, and the GitFS sync loop. Publishes run in a per-acquisition sub-context cancelled on lease loss; settings LOADING and secret extraction still run on every master, so takeover is instant. Standby masters log "publisher lease candidate started; standing by until acquired".
- **`"facts-secrets"`** — gates only the `PublishSecrets` call in the facts watcher (enrollment `MarkActive` stays ungated), so exactly one master reacts to facts churn with re-encryption.

Up to one TTL (~15s) of dual publishing during partitions is tolerated by design — all gated writes are idempotent Puts and/or hash-gated. On lease handover the new leader republishes the full file sets, so peels see one redundant revision bump per failover.

## Target Resolution Service

Master-side request/reply service backed by an in-memory facts index — replaces the O(fleet) full-facts-bucket scans for targeting and `basket()`. Every master maintains a `facts.Index` via `facts.WatchIntoIndex` (WatchAll replay-seeded, handles KV delete/purge) and serves `target.StartResolveService` on `zester.target.resolve` (queue group `zester-target-resolvers` — one master answers each request; runs on all masters, not lease-gated). Wire: `ResolveRequest{expr, type, want_facts}` → `ResolveResponse{peels, facts, err}` (msgpack, additive). Clients — the CLI's job mode and the peel's `basket()` (`internal/peeld/basket.go`) — wrap their KV lister in `target.NewServiceLister(ps, 5*time.Second, fallback, logger)`: `target.Resolve` auto-delegates whole expressions via `ExprResolver`, only matched peel IDs cross the wire, and ANY failure Warn-logs (`"target: resolve service unavailable, falling back to facts KV scan"`) and retries via the fallback — mixed-version fleets degrade to the old scan path. `--direct` mode keeps the pure KV lister. The master's `target-service` readiness check reports Down until the service starts (startup failure is non-fatal).

## Job System

- Jobs have CAS-protected ownership via KV (`jobsBucket.Create` for idempotent dispatch, `jobsBucket.Update` for fencing)
- Each master has a unique KSUID-based `masterID`
- **Active-jobs index**: the jobs bucket holds two key families — `<jid>` (msgpack `Job`) and `active.<jid>` (`job.ActiveJobKeyPrefix`; msgpack `ActiveJobEntry{owner, updated}`). Dispatch Creates the active key right after the job record; the orphan scanner enumerates ONLY `bus.ListKeysWithPrefix(jobsKV, "active.>")` — O(active jobs), independent of the 7-day retention — rewrites the key on reclaim, and self-heals stale keys (missing or terminal job record → delete + skip). The key is deleted on every terminal transition (Warn-only on failure; a CAS-fenced finalize deliberately leaves it for the superseding owner/scanner). Scheduler-synthetic jobs never get index entries. `zester job list` skips `active.*` keys; `job active` is built on the index.
- **Dispatch ordering invariant**: Create(claimed) → active key → dispatch event → CAS to `StatusRunning` → publish ExecRequests → start watcher. A CAS-to-running failure returns an error WITHOUT publishing, so `StatusClaimed` in KV provably means never-published — reclaiming a claimed job re-dispatches safely by construction (same CAS-before-publish inversion on `ReclaimJob`'s claimed path). A retried Dispatch of an own interrupted claim resumes from the running-CAS step and publishes exactly once relative to KV state.
- `Job.Deadline` (msgpack `deadline,omitempty`) is ALWAYS re-anchored by the master in `Manager.Dispatch` (client clocks untrusted). Recovered watchers honor the remaining budget; an expired deadline finalizes immediately from collected returns; a zero deadline is defensively normalized to `Created + Timeout` (`normalizeDeadline`) before use
- `Job.StateID` (msgpack `state_id,omitempty`) carries the CLI's bare positional (e.g. `zester '*' pkg.version nginx` → "nginx") and is forwarded as `ExecRequest.ID`, so job mode behaves like `--direct`
- `Job.User` is populated from the operator identity (os/user → `$USER` → "unknown"); shown in `zester job list` / `job active` (USER column) and `job show` JSON. `Job.TargetExpr` stores the operator's original targeting expression (audit-only, never interpreted by pkg/job); `Job.V` is stamped from `proto.ProtocolVersion` by `NewJob` (decoded 0 = never set, always compatible). The master logs every accepted dispatch at Info with jid, user, function, target count. `--direct` mode bypasses job records entirely (unattributed)
- **Per-peel returns are the source of truth**: the watcher persists each return to its per-peel key `"<jid>.<peelID>"` via a buffered channel (256) + dedicated writer goroutine (synchronous-persist fallback on overflow — never drops; guaranteed drain before finish/detach/finalize; failed writes re-queued). `finalizeJob` no longer writes the ~1MB aggregated value — the job record carries `ReturnCount`/`SuccessCount`, and `GetReturns`/recovery list the per-peel keys via `bus.ListKeysWithPrefix` — the per-peel keys are the only returns storage (scheduled results included)
- **Ack + re-dispatch reconciliation**: peels publish `job.Ack` (msgpack) on `bus.JobAckSubject(jid, peelID)` when ACCEPTING a job dispatch — after epoch/dedup fencing, before execution. Dispatch watchers re-publish the ExecRequest exactly ONCE (same Epoch) to targets that neither acked nor returned within `Manager.AckWindow` (default `DefaultAckWindow` = 5s; negative disables), distinguishing "never received" from "still executing" — safe because peels dedup on persisted jid→epoch (an acked-but-slow peel is never re-sent). Recovery watchers never re-dispatch
- **Reclaim replays the durable stream**: reclaiming a running job replays the `job-events` stream for that JID (`NewStreamReturnReplayer`: ephemeral pull consumer, DeliverAll, read-until-idle, subject-token-authoritative peel identity; auto-wired in `NewManager` when the JetStreamAPI also implements `bus.ConsumerAPI`) TWICE — once before the recovery watcher starts (seeding; KV-persisted returns win on collision) and again after its live subscriptions attach (`Watcher.Subscribed` + `MergeReturns`), so a return published in the replay→subscribe gap isn't lost. Seeds covering all targets finalize immediately instead of burning the remaining deadline
- Heartbeater writes to `master-heartbeat` bucket (TTL 15s, interval 5s)
- `ListLiveMasters` returns real KV errors (empty bucket ≠ error); a per-key heartbeat read error aborts the scan — only key-not-found (= expired) counts as dead
- OrphanScanner aborts the whole cycle on liveness errors and reclaims via CAS only after an owner is absent for `MissThreshold` (default `DefaultMissThreshold`=2) consecutive scan cycles — reclaim latency is ~heartbeat TTL + 2×20s scan cycles (~55s worst case). `Job.ReclaimCount` (msgpack `reclaim_count,omitempty`) is incremented per reclaim; after 3 reclaims (`maxReclaims`) the job is CAS-finalized failed (`Metadata["failed_reason"]`) instead of re-dispatched — stops ownership ping-pong
- Metric hooks: `Manager`/`Watcher.OnJobFinalized(function, status, duration)` fires on every finalize (not on CAS-fenced finalizes or shutdown detach); `OrphanScanner.OnReclaim` fires per reclaimed job

### Scheduler Returns (return_job)

Peels never write to the `jobs`/`job-returns` KV buckets. A schedule entry with `return_job: true` publishes a `job.ScheduledResult` (MessagePack) on the peel-scoped subject `zester.job.<jid>.schedule.<peelID>` (`bus.JobScheduleSubject`, `pkg/job/schedresult.go`). The existing `job-events` JetStream stream captures it durably (7-day retention), so results survive master downtime. All masters share the durable consumer `schedule-results` (`job.StartScheduledResultConsumer`, filter `bus.JobScheduleWildcard()`), which persists the synthetic job record (idempotent `jobsBucket.Create`, metadata `source: schedule`) plus the per-peel return. The reporting peel's identity comes from the NATS-permission-enforced subject token, never the payload: the peel JWT (`auth.PeelUserJWTOptions`) allows publishing only `zester.job.*.schedule.<peelID>` (and `zester.job.*.ack/return.<peelID>`) and carries no `$KV.jobs` / `$KV.job-returns` grants, so one compromised peel cannot read, forge, or overwrite other peels' job records or returns.

### Peel Heartbeat & Presence

Peels Put `facts.Heartbeat{ts, version, protocol}` (msgpack, additive-only) under `<peelID>` into the `peel-heartbeat` bucket (`bus.BucketPeelHeartbeat`, TTL 30s, history 1) every 10s from the connected phase; failures are Debug-logged and retried each tick. A peel is offline after ~3 missed beats. `zester peel list` derives ONLINE/LAST-SEEN from the bucket (absent key = offline; missing bucket = columns show `-`), and the master feeds the `zester_connected_peels` gauge from bucket key counts every 15s. The heartbeat write is authorized by the peel JWT's `$KV.peel-heartbeat.<peelID>` grant.

## Settings Pipeline

Two-phase rendering:
1. **Master side**: Sanitizes `.zy` files (replaces `!encrypted` values with `__ZESTER_SECRET:key__` placeholders), publishes to shared KV (files → `_manifest` → best-effort stale-key prune → `_revision` bump; publisher-lease-gated in multi-master), encrypts secrets per-peel (hash-gated, `facts-secrets`-lease-gated)
2. **Peel side**: Resolver loads raw files from KV, verifies them against the `_manifest`, evaluates `top.zy` targeting, renders templates with local facts, decrypts secrets

Resolver / publisher hardening:
- **Manifest verification**: when `_manifest` (`settings.ManifestKey`, sorted `[]ManifestEntry{key, sha256}` of the stored bytes) exists, top.zy and every matched file must be listed with a matching hash — listed-but-missing, hash mismatch, and loaded-but-unlisted all FAIL the resolve (torn-read protection, never cached). The manifest is mandatory: settings content without a `_manifest` fails the resolve as a torn or tampered publish; an absent manifest is legitimate only before the master's very first publish (resolves to empty). Stale-key pruning on publish is best-effort GC (failures Warn, never block the `_revision` bump — unlisted keys are inert because loading is manifest-selective).
- **Revision sampling**: `Resolve` samples `_revision` at entry and re-reads it at cache-write time, caching only when both match (and the invalidation generation is unchanged) — a publish landing mid-resolve can never pin the old batch under the new revision.
- `Resolver.Resolve` FAILS (and never caches) when unresolved `__ZESTER_SECRET:...__` placeholders survive the final merge — states can never be applied with placeholder strings as values.
- Secrets rotation applies without a settings-files revision bump: the peel's secrets-watch callback calls `resolver.InvalidateCache()` before the debounced re-resolve; an invalidation-generation counter prevents an in-flight resolve from re-caching pre-rotation values.
- **Secrets write amplification is gone**: `PublishSecrets` keeps a per-peel fingerprint cache (SHA-256 of sender curve pub ‖ recipient curve pub ‖ length-prefixed sorted secrets) and skips the randomized re-encrypt + KV put when unchanged (cache updated only after a successful put), and only the `facts-secrets` lease holder reacts to facts updates.
- **Snapshot warm-start**: the peel persists last-known-good resolved settings to `/data/settings-snapshot.msgpack` (0600, atomic rename, hash-gated) on every successful resolve and loads it at boot to warm `cachedSettings`, `basket_scope`, and the scheduler — offline boots enforce with snapshot settings.
- Peel exec path fails closed: on exec-time resolve error, `state.apply` / `state.highstate` fall back to last-known-good `cachedSettings` (Warn logged); if the peel has never resolved successfully (not even from the snapshot), the execution FAILS with an explanatory `ExecResponse` error. States are never applied with nil settings.
- Settings re-resolution runs on the peel's serialized exec-worker path (template rendering can invoke execution modules via `salt['mod.func']`); template-invoked module calls are bounded by the 60s `moduleDispatchTimeout`.

## Basket Scope

Query-side filtering for multi-cluster basket isolation. Configured via the `basket_scope` setting (e.g., `basket_scope: "G@cluster_name:{{ facts.cluster_name }}"`). The peel's `makeBasketFunc` in `internal/peeld/basket.go` accepts a `scopeFn func() string` callback; when the scope is non-empty, it compound-ANDs it with the caller's target using parentheses — `"(" + tgt + ") and (" + scope + ")"` — so `a or b` targets cannot bypass the scope. The scope value is stored in a `sync.RWMutex`-protected variable, seeded from initial settings resolution (or the snapshot) and refreshed on settings changes. Target resolution goes through the master resolve service via `target.NewServiceLister` (IDs-only round trip) with automatic fallback to the facts-KV scan.

## Enrollment System

Manual-approval enrollment flow for new peels. Replaces out-of-band `.creds` distribution with a challenge-response HTTP API on the master.

### Components
- **`pkg/enroll`** — State machine (`Record`, `State`), KV-backed store (`Store`), challenge nonces (`ChallengeStore`), credential issuance (`CredentialIssuer`), HTTP handler (`Handler`), TLS server (`Server`), peel client (`Client`), admin request/reply wire types (`AdminRequest`/`AdminResponse`), file persistence helpers
- **HTTP API (peel-facing)** — `GET /api/v1/enroll/nonce`, `POST /api/v1/enroll`, `GET /api/v1/enroll/{id}/status`, `GET /api/v1/enroll/{id}/creds`. TLS is mandatory — server refuses to start without cert/key. The same TLS listener also mounts the token-authenticated master REST API via `enroll.ServerConfig.ExtraRoutes` (see `pkg/masterapi`).
- **CLI (admin ops)** — `zester enroll list [--state]|show` read the enrollments KV directly; `approve|reject [--reason]|revoke [--reason]` send an `enroll.AdminRequest` over NATS request/reply (`bus.SubjectAdminEnrollApprove/Reject/Revoke`, 5s timeout), answered by the masters' `zester-masters-admin` queue group (`internal/masterd/admin.go`, Info-logs id+operator+action). The persistent `--direct-kv` flag is the break-glass path that writes the enrollment KV directly with the operator's NATS credentials (for when no master is running; a no-responders error hints at it). List/approve/reject/revoke are also available over the REST API, gated by bearer tokens.
- **Peel client** — Auto-enrollment when no `.creds` file exists; challenge-response + polls status with exponential backoff (10s..5m). Multi-URL failover via `enroll.ClientConfig.MasterURLs` (peel `master_urls`): rotates on connection-level failures/5xx across all operations, pins the URL on success, never rotates on 4xx; unknown/expired challenge (401 "challenge verification failed") triggers a transparent fresh-nonce re-request + resubmit (bounded, 3 total attempts)

### KV Buckets
- `enrollments` — Records keyed by enrollment ID (`enr-<KSUID>`), peel index (`peel.<peel-id>`), no TTL, 10 history (constant: `BucketEnrollments`)
- `enroll-challenges` — Challenge nonces keyed by `chl-<KSUID>`, 5-minute TTL, 1 history (constant: `BucketEnrollChallenges`)

### State Machine
6 states: `pending` → `approved` → `issued` → `active` → `revoked` (with `rejected` branch from `pending`). Revoke also valid from `approved` or `issued`. Defined in `pkg/enroll/enrollment.go` (`validTransitions` map).

### Security
- Ed25519 challenge-response: peel signs `nonce || curvePublicKey` — binds both Ed25519 and X25519 keys to the proof, preventing curve key substitution
- Unenrolled peels have **zero NATS access** — they only reach the enrollment HTTP API over TLS 1.3
- TLS is mandatory — `NewServer()` rejects startup without cert/key (default: `/data/auth/enroll.crt`, `/data/auth/enroll.key`)
- Private nkey seed generated locally on the peel, never transmitted
- Credential download requires second Nkey signature of enrollment ID
- Admin ops split by transport: CLI approve/reject/revoke go through the masters' NATS request/reply admin service (operator NATS creds carry the `zester.admin.>` grant; day-to-day admin no longer needs enrollments-bucket write access — only the `--direct-kv` break-glass path does; peel JWTs must never get the admin grant). List/approve/reject/revoke are additionally exposed over the HTTPS REST API (`pkg/masterapi`), which requires bearer tokens. Peel-facing enrollment endpoints are unauthenticated but strictly rate-limited.
- Peel index (`peel.<id>`) written first via `kv.Create` as atomic uniqueness guard — prevents duplicate enrollments even under concurrent requests
- Per-IP token bucket rate limiting, split by path: strict for `/api/v1/enroll` and subpaths (burst 10, 1 req/10s); all other routes on the listener (the REST API) get burst 120, 20 req/s
- Credentials delivered over TLS, stored on disk with 0600 permissions
- CAS concurrency control for multi-master safety

### Tests
`pkg/enroll` has test files (`package enroll_test`) using `bustest.NewFakeJS()` + real nkeys. Covers: state machine, store CRUD with CAS, challenge lifecycle, signature verification (including curve key binding), HTTP handlers via `httptest`, credential issuance, admin request/reply round-trips, and file persistence.

### Documentation
- `website/content/docs/architecture/enrollment.mdx` — System design, data flow, trust model, component architecture
- `website/content/docs/operations/enrollment.mdx` — Operator guide, setup, troubleshooting, runbooks
- `website/content/docs/reference/enrollment-api.mdx` — HTTP API reference with curl examples and error codes
- `website/content/docs/architecture/enrollment-design.mdx` — Protocol design specification
- `website/content/docs/architecture/enrollment-security.mdx` — Security requirements and threat model

Docs site: Fumadocs (Next.js) in `website/` — content in `website/content/docs/` (MDX), nav in `meta.json` files, deployed to GitHub Pages by `.github/workflows/docs.yml` (static export, `DOCS_BASE_PATH=/zester`).

## State Composition (Include / Extend / Highstate)

Salt-style state composition is supported via the `pkg/state/compiler` package.

### Include Directive

State files can include other state files using dot-notation references:
```yaml
include:
  - common.packages      # loads common/packages.zy
  - webserver.config     # loads webserver/config.zy
```

Include resolution is depth-first with cycle detection and diamond deduplication (each file loaded exactly once).

### Extend Directive

Included states can be modified via `extend:`. Requisite lists (`require`, `watch`, `onchanges`, `onfail`) are **appended**; all other config keys are **replaced** (last-writer wins). Extend can only target states from included files.

### State Merging

When multiple files define the same state ID (via includes, top file, or `CompileMultiple`), they are **deep-merged** automatically (same semantics as `MergeSettings` for the settings pipeline). Files are processed in depth-first load order for deterministic results. Merge rules: requisite lists (`require`, `watch`, `onchanges`, `onfail`) are **appended**; all other keys are **replaced** (later file wins). If the same state ID uses different modules across files, both modules are kept as separate states in the DAG (e.g., `pkg.installed:nginx` and `file.managed:nginx`).

### Multi-file States

Dot-notation references map to filesystem paths:
- `"webserver"` → `webserver/init.zy`
- `"webserver.config"` → `webserver/config.zy`
- `"common.packages"` → `common/packages.zy`

### State Top File

`/data/states/top.zy` maps targeting patterns to state sets (same format as settings `top.zy`). Rendered as a Jinja2 template before parsing.

### Highstate

`state.highstate` resolves the state top file for the target peel, compiles all matching states (with includes/extends), and applies them. CLI: `zester '<target>' state.highstate` (or `zester '<target>' state.highstate --direct` for direct peel access).

### Compiler Pipeline

`state.apply "webserver"` → Compiler loads `webserver/init.zy` → renders Jinja2 → parses YAML → extracts `include:`/`extend:` → recursively loads included files (depth-first order) → deep-merges duplicate state IDs → applies extends → loads Starlark modules from `_modules/` dirs → builds `[]state.State` via Registry → feeds to `runner.Run()`.

## Starlark Custom Modules

Users can extend Zester with custom state modules written in Starlark (a sandboxed Python dialect). Drop `.star` files into `_modules/` directories within the states tree and they become available as state modules.

### Package: `pkg/starmod`

| File | Purpose |
|------|---------|
| `convert.go` | Go ↔ Starlark value conversion (`GoToStarlark`, `StarlarkToGo`, `DictToCheckResult`, `DictToApplyResult`) |
| `builtins.go` | Builtins exposed to Starlark: `cmd_run`, `file_*`, `pkg_*`, `http_*`, `json`, `base64_*`, `hash_sha256`, `sleep`, `log.*`, `facts`, `settings` |
| `module.go` | `StarlarkState` implementing `state.State` (Check/Apply/Revert lifecycle) |
| `loader.go` | `Loader` discovers `_modules/*.star` files, parses them, registers modules with Registry |

### Loading Phases

1. **Peel startup**: `Loader.LoadGlobal()` scans `{statesDir}/_modules/` for globally available modules
2. **Compile time**: Compiler calls `Loader.LoadDir()` for each directory that contained state files (formula-specific modules)

The Loader is idempotent — re-loading the same `.star` file by absolute path is a no-op. Later registrations override earlier ones (formula overrides global).

### Function Discovery

A function in a `.star` file is registered as a module action if:
- It's a `*starlark.Function`
- Its name doesn't start with `_` (private helper convention)
- Its name doesn't end with `_check` or `_revert` (companion functions)

Optional `{name}_check` and `{name}_revert` companions provide Check and Revert support. Without them, Check always returns `NeedsChange: true` and Revert is a no-op.

### Module Naming

`{filename}.{function}` — e.g., `nginx.star` containing `configured()` registers as `nginx.configured`.

### Builtins

Exec-layer builtins delegate to `ModuleContext` providers (same fakes work in tests). HTTP, JSON, base64, hash, and sleep use Go stdlib directly. `facts` and `settings` are injected as frozen (immutable) Starlark dicts.

### Compiler Integration

`CompilerConfig.StarLoader` (interface `StarModLoader`) is called in `CompileMultiple()` before `Registry.Build()`. The `uniqueParentDirs()` helper extracts directories from source file paths. The interface is defined in the compiler package to avoid importing starmod.

## State File Distribution (KV + GitFS)

State files flow from master to peels via NATS KV, mirroring the settings pipeline:

1. **Master publishes** (publisher-lease-gated in multi-master): `statefiles.Publisher.PublishFiles` walks `--states-dir`, Puts all file keys (raw bytes, not MessagePack), Puts `_manifest` (`statefiles.KeyManifest`; sorted msgpack `Manifest{Files: []ManifestFile{key, sha256}}` describing the COMPLETE set), bumps `_revision` (the peel-watched signal), then best-effort Deletes bucket keys absent from the set (never `_revision`/`_manifest`; failures Warn — the manifest is the read-side source of truth, KV deletion is GC). Publishing an empty set over a populated bucket is REFUSED unless `PublisherConfig.AllowEmpty` — a misconfigured empty states dir cannot wipe every peel's cache.
2. **Peel caches**: `statefiles.Cache` debounces revision events (`settings.NewDebouncedFunc`, 2s debounce + 5s jitter defaults) and syncs with capped-backoff retry (1s→30s, ctx-aware) until the bucket's current `_revision` is synced (`Cache.LastSyncedRevision`; concurrent triggers collapse via a lost-trigger-free singleflight). `Sync` is atomic: the complete file set is staged in a fresh sibling temp dir and swapped in via rename (live→`.old`, stage→live, remove `.old`; leftover `.old` cleaned on next sync) — mid-download failures leave the previous complete tree intact. It fetches EXACTLY the manifest's file set with SHA-256 verification (mismatch = failed attempt → retry) and prunes local files not listed. The manifest is mandatory: a bucket holding file keys but no `_manifest` is a torn or tampered publish and fails the sync (the retry loop re-syncs once the publisher completes); an empty bucket (before the master's first publish) is a clean no-op that leaves the local cache untouched. Path-escaping keys are rejected.
3. **Compiler reads**: `compiler.NewCompiler()` uses the cache dir (or falls back to baked-in `/data/states`). The peel re-evaluates which dir to use (`resolveStatesDir`) before every execution, retrying an empty/missing cache dir 5×25ms (covers the atomic-swap window) before falling back (Warn logged). KV-only peels with no states dir at boot do not crash-loop: the states engine builds lazily on the first execution after state files land; until then `state.apply`/`state.highstate` return a clear "states engine unavailable" error and all other modules work normally.

### GitFS

Optional Git-based state file source. Master clones repos into `--states-dir` on startup, pulls on interval, republishes to KV after each sync (publisher-lease-gated).

Flags:
- `--gitfs-remotes` — Comma-separated Git remote URLs
- `--gitfs-interval` — Pull interval (default 5m)
- `--gitfs-ssh-key` — Path to SSH private key for authentication

Each remote is cloned into `{states-dir}/{repo-name}/` where repo-name is derived from the URL (last path segment without `.git`). Clone-vs-pull is decided on repository **validity** (own `.git` entry + anchored `git rev-parse`), not directory presence: an invalid dir (e.g. interrupted clone) is removed and re-cloned (self-healing, Warn logged). Fresh clones stage into hidden temp sibling dirs (`.<repo>.clone-*`, skipped by the publisher walk, stale ones swept) and `os.Rename` into place, so an interrupted clone never leaves a half-populated repo. When a remote's sync fails, the republish proceeds only if that remote's local clone is a VALID repo (last known good); otherwise the publish cycle is skipped entirely — a partial clone can never propagate fleet-wide deletions.

Passing `--gitfs-remotes ""` (explicitly empty) disables GitFS even when the YAML config lists remotes — `config.ApplyVisited` re-applies every user-set flag after YAML load, so the explicit empty value wins.

## Template Salt Compatibility

The template engine (`pkg/template`) includes a Salt compatibility layer for easier migration from SaltStack:

- **Variable aliases**: `grains` maps to `facts`, `pillar` maps to `settings` (registered in `engine.go` during context building)
- **Control structures**: `{% import_yaml 'path' as var %}` loads and parses a YAML file into a template variable; `{% do expr %}` evaluates an expression for side effects (e.g., `{% do list.append(item) %}`)
- **Salt-compat functions**: `pillar_get(key, default)` for colon-separated nested settings lookup; `grains_filter_by(lookup, grain, merge, base)` for OS-family-based config selection; `user_info(name)`, `cmd_has_exec(name)`, `file_dirname(path)` (defined in `pkg/template/functions_salt.go`)
- **Enhanced dict methods**: `.get(key, default)`, `.items()`, `.update(other)`, `.keys()`, `.values()` on all dict objects (defined in `pkg/template/methods.go`)
- **Filter alias**: `|json` is an alias for `|to_json` (registered in `pkg/template/filters.go`)

## Self-Update System

Bulletproof binary self-update via dedicated watchdog process. Operator publishes binary to NATS Object Store, starts a rollout, and the master coordinates fleet-wide updates in batches.

### Components (`pkg/update/`)

| File | Purpose |
|-|-|
| `slots.go` | Three-slot binary manager: current/previous/staging with atomic `os.Rename`. `Recover()` resolves partial swaps on startup. |
| `supervisor.go` | Child process lifecycle: start/stop/restart, HTTP probes (`CheckHealth` on `HealthURL`; `CheckReady` on `ReadyURL`, falling back to `CheckHealth` when empty; `checkEndpoint` accepts status `ok` OR `degraded` — degraded is not probe-fatal), exponential backoff auto-restart (1s→60s). After 10 consecutive failed restarts the supervisor enters a slow-retry tier (`SupervisorConfig.DegradedRetryInterval`, default 10m) and recovers automatically when the child comes back — degraded is not terminal. `HealthVersion` parses the `/healthz` `version` field for fleet status. |
| `manifest.go` | `Manifest` struct (msgpack; `MinProtocol` gates rollouts against the fleet's reported protocol) + KV CRUD via `ManifestStore`. `BinaryStore` wraps `jetstream.ObjectStore` for chunked upload/download with SHA-256 verification. |
| `handler.go` | NATS command handler on watchdog side. State machine: `idle → preparing → staged → applying → soaking → confirmed` (with `rolling_back` branch). The soak loop polls READINESS (`Supervisor.CheckReady`), so an alive-but-functionally-dead child (NATS-disconnected → `/readyz` 503) fails soak and auto-rolls back. Entering `soaking` arms a **confirm-deadline** (`HandlerConfig.ConfirmDeadline`, default 3×SoakTime floored at `MinConfirmDeadline`=5m): if neither confirm nor rollback arrives (dead/orphaned controller), the node auto-rolls back to the previous binary. Restart monitoring and `WaitForHealthy` still use liveness. Caveat: a NATS outage during a node's soak window rolls that node back even if the binary is fine — avoid rollouts during planned NATS maintenance. |
| `status.go` | `NodeStatus` struct + `Reporter` that writes to `update-status` KV every 30s (TTL 60s = heartbeat). `NodeStatus.Degraded` (msgpack additive) reports the supervisor's degraded state via `ReporterConfig.DegradedFn`; `NodeStatus.Protocol` (additive) is stamped from `ReporterConfig.Protocol` (the watchdog passes `proto.ProtocolVersion`). |
| `rollout.go` | `RolloutController` on master. Batched rollout: prepare → apply → soak → confirm per batch. `RequestFunc` abstraction for testability. `RolloutStore` persists state to KV. **Resume**: `RolloutState.DriverID`/`DriverHeartbeat` (per-rollout 10s heartbeat goroutine); `ResumeOrphaned`/`RunResumeLoop` (started on every master, 60s scan) CAS-adopt non-terminal, non-dry-run rollouts whose heartbeat is staler than `StaleAfter` (default 60s) and resume from the persisted batch — a resumed batch re-issues prepare, so nodes already past it reject the duplicate and count toward MaxFailed (backstopped by the confirm-deadline rollback). `StartRollout` excludes `Degraded` nodes (Info-logged; error if that empties the set) and REFUSES when any target's `NodeStatus.Protocol` is below the manifest's `MinProtocol` (0 disables the check). `AbortRollout` CAS-writes `RolloutAborted` even when the rollout isn't locally active; state saves retry with CAS-conflict classification (external abort / superseded adopter), and a final mid-run persistence failure loudly aborts — while ctx cancellation (master shutdown) leaves the record for adoption. |

### NATS Resources

KV buckets: `update-manifests` (history 5), `update-status` (TTL 60s, history 1), `update-rollouts` (history 10).
Object Store: `update-binaries` (TTL 30 days).
Subjects: `zester.update.cmd.<id>` (master→watchdog request/reply); watchdog→master status flows via `update-status` KV heartbeats (no event subject).

### CLI Commands

`zester update publish <path> --component peel --version v0.5.0` — Upload binary + create manifest.
`zester update rollout --component peel --version v0.5.0 [--batch-size 1] [--soak-time 60s]` — Start fleet rollout.
`zester update status [--rollout <id>]` — Show fleet or rollout status (incl. DEGRADED and PROTO columns).
`zester update abort <rollout-id>` — Abort in-progress rollout.
`zester update rollback --component peel --target '*'` — Force rollback to previous binary.
`zester update versions --component peel` — List published versions.

### Watchdog Binary (`cmd/zester-watchdog/`)

Uses `flag` package (not Cobra). Flags: `--child-bin`, `--child-args`, `--nats-url` (default `tls://localhost:4222`), `--nats-ca`, `--nats-creds`, `--health-url` (default `http://127.0.0.1:9090/healthz` — must match the child's `health_addr`; use `:9091` when wrapping a master), `--ready-url` (default `""` = derived from `--health-url` by replacing the path with `/readyz`, so it follows the child's port; explicit value wins; polled ONLY during the update soak phase — restart monitoring stays on `--health-url` liveness), `--health-timeout`, `--health-interval`, `--health-retries`, `--soak-time`, `--id`, `--component`, `--log-level`, `--log-format` (defaults info/json via `internal/logging`). The packaging/systemd units pass `--ready-url` explicitly next to `--health-url`. Enrollment-aware: starts child immediately, enters offline mode if creds missing, polls for creds file every 10s, connects to NATS when creds appear. NATS/bucket setup failures are retried with backoff (never exits and orphans the child); the child is stopped on every exit path. Reports `NodeStatus.Protocol = proto.ProtocolVersion`.

### ObjectStoreAPI Interface

`pkg/bus/kv.go` defines `ObjectStoreAPI` (separate from `JetStreamAPI`) with `CreateOrUpdateObjectStore`, `ObjectStore`, `DeleteObjectStore`. This avoids breaking existing `bustest.FakeJS` which doesn't implement Object Store methods. `InitializeObjectStores()` is called separately from `InitializeStorage()`; both delegate to `...Opts` variants that apply the replicas tiering.

## Conventions

- Error wrapping: `fmt.Errorf("pkg: operation: %w", err)`
- Logging: daemons bootstrap via `internal/logging.Setup(w, component, level, format)` — JSON default, `--log-level`/`--log-format` on all three daemons, base attrs `component` + `version` on every line (plus `master_id`/`peel_id` once known). Library packages take a `*slog.Logger` via config structs, defaulting to `slog.Default()`
- Config knobs: `flag:"..." usage:"..."` struct tags on the daemon config structs; `config.BindFlags` + `config.ApplyVisited` (`internal/config/bind.go`) generate flags and enforce flag > YAML > default precedence — never hand-write flag blocks or `flag.Visit` switches
- KV bucket names: constants in `pkg/bus/kv.go` (BucketFacts, BucketJobs, BucketPeelHeartbeat, BucketLeases, etc.)
- Object Store bucket names: constants in `pkg/bus/kv.go` (ObjectBucketUpdateBinaries)
- Subject naming: `zester.<category>.<target>` pattern, helpers in `pkg/bus/subjects.go` (incl. `zester.target.resolve`, `zester.admin.enroll.*`, `JobAckSubject`)
- Wire/KV struct evolution: additive-only msgpack (`,omitempty`), per the `pkg/proto` policy — mixed-version fleets must stay safe
