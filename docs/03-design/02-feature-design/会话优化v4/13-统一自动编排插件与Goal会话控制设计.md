# 13 · 统一自动编排插件与 Goal 会话控制设计

**状态**：Draft；本文冻结目标架构与安全边界，不代表运行时代码已实现或已发布。  
**日期**：2026-08-19  
**关联契约**：[12-Goal↔Handoff 协同契约](./12-GoalHandoff契约.md)  
**关联 ADR**：[ADR-0001：GoalState 静态存储风险](../../../adr/ADR-0001-handoff-goal-state-at-rest-encryption.md)  
**关联计划**：[统一自动编排插件执行计划](../../../04-implementation/plan/2026-08-19-unified-auto-orchestration-plugin-execution-plan.md)  
**关联审计**：[当前事实与缺口审计](../../../audit/2026-08-19-unified-auto-orchestration-plugin-audit.md)

---

## 1. 裁决与范围

### 1.1 裁决

新增一个逻辑上的 **Auto-Orchestration Plugin**。它是系统的编排层，不是第二套 LLM 执行器：

1. 插件在请求、治理、工具、响应、流结束、审计、durable event 和 session-close 等已有 hook 中消费事实。
2. 插件只产出可审计、可幂等、受策略约束的决策和 action；实际上游调用仍由 `dispatch`、`Executor`、`SurvivalCoordinator` 与 durable worker 执行。
3. `goal_sessions`、durable task、handoff confirmation、approval、session v2 各自保持其现有权威 store；编排插件只维护关联和决策 projection，禁止双写、禁止替代其终态语义。
4. 新会话的连续 Goal 必须由请求显式开启；**不得再从模型响应中的关键词或 `goal: true` 惰性激活**。
5. 普通同会话继续、跨会话 handoff、审批暂停和 durable 异步执行是四个不同协议，不能互相伪装或复用对方的 202 信封。

### 1.2 目标

- 将分散的 Goal、follow-up、audit、handoff、模型切换与 durable recovery 统一为可查询的会话编排时间线。
- 对每一个自动动作建立 tenant/session/request/task/attempt 的关联、预算、版本、幂等、租约和终态记录。
- 在没有语义输出前提供受限恢复；在 content、tool call 或外部副作用边界后安全停止，转入 `resume_safety_blocked` 或 `manual_required`。
- 允许管理员和客户端获取连续 Goal 的稳定状态，而不必从日志或进程内 goroutine 推断执行进度。
- 允许外部插件以最小权限接入已有数据面，而不把完整 gateway 环境、密钥、凭据或不受控 body 交给插件。

### 1.3 非目标

- 不让编排插件直接选择 credential、调用 provider 或绕开 dispatch/model policy。
- 不让插件自动确认显式 handoff、自动批准 approval、自动重放已提交内容或 tool call。
- 不把客户端 tool call 默认为网关可执行的工具。
- 不把 audit 建议自动应用为代码、部署、数据库、权限或外部系统修改。
- 不在本设计内实施 KMS、加密、生产 migration、生产部署或数据清理。

### 1.4 术语

| 术语 | 含义 |
|---|---|
| Goal session | `goal_sessions` 中面向模型 loop 的会话状态；不是完整执行账本。 |
| GoalRun | 一次持续目标执行的持久编排账本，以 `goal_run_id` 标识。 |
| Step | GoalRun 内一次单调序号的请求/恢复/审计/handoff 动作。 |
| Action | 可幂等、可重试的编排命令；不等于直接上游调用。 |
| Semantic commit | content 或 tool-call 已经对客户端可见的不可逆边界。 |
| `manual_required` | 系统有足够证据停止自动化，但不能安全选择下一步时的人工处置状态。 |
| Plugin binding | 一个插件被绑定到具体生命周期位置、能力、作用域、超时和失败策略的声明。 |

---

## 2. 当前事实与缺口

### 2.1 已有能力

| 能力 | 当前组件 | 权威边界 |
|---|---|---|
| 请求生命周期 | `domains/pipeline`、v1 `ChatHandler` | request 处理和 dispatch 前短路 |
| Goal loop | `domains/hooks/goal` | `goal_sessions`、continue/model-switch CAS |
| 跨会话 handoff | `domains/hooks/handoff` | confirmation 与 GoalState restore |
| Durable recovery | `durable`、`SurvivalCoordinator` | PG task、lease/fencing、commit state、outbox |
| 响应/流控制 | `response.ResponseInterceptor`、`interceptingStreamWriter` | 当前请求和 SSE frame |
| 工具定义/观测 | tool registry、tool interceptors、tool execution tracker | 定义改写与执行记录 |
| 审计/异步分析 | audit hook、`analysis_events` worker | 事件和分析投影 |
| 外部插件进程 | `plugin-runtime` | manifest、supervisor、health、API proxy |

