# Zester Architecture

**A SaltStack alternative built in pure Go, powered by NATS JetStream and nkeys.**

Salt -> Zest -> Zester. Same flavor family, sharper tool.

---

## Naming Map (Salt -> Zester)

| SaltStack     | Zester       | Purpose                                    |
|---------------|--------------|---------------------------------------------|
| Master        | Master       | Central control plane                       |
| Minion        | Peel         | Managed node agent                          |
| Grains        | Facts        | Local system information (collected by peel)|
| Pillars       | Settings     | Secure per-peel configuration (from master) |
| Mine          | Basket       | Peel-to-peel data sharing                   |
| Syndic        | Leaf Node    | Multi-tier / edge relay (NATS native)       |
| Event Bus     | NATS JetStream| Message bus + persistence                  |
| Salt Keys     | nkeys        | Ed25519 authentication                      |
| Jinja         | Gonja        | Jinja2-compatible Go template engine        |
| States        | States       | Declarative desired-state definitions       |
| Beacons       | Beacons      | Peel-side event monitors (planned)          |
| Reactor       | Reactor      | Master-side event-driven automation (planned)|
| Returner      | (built-in)   | JetStream KV replaces external returners    |

---

## Architecture Overview

```
+-----------------------------------------------------------+
|                      ZESTER MASTER                        |
|                                                           |
|  +-------------+  +-------------+  +------------------+  |
|  |  State      |  |  Settings   |  |  Targeting       |  |
|  |  Engine     |  |  Store      |  |  Engine          |  |
|  +------+------+  +------+------+  +---------+--------+  |
|         |                |                    |           |
|  +------v------------------------------------v---------+  |
|  |           NATS Server (External)                     |  |
|  |     JetStream + KV Store + Object Store             |  |
|  |     TLS 1.3 + nkey auth (Ed25519)                   |  |
|  +------------------------+----------------------------+  |
|                           |                               |
|  +------------------------v----------------------------+  |
|  |  Reactor / Event Engine (planned)                   |  |
|  |  (watches NATS subjects for beacon events)          |  |
|  +----------------------------------------------------|  |
+---------------------------+-------------------------------+
                            |  TLS + nkey mutual auth
           +----------------+----------------+
           |                |                |
+----------v---+  +---------v----+  +--------v-----+
|  ZESTER      |  |  ZESTER      |  |  ZESTER      |
|  PEEL        |  |  PEEL        |  |  PEEL        |
|              |  |              |  |              |
| +----------+ |  | +----------+ |  | +----------+ |
| |  Facts   | |  | |  Facts   | |  | |  Facts   | |
| | Collector| |  | | Collector| |  | | Collector| |
| +----------+ |  | +----------+ |  | +----------+ |
| +----------+ |  | +----------+ |  | +----------+ |
| | Beacons  | |  | | Beacons  | |  | | Beacons  | |
| +----------+ |  | +----------+ |  | +----------+ |
| +----------+ |  | +----------+ |  | +----------+ |
| |  State   | |  | |  State   | |  | |  State   | |
| | Executor | |  | | Executor | |  | | Executor | |
| +----------+ |  | +----------+ |  | +----------+ |
+--------------+  +--------------+  +--------------+
```

---

## 1. Message Bus - NATS JetStream

### Subject Hierarchy

```
zester.cmd.<target>              # Master -> Peel commands (request/reply)
zester.event.<peel-id>           # Peel -> Master events (beacons, returns)
zester.state.apply.<peel-id>     # State application jobs
zester.fact.<peel-id>            # Fact data publish/sync
zester.settings.<peel-id>       # Settings data (encrypted per-peel)
zester.basket.<key>              # Basket data sharing between peels
zester.job.<jid>                 # Job tracking and returns
zester.reactor                   # Event-driven reactions
```

### Why NATS Solves Salt's Scalability Problems

| Salt Problem                         | NATS Solution                                                     |
|--------------------------------------|-------------------------------------------------------------------|
| Thundering herd on master restart    | NATS handles millions of connections, peels reconnect with backoff|
| ZeroMQ single master bottleneck      | NATS cluster (R3 RAFT consensus), horizontal scaling built-in     |
| Custom event bus implementation      | JetStream: durable events, replay, KV, object store for free     |
| Syndic complexity for multi-tier     | NATS super-clusters + leaf nodes - native multi-region            |
| Pub/sub only, no persistence         | JetStream: at-least-once delivery, message replay, retention      |

### NATS Deployment Topology

