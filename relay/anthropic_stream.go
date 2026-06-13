package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
)

func StreamAnthropicSSE(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture) (outcome StreamOutcome) {
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic stream panic recovered", "panic", r, "stack", string(debug.Stack()), "request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
		}
	}()
	runtimeCfg := currentStreamRuntimeConfig()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return StreamOutcome{}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	msgID := "msg_"
	if len(requestID) > 24 {
		msgID += requestID[:24]
	} else if requestID != "" {
		msgID += requestID
	}

	initialMsg := map[string]any{
		"type":  "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         clientModel,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}
	writeSSE(w, "message_start", initialMsg)
	flusher.Flush()

	blockStart := map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}
	writeSSE(w, "content_block_start", blockStart)
	flusher.Flush()

	writeSSE(w, "ping", map[string]any{"type": "ping"})
	flusher.Flush()

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	reader := bufio.NewReaderSize(resp.Body, streamBufSize)
	lastSend := time.Now()

	finalFinishReason := ""
	outputTokens := 0
	inputTokens := 0

	// Buffered first text block (Phase 4): deltas accumulate here so we
	// can detect an embedded `<think>...</think>` and rewrite the SSE
	// stream as [thinking, text] at flush time.
	var bufferedText strings.Builder

	// First-byte timeout
	firstLine, err := readLineWithTimeout(ctx, reader, runtimeCfg.firstByteTimeout)
	if err != nil {
		if capture != nil {
			capture.MarkInterruptedWithReason("first_byte_timeout")
		}
		slog.Warn("anthropic stream first-byte timeout", "error", err)
		errPayload := map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "timeout", "message": "upstream first-byte timeout"},
		}
		writeSSE(w, "error", errPayload)
		flusher.Flush()
		writeAnthropicTail(w, flusher, msgID, clientModel, finalFinishReason, outputTokens)
		outcome.Interrupted = true
		outcome.Reason = "first_byte_timeout"
		return outcome
	}

	processLine := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			return
		}
		data := line[6:]
		if data == "[DONE]" {
			return
		}

		var chunk map[string]json.RawMessage
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return
		}

		if raw, ok := chunk["usage"]; ok {
			var usage map[string]any
			if json.Unmarshal(raw, &usage) == nil {
				if v, ok := usage["prompt_tokens"].(float64); ok {
					inputTokens = int(v)
				}
				if v, ok := usage["completion_tokens"].(float64); ok {
					outputTokens = int(v)
				}
			}
			if capture != nil {
				pt := inputTokens
				ct := outputTokens
				capture.ObserveUsage(&pt, &ct, nil, nil)
			}
		}

		var choices []map[string]any
		if raw, ok := chunk["choices"]; ok {
			json.Unmarshal(raw, &choices)
		}
		if len(choices) == 0 {
			return
		}

		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			finalFinishReason = fr
		}

		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			return
		}

		textDelta, _ := delta["content"].(string)
		if textDelta != "" {
			// Phase 4 of 4: buffer text deltas so we can split
			// `<think>...</think>` (minimax upstream) into independent
			// thinking + text blocks at flush time. See flushBufferedText.
			bufferedText.WriteString(textDelta)
			lastSend = time.Now()
			if capture != nil {
				capture.ObservePayload(fmt.Sprintf(`{"type":"content_block_delta","text":%q}`, textDelta), "", false)
			}
		}

		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for i, tc := range toolCalls {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				idx := i + 1
				fn, _ := tcMap["function"].(map[string]any)
				fnName := ""
				if fn != nil {
					fnName, _ = fn["name"].(string)
				}
				tcID, _ := tcMap["id"].(string)

				startEvent := map[string]any{
					"type":  "content_block_start",
					"index": idx,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    tcID,
						"name":  fnName,
						"input": map[string]any{},
					},
				}
				writeSSE(w, "content_block_start", startEvent)

				if fn != nil {
					args, _ := fn["arguments"].(string)
					if args != "" {
						argEvent := map[string]any{
							"type":  "content_block_delta",
							"index": idx,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
						}
						writeSSE(w, "content_block_delta", argEvent)
					}
				}
				lastSend = time.Now()
			}
		}
	}

	if firstLine != "" {
		processLine(firstLine)
	}

	for {
		readResult := readNextStreamLine(ctx, reader, w, &lastSend, runtimeCfg)
		if readResult.err != nil {
			switch readResult.state {
			case streamReadCanceled:
				slog.Debug("anthropic stream client disconnected")
				if capture != nil {
					capture.MarkInterruptedWithReason("client_disconnected")
				}
				outcome.Interrupted = true
				outcome.Reason = "client_cancel"
			case streamReadEOF:
			case streamReadTimeout:
				slog.Warn("anthropic stream read timeout", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				errPayload := map[string]any{
					"type":  "error",
					"error": map[string]any{"type": "timeout", "message": "upstream read timeout"},
				}
				writeSSE(w, "error", errPayload)
				flusher.Flush()
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
			default:
				slog.Warn("anthropic stream read error", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_error")
				}
				errPayload := map[string]any{
					"type":  "error",
					"error": map[string]any{"type": "upstream_error", "message": fmt.Sprintf("stream read error: %v", readResult.err)},
				}
				writeSSE(w, "error", errPayload)
				flusher.Flush()
				outcome.Interrupted = true
				outcome.Reason = "read_error"
			}
			break
		}

		line := readResult.line

		if line == "" {
			continue
		}
		processLine(line)
	}

	// Phase 4 split: flush the buffered text content (split into
	// thinking + text if the upstream embedded `<think>...</think>`).
	flushBufferedText(w, flusher, bufferedText.String(), capture)

	writeAnthropicTail(w, flusher, msgID, clientModel, finalFinishReason, outputTokens)

	// Only mark the capture as "done" if the stream was NOT interrupted.
	// If we received an interruption (e.g. stream_timeout, read_error,
	// client cancel), MarkInterruptedWithReason has already set
	// interrupted=true and finalFinish to the failure reason — calling
	// ObservePayload with done=true here would clobber the failure reason
	// and produce contradictory flags (interrupted=true && done=true).
	if capture != nil && !outcome.Interrupted {
		capture.ObservePayload(`{"type":"message_stop"}`, finalFinishReason, true)
	}
	return outcome
}