### 2.2 现有缺口

1. Goal 目前在响应后按关键词/响应字段惰性激活，且 follow-up 依赖进程内 goroutine、context depth 与 `sync.Map` 次数计数；重启和多实例无法形成完整账本。
2. 现有 synthetic follow-up 固定为 Chat Completions 形状，不是协议保真的 Responses、Anthropic 或 Gemini continuation。
3. durable worker 只执行 durable task 的一次 attempt；它不进入 Goal response interceptor，不能自动串起连续 Goal。
4. `plugin-runtime` 只管理外部进程/菜单/API proxy，尚未注册 request、response、tool 或 durable hook；manifest capability 目前是描述性字段，尚未强制授权。
5. 外部插件会继承 gateway 环境，当前还会得到数据库连接信息；它没有资源、网络、文件系统或 capability sandbox。
6. v2 governance 具备 `mutate` 类型，但当前 dispatch wrapper 不消费 `MutatedBody`；不能将其当作已可用的请求改写机制。
7. `MemoryBus` 与 batch audit writer 不是可靠 outbox；进程异常或 sink 失败时不能当作 durable 交付证明。

---

## 3. 总体架构与责任边界

```text
Manifest / signature / handshake / health
                 |
                 v
        PluginRuntimeRegistry
                 |
                 v
        PluginBindingRegistry
     /       |        |        \
request   response   tool     durable/session events
adapter   adapter    adapter      adapter
     \       |        |        /
      existing pipeline / interceptor / worker seams
                 |
                 v
GoalRun decision + action ledger (new projection)
                 |
                 v
Dispatch / durable / approval / handoff / audit owners
```

### 3.1 PluginRuntimeRegistry

继续负责 manifest、签名、插件进程、握手、健康、版本、菜单与 entitlement。`ready` 的定义必须改为：

1. manifest 语法、版本和签名通过；
2. supervisor 启动成功；
3. handshake 返回契约匹配、plugin ID 匹配且状态为 ready；
4. 至少一次 health check 成功；
5. 所有 binding 的 capability、phase、scope、timeout 和 failure policy 被校验并成功注册。

仅扫描 manifest 或启动进程成功不能标记 ready。

### 3.2 PluginBindingRegistry

新增独立注册表，不复用当前 `plugin-runtime.Registry` 的菜单/状态职责。每个 binding 至少包含：

```text
plugin_id, binding_id, phase, priority, execution_mode,
capabilities, tenant_scope, model_scope, timeout,
concurrency_limit, failure_policy, dto_profile, enabled
```

`domains/pipeline` 的 stage 按添加顺序而非 phase 自动排序；注册器必须在装配时明确排序，不能只依赖 phase 字段。

### 3.3 同步与异步分层

| 平面 | 接入点 | 允许行为 |
|---|---|---|
| 同步请求控制 | v1 request 前、governance、transform、tool definition | allow、受限 rewrite、block、suspend approval、handoff proposal |
| 同步流控制 | `InterceptStreamChunk` | 确定性 rewrite、协议终止；不得撤回历史 frame |
| 响应后控制 | response end / non-stream end | 记录、审计、规划 successor；不得声称能撤回已送达内容 |
| 可靠异步控制 | `analysis_events` / outbox / durable worker | action 投递、重试、时间线、session-close 归档 |

### 3.4 不变量

1. 只有已授权的 binding 能读到相应 DTO；默认只给 ID、hash、模型、协议、stream 标记和脱敏 metadata。
2. plugin 不得读取原始 Authorization、gateway API key、provider credential、DSN、HTTP writer、未脱敏 headers 或其他 tenant 数据。
3. plugin 不得直接写 session v2、goal session、durable task、handoff confirmation 的业务表；它必须通过 owner 的受限 action adapter 发起命令。
4. 并行 pipeline hook 共享普通 `Metadata` map，默认只读；需要写入时必须使用插件私有 namespace，或在串行 stage 中执行。
5. 外部插件调用超时不等于 goroutine/进程已停止；adapter 必须遵守 context、限并发并实施 circuit breaker。

---

## 4. Capability、DTO 与失败策略

