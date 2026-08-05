# 2026-08-06 — GT 分支会话前缀 + 即时总结增量滚动/分段 (GS 分支)

## 变更摘要

在 `2026-08-05 auto-title 父子关联 + 失败日志` 基础上进一步：
- **会话命名空间三段式前缀**：`gw_` / `gt_` / `gs_` —— 标题与总结分支会话显式命名，运维 SQL 一眼可辨
- **即时总结自动触发**：request-path 上新增与 auto-title 平行的自动总结分支；增量滚动闸门（每 N turn 重做）+ 超长会话 map-reduce 分段
- **持久化层共享**：把 v2 dispatch 的 summary upsert 提取到 `internal/summarystore`，request-path 与 session.closed 后台共用同一行结构

## 触发原因

| 项 | 现状 (上一轮交付后) | 用户诉求 |
|---|---|---|
| 标题请求 session_id | 自动生成新 `gw_<uuid>`，与用户会话无显式关联 | 用 `gt_` 前缀命名以与用户会话区分 |
| 即时总结 | 仅 on-demand admin 端点 + v2 dispatch 后台 worker | 在 request-path 自动触发，限流 + 防环路 + 库存位 |
| 超长会话 | 单次塞给 LLM，超 12k chars 截断 | map-reduce 分段式输入与输出 |
| summary 持久化 | 仅 `domains/sessionsummary` 私有写入 | request-path 与 dispatch 共用同一行结构 |

## 命名约定（三段式）

| 前缀 | 含义 | 写入 `gw_session_id` | 触发器 |
|---|---|---|---|
| `gw_<uuid>` | 用户主会话（既有） | `gw_xxx` | 任何用户请求 |
| `gt_<原 session_id>` | 标题生成分支 | `gt_gw_xxx` | auto-title loopback |
| `gs_<原 session_id>` | 即时总结分支 | `gs_gw_xxx` | auto-summary loopback |

剥掉前缀即得父 session：
```sql
SELECT
  child.gw_session_id AS branch,
  SUBSTRING(child.gw_session_id FROM 4) AS parent,
  child.parent_request_id,
  child.origin_actor
FROM request_logs_hot child
WHERE child.gw_session_id LIKE 'gt\_%' ESCAPE '\'
   OR child.gw_session_id LIKE 'gs\_%' ESCAPE '\'
ORDER BY child.ts DESC;
```

## 改动清单

| 文件 | 改动 | 行为 |
|------|------|------|
| `domains/streaming/handler.go` | 改 | `sanitizeGwSessionHeader` 接受 `gw_` / `gt_` / `gs_` 三前缀；新增 `autoSummaryGenerator` 接口 + `SetAutoSummaryGenerator`；emitTelemetry 加 `MaybeGenerateSummary` 调用点；新增 `shouldSkipAutoSummaryGeneration` 谓词 |
| `admin/auto_title_generator.go` | 改 | `doCallAutoTitleOnce` 加 `X-Gw-Session-Id: gt:<session_id>` header（GT 命名空间） |
| `admin/auto_summary_generator.go` | 新增 | 完整组件：corpus 拼装 → 增量滚动闸门 → 单次/map-reduce 选择 → HTTP retry & structured log → `summarystore.Upsert` 持久化 |
| `admin/auto_summary_generator_test.go` | 新增 | 7 个测试：splitter 边界 / JSON 解析 / 限流 / worker slots 并发上限 / loopback header / 503 重试 / no-rows 检测 |
| `admin/handler.go` | 改 | 新增 `autoSummaryGen` 字段 + `GetAutoSummaryGenerator` + `NewHandler` 内构造（`summarystore.NewStore(db)`） |
| `admin/logs_summary.go` | 改 | 新增 `loadSessionLogsBySessionID(ctx, sessionID, tenantID, limit)` 给 summary 生成器复用（配置 limit 参数） |
| `internal/summarystore/store.go` | 新增 | `Summary` 类型 + `Upsert` / `LastSummarized` / `CountNewTurns` —— 共享持久化层（pgxpool.Pool 后端） |
| `internal/summarystore/store_test.go` | 新增 | 4 个 nil-safety 测试，确保无 DB 时调用方拿到清晰的 error 而非 panic |
| `cmd/gateway/main.go` | 改 | 紧跟 `SetAutoTitleGenerator` 加 `SetAutoSummaryGenerator` 调用 |
| `domains/streaming/handler_test.go` | 改 | `TestSanitizeGwSessionHeader`（12 子用例）+ `TestShouldSkipAutoSummaryGeneration`（3 子用例） |
| `CHANGELOG.md` | 改 | Unreleased 段加 Added/Fixed 条目 |

## 触发策略

