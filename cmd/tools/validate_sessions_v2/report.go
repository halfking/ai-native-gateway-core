package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SessionReport represents a complete validation report for one session
type SessionReport struct {
	SessionID        string                `json:"session_id"`
	TenantID         string                `json:"tenant_id"`
	ValidationTime   time.Time             `json:"validation_time"`
	Status           string                `json:"status"` // "ok" | "warning" | "error"
	Summary          ReportSummary         `json:"summary"`
	Differences      []ValidationCheckV2   `json:"differences,omitempty"`
	IncrementalValid IncrementalValidation `json:"incremental_validation"`
}

// ReportSummary contains aggregate counts for a session
type ReportSummary struct {
	V1Turns  int     `json:"v1_turns"`
	V2Turns  int     `json:"v2_turns"`
	V1Tokens int64   `json:"v1_tokens"`
	V2Tokens int64   `json:"v2_tokens"`
	V1Cost   float64 `json:"v1_cost"`
	V2Cost   float64 `json:"v2_cost"`
}

// IncrementalValidation contains delta reconstruction results
type IncrementalValidation struct {
	Status         string                 `json:"status"` // "ok" | "warning" | "error"
	TurnsValidated int                    `json:"turns_validated"`
	Mismatches     []ReconstructionResult `json:"mismatches,omitempty"`
}

// BatchReport represents a batch validation report
type BatchReport struct {
	TenantID       string          `json:"tenant_id"`
	StartDate      time.Time       `json:"start_date,omitempty"`
	EndDate        time.Time       `json:"end_date,omitempty"`
	ValidationTime time.Time       `json:"validation_time"`
	SettleWindow   string          `json:"settle_window,omitempty"`
	Summary        BatchSummary    `json:"summary"`
	Sessions       []SessionReport `json:"sessions"`
}

// BatchSummary contains aggregate statistics for batch validation
type BatchSummary struct {
	SessionsChecked int `json:"sessions_checked"`
	SessionsOK      int `json:"sessions_ok"`
	SessionsWarning int `json:"sessions_warning"`
	SessionsError   int `json:"sessions_error"`
}

// ReportGenerator generates validation reports in various formats
type ReportGenerator struct{}

// NewReportGenerator creates a new report generator
func NewReportGenerator() *ReportGenerator {
	return &ReportGenerator{}
}

// GenerateSessionReport creates a session report from validation results
func (g *ReportGenerator) GenerateSessionReport(
	tenantID, sessionID string,
	v1Turns []V1Turn,
	v2Turns []V2Turn,
	checks []ValidationCheckV2,
	reconResults []ReconstructionResult,
) *SessionReport {
	report := &SessionReport{
		SessionID:        sessionID,
		TenantID:         tenantID,
		ValidationTime:   time.Now(),
		Summary:          g.buildSummary(v1Turns, v2Turns),
		Differences:      g.filterFailedChecks(checks),
		IncrementalValid: g.buildIncrementalValidation(reconResults),
	}

	// Determine overall status
	report.Status = g.determineStatus(checks, reconResults)

	return report
}

// buildSummary creates summary from V1/V2 turns
func (g *ReportGenerator) buildSummary(v1Turns []V1Turn, v2Turns []V2Turn) ReportSummary {
	summary := ReportSummary{
		V1Turns: len(v1Turns),
		V2Turns: len(v2Turns),
	}

	// Sum V1 tokens and cost
	for _, turn := range v1Turns {
		var usage map[string]interface{}
		if err := json.Unmarshal(turn.Usage, &usage); err == nil {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				summary.V1Tokens += int64(prompt)
			}
			if completion, ok := usage["completion_tokens"].(float64); ok {
				summary.V1Tokens += int64(completion)
			}
		}
		summary.V1Cost += turn.CostUSD
	}

	// Sum V2 tokens and cost
	for _, turn := range v2Turns {
		summary.V2Tokens += int64(turn.PromptTokens + turn.CompletionTokens)
		summary.V2Cost += turn.CostUSD
	}

	return summary
}

