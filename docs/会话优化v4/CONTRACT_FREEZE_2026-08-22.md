# T0 契约冻结（CONTRACT FREEZE — 2026-08-22）

| 项 | 值 |
|---|---|
| 版本 | v1.0（contract freeze T0 — 身份契约 / 重启语义 / action·lifecycle enum / 错误分类 / snapshot codec） |
| 日期 | 2026-08-22 |
| Owner | Contract owner |
| 范围 | 仅 fixture + 文档（`test/events/**` 与 `docs/会话优化v4/**`），不动 URSM/dispatch 生产路径、不动 migration、不动生产路由接线 |
| 代码基线 | HEAD `875e50a80`（main，2026-08-22） |
| 证据等级 | LOCAL_VERIFIED（`go test ./test/events/contract/...` 11/11 PASS + 现有 4/4 PASS，零回归） |

> 本文件冻结会话优化 v4 的核心契约表面**形状**（exact-bytes JSON）。具体生产行为由源代码常量枚举继续维护；本文件与其对应 fixture 是「不能漂移」的真值。任一修改须走 PR + 评审 + 同步更新 fixture + 同步更新本文件。

---

## 1. 身份契约（五类标识 exact-bytes 形状）

> 五类标识在同一请求/会话生命周期内并存，**互不重叠**、**互不替代**。任何跨字段混用（例如把 `session_id` 写到 `request_id` 位置）即视为契约违例。Fixture：`test/events/fixtures/identity_*.json`。

### 1.1 `request_id`（一次 HTTP 请求）
- **作用域**：网关内一次 HTTP 请求生命周期
- **生成方**：网关 server-side（不是客户端传入）
- **唯一性**：在 `(tenant_id, gateway_instance_id)` 内唯一
- **JSON 形状**：见 `test/events/fixtures/identity_request_id_v1.json`
- **消费方**：
  - `domains/requestjourney.JourneyEvent.RequestID`（`contract.go:405`）
  - `internal/liveactions.ActionEvent.RequestID`（`liveactions.go:99`）
  - `IngressEvent.RequestID`（`contract.go:279`）
- **禁止字段**：`request_body`、`request_headers`、`auth_token`、`user_content`、`system_prompt`、`api_key`

### 1.2 `attempt_id`（一次模型 × 节点尝试）
- **作用域**：一次（model × credential × provider）执行尝试
- **配对字段**：`attempt_no`（1-based，自 `request_id` 起始处为 1）
- **JSON 形状**：见 `test/events/fixtures/identity_attempt_id_v1.json`
- **消费方**：
  - `domains/requestjourney.AttemptRef.AttemptID`（`contract.go:378`）
  - `domains/dispatch.AttemptRef.AttemptID`（`journey.go:241-242`）
- **唯一性**：在 `(request_id, attempt_no)` 内唯一

### 1.3 `gw_session_id`（客户端逻辑会话文本标识）
- **作用域**：客户端多轮会话的逻辑文本标识（gateway pass-through）
- **生成方**：客户端（gateway 视为不透明字符串）
- **JSON 形状**：见 `test/events/fixtures/identity_gw_session_id_v1.json`
- **语义**：`gateway_logical_session_text`
- **重要不变量**：`gw_session_id` **不是** DB PK，不可单独做 unique 约束；与 `session_id` 配对使用

### 1.4 `session_id`（Sessions V2 文本标识）
- **作用域**：Sessions V2 表的文本 surrogate（稳定到 session 销毁）
- **生成方**：网关 server-side
- **JSON 形状**：见 `test/events/fixtures/identity_session_id_v1.json`
- **语义**：`sessions_v2_text`
- **消费方**：
  - `domains/session.Session.SessionID`
  - ASM `EventEnvelope.aggregate_id`（session-scoped 事件）
  - Sessions V2 API（list / lookup）

### 1.5 `SessionPK`（sessions.id 数值主键）
- **作用域**：`sessions` 表的数值主键
- **暴露面**：仅服务端内部；**不暴露**给客户端作为身份
- **JSON 形状**：见 `test/events/fixtures/identity_session_pk_v1.json`
- **语义**：`public_sessions_numeric_surrogate`
- **不变量**：`SessionPK > 0`；与 `session_id` 必须指向同一行；session 行生命周期内不可变
- **消费方**：`domains/session.SessionV2.Id`、Sessions V2 lookup 的内部 numeric 索引

