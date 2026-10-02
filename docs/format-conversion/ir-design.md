# IR（Internal Representation）数据结构设计

## 设计原则

1. **中性**：IR 不偏向任何协议，所有协议字段都能映射到 IR 字段。
2. **可扩展**：新增字段不需要破坏已有 IR 结构。
3. **无损**：Parse 阶段保留所有原始信息（包括未知字段、reasoning、annotations）。
4. **流式兼容**：同一份 IR 既能表示完整响应，也能增量表示 chunk。

## 主要类型

### `InternalRequest`

```go
type InternalRequest struct {
    Model         string
    Messages      []Message
    System        string  // 顶层 system（Anthropic 风格）
    Instructions  string  // Responses API 顶层 system
    MaxTokens     int
    Temperature   float64
    TopP          float64
    TopK          int
    StopSequences []string
    Tools         []Tool
    ToolChoice    ToolChoice
    Stream        bool
    UserID        string
    
    // 厂商私有字段
    EnableSearch     bool
    SearchOptions    *SearchOptions
    Thinking         *ThinkingConfig
    ThinkingKeep     string
    ThinkingBudget   int
    DoSample         bool
    MaskSensitiveInfo bool
    BotSetting       []BotSetting
    SafePrompt       bool
    Prefix           bool
    SearchParameters *SearchParameters
    Route            string
    Models           []string
    Provider         *ProviderConfig
    Transforms       []string
    Plugins          []Plugin
    GuidedChoice     []string
    GuidedRegex      string
    GuidedGrammar    string
    RepetitionPenalty float64
    MinP             float64
    UseBeamSearch    bool
    LoraAdapter      string
    KeepAlive        string
    PreviousResponseID string
    ReasoningEffort  string
    ParallelToolCalls bool
    
    // 多模态
    ResponseFormat   *ResponseFormat
    
    // 元数据
    VendorExtensions map[string]any  // 未知字段兜底
}
```

### `Message`

```go
type Message struct {
    Role             string  // "user" | "assistant" | "tool" | "system"
    Content          []ContentBlock
    Name             string  // 部分厂商支持
    ToolCalls        []ToolCall  // assistant message
    ToolCallID       string  // tool message
    ReasoningContent string  // assistant message 推理
}

type ContentBlock struct {
    Type      string  // "text" | "image" | "image_url" | "file" | "input_text" | "output_text" | "input_image" | "input_file" | "audio" | "tool_use" | "tool_result" | "thinking" | "redacted_thinking"
    Text      string
    ImageURL  *ImageURL
    File      *File
    ToolUse   *ToolUse
    ToolResult *ToolResult
    Thinking  *Thinking
    Citations []Citation
}
```

### `Tool`

```go
type Tool struct {
    Type           string  // "function" | "web_search" | "file_search" | "code_interpreter"
    Name           string
    Description    string
    Parameters     map[string]any  // JSON Schema
    Strict         bool  // OpenAI 严格模式
}

type ToolChoice struct {
    Type         string  // "auto" | "none" | "required" | "any" | "function"
    FunctionName string  // 当 Type == "function"
}

type ToolCall struct {
    ID        string
    Type      string  // "function"
    Function  struct {
        Name      string
        Arguments map[string]any  // 已 parse 为 map
    }
}

type ToolResult struct {
    ToolUseID string
    Content   string
    IsError   bool
}
```

### `Thinking`

```go
type ThinkingConfig struct {
    Type         string  // "enabled" | "disabled"
    BudgetTokens int
    Keep         string  // "all" | "last" | "none"
}

type Thinking struct {
    Type      string  // "thinking" | "redacted_thinking"
    Thinking  string
    Signature string  // Anthropic signature 必须保留
}
```

### `InternalResponse`

```go
type InternalResponse struct {
    ID              string
    Object          string
    Created         int64
    Model           string
    Status          string  // Responses API
    StopReason      string  // Anthropic
    FinishReason    string  // OpenAI
    Content         []ContentBlock
    ToolCalls       []ToolCall
    ReasoningContent string  // 顶层 reasoning
    Usage           Usage
    Citations       []Citation
    SafetyRatings   []SafetyRating
    Error           *UpstreamError
    SearchInfo      *SearchInfo  // Qwen 联网搜索
    Citations       []Citation
}

type Usage struct {
    PromptTokens       int
    CompletionTokens   int
    TotalTokens        int
    CachedTokens       int
    CacheCreationTokens int  // Anthropic
    CacheReadTokens    int  // Anthropic
    ReasoningTokens    int  // 推理 token
    Cost               float64  // OpenRouter 等
    Timing             *Timing  // Ollama
}

type Citation struct {
    Type  string  // "file_citation" | "url_citation" | "file_path"
    Title string
    URL   string
    FileID string
    Quote string
}
```

