# 2026-10-17 r1029：reasoning-only 空回复（154 + 211）

## 症状

154 客户端报：

```
模型未返回任何内容
provider=new-provider-2 model=glm-5.3 reason=empty_model_response
```

`new-provider-2` 是客户端自己的 provider 别名，**网关侧无此概念**——
`providers` 表按 `code`/`display_name` 检索 0 命中。网关侧 glm-5.3 有 11 个 provider
（nvidia / openrouter / zhipu / evol / vapeur / apiclaude / pulian / apinext /
apichina / glm-5.3-xianyu / glm-5.2-month），客户端编号只是它自己的序号。

报错文案「模型未返回任何内容」在网关里的**唯一产生点**是两条腿：

- `domains/streaming/messages.go:1515` — Anthropic 腿终态 502
- `domains/streaming/responses.go:1421` — Responses 腿终态 502

**chat 腿没有这条文案**——所以症状形态本身就把范围收窄到了
「所有候选都空」之后的终态，不是单次上游抖动。

## 取证

### 取数路径的两次修正

1. `request_logs_hot` 已不存在。154 的库 `10.88.1.188:5432/llm_gateway`
   （**独立库**，不是 245 共用的那个），表已改分区：
   `request_logs_default` / `_2026_10` / `_2026_11` …
2. `request_logs_2026_10` 的最后一批行停在 **2026-10-13 20:18**，
   且 `request_status` 全是 `in_progress` —— 写入在提交终态前断了。
   成因是 `df9a213de feat(retirement): 退役 request_logs v1 双写族`：
   双写下线后唯一来源是 `session_turns`。
   **这一条本身是独立故障（请求日志断流 4 天），与空回复无关，但会让任何
   依赖 `request_logs` 的取证静默返回 0 行**——先记下，别误读成「没有故障」。

### 决定性证据

改从 `session_turns_hot` 取。glm-5.3 24h 内 `provider_error` 里有 11 条
`status_code=200` 却判失败——上游返 HTTP 200 但内容为空。

取 `request_id=623e36495abf1de578592fa90c78417a` 的 raw_data 原文：

```
client_request    : {"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"ping"}],"max_tokens":8}
upstream_request  : {"max_tokens":8,...,"model":"z-ai/glm-5.3-flash","stream":true,"stream_options":{"include_usage":true}}
```

上游响应逐帧（8 个非空帧，全部只有 `reasoning_content`）：

```
delta.reasoning_content = "The" / " user" / " has" / " sent" / " just" / " \"ping\"."
finish_reason = length
saw [DONE]    = false
usage         = {completion_tokens:8, completion_tokens_details:{reasoning_tokens:6}}
frames_with_content  = 0
frames_with_reasoning= 6
```

`max_tokens=8` 全被推理吃掉，`content` 一次都没出现。

## 根因

`ChatChunkHasOutput` 把 `delta.reasoning_content != ""` 计入「agent-visible 输出」。
于是 `sawSemanticOutput=true`，空流 gate 认为已见语义输出，走 r0924b 的
**benign-close 分支**——补一个 `[DONE]` 判成功：

```
HTTP 200 + finish_reason=length + content 全空  →  客户端 empty_model_response
```

r1028（`85f81dba9`，今天 00:56）修的是**幻影 tool_calls**（空 name/arguments），
与本形态正交，所以没覆盖到。

## 修复（r1029）

口径：**agent-visible 输出 = content + 具名 tool_calls**；reasoning/thinking
走独立通道透传，不参与空回复判定。

同一条判据在仓里散落**四处**，必须一起改，否则腿与腿之间漂移：

| # | 位置 | 改动 |
|---|---|---|
| 1 | `internal/emptyoutcome/emptyoutcome.go` `ChatChunkHasOutput` | 去掉 `ReasoningContent` |
| 2 | 同上 `ChatChoicesIsEmpty`（非流式 chat） | 去掉 `ReasoningContent` 早退 |
| 3 | 同上 `AnthropicContentIsEmpty` + Responses 侧分类器 | `case "thinking"` 不再返回 false；两个 `Signature` 结构体字段随之成为死字段，一并删除 |
| 4 | `domains/streaming/anthropic_bridge.go` 两处 | `emittedContent` 不再由 reasoning / `signature_delta` 置位 |

第 3 处里的 Responses 侧分类器是**改完第 3 行才发现的**：`AnthropicContentIsEmpty`
与它各自持有一份 `case "thinking"` 分支，改了一处漏一处。判据补了
`TestR1029ResponsesThinkingOnlyIsEmpty` / `TestR1029ResponsesRealOutputNotEmpty`
把它钉住——同一判据散落多处时，**逐处 grep 而非凭记忆**是唯一可靠的做法。


`redacted_thinking` **保留为输出**：它是供应商代替模型思考内容给出的块，
按惯例伴随答案轮出现，属 R16「网关无法完整建模的形态按有输出处理」。

thinking 内容本身仍照常透传客户端（`responses_bridge` 的 `reasoningText`、
`WriteReasoningDelta`），改的只是「算不算回答了」这一层判定。

## 判据与变异验证