### 4.1 能力

```text
request.observe       request.mutate       request.block
session.observe       session.close
response.observe      response.mutate      response.block      response.stream
tool.observe          tool.mutate          tool.block          tool.execute
audit.emit            durable.consume
```

- `observe` 只读。
- `mutate` 只能操作 binding 声明的 patch 路径。
- `block` 只能在对应同步控制点产生明确协议错误或 suspend。
- `tool.execute` 默认禁用，必须是显式注册、租户许可、幂等、可审计、隔离执行的工具。
- `durable.consume` 只能消费持久事件，不能绕过 lease/fencing 更新 task。

当前 manifest 的 `capabilities` 尚未实施授权；在 enforcement 完成前，任何外部插件只能使用 observe 级别的本地试验 binding，不能进入生产数据面。

### 4.2 白名单 DTO

跨进程不序列化 `PipelineRequest`。建议 DTO：

```json
{
  "schema_version": 1,
  "request_id": "req_...",
  "tenant_id": "tenant_...",
  "session_id": "gw_...",
  "task_id": "task_...",
  "goal_run_id": "gr_...",
  "model": "...",
  "protocol": "openai-chat|responses|anthropic|gemini",
  "streaming": false,
  "direction": "request|response|tool|event",
  "body_profile": "none|summary|redacted",
  "metadata": {}
}
```

完整 body 必须由单独 capability、大小限制、脱敏、tenant policy 和审计共同批准；默认只传摘要或 hash。DTO 不包含 secret、token、cookie、credential、DSN、原始 confirmation token、完整工具结果或其他用户的内容。

### 4.3 Failure policy

| 类型 | 默认失败策略 |
|---|---|
| observe / audit | fail-open，记录可重试事件 |
| request mutate | 跳过 patch 并记录；高风险 patch fail-closed |
| request block / approval | fail-closed 或 suspend，取决于已冻结租户政策 |
| response mutate | fail-open；不得发送不完整或半修改内容 |
| tool mutate / execute | fail-closed |
| durable consumer | action 重试/死信，不阻塞已完成请求 |

失败策略必须由 binding 显式声明，不能依赖当前不同 hook 的隐式 `OnError` 习惯。

---

## 5. 会话过程拦截控制

### 5.1 决策类型

```text
allow
rewrite
block
suspend_approval
suspend_tool
suspend_analysis
handoff_propose
continue
terminal
```

| 决策 | 合法位置 | 约束 |
|---|---|---|
| allow | 任意 | 只记录事实 |
| rewrite | body snapshot 前、上游前 | 重新校验 schema、tenant policy、tool pairing；禁止修改身份/会话/幂等字段 |
| block | 请求前 | 返回协议正确错误；不得依赖响应后撤回 |
| suspend_approval | 请求前 | 持久化 snapshot 后返回 202；超时默认 reject |
| suspend_tool | tool plan 前 | 等待可信工具结果；不自动执行客户端 tool call |
| suspend_analysis | 请求前或异步 | 返回/投递可查询等待状态 |
| handoff_propose | context 压力时、dispatch 前 | 只产生 explicit proposal；不可静默换 session |
| continue | GoalRun action | 只在可恢复、无外部副作用、预算内状态创建 successor |
| terminal | GoalRun/durable owner | sticky；迟到 worker 不得覆盖 |

### 5.2 流式边界

`InterceptStreamChunk` 可改写当前 frame，但已经 flush 的内容不可撤回。chunk block 必须升级为完整协议终止：停止上游 attempt、发送正确 error terminal、记录已发送 frame 数、禁止对已提交内容自动重放。

stream end 仅适合审计、汇总和下一步决策；不能用作泄露补救或 client-visible body rewrite。

### 5.3 工具边界

- `finish_reason=tool_calls` 或等价工具语义进入 `waiting_tool`，不是完成，也不触发“请继续”。
- tool arguments 中的 `status=completed` 不证明工具已执行完成。
- 外部写入、shell、部署、数据库、权限、资金、身份或无 idempotency key 的工具必须 approval 或人工执行。
- tool-call semantic checkpoint 后，durable 不能透明 replay。

---

## 6. Goal 请求与 GoalRun 契约

### 6.1 显式激活

新会话创建或第一条 chat 请求可携带版本化 `goal` 对象：

