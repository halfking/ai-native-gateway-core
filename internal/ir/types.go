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
	// ProtocolGeminiGenerate is the native Google Gemini generateContent API.
	// audit-gemini-adapter (2026-07-13).
	ProtocolGeminiGenerate = "gemini-generate"
	// ProtocolOpenAIResponses is the OpenAI Responses API
	// (previous_response_id, status, etc.). 2026-07-28 (Step 4 round 2).
	ProtocolOpenAIResponses = "openai-responses"
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
	MaxTokens         int      // OpenAI: max_tokens; Anthropic: max_tokens
	Temperature       *float64 // OpenAI: temperature; Anthropic: temperature
	TopP              *float64 // OpenAI: top_p; Anthropic: top_p
	TopK              *int     // Anthropic-only (OpenAI has no equivalent)
	Stop              []string // OpenAI: stop[]; Anthropic: stop_sequences[]
	ParallelToolCalls *bool    // OpenAI-compatible providers

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

	// ─── Multimodal & Personalized Provider Fields (audit-provider-multimodal, 2026-07-13) ───

	// Reasoning enables extended reasoning mode across providers.
	// (OpenAI o1/o3 → reasoning_effort; DeepSeek R1/GLM-Z1/MiniMax-M3/Qwen QwQ → effort/budget)
	Reasoning *ReasoningConfig

	// Modalities specifies desired output modalities (OpenAI TTS / Gemini responseModalities):
	//   ["text"] / ["text","audio"] / ["image","text"]
	Modalities []string

	// AudioConfig is the OpenAI/Gemini output audio configuration.
	// OpenAI: { voice, format, speed }; Gemini: speech_config.
	AudioConfig *AudioConfig

	// LogitBias OpenAI-only: maps token IDs (-100..100) to bias values.
	LogitBias map[string]float64

	// Store OpenAI-only: whether to store the response for later retrieval.
	Store *bool

	// ServiceTier OpenAI-only: "auto" | "default" | "priority".
	ServiceTier string

	// Prediction OpenAI: predicted content for speculative-decoding latency reduction.
	Prediction *Prediction

	// Verbosity OpenAI: "low" | "medium" | "high".
	Verbosity string

	// WebSearchOptions OpenAI: web_search_options.context_size = "low"/"medium"/"high".
	WebSearchOptions *WebSearchOptions

	// PromptCacheKey OpenAI Responses: cache routing key.
	PromptCacheKey string

	// SafetyIdentifier OpenAI: abuse tracking identifier.
	SafetyIdentifier string

	// PreviousResponseID OpenAI Responses: chained response ID.
	PreviousResponseID string

	// Truncation OpenAI: "auto" | "disabled".
	Truncation string

	// ─── Claude 4.5+ fields (audit-claude-4-5, 2026-07-13) ───

	// MCPServers configures Model Context Protocol servers for Claude 4.5+.
	// When set, the upstream Anthropic API exposes MCP tools to the model.
	MCPServers []MCPServer

	// ContextManagement configures Claude 4.5+ automatic context cleanup.
	// When set, the upstream applies edits to compress conversation history
	// (typically when context window threshold is exceeded).
	ContextManagement *ContextManagement

	// Container describes an uploaded file container for Claude 4.5+ skills.
	Container *Container

	// ─── Gemini 专有（2026-08-11 P3 修复：此前静默丢失）───

	// SafetySettings 是 Gemini 的内容安全阈值配置。
	//
	// 修复前：parse_gemini.go 把 safetySettings 列入 knownFields（因此不进
	// Extensions），但从不赋值给 IR、从不序列化、也不上报 loss —— 完全静默。
	// 安全语义参数被静默丢弃属于合规风险。
	SafetySettings []SafetySetting

	// CachedContent 是 Gemini 的 cachedContents/{id} 引用。
	// 与 SafetySettings 同为此前的静默丢失字段。
	CachedContent string

	// ─── Source protocol (used by Serializer to determine output format) ───
	SourceProtocol string // "openai-chat" | "anthropic-messages"

	// Extensions carries non-standard top-level fields extracted by the
	// transport layer (transport.IRExtensionExtractor) for lossless round-trip
	// conversion. Populated during Parse, consumed during Serialize by
	// transport.IRExtensionRestorer. Not touched by ir serializers.
	//
	// Why: OpenAI↔Anthropic conversion via IR drops unknown fields (e.g.
	// provider-private params, future API additions). Extensions preserves
	// them so a round-trip is lossless.
	Extensions map[string]json.RawMessage

	// TargetProvider 是目标上游 provider 的 catalog code（来自
	// provider.Candidate.CatalogCode），用于在序列化层处理 provider 特定的
	// 协议变体。例如 MiniMax（Anthropic 兼容）的 tool_result 块使用
	// tool_call_id 字段名而非标准 Anthropic 的 tool_use_id。
	//
	// 空字符串 = 未指定，序列化器按标准协议处理。
	// 常见值: "minimax"、"anthropic"、"openai"。
	TargetProvider string
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
// "text" | "image" | "audio" | "video" | "document" | "input_audio" | "tool_use" | "tool_result" | "thinking" | "redacted_thinking"
type ContentBlock struct {
	Type string // Discriminant

	// type=text
	Text string

	// type=image
	Image *ImageSource

	// type=audio (OpenAI input_audio / Anthropic 4.6 audio / Qwen audio_url)
	// audit-provider-multimodal (2026-07-13): unified multimodal audio abstraction
	Audio *MediaSource

	// type=video (Gemini / Qwen video input)
	Video *MediaSource

	// type=document (Anthropic message content document / OpenAI file)
	// audit-provider-multimodal (2026-07-13): PDF/text/csv document support
	Document *DocumentBlock

	// type=input_audio (OpenAI chat audio input block)
	// Convenience alias for Audio with simplified structure
	InputAudio *InputAudioBlock

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

// MediaSource is the unified multimedia input source (audio/video).
// Supports URL, base64 data, file_id (Gemini/Anthropic container), and file_uri (Gemini).
type MediaSource struct {
	// Kind identifies the media type: "audio" | "video"
	Kind string `json:"kind,omitempty"`

	// Format is the media-specific format hint
	//   audio: "wav" | "mp3" | "pcm16" | "flac"
	//   video: "mp4" | "mov" | "webm"
	Format string `json:"format,omitempty"`

	// Type identifies the source carrier: "url" | "base64" | "file_id" | "file_uri"
	Type string `json:"type,omitempty"`

	// MediaType is the MIME type: "audio/wav", "video/mp4", etc.
	MediaType string `json:"media_type,omitempty"`

	// URL is the HTTP(S) URL for Type="url"
	URL string `json:"url,omitempty"`

	// Data is the base64-encoded payload (no prefix) for Type="base64"
	Data string `json:"data,omitempty"`

	// FileID references a pre-uploaded file (Gemini files API, Anthropic container)
	FileID string `json:"file_id,omitempty"`

	// FileURI references a remote file (Gemini fileData.fileUri)
	FileURI string `json:"file_uri,omitempty"`

	// Detail is the OpenAI image detail hint (low/high/auto)
	Detail string `json:"detail,omitempty"`
}

// DocumentBlock represents a PDF/text/csv document in message content.
// audit-provider-multimodal (2026-07-13): unified document abstraction for
// Anthropic PDF, OpenAI file input, and Gemini fileData.
type DocumentBlock struct {
	// Kind identifies the document type: "pdf" | "text" | "csv"
	Kind string `json:"kind,omitempty"`

	// MIMEType: "application/pdf" | "text/plain" | "text/csv" | ...
	MIMEType string `json:"mime_type,omitempty"`

	// Source carries the actual document payload
	Source *DocumentSource `json:"source,omitempty"`

	// Title is an optional human-readable label
	Title string `json:"title,omitempty"`

	// Context is an optional description (Anthropic document.context)
	Context string `json:"context,omitempty"`

	// CacheCtrl attaches Anthropic prompt caching hint
	CacheCtrl *CacheControl `json:"cache_control,omitempty"`
}

// InputAudioBlock is the OpenAI input_audio content block (simplified form).
type InputAudioBlock struct {
	Data   string `json:"data"`   // base64-encoded audio data
	Format string `json:"format"` // "wav" | "mp3"
}

// ImageSource represents an image in a message.
type ImageSource struct {
	Type      string `json:"type"`                 // "url" | "base64" | "file_id" | "file_uri"
	MediaType string `json:"media_type,omitempty"` // "image/png" etc.
	URL       string `json:"url,omitempty"`
	Data      string `json:"data,omitempty"` // base64 without prefix
	// audit-10 (2026-07-13): OpenAI image_url.detail parameter
	Detail string `json:"detail,omitempty"`
	// audit-gemini-adapter (2026-07-13): Gemini fileData.fileUri carrier
	FileID  string `json:"file_id,omitempty"`
	FileURI string `json:"file_uri,omitempty"`
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
//
// 2026-07-27 (F-1): the struct previously had only Name/Description/Parameters,
// which modelled only OpenAI/Anthropic "function"-shaped tools. Provider-specific
// tool types (Anthropic computer_use / bash / text_editor; OpenAI web_search /
// code_interpreter / file_search) have no function.name and no JSON-schema
// parameters, so they were silently dropped on parse and could never be
// re-emitted. Type + Raw capture them for passthrough:
//
//   - Type: the tool's wire "type". Empty or "function" → standard function
//     tool, serialized as {type:"function", function:{name,description,
//     parameters}}. Any other value (e.g. "computer_20250124",
//     "web_search_preview") → provider-specific; serialized from Raw on
//     same-protocol routes.
//   - Raw: the original JSON object bytes for a provider-specific tool, kept
//     verbatim so a same-protocol serialize is byte-faithful. Empty for
//     standard function tools.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"` // JSON Schema
	// Type is the wire type of the tool. "" and "function" mean a standard
	// function tool (serialized from Name/Description/Parameters). Any other
	// value marks a provider-specific tool serialized from Raw.
	Type string `json:"type,omitempty"`
	// Raw is the verbatim original JSON for a provider-specific tool. Only
	// populated when Type is neither "" nor "function".
	Raw json.RawMessage `json:"raw,omitempty"`
}

// IsFunction reports whether this tool is a standard function-shaped tool
// (Name/Description/Parameters) rather than a provider-specific type.
func (t ToolDefinition) IsFunction() bool {
	return t.Type == "" || t.Type == "function"
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

// ReasoningConfig holds extended reasoning configuration.
// audit-provider-multimodal (2026-07-13): Unified field across OpenAI o1/o3,
// DeepSeek R1, GLM-Z1, MiniMax-M3, Qwen QwQ, Gemini 2.5+, Anthropic thinking.
//
// Serializer maps to per-protocol fields:
//   - OpenAI:  reasoning_effort (Effort)
//   - Anthropic: thinking { type:"enabled", budget_tokens:Budget }
//   - Qwen:    enable_thinking=true + thinking_budget
//   - Gemini:  generationConfig.thinkingConfig
//   - DeepSeek/GLM/MiniMax: handled via Extensions when configured as object
type ReasoningConfig struct {
	// Type is the canonical toggle: "enabled" | "disabled"
	// When SourceProtocol is OpenAI, omitted means use effort level only.
	Type string `json:"type,omitempty"`

	// Effort is the OpenAI/DeepSeek/GLM reasoning effort level: "low" | "medium" | "high"
	Effort string `json:"effort,omitempty"`

	// BudgetTokens is the Anthropic-style token budget for thinking.
	BudgetTokens *int `json:"budget_tokens,omitempty"`

	// MaxReasoningTokens is the DeepSeek/Qwen QwQ maximum reasoning token cap.
	MaxReasoningTokens *int `json:"max_reasoning_tokens,omitempty"`
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

// ─── Personalized Provider Fields (audit-provider-multimodal, 2026-07-13) ───

// AudioConfig is the OpenAI/Gemini output audio configuration.
// OpenAI chat audio output: { voice, format, speed }.
// Gemini multimodal output: speech_config.
type AudioConfig struct {
	Voice  string  `json:"voice,omitempty"`  // "alloy" / "echo" / "fable" / "onyx" / "nova" / "shimmer"
	Format string  `json:"format,omitempty"` // "mp3" | "opus" | "aac" | "flac" | "wav" | "pcm"
	Speed  float64 `json:"speed,omitempty"`  // 0.25..4.0
}

// Prediction is the OpenAI predicted-content block for speculative decoding.
// When set, the model can return the prediction content in fewer output tokens,
// reducing latency for repeated prefixes (e.g., editing flows).
type Prediction struct {
	Type    string `json:"type"`              // "content"
	Content string `json:"content,omitempty"` // predicted assistant prefix
}

// WebSearchOptions configures the OpenAI built-in web_search tool.
// Includes context-size and optional user location hints.
type WebSearchOptions struct {
	ContextSize       string        `json:"context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *UserLocation `json:"user_location,omitempty"`
	SearchContextSize string        `json:"search_context_size,omitempty"` // alias for some versions
}

// UserLocation provides coarse user location for OpenAI search-result personalization.
type UserLocation struct {
	Type     string `json:"type,omitempty"` // "approximate"
	City     string `json:"city,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// ─── Gemini Native Adapter (audit-gemini-adapter, 2026-07-13) ─────────────

// GenerationConfig mirrors Gemini's generationConfig block. Used when an
// inbound request (OpenAI or Anthropic) needs to be serialized to native
// Gemini format. Most fields overlap with InternalRequest sampling params;
// Gemini-specific extras include responseSchema, responseMimeType, and
// thinkingConfig for Gemini 2.5+.
type GenerationConfig struct {
	Temperature      *float64              `json:"temperature,omitempty"`
	TopP             *float64              `json:"topP,omitempty"`
	TopK             *int                  `json:"topK,omitempty"`
	MaxOutputTokens  *int                  `json:"maxOutputTokens,omitempty"`
	StopSequences    []string              `json:"stopSequences,omitempty"`
	ResponseMimeType string                `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage       `json:"responseSchema,omitempty"`
	CandidateCount   *int                  `json:"candidateCount,omitempty"`
	PresencePenalty  *float64              `json:"presencePenalty,omitempty"`
	FrequencyPenalty *float64              `json:"frequencyPenalty,omitempty"`
	Seed             *int64                `json:"seed,omitempty"`
	ThinkingConfig   *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

// GeminiThinkingConfig is Gemini 2.5+ extended thinking configuration.
// Maps to { thinkingBudget: N } where N is the token budget.
type GeminiThinkingConfig struct {
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

// SafetySetting represents a Gemini safetySettings entry.
// Category: HARM_CATEGORY_HARASSMENT / HATE_SPEECH / SEXUALLY_EXPLICIT / DANGEROUS_CONTENT
// Threshold: BLOCK_NONE / BLOCK_ONLY_HIGH / BLOCK_MEDIUM_AND_ABOVE / BLOCK_LOW_AND_ABOVE
//
// 2026-08-11: 此前该类型是死代码 —— parse_gemini.go 解析了 safetySettings 却
// 从不赋值给 IR，序列化器也从不输出。现已接入全链路（P3 修复）。
type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`

	// Method 是 HarmBlockMethod（SEVERITY / PROBABILITY）。
	// 仅 Vertex AI 支持；Gemini Developer API 不认此字段。
	Method string `json:"method,omitempty"`
}

// GeminiPart represents a single element in Gemini's `contents[].parts[]` array.
// At most one of Text/InlineData/FileData/FunctionCall/FunctionResponse is set.
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
	FileData         *GeminiFileData         `json:"fileData,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	Thought          string                  `json:"thought,omitempty"` // Gemini 2.5 thinking part
}

// GeminiInlineData carries base64-encoded media in a Gemini part.
type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GeminiFileData references a file already uploaded via the Gemini Files API.
type GeminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

// GeminiFunctionCall mirrors the structure Gemini uses for assistant tool calls.
type GeminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// GeminiFunctionResponse is the role="function" message part returned to Gemini.
type GeminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// GeminiContent represents a single Gemini `contents[]` entry.
// Role: "user" | "model" | "function".
type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

// ─── Claude 4.5+ Fields (audit-claude-4-5, 2026-07-13) ────────────────────

// MCPServer describes a Model Context Protocol server exposed to Claude 4.5+.
// MCP servers extend Claude with external tools (databases, APIs, code execution).
// When set, the upstream Anthropic API will route tool calls to these servers.
type MCPServer struct {
	// Type: "url" (only "url" supported today).
	Type string `json:"type,omitempty"`

	// URL is the MCP server endpoint.
	URL string `json:"url,omitempty"`

	// Name identifies the server in error messages and tool prefixes.
	Name string `json:"name,omitempty"`

	// ToolConfig allows per-tool filtering or renaming.
	ToolConfig json.RawMessage `json:"tool_config,omitempty"`

	// AuthorizationToken is the bearer token sent to the MCP server.
	AuthorizationToken string `json:"authorization_token,omitempty"`
}

// ContextEdit represents a single edit operation in Claude 4.5+
// `context_management.edits[]`. Multiple edits are applied in order.
//
// Types:
//   - "clear_tool_uses_20250919" — clear tool_use/tool_result blocks
//   - "clear_thinking_20251015"   — clear thinking blocks (when budget exhausted)
type ContextEdit struct {
	Type string `json:"type"`

	// Threshold: % of context window that triggers the clear (0–100)
	Threshold *int `json:"threshold,omitempty"`

	// Keep is the minimum number of recent items to retain.
	Keep *int `json:"keep,omitempty"`

	// ClearToolInputs controls whether tool input/output is also cleared (default true)
	ClearToolInputs *bool `json:"clear_tool_inputs,omitempty"`
}

// ContextManagement is the container for Claude 4.5+ context edits.
// When set, the upstream Anthropic API will apply these edits to the
// conversation before sending to the model.
type ContextManagement struct {
	Edits []ContextEdit `json:"edits,omitempty"`
}

// ContainerSkill describes a skill loaded into a Claude 4.5+ container.
// Type: "anthropic" (built-in) | "custom" (user-uploaded).
type ContainerSkill struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Container describes an uploaded file container for Claude 4.5+.
// When set, the uploaded skills are available to the model.
type Container struct {
	ID     string           `json:"id,omitempty"`
	Skills []ContainerSkill `json:"skills,omitempty"`
}