`internal/emptyoutcome/r1029_reasoning_only_test.go`（9 条，含现场逐帧回放与
三条反向保护）。先写判据、确认在旧代码上**真的红**，再改实现。

变异验证三发，全部红在正确断言：

| 变异 | 变红处 |
|---|---|
| 恢复流式腿 `ReasoningContent` | r1029 流式逐帧断言 |
| 非流式腿删掉 tool_calls 分支 | r1029 非流式反向保护 |
| 恢复 Anthropic `thinking` 计输出 | r1029 Anthropic 断言 |

变异 2 顺带证明反向保护有效——不是把判据写成单向的。

## 撞到的既有冲突断言（9 处，全部逐条核实后处理）

**没有一条是「新引入的回归」**——除下面第 9 条已用 pristine 复跑证伪外，
其余都锁的是旧行为，与本次裁决正面冲突，按裁决更新；每处都**补了反向保护**，
否则这次修复会把正常推理模型打成空回复（换一个故障而不是修好）。

### 判定语义类（6 处）

| # | 位置 | 旧断言 | 处理 |
|---|---|---|---|
| 1 | `internal/emptyoutcome/emptyoutcome_test.go` | `reasoning content` → true | 拆成 `reasoning content only`=false / `reasoning alongside content`=true |
| 2 | `executors/empty_response_test.go` | `reasoning-only content is not empty` | 改 want，加 `reasoning alongside content is not empty` |
| 3 | 同上 | `signed thinking block is not empty` | 改 want，加 `signed thinking followed by text is not empty` |
| 4 | `executors/executor_anthropic_test.go` | 非空清单含 2 条 thinking | 移出，换成 `thinking + text` |
| 5 | `streaming/stream_content_gate_test.go` | `reasoning_content delta` → true | 改 want，加 `reasoning_content delta alongside content` |
| 6 | `streaming/empty_response_test.go` | `reasoning_content and finish_reason is not empty` | 改 want，加 `alongside content is not empty` |

### 样本撞上门类（2 处）——测的本意与空回复无关

| # | 位置 | 说明 |
|---|---|---|
| 7 | `streaming/stream_eof_test.go` `SplitsDoneJoinedToJSON` | 这条测**帧切分**（`}[DONE].` 传输粘连），样本恰好是 reasoning-only 才失败。给样本加 `content`，让它回到本来的测试对象 |
| 8 | `streaming/anthropic_bridge_test.go` `SignatureDeltaRetainedAndObserved` | 这条真正守的是两个安全性质——opaque signature **不伪造进 OpenAI wire**、且**可审计**（quality_flag）。`require.False(Interrupted)` 只是顺带，去掉它，两个安全断言原样保留（并顺手修掉 `out` 未使用的编译错） |

### 第 9 条：`tool_calls delta` ——r1028 的遗留红，与本次无关

`stream_content_gate_test.go` 的 `tool_calls delta` 样本只有 `{id, type}` 没有
`name`/`arguments`，按 r1028 的幻影 tool_calls 口径本就判空。**已用 pristine
复跑证伪**——`git stash` 掉 r1029 的全部改动、在原始 `origin/main` 上重跑，
该用例同样红：

```
--- FAIL: TestChunkHasContent_Cases/tool_calls_delta (0.00s)
    chunkHasContent(tool_calls delta) = false, want true
```

即 **r1028 落地时漏改了这条断言，已在 main 上红了一段时间**。本次按幻影
口径修正，并补 `named tool_calls delta` / `tool_calls arguments delta` 两条
正向断言把真实调用形态钉住。

### 一条容易被误判的红

第 9 条红在**同一张表**里，很容易被当成 r1029 的回归。判据是 pristine 复跑：
把自己的改动 stash 掉再跑一次，红的还在就是存量。这是判据纪律第 1 条的
直接应用——**「本来就坏」和「我改坏的」必须分开证明**。

同样方法用于另一组：`internal/partguard`（`TestNoParentTableDMLBeyondKnown`、
`TestPartitionParentsAreExhaustive`）与 `internal/rowsguard`
（`TestEveryRowsLoopIsGuarded`）。这三个用例在 **pristine origin/main 上同样红**，
与空回复判定毫无关系，成因指向 `df9a213de` 退役双写族那批迁移遗留的
分区父表 / rows 循环守卫问题，**需另起一轮处理**，不在本轮范围。




## 211 的情况

211 是 Docker 形态（容器 `llm-gateway-211`，:8781，`17f66c69-dirty`），
`/opt/llm-gateway-go` 不存在，无 `active-upstream.conf`。网关 healthy、ready:true，
但**日志里只有 NVIDIA/开源模型的探针流量，没有真实用户请求**，因此本次无法在
211 侧取证。代码是同一份，修复对两台一致生效。

## 附带发现

- `17f66c69-dirty` —— 211 跑的是 **dirty build**，工作区有未提交改动。
- `request_logs` 断流 4 天（见上），需要单独轮次处理。
- 154 的 DB 是 **独立库** `10.88.1.188:5432/llm_gateway`，与 245 不共用。
  本轮所有取证都在 154 自己的库上做。

