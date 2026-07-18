package v2

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// SessionWriterV2 is the main coordinator for writing session data to V2 tables
//
// It orchestrates:
//   - TurnWriter: writes turn metadata to session_turns
//   - BodiesWriter: writes incremental message deltas to session_bodies
//   - SessionAggregator: updates session snapshots in sessions table
//
// This is the entry point for shadow writes alongside request_logs.
type SessionWriterV2 struct {
	turnWriter        *TurnWriter
	bodiesWriter      *SessionBodiesWriter
	sessionAggregator *SessionAggregator
	turnLogsWriter    *TurnLogsWriter
}

// NewSessionWriterV2 creates a new SessionWriterV2 instance
func NewSessionWriterV2(tw *TurnWriter, bw *SessionBodiesWriter, sa *SessionAggregator, tlw *TurnLogsWriter) *SessionWriterV2 {
	return &SessionWriterV2{
		turnWriter:        tw,
		bodiesWriter:      bw,
		sessionAggregator: sa,
		turnLogsWriter:    tlw,
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
	LastOutboundBody    []Message              // Previous turn's outbound (for delta extraction)
	OutboundBody        []Message              // Actual outbound sent to LLM (after compression)
	CompressionApplied  bool
	CompressionStrategy string
	CompressionMeta     map[string]interface{}
	TokensSaved         int

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

	// Protocol-specific extensions (from ir.TransportContext)
	ProviderExtensions map[string]interface{} // Preserves vendor-specific fields

	// Multimodal content tracking
	MultimodalTypes []string // Types present: ["image", "audio", "video", "document"]

	// Processing stages (for turn logs)
	ProcessingStages []ProcessingStage
}

// ProcessingStage represents one stage in the request pipeline
type ProcessingStage struct {
	Stage      string                 // routing | compression | injection_check | llm_call | output_check | response
	Status     string                 // pending | running | success | failed | skipped
	StartedAt  time.Time
	CompletedAt time.Time
	EventData  map[string]interface{}
	ErrorMsg   string
}

// Write writes a processed request to all V2 tables
//
// This is a coordinated write that ensures consistency across:
//   1. session_turns (metadata)
//   2. session_bodies (incremental deltas)
//   3. session_turn_logs (processing stages)
//   4. sessions (snapshot, async)
//
// Error handling: If turn or bodies write fails, we return error.
// Session aggregation is async, so its failures are logged but don't fail the write.
func (w *SessionWriterV2) Write(ctx context.Context, req *ProcessedRequest) error {
	// 1. Detect submit mode
	submitMode := detectSubmitMode(req)

	// 2. Extract request delta (incremental messages)
	requestDelta := extractRequestDelta(req, submitMode)

	// 3. Write turn metadata
	requestAttachments := extractRequestAttachments(req)
	responseAttachments := extractResponseAttachments(req)
	attachmentCount := len(requestAttachments) + len(responseAttachments)
	attachmentTotalBytes := calculateTotalBytes(requestAttachments, responseAttachments)
	
	turnNo, err := w.turnWriter.AppendTurn(ctx, TurnRecord{
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

		SourceKind: "live",
		Quality:    "verified",
		
		// Attachment metadata
		AttachmentCount:      attachmentCount,
		AttachmentTotalBytes: attachmentTotalBytes,
		MultimodalTypes:      req.MultimodalTypes,
	})

	if err != nil {
		return fmt.Errorf("write turn: %w", err)
	}

	// 4. Write bodies (incremental deltas)
	err = w.bodiesWriter.WriteBodies(ctx, BodiesRecord{
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
	})

	if err != nil {
		return fmt.Errorf("write bodies: %w", err)
	}

	// 5. Write turn logs (processing stages)
	if len(req.ProcessingStages) > 0 {
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

	// 6. Update session snapshot (async)
	go func() {
		ctx := context.Background() // Detached context
		err := w.sessionAggregator.UpdateSession(ctx, SessionUpdate{
			SessionID:        req.SessionID,
			TenantID:         req.TenantID,
			LastTurnNo:       turnNo,
			LastRequestSummary: summarizeMessages(requestDelta),
			LastResponseSummary: summarizeMessages(req.ResponseBody),
			LastModel:        req.ClientModel,
			LastProvider:     req.ProviderID,
			TurnIncrement:    1,
			TokensIncrement:  req.PromptTokens + req.CompletionTokens,
			CostIncrement:    req.CostUSD,
			UpdatedAt:        req.Timestamp,
		})

		if err != nil {
			slog.Error("update session snapshot failed",
				"session_id", req.SessionID,
				"error", err)
		}
	}()

	return nil
}

// detectSubmitMode detects how the client submitted the request
//
// Priority order (high to low):
//   P0: X-Gw-Submit-Mode header
//   P1: Message count regression
//   P2: Summary marker detection
//   P3: Orphaned tool_result
//   P4: Normal full submission
func detectSubmitMode(req *ProcessedRequest) string {
	// For now, default to "full" mode
	// Full detection logic will be implemented in submit_mode_detector.go
	
	// Quick heuristic: if compression was applied and client sent fewer messages
	// than last outbound, likely client-side compression
	if req.CompressionApplied && len(req.LastOutboundBody) > 0 {
		if len(req.RequestBody) < len(req.LastOutboundBody) {
			return "inferred_compressed"
		}
	}

	return "full"
}

// extractRequestDelta extracts incremental messages that are new in this turn
//
// Logic:
//   - If submit_mode is "delta" or "snapshot": entire RequestBody is the delta
//   - If submit_mode is "full": extract messages not present in LastOutboundBody
//   - If LastOutboundBody is empty: entire RequestBody is the delta (first turn)
func extractRequestDelta(req *ProcessedRequest, submitMode string) []Message {
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

// messageKey generates a unique key for a message (for deduplication)
func messageKey(msg Message) string {
	// Simple key: role + first 100 chars of content
	content := msg.Content
	if len(content) > 100 {
		content = content[:100]
	}
	return fmt.Sprintf("%s:%s", msg.Role, content)
}

// extractRequestAttachments extracts attachment references from request
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

	// Take first message content, truncate to 200 chars
	firstMsg := messages[0]
	content := firstMsg.Content
	if len(content) > 200 {
		content = content[:200] + "..."
	}

	if len(messages) > 1 {
		return fmt.Sprintf("%s (%d messages)", content, len(messages))
	}

	return content
}
