# Provider Reliability × UI Closure Audit (2026-09-01)

Scope: last ~24h commits (since 2026-08-31 06:00 +0800) touching admin/self_check_handlers.go, admin/vendor_credential_error_handlers.go, domains/streaming/executors/*, bg/provider_error_aggregator*, bg/daily_probe_audit, bg/self_check_worker, domains/feishubot/*, domains/notification/lark.go, web/src/**/Provider*.vue, web/public/menu-config.json, plus critical-path files (errorsx/classify.go, domains/credential/reputation_*, cmd/gateway/feishubot_init.go, cmd/gateway/main_notification.go, admin/probe_dashboard.go, admin/attempt_quality_api.go).

Unstaged working-tree changes that matter:
- admin/self_check_handlers.go (model selection via pm/c/credential_model_bindings + raw_model_name ranking + featured set dedup, replaces request_logs COUNT query).
- scripts/deploy-lib/lock.sh (see audit elsewhere).
- tests/deploy_seamless_state_machine_test.sh (recovered).

Hard limits respected: ≤5 P0/P1 findings, ≤8 P2 findings.

---

## P0

(none confirmed at P0)

---

## P1

### P1-1 — Vendor error → credential detail: upstream_response_preview leaks raw vendor body without sanitization
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/admin/vendor_credential_error_handlers.go:189` (loadVendorRecentFailures SELECT), mirrored in `admin/candidate_failure_handlers.go:88-101,165-175`.
- Source chain:
  1. Upstream HTTP body → `upstream.Error.Body` (`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/upstream/client.go:55`) — captured verbatim, capped at 4 KB.
  2. Persisted → `candidate_failure_logs_hot.upstream_response_body` (1024 chars) and `upstream_response_preview` (320 chars) by `CandidateFailureWriter.buildRow` (`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/streaming/executors/candidate_failure_logger.go:227-235`).
  3. Aggregator copies `LEFT(error_message, 200)` verbatim into `provider_error_details` (`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/provider_error_aggregator.go:161`).
  4. Admin endpoint serializes the rows as `upstream_response_preview` and `error_message` JSON fields via `loadVendorRecentFailures` (`admin/vendor_credential_error_handlers.go:50-59, 188-211`) and writes them straight to the wire.
- Evidence: no regex/redaction pass exists between the raw upstream body and the JSON response (searched `errorsx/`, `bg/provider_error_aggregator.go`, `domains/streaming/executors/candidate_failure_logger.go`, the `Load`/`Write`/`Serialize`/`Output` paths). Concrete test: a vendor that returns `{"error":{"message":"Invalid API key sk-abc…redacted-XYZ","code":"invalid_api_key"}}` will be returned to any authenticated admin caller with the full key surface.
- Risk: super-admin / admin role holders browsing the credential detail UI will see vendor-side copies of API keys, OAuth tokens, or echoed Bearer credentials if any vendor leaks them in the 4 KB body (a known risk in the 2026-08-18 GLM/Minimax incident).
- Why P1, not P0: only super-admin / admin role holders see the data, and the surface is bounded (320 chars preview + 200 chars aggregated). No cross-tenant data path. But the field is named `upstream_response_preview` — operators expect vendor status text, not secrets.
- Reproduction:
  - Input: vendor returns 401 with body `{"error":{"message":"Invalid API key sk-1234567890abcdef"}}`.
  - Expected: preview redacts `sk-…` patterns and any header-shaped tokens; aggregator truncates to `<redacted: api_key>` placeholder.
  - Actual: preview is the raw 320-char string and aggregator row carries the first 200 chars of `error_message`.
- Minimal regression test (proposed, not written):
  1. Insert a row into `candidate_failure_logs_hot` with `error_message = 'upstream said: api_key=sk-LIVE-abcdef0123…'` and `upstream_response_preview = 'Bearer sk-LIVE-abcdef0123…'`.
  2. Call `GET /api/vendors/credentials/{id}/error-detail?hours=24`.
  3. Assert `upstream_response_preview` matches `^[^B]*$` for known-leak patterns, and `error_message` no longer contains `sk-LIVE-…` raw substring.
- Fix sketch: add a single `errorsx.SanitizeErrorText([]byte) []byte` helper that strips `Bearer …`, `sk-…`, common JSON field values, and applies a length cap. Apply it in `CandidateFailureWriter.buildRow` (before INSERT) AND in `loadVendorRecentFailures` (defense in depth). Do NOT aggregate `error_message` into `provider_error_details.error_message` without sanitization — change aggregator to store `error_kind + error_code + sha256(error_message)` as the dedup key and a sanitized preview in the column.

### P1-2 — Lark notification transport has no retry on transient HTTP / API failure
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/notification/lark.go:265-307` (`LarkBotChannel.sendJSON`); mirrored in `refreshAccessToken` (lines 212-263) and the per-recipient loops in `Send`/`SendCard` (lines 73-94, 114-130).
- Call chain:
  1. `feishubot.AlertRouter.PushAlert` (`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/feishubot/alert_router.go:144-166`) builds a card and calls `channel.SendCard`.
  2. `LarkChannelAdapter.SendCard` (`/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/feishubot/lark_adapter.go:52-58`) → `notification.LarkBotChannel.SendCard` (`lark.go:99-130`).
  3. For each recipient, `sendJSON` does exactly ONE POST to `https://open.feishu.cn/open-apis/im/v1/messages`. On `resp.StatusCode != 200` it returns `fmt.Errorf("notification: lark status %d: %s", resp.StatusCode, raw)`. No retry, no `Retry-After` header parsing, no 401 token-refresh.
  4. On error the recipient loop continues to the next recipient (`continue`), but each `recipient` failure causes one log line and the final returned error is the LAST failure only (`lastErr`).
