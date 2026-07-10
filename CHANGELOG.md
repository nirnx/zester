# Changelog

All notable changes to Zester are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/) (0.x — APIs may still change between minors).

## [Unreleased]

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