### 1.6 五类标识互查表

| 标识 | 形态 | 生成方 | 唯一域 | 主要消费 | DB 角色 |
|---|---|---|---|---|---|
| `request_id` | text | server | `(tenant_id, gateway_instance_id)` | JourneyEvent / ActionEvent / IngressEvent | 无（project-only） |
| `attempt_id` | text | server | `(request_id, attempt_no)` | AttemptRef (requestjourney / dispatch) | 无（project-only） |
| `gw_session_id` | text | client | client-domain | 逻辑分组 / 客户端回传 | 无 |
| `session_id` | text | server | sessions 表 | Session / Sessions V2 API / ASM aggregate_id | text unique |
| `SessionPK` | int64 | server | sessions 表 | SessionV2.Id / 内部 numeric 索引 | BIGINT PK |

---

## 2. 重启语义（四种 lane 不可漂移）

> 重启时，调度层与观测层必须按 lane 决定恢复策略。Fixture：`test/events/fixtures/restart_lane_*.json`。重启 lane 语义对 URSM / dispatch / durable / mirror 四条路径**一刀切**地由 `lane` 决定，不允许「中间态」。

### 2.1 `ordinary_dispatch`（普通调度）
- `execution_recovery: false`
- `result_replay: false`
- `lease_required: false`、`fencing_required: false`
- `lease_live_upstream_calls: 0`、`lease_expired_upstream_calls: 0`
- `takeover_requires_fencing: false`
- `expected_projection: dropped`
- **规则**：普通请求（非 pending / 非 durable）在重启后**不恢复**；投影视图下次重建时丢弃孤立条目；无 lease、无 fencing、无任何上游重放。

### 2.2 `pending`（待执行）
- `execution_recovery: false`
- `result_replay: true`
- `lease_required: false`、`fencing_required: false`
- `lease_live_upstream_calls: 0`、`lease_expired_upstream_calls: 0`
- `takeover_requires_fencing: false`
- `expected_projection: replayed_result`
- **规则**：pending 队列中的请求（尚未真正进入上游执行）由新进程**重放**其上游结果（上游已执行完但响应丢失的场景）；**不发起新的上游调用**；不发 lease / fencing（pending 仍未越界进入 upstream execution）。

### 2.3 `durable`（URSM 接管）
- `execution_recovery: true`
- `result_replay: true`
- `lease_required: true`、`fencing_required: true`
- `lease_live_upstream_calls: 0`（重启时 lease 仍活）或 `lease_expired_upstream_calls: 1`（重启时 lease 已过期）
- `takeover_requires_fencing: true`
- `expected_projection: recovered_terminal`
- **规则**：
  - 必须先取 lease + fencing token 才允许接管
  - lease 在重启时仍活 ⇒ 不发起新上游调用
  - lease 在重启时已过期 ⇒ 恰好发起 1 次上游调用确认终态
  - 无 lease / fencing 不得操作 durable 行

### 2.4 `queue_mirror`（镜像优先 / URSM P0-1 默认档）
- `execution_recovery: false`
- `result_replay: false`
- `lease_required: false`、`fencing_required: false`
- `metadata_rebuild: true`（**仅这一个 lane 启用**）
- `lease_live_upstream_calls: 0`、`lease_expired_upstream_calls: 0`
- `takeover_requires_fencing: false`
- `expected_projection: metadata_only`
- **规则**：mirror-first 路径仅重建元数据（`request_id` / `session_id` / `model`），**不重建 `attempt_id`**；不发起上游调用；不重放结果。

### 2.5 重启 lane 决策矩阵

| lane | 是否恢复执行 | 是否重放结果 | 是否发 lease | 是否发 fencing | 上游调用次数（live / expired） |
|---|---|---|---|---|---|
| `ordinary_dispatch` | ❌ | ❌ | ❌ | ❌ | `0 / 0` |
| `pending` | ❌ | ✅ | ❌ | ❌ | `0 / 0` |
| `durable` | ✅ | ✅ | ✅ | ✅ | `0 / 1` |
| `queue_mirror` | ❌（仅 metadata） | ❌ | ❌ | ❌ | `0 / 0` |

