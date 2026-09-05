# 轴A（round2）：IR 定义与多协议转换完整性

- 审计范围与方法：通读 `internal/ir/` 全部 parse_*/serialize_*/types/stream/response/response_protocols/extensions_restore 与 `domains/transformation/ir_transport.go`、`domains/streaming/`（anthropic_bridge / responses_bridge / handler / handler_gemini / tool_call_xml / executor_chat），沿「client→IR→upstream→响应→client」逐字段核对；已排除 24h 报告与八项闭环已修复项，遗留项（A-#4/#5/#6/#7/#8/#9/#11）逐条复核现状。
- 基线：`bb93bf6fe..3c51c2e36`；`go build ./...` 通过。

## 发现列表

### A-#13 [P0] System.Parts 在 OpenAI/Responses 序列化方向被整体丢弃（默认 IR 转换路径丢失系统提示词）
- 证据：`internal/ir/serialize_openai.go:204-210`（仅 `req.System.Content != ""` 才前置 system 消息，全文件零处引用 `System.Parts`）；`internal/ir/serialize_responses.go:68-72`（instructions 同样只取 Content）。而解析端把 Anthropic 数组 system（`parse_anthropic.go:196-207`）与 Gemini systemInstruction（`parse_gemini.go:367-387`）都归一为 **Parts-only**（Content 为空）。生产主路径确认走此代码：IR 是默认转换路径（`cmd/gateway/main.go:1528-1534`，"IR is the default protocol-conversion path"），`executor_chat.go:1681→1733` 直接 `ParseAnthropic→SerializeOpenAI`；Gemini 原生客户端走 `handler_gemini.go:267-290`（ParseGemini→SerializeOpenAI）。`validate_and_fix.go` 只归一化 system 角色 messages，不触碰 System.Parts。
- 影响：Claude Code（system 为块数组）→ DeepSeek/GLM 等 OpenAI 系上游、以及 Gemini 原生客户端 → 任意上游，系统提示词**静默全丢**，无任何 loss 事件。属主链路静默数据丢失。
- 最小修复：SerializeOpenAI 在 Content=="" 时把 Parts 文本 join 输出为 system 消息（serialize_gemini.go:208-229 已有同型处理可参照）；SerializeResponsesRequest 同理拼接进 instructions；补 array-system roundtrip 测试。工作量 S。

### A-#14 [P1] ParseOpenAIStreamChunk 不识别 in-band error 帧，responses_bridge 的错误分支是死代码
- 证据：`internal/ir/stream.go:168-361` 从不设置 `ChunkTypeError`——`data: {"error":{...}}` 落到 358-361 变成空 Delta chunk；`ParseAnthropicStreamEvent` 则会（stream.go:607）。`domains/streaming/responses_bridge.go:1073`（OpenAI 上游→Responses 客户端）检查 `chunk.Type == ir.ChunkTypeError` 永假（694 行 Anthropic 侧是活的）；`handler_gemini.go:156-168` 把错误帧转成空 `{"candidates":[{"index":0}]}`。
- 影响：OpenAI 系上游流中错误帧被吞，Responses 客户端收到以 `response.completed` 正常收尾的空响应，failover 不触发；Gemini 客户端同样看不到错误。GLM `finish_reason=network_error/sensitive` 流式值落到 writeFinalEvents 的 completed 分支同理。
- 最小修复：ParseOpenAIStreamChunk 识别顶层 `error` 对象 → ChunkTypeError+StreamError；writeFinalEvents 对错误类 finish_reason 降级为 error/incomplete。工作量 S。

### A-#15 [P2] A-#5 复核确认未修：流式 signature_delta 只解析不序列化
- `stream.go:538-544` 解析进 `Delta.ThinkingSignature`；四个流序列化器 SerializeOpenAI(623)/SerializeAnthropic(784)/SerializeResponses(963)/SerializeGemini(1150) 均无输出分支；stream.go:91-94 "downstream serializer can preserve it" 注释与实现不符。现仅 `anthropic_bridge.go:838`（emitted 判定）与 `:1177`（digest 审计）消费。`response_format_adapter.go` 仍无生产调用方（休眠），启用前必须先修。S。

### A-#16 [P2] A-#6 复核确认未修：max_completion_tokens 折叠进 MaxTokens 以 max_tokens 回写
- `parse_openai.go:154-158` 折叠；`serialize_openai.go:18-21` 恒输出 `max_tokens`；`max_completion_tokens` 已列 knownFields（parse_openai.go:65）故 Extensions 也不救。o 系列上游拒收 max_tokens 且语义不同（含 reasoning tokens），OpenAI→OpenAI 同协议请求被静默改写。修复需 IR 显式 `MaxCompletionTokens` 或 Extensions 记忆原名。S。

