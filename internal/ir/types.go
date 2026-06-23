// Package ir provides an Internal Representation (IR) schema that is a
// superset of both OpenAI Chat Completions and Anthropic Messages APIs.
//
// Architecture (3-layer):
//
//	Inbound Layer (Parser)          IR Layer                 Outbound Layer (Serializer)
//	┌─────────────────────┐       ┌─────────────────┐       ┌─────────────────────┐
//	│ OpenAI Parser        │──────▶│                 │──────▶│ OpenAI Serializer   │
//	│ Anthropic Parser     │──────▶│ InternalRequest │──────▶│ Anthropic Serializer│
//	│ (future) Gemini     │──────▶│  (ir.IR)        │──────▶│ (future) Gemini     │
//	└─────────────────────┘       └─────────────────┘       └─────────────────────┘
//	       ▲                                                      │
//	       │                                                      │
//	       └────────  Protocol Auto-Detection ◀────────────────────┘
//
// Complexity reduced from O(N²) to O(N): adding a new protocol only requires
// one Parser + one Serializer.
package ir

import "encoding/json"

// Protocol constants for SourceProtocol field.
const (
	ProtocolOpenAIChat        = "openai-chat"
	ProtocolAnthropicMessages = "anthropic-messages"
)

// InternalRequest is the unified intermediate representation for all inbound
// protocol requests. Its field set is the superset of OpenAI Chat Completions
// and Anthropic Messages API fields.
type InternalRequest struct {
	Model string // Model identifier (passthrough)

	// Messages is the unified message list. Both OpenAI and Anthropic formats
	// are normalized into this structure.
	Messages []Message

	// System is the system prompt. In OpenAI it's part of messages with role=system;
	// in Anthropic it's a top-level "system" field. We normalize to this field.
	System *SystemPrompt

	// Tools is the unified tool definitions. OpenAI tools[] and Anthropic tools[]
	// are both normalized here.
	Tools []ToolDefinition

	// ToolChoice controls which tool to call. OpenAI and Anthropic have compatible
	// representations.
	ToolChoice *ToolChoice

	// Sampling parameters (shared)
	MaxTokens   int      // OpenAI: max_tokens; Anthropic: max_tokens
	Temperature *float64 // OpenAI: temperature; Anthropic: temperature
	TopP        *float64 // OpenAI: top_p; Anthropic: top_p
	TopK        *int     // Anthropic-only (OpenAI has no equivalent)
	Stop        []string // OpenAI: stop[]; Anthropic: stop_sequences[]

	Stream bool // Streaming flag (passthrough both directions)

	// ─── Anthropic-specific fields (stored, serialized based on target protocol) ───

	// Thinking enables Claude's extended thinking mode (Anthropic-only).
	Thinking *ThinkingConfig

	// CacheControl is the semantic caching hint (Anthropic-only).
	// Serialized as cache_control object in Anthropic format.
	CacheControl []CacheControl

	// Documents is the document search/prompt injection (Anthropic-only).
	Documents []Document

	// ─── OpenAI-specific fields (stored, serialized based on target protocol) ───

	// FrequencyPenalty OpenAI-only
	FrequencyPenalty *float64
	// PresencePenalty OpenAI-only
	PresencePenalty *float64
	// Logprobs OpenAI-only
	Logprobs *bool
	// TopLogprobs OpenAI-only
	TopLogprobs *int
	// Seed OpenAI-only (deterministic sampling)
	Seed *int64
	// ResponseFormat OpenAI-only (json_schema / text)
	ResponseFormat *ResponseFormat
	// N OpenAI-only (number of completions)
	N int
	// User OpenAI-only (equivalent to Anthropic metadata.user_id)
	User string

	// Metadata is the generic metadata container (Anthropic: metadata.user_id → User)
	Metadata *Metadata

	// ─── Source protocol (used by Serializer to determine output format) ───
	SourceProtocol string // "openai-chat" | "anthropic-messages"
}

// SystemPrompt represents a normalized system prompt.
type SystemPrompt struct {
	Content   string         // Plain text content
	Parts     []ContentBlock // Anthropic-style content blocks (for mixed content)
	PDFs      []PDFDocument  // Anthropic PDF documents
	Priority  *int           // Priority for system prompt (Anthropic)
	CacheCtrl *CacheControl  // Cache control for system prompt
}

