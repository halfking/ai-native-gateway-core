# 2026-09-23 流式错误取证与策略修复轮（17 类用户报错归因）

- 输入：用户报告的 17 类网关错误（minimax-m3 / glm-5.2 / claude-* / gpt-5.6-terra）
- 现场：本地部署 `~/kaixuan/llm-gateway-go`（8781/8782 蓝绿）+ 245（`/var/log/llm-gateway-go`、PG@172.16.2.210）
- 结论：四类根因——①上游中途断流（主因，供应商侧）②网关错误信封的 retryable 语义过保守（策略侧）③`live stream delta push` 日志洪水把保留期烧到 ~9 分钟（可观测性侧）④minimax 工具调用文本泄漏只在 OpenAI 直通链路被纠偏，桥接链路漏接（转换侧）。

## 一、17 类错误逐项归因

| # | 现象（客户端可见） | 产生方 | 根因 | 供应商侧? |
|---|---|---|---|---|
| 0/2/10 | `provider_code=gateway_survival_resume_blocked`（minimax-m3 为主） | 网关 survival | 上游断流时已有语义输出提交；`errorsx.DecideNextAction`（action_policy.go:310）对可恢复 kind + CommitState≥Content 判 resume_blocked——透明重试会拼重复字节，设计上正确；**但信封 retryable=false 使 agent 客户端整轮硬失败** | 是（断流源于上游） |
| 3/8 | `gateway request survival ended: committed_output` | 网关（renderSurvivalTerminal SSE 帧） | 同上，同一事件的信封文案 | 是 |
| 1 | `reason=empty_model_response`（glm-5.2） | **客户端**文案 | 网关侧对应 `empty_response`：空流 gate 已可换节点（executor_chat.go:1408）；全节点空响应→穷尽失败。245 上 claude-opus-5 两天 142 次 empty_response（中继问题） | 是 |
| 4/9 | `Partial assistant output was discarded…` | **客户端**文案 | 客户端丢弃自累积部分输出；网关侧对应 L1 holdback 丢弃（survival_coordinator.go:322） | 间接 |
| 5 | `Network connection failed for the provider request` | **客户端**文案 | 网关分类 KindNetwork（可重试、换节点）已生效；终态 retryable=false 见 #0 修复 | 是 |
| 6 | `Our servers are currently overloaded` | 上游 body（apiclaude 等中继 500/502） | `concurrentOverloadRe`（classify.go:605）→ 500/502=KindUpstreamOverloaded（熔断 30s→5min）、429/503/529=KindConcurrent（2min）。**节点标注合理**；终态信封缺 retryable（见修复 4） | 是 |
| 7 | `Upstream service temporarily unavailable` + Turn failed（gpt-5.6-terra） | 网关 streamretry keepalive 注释（retry.go:343）+ 终态信封 | 245 上 gpt-5.6-terra 两天 164 次 no_candidates（全节点冷却/耗尽）；keepalive 是重试进行中的正常信号；prewarmed 耗尽信封无 retryable 字段→客户端默认 false 硬失败 | 是 |
| 11 | `Tool result is missing for tool call chatcmpl-tool-…` | **客户端** | 断流发生在 tool_call 增量中途（id 已发、参数未完）→ 客户端无终态 | 间接 |
| 12 | `prompt exceeds gateway budget: 1066969 > 1048576` | 网关 budget guard | 按设计拒绝（2026-09-17 审计轮已闭环，docs/audit/2026-09-17-245-prompt-too-large*）；不改 | 否（客户端输入） |
| 13 | `minimax[>[<tool_call>…]<]minimax` 文本泄漏 | 上游泄漏 + **网关漏接线** | minimax-m3 内部 token 包装漏进可见文本；纠偏器只在 OpenAI 直通环（stream.go:1346）接线，**anthropic/responses 桥（ZCode 实际路径）未接**；且跨 delta 拆分的标记前缀检测缺失；unwrap 硬拼接把字段粘连（"lsCheck"） | 是+网关缺陷 |
| 14 | `provider_code=stream_read_error`（claude-opus-5） | 网关 anthropic 桥（anthropic_bridge.go:1109） | 上游读错误；已提交输出→Resumable=false→resume_blocked 类；信封无 retryable | 是 |
| 15 | `provider_code=EPIPE`（claude-sonnet-5） | 上游连接断 | classify→KindNetwork；同 #14 处理链 | 是 |
| 16 | `Tool call ended without a terminal event` | **客户端** | 同 #11（断流打断 tool_call） | 间接 |
| 17 | `provider_code=eof_without_done`（minimax-m3） | 网关 §11.6 防（stream.go:1157） | 上流无 [DONE] 收尾且已提交——2026-09-22 §11.6 修复按设计发结构化错误帧+合成 [DONE]；信封无 retryable | 是 |

客户端 turn UUID（499a5fb6-… 等 5 个）在本地/245 的日志与 request_logs 均不可检索——它们是 agent 客户端 turn ID，非网关 request_id（网关为 32 位 hex）；按错误类别在两侧现场完成了取证。

## 二、修复清单（本轮 5 项）

