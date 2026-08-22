# 总览页「按模型分组的可用节点」优化方案与执行计划

**状态**：Implemented（D1–D4 已确认并实现，2026-08-21）；达 `LOCAL_VERIFIED`（vitest 36/36 + vite build）。待真实后端交互达 `REAL_DEPENDENCY_VERIFIED`。  
**日期**：2026-08-20（2026-08-21 对照代码复审补记；同日开工并完成 P1–P5 本地验证）  
**范围**：llm-gateway-go 前端（`web/src`，Vue 3 + Element Plus + Vite）。后端 API 全部已存在，**本次为纯前端改造，不涉及 DB 迁移 / 部署 / 重启**。  
**目标页面**：`https://llm.kxpms.cn/dashboard` → 「实时流」tab 下的「按模型分组的可用节点」区块，以及点击节点后弹出的「节点详情抽屉」。  
**代码复审结论（2026-08-21）**：§1 数据流表、§2 差距、§3 方案与当前源码一致；D1 的 `onDrop` 完整集映射、R5 的 z-index 已核验并写入本文。

---

## 0. 强制执行原则（与仓库约定一致）

```text
IMPLEMENTED != LOCAL_VERIFIED != REAL_DEPENDENCY_VERIFIED != RELEASE_READY
```

- `IMPLEMENTED`：代码写入，不代表编译/类型检查/单测通过。
- `LOCAL_VERIFIED`：`pnpm build` + `vitest` + 关键组件单测通过。
- `REAL_DEPENDENCY_VERIFIED`：在真实/隔离后端（monitor-summary、sliding-window、fp-slot-stats、candidate-bindings/reorder 等端点可达）下，用真实 credential 验证交互行为。
- `RELEASE_READY`：通过评审、canary/观察窗口、回滚演练后才可标记。

任何状态声明须附命令、日志或可复现证据。本次虽为纯前端，但仍须完成 `LOCAL_VERIFIED` 与 `REAL_DEPENDENCY_VERIFIED` 才能视为上线就绪。

---

## 1. 现状与业务流程分析

### 1.1 页面与组件路由

| 层级 | 文件 | 说明 |
|---|---|---|
| 路由 | `web/src/router.ts:141` `/dashboard` → `HomeView` | 已登录渲染 `DashboardView` |
| 入口 | `web/src/views/HomeView.vue` | 渲染 `DashboardView` |
| 统一入口 | `web/src/views/DashboardView.vue` | 默认租户 → `DashboardViewV2`；租户视图 → `TenantDashboardView` |
| 总览 V2 | `web/src/views/DashboardViewV2.vue` | 4 个 tab：board / stream / stats / selfcheck；托管 `RequestLogDrawer`（请求原始详情抽屉） |
| 实时流 | `web/src/components/LiveRequestStreamV2.vue:489` | 内部渲染 `<QueuePerspectivePanel />` |
| **模型分组节点** | `web/src/components/QueuePerspectivePanel.vue` | 「按模型分组的可用节点」区块 + 队列深度 + 节点详情抽屉入口 |
| 节点详情抽屉 | `web/src/components/NodeDetailDrawer.vue` | 3 tab：明细与近期情况 / 最近路由请求 / 设置与维护 |

**关键事实**：「按模型分组的可用节点」位于 **实时流（stream）tab**，不是 board tab。节点数据来自 SSE 实时流（`liveStreamStore`），模型范围与候选排序来自 REST 轮询。

### 1.2 数据流与数据来源（端点 / 结构 / 来源 全表）

