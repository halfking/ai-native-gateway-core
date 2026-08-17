---
title: admin UI v3 转发体 tab 永久显示 outbound_body
date: 2026-07-27
author: ACC Agent
scope: domains/streaming/handler.go, domains/streaming/request_log_pipeline.go
branch: fix/outbound-body-delta-only
---

# admin UI v3 转发体 tab 永久显示 outbound_body

## 问题

老板报告：trace `d94fd76c5880ea12b229db681bc1b83b`（minimax-m3，prompt 23264 tokens，completion 9 tokens）在 admin UI 上查看请求详情时，"v3 转发体" tab 显示为空。

### 数据库实测

```sql
SELECT request_id, ts, client_model, outbound_msg_count, outbound_token_est,
       outbound_body IS NULL AS ob_null, outbound_body
FROM request_logs_hot
WHERE request_id = 'd94fd76c5880ea12b229db681bc1b83b';
```

```
 request_id                       | ts                          | client_model | outbound_msg_count | outbound_token_est | ob_null | outbound_body
 d94fd76c5880ea12b229db681bc1b83b | 2026-07-27 01:51:44.76699+08 | minimax-m3    | 10                  | 25701               | f       | null
```

注：`ob_null = f`（不是 SQL NULL），但 `outbound_body` 是 JSONB literal `null`。`outbound_msg_count=10` 和 `outbound_token_est=25701` 有数据，说明 session compressor 处理过，但 body 内容没有被持久化。

同样的模式影响 minimax-m3 / minimax-m2.7 / minimax-text-01 / claude-opus-5 / claude-opus-4-8 等所有未触发 v3 会话压缩的请求。

## 根因

`domains/streaming/handler.go:2244` 的代码：

```go
if scResult != nil && scResult.CompressionStrategy != "" {
    logCtx.OutboundBody = scResult.OutboundBody
    logCtx.OutboundMsgHashes = []byte(scResult.MsgHashes)
    logCtx.OutboundStrategy = scResult.CompressionStrategy
    logCtx.OutboundSummaryMarker = scResult.SummaryMarker
    logCtx.OutboundWindowTriggered = scResult.WindowTriggered
    ...
}
```

`OutboundBody` 仅在 `scResult.CompressionStrategy != ""`（即 session compressor 实际修改了 body）时被设置。

而 `domains/streaming/request_log_pipeline.go:744-746` 的 `applySessionCompressorFields`：

```go
entry.OutboundMsgCount = c.OutboundMsgCount
entry.OutboundTokenEst = c.OutboundTokenEst

if c.OutboundStrategy == "" {
    return // remaining fields only when compression actually fired
}
// outbound body columns
if len(c.OutboundBody) > 0 {
    entry.OutboundBody = json.RawMessage(c.OutboundBody)
}
```

当 `OutboundStrategy == ""`（即 `logCtx.OutboundStrategy` 未被设置）时直接 return。即使后续 `logCtx.OutboundBody` 有值（理论上），也被跳过。

**两个条件互相加强**：只有当 session compressor 真正修改 body 时，OutboundBody 才会被持久化。

## 影响

- 受影响范围：所有走 session compressor 但没有触发实际压缩的请求（即"delta-only / fresh-session"路径）
- 表现：admin UI v3 转发体 tab 显示"无响应数据"或空（取决于前端 v-if 逻辑），但同时显示 `outbound_msg_count` / `outbound_token_est` 字段
- 历史数据：outbound_body 字段为 JSONB null，无法重建（原始上游 body 未持久化）

## 修复

### 1. `domains/streaming/handler.go` (line 3120-3134)

在 `emitTelemetry` 调用前增加兜底：

```go
auditBuilder.Success(true).Latency(time.Duration(result.LatencyMs) * time.Millisecond)
// Phase D (2026-06-22): use InboundBody (original client body) for audit
// logging, not RequestBody (which may be protocol-converted for upstream).
//
// 2026-07-27 (bugfix: outbound_body NULL on delta-only / fresh-session
// requests): outbound_body was previously only set when session
// compression fired (handler.go:2244 guards on
// scResult.CompressionStrategy != ""), so admin UI's v3 转发体 tab
// showed an empty body for every request without compression. Use the
// executor's actual upstream body (result.RequestBody) as the fallback
// so request_logs.outbound_body always reflects what was forwarded to
// upstream, regardless of whether compression fired.
if logCtx != nil && len(logCtx.OutboundBody) == 0 && len(result.RequestBody) > 0 {
    logCtx.OutboundBody = result.RequestBody
}
h.emitTelemetry(auditBuilder.Build(), result, endUser, keyInfo, streamCapture, "chat", txResult, result.InboundBody, result.ResponseBody, logCtx)
```

`result.RequestBody` 是 executor 内部完成 sanitize / protocol conversion 后实际发送给上游的 body 字节。

### 2. `domains/streaming/request_log_pipeline.go` (line 741-755)

`applySessionCompressorFields` 不再在 `OutboundStrategy == ""` 时提前 return：

