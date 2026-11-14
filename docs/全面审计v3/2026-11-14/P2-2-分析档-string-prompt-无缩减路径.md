# P2-2 分析档：string prompt/input 无缩减路径（2026-11-14 取证完成）

## 一、问题确认

B 组跨轮复审发现：`/v1/completions` 与 `/v1/responses` 的 string 形态巨型 prompt/input（实测 1.2MB）会导致 LargeIngress 422「cannot produce a valid bounded context」。这两个端点的 string 形态**无缩减路径**，即使内容可压缩（历史对话文本），也会因形态不匹配而被拒绝。

## 二、架构取证

### 入口确认
`domains/streaming/large_ingress.go:58 generationPath` 包含：
```go
case "/v1/chat/completions", "/chat/completions", "/v1/completions", "/completions", 
     "/v1/messages", "/messages", "/v1/responses", "/responses":
    return true
```
⇒ `/v1/completions` 与 `/v1/responses` **都在 LargeIngress 管辖内**。

### 缩减路径现状（两个函数的形态要求）

**internal/sessioncontext/file.go:48-54 Reduce 函数**：
```go
for _, k := range []string{"messages", "input", "contents"} {
    if a, ok := doc.Arrays[k]; ok {
        key, spans = k, a
        break
    }
}
if len(spans) == 0 {
    return nil, report, ErrProtectedContext
}
```
⇒ 要求 `messages`/`input`/`contents` 必须是**数组**（`doc.Arrays[k]`），string 形态直接 `ErrProtectedContext`。

**internal/sessioncontext/fit.go:88-95 messages 辅助函数**（被 Fit 调用）：
```go
for _, key := range []string{"messages", "input", "contents"} {
    raw, ok := root[key]
    if !ok {
        continue
    }
    var msgs []json.RawMessage
    if json.Unmarshal(raw, &msgs) != nil || len(msgs) == 0 {
        return "", nil, ErrProtectedContext
    }
    return key, msgs, nil
}
```
⇒ 同样要求数组，`json.Unmarshal(raw, &msgs)` 对 string 必失败。

### 两个端点的合法 string 形态

**OpenAI Completions API** (`/v1/completions`)：
```json
{
  "model": "gpt-3.5-turbo-instruct",
  "prompt": "Once upon a time",  // ← string 形态合法
  "max_tokens": 100
}
```
- `prompt` 字段可以是 string 或 string 数组（legacy completion 端点）。

**OpenAI Responses API** (`/v1/responses`)：
```json
{
  "model": "gpt-5",
  "input": "Summarize this text...",  // ← string 形态合法
  "max_output_tokens": 1024
}
```
- `input` 字段可以是 string 或 item 数组（2026-11-12 IR 主路径迁移后的新形态）。

## 三、根本矛盾

- **协议规范**：两个端点的 string 形态是合法的、客户端 SDK 支持的正常用法。
- **网关实现**：LargeIngress + sessioncontext 的缩减路径只认数组形态，string 形态被 `ErrProtectedContext` 拒绝 ⇒ 1.2MB 的纯文本 prompt 必 422。
- **实际影响面**：prompt/input 超 1MB 的场景（例如「总结整本书的文本」）在 string 形态下被砖化，而同样内容的数组形态（`{"messages":[{"role":"user","content":"..."}]}`）可以正常缩减。

## 四、可行修复路径（设计阶段）

### 路径 A：数组包装（最小改动）
在 `file.go:Reduce` 与 `fit.go:messages` 检测到 string 形态时，自动包装为单元素数组：
```go
// file.go Reduce 内，检查 key 循环后：
if len(spans) == 0 {
    // 尝试 string 形态回退
    for _, k := range []string{"prompt", "input"} {
        if field, ok := doc.Fields[k]; ok {
            // 读 string 内容，包装成单条 user message
            spans = []largebody.Span{field}
            key = "messages"  // 标准化为 messages 数组处理
            break
        }
    }
}
```
- **优点**：改动集中在两个函数；string→数组包装后可复用现有缩减逻辑（excerpt/summary）。
- **缺点**：`prompt` 字段不在 `generationPath` 的通用键集合里（/v1/completions 特有），需确认 IR 层是否已统一。

### 路径 B：plainText 直接摘要（无结构依赖）
对 string 形态调用 `plainText` + `sampleText` 生成摘要，不走 message 结构：
```go
if stringValue, ok := root["input"].(string); ok && len(stringValue) > maxBytes {
    summary := sampleText(stringValue, "", maxBytes-overhead)
    root["input"] = summary
    // ...
}
```
- **优点**：不依赖数组结构，逻辑最简单。
- **缺点**：丢失了 message-level 的 protected 判定（system/tool chain 保护）；completions 的 `prompt` 字段需单独处理。

### 路径 C：协议适配层（IR 统一化）
在 IR 层（`parse_openai.go` / `parse_responses.go`）把 string 形态统一转成内部数组表示：
```go
// parse_responses.go ParseResponses 内：
if jsonKind(src.Input) == jsonString {
    var s string
    json.Unmarshal(src.Input, &s)
    req.Messages = []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: s}}}}
} else {
    // 现有数组解析逻辑
}
```
- **优点**：下游（sessioncontext）无感知，保持单一数据模型；completions 的 `prompt` 可在 `parse_openai.go` 同形处理。
- **缺点**：IR 层改动，影响面较大；需确认 serialize 时是否要还原回 string 形态（取决于目标上游是否支持 string）。

## 五、推荐方案（待裁决）

**路径 C（IR 统一化）+ 路径 A 的包装思路**结合：
1. **IR 层标准化**：`ParseResponses` / `ParseOpenAI` 对 string input/prompt 自动包装为单条 user message ⇒ `InternalRequest.Messages` 始终是数组。
2. **sessioncontext 保持现状**：`file.go` 和 `fit.go` 继续要求数组（不增加 string 分支），由 IR 层保证输入已标准化。
3. **serialize 层适配**：`SerializeResponses` / `SerializeOpenAI` 检查 `len(req.Messages)==1 && sourceWasString` 时可选地还原回 string 形态（若目标上游偏好 string）。

### 理由
- **关注点分离**：IR 是协议适配层，负责「外部多样性→内部统一」；sessioncontext 是缩减层，只处理统一数据模型。
- **复用现有逻辑**：string 包装后走现有 message 缩减路径（protected 判定、excerpt、summary 全复用）。
- **扩展性**：未来若有其他端点引入 string 形态（如 Gemini generateContent 的 prompt），只需在 parse 层统一处理。

## 六、实施前置（待核实）

1. **IR 层是否已统一 prompt 字段**：确认 `/v1/completions` 的 `prompt` 是否在 `parse_openai.go` 已映射为 `InternalRequest` 的某个字段（`Messages` 或 `Prompt` 专用字段）。
2. **目标上游对 string 的依赖**：确认是否有上游**只认 string 不认数组**（若有，serialize 层必须还原）。
3. **IR 数组化的副作用**：确认 `input: "string"` 包装后是否会触发其他逻辑分支（如 tool chain 判定、modality 分类）。

## 七、变通方案（若暂不改架构）

**文档明确限制**：在 `/v1/completions` 与 `/v1/responses` 的 API 文档里标注「string 形态的 prompt/input 字段不支持超过 1MB；超大输入请使用数组形态（messages）」。客户端 SDK 在检测到超大 string 时自动包装（SDK 侧修复）。

⇒ 此方案不修网关代码，但对用户体验不友好（需主动规避）。
