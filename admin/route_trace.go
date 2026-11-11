// Package admin — route tracing for dynamic routing visualization.
//
// This module provides runtime route decision tracking for the live stream
// dashboard. Inspired by magpie's internal/gateway/trace.go but adapted to
// llm-gateway-go's architecture and integrated with the existing live stream
// SSE infrastructure.
//
// Design principles:
//  1. Non-blocking: route tracing must not impact request hot path latency
//  2. Safe: no sensitive data (credentials, internal IDs) in the wire format
//  3. Incremental: events are emitted as routing progresses, not just at completion
//  4. Integrated: reuses existing LiveStreamSSEHub and tenant isolation
//
// 2026-11-11: Initial implementation for dynamic routing visualization feature.
package admin

import (
	"sync"
	"time"
)

// RoutePhase represents the current stage of request routing.
// Phases progress in order: started → selecting → attempting → responding → done/failed.
// The "fallback" phase indicates a retry after a failed attempt.
type RoutePhase string

const (
	RoutePhaseStarted    RoutePhase = "started"    // Request received, routing begins
	RoutePhaseSelecting  RoutePhase = "selecting"  // Choosing credential/provider
	RoutePhaseAttempting RoutePhase = "attempting" // Sending request to upstream
	RoutePhaseResponding RoutePhase = "responding" // Receiving response content
	RoutePhaseFallback   RoutePhase = "fallback"   // Retrying with next credential
	RoutePhaseDone       RoutePhase = "done"       // Request completed successfully
	RoutePhaseFailed     RoutePhase = "failed"     // All attempts exhausted
)

// RouteTrace captures the complete routing decision chain for a single request.
// It is incrementally updated as routing progresses and pushed to connected
// dashboard clients via SSE.
//
// Wire-safe: all fields are sanitized and contain no secrets, internal IDs
// (other than public request_id), or cross-tenant data.
type RouteTrace struct {
	mu sync.RWMutex // protects all fields during incremental updates

	RequestID      string         `json:"request_id"`
	TenantID       int            `json:"tenant_id"`      // for tenant isolation in hub
	StartTime      time.Time      `json:"start_time"`
	Phase          RoutePhase     `json:"phase"`
	
	// Model selection
	ClientModel    string         `json:"client_model"`   // model requested by client
	CanonicalModel string         `json:"canonical_model"` // resolved canonical model name
	CanonicalID    int            `json:"canonical_id,omitempty"`
	
	// Current routing target
	Provider       string         `json:"provider"`       // provider code (e.g., "openai")
	CredentialID   int            `json:"credential_id,omitempty"` // public credential id
	CredentialLabel string        `json:"credential_label,omitempty"` // user-friendly label
	
	// Attempt history
	Attempts       []RouteAttempt `json:"attempts"`
	CurrentAttempt int            `json:"current_attempt"` // 1-indexed
	
	// Outcome
	Status         string         `json:"status"`          // "success" | "failed" | "in_progress"
	FinalStatus    int            `json:"final_status,omitempty"` // HTTP status code
	LatencyMs      int64          `json:"latency_ms,omitempty"`
	TTFT           int64          `json:"ttft_ms,omitempty"` // time to first token
	TokensUsed     int            `json:"tokens_used,omitempty"`
	Error          string         `json:"error,omitempty"` // sanitized error message
	ErrorKind      string         `json:"error_kind,omitempty"` // error classification
	
	// Metadata
	UpdatedAt      time.Time      `json:"updated_at"`
	Version        int            `json:"version"` // increments on each update
}

