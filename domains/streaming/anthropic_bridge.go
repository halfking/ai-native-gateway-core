package streaming

// anthropic_bridge.go ports the four relay/relay anthropic streaming/conversion
// helpers into the live streaming package so cmd/gateway can drop the
// _to-be-deprecated/relay import. Each wrapper here mirrors the
// deprecated implementation byte-for-byte and is exercised by the
// `cmd/gateway` flow tests once main.go is cut over.
//
// 2026-06-26 deep-integration: streaming package becomes the single
// source of truth for the OpenAI/Anthropic chat wire formats.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
)

const anthropicSSEBufSize = 64 * 1024

// StreamAnthropicPassthrough is the live Q4 Anthropic SSE forwarder. It
// reads Anthropic-format SSE events from upstream and writes them to
// the client unchanged (byte-for-byte), while scanning for
// has_thinking / usage accounting in the side-channel audit capture.
//
// This is the "Q4" path: client speaks Anthropic, upstream speaks
// Anthropic (e.g. anthropic provider, or minimax's /anthropic
// compatible endpoint), no protocol conversion required.
//
// Track C C5 (2026-06-21): when pc is non-nil, every byte forwarded
// to the client is also appended to the capturer buffer so the
// gateway can replay the full SSE response from pending store after
// a client disconnect. The capturer is finalized before return so
// the caller can snapshot and persist it (see cmd/gateway/main.go's
// saveCapturedPending helper). nil pc is fine.
func StreamAnthropicPassthrough(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic passthrough panic", "panic", r, "stack", string(debug.Stack()))
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		if pc != nil {
			pc.finalize(outcome)
		}
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{Interrupted: true, Reason: "no_flusher"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	reader := bufio.NewReaderSize(resp.Body, anthropicSSEBufSize)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			outcome.Interrupted = true
			outcome.Reason = "read_error"
			if capture != nil {
				capture.MarkInterruptedWithReason("read_error")
			}
			return outcome
		}
		if _, werr := w.Write([]byte(line)); werr != nil {
			outcome.Interrupted = true
			outcome.Reason = "client_disconnected"
			if capture != nil {
				capture.MarkInterruptedWithReason("client_disconnected")
			}
			return outcome
		}
		if pc != nil {
			pc.append(line)
		}
		if capture != nil && strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			payload = strings.TrimSpace(payload)
			observeAnthropicPayload(capture, payload, clientModel, outboundModel)
		}
		if line == "\n" {
			flusher.Flush()
		}
	}
	flusher.Flush()
	return outcome
}

// observeAnthropicPayload inspects a single Anthropic SSE data payload
// and updates the side-channel audit capture accordingly.
func observeAnthropicPayload(c *audit.StreamCapture, payload, clientModel, outboundModel string) {
	if payload == "" || payload == "[DONE]" {
		return
	}
	var v struct {
		Type    string `json:"type"`
		Message *struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens  *int `json:"input_tokens"`
				OutputTokens *int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage *struct {
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
		Index        *int `json:"index"`
		ContentBlock *struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return
	}
	switch v.Type {
	case "message_start":
		if v.Message != nil {
			if v.Message.Usage.InputTokens != nil {
				pt := *v.Message.Usage.InputTokens
				c.InputTokens = &pt
			}
			if v.Message.Model != "" {
				checkAnthropicModelMismatch(c, clientModel, outboundModel, v.Message.Model)
			}
		}
	case "message_delta":
		if v.Usage != nil && v.Usage.OutputTokens != nil {
			ot := *v.Usage.OutputTokens
			c.OutputTokens = &ot
		}
	case "message_stop":
		c.MarkDone()
	case "content_block_start":
		if v.ContentBlock != nil && v.ContentBlock.Type == "thinking" {
			c.HasThinking = true
			c.ThinkingBlocksN++
		}
	case "error":
		c.MarkStreamError()
	}
}

func checkAnthropicModelMismatch(c *audit.StreamCapture, clientModel, outboundModel, respModel string) {
	want := clientModel
	if want == "" {
		want = outboundModel
	}
	if want == "" || respModel == "" {
		return
	}
	if !strings.EqualFold(want, respModel) {
		c.ModelMismatch = true
	}
}

// StreamAnthropicSSEToOpenAI is the Q3 byte-level passthrough for
// Anthropic-format SSE upstreams. The actual IR-driven transform
// (Anthropic event → OpenAI chat.completion.chunk) lives in the
// executor's stream path; this function writes upstream bytes through
// to the client and feeds the side-channel audit capturer. When the
// upstream body is already OpenAI-shaped, the executor's own stream
// path is used instead and this helper is not invoked.
func StreamAnthropicSSEToOpenAI(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic-to-openai stream panic recovered",
				"panic", r, "stack", string(debug.Stack()),
				"request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		if pc != nil {
			pc.finalize(outcome)
		}
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{Interrupted: true, Reason: "no_flusher"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	reader := bufio.NewReaderSize(resp.Body, anthropicSSEBufSize)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			outcome.Interrupted = true
			outcome.Reason = "read_error"
			if capture != nil {
				capture.MarkInterruptedWithReason("read_error")
			}
			return outcome
		}
		if _, werr := w.Write([]byte(line)); werr != nil {
			outcome.Interrupted = true
			outcome.Reason = "client_disconnected"
			if capture != nil {
				capture.MarkInterruptedWithReason("client_disconnected")
			}
			return outcome
		}
		if pc != nil {
			pc.append(line)
		}
		if capture != nil && strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			payload = strings.TrimSpace(payload)
			observeAnthropicPayload(capture, payload, clientModel, outboundModel)
		}
		if line == "\n" {
			flusher.Flush()
		}
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	if pc != nil {
		pc.append("data: [DONE]\n\n")
	}
	return outcome
}

