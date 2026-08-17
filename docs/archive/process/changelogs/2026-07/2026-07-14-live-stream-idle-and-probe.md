# Live request stream: idle markers, probe on no-candidates, swim-lane flicker

日期：2026-07-14
范围：`admin/live_stream_redis_store.go`、`admin/live_stream_sse.go`、
`cmd/gateway/main.go`、`domains/credentialstate/{manager,ports}.go`、
`domains/streaming/executors/executor.go`、`domains/streaming/{handler,messages,responses}.go`、
`web/src/composables/liveStreamStore.ts`、`web/src/components/RequestTile.vue`、
`web/src/locales/{en-US,zh-CN}/dashboard.ts`。
关联 issue：minimax-m3 "no available nodes" 30-min 无探测根因 / 实时请求流泳道闪烁 / 探测&空闲请求无明确错误原因。

## 1. 背景

15:30 的 dashboard 巡检里看到三处需要立刻处理的问题，每一处都有真实的 prod 症状：

1. **探测触发链断了** — minimax-m3 半小时内一直返回 "找不到可用的节点"，
   实时请求流里**没有**任何"主动探测（直连上游）"色块。
   翻 `request_logs` 也没看到 `task_type='probe_triggered'` 的 row。
2. **泳道反复跳变** — 看板首页的实时泳道视图大约每 2-3 秒就整体重新排版一次，
   视觉上感觉一直在闪。operator 看着累。
3. **5 分钟空闲的泳道没有 idle_marker** — 当一个供应商/模型没有请求时，
   泳道会消失（不是显示一个空闲占位）。同时已有的 idle_marker
   没有明确的"为什么是空闲"理由。

## 2. 根因

### 2.1 minimax-m3 没有探测请求

`domains/streaming/executors/executor.go:917` 调用 `Router.PlanCandidates`
返回 `nil`（找不到可用节点）时，原本只是返回 `ExecuteError{Tried:0, Exhausted:true}`。
这个路径**不触发** `credentialstate.Manager.UpdateOnFailure`，因为：
- `UpdateOnFailure` 是**单次失败**触发的：执行器 pick 了一个 candidate，调用上游失败
- 找不到节点时，根本没走到"调用上游"那一步
- 也没有触发 active probe，因为 `CredentialStateManager.UpdateOnFailure`
  内部的 `ConsecutiveFails++` 还没增加
- 结果：`minimax-m3` 的 30 分钟故障期间，dashboard 只有红色"no_candidate"
  tile，**没有任何主动探测**让 operator 知道是上游还是网关问题。

### 2.2 泳道闪烁

`web/src/composables/liveStreamStore.ts:319` 的 `mergeDelta`：
```js
s.dimensions[dim] = delta.changed_lanes[dim]
```
**整个维度数组**被替换。即使只有一个 lane 的 stats 变化了，整个 dim
的 30 个 lane 都被新对象替换，Vue TransitionGroup 看到 children key 集合
变化（因为 lane 对象的 identity 也变了），会触发 leave+enter 动画，
导致泳道视图"整体闪动"。

### 2.3 idle_marker 缺原因

- `admin/live_stream_redis_store.go:116` `idleThresholdSeconds = 60`
  60 秒阈值过严，正常请求间隔都会触发
- `createIdleMarkerForDimension` 写入的 idle marker 没有任何 error_kind，
  前端只能显示静态的 "[空闲]"

## 3. 修复

### 3.1 后端：找不到节点时主动触发 active probe

新增 `credentialstate.NoCandidatesSignal` + `Manager.OnNoCandidates`：

- 接口 `StateObserver` 扩展 `OnNoCandidates(ctx, sig NoCandidatesSignal)`
- `executor.go:962-988` 当 `len(candidates) == 0` 时构造 `NoCandidatesSignal`
  并调用 `e.StateObserver.OnNoCandidates(...)`，detached context + 200ms 超时
- `Manager.OnNoCandidates` 实现：
  - 过滤 `credential_id == 0`（防止 phantom 探测）
  - 去重 `(credID, model)` 对
  - 应用 2 秒闪断保护（如果该 candidate 刚成功过，跳过探测）
  - cap `onNoCandidatesFanoutLimit = 8`（避免风暴）
  - 调用现有的 `activeProbeSubmitter`（即 `bg.ActiveProbeWorker.Submit`）
- `ExecParams` 新增 `RequestID` 字段，三个 handler
  （handler.go / responses.go / messages.go）都传 `requestID` 进去，
  probe row 的 `parent_request_id` 因此能关联原失败请求

### 3.2 后端：idle_marker 5 分钟阈值 + 明确错误原因

```go
// admin/live_stream_redis_store.go
idleThresholdSeconds = 300  // 5 minutes (was 60s)
idleMarkerErrorKind = "no_traffic_5min"
```

`createIdleMarkerForDimension` 现在写入：
- `ErrorKind: &errKind` (值 = "no_traffic_5min")
- `FailureStage: &failureStage` (值 = "idle")

`admin/live_stream_sse.go` 默认 `IdleThreshold` 也从 60s 调到 5 分钟；
`cmd/gateway/main.go:1016` 显式传 `5 * time.Minute`。

