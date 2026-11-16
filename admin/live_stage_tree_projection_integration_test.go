// Package admin — stage tree projection integration tests.
//
// These tests verify the complete pipeline from RouteTrace to StageTreeFrame,
// ensuring that routing phases correctly map to stage statuses and that the
// frontend receives properly structured visualization data.
//
// 2026-11-16: Initial test suite for stage tree projection.
package admin

import (
	"testing"
)

// TestProjectStageTree_InitialState verifies projection of a newly started request.
func TestProjectStageTree_InitialState(t *testing.T) {
	trace := NewRouteTrace("req_001", 1, "gpt-4")
	
	frame := ProjectStageTree(trace)
	
	if frame == nil {
		t.Fatal("ProjectStageTree returned nil")
	}
	
	if frame.RequestID != "req_001" {
		t.Errorf("RequestID = %q, want %q", frame.RequestID, "req_001")
	}
	
	if frame.ClientModel != "gpt-4" {
		t.Errorf("ClientModel = %q, want %q", frame.ClientModel, "gpt-4")
	}
	
	if frame.Phase != RoutePhaseStarted {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseStarted)
	}
	
	if frame.OverallStatus != StageStatusActive {
		t.Errorf("OverallStatus = %q, want %q", frame.OverallStatus, StageStatusActive)
	}
	
	// Verify send stages
	if len(frame.SendStages) != 7 {
		t.Fatalf("len(SendStages) = %d, want 7", len(frame.SendStages))
	}
	
	// Auth should be active, rest pending
	if frame.SendStages[0].Status != StageStatusActive {
		t.Errorf("Auth stage status = %q, want %q", frame.SendStages[0].Status, StageStatusActive)
	}
	
	for i := 1; i < 7; i++ {
		if frame.SendStages[i].Status != StageStatusPending {
			t.Errorf("SendStages[%d].Status = %q, want %q", i, frame.SendStages[i].Status, StageStatusPending)
		}
	}
	
	// Verify recv stages - all pending initially
	if len(frame.RecvStages) != 4 {
		t.Fatalf("len(RecvStages) = %d, want 4", len(frame.RecvStages))
	}
	
	for i := range frame.RecvStages {
		if frame.RecvStages[i].Status != StageStatusPending {
			t.Errorf("RecvStages[%d].Status = %q, want %q", i, frame.RecvStages[i].Status, StageStatusPending)
		}
	}
}

// TestProjectStageTree_SelectingPhase verifies projection during credential selection.
func TestProjectStageTree_SelectingPhase(t *testing.T) {
	trace := NewRouteTrace("req_002", 1, "gpt-4")
	trace.UpdatePhase(RoutePhaseSelecting)
	trace.SetModelResolution("gpt-4-turbo", 42)
	
	frame := ProjectStageTree(trace)
	
	if frame.Phase != RoutePhaseSelecting {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseSelecting)
	}
	
	if frame.CanonicalModel != "gpt-4-turbo" {
		t.Errorf("CanonicalModel = %q, want %q", frame.CanonicalModel, "gpt-4-turbo")
	}
	
	// Route stage should be active
	routeStage := findStage(frame.SendStages, StageKeyRoute)
	if routeStage == nil {
		t.Fatal("Route stage not found")
	}
	if routeStage.Status != StageStatusActive {
		t.Errorf("Route stage status = %q, want %q", routeStage.Status, StageStatusActive)
	}
	
	// Provider stage should be pending
	providerStage := findStage(frame.SendStages, StageKeyProvider)
	if providerStage == nil {
		t.Fatal("Provider stage not found")
	}
	if providerStage.Status != StageStatusPending {
		t.Errorf("Provider stage status = %q, want %q", providerStage.Status, StageStatusPending)
	}
}