// ConvertChatRequestToAnthropic is the live re-export of the Q2
// OpenAI→Anthropic request body converter used by the executor when
// an OpenAI-completions client must be routed to an
// anthropic-messages upstream.
func ConvertChatRequestToAnthropic(in []byte) ([]byte, error) {
	var src struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		MaxTokens       int     `json:"max_tokens"`
		Temperature     float64 `json:"temperature"`
		TopP            float64 `json:"top_p"`
		Stop            any     `json:"stop"`
		Stream          bool    `json:"stream"`
		System          string  `json:"system"`
		Tools           any     `json:"tools"`
		ToolChoice      any     `json:"tool_choice"`
		ReasoningEffort string  `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(in, &src); err != nil {
		return nil, fmt.Errorf("unmarshal chat request: %w", err)
	}
	out := map[string]any{
		"model":       src.Model,
		"max_tokens":  src.MaxTokens,
		"temperature": src.Temperature,
		"top_p":       src.TopP,
		"stream":      src.Stream,
	}
	if src.Stop != nil {
		out["stop_sequences"] = src.Stop
	}
	if len(src.Messages) > 0 {
		// OpenAI system message must become the Anthropic top-level "system" field.
		var systemParts []string
		var rest []map[string]any
		for _, m := range src.Messages {
			if m.Role == "system" {
				if s, ok := m.Content.(string); ok && s != "" {
					systemParts = append(systemParts, s)
				}
				continue
			}
			entry := map[string]any{"role": m.Role, "content": m.Content}
			rest = append(rest, entry)
		}
		if len(systemParts) > 0 {
			out["system"] = strings.Join(systemParts, "\n")
		}
		out["messages"] = rest
	}
	if src.ReasoningEffort != "" {
		// OpenAI's reasoning_effort is currently not a direct Anthropic
		// parameter; map to thinking budget if model supports it.
		// The executor decides per-model routing.
	}
	if src.Tools != nil {
		out["tools"] = src.Tools
	}
	if src.ToolChoice != nil {
		out["tool_choice"] = src.ToolChoice
	}
	return json.Marshal(out)
}

// ConvertAnthropicResponseToChat converts an Anthropic Messages
// response (non-stream) into OpenAI Chat Completions response.
// Used for Q3 (openai client <- anthropic upstream).
//
// Enhanced (2026-06-20): thinking blocks are preserved in the
// reasoning_content field (OpenAI o1-style extended thinking support).
func ConvertAnthropicResponseToChat(in []byte, clientModel string) ([]byte, error) {
	var src struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type      string         `json:"type"`
			Text      string         `json:"text"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Input     map[string]any `json:"input"`
			Thinking  string         `json:"thinking"`
			Signature string         `json:"signature"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(in, &src); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	outModel := src.Model
	if clientModel != "" {
		outModel = clientModel
	}
	var textParts []string
	var toolCalls []map[string]any
	var thinkingParts []string
	thinkingBlocks := 0
	for _, c := range src.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			}
		case "tool_use":
			argsJSON, err := json.Marshal(c.Input)
			if err != nil {
				slog.Warn("tool_use_marshal_failed",
					"error", err,
					"tool_use_id", c.ID,
					"tool_name", c.Name,
					"model", src.Model,
					"message_id", src.ID)
				continue
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(argsJSON),
				},
			})
		case "thinking":
			thinkingBlocks++
			if c.Thinking != "" {
				thinkingParts = append(thinkingParts, c.Thinking)
			}
		default:
			if c.Text != "" {
				textParts = append(textParts, c.Text)
			} else if c.Thinking != "" {
				thinkingParts = append(thinkingParts, c.Thinking)
			}
		}
	}
	msg := map[string]any{"role": "assistant"}
	if len(textParts) > 0 {
		msg["content"] = joinTextParts(textParts)
	} else if len(toolCalls) > 0 {
		msg["content"] = nil
	} else {
		msg["content"] = ""
	}
	if len(thinkingParts) > 0 {
		msg["reasoning_content"] = joinTextParts(thinkingParts)
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if len(textParts) == 0 && len(toolCalls) == 0 && len(thinkingParts) == 0 {
		return nil, fmt.Errorf("empty response from model %s: %d content blocks produced no extractable text/tool/thinking content",
			src.Model, len(src.Content))
	}
	finishReason := mapAnthropicFinishReasonToChat(src.StopReason)
	totalTokens := src.Usage.InputTokens + src.Usage.OutputTokens
	out := map[string]any{
		"id":      src.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   outModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     src.Usage.InputTokens,
			"completion_tokens": src.Usage.OutputTokens,
			"total_tokens":      totalTokens,
		},
	}
	if thinkingBlocks > 0 {
		reasoningContent, _ := msg["reasoning_content"].(string)
		out["_kxg_meta"] = map[string]any{
			"has_thinking":            true,
			"thinking_blocks_count":   thinkingBlocks,
			"reasoning_content_chars": len(reasoningContent),
		}
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal chat response: %w", err)
	}
	return result, nil
}

func joinTextParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

func mapAnthropicFinishReasonToChat(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}
