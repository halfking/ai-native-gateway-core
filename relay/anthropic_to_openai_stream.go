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
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
)

const sseDataPrefix = "data: "

// StreamAnthropicSSEToOpenAI converts Anthropic-format SSE upstream
// response to OpenAI-format SSE chunks written to w. Used in Q3 mode
// (openai-completions client -> anthropic-messages upstream, e.g.
// minimax /anthropic). Uses the IR layer for parsing and serializing,
// which eliminates format-specific code paths and reduces maintenance.
//
// IR-based flow:
//
//	Anthropic SSE → ir.ParseAnthropicStreamEvent() → ir.StreamChunk
//	             → ir.StreamChunk.SerializeOpenAI() → OpenAI SSE
//
// This handles:
//   - message_start → role prelude chunk
//   - content_block_delta.text → content delta
//   - content_block_delta.thinking → reasoning_content delta
//   - content_block_delta.input_json → tool_calls delta
//   - message_delta → finish_reason + usage
//   - message_stop → data: [DONE]
//   - pings and unknown events → dropped
//
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
		hasEmittedToolCalls bool
		bufferedToolArgs    strings.Builder
		currentToolCallID   string
		initialArgsSent     bool
	)

	// writeChunk writes a single OpenAI chunk to w and the capturer.
	writeChunk := func(chunk *ir.StreamChunk) {
		if chunk == nil {
			return
		}

		sseLine := chunk.SerializeOpenAI(chatID, chunkModel, createdAt)
		_, _ = io.WriteString(w, sseLine)
		flusher.Flush()

		if pc != nil {
			pc.append(sseLine)
		}

		if capture != nil {
			capture.ObserveChunk(chunk)
		}

		chunkCount++
	}

	// flushBufferedText emits the accumulated text content.
	// For minimax-style upstreams, splits <think>...</think> into reasoning_content.
	flushBufferedText := func() {
		if bufferedText.Len() == 0 {
			return
		}
		think, rest, ok := textsplit.SplitLeadingThink(bufferedText.String())
		if ok {
			if think != "" {
				chunk := &ir.StreamChunk{
					Type:           ir.ChunkTypeDelta,
					Delta:          &ir.StreamDelta{ReasoningContent: think},
					SourceProtocol: ir.ProtocolAnthropicMessages,
				}
				writeChunk(chunk)
			}
			if rest != "" {
				chunk := &ir.StreamChunk{
					Type:           ir.ChunkTypeDelta,
					Delta:          &ir.StreamDelta{Content: rest},
					SourceProtocol: ir.ProtocolAnthropicMessages,
				}
				writeChunk(chunk)
			}
		} else {
			chunk := &ir.StreamChunk{
				Type:           ir.ChunkTypeDelta,
				Delta:          &ir.StreamDelta{Content: bufferedText.String()},
				SourceProtocol: ir.ProtocolAnthropicMessages,
			}
			writeChunk(chunk)
		}
		bufferedText.Reset()
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
				flushBufferedText()
				// Emit usage if we have it
				if inputTokens > 0 || outputTokens > 0 {
					usageChunk := &ir.StreamChunk{
						Type: ir.ChunkTypeUsage,
						Usage: &ir.StreamUsage{
							PromptTokens:     inputTokens,
							CompletionTokens: outputTokens,
							TotalTokens:      inputTokens + outputTokens,
						},
						FinishReason:   "stop",
						SourceProtocol: ir.ProtocolAnthropicMessages,
					}
					writeChunk(usageChunk)
				}
				writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone, SourceProtocol: ir.ProtocolAnthropicMessages})
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

		// Early filter: drop OpenAI-format data leaking from upstream
		if isOpenAIFormatData(data) {
			slog.Warn("anthropic_to_openai: detected OpenAI-format data, dropping",
				"event_type", eventType,
				"data_preview", truncateForLog(string(data), 100),
				"request_id", requestID)
			continue
		}

		// ✅ Use IR parser
		chunk, err := ir.ParseAnthropicStreamEvent(eventType, data)
		if err != nil {
			slog.Warn("anthropic_to_openai: parse failed",
				"event_type", eventType, "error", err, "request_id", requestID)
			continue
		}

		// ✅ Use IR-based dispatch
		switch chunk.Type {
		case ir.ChunkTypeUsage:
			// message_start (input tokens) or message_delta (output tokens)
			if chunk.Usage != nil {
				if chunk.Usage.PromptTokens > 0 {
					inputTokens = chunk.Usage.PromptTokens
				}
				if chunk.Usage.CompletionTokens > 0 {
					outputTokens = chunk.Usage.CompletionTokens
				}
			}

			// Emit role prelude on first message_start
			if chunk.ID != "" && !emittedRole {
				roleChunk := &ir.StreamChunk{
					Type:           ir.ChunkTypeDelta,
					Delta:          &ir.StreamDelta{Role: "assistant"},
					SourceProtocol: ir.ProtocolAnthropicMessages,
				}
				writeChunk(roleChunk)
				emittedRole = true
			}

			// Update finish reason if present (from message_delta)
			if chunk.FinishReason != "" {
				fr := chunk.FinishReason
				finishReason = &fr
			}

		case ir.ChunkTypeDelta:
			// Determine which Anthropic event this came from by re-checking
			// (the IR chunk doesn't preserve the original event name)
			var baseCheck struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &baseCheck); err == nil {
				switch baseCheck.Type {
				case "content_block_start":
					// Tool use block start
					var evt sseAnthropicContentBlockStart
					if err := json.Unmarshal(data, &evt); err == nil && evt.ContentBlock.Type == "tool_use" {
						currentToolCallID = evt.ContentBlock.ID
						// Only emit initial tool call if input is not empty
						if len(evt.ContentBlock.InputRaw) > 0 && string(evt.ContentBlock.InputRaw) != "{}" {
							args := string(evt.ContentBlock.InputRaw)
							chunk := buildToolCallChunk(toolCallIndex, evt.ContentBlock.ID, evt.ContentBlock.Name, &args, true)
							writeChunk(chunk)
							toolCallIndex++
							initialArgsSent = true
						} else {
							chunk := buildToolCallChunk(toolCallIndex, evt.ContentBlock.ID, evt.ContentBlock.Name, nil, false)
							writeChunk(chunk)
							toolCallIndex++
							initialArgsSent = false
						}
						hasEmittedToolCalls = true
					}

				case "content_block_delta":
					// Text, thinking, or input_json_delta
					var evt struct {
						Index int `json:"index"`
						Delta struct {
							Type        string `json:"type"`
							Text        string `json:"text"`
							Thinking    string `json:"thinking"`
							PartialJSON string `json:"partial_json"`
						} `json:"delta"`
					}
					if err := json.Unmarshal(data, &evt); err == nil {
						switch evt.Delta.Type {
						case "text", "text_delta":
							// Accumulate text for <think>...</think> split
							bufferedText.WriteString(evt.Delta.Text)

						case "thinking", "thinking_delta":
							// Emit reasoning_content immediately
							chunk := &ir.StreamChunk{
								Type:           ir.ChunkTypeDelta,
								Delta:          &ir.StreamDelta{ReasoningContent: evt.Delta.Thinking},
								SourceProtocol: ir.ProtocolAnthropicMessages,
							}
							writeChunk(chunk)

						case "input_json_delta":
							// Only accumulate if we haven't sent initial args
							if !initialArgsSent && evt.Delta.PartialJSON != "" {
								bufferedToolArgs.WriteString(evt.Delta.PartialJSON)
							}

						default:
							slog.Warn("unknown_delta_type_in_stream",
								"delta_type", evt.Delta.Type,
								"has_text", evt.Delta.Text != "",
								"has_thinking", evt.Delta.Thinking != "",
								"request_id", requestID)
							if evt.Delta.Text != "" {
								bufferedText.WriteString(evt.Delta.Text)
							} else if evt.Delta.Thinking != "" {
								chunk := &ir.StreamChunk{
									Type:           ir.ChunkTypeDelta,
									Delta:          &ir.StreamDelta{ReasoningContent: evt.Delta.Thinking},
									SourceProtocol: ir.ProtocolAnthropicMessages,
								}
								writeChunk(chunk)
							}
						}
					}

				case "content_block_stop":
					// Flush accumulated text
					flushBufferedText()
					// Flush accumulated tool args if needed
					if !initialArgsSent && bufferedToolArgs.Len() > 0 {
						args := bufferedToolArgs.String()
						chunk := buildToolCallChunk(toolCallIndex-1, currentToolCallID, "", &args, true)
						writeChunk(chunk)
						bufferedToolArgs.Reset()
					}
					currentToolCallID = ""
					initialArgsSent = false

				case "message_start", "message_delta":
					// Already handled in ChunkTypeUsage
				}
			}

		case ir.ChunkTypeDone:
			// Flush any text accumulated
			flushBufferedText()

			// 2026-06-23 fix: Detect inconsistent finish_reason
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

			// Emit final chunk with finish_reason
			fr := "stop"
			if finishReason != nil {
				fr = *finishReason
			}
			finalChunk := &ir.StreamChunk{
				Type:           ir.ChunkTypeDelta,
				Delta:          &ir.StreamDelta{},
				FinishReason:   fr,
				SourceProtocol: ir.ProtocolAnthropicMessages,
			}
			writeChunk(finalChunk)

			// Emit usage chunk
			if inputTokens > 0 || outputTokens > 0 {
				usageChunk := &ir.StreamChunk{
					Type: ir.ChunkTypeUsage,
					Usage: &ir.StreamUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      inputTokens + outputTokens,
					},
					FinishReason:   fr,
					SourceProtocol: ir.ProtocolAnthropicMessages,
				}
				writeChunk(usageChunk)
			}

			// Emit [DONE]
			writeChunk(&ir.StreamChunk{Type: ir.ChunkTypeDone, SourceProtocol: ir.ProtocolAnthropicMessages})
			return StreamOutcome{ChunkCount: chunkCount}

		case ir.ChunkTypeError:
			if capture != nil {
				capture.MarkInterruptedWithReason("upstream_error")
			}
			if chunk.Error != nil {
				emitErrorChunk(w, chunk.Error.Type, chunk.Error.Message, flusher)
			}
			outcome.Interrupted = true
			outcome.Reason = "upstream_error"
			outcome.ChunkCount = chunkCount
			return outcome
		}
	}
}

// sseAnthropicContentBlockStart represents the content_block_start event structure.
type sseAnthropicContentBlockStart struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type     string          `json:"type"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		InputRaw json.RawMessage `json:"input"`
	} `json:"content_block"`
}

// buildToolCallChunk creates an IR chunk for a tool call delta.
func buildToolCallChunk(index int, id, name string, args *string, hasArgs bool) *ir.StreamChunk {
	tc := ir.StreamToolCallDelta{
		Index: index,
		ID:    id,
		Type:  "function",
		Name:  name,
	}
	if hasArgs && args != nil {
		tc.Arguments = *args
	}
	return &ir.StreamChunk{
		Type:           ir.ChunkTypeDelta,
		Delta:          &ir.StreamDelta{ToolCalls: []ir.StreamToolCallDelta{tc}},
		SourceProtocol: ir.ProtocolAnthropicMessages,
	}
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

// readSSEEvent reads one SSE event.
func readSSEEvent(_ context.Context, reader io.Reader, _ streamRuntimeConfig) (eventType string, data []byte, err error) {
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
				continue
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
// Messages streaming event types.
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