| 用途 | 前端调用 | HTTP 端点 | 返回结构（关键字段） | 数据来源 |
|---|---|---|---|---|
| 实时节点状态 | `nodesRef`（`liveStreamStore.ts:319`） | SSE `EventSource` → `node_update` / `initial_data` 信封 | `LiveNodeStatus{credential_id, provider_id?, provider_code?, circuit_state?, availability_state?, quota_state?, health_status?, manual_disabled, in_flight?, last_latency_ms?, last_error?, fp_disabled?, disable_kind?, raw_models?[]}` | 后端 `admin/live_stream_sse.go`，投影 `credential_model_bindings` + 节点健康 |
| 实时请求流 | `liveStreamState.requests` | SSE `request` / `request_lifecycle` | `LiveRequest{request_id?, model?, status?, latency_ms?, credential_id?}` | SSE |
| 模型范围（特色） | `getFeatured()`（`routing.ts:345`） | `GET /api/routing/featured` | `{featured_models: string[]}` | routing 配置 |
| 模型范围（热门） | `getRequestLogTopModels()`（`logs.ts:326`） | `GET /api/logs/top-models?from&to&limit=50` | `{items: TopRequestModel[]{canonical_name, display_name, request_count}}` | request_logs（近 72h） |
| 候选解析 + 重排版本 | `resolveRouting()`（`routing.ts:133`） | `GET /api/routing/resolve?model=` | `RoutingResolveResponse{candidates: RoutingCandidate[], reorder_revision?}` | routing 引擎 |
| 节点候选重排 | `reorderCandidateBindings()`（`routing.ts:190`） | `PATCH /api/routing/candidate-bindings/reorder` | `{raw_model, expected_revision, items[]}` | routing 写路径（原子） |
| 节点监控摘要（detail） | `getCredentialMonitorSummary({credential_id, mode:'detail'})`（`credential-monitor.ts:101`） | `GET /api/credentials/monitor-summary` | `CredentialMonitorSummary{models: CredentialModelStatus[], concurrency_limit, concurrency_limit_auto, effective_concurrency, ...}` | monitor 聚合 |
| 节点监控摘要（core） | 同上 `mode:'core'` | 同上 | 同上（轻量） | 同上 |
| 滑动窗口 | `getSlidingWindow(credId, model, 60)`（`credential-monitor.ts:136`） | `GET /api/credentials/sliding-window` | `{entries: CallEntry[]{rid, ts, ok, lat, err}, stats: WindowStats{total,success,failed,failure_rate,error_kinds}, source}` | redis / request_logs |
| 模型状态变化历史 | `getModelHistory(credId, model, 30)`（`credential-monitor.ts:250`） | `GET /api/credentials/model-history` | `{events: ModelHistoryEvent[]}` | 探针共识 + 人工操作合并 |
| 最近路由请求 | `getCredentialDecisions(credId, 50, scope?)`（`credential-monitor.ts:291`） | `GET /api/credentials/decisions` | `{decisions: CredentialRoutingDecision[]{request_id, ts, model, success, latency_ms, error_class, ...}}` | routing_decision_log |
| 候选明细（优先级/生命周期/并发） | `resolveRouting()`（同上） | `GET /api/routing/resolve` | `RoutingCandidate{manual_priority?, tier, weight, lifecycle_status, concurrency_limit, effective_concurrency, active_sessions?, ...}` | routing 引擎 |
| 保存路由排序 | `patchCandidateBinding()`（`routing.ts:148`） | `PATCH /api/routing/candidate-binding/{credId}?raw_model=` | `{message}` | routing 写路径 |
| 紧急维护 | `emergencyRepair()`（`routing.ts:230`） | `PATCH /api/routing/emergency-repair` | `{message}` | routing 写路径 |
| 手工禁用/恢复凭据 | `setManualDisabled()`（`credential-monitor.ts:320`） | `POST /api/credentials/set-manual-disabled` | `{success,message}` | monitor 写路径 |
| 模型上下线 | `toggleModelAvailability()`（`credential-monitor.ts:199`） | `POST /api/credentials/model-toggle` | `{success,available}` | monitor 写路径 |
| 会话 Ping | `sessionPingCredential()`（`credential-monitor.ts:128`） | `POST /api/admin/credentials/{id}/session-ping` | `{status, latency_ms, tested_at, error?}` | monitor 写路径 |
| **并发自动设置** | `setConcurrencyAuto()`（`credential-monitor.ts:173`） | `POST /api/credentials/set-concurrency-auto` | `{success,message}` | monitor 写路径 |
| **指纹 slot 统计** | `getCredentialFpSlotStats(providerId, credId)`（`providers.ts:646`） | `GET /api/providers/{pid}/credentials/{cid}/fp-slot-stats` | `FpSlotStats{credential_id, slot_limit?, healthy_slots, occupied_slots, free_slots?, holders?[], details?: FpSlotDetail[]}` | 槽位聚合 |
| **指纹 slot 明细** | `getCredentialSlots(credId)`（`providers.ts:676`） | `GET /api/credentials/{cid}/slots` | `SlotInfoResponse{enabled, fp_slot_limit, total_slots, active_slots, total_inflight, slots: SlotInfoV3[]}` | 槽位明细 |
| **fp slot 上限修改** | `updateCredential(providerId, credId, {fp_slot_limit})`（`providers.ts:295`） | `PATCH /api/providers/{pid}/credentials/{cid}` | `{message}` | provider 写路径 |
| **请求原始详情** | `getRequestLogDetail(requestId)`（`logs.ts:265`） | `GET /api/logs/{request_id}` | `RequestLogDetail extends RequestLogRow {request_body, response_body, attachments[], routing_attempts, ...}` | request_logs（即 `https://llmgo.kxpms.cn/request-logs` 原始详情） |

