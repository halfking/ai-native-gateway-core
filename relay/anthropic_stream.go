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
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
)

func StreamAnthropicSSE(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc *pendingCapturer) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic stream panic recovered", "panic", r, "stack", string(debug.Stack()), "request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		// Best-effort capturer finalise so the caller can snapshot and
		// persist (see cmd/gateway/main.go saveCapturedPending helper).
		// Mirrors StreamChatWithPendingCapture behaviour.
		if pc != nil {
			pc.finalize(outcome)
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

	// captureSSE writes a single Anthropic-shaped SSE event to w and
	// also appends the same bytes to the capturer buffer (Track C C5,
	// 2026-06-21). Use this for every writeSSE call below so the
	// capturer sees what the client sees — this is the body the client
	// expects to receive, and what we replay on reconnect via
	// GET /v1/sessions/{id}/pending-response.
	captureSSE := func(event string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		line := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
		//nolint:errcheck // HTTP write error non-recoverable
		w.Write([]byte(line))
		if pc != nil {
			pc.append(line)
		}
	}

	initialMsg := map[string]any{
		"type": "message_start",
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
	captureSSE("message_start", initialMsg)
	flusher.Flush()

	blockStart := map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}
	captureSSE("content_block_start", blockStart)
	flusher.Flush()

	captureSSE("ping", map[string]any{"type": "ping"})
	flusher.Flush()

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	// BUG-1 fix: hold the body closer so readNextStreamLine can close it on
	// chunk timeout, unblocking the ReadString goroutine immediately.
	bodyCloser := resp.Body
	reader := bufio.NewReaderSize(bodyCloser, streamBufSize)
	lastSend := time.Now()

	finalFinishReason := ""
	outputTokens := 0
	inputTokens := 0

	// Phase 4 stream-end split: lazy probing of the running text content
	// prefix. Most upstreams emit plain text and we want incremental
	// streaming UX for them. Only minimax-style upstreams that pack a
	// `<think>...</think>` reasoning trace need full buffering + split
	// on flush. We detect the latter by probing the prefix as it arrives:
	//   textAccProbing   — accumulate probeBuf until we have ≥ len("<think>")
	//                        bytes, then test the prefix
	//   ├─ prefix IS `<think>`        → textAccBuffering (split on flush)
	//   └─ prefix is anything else     → flush probe verbatim, then
	//                                    textAccPassthrough (emit deltas as
	//                                    they arrive for the rest of the stream)
	//   textAccPassthrough — emit deltas immediately (default)
	//   textAccBuffering   — accumulate bufferedText; split on flush
	var bufferedText strings.Builder
	var probeBuf strings.Builder
	const (
		textAccProbing = iota
		textAccBuffering
		textAccPassthrough
	)
	textAccMode := textAccProbing

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
		captureSSE("error", errPayload)
		flusher.Flush()
		writeAnthropicTail(w, flusher, pc, msgID, clientModel, finalFinishReason, outputTokens, inputTokens, capture)
		outcome.Interrupted = true
		outcome.Reason = "first_byte_timeout"
		return outcome
	}

	// 2026-06-20 audit fix: detect non-SSE JSON error bodies on
	// the anthropic path. Same rationale as relay/stream.go:
	// when the upstream returns `{"error":{...}}` for a stream
	// request, surface it as a resumable interruption so the
	// executor falls back to the next credential. The check
	// runs BEFORE the message_start / content_block_start
	// pre-declared tail below, so the client never sees a
	// half-built anthropic stream framing followed by a JSON
	// error.
	if firstLine != "" {
		if isErr, errKind, errMsg := isJSONErrorBody([]byte(firstLine)); isErr {
			slog.Warn("anthropic stream: upstream returned JSON error instead of SSE",
				"kind", errKind,
				"message", errMsg,
				"client_model", clientModel,
			)
			if capture != nil {
				capture.MarkInterruptedWithReason("json_error_in_stream")
			}
			captureSSE("error", map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "upstream_error", "message": errMsg, "code": errKind},
			})
			flusher.Flush()
			outcome.Interrupted = true
			outcome.Reason = "json_error_in_stream"
			outcome.Resumable = true
			outcome.ChunkCount = 0
			return outcome
		}
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
			//nolint:errcheck // test parse, non-critical
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
			// Phase 4 of 4: route the text delta through the accumulator
			// state machine. See the comment near the variable declarations
			// above for the semantics of each mode.
			switch textAccMode {
			case textAccProbing:
				probeBuf.WriteString(textDelta)
				if probeBuf.Len() < len("<think>") {
					break
				}
				probeStr := probeBuf.String()
				if strings.HasPrefix(probeStr, "<think>") {
					textAccMode = textAccBuffering
					bufferedText.WriteString(probeStr)
				} else {
					// Probe decided: not a <think> prefix. Flush probe verbatim
					// then enter passthrough for the rest of the stream.
					writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": 0,
						"delta": map[string]any{"type": "text_delta", "text": probeStr},
					})
					if flusher != nil {
						flusher.Flush()
					}
					textAccMode = textAccPassthrough
				}
			case textAccBuffering:
				bufferedText.WriteString(textDelta)
			case textAccPassthrough:
				writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": textDelta},
				})
				if flusher != nil {
					flusher.Flush()
				}
			}
			lastSend = time.Now()
			if capture != nil {
				// StreamAnthropicSSE processes OpenAI-format SSE (see line 232-236: chunk["choices"]),
				// so we must pass OpenAI-format payload to ObservePayload for extractDeltaText to work.
				openaiPayload := fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, textDelta)
				capture.ObservePayload(openaiPayload, "", false)
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
				writeSSEWithCapturer(w, pc, "content_block_start", startEvent)

				if fn != nil {
					args, _ := fn["arguments"].(string)
					if args != "" {
						argEvent := map[string]any{
							"type":  "content_block_delta",
							"index": idx,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
						}
						writeSSEWithCapturer(w, pc, "content_block_delta", argEvent)
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
		readResult := readNextStreamLine(ctx, reader, bodyCloser, w, &lastSend, runtimeCfg)
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
				writeSSEWithCapturer(w, pc, "error", errPayload)
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
				writeSSEWithCapturer(w, pc, "error", errPayload)
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

	// Phase 4: flush whatever mode we ended up in. Probing means the
	// stream ended before we accumulated enough bytes to decide — flush
	// whatever we have as a single text_delta so short content isn't
	// lost. Passthrough means deltas were already emitted in real time,
	// so just close the pre-declared block. Buffering means a <think>
	// prefix was confirmed; flush with the split logic.
	switch textAccMode {
	case textAccProbing:
		if probeBuf.Len() > 0 {
			writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": probeBuf.String()},
			})
		}
		writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		if flusher != nil {
			flusher.Flush()
		}
	case textAccBuffering:
		flushBufferedText(w, flusher, pc, bufferedText.String(), capture)
	case textAccPassthrough:
		writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		if flusher != nil {
			flusher.Flush()
		}
	}

	writeAnthropicTail(w, flusher, pc, msgID, clientModel, finalFinishReason, outputTokens, inputTokens, capture)

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

