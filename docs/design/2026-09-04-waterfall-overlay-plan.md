# Waterfall 叠加到请求处理流程图 — 实现 Plan（2026-09-04）

**状态**：plan（未动代码）。事实基线来自 2026-09-04 会话对前端/后端的全面调研，行号以 main@当前 HEAD 为准。

## 0. 目标（来自 2026-08-31/09-02 handoff）

在 `RequestProcessingFlowDiagram.vue` 中接入已存在的 `RequestJourney` attempts/events 与 waterfall 阶段时序，真实展示**重试分支**与**节点切换来源**；观测缺失时保持明确的 degraded 状态，**不臆造节点**。

## 1. 现状事实（调研结论）

### 数据已到位
- 三数据源已作为 props 传入组件：`trace` / `journey` / `waterfall`（`RequestProcessingFlowDiagram.vue:8-12`），由 `FlowTimingPanel.vue:38-50` 用 `Promise.allSettled` 并发加载。
- journey 事件含 per-attempt ref（attempt_no/model/provider/credential）、node_switched/model_switched 的 from→to、retry_reason、`observation_status`（`domains/requestjourney/contract.go:374-404`，client `web/src/api/request-journeys.ts`）。
- 单请求 waterfall t0..t9 + 9 段派生 ms：`GET /api/admin/dispatch/waterfall/request/{id}`（内存 ring 优先、`request_logs_hot` 兜底、带 `source`）；`web/src/utils/waterfallTimeline.ts` 已有 9 段布局 + `synthesized` 降级逻辑可复用。
- 降级语义三路齐备：journey `observation_status`、fetch error → FlowTimingPanel 横幅、waterfallTimeline `synthesized`。

### 关键缺口
1. **attempts 不持久**：`WaterfallAttempt[]`（started_at/first_byte_at/ended_at，真正的 per-attempt 时序）只在内存 ring；DB 兜底查询不返回 attempts。重启/多实例后只剩 journey 事件与 `/api/logs/:id` 的 `routing_attempts` JSONB（shape 不同，已由 `useRequestDetailLoader.ts:121-124` 兜底合成）。
2. **t0..t9 是"请求级最后写入"**：重试会覆盖 t5..t9（`dispatch/queued_request.go` setStage 直接覆盖）。**不能**把 t0..t9 当 per-attempt 分段；per-attempt 时间只能用 `WaterfallAttempt`（易失）或 journey `occurred_at`（durable）。
3. **journal 决策轨迹无前端 client**：`GET /api/admin/dispatch/journal/{tenant}/{request_id}`（`admin/journal_handlers.go`）是"节点切换来源"的权威数据（from/to model/credential、action 计数），但 web/src 无任何调用。
4. **四套阶段词汇无映射表**：trace Stage（internal/trace）↔ JourneyStage（9 段）↔ waterfall 段（waterfallTimeline.ts 9 段）↔ journal NextActionKind。目前只有 i18n locale key 的隐式回退。
5. **现有测试契约禁止臆造**：`RequestProcessingFlowDiagram.test.ts` 用例③④断言 `trace:null` 时 `.flow-node` 不得存在——叠加方案必须同步改这两条断言的语义（"journey/waterfall 可产生节点，但仅限真实观测到的阶段"）。

## 2. 方案：三层叠加（不臆造原则的具体化）

流程图轨道升级为**三层叠加**，每层数据来源显式标注，任何一层缺失显示对应 degraded 标记：

```
[Layer A] trace 主轨道（现有）        ← RequestTrace.events，按 seq 线性
[Layer B] journey 轨道分支（新增）    ← JourneyEvent[]，按 attempt_no 分叉泳道
[Layer C] waterfall 阶段条（新增）    ← t0..t9 对齐到 JourneyStage 时间轴
```

### Layer B：重试分支泳道（核心增量）
- journey events 按 `attempt.attempt_no` 分组，`attempt > 1` 的组渲染为从 retrying 事件分叉的下沉泳道；泳道节点 = 真实 event（stage + occurred_at + attempt ref），**不存在的阶段不画**。
- `node_switched` / `model_switched` 事件在泳道起点渲染切换徽标（from→to model/credential），数据已在 JourneyEvent 上。
- attempts 数据源优先级：journey events（durable）> waterfall.attempts（易失但含 first_byte_at）> `routing_attempts` JSONB 合成（现有兜底链复用）。三源冲突时以 journey 为准并在 UI 标注 `source=journey|waterfall|synthesized`。

### Layer C：waterfall 阶段条
- 复用 `waterfallTimeline.ts` 的 9 段定义与合成逻辑，在轨道下方渲染对齐的阶段条；`synthesized:true` 段显示斜纹 + "部分时间戳缺失"。
- **文档化 t0..t9 覆盖语义**：阶段条标注"末次尝试口径"（t5..t9 会被重试覆盖），per-attempt 时序看 Layer B 泳道——这是调研发现的语义陷阱，必须在 UI 上可解释。

### 阶段词汇映射表（新建 SSOT）
- 新建 `web/src/utils/journeyStageMap.ts`：JourneyStage → waterfall 段 → trace Stage 的显式映射（9+9+12 项），组件与 waterfallTimeline 共用；后端不动。
- locale key 回退逻辑保持，但以映射表为一等公民。

## 3. 分步实施（每步独立可合并）

| 步骤 | 内容 | 改动面 |
|---|---|---|
| W1 | `journeyStageMap.ts` 映射表 + 单测 | 纯前端新增 |
| W2 | Layer B 泳道：attempt 分组渲染 + 切换徽标 + retry_reason；改测试③④断言语义（journey 可产生节点，仍禁 waterfall 单独臆造） | 组件 + 测试 |
| W3 | Layer C 阶段条接入（复用 waterfallTimeline）+ "末次尝试口径"标注 | 组件 |
| W4 | attempts 三源优先级合并 + `source` 徽标 | useRequestDetailLoader + 组件 |
| W5（可选，需后端） | journal client（`web/src/api/dispatchJournal.ts`）接入：切换来源徽标点开显示决策轨迹明细 | 前端 client + 组件 |
| W6（可选，需后端） | `serveSessionTurnDetailDB` 补查 t0..t9 列（列已有、写路径已双写），让会话轮次抽屉也能渲染阶段条 | admin SELECT + 响应 meta |

## 4. 明确不做（本 plan 边界）

- 不持久化 `WaterfallAttempt`（改 dispatch/journal 存储是独立工作，量级大；journey 事件已 durable 覆盖主要诉求）。
- 不统一四套阶段词汇到后端 contract（前端映射表先行，后端契约统一随 tree-state/V2 路由契约另议）。
- 不改变 trace 端点行为。

## 5. 验收标准

1. 构造 journey 含 2 个 attempt 的请求：泳道出现分叉，切换徽标显示 from→to；trace 为 null 时仍不臆造 trace 节点。
2. journey/waterfall 均缺失（fetch 失败）：显示现有"观测降级"横幅，轨道退化为仅 trace。
3. t0..t9 缺失的请求：阶段条显示 synthesized 斜纹段，不隐藏"缺失"事实。
4. 既有 4 条组件测试改写后全过；新增泳道/映射表测试。
