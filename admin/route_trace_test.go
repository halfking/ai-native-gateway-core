package admin

import (
	"testing"
	"time"
)

func TestNewRouteTrace(t *testing.T) {
	requestID := "req-123"
	tenantID := 42
	clientModel := "gpt-4"
	
	trace := NewRouteTrace(requestID, tenantID, clientModel)
	
	if trace.RequestID != requestID {
		t.Errorf("Expected RequestID %s, got %s", requestID, trace.RequestID)
	}
	if trace.TenantID != tenantID {
		t.Errorf("Expected TenantID %d, got %d", tenantID, trace.TenantID)
	}
	if trace.ClientModel != clientModel {
		t.Errorf("Expected ClientModel %s, got %s", clientModel, trace.ClientModel)
	}
	if trace.Phase != RoutePhaseStarted {
		t.Errorf("Expected Phase %s, got %s", RoutePhaseStarted, trace.Phase)
	}
	if trace.Status != "in_progress" {
		t.Errorf("Expected Status in_progress, got %s", trace.Status)
	}
	if trace.Version != 1 {
		t.Errorf("Expected Version 1, got %d", trace.Version)
	}
}

func TestRouteTrace_UpdatePhase(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	initialVersion := trace.Version
	
	trace.UpdatePhase(RoutePhaseSelecting)
	
	if trace.Phase != RoutePhaseSelecting {
		t.Errorf("Expected Phase %s, got %s", RoutePhaseSelecting, trace.Phase)
	}
	if trace.Version != initialVersion+1 {
		t.Errorf("Expected Version to increment, got %d", trace.Version)
	}
}

func TestRouteTrace_SetModelResolution(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	
	trace.SetModelResolution("gpt-4-turbo", 123)
	
	if trace.CanonicalModel != "gpt-4-turbo" {
		t.Errorf("Expected CanonicalModel gpt-4-turbo, got %s", trace.CanonicalModel)
	}
	if trace.CanonicalID != 123 {
		t.Errorf("Expected CanonicalID 123, got %d", trace.CanonicalID)
	}
}

func TestRouteTrace_StartAttempt(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	
	seq1 := trace.StartAttempt(10, "OpenAI Key 1", "openai", "gpt-4-turbo", false)
	seq2 := trace.StartAttempt(11, "OpenAI Key 2", "openai", "gpt-4-turbo", true)
	
	if seq1 != 1 {
		t.Errorf("Expected first sequence 1, got %d", seq1)
	}
	if seq2 != 2 {
		t.Errorf("Expected second sequence 2, got %d", seq2)
	}
	if len(trace.Attempts) != 2 {
		t.Errorf("Expected 2 attempts, got %d", len(trace.Attempts))
	}
	if trace.CurrentAttempt != 2 {
		t.Errorf("Expected CurrentAttempt 2, got %d", trace.CurrentAttempt)
	}
	if trace.Phase != RoutePhaseAttempting {
		t.Errorf("Expected Phase %s, got %s", RoutePhaseAttempting, trace.Phase)
	}
	if !trace.Attempts[1].IsFallback {
		t.Error("Expected second attempt to be marked as fallback")
	}
}

func TestRouteTrace_RecordTTFT(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	
	trace.RecordTTFT(250)
	
	if trace.TTFT != 250 {
		t.Errorf("Expected TTFT 250, got %d", trace.TTFT)
	}
	if trace.Attempts[0].TTFT != 250 {
		t.Errorf("Expected attempt TTFT 250, got %d", trace.Attempts[0].TTFT)
	}
	if trace.Phase != RoutePhaseResponding {
		t.Errorf("Expected Phase %s, got %s", RoutePhaseResponding, trace.Phase)
	}
}

func TestRouteTrace_CompleteAttempt(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	
	trace.CompleteAttempt(200, true, 1234, "", "")
	
	attempt := trace.Attempts[0]
	if attempt.Status != 200 {
		t.Errorf("Expected Status 200, got %d", attempt.Status)
	}
	if !attempt.Success {
		t.Error("Expected Success true")
	}
	if attempt.LatencyMs != 1234 {
		t.Errorf("Expected LatencyMs 1234, got %d", attempt.LatencyMs)
	}
	if attempt.EndTime == nil {
		t.Error("Expected EndTime to be set")
	}
}

func TestRouteTrace_CompleteAttemptWithError(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	
	trace.CompleteAttempt(429, false, 500, "rate_limit", "Too many requests")
	
	attempt := trace.Attempts[0]
	if attempt.Status != 429 {
		t.Errorf("Expected Status 429, got %d", attempt.Status)
	}
	if attempt.Success {
		t.Error("Expected Success false")
	}
	if attempt.ErrorKind != "rate_limit" {
		t.Errorf("Expected ErrorKind rate_limit, got %s", attempt.ErrorKind)
	}
	if attempt.ErrorMessage != "Too many requests" {
		t.Errorf("Expected ErrorMessage 'Too many requests', got %s", attempt.ErrorMessage)
	}
}

