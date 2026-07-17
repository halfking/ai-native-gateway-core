package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// DualWriter writes to both V1 (request_logs) and V2 (sessions) in parallel
//
// This is the bridge that enables zero-downtime migration:
//   - V1 write is PRIMARY: failure means request fails
//   - V2 write is SHADOW: failure is logged but doesn't block request
//   - Feature flags control which systems are active
//
// Usage:
//
//	writer := NewDualWriter(v1Writer, v2Writer, flags, metrics)
//	err := writer.Write(ctx, req)
type DualWriter struct {
	v1Writer RequestLogWriter      // Legacy writer (request_logs)
	v2Writer SessionWriterV2Interface // V2 writer (sessions tables)

	flags   FeatureFlags
	metrics *DualWriteMetrics
}

// SessionWriterV2Interface defines the interface for V2 writer
type SessionWriterV2Interface interface {
	Write(ctx context.Context, req *v2.ProcessedRequest) error
}

// RequestLogWriter is the interface for legacy request_logs writer
type RequestLogWriter interface {
	Write(ctx context.Context, req *ProcessedRequest) error
}

// FeatureFlags controls which systems are active
type FeatureFlags interface {
	IsEnabled(flag string) bool
	IsEnabledForSession(sessionID, flag string) bool
	GetRolloutPercent(flag string) int
}

// DualWriteMetrics tracks dual write performance
type DualWriteMetrics struct {
	V1WriteSuccess Counter
	V1WriteFailed  Counter
	V2WriteSuccess Counter
	V2WriteFailed  Counter
	V2WriteLatency Histogram
	ValidationDiff Counter
}

// Counter is a simple counter interface (Prometheus compatible)
type Counter interface {
	Inc()
	Add(delta float64)
}

// Histogram is a simple histogram interface (Prometheus compatible)
type Histogram interface {
	Observe(value float64)
}

// NewDualWriter creates a new dual writer
func NewDualWriter(
	v1 RequestLogWriter,
	v2 SessionWriterV2Interface,
	flags FeatureFlags,
	metrics *DualWriteMetrics,
) *DualWriter {
	return &DualWriter{
		v1Writer: v1,
		v2Writer: v2,
		flags:    flags,
		metrics:  metrics,
	}
}

// ProcessedRequest represents a request that has been processed
//
// This is shared between V1 and V2 writers. In production, this would
// be defined in a shared package to avoid circular dependencies.
type ProcessedRequest struct {
	// Session context
	SessionID string
	TenantID  string
	RequestID string
	Timestamp time.Time

	// Request content
	RequestBody  []v2.Message
	ResponseBody []v2.Message
	Attachments  []v2.AttachmentRef

	// Compression state
	LastOutboundBody    []v2.Message
	OutboundBody        []v2.Message
	CompressionApplied  bool
	CompressionStrategy string
	CompressionMeta     map[string]interface{}
	TokensSaved         int

	// Governance
	InjectionVerdict string
	OutputVerdict    string

	// Routing
	ClientModel  string
	ProviderID   string
	CredentialID string

	// Usage
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

	// Processing stages
	ProcessingStages []v2.ProcessingStage
}

// Write writes to both V1 and V2 systems
//
// Strategy:
//  1. ALWAYS write to V1 (primary) - failure blocks request
//  2. IF v2_shadow_write enabled: write to V2 (shadow) - failure is logged
//  3. IF v2_dual_read enabled: validate consistency
//
// Guarantees:
//   - V1 write failure → request fails (backward compatible)
//   - V2 write failure → logged but doesn't fail request (safe rollout)
func (w *DualWriter) Write(ctx context.Context, req *ProcessedRequest) error {
	// 1. PRIMARY WRITE: V1 (request_logs)
	// This MUST succeed or the entire request fails
	startV1 := time.Now()
	err := w.v1Writer.Write(ctx, req)
	if err != nil {
		w.metrics.V1WriteFailed.Inc()
		return fmt.Errorf("v1 write failed (primary): %w", err)
	}
	w.metrics.V1WriteSuccess.Inc()

	slog.DebugContext(ctx, "dual writer v1 success",
		"request_id", req.RequestID,
		"duration_ms", time.Since(startV1).Milliseconds())

	// 2. SHADOW WRITE: V2 (sessions tables)
	// Only if feature flag is enabled
	if !w.flags.IsEnabled("sessions_v2_shadow_write") {
		return nil // V2 disabled, done
	}

	// Check rollout percentage (gradual rollout)
	rolloutPercent := w.flags.GetRolloutPercent("sessions_v2_rollout_percent")
	if !w.shouldWriteV2(req.SessionID, rolloutPercent) {
		slog.DebugContext(ctx, "dual writer v2 skipped (rollout)",
			"session_id", req.SessionID,
			"rollout_percent", rolloutPercent)
		return nil
	}

	// Write to V2 (shadow)
	startV2 := time.Now()
	v2Req := convertToV2Request(req)

	err = w.v2Writer.Write(ctx, v2Req)
	v2Latency := time.Since(startV2)

	w.metrics.V2WriteLatency.Observe(v2Latency.Seconds())

	if err != nil {
		// V2 write failed - LOG but DON'T fail the request
		w.metrics.V2WriteFailed.Inc()
		slog.ErrorContext(ctx, "dual writer v2 failed (shadow)",
			"request_id", req.RequestID,
			"session_id", req.SessionID,
			"error", err,
			"duration_ms", v2Latency.Milliseconds())

		// Return nil - shadow write failure doesn't block request
		return nil
	}

	w.metrics.V2WriteSuccess.Inc()
	slog.DebugContext(ctx, "dual writer v2 success",
		"request_id", req.RequestID,
		"duration_ms", v2Latency.Milliseconds())

	return nil
}

