# ADR: `gemini_response` 持久化字段的 `nil` 语义与历史兼容性

**项目**: llm-gateway-go（IR 协议转换层）
**日期**: 2026-08-16
**状态**: Accepted / Implemented

---

## 背景（Context）

`ToolResult.GeminiResponse`（`internal/ir/types.go:384`）是一个 `json.RawMessage`
字段，标签为 `json:"gemini_response,omitempty"`。它用于在同协议 Gemini → Gemini
中继链路中，保留上游 `functionResponse.response` 的原生 JSON 形状（对象 / 数组 /
标量 / 字面 `null`），避免被压成 `{"result": "<text>"}` 文本包装而丢失结构。

该字段在会话持久化往返（`domains/session/v2/ir_message_adapter.go` 通过
`json.Marshal` / `json.Unmarshal` 整轮往返 `ToolResult`）中会被序列化进会话行，
因此会话存储中会存在该列。

**需要明确的关键风险**：因为该字段带 `omitempty`，且旧会话行早于该字段的引入，
旧行反序列化时会得到 `nil`。如果未来维护者把"持久化中 `gemini_response` 为 `nil`
或字段缺失"误判为数据丢失 / 持久化缺陷，就可能错误地"修复"掉这个正确行为，
进而破坏历史会话的回放。本 ADR 正式记录其语义，避免此类误判。

---

## 决策（Decision）

### 1. `nil` 与显式 `null` 的区分语义

- **`nil`（含字段缺失）** = 上游线缆上 `functionResponse.response` 本身**缺失**。
  在序列化时回退到 `{"result": "<来自 Content 的文本>"}` 文本包装
  （见 `serialize_gemini.go:349-351` 的 `!= nil` 分支）。在会话持久化反序列化时，
  早于该字段的会话行会得到 `nil`，并**正确地**回退到文本——这是预期行为，不是数据丢失。
- **非 `nil` 字节（含字面量 `null` 的字节）** = 一个被**显式携带**的响应，序列化时
  原样写入 `functionResponse.response`。字面 `null` 与"缺失"语义上不同，必须区分。

解析侧仅在 `fr.Response != nil` 时写入该字段（`parse_gemini.go:239-241`），
从而与序列化侧的 `!= nil` 判定严格一致。

### 2. `omitempty` 标签的理由

标签 `json:"gemini_response,omitempty"` 是必要的：

- 语义层：`nil`（缺失）不应在线缆上产生 `"gemini_response": null` 噪声；
- 持久化层：`omitempty` 让旧行（无该列）自然反序列化为 `nil`，驱动上述文本回退；
- 隔离层：协议序列化器（`serialize_gemini` / `serialize_openai` /
  `serialize_anthropic`）全是手写 `map[string]any`，**不会**把 `gemini_response`
  键发射到协议线缆上。该字段只会出现在"整体 `ToolResult` 被 `json.Marshal`"
  的会话持久化路径，绝不会跨协议泄露。

### 3. 持久化往返保证

`ir_message_adapter.go` 通过 `json.Marshal` / `json.Unmarshal` 整轮往返 `ToolResult`，
正是 `json` 标签保证 `GeminiResponse` 在持久化往返中不丢失。缺少该标签时，
非 `nil` 的显式响应会在入库 / 出库时静默消失（这正是此前修复的核心）。

---

## 后果（Consequences）

- **向后兼容旧会话行**：早于该字段的会话行在反序列化时得到 `nil`，
  并正确回退到文本包装语义，`ToolResult.Content` 中始终保留文本块作为兜底，
  因此**没有任何历史数据丢失**。维护者遇到 `nil` / 字段缺失应视为预期。
- **跨协议隔离**：OpenAI / Anthropic 序列化器只读取 `Content` 文本，
  从不引用 `GeminiResponse`，故该字段不会污染跨协议转换结果。
- **显式 `null` 保真**：字面 `null` 以非 `nil` 字节形式穿越解析、IR、序列化、
  持久化全链路，与"缺失"严格区分。
- **可观测性**：审计或排障时，若持久化列中 `gemini_response` 为 `null` 或缺失，
  应结合"该会话行创建时间是否早于字段引入"判断；早于则为历史兼容性回退，无需告警。