// PDFDocument represents a PDF document in Anthropic system prompt.
type PDFDocument struct {
	Type      string // "document"
	Source    PDFSource
	Title     string        `json:"title,omitempty"`
	CacheCtrl *CacheControl `json:"cache_control,omitempty"`
}

// PDFSource is the source of a PDF document.
type PDFSource struct {
	Type     string `json:"type"` // "document"
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"` // base64 encoded
	URL      string `json:"url,omitempty"`
}

// Message is the unified message structure. Role values:
// "system" | "user" | "assistant" | "tool"
type Message struct {
	Role       string
	Content    []ContentBlock // Main content (mixed blocks)
	ToolCalls  []ToolCall     // OpenAI-style tool_calls from assistant
	ToolCallID string         // OpenAI: tool role uses this; Anthropic uses content blocks
	Name       string         // tool role: function name

	// RawContent preserves the original content format when we need exact round-trip.
	// Used for content that doesn't normalize cleanly (e.g., complex multimodal).
	RawContent any
}

// ContentBlock represents a single content element. Type values:
// "text" | "image" | "tool_use" | "tool_result" | "thinking" | "redacted_thinking"
type ContentBlock struct {
	Type string // Discriminant

	// type=text
	Text string

	// type=image
	Image *ImageSource

	// type=tool_use
	ToolUse *ToolUse

	// type=tool_result
	ToolResult *ToolResult

	// type=thinking
	Thinking *ThinkingBlock

	// type=redacted_thinking
	RedactedThinking string

	// Cache control (can appear on any block in Anthropic)
	CacheControl *CacheControl

	// Index for interleaved tool results (Anthropic)
	Index *int `json:"index,omitempty"`

	// RawContent preserves the original content format for unknown block types.
	RawContent any
}

// ImageSource represents an image in a message.
type ImageSource struct {
	Type      string `json:"type"`                 // "url" | "base64"
	MediaType string `json:"media_type,omitempty"` // "image/png" etc.
	URL       string `json:"url,omitempty"`
	Data      string `json:"data,omitempty"` // base64 without prefix
}

// ToolUse is an assistant's tool call request.
type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"` // Already serialized JSON object
}

// ToolCall is OpenAI's representation of a tool call.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // Always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

// ToolResult is the result of a tool execution.
type ToolResult struct {
	ToolUseID string         `json:"tool_use_id"`
	Content   []ContentBlock `json:"content"` // Can be multi-modal
	IsError   bool           `json:"is_error,omitempty"`
}

// ToolDefinition is a callable tool schema.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"` // JSON Schema
}

// ToolChoice controls automatic vs forced tool calling.
type ToolChoice struct {
	Type string // "auto" | "none" | "any" | "required" | "tool"
	Name string // When Type="tool", this is the forced function name
}

// ThinkingConfig enables/disables Claude's extended thinking mode.
type ThinkingConfig struct {
	Type         string `json:"type"` // "enabled" | "disabled"
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// ThinkingBlock is the actual thinking content from Claude.
//
// PR-2 (2026-06-24): Signature added so claude-opus-4-8 round-trips
// work end-to-end. Without it the next-turn request is rejected by
// Anthropic with HTTP 400 "signature: Input should be a valid string"
// and the assistant loses its prior tool_use context — the symptom
// that triggered this fix. Use `omitempty` so legacy callers that
// only set Thinking still serialise cleanly.
type ThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

// CacheControl represents Anthropic's semantic cache control.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// Document represents Anthropic's document prompt injection.
type Document struct {
	Type      string         `json:"type"` // "document"
	Source    DocumentSource `json:"source"`
	Title     string         `json:"title,omitempty"`
	Context   string         `json:"context,omitempty"`
	CacheCtrl *CacheControl  `json:"cache_control,omitempty"`
}

// DocumentSource is the source of a document.
type DocumentSource struct {
	Type      string `json:"type"` // "text" | "csv"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // Raw text or base64
	URL       string `json:"url,omitempty"`
}

// Metadata is generic key-value metadata.
type Metadata struct {
	UserID    string            `json:"user_id,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Other     map[string]string `json:"other,omitempty"`
}

// ResponseFormat specifies the expected response format (OpenAI).
type ResponseFormat struct {
	Type   string          `json:"type"` // "text" | "json_object"
	Schema json.RawMessage `json:"json_schema,omitempty"`
}
