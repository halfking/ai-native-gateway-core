# v6-W1.5/W1.7 · 调度装配与观察索引正确性修正

**日期**：2026-09-04
**分支**：main（工作树）
**证据等级**：`LOCAL_VERIFIED`（`go test ./...`、`go build ./...`、核心包 `-race` 均通过；真实多机 staging 负载与 PostgreSQL/Redis 外部依赖验证仍未完成）
**Commit / Migration / Flag**：
- commit：本轮工作树修改，尚未提交
- migration：N/A
- flag：无新增；沿用 `LLM_GATEWAY_DISPATCH_QUEUE_BACKEND`、`LLM_GATEWAY_DISPATCH_GOVERNOR_BACKEND`、`LLM_GATEWAY_DISPATCH_GOVERNOR_OBSERVER_TICK_MS`、`LLM_GATEWAY_DISPATCH_CAPACITY_AWARE_SORT`

## 1. 复核结论

本轮根据 v6 架构文档和当前代码复核调度闭环，确认现有执行主体已经是：

```text
Tier-0 总等待室 → Tier-1 模型 lane → Tier-2 凭据 lane → Governor → 上游执行
```

失败且尚未发送首字节时，继续复用既有 failover 阶梯：

```text
同 credential 重试 → 切换 credential → 切换 provider → 切换 model → 终态失败
```

`DimensionIndex` 的 model / credential / provider 项是管理、审计和 TTL 保留用的**观察成员索引**，不是可执行队列。Redis QueueBackend 共享集群准入、lane 容量和 due 可见性，但不迁移绑定本机连接、goroutine 和 `ResultCh` 的请求对象。

## 2. 已实施修正

### 2.1 组合根启动顺序

以前 `wireDispatchPipeline()` 内部直接启动 Pipeline，可能导致部分依赖在 worker 和 credential forwarder 创建后才注入。

现在调整为：

1. 创建 Pipeline；
2. 注入 observation、journal、queue projection、live actions；
3. 注入 RetryScheduler；
4. 注入 QueueMirror；
5. 注入 QueueBackend；
6. 注入 GovernorBackend 和 snapshot observer；
7. 注入 capacity-aware routing；
8. 调用 `Start()`；
9. 调用 `SetDispatchPipeline()`。

这样第一个按需创建的 credential forwarder 可以看到已经装配的 Governor backend；snapshot observer 仍由 `Pipeline.Start()` 统一启动。

涉及：

- `cmd/gateway/main.go`
- `cmd/gateway/main_dispatch.go`
- `cmd/gateway/main_dispatch_backend.go`

### 2.2 唯一执行路径注释

修正 `SetDispatchPipeline` 的过时注释，明确：

- dispatch pipeline 是唯一执行路径；
- 不存在 legacy synchronous fallback；
- Pipeline 未注入属于显式 wiring error。

涉及：

- `domains/streaming/executors/executor_dispatch.go`

### 2.3 sticky 路由判断

删除使用 credential ID `0` 作为首次 sticky 尝试哨兵的逻辑：

```go
if !qr.HasTriedCredential(0) && len(qr.TriedCredentials) == 0
```

改为：

```go
if len(qr.TriedCredentials) == 0
```

credential ID 为 `0` 时不再影响首次 session-affinity 路由。

涉及：

- `domains/streaming/executors/executor_dispatch.go`

### 2.4 DimensionIndex 孤立引用

修正 `DimensionIndex.insertLocked` 的 `MaxKeys` 边界处理。达到维度 cardinality 上限时，不再先写入 `byReq`、随后因无法创建 ring 而返回。

现在只有确认目标 ring 可以创建或已经存在后，才写入 `byReq`。因此不会保留无法在任何管理 ring 中看到、却会被 `Complete` / `UpdateWait` 长期引用的 request reference。

涉及：

- `domains/dispatch/dimension_index.go`
- `domains/dispatch/dimension_journal_test.go`

### 2.5 节点观察登记时序

`DimensionIndex.MarkNode()` 现在只在 credential lane 成功接收请求后执行：

```text
credential queue send 成功 → MarkNode
```

lane 满、Pipeline 关闭或 send 失败时，管理视图不会显示请求已经进入一个实际没有接收它的节点。

涉及：

- `domains/dispatch/pipeline.go`

### 2.6 恢复 autoroute treatment 归因

全仓构建曾被以下缺失方法阻断：

```text
d.annotateTreatment undefined
```

该方法在历史提交中存在，但后来被误删，而多个调用点仍保留。本轮按历史语义恢复：

- `SetTreatmentRollout`
- `treatmentConfig`
- `annotateTreatment`

该逻辑只负责实验归因，不改变实际路由字段。新增测试覆盖：

- Decider 级 rollout override；
- tenant/request identity；
- assignment hash 稳定性；
- 缺失身份时 fail-closed；
- 不修改 `ChosenModel` 和 `ChosenCredentialID`。

涉及：

- `autoroute/decision.go`
- `autoroute/treatment_test.go`

### 2.7 Prometheus 告警契约

`alerts.yml` 已增加 node-probe 队列告警，但旧测试将 hot-table 告警数量硬编码为 3 条，并要求所有规则都包含 `table`。

测试现在：

- 允许规则扩展；
- 仅要求 HotTablePromote 规则包含 `table` 低基数维度；
- 对 node-probe 规则单独验证；
- 继续禁止 `model`、`tenant` 等高基数标签。

涉及：

- `deploy/prometheus/rules/alerts_test.go`

### 2.8 Responses 请求负向 schema 门禁