> 以上 REST 端点均已被 `CredentialMonitorView.vue` / `NodeDetailDrawer.vue` / `RequestLogDrawer.vue` 等使用，服务端 handler 已存在；本次**不新增任何后端端点**，仅前端编排复用。

### 1.3 关键数据结构（精简）

- `LiveNodeStatus`（SSE，实时）：见 1.2。注意 `provider_id`、`raw_models` 为可选——fp slot 端点需要 `provider_id`，缺失时回退到候选 `RoutingCandidate.provider_id`。
- `ModelGroup`（`QueuePerspectivePanel.vue:135`）：`{model, nodes: LiveNodeStatus[], requestCount, featured, hotRequests, aliases[], reorderRawModel?, reorderRevision?}`。`reorderRawModel`/`reorderRevision` 仅在「单一 raw_model 且候选集完整」时存在，是拖拽重排的安全前提。
- `CredentialMonitorSummary` / `CredentialModelStatus`（`credential-monitor.ts:5/37`）：节点级 + (credential,model) 级可用性、并发、P95 等。
- `RoutingCandidate`（`routing.ts:14`）：`manual_priority`、`tier`、`weight`、`lifecycle_status`、`concurrency_limit`、`effective_concurrency`、`active_sessions` 等——设置与维护 tab 的编辑对象。
- `CallEntry` / `WindowStats`（`credential-monitor.ts:84/93`）：滑动窗口单元格，`rid` 即 request_id，是「点击请求看原始详情」的跳转键。
- `FpSlotStats` / `SlotInfoResponse`（`providers.ts:634/666`）：并发指纹槽位统计与逐槽明细。
- `RequestLogDetail`（`logs.ts:123`）：请求原始详情（含 request/response body、routing_attempts、附件）。

### 1.4 现状能力小结

- ✅ 「按模型分组的可用节点」区块已实现（模型分组、节点卡片、状态四态点）。
- ✅ 状态过滤四选框已实现，但**默认全开**（`liveStreamPreferences.ts:62` 种子 = 四个 `true`）。
- ✅ 拖拽重排已实现，但硬性要求 **四个状态全部勾选**（`canReorder` 依赖 `filtersAreAllEnabled`，`QueuePerspectivePanel.vue:369`）。
- ✅ 节点点击 → 打开 `NodeDetailDrawer`（`NodeDetailDrawer.vue`），含 3 tab。
- ⚠️ 三个 tab **手动按钮加载**（detail / requests / settings 各一个「加载…」按钮），非自动。
- ⚠️ 「设置与维护」tab 用单次 `startCorePreload()`（monitor core + candidate）一次性加载，未分段。
- ⚠️ 滑动窗口单元格**不可点击**，无请求详情入口。
- ❌ 并发 / 指纹 slot **未出现在节点详情抽屉**（仅在 `CredentialMonitorView` 独立页存在，API 可复用）。

