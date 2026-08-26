# Popular Models Follow-up Logic Points

> **2026-08-26 17:16 audit 修正**：5 项 follow-up 全部已合入 origin/main，详见每个 LP 的 `Commit` 字段。本文档保留为设计原档 + 实际落地位置对照表。

| ID | Scope | Estimated LOC | Depends on | Status | Commit |
|---|---|---:|---|---|---|
| LP1 | Tenant-scoped recent ZSET and SQL usage query | 130 | - | ✅ landed | `0216557f4` |
| LP2 | Per-tenant response cache and picker integration | 80 | LP1 | ✅ landed | `0216557f4` |
| LP3 | Configurable lookup window and SQL short-circuit | 80 | LP1 | ✅ landed | `0216557f4` |
| LP4 | SSE overlay outcome metric | 100 | - | ✅ landed | `749e5df0d` |
| LP5 | Focused unit coverage | 180 | LP1-LP4 | ✅ landed (unit only) | `0216557f4` |

All production logic points remain below 300 LOC. Tests are isolated by public seams: picker aggregation, telemetry model recording, configuration parsing, and overlay outcome recording.

## 实际落地位置（对照表）

| LP | File:line | 关键代码 |
|---|---|---|
| LP1 | `admin/routing.go:2689` | `recentlyUsedModelsKey(tenantID) → llmgw:routing:recently_used_models:<tenant_id>` |
| LP1 | `admin/routing.go:2814` | `popularModelsHotSQL` 第二参数 tenantID |
| LP2 | `admin/routing_popular_models_test.go` | tenant A / tenant B 隔离 + cache 失效 |
| LP3 | `admin/popular_models_config.go:14` | `popularModelsLookupHoursEnv = "LLM_GATEWAY_DB_POPULAR_MODELS_LOOKUP_HOURS"` |
| LP3 | `admin/routing.go:2810` | `if len(popular) >= limit { return popular[:limit] }` |
| LP4 | `metrics/live_stream_overlay_metrics.go:34` | `llmgw_live_stream_tile_overlay_db_lookup_total{outcome}` |
| LP4 | `admin/live_stream_sse.go:2465` | `met.RecordLiveStreamTileOverlayDBLookup(st)` |
| LP5 | `admin/routing_popular_models_test.go` | 77 行 + 7 case |

## 未落地部分

- **P3 真实 PG 集成测试**（testcontainer）：仅 154 harness 实测间接覆盖。Follow-up（rule 17 §5 skip when not set）：CI gate 加 `LLM_GATEWAY_PG_URL` 集成测试，按需启用。