// TestProjectStageTree_AttemptingPhase verifies projection during upstream request.
func TestProjectStageTree_AttemptingPhase(t *testing.T) {
	trace := NewRouteTrace("req_003", 1, "claude-3")
	trace.UpdatePhase(RoutePhaseSelecting)
	trace.SetModelResolution("claude-3-opus", 50)
	trace.SetRoutingTarget("anthropic", 101, "cred-anthropic-main")
	trace.StartAttempt(101, "cred-anthropic-main", "anthropic", "claude-3-opus", false)
	
	frame := ProjectStageTree(trace)
	
	if frame.Phase != RoutePhaseAttempting {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseAttempting)
	}
	
	// Provider stage should be active
	providerStage := findStage(frame.SendStages, StageKeyProvider)
	if providerStage == nil {
		t.Fatal("Provider stage not found")
	}
	if providerStage.Status != StageStatusActive {
		t.Errorf("Provider stage status = %q, want %q", providerStage.Status, StageStatusActive)
	}
	
	// Check provider metadata
	if providerStage.Metadata == nil {
		t.Fatal("Provider metadata is nil")
	}
	
	if provider, ok := providerStage.Metadata["provider"].(string); !ok || provider != "anthropic" {
		t.Errorf("Provider metadata = %v, want %q", providerStage.Metadata["provider"], "anthropic")
	}
	
	// Verify attempt recorded
	if len(frame.Attempts) != 1 {
		t.Fatalf("len(Attempts) = %d, want 1", len(frame.Attempts))
	}
	
	if frame.CurrentAttempt != 1 {
		t.Errorf("CurrentAttempt = %d, want 1", frame.CurrentAttempt)
	}
}

// TestProjectStageTree_RespondingPhase verifies projection during response streaming.
func TestProjectStageTree_RespondingPhase(t *testing.T) {
	trace := NewRouteTrace("req_004", 1, "deepseek-chat")
	trace.SetModelResolution("deepseek-chat", 60)
	trace.SetRoutingTarget("deepseek", 102, "cred-deepseek-pool")
	trace.StartAttempt(102, "cred-deepseek-pool", "deepseek", "deepseek-chat", false)
	trace.RecordTTFT(250) // 250ms to first token
	
	frame := ProjectStageTree(trace)
	
	if frame.Phase != RoutePhaseResponding {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseResponding)
	}
	
	if frame.TTFT != 250 {
		t.Errorf("TTFT = %d, want 250", frame.TTFT)
	}
	
	// First byte stage should be done
	firstByteStage := findStage(frame.RecvStages, StageKeyFirstByte)
	if firstByteStage == nil {
		t.Fatal("FirstByte stage not found")
	}
	if firstByteStage.Status != StageStatusDone {
		t.Errorf("FirstByte stage status = %q, want %q", firstByteStage.Status, StageStatusDone)
	}
	if firstByteStage.DurationMs != 250 {
		t.Errorf("FirstByte duration = %d, want 250", firstByteStage.DurationMs)
	}
	
	// Billing stage should be active
	billingStage := findStage(frame.RecvStages, StageKeyBilling)
	if billingStage == nil {
		t.Fatal("Billing stage not found")
	}
	if billingStage.Status != StageStatusActive {
		t.Errorf("Billing stage status = %q, want %q", billingStage.Status, StageStatusActive)
	}
}

