# Goal 模式客户端信号（gw-continue / gw-handoff）

> 2026-09-03 实现。本文描述网关在 goal 模式下向客户端发出"影子指令"的协议、触发条件、客户端职责，以及与现有 goal_continue 自调用路径的并存方式。

---

## 1. 动机

原有的 goal 模式用 `goal_continue` 服务端自调用来推进任务——客户端完全无感。这导致三个问题：

1. **客户端无法判断"这次响应是否包含续跑"** —— UX 上无法在 UI 上显示"任务进行中"
2. **跨会话迁移只能靠 `/handoff` 显式 skill** —— 没有隐式、按上下文阈值自动触发的路径
3. **续跑成本归因不透明** —— 网关自调用算谁的请求？

本次引入两个 SSE 自定义事件，把续跑/迁移的控制权交给网关感知客户端。标准 OpenAI 客户端（不声明能力）继续走自调用路径，零行为变化。

---

## 2. 协议总览

### 2.1 客户端能力声明

请求头：
```
X-Gw-Capabilities: continue,handoff
```

支持的 token（白名单解析）：
- `durable-recovery`（已有）
- `status-events`（已有）
- `continue`（2026-09-03 新增）→ 触发 `gw-continue`
- `handoff`（2026-09-03 新增）→ 触发 `gw-handoff`

服务端环境变量：
- `LLM_GATEWAY_GOAL_CLIENT_DRIVEN=true` 主开关
- `LLM_GATEWAY_GOAL_CLIENT_SIGNAL_MODE=auto|continue|handoff|both`
- `LLM_GATEWAY_GOAL_HANDOFF_SIGNAL_THRESHOLD=200000`（handoff 阈值 tokens）

### 2.2 服务端发出的两种信号

#### `event: gw-continue`（同会话继续）

```text
event: gw-continue
data: {"type":"gw_continue","version":1,"reason":"goal_incomplete","request_id":"...","session_id":"...","attempt":1,"max_attempts":3,"hint":"请继续下一步","tokens_used":12000,"context_window":128000,"sub_agents_pending":0}
```

**触发条件**（全部满足）：
- `goal.client_signal_enabled=true`
- 客户端声明 `continue` 能力
- `tokens_used < handoff_signal_threshold_tokens`（默认 200000）
- GoalJudge 判定任务**未完成**
- 续跑预算未耗尽（`continue_attempt < max_auto_continue_count`）

**客户端行为**：
- 读取 `hint`（默认"请继续下一步"），或自行构造继续提示
- 在**同一个 session_id** 上发起新一轮 chat completion
- 在 `messages` 末尾追加 `{"role":"user","content": <hint>}`
- 在 header 携带 `X-Gw-Parent-Request-Id: <event.request_id>`

#### `event: gw-handoff`（客户端发起跨会话迁移）

```text
event: gw-handoff
data: {"type":"gw_handoff","version":1,"reason":"context_near_limit","request_id":"...","session_id":"...","tokens_used":215000,"context_window":256000,"handoff_threshold":200000,"keep_recent_n":4,"summary_anchor":"<original goal truncated>"}
```

**触发条件**（全部满足）：
- `goal.client_signal_enabled=true`
- 客户端声明 `handoff` 能力
- `tokens_used >= handoff_signal_threshold_tokens`（默认 200000）
- GoalJudge 判定任务未完成且继续无意义（上下文已满）

**客户端行为**：
- 生成**新 session_id**
- 新请求 messages 构造成：`[system(summary_anchor), ...keep_recent_n 最近轮]`
- 携带 header `X-Gw-Previous-Session-Id: <event.session_id>`
- 可选：从上次会话本地缓存取最近 N 轮，否则从服务端拉取

### 2.3 非流式响应

非流式响应通过 response trailer 表达信号（HTTP trailer 在 body 之后到达）：
```
X-Gw-Client-Signal: gw-continue
X-Gw-Client-Signal-Payload-B64: <base64 of JSON payload>
```

---

## 3. 子代理追踪（sub-agent gate）

### 3.1 客户端报告

请求头：
```
X-Gw-Sub-Agents: [{"id":"agent-1","status":"completed"},{"id":"agent-2","status":"running"}]
```

- `status` 是字符串，网关只识别 `completed` 作为终态
- 任何其他 status（含 `running`、`queued`、未知）视为**未完成**

### 3.2 网关行为

- 每次请求到达时，网关解析该 header 并写入 `goal_sessions.sub_agents_*`
- `CompletionDetector.IsCompleted` 新增 Strategy 0：若 `sub_agents_pending > 0` 则**不判定完成**
- 这防止"assistant 文本说 done 但子代理还在跑"的误判

---

## 4. 数据库 schema

新 migration：`db/migrations/365_goal_client_signal.sql`

### `goal_sessions` 新增列

| 列 | 类型 | 说明 |
|---|---|---|
| `continue_attempt` | INT NOT NULL DEFAULT 0 | gw-continue 已发次数 |
| `last_completion_judgement` | VARCHAR(32) | 最后一次判定理由（如 `subagent:pending`、`keyword:done`） |
| `sub_agents_total` | INT NOT NULL DEFAULT 0 | 客户端上报的子代理总数 |
| `sub_agents_completed` | INT NOT NULL DEFAULT 0 | 已完成的子代理数 |
| `sub_agents_pending` | INT NOT NULL DEFAULT 0 | 未完成的子代理数 |
| `last_sub_agents_report_at` | TIMESTAMPTZ | 最近一次上报时间 |

### `session_summaries` 新增列

