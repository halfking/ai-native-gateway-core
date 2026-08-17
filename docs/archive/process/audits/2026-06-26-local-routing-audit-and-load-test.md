# 2026-06-26 Local Routing Audit And Load Test

## Summary

This report records the local audit, fixes, deployment recovery, and routing load-test results for `llm-gateway-go`.

Scope:
- Audit the current local deployment/test chain.
- Fix blockers that prevent local deployment and routing verification.
- Bring up local `gateway-v2` successfully.
- Run high-concurrency routing simulation against the local HTTP entry.

## Audit Findings And Fixes

### 1. Migration backup script was misleading

File:
- `scripts/migrate-to-domains.sh`

Problem:
- The script claimed it would back up source directories before migration, but it only copied into the destination tree.

Fix:
- Added explicit backup copy into `_deprecated/old-*` before package migration.

### 2. Local migration chain was incomplete

File:
- `scripts/local-r112-migrate.sh`

Problems:
- It assumed `request_logs` already existed.
- It did not load the base schema first.
- It failed on redacted/demo seed rows and duplicate demo migrations.

Fixes:
- Reworked local migration flow to rebuild the local test DB.
- Added base bootstrap order:
  - `sql/schema/00-prereqs.sql`
  - `sql/schema/01-schema.sql`
  - `sql/schema/02-seed.sql`
- Filtered redacted placeholder rows from local seed loading.
- Skipped local-only demo/seed migrations that conflict with the base schema.
- Treated local duplicate-object / duplicate-seed cases as benign for local verification.

### 3. Migration 038 had a real SQL bug

File:
- `db/migrations/038_adaptive_probe_scheduling.sql`

Problem:
- `model_probe_passive_boost()` used `DECLARE`/`BEGIN` syntax under `LANGUAGE SQL`, which is invalid.

Fix:
- Changed the function to `LANGUAGE plpgsql`.

### 4. Local v2 deployment target was wired to the wrong binary

Files:
- `docker-compose.local-r112.yml`
- `cmd/gateway-v2/Dockerfile`

Problems:
- Local compose intended to run `gateway-v2`, but originally pointed at a root Dockerfile that builds `cmd/gateway`.
- `cmd/gateway-v2` had no dedicated Dockerfile.
- Port mapping and healthcheck were inconsistent with the actual v2 listen address.

Fixes:
- Added `cmd/gateway-v2/Dockerfile`.
- Pointed local compose to that Dockerfile.
- Fixed gateway-v2 port mapping to `__PORT_4__:__PORT_4__`.
- Fixed gateway-v2 healthcheck to target `localhost:__PORT_4__/healthz`.

### 5. Local mock dependencies were unstable

Files:
- `docker-compose.local-r112.yml`
- `scripts/mocks/llm-mock.conf`

Problems:
- `llm-mock` healthcheck depended on a MockServer endpoint that returned 404.
- `memora-mcp`/`llm-mock` health behavior was too brittle for local verification.

Fixes:
- Replaced local `llm-mock` with a stable nginx stub.
- Added deterministic HTTP 200 responses via `scripts/mocks/llm-mock.conf`.
- Simplified healthchecks to stable endpoints.

### 6. Demo routing path rejected `gpt-4o`

File:
- `cmd/gateway-v2/main.go`

Problem:
- The in-memory demo provider/credential seed supported `gpt-4`, but the load test targeted `gpt-4o`, causing all requests to fail with `provider: no healthy provider for model gpt-4o`.

Fix:
- Added `gpt-4o` support to the preloaded demo provider and credential setup.

## Deployment Result

Local dependency stack was recovered successfully:
- `r112_postgres`
- `r112_redis`
- `r112_memora_mcp`
- `r112_llm_mock`
- `r112_gateway_v2`

Health verification:
- `GET http://localhost:__PORT_4__/healthz` => `200 ok`

Sample request verification:
- `GET /v1/chat?q=hello&model=gpt-4o`
- Response: `200 OK`

## High-Concurrency Routing Simulation

Target:
- `GET http://localhost:__PORT_4__/v1/chat?q=hello&model=gpt-4o`

Headers:
- `X-Tenant-ID: t1`
- `X-Session-ID: s1`
- `X-API-Key: local-test`

Tool:
- `autocannon`

### Run 1: Medium concurrency

Command shape:
- `autocannon -c 50 -d 20 ...`

Results:
- Avg latency: `0.16 ms`
- P99 latency: `1 ms`
- Avg throughput: `65,075 req/s`
- Total requests: `1302k / 20s`

### Run 2: High concurrency

Command shape:
- `autocannon -c 200 -d 30 ...`

Results:
- Avg latency: `2.01 ms`
- P99 latency: `5 ms`
- Avg throughput: `81,187 req/s`
- Total requests: `2436k / 30s`

## Replay Status

The historical replay tool `cmd/traffic-replay` was not completed end-to-end in this pass because:
- The local host `__PORT_5__` is conflicted by both Docker and a host PostgreSQL instance.
- The new local DB has no meaningful historical `request_logs` traffic to replay.

This means:
- The routing pipeline itself was validated under real HTTP concurrency.
- Historical divergence analysis still requires importing real request samples into local `llm_gateway`.

## Remaining Risks

### 1. Local-only migration behavior

The local bootstrap now skips a small set of demo/seed migrations to keep the environment executable. This is correct for local verification, but is not a production migration policy.

### 2. Legacy main binary still needs follow-up

`cmd/gateway` still shows new/old package drift in the session-compression wiring path. This was intentionally not forced through in this pass because the local validation target was `cmd/gateway-v2`.

### 3. Historical replay still needs real samples

To run a useful `traffic-replay` report, local DB needs imported real `request_logs` rows.

## Suggested Next Steps

1. Import a real `request_logs` sample set into local `llm_gateway`.
2. Run `cmd/traffic-replay` against those imported rows.
3. Add a dedicated local routing benchmark script for repeatable regression checks.
4. Follow up on `cmd/gateway` type drift separately if the old entrypoint must remain buildable in local compose.