1. **日志洪水（P0）** `admin/live_stream_sse.go`：`live stream delta push` Info→Debug。实测占日志体量 **99.7%**（811 行=104MB，单行 ~130KB；100MB×10 轮转在 ~9 分钟内翻完）→ 修复后 ~12MB/h，保留期 **~9 分钟 → ~3.5 天**。现有全链路日志（survival_attempt_start / upstream_call_starting / upstream_http_attempt / executor: stream interrupted / request_survival_finished / audit: request completed）得以留存，满足"完整流程可事后恢复"。
2. **minimax 泄漏（P0）** ①`NewXMLToolCallCoercingBody`（tool_call_xml.go）：行变换 Reader 包装 `resp.Body`，在 cmd/gateway/main.go 的 `OpenAIToAnthropicStream`/`OpenAIToResponsesStream` 闭包接线（类型签名加 `toolsRequested`）——桥接链路与直通链路同权纠偏；②coercer 跨 delta 硬化：标记**前缀尾部**检测（`endsWithMarkerPrefix`，≥4 字节）缓冲跨 delta 拆分；不能成为工具调用的缓冲**回灌为正文**（不再吞字）；③`UnwrapMiniMaxTokenWrappers` 相邻非空载荷以空格拼接（wrapper 边界=字段分隔，修复 "lsCheck" 粘连）。
3. **resume_blocked 信封 retryable=true（策略修正）** survival_wiring.go `renderSurvivalTerminal`：resume_blocked 仅由可恢复 kind 触发（已核实 action_policy.go:310 全部产生点），网关侧不可透明续传 ≠ 客户端不可整轮重试；ZCode 等客户端自有的"丢弃部分输出重发"机制由此激活。fail_terminal 保持 false。
4. **瞬态终态信封补 retryable** ①handler.go `writePrewarmedStreamErrorFull`：Exhausted 瞬态类（network/upstream_down/overloaded/concurrent/纯 no_candidates）携带 `retryable`（对齐非 prewarmed 分支的 `EffectiveRetryable`）；rate_limit 附 `retry_after`；②stream.go §11.6 `eof_without_done` 帧 + `stream_timeout` 帧补 `reason`/`retryable:true`；③anthropic_stream.go timeout/read-error 帧、anthropic_bridge.go `emitAnthropicBridgeErrorChunk`（stream_read_error/stream_chunk_timeout=true）同步补齐——覆盖 #6/#7/#14/#15/#17 的客户端后续动作。
5. **survival 终态落库 + 双终态帧防护（设计文档 §四.2 挂账项）** survival_wiring.go 新增 `survivalTerminalError`（action/reason/kinds/cause，Unwrap 保 errors.Is/As 链）：handler 在通用 fallthrough 前拦截，`failure_detail_code=error_kind="gateway_survival_<action>"` 落 request_logs（此前为空/泛化 provider_error），且不再叠加第二个错误帧；与 R58 `errSurvivalTerminalRendered` 哨兵链共存（terminalOnWire 时 cause 以 `%w (%w)` 挂哨兵，blackhole 守卫继续生效）；**ctx 取消路径不包裹**（保持既有取消分类，避免误标失败）。

## 三、验证

- 单测：`go test ./domains/streaming/... ./internal/vendorstrip/... ./admin/... ./cmd/gateway/... ./errorsx/...` 全绿；新增 `tool_call_coercing_body_test.go`（6 用例：泄漏原文纠偏/跨 delta 拆分/假阳性回灌/无 tools 直通/普通流字节保真/Close 传播）、`TestOpenAIToAnthropicBridgeWithCoercingBodyConvertsLeakToToolUse`（桥级集成）、`TestRenderSurvivalTerminalResumeBlockedIsClientRetryable`（3 协议）、`TestRenderSurvivalTerminalFailTerminalStaysNonRetryable`、§11.6 帧断言扩展、unwrap 字段分隔 3 用例；既有 R58 e2e `TestSurvivalCommittedBroken_NoSecondTerminalAfterDone` 合并后仍绿。
- 本地部署：deploy-local 蓝绿 2224→2226（`VERIFY_PASS=1`，凭据解密冒烟 587 providers/7 creds/0 failed）；E2E（临时系统 key，已回收）：非流式 minimax-m3 200 ✓；**anthropic /v1/messages 流式 + tools** 产出规范 `tool_use` 块 + `input_json_delta`，无 wrapper 泄漏 ✓；两条请求 request_logs_hot 落行 + 日志完整旅程（http_request→routing_resolve→candidates_resolved→upstream_call_starting→upstream_http_attempt→attempt_commit_gate→audit: request completed）✓；洪水线 7 分钟零新增（对照 hub 其它 INFO 持续在写）✓。

## 四、遗留（未在本轮修，供后续轮次）

