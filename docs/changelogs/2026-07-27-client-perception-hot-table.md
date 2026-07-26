# 2026-07-27 — request_logs_hot 客户端感知字段写入修复

## 背景

老板发现生产 `request_logs_hot.agent_name` 统计为 0% — 客户端类型 (智能体) 统计完全无法使用。

经过探查 (ssh 到 252 直连 PG),确认根因:
- `request_logs_hot` 主表 `agent_name`, `agent_type`, `client_protocol`, `virtual_client_id`, `request_body` 5 个字段填充率均为 0%
- 但 `request_context_attrs` 侧表填充率 95.4% — 兜底逻辑正常工作
- 根因: `client.go` 的 INSERT 到 `request_logs_hot` SQL **从未列出** 这 4 个字段 (line 722-808),即使 `fillAttemptMeta` 已经填充了 meta

## 实施改动 (3 文件)

### `domains/hooks/observability/telemetry/client.go`

1. **`RequestLogEntry` 结构** — 新增 4 字段: `AgentName`, `AgentType`, `ClientProtocol`, `VirtualClientID` (全部 `*string`)
2. **INSERT 列** — 在 origin_actor 后追加 `agent_name, agent_type, client_protocol, virtual_client_id`
3. **VALUES 参数** — 新增 `$86, $87, $88, $89`
4. **ON CONFLICT DO UPDATE** — 4 个新字段用 `COALESCE(request_logs_hot.X, EXCLUDED.X)` (first-write-wins,避免 retry 覆盖 origin_mw/fillAttemptMeta 写入的真实值)
5. **参数绑定** — `entry.AgentName`, `entry.AgentType`, `entry.ClientProtocol`, `entry.VirtualClientID` 4 个参数

### `domains/streaming/request_meta.go`

`enrichRequestLogFromMeta` 函数 — 把 `meta` 中的 4 字段透传到 `reqLog` (仅在 `meta.X != ""` 且 `reqLog.X == nil` 时赋值,避免覆盖已有值)

### `domains/streaming/handler.go`

两处 `applyKeyInfoToRequestLog` 之后插入 `enrichRequestLogFromMeta(reqLog, keyInfo, &logCtx.meta)`:
- line 3909 (success path, `emitTelemetry`)
- line 4491 (streaming path, `applyKeyInfoToRequestLog` 在 `applyAutoRouteFields` 之后)

## 不在本次范围

- **`request_body` 写入主表** — 老板明确要求单独任务处理 (size 上限 + 脱敏策略)。当前 INSERT 写 `nil, // request_body` (client.go:944),侧表 `request_logs_bodies_hot` 仍承担持久化
- **新增 dashboard 统计接口** — 待 SQL 修复上线后单独跟进

## 验证结果

| 检查 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./telemetry/... ./domains/streaming/... ./domains/hooks/observability/telemetry/...` | ✅ exit 0 |
| 单元测试 | `go test -vet=off ./telemetry/...` | ✅ 0.211s 全过 |
| 单元测试 | `go test -vet=off ./domains/streaming/` | ✅ 18.019s 全过 |
| 单元测试 | `go test ./domains/hooks/observability/telemetry/` | ✅ 0.682s 全过 |
| Gofmt | `gofmt -w` | ✅ 无 diff |
| Lint | `golangci-lint run ./telemetry/... ./domains/hooks/observability/telemetry/...` | ✅ 改动文件 0 issue |

## 新增测试覆盖

- `TestEnrichRequestLogFromMeta_AgentFields` — 验证 4 字段从 meta 透传到 reqLog
- `TestEnrichRequestLogFromMeta_EmptyMetaLeavesFieldsNil` — 验证 meta 空时 reqLog 字段保持 nil

## 期望效果 (部署 245 后)

- `request_logs_hot.agent_name` 填充率从 0% → 95%+
- `SELECT agent_name, COUNT(*) FROM request_logs_hot GROUP BY agent_name` 不再返回 0 行
- 老板 dashboard 上的"客户端类型"统计正常工作

## 部署步骤

1. merge PR
2. 部署到 245 staging (验证通过)
3. 部署到 154 生产
4. 在 252 上跑 SQL 验证:
   ```sql
   SELECT 
     COUNT(*) FILTER (WHERE agent_name IS NOT NULL) AS with_agent,
     COUNT(*) AS total,
     ROUND(100.0 * COUNT(*) FILTER (WHERE agent_name IS NOT NULL) / COUNT(*), 1) AS pct
   FROM request_logs_hot
   WHERE ts > NOW() - INTERVAL '1 hour';
   ```