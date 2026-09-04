# 审计报告 · Axis A：IR 核心数据结构清晰度 + 多协议转换完整性闭环

- 仓库：`/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5`
- 日期：2026-09-05（覆盖过去 24h 提交：`034c94ee7` dispatch race 修复、`7a05ae4ad` IR fuzz smoke + Gemini 配置回归、`3970a7de7` main.go 清理、`b76f9f2d4` Responses 请求形状校验加固、`035df5f74` dual-mode storage）
- 审计范围：`internal/ir/`（types/stream/4 个 parse_*/4 个 serialize_*/3 个流解析器）与 `domains/session/v2/ir_attachment_adapter.go`
- 方法：全部结论先读代码，关键丢失路径均以 `go test -overlay` 外挂用例实证复现（未修改仓库任何文件）；`go test ./internal/ir -run 'TestGeminiSafety|TestStreamChunk_Responses'` 通过。

## 一、发现清单（按严重度排序）

| # | 级别 | 位置 | 问题 | 证据 | 引入 | 建议修复 |
|---|------|------|------|------|------|----------|
| 1 | **P0** | `internal/ir/parse_anthropic.go:471-482`、`parse_anthropic.go:495-505`、`serialize_anthropic.go:514-546`、`serialize_anthropic.go:625-646` | Anthropic Files API `source:{type:"file", file_id}` 的**图片与文档全链路丢失**：parse 只读 type/media_type/url/data，不读 file_id；serialize 对 `type:"file"` 落入 default/base64/url 分支，输出残缺 `{"source":{"type":"file"}}`（无 file_id），上游 Anthropic 必 400，附件语义完全丢失。IR 类型本身已有 `ImageSource.FileID`（types.go:365），仅 parser/serializer 未接线 | 实证复现：输入 `{"type":"image","source":{"type":"file","file_id":"file_abc123"}}` → Parse 后 `Image.Type="file", FileID=""` → SerializeAnthropic 输出 `{"source":{"type":"file"},"type":"image"}`；document 同样 | 存量 | parseAnthropicImageBlock / parseAnthropicDocumentBlock 读取 `source.file_id` 存入 FileID/Data；serialize 增加 `case "file": source["file_id"]` 分支；补 roundtrip 测试 |
| 2 | **P1** | `internal/ir/serialize_anthropic.go:393-411`、`serialize_anthropic.go:566-580` | **tool_result 内多模态内容被清空**：role=tool 路径与 tool_result 块序列化只保留 `cb.Type=="text"`，image/document 静默丢弃。Parse 侧明明支持任意块（parse_anthropic.go:346-358），Anthropic→IR→Anthropic roundtrip 后 `content:[]` | 实证复现：tool_result 携带 base64 image → 输出 `"content":[]` | 存量 | 序列化 tool_result 时递归调用 serializeAnthropicContentBlock 保留多模态块，或在丢弃时 ReportProtocolLoss |
| 3 | **P1** | `internal/ir/parse_gemini.go:53-56`、`parse_gemini.go:455-466`、`types.go:563-577` | **Gemini generationConfig 子字段静默丢失**：generationConfig 是顶层已知字段不进 Extensions，子字段只解析固定集合；`responseLogprobs`、`logprobs` 等既不解析也无 loss 事件。且 `GenerationConfig` 类型已声明 `PresencePenalty/FrequencyPenalty`（types.go:573-574）但 parseGeminiGenerationConfig 结构体漏了这两个字段——解析与类型定义脱节 | 实证复现：`generationConfig:{temperature:0.5, responseLogprobs:true}` → 序列化输出只剩 `{"temperature":0.5}`，`Extensions` 中无 generationConfig | 存量 | parseGeminiGenerationConfig 补齐 presencePenalty/frequencyPenalty；未知 generationConfig 子字段收进 IR 级 geminiGenerationConfigExtra 或上报 loss |
| 4 | **P1** | `internal/ir/parse_gemini.go:142-151` | **Gemini `executableCode` / `codeExecutionResult` parts 整体丢弃**：parts 结构体只认 text/inlineData/fileData/functionCall/functionResponse/thought 六种，code-execution 工具回路（Gemini 原生代码执行）的 model 轮次解析后 blocks=0，序列化后整个轮次从 contents 消失 | 实证复现：3 条 contents（含 executableCode 与 codeExecutionResult）→ 解析后 msg[1]/msg[2] blocks=0，输出只剩第一条 user 文本 | 存量 | 按 RawContent 捕获未知 part（对齐 ContentBlock.RawContent 模式），至少上报 ReportUnknownField |
| 5 | **P2** | `internal/ir/stream.go:541-544`（解析）、`stream.go:784-930` / `stream.go:963-1108`（序列化无分支） | **流式 signature_delta 只有解析没有序列化**：`ParseAnthropicStreamEvent` 把 signature 写入 `Delta.ThinkingSignature`，但 `SerializeAnthropic`/`SerializeOpenAI`/`SerializeResponses` 三个流序列化器均无输出分支——signature-only chunk 序列化为空串。主 Anthropic 客户端路径（anthropic_bridge.go:836）只把它用于 emitted 判定不依赖序列化，因此当前多为潜在风险；但 `response_format_adapter.go:322` 一旦启用该通用路径（当前仅测试引用，处于休眠），Anthropic 客户端将收不到签名，下一轮 thinking 块校验失败 | stream.go:91-94 注释声称"downstream serializer can preserve it"，实际无任何 serializer 读该字段 | 存量 | SerializeAnthropic 增加 signature_delta 输出（index 需随 chunk 传递，注意 StreamChunk 目前不带 block index）；短期在 response_format_adapter 启用前必须先补 |
| 6 | **P2** | `internal/ir/parse_openai.go:154-158`、`serialize_openai.go`（max_tokens 回写） | **`max_completion_tokens` 被折叠进 `MaxTokens` 并以 `max_tokens` 回写**：o 系列推理模型语义不同（max_completion_tokens 含 reasoning tokens；o3/o4 明确拒收 `max_tokens`），OpenAI→IR→OpenAI 请求参数被静默改写 | 实证复现：入参 `max_completion_tokens:1234` → 序列化输出 `"max_tokens":1234` | 存量 | IR 增加 `MaxCompletionTokens` 显式字段（或 Extensions 记忆原名），OpenAI 序列化时优先还原 max_completion_tokens |
| 7 | **P2** | `internal/ir/fuzz_parsers_test.go:46-54` | **本次 24h 新增 fuzz 覆盖面偏窄**：`FuzzIRParsersNeverPanic` 成功 parse 后只验证 `EncodeRequestDocument` 可编码，不执行 Serialize{OpenAI,Anthropic,Gemini,Responses}，也不覆盖响应方向解析（response.go/Parse*Response）；序列化层 panic 或产出非法 JSON 不会被 smoke 捕获。断言本身"安全即可"的定位合理，但与"IR fuzz smoke"目标相比有缺口 | fuzz_parsers_test.go:48-54 只有 EncodeRequestDocument 断言 | **本次 24h 引入**（7a05ae4ad） | smoke 中对成功 parse 的 IR 依次跑 4 个协议 Serialize + json.Valid；后续把响应方向解析器纳入第二轮 fuzz |
| 8 | **P2** | `internal/ir/types.go:509-513`、`types.go:196-204` | **网关侧元数据无显式 IR 承载**（详见对照表）：project/task 类型/轮次/总轮次/日期时间/多 tag/凭据/路由来源在 IR 中没有字段。Metadata 只有 `UserID/RequestID/Other map[string]string`（扁平字符串，无法表达 tag 列表，且仓库内无写入方）；IR 仅有的网关注记是 Class/DueAt；凭据与瀑布调度数据在 dispatch 层（`dispatch.QueuedRequest.SelectedCred`、`ResolvedModelSnapshot`，queued_request.go:21/116/269），IR 不可见 | grep `TaskType|ProjectID|TurnNumber|TotalTurns` 在 internal/ir、domains/transformation 零命中 | 存量 | 若产品要求"IR 承载会话元数据"，需扩展 Metadata（如 ProjectID/TurnIndex/TotalTurns/Tags []string，`json:"-"` 防止外泄到上游）；否则在文档中明确这些数据的归属层，避免"IR 是全量载体"的误解 |
| 9 | **P2** | `internal/ir/parse_gemini_stream.go:63-68`、`203-205` | **Gemini 流解析的 part 覆盖不全**：流式 parts 结构体无 `fileData`（静默忽略）；非 audio 的 inlineData 直接返回 error 丢弃整 chunk（不 panic，但该 chunk 全部内容连带丢失） | parse_gemini_stream.go:67 无 FileData 字段；204 行 `return nil, fmt.Errorf(... "not representable as audio")` | 存量 | fileData part 记录为可忽略但打点；非 audio inlineData 改为跳过该 part 并上报而非整 chunk 失败 |
| 10 | **P3** | `internal/ir/serialize_responses_stream_test.go:13-38` vs `40-108` | **「新增事件类型必须同步测试」约束当前未被违反，但注释与用例已漂移**：文档注释把 `response.audio.delta`、`response.audio_transcript.delta` 列为 "Pinned event types"，用例表却没有 audio 用例；同时 `StreamDelta.ThinkingSignature` 是已知 IR 字段却被 SerializeResponses 静默吞掉，未列入 22-38 行的 documented-loss 表。24h 内无新增事件类型（该文件最后改动 94a0221eb 2026-08-31），约束"当前遵守"但约束机制本身不闭合 | serialize_responses_stream_test.go:18-19（声称 pinned）与 40-108（无 audio case） | 存量 | 补 audio 两个用例；把 signature_delta 明确写入 documented-loss 表或实现输出 |
| 11 | **P3** | `internal/ir/parse_responses.go:323-337` vs `410-427` | **死代码**：`function_call_output` 在 parseResponsesInputItem 顶部已提前 return，函数尾部 410-427 的第二个 function_call_output 分支永不可达，误导后续维护 | 两段重复的 `itemType == "function_call_output"` 处理 | 存量 | 删除尾部死分支 |
| 12 | **P3（信息澄清）** | `domains/session/v2/ir_attachment_adapter.go`（全文） | **审计输入前提修正**：该文件 24h 内并无改动（最后提交 0954290b6 2026-08-08；24h 内 session/v2 改动是 cache_v2 双模存储 035df5f74）。文件仅做 AttachmentMetadata→AttachmentRef 映射与类型统计，**不接触 base64 payload**，无大文件内存/溢出风险；真正承载 IR payload 的是 ir_message_adapter.go（ContentBlock 各字段以 json.RawMessage 整体搬运，无逐字段截断）。附件重放的大文件风险应在 attachments 存储域另行评估，不在本文件 | git log 该文件最近 3 次提交均早于 24h 窗口；ir_attachment_adapter.go:29-52 仅字段拷贝 | — | 无需修复；后续审计请把"附件提取"指向 ir_message_adapter.go 与 domains/attachments |

