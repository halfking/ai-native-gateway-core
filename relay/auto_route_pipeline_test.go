package relay

// auto_route_pipeline_test.go — P7.2 tests for the JSON->column
// population in applyAutoRouteFields. We don't hit a real DB; we
// just verify the in-memory RequestLogEntry is populated correctly
// from the wire JSON, which is what the insertSQL would write
// out to disk.

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/telemetry"
)

func TestApplyAutoRouteFields_PopulatesPromotedColumnsFromJSON(t *testing.T) {
	c := &RequestLogContext{
		IsAutoRequest: true,
		TaskType:      "code",
		AutoProfile:   "smart",
	}
	// Build the same wire JSON that decisionToWire produces
	wire := autoRouteDecision{
		TaskType:    "code",
		Confidence:  0.92,
		ChosenModel: "claude-3-5-sonnet",
	}
	c.AutoDecision, _ = json.Marshal(wire)
	c.AutoConfidence = 0.92

	entry := &telemetry.RequestLogEntry{}
	applyAutoRouteFields(entry, c)

	// Verify the 4 promoted columns are set
	if entry.TaskTypeChosen == nil || *entry.TaskTypeChosen != "code" {
		t.Errorf("TaskTypeChosen = %v, want 'code'", entry.TaskTypeChosen)
	}
	if entry.ConfidenceNum == nil || *entry.ConfidenceNum != 0.92 {
		t.Errorf("ConfidenceNum = %v, want 0.92", entry.ConfidenceNum)
	}
	if entry.ModelChosen == nil || *entry.ModelChosen != "claude-3-5-sonnet" {
		t.Errorf("ModelChosen = %v, want 'claude-3-5-sonnet'", entry.ModelChosen)
	}
}

func TestApplyAutoRouteFields_SkipsWhenNotAutoRequest(t *testing.T) {
	c := &RequestLogContext{
		IsAutoRequest: false,
		TaskType:      "chat",
	}
	wire := autoRouteDecision{TaskType: "should-not-be-picked", ChosenModel: "x"}
	c.AutoDecision, _ = json.Marshal(wire)

	entry := &telemetry.RequestLogEntry{}
	applyAutoRouteFields(entry, c)

	// When IsAutoRequest is false, none of the auto fields are set
	if entry.IsAutoRequest != nil {
		t.Error("IsAutoRequest should be nil when c.IsAutoRequest=false")
	}
	if entry.TaskTypeChosen != nil {
		t.Errorf("TaskTypeChosen = %v, should be nil for non-auto", entry.TaskTypeChosen)
	}
	if entry.ModelChosen != nil {
		t.Errorf("ModelChosen = %v, should be nil for non-auto", entry.ModelChosen)
	}
}

func TestApplyAutoRouteFields_HandlesEmptyJSON(t *testing.T) {
	c := &RequestLogContext{
		IsAutoRequest:  true,
		TaskType:       "code",
		AutoDecision:   []byte(`{}`), // empty
		AutoConfidence: 0.5,
	}
	entry := &telemetry.RequestLogEntry{}
	applyAutoRouteFields(entry, c)

	// Empty JSON → fields stay nil (don't overwrite with empty strings)
	if entry.TaskTypeChosen != nil {
		t.Errorf("TaskTypeChosen = %v, want nil for empty JSON", entry.TaskTypeChosen)
	}
	if entry.ModelChosen != nil {
		t.Errorf("ModelChosen = %v, want nil for empty JSON", entry.ModelChosen)
	}
}

func TestApplyAutoRouteFields_HandlesMalformedJSON(t *testing.T) {
	c := &RequestLogContext{
		IsAutoRequest:  true,
		TaskType:       "code",
		AutoDecision:   []byte(`{this is not valid json`), // malformed
		AutoConfidence: 0.5,
	}
	entry := &telemetry.RequestLogEntry{}
	// Should NOT panic; should leave the 4 columns nil but still
	// set IsAutoRequest + AutoDecision string.
	applyAutoRouteFields(entry, c)

	if entry.IsAutoRequest == nil || !*entry.IsAutoRequest {
		t.Error("IsAutoRequest should still be set even with malformed JSON")
	}
	if entry.TaskTypeChosen != nil {
		t.Errorf("TaskTypeChosen = %v, want nil for malformed JSON", entry.TaskTypeChosen)
	}
}

func TestApplyAutoRouteFields_HandlesZeroConfidence(t *testing.T) {
	c := &RequestLogContext{
		IsAutoRequest: true,
		TaskType:      "code",
	}
	wire := autoRouteDecision{
		TaskType:    "code",
		Confidence:  0, // zero
		ChosenModel: "claude-3-5-sonnet",
	}
	c.AutoDecision, _ = json.Marshal(wire)

	entry := &telemetry.RequestLogEntry{}
	applyAutoRouteFields(entry, c)

	// Zero confidence is not positive → ConfidenceNum stays nil
	if entry.ConfidenceNum != nil {
		t.Errorf("ConfidenceNum = %v, want nil for zero confidence", *entry.ConfidenceNum)
	}
	// Other fields still set
	if entry.TaskTypeChosen == nil || *entry.TaskTypeChosen != "code" {
		t.Errorf("TaskTypeChosen = %v, want 'code'", entry.TaskTypeChosen)
	}
	if entry.ModelChosen == nil || *entry.ModelChosen != "claude-3-5-sonnet" {
		t.Errorf("ModelChosen = %v, want 'claude-3-5-sonnet'", entry.ModelChosen)
	}
}