- Evidence: `grep -n "retry\|Retry\|maxRetr\|backoff" /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/notification/lark.go` returns ZERO matches. The 30s `http.Client.Timeout` (line 58) is the only bound. There is no separate refresh-token retry on 401/99991663.
- Risk: a single transient 5xx from Feishu (rate limiting, gateway hiccup) drops the alert silently for every recipient. Severity filter (`IsSeverityPassing`) and deduper fingerprint already rate-limit on the alert side, so the alert won't be re-attempted for 60 s — by then the operator has missed a real incident.
- Why P1, not P0: critical alerts (`SeverityCritical`) bypass quiet hours (`alert_router.go:109-111`) and the alert router dedupe is per-fingerprint-per-60s, so for a brief blip the alert can be re-pushed once the dedupe window expires. But for the entire 60 s window, NO alert reaches the user, which defeats the purpose of severity routing.
- Reproduction:
  - Input: Feishu returns 429 with `Retry-After: 1`.
  - Expected: at least one retry with backoff ≤ Retry-After.
  - Actual: `sendJSON` returns an error; the Feishu-side alert is lost until a NEW alert (different fingerprint) is pushed 60 s later.
- Minimal regression test: feed `httptest.Server` a handler that returns 429 on the first call and 200 on the second; call `LarkBotChannel.Send` with one recipient; assert `httpClient` issued ≥2 requests.
- Fix sketch: wrap `c.httpClient.Do(req)` in a small `retryableHTTPDo` that retries 5xx / 408 / 429 / network errors up to 3 times with bounded backoff (100 ms / 500 ms / 2 s). On 401 or Feishu `code == 99991663/99991661`, force a token refresh and retry once.

### P1-3 — `self_check_handlers.handleTrigger` no longer sanitizes invalid `model_source` early in the multi-model path
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/admin/self_check_handlers.go:603-606` (the unstaged version on disk).
- Call chain:
  1. POST `/api/admin/self-check/trigger` with empty body.
  2. `handleTrigger` reads `self_check_settings` (lines 529-549).
  3. Builds the model set from `featured` / `top10` / `both` (lines 552-628).
  4. **Only AFTER** populating `modelSet`, lines 603-606 reject an invalid `model_source` with 400.
  5. If invalid, the function still executed two DB queries (the settings read at 529-549 and the top-N read at 569-602 inside the `top10 || both` branch).
- Evidence: trace of branches on a settings row with `model_source='legacy'` → `top10`/`featured`/`both` branches all return false → the code falls into the `top10 || both` query, scans rows, then returns 400 at line 603-606. The settings-row query already happened, but the `top10` JOIN over `provider_models/credential_model_bindings/credentials/providers` is wasted CPU/IO. Trivial DoS amplifier for a multi-tenant deployment.
- Why P1: any unauthenticated-but-admin path (or a misconfigured role) that POSTs to the trigger can burn a multi-table join on every invalid request. With concurrent traffic this becomes a CPU drain.
- Reproduction:
  - Input: `POST /api/admin/self-check/trigger` with `{}` body when `self_check_settings.model_source='legacy'`.
  - Expected: immediate 400 before any non-settings DB hit.
  - Actual: top-N JOIN runs first (lines 569-602), then 400 returned.
- Minimal regression test: mock DB, set `model_source='legacy'`, call handler, assert exactly 1 `QueryRow` (settings) was issued and 0 `Query` (top-N).
- Fix sketch: move the `if settings.ModelSource != "featured" && settings.ModelSource != "top10" && settings.ModelSource != "both"` guard to immediately after line 533 (`Scan` of the settings row).

---

## P2

### P2-1 — `ReputationWorker.Stop()` may leak the goroutine on shutdown
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/credential/reputation_worker.go:133-155`.
- Evidence: `Stop()` waits up to 2 s (`time.After(2*time.Second)`) and returns without canceling the parent context or signaling it. If `Run(ctx)` is stuck on a slow `creds.List("")` or `store.SaveTimeseries`, the goroutine outlives `Stop()`. The same pattern is present in `bg/daily_probe_audit.go` (no stop channel at all in `run` — only the outer goroutine select has it; once the run call is in flight, stop is no-op).
- Risk: in-process tests and graceful shutdown leave zombie goroutines referencing the worker's `store`/`scorer`; once the process actually exits, the goroutines die with it. No memory leak in production but a noisy `pprof` in tests.
- Fix: cancel the parent context in `Stop()` before waiting; or make `Run` accept a derived context that `Stop()` cancels.

