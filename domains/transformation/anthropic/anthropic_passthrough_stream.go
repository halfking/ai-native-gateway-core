package anthropic

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const anthropicSSEBufSize = 64 * 1024

// StreamAnthropicPassthrough is the Q4 Anthropic SSE forwarder. It reads
// Anthropic-format SSE events from upstream and writes them to the
// client unchanged (byte-for-byte), while scanning for has_thinking /
// usage accounting in the side-channel audit capture.
//
// This is the "Q4" path: client speaks Anthropic, upstream speaks
// Anthropic (e.g. anthropic provider, or minimax's /anthropic
// compatible endpoint), no protocol conversion required.
//
// Track C C5 (2026-06-21): when pc is non-nil, every byte forwarded to
// the client is also appended to the capturer buffer so the gateway can
// replay the full SSE response from pending store after a client
// disconnect. The capturer is finalized before return so the caller can
// snapshot and persist it (see cmd/gateway/main.go's saveCapturedPending
// helper). nil pc is fine (legacy / non-session requests).
//
// Q3 parity TODO (2026-08-28, audit-24h-20260828-r3): AnthropicToOpenAIStream
// and AnthropicToResponsesStream at ../anthropic_to_openai_stream.go and
// ../anthropic_to_responses_stream.go run their own SSE loops and do NOT
// invoke the empty-response detector added in this commit — an OpenAI or
// Responses client hitting an empty Anthropic upstream will still record
// a successful empty stream. Match the executor_anthropic.go:1263-1272
// comment on Q3 parity when those translators grow the same check.
func StreamAnthropicPassthrough(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel, requestID string,
	capture *audit.StreamCapture,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	// Top-level panic recovery. Mirrors StreamChatWithPendingCapture
	// so a panic during streaming (e.g. JSON parse failure, write to
	// a closed connection) does not skip the deferred capturer
	// finalize and lose the pending-store entry entirely.
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
		// Best-effort capturer finalise. If the stream completed
		// normally, the capturer holds the full body ready for replay
		// via GET /v1/sessions/{id}/pending-response. If terminated
		// abnormally, the capturer still holds whatever was captured
		// so the admin API can inspect (status=failed).
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
	// audit-24h-20260828-r3 P1-B: track chunk count for empty-response
	// detection. A "chunk" here is any `data:` line sent from upstream;
	// the EOF-time empty check mirrors the non-stream path at
	// executor_anthropic.go:1273 (isEmptyAnthropicMessagesResponse).
	chunkCount := 0

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
		// Track C C5 (2026-06-21): record every line into the capturer
		// BEFORE the side-channel audit observer runs so a panic in
		// observeAnthropicPayload does not skip the buffer write.
		// Anthropic SSE events span 2-3 lines each (`event:` + `data:`
		// + blank line); storing the whole line is correct for replay.
		if pc != nil {
			pc.append(line)
		}
		// audit-24h-20260828-r3 P1-B: count only content-bearing events
		// (content_block_*), not envelope events (message_start/stop/delta).
		// The EOF-time empty check mirrors the non-stream path's content-array
		// length check (isEmptyAnthropicMessagesResponse).
		if capture != nil && strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			payload = strings.TrimSpace(payload)
			// Parse just enough to know if this is a content_block_* event.
			// content_block_start / content_block_delta / content_block_stop
			// all have `"type":"content_block_*"`.
			if strings.Contains(payload, `"type":"content_block_`) {
				chunkCount++
			}
			observeAnthropicPayload(capture, payload, clientModel, outboundModel)
		}
		if line == "\n" {
			flusher.Flush()
		}
	}
	flusher.Flush()

	// audit-24h-20260828-r3 P1-B: anthropic stream empty-response parity
	// with the non-stream detector at executor_anthropic.go:1273. After a
	// clean EOF, if the upstream sent zero semantic bytes (no text/thinking/
	// tool blocks observed, no usage tokens), surface KindEmptyResponse so
	// the executor routes this to the next candidate via streamInterruptedError
	// + TaskActionRetryNow, instead of recording it as a successful empty
	// stream. Mirrors executor_chat.go:1153 (Reason -> Kind upgrade).
	//
	// The check is strict: chunkCount==0 (no `data:` lines at all) AND
	// (capture is nil OR both OutputTokens and InputTokens are nil). If
	// upstream returned `message_start` with usage but zero content_block_*
	// events, this still flags empty — matching non-stream semantics
	// (isEmptyAnthropicMessagesResponse checks content array length, not
	// usage token presence).
	isEmpty := chunkCount == 0 &&
		(capture == nil ||
			(capture.OutputTokens == nil && capture.InputTokens == nil))
	if isEmpty {
		if capture != nil {
			capture.MarkInterruptedWithReason("anthropic_empty_response")
		}
		if pc != nil {
			pc.markInterrupted("anthropic_empty_response")
		}
		return StreamOutcome{
			Interrupted: true,
			Reason:      "anthropic_empty_response",
			Kind:        errorsx.KindEmptyResponse,
			Resumable:   true,
			ChunkCount:  0,
		}
	}
	return outcome
}

// observeAnthropicPayload inspects a single Anthropic SSE data payload
// and updates the side-channel audit capture accordingly. It handles
// the three event types that carry state worth recording:
//
//   - message_start: contains usage.input_tokens (initial value)
//   - message_delta:  contains usage.output_tokens (cumulative final)
//   - content_block_start with type=thinking: marks the response as
//     containing reasoning, increments the thinking-block counter
//   - any event with a "message.model" field: triggers model-mismatch
//     check against the requested model
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
			// 2026-07-27 并发修复：走带锁 setter（audit.StreamCapture.MarkThinkingBlock）。
			c.MarkThinkingBlock()
		}
	case "error":
		c.MarkStreamError()
	}
}

// checkAnthropicModelMismatch sets the capture's ModelMismatch flag
// when the upstream response model name does not case-insensitively
// match the clientModel (or outboundModel fallback). This is a
// side-channel detection — most upstreams will report a different
// model name in the response message_start than what the client
// asked for (e.g. "claude-3-5-sonnet-20241022" vs "claude-3-5-sonnet"),
// so we only flag a mismatch when the names diverge in a way the
// caller likely cares about.
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
