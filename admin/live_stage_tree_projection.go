// Package admin — stage tree projection for live stream dashboard.
//
// This module transforms RouteTrace data into StageTreeFrame format suitable
// for frontend visualization. It maps the routing decision chain to the
// complete request pipeline stages (auth → approval → injection → sanitize →
// compress → route → provider → first_byte → restore → billing → reply).
//
// Design principles:
//  1. Separation of concerns: RouteTrace captures routing decisions;
//     StageTreeFrame captures the full pipeline visualization
//  2. Progressive enhancement: stages are marked as they complete
//  3. Wire-safe: all sensitive data remains filtered
//  4. Idempotent: multiple projections of the same trace yield identical frames
//
// 2026-11-16: Initial implementation based on design prototype requirements.
package admin

import (
	"time"
)

// StageStatus represents the execution state of a pipeline stage.
type StageStatus string

const (
	StageStatusPending   StageStatus = "pending"   // Not yet reached
	StageStatusActive    StageStatus = "active"    // Currently executing
	StageStatusDone      StageStatus = "done"      // Completed successfully
	StageStatusFailed    StageStatus = "failed"    // Failed with error
	StageStatusSkipped   StageStatus = "skipped"   // Intentionally bypassed
	StageStatusWarning   StageStatus = "warning"   // Completed with warnings
	StageStatusUnknown   StageStatus = "unknown"   // Status not measured
)

// StageKey identifies a specific pipeline stage.
type StageKey string

const (
	// Send rail stages (left to right)
	StageKeyAuth      StageKey = "auth"       // Authentication & rate limiting
	StageKeyApproval  StageKey = "approval"   // Human approval gate
	StageKeyInjection StageKey = "injection"  // Prompt injection detection
	StageKeySanitize  StageKey = "sanitize"   // Data sanitization/masking
	StageKeyCompress  StageKey = "compress"   // Session compression
	StageKeyRoute     StageKey = "route"      // Credential routing
	StageKeyProvider  StageKey = "provider"   // Upstream provider/pool
	
	// Receive rail stages (right to left)
	StageKeyFirstByte StageKey = "first_byte" // Time to first token
	StageKeyRestore   StageKey = "restore"    // Data restoration
	StageKeyBilling   StageKey = "billing"    // Token metering
	StageKeyReply     StageKey = "reply"      // Response delivery
)

// StageNode represents one stage in the pipeline visualization.
type StageNode struct {
	Key        StageKey    `json:"key"`
	Name       string      `json:"name"`              // Display name (localized)
	Status     StageStatus `json:"status"`
	StartTime  *time.Time  `json:"start_time,omitempty"`
	EndTime    *time.Time  `json:"end_time,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
	
	// Stage-specific metadata
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	
	// Error details (when status = failed/warning)
	ErrorKind    string `json:"error_kind,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	ErrorSource  string `json:"error_source,omitempty"` // "gateway" | "provider" | "other"
}

// StageTreeFrame is the complete visualization frame for a request.
// It contains all pipeline stages plus routing metadata.
type StageTreeFrame struct {
	RequestID      string      `json:"request_id"`
	TenantID       int         `json:"tenant_id"`
	
	// Client info
	ClientName     string      `json:"client_name,omitempty"`
	ClientType     string      `json:"client_type,omitempty"`
	
	// Model resolution
	ClientModel    string      `json:"client_model"`
	CanonicalModel string      `json:"canonical_model,omitempty"`
	IsAutoModel    bool        `json:"is_auto_model,omitempty"`
	
	// Current state
	Phase          RoutePhase  `json:"phase"`
	OverallStatus  StageStatus `json:"overall_status"`
	
	// Pipeline stages
	SendStages     []StageNode `json:"send_stages"`    // Auth → Provider
	RecvStages     []StageNode `json:"recv_stages"`    // FirstByte → Reply
	
	// Routing attempts
	Attempts       []RouteAttempt `json:"attempts,omitempty"`
	CurrentAttempt int            `json:"current_attempt"`
	
	// Aggregated metrics
	TotalLatencyMs int64       `json:"total_latency_ms,omitempty"`
	TTFT           int64       `json:"ttft_ms,omitempty"`
	TokensIn       int         `json:"tokens_in,omitempty"`
	TokensOut      int         `json:"tokens_out,omitempty"`
	CacheRead      int         `json:"cache_read,omitempty"`
	
	// Session context
	SessionID      string      `json:"session_id,omitempty"`
	SessionTurn    int         `json:"session_turn,omitempty"`
	IsFirstTurn    bool        `json:"is_first_turn,omitempty"`
	
	// Timestamps
	StartTime      time.Time   `json:"start_time"`
	UpdatedAt      time.Time   `json:"updated_at"`
	Version        int         `json:"version"`
}

