# 统一自动编排插件与 Goal 会话控制审计（只读）

**日期**：2026-08-19  
**基线**：文档生成时为 `main@8992aa0cf`；执行前必须重新核验 HEAD、工作树与 reflog。  
**范围**：Goal、pipeline、response/stream、tool、plugin-runtime、audit/analysis events、durable、handoff、pending/session。  
**非目标**：本审计不修改代码、不执行生产操作、不宣称真实 PG/Redis/provider/canary 已验证。

---

## 0. TL;DR

仓库已有会话过程控制的关键构件，但它们尚未形成一个跨重启、多实例、跨协议且带最小权限插件边界的统一编排系统。

- **强请求前控制存在**：v1 `ChatHandler` 的 session audit 能真实 block 或返回 approval 202；工具展开、压缩、auto route 也位于上游前。
- **响应后控制受限**：非流响应多数已写给客户端，`ModifiedBody` 不能可靠回写；stream end 更不能撤回已经发送的内容。
- **durable 边界正确但不连续**：commit state、lease/fencing、outbox 和 `resume_safety_blocked` 防重放较完整；worker 不进入 Goal continuation loop。
- **Goal 当前不是请求级连续执行**：它响应后惰性激活，使用同 session synthetic Chat follow-up，次数计数在内存中。
- **plugin-runtime 不是 hook runtime**：它不具备 binding、capability enforcement、data-plane RPC、durable delivery 或足够 sandbox。

因此，发布前必须先实现统一编排 ledger/action、请求级 GoalRun、binding/security gate 和真实依赖测试；不能仅打开 Goal 配置就声称已具备自动持续执行能力。

---

## 1. 证据等级

| 等级 | 含义 | 本文使用规则 |
|---|---|---|
| S1 | 静态源码/测试事实 | 可说明当前代码路径，不可替代运行证据 |
| S2 | mock、本地单元或模拟集成 | 可说明契约意图，不可证明真实依赖行为 |
| S3 | 隔离真实 PG/Redis/provider/plugin | 可证明特定版本和环境的真实交互 |
| S4 | 经批准目标环境、观察窗口和回滚验证 | 才可作为 release gate 证据 |

目前本文发现主要为 S1；除非后续 handoff 写入独立命令与结果，任何 S3/S4 均视为未验证。

---

## 2. 生命周期控制矩阵

| 环节 | 当前接入 | 可做 | 不可保证/禁止 |
|---|---|---|---|
| request 前 | v1 handler、pipeline、session audit | block、approval suspend、工具/模型/压缩改写 | 解析失败时不能假设完整安全审计；身份/tenant/session 不可改写 |
| governance | `Decision` / dispatch gate | continue、block、suspend、terminate | `DecisionMutate` 当前没有可靠 body 消费点 |
| transform/tool | tool registry、meta-tool、compression | 定义展开、受限请求改写 | client tool call 不等于 gateway 执行授权 |
| non-stream response | response interceptor | 后续 telemetry/cache/audit/follow-up | 已写响应不能可靠撤回或修改客户端内容 |
| SSE chunk | intercepting stream writer | 当前 frame rewrite/drop/inject | 历史 frame 不可撤回；block 未必终止上游 |
| stream end | capture/reassembly/interceptor | completion candidate、audit、follow-up | 不可作为内容泄露补救 |
| approval | approval queue/resume | 持久 snapshot、202、批准后 resume | 高风险 timeout 不应 auto-approve |
| durable | Store/worker/commit gate | async 202、lease/fence、重排、PG fallback | content/tool checkpoint 后不可透明 replay |
| handoff | request-side proposal/confirmation | explicit 202、target session confirmation、restore | 不可静默轮转或伪造新 session |

---

## 3. 逐链路事实与缺口

### 3.1 Goal

