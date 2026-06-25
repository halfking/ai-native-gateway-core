// Package anthropic 中转 relay 包的支撑类型和函数。
//
// 背景：R1.8 把 21 个 anthropic_*.go 文件从 relay/ 迁到 domains/transformation/anthropic/，
// 迁移目标是把 anthropic 协议转换代码独立成"完整可编译"的包。但 anthropic 文件
// 依赖了 relay/ 包的若干未导出符号（pendingCapturer/StreamOutcome/streamRuntimeConfig
// 等），而 Go 不允许跨包访问未导出符号，因此需要把支撑代码一起带过来。
//
// 本文件就是把 relay/ 包里 anthropic 文件实际用到的支撑代码（≈ 300 行）原样搬过来，
// 仅修改包名，不修改任何逻辑。这样：
//   - 新 anthropic 包可以独立编译（go build ./domains/transformation/anthropic/）
//   - 旧 relay/ 包继续保留自己的副本（这次迁移只动 anthropic 子树）
//   - 两份代码的语义完全一致，不引入行为差异
//
// 包含的支撑代码来自：
//   - relay/stream.go       (streamBufSize / sseKeepaliveComment / StreamOutcome /
//     pendingCapturer+PendingCapturer / NewPendingCapturer /
//     PendingFinalState / readLineWithTimeoutAndCloser /
//     timedLineReader+newTimedLineReader)
//   - relay/stream_runtime.go (streamRuntimeConfig / currentStreamRuntimeConfig /
//     SetConfigStore / StreamTimeout / UpstreamTimeout /
//     durationSecondsOrDefault / envDurationSeconds)
//   - relay/stream_reader.go  (streamReadResult / streamKeepaliveWriter /
//     readNextStreamLine / maybeSendKeepalive)
//   - relay/stream_errors.go  (streamReadState + 5 个常量 / classifyStreamReadError)
package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/_to-be-deprecated/relay"
)

// =====================================================================
// 来自 relay/stream.go 的常量与类型
// =====================================================================

const (
	streamBufSize       = 64 * 1024
	sseKeepaliveComment = ": keep-alive\n\n"
)

// StreamOutcome describes the terminal result of a streaming call.
// The relay caller inspects this to decide how to record the request
// in audit/observability.
type StreamOutcome struct {
	Interrupted bool
	Reason      string
	Resumable   bool // Whether the stream can be resumed with a different credential
	ChunkCount  int  // Number of chunks sent before interruption
}

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
func (p *pendingCapturer) finalize(outcome relay.StreamOutcome) {
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

// readLineWithTimeout is the BUG-1 fix variant of
// readLineWithTimeoutAndCloser: it does not have a closer to
// unblock the read goroutine on timeout, so callers must already
// have wired their own cleanup. The anthropic first-byte path
// uses this (no closer because the body lifetime is managed by
// the defer in StreamOpenAIToAnthropicSSE).
func readLineWithTimeout(ctx context.Context, reader *bufio.Reader, timeout time.Duration) (string, error) {
	return newTimedLineReader(reader, nil).ReadLine(ctx, timeout)
}

// readLineWithTimeoutAndCloser is the BUG-1 fix variant of
// readLineWithTimeout: it also closes the underlying body on
// timeout to unblock the ReadString goroutine. Used by
// readNextStreamLine for the chunk loop and by the first-byte
// path in StreamChatWithPendingCapture.
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

// safeFlush is a small wrapper around flusher.Flush that recovers
// from "flush after close" panics. Used in the streaming hot path
// where the client may have disconnected between write and flush.
func safeFlush(flusher http.Flusher) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("flush after close", "recover", r)
		}
	}()
	flusher.Flush()
}

// safeWriteSSE wraps io.WriteString with a panic-recovery so a
// client disconnect mid-stream does not crash the worker.
func safeWriteSSE(w io.Writer, line string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("write after close", "recover", r)
		}
	}()
	//nolint:errcheck // best-effort write
	io.WriteString(w, line)
}

// =====================================================================
// 来自 relay/stream_runtime.go 的运行时配置
// =====================================================================