// RouteAttempt records a single upstream request attempt within a route.
// Multiple attempts occur when fallback/retry logic is triggered.
type RouteAttempt struct {
	Sequence       int            `json:"sequence"`        // 1, 2, 3, ...
	CredentialID   int            `json:"credential_id"`
	CredentialLabel string        `json:"credential_label,omitempty"`
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	
	StartTime      time.Time      `json:"start_time"`
	EndTime        *time.Time     `json:"end_time,omitempty"`
	
	Status         int            `json:"status,omitempty"`    // HTTP status
	Success        bool           `json:"success"`
	LatencyMs      int64          `json:"latency_ms,omitempty"`
	TTFT           int64          `json:"ttft_ms,omitempty"`
	
	ErrorKind      string         `json:"error_kind,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"` // sanitized
	IsFallback     bool           `json:"is_fallback"`     // true if this is a retry
	
	// Token usage for this attempt
	PromptTokens   int            `json:"prompt_tokens,omitempty"`
	CompletionTokens int          `json:"completion_tokens,omitempty"`
}

// NewRouteTrace creates a new route trace for a request.
func NewRouteTrace(requestID string, tenantID int, clientModel string) *RouteTrace {
	now := time.Now()
	return &RouteTrace{
		RequestID:      requestID,
		TenantID:       tenantID,
		ClientModel:    clientModel,
		StartTime:      now,
		UpdatedAt:      now,
		Phase:          RoutePhaseStarted,
		Status:         "in_progress",
		Attempts:       make([]RouteAttempt, 0, 2), // preallocate for common case
		CurrentAttempt: 0,
		Version:        1,
	}
}