---

## 参考（References）

- `internal/ir/types/...` → `internal/ir/types.go:384` — `ToolResult.GeminiResponse json.RawMessage` 字段定义与标签。
- `internal/ir/parse_gemini.go:239-241` — 解析侧仅当 `fr.Response != nil` 时写入 `GeminiResponse`。
- `internal/ir/serialize_gemini.go:344-358` — 请求侧序列化 `GeminiResponse != nil` 时原样作为 `response` 值，否则回退文本包装；该分支即结构化保真的请求侧实现。
- `domains/session/v2/ir_message_adapter.go` — 会话持久化适配器，经 `json.Marshal` / `json.Unmarshal` 整轮往返 `ToolResult`，`json` 标签保证字段存活。

---

## 附录：剩余任务的评估结论（任务 (1)、(3)）

本 ADR 仅覆盖任务 (2)。对交接文档中列出的另外两项评估性任务，结论如下，均**不需要**新增代码改动。

### 任务 (1)：OpenAI / Anthropic 是否需要同类原生结构化 tool result 保留

**结论：不需要。** 依据上游协议规范与网关现有 serializer 行为：

- **OpenAI Chat Completions** `tool` 消息的 `content` 在规范中**强制为字符串**（`tool_call_id` 配对）。不存在 object/array 结构化变体。网关 `parse_openai.go:252-265` 与 `serialize_openai.go:229-293` 的字符串处理即线规本身。
- **OpenAI Responses API** `function_call_output` 的 `output` 同为非结构化**字符串**（`serialize_responses.go:420-441`）。需要 JSON 时由调用方自行序列化进字符串，网关无需额外保留通道。
- **Anthropic Messages** `tool_result` 的 `content` 为 `string | array<content_block>`（数组元素为 `text`/`image`/`document` 等**类型化**块，而非裸 JSON 值）。网关 `parse_anthropic.go:334-364` 与 `serialize_anthropic.go:579-604` 已完整覆盖两种形状。

对比之下，Gemini 的 `functionResponse.response` 是**任意 JSON 值**（对象/数组/标量/null），`42`、`[1,2,3]` 等标量无法无损转为文本（类型会在序列化/反序列化中丢失），这正是 `GeminiResponse` 字段存在的理由。OpenAI/Anthropic 不暴露等价的原生结构化槽位，因此为其新增 `OpenAIResponse`/`AnthropicResponse json.RawMessage` 字段**没有上游映射目标**，只会在每次序列化时被丢弃或被迫发明非规范包装，徒增复杂度而不保留任何新增信息。现有 `Content` 文本路径对两者已是正确且充分的表示。

### 任务 (3)：流式 tool result 保真（核查 `response.go` 的 `SerializeGeminiResponse`）

**结论：不需要代码改动，现有实现正确。**

- `SerializeGeminiResponse`（`response.go:914`）与流式 `StreamChunk.SerializeGemini`（`stream.go:1092`）均生成**模型 → 客户端**的输出。按 Gemini API 契约，流式/完整响应中只含 `functionCall` 部件（模型请求调用工具），**绝不**含 `functionResponse`。`functionResponse` 始终是**客户端 → 服务端请求**数据。
- 结构化 `functionResponse` 保真已在**请求侧**完整实现并测试：`parse_gemini.go:227-247` 将原始 `fr.Response` 存入 `GeminiResponse`，`serialize_gemini.go:344-358` 在同协议序列化时原样回写。覆盖测试：`TestGeminiFunctionResponseStructuredRoundTrip`、`TestSerializeGemini_FunctionResponseLegacyTextFallback`。
- `geminiStreamWriter`（`handler_gemini.go:91`）将上游 OpenAI SSE 解析为 `StreamChunk` 后调用 `SerializeGemini`；OpenAI 从不流式回传 tool result（only 模型增量），故流式序列化器本就不存在 functionResponse 路径。

综上，流式响应路径无 structured-tool-result 保真缺口，与任务 (1) 根因相同——工具结果路径上没有任何信息正在被降级。