type streamRuntimeConfig struct {
	upstreamTimeout    time.Duration
	streamTimeout      time.Duration
	streamChunkTimeout time.Duration
	firstByteTimeout   time.Duration
	keepaliveInterval  time.Duration
}

var streamConfigStore atomic.Pointer[config.Store]

// SetConfigStore is the wiring hook called by cmd/gateway/main.go
// during startup. Stream reads from it via currentStreamRuntimeConfig.
// We hold an atomic.Pointer to avoid blocking the stream path.
func SetConfigStore(store *config.Store) {
	streamConfigStore.Store(store)
}

// currentStreamRuntimeConfig returns the current effective stream
// runtime config. The config store is consulted first (it carries
// admin overrides); if unset, the env-driven defaults win. The
// indirection via streamConfigStore lets us hot-reload config
// without restarting the stream path.
func currentStreamRuntimeConfig() streamRuntimeConfig {
	if store := streamConfigStore.Load(); store != nil {
		if cfg := store.Get(); cfg != nil {
			return streamRuntimeConfig{
				upstreamTimeout:    durationSecondsOrDefault(cfg.UpstreamTimeout, 120*time.Second),
				streamTimeout:      durationSecondsOrDefault(cfg.StreamTimeout, 900*time.Second),
				streamChunkTimeout: durationSecondsOrDefault(cfg.StreamChunkTimeout, 300*time.Second),
				firstByteTimeout:   durationSecondsOrDefault(cfg.FirstByteTimeout, 30*time.Second),
				keepaliveInterval:  durationSecondsOrDefault(cfg.KeepaliveInterval, 15*time.Second),
			}
		}
	}
	return streamRuntimeConfig{
		upstreamTimeout:    envDurationSeconds("LLM_GATEWAY_UPSTREAM_TIMEOUT", 120*time.Second),
		streamTimeout:      envDurationSeconds("LLM_GATEWAY_STREAM_TIMEOUT", 900*time.Second),
		streamChunkTimeout: envDurationSeconds("LLM_GATEWAY_STREAM_CHUNK_TIMEOUT", 300*time.Second),
		firstByteTimeout:   envDurationSeconds("LLM_GATEWAY_FIRST_BYTE_TIMEOUT", 30*time.Second),
		keepaliveInterval:  envDurationSeconds("LLM_GATEWAY_KEEPALIVE_INTERVAL", 15*time.Second),
	}
}

func durationSecondsOrDefault(seconds int, def time.Duration) time.Duration {
	if seconds <= 0 {
		return def
	}
	return time.Duration(seconds) * time.Second
}

func envDurationSeconds(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	s, err := strconv.Atoi(v)
	if err != nil || s <= 0 {
		return def
	}
	return time.Duration(s) * time.Second
}

// StreamTimeout returns the effective overall stream timeout
// for the current configuration. Exposed for callers that need
// to set their own context deadlines (e.g. executor_anthropic).
func StreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().streamTimeout
}

// UpstreamTimeout returns the effective upstream HTTP call timeout
// (separate from the per-chunk read timeout). Used by executors
// that issue the upstream call and want to align their net.Dial /
// http.Client.Timeout with the stream config.
func UpstreamTimeout() time.Duration {
	return currentStreamRuntimeConfig().upstreamTimeout
}

// =====================================================================
// 来自 relay/stream_reader.go 的流读取逻辑
// =====================================================================

type streamReadResult struct {
	line  string
	state streamReadState
	err   error
	EOF   bool
}

// readNextStreamLine reads the next SSE line from the upstream response.
// closer is the underlying io.ReadCloser (resp.Body); it is passed through
// to readLineWithTimeoutAndCloser so that on chunk timeout the body is
// closed immediately, unblocking the ReadString goroutine (BUG-1 fix,
// 2026-06-19). Pass nil when no closer is available (e.g. tests,
// anthropic_stream.go which manages its own body lifecycle).
func readNextStreamLine(ctx context.Context, reader *bufio.Reader, closer io.ReadCloser, w streamKeepaliveWriter, lastSend *time.Time, runtimeCfg streamRuntimeConfig) streamReadResult {
	maybeSendKeepalive(w, lastSend, runtimeCfg.keepaliveInterval)
	line, err := readLineWithTimeoutAndCloser(ctx, reader, closer, runtimeCfg.streamChunkTimeout)
	if err != nil {
		state := classifyStreamReadError(ctx, err)
		return streamReadResult{line: line, state: state, err: err, EOF: state == streamReadEOF}
	}
	return streamReadResult{line: line, state: streamReadNext}
}

