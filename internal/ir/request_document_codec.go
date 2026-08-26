package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	InternalRequestDocumentVersionV1      = 1
	CurrentInternalRequestDocumentVersion = InternalRequestDocumentVersionV1
	internalRequestDocumentKind           = "internal_request"
)

var (
	ErrUnsupportedRequestDocumentVersion = errors.New("ir: unsupported request document version")
	ErrInvalidRequestDocument            = errors.New("ir: invalid request document")
)

type requestDocumentEnvelope struct {
	Version int                 `json:"version"`
	Kind    string              `json:"kind"`
	Payload requestDocumentWire `json:"payload"`
}

type requestDocumentHeader struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type requestDocumentWire struct {
	Model string `json:"model,omitempty"`

	Messages   []messageDocumentWire   `json:"messages,omitempty"`
	System     *systemDocumentWire     `json:"system,omitempty"`
	Tools      []ToolDefinition        `json:"tools,omitempty"`
	ToolChoice *toolChoiceDocumentWire `json:"tool_choice,omitempty"`

	MaxTokens         int      `json:"max_tokens,omitempty"`
	Temperature       *float64 `json:"temperature,omitempty"`
	TopP              *float64 `json:"top_p,omitempty"`
	TopK              *int     `json:"top_k,omitempty"`
	Stop              []string `json:"stop,omitempty"`
	ParallelToolCalls *bool    `json:"parallel_tool_calls,omitempty"`
	Stream            bool     `json:"stream,omitempty"`

	Thinking     *ThinkingConfig `json:"thinking,omitempty"`
	CacheControl []CacheControl  `json:"cache_control,omitempty"`
	Documents    []Document      `json:"documents,omitempty"`

	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	Logprobs         *bool           `json:"logprobs,omitempty"`
	TopLogprobs      *int            `json:"top_logprobs,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
	ResponseFormat   *ResponseFormat `json:"response_format,omitempty"`
	N                int             `json:"n,omitempty"`
	User             string          `json:"user,omitempty"`
	Metadata         *Metadata       `json:"metadata,omitempty"`

	Reasoning          *ReasoningConfig   `json:"reasoning,omitempty"`
	Modalities         []string           `json:"modalities,omitempty"`
	AudioConfig        *AudioConfig       `json:"audio_config,omitempty"`
	LogitBias          map[string]float64 `json:"logit_bias,omitempty"`
	Store              *bool              `json:"store,omitempty"`
	ServiceTier        string             `json:"service_tier,omitempty"`
	Prediction         *Prediction        `json:"prediction,omitempty"`
	Verbosity          string             `json:"verbosity,omitempty"`
	WebSearchOptions   *WebSearchOptions  `json:"web_search_options,omitempty"`
	PromptCacheKey     string             `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier   string             `json:"safety_identifier,omitempty"`
	PreviousResponseID string             `json:"previous_response_id,omitempty"`
	Truncation         string             `json:"truncation,omitempty"`

	MCPServers        []MCPServer        `json:"mcp_servers,omitempty"`
	ContextManagement *ContextManagement `json:"context_management,omitempty"`
	Container         *Container         `json:"container,omitempty"`

	SafetySettings []SafetySetting `json:"safety_settings,omitempty"`
	CachedContent  string          `json:"cached_content,omitempty"`

	SourceProtocol string                     `json:"source_protocol,omitempty"`
	Extensions     map[string]json.RawMessage `json:"extensions,omitempty"`
	TargetProvider string                     `json:"target_provider,omitempty"`
}

type systemDocumentWire struct {
	Content   string                     `json:"content,omitempty"`
	Parts     []contentBlockDocumentWire `json:"parts,omitempty"`
	PDFs      []pdfDocumentWire          `json:"pdfs,omitempty"`
	Priority  *int                       `json:"priority,omitempty"`
	CacheCtrl *CacheControl              `json:"cache_ctrl,omitempty"`
}

type pdfDocumentWire struct {
	Type      string        `json:"type,omitempty"`
	Source    pdfSourceWire `json:"source"`
	Title     string        `json:"title,omitempty"`
	CacheCtrl *CacheControl `json:"cache_control,omitempty"`
}