---

## 2. 需求与现状差距对照

| # | 用户需求 | 现状 | 差距 |
|---|---|---|---|
| R1 | 默认只显示在用的数据，可勾选其它三个状态显示剩余节点 | 四选框存在，默认全开 | 改默认种子为仅「在用」 |
| R2 | 可拖动节点更新优先顺序 | 拖拽已实现，但要求全状态勾选 | 与 R1 默认仅「在用」冲突，需解耦（见 D1） |
| R3 | 点击节点显示详情页，异步自动加载三个 tab，tab 内显示加载动画 | tab 存在，但手动加载、无自动 | 改为打开即并行自动加载 + 每 tab 骨架/转圈 |
| R4 | 优化「设置与维护」tab 加载，多段异步加载（当前太慢） | 单次 core 预载 | 拆分为多段独立加载、逐段渲染 |
| R5 | 滑动窗口点击请求 → 显示该请求原始详情（用 request-logs 原始详情） | 单元格不可点 | 单元格可点 → 打开 `RequestLogDetail` |
| R6 | 显示并发与指纹 slot，可修改保存 | 详情抽屉无此内容 | 在设置与维护 tab 增加并发 + 指纹 slot 展示与编辑（复用既有 API 与 `FpSlotVisualizer`） |

---

## 3. 优化方案

### 3.1 R1：默认只显示「在用」，其余三状态可选（低风险）

- 修改 `web/src/composables/liveStreamPreferences.ts` 的 `defaultLiveStreamPreferences()` 种子：
  ```ts
  statusFilter: { active: true, degraded: false, manualDisabled: false, exhausted: false }
  ```
- 保留用户**显式**选择后的持久化（`writeLiveStreamPreferences` 行为不变）。即：首次进入默认仅「在用」；用户之后勾选其它状态会被持久化并在下次恢复。
- 渲染逻辑（`filteredModelGroups`、`passesStatusFilter`、`nodeStatusBucket`）**无需改动**——已支持任意子集。
- 仅当 `modelScopeLoading||modelScopeError||hasModelGroups` 时才渲染该区块的约束保持不变（零值不冒充）。

### 3.2 R2 + D1：拖动更新优先级，且与「默认仅在用」共存

**设计决策 D1（待审计确认，建议采纳）**：将 `canReorder` 从「必须四个状态全开」**解耦**为「单一 raw_model 候选集完整且拿到 `reorder_revision`」：

- 新判定：`canReorder(group) = isSuperAdmin() && !dragSaving && group.reorderRawModel != null && group.reorderRevision != null`。
- 原理：面板已持有 `modelCandidatesByRawModel`（完整候选列表，见 `QueuePerspectivePanel.vue:153/296`）。显示的是 `filteredModelGroups[].nodes`（过滤后子集，是完整候选列表的子序列）。
- **实现硬约束（代码审计补记，2026-08-21）**：当前 `onDrop`（约 L427–445）直接对 `group.nodes` splice 并提交 `items`。在「四个状态全开」前提下这恰好等于完整集；**一旦解耦过滤，必须改写**，否则会提交不完整集合，触发后端原子重排 409 / 数据错误：
  1. 取完整候选：`full = modelCandidatesByRawModel.get(rawModel)`（credential_id 有序）。
  2. 在**可见子集**上计算 from/to（用户拖的是过滤后的卡片）。
  3. 把可见子集的相对顺序写回完整列表：仅调整可见 credential 在 `full` 中的相对次序，**不可见节点保持原相对位置不变**（典型算法：从 `full` 抽出可见 ids 按新顺序，再按原索引位回填）。
  4. 提交 `items` = 完整 `full` 的 `1..N` 连续 `manual_priority` + `expectedRevision`。