### `StreamChunk`

```go
type StreamChunk struct {
    ID              string
    Object          string
    Created         int64
    Model           string
    Delta           *Message  // 增量内容
    FinishReason    string
    StopReason      string
    Usage           *Usage  // 仅末块
    Error           *UpstreamError
    ReasoningDelta  string  // 推理增量
    ToolCallDelta   []ToolCall  // 工具调用增量
}
```

## 字段映射规则

### 1. content 数组的多形态统一

IR 用 `[]ContentBlock` 统一表示：

| 来源 | 形态 | IR 形态 |
|------|------|---------|
| OpenAI string content | `"Hello"` | `[{"type":"text","text":"Hello"}]` |
| OpenAI array content | `[{"type":"text","text":"Hi"}, {"type":"image_url","image_url":{...}}]` | `[{"type":"text","text":"Hi"}, {"type":"image_url","image_url":{...}}]` |
| Anthropic string content | `"Hello"` | `[{"type":"text","text":"Hello"}]` |
| Anthropic array content | `[{"type":"text","text":"Hi"}, {"type":"image","source":{...}}]` | `[{"type":"text","text":"Hi"}, {"type":"image","image":{...}}]` |
| Gemini parts | `[{"text":"Hi"}, {"inlineData":{...}}]` | `[{"type":"text","text":"Hi"}, {"type":"image","image":{...}}]` |
| Responses input | `[{"type":"input_text","text":"Hi"}, {"type":"input_image","image_url":"..."}]` | `[{"type":"text","text":"Hi"}, {"type":"image","image_url":{...}}]` |

### 2. role 命名统一

| 来源 | IR | 序列化目标 |
|------|----|-----------|
| OpenAI "assistant" | "assistant" | OpenAI: "assistant" |
| Anthropic "assistant" | "assistant" | Gemini: "model" |
| Gemini "model" | "assistant" | Anthropic: "assistant" |
| Responses "assistant" | "assistant" | 同上 |

### 3. tool_calls.arguments 类型

IR 统一为 `map[string]any`（已 parse），序列化时按目标协议要求转回字符串：

- OpenAI：`"{\"key\":\"value\"}"` 字符串
- Anthropic：作为 `tool_use.input` 对象的字段，JSON 对象
- Gemini：作为 `functionCall.args` 对象的字段，JSON 对象

### 4. system prompt 位置

| 来源 | IR | 序列化目标 |
|------|----|-----------|
| OpenAI system message | `Messages[0]` (role="system") | OpenAI: 同位置 |
| Anthropic 顶层 system | `Request.System` | Anthropic: 同位置 |
| Responses 顶层 instructions | `Request.Instructions` | Responses: 同位置 |
| Gemini systemInstruction | `Request.System` | Gemini: `systemInstruction.parts` |

### 5. reasoning / thinking 内容

| 来源 | IR | 序列化目标 |
|------|----|-----------|
| OpenAI 顶层 reasoning_content | `Response.ReasoningContent` | OpenAI Chat: 顶层 `reasoning_content` |
| Anthropic thinking block | `ContentBlock.Thinking`（含 signature） | Anthropic: content block |
| Gemini thought part | `Response.ReasoningContent`（带 `thought: true` 标记） | Gemini: parts |

⚠️ **关键约束**：
- Anthropic thinking 块必须有 signature 才能跨多轮保留，否则只能当作 text 输出。
- vendor reasoning 不能伪造为 Anthropic thinking（避免 Anthropic 验证失败）。

### 6. usage 字段统一

所有 usage 字段在 IR 中统一：

| 来源 | IR 字段 |
|------|---------|
| `usage.prompt_tokens` | `Usage.PromptTokens` |
| `usage.completion_tokens` | `Usage.CompletionTokens` |
| `usage.total_tokens` | `Usage.TotalTokens` |
| `usage.prompt_tokens_details.cached_tokens` | `Usage.CachedTokens` |
| `usage.completion_tokens_details.reasoning_tokens` | `Usage.ReasoningTokens` |
| `usage.input_tokens` (Anthropic) | `Usage.PromptTokens` |
| `usage.output_tokens` (Anthropic) | `Usage.CompletionTokens` |
| `usage.cache_creation_input_tokens` (Anthropic) | `Usage.CacheCreationTokens` |
| `usage.cache_read_input_tokens` (Anthropic) | `Usage.CacheReadTokens` |
| `usageMetadata.cachedContentTokenCount` (Gemini) | `Usage.CachedTokens` |
| `usageMetadata.thoughtsTokenCount` (Gemini) | `Usage.ReasoningTokens` |
| `prompt_cache_hit_tokens` (DeepSeek) | `Usage.CachedTokens` |
