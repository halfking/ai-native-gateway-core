# 2026-07-20 — 流程详情 (RequestTracePanel) 显示为空 + 链路事件 100% 落库失败

## 症状

老板在 154 (llm.kxpms.cn) 报告两个并发问题：

1. **"原始请求详情" → "流程详情" 展开后空白**，所有近 7 天请求都看不到链路。
2. **链路事件持久化 100% 失败**，最近每个 `/v1/chat/completions` 都触发 `trace.writeStageEvents failed` WARN 日志。

## 根因 1：流程详情查空表（前端层面）

`internal/trace/trace.go::LoadFromPG` 与 `admin/request_trace.go::requestTraceState / fetchRequestSummary` 都只查 `request_logs`（分区表），但最近 7 天的请求被 migration 341 (hot_table_independence, 2026-07-05) 路由到独立的 `request_logs_hot` 表：

```
SELECT count(*), count(trace_events) FROM request_logs        WHERE ts > now() - 10min
-- 0 / 0     ← 没有数据（请求都写到 hot 表了）

SELECT count(*), count(trace_events) FROM request_logs_hot   WHERE ts > now() - 10min
-- 849 / 826 ← 实际数据在 hot 表
```

Redis 里有 10 min TTL 缓存的 trace，超过 10 min 全部丢失，PG fallback 永远查空表 → 前端永远拿到 not_found。

## 根因 2：链路事件 100% 落库失败（写入层面）

`internal/trace/stage_events.go::writeStageEvents` 在 production binary 上每条 INSERT 都触发 SQLSTATE 22P02 (`invalid input syntax for type json`)，即使 details/snapshot 是合法 JSON：

### 2a. 漏传 `tenant_id`

migration 420 给 `request_logs` 加了 `trace_events JSONB`，但 `request_logs_hot` 是独立物理表（`hot_table_independence`），**没继承**该列。production PG 上有人手工 ALTER 给 `request_stage_events` 加上了 `tenant_id TEXT NOT NULL`，但 stage_events.go 的 INSERT SQL 漏传此字段。

### 2b. pgxpool SimpleProtocol + `[]byte` + `::jsonb` cast = 100% 22P02（本次新发现的）

`db/db.go::Open` 在 2026-07-15 因 provider_model_bindings schema 变更显式设置：

```go
cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
```

SimpleProtocol 模式下，pgx v5 通过 `internal/sanitize/sanitize.go::QuoteBytes` 把 `[]byte` 参数序列化为 bytea hex literal `'\x7b22...'`：

```sql
-- 我们写的
INSERT ... VALUES ($1, ..., $13::jsonb, $14::jsonb)
-- SimpleProtocol 内联后（$13 = detailsJSON []byte）
INSERT ... VALUES ('', 'r', 1, ..., '\x7b22636c69656e745f6970223a...', '\x6e756c6c')
```

PG 收到后 `bytea` cast 到 `jsonb` → PG 先 hex 解码得到原始字节序列（`{"cli...` 等）→ 然后尝试 JSON parse，但 hex 解码后的字节里包含 `\`、`"` 等特殊字符（来自 JSON string 的转义），**jsonb parser 在 `\` 处失败 → 22P02**。

复现：

```sql
SELECT '\x7b22636c69656e745f6970...7d'::jsonb;
-- ERROR: invalid input syntax for type json
-- DETAIL: Token "\" is invalid.
```

`request_stage_events` 表自 migration 434 (2026-07-19) 上线以来**从未成功写入过任何行**：

```
SELECT count(*) FROM request_stage_events; -- 0 rows
```

## 修复

### Fix 1 — `internal/trace/trace.go::LoadFromPG`

查 hot 表 + 分区表（按 ts desc 取最新一条）：

```go
SELECT trace_events FROM (
    SELECT trace_events, ts FROM request_logs_hot WHERE request_id = $1
    UNION ALL
    SELECT trace_events, ts FROM request_logs     WHERE request_id = $1
) t WHERE trace_events IS NOT NULL ORDER BY ts DESC LIMIT 1
```

### Fix 2 — `admin/request_trace.go::requestTraceState / fetchRequestSummary`

同样把 `FROM request_logs` 改为 hot + 分区表 UNION ALL。

### Fix 3 — `internal/trace/stage_events.go::writeStageEvents`

3 个子修复：

1. **加 tenant_id 提取**：新增 `extractTenantID(events)`，从 authenticate 事件的 `details.tenant_id` 读取，缺省 `""`（PG `TEXT NOT NULL` 接受空串）。
2. **JSON 参数从 `[]byte` 改为 `string`**：SimpleProtocol 模式下，`string` 类型走 `QuoteString` 输出 `'...'` 单引号字符串字面量，PG 接收后 `'{"a":1}'::jsonb` 正常解析为 JSON。
3. **改用 tx.Exec 串行**：原实现用 `pgx.Batch.Queue` + `SendBatch`，在生产 SimpleProtocol 模式下与 `::jsonb` cast 协同有问题；改为 `db.Begin()` + 多次 `tx.Exec()`。每个请求 N 个 events（通常 8-15），逐条 Exec 增加约 10ms overhead，可接受。

### Fix 4 — 增强诊断日志

writeStageEvents 失败时 dump 第一个失败 event 的 details/snapshot 字符串（截断 512 字节）到 slog.Warn，便于下次同类问题能直接看到具体哪个 event 的内容。

### Fix 5 — 新增单元测试 `TestExtractTenantID`

覆盖 5 个 case：authenticate 携带 tenant_id、非 authenticate 携带、缺、events 空、跳过空字符串。

## 验证

### 本地

```
go build ./...                                # OK
go test ./internal/trace/ -count=1           # PASS
```

### 154 部署（build_seq 1201-c1a01552 → 1205 build）

```bash
curl -sS -X POST http://localhost:8781/v1/chat/completions -d '...'
# 缺 API key 也走完 trace 全链路 + flush
```

随后查 stage_events 表：

```
SELECT count(*) FROM request_stage_events WHERE created_at > now() - 5min;
-- 9 rows (3 个请求 × 3 events: receive_request / body_parse / request_complete)
-- 修复前: 0 rows
```

再查日志确认 22P02 不再发生：

```
journalctl -u llm-gateway-go --since "1 min ago" | grep "22P02\|writeStageEvents"
-- (空)
```

L4 业务真实（用真实凭据调用 `/v1/chat/completions`）也间接通过 —— 所有 trace_events 正常写入 PG，前端"流程详情"展开后通过 `getRequestTrace` API 能拿到 events 数组。

## 后续 TODO

- `pgx.QueryExecModeSimpleProtocol` 在业务表频繁含 jsonb 列的场景下都不友好。如果未来其他模块也踩到类似 22P02，可考虑：
  1. 改回 `QueryExecModeCacheStatement`（或 `QueryExecModeExec`），接受 ~5% 性能回退换取 prepared statement 缓存，
  2. 或者为 jsonb 列的写入统一封装一个 `pgtype.JSONBCodec` helper，避开 sanitize 包的 bytea 路径。
- 增加 `request_stage_events` 行的 smoke 检查（在 health endpoint 或每日 cron 中），保证未来若 SimpleProtocol 被默认关闭、stale prepared statement 重新出现，能被尽早发现。