---

## 3. action / lifecycle 枚举

### 3.1 Lifecycle 三态
**锚点**：`domains/requestjourney/contract.go:145-179`

| 状态 | 值 | 来源 stage | `retry_at` 是否要求 | `outcome` 是否要求 |
|---|---|---|---|---|
| `pending` | `"pending"` | received / routing / model_queue / credential_queue / node_selection | 可选（定时重试 parked 时设置） | 否 |
| `in_flight` | `"in_flight"` | upstream / streaming / retrying | 否（除非事件是 `retry_scheduled` 且携带 `retry_at`，但 lifecycle 仍为 in_flight；若 parked 则回到 pending） | 否 |
| `completed` | `"completed"` | terminal | 否 | **是** |

完整字段集合：`requestjourney.AllLifecycleStates()` 必须恰好返回 `[pending, in_flight, completed]`。Fixture：`test/events/fixtures/lifecycle_states_v1.json`。

### 3.2 `retry_at` 字段
**锚点**：`contract.go:427`（`JourneyEvent.RetryAt`）、`contract.go:495-501`（校验）

| 项 | 值 |
|---|---|
| 类型 | `*time.Time`（nullable） |
| JSON 编码 | RFC3339 UTC（nullable → 字段缺失） |
| 允许的事件类型 | `retry_scheduled`（**仅此一种**） |
| 校验 | `retry_at` 存在 ⇒ `!RetryAt.IsZero()`；其他事件类型携带 `retry_at` ⇒ `Validate()` 报错 |
| 快照兼容性 | 加性字段，旧 producer 写空值可被新 consumer 忽略 |
| 在生命周期中的角色 | `retry_scheduled + retry_at` ⇒ lifecycle=`pending`（parked back） |

### 3.3 `StageTerminal`
**锚点**：`contract.go:79-89`

```go
const (
    StageReceived        JourneyStage = "received"
    StageRouting         JourneyStage = "routing"
    StageModelQueue      JourneyStage = "model_queue"
    StageCredentialQueue JourneyStage = "credential_queue"
    StageNodeSelection   JourneyStage = "node_selection"
    StageUpstream        JourneyStage = "upstream"
    StageStreaming       JourneyStage = "streaming"
    StageRetrying        JourneyStage = "retrying"
    StageTerminal        JourneyStage = "terminal"
)
```

- `StageTerminal` 是 9 个 JourneyStage 之一，**且是唯一映射到 `LifecycleCompleted`** 的 stage
- 任何 `JourneyEvent.Stage == StageTerminal` ⇒ lifecycle 推导为 `completed`
- 终态投影（TotalRequestFIFO / ModelFIFO / NodeFIFO）一旦 stage=terminal，停止覆盖式更新（仅追加）

### 3.4 Outcome 枚举
**锚点**：`contract.go:361-373`（`type Outcome string` + 三个常量 + `Valid()`）

| 值 | 含义 | 触发的 event_type |
|---|---|---|
| `success` | 客户端拿到完整响应 | `request_succeeded` |
| `failure` | 重试预算耗尽 OR 不可恢复上游错误 | `request_failed` |
| `canceled` | 客户端 / context 取消 | `request_canceled` |

不变量：
- `Outcome == canceled` ⇒ `EventType == EventRequestCanceled`（`contract.go:486-489`）
- 空值合法（尚未到达终态）
- `Outcome.Valid()` 仅接受 `{success, failure, canceled, ""}`

### 3.5 Action 词表（V3.3-OBS OBS-B1）
**锚点**：`internal/liveactions/liveactions.go:46-92`（13 actions）

```
arrive, route_resolved, model_enqueued, credential_selected,
node_enqueued, node_selected, upstream_request, first_byte,
reply, node_switch, model_switch, state_change, no_route
```

完整集合：`liveactions.Actions`。Fixture：`test/events/fixtures/vocabulary_v1.json` 中的 `actions` 字段。

### 3.6 Lifecycle 状态转移表