```json
{
  "goal": {
    "version": 1,
    "enabled": true,
    "root_goal_id": "optional-client-id",
    "instruction": "持续执行指定任务，直到完成、阻塞或预算耗尽",
    "execution_mode": "continuous",
    "durability": "durable",
    "completion_policy": {
      "detector": "goal_v2",
      "min_confidence": 0.8,
      "require_terminal_evidence": true
    },
    "limits": {
      "max_wall_time_seconds": 86400,
      "max_turns": 50,
      "max_follow_ups": 50,
      "max_model_switches": 3,
      "max_handoffs": 5
    },
    "delivery": {"mode": "poll"}
  }
}
```

兼容 header 可为 `X-Gw-Goal-Mode: continuous`，但 header 只表达意图。服务端必须绑定 tenant、API key、session，并保存被平台上限收紧后的 policy snapshot。客户端不能通过 body 或 header 提升 token、成本、时间、重试或模型权限。

响应至少返回：`goal_run_id`、有效 policy version、effective limits、当前 status 和状态查询地址。

### 6.2 GoalRun ledger

新增独立账本，不把跨会话执行状态塞入 `goal_sessions`：

```text
goal_runs:
  goal_run_id, tenant_id, api_key_id,
  root_goal_id, root_session_id, current_session_id,
  root_request_id, last_request_id, last_durable_task_id,
  status, policy_version, policy_snapshot,
  instruction_hash, redacted_instruction_summary,
  turn_count, follow_up_count, retry_count, model_switch_count,
  handoff_count, tokens_used, last_progress_hash,
  deadline_at, lease_owner, lease_until, version,
  terminal_reason, created_at, updated_at, completed_at

goal_run_steps:
  goal_run_id, sequence, request_id, parent_request_id,
  session_id, durable_task_id, action, status,
  response_hash, result_version, created_at, completed_at

goal_run_actions:
  action_id, goal_run_id, causation_id, action_type,
  idempotency_key, expected_version, status, retry_at, attempts
```

每个 successor 必须由数据库 CAS 或 lease 创建，且 `sequence` 单调。进程内 depth/context/map 只能做防御性优化，不能作为全局预算或并发真相。

### 6.3 状态机

```text
created -> queued -> running
running -> waiting_tool | waiting_input | waiting_handoff | auditing
running -> resume_safety_blocked | manual_required
waiting_handoff -> restoring -> queued
waiting_tool/input -> queued
queued/running/auditing/restoring -> completed | failed | canceled | expired | manual_required
```

所有终态 sticky。`canceled` 优先于迟到 follow-up 或 worker 成功提交；lease 丢失的 worker 只能丢弃结果。

### 6.4 完成与审计

完成检测顺序：

1. 未完成 tool/approval/handoff 优先进入等待状态；
2. 可验证的结构化完成状态和工具/测试/外部结果证据；
3. 独立完成 detector 的置信度；
4. `stop`、`length`、关键词和自然语言仅是候选信号，不能单独证明可交付完成；
5. 完成候选进入 `auditing`；审计通过才 `completed`，审计解析失败/低置信度为 `audit_degraded` 或 `manual_required`，不得默认为通过。

审计模型可通过现有 model policy 选择，但不应绕过 dispatch。自动修复只能创建建议或需要审批的 action；不得直接提交、合并、推送、部署或修改生产配置。

---

## 7. Durable、pending 与 handoff 协议

### 7.1 Durable continuous Goal

连续 Goal 默认应要求客户端声明 durable capability 并请求异步交付。现有 durable 202 保持其 headers 和语义，并增加 GoalRun 字段；不要使用 handoff 的 headers：

```json
{
  "status": "in_progress",
  "goal_run_id": "gr_...",
  "goal_status": "queued",
  "sequence": 0,
  "task_id": "task_...",
  "session_id": "gw_...",
  "request_id": "req_...",
  "status_url": "/v1/goal-runs/gr_...",
  "result_url": "/v1/sessions/gw_.../pending-response?request_id=req_..."
}
```

PG 是权威状态；Redis pending 只是 projection。状态 API 应支持 `queued/running/waiting_tool/waiting_input/waiting_handoff/auditing/completed/failed/canceled/expired/manual_required/resume_safety_blocked`，并携带 request/task/sequence/run ID、terminal 标记和 retry hint。

### 7.2 Semantic commit 与恢复

- `none`/`metadata` checkpoint 才可自动 claim、reschedule 或重试。
- content/tool call/side-effect checkpoint 后禁止透明重放，进入 `resume_safety_blocked` 或 `manual_required`。
- recovery 时重新验证 API key、tenant、policy、模型候选和 session ownership；snapshot 不保存动态 credential。
- deadline、attempt、token、cost、loop、handoff 或 lease 上限触发时写确定终态，不能遗留无限 `in_progress`。