### 增量滚动闸门

`AutoSummaryGenerator.shouldTriggerSummary`：
1. 读 `session_summaries.last_summarized_at`（NULL → "never_summarized" → 触发）
2. 若存在上次总结：算 `COUNT(*) FROM request_logs_hot WHERE gw_session_id = $1 AND success = TRUE AND ts > last_summarized_at`
3. 若新 turn 数 < 3 → 跳过（"only_N_new_turns_since_last_summary"）
4. 否则 → 触发

### map-reduce

语料 ≤ 12k chars → 单次 LLM 调用  
语料 > 12k chars → `splitCorpusIntoChunks` 按 ~3000 chars/chunk 切片（优先 newline 边界，off-by-one 已修复）→ N 个 partial LLM 调用并发（goroutine）→ reduce 合并为最终 summary

### 资源保护

| 机制 | 实现 | 默认值 |
|---|---|---|
| 每租户限流 | `golang.org/x/time/rate.Limiter`，`rate.Every(time.Minute/N)` | 6/min/tenant |
| 全局并发上限 | `chan struct{}` 信号量 | 4 slots |
| 链式自触发 | `X-Gw-Is-Auto: true` → `logCtx.IsAutoRequest` → `shouldSkipAutoSummaryGeneration` | 必启用 |

任一保护触发 → slog.Info 记录原因并跳过（不阻塞主请求路径）。

## 持久化共享

`internal/summarystore` 包封装三个 DB 操作（pgxpool.Pool 后端）：
- `Upsert(ctx, Summary)` —— INSERT ... ON CONFLICT (session_key) DO UPDATE，`summary_version` 单调递增（`COALESCE(...)+1`）
- `LastSummarized(ctx, sessionKey)` —— 用于滚动闸门
- `CountNewTurns(ctx, sessionKey, since)` —— 用于滚动闸门

所有方法 nil-safe：无 pool 时返回明确 error，不 panic。

## 验证

- `go build ./...` ✅ / `go vet ./...` ✅
- `go test ./admin/ ./domains/streaming/ ./internal/summarystore/` 全绿
- 新增单测：11 个（4 个 summarystore nil-safety + 7 个 summary-generator 单元）
- 全仓 `go test ./...`：除已知的 DB 集成测试（与本改动无关）外全绿

## 遗留与风险

- **rolling 闸门常量 3 hard-coded**：后续可下沉为 `session_analytics` settings 的可调参数（与 v2 dispatch 共用）
- **worker slot 默认 4**：在 session.closed 高并发窗口下可能成为瓶颈；后续接入 metrics 后按 P95 调优
- **summary JSON 容错**：`parseSummaryJSON` 已支持 prose 包裹，但模型返回 markdown fence 时仍可能解析失败 —— 下一轮可加 markdown fence 剥离
- **summarystore 写入未触发 conflict 时不返回错误**：保持 v2 dispatch 行为一致；如需 conflict metrics，下一轮加 `RETURNING summary_version`

## 部署后实地验证 SQL

```sql
-- 1. 父→子分支链路
SELECT
  child.gw_session_id  AS branch,
  SUBSTRING(child.gw_session_id FROM 4) AS parent,
  child.parent_request_id,
  child.origin_actor,
  child.success, child.error_kind, child.ts
FROM request_logs_hot child
WHERE child.gw_session_id ~ '^(gt|gs)_gw_'
  AND child.ts > now() - interval '1 hour'
ORDER BY child.ts DESC
LIMIT 50;

-- 2. 验收：origin_actor 分类
SELECT origin_actor, COUNT(*), AVG(latency_ms)
FROM request_logs_hot
WHERE gw_session_id ~ '^(gt|gs)_gw_'
  AND ts > now() - interval '1 hour'
GROUP BY 1;

-- 3. 验收：summary 表被填充
SELECT session_key, summary_version, title, last_summarized_at
FROM session_summaries
WHERE session_key LIKE 'gw_%'
  AND last_summarized_at > now() - interval '1 hour'
ORDER BY last_summarized_at DESC
LIMIT 10;

-- 4. 验收：失败率
SELECT
  DATE_TRUNC('hour', ts) AS hr,
  origin_actor,
  COUNT(*) FILTER (WHERE success) AS ok,
  COUNT(*) FILTER (WHERE NOT success) AS fail,
  ROUND(100.0 * COUNT(*) FILTER (WHERE NOT success) / COUNT(*), 2) AS fail_pct
FROM request_logs_hot
WHERE origin_actor IN ('auto-title-generator','auto-summary-generator')
  AND ts > now() - interval '24 hours'
GROUP BY 1, 2
ORDER BY 1 DESC, 2;
```