// shouldWriteV2 determines if this session should be written to V2
//
// Uses consistent hashing on session_id to ensure the same session
// always gets the same result (important for data consistency).
func (w *DualWriter) shouldWriteV2(sessionID string, rolloutPercent int) bool {
	if rolloutPercent == 0 {
		return false
	}
	if rolloutPercent >= 100 {
		return true
	}

	// Consistent hash: same session always gets same result
	hash := hashString(sessionID)
	return (hash % 100) < rolloutPercent
}

// hashString generates a consistent hash from a string
func hashString(s string) int {
	hash := 0
	for i := 0; i < len(s); i++ {
		hash = (hash*31 + int(s[i])) & 0x7FFFFFFF
	}
	return hash
}

// convertToV2Request converts ProcessedRequest to V2 format
func convertToV2Request(req *ProcessedRequest) *v2.ProcessedRequest {
	return &v2.ProcessedRequest{
		SessionID:           req.SessionID,
		TenantID:            req.TenantID,
		RequestID:           req.RequestID,
		Timestamp:           req.Timestamp,
		RequestBody:         req.RequestBody,
		ResponseBody:        req.ResponseBody,
		Attachments:         req.Attachments,
		LastOutboundBody:    req.LastOutboundBody,
		OutboundBody:        req.OutboundBody,
		CompressionApplied:  req.CompressionApplied,
		CompressionStrategy: req.CompressionStrategy,
		CompressionMeta:     req.CompressionMeta,
		TokensSaved:         req.TokensSaved,
		InjectionVerdict:    req.InjectionVerdict,
		OutputVerdict:       req.OutputVerdict,
		ClientModel:         req.ClientModel,
		ProviderID:          req.ProviderID,
		CredentialID:        req.CredentialID,
		PromptTokens:        req.PromptTokens,
		CompletionTokens:    req.CompletionTokens,
		CacheReadTokens:     req.CacheReadTokens,
		CacheWriteTokens:    req.CacheWriteTokens,
		CostUSD:             req.CostUSD,
		StartedAt:           req.StartedAt,
		CompletedAt:         req.CompletedAt,
		StatusCode:          req.StatusCode,
		Success:             req.Success,
		ErrorKind:           req.ErrorKind,
		ProcessingStages:    req.ProcessingStages,
	}
}

// GetV1Writer returns the V1 writer (for testing/debugging)
func (w *DualWriter) GetV1Writer() RequestLogWriter {
	return w.v1Writer
}

// GetV2Writer returns the V2 writer (for testing/debugging)
func (w *DualWriter) GetV2Writer() SessionWriterV2Interface {
	return w.v2Writer
}

// Stats returns current dual write statistics
func (w *DualWriter) Stats() DualWriteStats {
	return DualWriteStats{
		V2Enabled:      w.flags.IsEnabled("sessions_v2_shadow_write"),
		RolloutPercent: w.flags.GetRolloutPercent("sessions_v2_rollout_percent"),
		// TODO: Add counters from metrics
	}
}

// DualWriteStats contains dual write statistics
type DualWriteStats struct {
	V2Enabled      bool
	RolloutPercent int
	V1WriteCount   int64
	V2WriteCount   int64
	V2FailCount    int64
}