- 效果：即便默认仅显示「在用」节点，只要该模型是单一 raw_model 且后端返回了 `reorder_revision`，即可拖动重排；其他合并模型 / 别名聚合 / 未拿全候选集的组仍禁用（tooltip 说明原因）。
- `dragDisabledHint` 文案同步更新（去掉「请显示全部状态」那条）。
- `onDragStart/Over`、`reorderCandidateBindings`、409 过期刷新保持不变；**仅 `canReorder` + `onDrop` 映射逻辑变更**。

> 备选（不推荐）：保留「必须全开」——但那样 R1 默认仅「在用」会让拖动默认不可用，违背 R2 直观预期。故建议采纳 D1。

### 3.3 R3：节点详情三 tab 异步自动加载 + 每 tab 加载动画

- `NodeDetailDrawer.vue` 当前 `watch`（`visible + activeTab`）已走 `maybeAutoLoad`，但 `ensureTabLoaded` 在 detail tab 被 `maybeAutoLoad` 显式跳过（`NodeDetailDrawer.vue:179`："`tab === 'detail'` 不自动"）。
- 改为：抽屉打开（`watch` 命中 open 且 key 变化）后，**并行**触发三个 tab 的加载：
  ```ts
  void ensureTabLoaded('detail')
  void ensureTabLoaded('requests')
  void ensureTabLoaded('settings')
  ```
  每个 tab 维持各自的 `*Loading` / `*Loaded` 状态；模板中把现有「手动加载按钮」占位替换为**骨架屏 / 转圈 + 文案**（detail 实时状态区可立即显示，统计区骨架）。
- 复用现有的 `AbortController` 序列机制（`sequence` / `resetDrawerData`），关闭抽屉或切换节点时自动取消在途请求，避免竞态。
- 保留「刷新」按钮做手动重拉。

### 3.4 R4：「设置与维护」tab 多段异步加载

将现有 `startCorePreload()`（一次 monitor core + candidate）拆为**独立分段**，每段自带 loading 态，逐段渲染，先到的先显示：

- 段 1（核心状态，快）：`getCredentialMonitorSummary({credential_id, mode:'core'})` → 连通性/紧急维护区立即可用。
- 段 2（候选与排序，中）：`resolveRouting(selectedModel)` → 路由排序与生命周期表单（manual_priority/tier/weight/lifecycle + 并发 `concurrency_limit`/`effective_concurrency`）。
- 段 3（指纹 slot 统计，慢/独立）：`getCredentialFpSlotStats(providerId, credId)` → 指纹 slot 区（见 3.6）。
- 段 4（可选懒加载）：`getCredentialSlots(credId)` → 逐槽明细，仅在用户展开「槽位明细」时加载。
- 实现：用 `Promise.allSettled` 并行段 1/2/3，各自 `try/finally` 更新对应 `*Loading`；失败段不影响其它段，并在该段下方显示局部错误提示 + 重试。

### 3.5 R5：滑动窗口点击请求 → 原始请求详情

