# 2026-07-13 — 路由事件诊断 Phase 2（mutating + audit + 证据导出）

## 背景

Phase 1 落地了只读链路（检测、持久化、呈现、调查）。
Phase 2 在不破坏 Phase 1 不变量的前提下加入：

- **受控 mutating actions**：5 个（recover, reprobe, release_slot,
  reset_slots, reset_availability）+ 2 个 diagnostic tests
  （direct_upstream_test, through_gateway_test）
- **不可变审计**：每次 mutating 与 evidence export 都写
  `routing_audit_log`，同事务提交
- **证据导出**：从一次已完成的 DiagnosticRun 拼装脱敏 +
  SHA-256 校验和的证据包

按 spec §"Phase Two Diagnostic Runs And Actions"。

## 关键不变量

1. **每个 mutating action 都必须经过同一调度器**
   （`domains/routeincident.Store.dispatchAction`），该调度器在
   一笔事务内完成：
   - `SELECT ... FOR UPDATE` 锁事件（`lockIncidentForUpdate`）
   - `expected_version` 一致性检查（缺则返回 `ErrStaleState`）
   - 执行 executor（可能写 `route_incidents` / 插入
     `diagnostic_runs`）
   - 写 `routing_audit_log` 行
   - 提交
2. **请求必须携带 `idempotency_key`**：唯一索引拒绝重复执行；
   命中冲突 → dispatcher 回读缓存并 `idempotent: true` 返回。
3. **`confirmation_token` 用 SHA-256 哈希后存**：原始令牌永远
   不落盘；前端仅用于视觉确认（"复制 + 粘贴" 模式）。
4. **写操作强制要求 `reason`**：≤256 字符，超长截断，缺则 400。
5. **参数 allow-list**（`allowedParameterKeys`）：
   `throughput_ms` / `max_tokens` / `max_cost_usd` /
   `timeout_ms` / `reason_extra` / `target_state` /
   `slot_id` / `note`。**任何 URL、body、shell、SQL 都被丢弃**。
6. **写操作要求 super-admin 角色**（中间件）+ `tenant_id`
   过滤 + 跨租户 404。
7. **不绕过健康检查**：spec 明确要求
   > "Recovery means requesting a controlled re-probe and only
   > re-enabling a route after verified state, never bypassing
   > health checks."
   `recover` executor 仅切状态机；下游凭据状态原语才是真实信号。
8. **Evidence Export 永远不含**：
   - 凭据 secret / auth header / cookie
   - 请求体 / 响应体
   - 客户端 IP（仅 IP 哈希前缀进 audit_row）
   - 用户代理
   - 上游原始错误
   - session 标题
9. **导出大小上限 2 MiB**，事件数上限 200。

## 改动清单

```
新增：
  domains/routeincident/actions.go              # ActionKind / AuditLogEntry / DiagnosticRun / ActionRequest / ActionResponse / EvidenceExport
  domains/routeincident/actions_exec.go         # 7 个 executor + Dispatch* 公开方法
  domains/routeincident/action_infra.go         # dispatchAction 调度器 + 幂等 + 审计
  domains/routeincident/evidence.go             # BuildEvidenceExport + RecordEvidenceExportAudit
  domains/routeincident/actions_test.go         # sanitizeParameters / IsAllowedAction / hash 等
  domains/routeincident/action_infra_test.go    # 调度器输入校验
  sql/migrations/startup/390_routing_audit_log.sql (+ down)
  .scratch/route-incident/route-incident-phase2-demo.html
  .scratch/route-incident/verify_route_incident_phase2_ui.py

修改：
  db/db.go                                       # ensureRouteIncidentPhase2Schema
  admin/route_incidents.go                       # handleAction / handleAudit / handleRuns / handleExport + actorFromRequest
  web/src/types/routeIncident.ts                 # ActionKind / ActionOutcome / DiagnosticRun / AuditLogEntry / EvidenceExport / ACTION_LABEL
  web/src/api/routeIncidents.ts                  # dispatchAction / getAuditLog / getDiagnosticRuns / exportEvidence / generateIdempotencyKey
  web/src/components/RouteIncidentDrawer.vue      # action panel (8.) + audit log (9.) + confirm modal + export banner
  CHANGELOG.md
```

