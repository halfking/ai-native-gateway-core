# Goal 影子指令与续跑优化方案

> 2026-09-17。基于对 goal 模式"影子指令"语义的可行性分析结论，落地的三项优化：
> 影子轮打标（网关侧严格影子）、tool_calls 收尾的 advisory gw-continue、goal 配置缺失启动告警。
> 前置分析对象：`docs/GOAL_CLIENT_SIGNALS.md`（gw-continue/gw-handoff 协议）、`domains/hooks/goal/`（goal 模式实现）。

---

## 1. 背景与可行性结论回顾

**需求**：goal 模式判定任务未完成时，网关向客户端发起"请继续/continue"类影子指令，客户端在原会话中向大模型发起继续指令推进任务，但网关发出的指令不进入会话。

**结论**：可行，且架构已实现两条路径。"影子"语义成立的核心原因是**网关对会话历史无状态**——客户端每轮自带完整 messages 数组，网关没有任何渠道把内容写进客户端下次请求的对话里，带外信号（SSE 帧/Trailer）天然不进会话。

| 路径 | 机制 | 客户端对话历史 | 网关审计/会话存储 |
|---|---|---|---|
| A 服务端自调用（legacy） | 网关回环合成"请继续下一步"请求，同 session id | 不进入 ✅ | **进入**（与客户端历史出现分歧） |
| B gw-continue 信号 | `[DONE]` 后 SSE 帧 / 非 流式 Trailer，客户端自行续跑 | **hint 按协议追加为 user 消息，会进入** ⚠️ | 正常落一轮 |
| 信号本身 | 传输层自定义事件，非 chat message | 不进入 ✅ | 不适用 |

两个偏差点即本方案的优化对象：

1. **路径 A 的影子轮在网关侧不可辨识**：合成请求走完整管线后，"请继续下一步"这轮与正常客户端轮在 `request_logs`/`session_turns` 里完全无法区分，admin 看会话时分不清哪些轮是客户端发的、哪些是网关影子续跑。
2. **`finish_reason=tool_calls` 时两条续跑路径都不触发**（`mode_hook.go shouldAutoContinue` default 分支）：agent 型客户端大量 tool 轮后若自行停止循环而 goal 未完成，任务静默搁浅，网关没有任何提示通道。

---

## 2. 方案总览

| 编号 | 方案 | 优先级 | 默认行为变化 |
|---|---|---|---|
| 优化一 | 影子轮打标：follow-up 合成请求携带 `X-Gw-Source-Actor` / `X-Gw-Parent-Request-Id`，复用现有 origin_actor/parent_request_id 落库链 | P1 | 无（纯可观测增强） |
| 优化二 | tool_calls 收尾 advisory gw-continue：`goal.client_signal_on_tool_calls`（默认 false） | P2 | 无（双开关 opt-in） |
| 优化三 | goal 启用但 `LLMGatewayAutoLLMEndpoint` 缺失时启动 WARN | P0 | 无（仅日志） |

三项均向后兼容：不打标不影响任何行为；advisory 信号默认关闭且仅对声明 continue 能力的客户端生效；告警只是日志。

---

## 3. 优化一：影子轮打标（网关侧严格影子）

### 3.1 设计

goal 影子轮（`goal_continue` / `goal_model_switch` / `audit` 三类 follow-up）由 `defaultDispatchFollowUp` 构造合成请求回环走 `ChatHandler.ServeHTTP`。仓库已有完整的"网关内部请求标注"先例——auto-title 回环通过两个请求头落库：

- `X-Gw-Parent-Request-Id` → `logCtx.ParentRequestID` → `request_logs.parent_request_id`
- `X-Gw-Source-Actor` → `logCtx.OriginActor` → `request_logs.origin_actor` → `sessionv2mirror` s1a_fields → `session_turns.origin_actor`

（写入链：`handler.go` 入口读取 → `applyParentCorrelationFields`（request_log_pipeline.go:472）→ `internal/sessionv2mirror/hook.go entryToProcessedRequest` → `session_writer_v2.go` → `turn_writer.go`。）