// filterFailedChecks returns only checks that didn't pass
func (g *ReportGenerator) filterFailedChecks(checks []ValidationCheckV2) []ValidationCheckV2 {
	var failed []ValidationCheckV2
	for _, check := range checks {
		if !check.Passed {
			failed = append(failed, check)
		}
	}
	return failed
}

// buildIncrementalValidation creates incremental validation section
func (g *ReportGenerator) buildIncrementalValidation(results []ReconstructionResult) IncrementalValidation {
	iv := IncrementalValidation{
		Status:         "ok",
		TurnsValidated: len(results),
	}

	for _, result := range results {
		if result.Status == "error" {
			iv.Status = "error"
			iv.Mismatches = append(iv.Mismatches, result)
		} else if result.Status == "warning" && iv.Status != "error" {
			iv.Status = "warning"
			iv.Mismatches = append(iv.Mismatches, result)
		}
	}

	return iv
}

// determineStatus determines overall session status
func (g *ReportGenerator) determineStatus(checks []ValidationCheckV2, reconResults []ReconstructionResult) string {
	hasError := false
	hasWarning := false

	// Check validation checks
	for _, check := range checks {
		if !check.Passed {
			if check.Severity == "error" {
				hasError = true
			} else if check.Severity == "warning" {
				hasWarning = true
			}
		}
	}

	// Check reconstruction results
	for _, result := range reconResults {
		if result.Status == "error" {
			hasError = true
		} else if result.Status == "warning" {
			hasWarning = true
		}
	}

	if hasError {
		return "error"
	}
	if hasWarning {
		return "warning"
	}
	return "ok"
}

// GenerateBatchReport creates a batch report from multiple session reports
func (g *ReportGenerator) GenerateBatchReport(
	tenantID string,
	startDate, endDate time.Time,
	settleWindow time.Duration,
	sessions []SessionReport,
) *BatchReport {
	report := &BatchReport{
		TenantID:       tenantID,
		StartDate:      startDate,
		EndDate:        endDate,
		ValidationTime: time.Now(),
		Sessions:       sessions,
	}

	if settleWindow > 0 {
		report.SettleWindow = settleWindow.String()
	}

	// Calculate summary
	report.Summary.SessionsChecked = len(sessions)
	for _, session := range sessions {
		switch session.Status {
		case "ok":
			report.Summary.SessionsOK++
		case "warning":
			report.Summary.SessionsWarning++
		case "error":
			report.Summary.SessionsError++
		}
	}

	return report
}

// FormatJSON formats a report as JSON
func (g *ReportGenerator) FormatJSON(report interface{}) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// FormatText formats a session report as human-readable text
func (g *ReportGenerator) FormatTextSession(report *SessionReport) string {
	var buf strings.Builder

	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString("SESSION VALIDATION REPORT\n")
	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString(fmt.Sprintf("Session:    %s\n", report.SessionID))
	buf.WriteString(fmt.Sprintf("Tenant:     %s\n", report.TenantID))
	buf.WriteString(fmt.Sprintf("Validated:  %s\n", report.ValidationTime.Format("2006-01-02 15:04:05")))
	buf.WriteString(fmt.Sprintf("Status:     %s\n", strings.ToUpper(report.Status)))
	buf.WriteString(strings.Repeat("-", 80) + "\n\n")

	// Summary
	buf.WriteString("Summary:\n")
	buf.WriteString(fmt.Sprintf("  V1: %d turns, %d tokens, $%.6f\n",
		report.Summary.V1Turns, report.Summary.V1Tokens, report.Summary.V1Cost))
	buf.WriteString(fmt.Sprintf("  V2: %d turns, %d tokens, $%.6f\n\n",
		report.Summary.V2Turns, report.Summary.V2Tokens, report.Summary.V2Cost))

	// Differences
	if len(report.Differences) == 0 {
		buf.WriteString("✓ All validation checks passed\n\n")
	} else {
		buf.WriteString(fmt.Sprintf("Differences (%d):\n", len(report.Differences)))
		for i, diff := range report.Differences {
			severity := "✗"
			if diff.Severity == "warning" {
				severity = "⚠"
			}
			buf.WriteString(fmt.Sprintf("  %s [%d] %s (%s)\n",
				severity, i+1, diff.Name, strings.ToUpper(diff.Severity)))
			buf.WriteString(fmt.Sprintf("      %s\n", diff.Description))
			if len(diff.Details) > 0 && len(diff.Details) <= 3 {
				for _, detail := range diff.Details {
					buf.WriteString(fmt.Sprintf("      - %s\n", detail))
				}
			}
		}
		buf.WriteString("\n")
	}

	// Incremental validation
	buf.WriteString(fmt.Sprintf("Incremental Validation: %s (%d turns)\n",
		strings.ToUpper(report.IncrementalValid.Status),
		report.IncrementalValid.TurnsValidated))
	if len(report.IncrementalValid.Mismatches) > 0 {
		buf.WriteString(fmt.Sprintf("  %d mismatch(es):\n", len(report.IncrementalValid.Mismatches)))
		for _, mm := range report.IncrementalValid.Mismatches {
			if len(mm.Description) > 0 {
				buf.WriteString(fmt.Sprintf("    Turn %d: %s\n", mm.TurnNo, mm.Description))
			}
		}
	}

	buf.WriteString(strings.Repeat("=", 80) + "\n")

	return buf.String()
}