func writeAnthropicTail(w http.ResponseWriter, flusher http.Flusher, msgID, clientModel, finishReason string, outputTokens int) {
	stopReason := mapAnthropicStopReason(finishReason)

	blockStop := map[string]any{"type": "content_block_stop", "index": 0}
	writeSSE(w, "content_block_stop", blockStop)

	deltaPayload := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": outputTokens},
	}
	writeSSE(w, "message_delta", deltaPayload)

	writeSSE(w, "message_stop", map[string]any{"type": "message_stop"})
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	w.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)))
}

// flushBufferedText emits the accumulated text content of the first text
// block as either one text block (no `<think>` prefix) or two blocks
// (thinking + text) when the content begins with `<think>...</think>`.
// The caller has already emitted the content_block_start (text, index=0);
// we emit the deltas, the stop, and (on split) a fresh thinking block
// at index 0 plus an optional text block at index 1.
func flushBufferedText(w http.ResponseWriter, flusher http.Flusher, fullText string, capture *audit.StreamCapture) {
	if fullText == "" {
		// No text emitted. Close the pre-declared empty text block at index 0.
		writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		flusher.Flush()
		return
	}

	think, rest, ok := splitLeadingThinkBlock(fullText)
	if !ok {
		// No <think> prefix: emit the whole content as a single text_delta
		// on the pre-declared block, then close it.
		writeSSE(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": fullText},
		})
		writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		flusher.Flush()
		return
	}

	// Split path:
	// 1. Close the pre-declared empty text block at index 0.
	// 2. Open a thinking block at index 0 (reuse the slot).
	// 3. Emit the thinking delta + stop.
	// 4. If rest is non-empty, open a NEW text block at index 1 + delta + stop.
	writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSE(w, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
	writeSSE(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "thinking_delta", "thinking": think},
	})
	writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	if capture != nil {
		capture.HasThinking = true
		capture.ThinkingBlocksN++
	}
	if rest != "" {
		writeSSE(w, "content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         1,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		writeSSE(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 1,
			"delta": map[string]any{"type": "text_delta", "text": rest},
		})
		writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
	}
	flusher.Flush()
}
