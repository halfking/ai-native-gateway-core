# v2 Pipeline Feature Flag (R1.12)

> **Status (2026-06-26)**: Implemented behind `LLM_GATEWAY_V2_ENABLED`. Default
> OFF. **Not yet enabled in production.** Local + CI tests pass; full
> integration validation pending.
>
> **Owner**: llm-gateway-go team. Related plan:
> [`docs/go-migration/2026-06-12-master-plan.md`](../../go-migration/2026-06-12-master-plan.md)
> and [`docs/go-migration/2026-06-12-decisions-locked.md`](../../go-migration/2026-06-12-decisions-locked.md).

## 1. Goal

Add an opt-in path inside `cmd/gateway/main.go` (the production v1 binary)
that exposes the new Hook Pipeline architecture as a parallel `/v2/*` route
group. Operators can route a small percentage of traffic to `/v2/*` to
灰度 test the new Pipeline before committing to a full cutover.

The flag is designed so a single `restart` toggles between "v1 only" (the
default today) and "v1 + v2 side-by-side". No dual-binary deployment, no
nginx rewrite, no traffic split at the application layer.

## 2. Enable / Disable

The flag is read once at process startup from the env var
`LLM_GATEWAY_V2_ENABLED`.

| Env var                       | Default | Effect                                                              |
|-------------------------------|---------|---------------------------------------------------------------------|
| `LLM_GATEWAY_V2_ENABLED`      | unset   | **OFF** (production safe). No `/v2/*` routes registered.            |
| `LLM_GATEWAY_V2_ENABLED=true` | —       | ON. Registers `/v2/*` route group. v1 routes untouched.             |
| `LLM_GATEWAY_V2_ENABLED=false`| —       | OFF. Same as unset.                                                 |

Truthy values: `1`, `true`, `yes` (case-insensitive). Anything else
(including empty, `0`, `no`, `off`, garbage) → OFF.

### 2.1 Per-stage sub-flags (R1.12 灰度 tuning)

| Env var                       | Default | Effect                                               |
|-------------------------------|---------|------------------------------------------------------|
| `LLM_GATEWAY_V2_CACHE`        | true    | Enable cache_lookup + cache_save stages.             |
| `LLM_GATEWAY_V2_SECURITY`     | true    | Enable security hook (intent + threat detection).    |
| `LLM_GATEWAY_V2_AUDIT`        | true    | Enable audit_log hook.                               |
| `LLM_GATEWAY_V2_OBSERV`       | true    | Enable tracing + metrics hooks.                      |
| `LLM_GATEWAY_V2_STREAMING`    | true    | Enable streaming hook.                               |

These mirror `cmd/gateway-v2/main.go`'s `v2Config` so both binaries stay in
sync. Differential 灰度 (e.g. enable cache, disable audit) is supported
without re-building.

### 2.2 Where to set it

The flag is consumed by the `cmd/gateway` process, so set it on whichever
node runs the v1 binary (currently **184 k3s** and **71 host docker**).
Do NOT add it to `deploy-llm-gateway-go-*.sh` until R1.13 (full DB-backed
v2). Per task scope, **no deploy script changes** in this commit.

For manual 灰度, prefix the env var on the systemd unit or k8s Deployment
spec, then `systemctl restart llm-gateway-go` (71) or
`kubectl -n pms-test rollout restart deploy/kx-llm-gateway-go` (184).

## 3. Route Map

| Path                       | When OFF                | When ON                                                              |
|----------------------------|-------------------------|----------------------------------------------------------------------|
| `/v1/chat/completions`     | v1 chat relay           | v1 chat relay (unchanged)                                            |
| `/v1/messages`             | v1 messages             | v1 messages (unchanged)                                              |
| `/v1/responses`            | v1 responses            | v1 responses (unchanged)                                             |
| `/v1/models`               | v1 models               | v1 models (unchanged)                                                |
| `/healthz`                 | v1 healthz              | v1 healthz (unchanged)                                               |
| `/metrics`                 | v1 prom metrics         | v1 prom metrics (unchanged)                                          |
| `/api/*` (admin)           | v1 admin                | v1 admin (unchanged)                                                 |
| `/v2/chat/completions`     | **404**                 | v2 Pipeline demo (in-memory deps; staging only)                      |
| `/v2/healthz`              | **404**                 | `{"service":"llm-gateway-go","pipeline":"v2","status":"ok"}`         |
| `/v2/*` (anything else)    | **404**                 | 404 from the v2 sub-mux (only the two routes above are registered)   |

### 3.1 v1 ↔ v2 isolation

`registerV2PipelineRoutes(mux)` mounts the v2 sub-mux under `/v2/`. The v1
mux's pre-existing routes (`/v1/*`, `/healthz`, `/metrics`, `/api/*`) keep
their original handlers — the registration order in `main.go` is:

1. v1 `/healthz`, `/metrics`
2. **v2 sub-mux at `/v2/`** (new)
3. v1 `/v1/*`
4. v1 admin `/api/*`

Go's `http.ServeMux` uses longest-prefix match, so `/v2/healthz` and
`/v2/chat/completions` resolve to the v2 sub-mux while `/v1/*` and the
admin API stay on v1.

## 4. 灰度 Rollout Plan

### 4.1 Pre-flight (must complete before enabling)

- [x] `cmd/gateway` builds clean (`go build ./...` exit 0).
- [x] `cmd/gateway` vets clean (`go vet ./...` no diagnostics).
- [x] New unit tests cover OFF/ON/isolation/nil-safe paths (10 cases).
- [x] `cmd/gateway-v2` existing tests still pass (13/13).
- [ ] R1.12 milestone: full e2e run on a dev cluster (not in this commit).

### 4.2 Suggested 灰度 sequence