- `NodeDetailDrawer.vue` 滑动窗口区（`windowEntries` 渲染，约 `NodeDetailDrawer.vue:613`）单元格当前为纯展示 `<span class="nd-window-cell">`。
- 改造：单元格带 `@click`，传 `entry.rid`；抽屉内置一个 `RequestLogDrawer` 实例，由 `requestId` 驱动显示 `getRequestLogDetail` 的原始详情（与 `https://llmgo.kxpms.cn/request-logs` 同一套原始详情 API）。
- **z-index 审计补记（2026-08-21）**：~~`nd-drawer` = 3001；`RequestLogDrawer` 已有 `z-index: 9999 / 10000`，**无需再改 z-index**，仅需在 `NodeDetailDrawer` 内挂载并接线。~~
- **作废（2026-08-22）**：上述「9999 已够」为误判——9999/10000 仅属附件 lightbox；抽屉根 `.drawer-backdrop` 全局为 `z-index: 100`，嵌套时会被 `nd-drawer`(3001) 盖住。正确方案见 [`2026-08-22-dashboard-node-card-window-dnd-overlay-plan.md`](./2026-08-22-dashboard-node-card-window-dnd-overlay-plan.md)（N3：`stackLevel=nested` → z-index 3200）。
- 不另起事件链：`NodeDetailDrawer` 自己托管子 `RequestLogDrawer`，自包含，避免 `QueuePerspectivePanel → LiveRequestStreamV2 → DashboardViewV2` 三级事件透传。
- `rid` 可能为空的样本跳过点击（无详情可展示）。

### 3.6 R6：并发与指纹 slot 展示 + 修改保存

在「设置与维护」tab 新增一段「并发与指纹槽位」：

- **并发**：
  - 展示：`concurrency_limit`（手动）、`concurrency_limit_auto`（自动）、`effective_concurrency`（当前生效）、`RoutingCandidate.effective_concurrency` / `active_sessions`。
  - 编辑：复用 `setConcurrencyAuto(credId, value, reason)`（需 reason，参考 `CredentialMonitorView.vue:682-697` 的对话框模式）。保存后刷新该段。
- **指纹 slot**：
  - 展示：`slot_limit`、`healthy_slots`、`occupied_slots`、`free_slots`、`holders`，并用 `FpSlotVisualizer.vue`（已有组件，输入 `:details` `:slot-limit`）渲染逐槽指纹/inflight；可选叠加 `getCredentialSlots` 的 `SlotInfoV3`（pin/ttl/memory_mode）。
  - 编辑：`fp_slot_limit` 修改复用 `updateCredential(providerId, credId, {fp_slot_limit})`（`CredentialMonitorView` 已有 `FpSlotStats` + 该 PATCH 用法）。保存需 reason（与现有维护原因一致）。
- **provider_id 获取**：优先 `node.provider_id`；缺失时回退 `candidate.provider_id`（来自 `resolveRouting`）。两处皆空则禁用 fp slot 编辑并提示。
- 权限：`canEdit = isSuperAdmin()`（`NodeDetailDrawer.vue:188`），非超管只读——与现有设置面板一致。

### 3.7 关键设计决策汇总（待审计确认）

- **D1**（R2）：拖拽解耦「全状态勾选」，改为仅需单一 raw_model 完整候选集 + `reorder_revision`。推荐采纳。
- **D2**（R1）：默认种子改为仅「在用」，保留用户后续显式选择持久化（非强制每次重置）。
- **D3**（R5）：滑动窗口详情用抽屉内置 `RequestLogDrawer`（z>3001），而非事件透传到 DashboardViewV2。~~「9999 已够」已作废~~ → 见 2026-08-22 N3。
- **D4**（R6）：并发与指纹 slot 放在「设置与维护」tab（而非明细 tab），与「维护」语义一致；复用 `FpSlotVisualizer` 与既有写 API。

---

## 4. 执行方案与计划

### 4.1 改动文件清单