```go
// outbound_msg_count / outbound_token_est are always populated
entry.OutboundMsgCount = c.OutboundMsgCount
entry.OutboundTokenEst = c.OutboundTokenEst

// 2026-07-27 (bugfix: outbound_body NULL on delta-only / fresh-session
// requests): the handler now populates c.OutboundBody from
// executor's result.RequestBody for every request path, not only when
// compression fired. Persist the body unconditionally so the admin UI's
// v3 转发体 tab always reflects what was forwarded upstream.
if len(c.OutboundBody) > 0 {
    entry.OutboundBody = json.RawMessage(c.OutboundBody)
}
if c.OutboundStrategy == "" {
    return // compression_meta fields only when compression actually fired
}
if len(c.OutboundMsgHashes) > 0 {
    entry.OutboundMsgHashes = json.RawMessage(c.OutboundMsgHashes)
}
```

OutboundBody 始终写入；compression_meta 相关字段仍要求真实 strategy 值。

## 回归测试

### `TestApplySessionCompressorFields_OutboundBodyPersistedWithoutCompression`

无压缩路径（d94fd76c 回归场景）：

```go
func TestApplySessionCompressorFields_OutboundBodyPersistedWithoutCompression(t *testing.T) {
    entry := &telemetry.RequestLogEntry{}
    c := &RequestLogContext{}
    c.OutboundStrategy = ""
    c.OutboundBody = []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":"hi"}]}`)
    msgCount := 10
    c.OutboundMsgCount = &msgCount
    c.OutboundTokenEst = outboundIntPtr(25701)

    applySessionCompressorFields(entry, c)

    if entry.OutboundBody == nil {
        t.Fatal("OutboundBody must be populated even when CompressionStrategy is empty")
    }
    // ... 验证 OutboundBody, OutboundMsgCount, OutboundTokenEst 都被持久化
}
```

### `TestApplySessionCompressorFields_CompressionKeepsHashes`

有压缩路径保护（防止新代码破坏现有逻辑）：

```go
func TestApplySessionCompressorFields_CompressionKeepsHashes(t *testing.T) {
    // ... 验证 mechanical_trim 路径下 OutboundBody + OutboundMsgHashes + CompressionStrategy 都正确传递
}
```

## 验证

```
$ go test -count=1 -run "TestApplySessionCompressorFields|TestPreferCapturedBody" ./domains/streaming/
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming	0.681s
```

```
$ go build ./...
(无输出)

$ go vet ./...
(无输出)
```

Pre-commit checks: PASS=4 FAIL=0

## 运维 SQL（调查 + 不需要回填）

新请求会自动修复，不要执行“把 NULL 更新成 NULL”之类的回填操作。历史数据无法可靠重建，因为原始上游 body 未持久化。

调查请求受影响范围的 SQL：

```sql
-- 估算受影响请求数（按模型 + 时间窗口）
SELECT
  client_model,
  COUNT(*) AS total_requests,
  COUNT(*) FILTER (WHERE outbound_body IS NULL OR outbound_body = 'null'::jsonb) AS missing_body_count,
  COUNT(*) FILTER (WHERE outbound_body IS NULL) AS sql_null_count,
  COUNT(*) FILTER (WHERE outbound_body = 'null'::jsonb) AS json_null_count,
  ROUND(100.0 * COUNT(*) FILTER (WHERE outbound_body IS NULL OR outbound_body = 'null'::jsonb) / COUNT(*), 2) AS missing_pct
FROM request_logs_hot
WHERE ts > NOW() - INTERVAL '7 days'
  AND outbound_msg_count IS NOT NULL
GROUP BY client_model
HAVING COUNT(*) FILTER (WHERE outbound_body IS NULL OR outbound_body = 'null'::jsonb) > 0
ORDER BY missing_body_count DESC
LIMIT 20;
```

## 部署

分支 `fix/outbound-body-delta-only`（核心修复 commit `280b1f8f0`）已推送到 origin：

```
$ git log --oneline -3 origin/fix/outbound-body-delta-only
280b1f8f0 fix(streaming): persist outbound_body for non-compression paths
7eaf17014 docs(session-logs): 2026-07-27 credential pressure + LoadFromRedis fixes
323a61368 fix(credential): make Save() synchronous to close async-HSET override race
```

部署到 154 服务器后，新请求的 `request_logs.outbound_body` 会被正确填充。生产 trace 验证流程：

```sql
-- 在生产 PG 上查询新请求
SELECT request_id, ts,
       outbound_body IS NULL AS sql_null,
       outbound_body = 'null'::jsonb AS json_null,
       outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb AS has_body
FROM request_logs_hot
WHERE ts > NOW() - INTERVAL '1 hour'
ORDER BY ts DESC
LIMIT 5;
```

如果 `has_body = true`，修复生效。仅看到 `outbound_body IS NOT NULL` 不足以证明有效，因为 JSONB literal `null` 也满足该条件。

## 后续 TODO

- [ ] merge 到 main（需先解决 main 上的 URSM v1→v2 迁移 in-progress 工作）
- [ ] 部署到 154 生产服务器
- [ ] 在 admin UI 上重新打开 d94fd76c 验证 v3 转发体 tab 现在显示真实 body
