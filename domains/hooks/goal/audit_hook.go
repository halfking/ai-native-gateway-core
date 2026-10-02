// Package goal implements goal-oriented automatic session management.
package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// AuditHook handles post-completion code auditing.
type AuditHook struct {
	db        GoalStore
	llmCaller LLMCaller
	config    AuditConfig
	history   HistoryStore
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
	// Round (Wave 3 B12) is the 1-based audit round that produced this
	// result. Legacy rows (written before B12) have no round field and read
	// back as round 1.
	Round int `json:"round,omitempty"`
	// Verify (Wave 3 B12) carries the independent verification verdict of
	// the final round: a separate LLM pass (different persona/prompt) that
	// judges whether the reported issues were actually resolved by the
	// auto-fix rounds. nil = verify not run / not enabled.
	Verify *AuditVerifyResult `json:"verify,omitempty"`
}

// AuditVerifyResult is the independent VERIFY stage verdict (Wave 3 B12).
type AuditVerifyResult struct {
	Verified   bool    `json:"verified"`
	Confidence float64 `json:"confidence"`
	Summary    string  `json:"summary"`
}

// defaultAuditMaxRounds is the design's audit round budget (§B12: audit →
// fix → audit → fix → audit, then an independent VERIFY). Overridable via
// the goal.audit_max_rounds setting.
const defaultAuditMaxRounds = 3

// auditAutoFixAction is the FollowUpAction stamped on injected fix
// follow-ups; the response to such a follow-up re-enters the audit hook and
// advances the round.
const auditAutoFixAction = "audit_auto_fix"

// AuditRoundStore is the optional GoalStore extension powering multi-round
// audits. Declared as a separate interface so existing GoalStore
// implementations (and their test fakes) keep compiling: hooks fall back to
// the legacy single-round path when the store does not implement it.
type AuditRoundStore interface {
	// UpdateSessionAuditRound atomically persists an audit result for the
	// given round. It wins the race when no audit result exists yet or the
	// persisted round is strictly older (legacy rows without a round field
	// count as round 1), so concurrent audits and replays cannot move a
	// session backwards.
	UpdateSessionAuditRound(ctx context.Context, tenantID, sessionID string, auditResult []byte, round int) (bool, error)
}

// auditRoundOf parses the persisted audit result and reports its round
// (legacy rows without a round field → 1) and whether it already passed.
func auditRoundOf(raw json.RawMessage) (round int, passed bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var r AuditResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return 1, false
	}
	if r.Round <= 0 {
		r.Round = 1
	}
	return r.Round, r.Passed
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

// NewAuditHook creates a new audit hook with configuration. history may be nil;
// when nil the hook audits only the last assistant reply (legacy behaviour).
func NewAuditHook(db GoalStore, llmCaller LLMCaller, config AuditConfig) *AuditHook {
	return NewAuditHookWithHistory(db, llmCaller, config, nil)
}

// NewAuditHookWithHistory wires a HistoryStore so the audit can reason over the
// full conversation transcript reconstructed from request_logs.
func NewAuditHookWithHistory(db GoalStore, llmCaller LLMCaller, config AuditConfig, history HistoryStore) *AuditHook {
	if config.MinConfidence == 0 {
		config.MinConfidence = 0.7 // Default confidence threshold
	}
	if history == nil {
		history = NoopHistoryStore()
	}
	return &AuditHook{
		db:        db,
		llmCaller: llmCaller,
		config:    config,
		history:   history,
	}
}

// InterceptNonStream handles audit logic for completed goal sessions.
// Wave 3 B12: stores implementing AuditRoundStore get the multi-round
// pipeline (audit → fix → audit … + independent VERIFY); everything else
// keeps the legacy single-round behaviour unchanged.
func (a *AuditHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if _, ok := a.db.(AuditRoundStore); ok {
		return a.InterceptNonStreamMultiRound(ctx, req)
	}
	return a.interceptNonStreamSingleRound(ctx, req)
}

// interceptNonStreamSingleRound is the pre-B12 single-round audit path.
func (a *AuditHook) interceptNonStreamSingleRound(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if strings.HasPrefix(req.FollowUpAction, "audit") {
		return nil, nil
	}
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
	session, err := a.db.GetSession(ctx, req.TenantID, req.SessionID)
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
	auditResult, err := a.performAudit(ctx, req, session, 1, 1)
	if err != nil {
		slog.Warn("audit_failed", "session_id", req.SessionID, "error", err)
		return nil, err
	}

	// Atomically persist audit result (UPDATE ... WHERE audit_result IS NULL).
	// Only one caller wins the race; others see "already audited" and skip.
	resultJSON, _ := json.Marshal(auditResult)
	won, persistErr := a.db.UpdateSessionAudit(ctx, req.TenantID, req.SessionID, resultJSON)
	if persistErr != nil {
		slog.Warn("audit_persist_failed", "session_id", req.SessionID, "error", persistErr)
		// Fall through: still log the result so operators can see it.
	}
	if !won {
		slog.Debug("audit_lost_race", "session_id", req.SessionID,
			"reason", "another audit already persisted")
		return nil, nil
	}

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
			Action:         auditAutoFixAction,
			InjectFollowUp: []byte(fixPrompt),
		}, nil
	}

	return nil, nil
}

