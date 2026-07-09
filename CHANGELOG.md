# Changelog

All notable changes to Zester are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/) (0.x — APIs may still change between minors).

## [Unreleased]

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

[Unreleased]: https://github.com/nirnx/zester/compare/v0.3.4...HEAD
[0.3.4]: https://github.com/nirnx/zester/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/nirnx/zester/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/nirnx/zester/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/nirnx/zester/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nirnx/zester/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/nirnx/zester/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nirnx/zester/releases/tag/v0.1.0