## 二、IR 字段覆盖对照表（审计任务第 1 项）

判定口径：显式字段=已覆盖；塞在 Extensions/Other 等兜底=部分覆盖；无承载=缺失。

| 需求项 | IR 承载位置 | 判定 |
|---|---|---|
| 多轮对话·消息历史 | `InternalRequest.Messages []Message`（types.go:45） | 已覆盖 |
| 多轮对话·轮次编号 | Message 无 turn 序号（`ContentBlock.Index` 只是 Anthropic 块序号，types.go:288） | 缺失 |
| 路由·模型 | `Model`（types.go:41） | 已覆盖 |
| 路由·供应商 | `TargetProvider`（types.go:194）+ `SourceProtocol`（types.go:175） | 已覆盖 |
| 路由·凭据 | 不在 IR；在 `dispatch.CredentialRef` / `QueuedRequest.SelectedCred`（dispatch/queued_request.go:21/116） | 缺失（归属 dispatch 层，符合分层但 IR 不可见） |
| 路由·路由来源 | 无任何字段 | 缺失 |
| 流程跟踪·阶段 | `Class`（immediate/scheduled，types.go:201，`json:"-"` 不外泄） | 部分覆盖 |
| 流程跟踪·瀑布调度 | 无；在 `dispatch/waterfall.go`（buildWaterfallRequest） | 缺失 |
| 压缩脱敏数据 | 无显式字段；`sanitize_tool_messages.go` 只做即时清洗不留存 | 缺失 |
| 附件·图片 | `ContentBlock.Image/ImageSource`：url/base64/file_id/file_uri（types.go:357-367） | 已覆盖（但 Anthropic parser 未接 file_id，见发现 #1） |
| 附件·PDF/文档 | `DocumentBlock/DocumentSource`（types.go:330-348）+ `SystemPrompt.PDFs`（types.go:211） | 已覆盖（Anthropic file source 未接；`DocumentSource.Data` 同时装 base64 与 file_id，字段过载属清晰度债） |
| 附件·音频 | `Audio *MediaSource` + `InputAudioBlock`（types.go:259/270/351） | 已覆盖 |
| 附件·视频 | `Video *MediaSource`（types.go:262） | 已覆盖 |
| 元数据·用户 | `User`（types.go:98）+ `Metadata.UserID`（types.go:510），Anthropic metadata.user_id 归一化 | 已覆盖 |
| 元数据·日期时间 | 无 | 缺失 |
| 元数据·项目 | 无 | 缺失 |
| 元数据·任务类型 | 无（`Class` 只区分 immediate/scheduled） | 缺失 |
| 元数据·轮次 / 总轮次 | 无 | 缺失 |
| 元数据·多个 tag | `Metadata.Other map[string]string`（types.go:512）——扁平字符串、无写入方 | 部分覆盖（兜底字段，按口径不算显式） |
| 元数据·模型 / 供应商 | `Model` / `TargetProvider` | 已覆盖 |
| 元数据·凭据 | 无 | 缺失 |

