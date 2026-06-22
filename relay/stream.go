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
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/audit"
)

const (
	streamBufSize       = 64 * 1024
	sseKeepaliveComment = ": keep-alive\n\n"
)

// qualityFixModeCtxKey is the context value key used to thread the
// per-provider quality_fix_mode (017_quality_fix_mode.sql) from the
// routing executor into the relay-side stream reader. The executor
// sets it via SetQualityFixModeOnContext before the upstream call;
// the stream reader pulls it out via qualityFixModeFromContext on
// every line.
type qualityFixModeCtxKey struct{}

// SetQualityFixModeOnContext stamps the provider's quality_fix_mode
// onto the given context. Empty string ⇒ "no mode set" (off by
// default). The routing executor calls this before issuing the
// upstream request so the relay stream reader can look it up without
// needing direct access to the provider.Candidate struct.
func SetQualityFixModeOnContext(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, qualityFixModeCtxKey{}, mode)
}

// qualityFixModeFromContext returns the mode stashed by
// SetQualityFixModeOnContext, or empty string if none was set. The
// stream reader calls this once per chunk; the cost is one map lookup
// which is fine on the hot path.
func qualityFixModeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(qualityFixModeCtxKey{}).(string); ok {
		return v
	}
	return ""
}

type StreamOutcome struct {
	Interrupted bool
	Reason      string
	Resumable   bool // Whether the stream can be resumed with a different credential
	ChunkCount  int  // Number of chunks sent before interruption
}

func StreamChat(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer) StreamOutcome {
	return StreamChatWithCapture(w, resp, clientModel, outboundModel, norm, nil)
}

func StreamChatWithCapture(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer, capture *audit.StreamCapture) StreamOutcome {
	return StreamChatWithCaptureAndToolFallback(w, resp, clientModel, outboundModel, norm, capture, false, nil)
}

func StreamChatWithCaptureAndToolFallback(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel string, norm *Normalizer, capture *audit.StreamCapture, toolsRequested bool, stripFn func([]byte) []byte) (outcome StreamOutcome) {
	return StreamChatWithPendingCapture(w, resp, clientModel, outboundModel, norm, capture, toolsRequested, stripFn, nil)
}

