package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGenerateSessionReport_AllPass(t *testing.T) {
	v1Turns := []V1Turn{
		{Usage: json.RawMessage(`{"prompt_tokens": 100, "completion_tokens": 50}`), CostUSD: 0.05},
	}
	v2Turns := []V2Turn{
		{PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.05},
	}
	checks := []ValidationCheckV2{
		{Name: "Check 1", Passed: true, Severity: "error"},
		{Name: "Check 2", Passed: true, Severity: "warning"},
	}
	reconResults := []ReconstructionResult{
		{TurnNo: 1, Status: "ok"},
	}
	
	gen := NewReportGenerator()
	report := gen.GenerateSessionReport("tenant_1", "gw_abc", v1Turns, v2Turns, checks, reconResults)
	
	if report.Status != "ok" {
		t.Errorf("Expected status=ok, got %s", report.Status)
	}
	if len(report.Differences) != 0 {
		t.Errorf("Expected 0 differences for all-pass, got %d", len(report.Differences))
	}
	if report.Summary.V1Turns != 1 || report.Summary.V2Turns != 1 {
		t.Errorf("Expected 1 turn in each, got V1=%d, V2=%d",
			report.Summary.V1Turns, report.Summary.V2Turns)
	}
}

func TestGenerateSessionReport_WithErrors(t *testing.T) {
	v1Turns := []V1Turn{{}}
	v2Turns := []V2Turn{{}}
	checks := []ValidationCheckV2{
		{Name: "Check 1", Passed: false, Severity: "error", Description: "Failed"},
		{Name: "Check 2", Passed: true, Severity: "warning"},
	}
	reconResults := []ReconstructionResult{}
	
	gen := NewReportGenerator()
	report := gen.GenerateSessionReport("tenant_1", "gw_abc", v1Turns, v2Turns, checks, reconResults)
	
	if report.Status != "error" {
		t.Errorf("Expected status=error, got %s", report.Status)
	}
	if len(report.Differences) != 1 {
		t.Errorf("Expected 1 difference (failed check), got %d", len(report.Differences))
	}
}

func TestGenerateSessionReport_WarningsOnly(t *testing.T) {
	v1Turns := []V1Turn{{}}
	v2Turns := []V2Turn{{}}
	checks := []ValidationCheckV2{
		{Name: "Check 1", Passed: false, Severity: "warning", Description: "Minor issue"},
		{Name: "Check 2", Passed: true, Severity: "error"},
	}
	reconResults := []ReconstructionResult{}
	
	gen := NewReportGenerator()
	report := gen.GenerateSessionReport("tenant_1", "gw_abc", v1Turns, v2Turns, checks, reconResults)
	
	if report.Status != "warning" {
		t.Errorf("Expected status=warning, got %s", report.Status)
	}
}

func TestDetermineStatus_ErrorTakesPrecedence(t *testing.T) {
	checks := []ValidationCheckV2{
		{Passed: false, Severity: "error"},
		{Passed: false, Severity: "warning"},
	}
	reconResults := []ReconstructionResult{
		{Status: "warning"},
	}
	
	gen := NewReportGenerator()
	status := gen.determineStatus(checks, reconResults)
	
	if status != "error" {
		t.Errorf("Expected error to take precedence over warning, got %s", status)
	}
}

func TestGenerateBatchReport(t *testing.T) {
	sessions := []SessionReport{
		{SessionID: "s1", Status: "ok"},
		{SessionID: "s2", Status: "warning"},
		{SessionID: "s3", Status: "error"},
		{SessionID: "s4", Status: "ok"},
	}
	
	gen := NewReportGenerator()
	report := gen.GenerateBatchReport(
		"tenant_1",
		time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		10*time.Minute,
		sessions,
	)
	
	if report.Summary.SessionsChecked != 4 {
		t.Errorf("Expected 4 sessions checked, got %d", report.Summary.SessionsChecked)
	}
	if report.Summary.SessionsOK != 2 {
		t.Errorf("Expected 2 OK sessions, got %d", report.Summary.SessionsOK)
	}
	if report.Summary.SessionsWarning != 1 {
		t.Errorf("Expected 1 warning session, got %d", report.Summary.SessionsWarning)
	}
	if report.Summary.SessionsError != 1 {
		t.Errorf("Expected 1 error session, got %d", report.Summary.SessionsError)
	}
	if report.SettleWindow != "10m0s" {
		t.Errorf("Expected settle window=10m0s, got %s", report.SettleWindow)
	}
}

