# Changelog

All notable changes to Zester are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/) (0.x — APIs may still change between minors).

## [Unreleased]

### Changed
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

[Unreleased]: https://github.com/nirnx/zester/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/nirnx/zester/releases/tag/v0.1.0