// StreamChatWithPendingCapture (Track C C2, 2026-06-18) extends
// StreamChatWithCaptureAndToolFallback with an optional
// pendingCapturer. When supplied, every chunk the upstream
// sends is recorded into the capturer's buffer in addition
// to being forwarded to the client — regardless of whether
// the client is still connected. When the stream ends, the
// capturer's finalize is called with the terminal outcome
// so a caller-driven Save() can persist the buffer.
//
// Why this works even when the client is gone:
//   - C1 decoupled the upstream context from the client
//     context when the request carries a session id, so the
//     read loop keeps going past the client disconnect.
//   - safeWriteSSE already recovers from "write to closed
//     conn" panics, so existing w.WriteString calls are safe.
//
// The capturer is intentionally decoupled from the audit
// StreamCapture — it serves a different purpose (replay) with
// different size limits (1 MiB cap here, vs unbounded there).
func StreamChatWithPendingCapture(
	w http.ResponseWriter,
	resp *http.Response,
	clientModel, outboundModel string,
	norm *Normalizer,
	capture *audit.StreamCapture,
	toolsRequested bool,
	stripFn func([]byte) []byte,
	pc *pendingCapturer,
) (outcome StreamOutcome) {
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()
	// Top-level panic recovery so a panic during streaming (e.g. JSON parse
	// failure, write to a closed connection) does not skip the deferred
	// audit emit in the caller and lose the request_logs row entirely.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("stream panic recovered", "panic", r, "stack", string(debug.Stack()), "client_model", clientModel)
			if capture != nil {
				capture.MarkInterruptedWithReason("stream_panic")
			}
			outcome.Interrupted = true
			outcome.Reason = "stream_panic"
			if pc != nil {
				pc.markInterrupted("stream_panic")
			}
		}
		// Best-effort capture finalise. If the stream completed
		// normally, the capturer holds the full body ready for
		// replay via GET /v1/sessions/{id}/pending-response.
		// If terminated abnormally, the capturer still holds
		// whatever was captured so the admin API can inspect.
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
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var ctx context.Context
	if resp.Request != nil {
		ctx = resp.Request.Context()
	} else {
		ctx = context.Background()
	}

	// BUG-1 fix (2026-06-19): hold a reference to the raw body as an
	// io.ReadCloser so readLineWithTimeoutAndCloser can close it on timeout,
	// unblocking the ReadString goroutine immediately instead of leaking it
	// until StreamTimeout (up to 900 s on the session path).
	bodyCloser := resp.Body
	reader := bufio.NewReaderSize(bodyCloser, streamBufSize)
	discoveredUpstream := ""
	lastSend := time.Now()
	chunkCount := 0 // Track number of chunks sent

	if clientModel != "" && outboundModel != "" && clientModel != outboundModel {
		slog.Debug("upstream model diff",
			"client_model", clientModel,
			"outbound_model", outboundModel,
		)
	}

	// ── First-byte timeout ──────────────────────────────────────────
	firstLine, err := readLineWithTimeoutAndCloser(ctx, reader, bodyCloser, runtimeCfg.firstByteTimeout)
	if err != nil {
		if capture != nil {
			capture.MarkInterruptedWithReason("first_byte_timeout")
		}
		slog.Warn("stream first-byte timeout", "error", err)
		safeWriteSSE(w, "data: {\"error\":{\"message\":\"upstream first-byte timeout\",\"type\":\"timeout\",\"code\":\"first_byte_timeout\"}}\n\n")
		safeFlush(flusher)
		outcome.Interrupted = true
		outcome.Reason = "first_byte_timeout"
		outcome.Resumable = true // First-byte timeout is resumable (no chunks sent)
		outcome.ChunkCount = 0
		return outcome
	}

	if firstLine != "" {
		// 2026-06-20 audit fix: when the upstream returns a
		// non-SSE JSON error body (e.g. {"error":{"type":
		// "service_unavailable","message":"积分不足"}}) for a
		// stream request, do NOT pass the body through to the
		// client as if it were a valid first chunk. Treat it
		// as a resumable stream interruption so the executor
		// falls back to the next credential. This branch fires
		// before the firstLine is written to the client, so the
		// client never sees a 200 + the raw JSON error.
		//
		// Resumable=true + ChunkCount=0 < StreamRetryThreshold
		// (default 5) → executor_chat.go:521 will surface this
		// as a streamInterruptedError → executor.go:737 will
		// continue to the next candidate.
		if isErr, errKind, errMsg := isJSONErrorBody([]byte(firstLine)); isErr {
			slog.Warn("stream: upstream returned JSON error instead of SSE",
				"kind", errKind,
				"message", errMsg,
				"client_model", clientModel,
			)
			if capture != nil {
				capture.MarkInterruptedWithReason("json_error_in_stream")
			}
			// Surface a clean SSE error to the client instead of
			// the raw vendor error envelope, so SDK clients can
			// parse it as a normal chat.completion.chunk stream
			// error rather than choking on an unexpected shape.
			safeWriteSSE(w, fmt.Sprintf("data: {\"error\":{\"message\":%q,\"type\":%q,\"code\":%q}}\n\n", errMsg, "upstream_error", errKind))
			safeFlush(flusher)
			outcome.Interrupted = true
			outcome.Reason = "json_error_in_stream"
			outcome.Resumable = true
			outcome.ChunkCount = 0
			return outcome
		}
		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql). Run
		// before XML coercion so the scanner sees the raw upstream
		// delta.tool_calls shape and can rewrite empty names to
		// __unknown_tool_stream_<i>__. detect_only mode leaves the
		// line byte-identical but still tags the issue in the
		// capture's QualityFlags slice.
		qualityMode := qualityFixModeFromContext(ctx)
		if qualityMode != "" && qualityMode != QualityModeOff && capture != nil {
			newLine, newFlags, newSeen := ProcessStreamLine(firstLine, qualityMode, capture.QualityFlags, capture.QualitySeenToolCallIDs)
			if newLine != "" {
				firstLine = newLine
			}
			if len(newFlags) > 0 {
				capture.QualityFlags = newFlags
			}
			if newSeen != nil {
				capture.QualitySeenToolCallIDs = newSeen
			}
		}
		firstLine = coerceXMLToolCallsInStreamLine(firstLine, toolsRequested)
		if clientModel != "" && discoveredUpstream == "" {
			discoveredUpstream = extractModelFromChunk(firstLine)
		}
		if clientModel != "" {
			firstLine = replaceModelInChunk(firstLine, clientModel, discoveredUpstream)
		}

		payload := extractPayload(firstLine)
		if payload != "" && capture != nil {
			finishReason := ExtractFinishReason(payload)
			usage := ExtractUsageFromChunk(payload)
			capture.ObservePayload(payload, finishReason, false)
			if usage.PromptTokens != nil || usage.CompletionTokens != nil {
				capture.ObserveUsage(usage.PromptTokens, usage.CompletionTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			}
		}

		if norm != nil {
			firstLine = string(norm.NormalizeChunk([]byte(firstLine), true))
		}
		if pc != nil {
			pc.append(firstLine)
		}
		safeWriteSSE(w, firstLine)
		safeFlush(flusher)
		lastSend = time.Now()
		chunkCount++ // Count first chunk
	}

	// ── Main streaming loop with keep-alive ─────────────────────────
	// upstreamDoneReceived tracks whether the upstream sent the literal
	// "data: [DONE]\n\n" terminator. If the stream ended by EOF without
	// [DONE] (e.g. upstream crashed mid-response), we do NOT want to
	// mark the capture as doneReceived=true — that would misreport an
	// interruption as a clean completion.
	upstreamDoneReceived := false
	for {
		select {
		case <-ctx.Done():
			if capture != nil {
				capture.MarkInterruptedWithReason("client_cancel")
			}
			outcome.Interrupted = true
			outcome.Reason = "client_cancel"
			outcome.ChunkCount = chunkCount
			outcome.Resumable = false
			return outcome
		default:
		}

		readResult := readNextStreamLine(ctx, reader, bodyCloser, w, &lastSend, runtimeCfg)
		if readResult.err != nil {
			switch readResult.state {
			case streamReadEOF:
				// If upstream closed before sending [DONE], still send
				// the terminator to the client (clients expect a
				// well-formed stream) but mark the capture as
				// interrupted with reason "eof_without_done" so audit
				// queries can distinguish a clean completion from a
				// premature close.
				if !upstreamDoneReceived {
					slog.Warn("upstream EOF without [DONE]", "client_model", clientModel)
					if capture != nil {
						capture.MarkInterruptedWithReason("eof_without_done")
					}
					outcome.Interrupted = true
					outcome.Reason = "eof_without_done"
				}
				safeWriteSSE(w, "data: [DONE]\n\n")
				safeFlush(flusher)
				if capture != nil && upstreamDoneReceived {
					capture.ObservePayload("[DONE]", "", true)
				}
				outcome.ChunkCount = chunkCount
			case streamReadCanceled:
				slog.Debug("stream cancelled by client")
				if capture != nil {
					capture.MarkInterruptedWithReason("client_cancel")
				}
				outcome.Interrupted = true
				outcome.Reason = "client_cancel"
				outcome.ChunkCount = chunkCount
			case streamReadTimeout:
				slog.Warn("stream read timeout, sending error chunk")
				safeWriteSSE(w, "data: {\"error\":{\"message\":\"upstream read timeout\",\"type\":\"timeout\",\"code\":\"stream_timeout\"}}\n\n")
				safeFlush(flusher)
				if capture != nil {
					capture.MarkInterruptedWithReason("stream_timeout")
				}
				outcome.Interrupted = true
				outcome.Reason = "stream_timeout"
				outcome.Resumable = true // Timeout is resumable
				outcome.ChunkCount = chunkCount
			default:
				slog.Warn("stream read error", "error", readResult.err)
				if capture != nil {
					capture.MarkInterruptedWithReason("read_error")
				}
				outcome.Interrupted = true
				outcome.Reason = "read_error"
				outcome.Resumable = true // Read error is resumable
				outcome.ChunkCount = chunkCount
			}
			return outcome
		}

		line := readResult.line

		// 2026-06-19 quality fix mode (017_quality_fix_mode.sql). See
		// the first-line equivalent above for the rationale. We run
		// the quality check before XML coercion and normalisation so
		// the scanner sees the raw upstream delta.tool_calls shape.
		qualityMode := qualityFixModeFromContext(ctx)
		if qualityMode != "" && qualityMode != QualityModeOff && capture != nil {
			newLine, newFlags, newSeen := ProcessStreamLine(line, qualityMode, capture.QualityFlags, capture.QualitySeenToolCallIDs)
			if newLine != "" {
				line = newLine
			}
			if len(newFlags) > 0 {
				capture.QualityFlags = newFlags
			}
			if newSeen != nil {
				capture.QualitySeenToolCallIDs = newSeen
			}
		}

		line = coerceXMLToolCallsInStreamLine(line, toolsRequested)

		if clientModel != "" && discoveredUpstream == "" {
			discoveredUpstream = extractModelFromChunk(line)
		}
		if clientModel != "" {
			line = replaceModelInChunk(line, clientModel, discoveredUpstream)
		}

		if stripFn != nil {
			line = stripChunkFields(line, stripFn)
		}

		payload := extractPayload(line)
		if payload != "" && capture != nil {
			finishReason := ExtractFinishReason(payload)
			usage := ExtractUsageFromChunk(payload)
			isDone := payload == "[DONE]"
			if isDone {
				upstreamDoneReceived = true
			}
			capture.ObservePayload(payload, finishReason, isDone)
			if usage.PromptTokens != nil || usage.CompletionTokens != nil {
				capture.ObserveUsage(usage.PromptTokens, usage.CompletionTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			}
		}

		if norm != nil {
			line = string(norm.NormalizeChunk([]byte(line), true))
		}

		// Track C C2: capture the chunk into the pending buffer
		// BEFORE attempting the client write. The capturer is
		// bounded by maxBytes (1 MiB default) so a runaway
		// upstream cannot OOM the gateway.
		if pc != nil {
			pc.append(line)
		}

		// 2026-06-22 fix: Filter empty choices blocks from OpenAI streams.
		// Some upstreams (e.g. glm-5.2 at https://api.supxh.xin) send
		// {"choices":[],"usage":{...}} blocks at stream end, which crash
		// OpenAI clients that assume choices[0] exists. Drop these blocks
		// before writing to the client.
		checkPayload := extractPayload(line)
		if checkPayload != "" && checkPayload != "[DONE]" {
			if isOpenAIFormatData([]byte(checkPayload)) {
				// Check if it has empty choices array
				if strings.Contains(checkPayload, `"choices":[]`) {
					slog.Warn("relay: dropping empty choices block",
						"payload_preview", truncateForLog(checkPayload, 100))
					continue // Skip this chunk
				}
			}
		}

		safeWriteSSE(w, line)
		safeFlush(flusher)
		lastSend = time.Now()
		chunkCount++ // Track chunks sent
	}
}