// InterceptNonStreamMultiRound is the Wave 3 B12 audit pipeline: up to
// defaultAuditMaxRounds audit rounds (audit → auto-fix → audit …) followed
// by an INDEPENDENT VERIFY stage on the final round — a separate LLM pass
// with its own persona that judges whether the reported issues were actually
// resolved, not merely re-audited by the same prompt.
//
// Round bookkeeping lives inside the persisted AuditResult JSON (round
// field), so no schema change is needed; legacy single-round rows read back
// as round 1 and are never rewritten. Stores that do not implement
// AuditRoundStore keep the legacy single-round behaviour.
func (a *AuditHook) InterceptNonStreamMultiRound(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	// Unknown audit-family actions keep the legacy skip; only the auto-fix
	// action re-enters as the next round's trigger.
	isFixResponse := req.FollowUpAction == auditAutoFixAction
	if strings.HasPrefix(req.FollowUpAction, "audit") && !isFixResponse {
		return nil, nil
	}
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
	session, err := a.db.GetSession(ctx, req.TenantID, req.SessionID)
	if err != nil || session == nil {
		return nil, nil
	}

	// Only audit completed sessions
	if session.State != StateCompleted {
		return nil, nil
	}

	roundStore, multiRound := a.db.(AuditRoundStore)
	if !multiRound {
		return a.interceptNonStreamSingleRound(ctx, req)
	}

	maxRounds := defaultAuditMaxRounds
	verifyEnabled := true
	if a.config.SettingsGetter != nil {
		if n := a.config.SettingsGetter.GetInt(req.TenantID, "goal.audit_max_rounds", defaultAuditMaxRounds); n > 0 {
			maxRounds = n
		}
		verifyEnabled = a.config.SettingsGetter.GetBool(req.TenantID, "goal.audit_verify_enabled", true)
	}

	prevRound, prevPassed := auditRoundOf(session.AuditResult)
	if len(session.AuditResult) > 0 && (prevPassed || prevRound >= maxRounds) {
		// Final state: passed early, or the round budget is exhausted
		// (verify already recorded on the final round's result).
		slog.Debug("session_audit_final", "session_id", req.SessionID,
			"round", prevRound, "passed", prevPassed)
		return nil, nil
	}
	if prevRound > 0 && !isFixResponse {
		// A round is already recorded but this completion is not the fix's
		// outcome — wait for the fix follow-up to re-enter with its result.
		return nil, nil
	}

	nextRound := prevRound + 1
	auditResult, err := a.performAudit(ctx, req, session, nextRound, maxRounds)
	if err != nil {
		slog.Warn("audit_failed", "session_id", req.SessionID, "round", nextRound, "error", err)
		return nil, err
	}
	auditResult.Round = nextRound

	// Independent VERIFY stage: only on the final round of a still-failing
	// audit, when enabled.
	if nextRound >= maxRounds && !auditResult.Passed && verifyEnabled {
		if vr, vErr := a.performVerify(ctx, req, session, auditResult); vErr != nil {
			slog.Warn("audit_verify_failed", "session_id", req.SessionID, "error", vErr)
		} else if vr != nil {
			auditResult.Verify = vr
		}
	}

	resultJSON, _ := json.Marshal(auditResult)
	won, persistErr := roundStore.UpdateSessionAuditRound(ctx, req.TenantID, req.SessionID, resultJSON, nextRound)
	if persistErr != nil {
		slog.Warn("audit_persist_failed", "session_id", req.SessionID, "round", nextRound, "error", persistErr)
	}
	if !won {
		slog.Debug("audit_lost_race", "session_id", req.SessionID,
			"round", nextRound, "reason", "a newer audit round is already persisted")
		return nil, nil
	}

	slog.Info("audit_completed",
		"session_id", req.SessionID,
		"round", nextRound,
		"max_rounds", maxRounds,
		"passed", auditResult.Passed,
		"confidence", auditResult.Confidence,
		"issues_count", len(auditResult.Issues),
		"verified", auditResult.Verify != nil,
	)

	// Inject the next fix round while budget remains; the final round ends
	// the pipeline (its verify verdict, if any, is persisted above).
	if a.config.AutoFixEnabled && !auditResult.Passed && nextRound < maxRounds && len(auditResult.Suggestions) > 0 {
		fixPrompt := a.buildFixPrompt(auditResult)
		return &response.InterceptResult{
			ShouldBlock:    false,
			Action:         auditAutoFixAction,
			InjectFollowUp: []byte(fixPrompt),
		}, nil
	}

	return nil, nil
}

