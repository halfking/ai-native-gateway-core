---
name: llm-gateway-deploy
description: Deploy llm-gateway-go locally or promote the same verified release through 245 and 154. Use for deployment planning, local installation layout, PostgreSQL/Redis reuse, blue-green release switching, rollback, and verification.
---

# llm-gateway-go deployment

## Promotion order

Use the same commit, `version.json`, and build sequence in this order:

```text
local -> 245 (pre-production gate) -> 154 (production)
```

`245` and `154` must use the repository's `scripts/deploy-245.sh` and
`scripts/deploy-154.sh` wrappers. They delegate to the audited
`deploy-seamless.sh`; do not use the legacy `scripts/deploy.sh`, `deploy-to-154.sh`,
`deploy/phase0/*`, or `SSHPASS` paths.

Run a non-mutating preflight first:

```bash
bash scripts/deploy-local.sh deploy --dry-run
bash scripts/deploy-245.sh --dry-run
bash scripts/deploy-154.sh --dry-run
```

Remote credentials and SSH keys come only from the env-injector SSOT. Inject
`aliyun-frontend-245` and `aliyun-gateway-154` (or their canonical aliases) before
an apply. Never print or commit secret values, and never use Docker Hub images.

## Local installation contract

`~/kaixuan` is a shared parent for multiple projects. `LLM_GATEWAY_ROOT` must
point to this project's installation directory when overridden. Otherwise the
script chooses:

| OS | default project directory |
|---|---|
| macOS | `~/kaixuan/llm-gateway-go` |
| Linux | `/opt/kaixuan/llm-gateway-go` |
| Windows | writable `D:/kaixuan/llm-gateway-go`, then `C:/kaixuan/llm-gateway-go` |

The project directory contains `attachments`, `bin`, `backups`, `logs`,
`raw-logs`, and `run`. `bin/<version>.<build>/` is an immutable release bundle
and `bin/current` is the active symlink. `run/active-version`, `run/active-port`,
and bundle metadata record the serving identity. Existing `~/Downloads` layouts
and old files directly under `~/kaixuan` are not moved, deleted, or adopted;
use `--root` for an explicit legacy migration or a separate project.

Do not change `PROJECT_ROOT` to this installation directory: it remains the
source checkout containing `version.json`, `sql/`, `web/`, and `cmd/gateway`. Legacy
helpers such as `local-docker-up.sh`, `local-host-*`, and `scripts/user/*` have
older root contracts and should not be mixed with this entry point.

### Shared services under `~/kaixuan`

PostgreSQL, Redis and other infrastructure services live in shared subdirectories
so multiple projects on the host reuse the same instance. Override via
`KAIXUAN_ROOT` when the host layout differs.

| Service | Default path |
|---|---|
| PostgreSQL data | `~/kaixuan/postgres` (bind mount for `llm-gateway-pg`) |
| PostgreSQL logs | `~/kaixuan/postgres/logs` |
| PostgreSQL backups | `~/kaixuan/postgres/backups` |
| PostgreSQL runtime | `~/kaixuan/postgres/run` |
| Redis data | `~/kaixuan/redis` (kept on the existing docker volume) |
| Redis logs | `~/kaixuan/redis/logs` |
| Redis runtime | `~/kaixuan/redis/run` |

The application project directory only stores application release, application
logs (`logs/gateway-<port>.log`, `raw-logs/`), attachments, and runtime state
(`run/active-*`, `run/llm-gateway-local-*.env`, `run/runtime.Dockerfile`,
`run/dependencies.{compose.yml,env}`). Service backups and migration logs never
live inside the project directory.

`postgres` and `redis` are created only when this deployment has to create the
corresponding local Docker dependency. If a PostgreSQL or Redis process/container
already exists, it is reused in place. The deployment does not recreate it, move
its data mount, alter its password, or flush keys. For an existing Docker
container, its actual data mount is diagnostic information; do not infer that a
new empty directory is safe to use as a replacement.

