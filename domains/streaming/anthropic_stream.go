package streaming

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/textsplit"
)

// q2ToolStreamBlock tracks one OpenAI tool_calls[] entry as it is relayed
// into Anthropic content_block events. Anthropic's protocol requires
// sequential blocks: start (with id/name) → input_json_delta fragments →
// stop. OpenAI's contract instead splits fragments across chunks keyed by
// tool_calls[].index, where only the first fragment carries id/name.
type q2ToolStreamBlock struct {
	anthropicIdx int
	id           string
	name         string
	started      bool
	stopped      bool
	pendingArgs  string
}

// StreamOpenAIToAnthropicSSE converts OpenAI-format SSE (from upstream)
// into Anthropic-format SSE (for client). Processes chunk["choices"][0]["delta"]
// and emits Anthropic events (message_start, content_block_delta, etc.).
//
// IMPORTANT: Despite the legacy naming confusion, this function has ALWAYS
// processed OpenAI format. The confusion stems from the Q4 Anthropic passthrough
// path (executor_anthropic.go) which calls this function after receiving
// OpenAI-shaped upstream responses.
//
// Evidence: Line 232-236 parse chunk["choices"], which is OpenAI-specific.
// Anthropic uses content[] blocks, not choices[].
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamOpenAIToAnthropicSSE(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	return StreamOpenAIToAnthropicSSEWithDiagnostics(
		ctx, w, resp, clientModel, outboundModel, requestID, capture, pc, nil,
	)
}