func extractPayload(line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return ""
	}
	payload := strings.TrimPrefix(line, "data: ")
	return strings.TrimSpace(payload)
}

// stripChunkFields applies stripFn to the JSON payload of a "data: {...}" line.
// Non-data lines (event:, comment:, blank) are returned unchanged.
func stripChunkFields(line string, stripFn func([]byte) []byte) string {
	if !strings.HasPrefix(line, "data: ") || stripFn == nil {
		return line
	}
	payload := strings.TrimPrefix(line, "data: ")
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" {
		return line
	}
	stripped := stripFn([]byte(payload))
	if len(stripped) == 0 {
		return line
	}
	return "data: " + string(stripped) + "\n"
}

func readLineWithTimeout(ctx context.Context, reader *bufio.Reader, timeout time.Duration) (string, error) {
	return newTimedLineReader(reader, nil).ReadLine(ctx, timeout)
}

// readLineWithTimeoutAndCloser is like readLineWithTimeout but also takes the
// underlying io.ReadCloser. On timeout it closes the closer to unblock the
// ReadString goroutine, then drains the channel — eliminating the goroutine
// leak that existed in the plain readLineWithTimeout path (BUG-1 fix).
func readLineWithTimeoutAndCloser(ctx context.Context, reader *bufio.Reader, closer io.ReadCloser, timeout time.Duration) (string, error) {
	return newTimedLineReader(reader, closer).ReadLine(ctx, timeout)
}