The migration path (`migrate_existing_pg_to_shared`) rebinds an existing
`llm-gateway-pg` container to the shared service directory and writes its
backup and migration log under `~/kaixuan/postgres/{backups,logs}`. After a
successful migration the legacy project-local copy at
`~/kaixuan/llm-gateway-go/postgres` is removed once it matches the shared
cluster. Run with `--cleanup-downloads` to also archive
`~/Downloads/llm-gateway-files/{postgres,redis,bin}` into
`~/kaixuan/postgres/backups/downloads-legacy-<UTC>.tar.gz` and delete those
subtrees (off by default).

### Redis discovery contract

Local and remote deployments never assume a fixed Redis container name. Three
fallback levels run in order and the first healthy match wins:

| Level | Input | Output | Where |
|---|---|---|---|
| 1. named | `nbjl-redis`, `llm-gateway-redis`, `redis`, `kx-redis` | container name | `scripts/deploy-local-lib.sh:dl_redis_try_named` |
| 2. scan | `docker ps` rows with image matching `redis\|valkey\|cache` or port `6379/tcp` | container name | `dl_redis_try_scan` (same file) |
| 3. system | host listeners on 6379/16379 via `ss` (Linux) or `netstat` (macOS) | `127.0.0.1:<port>` | `dl_redis_try_system` |

Each level is probed with `redis-cli PING` (inside the container or via host
client). When `redis-cli` is missing inside a candidate image, the probe trusts
the inspect result if `6379` is exposed and logs the fallback so operators
see what was accepted. The local helper writes
`[deploy-local] redis-discover: <level> → <target>` to stderr on success.

For 154 / 245 the same logic runs on the 252 host through
`scripts/deploy-lib/redis-discover.sh`, invoked by `deploy-seamless.sh` at
`[0.05/9] 部署前 Redis 发现`. The discovered name (or `host:port`) is written
to the target env file as `LLM_GATEWAY_REDIS_ADDR` only when missing; existing
SSOT entries win. Operators `OptionalS env-252.sh` with `REMOTE_REDIS_CONTAINER`,
`REMOTE_REDIS_HOST_PORT`, or `REMOTE_REDIS_PASSWORD` to short-circuit discovery
when the topology is stable.

## PostgreSQL and Redis

Local deployment prefers a healthy `llm-gateway-pg` (also recognizes the existing
Postgres-compatible names) and an existing Redis. If no usable dependency exists
and Docker Compose is available, it creates a bind-mounted local dependency under
the root. If neither an existing service nor Docker is available, it fails closed.

The database flow is non-destructive:

1. Check the existing cluster with `SELECT 1` and inspect whether the public schema
   is empty.
2. Apply the schema snapshot only for a genuinely empty cluster.
3. Run the gateway's idempotent `migrate` entry point for startup migrations.
4. Verify migration checksums/readiness through the gateway.

On 245/154 the database and Redis belong to the 252 topology. Those scripts only
connect to the injected target environment and apply pending idempotent migrations;
they never create local `postgres` or `redis` directories.

## Blue-green and rollback

Local deployment warms the candidate on the opposite port (`8781`/`8782`) and
requires `/healthz`, `/readyz`, and `/version` before promotion. With
`LLM_GATEWAY_UPSTREAM_FILE` configured, the file is updated for an external proxy
and both instances can overlap. Without a proxy, the script performs a bounded
controlled restart and explicitly reports that this is not zero-downtime.

Remote deployment uses the canary units and atomic upstream handoff in
`deploy-seamless.sh`. A failed candidate must not replace `current`; use
`rollback` only to a verified release. Binary rollback never rolls back database
schema.

## Verification contract

A successful local verify/deploy prints:

```text
VERIFY_TOOL=deploy-local.sh
VERIFY_DEVICE=local
VERIFY_PASS=1
```

Remote verification is emitted by the seamless runner and must include the target,
health/readiness, version identity, and model/auth evidence required by the 245 or
154 gate. Logs may include paths and statuses but never credentials. Before
finishing, preserve the pre-existing working tree changes and run:

```bash
bash -n scripts/deploy-local-lib.sh scripts/deploy-local.sh scripts/deploy-245.sh scripts/deploy-154.sh
bash tests/deploy_blue_green_contract_test.sh
bash tests/deploy_host_test.sh
bash tests/deploy_wrapper_test.sh
```
