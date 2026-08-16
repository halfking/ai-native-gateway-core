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

- `internal/ir/types.go:384` — `ToolResult.GeminiResponse json.RawMessage` 字段定义与标签。
- `internal/ir/parse_gemini.go:239-241` — 解析侧仅当 `fr.Response != nil` 时写入 `GeminiResponse`。
- `internal/ir/serialize_gemini.go:349-351` — 序列化侧 `GeminiResponse != nil` 时原样作为 `response` 值，否则回退文本包装。
- `domains/session/v2/ir_message_adapter.go` — 会话持久化适配器，经 `json.Marshal` / `json.Unmarshal` 整轮往返 `ToolResult`，`json` 标签保证字段存活。
