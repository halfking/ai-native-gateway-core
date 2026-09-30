# R75 —— 供应商错误处理与 think 模式错误转达（第一部分）

日期：2026-10-01
范围：`domains/streaming`（think 注入与生存流错误转达）、`domains/streaming/survival_wiring.go`、`action_bridge.go`
触发：objective「全面检查连接供应商端的请求的错误处理逻辑……**并且在有备用的情况下，需要以 think 或类似不影响会话的模式返回给客户端，并且不会中断请求流程。**」+ 注意事项「原则上所有的操作不要影响客户端的观感，要通过思考模式将信息进行转达，但不要影响会话，确保会话持续流畅地进行」

本报告覆盖 R75-B（think 模式）。R75-A（错误落库 `provider_error_details` + 凭据错误集合）子代理仍在运行，出方案后另补 §7。

---

## 0. 结论

**PARTIAL —— objective 的核心诉求在生产路径已真正达成。**

「上游失败 → think 注释转达 → **真实重试到备用凭据** → 唯一答案帧落地，无「假装成功」」这条链是通的，本轮实测确认。本轮**已修 1 个 P2**（最需要解释的失败类缺 think），另修 1 个被我的改动暴露的门缺陷（判据过宽），**出方案 2 项 P2**。

| # | 缺陷 | 严重度 | 状态 |
|---|---|---|---|
| 1 | 已提交内容后中断（resume_blocked）不发任何 think，客户端只拿到裸 error 信封 | **P2** | ✅ 已修 |
| 2 | 语义帧 Anthropic 缺 `content_block_start`、chat 的 `finish_reason` 为空串 | P2 | ⏸ 生产死代码，接线时升 P1 |
| 3 | `action_bridge.go` 声称「已在 R36-DEBT 登记」，该文档并无此条目 | P2 | ⏸ 出方案 |
| 4 | `handler.go` think 格式注释与实现矛盾（声称 `event: thinking + data:`，实为 `: thinking:` 注释帧） | P2 | ⏸ 出方案 |
| — | `TestDurableNonChatEndpointsLeaseLoss` 的内容哨兵用 `"hi"`，被 `thinking` 命中 | — | ✅ 已修（判据过宽，收紧而非改门） |

---

## 1. objective 核心诉求的实测结论

子代理端到端实测（我复核了实现路径）：

```
attempts=3 succeed=true decision=succeed/success
WIRE:
    : keep-alive
    : thinking: "正在等待可用节点并重试（第 1 次，原因=recoverable_candidate）"
    : gw-survival-keepalive
    : thinking: "正在等待可用节点并重试（第 2 次，原因=recoverable_candidate）"
    : gw-survival-keepalive
    data: {"choices":[{"delta":{"content":"REAL ANSWER"}}]}
    data: [DONE]
```

3 次上游调用（2 失败 + 1 备用成功）、2 条 think、**答案帧恰好 1 个**。无备用时 `succeed=false` + 干净 error 终态。

**「不中断」成立，「假装成功」不存在。** 机制在 `survival_coordinator.go:815` 发 think 后，于 retry 分支走 `Refresh()` + `continue` 真正再执行一次 `ExecuteAttempt`（`survival_wiring.go:131-144` 的 `Refresh` 重新解析候选）。

---

## 2. 已修

### 2.1 resume_blocked 不发任何 think 【P2】

**位置**：`domains/streaming/survival_coordinator.go:901`（resume_blocked 分支）

**事实**：这是**唯一不发出任何传输帧**的失败类。客户端手里握着部分答案（已 commit），却只收到一个裸 `event: error` 信封，无法区分「网关有意停止」与「连接被截断」。

objective 要求的「以 think 转达」在最需要解释的一类失败上缺失——**客户端观感最差的一类恰好是唯一无 think 的一类**。

**修法**：在该分支的 `renderTerminal` 之前发一条 think。

**刻意不复用 `notifyRetry`**：它渲染「正在等待可用节点并重试（…，等待 0s）」，而 `ADR-Disp-003` 明确禁止已提交后透明重试（否则客户端看到重复答案）。**先承诺再停止比沉默更糟。** 因此在 wiring 层为该 reason 单独出文案：

