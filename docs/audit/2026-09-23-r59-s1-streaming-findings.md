# R59 48h 审计轮 — S1 组（流式终态闭环 + 错误信封）审计发现

- 仓库：llm-gateway-go-2 @ main `9f7b0ea3f`，只读审计（build + test 均实跑）
- 审计窗口：`git log --since="2026-09-21 12:00"` 流式域 7 个修复 commit
- 实证基线：`go build ./...` 通过；`go test ./domains/streaming/...` 全绿（streaming 80s / executors 35s / webcookie / integrity / state），`errorsx` / `internal/vendorstrip` / `internal/requestflow` 全绿
- 方法：每项声明读代码定位修复体 + 实跑对应测试；对"单终态帧不变量 / TerminalRendered 全渲染点置位 / retryable 一致性"做了跨四路径（chat / anthropic / anthropic-passthrough / responses）穷举

## 一、声明逐项实证结果

| # | Commit / 声明 | 实证结果 | 证据 |
|---|---|---|---|
| 1 | 524457546 §11.6 post-DONE 第二终端泄漏根修（终态哨兵） | ✅已实证（chat 面）。`errSurvivalTerminalRendered` 哨兵 + `terminalOnWire` 闩 + `%w (%w)` 双包装 + handler 黑洞守卫全部在位；e2e 钉桩实跑 PASS（6.5s） | survival_wiring.go:35,118,153,261；handler.go:4717-4726；survival_committed_broken_e2e_test.go:84 |
| 2 | 0cc311abe passthrough/通用 fallthrough 补 retryable + 双终态帧根治（共享哨兵 / TerminalRendered 双别名 / execTerminalRendered / blackhole 守卫） | ⚠️部分实证。哨兵/守卫/fallthrough/passthrough 信封均在位且测试绿；**但 anthropic executor 丢弃 TerminalRendered（见 F1），"根治"仅覆盖 chat executor 家族** | errorsx/terminal.go:20；handler.go:9161-9172,5182-5208；anthropic_bridge.go:504-540；executors/executor.go:343-346（type alias） |
| 3 | b0c77269d wrapAttemptWriter 复用 GateWriter | ✅已实证。Unwrap 循环命中 `*GateWriter` 返回外层 writer + 既有 gate；单测实跑 PASS | attempt_gate_wiring.go:78-87；gate_writer_test.go |
| 4 | 44823d798 unwrap not-found 分支丢装饰层 + 两个 wire 级回归测试 | ✅已实证。`w = orig` 恢复 + 8 跳上限在位；`TestWrapAttemptWriterKeepsDecorationWhenNoGateWriter` / `TestWrapAttemptWriterReusesPreGatedWriterBehindMonitor` / `TestStreamChatSurvivalGateReuse_SingleTerminalOnCommittedBreak` 三测试实跑 PASS（后者断言真实 wire 字节：恰 1 错误帧、1 [DONE]、retryable:true、[DONE] 后零帧） | attempt_gate_wiring.go:83-92；stream_eof_test.go:856-912 |
| 5 | d8a7849fd 良性EOF三桥对齐 | ⚠️部分实证。chat 桥（本轮新增）与 anthropic→openai、anthropic→responses（09-13 存量）三处对齐；**OpenAI→Responses 桥（/v1/responses 经 OpenAI 形上游）未对齐（见 F2）** | stream.go:1132-1152；anthropic_bridge.go:999-1001；responses_bridge.go:737-770（✓）；responses_bridge.go:1187-1203（✗） |
| 6 | 9f3e89cae 17 类取证修复 | ✅已实证（7 个子项）：resume_blocked 信封 retryable=true（survival_wiring.go:349-351，fail_terminal 保持 false 有测试）；prewarmed 耗尽信封 EffectiveRetryable + Tried==0 兜底（handler.go:5118-5125）；§11.6 帧补 reason/retryable（stream.go:1226,1303）；writePassthroughErrorEventFull 补 code/retryable（anthropic_bridge.go:526-540）；minimax coercer 在 main.go 两个闭包都接线（cmd/gateway/main.go:1789,1849）；endsWithMarkerPrefix 跨 delta + unwrap 空格拼接（tool_call_xml.go:115；internal/vendorstrip/minimax.go:141-156）；live stream delta push Info→Debug（admin/live_stream_sse.go:1957）。coercer 8 用例 + 信封 retryable 断言全 PASS | 各 file:line 如上 |
| 7 | isClientInterruptError 精确匹配 client_cancel 强制 false | ✅已实证。仅前缀匹配三个客户端中断 reason（client_cancel/client_disconnected/client_write_failed），与 bridges 全部 client reason 词表穷举一致，无误伤上游类；handler.go:5189-5195 强制 fallbackRetryable=false | handler.go:9179-9187,5189-5195 |