func TestFormatJSON(t *testing.T) {
	report := &SessionReport{
		SessionID: "gw_abc",
		TenantID:  "tenant_1",
		Status:    "ok",
		Summary: ReportSummary{
			V1Turns: 5,
			V2Turns: 5,
		},
	}
	
	gen := NewReportGenerator()
	jsonBytes, err := gen.FormatJSON(report)
	
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}
	
	// Verify it's valid JSON
	var parsed SessionReport
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Errorf("Generated JSON is not valid: %v", err)
	}
	
	if parsed.SessionID != "gw_abc" {
		t.Errorf("JSON parsing lost data: expected session_id=gw_abc, got %s", parsed.SessionID)
	}
}

func TestFormatTextSession(t *testing.T) {
	report := &SessionReport{
		SessionID:      "gw_abc",
		TenantID:       "tenant_1",
		ValidationTime: time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC),
		Status:         "error",
		Summary: ReportSummary{
			V1Turns:  10,
			V2Turns:  9,
			V1Tokens: 15000,
			V2Tokens: 15000,
			V1Cost:   0.15,
			V2Cost:   0.15,
		},
		Differences: []ValidationCheckV2{
			{Name: "Request ID Parity", Severity: "error", Description: "Missing 1 request in V2"},
		},
		IncrementalValid: IncrementalValidation{
			Status:         "ok",
			TurnsValidated: 9,
		},
	}
	
	gen := NewReportGenerator()
	text := gen.FormatTextSession(report)
	
	// Verify key content is present
	if !strings.Contains(text, "gw_abc") {
		t.Errorf("Text report missing session ID")
	}
	if !strings.Contains(text, "ERROR") {
		t.Errorf("Text report missing status")
	}
	if !strings.Contains(text, "Request ID Parity") {
		t.Errorf("Text report missing difference details")
	}
	if !strings.Contains(text, "Differences (1)") {
		t.Errorf("Text report missing difference count")
	}
}

func TestFormatTextBatch(t *testing.T) {
	report := &BatchReport{
		TenantID:       "tenant_1",
		StartDate:      time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		EndDate:        time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
		ValidationTime: time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC),
		SettleWindow:   "10m0s",
		Summary: BatchSummary{
			SessionsChecked: 100,
			SessionsOK:      95,
			SessionsWarning: 3,
			SessionsError:   2,
		},
		Sessions: []SessionReport{
			{SessionID: "err1", Status: "error", Differences: []ValidationCheckV2{{}}},
			{SessionID: "warn1", Status: "warning", Differences: []ValidationCheckV2{{}}},
		},
	}
	
	gen := NewReportGenerator()
	text := gen.FormatTextBatch(report)
	
	// Verify key content
	if !strings.Contains(text, "tenant_1") {
		t.Errorf("Batch report missing tenant ID")
	}
	if !strings.Contains(text, "Sessions Checked: 100") {
		t.Errorf("Batch report missing session count")
	}
	if !strings.Contains(text, "Exit Code: 1") {
		t.Errorf("Batch report should show exit code 1 for errors")
	}
	if !strings.Contains(text, "Sessions with ERRORS") {
		t.Errorf("Batch report missing error session section. Got:\n%s", text)
	}
}

func TestBuildSummary(t *testing.T) {
	v1Turns := []V1Turn{
		{Usage: json.RawMessage(`{"prompt_tokens": 100, "completion_tokens": 50}`), CostUSD: 0.05},
		{Usage: json.RawMessage(`{"prompt_tokens": 200, "completion_tokens": 100}`), CostUSD: 0.10},
	}
	v2Turns := []V2Turn{
		{PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.05},
		{PromptTokens: 200, CompletionTokens: 100, CostUSD: 0.10},
	}
	
	gen := NewReportGenerator()
	summary := gen.buildSummary(v1Turns, v2Turns)
	
	if summary.V1Turns != 2 || summary.V2Turns != 2 {
		t.Errorf("Expected 2 turns each, got V1=%d, V2=%d", summary.V1Turns, summary.V2Turns)
	}
	if summary.V1Tokens != 450 || summary.V2Tokens != 450 {
		t.Errorf("Expected 450 tokens each, got V1=%d, V2=%d", summary.V1Tokens, summary.V2Tokens)
	}
	if abs64(summary.V1Cost-0.15) > 0.0001 || abs64(summary.V2Cost-0.15) > 0.0001 {
		t.Errorf("Expected 0.15 cost each, got V1=%f, V2=%f", summary.V1Cost, summary.V2Cost)
	}
}