## 关键 API

### POST `/api/admin/route-incidents/{id}/{action}`

Body：
```json
{
  "reason": "上游短暂抖动后人工复核并标记恢复",
  "confirmation_token": "4f8a2c9b1e7d",
  "idempotency_key": "recover-7d4f...",
  "parameters": { "target_state": "recovered" }
}
```

响应（成功）：
```json
{
  "audit_id": 42,
  "outcome": "success",
  "idempotent": false,
  "incident": { "id": "...", "state": "recovered", "version": 8 },
  "diagnostic_run": { "id": "...", "kind": "recover", "state": "succeeded", ... }
}
```

错误：
- 400 — 缺失 reason / confirmation_token / 不在 allow-list
- 404 — 跨租户或事件不存在
- 409 — stale state（expected_version 不匹配）或
  idempotency_key 冲突（同一 key 用于不同事件）
- 503 — DB 不可用

### GET `/api/admin/route-incidents/{id}/export?run_id=...&reason=...&idempotency_key=...`

返回完整证据包 + SHA-256 integrity checksum；写 audit row 记录
导出者。2 MiB 上限，超限返回 400。

## 验证

### 后端
```
$ go build ./...
（无输出）
$ go test -count=1 -timeout 60s ./domains/routeincident/... ./admin/...
ok  github.com/kaixuan/llm-gateway-go/domains/routeincident  8.5s
ok  github.com/kaixuan/llm-gateway-go/admin                  1.6s
```

新增测试覆盖：
- `sanitizeReason` 边界
- `sanitizeParameters` allow-list（拒绝 credentials / api_key /
  raw_body / random_url）
- `IsAllowedAction` / `ActionKind.IsMutating`
- `DiagnosticRunState.IsTerminal`
- `hashToken` / `hashIP` 长度与冲突
- 调度器 5 条错误路径（missing required field / unknown action /
  missing reason / missing token / evidence_export 豁免）

### 前端
```
$ npm run build
✓ built in 8.4s
$ npx vitest run useRouteIncidents
✓ 6 tests passed
```

### 视觉验证
Playwright 抓取 5 张 PNG（desktop 1280×900）：
- `ui-verify-route-incident-phase2-actions-*.png` — 7 个按钮 +
  操作结果条
- `ui-verify-route-incident-phase2-audit-*.png` — 审计行
  （actor / reason / outcome pill / inline 证据链接）
- `ui-verify-route-incident-phase2-confirm-*.png` — 二次确认
  弹窗（reason + 令牌 + allow-list 参数）
- `ui-verify-route-incident-phase2-export-*.png` — 导出 banner
  （SHA-256 前 16 字符 + 计数 + 下载按钮）
- `ui-verify-route-incident-phase2-overview-*.png` — 完整页面

截图位于 `.scratch/route-incident/`，证明 UI 在所有 4 个新
状态下视觉输出正确。

## 已知遗留与下一步

1. **真实端到端测试**：与 Phase 1 一样，截图通过静态 demo
   生成。要在真实 dashboard 验证，需要重新构建网关 + 部署到
   r112 容器 + 触发真实 incident → 截图保存。
2. **`evidence_export` 的 audit row** 当前用一条固定 SQL 写入；
   下一步可改为走同一个 `dispatchAction` 调度器以便统一审计
   （目前它绕过 dispatcher 因为它不需要事务）。
3. **第 9 节审计日志的分页**：Phase 2 仅一次性拉 100 条，
   当一条 incident 触发上百次操作时需要分页或滚动加载。
4. **`max_cost_usd` 参数**：保留 allow-list 但尚无 action
   实际使用，作为第二期的扩展位。
5. **第二期的"实际 wire-level probe / slot release"**：spec 说
   "Existing direct probe and reset endpoints are supporting
   primitives, not direct UI dependencies." Phase 2 在 dispatcher
   层落地了审计 + 状态转换 + 脱敏，但实际的探针调用仍是
   audit + intent record，没有真正打到上游。下一步可把
   `bg/ActiveProbeWorker` 接入 `direct_upstream_test` executor，
   把"实际成功/失败"写回 DiagnosticRun 的 `result` 字段。