type timedLineReader struct {
	reader *bufio.Reader
	// closer is the underlying io.ReadCloser (e.g. resp.Body). When non-nil,
	// ReadLine closes it on timeout so the blocked ReadString goroutine returns
	// immediately rather than leaking until the TCP connection is closed.
	closer io.ReadCloser
}

func newTimedLineReader(reader *bufio.Reader, closer io.ReadCloser) *timedLineReader {
	return &timedLineReader{reader: reader, closer: closer}
}

func (r *timedLineReader) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{"", fmt.Errorf("read panic: %v", r)}
			}
		}()
		line, err := r.reader.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case res := <-ch:
		return res.line, res.err
	case <-readCtx.Done():
		// BUG-1 fix (2026-06-19): close the underlying body to force the
		// blocked ReadString goroutine to return an error immediately.
		// Without this, the goroutine would leak until resp.Body.Close()
		// is called by the deferred cleanup in StreamChatWithPendingCapture,
		// which can be minutes later on the session path (context.Background).
		// After Close(), drain the channel so the goroutine completes before
		// we return — zero goroutine leak guarantee.
		if r.closer != nil {
			_ = r.closer.Close()
		}
		// Drain: the goroutine returns shortly after Close() because
		// ReadString on a closed body returns io.ErrClosedPipe or io.EOF.
		// The buffered channel (size 1) ensures this never blocks forever.
		<-ch
		if readCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("stream read timeout")
		}
		return "", readCtx.Err()
	}
}