本方案让影子轮**完全复用该链路**，零 schema 改动：

```go
// defaultDispatchFollowUp 内，合成请求新增：
req.Header.Set("X-Gw-Source-Actor", followUpSourceActor(action))  // goal-continue / goal-model-switch / goal-audit / goal-followup
req.Header.Set("X-Gw-Parent-Request-Id", parentRequestID)          // 触发本次续跑的客户端请求 ID
```

`injectFollowUpRequest` 与 dispatch seam（`dispatchFollowUpFunc`）各增加一个 `parentRequestID` 参数，由 handler 调用点（流式 handler.go:5183 / 非流式 :5209）传入当前 `requestID`。

### 3.2 效果

打标后可一键查询影子轮及其父链：

```sql
-- 某会话的全部影子续跑轮及其触发的客户端请求
SELECT t.request_id, t.origin_actor, t.parent_request_id, t.ts
FROM session_turns_hot t
WHERE t.gw_session_id = $1 AND t.origin_actor LIKE 'goal-%'
ORDER BY t.ts;
```

- **request_logs 侧**：影子轮 `origin_actor`/`parent_request_id` 非空，与正常轮可区分，审计真相完整保留。
- **session_turns 侧**：同样打标。会话视图里影子轮可见但可过滤——这是有意选择：若在镜像钩子里排除影子轮（类似 `isInternalAutoEntry`），`session_mirror_outbox`/GLOBAL_G2 对账不变式会把排除轮计为"漏镜像"，破坏 2026-09-15 刚闭环的 GAP-2 观察期。打标不排除，把过滤权留给查询侧。
- **客户端侧**：不变——影子轮从不回传原连接、从不进入客户端 messages。

### 3.3 明确不做的事

- **不排除镜像**：理由见上（GLOBAL_G2 对账）。若未来需要"会话视图与客户端历史严格一致"，应以独立开关 `sessions_v2.exclude_shadow_turns` 实现，并在对账 SQL 中同步豁免。
- **不引入 ephemeral message**：continue 指令必然进入 LLM 上下文（这正是续跑机制本身）；"影子"只能承诺不进客户端持久历史与可辨识的网关审计，协议上无手段让 LLM 收到指令又不让它看见。

---

## 4. 优化二：tool_calls 收尾的 advisory gw-continue

### 4.1 问题

`shouldAutoContinue` 对 `finish_reason=tool_calls` 返回 false（工具要客户端执行，legacy 自调用路径代跑无意义——该判断本身正确）。但对声明了 `continue` 能力的客户端驱动路径，同样的判断把信号通道也关掉了：agent 客户端在 tool 轮之间自行循环，若其循环因上限/异常终止而 goal 未完成，网关没有任何"目标还没完成"的提示通道，任务静默搁浅。

### 4.2 设计

新增租户级开关 `goal.client_signal_on_tool_calls`（env `LLM_GATEWAY_GOAL_CLIENT_SIGNAL_ON_TOOL_CALLS`，默认 **false**）。开启后，`decideAndContinue` 新增 Path 1.5：

**触发条件（全部满足）**：
- `goal.client_signal_on_tool_calls=true`
- `finish_reason == "tool_calls"`
- 客户端声明了 `continue` 能力（`req.ClientSignalAllowed`）——legacy 客户端永远不会收到，也绝不触发自调用
- `goal.client_signal_mode` 允许 continue（""/auto/continue/both）
- `sub_agents_pending == 0`（子代理还在跑时不打扰）
- 未进入 give-up（`!decision.giveUp`，循环检测已放弃时不继续骚扰）

**信号形态**：`event: gw-continue` 帧，但与常规续跑信号有三点不同：
1. **`advisory: true`**——客户端应视为状态提示而非追加指令；
2. **不含 `hint` 字段**——常规信号的协议动作是"把 hint 追加为 user 消息"，advisory 信号没有指令语义，避免客户端在 tool 轮之间插入"请继续下一步"污染对话；
3. **不消耗续跑预算**——不走 `ClaimContinueAttempt`，`attempt` 仅回显当前值。tool 轮次可能很多，若每轮扣预算，3 次的默认预算会在真正的搁浅场景到来前耗尽。