// TestProjectStageTree_CompletedRequest verifies projection of completed request.
func TestProjectStageTree_CompletedRequest(t *testing.T) {
	trace := NewRouteTrace("req_005", 1, "qwen-max")
	trace.SetModelResolution("qwen-max", 70)
	trace.SetRoutingTarget("aliyun", 103, "cred-aliyun-bailian")
	
	attemptSeq := trace.StartAttempt(103, "cred-aliyun-bailian", "aliyun", "qwen-max", false)
	trace.RecordTTFT(180)
	trace.SetTokenUsage(1500, 800)
	trace.CompleteAttempt(200, true, 3500, "", "")
	trace.MarkDone(200, 3500)
	
	frame := ProjectStageTree(trace)
	
	if frame.Phase != RoutePhaseDone {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseDone)
	}
	
	if frame.OverallStatus != StageStatusDone {
		t.Errorf("OverallStatus = %q, want %q", frame.OverallStatus, StageStatusDone)
	}
	
	if frame.TotalLatencyMs != 3500 {
		t.Errorf("TotalLatencyMs = %d, want 3500", frame.TotalLatencyMs)
	}
	
	if frame.TokensIn != 1500 {
		t.Errorf("TokensIn = %d, want 1500", frame.TokensIn)
	}
	
	if frame.TokensOut != 800 {
		t.Errorf("TokensOut = %d, want 800", frame.TokensOut)
	}
	
	// All send stages should be done
	for i, stage := range frame.SendStages {
		if stage.Key == StageKeyApproval || stage.Key == StageKeyInjection || 
		   stage.Key == StageKeySanitize || stage.Key == StageKeyCompress {
			// These stages are typically skipped
			continue
		}
		if stage.Status != StageStatusDone {
			t.Errorf("SendStages[%d] (%s) status = %q, want %q", i, stage.Key, stage.Status, StageStatusDone)
		}
	}
	
	// All recv stages should be done
	for i, stage := range frame.RecvStages {
		if stage.Key == StageKeyRestore {
			// Restore is often skipped
			continue
		}
		if stage.Status != StageStatusDone {
			t.Errorf("RecvStages[%d] (%s) status = %q, want %q", i, stage.Key, stage.Status, StageStatusDone)
		}
	}
	
	// Verify attempt
	if attemptSeq != 1 {
		t.Errorf("attemptSeq = %d, want 1", attemptSeq)
	}
	
	if len(frame.Attempts) != 1 {
		t.Fatalf("len(Attempts) = %d, want 1", len(frame.Attempts))
	}
	
	attempt := frame.Attempts[0]
	if !attempt.Success {
		t.Error("Attempt should be successful")
	}
	if attempt.LatencyMs != 3500 {
		t.Errorf("Attempt latency = %d, want 3500", attempt.LatencyMs)
	}
}

// TestProjectStageTree_FailedRequest verifies projection of failed request.
func TestProjectStageTree_FailedRequest(t *testing.T) {
	trace := NewRouteTrace("req_006", 1, "gpt-4")
	trace.SetModelResolution("gpt-4-turbo", 42)
	trace.SetRoutingTarget("openai", 104, "cred-openai-main")
	
	trace.StartAttempt(104, "cred-openai-main", "openai", "gpt-4-turbo", false)
	trace.CompleteAttempt(429, false, 500, "upstream_rate_limited", "Rate limit exceeded")
	trace.MarkFailed(429, 500, "upstream_rate_limited", "Rate limit exceeded")
	
	frame := ProjectStageTree(trace)
	
	if frame.Phase != RoutePhaseFailed {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseFailed)
	}
	
	if frame.OverallStatus != StageStatusFailed {
		t.Errorf("OverallStatus = %q, want %q", frame.OverallStatus, StageStatusFailed)
	}
	
	// Provider stage should show failure
	providerStage := findStage(frame.SendStages, StageKeyProvider)
	if providerStage == nil {
		t.Fatal("Provider stage not found")
	}
	if providerStage.Status != StageStatusFailed {
		t.Errorf("Provider stage status = %q, want %q", providerStage.Status, StageStatusFailed)
	}
	if providerStage.ErrorKind != "upstream_rate_limited" {
		t.Errorf("Provider error kind = %q, want %q", providerStage.ErrorKind, "upstream_rate_limited")
	}
	if providerStage.ErrorSource != "provider" {
		t.Errorf("Provider error source = %q, want %q", providerStage.ErrorSource, "provider")
	}
	
	// Recv stages should be skipped
	for i, stage := range frame.RecvStages {
		if stage.Status != StageStatusSkipped {
			t.Errorf("RecvStages[%d] status = %q, want %q", i, stage.Status, StageStatusSkipped)
		}
	}
}

