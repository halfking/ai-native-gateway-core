# 2026-07-25 — 实时流归一 / 弹窗一致 / node_probe 路由守卫

> 单一 SPEC 涵盖三条产品诉求，目的让运营点击流 + 路由视图在同一窗口内一致。
> 落地路径：与 `docs/superpowers/specs/` 已有的设计文档并列；实施期另起 `plans/<date>-realtime-routing-self-heal.md`。

## 0. 背景

- 实时流泳道（`dashboard?tab=stream`）模型维度在混合大小写或前导/内部空白下出现重复泳道。
- 多维筛选弹窗（`LiveStreamFilterDialog`）打开期间受 SSE delta 触发 `availableXxx` 重算，偶发“关闭后下次重开 draft 漂移”。
- 路由视图 `v_routable_credential_models.unavailable_reason` 中 `node_probe_failed` 在供应商恢复后无法即时松开，导致 `requests return "no available nodes"` 与 `/api/admin/diagnostics/routing-blocked` 计数器长期不为零。

这三条链路跨实时流前端 / 仪表盘诊断前端 / NodeProbe 后端，本 SPEC 给出范围、行为契约、回归与回滚。

## 1. 链路 1 · 实时流模型名聚合统一标准模型

### 1.1 行为契约

1. 后端写入 SSE `tile.model` 永远优先使用 `models_canonical.canonical_name`，回退到 `provider_models.standardized_name`，再到 `request_logs.client_model`，最后 `request_logs.outbound_model`（保持当前 `liveStreamDimensionKey("model", req)` 行为）。
2. Redis 队列键 + Lane ID 用 `normalizeModelKey(emptyAs(CanonicalName, Model))`；归一化后大小写、空格、Unicode 空白统一折叠（已实现，仅保留语义）。
3. 前端 `LiveRequestStreamV2.vue` 移除重复归一化，`standardModelName` 收敛为 `(model || '').trim()` 并加注释“this is a passthrough; backend normalises”。
4. 前端模型筛选下拉的 `availableModels` 复用 `providerShortLabel` 等“显示层”助手，但底层仍以后端 `tile.model` 为唯一键（前端不得引入自维护 alias 表）。

### 1.2 验收

- AC1：连续 30 分钟流量包含 `MiniMax-M3`/`minimax-m3`/`MINIMAX-M3`，`groupBy=model` 下仅出现 **1** 条泳道 `minimax-m3`。
- AC2：泳道聚合不破坏 `provider` 与 `vendor` 维度的现有分桶（按 `provider_code` / `vendor_code` 各自独立）。
- AC3：`LiveRequestStreamV2.vue` 中仅剩 1 处 `trim()` 的归一化；`useSwimLane` 内不新增 alias 逻辑。

### 1.3 测试

- Go：`admin/live_stream_redis_store_test.go` 新增 case，断言 `buildLiveStreamLanes("model", items)` 仅出现 1 条 lane，id 等于 `normalizeModelKey(canonical)`。
- TS：`composables/liveStreamDisplay.test.ts` 在现有规范基础上新增 case：`standardModelName('  minimax-M3  ') === 'minimax-M3'`（trim-only）。
- e2e：Cypress/Playwright 脚本推一连串混合大小写请求，断言泳道数 = 1。

### 1.4 回滚

- 前端：revert `LiveRequestStreamV2.vue` 单文件即可。
- 后端：保留现有 `normalizeModelKey` / `liveStreamDimensionKey`；无需 SQL 回滚。

---

## 2. 链路 2 · 多维筛选弹窗与 SSE delta 解耦

### 2.1 行为契约

1. `LiveStreamFilterDialog` 打开瞬间 `draft = new Set(props.selected)` 并 `search=''`（保持现有行为）。
2. 弹窗打开期间，渲染列表使用本地 `localSnapshot` 快照；SSE delta 更新 `props.options` 时**只更新 `localSnapshot = props.options`**（首次/已存在时拷贝），不重置 `draft` 与 search。
3. 关闭 → 再次打开仍以“当前 props.selected”为准（**不**保留“未 apply 的 draft”）。
4. 提交时 `emit('apply', Array.from(draft))` 与 `emit('update:open', false)`；父组件 `applyXxxFilter` 维持单一签名。

### 2.2 验收

- AC1：弹窗打开期间注入 ≥1 个 SSE delta，`draft` / 已选计数 / 标题保持不变。
- AC2：连续打开/关闭 3 次后，初次 draft 与 关闭前一次 apply 后的 `selected` 完全一致。
- AC3：弹窗不监听 props.selected 的实时变化（避免和已 apply 的回流产生竞态）。