type streamKeepaliveWriter interface {
	Write([]byte) (int, error)
}

func maybeSendKeepalive(w streamKeepaliveWriter, lastSend *time.Time, keepaliveInterval time.Duration) {
	if w == nil || lastSend == nil || keepaliveInterval <= 0 {
		return
	}
	if time.Since(*lastSend) <= keepaliveInterval {
		return
	}
	_, _ = w.Write([]byte(sseKeepaliveComment))
	*lastSend = time.Now()
}

// =====================================================================
// 来自 relay/stream_errors.go 的流错误分类
// =====================================================================

type streamReadState int

const (
	streamReadNext streamReadState = iota
	streamReadEOF
	streamReadCanceled
	streamReadTimeout
	streamReadFailed
)

func classifyStreamReadError(ctx context.Context, err error) streamReadState {
	if err == nil {
		return streamReadNext
	}
	if err == io.EOF || strings.Contains(err.Error(), "EOF") {
		return streamReadEOF
	}
	if ctx != nil && ctx.Err() == context.Canceled {
		return streamReadCanceled
	}
	if strings.Contains(err.Error(), "context canceled") {
		return streamReadCanceled
	}
	if strings.Contains(err.Error(), "timeout") {
		return streamReadTimeout
	}
	return streamReadFailed
}

// =====================================================================
// 来自 relay/stream_error_body.go 的 JSON error body 识别
// =====================================================================

// jsonErrorBody is the inner error object inside the standard
// `{"error": {...}}` envelope used by OpenAI / Anthropic / most
// proxy-style upstreams. The fields are deliberately permissive:
//   - Type:    the upstream's semantic classification
//     (e.g. "service_unavailable", "insufficient_quota")
//   - Code:    the upstream's machine-readable code when Type is
//     absent (some upstreams use one or the other, never both)
//   - Message: human-readable reason
//   - Param:   optional structured field (kept so a future audit
//     column can surface it without re-parsing the body)
type jsonErrorBody struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

// jsonErrorEnvelope is a single struct that tolerates BOTH of
// the production shapes we have observed (2026-06-20 audit):
//
//	{"error": {"type": "...", "message": "..."}}    — OpenAI / proxies
//	{"error": {"code": "...", "message": "..."}}    — quota / billing
//	{"type": "...", "message": "..."}               — Anthropic-style
//
// The pointer + plain fields mean a single Unmarshal populates
// the right slot regardless of which shape arrived. The caller
// then resolves which slot is non-empty.
type jsonErrorEnvelope struct {
	Error   *jsonErrorBody `json:"error,omitempty"`
	Type    string         `json:"type,omitempty"`
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
}

// resolveError returns the (kind, message) pair for an envelope
// regardless of which shape it took. kind is preferred from
// inside the "error" wrapper when present, then from the
// top-level type / code fields. message is preferred from the
// inner wrapper, then from the top-level message.
func (e *jsonErrorEnvelope) resolveError() (kind string, message string) {
	if e.Error != nil {
		kind = firstNonEmpty(e.Error.Code, e.Error.Type)
		message = e.Error.Message
	}
	if kind == "" {
		kind = firstNonEmpty(e.Code, e.Type)
	}
	if message == "" {
		message = e.Message
	}
	return kind, message
}