- `domains/hooks/goal/mode_hook.go` 的 non-stream/stream-end hook 在找不到 Goal session 时以响应 body 字段或关键词激活；它不等同于新会话显式 Goal 申请。
- `buildContinueMessage` 生成 synthetic Chat Completions 请求；Responses/Anthropic/Gemini 的原始协议形状不能保证保留。
- `response_interceptor_helpers.go` 的 depth/context 和 per-session counter 是进程内状态，重启/多实例不可作为全局上限。
- `goal_sessions` 保存 loop、模型切换与 terminal 状态，但不保存 root request、完整 child chain、durable task 关联、lease 或 action outbox。
- `tool_calls` 已不自动 continue；但 completion detector 必须避免把未执行 tool call arguments 的 `status=completed` 当作执行完成证据。
- `ModeLLM`、repeat-detection 开关、终态 session 的短路语义需要由实现和测试重新核验，不能仅靠字段存在认定已正确生效。

### 3.2 Pipeline 与治理

- `domains/pipeline/pipeline.go` 的 stage 执行依赖添加顺序，phase 不自动排序；新增 binding 必须显式排序。
- 并行 stage 共用普通 `Metadata` map；并行插件不能无锁写共享状态。
- `cmd/gateway/main_pipeline.go` 的 v2 wrapper 在 fallback `ServeHTTP` 前运行 pipeline；标称 post-upstream/post-response 的 hook 不是当前 v1 请求真正的响应后控制点。
- governance dispatch gate 能处理 block/suspend/terminate；`mutate` 当前没有完整的 request body 替换、重新解析、再授权和审计路径。

### 3.3 Response 与 stream

- `domains/hooks/response` 的 chain 支持 non-stream、chunk 和 stream-end；后写 follow-up 可能覆盖前一个 follow-up，缺少 action planner。
- `interceptingStreamWriter` 按 SSE frame 处理，已发送内容不可撤回。
- 当前 chunk `ShouldBlock` 需要补上停止上游、协议 error terminal、audit 与不可重放语义，否则可能出现半截 stream。

### 3.4 Tools

- tools hook 依赖 `Metadata["tool_calls"]` 已被填充；常规 v1 body tools 与这一 metadata 通路不是统一事实来源。
- tool execution tracker 主要记录 lifecycle；没有默认的 client tool result -> trusted gateway execution -> resume 生产闭环。
- `tool.execute` 若成为插件能力，需要独立 allowlist、tenant approval、idempotency、资源隔离和审计；当前不可视为现成能力。

### 3.5 Plugin runtime

- plugin runtime 支持 manifest、可选签名、独立进程、health/API proxy；当前 registry 不注册 hook binding。
- manifest capabilities 未强制授权；handshake 没有成为 ready 的启动门禁。
- supervisor 继承完整进程环境且提供数据库连接信息；没有资源、网络、文件系统或用户 sandbox。
- 外部插件不能直接接收完整 `PipelineRequest`，需要版本化 DTO、字段白名单、脱敏与 capability gate。

### 3.6 Audit 与异步事件

- audit batch writer 和 MemoryBus 适合 best-effort 记录，不是可靠 action outbox。
- `analysis_events` 的 PG publisher/worker、claim/retry 可作为编排持久事件基础；事件 payload 必须限制为 ID、reason code、hash 和脱敏摘要。
- Session V2 的 telemetry persisted hook 已是生产持久化 owner；不得新增第二个 pipeline owner 导致重复写入。

### 3.7 Durable、pending 与 handoff