### 2.3 测试

- Vitest：`LiveStreamFilterDialog.test.ts` mount 后：
  1. `open=false → true`，断言 `draft` 同步 `props.selected`；
  2. 触发 `wrapper.setProps({ options: [...新值] })`，断言 draft 不变，但渲染列表使用新值；
  3. `open=false`，draft 不重置；再次 `open=true`，draft 与前次 `apply` 一致。
- Cypress：在请求流 tab 中推一段 delta，断言弹窗选项列表稳态。

### 2.4 回滚

- 单文件 revert，弹窗恢复为原本 `props.options` 直传，无快照。

---

## 3. 链路 3 · `node_probe_failed` 不再黏住已恢复凭据

### 3.1 范围限定

- 仅修改 NodeProbe 后端 + 路由诊断/触发链路：
  - `bg/node_probe.go` 的 `runOne` 成功分支；
  - `bg/node_probe.go` 的 `updateBindingAvailability` 成功路径；
  - `bg/auto_route_realtime_listener.go` 与 `admin/credential_monitor.go::invalidateRoutingCaches` 的协同；
  - `admin/routing.go::handleNodeProbeStateReset` 的“仅清不探”路径保留为紧急按钮。
- 不动：
  - `v_routable_credential_models`（migration 417）**保留** `node_probe_failed` 谓词；
  - URSM v2 / CredentialProbeV2 / ModelProbeWorker。

### 3.2 行为契约

1. **NodeProbeWorker.runOne success 分支**：成功时除“写 `next_retry_at = now + 1h, consecutive_failures=0, paused=FALSE, in_flight_until=NULL`”外，**额外**写：
   - `last_direct_ok = TRUE`、`last_gateway_ok = TRUE`
   - `last_err_code = NULL`、`last_err_detail = NULL`
   - 调用 `provider.InvalidateCandidateCacheForCredential(credID)` 清理前端候选缓存。
2. **立即触发路由视图刷新**：成功后调用 `db.Exec(bgCtx, "SELECT pg_notify('auto_route_refresh', $1)", "credentials:UPDATE:credID")`，复用 `AutoRouteRealtimeListener` 现有 5s 防抖。
3. **失败路径不再写 `cmb.unavailable_recover_at`** = `nps.next_retry_at` 同步：保留现有 migration 416 的初始失败拉黑，但 `invalidateRoutingCaches` 由 NodeProbeWorker 失败分支负责触发一次。
4. **`handleNodeProbeStateReset`（紧急按钮）**：保持“清状态，不重新探测”。
5. **`handleProviderProbeHistoryTrigger` 改造**：触发成功后**复用** `MarkNodeProbeHealthy`（已实现），并不再依赖 `modelProbe.TriggerManual` 的 5 轮共识（5 轮共识继续保留为手动“全面探测”入口）。
6. **`/probe-health/:model` 的“触发探活”**：上传参数 `(provider_id, credential_id)`，触发流程链路：
   ```
   POST /api/admin/providers/:id/probe-history/trigger
     → handleProviderProbeHistoryTrigger (handleNodeProbeStateReset 内联)
     → modelProbe.TriggerManual
     → runOne success → MarkNodeProbeHealthy + pg_notify('auto_route_refresh', ...)
   ```
7. **错误关联澄清**：当 `nps.last_err_code` 来自上游 `http_4xx/http_5xx/timeout/transport` 时，`unavailable_reason` 应继续归因到对应的 probe_* 标签；如果一次 runOne 失败的直接原因是 `endpoint_build + no_rows_in_result_set`（migration 419 引入的 orphan 检测），不写入 `cmb.unavailable_recover_at`，跑 `isMissingBindingErr` 分支直接 DELETE node_probe_state 行。

### 3.3 验收

- AC1：人工制造 `nps.last_direct_ok=FALSE, nps.next_retry_at > now()` 状态后，路由 `/api/routing/resolve?model=...` 返回 `is_routable=false, unavailable_reason='node_probe_failed'`。
- AC2：触发单次 `nodeProbe.Submit` 等到 runOne success（direct + gateway 都 200），**不晚于 5s** 内：
  - `v_routable_credential_models.is_routable` 变为 `true`；
  - `nps.last_direct_ok = TRUE`、`last_err_code = NULL`；
  - `pg_notify('auto_route_refresh', 'credentials:UPDATE:<id>')` 触发一次。
