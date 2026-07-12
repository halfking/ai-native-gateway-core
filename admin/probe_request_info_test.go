// admin/probe_request_info_test.go — unit tests for extractProbeInfo.
//
// These tests lock in the contract between bg.ActiveProbeEmitter
// (which writes probe rows) and admin.extractProbeInfo (which surfaces
// them as LiveRequest.IsProbe / ProbeOrigin / ProbeAttempt).
package admin

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

func ptrStr(s string) *string { return &s }

func TestExtractProbeInfo_NilEntry(t *testing.T) {
	got := extractProbeInfo(nil)
	if got.IsProbe {
		t.Errorf("nil entry should not be a probe, got %+v", got)
	}
}

func TestExtractProbeInfo_NotAProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType: ptrStr("normal_request"),
	}
	got := extractProbeInfo(entry)
	if got.IsProbe {
		t.Errorf("normal_request should not be a probe, got %+v", got)
	}
}

func TestExtractProbeInfo_NilTaskType(t *testing.T) {
	entry := &telemetry.RequestLogEntry{}
	got := extractProbeInfo(entry)
	if got.IsProbe {
		t.Errorf("nil TaskType should not be a probe, got %+v", got)
	}
}

func TestExtractProbeInfo_DirectProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("probe_triggered should be detected as probe")
	}
	if got.ProbeOrigin != "direct" {
		t.Errorf("ProbeOrigin = %q, want direct", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_GatewayProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_gateway"),
	}
	got := extractProbeInfo(entry)
	if got.ProbeOrigin != "gateway" {
		t.Errorf("ProbeOrigin = %q, want gateway", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_ScheduledProbe(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_scheduled"),
	}
	got := extractProbeInfo(entry)
	if got.ProbeOrigin != "scheduled" {
		t.Errorf("ProbeOrigin = %q, want scheduled", got.ProbeOrigin)
	}
}

func TestExtractProbeInfo_AttemptFromAutoDecision(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		AutoDecision:   ptrStr(`{"probe_attempt":3,"probe_status":"failed"}`),
	}
	got := extractProbeInfo(entry)
	if got.ProbeAttempt != 3 {
		t.Errorf("ProbeAttempt = %d, want 3 (from auto_decision JSON)", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_AttemptFromQualityFlags(t *testing.T) {
	// Older rows (written before auto_decision was added) encode attempt
	// in quality_flags. extractProbeInfo must fall back to that.
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		QualityFlags:   []string{"probe", "direct", "attempt_2"},
	}
	got := extractProbeInfo(entry)
	if got.ProbeAttempt != 2 {
		t.Errorf("ProbeAttempt = %d, want 2 (from quality_flags fallback)", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_AttemptPreference(t *testing.T) {
	// When both auto_decision and quality_flags carry an attempt, the
	// auto_decision value wins (it's the typed source of truth).
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		AutoDecision:   ptrStr(`{"probe_attempt":4}`),
		QualityFlags:   []string{"probe", "direct", "attempt_1"},
	}
	got := extractProbeInfo(entry)
	if got.ProbeAttempt != 4 {
		t.Errorf("ProbeAttempt = %d, want 4 (auto_decision should win over quality_flags)", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_BadAutoDecisionJSON(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType:       ptrStr("probe_triggered"),
		TaskTypeChosen: ptrStr("probe_direct"),
		AutoDecision:   ptrStr(`{not valid json`),
	}
	got := extractProbeInfo(entry)
	// Should fall back to attempt=0 without panicking.
	if got.ProbeAttempt != 0 {
		t.Errorf("ProbeAttempt = %d, want 0 after JSON parse failure", got.ProbeAttempt)
	}
}

func TestExtractProbeInfo_DefaultOriginWhenTaskTypeChosenNil(t *testing.T) {
	entry := &telemetry.RequestLogEntry{
		TaskType: ptrStr("probe_triggered"),
		// TaskTypeChosen intentionally nil
	}
	got := extractProbeInfo(entry)
	if !got.IsProbe {
		t.Fatal("probe_triggered should still be detected as probe")
	}
	if got.ProbeOrigin != "direct" {
		t.Errorf("ProbeOrigin = %q, want direct (default)", got.ProbeOrigin)
	}
}