// ProjectStageTree converts a RouteTrace to a StageTreeFrame.
// This is the main projection function used by the SSE hub.
func ProjectStageTree(trace *RouteTrace) *StageTreeFrame {
	if trace == nil {
		return nil
	}
	
	snapshot := trace.Snapshot()
	
	frame := &StageTreeFrame{
		RequestID:      snapshot.RequestID,
		TenantID:       snapshot.TenantID,
		ClientModel:    snapshot.ClientModel,
		CanonicalModel: snapshot.CanonicalModel,
		Phase:          snapshot.Phase,
		Attempts:       snapshot.Attempts,
		CurrentAttempt: snapshot.CurrentAttempt,
		TotalLatencyMs: snapshot.LatencyMs,
		TTFT:           snapshot.TTFT,
		StartTime:      snapshot.StartTime,
		UpdatedAt:      snapshot.UpdatedAt,
		Version:        snapshot.Version,
	}
	
	// Determine overall status from phase
	frame.OverallStatus = phaseToOverallStatus(snapshot.Phase, snapshot.Status)
	
	// Project send rail stages
	frame.SendStages = projectSendStages(&snapshot)
	
	// Project receive rail stages
	frame.RecvStages = projectRecvStages(&snapshot)
	
	// Extract token metrics from attempts
	if len(snapshot.Attempts) > 0 {
		lastAttempt := snapshot.Attempts[len(snapshot.Attempts)-1]
		frame.TokensIn = lastAttempt.PromptTokens
		frame.TokensOut = lastAttempt.CompletionTokens
	}
	
	return frame
}

// phaseToOverallStatus maps routing phase to overall stage tree status.
func phaseToOverallStatus(phase RoutePhase, status string) StageStatus {
	switch phase {
	case RoutePhaseDone:
		return StageStatusDone
	case RoutePhaseFailed:
		return StageStatusFailed
	case RoutePhaseStarted, RoutePhaseSelecting, RoutePhaseAttempting, RoutePhaseResponding:
		return StageStatusActive
	case RoutePhaseFallback:
		return StageStatusWarning
	default:
		return StageStatusUnknown
	}
}

// projectSendStages creates the send rail stage nodes.
func projectSendStages(trace *RouteTrace) []StageNode {
	stages := []StageNode{
		{Key: StageKeyAuth, Name: "鉴权/限流", Status: StageStatusDone},
		{Key: StageKeyApproval, Name: "审批", Status: StageStatusSkipped},
		{Key: StageKeyInjection, Name: "注入检测", Status: StageStatusSkipped},
		{Key: StageKeySanitize, Name: "脱敏", Status: StageStatusSkipped},
		{Key: StageKeyCompress, Name: "压缩", Status: StageStatusSkipped},
		{Key: StageKeyRoute, Name: "路由", Status: StageStatusDone},
		{Key: StageKeyProvider, Name: "供应商/令牌池", Status: StageStatusPending},
	}
	
	// Update stages based on routing phase
	switch trace.Phase {
	case RoutePhaseStarted:
		stages[0].Status = StageStatusActive // Auth active
		for i := 1; i < len(stages); i++ {
			stages[i].Status = StageStatusPending
		}
		
	case RoutePhaseSelecting:
		// Auth → Route completed, selecting provider
		stages[5].Status = StageStatusActive // Route active
		stages[6].Status = StageStatusPending
		
	case RoutePhaseAttempting, RoutePhaseResponding, RoutePhaseFallback:
		// Provider stage active
		stages[6].Status = StageStatusActive
		if trace.Provider != "" {
			stages[6].Metadata = map[string]interface{}{
				"provider":         trace.Provider,
				"credential_id":    trace.CredentialID,
				"credential_label": trace.CredentialLabel,
			}
		}
		
	case RoutePhaseDone:
		// All send stages completed
		stages[6].Status = StageStatusDone
		if trace.Provider != "" {
			stages[6].Metadata = map[string]interface{}{
				"provider":         trace.Provider,
				"credential_id":    trace.CredentialID,
				"credential_label": trace.CredentialLabel,
			}
		}
		
	case RoutePhaseFailed:
		// Mark failed stage
		if trace.ErrorKind != "" {
			// Determine which stage failed based on error kind
			failedStage := determineFailedStage(trace.ErrorKind)
			for i := range stages {
				if stages[i].Key == failedStage {
					stages[i].Status = StageStatusFailed
					stages[i].ErrorKind = trace.ErrorKind
					stages[i].ErrorMessage = trace.Error
					stages[i].ErrorSource = classifyErrorSource(trace.ErrorKind)
				}
			}
		}
	}
	
	return stages
}