### 7.3 Handoff 边界

同 session continuation 使用 GoalRun successor；跨 session 才使用现有 explicit handoff proposal/confirmation。普通连续 Goal 不得伪装为 `handoff_required`。

如 policy 需要用户确认，GoalRun 进入 `waiting_handoff`，使用一次性 confirmation capability。accounting confirmed 与 Goal restore 的分离、幂等、expiry、`manual_required` 语义继续遵守 12 号契约。

---

## 8. 预算、模型与止损

平台硬上限优先，租户/客户端仅能收紧：

- wall-clock deadline、每 step timeout、lease expiry；
- per-run token/cost、tenant 月度预算；
- 最大 logical turn、follow-up、retry、model switch、handoff；
- 重复/无进度 hash 阈值；
- 单 GoalRun 同时只有一个有效 successor；
- dispatch 只在首个 semantic byte 前透明切换 credential/provider/model。

Goal 的 loop-level model switch 与 dispatch 的 request-level model fallback 分别计数，通过 event 关联，不得互相直接修改计数。fallback 必须复用 capability、task/work type、工具、response format、租户 policy、成本和 IQ gate；已尝试模型必须排除。

以下情况 fail-closed 或升级人工：撤销 key、tenant/session 不匹配、未知 policy/snapshot 版本、损坏 snapshot、目标 session 冲突、tool/side-effect checkpoint、lease 丢失、预算/期限耗尽。

---

## 9. 安全、审计与观测

### 9.1 安全前置

在允许外部 data-plane plugin 前，必须补齐：

1. manifest capability enforcement；
2. handshake 参与 ready 判定；
3. 移除插件的完整环境继承和 DSN 传递；
4. 最小 OS 用户、资源限制、网络 egress、文件访问限制或等价 sandbox；
5. plugin concurrency limit、timeout、circuit breaker；
6. DTO 脱敏与字段 allowlist；
7. 插件操作审计、tenant scope 重验和可撤销 kill switch。

当前 GoalState 仅有启发式脱敏、大小限制和短 TTL，**不是静态加密**。在 KMS 或批准的字段收窄方案落地前，禁止扩大持久化的明文任务内容。

### 9.2 审计事件

每一个决定和 action 记录：`event_id`、类型、时间、tenant、goal_run/session/request/task、causation/correlation、attempt/fencing token、policy version、reason code、脱敏 payload hash。ID 不进入高基数 metrics label。

建议指标：active runs、terminal reason、retry、budget stop、lease conflict、resume safety block、action duplicate、handoff restore failure、audit degraded、fallback rejection、pending projection error。

### 9.3 发布门禁

- mock/local 通过不等于真实 PG/Redis/provider 验证；真实依赖通过不等于可发布。
- 245/154/252 不是隔离 canary。隔离验证必须使用独立 PG、Redis、tenant、credential、model allowlist、key prefix 和 token。
- 生产 DDL、cleanup、部署、SSH、systemd、重启、凭据或模型 policy 变更需要独立书面授权、窗口、回滚和责任人。

---

## 10. 验收与开放决策

### 10.1 最小验收

1. 新会话 Goal 请求的 schema、tenant/API-key binding 和幂等初始化。
2. `root_goal_id -> goal_run_id -> sequence -> task_id -> request_id` 可追溯。
3. 多 worker/重启/Redis 丢失下只有一个 successor，PG 可恢复状态。
4. content/tool checkpoint 后不重放；cancel/terminal sticky。
5. handoff、durable、pending 202 协议严格分离。
6. tool call 不被误判为完成或自动执行。
7. audit degraded 不触发自动修复或发布。
8. 插件 capability/scope/DTO/timeout/failure policy 有 unit、integration、race 和真实依赖验证。

### 10.2 开放决策

- GoalRun 是否允许在明确 policy 下自动创建目标 handoff session，还是始终要求客户端创建；默认保持显式确认。
- GoalState v2 是采用 KMS envelope encryption，还是经产品批准后字段收窄；ADR-0001 在此之前继续有效。
- 外部插件使用 Unix RPC、mTLS HTTP 还是 in-process trusted adapter；在 sandbox 完成前，外部插件仅限 observe。
- 连续 Goal 的状态事件是否新增 SSE；轮询 API 必须始终存在，不能以 SSE 作为唯一交付。