// FormatTextBatch formats a batch report as human-readable text
func (g *ReportGenerator) FormatTextBatch(report *BatchReport) string {
	var buf strings.Builder

	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString("BATCH VALIDATION REPORT\n")
	buf.WriteString(strings.Repeat("=", 80) + "\n")
	buf.WriteString(fmt.Sprintf("Tenant:        %s\n", report.TenantID))
	if !report.StartDate.IsZero() {
		buf.WriteString(fmt.Sprintf("Date Range:    %s to %s\n",
			report.StartDate.Format("2006-01-02"),
			report.EndDate.Format("2006-01-02")))
	}
	if report.SettleWindow != "" {
		buf.WriteString(fmt.Sprintf("Settle Window: %s\n", report.SettleWindow))
	}
	buf.WriteString(fmt.Sprintf("Validated:     %s\n", report.ValidationTime.Format("2006-01-02 15:04:05")))
	buf.WriteString(strings.Repeat("-", 80) + "\n\n")

	// Summary
	buf.WriteString("Summary:\n")
	buf.WriteString(fmt.Sprintf("  Sessions Checked: %d\n", report.Summary.SessionsChecked))
	buf.WriteString(fmt.Sprintf("  ✓ OK:            %d\n", report.Summary.SessionsOK))
	buf.WriteString(fmt.Sprintf("  ⚠ Warnings:      %d\n", report.Summary.SessionsWarning))
	buf.WriteString(fmt.Sprintf("  ✗ Errors:        %d\n\n", report.Summary.SessionsError))

	// Session details (show errors and warnings only)
	errorSessions := []SessionReport{}
	warningSessions := []SessionReport{}
	for _, session := range report.Sessions {
		if session.Status == "error" {
			errorSessions = append(errorSessions, session)
		} else if session.Status == "warning" {
			warningSessions = append(warningSessions, session)
		}
	}

	if len(errorSessions) > 0 {
		buf.WriteString(fmt.Sprintf("Sessions with ERRORS (%d):\n", len(errorSessions)))
		for _, session := range errorSessions {
			buf.WriteString(fmt.Sprintf("  %s: %d difference(s)\n",
				session.SessionID, len(session.Differences)))
		}
		buf.WriteString("\n")
	}

	if len(warningSessions) > 0 {
		buf.WriteString(fmt.Sprintf("Sessions with WARNINGS (%d):\n", len(warningSessions)))
		for _, session := range warningSessions {
			buf.WriteString(fmt.Sprintf("  %s: %d difference(s)\n",
				session.SessionID, len(session.Differences)))
		}
		buf.WriteString("\n")
	}

	// Exit code hint
	if report.Summary.SessionsError > 0 {
		buf.WriteString("Exit Code: 1 (errors found)\n")
	} else {
		buf.WriteString("Exit Code: 0 (no errors)\n")
	}

	buf.WriteString(strings.Repeat("=", 80) + "\n")

	return buf.String()
}