// projectRecvStages creates the receive rail stage nodes.
func projectRecvStages(trace *RouteTrace) []StageNode {
	stages := []StageNode{
		{Key: StageKeyFirstByte, Name: "首字节", Status: StageStatusPending},
		{Key: StageKeyRestore, Name: "还原", Status: StageStatusPending},
		{Key: StageKeyBilling, Name: "计量", Status: StageStatusPending},
		{Key: StageKeyReply, Name: "回复", Status: StageStatusPending},
	}
	
	// Update stages based on routing phase
	switch trace.Phase {
	case RoutePhaseResponding:
		// First byte received
		stages[0].Status = StageStatusDone
		if trace.TTFT > 0 {
			stages[0].DurationMs = trace.TTFT
		}
		stages[1].Status = StageStatusSkipped
		stages[2].Status = StageStatusActive
		
	case RoutePhaseDone:
		// All receive stages completed
		stages[0].Status = StageStatusDone
		if trace.TTFT > 0 {
			stages[0].DurationMs = trace.TTFT
		}
		stages[1].Status = StageStatusSkipped
		stages[2].Status = StageStatusDone
		stages[3].Status = StageStatusDone
		
	case RoutePhaseFailed:
		// Mark as skipped if failed before receiving
		for i := range stages {
			stages[i].Status = StageStatusSkipped
		}
	}
	
	return stages
}

// determineFailedStage maps error kind to the stage that failed.
func determineFailedStage(errorKind string) StageKey {
	switch errorKind {
	case "auth_rejected", "rate_limited":
		return StageKeyAuth
	case "approval_required":
		return StageKeyApproval
	case "prompt_injection_blocked":
		return StageKeyInjection
	case "sanitize_error":
		return StageKeySanitize
	case "compression_guard":
		return StageKeyCompress
	case "no_route", "circuit_open", "model_not_routable":
		return StageKeyRoute
	case "upstream_rate_limited", "upstream_5xx", "upstream_timeout", 
	     "connect_failed", "upstream_model_missing":
		return StageKeyProvider
	default:
		return StageKeyProvider // Default to provider stage
	}
}

// classifyErrorSource determines error source (gateway/provider/other).
func classifyErrorSource(errorKind string) string {
	gatewayErrors := map[string]bool{
		"auth_rejected": true, "approval_required": true,
		"prompt_injection_blocked": true, "sanitize_error": true,
		"compression_guard": true, "no_route": true,
		"gateway_concurrency_limit": true, "circuit_open": true,
		"model_not_routable": true,
	}
	
	providerErrors := map[string]bool{
		"upstream_rate_limited": true, "upstream_5xx": true,
		"upstream_timeout": true, "connect_failed": true,
		"upstream_model_missing": true,
	}
	
	if gatewayErrors[errorKind] {
		return "gateway"
	}
	if providerErrors[errorKind] {
		return "provider"
	}
	return "other"
}