func extractModelFromChunk(line string) string {
	if !strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "data: [DONE") {
		return ""
	}
	jsonStr := strings.TrimPrefix(line, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return ""
	}
	if modelRaw, ok := obj["model"]; ok {
		var modelStr string
		if err := json.Unmarshal(modelRaw, &modelStr); err == nil {
			return modelStr
		}
	}
	return ""
}

func safeFlush(flusher http.Flusher) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("flush after close", "recover", r)
		}
	}()
	flusher.Flush()
}

func safeWriteSSE(w io.Writer, line string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("write after close", "recover", r)
		}
	}()
	//nolint:errcheck // test write, non-critical
	io.WriteString(w, line)
}

func replaceModelInChunk(line, clientModel, discoveredUpstream string) string {
	if !strings.HasPrefix(line, "data: ") || clientModel == "" {
		return line
	}
	if strings.HasPrefix(line, "data: [DONE") {
		return line
	}
	jsonStr := strings.TrimPrefix(line, "data: ")
	jsonStr = strings.TrimSpace(jsonStr)

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
		return line
	}
	modelRaw, ok := obj["model"]
	if !ok {
		return line
	}
	var modelStr string
	if err := json.Unmarshal(modelRaw, &modelStr); err != nil {
		return line
	}
	if modelStr == clientModel {
		return line
	}
	if discoveredUpstream != "" && modelStr != discoveredUpstream {
		return line
	}
	obj["model"], _ = json.Marshal(clientModel)
	newJSON, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(newJSON) + "\n"
}

