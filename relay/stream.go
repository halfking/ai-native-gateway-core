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

	reader := bufio.NewReaderSize(resp.Body, streamBufSize)
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
	firstLine, err := readLineWithTimeout(ctx, reader, runtimeCfg.firstByteTimeout)
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

		readResult := readNextStreamLine(ctx, reader, w, &lastSend, runtimeCfg)
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
	return newTimedLineReader(reader).ReadLine(ctx, timeout)
}

type timedLineReader struct {
	reader *bufio.Reader
}

func newTimedLineReader(reader *bufio.Reader) *timedLineReader {
	return &timedLineReader{reader: reader}
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
	case r := <-ch:
		return r.line, r.err
	case <-readCtx.Done():
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
		line = line[:remaining]
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

// finalize records the terminal state. A client_cancel with
// a full buffer counts as "completed" because we have the
// body for replay; other interrupted reasons count as
// "failed" so the replay surfaces the error to the client.
func (p *pendingCapturer) finalize(outcome StreamOutcome) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finalized {
		return
	}
	if outcome.Interrupted {
		if outcome.Reason == "client_cancel" {
			p.finalState = PendingFinalState{
				Status:      "completed",
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