### A-#17 [P2] redacted_thinking 字段名错位 + 响应方向整体丢弃
- 请求方向 parse 读 `thinking` 键（`parse_anthropic.go:378-381,461-464`）、serialize 输出 `thinking` 键（`serialize_anthropic.go:641-643`）；真实 Anthropic 线格式为 `{"type":"redacted_thinking","data":"..."}`——内部自洽但对真实客户端是静默丢内容（RedactedThinking 恒空）。响应方向 `response.go:185-231` ParseAnthropicResponse 无 redacted_thinking case，上游返回的 redacted 块整体消失（buildAnthropicResponseContent 同样无输出分支），下一轮 thinking 链校验可能 400。S-M。

### A-#18 [P2] file/file_id 修复未覆盖的三个同类缺口（本轮 P0 修复的邻接面）
- a) 顶层 `documents`：`parse_anthropic.go:646-651` 不读 file_id、`serialize_anthropic.go:839-868` 不输出 → `{"source":{"type":"file"}}` 残缺（与已修 P0 同型）。
- b) 双轨编码冲突：OpenAI 解析把 file_id 放 `DocumentSource.Data`（`parse_openai.go:783-786`），Anthropic 解析放 `DocumentSource.FileID`（types.go:506-507）；`serialize_anthropic.go:654-660` 只认 FileID/Type=="file" → OpenAI file 文档路由 Anthropic 上游输出空源 `{"type":"file_id"}`。
- c) `ImageSource.FileID` 仅 Anthropic/Gemini 序列化器消费；`serialize_openai.go:355-379` 与 `buildResponsesInputImage`（serialize_responses.go:324-343）对 file_id 图片输出 `image_url:""` 空串——而 Responses parse 明确支持 input_image.file_id（parse_responses.go:506-509），**同协议 roundtrip 即损坏**。
- 修复：DocumentSource 统一走 FileID 字段；OpenAI/Responses 序列化器对 file_id 图片跳过或 ReportProtocolLoss，不输出空 URL。M。

### A-#19 [P2] 同族静默丢失/降级规则不明确（逐项）
- a) `extractSystemPrompt`（parse_openai.go:613-637）多块 system 只留第一块文本；第二条及以后 system 消息留在 Messages——Anthropic 目标原样输出 role:"system"（上游 400，serialize_anthropic.go:420-421），Gemini 目标静默 continue（serialize_gemini.go:243-245）。
- b) Gemini generationConfig：`responseMimeType != "application/json"` 静默丢（parse_gemini.go:515-523）；`responseLogprobs/logprobs` 被声明 known（497-505）但不解析不序列化、无 loss 事件。
- c) Anthropic thinking 块在 OpenAI/Responses 请求序列化中整体静默丢弃（serialize_openai.go:346-431 无 case；serialize_responses.go:303-307 注释性跳过）——signature 有 loss 事件，thinking 文本本体无。
- 修复：逐项补 ReportProtocolLoss 或在降级表中固化说明。M。

### A-#20 [P2] tool_result→Gemini 的 functionResponse.name 取自 tool_use_id 而非函数名
- `serialize_gemini.go:345-359` + `toolUseNameFromID`（583-611，只剥 `gemini_call_` 前缀）：Anthropic/OpenAI 客户端的工具回传（`toolu_...`/`call_...`）路由 Gemini 上游时 name=ID，语义错误、可能被上游拒。应跨消息查 tool_use/tool_calls 建立 ID→name 映射。M。

### A-#21 [P3] ir_transport 流式路径两处缺陷（当前 dormant：TransportFactory 仅测试引用）
- `ir_transport.go:551-556` 对 anthropic-messages 客户端把 `SerializeAnthropic` 的完整 `event:...\ndata:...\n\n` 输出再包一层 `data: %s\n\n`（双重包装成非法 SSE）；`:599-612` 无 gemini 客户端分支且 unsupported 协议静默返回空串（客户端收空 200 流）。接线生产前必须修；unsupported 分支应显式报错。S。

### A-#22 [P3] A-#8 维持：业务元数据在 IR 层无归宿
- `types.go:510-515` Metadata 仅 UserID/RequestID/Other（map[string]string，无生产写入方）；project/task/轮次/总轮次/多 tag/凭据归属/路由来源/日期时间均无字段（凭据与瀑布已在 dispatch.QueuedRequest/ResolvedModelSnapshot 承载）。建议 Metadata 以 `json:"-"` 扩展（ProjectID/TurnIndex/TotalTurns/Tags）或出分层归属文档，二选一。S。

