package v2

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	aggregateMaxAttempts = 3
	aggregateRetryDelay  = 25 * time.Millisecond
)

// sessionUpdater is the minimal surface of SessionAggregator that
// SessionWriterV2 uses. Declared as an interface so unit tests can inject a
// fake (e.g. a recording aggregator) and so the lifecycle code can be tested
// without a live PostgreSQL instance.
type sessionUpdater interface {
	UpdateSession(ctx context.Context, update SessionUpdate) error
}

// SessionWriterV2 is the main coordinator for writing session data to V2 tables
//
// It orchestrates:
//   - TurnWriter: writes turn metadata to session_turns
//   - BodiesWriter: writes incremental message deltas to session_bodies
//   - SessionAggregator: updates session snapshots in sessions table
//
// This is the entry point for shadow writes alongside request_logs.
//
// Lifecycle (spec §6.3):
//
//	The session-snapshot aggregate update runs in a goroutine tracked by
//	aggWg and bound to lifecycleCtx. Callers MUST call Stop before the
//	process exits so the goroutine is awaited (it is no longer a detached
//	fire-and-forget). Stop is idempotent and safe to call from the
//	gateway shutdown goroutine.
type SessionWriterV2 struct {
	turnWriter        *TurnWriter
	bodiesWriter      *SessionBodiesWriter
	sessionAggregator sessionUpdater
	turnLogsWriter    *TurnLogsWriter

	// aggWg tracks the in-flight aggregate snapshot goroutines so Stop can
	// wait for them (spec §6.3). Each Write that reaches the aggregate step
	// does Add(1) before launching the goroutine and Done() when it returns.
	aggWg sync.WaitGroup

	// lifecycleCtx / lifecycleCancel gate the aggregate goroutine. Stop
	// cancels lifecycleCtx so a blocked/slow aggregate returns promptly,
	// then waits on aggWg. New writes after Stop will see a cancelled ctx
	// and skip the aggregate (the primary turn+bodies write still runs).
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleInit   sync.Once
	stopOnce        sync.Once

	// lifecycleMu serializes aggregate registration with Stop. Without this
	// gate, WaitGroup.Add could race with Wait when a write finishes as shutdown
	// begins, which is unsupported and can let work escape the drain.
	lifecycleMu sync.Mutex
	stopped     bool
}

// ensureLifecycle lazily initializes lifecycleCtx / lifecycleCancel for
// SessionWriterV2 instances that were constructed via a struct literal (e.g.
// in tests) instead of NewSessionWriterV2. Idempotent.
func (w *SessionWriterV2) ensureLifecycle() {
	w.lifecycleInit.Do(func() {
		if w.lifecycleCtx == nil {
			ctx, cancel := context.WithCancel(context.Background())
			w.lifecycleCtx = ctx
			w.lifecycleCancel = cancel
		}
	})
}

// NewSessionWriterV2 creates a new SessionWriterV2 instance
func NewSessionWriterV2(tw *TurnWriter, bw *SessionBodiesWriter, sa *SessionAggregator, tlw *TurnLogsWriter) *SessionWriterV2 {
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionWriterV2{
		turnWriter:        tw,
		bodiesWriter:      bw,
		sessionAggregator: sa,
		turnLogsWriter:    tlw,
		lifecycleCtx:      ctx,
		lifecycleCancel:   cancel,
	}
}