> 本次回答已输出部分内容后中断，网关不会透明重试以避免重复；请开启新会话继续。

reason 走内部闭集常量 `partial_answer_delivered_stop`，不引入新词表。

**变异验证**：去掉 think 后 `TestR74_ResumeBlockedEmitsThinkNotice` 红（`notices=[]`）。

> 变异脚本第一次没切中代码块导致「仍绿」，重做（用 `\n\t\t\t}\n` 锚定而非首个 `}`）后确认有判别力。**变异没红的第一解释应是「变异没生效」，不是「门没覆盖」。**

### 2.2 被我的改动暴露的门缺陷（判据过宽）【非缺陷，是门的问题】

**位置**：`domains/streaming/durable_nonchat_endpoint_test.go:268`

上一提交补 think 后，`TestDurableNonChatEndpointsLeaseLossRendersOneNativeTerminal` 转红。按纪律先假设门是对的，查下来**门确实过宽**：

```go
if strings.Contains(out, "hi") || ...   // 内容哨兵
```

传输注释帧是 `: thinking: …`，而 **"thinking" 里含有 "hi"**——门被 SSE 协议关键字本身触发，与内容是否重发无关。

**判据意图是对的**（「中断的尝试的内容没有被重发」），所以**收紧判据而不是放弃 think**：改为检查执行器写入的那一整帧 `tc.frame` 而非两字母片段。`tc.frame` 随用例不同（messages 是 `content_block_delta`，responses 是 `response.output_text.delta`），一条断言覆盖两个协议。

**判别力实证**（直接对比两个判据）：

| 场景 | 旧判据 `"hi"` | 新判据 `tc.frame` |
|---|---|---|
| 仅 think 帧（无内容重发） | **误报 = true** | 正确 = false |
| 含内容重发（真缺陷） | true | **true** |

旧判据在真缺陷时能报是**巧合**（它对一切含 "hi" 的串都报），在只有 think 帧时也报（假警报）。新判据只在真缺陷时报。

---

## 3. 出方案

| # | 缺陷 | 为什么不本轮修 |
|---|---|---|
| 2 | 语义帧（`action_bridge.go:337-368`）：Anthropic 只发 `content_block_delta` 无前置 `content_block_start`、index 硬编码 0；chat 的 `finish_reason` 为空串（OpenAI 规范要求 mid-stream 为 `null`） | **生产死代码**：`gatewayActionBridge` 在 `cmd/gateway/main_dispatch.go:40` 仅 `var` 声明、全仓无赋值点；`NewActionBridge` 的调用方仅 `action_bridge_test.go`。已双重 grep 确认。改它等于改一段跑不到的代码；若 owner 接线则升 P1 |
| 3 | `action_bridge.go:1-4` 声称「Registered as cleanup candidate R36-DEBT in `docs/audit/2026-09-17-r36-24h-audit-round.md` §四」，该文档 §四 的 6 条遗留登记中无此条目（已亲读全文） | 需先确认该登记是否记在别处；改文档前要问 owner 这个债务项是否真实存在 |
| 4 | `handler.go:4436-4438` 注释写 `event: thinking + data: {...}` 且「Zod skips unknown event names」，实现是 `: thinking: <json>` 注释帧；同文件 `:405-414` 已明确记录 `data:` 方案**是错的**（Zod `invalid_union`） | 与 2 同属死代码路径的注释；且 `:402-403`、`:406` 也有同类失真，宜一并处理 |

---

## 4. 已核对确认没问题的部分