### P2-2 — `BaseWorker.Stop` does not cancel the parent context; cancelled child only
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/base_worker.go:104-123`.
- Evidence: `Stop` invokes `cancel()` on the internal `context.WithCancel(parent)` (line 91), not on `parent` itself. This is correct but the worker does NOT call `parent.Done()` so the user thinks the system stopped when in fact only the worker did. Acceptable design but worth noting for callers wiring the same `ctx` into multiple workers (none currently do).
- Risk: minor; only relevant if a caller chains multiple BaseWorkers on the same parent and expects one to take down the others.

### P2-3 — `executeCh at.go:runModels` semaphore never closes when one goroutine panics
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/self_check_worker.go:326-339`.
- Evidence: `sem := make(chan struct{}, 5)` plus `sem <- struct{}{}` and `defer func() { <-sem }()`. If a goroutine in `runModel` panics BEFORE the `<-sem` runs (e.g. via `defer` ordering with panic), the slot is held. There is no `recover()` in the goroutine. With panic recovery disabled at the package boundary (no `recover()` in `runModel`), a panic leaks a semaphore slot permanently, and after 5 panics all subsequent `runModels` calls block forever.
- Risk: theoretical (panics are exceptional). Acceptable trade-off given the bounded model set; flag for monitoring.

### P2-4 — `SelfCheckWorker.Start` ignores `Stop()` calls made between Start and the goroutine launch
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/self_check_worker.go:105-188`.
- Evidence: `Start` uses `startOnce.Do` and launches the goroutine unconditionally inside it. `Stop` closes `stopCh` and cancels `cancel`. If `Stop` is called AFTER `Start` returned but BEFORE the goroutine read `w.stopCh`/`w.cancel`, both the channel closure and cancel happen first; the goroutine sees `ctx.Done()` immediately and exits. This is correct, but `Stop` does not check whether `Start` ever set `w.cancel` — `cancel` may be the zero `context.CancelFunc` (nil) if `Start` is racy with the stopCh close path. Actually `Start` always assigns `w.cancel` before goroutine launch (line 117-118), so this is safe today but brittle to future refactors.
- Fix: document the invariant or add `if cancel != nil { cancel() }` defensively.

### P2-5 — `provider_error_aggregator` watermark regression check only runs when `groups > 0`
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/bg/provider_error_aggregator.go:292-309`.
- Evidence: the "watermark regressed" / "did not advance" branches both gate on `groups > 0`. On idle ticks (no new rows) the watermark never advances but `groups == 0` masks the regression check. This is intentional and well-commented, but worth confirming that the upstream `new_source_rows` CTE stays non-empty in pathological cases (e.g. negative-`aggregation_id` historical rows producing zero `DISTINCT ON` groups). Acceptable as-is.