小结：**协议面字段（消息/工具/采样/多模态/各厂商专有）覆盖是这套 IR 的强项**（30+ 显式字段 + Extensions 前向兼容 + paramreg 按方言还原，extensions_restore.go:47-119 的按字段判定设计合理）；**会话/业务元数据（project/task/turn/tag/凭据/路由来源）基本没有显式承载**，若需求文档把 IR 定位为"全量数据载体"，这是最大的清晰度缺口。

## 三、分项结论

### 任务 2（传输/转换完整性抽查）
4 个请求解析器 + 4 个序列化器通读：Extensions 兜底机制对**顶层未知字段**覆盖良好；丢失集中在三个形态——(a) 已列入 knownFields 但未接线的字段（发现 #3，与 2026-08-11 已修复的 safetySettings 同型）；(b) 嵌套层的未知键不进 Extensions（generationConfig 子字段、message 级未知键、Gemini 未知 part，发现 #3/#4/#9）；(c) 解析支持但序列化裁剪（发现 #1/#2/#5/#6）。24h 新增的 gemini_safety_roundtrip_test.go 质量良好（覆盖 method 字段与序列化输出），fuzz_parsers_test.go 定位是"不 panic"合理但见发现 #7 的覆盖缺口。

### 任务 3（流式与非流式）
流式用独立的 `StreamChunk/StreamDelta/StreamToolCallDelta/StreamUsage`（stream.go:21-141），与非流式 `Message/ContentBlock/ResponseUsage` 是平行结构、不共用；文本/思考/工具调用/音频/usage 字段两侧目前已对齐（audit-stream-multimodal 后），但双结构并行存在漂移风险（P3 记录）。`serialize_responses_stream_test` 的同步测试约束：当前无新增事件、未违反；注释与用例有漂移（发现 #10）。

### 任务 4（附件/文件解析）
`ir_attachment_adapter.go` 24h 无改动、不接触 payload、无内存风险（发现 #12）。

## 四、总体结论

本次 24h 的三个提交本身质量合格：dispatch race 修复（034c94ee7）锁模式与既有 modelMu/selectedCredMu 一致；7a05ae4ad 的 fuzz 与 Gemini 回归是真实有效的防线并通过；3970a7de7 清理无副作用。**但它们没有触及本次审计实证出的存量转换闭环缺口**，其中 P0 的 Anthropic Files API file_id 丢失（发现 #1）会让携带 Files 附件的请求经网关后必然 400，P1 的 tool_result 多模态清空（#2）与 Gemini code-execution 轮次消失（#4）属于同类"解析支持、序列化裁剪/解析白名单不全"的静默数据丢失，建议与 safetySettings 修复同模式（接线 + ReportProtocolLoss + roundtrip pin 测试）批量收口。IR 类型定义本身清晰、注释充分、分层（协议字段显式 + Extensions 兜底 + 网关注记 `json:"-"`）设计良好；主要债在于嵌套层无兜底机制、以及会话/业务元数据在 IR 层没有归宿。