> 表中 `trigger` 列：进入 terminal stage 的事件统一是 EventType（`request_succeeded` / `request_failed` / `request_canceled`）；**`pending → completed` 的两条提前终止路径**由 ErrorKind（`no_route` / `overflow`）驱动，是 dispatch 层在未进入 upstream 阶段就拒绝的特例，不是 EventType。

| from | to | trigger（EventType 或 ErrorKind） |
|---|---|---|
| `pending` | `in_flight` | `attempt_started`（EventType） |
| `in_flight` | `pending` | `retry_scheduled`（EventType，携带 `retry_at`，parked back） |
| `in_flight` | `completed` | `request_succeeded` / `request_failed` / `request_canceled`（EventType） |
| `pending` | `completed` | ErrorKind `no_route` 或 `overflow`（dispatch 层提前拒绝，未入 upstream） |

---

## 4. 错误分类词表 subset

> 错误分类遵循 **「dispatch 子集冻结，其他开放」**（`error_kind_policy: open_with_frozen_dispatch_subset`）。下表 11 项为冻结子集，新增 kind 必须更新 fixture `error_classification_v1.json` + 同步更新本表 + PR 评审。
> Fixture：`test/events/fixtures/error_classification_v1.json`。

| error_kind | 类别 | retryable | 主要 producer |
|---|---|---|---|
| `network` | transport | ✅ | dispatch, handler |
| `eof_without_done` | transport | ✅ | dispatch |
| `empty_response` | upstream | ✅ | dispatch, handler |
| `route_transient` | routing | ✅ | ursm, dispatch |
| `state_transient` | node_state | ✅ | ursm, credentialhealth |
| `queue_overflow` | admission | ✅ | dispatch（`journey.go:277`） |
| `no_candidate` | routing | ❌ | ursm, dispatch |
| `invalid_model` | client_error | ❌ | handler |
| `auth_failed` | credential | ❌ | dispatch, credentialhealth |
| `rate_limit` | throttle | ✅ | dispatch, ratelimit |
| `quota` | throttle | ❌ | ratelimit |

校验：
- `error_kind` 必须是 snake_case 小写字符串
- `error_kind` 不得泄露请求体、prompt、response、API key
- 旧 producer 可携带本表外的 kind（open 部分），但消费方不得依赖新增；新增须落本表 + fixture

`queue_overflow` 与 dispatch 一致：`domains/dispatch/journey.go:277`（`classifyError` 中 `errors.Is(err, ErrOverflow)` → `"overflow"`）。requestjourney 侧亦保留同义词 `ErrorKindOverflow = "overflow"`（`contract.go:200`）。

---

## 5. Snapshot codec 版本

| 字段 / 类型 | 版本 / 形态 | 备注 |
|---|---|---|
| `RequestSnapshot` | `schema_version: 1`（隐式；JSON 不强制含 `schema_version` 顶层字段） | 加性：`LifecycleState` 与 `RetryAt` 为加性字段（`omitempty`），旧 producer 写空值可被新 consumer 忽略 |
| `TotalRequestFIFOSnapshot` | v1 | `capacity` 显式，遵循 `DefaultTotalRequestCapacity` |
| `ModelFIFOSnapshot` | v1 | `model` 必填，`capacity` 显式 |
| `NodeFIFOSnapshot` | v1 | `(model, provider_id, credential_id)` 三元组作 key，`capacity` 显式 |
| `JourneyEvent.RetryAt` | v1 加性 | nullable；旧 producer 缺省即可 |
| `JourneyEvent.ObservationStatus` | v1 | `complete` / `observation_degraded` 二选一 |
| `JourneyEvent.Outcome` | v1 | `{success, failure, canceled, ""}` |
| `ActionEvent`（Redis LIST） | v1 | `seq` 进程内单调，全局不强保证 |
| `IngressEvent` | v1 | `request_id` + `gateway_instance_id` 双主键 |

兼容性策略：
- **加性字段**：写入时 `omitempty`，读取时缺省 → 视为空值（旧 producer 兼容）
- **删除字段**：禁止；如需语义替换，新增字段 + 旧字段保留 → 新 consumer 忽略旧字段
- **枚举变更**：仅允许追加；删除/改名 → MAJOR bump（v2）