// TestProjectStageTree_FallbackRetry verifies projection with retry attempts.
func TestProjectStageTree_FallbackRetry(t *testing.T) {
	trace := NewRouteTrace("req_007", 1, "claude-3")
	trace.SetModelResolution("claude-3-opus", 50)
	trace.SetRoutingTarget("anthropic", 105, "cred-anthropic-1")
	
	// First attempt fails
	trace.StartAttempt(105, "cred-anthropic-1", "anthropic", "claude-3-opus", false)
	trace.CompleteAttempt(503, false, 300, "upstream_5xx", "Service unavailable")
	
	// Second attempt (fallback) succeeds
	trace.UpdatePhase(RoutePhaseFallback)
	trace.SetRoutingTarget("anthropic", 106, "cred-anthropic-2")
	trace.StartAttempt(106, "cred-anthropic-2", "anthropic", "claude-3-opus", true)
	trace.RecordTTFT(200)
	trace.SetTokenUsage(1200, 600)
	trace.CompleteAttempt(200, true, 2500, "", "")
	trace.MarkDone(200, 2800) // Total including first attempt
	
	frame := ProjectStageTree(trace)
	
	if frame.Phase != RoutePhaseDone {
		t.Errorf("Phase = %q, want %q", frame.Phase, RoutePhaseDone)
	}
	
	if len(frame.Attempts) != 2 {
		t.Fatalf("len(Attempts) = %d, want 2", len(frame.Attempts))
	}
	
	// First attempt should be marked as failed
	attempt1 := frame.Attempts[0]
	if attempt1.Success {
		t.Error("First attempt should be failed")
	}
	if attempt1.IsFallback {
		t.Error("First attempt should not be marked as fallback")
	}
	
	// Second attempt should be successful and marked as fallback
	attempt2 := frame.Attempts[1]
	if !attempt2.Success {
		t.Error("Second attempt should be successful")
	}
	if !attempt2.IsFallback {
		t.Error("Second attempt should be marked as fallback")
	}
	
	if frame.CurrentAttempt != 2 {
		t.Errorf("CurrentAttempt = %d, want 2", frame.CurrentAttempt)
	}
}

// TestProjectStageTree_NilTrace verifies handling of nil input.
func TestProjectStageTree_NilTrace(t *testing.T) {
	frame := ProjectStageTree(nil)
	
	if frame != nil {
		t.Error("ProjectStageTree(nil) should return nil")
	}
}

// TestProjectStageTree_VersionIncrement verifies version tracking.
func TestProjectStageTree_VersionIncrement(t *testing.T) {
	trace := NewRouteTrace("req_008", 1, "gpt-4")
	
	frame1 := ProjectStageTree(trace)
	v1 := frame1.Version
	
	// Update trace
	trace.UpdatePhase(RoutePhaseSelecting)
	frame2 := ProjectStageTree(trace)
	v2 := frame2.Version
	
	if v2 <= v1 {
		t.Errorf("Version should increment: v1=%d, v2=%d", v1, v2)
	}
	
	// Another update
	trace.SetModelResolution("gpt-4-turbo", 42)
	frame3 := ProjectStageTree(trace)
	v3 := frame3.Version
	
	if v3 <= v2 {
		t.Errorf("Version should increment: v2=%d, v3=%d", v2, v3)
	}
}

// TestClassifyErrorSource verifies error source classification.
func TestClassifyErrorSource(t *testing.T) {
	tests := []struct {
		errorKind string
		want      string
	}{
		{"auth_rejected", "gateway"},
		{"approval_required", "gateway"},
		{"prompt_injection_blocked", "gateway"},
		{"sanitize_error", "gateway"},
		{"no_route", "gateway"},
		{"circuit_open", "gateway"},
		{"upstream_rate_limited", "provider"},
		{"upstream_5xx", "provider"},
		{"upstream_timeout", "provider"},
		{"connect_failed", "provider"},
		{"client_canceled", "other"},
		{"unknown_error", "other"},
	}
	
	for _, tt := range tests {
		got := classifyErrorSource(tt.errorKind)
		if got != tt.want {
			t.Errorf("classifyErrorSource(%q) = %q, want %q", tt.errorKind, got, tt.want)
		}
	}
}

// Helper function to find a stage by key.
func findStage(stages []StageNode, key StageKey) *StageNode {
	for i := range stages {
		if stages[i].Key == key {
			return &stages[i]
		}
	}
	return nil
}