### P2-6 — `loadVendorErrorSummary` is called even when the credential row exists but has zero candidate failures
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/admin/vendor_credential_error_handlers.go:101-106, 162-185`.
- Evidence: the function issues a `SELECT … GROUP BY error_kind …` even when `len(summary)` will be 0. Combined with P1-3 above (settings validation), admin handlers do not short-circuit when the underlying table is empty.
- Risk: low; this query is cheap (covered by the partition pruning on `ts`). No fix needed; flag for future cost awareness.

### P2-7 — `health_tracker` always fires a goroutine on `OnSuccess` / `OnError` even when Redis is unavailable
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/domains/streaming/executors/health_tracker.go:73-97, 114-135`.
- Evidence: the early-return `if !h.Enabled()` covers `r == nil` but not `r.client == nil` (the `Enabled()` method returns `r != nil && r.client != nil`, so the early return IS hit). However, when `Enabled()` is true and Redis is down, the goroutine still spawns and fails its `Append` silently (slog.Warn). Under a sync-retry storm (120s loop × N candidates), this is N goroutines per candidate failure, all waiting up to 3-10 s on Redis I/O. Acceptable but worth a `sync.Pool` or bounded worker pool.
- Risk: bounded by Redis timeout (3-10 s) and goroutine count; not a leak, just wasteful under load.