| 列 | 类型 | 说明 |
|---|---|---|
| `parent_session_key` | VARCHAR(255) DEFAULT '' | 为后续客户端 handoff lineage 记录预留；本次网关只发出 `gw-handoff` 信号，不自动创建新会话或填写该列 |
| `handoff_reason` | VARCHAR(64) DEFAULT '' | 为后续客户端 handoff lineage 记录预留；本次网关只发出 `gw-handoff` 信号，不自动写入该列 |

加 partial index `idx_session_summaries_parent`。

---

## 5. 行为变更与回退

### 5.1 双轨灰度

| 客户端类型 | 行为 |
|---|---|
| 不声明 `continue`/`handoff` | 走原有 goal_continue 自调用路径（不变） |
| 声明 `continue` 但网关 `LLM_GATEWAY_GOAL_CLIENT_DRIVEN=false` | 同上（自调用） |
| 声明 `continue` 且开关打开 | 发 `gw-continue` SSE 帧；不再自调用 |
| 声明 `handoff` 且上下文超阈值 | 发 `gw-handoff`；客户端换新会话 |

### 5.2 安全边界

- 网关**只发信号**，不替客户端发请求；继续消息的最终组织由客户端决定
- 网关无法伪造用户输入；只能提示"请继续"
- `X-Gw-Sub-Agents` header 失败时静默忽略（fail-open），不阻断请求

### 5.3 关闭开关

设置 `LLM_GATEWAY_GOAL_CLIENT_DRIVEN=false` 或 `goal.client_signal_enabled=false`（租户级）即恢复到原有行为。

---

## 6. 实现清单（按文件）

| 文件 | 改动 |
|---|---|
| `db/migrations/365_goal_client_signal.sql` | 新增 |
| `domains/streaming/durable_contract.go` | 新增 `CapabilityClientSignal`/`CapabilityHandoffSignal` 常量、`ClientSignalRequested`/`HandoffSignalRequested` helper |
| `domains/streaming/stream_recovery.go` | 新增 `RenderClientSignalFrame` |
| `domains/streaming/sub_agents.go` | 新增 `parseSubAgentsHeader` + `SubAgentSnapshot` |
| `domains/streaming/goal_sub_agent_recorder.go` | 新增 `GoalSubAgentRecorder` 接口 |
| `domains/streaming/handler.go` | ChatHandler 字段 + setter + 追发逻辑 + RequestIdentity.SubAgents |
| `domains/hooks/response/types.go` | `EndResult`/`InterceptResult` 新增 `ClientSignal*` 字段 |
| `domains/hooks/response/chain.go` | Chain 合并 `ClientSignal*`（last-writer-wins） |
| `domains/hooks/goal/mode_hook.go` | `ModeConfig.ClientSignal*` + `Session.ContinueAttempt`/`SubAgents*` + `tryBuildClientSignal` |
| `domains/hooks/goal/completion_detector.go` | Strategy 0: sub-agent gate |
| `domains/hooks/goal/store.go` | `GetSession`/`CreateSession` 加列；新增 `RecordSubAgents`/`ClaimContinueAttempt`/`RecordLastCompletionJudgement`（client-signal 方法通过可选接口接入，兼容旧 store） |
| `settings/goal_specs.go` | 新增 `goal.client_signal_enabled`/`mode`/`handoff_signal_threshold_tokens` 三条 spec |
| `cmd/gateway/goal_control.go` | 读取 client-signal 环境变量并注入 `ModeConfig`；子代理快照通过 `SetGoalSubAgentRecorder` 持久化 |

---

## 7. 验收

### 7.1 单元测试（已通过）

```
go test -run "TestRenderClientSignalFrame|TestParseSubAgentsHeader|TestParseClientCapabilitiesContinueHandoff|TestClientSignalRequested" ./domains/streaming/
ok  	github.com/kaixuan/llm-gateway-go/domains/streaming	0.815s
```

覆盖：
- `gw-continue` / `gw-handoff` 帧格式正确
- 空 EventName 防御
- sub-agent header 解析（缺失/JSON 错/全完成/混合/未知 status）
- capability 解析（白名单扩展、向后兼容、未知 token 忽略）
- `ClientSignalRequested` / `HandoffSignalRequested` 通过真实 `*http.Request`

### 7.2 已验证的自动化覆盖

当前已验证（可追溯到 `domains/hooks/goal/goal_test.go` 与 `domains/streaming/client_signal_test.go`）：
- GoalHook 的 `gw-continue` 与 `gw-handoff` 决策分支；
- handoff 不消耗 continue budget；continue 通过 store 的原子 claim 接口申请预算；
- 未声明 capability 时保留 legacy `goal_continue` 路径；
- pending sub-agent gate 阻止完成判定；
- capability 白名单、子代理 header 解析、SSE frame 格式和 non-stream trailer contract helper。

这些证据是 Hook/协议级测试，不代表完整 HTTP handler、真实 PostgreSQL 或客户端端到端流程均已验证。

### 7.3 尚未完成的集成/端到端覆盖

以下必须按 [专项测试方案](05-testing/02-test-plans/TP-CLIENT-GOAL-SIGNALS.md) 执行；没有对应环境时应记录为 `UNKNOWN`：
- mock upstream + `httptest` 验证 stream 的上游内容、`[DONE]` 和单个 gw frame 的实际写出顺序；
- non-stream 真正接收 trailer、base64 payload 解码，以及 signal 不与 legacy follow-up 并发执行；
- 真实 PostgreSQL 的 `ClaimContinueAttempt` 并发上限、重启恢复、schema 升级/重复执行/回滚；
- 多协议入口（OpenAI Chat、Anthropic Messages、Responses）及 tenant hot-reload 隔离；
- 真实客户端消费 `gw-continue` / `gw-handoff` 后的续跑/换会话行为；
- 客户端创建新会话后写入并展示 `parent_session_key` / `handoff_reason` lineage。