| 文件 | 改动 |
|---|---|
| `web/src/composables/liveStreamPreferences.ts` | D2：默认 `statusFilter` 种子改为仅 `active` |
| `web/src/components/QueuePerspectivePanel.vue` | D1：`canReorder` 解耦全状态勾选；`dragDisabledHint` 文案；默认仅「在用」自动生效（渲染逻辑不变） |
| `web/src/components/NodeDetailDrawer.vue` | R3 自动并行加载三 tab + 骨架；R4 设置 tab 多段异步；R5 滑动窗口点击 + 内置 `RequestLogDrawer`；R6 并发/指纹 slot 段（展示+编辑） |
| `web/src/components/RequestLogDrawer.vue` | R5：~~z-index 已满足（9999>3001），原则上无改动~~ **作废** → 见 2026-08-22 N3（`stackLevel`） |
| `web/src/components/FpSlotVisualizer.vue` | R6：复用，无改动（或按需微调尺寸以适配抽屉宽度） |
| 单测 | `QueuePerspectivePanel.test.ts` 增：默认仅 active、D1 解耦后拖拽可启用；`NodeDetailDrawer.test.ts` 增：自动三 tab 加载、设置 tab 多段、滑动窗口点击触发 request 详情、fp slot 编辑保存 |

### 4.2 阶段划分与建议顺序（相对工期，不锁日历日期）

- **P0 分析 & 方案审计（本步）**：产出本文，评审确认 D1–D4。
- **P1 R1 + R2（默认过滤 + 拖拽解耦）**：改 `liveStreamPreferences.ts` + `QueuePerspectivePanel.vue`。风险低、可独立验证。
- **P2 R3 + R4（详情抽屉自动加载 + 设置多段）**：改 `NodeDetailDrawer.vue` 加载编排。
- **P3 R5（滑动窗口 → 原始详情）**：`NodeDetailDrawer.vue` + `RequestLogDrawer.vue` z-index。
- **P4 R6（并发 + 指纹 slot 展示编辑）**：`NodeDetailDrawer.vue` 新增段，复用既有 API/组件。
- **P5 验证与收尾**：`pnpm build` + `vitest` + 真实后端交互验证（隔离/真实环境，按 R4 生产边界要求）。

每阶段结束达 `LOCAL_VERIFIED`；P5 达 `REAL_DEPENDENCY_VERIFIED` 后方可标记 `RELEASE_READY`。

### 4.3 验证要点（审计用）

- `pnpm --filter llm-gateway-web build` 通过；`vitest run` 全绿。
- 新增单测覆盖：默认仅「在用」分组数、D1 解耦后 `canReorder` 在仅 active + 有 revision 时为真、滑动窗口点击设 `requestId`、fp slot `updateCredential` 调用参数正确。
- 真实后端：打开节点抽屉确认三 tab 并行加载各有动画；设置 tab 分段先出核心、后出 fp slot；拖动仅「在用」节点后 `candidate-bindings/reorder` 提交完整集；滑动窗口单元格点击打开原始 request 详情（与 `https://llmgo.kxpms.cn/request-logs/{id}` 一致）；并发/指纹 slot 修改保存后实时对账。

---

## 5. 审计清单 / 待确认项

- [x] **D1** 拖拽是否解耦「全状态勾选」（已采纳并实现：完整候选集映射）。
- [x] **D2** 默认仅「在用」是否覆盖已保存偏好（已采纳：仅作首次种子，保留显式持久化）。
- [x] **D3** 滑动窗口详情是否用抽屉内置 `RequestLogDrawer`（已采纳）。~~z-index 9999 已够~~ **作废** → 见 [`2026-08-22-dashboard-node-card-window-dnd-overlay-plan.md`](./2026-08-22-dashboard-node-card-window-dnd-overlay-plan.md) N3。
- [x] **D4** 并发/指纹 slot 是否置于「设置与维护」tab（已采纳，抽出 `NodeDetailConcurrencyPanel`）。
- [ ] 是否存在尚不可见的后端约束（如 fp slot 写需要特定 role/tenant）需在真实环境验证。
- [x] 三 tab 自动并行加载是否会对高频点击节点造成请求风暴（沿用 `sequence`/Abort；core/detail monitor 合并防覆盖）。

### 本地验证证据（2026-08-21）

- `pnpm exec vitest run`（liveStreamPreferences + QueuePerspectivePanel + NodeDetailDrawer）→ **36/36 passed**
- `pnpm exec vue-tsc --noEmit` + `pnpm exec vite build` → **通过**