func TestRouteTrace_SetTokenUsage(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	
	trace.SetTokenUsage(1000, 500)
	
	if trace.TokensUsed != 1500 {
		t.Errorf("Expected TokensUsed 1500, got %d", trace.TokensUsed)
	}
	attempt := trace.Attempts[0]
	if attempt.PromptTokens != 1000 {
		t.Errorf("Expected PromptTokens 1000, got %d", attempt.PromptTokens)
	}
	if attempt.CompletionTokens != 500 {
		t.Errorf("Expected CompletionTokens 500, got %d", attempt.CompletionTokens)
	}
}

func TestRouteTrace_MarkDone(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	
	trace.MarkDone(200, 1500)
	
	if trace.Phase != RoutePhaseDone {
		t.Errorf("Expected Phase %s, got %s", RoutePhaseDone, trace.Phase)
	}
	if trace.Status != "success" {
		t.Errorf("Expected Status success, got %s", trace.Status)
	}
	if trace.FinalStatus != 200 {
		t.Errorf("Expected FinalStatus 200, got %d", trace.FinalStatus)
	}
	if trace.LatencyMs != 1500 {
		t.Errorf("Expected LatencyMs 1500, got %d", trace.LatencyMs)
	}
}

func TestRouteTrace_MarkFailed(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	
	trace.MarkFailed(500, 2000, "upstream_error", "Internal server error")
	
	if trace.Phase != RoutePhaseFailed {
		t.Errorf("Expected Phase %s, got %s", RoutePhaseFailed, trace.Phase)
	}
	if trace.Status != "failed" {
		t.Errorf("Expected Status failed, got %s", trace.Status)
	}
	if trace.FinalStatus != 500 {
		t.Errorf("Expected FinalStatus 500, got %d", trace.FinalStatus)
	}
	if trace.ErrorKind != "upstream_error" {
		t.Errorf("Expected ErrorKind upstream_error, got %s", trace.ErrorKind)
	}
	if trace.Error != "Internal server error" {
		t.Errorf("Expected Error message, got %s", trace.Error)
	}
}

func TestRouteTrace_Snapshot(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	trace.StartAttempt(10, "Key 1", "openai", "gpt-4", false)
	trace.UpdatePhase(RoutePhaseResponding)
	
	snapshot := trace.Snapshot()
	
	// Verify snapshot is a copy
	if &snapshot == trace {
		t.Error("Snapshot should be a copy, not the same reference")
	}
	
	// Verify data is copied correctly
	if snapshot.RequestID != trace.RequestID {
		t.Error("Snapshot RequestID mismatch")
	}
	if snapshot.Phase != trace.Phase {
		t.Error("Snapshot Phase mismatch")
	}
	if len(snapshot.Attempts) != len(trace.Attempts) {
		t.Error("Snapshot Attempts length mismatch")
	}
	
	// Modify original and verify snapshot is unchanged
	trace.UpdatePhase(RoutePhaseDone)
	if snapshot.Phase == trace.Phase {
		t.Error("Snapshot should not be affected by changes to original")
	}
}

func TestRouteTrace_ConcurrentAccess(t *testing.T) {
	trace := NewRouteTrace("req-1", 1, "gpt-4")
	
	done := make(chan bool)
	
	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			trace.UpdatePhase(RoutePhaseSelecting)
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()
	
	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_ = trace.Snapshot()
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()
	
	// Wait for both goroutines
	<-done
	<-done
	
	// If we reach here without data race, test passes
}

func TestRouteTraceStore_BasicOperations(t *testing.T) {
	store := NewRouteTraceStore()
	
	trace1 := NewRouteTrace("req-1", 1, "gpt-4")
	trace2 := NewRouteTrace("req-2", 1, "claude-3")
	
	// Test Set and Get
	store.Set(trace1)
	store.Set(trace2)
	
	if store.Len() != 2 {
		t.Errorf("Expected 2 traces, got %d", store.Len())
	}
	
	retrieved := store.Get("req-1")
	if retrieved == nil {
		t.Fatal("Expected to retrieve trace req-1")
	}
	if retrieved.RequestID != "req-1" {
		t.Errorf("Retrieved wrong trace: %s", retrieved.RequestID)
	}
	
	// Test Remove
	store.Remove("req-1")
	if store.Len() != 1 {
		t.Errorf("Expected 1 trace after removal, got %d", store.Len())
	}
	
	retrieved = store.Get("req-1")
	if retrieved != nil {
		t.Error("Expected nil after removal")
	}
}