1. **Shadow only (1%)** — set `LLM_GATEWAY_V2_ENABLED=true` on the **184
   k3s pod only**. Watch logs for `"v2 pipeline: LLM_GATEWAY_V2_ENABLED=true,
   /v2/* routes registered"`. Manually `curl /v2/healthz` from inside the
   pod to confirm registration. **No production traffic is routed to `/v2/*`
   because nginx points 56→71:8781 and llm.kxpms.cn upstream does not include
   `/v2/`.**
2. **Manual probe (5 requests)** — `curl /v2/chat/completions?q=test&model=gpt-4`
   from a non-production client. Verify the response is
   `{"request_id":"v2-req-…","status":"ok","tenant_id":"…"}`.
3. **Burst test (1k RPS)** — use `vegeta` or `wrk` against `/v2/healthz`
   from inside the cluster to measure latency and memory.
4. **Nginx-split test (1% real traffic)** — temporarily add a `location
   /v2/` to `kxpms-cn-all-vhosts.conf` pointing at the v2-enabled pod with
   a 1% weight (cookie-based or `split_clients`). Watch
   `request_logs` for any errors.
5. **Production cutover decision** — once Steps 1–4 are green, decide whether
   to R1.13 cutover (rewrite `cmd/gateway/main.go` to use the Pipeline path
   natively) or shelve.

### 4.3 Rollback

Set `LLM_GATEWAY_V2_ENABLED=false` (or unset) and restart the process.
The mux will revert to v1-only. No DB schema or persistent state changes —
rollback is a single env var + restart.

## 5. Current Limitations

This is a **staging entry point**, not a production cutover. Specifically:

| Limitation                                              | Why                                              |
|---------------------------------------------------------|--------------------------------------------------|
| v2 deps are **in-memory**                               | The v2 Pipeline demo uses in-memory stores; the production DB pool / Redis / Memora are deliberately NOT touched so an accidental enable cannot affect v1 traffic. |
| `transform` stage is omitted                            | The `domains/transformation` package registers prometheus metrics under the `transport_*` prefix (a pre-existing copy-paste bug in `metrics.go`), which conflicts with `transport/metrics.go`. Importing both panics the process at init. The `transform` stage is replaced by a no-op placeholder (`passthroughHook`) so stage order is preserved. Fixing the metric name conflict is tracked in R1.13+. |
| `passthroughHook` for transform records stage metadata  | Operators can confirm the Pipeline ran end-to-end even with the placeholder by inspecting `Metadata["v2_stage_transform"]` in tests. |
| No streaming response body                               | `LLM_GATEWAY_V2_STREAMING=true` enables the `streaming` Hook but the demo handler returns JSON only. Real streaming integration is R1.13+. |
| No upstream call                                        | The v2 demo echoes a `status: ok` response; it does not actually call any LLM provider. Real upstream wiring is R1.13+. |
| No Memora persistence                                   | The v2 demo doesn't write to Memora. Production Memora integration comes with R1.13+. |
| No JWT / API key auth                                   | The v2 demo accepts any request. Production auth integration comes with R1.13+. |

## 6. Future Work (R1.13+)

| Task                                                 | Why                                                |
|------------------------------------------------------|----------------------------------------------------|
| Rename `domains/transformation/metrics.go` to use `transformation_*` prefix | Unblock importing `domains/transformation` from `cmd/gateway`; enable the full transform stage. |
| Wire v2 deps to production DB pool (`dbConn.Pool()`) | Replace in-memory stores with PG-backed credential / provider stores. |
| Wire v2 deps to production Redis                    | Move cache from `cache.NewInMemoryStore()` to the shared `*redis.Client`. |
| Wire v2 deps to production Memora                    | Persist audit events via `memora.Sink` instead of `audit.NewInMemorySink`. |
| Move v2 wiring into `cmd/gateway/main.go`            | Once integration tests pass under load, drop the feature flag and make the Pipeline the default data plane. |
| Delete `cmd/gateway-v2/main.go`                      | The demo binary is replaced by the unified `cmd/gateway` once R1.13 ships. |

## 7. File Map

| File                                                    | Status   | Purpose                                       |
|---------------------------------------------------------|----------|-----------------------------------------------|
| `cmd/gateway/main_v2_pipeline.go`                       | **NEW**  | Feature flag, route registration, Pipeline wiring. |
| `cmd/gateway/main_v2_pipeline_test.go`                  | **NEW**  | Unit tests for OFF / ON / isolation / nil-safe. |
| `cmd/gateway/main.go`                                   | modified | One-line call to `registerV2PipelineRoutes(mux)` after healthz registration. |
| `cmd/gateway-v2/main.go`                                | untouched | Demo binary remains as the integration-test target. |
| `docs/r112/v2-pipeline-feature-flag.md`                 | **NEW**  | This document.                                |

## 8. Quick Verification

```bash
# 1. Build clean.
cd services/llm-gateway-go
go build ./... && echo "BUILD OK"

# 2. Vet clean.
go vet ./... && echo "VET OK"

# 3. New v2 tests pass.
go test -count=1 ./cmd/gateway/ -run TestV2Pipeline -v

# 4. Existing v2 demo tests still pass.
go test -count=1 ./cmd/gateway-v2/... -v

# 5. Confirm OFF by default — no /v2/* registered.
LLM_GATEWAY_V2_ENABLED= go test -count=1 ./cmd/gateway/ -run TestV2Pipeline_MuxIsolation_DefaultOff -v

# 6. Confirm ON — /v2/* registered alongside v1.
LLM_GATEWAY_V2_ENABLED=true go test -count=1 ./cmd/gateway/ -run TestV2Pipeline_MuxIsolation_V1AndV2Coexist -v
```