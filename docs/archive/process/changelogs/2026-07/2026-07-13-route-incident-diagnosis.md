# 2026-07-13 — 路由事件诊断 (Phase 1 read-only)

## 背景

仪表盘的实时请求流泳道需要为运营提供"在错误持续发生时一键
诊断、恢复后自动隐藏"的能力（spec
`docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md`）。
本批次实现第一期只读链路：检测、持久化、呈现、调查，不变更
任何 live routing 状态。

## 范围（Phase 1 only）

| 区域       | 状态 |
| ---------- | ---- |
| 后端状态机 | ✅ `domains/routeincident` |
| 持久化     | ✅ `route_incidents` + `route_incident_events` |
| 异步观察器 | ✅ 挂钩 `telemetry.SetOnRequestLogPersisted` |
| 只读 API   | ✅ `/api/admin/route-incidents/*`（super-admin） |
| SSE 增量   | ✅ `incident_update` envelope |
| 泳道入口   | ✅ `SwimLane` 诊断按钮 + `Other` 泳道禁用 |
| 抽屉 UI    | ✅ 7 节只读 + 焦点陷阱 + reduced-motion |
| 移动端     | ✅ 全视口 |
| DiagnosticRun / 立即恢复 / 证据包导出 | ❌ 第二期 |

## 关键不变量

1. **客户端 key 错误、客户端取消、其它非供应商原因导致的失败**
   **不计入诊断触发**。`Observer.qualifiesForIncident` +
   `nonRoutingFailureKinds` 实现：
   - `client_key_invalid` / `client_key_missing` / `client_key_disabled`
   - `client_cancel` / `client_disconnected`
   - `input_validation` / `input_too_large`
   - `unauthorized` / `forbidden`
   - `rate_limited_client` / `context_length_input`
2. 触发阈值 `连续 3 次终态失败`，恢复必须 `连续 5 次终态成功`，
   任一失败打断恢复进度（spec §"Incident Lifecycle"）。
3. 路由对象 = `tenant + endpoint/protocol + canonical/outbound
   model + provider + credential`；partial unique index
   `WHERE state IN ('active', 'recovering')` 保证一个路由只有
   一个 active/recovering 行。
4. 跨租户资源返回统一 `404`（不是 403），通过 store 层
   `tenant_id` 过滤 + admin `superAdmin` middleware。
5. SSE envelope 携带 `RouteKey` 但 **不**携带 `tenant_id`、
   凭据 secret、完整请求体、原始上游错误。
6. 第二期 mutating action 全部下线（不实现）：`direct_upstream_test`、
   `through_gateway_test`、`reprobe`、`release_slot`、`reset_slots`、
   `reset_availability`、`recover`。相应字段在响应中保留为
   `null` / `0`，UI 显示占位文本。

## 改动文件

```
新增：
  domains/routeincident/state.go           # 纯状态机 (DecideState)
  domains/routeincident/types.go           # DTO
  domains/routeincident/redact.go          # 脱敏 helper + allow-list
  domains/routeincident/store.go           # 事务式持久化 + 读路径
  domains/routeincident/observer.go        # 异步观察器
  domains/routeincident/state_test.go
  domains/routeincident/observer_test.go
  admin/route_incidents.go                 # 只读 API
  sql/migrations/startup/389_route_incidents.sql
  sql/migrations/startup/389_route_incidents.down.sql
  web/src/types/routeIncident.ts
  web/src/api/routeIncidents.ts
  web/src/composables/useRouteIncidents.ts
  web/src/composables/useRouteIncidents.test.ts
  web/src/components/RouteIncidentDrawer.vue

修改：
  db/db.go                                  # ensureRouteIncidentSchema
  admin/handler.go                          # SetRouteIncidentsHandler + 注册
  admin/live_stream_sse.go                  # LiveIncidentUpdate envelope + PublishIncidentUpdate
  cmd/gateway/main.go                       # observer wiring + incidentUpdateFromResult
  web/src/components/SwimLane.vue           # 诊断按钮
  web/src/components/LiveRequestStreamV2.vue # 转发 diagnose 事件
  web/src/composables/liveStreamStore.ts    # 解析 incident_update envelope
  CHANGELOG.md                              # Added(route-incident diagnosis, Phase 1)
```

## 验证

### 后端
- `go build ./...` ✅
- `go test -count=1 ./domains/routeincident/... ./admin/...` ✅
  - DecideState 全部状态转移
  - `RedactErrorKind` / `SanitizeEvidence` 边界（UTF-8 / 长度 / 允许列表）
  - Observer 队列溢出 + 非路由失败过滤
  - SSE 端点注册可见

### 前端
- `npm run build` ✅
- `npx vitest run useRouteIncidents` ✅ (6 tests)
  - 注册活跃事件
  - 同 incident 多次更新不重复
  - `visible=false` 移除索引
  - `Other` 泳道禁用
  - 未知 lane 返回空
  - 缺 `affected_lanes` 不索引

### 视觉验证
Playwright 抓取 6 张 PNG（desktop 1280×900 + mobile 375×812），
证明 5 个状态都能正确渲染：

- `ui-verify-route-incident-active-*.png` — 活跃（3 连续失败）
- `ui-verify-route-incident-recovering-*.png` — 恢复中（Recovery 2/5）
- `ui-verify-route-incident-other-disabled-*.png` — 其它泳道禁用 + tooltip
- `ui-verify-route-incident-drawer-insufficient-data-*.png` — 证据不足显式提示
- `ui-verify-route-incident-mobile-drawer-*.png` — 移动端全视口
- `ui-verify-route-incident-overview-*.png` — 完整页面

（截图位于 `.scratch/route-incident/`。）

## 已知遗留

- 截图通过静态 demo HTML 生成，**非**真实运行中的网关。这
  反映了组件本身在所有 5 个状态下的视觉输出，但未连接到
  实际 SSE 流。要在 dashboard 验证，需要重新构建网关 + 部署
  到 r112 — 见 `.scratch/route-incident/01-phase1-readonly.md`。
- `Other` 泳道当前没有显式的视觉降级（按钮仍然渲染但 disabled）；
  第二期可以进一步把它移到 `RequestTile` 之前的位置以减少噪音。
- `domains/routeincident.Store.Timeline24h` 使用 `canonical_id`
  过滤；当前 incident 投影没有 `canonical_id` 字段，所以会
  走 `outbound_model / client_model` 兜底。第二期可以把
  `canonical_id` 加到 `route_incidents` 聚合以提升时间线准确性。

## 下一步（第二期）

按 spec §"Phase Two Diagnostic Runs And Actions"：
- `DiagnosticRun` 服务（不可变 + 受控的 `direct_upstream_test`
  / `through_gateway_test` / `reprobe` / `release_slot` /
  `reset_slots` / `reset_availability` / `recover`）
- `routing_audit_log` 不可变审计行（必须在 action 状态转换
  同一事务里提交）
- 受限的 evidence 导出（allow-list DTO + 完整性 checksum + 短
  时下载链接 + 每次下载都被审计）
- 在 `RouteIncidentDrawer` 第 6 节加上 action 按钮 + 二次
  确认 + 审计行展示