func TestRouteTraceStore_Cleanup(t *testing.T) {
	store := NewRouteTraceStore()
	
	// Create old trace
	oldTrace := NewRouteTrace("req-old", 1, "gpt-4")
	oldTrace.UpdatedAt = time.Now().Add(-2 * time.Hour)
	store.Set(oldTrace)
	
	// Create recent trace
	recentTrace := NewRouteTrace("req-recent", 1, "gpt-4")
	store.Set(recentTrace)
	
	if store.Len() != 2 {
		t.Fatalf("Expected 2 traces before cleanup, got %d", store.Len())
	}
	
	// Cleanup traces older than 1 hour
	removed := store.Cleanup(1 * time.Hour)
	
	if removed != 1 {
		t.Errorf("Expected 1 trace removed, got %d", removed)
	}
	if store.Len() != 1 {
		t.Errorf("Expected 1 trace after cleanup, got %d", store.Len())
	}
	
	// Verify correct trace was removed
	if store.Get("req-old") != nil {
		t.Error("Old trace should be removed")
	}
	if store.Get("req-recent") == nil {
		t.Error("Recent trace should remain")
	}
}

func TestRouteTraceStore_ConcurrentAccess(t *testing.T) {
	store := NewRouteTraceStore()
	done := make(chan bool)
	
	// Multiple writers
	for i := 0; i < 5; i++ {
		go func(id int) {
			for j := 0; j < 20; j++ {
				trace := NewRouteTrace("req-"+string(rune(id*20+j)), 1, "gpt-4")
				store.Set(trace)
				time.Sleep(time.Microsecond)
			}
			done <- true
		}(i)
	}
	
	// Multiple readers
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 20; j++ {
				_ = store.Len()
				_ = store.Get("req-1")
				time.Sleep(time.Microsecond)
			}
			done <- true
		}()
	}
	
	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
	
	// If we reach here without data race, test passes
}

func TestRouteTrace_FullWorkflow(t *testing.T) {
	// Simulate a complete request routing workflow
	trace := NewRouteTrace("req-workflow", 42, "gpt-4")
	
	// Phase 1: Model resolution
	trace.SetModelResolution("gpt-4-turbo", 101)
	if trace.Phase != RoutePhaseStarted {
		t.Error("Model resolution should not change phase from started")
	}
	
	// Phase 2: Credential selection
	trace.UpdatePhase(RoutePhaseSelecting)
	trace.SetRoutingTarget("openai", 10, "OpenAI Key 1")
	
	// Phase 3: First attempt (fails)
	trace.StartAttempt(10, "OpenAI Key 1", "openai", "gpt-4-turbo", false)
	if trace.Phase != RoutePhaseAttempting {
		t.Error("StartAttempt should set phase to attempting")
	}
	trace.CompleteAttempt(429, false, 500, "rate_limit", "Rate limit exceeded")
	
	// Phase 4: Fallback to second credential
	trace.UpdatePhase(RoutePhaseFallback)
	trace.StartAttempt(11, "OpenAI Key 2", "openai", "gpt-4-turbo", true)
	
	// Phase 5: Second attempt succeeds
	trace.RecordTTFT(250)
	if trace.Phase != RoutePhaseResponding {
		t.Error("RecordTTFT should set phase to responding")
	}
	trace.SetTokenUsage(1000, 500)
	trace.CompleteAttempt(200, true, 1234, "", "")
	
	// Phase 6: Mark done
	trace.MarkDone(200, 1734) // 500ms first attempt + 1234ms second
	
	// Verify final state
	if trace.Phase != RoutePhaseDone {
		t.Errorf("Expected final phase %s, got %s", RoutePhaseDone, trace.Phase)
	}
	if trace.Status != "success" {
		t.Errorf("Expected status success, got %s", trace.Status)
	}
	if len(trace.Attempts) != 2 {
		t.Errorf("Expected 2 attempts, got %d", len(trace.Attempts))
	}
	if trace.Attempts[0].IsFallback {
		t.Error("First failed attempt should not be fallback")
	}
	if !trace.Attempts[1].IsFallback {
		t.Error("Second attempt should be marked as fallback")
	}
	if trace.TokensUsed != 1500 {
		t.Errorf("Expected 1500 tokens used, got %d", trace.TokensUsed)
	}
	if trace.TTFT != 250 {
		t.Errorf("Expected TTFT 250ms, got %d", trace.TTFT)
	}
	
	// Verify snapshot works after full workflow
	snapshot := trace.Snapshot()
	if snapshot.Phase != RoutePhaseDone {
		t.Error("Snapshot should capture final state")
	}
	if len(snapshot.Attempts) != 2 {
		t.Error("Snapshot should include all attempts")
	}
}
