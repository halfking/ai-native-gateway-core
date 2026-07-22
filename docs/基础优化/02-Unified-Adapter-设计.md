# Unified Adapter 统一适配器设计文档

> **模块**: `adapter/unified/`
> **版本**: v1.0
> **日期**: 2026-07-18
> **状态**: 📋 设计阶段
> **Phase**: Phase 1A - 基础优化

---

## 1. 背景与目标

### 1.1 问题

LLM Gateway 当前直接调用各提供商的原生 API，存在以下问题：

**API 差异大**:
- OpenAI: `max_tokens` (可选)，消息格式 `{role, content}`
- Anthropic: `max_tokens` (必填)，消息格式 `{role, content}` (细微差异)
- Azure OpenAI: 需要额外的 `deployment_id`，认证方式不同

**维护成本高**:
- 每个提供商需要独立的请求/响应处理逻辑
- 新增提供商需要修改多处代码
- 错误处理逻辑分散

**代码重复**:
- 参数校验逻辑重复
- 流式响应解析逻辑重复
- Token 计数逻辑重复

### 1.2 目标

设计统一适配器层，实现：
- ✅ **统一接口**: 上游调用方使用统一的请求/响应格式
- ✅ **双向转换**: 自动完成 Unified ↔ Provider 的格式转换
- ✅ **易扩展**: 新增提供商只需实现 Adapter 接口
- ✅ **可观测**: 记录转换错误和性能指标

### 1.3 非目标

- ❌ 不实现提供商路由逻辑 (由 Dispatcher 负责)
- ❌ 不实现请求重试 (由上层负责)
- ❌ 不实现 Token 计费 (由 Billing 模块负责)

---

## 2. 架构设计

### 2.1 整体架构

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │ Unified Request
       ▼
┌─────────────────────────┐
│   Unified Adapter       │
│  ┌──────────────────┐   │
│  │  Adapter Factory │   │
│  └────────┬─────────┘   │
│           │             │
│     ┌─────┴─────┐       │
│     ▼           ▼       │
│ ┌────────┐ ┌─────────┐ │
│ │OpenAI  │ │Anthropic│ │
│ │Adapter │ │Adapter  │ │
│ └────┬───┘ └────┬────┘ │
└──────┼──────────┼───────┘
       │          │
       ▼          ▼
  OpenAI API  Anthropic API
```

### 2.2 数据流

**请求流**:
```
Client Request (Unified)
  → Adapter.ToProviderRequest()
  → Provider Request (OpenAI/Anthropic)
  → HTTP Client
  → Provider API
```

**响应流**:
```
Provider Response
  → Adapter.FromProviderResponse()
  → Unified Response
  → Client
```

---

## 3. 接口设计

### 3.1 核心接口

```go
package unified

import "context"

// Adapter 是提供商适配器接口
type Adapter interface {
    // Name 返回提供商名称
    Name() string

    // ToProviderRequest 将统一请求转换为提供商请求
    ToProviderRequest(req *UnifiedRequest) (interface{}, error)

    // FromProviderResponse 将提供商响应转换为统一响应
    FromProviderResponse(resp interface{}) (*UnifiedResponse, error)

    // SupportedModels 返回支持的模型列表
    SupportedModels() []string

    // ValidateRequest 验证请求参数
    ValidateRequest(req *UnifiedRequest) error
}

// StreamAdapter 是支持流式响应的适配器接口
type StreamAdapter interface {
    Adapter

    // FromProviderStreamChunk 将流式响应块转换为统一格式
    FromProviderStreamChunk(chunk interface{}) (*UnifiedStreamChunk, error)
}
```

### 3.2 统一请求格式

```go
// UnifiedRequest 是标准化的请求格式
type UnifiedRequest struct {
    // 基础字段
    Model       string    `json:"model"`
    Messages    []Message `json:"messages"`
    Stream      bool      `json:"stream,omitempty"`

    // 生成控制
    MaxTokens       *int     `json:"max_tokens,omitempty"`
    Temperature     *float64 `json:"temperature,omitempty"`
    TopP            *float64 `json:"top_p,omitempty"`
    PresencePenalty *float64 `json:"presence_penalty,omitempty"`
    FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`

    // 停止条件
    Stop []string `json:"stop,omitempty"`

    // 高级参数
    ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
    Tools          []Tool          `json:"tools,omitempty"`
    ToolChoice     interface{}     `json:"tool_choice,omitempty"`

    // 用户标识
    User string `json:"user,omitempty"`
}

type Message struct {
    Role    string      `json:"role"`    // system | user | assistant | tool
    Content interface{} `json:"content"` // string 或 []ContentPart
    Name    string      `json:"name,omitempty"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type ResponseFormat struct {
    Type string `json:"type"` // text | json_object | json_schema
}
```

### 3.3 统一响应格式

```go
// UnifiedResponse 是标准化的响应格式
type UnifiedResponse struct {
    ID      string   `json:"id"`
    Object  string   `json:"object"` // chat.completion
    Created int64    `json:"created"`
    Model   string   `json:"model"`
    Choices []Choice `json:"choices"`
    Usage   Usage    `json:"usage"`
}

type Choice struct {
    Index        int     `json:"index"`
    Message      Message `json:"message"`
    FinishReason string  `json:"finish_reason"` // stop | length | tool_calls | content_filter
}

type Usage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
    TotalTokens      int `json:"total_tokens"`
}

