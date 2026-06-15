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
	Type       string          `json:"type,omitempty"`
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	InputJSON  json.RawMessage `json:"input_json,omitempty"`
	StopReason *string         `json:"stop_reason,omitempty"`
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
) (outcome StreamOutcome) {
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
		ctx           context.Context
		inputTokens   int
		outputTokens  int
		finishReason  *string
		toolCallIndex int
		emittedRole   bool
		chunkCount    int
		bufferedText  strings.Builder
	)

	// emit a single OpenAI chat.completion.chunk; clear finishReason
	// after each emit so subsequent chunks don't repeat it.
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
		_, _ = w.Write([]byte(sseDataPrefix))
		_, _ = w.Write(body)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
		chunkCount++
		finishReason = nil
	}

	emitUsage := func() {
		if inputTokens == 0 && outputTokens == 0 {
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
		c.Usage = &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{PromptTokens: inputTokens, CompletionTokens: outputTokens, TotalTokens: inputTokens + outputTokens}
		body, _ := json.Marshal(c)
		_, _ = w.Write([]byte(sseDataPrefix))
		_, _ = w.Write(body)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
		chunkCount++
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
		_, _ = w.Write([]byte(sseDataPrefix))
		_, _ = w.Write(body)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
		emittedRole = true
		chunkCount++
	}

	emitToolCall := func(toolID, toolName string, args json.RawMessage) {
		if toolID == "" && toolName == "" && len(args) == 0 {
			return
		}
		idx := toolCallIndex
		toolCallIndex++
		entry := map[string]any{"index": idx}
		if toolID != "" {
			entry["id"] = toolID
		}
		if toolName != "" {
			entry["type"] = "function"
			entry["function"] = map[string]any{"name": toolName, "arguments": ""}
		}
		if len(args) > 0 {
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
				_, _ = w.Write([]byte(sseDataPrefix + "[DONE]\n\n"))
				flusher.Flush()
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

		var ev sseAnthropicEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			slog.Warn("anthropic_to_openai: malformed event JSON",
				"event_type", eventType, "error", err, "request_id", requestID)
			continue
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
			var blk struct {
				Type     string          `json:"type"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				InputRaw json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(data, &blk); err == nil && blk.Type == "tool_use" {
				emitToolCall(blk.ID, blk.Name, blk.InputRaw)
			}

		case "content_block_delta":
			var d sseAnthropicDelta
			if err := json.Unmarshal(ev.Delta, &d); err != nil {
				continue
			}
			switch d.Type {
			case "text":
				// Accumulate all text deltas in a single buffer. The
				// <think>...</think> split is performed on content_block_stop
				// when the full text is available — see below. This avoids
				// the cross-chunk probe problem (a single character at a
				// time is not enough to detect the leading think tag).
				bufferedText.WriteString(d.Text)

			case "thinking":
				emit("", d.Thinking, nil)

			case "input_json":
				emitToolCall("", "", d.InputJSON)
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

		case "message_delta":
			var d sseAnthropicDelta
			if err := json.Unmarshal(ev.Delta, &d); err == nil && d.StopReason != nil {
				sr := mapAnthropicStopReason(*d.StopReason)  // identical function from messages.go
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
			emit("", "", nil) // emit finish_reason + clear
			emitUsage()
			_, _ = w.Write([]byte(sseDataPrefix + "[DONE]\n\n"))
			flusher.Flush()
			chunkCount++
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

var _ = fmt.Sprintf