- durable snapshot 在认证/session ownership/tool expansion/body normalize 后冻结，并用 lease/fencing/commit state 约束恢复；此边界应复用。
- durable worker 每 task 一次 attempt，当前不接 Goal interceptor，故不能连续 Goal 自动续跑。
- pending response 是结果轮询/重连投影，状态和 body 应以 PG durable source 为权威；它不是 GoalRun 时间线 API。
- handoff 是 request-side explicit proposal + client confirmation；response-side follow-up 只在原 session 内续跑，跨 session 不能静默替代 confirmation。
- `CheckpointCommitState` 是 write-ahead fail-closed gate：checkpoint 写失败时不得发出语义 frame；不应以普通重试或继续发流补救未知写入结果。
- 前台 terminal settlement 对 `PersistSettlementIntent` 只有短暂重试，而 RecoveryWorker 目前为单次持久化尝试。若结果已生成但 intent 迟迟不能落库，可能出现前台轮询结果不可恢复，或后台 lease 到期后产生可避免的重复执行。intent 已成功落库后的 `FinalizeSettlement` repair 已由 worker drain 覆盖，修复不得重放 upstream。
- `ReleaseToWorker` 单次 `Reschedule` 失败会由 lease expiry 最终兜底，但造成可避免的即时恢复延迟；应使用与 settlement 一致的有界、lease-aware retry。

---

## 4. 主要风险

| 风险 | 影响 | 必要控制 |
|---|---|---|
| 双状态机/双写 | terminal、预算和恢复互相覆盖 | GoalRun 仅为编排 projection；owner action adapter |
| 语义输出后 replay | 重复内容、重复工具副作用 | commit gate、`resume_safety_blocked`、人工升级 |
| 进程内 follow-up | 重启/多实例导致重复或丢失 | DB action/lease/CAS/outbox |
| tool 误完成/自动执行 | 未执行工具被标完成或执行危险操作 | `waiting_tool`、结果证据、approval/allowlist |
| 插件权限过大 | secret/DSN/跨 tenant 泄漏或任意执行 | DTO/capability/sandbox/ready gate |
| response 后拦截误用 | 已泄露内容无法撤回 | 请求前控制、chunk-time终止、明确局限 |
| audit parse 降级 | 不可靠审计被视为通过 | `audit_degraded`，禁止自动修复/发布 |
| 202 协议混淆 | 客户端错误恢复或会话分叉 | durable/pending/handoff/approval 独立信封 |

---

## 5. 发布阻断与测试门禁

以下未完成时不得启用 action mode：

1. Goal request schema、GoalRun initialization、tenant/API-key/session binding、policy snapshot 和 idempotency。
2. action/step 的 CAS、lease、fencing、outbox、terminal sticky、cancel race。
3. content/tool checkpoint 后的 no-replay 和 `resume_safety_blocked`。
4. 多实例/重启、Redis flush + PG fallback、deadline/attempt/budget/model switch/handoff cap。
5. tool wait/approval 语义，以及 handoff/durable/pending/approval 202 协议隔离。
6. plugin signature/handshake/capability/DTO/scope/timeout/sandbox 基线。
7. audit degraded、repair suggestion、model fallback policy 的安全边界。
8. 隔离真实 PG/Redis/plugin/provider 的端到端验证和 24 小时 observation gate。

当前 245 canary 的 audit/URSM/routing resolve 仍不是全量发布证据；任何 245/154 生产动作仍需要独立授权。

---

## 6. 推荐整改顺序

1. 先冻结设计、状态机、capability、DTO 和 202 协议。
2. 建 GoalRun ledger/action outbox 和 request-level explicit activation，再接 status API。
3. 将 continuation 收敛为 durable action scheduler，随后接 recovery/checkpoint/fencing。
4. 持久化 handoff reservation/restore，并处理 GoalState v2 安全决策。
5. 最后接外部插件 data plane、tool execute、stream termination 与 audit/repair orchestration。
6. 通过 isolate canary 和观察后才逐层打开 observe/recommend/action gate。

---

## 7. 参考

- `docs/03-design/02-feature-design/会话优化v4/12-GoalHandoff契约.md`
- `docs/03-design/02-feature-design/会话优化v4/13-统一自动编排插件与Goal会话控制设计.md`
- `docs/adr/ADR-0001-handoff-goal-state-at-rest-encryption.md`
- `docs/design/2026-08-19-stream-state-machine.md`
- `docs/handoff/2026-08-19-canary-runbook.md`
- `docs/handoff/2026-08-19-canary-evidence-and-status.md`
- `docs/session-logs/2026/08/2026-08-19-245-restart-loop-followup.md`