- **三协议 think 一致**：`handler.go:4439`、`messages.go:742`、`responses.go:732` 三处 `OnNodeJump` 闭包**逐字同构**。`responses_bridge.go` 经 grep 确认**不含任何 think 注入**（仅 2 处注释提及），转达集中于 handler，无重复通道。
- **SSE 帧合法性**：`: keep-alive\n\n: thinking: "…"\n\n` — 无 `data:` 行、无裸换行（`json.Marshal` 转义）、`\n\n` 正确分帧。
- **不污染答案**：答案帧恒为 1；`action_bridge_test.go` 的 `TestActionBridgeNeverPollutesAnswerStream` 已固化。
- **中文字段污染**：think 正文全部是受控中文模板 + 枚举，**无英文技术词当正文**。上游 err 中的 `sk-` 密钥、内部 URL、用户 prompt **均未**进入 wire。
- **pause 不吞失败通知**：`pause()` 只 gate 心跳 ticker（`stream_session.go:80`），`writeThinking` 走 `WriteTransportFrame` 无 paused 检查。
- **并发无撕裂**：8 goroutine × 100 think + 100 答案帧，`-race` 干净，800 think 帧零 torn。`serializedResponseWriter` 单一 `SerializedStreamWriter` 串行化成立。
- **生存协调器三协议均已接线**：`handler.go:4622`、`messages.go:831`、`responses.go:832`。
- **已提交后不重复内容**：committed resume_blocked 实测 `partial answer` 出现 1 次。
- **非流式无 think 通道**：改走 `FailoverNoticeHeader` 响应头，drop 计入 `DispatchNoticeDroppedTotal`，注释与实现一致。

---

## 5. 子代理主动撤回的一条误判（记录在案）

R75-B 一度把「Anthropic / Responses 终态帧没有 `message_stop` / `[DONE]`」报为协议缺陷，随后**自行撤回**：该形态是全项目钉死的约定（`native_response_hook_integration_test.go:60-62` 显式 pin 三协议终态帧），**非 survival 特有**，非生存路径用同样形态。撤回理由充分，本轮未追查。

**我没有独立复核这一条**（涉及真实 Anthropic SDK 行为，本地无法验证），如实登记为「未验证」。

---

## 6. 验证

```
go build ./...                                    exit 0
gofmt -l                                          空
go test ./domains/streaming/ -run 'TestR74_|TestSurvival|TestDurableNonChat'   ok
```

**变异验证 2 处**：
- 去掉 resume_blocked 的 think ⇒ 红（`notices=[]`）
- 判据退回 `"hi"` 哨兵 ⇒ 红（证明收紧是必要的）

---

## 7. 一次操作失误的记录

为验证那个测试是否本轮就红，我执行了 `git stash`——**它抓到了并发会话的 WIP**（taskprofile 前后端 6 个文件）。`stash pop` 因 `menu-config.json` 的 `exported_at` 时间戳冲突而中止恢复，留下 `UU` 冲突状态。

处置：核对 stash 内容完整后，用 `git checkout stash@{0} -- <5 个文件>` 把并发会话的改动原样取回工作区（`menu-config.json` 保留 upstream 的新时间戳，`exported_at` 是自动生成元数据，取新值正确），确认工作区与 stash 逐文件无差异后才 drop。

**结论：并发会话的 WIP 未丢失，已完整恢复。**

**教训**：`git stash`（不带 pathspec）在有并发会话的 worktree 里是**破坏性操作**——它会连别人的未提交改动一起抓走。本会话此前每次 stash 都带了明确的 pathspec 或在确认工作区干净后才用；这次为图省事省掉了前提检查。今后在本仓库：`git stash` 之前必须先 `git status --porcelain` 确认为空，否则一律带 pathspec。

---

## 8. 能力边界

- **无运行时/线上验证**：全部结论来自静态审读 + 注入式单测。未在 154/252 生产验证真实客户端观感。
- **「Zod 会拒收 `finish_reason:""`」是基于 `handler.go:405-414` 记录的既有研究推断**，未实跑 opencode/zcode SDK。
- **D2 的严重度依赖「生产未接线」这一前提**：已双重 grep 确认源码侧，但未核对 154/252 实际二进制是否含旧版 ActionBridge 接线。若线上曾接线过，该项应重判 P1。
- **未追进 `internal/liveactions` 的 Detail 字段**验证「只含 id/标签/计数」的承诺（该链路当前亦为死代码）。
- **R71–R74 域未触碰**，不评价其结论。
