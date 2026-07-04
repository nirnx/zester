# Zester

A SaltStack alternative built in pure Go, powered by **NATS JetStream** for messaging and **Ed25519 nkeys** for identity. Single static binaries — no Python runtime, no external database, no ZeroMQ.

State files stay Salt-compatible: requisites (`require`/`watch`/`onchanges`/`onfail`/`prereq`/`listen` plus the `_in` inverses), `onlyif`/`unless` guards, `order`/`retry`/`failhard`/`names`, `test=True` dry runs, and the `salt['mod.func'](...)` template accessor all work, and `zester-migrate` converts existing `.sls` trees.

Event-driven automation is built in (Salt's beacons + reactor): peels emit events and service-state beacons onto a durable JetStream stream, and master-side reactor rules dispatch jobs, auto-approve enrollments, or chain further events — with exactly-once reactions, loop guards, and hot-reloaded rules.

**📖 Documentation: [zester.cc](https://zester.cc/)** — sources live in [`website/`](website/) (Fumadocs; run `pnpm dev` inside `website/` to browse locally).

## Binaries

| Binary | Purpose |
|---|---|
| `zester-master` | Dispatches jobs, compiles settings, watches facts, serves the enrollment + REST API on one TLS listener, persists scheduler results, runs the event reactor, coordinates self-update rollouts |
| `zester-peel` | Agent on managed nodes: collects facts, executes jobs and states, resolves settings locally, runs the peel-side scheduler |
| `zester` | Operator CLI: `zester '<target>' <module.function> [args...]` (`--test` for Salt-style `test=True` dry runs), plus `job`, `enroll`, `basket`, `update`, `event`, `reactor` subcommands |
| `zester-watchdog` | Process supervisor for self-updates: manages binary slots, health-monitors the child process, applies update commands via NATS |
| `zester-migrate` | Standalone converter for Salt `.sls` files to Zester `.zy` format |

## TLS-Only NATS

All NATS connections use the `tls://` URL scheme — master, peel, and watchdog reject `nats://` URLs at startup. Defaults: `tls://nats:4222` for master and peel, `tls://localhost:4222` for the watchdog and CLI.

The client CA certificate is resolved in this order:

1. Explicit `--nats-ca` flag / `nats_ca` YAML field
2. `NATS_CA_FILE` environment variable
3. `/data/auth/nats-ca.crt` if present
4. System trust store

## Quick Start (Docker Playground)

Requires Docker. The playground stack (`docker-compose.yml`) runs NATS, one master, five peels (`web-01..03`, `db-01..02`), and an `admin` container with the CLI preconfigured.

```bash
make docker-up
```

An init container bootstraps everything into shared volumes before the other services start:

- nkey trust hierarchy (operator/account JWTs, master and admin credentials)
- TLS certificates for both the NATS transport and the enrollment HTTPS API
- a **random** REST API token at `/data/auth/api-tokens/integration.token`
- master config pointing at `tls://nats:4222` with the NATS CA

Peels enroll against the master over HTTPS (`https://master:8443`) and wait for approval. Approve them, then run modules:

```bash
# Approve pending peel enrollments
docker compose exec admin sh /playground/auto-approve.sh

# Ping every peel
docker compose exec admin zester '*' test.ping

# Apply a state
docker compose exec admin zester 'web-*' state.apply webserver
```

Tear down with `make docker-down`.

## Building and Testing

```bash
make build-binaries      # build all binaries to bin/
make test                # unit tests (no external dependencies)
make test-integration    # Docker-based end-to-end suite
```

CI (`.github/workflows/ci.yml`) runs gofmt, `go vet`, a `go mod tidy` check, the build, unit tests, and the race detector on core packages; integration tests run on pushes to `main`.