// isJSONErrorBody returns true when the given raw bytes look like
// a non-SSE JSON error body returned by an upstream provider. The
// caller is expected to pass either the entire body or just the
// first line; the function tolerates trailing whitespace, SSE
// "data: " prefixes, and both envelope shapes (with and without
// the outer "error" wrapper).
//
// Recognised shapes (audit 2026-06-20):
//   - {"error": {"type": "service_unavailable", "message": "..."}}
//   - {"error": {"code": "insufficient_quota", "message": "..."}}
//   - {"type": "upstream_error", "message": "..."}   (Anthropic bare)
//
// Returns (true, errorType, errorMessage) on match, (false, "", "")
// otherwise. errorType feeds the audit "failure_detail_code" column
// (so operators can group "积分不足" hits together) and errorMessage
// is the human-readable reason that goes into slog + response_body
// preview.
func isJSONErrorBody(body []byte) (bool, string, string) {
	if len(body) == 0 {
		return false, "", ""
	}
	// Trim trailing whitespace and stray SSE terminator fragments
	// so a body of `{"error":...}\n\n` still parses.
	trimmed := strings.TrimRight(string(body), " \t\r\n")
	if trimmed == "" {
		return false, "", ""
	}
	// Defensive: a stream reader might pass an SSE-prefixed line
	// to this helper by mistake. Strip the prefix and re-test
	// the JSON shape so the helper stays robust to that call site
	// bug. After stripping, we still require the body to start
	// with '{' so legitimate SSE comments (":heartbeat") and
	// "event:" lines don't false-positive.
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed[0] != '{' {
		return false, "", ""
	}
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return false, "", ""
	}
	kind, msg := env.resolveError()
	if kind == "" && msg == "" {
		return false, "", ""
	}
	return true, kind, msg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// =====================================================================
// 来自 relay/messages.go 的 Anthropic stop reason 映射
// =====================================================================

// mapAnthropicStopReason normalises an OpenAI-style finish_reason
// into the Anthropic "stop_reason" string the Anthropic client
// expects. We treat "stop"/"end_turn" and "content_filter" all
// as "end_turn" (Anthropic has no separate "filter_hit" stop
// reason) and "tool_calls"/"function_call" as "tool_use".
func mapAnthropicStopReason(finishReason string) string {
	switch strings.ToLower(finishReason) {
	case "stop", "end_turn":
		return "end_turn"
	case "tool_calls", "function_call":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// =====================================================================
// 来自 relay/openai_format_detector.go 的 OpenAI 格式检测与日志截断
// =====================================================================

// isOpenAIFormatData performs a coarse string-based check to detect
// OpenAI-format data that should not appear in an Anthropic SSE stream.
// This is a fast pre-filter that runs before JSON parsing.
//
// Used in Q3 path (OpenAI client -> Anthropic upstream) to catch
// upstreams that leak OpenAI-format chunks into their Anthropic streams.
// See anthropic_to_openai_stream.go for the call site.
//
// Checks for OpenAI-specific fields that should never appear in Anthropic
// Messages streaming events:
//   - "choices" - OpenAI chat completions field
//   - "created" - OpenAI timestamp field (Anthropic uses ISO8601 strings)
//   - "object":"chat.completion" - OpenAI object type
//
// Returns true if the data appears to be OpenAI format and should be dropped.
func isOpenAIFormatData(data []byte) bool {
	dataStr := string(data)

	// Quick exit: if data is too short, it can't be a valid event
	if len(dataStr) < 10 {
		return false
	}

	// Check 1: Contains "choices" field as a top-level key (most common OpenAI indicator)
	// Use more specific pattern to avoid false positives when "choices" appears in text content
	if strings.Contains(dataStr, `"choices":[`) || strings.Contains(dataStr, `"choices": [`) {
		return true
	}

	// Check 2: Contains both "object" and "chat.completion"
	// (OpenAI chat completion signature)
	if strings.Contains(dataStr, `"object"`) &&
		strings.Contains(dataStr, `"chat.completion`) {
		return true
	}

	// Check 3: Contains "created" as a numeric field
	// Anthropic uses "created_at" with ISO8601 strings, not unix timestamps
	// Pattern: "created":1234567890 (numeric follows)
	if strings.Contains(dataStr, `"created":`) {
		// Look for the pattern with a digit after the colon
		idx := strings.Index(dataStr, `"created":`)
		if idx >= 0 && idx+10 < len(dataStr) {
			// Check if there's a digit after "created":
			afterColon := strings.TrimSpace(dataStr[idx+10:])
			if len(afterColon) > 0 && afterColon[0] >= '0' && afterColon[0] <= '9' {
				return true
			}
		}
	}

	return false
}

// truncateForLog truncates a string for logging, adding "..." if truncated.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