```
              +---- NATS Supercluster ----+
              |                           |
       +------+------+            +-------+-----+
       | Region A    |            | Region B    |
       | NATS Cluster|<--gateway-->| NATS Cluster|
       | (3 nodes)   |            | (3 nodes)   |
       +------+------+            +-------+-----+
              |                           |
       +------+------+            +-------+-----+
       | Leaf Node   |            | Leaf Node   |
       | (edge/DMZ)  |            | (edge/DMZ)  |
       +-------------+            +-------------+
```

Leaf nodes replace Salt's syndic entirely. Edge peels connect to local leaf nodes which gateway back to the central cluster. No custom protocol needed.

---

## 2. Authentication - nkeys Replacing Salt Keys

### Salt's Key Model vs Zester's nkey Model

```
SALT                              ZESTER
------------------------------    -----------------------------------
1. Minion generates RSA keypair   1. Peel generates Ed25519 nkey seed
2. Sends public key to master     2. Publishes public key (NATS subject)
3. Master admin runs              3. Master auto-accepts if signed by
   `salt-key --accept`               trusted operator JWT, OR
4. Keys stored in /etc/salt/pki      admin accepts via CLI/API
5. AES session key negotiated     4. NATS handles TLS + nkey challenge
                                  5. No session key needed - TLS does it
```

### Three-Tier Trust Hierarchy (NATS JWT)

```
Operator (org-level root of trust)
  +-- Account: "production"
  |     +-- User: peel-web-01    (nkey: UABC...)
  |     +-- User: peel-web-02    (nkey: UDEF...)
  |     +-- User: master-01      (nkey: UGHI...)
  +-- Account: "staging"
        +-- User: peel-stg-01    (nkey: UJKL...)
        +-- User: master-stg     (nkey: UMNO...)
```

Key advantages over Salt:

- **No key files on the master** - server validates signatures against public keys in JWTs
- **Decentralized user management** - account admins can onboard peels without touching master config
- **Account isolation** - production peels can't accidentally target staging
- **Ed25519** - faster and more secure than Salt's RSA, 32-byte keys

### Peel Bootstrap Flow

```
1. Admin generates operator nkey + JWT (one-time setup)
2. Admin creates account nkey + JWT for each environment
3. For new peel:
   a. Generate user nkey seed locally: `zester peel init`
   b. Create user JWT signed by account key (can be automated via API)
   c. Peel connects with .creds file (JWT + nkey seed)
   d. NATS validates JWT signature chain + nkey challenge
   e. Peel is authenticated and authorized per JWT permissions
```

---

## 3. Facts (Salt Grains Equivalent)

Local system information collected by peels.

### Collection Interface

```go
type Collector interface {
    Name() string
    Collect(ctx context.Context) (map[string]any, error)
    Interval() time.Duration  // 0 = collect once at startup
}
```

Built-in collectors:

- **os** - OS name, version, arch, kernel
- **network** - IPs, interfaces, MACs, hostname, FQDN
- **cpu** - count, model, features
- **memory** - total, available
- **disk** - mounts, sizes
- **custom** - user-defined facts from shell scripts in `/etc/zester/facts`

### Storage and Sync

Facts stored in NATS KV bucket `facts`:

```
facts.<peel-id> = { "os": "linux", "arch": "amd64", ... }
```

- Peels publish facts on startup and at configurable intervals
- Master reads any peel's facts instantly from KV (no round-trip RPC)
- KV history tracks fact changes over time
- Eliminates Salt's `salt '*' grains.items` full fan-out

### Custom Facts

```yaml
# /etc/zester/facts.d/app.yaml
app_version:
  cmd: "/opt/myapp/bin/version"
  interval: 5m
datacenter:
  value: "us-east-1"  # static
```

---

## 4. Settings (Salt Pillars Equivalent)

Secure, per-peel configuration data. The master sanitizes `.zy` files (replacing `!encrypted` values with `__ZESTER_SECRET:*__` placeholders) and publishes them to the shared `settings-files` KV bucket. Each peel evaluates `top.zy` locally, renders templates with its own facts, and decrypts secrets from the `secrets` KV bucket.

### Structure

```
/srv/zester/settings/
+-- top.zy                    # targeting map (.zy = zester yaml)
+-- common/
|   +-- base.zy
+-- webservers/
|   +-- nginx.zy
|   +-- certs.zy
+-- databases/
    +-- postgres.zy
```

### top.zy (targeting which peels get which settings)