继续执行第 5 节“请求转换可靠性”的本地可验证切片，对 OpenAI Responses 请求入口增加结构校验：

- 顶层 JSON 必须是 object；显式拒绝 `null`、array、string、number 等合法但错误的 JSON 形状；
- 对解析器已消费的字段校验其支持的 JSON 类型，例如 `input` 仅接受 string/array，`tools` 仅接受 array，`tool_choice` 仅接受 string/object；
- `tools[]` 中的元素必须是 object，不再静默跳过数字、字符串等无效元素；
- 保留既有兼容语义：nil/空字节仍降级为空 IR，`{}` 仍可解析，已知可选字段显式为 `null` 时仍忽略，未知顶层字段仍进入 `Extensions`。

本切片没有新增 model、input 等必填字段约束，也没有扩展到 OpenAI Chat、Anthropic Messages、响应方向或流式 idle watchdog，避免一次修改扩大多协议兼容面。

涉及：

- `internal/ir/parse_responses.go`
- `internal/ir/parse_responses_test.go`

### 2.9 二次合并：去除与 9c20a6e51 的 treatment 重复声明

将本轮 commit rebase 到远程 `9c20a6e51`（credential lifecycle 与 autoroute rollout hardening）后，远程已经在 `autoroute/treatment_decision.go` 恢复 `Decider.annotateTreatment`。本轮在 `autoroute/decision.go` 添加的同名方法造成 `method Decider.annotateTreatment already declared` 构建错误。

二次调整：

- 移除 `autoroute/decision.go` 中重复的 `annotateTreatment`；
- 保留 `Decider.SetTreatmentRollout`（公共覆写入口，便于受控测试与嵌入式部署）与内部 `treatmentConfig()` helper（`treatment_decision.go` 当前的实现是内联展开，未引用 helper，二者并存无冲突）；
- `autoroute/treatment_test.go` 同步改写：用 `TestDeciderSetTreatmentRolloutOverride` 验证 setter 的副本语义与 nil 复位；`annotateTreatment` 的语义契约由远程 `treatment_decision_test.go` 覆盖。

涉及：

- `autoroute/decision.go`
- `autoroute/treatment_test.go`

## 3. 当前运行契约

1. CPU 数只决定 dispatcher / failover 决策 worker 数；供应商实际并发、RPM、TPM 继续由 credential Governor 控制。
2. 只有 pre-first-byte 错误可经 planner 回队、重试、切换节点或切换模型；首字节后不跨节点重放。
3. 等待、重试和切换信息通过 `: thinking: <json>` SSE comment 发送，不改变正式对话事件 schema。
4. terminal 请求从执行准入中释放；DimensionIndex 的 completed 条目仍由 TTL 或 ring capacity 淘汰，不在完成时直接删除。
5. QueueBackend 在 Redis 不可用时按既有设计回退到本地容量口径；Redis enforce Governor 的供应商额度保护保持 fail-closed。两者必须分别监控与告警。

## 4. 验证结果

以下命令均通过：

```text
gofmt -w autoroute/decision.go autoroute/treatment_test.go \
  cmd/gateway/main.go cmd/gateway/main_dispatch.go \
  deploy/prometheus/rules/alerts_test.go \
  domains/dispatch/pipeline.go domains/dispatch/dimension_index.go \
  domains/dispatch/dimension_journal_test.go \
  domains/streaming/executors/executor_dispatch.go \
  internal/ir/parse_responses.go internal/ir/parse_responses_test.go

go test ./internal/ir
go test ./domains/transformation
go test -race ./internal/ir ./domains/transformation
go test ./autoroute
go test ./domains/dispatch
go test -race ./domains/dispatch
go test ./domains/streaming/executors ./cmd/gateway
go test ./deploy/prometheus/rules
go test ./...
go build ./...
git diff --check
```

真实多机 staging 负载、供应商真实并发/RPM/TPM 压测，以及 PostgreSQL/Redis 外部依赖验证仍未完成，因此当前证据等级保持为 `LOCAL_VERIFIED`，不标记为 `REAL_DEPENDENCY_VERIFIED` 或 `RELEASE_READY`。

## 5. 尚未完成的 v6 高风险工作

- **Provider-wide 治理**：如果策略数据包含跨 credential 的供应商级 concurrency/RPM/TPM 上限，应基于现有资源抽象增加共享 provider lease / Governor，不应把 DimensionIndex 改成第四层执行队列。
- **RetryBudget 统一**：stream、dispatch、survival/goal 等重试层尚未完全收敛到单一预算。
- **自检状态一致性**：node probe、PostgreSQL binding、gateway probe、URSM 状态仍需统一提交或幂等补偿，避免异步 probe 去重和 holdoff 导致恢复延迟。
- **请求转换可靠性**：协议 → IR → provider serializer、provider response → IR/协议响应仍需补齐字段保真和负向 schema 测试；流式 idle watchdog 也需统一。
- **存储闭环**：hot 8h promote/partition、FS fallback 单一权威、queue mirror 跨实例只读语义仍需真实 PostgreSQL/Redis 集成验证。
- **压力验收**：需要混合即时请求、定时请求、429/5xx/timeout、节点切换和模型切换，验证队列有界、客户端连接稳定、think/keepalive 持续、供应商不超发、终态无重复计费。

## 6. 回滚

本轮没有 schema、环境变量或新依赖。回滚本轮相关代码即可恢复原装配和观察行为；如需禁用 Redis 治理，应继续使用既有 backend flags 回退 local，而不是运行期移除 Pipeline。