### A-#23 [P3] 前轮遗留复核 + 响应方向新缺口
- A-#4 未修：`parse_gemini.go:142-152` parts 白名单无 executableCode/codeExecutionResult、无 Raw 兜底，代码执行轮次整体消失。
- A-#9 未修：`parse_gemini_stream.go:63-68` 无 fileData（静默忽略）；`:203-205` 非 audio inlineData 报错丢整 chunk。
- A-#7 未修：fuzz 仍只断言 EncodeRequestDocument（fuzz_parsers_test.go:47-54），4 协议 Serialize 与响应方向解析未入 fuzz。
- A-#11 未修：`parse_responses.go:410-427` 死分支仍在。
- 新发现：`response_protocols.go:14-31` ParseGeminiResponse parts 无 inlineData/fileData → Gemini 图像输出响应内容整体为空（非流式）。

## 本轮新修复质量复核

- **Anthropic file_id source**：message 级 image/document parse+serialize 已对称（parse_anthropic.go:479-480,502-503；serialize_anthropic.go:558-576,654-660），配套 `anthropic_file_source_test.go` — ✅ 修复成立；遗留见 A-#18（documents 顶层与 OpenAI 双轨编码）。
- **tool_result 多模态保留**：serialize_anthropic.go:606-624 递归保留非 text 块 + 单 text 简写契约 — ✅ 成立（role=tool 路径 395-409 仍 text-only，但 OpenAI tool 消息按规范即纯文本，可接受）。
- **Gemini 罚项**：parse_gemini.go:491-492 ↔ serialize_gemini.go:702-709 真正对称，`gemini_generation_config_roundtrip_test.go` 钉住 — ✅ 成立；candidateCount↔N 亦对称。
- **f0447666c candidates[0] 越界**：守卫正确（handler.go:4827-4840）；全仓扫描 `candidates[0]` 其余点位均有 len 守卫（handler.go:3162-3175/3285、responses.go:554-560、messages.go:568-574、providerprofile/adapters.go:74-78），同类索引越界未再发现 — ✅ 修复完整。

## 已确认闭环

- message 级 Anthropic image/document `source.type=file_id` 全链路（含回归测试）
- tool_result 多模态内容保留 + 单 text 字符串简写契约
- Gemini generationConfig presencePenalty/frequencyPenalty roundtrip + 未知子字段 ReportUnknownField
- f0447666c 空候选 panic 守卫及同类首元素索引扫描
- Extensions restore 注册表驱动覆盖全部四个请求序列化器（含 serialize_gemini 此前缺失处）
- 非流式响应按 client protocol 分派四协议全覆盖（ir_transport.go:687-700 + handler/messages/responses/handler_gemini）
- SSE ctx 可取消 body（newCtxCancellableBody）接入 anthropic_bridge 读循环（anthropic_bridge.go:887）
- 流式 audio delta 四协议序列化对称（stream.go:688-697/1029-1052/1218-1224）
- 流式 tool args 校验 quality 注解 `json:"-"` 不外泄 + anthropic_bridge content_block_stop 校验
- paramreg 方言裁剪含 ActionDrop loss 上报路径
- Responses input 方向 function_call/function_call_output 提前归一；file_data data-URI/text/file_id 同协议 roundtrip 对称
- `thinking:` SSE 注释 keepalive（handler.go:232）与 geminiStreamWriter 原样透传 SSE 注释帧（handler_gemini.go:150-155）
- Gemini 多候选折叠为 candidates[0] 为已文档化产品决策（handler_gemini.go:38-44 + 契约测试）

## 冗余/待清理代码

- `serialize_anthropic.go:359-365`：serializeAnthropicMessages 文档注释整块重复两遍
- `serialize_anthropic.go:985-997`：`mapEffortToBudget` 已标 Deprecated 且全仓零调用方（死代码）
- `parse_responses.go:410-427`：不可达的第二个 function_call_output 分支（前轮 A-#11）
- `parse_openai.go:295-299`：`msg["source"]` 特例为 no-op（`_ = source`）
- `serialize_openai.go:890-892`：`requestIDFromIR` 恒返回 "unknown" 的占位死接口
- joinTextParts 三胞胎：serialize_openai.go:325 / serialize_anthropic.go:441（joinTextPartsAnthropic）/ serialize_gemini.go:570（extractTextFromContent），逻辑近似可合并
- `domains/transformation/ir_transport.go` ConvertStream 路径生产无调用方（TransportFactory 仅测试引用），其 anthropic 双重包装/gemini 缺分支为休眠缺陷（A-#21）

## 统计

P0×1、P1×1、P2×6、P3×3（另复核确认前轮 A-#4/#5/#6/#7/#8/#9/#11 全部未修，已并入对应条目）。
