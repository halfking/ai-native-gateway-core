# 2026-08-26 follow-up: rate-limit 黑洞修复 + 静态 key 强化

## 续集（黑洞修复）

d24dab5e7 修复了 RPM 排队耗尽预算导致的 kimi-k3 "总是失败"，但留下一个观测黑洞：

**症状**：rate-limit 早 return 路径（`handler.go:2759` captureAndEmitRateLimited → return）从未经过 `recordInitialRequestLog`（handler.go:3572 正常 INSERT 入口），后续 `EmitRateLimited`（`request_log_pipeline.go:1167`）走 UPDATE 而非 INSERT，命中 0 行 → `request_logs_hot` 没有该失败记录，但 `request_wal_hot` 和 Redis trace 都记了。

**日志证据**：`trace.FlushToPG: request log row not found, retaining Redis trace`

## 根因（subagent 调研结论）

- `request_wal_hot` 在 `handler.go:1734-1747` 早期 WAL 路径覆盖所有入口（包括 rate-limit 早 return）→ WAL **不黑**
- `request_logs_hot` 的 INSERT 仅由 `recordInitialRequestLog`（`handler.go:6492`）触发 → rate-limit 早 return **黑洞**
- safety net defer（`handler.go:1749-1866`）兜底走 `EmitFailure` / `EmitRequestLogUpdate`，**也不补 INSERT** → 黑洞

## 修复（commit TBD）

`domains/streaming/handler.go` 新增 `insertRateLimitedPlaceholder` helper，在 `captureAndEmitRateLimited` 调用 `EmitRateLimited` 之前补一次最小 INSERT：

```go
// 2026-08-26 follow-up: 黑洞修复
h.insertRateLimitedPlaceholder(logCtx)
logCtx.EmitRateLimited(errCode, errMsg, providerID, credentialID)
logCtx.MarkLogged()
```

`insertRateLimitedPlaceholder` 行为：
- `logCtx.IsLogged()` 为真 → 跳过（race safety：成功路径已 INSERT 过）
- `h.telemetryClient` 未启用 → 跳过（无 DB）
- 否则构造最小 `RequestLogEntry{RequestID, TenantID, ClientModel, RequestStatus=in_progress, [APIKeyID, ApplicationID from keyInfo]}`，调 `EmitRequestLogInsert`

幂等性：`request_logs_hot` 主键为 `request_id`（migration 461 后），INSERT 走 `ON CONFLICT DO UPDATE`，与后续 `EmitRateLimited` 的 UPDATE 同一行不冲突。

## 行为变更

| 之前 | 之后 |
|---|---|
| `request_logs_hot` 无 rate-limit 失败行 | 有 rate_limited status 行（之前黑洞回填） |
| dashboard `rate_limited` 行数 = 实际成功路径拒绝 | 加上早 return 的拒绝（更准确） |
| 245 事故日志黑洞：`trace.FlushToPG: request log row not found` 大量 | 修复后 0 |

## 验证

- 单测：`TestInsertRateLimitedPlaceholder_SkipsWhenLoggedOrDisabled` 覆盖 nil/disabled/already-logged 三条 guard
- 已有 `TestRequestLogContext_RateLimitedStatus` 验证 EmitRateLimited 路径仍产出 `request_status=rate_limited`
- 全测试套件：`go test ./ratelimit/ ./middleware/ ./domains/authentication/ ./domains/streaming/...` 全绿
- 端到端（154 seq 1762 已含 d24dab5e）：构造超 RPM 限 → 期望 `request_logs_hot` 出现 `request_status=rate_limited` 行
- 测试稳定化：`TestCheckGatewayRateLimit_QueuedBeyondBudgetFailsFast` 重写 warm-up（绕过 checkGatewayRateLimit 预算逻辑，直接 AdmitRPM 占桶）100 次连跑全绿

## 遗留（与本次任务无关）

- 245 仍在 seq 1760（不含 d24dab5e）—— 需手工部署
- `recordInitialRequestLog` 14 参签名仍是历史包袱；本次仅补 INSERT，不重构
- safety net defer 同样不能补 INSERT；本次不动