## 二、发现清单

| 级别 | 发现 | 反证（file:line） | 修复建议 |
|---|---|---|---|
| **P1** | **F1：0cc311abe "双终态帧根治"未覆盖 anthropic executor 家族——`terminalRendered` 字段只在 executeOpenAI 回填，AnthropicExecutor 的 streamInterruptedError 不携带。anthropic 桥在已提交流上写完终态错误帧（chunk_timeout / read_error / passthrough 帧）后 outcome.TerminalRendered 被丢弃 → forwardForDispatch 不包哨兵 → handler blackhole 守卫失明 → Exhausted/provider_error 信封可叠第二终态；survival 面同时因桥帧从不 `gate.MarkTerminalRendered()`，renderTerminal 的 gate 守卫也不触发 → `gateway_survival_*` 第二终态。R2 的 wire 级回归测试只钉了 chat 流函数，anthropic 家族零 wire 级测试** | executors/executor_anthropic.go:1380,1339（无 `terminalRendered:` 字段）vs executors/executor_chat.go:1482（有）；executor_dispatch.go:887-899（依赖该字段）；survival_coordinator.go:1116（gate 守卫）；anthropic_stream.go:891,913（帧已写、仅置 outcome）；stream_eof_test.go:856（测试仅 chat） | ①executor_anthropic.go:1380 补 `terminalRendered: outcome.TerminalRendered`；②anthropic_bridge.go / anthropic_stream.go 的错误帧渲染点补 `gate.MarkTerminalRendered()`（对齐 responsesScaffold.finishAttempt 模式）；③补 anthropic 版 `TestStreamChatSurvivalGateReuse` 等价 wire 级测试 |
| **P2** | **F2：良性 EOF 对齐缺口——OpenAI→Responses 转换桥（`StreamOpenAIToResponsesSSEWithDiagnostics`，/v1/responses 经 OpenAI 形上游的活路径）EOF 分支无视已跟踪的 finishReason，df60575b 同形（minimax 式上游发完 finish_reason 即断连不发包尾）在 responses 路径仍判 eof_without_done → turn 层硬失败复发** | responses_bridge.go:1187-1203（`if !upstreamDoneReceived` 直接 Interrupted=true，finishReason 在 :907/:1055 已跟踪但不消费）；对照同文件 anthropic→responses :737-770（对齐）与 stream.go:1132（chat 对齐） | 在 :1188 分支前加 `if finishReason != "" && chunkCount > 0` 良性判定 + finishAttempt，镜像 :737-770 的写法与空流 gate |
| **P2** | **F3：committed stream_timeout 终端帧缺合成 [DONE] 且缺帧根 code/reason/retryable——eof_without_done 帧按 df60575b 双形状修复并合成 [DONE]（"否则部分客户端永远等不到"），timeout 帧两者皆无；且 TerminalRendered 闩使 handler 黑洞，[DONE] 永不再来。同类客户端挂起风险按项目自己的论证成立** | stream.go:1303-1310（仅 error 对象内字段、无 [DONE]）vs stream.go:1226-1235（双形状 + `data: [DONE]\n\n` + 注释 :1229-1232 论证客户端挂起） | timeout 帧复制 eof 帧的双形状并在帧后合成 [DONE]，或显式论证 timeout 路径不需要（连接关闭兜底）并留注释 |
| **P2** | **F4：anthropic→openai 桥三个已提交输出错误帧渲染点漏置 TerminalRendered——stream_panic（:947-949）、malformed_tool_args（:1390-1392）、上游 terminal error event（:1539-1552）都向 committed 客户端写了错误帧，但 outcome.TerminalRendered 未置位（对照 :971-973、:1131-1132 已置位）。即使 F1 修复，这三处仍会双终态** | anthropic_bridge.go:947-949,1390-1392,1552（无置位）vs :971-973,1131-1132（有置位） | 三处 `outcome.TerminalRendered = true` 补齐（panic 分支在 deferred recover 内需注意命名捕获） |
| **P3** | F5：errorsx/terminal.go 注释断言 "ExecuteError.LastErr **has no Unwrap**" 与事实不符（executor.go:1879 有 `Unwrap()`），handler.go:9168-9169 的显式 LastErr 检查因此恒冗余（无害）。文档失真会误导下一轮修复者 | errorsx/terminal.go:12-13 vs executors/executor.go:1879-1884 | 修注释；显式检查可留作防御 |
| **P3** | F6：良性 EOF 成功路径（Interrupted=false）置 `outcome.TerminalRendered = true`，与字段文档"for a committed-output **interruption**"语义冲突。当前无害（成功路径不触发哨兵包装），但该 flag 若被成功路径消费者复用会误判 | stream.go:1147-1151 vs stream.go:459-466（字段文档） | 要么改文档为"protocol terminal on the wire（含成功合成 [DONE]）"，要么成功路径不置位 |