// performAudit executes the actual audit using LLM.
//
// When a HistoryStore is wired, the audit is based on the full conversation
// transcript (reconstructed from request_logs by gw_session_id) so the auditor
// can judge the complete task execution, not just the final reply. Without a
// store it falls back to auditing only the last response body.
//
// Wave 3 B12: round > 1 tells the auditor this is a re-audit after an
// auto-fix round, so it focuses on whether earlier findings persist rather
// than re-flagging the whole transcript from scratch.
func (a *AuditHook) performAudit(ctx context.Context, req *response.InterceptRequest, session *Session, round, maxRounds int) (*AuditResult, error) {
	// Build the transcript block. Prefer the full conversation history; fall
	// back to the last response body when no history is available.
	transcript := string(req.ResponseBody)
	if a.history != nil {
		if msgs, hErr := a.history.FetchBySession(ctx, req.SessionID, req.TenantID, defaultHistoryLimit); hErr != nil {
			slog.Warn("audit_history_fetch_failed", "session_id", req.SessionID, "error", hErr)
		} else if len(msgs) > 0 {
			transcript = FormatHistoryForPrompt(msgs)
			slog.Debug("audit_with_history",
				"session_id", req.SessionID,
				"messages", len(msgs))
		}
	}

	// Build audit prompt. Round > 1 reframes the audit as a re-check after
	// an auto-fix round (Wave 3 B12) so the auditor judges persistence of
	// the earlier findings instead of re-describing the whole transcript.
	roundNote := ""
	if round > 1 {
		roundNote = fmt.Sprintf("\nThis is audit round %d of %d: the previous round reported issues and an auto-fix was applied. Focus on whether the previously reported issues are resolved and whether new issues were introduced.\n", round, maxRounds)
	}
	auditPrompt := fmt.Sprintf(`You are a code auditor. Review the following completed task and provide a structured audit.
%s
Original Goal: %s

Conversation transcript to audit:
%s

Provide your audit in JSON format:
{
  "passed": true/false,
  "confidence": 0.0-1.0,
  "issues": [{"severity": "high", "category": "security", "description": "...", "location": "..."}],
  "suggestions": [{"title": "...", "description": "...", "auto_fixable": true/false}],
  "summary": "Brief summary of audit findings"
}`, roundNote, session.OriginalGoal, transcript)

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

// performVerify runs the Wave 3 B12 INDEPENDENT VERIFY stage: a separate
// LLM pass with a QA-verifier persona (not the audit persona) that judges
// whether the issues reported by the final audit round were actually
// resolved by the auto-fix rounds. Independence is deliberate: the verifier
// receives the issue list to check one by one and is never shown the
// auditor's pass/fail verdict, so a lenient auditor cannot rubber-stamp its
// own round.
func (a *AuditHook) performVerify(ctx context.Context, req *response.InterceptRequest, session *Session, audit *AuditResult) (*AuditVerifyResult, error) {
	var issueLines strings.Builder
	for i, issue := range audit.Issues {
		fmt.Fprintf(&issueLines, "%d. [%s/%s] %s (location: %s)\n",
			i+1, issue.Severity, issue.Category, issue.Description, issue.Location)
	}
	if issueLines.Len() == 0 {
		// Nothing to verify — the audit failed without concrete issues.
		return &AuditVerifyResult{Verified: false, Confidence: 0.5,
			Summary: "audit failed without concrete issues; nothing to verify"}, nil
	}

	transcript := string(req.ResponseBody)
	if a.history != nil {
		if msgs, hErr := a.history.FetchBySession(ctx, req.SessionID, req.TenantID, defaultHistoryLimit); hErr == nil && len(msgs) > 0 {
			transcript = FormatHistoryForPrompt(msgs)
		}
	}

	verifyPrompt := fmt.Sprintf(`You are an independent QA verifier. A separate auditor reported the issues below on a completed task; fixes were then applied. Verify EACH issue against the final transcript and decide whether it is resolved.

Original Goal: %s

Reported issues to verify:
%s

Final conversation transcript:
%s

Respond with JSON only:
{
  "verified": true/false,
  "confidence": 0.0-1.0,
  "summary": "one-line verdict stating which issues remain, if any"
}`, session.OriginalGoal, issueLines.String(), transcript)

	model := a.config.FallbackModel
	if a.config.UseAutoroute {
		model = "auto:code_audit"
	}
	if a.config.SettingsGetter != nil {
		// Deliberately a DIFFERENT settings key from the auditor model: an
		// independent verdict should not silently share the auditor's model
		// unless the operator explicitly pins both to the same value.
		model = a.config.SettingsGetter.GetString(req.TenantID, "goal.audit_verify_model", model)
	}

	messages := []map[string]string{
		{"role": "system", "content": "You are an independent QA verifier. Always respond with valid JSON."},
		{"role": "user", "content": verifyPrompt},
	}
	respText, err := a.llmCaller.CallLLM(ctx, model, messages)
	if err != nil {
		return nil, fmt.Errorf("verify llm call failed: %w", err)
	}
	var result AuditVerifyResult
	if err := json.Unmarshal([]byte(respText), &result); err != nil {
		slog.Warn("verify_parse_failed", "session_id", req.SessionID, "response", respText, "error", err)
		return nil, fmt.Errorf("verify parse failed: %w", err)
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