- AC3：`/admin/diagnostics/routing-blocked?provider_id=...` 在一次“上游 5xx → probe 成功”链后，1 分钟内 `block_reason_breakdown['node_probe_failed']` 计数归零（除非其他凭据尚未恢复）。
- AC4：`isMissingBindingErr` 触发后不再出现 “探活永远在 ladder 上失败” 的 backlog；`node_probe_state` 行被 DELETE，不会因为 operator 误改 mapping 后再次被 burn backoff。
- AC5：dashboard `triggerAllProbes` 5 轮 consensus 流程不受影响（节点成功/失败的语义与改造前一致）；`handleNodeProbeStateReset` 紧急按钮仍只清不探。

### 3.4 测试

- Go：
  - `bg/node_probe_test.go` 新增 `TestRunOneSuccessInvokesInvalidateAndNotify`：fake DB 断言 `provider.InvalidateCandidateCacheForCredential` 被调，并发出一次 `pg_notify('auto_route_refresh', 'credentials:UPDATE:<id>')`。
  - `bg/node_probe_test.go` 新增 `TestRunOneSuccessClearsLastDirectOk`：断言 success 分支写入 `last_direct_ok=TRUE` 且 `last_err_code=NULL`（对齐 `MarkNodeProbeHealthy`）。
  - `admin/routing_test.go`（或等价）覆盖 `handleNodeProbeStateReset` 在裸路径上不会重新探测，只清状态。
- DB 集成：
  - 在临时 schema 创建 `v_routable_credential_models` 复刻 + `node_probe_state`，手动制造 failure 行；触发 runOne success；查询视图行 `is_routable` 应为 `true`。
- e2e：Cypress 推流：制造一次 “minimax-m3 5xx → 主动探测” → 等待 6s → 截图 `/api/admin/diagnostics/routing-blocked?provider_id=NVIDIA` 应无 `node_probe_failed`。

### 3.5 回滚

- 若 AC2 触发 5s 不达标：
  - 在 `cmd/gateway/main.go` 临时启用旧路径（保留旧 `updateBindingAvailability` 中 `cmb.unavailable_recover_at` 同步）。
  - NodeProbeWorker 的 success 分支通过环境变量回退（`NODE_PROBE_WRITEBACK_V2=false`）。
- SQL 回滚：无需。`migration 417` 已具备 `isMissingBindingErr` 的硬删除路径；`migration 418` 仍能再次强制 rearm node_probe_state。

---

## 4. 风险与回滚总览

| 风险 | 影响 | 缓解 / 回退 |
|---|---|---|
| NodeProbeWorker success 分支误触发 `pg_notify`，导致 AutoIndexRefresher 每秒 1 次重算 | 路由/索引抖动 | 在 `updateObservedState` / success 分支共用 `go`-gated `pg_notify`，依赖现有 5s 防抖 |
| 弹窗快照在 route incident drawer 打开时仍然更新 `props.selected` | 与“apply 后不再变”原则冲突 | AC2 明确不再保留 draft；incident drawer 不会立刻 emit `apply`，只关弹窗 |
| `standardModelName` trim 后偶发出现空字符串，与 “不可选模型” 冲突 | UI 出现“空 model”行 | `availableModels` 已在计算时 filter `'[空闲]'`；扩展过滤空字符串 |
| 旧 `force_enable` / `reset_errors` 调用方未及时升级 | Dashboard 紧急按钮失效 | `admin/routing.go` 旧分支保留；`handleNodeProbeStateReset` 仍只清不探；验收 AC5 涵盖 |
| `nodeProbe.SetStateProvider` nil | ProbeSync 无法复用 in-flight，避免漏检 | 已存在 nil 兜底；测试覆盖 |

## 5. 不在本 SPEC 范围

- 重新设计 URSM v2 / 重写 CredentialProbeV2 / 重构 node_probe_state 主键。
- 增加新的 NodeProbe 探测模式（multi-region 双 round）。
- 修改 `migrations/417` 的 `is_routable` SQL 公式。

---

## 6. 关联文档

- `live-stream-canonical-name-fallback-2026-07-07.md` — canonical 命名基线
- `2026-07-15-routing-state-probe-capability-unification.md` — URSM v2 起源
- `2026-07-24-phase2.2-implementation-plan.md` — Phase 2 蓝图（与之配套）
- `CHANGELOG.md` — node_probe backoff 6h / 路由排除 / reconciler 修复 的历史记录
