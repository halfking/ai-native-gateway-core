package relay

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/telemetry"
)

// TestApplyAutoRouteFields_NonAutoRequest verifies that non-auto requests
// get is_auto_request explicitly set to false (not left as nil).
func TestApplyAutoRouteFields_NonAutoRequest(t *testing.T) {
	// Setup: non-auto request context
	ctx := &RequestLogContext{
		IsAutoRequest: false, // This is the key field
		ClientModel:   "gpt-4",
		WorkType:      "chat",
	}

	entry := &telemetry.RequestLogEntry{}

	// Act
	applyAutoRouteFields(entry, ctx)

	// Assert: is_auto_request should be explicitly set to false, not nil
	if entry.IsAutoRequest == nil {
		t.Error("Expected is_auto_request to be set to false for non-auto request, got nil")
		return
	}
	if *entry.IsAutoRequest != false {
		t.Errorf("Expected is_auto_request=false, got %v", *entry.IsAutoRequest)
	}

	// Verify that auto-specific fields are NOT set
	if entry.TaskType != nil {
		t.Errorf("Expected task_type to be nil for non-auto request, got %v", *entry.TaskType)
	}
	if entry.AutoProfile != nil {
		t.Errorf("Expected auto_profile to be nil for non-auto request, got %v", *entry.AutoProfile)
	}

	// Verify work_type is still set (it applies to all requests)
	if entry.WorkType == nil || *entry.WorkType != "chat" {
		t.Errorf("Expected work_type='chat', got %v", entry.WorkType)
	}
}

// TestApplyAutoRouteFields_AutoRequest verifies that auto requests
// get is_auto_request=true and related fields populated.
func TestApplyAutoRouteFields_AutoRequest(t *testing.T) {
	// Setup: auto request context
	ctx := &RequestLogContext{
		IsAutoRequest:  true,
		TaskType:       "coding",
		AutoProfile:    "standard",
		AutoConfidence: 0.95,
		AutoDecision:   []byte(`{"task_type":"coding","confidence":0.95,"chosen_model":"gpt-4","profile":"standard"}`),
		WorkType:       "completion",
	}

	entry := &telemetry.RequestLogEntry{}

	// Act
	applyAutoRouteFields(entry, ctx)

	// Assert: is_auto_request should be true
	if entry.IsAutoRequest == nil {
		t.Error("Expected is_auto_request to be set to true for auto request, got nil")
		return
	}
	if *entry.IsAutoRequest != true {
		t.Errorf("Expected is_auto_request=true, got %v", *entry.IsAutoRequest)
	}

	// Verify auto-specific fields are set
	if entry.TaskType == nil || *entry.TaskType != "coding" {
		t.Errorf("Expected task_type='coding', got %v", entry.TaskType)
	}
	if entry.AutoProfile == nil || *entry.AutoProfile != "standard" {
		t.Errorf("Expected auto_profile='standard', got %v", entry.AutoProfile)
	}
	if entry.AutoConfidence == nil || *entry.AutoConfidence != 0.95 {
		t.Errorf("Expected auto_confidence=0.95, got %v", entry.AutoConfidence)
	}
	if entry.AutoDecision == nil {
		t.Error("Expected auto_decision to be set, got nil")
	}

	// Verify work_type is set
	if entry.WorkType == nil || *entry.WorkType != "completion" {
		t.Errorf("Expected work_type='completion', got %v", entry.WorkType)
	}

	// Verify promoted columns from auto_decision JSON
	if entry.TaskTypeChosen == nil || *entry.TaskTypeChosen != "coding" {
		t.Errorf("Expected task_type_chosen='coding', got %v", entry.TaskTypeChosen)
	}
	if entry.ModelChosen == nil || *entry.ModelChosen != "gpt-4" {
		t.Errorf("Expected model_chosen='gpt-4', got %v", entry.ModelChosen)
	}
	if entry.ConfidenceNum == nil || *entry.ConfidenceNum != 0.95 {
		t.Errorf("Expected confidence_num=0.95, got %v", entry.ConfidenceNum)
	}
}

// TestApplyAutoRouteFields_NilContext verifies safe handling of nil inputs.
func TestApplyAutoRouteFields_NilContext(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	
	// Should not panic with nil context
	applyAutoRouteFields(entry, nil)
	
	// Entry should remain unchanged
	if entry.IsAutoRequest != nil {
		t.Errorf("Expected is_auto_request to remain nil with nil context, got %v", *entry.IsAutoRequest)
	}
	
	// Should not panic with nil entry
	ctx := &RequestLogContext{IsAutoRequest: true}
	applyAutoRouteFields(nil, ctx)
}

// TestApplyAutoRouteFields_WorkTypeOnly verifies that work_type is applied
// even when is_auto_request is false and other auto fields are empty.
func TestApplyAutoRouteFields_WorkTypeOnly(t *testing.T) {
	ctx := &RequestLogContext{
		IsAutoRequest: false,
		WorkType:      "image_generation",
	}

	entry := &telemetry.RequestLogEntry{}
	applyAutoRouteFields(entry, ctx)

	// work_type should be set
	if entry.WorkType == nil || *entry.WorkType != "image_generation" {
		t.Errorf("Expected work_type='image_generation', got %v", entry.WorkType)
	}

	// is_auto_request should be false
	if entry.IsAutoRequest == nil || *entry.IsAutoRequest != false {
		t.Errorf("Expected is_auto_request=false, got %v", entry.IsAutoRequest)
	}
}

// TestBuildFailureEntry_NonAutoRequest verifies that failure entries
// for non-auto requests have is_auto_request=false.
func TestBuildFailureEntry_NonAutoRequest(t *testing.T) {
	handler := &ChatHandler{}
	ctx := handler.NewRequestLogContext(nil, "test-req-123", time.Now())
	ctx.ClientModel = "claude-3-opus"
	ctx.Body = []byte(`{"model":"claude-3-opus","messages":[{"role":"user","content":"test"}]}`)
	// Note: IsAutoRequest defaults to false

	entry := ctx.BuildFailureEntry("timeout", "request timeout", nil, nil)

	if entry == nil {
		t.Fatal("Expected non-nil failure entry")
	}

	// Verify is_auto_request is explicitly false
	if entry.IsAutoRequest == nil {
		t.Error("Expected is_auto_request to be set to false, got nil")
	} else if *entry.IsAutoRequest != false {
		t.Errorf("Expected is_auto_request=false, got %v", *entry.IsAutoRequest)
	}

	// Verify failure metadata
	if entry.Success != false {
		t.Error("Expected success=false for failure entry")
	}
	if entry.ErrorKind == nil || *entry.ErrorKind != "timeout" {
		t.Errorf("Expected error_kind='timeout', got %v", entry.ErrorKind)
	}
}