// UnifiedStreamChunk 是流式响应块
type UnifiedStreamChunk struct {
    ID      string        `json:"id"`
    Object  string        `json:"object"` // chat.completion.chunk
    Created int64         `json:"created"`
    Model   string        `json:"model"`
    Choices []ChunkChoice `json:"choices"`
}

type ChunkChoice struct {
    Index        int          `json:"index"`
    Delta        MessageDelta `json:"delta"`
    FinishReason *string      `json:"finish_reason"`
}

type MessageDelta struct {
    Role      string `json:"role,omitempty"`
    Content   string `json:"content,omitempty"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}
```

---

## 4. Adapter 实现

### 4.1 OpenAI Adapter

```go
package unified

type OpenAIAdapter struct {
    version string
}

func NewOpenAIAdapter() *OpenAIAdapter {
    return &OpenAIAdapter{version: "v1.0"}
}

func (a *OpenAIAdapter) Name() string {
    return "openai"
}

func (a *OpenAIAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
    // OpenAI 格式基本与 Unified 一致，直接转换
    return &openai.ChatCompletionRequest{
        Model:            req.Model,
        Messages:         convertMessages(req.Messages),
        Stream:           req.Stream,
        MaxTokens:        req.MaxTokens,
        Temperature:      req.Temperature,
        TopP:             req.TopP,
        PresencePenalty:  req.PresencePenalty,
        FrequencyPenalty: req.FrequencyPenalty,
        Stop:             req.Stop,
        ResponseFormat:   convertResponseFormat(req.ResponseFormat),
        Tools:            convertTools(req.Tools),
        ToolChoice:       req.ToolChoice,
        User:             req.User,
    }, nil
}

func (a *OpenAIAdapter) FromProviderResponse(resp interface{}) (*UnifiedResponse, error) {
    oaiResp, ok := resp.(*openai.ChatCompletionResponse)
    if !ok {
        return nil, fmt.Errorf("invalid response type: %T", resp)
    }

    return &UnifiedResponse{
        ID:      oaiResp.ID,
        Object:  oaiResp.Object,
        Created: oaiResp.Created,
        Model:   oaiResp.Model,
        Choices: convertChoices(oaiResp.Choices),
        Usage:   convertUsage(oaiResp.Usage),
    }, nil
}

func (a *OpenAIAdapter) SupportedModels() []string {
    return []string{
        "gpt-4", "gpt-4-turbo", "gpt-4o",
        "gpt-3.5-turbo", "gpt-3.5-turbo-16k",
    }
}

func (a *OpenAIAdapter) ValidateRequest(req *UnifiedRequest) error {
    if req.Model == "" {
        return errors.New("model is required")
    }
    if len(req.Messages) == 0 {
        return errors.New("messages cannot be empty")
    }
    return nil
}
```

### 4.2 Anthropic Adapter

```go
package unified

type AnthropicAdapter struct {
    version string
}

func NewAnthropicAdapter() *AnthropicAdapter {
    return &AnthropicAdapter{version: "v1.0"}
}

func (a *AnthropicAdapter) Name() string {
    return "anthropic"
}

func (a *AnthropicAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
    // Anthropic 特殊处理

    // 1. max_tokens 是必填的
    maxTokens := 4096 // 默认值
    if req.MaxTokens != nil {
        maxTokens = *req.MaxTokens
    }

    // 2. system 消息需要单独提取
    systemMsg, messages := extractSystemMessage(req.Messages)

    // 3. 构造 Anthropic 请求
    return &anthropic.MessageRequest{
        Model:       req.Model,
        Messages:    convertToAnthropicMessages(messages),
        System:      systemMsg,
        MaxTokens:   maxTokens,
        Temperature: req.Temperature,
        TopP:        req.TopP,
        StopSequences: req.Stop,
        Stream:      req.Stream,
    }, nil
}

func (a *AnthropicAdapter) FromProviderResponse(resp interface{}) (*UnifiedResponse, error) {
    anthResp, ok := resp.(*anthropic.MessageResponse)
    if !ok {
        return nil, fmt.Errorf("invalid response type: %T", resp)
    }

    // 转换为 Unified 格式
    return &UnifiedResponse{
        ID:      anthResp.ID,
        Object:  "chat.completion",
        Created: time.Now().Unix(),
        Model:   anthResp.Model,
        Choices: []Choice{
            {
                Index: 0,
                Message: Message{
                    Role:    "assistant",
                    Content: extractContent(anthResp.Content),
                },
                FinishReason: mapFinishReason(anthResp.StopReason),
            },
        },
        Usage: Usage{
            PromptTokens:     anthResp.Usage.InputTokens,
            CompletionTokens: anthResp.Usage.OutputTokens,
            TotalTokens:      anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens,
        },
    }, nil
}

func (a *AnthropicAdapter) SupportedModels() []string {
    return []string{
        "claude-3-opus-20240229",
        "claude-3-sonnet-20240229",
        "claude-3-haiku-20240307",
        "claude-2.1",
    }
}

// 提取 system 消息 (Anthropic 要求单独传递)
func extractSystemMessage(messages []Message) (string, []Message) {
    var systemMsg string
    var filtered []Message

    for _, msg := range messages {
        if msg.Role == "system" {
            if content, ok := msg.Content.(string); ok {
                systemMsg = content
            }
        } else {
            filtered = append(filtered, msg)
        }
    }

    return systemMsg, filtered
}

// 映射 finish_reason
func mapFinishReason(stopReason string) string {
    switch stopReason {
    case "end_turn":
        return "stop"
    case "max_tokens":
        return "length"
    case "stop_sequence":
        return "stop"
    default:
        return stopReason
    }
}
```

### 4.3 Azure OpenAI Adapter

```go
package unified

type AzureOpenAIAdapter struct {
    *OpenAIAdapter
    deploymentMapping map[string]string // model → deployment_id
}

func NewAzureOpenAIAdapter(deployments map[string]string) *AzureOpenAIAdapter {
    return &AzureOpenAIAdapter{
        OpenAIAdapter:     NewOpenAIAdapter(),
        deploymentMapping: deployments,
    }
}

func (a *AzureOpenAIAdapter) Name() string {
    return "azure_openai"
}

func (a *AzureOpenAIAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
    // 先调用 OpenAI Adapter
    oaiReq, err := a.OpenAIAdapter.ToProviderRequest(req)
    if err != nil {
        return nil, err
    }

    // 映射 model → deployment_id
    deploymentID, ok := a.deploymentMapping[req.Model]
    if !ok {
        return nil, fmt.Errorf("no deployment mapping for model: %s", req.Model)
    }

    // Azure 特定字段
    azureReq := oaiReq.(*openai.ChatCompletionRequest)
    azureReq.DeploymentID = deploymentID

    return azureReq, nil
}
```

---

## 5. Adapter Registry

### 5.1 注册机制

```go
package unified

var registry = NewRegistry()

type Registry struct {
    adapters map[string]Adapter
    mu       sync.RWMutex
}

func NewRegistry() *Registry {
    return &Registry{
        adapters: make(map[string]Adapter),
    }
}

func (r *Registry) Register(adapter Adapter) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.adapters[adapter.Name()] = adapter
}

func (r *Registry) Get(provider string) (Adapter, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()

    adapter, ok := r.adapters[provider]
    if !ok {
        return nil, fmt.Errorf("adapter not found: %s", provider)
    }
    return adapter, nil
}

func (r *Registry) List() []string {
    r.mu.RLock()
    defer r.mu.RUnlock()

    names := make([]string, 0, len(r.adapters))
    for name := range r.adapters {
        names = append(names, name)
    }
    return names
}

// 全局注册函数
func Register(adapter Adapter) {
    registry.Register(adapter)
}

func GetAdapter(provider string) (Adapter, error) {
    return registry.Get(provider)
}
```

### 5.2 初始化

```go
package unified

func init() {
    // 注册内置 Adapter
    Register(NewOpenAIAdapter())
    Register(NewAnthropicAdapter())

    // Azure OpenAI 需要配置后注册
    // Register(NewAzureOpenAIAdapter(deployments))
}
```

---

## 6. 流式响应处理

### 6.1 流式接口

```go
func (a *OpenAIAdapter) FromProviderStreamChunk(chunk interface{}) (*UnifiedStreamChunk, error) {
    oaiChunk, ok := chunk.(*openai.ChatCompletionStreamChunk)
    if !ok {
        return nil, fmt.Errorf("invalid chunk type: %T", chunk)
    }

    choices := make([]ChunkChoice, len(oaiChunk.Choices))
    for i, choice := range oaiChunk.Choices {
        choices[i] = ChunkChoice{
            Index: choice.Index,
            Delta: MessageDelta{
                Role:    choice.Delta.Role,
                Content: choice.Delta.Content,
            },
            FinishReason: choice.FinishReason,
        }
    }

    return &UnifiedStreamChunk{
        ID:      oaiChunk.ID,
        Object:  oaiChunk.Object,
        Created: oaiChunk.Created,
        Model:   oaiChunk.Model,
        Choices: choices,
    }, nil
}
```

---

## 7. Prometheus Metrics

### 7.1 指标定义

```go
var (
    AdapterRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_adapter_requests_total",
            Help: "Total adapter requests",
        },
        []string{"adapter_version", "provider", "status"},
    )

    AdapterConversionErrors = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_adapter_conversion_errors_total",
            Help: "Total adapter conversion errors",
        },
        []string{"adapter_version", "error_type"},
    )

    AdapterConversionDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_adapter_conversion_duration_seconds",
            Help:    "Adapter conversion duration",
            Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01},
        },
        []string{"adapter_version", "provider", "direction"},
    )
)
```

### 7.2 指标埋点

```go
func (a *OpenAIAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
    start := time.Now()
    defer func() {
        duration := time.Since(start).Seconds()
        AdapterConversionDuration.WithLabelValues(
            a.version, a.Name(), "to_provider",
        ).Observe(duration)
    }()

    // 转换逻辑...

    AdapterRequests.WithLabelValues(a.version, a.Name(), "success").Inc()
    return result, nil
}
```

---

## 8. 测试策略

### 8.1 单元测试

```go
func TestOpenAIAdapter_ToProviderRequest(t *testing.T) {
    adapter := NewOpenAIAdapter()

    req := &UnifiedRequest{
        Model: "gpt-4",
        Messages: []Message{
            {Role: "user", Content: "Hello"},
        },
        MaxTokens: intPtr(100),
        Temperature: float64Ptr(0.7),
    }

    result, err := adapter.ToProviderRequest(req)
    require.NoError(t, err)

    oaiReq := result.(*openai.ChatCompletionRequest)
    assert.Equal(t, "gpt-4", oaiReq.Model)
    assert.Equal(t, 100, *oaiReq.MaxTokens)
    assert.Equal(t, 0.7, *oaiReq.Temperature)
}

func TestAnthropicAdapter_ExtractSystemMessage(t *testing.T) {
    adapter := NewAnthropicAdapter()

    req := &UnifiedRequest{
        Model: "claude-3-opus",
        Messages: []Message{
            {Role: "system", Content: "You are helpful"},
            {Role: "user", Content: "Hello"},
        },
    }

    result, err := adapter.ToProviderRequest(req)
    require.NoError(t, err)

    anthReq := result.(*anthropic.MessageRequest)
    assert.Equal(t, "You are helpful", anthReq.System)
    assert.Len(t, anthReq.Messages, 1)
}
```

### 8.2 集成测试

```go
func TestAdapter_RoundTrip(t *testing.T) {
    tests := []struct {
        name     string
        adapter  Adapter
        model    string
    }{
        {"OpenAI", NewOpenAIAdapter(), "gpt-4"},
        {"Anthropic", NewAnthropicAdapter(), "claude-3-opus"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Unified Request
            req := &UnifiedRequest{
                Model: tt.model,
                Messages: []Message{
                    {Role: "user", Content: "test"},
                },
            }

            // To Provider
            providerReq, err := tt.adapter.ToProviderRequest(req)
            require.NoError(t, err)

            // 模拟 Provider 响应
            providerResp := mockProviderResponse(tt.name)

            // From Provider
            unifiedResp, err := tt.adapter.FromProviderResponse(providerResp)
            require.NoError(t, err)

            // 验证
            assert.Equal(t, tt.model, unifiedResp.Model)
            assert.NotEmpty(t, unifiedResp.Choices)
        })
    }
}
```

---

## 9. 相关文档

- [Phase 1 实施计划](../Phase1-实施计划.md)
- [Circuit Breaker 设计](01-Circuit-Breaker-设计.md)
- [Prometheus Metrics 命名规范](../prometheus_metrics_naming.md)

---

**作者**: Infrastructure Team
**审阅者**: 待定
**下次复审**: 实现完成后