```text
event: gw-continue
data: {"type":"gw_continue","version":1,"reason":"goal_incomplete","advisory":true,
       "finish_reason":"tool_calls","request_id":"...","session_id":"...",
       "attempt":1,"max_attempts":3,"tokens_used":12000,"context_window":128000,
       "sub_agents_pending":0}
```

**客户端处置建议**（写入协议文档）：`advisory=true` 时不需要立即动作；推荐在自身 tool 循环退出、且 goal 未完成时把它当作"继续推进"的依据；UI 可展示"网关提示目标未完成"。

### 4.3 与完成判定的关系

tool_calls 轮同样先过 `IsCompletedWithSubAgents`（Strategy 1 会识别 tool arguments 里的 `{"status":"completed"}`）；只有判定未完成才会走到 Path 1.5。已完成的会话 CAS 进 `completed`，不会发 advisory。

---

## 5. 优化三：goal 配置缺失启动告警

goal 链路全部默认关闭，依赖 `LLM_GATEWAY_GOAL_ENABLED=true` + `LLMGatewayAutoLLM*` 端点；漏配端点时 LLM judge 与审计静默降级为关键词启发式，完成判定质量骤降且无任何提示。`initGoalControl` 在 `goal.enabled=true && !llmCallerConfigured()` 时输出启动 WARN，写明影响与补救配置项。纯日志，无行为变化。

---

## 6. 实现清单

| 文件 | 改动 |
|---|---|
| `domains/streaming/response_interceptor_helpers.go` | `injectFollowUpRequest`/`defaultDispatchFollowUp` 增加 `parentRequestID` 参数；合成请求设置 `X-Gw-Source-Actor`（action 映射）与 `X-Gw-Parent-Request-Id`；新增 `followUpSourceActor` |
| `domains/streaming/handler.go` | `dispatchFollowUpFunc` 类型加参；两处 `go h.injectFollowUpRequest(...)` 调用点传 `requestID` |
| `domains/hooks/goal/mode_hook.go` | `ModeConfig.ClientSignalOnToolCalls`；`decideAndContinue` Path 1.5；`tryBuildAdvisoryToolSignal`；`toolCallsSignalEnabled` |
| `settings/goal_specs.go` | `goal.client_signal_on_tool_calls` spec |
| `cmd/gateway/goal_control.go` | 读取新 env；启动告警 |
| `domains/streaming/response_interceptor_helpers_test.go` | seam stub 签名同步 + 父请求 ID 透传断言 |
| `domains/hooks/goal/goal_test.go`（或新增） | advisory 信号触发/关闭/无能力/sub-agent 门禁/不给 up/不扣预算 用例 |
| `docs/GOAL_CLIENT_SIGNALS.md` | advisory 变体协议 + 影子轮打标章节 |

---

## 7. 测试与验证

单元测试（本方案实施时落地）：
- `followUpSourceActor` action→actor 映射；`injectFollowUpRequest` 将 parentRequestID 透传到 dispatch seam。
- advisory 信号六分支（开/关/无能力/sub-agent pending/give-up/非 tool_calls）+ 不扣预算（mock store 断言 `ClaimContinueAttempt` 未被调用）。
- 既有 goal/streaming 全量回归。

上线观察项（部署后）：
- `origin_actor LIKE 'goal-%'` 的轮次占比与预算消耗对账；
- 开启 advisory 的租户，tool 轮信号频率（若过噪，后续可加每会话 advisory 上限）。

## 8. 后续候选（本期不做）

1. 客户端 ephemeral 续跑指引：协议文档给出"hint 短暂使用后从本地持久历史剔除"的推荐模式（网关无法强制，客户端自决）。
2. `sessions_v2.exclude_shadow_turns` 会话视图豁免开关（需同步对账豁免，风险高，需专门评审）。
3. advisory 信号每会话频控。
4. gw-continue/gw-handoff 真实客户端端到端验证（GOAL_CLIENT_SIGNALS.md §7.3 遗留）。
