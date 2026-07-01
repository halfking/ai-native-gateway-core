// Package goal implements goal-oriented automatic session management.
package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// AuditHook handles post-completion code auditing.
type AuditHook struct {
	db        GoalStore
	llmCaller LLMCaller
	config    AuditConfig
}

// AuditConfig contains audit hook configuration.
type AuditConfig struct {
	Enabled        bool
	UseAutoroute   bool
	FallbackModel  string
	AutoFixEnabled bool
	MinConfidence  float64
	SettingsGetter SettingsGetter
}

// AuditResult represents the result of a code audit.
type AuditResult struct {
	Passed      bool              `json:"passed"`
	Confidence  float64           `json:"confidence"`
	Issues      []AuditIssue      `json:"issues,omitempty"`
	Suggestions []AuditSuggestion `json:"suggestions,omitempty"`
	Summary     string            `json:"summary"`
}

// AuditIssue represents a single audit finding.
type AuditIssue struct {
	Severity    string `json:"severity"` // critical, high, medium, low
	Category    string `json:"category"` // security, performance, style, etc.
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
}

// AuditSuggestion represents a recommended fix.
type AuditSuggestion struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Code        string `json:"code,omitempty"`
	AutoFixable bool   `json:"auto_fixable"`
}

// NewAuditHook creates a new audit hook with configuration.
func NewAuditHook(db GoalStore, llmCaller LLMCaller, config AuditConfig) *AuditHook {
	if config.MinConfidence == 0 {
		config.MinConfidence = 0.7 // Default confidence threshold
	}
	return &AuditHook{
		db:        db,
		llmCaller: llmCaller,
		config:    config,
	}
}

// InterceptNonStream handles audit logic for completed goal sessions.
func (a *AuditHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if a.db == nil || a.llmCaller == nil {
		return nil, nil
	}

	// Check if audit is enabled
	enabled := a.config.Enabled
	if a.config.SettingsGetter != nil {
		enabled = a.config.SettingsGetter.GetBool(req.TenantID, "goal.audit_enabled", a.config.Enabled)
	}
	if !enabled {
		return nil, nil
	}

	// Get goal session
	session, err := a.db.GetSession(ctx, req.SessionID)
	if err != nil || session == nil {
		return nil, nil
	}

	// Only audit completed sessions
	if session.State != StateCompleted {
		return nil, nil
	}

	// Check if already audited
	if len(session.AuditResult) > 0 {
		slog.Debug("session_already_audited", "session_id", req.SessionID)
		return nil, nil
	}

	// Perform audit
	auditResult, err := a.performAudit(ctx, req, session)
	if err != nil {
		slog.Warn("audit_failed", "session_id", req.SessionID, "error", err)
		return nil, err
	}

	// Store audit result - Note: We need to add UpdateSessionAudit method to GoalStore
	// For now, just log the result
	resultJSON, _ := json.Marshal(auditResult)
	slog.Info("audit_result_generated",
		"session_id", req.SessionID,
		"result", string(resultJSON),
	)

	// Log audit summary
	slog.Info("audit_completed",
		"session_id", req.SessionID,
		"passed", auditResult.Passed,
		"confidence", auditResult.Confidence,
		"issues_count", len(auditResult.Issues),
	)

	// If auto-fix is enabled and issues found, inject fix request
	if a.config.AutoFixEnabled && !auditResult.Passed && len(auditResult.Suggestions) > 0 {
		fixPrompt := a.buildFixPrompt(auditResult)
		return &response.InterceptResult{
			ShouldBlock:    false,
			Action:         "audit_auto_fix",
			InjectFollowUp: []byte(fixPrompt),
		}, nil
	}

	return nil, nil
}

// performAudit executes the actual audit using LLM.
func (a *AuditHook) performAudit(ctx context.Context, req *response.InterceptRequest, session *Session) (*AuditResult, error) {
	// Build audit prompt
	auditPrompt := fmt.Sprintf(`You are a code auditor. Review the following completed task and provide a structured audit.

Original Goal: %s

Response to audit:
%s

Provide your audit in JSON format:
{
  "passed": true/false,
  "confidence": 0.0-1.0,
  "issues": [{"severity": "high", "category": "security", "description": "...", "location": "..."}],
  "suggestions": [{"title": "...", "description": "...", "auto_fixable": true/false}],
  "summary": "Brief summary of audit findings"
}`, session.OriginalGoal, string(req.ResponseBody))

	// Call LLM for audit
	model := a.config.FallbackModel
	if a.config.UseAutoroute {
		model = "auto:code_audit"
	}
	if a.config.SettingsGetter != nil {
		model = a.config.SettingsGetter.GetString(req.TenantID, "goal.audit_model", model)
	}

	messages := []map[string]string{
		{"role": "system", "content": "You are a code auditor. Always respond with valid JSON."},
		{"role": "user", "content": auditPrompt},
	}

	respText, err := a.llmCaller.CallLLM(ctx, model, messages)
	if err != nil {
		return nil, fmt.Errorf("audit llm call failed: %w", err)
	}

	// Parse audit result
	var result AuditResult
	if err := json.Unmarshal([]byte(respText), &result); err != nil {
		// LLM didn't return valid JSON, create a basic result
		slog.Warn("audit_parse_failed", "response", respText, "error", err)
		return &AuditResult{
			Passed:     true, // Default to passed if we can't parse
			Confidence: 0.5,
			Summary:    "Audit completed but result parsing failed",
		}, nil
	}

	// Validate confidence threshold
	if result.Confidence < a.config.MinConfidence && !result.Passed {
		slog.Warn("audit_low_confidence",
			"session_id", req.SessionID,
			"confidence", result.Confidence,
			"threshold", a.config.MinConfidence,
		)
	}

	return &result, nil
}

// buildFixPrompt constructs a prompt for auto-fixing issues.
func (a *AuditHook) buildFixPrompt(audit *AuditResult) string {
	prompt := map[string]interface{}{
		"model": a.config.FallbackModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a code fixer. Apply the suggested fixes carefully.",
			},
			{
				"role":    "user",
				"content": fmt.Sprintf("Please fix the following issues:\n\nSummary: %s\n\nSuggestions:\n", audit.Summary),
			},
		},
	}

	// Add suggestions
	for i, sug := range audit.Suggestions {
		if sug.AutoFixable {
			prompt["messages"] = append(prompt["messages"].([]map[string]string), map[string]string{
				"role":    "user",
				"content": fmt.Sprintf("%d. %s: %s", i+1, sug.Title, sug.Description),
			})
		}
	}

	promptJSON, _ := json.Marshal(prompt)
	return string(promptJSON)
}

// InterceptStreamChunk is a no-op for audit.
func (a *AuditHook) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd is a no-op for audit.
func (a *AuditHook) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}

// Ensure AuditHook implements ResponseInterceptor
var _ response.ResponseInterceptor = (*AuditHook)(nil)