func BuildSSEChunk(data string) string {
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		fmt.Fprintf(&b, "data: %s\n", scanner.Text())
	}
	b.WriteString("\n")
	return b.String()
}

// pendingCapturer (Track C C2, 2026-06-18) records every chunk
// the upstream sends so a client that disconnected mid-stream
// can still recover the response via GET /v1/sessions/{id}/
// pending-response. The capturer is intentionally minimal:
// the streaming hot path stays in StreamChatWithPendingCapture;
// this type only collects bytes and finalises them at the end.
//
// Concurrency: written from one goroutine (the stream loop)
// and finalised from the deferred cleanup of that same
// goroutine. The mutex is belt-and-braces — in practice it's
// never contended.
// PendingFinalState is the state recorded by finalize() and
// read by Snapshot(). cmd/gateway/main.go reads these fields
// after the stream returns to write the captured body to the
// pending store. The fields are exported so the wiring caller
// can read them across the package boundary.
type PendingFinalState struct {
	Status      string
	ErrMessage  string
	CompletedAt int64
}

// pendingCapturer is the unexported canonical name. We also
// export it (below) so cmd/gateway/main.go can hold a
// reference without an awkward constructor signature.
type pendingCapturer struct {
	mu       sync.Mutex
	buffer   []byte
	bytes    int
	maxBytes int

	finalized  bool
	finalState PendingFinalState
}

// PendingCapturer is the exported alias used by the wiring.
// Internally everything uses the unexported name so godoc
// links to the right type.
type PendingCapturer = pendingCapturer

// NewPendingCapturer is the exported constructor; the
// unexported name is the canonical one for godoc + tests.
func NewPendingCapturer(maxBytes int) *pendingCapturer {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &pendingCapturer{maxBytes: maxBytes}
}

// append copies line into the internal buffer up to the cap.
// Once the cap is reached, subsequent chunks are dropped.
// Audit fix 3.3: if a chunk doesn't fully fit, we drop the
// entire chunk rather than truncating mid-JSON. A truncated
// SSE line produces a parse error on replay; dropping it
// leaves the preceding chunks intact (which is better).
func (p *pendingCapturer) append(line string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bytes >= p.maxBytes {
		return
	}
	remaining := p.maxBytes - p.bytes
	if len(line) > remaining {
		// Drop the entire chunk — truncating mid-JSON would
		// produce an invalid SSE line on replay.
		return
	}
	p.buffer = append(p.buffer, line...)
	p.bytes = len(p.buffer)
}

// markInterrupted is called from the panic-recovery path to
// mark the buffer as failed (rather than completed).
func (p *pendingCapturer) markInterrupted(reason string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return
	}
	p.finalState = PendingFinalState{
		Status:      "failed",
		ErrMessage:  "stream_" + reason,
		CompletedAt: time.Now().Unix(),
	}
	p.finalized = true
}