```yaml
base:
  '*':
    - common.base
  'role:webserver':
    - webservers.nginx
    - webservers.certs
  'os:ubuntu and environment:production':
    - databases.postgres
```

### Encryption Model

Unlike Salt which encrypts the entire pillar blob with the minion's RSA key:

```
1. Master loads .zy files, sanitizes !encrypted values to __ZESTER_SECRET:*__ placeholders
2. Sanitized templates stored in shared "settings-files" KV bucket
3. Per-peel encrypted secrets stored in "secrets" KV bucket (NaCl box)
4. Peel evaluates top.zy locally (matches own facts)
5. Peel renders matched .zy templates with local facts
6. Peel decrypts own secrets and substitutes placeholders
7. NATS subject permissions ensure peel can ONLY read its own secrets
8. TLS encrypts in transit
```

Double encryption for sensitive fields:

```yaml
db_password: !encrypted |
  ENC[nkey,AQFz8r3...]  # encrypted with peel's public nkey
```

Peel decrypts locally with its nkey seed. Even if NATS is compromised, encrypted fields remain secure.

---

## 5. Basket (Salt Mine Equivalent)

Peel-to-peel data sharing. Allows peels to publish data that other peels can query.

### How It Works

```
1. Peel executes a function and publishes result to NATS KV
   Bucket: "basket"
   Key: <peel-id>.<function-name>

2. Other peels (or master) read from KV directly
   No fan-out, no master proxy needed

3. Configurable auto-refresh interval per basket function
```

### Configuration

```yaml
# /etc/zester/peel.d/basket.yaml
basket:
  network.ip_addrs:
    interval: 5m
  grains.fqdn:
    interval: 1h
  cmd.run:
    args: ["cat /opt/app/version"]
    interval: 10m
```

### Usage in Templates

```jinja
{# Access basket data from other peels in state files #}
{% for peel in basket("role:webserver", "network.ip_addrs") %}
  allow {{ peel.value }};
{% endfor %}
```

### NATS KV Layout

```
basket.<peel-id>.network.ip_addrs = ["10.0.1.5", "10.0.1.6"]
basket.<peel-id>.grains.fqdn      = "web-01.prod.example.com"
basket.<peel-id>.cmd.run           = "2.4.1"
```

### CLI

```bash
# Query basket data
zester basket get 'role:webserver' network.ip_addrs

# List all basket keys for a peel
zester basket list web-01

# Force refresh
zester basket refresh web-01 network.ip_addrs
```

---

## 6. Templating - Gonja (Jinja2-Compatible)

