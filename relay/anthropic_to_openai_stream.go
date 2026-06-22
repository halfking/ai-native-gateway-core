package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
)

const sseDataPrefix = "data: "

// sseAnthropicEvent is the minimal subset of Anthropic Messages streaming
// events the translator needs to recognise. Unknown event types are
// dropped (not forwarded) so the OpenAI client only sees OpenAI-shape
// chunks.
type sseAnthropicEvent struct {
	Type    string          `json:"type"`
	Index   int             `json:"index,omitempty"`
	Delta   json.RawMessage `json:"delta,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type sseAnthropicDelta struct {
	Type        string          `json:"type,omitempty"`
	Text        string          `json:"text,omitempty"`
	Thinking    string          `json:"thinking,omitempty"`
	InputJSON   json.RawMessage `json:"input_json,omitempty"`
	PartialJSON string          `json:"partial_json,omitempty"` // For input_json_delta
	StopReason  *string         `json:"stop_reason,omitempty"`
}

// StreamAnthropicSSEToOpenAI converts Anthropic-format SSE upstream
// response to OpenAI-format SSE chunks written to w. Used in Q3 mode
// (openai-completions client -> anthropic-messages upstream, e.g.
// minimax /anthropic). Maps message_start -> role prelude chunk;
// content_block_delta.text -> content delta; content_block_delta.thinking
// -> reasoning_content delta; content_block_delta.input_json ->
// tool_calls delta; message_delta -> finish_reason + usage;
// message_stop -> data: [DONE]. Pings and unknown events are dropped.
// For minimax-style upstreams that pack the reasoning trace inside
// the text block as `<think>...</think>`, the function probes the
// running text prefix and splits the leading think tag into
// reasoning_content, leaving the rest as content. First-byte and
// per-chunk timeouts from stream_runtime.go apply.
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
		// Capturer finalise mirrors StreamChatWithPendingCapture and the
		// other Anthropic stream paths (Track C C5, 2026-06-21).
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

	chatID := "chatcmpl-" + requestID
	if requestID == "" {
		chatID = "chatcmpl-anthropic-openai"
	}
	createdAt := time.Now().Unix()
	chunkModel := clientModel
	if chunkModel == "" {
		chunkModel = outboundModel
	}

	var (
		ctx                 context.Context
		inputTokens         int
		outputTokens        int
		finishReason        *string
		toolCallIndex       int
		emittedRole         bool
		chunkCount          int
		bufferedText        strings.Builder
		hasEmittedToolCalls bool            // Track whether any tool_calls were actually emitted
		bufferedToolArgs    strings.Builder // Accumulate input_json_delta partial_json
		currentToolCallID   string          // Cache the current tool_use block ID for delta updates
		initialArgsSent     bool            // Track if we sent initial args from content_block_start
	)

	// emit a single OpenAI chat.completion.chunk; clear finishReason
	// after each emit so subsequent chunks don't repeat it.
	// emitChunk writes a single OpenAI chat.completion.chunk to w and
	// (Track C C5, 2026-06-21) appends the same bytes to the
	// pending-store capturer so the gateway can replay them on
	// reconnect. The chunkCount is incremented here so all write
	// paths (regular emit, [DONE] sentinel) flow through the same
	// capturer-aware write.
	emitChunk := func(payload []byte) {
		_, _ = w.Write([]byte(sseDataPrefix))
		_, _ = w.Write(payload)
		_, _ = w.Write([]byte("\n\n"))
		if pc != nil {
			pc.append(sseDataPrefix)
			pc.append(string(payload))
			pc.append("\n\n")
		}
		// Capture the OpenAI-format chunk for audit trail
		if capture != nil {
			// Extract finish_reason from the chunk if present
			var finishReasonFromChunk string
			if string(payload) == "[DONE]" {
				finishReasonFromChunk = ""
			} else {
				// Try to extract finish_reason from JSON
				var chunk map[string]any
				if json.Unmarshal(payload, &chunk) == nil {
					if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
						if choice, ok := choices[0].(map[string]any); ok {
							if fr, ok := choice["finish_reason"].(string); ok {
								finishReasonFromChunk = fr
							}
						}
					}
				}
			}
			isDone := string(payload) == "[DONE]"
			capture.ObservePayload(string(payload), finishReasonFromChunk, isDone)
		}
		flusher.Flush()
		chunkCount++
	}
	emit := func(deltaContent, deltaReasoning string, toolCalls json.RawMessage) {
		c := anthropicToOpenAIChunk{
			ID: chatID, Object: "chat.completion.chunk",
			Created: createdAt, Model: chunkModel,
		}
		c.Choices = append(c.Choices, struct {
			Index int `json:"index"`
			Delta struct {
				Role             string          `json:"role,omitempty"`
				Content          string          `json:"content,omitempty"`
				ReasoningContent string          `json:"reasoning_content,omitempty"`
				ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		}{})
		c.Choices[0].Index = 0
		if !emittedRole {
			c.Choices[0].Delta.Role = "assistant"
			emittedRole = true
		}
		if deltaContent != "" {
			c.Choices[0].Delta.Content = deltaContent
		}
		if deltaReasoning != "" {
			c.Choices[0].Delta.ReasoningContent = deltaReasoning
		}
		if toolCalls != nil {
			c.Choices[0].Delta.ToolCalls = toolCalls
		}
		c.Choices[0].FinishReason = finishReason
		body, _ := json.Marshal(c)
		emitChunk(body)
		finishReason = nil
	}

	emitUsage := func() {
		if inputTokens == 0 && outputTokens == 0 {
			return
		}
		// Record usage in capture for audit trail
		if capture != nil {
			capture.ObserveUsage(&inputTokens, &outputTokens, nil, nil)
		}
		c := anthropicToOpenAIChunk{
			ID: chatID, Object: "chat.completion.chunk",
			Created: createdAt, Model: chunkModel,
		}
		// OpenAI streaming spec requires a non-empty choices array in every chunk.
		// Emit an empty delta {} so the choice object is well-formed even when
		// only usage is being reported. Without this, some clients encounter
		// "list index out of range" when choices is serialized as [].
		choice := struct {
			Index int `json:"index"`
			Delta struct {
				Role             string          `json:"role,omitempty"`
				Content          string          `json:"content,omitempty"`
				ReasoningContent string          `json:"reasoning_content,omitempty"`
				ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		}{}
		choice.Index = 0
		c.Choices = append(c.Choices, choice)
		c.Usage = &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{PromptTokens: inputTokens, CompletionTokens: outputTokens, TotalTokens: inputTokens + outputTokens}
		body, _ := json.Marshal(c)
		emitChunk(body)
	}

	emitRolePrelude := func() {
		if emittedRole {
			return
		}
		c := anthropicToOpenAIChunk{
			ID: chatID, Object: "chat.completion.chunk",
			Created: createdAt, Model: chunkModel,
		}
		c.Choices = append(c.Choices, struct {
			Index int `json:"index"`
			Delta struct {
				Role             string          `json:"role,omitempty"`
				Content          string          `json:"content,omitempty"`
				ReasoningContent string          `json:"reasoning_content,omitempty"`
				ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		}{})
		c.Choices[0].Index = 0
		c.Choices[0].Delta.Role = "assistant"
		if inputTokens > 0 {
			c.Usage = &struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: inputTokens, CompletionTokens: 0, TotalTokens: inputTokens}
		}
		body, _ := json.Marshal(c)
		emitChunk(body)
		emittedRole = true
	}

	emitToolCall := func(toolID, toolName string, args json.RawMessage) {
		if toolID == "" && toolName == "" && len(args) == 0 {
			return
		}
		hasEmittedToolCalls = true // Mark that we've emitted at least one tool call
		idx := toolCallIndex
		toolCallIndex++
		entry := map[string]any{"index": idx}
		// 2026-06-23 fix: Always include an ID field. If toolID is empty
		// (which can happen when input_json_delta arrives without a
		// content_block_start), generate a fallback ID to satisfy OpenAI
		// clients that require "id" to be a string.
		if toolID != "" {
			entry["id"] = toolID
		} else {
			// Fallback: generate a synthetic tool call ID
			entry["id"] = fmt.Sprintf("call_%s_%d", requestID, idx)
		}
		if toolName != "" {
			entry["type"] = "function"
			funcMap := map[string]any{"name": toolName}
			// 2026-06-23 fix: Only include arguments if non-empty.
			// Omit the field entirely for the initial chunk (ID+name only),
			// then send arguments in subsequent deltas. This prevents
			// clients from concatenating "{}"+"{real}" → invalid JSON.
			if len(args) > 0 && string(args) != "{}" {
				funcMap["arguments"] = string(args)
			}
			entry["function"] = funcMap
		} else if len(args) > 0 && string(args) != "{}" {
			// Only arguments, no name (incremental delta)
			entry["function"] = map[string]any{"arguments": string(args)}
		}
		arr, _ := json.Marshal([]map[string]any{entry})
		emit("", "", arr)
	}

	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	runtimeCfg := currentStreamRuntimeConfig()
	reader := bufio.NewReaderSize(resp.Body, streamBufSize)
	for {
		eventType, data, err := readSSEEvent(ctx, reader, runtimeCfg)
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				emitUsage()
				emitChunk([]byte("[DONE]"))
				return StreamOutcome{ChunkCount: chunkCount}
			}
			if capture != nil {
				capture.MarkInterruptedWithReason("anthropic_to_openai_read_error")
			}
			emitErrorChunk(w, "stream_read_error", err.Error(), flusher)
			outcome.Interrupted = true
			outcome.Reason = "read_error"
			outcome.ChunkCount = chunkCount
			return outcome
		}

		if eventType == "" || len(data) == 0 {
			continue
		}

		// 2026-06-21 enhancement: Early coarse filter to drop OpenAI-format
		// data before JSON parsing. This catches the glm-5.2 empty choices
		// issue confirmed in production testing where the upstream sends
		// {"choices":[],"usage":{...}} blocks at stream end.
		// See openai_format_detector.go for detection logic.
		if isOpenAIFormatData(data) {
			slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
				"event_type", eventType,
				"data_preview", truncateForLog(string(data), 100),
				"request_id", requestID)
			continue
		}

		var ev sseAnthropicEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			slog.Warn("anthropic_to_openai: malformed event JSON",
				"event_type", eventType, "error", err, "request_id", requestID)
			continue
		}

		// 2026-06-21 fix: Some anthropic-compatible upstreams (notably
		// glm-5.2-oneday at https://api.supxh.xin) leak OpenAI-format
		// chunks into the Anthropic SSE stream. The mixed chunks have
		// `event:` prefixes (so they survive the empty-event filter
		// above) but carry OpenAI fields like {"choices":[],"model":"x"}.
		// Without this guard, they pass the switch below, fall through
		// every Anthropic case, and get emitted as-is — producing empty
		// choices[] chunks that crash OpenAI streaming clients with
		// "list index out of range".
		//
		// Drop any event whose `type` field is not a known Anthropic
		// event name. Unknown event types are already silently dropped
		// by the switch below, but the OpenAI-shaped data still
		// pollutes the client stream because the empty default case
		// emits nothing — however the upstream-sent OpenAI data is
		// already on the wire by the time the case falls through.
		// The real fix is to filter at the SSE layer: if the JSON does
		// not carry an Anthropic-shaped top-level `type` field, drop
		// the event before any emit.
		if !isKnownAnthropicEventType(ev.Type) {
			slog.Warn("anthropic_to_openai: dropping non-Anthropic event from upstream",
				"event_type", eventType,
				"ev_type", ev.Type,
				"request_id", requestID,
				"data_preview", truncateForLog(string(data), 300))
			continue
		}

		// 2026-06-21 debug: detect upstream sending OpenAI-format chunks
		// instead of Anthropic events. Some providers (e.g. glm-5.2) may
		// mix formats, causing empty choices[] chunks to leak through.
		if ev.Type == "" {
			// Check if this is an OpenAI-format chunk leaked from upstream
			var oaiCheck struct {
				Choices []any  `json:"choices"`
				ID      string `json:"id"`
				Created int64  `json:"created"`
			}
			if err := json.Unmarshal(data, &oaiCheck); err == nil {
				if oaiCheck.Choices != nil || oaiCheck.ID != "" || oaiCheck.Created > 0 {
					slog.Warn("anthropic_to_openai: upstream sent OpenAI-format chunk, skipping",
						"has_choices", oaiCheck.Choices != nil,
						"choices_len", len(oaiCheck.Choices),
						"id", oaiCheck.ID,
						"created", oaiCheck.Created,
						"request_id", requestID)
					continue
				}
			}
		}

		switch ev.Type {
		case "message_start":
			var msg struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(ev.Message, &msg); err == nil {
				if msg.Usage.InputTokens > 0 {
					inputTokens = msg.Usage.InputTokens
				}
			}
			emitRolePrelude()

		case "content_block_start":
			var evt struct {
				Type         string `json:"type"`
				Index        int    `json:"index"`
				ContentBlock struct {
					Type     string          `json:"type"`
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					InputRaw json.RawMessage `json:"input"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal(data, &evt); err == nil && evt.ContentBlock.Type == "tool_use" {
				// Cache the tool_use ID for subsequent delta updates
				currentToolCallID = evt.ContentBlock.ID
				// 2026-06-23 fix: Only emit initial tool call if input is not empty.
				// Anthropic often sends input:{} at content_block_start, then sends
				// the actual arguments via input_json_delta. Emitting {} causes
				// clients to concatenate "{}"+"{real_args}" → invalid JSON.
				// Skip empty input and let input_json_delta handle it.
				if len(evt.ContentBlock.InputRaw) > 0 && string(evt.ContentBlock.InputRaw) != "{}" {
					emitToolCall(evt.ContentBlock.ID, evt.ContentBlock.Name, evt.ContentBlock.InputRaw)
					initialArgsSent = true // Mark that we sent initial args
				} else {
					// Emit tool call with ID and name only, no arguments yet
					emitToolCall(evt.ContentBlock.ID, evt.ContentBlock.Name, nil)
					initialArgsSent = false // Will accumulate args from input_json_delta
				}
			}

		case "content_block_delta":
			var d sseAnthropicDelta
			if err := json.Unmarshal(ev.Delta, &d); err != nil {
				continue
			}
			switch d.Type {
			case "text", "text_delta":
				// Accumulate all text deltas in a single buffer. The
				// <think>...</think> split is performed on content_block_stop
				// when the full text is available — see below. This avoids
				// the cross-chunk probe problem (a single character at a
				// time is not enough to detect the leading think tag).
				// Claude Opus 4-8 uses "text_delta" instead of "text"
				bufferedText.WriteString(d.Text)

			case "thinking":
				emit("", d.Thinking, nil)

			case "input_json":
				// 2026-06-23 safety: Only emit if we haven't sent initial args
				if !initialArgsSent {
					emitToolCall(currentToolCallID, "", d.InputJSON)
				}

			case "input_json_delta":
				// 2026-06-23 safety: Only accumulate if we haven't sent initial args.
				// If content_block_start had non-empty input, we already sent it,
				// so ignore any subsequent input_json_delta to prevent duplication.
				if !initialArgsSent && d.PartialJSON != "" {
					bufferedToolArgs.WriteString(d.PartialJSON)
				}

			default:
				// Handle unknown delta types
				slog.Warn("unknown_delta_type_in_stream",
					"delta_type", d.Type,
					"has_text", d.Text != "",
					"has_thinking", d.Thinking != "",
					"request_id", requestID)

				// Try to extract text or thinking field
				if d.Text != "" {
					bufferedText.WriteString(d.Text)
				} else if d.Thinking != "" {
					emit("", d.Thinking, nil)
				}
			}

		case "content_block_stop":
			// Flush the accumulated text. If the upstream packed a
			// leading <think>...</think> (the minimax convention),
			// split it into reasoning_content + content so SDK clients
			// can render the trace separately.
			if bufferedText.Len() > 0 {
				think, rest, ok := textsplit.SplitLeadingThink(bufferedText.String())
				if ok {
					emit("", think, nil)
					if rest != "" {
						emit(rest, "", nil)
					}
				} else {
					emit(bufferedText.String(), "", nil)
				}
				bufferedText.Reset()
			}
			// 2026-06-23 safety: Only flush accumulated tool arguments if we
			// didn't send initial args from content_block_start. This prevents
			// sending arguments twice if Anthropic sends both non-empty input
			// and input_json_delta (defensive, shouldn't happen in practice).
			if !initialArgsSent && bufferedToolArgs.Len() > 0 {
				emitToolCall(currentToolCallID, "", json.RawMessage(bufferedToolArgs.String()))
				bufferedToolArgs.Reset()
			}
			// Clear the cached tool ID and reset state when content block ends
			currentToolCallID = ""
			initialArgsSent = false

		case "message_delta":
			var d sseAnthropicDelta
			if err := json.Unmarshal(ev.Delta, &d); err == nil && d.StopReason != nil {
				sr := mapAnthropicFinishReasonToChat(*d.StopReason) // Anthropic → OpenAI mapping
				finishReason = &sr
			}
			var u struct {
				OutputTokens int `json:"output_tokens"`
			}
			if err := json.Unmarshal(ev.Usage, &u); err == nil {
				outputTokens = u.OutputTokens
			}

		case "message_stop":
			// Flush any text accumulated without a content_block_stop
			// (e.g. shorter upstreams that skip the stop event).
			if bufferedText.Len() > 0 {
				think, rest, ok := textsplit.SplitLeadingThink(bufferedText.String())
				if ok {
					emit("", think, nil)
					if rest != "" {
						emit(rest, "", nil)
					}
				} else {
					emit(bufferedText.String(), "", nil)
				}
				bufferedText.Reset()
			}
			if finishReason == nil {
				def := "stop"
				finishReason = &def
			}
			// 2026-06-23 fix: Detect inconsistent finish_reason when model
			// indicates tool_calls but never emitted any tool_use blocks.
			// This happens with some Claude models (e.g. claude-sonnet-4-6)
			// where the model returns stop_reason:"tool_use" but sends no
			// content_block_start(tool_use) event. Clients would wait forever
			// for tool_calls data that never arrives. Correct to "stop" and log.
			if finishReason != nil && *finishReason == "tool_calls" && !hasEmittedToolCalls {
				slog.Warn("inconsistent_tool_calls_finish_reason",
					"request_id", requestID,
					"model", clientModel,
					"prompt_tokens", inputTokens,
					"completion_tokens", outputTokens,
					"action", "correcting_to_stop",
					"original_finish_reason", "tool_calls")
				stop := "stop"
				finishReason = &stop
			}
			emit("", "", nil) // emit finish_reason + clear
			emitUsage()
			emitChunk([]byte("[DONE]"))
			return StreamOutcome{ChunkCount: chunkCount}

		case "ping":
			// OpenAI SSE has no equivalent — drop.

		case "error":
			if capture != nil {
				capture.MarkInterruptedWithReason("upstream_error")
			}
			var e struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(ev.Error, &e)
			if e.Type == "" {
				e.Type = "upstream_error"
			}
			emitErrorChunk(w, e.Type, e.Message, flusher)
			outcome.Interrupted = true
			outcome.Reason = "upstream_error"
			outcome.ChunkCount = chunkCount
			return outcome
		}
	}
}

type anthropicToOpenAIChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string          `json:"role,omitempty"`
			Content          string          `json:"content,omitempty"`
			ReasoningContent string          `json:"reasoning_content,omitempty"`
			ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

func emitErrorChunk(w http.ResponseWriter, code, message string, flusher http.Flusher) {
	errBody := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	body, _ := json.Marshal(errBody)
	_, _ = w.Write([]byte(sseDataPrefix))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n\n"))
	_, _ = w.Write([]byte(sseDataPrefix + "[DONE]\n\n"))
	flusher.Flush()
}

// readSSEEvent reads one SSE event. Returns ("", nil, nil) for
// keep-alive / blank-line separators so the caller can loop.
// Implementation uses a local bufio.Scanner with ScanLines so it is
// independent of readLineWithTimeout's timeout machinery — that
// machinery was tripping unit tests where the upstream body is a
// short in-memory io.NopCloser(strings.NewReader(...)) that needs
// to drain synchronously.
func readSSEEvent(_ context.Context, reader io.Reader, _ streamRuntimeConfig) (eventType string, data []byte, err error) {
	// v2.0.4 fix: previously used bufio.NewScanner(reader) on each call,
	// which created a NEW scanner every invocation. The scanner's
	// internal buffer pre-read bytes beyond the current event, and those
	// bytes were lost when the next call created a new scanner.
	//
	// Fix: use the passed-in *bufio.Reader directly via ReadString('\n').
	// This preserves the reader's internal buffer across calls.
	br, ok := reader.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(reader)
	}
	var dataLines []string
	for {
		line, rerr := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				if rerr != nil {
					return eventType, nil, rerr
				}
				continue // skip blank lines between events
			}
			return eventType, []byte(strings.Join(dataLines, "\n")), nil
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.HasPrefix(line, ":"):
			// SSE comment (heartbeat) — ignore.
		}
		if rerr != nil {
			if len(dataLines) > 0 {
				return eventType, []byte(strings.Join(dataLines, "\n")), nil
			}
			return eventType, nil, io.EOF
		}
	}
}

// isKnownAnthropicEventType returns true if t is one of the Anthropic
// Messages streaming event types. Used to filter out non-Anthropic
// payloads that some upstreams (e.g. glm-5.2-oneday) leak into their
// Anthropic SSE stream. See the call site for full context.
//
// Reference (Anthropic Messages streaming spec):
//
//	message_start, message_delta, message_stop, content_block_start,
//	content_block_delta, content_block_stop, ping, error
func isKnownAnthropicEventType(t string) bool {
	switch t {
	case "message_start",
		"message_delta",
		"message_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"ping",
		"error":
		return true
	}
	return false
}

var _ = fmt.Sprintf
