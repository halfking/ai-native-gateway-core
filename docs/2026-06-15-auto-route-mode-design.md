# LLM Gateway Go v2.0 — Auto Route Mode Design

**Date**: 2026-06-14
**Author**: claude + cursor
**Status**: ✅ Implemented and pushed to `main` (commit b0b1bd98)

---

## 1. Goal

When a client sends `{"model": "auto"}` to the gateway, instead of failing
with "no available provider for model 'auto'", the gateway should:

1. **Classify the request** into one of 8 task types
2. **Score every candidate credential** for that task type against the
   client's preferred profile
3. **Forward the request** to the winning credential
4. **Surface the decision** in headers + admin UI + request_logs

All while preserving the existing routing behaviour for explicit model
requests — `auto` is opt-in.

---

## 2. Why

Production observations from the 2026-06-13 ~ 2026-06-14 wave of work:

- The codebase had **zero** task-type awareness in routing — every
  model received every request, regardless of whether it was suited.
- The 6-dim scoring data (price, latency, success rate, pressure ratio,
  context fit) existed in DB tables but was **never** consulted for
  routing decisions.
- The peak statistics infrastructure (credential_model_peak_1m /
  weekly_peak) was built for auto-tuning concurrency limits but not
  for routing.
- Clients had no way to express "I care about latency more than cost"
  — only `manual_priority` per credential.

Auto-route closes these gaps while leaving the existing manual flow
100% intact.

---

## 3. Architecture

```
                        ┌─────────────────────────────┐
   POST /v1/chat        │      relay/handler.go       │
   {"model":"auto"}     │  serveWithExecutor()        │
   X-Gw-Auto-Profile    │                             │
   X-Gw-Task-Hint       │   model=="auto"?            │
                        │     ↓ yes                   │
                        │  maybeResolveAuto()         │
                        └──────────────┬──────────────┘
                                       │
                                       ▼
                  ┌────────────────────────────────────────┐
                  │   autoroute/                            │
                  │   ┌──────────────┐                      │
                  │   │  classifier  │ ← heuristic + LLM fb │
                  │   │  (8 types)   │   2 sigs → 1 task    │
                  │   └──────┬───────┘                      │
                  │          ▼                              │
                  │   ┌──────────────┐                      │
                  │   │  index       │ ← credential_model_  │
                  │   │  (live 5min) │   index snapshot     │
                  │   └──────┬───────┘                      │
                  │          ▼                              │
                  │   ┌──────────────┐                      │
                  │   │  scoring     │ ← 6-dim weighted    │
                  │   │  (× profile) │   0-100 composite   │
                  │   └──────┬───────┘                      │
                  │          ▼                              │
                  │   ┌──────────────┐                      │
                  │   │  decision    │ → chosen model +    │
                  │   │  (top-N)     │   audit JSON        │
                  │   └──────────────┘                      │
                  └────────────────────────────────────────┘
                                       │
                                       ▼
                  ┌────────────────────────────────────────┐
                  │  request_logs:                          │
                  │    is_auto_request = true              │
                  │    task_type       = "code"            │
                  │    auto_profile    = "smart"           │
                  │    auto_decision   = {candidates,...}  │
                  │    auto_confidence = 0.92              │
                  └────────────────────────────────────────┘
```

The index is **refreshed every 5 minutes** by `bg/auto_index_refresher.go`,
which writes `credential_model_index` (per-cred × per-model live scores)
and `model_task_index` (per-canonical × per-task_type aggregated metrics).

---

## 4. Task Type Classification

8 task types defined in `autoroute/classifier.go`:

| Task          | Trigger signals                                       | Confidence |
|---------------|-------------------------------------------------------|------------|
| `chat`        | default fallback (no strong signal)                   | 0.1        |
| `reasoning`   | keywords "solve/prove/推导/证明", step-by-step prompts | 0.4+       |
| `code`        | keywords "function/code/算法/写代码", ``` fences     | 0.4+       |
| `agent`       | `tools[]` >= 3 AND any message has role `tool`        | 0.85       |
| `creative`    | keywords "write/translate/总结/创作", open-ended       | 0.4+       |
| `long_context`| estimated tokens > 50,000                            | 0.90       |
| `vision`      | any message part has type `image_url` / `image`      | 0.95       |
| `function_call`| `tools[]` length 1-2 (no tool results yet)            | 0.80       |

**Pipeline**:
1. Hard override (vision, long_context, agent) → returns immediately
2. Tool-based dispatch (function_call)
3. Keyword scoring (3 channels: reasoning, code, creative)
4. Tiebreak by priority: reasoning > code > agent > creative > function_call > vision > long_context > chat
5. Confidence = top channel score + 0.05 bonus if score >= 0.8

**LLM fallback**: when confidence < 0.7 (default), the decider calls
`LLMFallbackClassifier` — a thin shim that invokes a cheap chat model
with a 200-token prompt asking it to return ONE task type. 3-second
timeout. Falls back to heuristic result if LLM call fails.

---

## 5. 6-Dimension Scoring

```
PriceScore     = 100 * (1.5 - price / cohort_P75)    # cheaper → higher
SpeedScore     = 100 * (1 - p95 / cohort_max_p95)    # faster → higher
StabilityScore = success_rate * 100                  # 30-100 range
MatchScore     = tag_intersection(task, model) * 100 # 0-100
PressureScore  = (1 - active_sessions / concurrency_limit) * 100
ContextFit     = context_window / max(estimated, 4096) * 100, capped 100
```

Profile weights (sum normalised at score-time):

| Profile        | Price | Speed | Stability | Match | Pressure | ContextFit |
|----------------|-------|-------|-----------|-------|----------|------------|
| `smart`        | 25    | 25    | 20        | 25    | 10       | 15         |
| `speed_first`  | 10    | 50    | 20        | 15    | 5        | 10         |
| `cost_first`   | 50    | 10    | 15        | 20    | 5        | 10         |

Composite = weighted sum ÷ Σweights.

---

## 6. Profile Resolution

Precedence: `X-Gw-Auto-Profile` header → sticky (per-API-Key, 30 min TTL) → default (`smart`).

Sticky state lives in `api_key_auto_profile` (DB) with a process-local
in-memory cache (`MemoryProfileStore`) to avoid hammering DB on every
request.

---

## 7. API Surface

### Request

```http
POST /v1/chat/completions
Authorization: Bearer <key>
Content-Type: application/json
X-Gw-Auto-Profile: cost_first        # optional override
X-Gw-Task-Hint: code                 # optional client hint

{
  "model": "auto",
  "messages": [{"role":"user","content":"用python写一个快排"}],
  "stream": false
}
```

### Response

```http
HTTP/1.1 200 OK
X-Gw-Auto-Decision: {"task_type":"code","confidence":0.92,"classifier":"heuristic","profile":"smart","chosen_model":"claude-sonnet-4.5","chosen_credential_id":12,"candidates_top3":[{"model":"claude-sonnet-4.5","composite_score":85.2},{"model":"glm-5","composite_score":78.5},{"model":"qwen3-coder","composite_score":75.1}]}
Content-Type: application/json

{
  "id": "chat-...",
  "model": "claude-sonnet-4.5",   ← substituted
  ...
}
```

### Failure modes

| Scenario                          | Behaviour                                |
|-----------------------------------|------------------------------------------|
| `auto` with no decider wired      | Fall back to `claude-sonnet-4.5`         |
| Decider error (e.g. empty index)  | Fall back to `claude-sonnet-4.5`         |
| LLM fallback fails                | Trust heuristic at low confidence        |
| All candidates exhausted          | 503 with `no_available_provider`         |

---

## 8. SQL Schema Changes

3 new tables + 5 new request_logs columns. See
`docs/2026-06-15-auto-route-mode.sql` for the full DDL and
`docs/2026-06-15-auto-route-mode.down.sql` for the rollback.

Storage cost: ~50 MB for `credential_model_index` at 5-minute buckets
× 200 credentials × 30 days retention. Negligible.

---

## 9. Performance Budget

Per-request cost (model=auto, hot path):

| Step                       | Time      | Notes                              |
|----------------------------|-----------|------------------------------------|
| signal extraction          | < 1ms     | regex + simple count               |
| heuristic classification   | < 1ms     | keyword scan, no I/O               |
| index lookup               | < 1ms     | in-memory map                      |
| 6-dim scoring              | < 1ms     | arithmetic on small cohort         |
| LLM fallback (rare)        | 200-500ms | only when confidence < 0.7         |
| **Total (no LLM)**         | **< 5ms** | acceptable overhead                |
| **Total (LLM fallback)**   | 200-500ms | same as a regular chat completion  |

Index refresh (bg worker, every 5 min): ~2-5 seconds, runs in
dedicated goroutine, never blocks request handling.

---

## 10. Testing

- 30+ unit tests across classifier / scoring / profile / decision / index
- All pass: `go test ./autoroute/... ./bg/... ./admin/...`
- Integration tests require live PG — staged for v2.0.1 once we
  hit prod

---

## 11. Rollout Plan

| Phase | Date       | Servers                | Rollback strategy               |
|-------|------------|------------------------|---------------------------------|
| v2.0  | 2026-06-14 | 184 only (prod)        | revert to v0.79 image           |
| v2.0.1| TBD        | 184 + 71 + 252 + 245   | same, gradual canary            |

**v2.0 guard**: `LLM_GATEWAY_ENABLE_AUTO_ROUTE=false` falls back to
explicit-model routing (same as v0.79 behaviour). Set this env var
in the 184 deployment to disable.

---

## 12. Future Work (NOT in v2.0)

- A/B testing framework: serve 10% of auto requests with random model
  to collect ground-truth labels
- Per-tenant profile overrides (currently API-key scoped)
- Feedback loop: clients can submit "was this model good?" → adjust
  scoring weights over time
- Cross-region failover: index can include EU/US latencies when we
  deploy to a 2nd region