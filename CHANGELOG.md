# Changelog

All notable changes to Zester are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/) (0.x — APIs may still change between minors).

## [Unreleased]

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

[Unreleased]: https://github.com/nirnx/zester/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/nirnx/zester/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nirnx/zester/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/nirnx/zester/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nirnx/zester/releases/tag/v0.1.0