### P2-8 — Web `ProviderDetailView` does not surface the credential-level "Recent failures" body without sanitization (mirror of P1-1)
- File: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/web/src/views/provider-detail/ErrorDetailTab.vue:118-126`.
- Evidence: the table renders `item.error_message` and `item.upstream_response_preview` raw with `<td class="message-cell">{{ item.error_message ?? '—' }}</td><td class="preview-cell">{{ item.upstream_response_preview ?? '—' }}</td>`. No client-side redactor, no tooltip warning. If P1-1 is fixed on the backend, the frontend renders the sanitized form; if not, secrets appear in the admin's browser DOM (still safer than transit, but still exposed to anyone screen-sharing or saving HTML).
- Fix: render with `v-html` + a `<SafeText>` wrapper OR rely on backend sanitization (preferred).

---

## Already-fixed (skipped)

- **SelfCheckPanel `postTriggerReloadTimer` leak** — commit `e2de193a8` (2026-09-01 04:34 +0800) `fix(web): SelfCheckPanel 组件卸载时清理 postTriggerReloadTimer` adds `clearTimeout` in `onUnmounted`. Verified at `web/src/views/SelfCheckPanel.vue:124-130, 210-216`.
- **SelfCheck web composable lifecycle** — commit `82e324b79` `fix(web): 修复 composable 生命周期管理与内存泄漏`.
- **LiveStreamRedisStore sentinels + `errors.Is` adapter** — commit `629b4fd81`.
- **`provider_error_aggregator` watermark regression detection + zombie-lock streak gauge** — commits `b5335637e`, `c2d67f63d`, `a88f3a5de`.
- **HTTPHealthChecker concurrency** — `5460d3c80`.
- **`useRequestDetailLoader` cachePut merge** — `375795fc7`.
- **Self-calls follow own listen port / mv-refresher on traffic-only** — `48dd51fa7`, `16068c099`.
- **L3 continuation + R12 attempt cap** — well-tested in `domains/dispatch/planner_test.go:61-171` (E5 / E100 budget gates).
- **Transparent resume (errorsx projection)** — `candidate_failure_logger.go:251-255` already passes `errorsx.ProjectRecovery(kind)` into `recoveryContext` so consumers can see `generic_retryable/candidate_failover/transparent_resume` flags. No orphan tool-call wiring change needed.
- **Circuit-breaker fallback ("think / 不中断")** — verified `domains/transformation/circuit_breaker.go` does not interfere with stream continuity: it observes stream errors and calls `ShouldFallback()` for transport decisions. The actual thinking-frame fallback path goes through `params.OnNodeJump` (`domains/streaming/executors/executor.go:1193-1197` → `domains/streaming/handler.go:3956-3960` → `preStream.writeThinking(message)`). When the primary vendor returns a think-only stream and aborts, `streamInterruptedError{resumable: true, kind: KindTransient}` is returned (`executor_chat.go:1261-1277`), and the dispatcher mover handles continuation through `domains/dispatch/dispatcher.go:149` ("An alternative model remains: continuation"). No duplication or content loss detected.
- **API key health pipeline (G)** — `credentialhealth/recorder.go:54-93` writes each success/failure to a Redis LIST; `credentialhealth/prober.go:35-100` reads back and decides probe verdict; `domains/streaming/executors/health_tracker.go` records from the dispatcher. There is no `credential_health` SQL table — health state lives in Redis, which the routing layer reads via `e.HealthTracker.OnSuccess/OnError`. No missing link; the wiring is healthy.

---

## Verification gaps

1. **Staging-only verification needed for P1-1**: the leak surface depends on what real vendor bodies contain. The proposed regression test (synthesized leak string) is the minimum; a follow-up should sample 50 representative `upstream_response_preview` values from `candidate_failure_logs_hot` over a 24h window in env-154 and run a regex sweep for `sk-`, `Bearer`, `api_key=` substrings.
2. **Staging-only verification needed for P1-2**: the retry fix needs to be exercised against Feishu's real rate-limit error envelope (HTTP 429 with `Retry-After`, or 200 + `code: 99991400`). Synthetic `httptest.Server` covers the HTTP layer but not the Feishu API semantics.
3. **P1-3 is logic-correct without staging**, but the new `top10` ranking query joins 4 tables; an `EXPLAIN ANALYZE` on env-154 is recommended to confirm it does not regress the original `request_logs` 7-day aggregate performance.
4. **`feishubot.Plugin.Stop` not audited** — out of strict scope. Worth a follow-up to confirm the plugin stops its dedupers and eventbus subscriptions without leak under SIGTERM.
5. **`domains/credential/reputation_worker.go` SQL path not exercised end-to-end in this audit** — only the lifecycle was reviewed.

---

## Summary table

| ID  | Severity | Title                                                                              | File:Line                                      | Evidence                                                                                                          | Repro / Test suggestion                                                              |
| --- | -------- | ---------------------------------------------------------------------------------- | ---------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| P1-1 | P1       | Upstream body / `error_message` returned to admin without sanitization             | `admin/vendor_credential_error_handlers.go:189` (chain 1-4 above) | Vendor body echoed verbatim; no redaction in writer, aggregator, or admin handler                                       | Insert row with `error_message='sk-LIVE-…'` and assert response redacts                  |
| P1-2 | P1       | Lark `sendJSON` has no retry on transient 5xx/429/network                           | `domains/notification/lark.go:265-307`         | Zero `retry`/`Retry` matches in file                                                                              | httptest returns 429 then 200; assert ≥2 POSTs                                           |
| P1-3 | P1       | Invalid `model_source` triggers top-N JOIN before 400                              | `admin/self_check_handlers.go:603-606`         | Branch order: populate `modelSet` first, validate second                                                          | Mock DB with `model_source='legacy'`; assert 0 `Query` calls                              |
| P2-1 | P2       | `ReputationWorker.Stop` may leak goroutine if `Run` is slow                        | `domains/credential/reputation_worker.go:133-155` | 2 s `time.After` ceiling; no parent cancel                                                                          | Mock a 10 s `creds.List`; assert goroutine still alive after Stop                        |
| P2-2 | P2       | `BaseWorker.Stop` only cancels child context (by design)                           | `bg/base_worker.go:104-123`                    | Only `cancel` of internal child is invoked                                                                          | Documentation                                                                              |
| P2-3 | P2       | `runModels` semaphore slots leak on panic                                          | `bg/self_check_worker.go:326-339`              | No `recover()` in goroutine; `defer` ordering can lose `<-sem`                                                      | Inject panic in `runModel`; assert 5 panics lock subsequent calls                       |
| P2-4 | P2       | `SelfCheckWorker.Stop` race on `w.cancel` assignment                              | `bg/self_check_worker.go:105-188`              | nil `cancel` is not checked before invocation (currently safe by code order)                                          | Race-detector test                                                                      |
| P2-5 | P2       | Watermark regression check gated on `groups>0` (intentional)                       | `bg/provider_error_aggregator.go:292-309`      | Commented; OK by design                                                                                             | None                                                                                       |
| P2-6 | P2       | `loadVendorErrorSummary` runs even when no failures                               | `admin/vendor_credential_error_handlers.go:101` | Always issues GROUP BY query                                                                                          | None (cost-tracked)                                                                       |
| P2-7 | P2       | `health_tracker` spawns goroutines on Redis-down case                              | `domains/streaming/executors/health_tracker.go:73` | Goroutine + 3-10 s timeout per failed candidate                                                                      | None (bounded by timeout)                                                                |
| P2-8 | P2       | Frontend renders raw upstream body in ErrorDetailTab                                | `web/src/views/provider-detail/ErrorDetailTab.vue:118-126` | Mirrors P1-1; HTML renders verbatim                                                                                  | Rely on backend fix                                                                      |

