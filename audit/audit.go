package audit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Event struct {
	RequestID      string    `json:"request_id"`
	Timestamp      time.Time `json:"ts"`
	TenantID       string    `json:"tenant_id"`
	ApplicationID  int       `json:"application_id"`
	APIKeyID       int       `json:"api_key_id"`
	ClientModel    string    `json:"client_model"`
	OutboundModel  string    `json:"outbound_model"`
	ResolutionPath string    `json:"resolution_path,omitempty"`
	CanonicalName  string    `json:"canonical_name,omitempty"`
	IdentityHash   string    `json:"identity_hash"`
	ClientProfile  string    `json:"client_profile,omitempty"`
	Stream         bool      `json:"stream"`
	ProviderID     int       `json:"provider_id"`
	CredentialID   int       `json:"credential_id"`
	LatencyMs      int       `json:"latency_ms"`
	Success        bool      `json:"success"`
	ErrorKind      string    `json:"error_kind,omitempty"`
	FailureStage   string    `json:"failure_stage,omitempty"`
	PromptTokens   int       `json:"prompt_tokens,omitempty"`
	CompletionToken int      `json:"completion_tokens,omitempty"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	TransformRule  string    `json:"transform_rule,omitempty"`
	RequestChecksum string   `json:"request_checksum,omitempty"`
	StreamChunkCount int     `json:"stream_chunk_count,omitempty"`
	StreamTTFBMs   int       `json:"stream_ttfb_ms,omitempty"`
	DecisionTrace  any       `json:"decision_trace,omitempty"`
	StreamDone     bool      `json:"stream_done,omitempty"`
	StreamInterrupted bool   `json:"stream_interrupted,omitempty"`
	// Link layer event fields
	EventType      string    `json:"event_type,omitempty"`      // Event type (e.g., "credential_switch", "pool_state_change")
	FromCredential int       `json:"from_credential,omitempty"` // Previous credential ID
	ToCredential   int       `json:"to_credential,omitempty"`   // New credential ID
	Reason         string    `json:"reason,omitempty"`          // Reason for state change
	ChunkCount     int       `json:"chunk_count,omitempty"`     // Chunks sent before interruption
	FromState      string    `json:"from_state,omitempty"`      // Previous state
	ToState        string    `json:"to_state,omitempty"`        // New state
}

// maxTextContentBytes caps the reconstructed stream text per request. The
// reconstructed body is emitted into request_logs.response_body so downstream
// auditors/queries can inspect the full assistant message. 128 KiB is large
// enough for the longest completions seen in production (max observed
// completion_tokens in 24h ≈ 4500 ≈ 18 KiB) and still keeps per-request
// memory bounded under high concurrency (100 concurrent captures ≈ 12 MiB).
const maxTextContentBytes = 128 * 1024

// isInterruptionCode reports whether a `finalFinish` value looks like a
// stream-interruption / failure code (rather than a normal upstream
// finish_reason). Used by StreamCapture.SummaryAsMap to decide whether
// to also publish the value as failure_detail_code.
//
// 2026-06-19 T-NEW-7: prior to the split, this code was implicitly
// encoded by the position of the write — finish reasons ("stop",
// "tool_calls", "length", "end_turn", "function_call", "max_tokens")
// happened to look different from interruption codes
// ("eof_without_done", "stream_timeout", "client_cancel", …) at
// display time, but they were both written to the same column, so
// the admin UI could not tell a successful "tool_calls" finish from
// an actual "tool_calls" failure. The whitelist below is the
// authoritative list of "this is a real failure / interruption".
func isInterruptionCode(s string) bool {
	switch s {
	// Stream-level interruption codes (relay/handler.go::classifyStreamInterruption
	// + relay/stream.go).
	case "eof_without_done",
		"stream_timeout",
		"client_cancel",
		"client_disconnected",
		"no_deltas",
		"invalid_first_chunk",
		"invalid_json",
		"upstream_5xx",
		"upstream_4xx",
		"unexpected_status",
		"connection_reset",
		"write_failed",
		"hangup",
		"body_too_large",
		"eof_mid_tool_call":
		return true
	}
	return false
}

type StreamCapture struct {
	mu               sync.Mutex
	startTime        time.Time
	chunkCount       int
	firstChunkMs     int
	doneReceived     bool
	interrupted      bool
	checksum         [32]byte
	finalFinish      string
	preview          []byte
	textContent      []byte
	promptTokens     *int
	completionTokens *int
	cacheReadTokens  *int
	cacheWriteTokens *int
	// HasThinking is set when the stream contained at least one
	// Anthropic-style thinking content block. Detected in the
	// side-channel audit of the Q4 passthrough path.
	HasThinking bool
	// ThinkingBlocksN counts the number of content_block_start events
	// with type=thinking. Used for audit (how deeply did the model
	// reason) and cost estimation.
	ThinkingBlocksN int
	// ModelMismatch is set when the upstream response model name
	// does not match the request model name (case-insensitive).
	// Detected in the side-channel audit; surfaces in
	// SummaryAsMap as a soft-mismatch flag for the admin UI.
	ModelMismatch bool
	// QualityFlags accumulates tool_call quality issues detected during
	// the stream (empty names, duplicate ids). Populated by the
	// stream-side quality processor in relay/stream.go; merged into
	// request_logs.quality_flags by relay/handler.go emitTelemetry.
	// Empty when quality_fix_mode='off' for the chosen provider.
	QualityFlags []string
	// QualityFixActions is the JSON-encoded per-flag action summary
	// for the stream path. Mostly empty — the streaming variant
	// reports detected vs renamed counts per chunk but does not
	// produce the deep fix-actions JSONB that the non-stream path
	// does. Keep the field so the schema stays symmetric.
	QualityFixActions []byte
	// QualityScore mirrors the non-stream QualityScore from
	// ProcessNonStreamBody. nil when no issues were found OR when
	// the provider is in 'off' mode.
	QualityScore *float64
	// QualitySeenToolCallIDs is the per-stream id dedup map used by
	// ProcessStreamLine. Not part of the audit summary; only the
	// stream processor reads/writes it.
	QualitySeenToolCallIDs map[string]int
	// InputTokens / OutputTokens mirror promptTokens /
	// completionTokens under Anthropic naming (input_tokens /
	// output_tokens in the SSE event payloads). The Q4 passthrough
	// populates these directly from message_start / message_delta
	// events so the executor can read them without a separate
	// extraction pass.
	InputTokens  *int
	OutputTokens *int
}

func NewStreamCapture() *StreamCapture {
	return &StreamCapture{
		startTime: time.Now(),
	}
}

func (sc *StreamCapture) RecordChunk(payload []byte) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.chunkCount++
	elapsed := int(time.Since(sc.startTime).Milliseconds())
	if sc.firstChunkMs == 0 {
		sc.firstChunkMs = elapsed
	}
	sc.checksum = sha256.Sum256(append(sc.checksum[:], payload...))
}

func (sc *StreamCapture) RecordDone() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.doneReceived = true
}

func (sc *StreamCapture) MarkInterrupted() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.interrupted = true
}

// Reset clears all accumulated state so the capture can be reused for a
// fresh stream attempt. Used by the executor when failing over to a new
// credential mid-request — without Reset, the new attempt's textContent
// would be appended to the previous attempt's textContent and the
// interrupted/done flags would carry over (logical inconsistency).
func (sc *StreamCapture) Reset() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.startTime = time.Now()
	sc.chunkCount = 0
	sc.firstChunkMs = 0
	sc.doneReceived = false
	sc.interrupted = false
	sc.checksum = [32]byte{}
	sc.finalFinish = ""
	sc.preview = sc.preview[:0]
	sc.textContent = sc.textContent[:0]
	sc.promptTokens = nil
	sc.completionTokens = nil
	sc.cacheReadTokens = nil
	sc.cacheWriteTokens = nil
	sc.HasThinking = false
	sc.ThinkingBlocksN = 0
	sc.ModelMismatch = false
	sc.InputTokens = nil
	sc.OutputTokens = nil
}

func (sc *StreamCapture) Snapshot() (chunkCount, ttfbMs int, done, interrupted bool, checksum string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.chunkCount, sc.firstChunkMs, sc.doneReceived, sc.interrupted, hex.EncodeToString(sc.checksum[:])
}

// appendText appends s to textContent, truncating s if it would push the
// total beyond maxTextContentBytes. This guarantees strict adherence to the
// cap regardless of chunk size.
//
// The truncation is rune-aware: cutting in the middle of a multi-byte UTF-8
// sequence (e.g. Chinese / emoji) would produce invalid bytes that PostgreSQL
// rejects with SQLSTATE 22021, dropping the entire request_logs row.  See
// incident 2026-06-11.
func (sc *StreamCapture) appendText(s string) {
	if s == "" {
		return
	}
	remaining := maxTextContentBytes - len(sc.textContent)
	if remaining <= 0 {
		return
	}
	if len(s) > remaining {
		s = safeTruncateUTF8(s, remaining)
	}
	sc.textContent = append(sc.textContent, s...)
}

// safeTruncateUTF8 returns the longest valid-UTF-8 prefix of s whose byte
// length is <= limit.  Never returns an invalid byte sequence; may return ""
// when limit is smaller than the first rune.
func safeTruncateUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := 0
	for i, r := range s {
		next := i + utf8.RuneLen(r)
		if next > limit {
			cut = i
			break
		}
		cut = next
	}
	return s[:cut]
}

func (sc *StreamCapture) ObservePayload(payload string, finishReason string, done bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if done && payload == "[DONE]" {
		if finishReason != "" {
			sc.finalFinish = finishReason
		}
		sc.doneReceived = true
		return
	}
	if payload == "" {
		if finishReason != "" {
			sc.finalFinish = finishReason
		}
		if done {
			sc.doneReceived = true
		}
		return
	}
	sc.chunkCount++
	elapsed := int(time.Since(sc.startTime).Milliseconds())
	if sc.firstChunkMs == 0 {
		sc.firstChunkMs = elapsed
	}
	sc.checksum = sha256.Sum256(append(sc.checksum[:], []byte(payload)...))
	if len(sc.preview) < 2048 {
		remaining := 2048 - len(sc.preview)
		if len(payload) > remaining {
			payload = safeTruncateUTF8(payload, remaining)
		}
		sc.preview = append(sc.preview, payload...)
	}
	// Capture reasoning chain and final answer with markers so downstream
	// auditors can distinguish the two sections. The reasoning block comes
	// first because reasoning models emit it before the final answer.
	reasoning := extractDeltaReasoningText(payload)
	if reasoning != "" {
		sc.appendText("<reasoning>\n")
		sc.appendText(reasoning)
		sc.appendText("\n</reasoning>\n")
	}
	text := extractDeltaText(payload)
	if text != "" {
		sc.appendText(text)
	}
	toolText := extractDeltaToolText(payload)
	if toolText != "" {
		sc.appendText(toolText)
	}
	if finishReason != "" {
		sc.finalFinish = finishReason
	}
	if done {
		sc.doneReceived = true
	}
}

func (sc *StreamCapture) ObserveUsage(promptTokens, completionTokens, cacheRead, cacheWrite *int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if promptTokens != nil {
		sc.promptTokens = promptTokens
	}
	if completionTokens != nil {
		sc.completionTokens = completionTokens
	}
	if cacheRead != nil {
		sc.cacheReadTokens = cacheRead
	}
	if cacheWrite != nil {
		sc.cacheWriteTokens = cacheWrite
	}
}

func (sc *StreamCapture) MarkInterruptedWithReason(finishReason string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.interrupted = true
	if finishReason != "" {
		sc.finalFinish = finishReason
	}
}

func (sc *StreamCapture) MarkDone() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.doneReceived = true
}

func (sc *StreamCapture) MarkStreamError() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.interrupted = true
}

// Link layer event types
const (
	EventTypeCredentialSwitch = "credential_switch" // Credential switched during stream
	EventTypePoolStateChange  = "pool_state_change" // Pool state changed
	EventTypeFailover         = "failover"          // Failover to next candidate
	EventTypeGracePeriod      = "grace_period"      // Grace period event
	EventTypeTunnelStatus     = "tunnel_status"     // NPS tunnel status
)

// EmitCredentialSwitch emits a credential switch event.
func EmitCredentialSwitch(sink Sink, requestID string, fromCred, toCred int, reason string, chunkCount int) {
	if sink == nil {
		return
	}
	event := Event{
		RequestID:    requestID,
		Timestamp:    time.Now(),
		EventType:    EventTypeCredentialSwitch,
		FromCredential: fromCred,
		ToCredential: toCred,
		Reason:       reason,
		ChunkCount:   chunkCount,
	}
	sink.Emit(context.Background(), event)
	slog.Info("audit: credential switch",
		"event", EventTypeCredentialSwitch,
		"request_id", requestID,
		"from_credential", fromCred,
		"to_credential", toCred,
		"reason", reason,
		"chunk_count", chunkCount,
	)
}

// EmitPoolStateChange emits a pool state change event.
func EmitPoolStateChange(sink Sink, poolKey string, fromState, toState string, failCount int) {
	if sink == nil {
		return
	}
	event := Event{
		RequestID: fmt.Sprintf("pool-%d", time.Now().UnixMilli()),
		Timestamp: time.Now(),
		EventType: EventTypePoolStateChange,
		FromState: fromState,
		ToState:   toState,
		Reason:    fmt.Sprintf("failures=%d", failCount),
	}
	sink.Emit(context.Background(), event)
	slog.Info("audit: pool state change",
		"event", EventTypePoolStateChange,
		"pool_key", poolKey,
		"from_state", fromState,
		"to_state", toState,
		"fail_count", failCount,
	)
}

// EmitFailover emits a failover event.
func EmitFailover(sink Sink, requestID string, fromCred, toCred int, reason string) {
	if sink == nil {
		return
	}
	event := Event{
		RequestID:      requestID,
		Timestamp:      time.Now(),
		EventType:      EventTypeFailover,
		FromCredential: fromCred,
		ToCredential:   toCred,
		Reason:         reason,
	}
	sink.Emit(context.Background(), event)
	slog.Info("audit: failover",
		"event", EventTypeFailover,
		"request_id", requestID,
		"from_credential", fromCred,
		"to_credential", toCred,
		"reason", reason,
	)
}

func (sc *StreamCapture) SummaryAsMap() map[string]any {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	m := map[string]any{
		"stream_chunk_count":   sc.chunkCount,
		"stream_done_received": sc.doneReceived,
		"stream_interrupted":   sc.interrupted,
	}
	if sc.chunkCount > 0 {
		m["stream_first_chunk_ms"] = sc.firstChunkMs
		m["response_checksum"] = hex.EncodeToString(sc.checksum[:])
	}
	if len(sc.preview) > 0 {
		m["response_preview"] = string(sc.preview)
	}
	if len(sc.textContent) > 0 {
		m["stream_text_content"] = string(sc.textContent)
	}
	if sc.finalFinish != "" {
		// 2026-06-19 T-NEW-7: stop overloading failure_detail_code with the
		// upstream finish_reason. The capture used to publish the
		// finish_reason under "failure_detail_code" for BOTH success and
		// failure rows, which made the admin UI show "失败详情: tool_calls"
		// for a perfectly normal 200 OK tool-call response. We now:
		//   - always emit upstream_finish_reason (the SOLE home for the
		//     upstream finish_reason, populated for success AND failure).
		//   - emit failure_detail_code only when the value is a known
		//     interruption/failure code (i.e. when MarkInterruptedWithReason
		//     or classifyStreamInterruption put it there). For the common
		//     "successful stream ended with stop/tool_calls/length" case,
		//     failure_detail_code is intentionally left absent.
		m["upstream_finish_reason"] = sc.finalFinish
		if isInterruptionCode(sc.finalFinish) {
			m["failure_detail_code"] = sc.finalFinish
		}
	}
	if sc.promptTokens != nil {
		m["prompt_tokens"] = *sc.promptTokens
	}
	if sc.completionTokens != nil {
		m["completion_tokens"] = *sc.completionTokens
	}
	if sc.cacheReadTokens != nil {
		m["cache_read_tokens"] = *sc.cacheReadTokens
	}
	if sc.cacheWriteTokens != nil {
		m["cache_write_tokens"] = *sc.cacheWriteTokens
	}
	if sc.InputTokens != nil {
		m["input_tokens"] = *sc.InputTokens
	}
	if sc.OutputTokens != nil {
		m["output_tokens"] = *sc.OutputTokens
	}
	if sc.HasThinking {
		m["has_thinking"] = true
		m["thinking_blocks_n"] = sc.ThinkingBlocksN
	}
	if sc.ModelMismatch {
		m["model_mismatch"] = true
	}
	// 2026-06-19 quality fix mode (017_quality_fix_mode.sql): surface
	// the stream-collected quality signals so emitTelemetry can persist
	// them on the request_log row. quality_flags and quality_score
	// are scalar; quality_fix_actions is JSONB-as-string (decoded
	// back into the structured column on the consumer side).
	if len(sc.QualityFlags) > 0 {
		m["quality_flags"] = sc.QualityFlags
	}
	if len(sc.QualityFixActions) > 0 {
		m["quality_fix_actions"] = string(sc.QualityFixActions)
	}
	if sc.QualityScore != nil {
		m["quality_score"] = *sc.QualityScore
	}
	return m
}

type EventBuilder struct {
	event Event
}

func NewEvent() *EventBuilder {
	now := time.Now()
	return &EventBuilder{
		event: Event{
			RequestID: fmt.Sprintf("go-%d-%s", now.UnixMilli(), randomHex(8)),
			Timestamp: now,
		},
	}
}

func (b *EventBuilder) RequestID(id string) *EventBuilder      { b.event.RequestID = id; return b }
func (b *EventBuilder) ClientModel(m string) *EventBuilder     { b.event.ClientModel = m; return b }
func (b *EventBuilder) OutboundModel(m string) *EventBuilder   { b.event.OutboundModel = m; return b }
func (b *EventBuilder) ResolutionPath(p string) *EventBuilder  { b.event.ResolutionPath = p; return b }
func (b *EventBuilder) CanonicalName(n string) *EventBuilder   { b.event.CanonicalName = n; return b }
func (b *EventBuilder) DecisionTrace(t any) *EventBuilder     { b.event.DecisionTrace = t; return b }
func (b *EventBuilder) IdentityHash(h string) *EventBuilder    { b.event.IdentityHash = h; return b }
func (b *EventBuilder) ClientProfile(p string) *EventBuilder   { b.event.ClientProfile = p; return b }
func (b *EventBuilder) Stream(s bool) *EventBuilder            { b.event.Stream = s; return b }
func (b *EventBuilder) Provider(id int) *EventBuilder          { b.event.ProviderID = id; return b }
func (b *EventBuilder) Credential(id int) *EventBuilder        { b.event.CredentialID = id; return b }
func (b *EventBuilder) Success(s bool) *EventBuilder           { b.event.Success = s; return b }
func (b *EventBuilder) ErrorKind(k string) *EventBuilder       { b.event.ErrorKind = k; return b }
func (b *EventBuilder) FailureStage(s string) *EventBuilder    { b.event.FailureStage = s; return b }
func (b *EventBuilder) Tokens(p, c int) *EventBuilder          { b.event.PromptTokens = p; b.event.CompletionToken = c; return b }
func (b *EventBuilder) Cost(usd float64) *EventBuilder         { b.event.CostUSD = usd; return b }
func (b *EventBuilder) TransformRule(r string) *EventBuilder   { b.event.TransformRule = r; return b }
func (b *EventBuilder) TenantID(id string) *EventBuilder       { b.event.TenantID = id; return b }
func (b *EventBuilder) ApplicationID(id int) *EventBuilder     { b.event.ApplicationID = id; return b }
func (b *EventBuilder) APIKeyID(id int) *EventBuilder          { b.event.APIKeyID = id; return b }

func (b *EventBuilder) Latency(d time.Duration) *EventBuilder {
	b.event.LatencyMs = int(d.Milliseconds())
	return b
}

func (b *EventBuilder) RequestChecksum(body []byte) *EventBuilder {
	h := sha256.Sum256(body)
	b.event.RequestChecksum = hex.EncodeToString(h[:])
	return b
}

func (b *EventBuilder) StreamMetrics(sc *StreamCapture) *EventBuilder {
	if sc == nil {
		return b
	}
	count, ttfb, done, interrupted, _ := sc.Snapshot()
	b.event.StreamChunkCount = count
	b.event.StreamTTFBMs = ttfb
	b.event.StreamDone = done
	b.event.StreamInterrupted = interrupted
	return b
}

func (b *EventBuilder) Build() Event {
	if b.event.LatencyMs == 0 {
		b.event.LatencyMs = int(time.Since(b.event.Timestamp).Milliseconds())
	}
	return b.event
}

type Sink interface {
	Emit(ctx context.Context, event Event)
}

type LogSink struct{}

func (s *LogSink) Emit(_ context.Context, event Event) {
	// Handle link layer events separately
	if event.EventType != "" {
		attrs := []any{
			"audit", "link",
			"event_type", event.EventType,
			"timestamp", event.Timestamp,
		}
		if event.RequestID != "" {
			attrs = append(attrs, "request_id", event.RequestID)
		}
		if event.FromCredential > 0 {
			attrs = append(attrs, "from_credential", event.FromCredential)
		}
		if event.ToCredential > 0 {
			attrs = append(attrs, "to_credential", event.ToCredential)
		}
		if event.Reason != "" {
			attrs = append(attrs, "reason", event.Reason)
		}
		if event.ChunkCount > 0 {
			attrs = append(attrs, "chunk_count", event.ChunkCount)
		}
		if event.FromState != "" {
			attrs = append(attrs, "from_state", event.FromState)
		}
		if event.ToState != "" {
			attrs = append(attrs, "to_state", event.ToState)
		}
		slog.Info("audit: link event", attrs...)
		return
	}

	// Handle request events
	attrs := []any{
		"audit", "request",
		"request_id", event.RequestID,
		"model", event.ClientModel,
		"outbound", event.OutboundModel,
		"provider", event.ProviderID,
		"credential", event.CredentialID,
		"latency_ms", event.LatencyMs,
		"success", event.Success,
	}
	if event.ErrorKind != "" {
		attrs = append(attrs, "error_kind", event.ErrorKind)
	}
	if event.Stream {
		attrs = append(attrs, "stream_chunks", event.StreamChunkCount, "stream_ttfb_ms", event.StreamTTFBMs)
	}
	if event.PromptTokens > 0 || event.CompletionToken > 0 {
		attrs = append(attrs, "prompt_tokens", event.PromptTokens, "completion_tokens", event.CompletionToken)
	}
	slog.Info("audit: request completed", attrs...)
}

type MultiSink struct {
	sinks []Sink
}

func NewMultiSink(sinks ...Sink) *MultiSink {
	return &MultiSink{sinks: sinks}
}

func (m *MultiSink) Emit(ctx context.Context, event Event) {
	for _, s := range m.sinks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("audit sink panic", "error", r)
				}
			}()
			s.Emit(ctx, event)
		}()
	}
}

type JSONSink struct {
	mu     sync.Mutex
	lastN  []Event
	maxLen int
}

func NewJSONSink(maxLen int) *JSONSink {
	if maxLen <= 0 {
		maxLen = 1000
	}
	return &JSONSink{maxLen: maxLen}
}

func (j *JSONSink) Emit(_ context.Context, event Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lastN = append(j.lastN, event)
	if len(j.lastN) > j.maxLen {
		trimmed := make([]Event, j.maxLen)
		copy(trimmed, j.lastN[len(j.lastN)-j.maxLen:])
		j.lastN = trimmed
	}
}

func (j *JSONSink) Recent(n int) []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	if n > len(j.lastN) {
		n = len(j.lastN)
	}
	out := make([]Event, n)
	copy(out, j.lastN[len(j.lastN)-n:])
	return out
}

func (j *JSONSink) RecentJSON(n int) []byte {
	events := j.Recent(n)
	data, err := json.Marshal(events)
	if err != nil {
		return []byte(`[]`)
	}
	return data
}

func (j *JSONSink) Count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.lastN)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ComputeRequestChecksum(model string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func MessageDigest(messages []byte) (checksum string, preview string) {
	h := sha256.New()
	h.Write(messages)
	checksum = hex.EncodeToString(h.Sum(nil))

	var msgs []map[string]json.RawMessage
	if json.Unmarshal(messages, &msgs) != nil {
		if len(messages) > 200 {
			preview = safeTruncateUTF8(string(messages), 200)
		} else {
			preview = string(messages)
		}
		return
	}

	start := len(msgs) - 4
	if start < 0 {
		start = 0
	}
	var parts []string
	for i := start; i < len(msgs); i++ {
		raw, _ := json.Marshal(msgs[i])
		s := string(raw)
		if len(s) > 200 {
			s = safeTruncateUTF8(s, 200)
		}
		parts = append(parts, s)
	}
	preview = strings.Join(parts, "\n")
	if len(preview) > 4096 {
		preview = safeTruncateUTF8(preview, 4096)
	}
	return
}

// extractDeltaReasoningText returns the reasoning chain text from a streaming
// delta. Reasoning models (DeepSeek-R1, Qwen3-Thinking, etc.) emit their
// chain-of-thought in `delta.reasoning_content` (separate from the final
// answer in `delta.content`). Without this helper, the audit row would
// capture only the final answer, losing the reasoning trace entirely.
func extractDeltaReasoningText(payload string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return ""
	}
	choicesRaw, ok := obj["choices"]
	if !ok {
		return ""
	}
	var choices []map[string]any
	if err := json.Unmarshal(choicesRaw, &choices); err != nil || len(choices) == 0 {
		return ""
	}
	delta, ok := choices[0]["delta"].(map[string]any)
	if !ok {
		return ""
	}
	reasoning, _ := delta["reasoning_content"].(string)
	return reasoning
}

func extractDeltaText(payload string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return ""
	}
	choicesRaw, ok := obj["choices"]
	if !ok {
		return ""
	}
	var choices []map[string]any
	if err := json.Unmarshal(choicesRaw, &choices); err != nil || len(choices) == 0 {
		return ""
	}
	delta, ok := choices[0]["delta"].(map[string]any)
	if !ok {
		return ""
	}
	content, _ := delta["content"].(string)
	return content
}

// extractDeltaToolText returns the concatenated arguments of tool_calls in a
// streaming delta. Function-calling responses (e.g. minimax-m3) emit
// `delta.tool_calls[].function.arguments` across multiple chunks; without this
// the reconstructed response_body would be empty for those requests.
func extractDeltaToolText(payload string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return ""
	}
	choicesRaw, ok := obj["choices"]
	if !ok {
		return ""
	}
	var choices []map[string]any
	if err := json.Unmarshal(choicesRaw, &choices); err != nil || len(choices) == 0 {
		return ""
	}
	delta, ok := choices[0]["delta"].(map[string]any)
	if !ok {
		return ""
	}
	raw, ok := delta["tool_calls"]
	if !ok {
		return ""
	}
	arr, ok := raw.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for i, item := range arr {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		if i == 0 {
			if name, _ := fn["name"].(string); name != "" {
				b.WriteString("[tool:")
				b.WriteString(name)
				b.WriteString("] ")
			}
		}
		if args, _ := fn["arguments"].(string); args != "" {
			b.WriteString(args)
		}
	}
	return b.String()
}