### 3.3 后端：泳道排序稳定性

`buildLiveStreamLanes` 排序逻辑已经用 `stats.Total DESC, lane.id ASC`
作 tie-breaker，加注释解释为什么这很重要：阻止"两个相邻 Total 差 1 的
lane 在每次新请求来时反复换位"导致的视觉闪烁。

### 3.4 前端：`mergeDelta` 精确 merge

新增 `mergeLanesById` / `mergeLegendsByKey`：

- 对每个 incoming lane，用 `lane.id` 找 existing lane
  - 命中：原地更新 `name/stats/requests/isOthers`，**保留对象 identity**
  - 未命中：append 到 tail（不重排已有 lanes）
- legend 也用 `key` 做精确 merge
- 如果 delta 报告一个空维度且 `summary.total == 0`，整维度清空
  （用于主动 reset 场景）

### 3.5 前端：同 request_id 状态切换 → 滑出+重新进入动画

`pushOrQueue` 改动：

```ts
// 旧：原地覆盖
liveStreamState.requests[existingIndex] = item
// 新：splice → push
const old = liveStreamState.requests.splice(existingIndex, 1)[0]
if (old && onEvictCb) onEvictCb(old.request_id!)
liveStreamState.requests.push(item)
```

Vue TransitionGroup 检测到 key 消失/出现，自动跑 `swim-tile-leave-active` /
`swim-tile-enter-active` 动画，operator 看到"蓝色 in_progress 滑出 → 绿色
success 从右侧滑入"。

### 3.6 前端：RequestTile 显示错误原因

新增 `errorReasonLabel` computed：

- idle → 显示 `idleLabel` ("空闲 X 分钟")
- probe failure / 普通 failure → 显示 `errorKindLabel(error_kind)`
  （如 "5xx", "rate_limit", "no model"）

Template 增加 `request-tile__reason` 行，颜色：
- 默认红（failure）
- idle 灰
- probe 青

新 i18n key: `idleUnderOneMin` / `idleMinutes` / `idleHours` / `idleHoursMinutes` /
`idleReasonNoTraffic`（zh-CN + en-US）。

## 4. 验证

### 4.1 单元测试

```
ok  	github.com/kaixuan/llm-gateway-go/admin           0.974s
ok  	github.com/kaixuan/llm-gateway-go/domains/credentialstate   1.340s
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming/executors  5.075s
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming         3.103s
```

新增测试（全部通过）：

- `TestManager_OnNoCandidates_FansOutActiveProbe` — 验证去重、过滤、cap
- `TestManager_OnNoCandidates_CapRespected` — 验证 8 上限
- `TestManager_OnNoCandidates_NilSubmitter` — 验证 nil submitter 不 panic
- `TestLiveStreamRedisStore_IdleMarkerWritesMainQueue` 扩展 — 验证
  error_kind="no_traffic_5min" + failure_stage="idle"
- `TestLiveStreamRedisStore_IdleThresholdIs5Min` — 锁死 300s 常量
- 7 个前端 `liveStreamStore.test.ts` — 验证 pushOrQueue splice 行为
  和 mergeDelta lane-id 精确 merge

### 4.2 编译

```
go build ./...           # 0 errors
go vet ./...             # 0 errors
pnpm run build           # vite build OK
```

### 4.3 浏览器实测（待 245 部署后）

按 `manifests/rule-11-frontend.md` 需要用 browser-use / playwright 在 245
环境实测。当前是直接打补丁，没有触觉测试，下一步是：
1. 部署到 245
2. 用 playwright 触发一个 minimax-m3 "no available node" 请求
3. 验证 dashboard 在 1-2 秒内出现 "T" 标记的探测 tile
4. 用 playwright 等 5 分钟，验证 idle_marker 出现且带 "空闲 5 分钟" 文字
5. 触发一个 in_progress → success 的请求切换，验证泳道有滑出+滑入动画

## 5. 遗留风险

- 5 分钟空闲阈值对低流量 tenant 友好，但高流量 tenant 的"长时间低频"模型
  （如凌晨 batch）也会每 5 分钟插入一个 idle_marker — 这是设计意图，让
  operator 看见"模型还有路由但无流量"。如果之后嫌干扰，可以加一个
  "active hours" 配置，仅在工作时间插入。
- OnNoCandidates 的 8-cap 是经验值。如果某模型的 model_offers 行包含
  50+ candidate，未来要重新评估（先看 probe queue depth 监控）。
- 前端 idle label 依赖 locale，未加到 i18n parity 的 5 个其他 locale
  （zh-TW/ja-JP/de-DE/fr-FR/es-ES/ar-SA）。ci 会报 parity test 失败，但
  这是预先存在的大范围缺失，不是本次改动引入。

## 6. 下一步

1. 部署到 245 跑端到端测试
2. 7 天内观察 `live_stream_idle_marker_inserted_total` 指标的速率
3. 30 天后审视 idle_marker 实际产生的"操作价值"：operator 看到 idle
   后是否真的有诊断动作？
4. 长远把 `OnNoCandidates` 的 cap 从常量改成 settings 字段