Using [gonja](https://github.com/nikolalohinski/gonja/v2), a Jinja2-compatible template engine for Go.

### State Files (.zy)

```jinja
# /srv/zester/states/nginx/init.zy

{% set workers = facts.cpu_count * 2 %}

nginx_package:
  pkg.installed:
    - name: nginx
    - version: {{ settings.nginx_version | default("latest") }}

nginx_config:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: zester://nginx/files/nginx.conf.zy
    - template: gonja
    - context:
        worker_processes: {{ workers }}
        server_name: {{ facts.fqdn }}
    - require:
      - pkg: nginx_package

{% for vhost in settings.vhosts %}
vhost_{{ vhost.name }}:
  file.managed:
    - name: /etc/nginx/sites-enabled/{{ vhost.name }}.conf
    - source: zester://nginx/files/vhost.conf.zy
    - template: gonja
    - context:
        domain: {{ vhost.domain }}
        port: {{ vhost.port }}
        ssl_cert: {{ vhost.ssl_cert }}
{% endfor %}

nginx_service:
  service.running:
    - name: nginx
    - enable: true
    - watch:
      - file: nginx_config
```

Gonja provides: `{% for %}`, `{% if %}`, `{{ var | filter }}`, `{% macro %}`, `{% include %}`, `{% extends %}`.

### Custom Filters and Functions

```go
engine.RegisterFilter("settings_decrypt", func(in *exec.Value, params *exec.VarArgs) *exec.Value {
    // decrypt NaCl box with peel nkey
})

engine.RegisterFunction("basket", func(params *exec.VarArgs) *exec.Value {
    // fetch basket data from NATS KV
})
```

---

## 7. Targeting Engine

```go
type TargetType int

const (
    Glob      TargetType = iota  // "web*"
    PCRE                          // "E@web-dc[12]-srv\d+"
    Fact                          // "G@os:ubuntu"
    Settings                      // "I@role:webserver"
    Compound                      // "web* and G@os:ubuntu and not E@.*-dev-.*"
    List                          // "L@web01,web02,web03"
)
```

### How Targeting Works at Scale

Instead of Salt's approach (master resolves targets -> sends to all -> minions self-filter):

```
1. Peel publishes facts to NATS KV on connect
2. Master maintains an in-memory index of all facts (rebuilt from KV on start)
3. Target resolution happens on master - resolves to list of peel IDs
4. Master publishes command to each matched peel's NATS inbox
   OR uses NATS subject wildcards for broad targeting
5. Result: only targeted peels receive the message (zero wasted bandwidth)
```

For 50k+ peels, the master-side fact index uses a radix tree for glob matching and a precompiled regex cache for PCRE, making resolution sub-millisecond.

---

## 8. State Engine

### Execution Model

```go
type State interface {
    Name() string
    Reqs() Requisites
    // Check returns current state - idempotent, read-only
    Check(ctx context.Context) (CheckResult, error)
    // Apply enforces desired state - only called if Check shows drift
    Apply(ctx context.Context) (ApplyResult, error)
    // Revert rolls back (optional, for orchestration failures)
    Revert(ctx context.Context) (ApplyResult, error)
}

type CheckResult struct {
    NeedsChange bool
    Diff        string
}

type ApplyResult struct {
    Changed  bool
    Diff     string
    Details  map[string]string
}
```

### Built-in State Modules

```
cmd.run              - Command execution
file.managed         - File management (content, permissions)
file.directory       - Directory management
file.absent          - File/directory removal
file.append          - Append content to files
file.symlink         - Symbolic link management
file.recurse         - Recursive directory management
file.blockreplace    - Block replacement within files
pkg.installed        - Package installation (apt, dnf, yum, brew)
pkg.removed          - Package removal
service.running      - Ensure service is running
service.enabled      - Ensure service is enabled at boot
service.dead         - Ensure service is stopped
cron.present         - Cron job creation
cron.absent          - Cron job removal
sysctl.present       - Sysctl parameter management
mount.mounted        - Filesystem mount management
git.cloned           - Git repository cloning
pip.installed        - Python package installation
timezone.system      - System timezone configuration
locale.present       - System locale configuration
user.present         - User creation/management
user.absent          - User removal
group.present        - Group creation/management
group.absent         - Group removal
test.ping            - Connectivity test
```

Query modules (handled as special cases in peel exec handler):
```
facts.items       - List all facts
facts.get         - Get a specific fact
facts.keys        - List fact keys
settings.items    - List all settings
settings.get      - Get a specific setting
settings.keys     - List setting keys
```

### Extensibility

State modules are registered via the `Registry` with closure-based `Builder` functions:

```go
// Builder constructs a State from a config map.
type Builder func(id string, config map[string]any) (State, error)

// Register a built-in module (closure captures ModuleContext)
registry.Register("pkg.installed", NewPkgInstalledBuilder(mctx))
```

Custom modules can also be written in Starlark (`.star` files in `_modules/` directories).

---

## 9. Job Tracking

### NATS Subjects

```
zester.job.<jid>.dispatch          # Master publishes job spec
zester.job.<jid>.ack.<peel-id>     # Peel acknowledges receipt
zester.job.<jid>.return.<peel-id>  # Peel publishes result
zester.job.<jid>.status            # Master publishes aggregated status
zester.job.<jid>.cancel            # Cancel signal to peels
```

### Job ID (JID)

KSUID - timestamp-sortable, globally unique, no coordination needed:

```go
jid := ksuid.New().String()  // e.g. "2oHfKnCPMQnLEYQeBQsNtUiJp3r"
```

KSUIDs over UUIDs because they sort chronologically.

### Storage - Three NATS JetStream Structures

```
1. KV Bucket: "jobs"
   Key: <jid>
   Value: { spec, target, state, created_at, expected_peels, timeout }
   TTL: 7 days (configurable)

2. KV Bucket: "job-returns"
   Key: <jid>.<peel-id>
   Value: { result, changed, duration, diff, error }
   TTL: 7 days

3. Stream: "job-events"
   Full event log for replay/audit
   (dispatch, ack, return, timeout, retry)
```

### Job Lifecycle

```
Master                          NATS                         Peel
  |                               |                            |
  |-- 1. Create job in KV ------->|                            |
  |-- 2. Publish to               |                            |
  |   zester.job.<jid>.dispatch ->|---- deliver -------------->|
  |                               |                            |
  |                               |<-- 3. ACK -----------------|
  |                               |   zester.job.<jid>.ack.*   |
  |                               |                            |
  |                               |         (peel executes)    |
  |                               |                            |
  |                               |<-- 4. RETURN --------------|
  |                               |   zester.job.<jid>.return.*|
  |                               |   (stored in job-returns)  |
  |                               |                            |
  |<- 5. Master aggregates ------|                            |
  |   updates job status in KV    |                            |
  |   (completed/partial/failed)  |                            |
```

### Job Spec

```go
type Job struct {
    JID        string         `msgpack:"jid"`
    Function   string         `msgpack:"fun"`        // "state.apply", "cmd.run"
    Args       []any          `msgpack:"args"`
    Kwargs     map[string]any `msgpack:"kwargs"`
    Target     string         `msgpack:"tgt"`        // "web*"
    TargetType TargetType     `msgpack:"tgt_type"`   // glob, fact, compound
    PeelIDs    []string       `msgpack:"peel_ids"`   // resolved peel list
    Timeout    time.Duration  `msgpack:"timeout"`
    BatchSize  int            `msgpack:"batch_size"` // 0 = all at once
    CreatedAt  time.Time      `msgpack:"created_at"`
    User       string         `msgpack:"user"`       // who issued it
}
```

### Job Status

```go
type JobStatus struct {
    JID       string            `msgpack:"jid"`
    State     JobState          `msgpack:"state"`    // pending/running/complete/partial/timeout
    Expected  int               `msgpack:"expected"`
    Returned  int               `msgpack:"returned"`
    Succeeded int               `msgpack:"succeeded"`
    Failed    int               `msgpack:"failed"`
    Peels     map[string]string `msgpack:"peels"`    // peel-id -> ack/running/done/fail/timeout
}
```

### Job Return

```go
type JobReturn struct {
    JID      string        `msgpack:"jid"`
    PeelID   string        `msgpack:"peel_id"`
    Success  bool          `msgpack:"success"`
    Return   any           `msgpack:"return"`
    Changed  bool          `msgpack:"changed"`
    Duration time.Duration `msgpack:"duration"`
    Retcode  int           `msgpack:"retcode"`
}
```

### Timeout and Retry

The master runs a watcher per active job. If peels don't return within the timeout, they're marked as timed out. A reactor event `zester/job/<jid>/timeout` is emitted for automated handling.

### Job Retention

```yaml
# master config
jobs:
  retention: 7d
  max_returns: 100000
  archive: true  # move expired to object store before deletion
```

JetStream retention policies handle cleanup natively.

### CLI

```bash
zester job list                                    # list recent jobs
zester job show <jid>                              # job detail
zester job return <jid> --peel db-03               # per-peel return
zester job active                                  # currently running
zester job kill <jid>                              # cancel a job
```

---

## 10. Event System - Reactor and Beacons (Planned)

!!! note "Not yet implemented"
    Beacons and Reactor are planned features. The `pkg/beacon/` and `pkg/reactor/` packages exist but contain no implementation yet. The designs below represent the intended architecture.

### Beacons (Peel-Side) — Planned

Beacons will monitor local conditions on peels and publish events to `zester.event.<peel-id>.beacon.<name>` when thresholds are crossed.

### Reactor (Master-Side) — Planned

The reactor will watch for events and trigger automated responses. Events will be persisted in JetStream for replay after master restart.

---

## 11. Encryption - All Traffic Encrypted by Default

### Four Layers

```
Layer 1: TLS 1.3 (NATS native)
  - All NATS connections require TLS - no plaintext mode
  - Auto-cert via embedded ACME or pre-provisioned certs
  - Certificate rotation without restart (NATS supports reload)

Layer 2: nkey challenge-response (NATS native)
  - Ed25519 signature on every connection
  - No passwords stored or transmitted

Layer 3: NaCl box encryption for settings secrets
  - Per-peel encryption using X25519 derived from Ed25519 nkeys
  - Secrets encrypted at rest in NATS KV
  - Only the target peel can decrypt

Layer 4: JetStream encryption at rest
  - AES-256-GCM with operator-provided key
  - All persisted data encrypted on disk
```

Zero plaintext paths exist. From peel bootstrap to state execution, every byte is encrypted.

---

## 12. High-Throughput Design Decisions

| Decision                              | Rationale                                                        |
|---------------------------------------|------------------------------------------------------------------|
| MessagePack for serialization         | 2-3x faster than JSON, 30% smaller                              |
| NATS inbox pattern for RPC            | Request/reply without blocking the bus                           |
| Peel-side fact caching in NATS KV     | Master never does full fan-out for fact queries                   |
| Parallel state execution              | DAG-based dependency resolution, maximum parallelism             |
| Connection pooling                    | Each peel maintains one NATS connection with multiplexed subs    |
| Batch job dispatch                    | Configurable batch sizes to avoid thundering herd                |
| Binary protocol                       | NATS protocol is text-based but payloads are msgpack binary      |
| Zero-copy where possible              | NATS Go client supports zero-copy message delivery               |

### Estimated Throughput

Based on NATS benchmarks (200k-400k msg/sec with JetStream persistence):

- Command fan-out to 100k peels: ~250ms
- Fact sync for 100k peels: continuous via KV, no polling
- Job returns from 10k peels: ~50ms aggregation via NATS inbox
- State apply to 1k peels: seconds, not minutes

---

## 13. Comparison with Salt

| Feature              | SaltStack                               | Zester                                           |
|----------------------|-----------------------------------------|--------------------------------------------------|
| Language             | Python                                  | Go (single static binary)                        |
| Message Bus          | ZeroMQ (custom protocol)                | NATS JetStream (production-grade)                |
| Auth                 | RSA key exchange + AES                  | Ed25519 nkeys + TLS 1.3                          |
| Scaling              | Syndic hierarchy                        | NATS superclusters + leaf nodes                  |
| Fact storage         | In-memory on master                     | NATS KV (persistent, distributed)                |
| Settings delivery    | Full render, encrypt, send              | KV with per-peel subject isolation + NaCl        |
| Peel data sharing    | Mine (master-proxied)                   | Basket (direct NATS KV, no master proxy)          |
| Event persistence    | None (fire-and-forget)                  | JetStream (durable, replayable)                  |
| Job tracking         | Master memory + optional returner       | NATS KV (persistent, distributed, no external DB)|
| Deployment           | pip install + dependencies              | Single binary, zero dependencies                 |
| Templating           | Jinja2 (Python)                         | Gonja (Jinja2-compatible, Go)                    |
| Multi-region         | Complex syndic chains                   | Native NATS gateways                             |

---

## 14. Project Structure

```
zester/
+-- cmd/
|   +-- zester-master/           # Master binary
|   +-- zester-peel/             # Peel binary
|   +-- zester/                  # CLI tool
|   +-- zester-watchdog/         # Process supervisor for self-updates
|   +-- zester-migrate/          # Salt .sls → Zester .zy converter
+-- pkg/
|   +-- auth/                    # nkey + JWT management
|   +-- bus/                     # NATS connection management
|   +-- facts/                   # Fact collection + storage
|   |   +-- collectors/          # OS, network, CPU, memory, disk, custom
|   |   +-- index.go             # Radix tree index for targeting
|   +-- settings/                # Settings rendering + encryption
|   +-- state/                   # State engine + module interface
|   |   +-- modules/             # Built-in state modules
|   |   +-- dag.go               # Dependency DAG resolver
|   +-- target/                  # Targeting engine
|   +-- template/                # Gonja integration + custom filters
|   +-- exec/                    # Execution layer (provider interfaces + OS impls)
|   +-- starmod/                 # Starlark custom module support
|   +-- statefiles/              # State file distribution (KV + GitFS)
|   +-- update/                  # Self-update system
|   +-- enroll/                  # Peel enrollment
|   +-- job/                     # Job manager (dispatch, tracking, returns)
|   +-- basket/                  # Peel-to-peel data sharing
|   +-- proto/                   # MessagePack message definitions
+-- states/                      # Example state tree
+-- settings/                    # Example settings tree
+-- docs/
```

---

## 15. Build Order

1. **Bus layer** - External NATS server + peel connection with nkeys + TLS
2. **Facts** - Collectors + NATS KV sync
3. **CLI** - `zester` command to query facts, list peels
4. **Settings** - Template rendering + per-peel encryption
5. **State engine** - Core state interface + `file.managed` + `pkg.installed`
6. **Targeting** - Glob + fact-based + compound
7. **Job manager** - Dispatch, tracking, returns
8. **Reactor + Beacons** - Event-driven automation
9. **Basket** - Peel-to-peel data sharing
10. **Orchestration** - Multi-peel coordinated workflows
11. **Additional state modules** - Expand coverage