---

## 6. fixture 与测试落地（不可漂移）

### 6.1 新增 fixture（exact-bytes）
| 文件 | 内容 |
|---|---|
| `test/events/fixtures/identity_request_id_v1.json` | request_id 形状 |
| `test/events/fixtures/identity_attempt_id_v1.json` | attempt_id 形状 |
| `test/events/fixtures/identity_gw_session_id_v1.json` | gw_session_id 形状 |
| `test/events/fixtures/identity_session_id_v1.json` | session_id 形状 |
| `test/events/fixtures/identity_session_pk_v1.json` | SessionPK 形状 |
| `test/events/fixtures/restart_lane_ordinary_v1.json` | ordinary_dispatch lane |
| `test/events/fixtures/restart_lane_pending_v1.json` | pending lane |
| `test/events/fixtures/restart_lane_durable_v1.json` | durable lane |
| `test/events/fixtures/restart_lane_queue_mirror_v1.json` | queue_mirror lane |
| `test/events/fixtures/lifecycle_states_v1.json` | 三态 + retry_at + StageTerminal + Outcome |
| `test/events/fixtures/error_classification_v1.json` | 11 个 frozen subset |

### 6.2 新增测试
| 测试 | 覆盖范围 |
|---|---|
| `TestFreezeIdentityRequestID` | identity_request_id_v1 |
| `TestFreezeIdentityAttemptID` | identity_attempt_id_v1 |
| `TestFreezeIdentityGwSessionID` | identity_gw_session_id_v1 |
| `TestFreezeIdentitySessionID` | identity_session_id_v1 |
| `TestFreezeIdentitySessionPK` | identity_session_pk_v1 |
| `TestFreezeRestartLaneOrdinary` | restart_lane_ordinary_v1 |
| `TestFreezeRestartLanePending` | restart_lane_pending_v1 |
| `TestFreezeRestartLaneDurable` | restart_lane_durable_v1 |
| `TestFreezeRestartLaneQueueMirror` | restart_lane_queue_mirror_v1 |
| `TestFreezeLifecycleThreeStates` | lifecycle_states_v1（states + retry_at + StageTerminal + Outcome） |
| `TestFreezeErrorClassificationSubset` | error_classification_v1（11 kinds） |

### 6.3 现有回归
- `TestSessionIdentityV1Fixture`（`session_identity_v1_valid.json`）
- `TestRestartSemanticsV1Fixture`（`restart_semantics_v1_valid.json`）
- `TestVocabularyV1Fixture`（`vocabulary_v1_valid.json`）
- `TestCurrentGatewayPublisher`（end-to-end field-completion）

### 6.4 验收
- `go test ./test/events/contract/...` 须 **PASS**（11 freeze + 4 existing + 1 publisher + 6 skip = 22 cases）
- 任一 fixture 漂移（缺字段 / 改字段名 / 改枚举值）⇒ 对应 freeze test FAIL
- 跨包消费方（`requestjourney` / `liveactions` / `dispatch`）代码回归由 `go build ./...` + 各包 `_test.go` 兜底

---

## 7. 不做 / 范围外

- ❌ URSM / dispatch 生产路径改动
- ❌ migration / schema 改动
- ❌ 生产路由接线
- ❌ 旧 fixture（`session_identity_v1_valid.json` / `restart_semantics_v1_valid.json` / `vocabulary_v1_valid.json`）的删除与重写 — 它们是 v1 旧入口，由 `TestSessionIdentityV1Fixture` / `TestRestartSemanticsV1Fixture` / `TestVocabularyV1Fixture` 继续守护
- ❌ 任何「中间态」lane（除上述 4 种 lane 之外，不存在第 5 种 lane）

---

## 8. 变更记录

| 版本 | 日期 | 变更 | 证据 |
|---|---|---|---|
| v1.0 | 2026-08-22 | T0 契约冻结首版（5 identities + 4 lanes + 3 lifecycle states + 11 error_kinds + snapshot codec v1） | `go test ./test/events/contract/...` PASS |

> 修改本文件时必须同时更新 §6.4 验收状态、§8 变更记录、以及对应的 fixture 与 freeze test。