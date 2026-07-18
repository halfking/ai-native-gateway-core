package unified

import (
	"context"
	"time"
)

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

// UnifiedRequest 是标准化的请求格式
type UnifiedRequest struct {
	// 基础字段
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`

	// 生成控制
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`

	// 停止条件
	Stop []string `json:"stop,omitempty"`

	// 高级参数
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Tools          []Tool          `json:"tools,omitempty"`
	ToolChoice     interface{}     `json:"tool_choice,omitempty"`

	// 用户标识
	User string `json:"user,omitempty"`

	// 元数据 (不发送到上游，用于内部路由)
	Metadata map[string]string `json:"-"`
}

// Message 是消息结构
type Message struct {
	Role       string      `json:"role"` // system | user | assistant | tool
	Content    interface{} `json:"content"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// ToolCall 是工具调用
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"` // function
	Function Function `json:"function"`
}

// Function 是函数定义
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ResponseFormat 是响应格式
type ResponseFormat struct {
	Type       string      `json:"type"` // text | json_object | json_schema
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

// JSONSchema 是 JSON Schema 定义
type JSONSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Schema      map[string]interface{} `json:"schema"`
	Strict      bool                   `json:"strict,omitempty"`
}

// Tool 是工具定义
type Tool struct {
	Type     string       `json:"type"` // function
	Function ToolFunction `json:"function"`
}

// ToolFunction 是工具函数
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// UnifiedResponse 是标准化的响应格式
type UnifiedResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"` // chat.completion
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	// 元数据 (用于内部日志)
	Metadata map[string]string `json:"-"`
}

// Choice 是选择项
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"` // stop | length | tool_calls | content_filter
	Logprobs     *string `json:"logprobs,omitempty"`
}

// Usage 是 token 使用量
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

// ChunkChoice 是流式选择项
type ChunkChoice struct {
	Index        int          `json:"index"`
	Delta        MessageDelta `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
	Logprobs     *string      `json:"logprobs,omitempty"`
}

// MessageDelta 是消息增量
type MessageDelta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// AdapterContext 是适配器上下文
type AdapterContext struct {
	context.Context
	Provider  string
	TraceID   string
	StartTime time.Time
}