func writeAnthropicTail(w http.ResponseWriter, flusher http.Flusher, pc *pendingCapturer, msgID, clientModel, finishReason string, outputTokens int, inputTokens int, capture *audit.StreamCapture) {
	stopReason := mapAnthropicStopReason(finishReason)

	// Record usage in capture for audit trail
	if capture != nil && (inputTokens > 0 || outputTokens > 0) {
		capture.ObserveUsage(&inputTokens, &outputTokens, nil, nil)
	}

	// Note: the trailing content_block_stop is intentionally omitted. Phase 4's
	// flushBufferedText already emits per-block stops for the first text
	// block (or for thinking + post-think text after a split). Emitting an
	// extra stop here would produce a duplicate on the un-split path.

	deltaPayload := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": outputTokens},
	}
	writeSSEWithCapturer(w, pc, "message_delta", deltaPayload)

	writeSSEWithCapturer(w, pc, "message_stop", map[string]any{"type": "message_stop"})
	flusher.Flush()
}

// writeSSEWithCapturer is the capturer-aware variant of writeSSE for the
// Anthropic path. When pc is non-nil the same bytes are appended to the
// capturer buffer so the gateway can replay them via the pending-response
// endpoint on client reconnect (Track C C5, 2026-06-21). nil pc is fine —
// it just writes to w.
func writeSSEWithCapturer(w http.ResponseWriter, pc *pendingCapturer, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	line := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write([]byte(line))
	if pc != nil {
		pc.append(line)
	}
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)))
}

// flushBufferedText emits the accumulated text content of the first text
// block as either one text block (no `<think>` prefix) or two blocks
// (thinking + text) when the content begins with `<think>...</think>`.
// The caller has already emitted the content_block_start (text, index=0);
// we emit the deltas, the stop, and (on split) a fresh thinking block
// at index 0 plus an optional text block at index 1.
//
// pc is the optional pending-store capturer (Track C, 2026-06-21);
// every emitted event is also appended to its buffer for replay.
func flushBufferedText(w http.ResponseWriter, flusher http.Flusher, pc *pendingCapturer, fullText string, capture *audit.StreamCapture) {
	if fullText == "" {
		// No text emitted. Close the pre-declared empty text block at index 0.
		writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		flusher.Flush()
		return
	}

	think, rest, ok := textsplit.SplitLeadingThink(fullText)
	if !ok {
		// No <think> prefix: emit the whole content as a single text_delta
		// on the pre-declared block, then close it.
		writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": fullText},
		})
		writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		flusher.Flush()
		return
	}

	// Split path:
	// 1. Close the pre-declared empty text block at index 0.
	// 2. Open a thinking block at index 0 (reuse the slot).
	// 3. Emit the thinking delta + stop.
	// 4. If rest is non-empty, open a NEW text block at index 1 + delta + stop.
	writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSEWithCapturer(w, pc, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
	writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "thinking_delta", "thinking": think},
	})
	writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	if capture != nil {
		capture.HasThinking = true
		capture.ThinkingBlocksN++
	}
	if rest != "" {
		writeSSEWithCapturer(w, pc, "content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         1,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 1,
			"delta": map[string]any{"type": "text_delta", "text": rest},
		})
		writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 1})
	}
	flusher.Flush()
}