## 三、审过的反证面（未升级为发现）

- **retryable 一致性**：同类错误四路径信封核对——survival 帧按 action 推导（retry 类 action=true、resume_blocked 强制 true、fail_terminal/false_closed=false）；chat fallthrough 用 `ClassifyError/LastKind → EffectiveRetryable` + 客户端中断强制 false；anthropic passthrough 用 `EffectiveRetryable(oc.Kind)`；prewarmed 耗尽与非 prewarmed JSON 分支同源。唯一不一致是 F3 的 stream_timeout 帧根字段。
- ** survivalTerminalError 落库**：handler.go:4784-4814 在通用 fallthrough 前拦截，`failure_detail_code=gateway_survival_<action>` 落 request_logs，`ctx` 取消路径不包裹（survival_wiring.go:236-245），与哨兵共存正确。
- **minimax coercer**：main.go 两个桥闭包都接线；直通链路 `coerceXMLToolCallsInStreamLine`（stream.go:440）已有；8 个单测/桥级集成实跑全 PASS。
- **wrapAttemptWriter not-found 分支**：`w = orig` 恢复 + 8 跳上限正确，非 survival chat 流监控不再被旁路。
- **deprecated** `StreamResponsesSSE`（responses_stream.go:23）EOF 分支连终态帧都不写（:274-286），但唯一入口 responsesStreamWrapper 为 `//nolint:unused`（responses.go:1368），不可达，仅提示勿复活。

## 四、结论

7 项声明中 5 项完整实证（哨兵根修、gate 复用链、17 类信封修复、isClientInterruptError 精确性、wire 级回归测试全绿且断言真实字节）；2 项（0cc311abe 双终态根治、d8a7849fd 三桥对齐）声明范围大于实际修复面，分别留下 P1（anthropic executor 家族双终态守卫失明）与 P2（responses 桥良性 EOF 缺口）口子。新增测试均有测试背书且敏感性论证可信；本轮无"无测试背书的修复声明"。