// UpdatePhase transitions to a new routing phase.
// Thread-safe.
func (rt *RouteTrace) UpdatePhase(phase RoutePhase) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	rt.Phase = phase
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// SetModelResolution records the canonical model resolution.
// Thread-safe.
func (rt *RouteTrace) SetModelResolution(canonicalModel string, canonicalID int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	rt.CanonicalModel = canonicalModel
	rt.CanonicalID = canonicalID
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// SetRoutingTarget records the selected provider and credential.
// Thread-safe.
func (rt *RouteTrace) SetRoutingTarget(provider string, credentialID int, credentialLabel string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	rt.Provider = provider
	rt.CredentialID = credentialID
	rt.CredentialLabel = credentialLabel
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// StartAttempt records the beginning of an upstream request attempt.
// Returns the attempt sequence number.
// Thread-safe.
func (rt *RouteTrace) StartAttempt(credentialID int, credentialLabel, provider, model string, isFallback bool) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	sequence := len(rt.Attempts) + 1
	attempt := RouteAttempt{
		Sequence:        sequence,
		CredentialID:    credentialID,
		CredentialLabel: credentialLabel,
		Provider:        provider,
		Model:           model,
		StartTime:       time.Now(),
		IsFallback:      isFallback,
	}
	
	rt.Attempts = append(rt.Attempts, attempt)
	rt.CurrentAttempt = sequence
	rt.Phase = RoutePhaseAttempting
	rt.UpdatedAt = time.Now()
	rt.Version++
	
	return sequence
}

// RecordTTFT records time-to-first-token for the current attempt.
// Thread-safe.
func (rt *RouteTrace) RecordTTFT(ttftMs int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	if rt.CurrentAttempt > 0 && rt.CurrentAttempt <= len(rt.Attempts) {
		rt.Attempts[rt.CurrentAttempt-1].TTFT = ttftMs
	}
	rt.TTFT = ttftMs
	rt.Phase = RoutePhaseResponding
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// CompleteAttempt marks the current attempt as finished.
// Thread-safe.
func (rt *RouteTrace) CompleteAttempt(status int, success bool, latencyMs int64, errorKind, errorMsg string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	if rt.CurrentAttempt > 0 && rt.CurrentAttempt <= len(rt.Attempts) {
		now := time.Now()
		attempt := &rt.Attempts[rt.CurrentAttempt-1]
		attempt.EndTime = &now
		attempt.Status = status
		attempt.Success = success
		attempt.LatencyMs = latencyMs
		attempt.ErrorKind = errorKind
		attempt.ErrorMessage = errorMsg
	}
	
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// SetTokenUsage records token usage for the current attempt and overall.
// Thread-safe.
func (rt *RouteTrace) SetTokenUsage(promptTokens, completionTokens int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	if rt.CurrentAttempt > 0 && rt.CurrentAttempt <= len(rt.Attempts) {
		attempt := &rt.Attempts[rt.CurrentAttempt-1]
		attempt.PromptTokens = promptTokens
		attempt.CompletionTokens = completionTokens
	}
	
	rt.TokensUsed = promptTokens + completionTokens
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// MarkDone marks the entire route as completed.
// Thread-safe.
func (rt *RouteTrace) MarkDone(finalStatus int, latencyMs int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	rt.Phase = RoutePhaseDone
	rt.Status = "success"
	rt.FinalStatus = finalStatus
	rt.LatencyMs = latencyMs
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// MarkFailed marks the route as failed after exhausting all attempts.
// Thread-safe.
func (rt *RouteTrace) MarkFailed(finalStatus int, latencyMs int64, errorKind, errorMsg string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	rt.Phase = RoutePhaseFailed
	rt.Status = "failed"
	rt.FinalStatus = finalStatus
	rt.LatencyMs = latencyMs
	rt.Error = errorMsg
	rt.ErrorKind = errorKind
	rt.UpdatedAt = time.Now()
	rt.Version++
}

// Snapshot returns a read-only copy of the current route trace.
// Thread-safe.
func (rt *RouteTrace) Snapshot() RouteTrace {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	
	// Deep copy attempts slice
	attemptsCopy := make([]RouteAttempt, len(rt.Attempts))
	copy(attemptsCopy, rt.Attempts)
	
	return RouteTrace{
		RequestID:       rt.RequestID,
		TenantID:        rt.TenantID,
		StartTime:       rt.StartTime,
		Phase:           rt.Phase,
		ClientModel:     rt.ClientModel,
		CanonicalModel:  rt.CanonicalModel,
		CanonicalID:     rt.CanonicalID,
		Provider:        rt.Provider,
		CredentialID:    rt.CredentialID,
		CredentialLabel: rt.CredentialLabel,
		Attempts:        attemptsCopy,
		CurrentAttempt:  rt.CurrentAttempt,
		Status:          rt.Status,
		FinalStatus:     rt.FinalStatus,
		LatencyMs:       rt.LatencyMs,
		TTFT:            rt.TTFT,
		TokensUsed:      rt.TokensUsed,
		Error:           rt.Error,
		ErrorKind:       rt.ErrorKind,
		UpdatedAt:       rt.UpdatedAt,
		Version:         rt.Version,
	}
}

// RouteTraceStore manages active route traces.
// It provides a thread-safe registry for in-flight request traces.
type RouteTraceStore struct {
	mu     sync.RWMutex
	traces map[string]*RouteTrace // keyed by request_id
}

// NewRouteTraceStore creates a new route trace store.
func NewRouteTraceStore() *RouteTraceStore {
	return &RouteTraceStore{
		traces: make(map[string]*RouteTrace),
	}
}

// Get retrieves a route trace by request ID.
// Returns nil if not found.
// Thread-safe.
func (s *RouteTraceStore) Get(requestID string) *RouteTrace {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.traces[requestID]
}

// Set stores a route trace.
// Thread-safe.
func (s *RouteTraceStore) Set(trace *RouteTrace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces[trace.RequestID] = trace
}

// Remove deletes a route trace from the store.
// Called after request completion to bound memory usage.
// Thread-safe.
func (s *RouteTraceStore) Remove(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.traces, requestID)
}

// Cleanup removes traces older than the given duration.
// Should be called periodically to prevent unbounded growth.
// Thread-safe.
func (s *RouteTraceStore) Cleanup(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	now := time.Now()
	removed := 0
	
	for requestID, trace := range s.traces {
		trace.mu.RLock()
		age := now.Sub(trace.UpdatedAt)
		trace.mu.RUnlock()
		
		if age > maxAge {
			delete(s.traces, requestID)
			removed++
		}
	}
	
	return removed
}

// Len returns the current number of tracked traces.
// Thread-safe.
func (s *RouteTraceStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.traces)
}