// Stop signals shutdown and waits for all in-flight aggregate goroutines to
// finish (or be cancelled). It is idempotent and safe to call multiple times
// and from multiple goroutines.
//
// Per spec §6.3, the gateway shutdown sequence must call this AFTER
// telemetryClient.Stop so the telemetry onPersisted hook (which feeds Write)
// has stopped producing new work before we drain.
func (w *SessionWriterV2) Stop(ctx context.Context) error {
	w.ensureLifecycle()
	w.stopOnce.Do(func() {
		w.lifecycleMu.Lock()
		w.stopped = true
		if w.lifecycleCancel != nil {
			w.lifecycleCancel()
		}
		w.lifecycleMu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		w.aggWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ProcessedRequest represents a request that has been processed through the pipeline
//
// This mirrors the data structure from domains/streaming but is defined here
// to avoid circular dependencies. In production, we'd use a shared interface.
type ProcessedRequest struct {
	// Session context
	SessionID string
	TenantID  string
	RequestID string
	Timestamp time.Time

	// Request content
	RequestBody  []Message // Full request body from client
	ResponseBody []Message // Response from LLM
	Attachments  []AttachmentRef

	// Compression state
	LastOutboundBody    []Message // Previous turn's outbound (for delta extraction)
	OutboundBody        []Message // Actual outbound sent to LLM (after compression)
	CompressionApplied  bool
	CompressionStrategy string
	CompressionMeta     map[string]interface{}
	TokensSaved         int

	// Submit mode detection
	SubmitModeHeader string // X-Gw-Submit-Mode header value from client

	// Governance verdicts
	InjectionVerdict string
	OutputVerdict    string

	// Routing & model
	ClientModel  string
	ProviderID   string
	CredentialID string

	// Usage & cost
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64

	// Performance
	StartedAt   time.Time
	CompletedAt time.Time
	StatusCode  int
	Success     bool
	ErrorKind   string

	// V3.1 dispatch 9-stage (10 timestamps) queue timestamps (migration 513).
	// Mirrors RequestLogEntry.T0ArrivedAt..T9ResponseEndAt from migration 491.
	// Plumbed through to public.session_turns so the session timeline view
	// can answer "how long did stage X take" without joining request_logs.
	// nil-safe — callers (e.g. pipeline_hook.go) that don't have these yet
	// simply leave them nil and the columns persist as NULL.
	T0ArrivedAt       *time.Time
	T1TotalEnqueuedAt *time.Time
	T2TotalDequeuedAt *time.Time
	T3ModelEnqueuedAt *time.Time
	T4ModelDequeuedAt *time.Time
	T5CredEnqueuedAt  *time.Time
	T6CredDequeuedAt  *time.Time
	T7ForwardStartAt  *time.Time
	T8ResponseStartAt *time.Time
	T9ResponseEndAt   *time.Time

	// Protocol-specific extensions (from ir.TransportContext)
	ProviderExtensions map[string]interface{} // Preserves vendor-specific fields

	// Multimodal content tracking
	MultimodalTypes []string // Types present: ["image", "audio", "video", "document"]

	// Processing stages (for turn logs)
	ProcessingStages []ProcessingStage
}

// ProcessingStage represents one stage in the request pipeline
type ProcessingStage struct {
	Stage       string // routing | compression | injection_check | llm_call | output_check | response
	Status      string // pending | running | success | failed | skipped
	StartedAt   time.Time
	CompletedAt time.Time
	EventData   map[string]interface{}
	ErrorMsg    string
}

// Write writes a processed request to all V2 tables
//
// This is a coordinated write that ensures consistency across:
//  1. session_turns (metadata)
//  2. session_bodies (incremental deltas)  — committed atomically with (1)
//  3. session_turn_logs (processing stages) — best-effort, own connection
//  4. sessions (snapshot, async)            — best-effort, lifecycle-managed
//
// Atomicity (spec §6.2): the turn INSERT and the bodies INSERT run inside a
// SINGLE transaction. If either fails the whole tx is rolled back, so a
// bodies failure can never leave an orphan turn row. Stage logs and the
// aggregate snapshot are best-effort (their failures are logged but do not
// fail the primary write), as the spec explicitly allows.
//
// Lifecycle (spec §6.3): the aggregate snapshot update runs in a goroutine
// tracked by aggWg and bound to lifecycleCtx; Stop() awaits it.
func (w *SessionWriterV2) Write(ctx context.Context, req *ProcessedRequest) error {
	// 2026-08-08 audit fix: bound the advisory-lock + body-read critical
	// section. r.Context() in telemetry / node-probe paths can be very long,
	// so a slow DB during GetLatestBodiesInTx would hold the per-session
	// advisory lock for unbounded time, blocking every other concurrent
	// write for that session. A 5s deadline matches the per-credential
	// probe timeout (see bg/node_probe.go) — same order of magnitude, same
	// operational expectation. The outer ctx is preserved for the rest of
	// the turn (detector, marshal, WriteBodiesInTx) which has no shared
	// lock that would cascade.
	lockCtx, cancelLock := context.WithTimeout(ctx, 5*time.Second)
	defer cancelLock()

	// Begin the transaction before reading the previous body. AppendTurnInTx
	// uses the same transaction-scoped advisory lock; acquiring it here makes
	// delta derivation observe the exact state that this turn will follow.
	tx, err := w.turnWriter.BeginTx(lockCtx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(lockCtx)
		}
	}()
	if err := w.turnWriter.LockSessionInTx(lockCtx, tx, req.TenantID, req.SessionID); err != nil {
		return err
	}

	// 1. Detect submit mode using the latest locked body.
	detector := NewSubmitModeDetector()
	var previousAttachments []AttachmentRef
	previousBody, err := w.bodiesWriter.GetLatestBodiesInTx(lockCtx, tx, req.TenantID, req.SessionID)
	if err != nil {
		slog.WarnContext(ctx, "failed to get previous body for submit mode detection",
			"session_id", req.SessionID, "error", err)
	} else if previousBody != nil {
		previousAttachments = previousBody.RequestAttachments
		if len(req.LastOutboundBody) == 0 {
			req.LastOutboundBody = previousBody.OutboundBody
		}
	}

	submitMode := string(detector.Detect(DetectionContext{
		SubmitModeHeader:    req.SubmitModeHeader,
		ClientMessages:      req.RequestBody,
		LastOutboundBody:    req.LastOutboundBody,
		CompressionApplied:  req.CompressionApplied,
		CurrentAttachments:  req.Attachments,
		PreviousAttachments: previousAttachments,
	}))

	// 2. Extract request delta after the locked previous-body read.
	requestDelta := extractRequestDelta(req, submitMode)

	// 3. Build the turn record and bodies record (computed before the insert so
	// marshalling errors fail fast while the lock remains held).

	requestAttachments := extractRequestAttachments(req)
	responseAttachments := extractResponseAttachments(req)
	attachmentCount := len(requestAttachments) + len(responseAttachments)
	attachmentTotalBytes := calculateTotalBytes(requestAttachments, responseAttachments)

	// Auto-populate MultimodalTypes if not provided
	if len(req.MultimodalTypes) == 0 && attachmentCount > 0 {
		allAttachments := append(requestAttachments, responseAttachments...)
		req.MultimodalTypes = ExtractMultimodalTypes(allAttachments)
	}

	turnRec := TurnRecord{
		SessionID:  req.SessionID,
		TenantID:   req.TenantID,
		RequestID:  req.RequestID,
		Ts:         req.Timestamp,
		SubmitMode: submitMode,

		CompressionApplied:  req.CompressionApplied,
		CompressionStrategy: req.CompressionStrategy,
		CompressionMeta:     req.CompressionMeta,
		TokensSaved:         req.TokensSaved,

		InjectionVerdict: req.InjectionVerdict,
		OutputVerdict:    req.OutputVerdict,

		Model:        req.ClientModel,
		Provider:     req.ProviderID,
		CredentialID: req.CredentialID,

		PromptTokens:     req.PromptTokens,
		CompletionTokens: req.CompletionTokens,
		CacheReadTokens:  req.CacheReadTokens,
		CacheWriteTokens: req.CacheWriteTokens,
		CostUSD:          req.CostUSD,

		LatencyMs:  int(req.CompletedAt.Sub(req.StartedAt).Milliseconds()),
		StatusCode: req.StatusCode,
		Success:    req.Success,
		ErrorKind:  req.ErrorKind,

		// V3.2 dual-write (migration 513): copy the 10 dispatch queue
		// timestamps from the ProcessedRequest (populated by the
		// sessionv2mirror hook from telemetry entry) so public.session_turns
		// stays in sync with public.request_logs_hot.
		T0ArrivedAt:       req.T0ArrivedAt,
		T1TotalEnqueuedAt: req.T1TotalEnqueuedAt,
		T2TotalDequeuedAt: req.T2TotalDequeuedAt,
		T3ModelEnqueuedAt: req.T3ModelEnqueuedAt,
		T4ModelDequeuedAt: req.T4ModelDequeuedAt,
		T5CredEnqueuedAt:  req.T5CredEnqueuedAt,
		T6CredDequeuedAt:  req.T6CredDequeuedAt,
		T7ForwardStartAt:  req.T7ForwardStartAt,
		T8ResponseStartAt: req.T8ResponseStartAt,
		T9ResponseEndAt:   req.T9ResponseEndAt,

		SourceKind: "live",
		Quality:    "verified",

		// Attachment metadata
		AttachmentCount:      attachmentCount,
		AttachmentTotalBytes: attachmentTotalBytes,
		MultimodalTypes:      req.MultimodalTypes,

		// Turn-level title / summary (migration 456). Derive deterministic
		// previews from the new messages in this turn so the admin turns-list
		// UI shows something useful without waiting for the async LLM
		// summarizer. summarizeMessages already produces a 200-char cap.
		Title:   summarizeMessages(requestDelta),
		Summary: summarizeMessages(req.ResponseBody),
	}

	// 4. Atomic turn + bodies write (spec §6.2). The transaction and lock were
	// opened before the previous-body read, so AppendTurnInTx reuses them.
	turnNo, err := w.turnWriter.appendTurnInLockedTx(lockCtx, tx, turnRec)
	if err != nil {
		return fmt.Errorf("write turn: %w", err)
	}

	bodiesRec := BodiesRecord{
		SessionID: req.SessionID,
		TurnNo:    turnNo,
		TenantID:  req.TenantID,
		RequestID: req.RequestID,
		Ts:        req.Timestamp,

		RequestDelta:  requestDelta,
		ResponseDelta: req.ResponseBody,
		OutboundBody:  req.OutboundBody,

		RequestAttachments:  requestAttachments,
		ResponseAttachments: responseAttachments,
	}
	if err := w.bodiesWriter.WriteBodiesInTx(lockCtx, tx, bodiesRec); err != nil {
		return fmt.Errorf("write bodies: %w", err)
	}

	if err := tx.Commit(lockCtx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	committed = true

	// 5. Write turn logs (processing stages) — best-effort, NOT in the tx.
	//
	// spec §6.2 allows turn logs to fail without failing the write; keeping
	// them out of the atomic tx means a slow/stale stage log can't hold the
	// turn+bodies transaction open.
	if w.turnLogsWriter != nil && len(req.ProcessingStages) > 0 {
		for _, stage := range req.ProcessingStages {
			err := w.turnLogsWriter.WriteStage(ctx, TurnLogRecord{
				SessionID: req.SessionID,
				TurnNo:    turnNo,
				TenantID:  req.TenantID,
				RequestID: req.RequestID,

				Stage:       stage.Stage,
				StageStatus: stage.Status,
				EventData:   stage.EventData,
				ErrorMsg:    stage.ErrorMsg,

				StartedAt:   stage.StartedAt,
				CompletedAt: stage.CompletedAt,
			})

			if err != nil {
				// Log but don't fail the write (turn logs are optional)
				slog.ErrorContext(ctx, "write turn log failed",
					"session_id", req.SessionID,
					"turn_no", turnNo,
					"stage", stage.Stage,
					"error", err)
			}
		}
	}

	// 6. Update session snapshot — best-effort, lifecycle-managed goroutine
	// (spec §6.3). The goroutine is tracked by aggWg so Stop can await it,
	// and bound to lifecycleCtx so a blocked aggregate is cancelled on
	// shutdown instead of leaking.
	if w.sessionAggregator != nil {
		w.ensureLifecycle()
		w.lifecycleMu.Lock()
		if w.stopped {
			w.lifecycleMu.Unlock()
			return nil
		}
		w.aggWg.Add(1)
		w.lifecycleMu.Unlock()
		go func() {
			defer w.aggWg.Done()
			// lifecycleCtx gates the goroutine on shutdown. A small timeout
			// bounds it so a slow DB can't stall Stop indefinitely even if
			// the ctx isn't yet cancelled.
			aggCtx, cancel := context.WithTimeout(w.lifecycleCtx, 30*time.Second)
			defer cancel()
			update := SessionUpdate{
				SessionID: req.SessionID,
				TenantID:  req.TenantID,
				// 2026-07-28 request-flow Step 3 (spec §6.2): pass RequestID so
				// the aggregator can claim this turn exactly once and never
				// double-accumulate token/turn/cost on a replay.
				RequestID:           req.RequestID,
				LastTurnNo:          turnNo,
				LastRequestSummary:  summarizeMessages(requestDelta),
				LastResponseSummary: summarizeMessages(req.ResponseBody),
				LastModel:           req.ClientModel,
				LastProvider:        req.ProviderID,
				TurnIncrement:       1,
				TokensIncrement:     req.PromptTokens + req.CompletionTokens,
				CostIncrement:       req.CostUSD,
				UpdatedAt:           req.Timestamp,
			}
			if err := w.updateSessionAggregate(aggCtx, update); err != nil {
				slog.Error("update session snapshot failed after retries",
					"session_id", req.SessionID,
					"request_id", req.RequestID,
					"error", err)
			}
		}()
	}

	return nil
}

func (w *SessionWriterV2) updateSessionAggregate(ctx context.Context, update SessionUpdate) error {
	var lastErr error
	for attempt := 1; attempt <= aggregateMaxAttempts; attempt++ {
		if err := w.sessionAggregator.UpdateSession(ctx, update); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == aggregateMaxAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * aggregateRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// getPreviousBody retrieves the most recent persisted turn body.
//
// The caller uses both its outbound snapshot (for multi-turn delta detection)
// and attachment references (for attachment-only submit-mode detection).
func (w *SessionWriterV2) getPreviousBody(ctx context.Context, sessionID, tenantID string) (*BodiesRecord, error) {
	body, err := w.bodiesWriter.GetLatestBodies(ctx, tenantID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get latest bodies: %w", err)
	}
	return body, nil
}

// extractRequestDelta extracts incremental messages that are new in this turn
//
// Logic:
//   - If submit_mode is "delta" or "snapshot": entire RequestBody is the delta
//   - If submit_mode is "full": extract messages not present in LastOutboundBody
//   - If LastOutboundBody is empty: entire RequestBody is the delta (first turn)
func extractRequestDelta(req *ProcessedRequest, submitMode string) []Message {
	// Attachment-only turns carry no new message body; persist an empty delta
	// while retaining attachment metadata on the turn row.
	if submitMode == "attachment_only" {
		return nil
	}

	// If client explicitly sent delta or snapshot, trust it
	if submitMode == "delta" || submitMode == "snapshot" || submitMode == "inferred_compressed" {
		return req.RequestBody
	}

	// If no previous outbound, everything is new
	if len(req.LastOutboundBody) == 0 {
		return req.RequestBody
	}

	// Extract delta: messages in RequestBody but not in LastOutboundBody
	var delta []Message
	lastOutboundSet := buildMessageSet(req.LastOutboundBody)

	for _, msg := range req.RequestBody {
		msgKey := messageKey(msg)
		if !lastOutboundSet[msgKey] {
			delta = append(delta, msg)
		}
	}

	// Fallback: if delta extraction found nothing, return full body
	// (This can happen if client sent identical history but we don't detect it)
	if len(delta) == 0 {
		return req.RequestBody
	}

	return delta
}

// buildMessageSet creates a set of message keys for fast lookup
func buildMessageSet(messages []Message) map[string]bool {
	set := make(map[string]bool)
	for _, msg := range messages {
		set[messageKey(msg)] = true
	}
	return set
}

// messageKey generates a unique key for a message (for delta extraction /
// deduplication).
//
// It mirrors the V1 compression fingerprint domains/hooks/compression/diff.go
// msgHash: sha256(role + \x00 + first-512-bytes-content + \x00 + toolCallID),
// truncated to 16 bytes (32 hex). Keeping the two schemes aligned is a hard
// prerequisite for unifying the V1 (LCS) and V2 (delta) read paths — see
// docs/omni-ref3/03-MULTITURN-ASSEMBLY.md A2.
//
// The previous implementation used "role:first-100-chars", which (a) collided
// on any two tool results sharing a 100-char prefix despite different
// tool_call_ids — exactly the mis-pairing SanitizeToolMessages exists to
// prevent — and (b) used a plain ":" separator that the content itself could
// contain. Both are fixed here.
func messageKey(msg Message) string {
	contentKey := msg.Content
	if len(contentKey) > 512 {
		contentKey = contentKey[:512]
	}
	if structured := structuredMessagePayload(msg); structured != "" {
		contentKey += "\x00structured:" + structured
	}
	// NUL bytes separate the fields so no legal content can spoof a different
	// (role, content, toolCallID) triple, matching the V1 scheme.
	h := sha256.Sum256([]byte(msg.Role + "\x00" + contentKey + "\x00" + msg.ToolCallID))
	return fmt.Sprintf("%x", h[:16])
}

func structuredMessagePayload(msg Message) string {
	payload := struct {
		ContentRaw json.RawMessage          `json:"content,omitempty"`
		RawContent any                      `json:"raw,omitempty"`
		ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
		Name       string                   `json:"name,omitempty"`
	}{ContentRaw: msg.ContentRaw, RawContent: msg.RawContent, ToolCalls: msg.ToolCalls, Name: msg.Name}
	if len(payload.ContentRaw) == 0 && payload.RawContent == nil && len(payload.ToolCalls) == 0 && payload.Name == "" {
		return ""
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}

func extractRequestAttachments(req *ProcessedRequest) []AttachmentRef {
	// Return the attachments that were already extracted and stored
	// In the full pipeline, this comes from the attachment extraction layer
	return req.Attachments
}

// extractResponseAttachments extracts attachment references from response
func extractResponseAttachments(req *ProcessedRequest) []AttachmentRef {
	// For now, responses rarely contain attachments (future: audio/image outputs)
	// This would be populated by the response parser if the LLM returns media
	// TODO: Parse response_body for attachment references when models support output media
	return []AttachmentRef{}
}

// calculateTotalBytes sums up the total bytes from all attachments
func calculateTotalBytes(requestAttachments, responseAttachments []AttachmentRef) int64 {
	var total int64
	for _, att := range requestAttachments {
		total += att.SizeBytes
	}
	for _, att := range responseAttachments {
		total += att.SizeBytes
	}
	return total
}

// summarizeMessages creates a brief summary of messages for session snapshot
func summarizeMessages(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}

	// Take first message content, truncate to 200 runes (not bytes) to avoid
	// cutting UTF-8 sequences in the middle. PostgreSQL text columns enforce
	// valid UTF-8, so byte-slicing [:200] would panic on insert if the cut
	// lands inside a multi-byte character.
	firstMsg := messages[0]
	content := firstMsg.Content
	runes := []rune(content)
	if len(runes) > 200 {
		content = string(runes[:200]) + "..."
	}

	if len(messages) > 1 {
		return fmt.Sprintf("%s (%d messages)", content, len(messages))
	}

	return content
}