// StreamOpenAIToAnthropicSSEWithDiagnostics converts an OpenAI stream with
// optional best-effort diagnostics.
// P1-2 fix (2026-08-28): Added ctx parameter for context propagation to gate.
func StreamOpenAIToAnthropicSSEWithDiagnostics(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
	diagnostics *DiagnosticContext,
) (outcome StreamOutcome) {
	bodyCloser := &onceReadCloser{ReadCloser: resp.Body}
	//nolint:errcheck // best-effort close
	defer bodyCloser.Close()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("anthropic stream panic recovered", "panic", r, "stack", string(debug.Stack()), "request_id", requestID)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			outcome.Kind = errorsx.KindUpstreamDown
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

	diagnosticCollector := &streamDiagnosticCollector{}
	defer func() {
		diagnosticCollector.report(diagnostics, requestID, "openai-completions", "anthropic-messages", outcome.Interrupted)
	}()
	runtimeCfg := currentStreamRuntimeConfig()

	// SR-W1: route client frames through the attempt commit gate.
	// Disabled (default) this is the identity function — legacy wire bytes.
	// P1-2 fix (2026-08-28): Pass context to gate for checkpoint propagation.
	w, gate := wrapAttemptWriter(ctx, w, ProtocolAnthropic)
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
	if !safeFlush(flusher) {
		if capture != nil {
			capture.MarkInterruptedWithReason("client_write_failed")
		}
		if pc != nil {
			pc.markInterrupted("client_write_failed")
		}
		// Client connection is dead before any frame — including headers —
		// reaches the wire. A transparent retry would re-attempt the same
		// header flush on the same dead connection, wasting an upstream call.
		return StreamOutcome{Interrupted: true, Reason: "client_write_failed", Kind: errorsx.KindCanceled, Resumable: false}
	}

	clientWriter := newClientStreamWriter(w, flusher)
	chunkCount := 0

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
		clientWriter.write(line)
		if pc != nil {
			pc.append(line)
		}
	}

	// P1-2 fix (2026-08-28): ctx is now a function parameter, removed redundant declaration.

	// BUG-1 fix: hold the body closer so readNextStreamLine can close it on
	// chunk timeout, unblocking the ReadString goroutine immediately.
	reader := bufio.NewReaderSize(bodyCloser, streamBufSize)
	lastSend := time.Now()

	finalFinishReason := ""
	outputTokens := 0
	inputTokens := 0
	upstreamDoneReceived := false

	// Q2 tool-call block state: keyed by the OpenAI tool_calls[].index (stable
	// per spec across fragments), tracks the emitted Anthropic block and
	// whether start/stop events have been sent. textBlockOpen latches false
	// once the implicit text block 0 has been closed — Anthropic content
	// blocks must be strictly sequential (no tool_use while text is open).
	toolBlocks := make(map[int]*q2ToolStreamBlock)
	var toolOrder []int
	nextToolBlockIdx := 1
	textBlockOpen := true
	closeTextBlock := func() {
		if !textBlockOpen {
			return
		}
		textBlockOpen = false
		writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		if flusher != nil {
			flusher.Flush()
		}
	}
	// midStreamHalt is set when processLine detects an upstream error encoded
	// outside its normal channel — OpenAI-style data: {"error":{...}} chunks
	// (several second-tier OpenAI-compatible upstreams send mid-stream errors
	// this way instead of an SSE `event: error` frame) and GLM-style
	// finish_reason values that actually mean failure. The read loop then
	// bails and treats the attempt as an upstream interruption rather than
	// silently dropping the error.
	var midStreamHalt *StreamOutcome

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
	firstLine, err := readLineWithTimeoutAndCloser(ctx, reader, bodyCloser, runtimeCfg.firstByteTimeout)

	if err != nil {
		if capture != nil {
			capture.MarkInterruptedWithReason("first_byte_timeout")
		}
		slog.Warn("anthropic stream first-byte timeout",
			"error", err,
			"first_byte_timeout_seconds", int(runtimeCfg.firstByteTimeout.Seconds()),
			"hint", "if frequent, increase LLM_GATEWAY_FIRST_BYTE_TIMEOUT or admin config (default 120s)",
		)
		terminalVisible := attemptHasClientSemanticOutput(gate, 0)
		if terminalVisible {
			errPayload := map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "timeout", "message": "upstream first-byte timeout"},
			}
			captureSSE("error", errPayload)
			flusher.Flush()
			writeAnthropicTail(w, flusher, pc, msgID, clientModel, finalFinishReason, outputTokens, inputTokens, capture)
		}
		outcome.Interrupted = true
		outcome.Reason = "first_byte_timeout"
		outcome.Kind = errorsx.KindStreamTimeout
		outcome.Resumable = !terminalVisible
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
			terminalVisible := attemptHasClientSemanticOutput(gate, 0)
			if terminalVisible {
				captureSSE("error", map[string]any{
					"type":  "error",
					"error": map[string]any{"type": "upstream_error", "message": errMsg, "code": errKind},
				})
				flusher.Flush()
			}
			outcome.Interrupted = true
			outcome.Reason = "json_error_in_stream"
			outcome.Kind = errorsx.KindUpstreamDown
			outcome.Resumable = !terminalVisible

			outcome.ChunkCount = 0
			return outcome
		}
	}

	initialMsg := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant", "content": []any{},
			"model": clientModel, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}
	captureSSE("message_start", initialMsg)
	captureSSE("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	captureSSE("ping", map[string]any{"type": "ping"})

	// emitQ2GateError renders a sanitized Anthropic error frame only when
	// the attempt already committed client-visible content; pre-content
	// interruptions stay invisible so the executor can fail over transparently.
	emitQ2GateError := func(code, message string) {
		if !attemptHasClientSemanticOutput(gate, chunkCount) {
			return
		}
		writeSSEWithCapturer(w, pc, "error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": code, "message": message},
		})
		if flusher != nil {
			flusher.Flush()
		}
	}

	processLine := func(line string) bool {

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			return false
		}
		data := line[6:]
		if data == "[DONE]" {
			upstreamDoneReceived = true
			return false
		}

		rawFrame := []byte(line)
		logRawUpstreamFrame(diagnostics, auditFromDiagnostics(diagnostics, requestID, "openai-completions"), rawFrame)
		diagnosticCollector.observeRaw([]byte(data))

		var chunk map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			reportConversionAnomaly(
				diagnostics, requestID, "openai-completions", "anthropic-messages", "parse_stream_frame", []byte(data), err, nil,
			)
			return false
		}

		parsedChunk, parseErr := ir.ParseOpenAIStreamChunk("data: " + data + "\n\n")
		if parseErr != nil {
			reportConversionAnomaly(
				diagnostics, requestID, "openai-completions", "anthropic-messages", "parse_stream_chunk", []byte(data), parseErr, nil,
			)
		} else {
			diagnosticCollector.observeChunk(parsedChunk)
			// tool_calls_missing accounting must fire at the EMIT site below
			// (the tool-call write loop), not here: client-facing tool-call
			// events are written from the raw delta map independent of the IR
			// parser, so a parse failure here would otherwise register as
			// "no tool call emitted" on a stream that delivered them fine.
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

		// 2026-08-28 spec audit: OpenAI-compatible upstreams (Qwen DashScope,
		// some Kimi deployments, Azure) surface mid-stream failures as a bare
		// data: {"error":{...}} chunk with no choices array. Treat it as an
		// Anthropic-style terminal error instead of silently dropping it.
		if rawErr, hasErr := chunk["error"]; hasErr {
			var errObj map[string]any
			if json.Unmarshal(rawErr, &errObj) == nil && errObj != nil {
				code, _ := errObj["type"].(string)
				if code == "" {
					code, _ = errObj["code"].(string)
				}
				msg, _ := errObj["message"].(string)
				kind := classifyAnthropicStreamError(code, []byte(data))
				midStreamHalt = &StreamOutcome{
					Interrupted: true,
					Reason:      "upstream_error",
					Kind:        kind,
					Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
					ChunkCount:  chunkCount,
				}
				if capture != nil {
					capture.MarkInterruptedWithReason("upstream_error")
				}
				slog.Warn("anthropic stream: upstream mid-stream JSON error",
					"request_id", requestID,
					"client_model", clientModel,
					"error_code", code,
					"kind", string(kind),
					"error_headline", truncateForLog(firstLineOfLogSafe(msg), 120),
					"client_visible_chunks", chunkCount,
				)
				if attemptHasClientSemanticOutput(gate, chunkCount) {
					clCode := code
					if clCode == "" {
						clCode = "api_error"
					}
					writeSSEWithCapturer(w, pc, "error", map[string]any{
						"type":  "error",
						"error": map[string]any{"type": clCode, "message": "upstream stream error: " + clCode},
					})
					flusher.Flush()
				}
				return true
			}
		}

		var choices []map[string]any
		if raw, ok := chunk["choices"]; ok {
			//nolint:errcheck // test parse, non-critical
			json.Unmarshal(raw, &choices)
		}
		if len(choices) == 0 {
			return false
		}

		choice := choices[0]
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			// 2026-08-28 spec audit (Zhipu GLM): finish_reason is also an
			// error channel on this vendor — network_error/sensitive/
			// model_context_window_exceeded mean the stream FAILED rather
			// than ended normally. Reclassify to an upstream interruption
			// instead of mapping them to end_turn.
			switch fr {
			case "network_error":
				midStreamHalt = &StreamOutcome{
					Interrupted: true,
					Reason:      "network_error",
					Kind:        errorsx.KindNetwork,
					Resumable:   !attemptHasClientSemanticOutput(gate, chunkCount),
					ChunkCount:  chunkCount,
				}
				if capture != nil {
					capture.MarkInterruptedWithReason("network_error")
				}
				emitQ2GateError("network_error", "upstream stream error: network_error")
				return true
			case "sensitive":
				midStreamHalt = &StreamOutcome{
					Interrupted: true,
					Reason:      "content_filter",
					Kind:        errorsx.KindContentFilter,
					Resumable:   false,
					ChunkCount:  chunkCount,
				}
				if capture != nil {
					capture.MarkInterruptedWithReason("content_filter")
				}
				emitQ2GateError("content_filter", "upstream refused to produce this content")
				return true
			case "model_context_window_exceeded":
				midStreamHalt = &StreamOutcome{
					Interrupted: true,
					Reason:      "context_length_exceeded",
					Kind:        errorsx.KindContextLength,
					Resumable:   false,
					ChunkCount:  chunkCount,
				}
				if capture != nil {
					capture.MarkInterruptedWithReason("context_length_exceeded")
				}
				emitQ2GateError("request_too_large", "upstream context window exceeded")
				return true
			}
			finalFinishReason = fr
		}

		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			return false
		}

		textDelta, _ := delta["content"].(string)
		if textDelta != "" {
			chunkCount++
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
				// IR-based audit: pass structured chunk to ObserveChunk
				// This replaces the previous string-based ObservePayload hack
				// that manually constructed an OpenAI-format JSON string.
				capture.ObserveChunk(&ir.StreamChunk{
					Type: ir.ChunkTypeDelta,
					Delta: &ir.StreamDelta{
						Content: textDelta,
					},
					SourceProtocol: ir.ProtocolOpenAIChat,
				})
				if capture.IntegrityBreached() {
					return true
				}
			}
		}

		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for i, tc := range toolCalls {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				// 2026-08-28 spec alignment: OpenAI tool_calls[].index is the
				// stable aggregation key across chunks — the array position
				// within THIS chunk is not. Map the OpenAI index to an
				// Anthropic content-block index on first sight; later fragments
				// for the same call only extend input_json_delta.
				openaiIdx := i
				if v, ok := tcMap["index"].(float64); ok {
					openaiIdx = int(v)
				}
				fn, _ := tcMap["function"].(map[string]any)
				fnName := ""
				if fn != nil {
					fnName, _ = fn["name"].(string)
				}
				tcID, _ := tcMap["id"].(string)

				state := toolBlocks[openaiIdx]
				if state == nil {
					state = &q2ToolStreamBlock{
						anthropicIdx: nextToolBlockIdx,
						id:           tcID,
						name:         fnName,
					}
					nextToolBlockIdx++
					toolBlocks[openaiIdx] = state
					toolOrder = append(toolOrder, openaiIdx)
				}
				if tcID != "" && state.id == "" {
					state.id = tcID
				}
				if fnName != "" && state.name == "" {
					state.name = fnName
				}

				chunkCount++
				if !state.started {
					// tool_calls_missing accounting: the moment we promise this
					// tool call to the client via content_block_start, the
					// tool_calls_missing detector must register it as emitted.
					// Calling observeEmittedChunk on the IR parse site above
					// (where the original audit found it) conflated "the parser
					// saw the call" with "the client received it"; subsequent
					// argument deltas for the same call are deliberately not
					// counted again so emittedToolCallCount tracks distinct calls.
					if diagnostics != nil {
						diagnosticCollector.observeEmittedChunk(&ir.StreamChunk{
							Type: ir.ChunkTypeDelta,
							Delta: &ir.StreamDelta{
								ToolCalls: []ir.StreamToolCallDelta{{Index: state.anthropicIdx, ID: state.id}},
							},
						})
					}

					// Anthropic content blocks are sequential: close the
					// implicit text block (index 0) before opening the first
					// tool_use block.
					closeTextBlock()

					startEvent := map[string]any{
						"type":  "content_block_start",
						"index": state.anthropicIdx,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    state.id,
							"name":  state.name,
							"input": map[string]any{},
						},
					}
					writeSSEWithCapturer(w, pc, "content_block_start", startEvent)
					state.started = true
				}

				if fn != nil {
					args, _ := fn["arguments"].(string)
					if args != "" {
						partial := state.pendingArgs + args
						state.pendingArgs = ""
						argEvent := map[string]any{
							"type":  "content_block_delta",
							"index": state.anthropicIdx,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
						}
						writeSSEWithCapturer(w, pc, "content_block_delta", argEvent)
					}
				}
				lastSend = time.Now()
			}
		}

		return false
	}


	if firstLine != "" {
		normalizedLine, hasCombinedDone := splitCombinedDoneFrame(firstLine)
		firstLine = normalizedLine
		if hasCombinedDone {
			reader = prependDoneFrame(reader)
		}
		if processLine(firstLine) {
			if midStreamHalt != nil {
				return *midStreamHalt
			}
			outcome = integrityBreachOutcome(capture, 0)
			return outcome
		}
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
				outcome.Kind = errorsx.KindCanceled
			case streamReadEOF:
				if !upstreamDoneReceived {
					if capture != nil {
						capture.MarkInterruptedWithReason("eof_without_done")
					}
					outcome.Interrupted = true
					outcome.Reason = "eof_without_done"
					outcome.Kind = errorsx.KindUpstreamDown
					outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
					outcome.ChunkCount = chunkCount
				}
			case streamReadTimeout:
				slog.Warn("anthropic stream read timeout", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				if attemptHasClientSemanticOutput(gate, chunkCount) {
					errPayload := map[string]any{
						"type":  "error",
						"error": map[string]any{"type": "timeout", "message": "upstream read timeout"},
					}
					writeSSEWithCapturer(w, pc, "error", errPayload)
					flusher.Flush()
				}
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
				outcome.Kind = errorsx.KindStreamTimeout
				outcome.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
				outcome.ChunkCount = chunkCount
			default:
				failure := streamReadFailureOutcome(readResult.err, chunkCount)
				slog.Warn("anthropic stream read error", "error", readResult.err, "kind", failure.Kind, "reason", failure.Reason)
				if capture != nil {
					capture.MarkInterruptedWithReason(failure.Reason)
				}
				if attemptHasClientSemanticOutput(gate, chunkCount) {
					errPayload := map[string]any{
						"type":  "error",
						"error": map[string]any{"type": "upstream_error", "message": fmt.Sprintf("stream read error: %v", readResult.err)},
					}
					writeSSEWithCapturer(w, pc, "error", errPayload)
					flusher.Flush()
				}
				failure.Resumable = !attemptHasClientSemanticOutput(gate, chunkCount)
				outcome = failure
			}
			break
		}

		line := readResult.line
		normalizedLine, hasCombinedDone := splitCombinedDoneFrame(line)
		line = normalizedLine
		if hasCombinedDone {
			reader = prependDoneFrame(reader)
		}

		if line == "" {
			continue
		}
		if processLine(line) {
			if midStreamHalt != nil {
				return *midStreamHalt
			}
			outcome = integrityBreachOutcome(capture, 0)
			return outcome
		}
	}

	// Phase 4: flush whatever mode we ended up in. Probing means the
	// stream ended before we accumulated enough bytes to decide — flush
	// whatever we have as a single text_delta so short content isn't
	// lost. Passthrough means deltas were already emitted in real time,
	// so just close the pre-declared block. Buffering means a <think>
	// prefix was confirmed; flush with the split logic.
	// SR-W1: pending REAL content must still be delivered even while the
	// gate holds an uncommitted attempt — delivering it commits the attempt,
	// which then allows the closing tail. Only a stream with nothing to
	// deliver (empty/interrupted pre-content) stays droppable for the
	// coordinator to discard and retry.
	pendingContent := (textAccMode == textAccProbing && probeBuf.Len() > 0) ||
		(textAccMode == textAccBuffering && bufferedText.Len() > 0)
	if (!outcome.Interrupted || !outcome.Resumable) && (gate.MayWriteTerminal() || pendingContent) {
		switch textAccMode {
		case textAccProbing:
			if probeBuf.Len() > 0 {
				writeSSEWithCapturer(w, pc, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": probeBuf.String()},
				})
			}
		case textAccBuffering:
			flushBufferedText(w, flusher, pc, bufferedText.String(), capture)
		case textAccPassthrough:
			// When the Q2 tool-call state machine already closed the implicit
			// text block (index 0) before opening the first tool_use block,
			// don't emit a duplicate stop here.
			if textBlockOpen {
				writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
				textBlockOpen = false
				if flusher != nil {
					flusher.Flush()
				}
			}
		}

		// Anthropic requires content_block_stop for every opened tool_use
		// block before the message_delta; close any still-open tool blocks
		// in first-open order.
		for _, openaiIdx := range toolOrder {
			st := toolBlocks[openaiIdx]
			if st != nil && st.started && !st.stopped {
				writeSSEWithCapturer(w, pc, "content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": st.anthropicIdx,
				})
				st.stopped = true
			}
		}
		if len(toolOrder) > 0 {
			finalFinishReason = "tool_calls"
		}

		// Content delivery above may have committed the gate. A detached client
		// still needs the clean terminal sequence in the pending capturer.
		if gate.MayWriteTerminal() || pc != nil {
			writeAnthropicTail(w, flusher, pc, msgID, clientModel, finalFinishReason, outputTokens, inputTokens, capture)
		}
	}

	// Only mark the capture as "done" if the stream was NOT interrupted.
	// If we received an interruption (e.g. stream_timeout, read_error,
	// client cancel), MarkInterruptedWithReason has already set
	// interrupted=true and finalFinish to the failure reason — calling
	// ObservePayload with done=true here would clobber the failure reason
	// and produce contradictory flags (interrupted=true && done=true).
	if capture != nil && !outcome.Interrupted {
		capture.ObserveChunk(&ir.StreamChunk{
			Type:           ir.ChunkTypeDone,
			FinishReason:   finalFinishReason,
			SourceProtocol: ir.ProtocolAnthropicMessages,
		})
	}
	return outcome
}

func writeAnthropicTail(w http.ResponseWriter, flusher http.Flusher, pc *pendingCapturer, msgID, clientModel, finishReason string, outputTokens int, inputTokens int, capture *audit.StreamCapture) {
	stopReason := mapAnthropicStopReason(finishReason)

	// Record usage in capture for audit trail (IR-based)
	if capture != nil && (inputTokens > 0 || outputTokens > 0) {
		capture.ObserveChunk(&ir.StreamChunk{
			Type: ir.ChunkTypeUsage,
			Usage: &ir.StreamUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      inputTokens + outputTokens,
			},
			SourceProtocol: ir.ProtocolAnthropicMessages,
		})
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
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
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
		// 2026-07-27 并发修复：走带锁 setter（audit.StreamCapture.MarkThinkingBlock）。
		capture.MarkThinkingBlock()
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