// finalize records the terminal state. A client_cancel with a non-empty
// buffer counts as "completed" because we have the body for replay.
// Other interrupted reasons count as "failed" so the replay surfaces
// the error to the client.
//
// BUG-4 fix (2026-06-19): if the client cancels before any chunk arrives
// (p.bytes == 0), mark the entry "failed" rather than "completed". An
// empty-body "completed" entry is misleading — the GET endpoint already
// guards against it (returning 404 for empty body) but the Status field
// itself is wrong, and any future code path inspecting Status == "completed"
// would misread it as a successful, replayable response.
//
// Track C C5 (2026-06-21): "client_disconnected" (used by the Anthropic
// passthrough path) is treated identically to "client_cancel" for
// replayability — both indicate the upstream kept streaming but the
// client went away mid-stream, so we have the body for replay.
func (p *pendingCapturer) finalize(outcome StreamOutcome) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return
	}
	clientWentAway := outcome.Reason == "client_cancel" || outcome.Reason == "client_disconnected"
	if outcome.Interrupted {
		if clientWentAway && p.bytes > 0 {
			// Client disconnected but we captured at least one chunk —
			// the body is replayable.
			p.finalState = PendingFinalState{
				Status:      "completed",
				CompletedAt: time.Now().Unix(),
			}
		} else if clientWentAway {
			// Client cancelled before the first byte arrived. Nothing to
			// replay; mark failed so the GET endpoint returns a clear error.
			p.finalState = PendingFinalState{
				Status:      "failed",
				ErrMessage:  "client_cancel_before_first_chunk",
				CompletedAt: time.Now().Unix(),
			}
		} else {
			p.finalState = PendingFinalState{
				Status:      "failed",
				ErrMessage:  outcome.Reason,
				CompletedAt: time.Now().Unix(),
			}
		}
	} else {
		p.finalState = PendingFinalState{
			Status:      "completed",
			CompletedAt: time.Now().Unix(),
		}
	}
	p.finalized = true
}

// Snapshot returns a copy of the buffer and final state.
// Exposed for the wiring in cmd/gateway/main.go to read the
// captured body after the stream returns and write it to
// the pending store.
func (p *pendingCapturer) Snapshot() (body []byte, state PendingFinalState, ok bool) {
	if p == nil {
		return nil, PendingFinalState{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.finalized {
		return nil, PendingFinalState{}, false
	}
	out := make([]byte, len(p.buffer))
	copy(out, p.buffer)
	return out, p.finalState, true
}

// BytesCaptured returns the number of bytes in the buffer.
// Exposed for the wiring in cmd/gateway/main.go to compute
// the approximate createdAt timestamp.
func (p *pendingCapturer) BytesCaptured() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bytes
}

// ClientHasSessionID (Track C C2, 2026-06-18) is a small helper
// used by the wiring in cmd/gateway/main.go to decide whether
// to attach a capturer to a stream. The decision mirrors the
// one in routing.hasSessionID: if the upstream request
// carries X-Gw-Session-Id or X-Session-Id, the client is
// eligible for replay and we capture the stream body.
//
// Reads from w.Header() (the request the executor forwarded to
// the upstream) and from resp.Request (the actual http.Request
// the upstream call was built with). Both should agree in
// production; we check both for defence in depth.
func ClientHasSessionID(w http.ResponseWriter, resp *http.Response) bool {
	// w in production is the gateway response writer; we
	// cannot read the original request headers from it.
	// The canonical source is resp.Request.
	if resp == nil || resp.Request == nil {
		return false
	}
	if v := resp.Request.Header.Get("X-Gw-Session-Id"); v != "" {
		return true
	}
	if v := resp.Request.Header.Get("X-Session-Id"); v != "" {
		return true
	}
	return false
}

// SessionIDFromResp (Track C C2) reads the canonical session
// id from the upstream request headers. Returns "" if no
// session is in play — the caller decides whether that is a
// writeable key (it is not: no session means no GET endpoint).
func SessionIDFromResp(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	if v := resp.Request.Header.Get("X-Gw-Session-Id"); v != "" {
		return v
	}
	return resp.Request.Header.Get("X-Session-Id")
}

// RequestIDFromResp (Track C C2) reads the per-request id.
// Falls back to the time-suffixed synthetic id used in
// async-retry when no X-Request-Id is supplied. We use the
// same fallback here so the GET endpoint can always locate
// the entry by request_id.
func RequestIDFromResp(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	if v := resp.Request.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	return ""
}