type pdfSourceWire struct {
	Type     string `json:"type"`
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

type messageDocumentWire struct {
	Role       string                     `json:"role,omitempty"`
	Content    []contentBlockDocumentWire `json:"content,omitempty"`
	ToolCalls  []ToolCall                 `json:"tool_calls,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	Name       string                     `json:"name,omitempty"`
	RawContent *rawContentDocumentWire    `json:"raw_content,omitempty"`
}

type rawContentDocumentWire struct {
	String *string         `json:"string,omitempty"`
	JSON   json.RawMessage `json:"json,omitempty"`
}

type contentBlockDocumentWire struct {
	Type             string                  `json:"type"`
	Text             string                  `json:"text,omitempty"`
	Image            *ImageSource            `json:"image,omitempty"`
	Audio            *MediaSource            `json:"audio,omitempty"`
	Video            *MediaSource            `json:"video,omitempty"`
	Document         *DocumentBlock          `json:"document,omitempty"`
	InputAudio       *InputAudioBlock        `json:"input_audio,omitempty"`
	ToolUse          *ToolUse                `json:"tool_use,omitempty"`
	ToolResult       *toolResultDocumentWire `json:"tool_result,omitempty"`
	Thinking         *ThinkingBlock          `json:"thinking,omitempty"`
	RedactedThinking string                  `json:"redacted_thinking,omitempty"`
	CacheControl     *CacheControl           `json:"cache_control,omitempty"`
	Index            *int                    `json:"index,omitempty"`
	RawContent       *rawContentDocumentWire `json:"raw_content,omitempty"`
}

type toolResultDocumentWire struct {
	ToolUseID      string                     `json:"tool_use_id"`
	Content        []contentBlockDocumentWire `json:"content,omitempty"`
	IsError        bool                       `json:"is_error,omitempty"`
	GeminiResponse json.RawMessage            `json:"gemini_response,omitempty"`
}

type toolChoiceDocumentWire struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

// EncodeRequestDocument encodes an already-normalized IR request into the
// explicit versioned IR document used by request archives. It does not parse a
// provider request and does not use the provider wire serializers.
func EncodeRequestDocument(req *InternalRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrInvalidRequestDocument)
	}
	payload, err := requestDocumentWireFromIR(req)
	if err != nil {
		return nil, err
	}
	envelope := requestDocumentEnvelope{
		Version: InternalRequestDocumentVersionV1,
		Kind:    internalRequestDocumentKind,
		Payload: payload,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("ir: encode request document: %w", err)
	}
	return canonicalRequestDocumentJSON(encoded)
}

// DecodeRequestDocument decodes a versioned IR document. Unknown additive
// fields are ignored for forward compatibility; the version and kind are not.
func DecodeRequestDocument(body []byte) (*InternalRequest, error) {
	if len(bytes.TrimSpace(body)) == 0 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil, fmt.Errorf("%w: empty or null document", ErrInvalidRequestDocument)
	}
	var header requestDocumentHeader
	if err := json.Unmarshal(body, &header); err != nil {
		return nil, fmt.Errorf("%w: decode envelope: %v", ErrInvalidRequestDocument, err)
	}
	if header.Version != InternalRequestDocumentVersionV1 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedRequestDocumentVersion, header.Version)
	}
	if header.Kind != internalRequestDocumentKind {
		return nil, fmt.Errorf("%w: kind %q", ErrInvalidRequestDocument, header.Kind)
	}
	if len(header.Payload) == 0 || bytes.Equal(bytes.TrimSpace(header.Payload), []byte("null")) {
		return nil, fmt.Errorf("%w: payload required", ErrInvalidRequestDocument)
	}
	var payload requestDocumentWire
	if err := json.Unmarshal(header.Payload, &payload); err != nil {
		return nil, fmt.Errorf("%w: decode payload: %v", ErrInvalidRequestDocument, err)
	}
	request, err := internalRequestFromDocumentWire(payload)
	if err != nil {
		return nil, err
	}
	if _, err := EncodeRequestDocument(request); err != nil {
		return nil, fmt.Errorf("%w: invalid payload: %v", ErrInvalidRequestDocument, err)
	}
	return request, nil
}

func requestDocumentWireFromIR(req *InternalRequest) (requestDocumentWire, error) {
	if err := validateRequestDocumentRawFields(req); err != nil {
		return requestDocumentWire{}, err
	}
	messages := make([]messageDocumentWire, 0, len(req.Messages))
	for i := range req.Messages {
		message, err := messageDocumentWireFromIR(req.Messages[i])
		if err != nil {
			return requestDocumentWire{}, fmt.Errorf("ir: encode messages[%d]: %w", i, err)
		}
		messages = append(messages, message)
	}
	var system *systemDocumentWire
	if req.System != nil {
		converted, err := systemDocumentWireFromIR(*req.System)
		if err != nil {
			return requestDocumentWire{}, err
		}
		system = &converted
	}
	var toolChoice *toolChoiceDocumentWire
	if req.ToolChoice != nil {
		toolChoice = &toolChoiceDocumentWire{Type: req.ToolChoice.Type, Name: req.ToolChoice.Name}
	}
	extensions, err := cloneRawMap(req.Extensions, "extensions")
	if err != nil {
		return requestDocumentWire{}, err
	}
	return requestDocumentWire{
		Model: req.Model, Messages: messages, System: system, Tools: req.Tools, ToolChoice: toolChoice,
		MaxTokens: req.MaxTokens, Temperature: req.Temperature, TopP: req.TopP, TopK: req.TopK,
		Stop: req.Stop, ParallelToolCalls: req.ParallelToolCalls, Stream: req.Stream,
		Thinking: req.Thinking, CacheControl: req.CacheControl, Documents: req.Documents,
		FrequencyPenalty: req.FrequencyPenalty, PresencePenalty: req.PresencePenalty, Logprobs: req.Logprobs,
		TopLogprobs: req.TopLogprobs, Seed: req.Seed, ResponseFormat: req.ResponseFormat, N: req.N, User: req.User,
		Metadata: req.Metadata, Reasoning: req.Reasoning, Modalities: req.Modalities, AudioConfig: req.AudioConfig,
		LogitBias: req.LogitBias, Store: req.Store, ServiceTier: req.ServiceTier, Prediction: req.Prediction,
		Verbosity: req.Verbosity, WebSearchOptions: req.WebSearchOptions, PromptCacheKey: req.PromptCacheKey,
		SafetyIdentifier: req.SafetyIdentifier, PreviousResponseID: req.PreviousResponseID, Truncation: req.Truncation,
		MCPServers: req.MCPServers, ContextManagement: req.ContextManagement, Container: req.Container,
		SafetySettings: req.SafetySettings, CachedContent: req.CachedContent, SourceProtocol: req.SourceProtocol,
		Extensions: extensions, TargetProvider: req.TargetProvider,
	}, nil
}

func validateRequestDocumentRawFields(req *InternalRequest) error {
	if _, err := cloneRawMap(req.Extensions, "extensions"); err != nil {
		return err
	}
	for i, tool := range req.Tools {
		if _, err := cloneRaw(tool.Parameters, fmt.Sprintf("tools[%d].parameters", i)); err != nil {
			return err
		}
		if _, err := cloneRaw(tool.Raw, fmt.Sprintf("tools[%d].raw", i)); err != nil {
			return err
		}
	}
	if req.ResponseFormat != nil {
		if _, err := cloneRaw(req.ResponseFormat.Schema, "response_format.json_schema"); err != nil {
			return err
		}
	}
	for i, server := range req.MCPServers {
		if _, err := cloneRaw(server.ToolConfig, fmt.Sprintf("mcp_servers[%d].tool_config", i)); err != nil {
			return err
		}
	}
	return validateMessagesRawFields(req.Messages, "messages")
}

func validateMessagesRawFields(messages []Message, field string) error {
	for i, message := range messages {
		if _, err := rawContentDocumentWireFromAny(message.RawContent, fmt.Sprintf("%s[%d].raw_content", field, i)); err != nil {
			return err
		}
		for blockIndex, block := range message.Content {
			if err := validateContentBlockRawFields(block, fmt.Sprintf("%s[%d].content[%d]", field, i, blockIndex)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateContentBlockRawFields(block ContentBlock, field string) error {
	if _, err := rawContentDocumentWireFromAny(block.RawContent, field+".raw_content"); err != nil {
		return err
	}
	if block.ToolUse != nil {
		if _, err := cloneRaw(block.ToolUse.Input, field+".tool_use.input"); err != nil {
			return err
		}
	}
	if block.ToolResult != nil {
		if _, err := cloneRaw(block.ToolResult.GeminiResponse, field+".tool_result.gemini_response"); err != nil {
			return err
		}
		for i, child := range block.ToolResult.Content {
			if err := validateContentBlockRawFields(child, fmt.Sprintf("%s.tool_result.content[%d]", field, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func messageDocumentWireFromIR(message Message) (messageDocumentWire, error) {
	content := make([]contentBlockDocumentWire, 0, len(message.Content))
	for i := range message.Content {
		block, err := contentBlockDocumentWireFromIR(message.Content[i])
		if err != nil {
			return messageDocumentWire{}, fmt.Errorf("content[%d]: %w", i, err)
		}
		content = append(content, block)
	}
	raw, err := rawContentDocumentWireFromAny(message.RawContent, "message.raw_content")
	if err != nil {
		return messageDocumentWire{}, err
	}
	return messageDocumentWire{Role: message.Role, Content: content, ToolCalls: message.ToolCalls,
		ToolCallID: message.ToolCallID, Name: message.Name, RawContent: raw}, nil
}

func contentBlockDocumentWireFromIR(block ContentBlock) (contentBlockDocumentWire, error) {
	raw, err := rawContentDocumentWireFromAny(block.RawContent, "content_block.raw_content")
	if err != nil {
		return contentBlockDocumentWire{}, err
	}
	var toolResult *toolResultDocumentWire
	if block.ToolResult != nil {
		converted := &toolResultDocumentWire{ToolUseID: block.ToolResult.ToolUseID, IsError: block.ToolResult.IsError}
		converted.GeminiResponse, err = cloneRaw(block.ToolResult.GeminiResponse, "tool_result.gemini_response")
		if err != nil {
			return contentBlockDocumentWire{}, err
		}
		converted.Content = make([]contentBlockDocumentWire, 0, len(block.ToolResult.Content))
		for i := range block.ToolResult.Content {
			child, childErr := contentBlockDocumentWireFromIR(block.ToolResult.Content[i])
			if childErr != nil {
				return contentBlockDocumentWire{}, fmt.Errorf("tool_result.content[%d]: %w", i, childErr)
			}
			converted.Content = append(converted.Content, child)
		}
		toolResult = converted
	}
	return contentBlockDocumentWire{Type: block.Type, Text: block.Text, Image: block.Image, Audio: block.Audio,
		Video: block.Video, Document: block.Document, InputAudio: block.InputAudio, ToolUse: block.ToolUse,
		ToolResult: toolResult, Thinking: block.Thinking, RedactedThinking: block.RedactedThinking,
		CacheControl: block.CacheControl, Index: block.Index, RawContent: raw}, nil
}

func systemDocumentWireFromIR(system SystemPrompt) (systemDocumentWire, error) {
	parts := make([]contentBlockDocumentWire, 0, len(system.Parts))
	for i := range system.Parts {
		part, err := contentBlockDocumentWireFromIR(system.Parts[i])
		if err != nil {
			return systemDocumentWire{}, fmt.Errorf("ir: encode system.parts[%d]: %w", i, err)
		}
		parts = append(parts, part)
	}
	pdfs := make([]pdfDocumentWire, 0, len(system.PDFs))
	for _, pdf := range system.PDFs {
		pdfs = append(pdfs, pdfDocumentWire{Type: pdf.Type, Source: pdfSourceWire{Type: pdf.Source.Type,
			MimeType: pdf.Source.MimeType, Data: pdf.Source.Data, URL: pdf.Source.URL}, Title: pdf.Title, CacheCtrl: pdf.CacheCtrl})
	}
	return systemDocumentWire{Content: system.Content, Parts: parts, PDFs: pdfs, Priority: system.Priority, CacheCtrl: system.CacheCtrl}, nil
}

func internalRequestFromDocumentWire(payload requestDocumentWire) (*InternalRequest, error) {
	messages := make([]Message, 0, len(payload.Messages))
	for i := range payload.Messages {
		message, err := messageFromDocumentWire(payload.Messages[i])
		if err != nil {
			return nil, fmt.Errorf("ir: decode messages[%d]: %w", i, err)
		}
		messages = append(messages, message)
	}
	var system *SystemPrompt
	if payload.System != nil {
		converted, err := systemFromDocumentWire(*payload.System)
		if err != nil {
			return nil, err
		}
		system = &converted
	}
	var toolChoice *ToolChoice
	if payload.ToolChoice != nil {
		toolChoice = &ToolChoice{Type: payload.ToolChoice.Type, Name: payload.ToolChoice.Name}
	}
	extensions, err := cloneRawMap(payload.Extensions, "extensions")
	if err != nil {
		return nil, err
	}
	return &InternalRequest{Model: payload.Model, Messages: messages, System: system, Tools: payload.Tools,
		ToolChoice: toolChoice, MaxTokens: payload.MaxTokens, Temperature: payload.Temperature, TopP: payload.TopP,
		TopK: payload.TopK, Stop: payload.Stop, ParallelToolCalls: payload.ParallelToolCalls, Stream: payload.Stream,
		Thinking: payload.Thinking, CacheControl: payload.CacheControl, Documents: payload.Documents,
		FrequencyPenalty: payload.FrequencyPenalty, PresencePenalty: payload.PresencePenalty, Logprobs: payload.Logprobs,
		TopLogprobs: payload.TopLogprobs, Seed: payload.Seed, ResponseFormat: payload.ResponseFormat, N: payload.N,
		User: payload.User, Metadata: payload.Metadata, Reasoning: payload.Reasoning, Modalities: payload.Modalities,
		AudioConfig: payload.AudioConfig, LogitBias: payload.LogitBias, Store: payload.Store, ServiceTier: payload.ServiceTier,
		Prediction: payload.Prediction, Verbosity: payload.Verbosity, WebSearchOptions: payload.WebSearchOptions,
		PromptCacheKey: payload.PromptCacheKey, SafetyIdentifier: payload.SafetyIdentifier,
		PreviousResponseID: payload.PreviousResponseID, Truncation: payload.Truncation, MCPServers: payload.MCPServers,
		ContextManagement: payload.ContextManagement, Container: payload.Container, SafetySettings: payload.SafetySettings,
		CachedContent: payload.CachedContent, SourceProtocol: payload.SourceProtocol, Extensions: extensions,
		TargetProvider: payload.TargetProvider}, nil
}

func messageFromDocumentWire(message messageDocumentWire) (Message, error) {
	content := make([]ContentBlock, 0, len(message.Content))
	for i := range message.Content {
		block, err := contentBlockFromDocumentWire(message.Content[i])
		if err != nil {
			return Message{}, fmt.Errorf("content[%d]: %w", i, err)
		}
		content = append(content, block)
	}
	return Message{Role: message.Role, Content: content, ToolCalls: message.ToolCalls, ToolCallID: message.ToolCallID,
		Name: message.Name, RawContent: anyFromRawContentDocumentWire(message.RawContent)}, nil
}

func contentBlockFromDocumentWire(block contentBlockDocumentWire) (ContentBlock, error) {
	var toolResult *ToolResult
	if block.ToolResult != nil {
		content := make([]ContentBlock, 0, len(block.ToolResult.Content))
		for i := range block.ToolResult.Content {
			child, err := contentBlockFromDocumentWire(block.ToolResult.Content[i])
			if err != nil {
				return ContentBlock{}, fmt.Errorf("tool_result.content[%d]: %w", i, err)
			}
			content = append(content, child)
		}
		toolResult = &ToolResult{ToolUseID: block.ToolResult.ToolUseID, Content: content, IsError: block.ToolResult.IsError,
			GeminiResponse: cloneRawOrNil(block.ToolResult.GeminiResponse)}
	}
	return ContentBlock{Type: block.Type, Text: block.Text, Image: block.Image, Audio: block.Audio, Video: block.Video,
		Document: block.Document, InputAudio: block.InputAudio, ToolUse: block.ToolUse, ToolResult: toolResult,
		Thinking: block.Thinking, RedactedThinking: block.RedactedThinking, CacheControl: block.CacheControl,
		Index: block.Index, RawContent: anyFromRawContentDocumentWire(block.RawContent)}, nil
}

func systemFromDocumentWire(system systemDocumentWire) (SystemPrompt, error) {
	parts := make([]ContentBlock, 0, len(system.Parts))
	for i := range system.Parts {
		part, err := contentBlockFromDocumentWire(system.Parts[i])
		if err != nil {
			return SystemPrompt{}, fmt.Errorf("ir: decode system.parts[%d]: %w", i, err)
		}
		parts = append(parts, part)
	}
	pdfs := make([]PDFDocument, 0, len(system.PDFs))
	for _, pdf := range system.PDFs {
		pdfs = append(pdfs, PDFDocument{Type: pdf.Type, Source: PDFSource{Type: pdf.Source.Type, MimeType: pdf.Source.MimeType,
			Data: pdf.Source.Data, URL: pdf.Source.URL}, Title: pdf.Title, CacheCtrl: pdf.CacheCtrl})
	}
	return SystemPrompt{Content: system.Content, Parts: parts, PDFs: pdfs, Priority: system.Priority, CacheCtrl: system.CacheCtrl}, nil
}

func rawContentDocumentWireFromAny(value any, field string) (*rawContentDocumentWire, error) {
	if value == nil {
		return nil, nil
	}
	if raw, ok := value.(string); ok {
		if raw == "" || !json.Valid([]byte(raw)) {
			return nil, fmt.Errorf("%w: %s is malformed JSON string", ErrInvalidRequestDocument, field)
		}
		return &rawContentDocumentWire{String: &raw}, nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		cloned, err := cloneRaw(raw, field)
		if err != nil {
			return nil, err
		}
		return &rawContentDocumentWire{JSON: cloned}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("ir: %s: %w", field, err)
	}
	return &rawContentDocumentWire{JSON: encoded}, nil
}

func anyFromRawContentDocumentWire(value *rawContentDocumentWire) any {
	if value == nil {
		return nil
	}
	if value.String != nil {
		return *value.String
	}
	if len(value.JSON) == 0 {
		return nil
	}
	return cloneRawOrNil(value.JSON)
}

func rawJSONFromAny(value any, field string) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return cloneRaw(raw, field)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("ir: %s: %w", field, err)
	}
	return encoded, nil
}

func cloneRaw(value json.RawMessage, field string) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !json.Valid(value) {
		return nil, fmt.Errorf("%w: %s is malformed JSON", ErrInvalidRequestDocument, field)
	}
	return append(json.RawMessage(nil), value...), nil
}

func cloneRawOrNil(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func cloneRawAnyOrNil(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func cloneRawMap(values map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		cloned, err := cloneRaw(value, fmt.Sprintf("%s.%s", field, key))
		if err != nil {
			return nil, err
		}
		out[key] = cloned
	}
	return out, nil
}

func canonicalRequestDocumentJSON(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: canonicalize: %v", ErrInvalidRequestDocument, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: multiple JSON values", ErrInvalidRequestDocument)
		}
		return nil, fmt.Errorf("%w: canonicalize trailing data: %v", ErrInvalidRequestDocument, err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical marshal: %v", ErrInvalidRequestDocument, err)
	}
	return canonical, nil
}