- L2 对齐续传默认仍 off（设计 P0-P4 灰度路线不变，本修复 3 是其 L4 客户端侧兜底的信封层实现）。
- D5 模型级回退仍仅非流式 + 默认关（AUTO_ROUTE_FALLBACK_ENABLED）；流式 pre-commit 场景的模型回退需单独设计。
- #12 prompt_too_large 按设计不放宽（1M 上限）；客户端侧 7MB 压缩阈值/分段属客户端改进。
- minimax 泄漏的 loose 纠偏产出 `{"input": <raw>}` 形工具调用（name="tool"）——比泄漏结构化，但 agent 仍可能 "no such tool"；依赖厂商修复 m3 的工具通道。
- 245 中继 minimax-m3 凭据大面积 auth_failed/410（探针侧），属凭据运维项。

## 五、并发冲突记录

本会话工作期间另一会话在同一工作树推进 R58/D1（commits 95b22c817/764a95330/70e47c4f4）并触发 stash-pop 冲突（survival_wiring.go）；已按双语义合并：`survivalTerminalError`（本轮）+ `errSurvivalTerminalRendered`/`terminalOnWire`（R58）共存，R58 e2e 与本轮测试均绿。多写者同树操作再次印证需要 checkout 隔离。

## 六、批判式复审轮（2026-09-23 晚，用户指令：核查"只声明未实证"项）

复审方法：对上一轮每一项声明找"生产路径实证"，找不到的补证或修。

### 发现并修复的三个缺陷

- **F-1 anthropic 直通帧缺 code/retryable**：`finalizePassthroughInterruption`→`writePassthroughErrorEvent`（/v1/messages + anthropic 上游，#14/#15 的路径之一）只发 `{type,message}`。修复：`writePassthroughErrorEventFull` 携带 `code=stream_interrupted` + `retryable=EffectiveRetryable(kind)`；新增 2 个单测（含未提交不渲染断言）。
- **F-2 通用 fallthrough 硬编码 retryable=false**：handler 非 survival 已提交中断（streamInterruptedError）最终走 `writePrewarmedStreamError("upstream request failed","provider_error")` 无 retryable。修复：`ClassifyError` 推导 kind + `EffectiveRetryable`；**复审中发现并堵住自身回归**——`client_cancel` 文本分类为 transient→true，会误导对已取消请求重试双计费，`isClientInterruptError`（前缀精确匹配）强制 false。
- **F-3 双终态帧（mock E2E 实测发现）**：chat 路径已提交 EOF 时，§11.6 帧后 handler 的 Exhausted 分支又写一帧误导性 `model_not_found` "No available provider…"。修复：`errorsx.ErrProtocolTerminalRendered` 共享哨兵（executors 不能 import streaming，故放 errorsx；survival 哨兵 wrap 它）；`StreamOutcome.TerminalRendered`（两份别名结构体同步加字段）在 §11.6/timeout/anthropic 帧/passthrough finalize 全部渲染点置位；`streamInterruptedError.terminalRendered`→`forwardForDispatch` wrap 哨兵→handler `execTerminalRendered`（含 ExecuteError.LastErr 显式检查，ExecuteError 无 Unwrap）→ 既有 blackhole 守卫拦截第二帧。**E2E 复测：第二个信封消失**，仅剩 §11.6 帧+合成 [DONE]+parser-safe thinking 注释。

### 实证补齐（上一轮只声明未实证的）

- **minimax coercer 生产接线**：mock 上游（自写 SSE server，回放报告 #13 原文 payload，且在 `minimax[>[` 标记中间拆分跨 delta）+ DB 翻转 mock provider 34874/credential 70 绑定 + `/v1/messages` 流式+tools 经**已部署网关**：输出 0 处 `minimax[>[`、产出规范 `tool_use` 块。上一轮 E2E 用的是原生 tool_calls 路径，未证 coercer——本项补上。
- **§11.6 retryable 帧**：mock 截断（发出内容后不带 [DONE] 硬断开）→ `/v1/chat/completions` 实测返回 `"code":"eof_without_done","reason":"eof_without_done","retryable":true` + 合成 [DONE]。
- **survival 激活范围（如实更正）**：本地与 245 的 env 均无 SURVIVAL 配置（默认 false）→ 本地部署验证覆盖的是常开路径（coercer/§11.6/prewarmed/通用 fallthrough）。**154 canary 实证 survival 活跃**（日志 survival_attempt_start attempt=82，build 2211，不含修复）——用户报的 resume_blocked 类错误源于此类环境；**修复在 main 上，154 需重新部署才能生效**（本轮未部署 154）。
- 日志保留期 "~3.5 天"为估算（基于实测 ~12MB/h 低负载增速），随流量线性变化。

### 过程坑（复用价值）

- 直写 DB 翻转绑定后需 `docker restart llm-gateway-local-8782` 清内存候选缓存；截断 mock 会击穿探针→`auto_cool_high_failure_rate` 冷却→no_candidate，mock 必须"标记触发"（body 含 TRUNCATE 才截断，探针正常答）。
- 测试 key 不设 `rate_limit_rpm=0` 会回落 tier 默认 12 RPM（再次踩中）。
- 并发会话同树部署竞态：`gateway.build` 变 linux ELF + DL_DOCKER=0 时宿主 exec 报 Exec format error；重试即可。`psql_query` 在 `LLM_GATEWAY_DATABASE_URL` 未导出时 set -u 崩（另一会话未提交脚本改动相关）。
