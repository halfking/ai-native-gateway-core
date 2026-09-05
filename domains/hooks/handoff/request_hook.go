package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// Request contains the request-side context used to decide whether a handoff
// is needed before the provider receives the current turn.
type Request struct {
	SessionID     string
	TenantID      string
	ClientModel   string
	Body          []byte
	Protocol      string
	ContextWindow int
	TokenEstimate int
	MessageCount  int
	Explicit      bool
	// UpstreamAPIKey is populated only when the caller has established that
	// it is a direct provider credential, never a gateway authentication key.
	UpstreamAPIKey string
}

// DefaultExplicit reports whether this tenant defaults to the explicit client
// protocol. The compatibility default is transparent; clients may opt in per
// request with X-Gw-Handoff-Mode: explicit.
func (h *TriggerHook) DefaultExplicit(tenantID string) bool {
	return h.loadString(tenantID, "handoff.client_mode", "transparent") == "explicit"
}

// ResumePacket is the bounded handoff payload passed to a fresh session. It
// deliberately excludes credentials and raw authentication material.
type ResumePacket struct {
	Version         int             `json:"version"`
	PreviousSession string          `json:"previous_session_id"`
	TriggerReason   string          `json:"trigger_reason"`
	Summary         string          `json:"summary"`
	SkillName       string          `json:"skill_name"`
	GoalHandoff     *HandoffMessage `json:"goal_handoff,omitempty"`
}

// RequestResult describes a request-side handoff decision. The handler owns
// session creation because it is responsible for session ownership checks.
type RequestResult struct {
	Triggered     bool
	Explicit      bool
	Reason        string
	Body          []byte
	ResumePacket  ResumePacket
	Record        *HandoffRecord
	GoalState     *GoalState
	ReservationID string
}

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var resumeSensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]{12,}`),
	regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{12,})`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|secret|token|password|cookie)["']?\s*[:=]\s*["']?)[^\s,;"']{8,}`),
	regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]*PRIVATE KEY-----).*?(-----END [A-Z ]*PRIVATE KEY-----)`),
}

// PrepareRequest evaluates the current request without mutating the provider
// payload. Automatic handoff requires explicit client opt-in; a manual skill
// invocation is itself explicit and returns a resume packet to the client.
func (h *TriggerHook) PrepareRequest(ctx context.Context, req *Request) (*RequestResult, error) {
	if req == nil || !h.loadBool(req.TenantID, "handoff.enabled", h.config.Enabled) {
		return nil, nil
	}

	mode := TriggerMode(h.loadString(req.TenantID, "handoff.trigger_mode", string(h.config.TriggerMode)))
	if mode == "" {
		mode = TriggerModeAuto
	}
	skillName := h.loadString(req.TenantID, "handoff.skill_name", h.config.SkillName)
	if !skillNamePattern.MatchString(skillName) {
		slog.Warn("handoff_invalid_skill_name", "skill_name", skillName)
		return nil, nil
	}

	manual := hasSkillInvocation(req.Body, skillName)
	if mode == TriggerModeManual && !manual {
		return nil, nil
	}
	if mode == TriggerModeAuto && manual {
		return nil, nil
	}

	sessionTokens, sessionMessages := h.requestTotals(ctx, req)
	interceptReq := &response.InterceptRequest{
		SessionID: req.SessionID, TenantID: req.TenantID, ClientModel: req.ClientModel,
		TokensUsed: sessionTokens, ContextWindow: req.ContextWindow, MessageCount: sessionMessages,
	}
	var (
		d             *decision
		goalSignal    TriggerSignal
		reservationID string
	)
	if manual {
		d = &decision{reason: "manual_skill:" + skillName, tokensAtTrig: sessionTokens, msgCount: sessionMessages}
		goalSignal = TriggerSignal{Kind: SignalGoalDegraded, Source: "manual", Reason: d.reason, Severity: 3, ObservedAt: time.Now().UTC()}
	} else {
		if !req.Explicit {
			return nil, nil
		}
		d = h.evaluate(ctx, interceptReq, mode)
		if h.config.ContextMonitor != nil && h.config.GoalTrigger != nil {
			contextSignal := h.config.ContextMonitor.Evaluate(ContextSnapshot{
				SessionID: req.SessionID, TenantID: req.TenantID, TokensUsed: sessionTokens,
				ContextWindow: req.ContextWindow, MessageCount: sessionMessages,
				AbsoluteThreshold:   h.loadInt(req.TenantID, "handoff.absolute_threshold", h.config.AbsoluteThreshold),
				PercentageThreshold: h.loadFloat(req.TenantID, "handoff.percentage_threshold", h.config.PercentageThreshold),
			})
			if d != nil {
				if contextSignal.Kind == SignalNone {
					contextSignal = TriggerSignal{Kind: SignalContextPressure, Source: "handoff", Severity: 2, ObservedAt: time.Now().UTC()}
				}
				if contextSignal.Kind == SignalContextPressure {
					contextSignal.Reason = d.reason
				}
			}
			if !h.canPrepareRequest(ctx, req.TenantID, req.SessionID, sessionMessages) {
				return nil, nil
			}
			if id, signal, ok := h.config.GoalTrigger.Reserve(req.SessionID, contextSignal); ok {
				reservationID = id
				goalSignal = signal
				d = &decision{reason: signal.Reason, tokensAtTrig: sessionTokens, msgCount: sessionMessages}
			} else {
				d = nil
			}
		}
	}
	if d == nil || !h.canPrepareRequest(ctx, req.TenantID, req.SessionID, d.msgCount) {
		if h.config.GoalTrigger != nil {
			h.config.GoalTrigger.Abort(reservationID)
		}
		return nil, nil
	}

	engine := SummaryEngine(h.loadString(req.TenantID, "handoff.summary_engine", string(h.config.SummaryEngine)))
	summaryRequest := *req
	if manual {
		summaryRequest.Body = stripSkillInvocation(req.Body, skillName)
	}
	summary := h.buildRequestSummary(ctx, &summaryRequest, engine)
	var goalState *GoalState
	if h.config.GoalStateSerializer != nil {
		costMode := ""
		if h.config.GoalCostMode != nil {
			costMode = h.config.GoalCostMode(req.TenantID)
		}
		var err error
		goalState, err = h.config.GoalStateSerializer.Serialize(ctx, GoalStateInput{
			TenantID: req.TenantID, SessionID: req.SessionID, CostMode: costMode, TokensUsed: sessionTokens, MessageCount: sessionMessages,
		})
		if err != nil {
			slog.Warn("handoff_goal_state_serialize_failed", "session_id", req.SessionID, "error", err)
			goalState = nil
		}
	}
	packet := ResumePacket{
		Version: 1, PreviousSession: req.SessionID, TriggerReason: d.reason,
		Summary: summary, SkillName: skillName,
	}
	if goalState != nil && h.config.MessageBuilder != nil {
		if goalSignal.Kind == SignalNone {
			goalSignal = TriggerSignal{Kind: SignalContextPressure, Source: "handoff", Reason: d.reason, Severity: 2, ObservedAt: time.Now().UTC()}
		}
		message, err := h.config.MessageBuilder.Build(req.SessionID, goalSignal, goalState, summary)
		if err != nil {
			slog.Warn("handoff_goal_message_build_failed", "session_id", req.SessionID, "error", err)
		} else {
			packet.GoalHandoff = message
		}
	}
	result := &RequestResult{
		Triggered: true, Explicit: true, Reason: d.reason, ResumePacket: packet,
		GoalState: goalState, ReservationID: reservationID,
		Record: &HandoffRecord{
			SessionKey: req.SessionID, TenantID: req.TenantID, TriggerMode: string(mode), TriggerReason: d.reason,
			TokensAtTrigger: req.TokenEstimate, ContextWindow: req.ContextWindow, MessagesAtTrigger: d.msgCount,
			TokensInSession: d.tokensAtTrig, SummaryEngine: string(engine), SummaryText: summary,
			SkillName: skillName, CreatedAt: time.Now(),
		},
	}
	return result, nil
}

// CommitRequest records a successfully prepared handoff after the handler has
// created the target session. Recording failures do not block the client turn.
func (h *TriggerHook) CommitRequest(ctx context.Context, result *RequestResult, newSessionID string) {
	if result == nil || result.Record == nil || strings.TrimSpace(newSessionID) == "" {
		return
	}
	record := *result.Record
	record.NewSessionID = newSessionID
	record.HandoffPrompt = string(result.Body)
	record.DurationMs = int(time.Since(record.CreatedAt).Milliseconds())
	if h.db != nil {
		if err := h.db.RecordHandoff(ctx, &record); err != nil {
			slog.Warn("handoff_record_failed", "session_id", record.SessionKey, "error", err)
		}
	}
	level := NotifyLevel(h.loadString(record.TenantID, "handoff.notify_level", string(h.config.NotifyLevel)))
	h.notify(ctx, level, &record)
}

func (h *TriggerHook) requestTotals(ctx context.Context, req *Request) (int, int) {
	tokens, messages := req.TokenEstimate, req.MessageCount
	if h.db == nil || req.SessionID == "" {
		return tokens, messages
	}
	if value, err := h.db.GetSessionTokens(ctx, req.SessionID); err == nil && value > 0 {
		tokens = value
	}
	if value, err := h.db.GetSessionMessages(ctx, req.SessionID); err == nil && value > 0 {
		messages = value
	}
	return tokens, messages
}

func (h *TriggerHook) canPrepareRequest(ctx context.Context, tenantID, sessionID string, msgCount int) bool {
	minMsg := h.loadInt(tenantID, "handoff.min_messages", h.config.MinMessages)
	if minMsg > 0 && msgCount > 0 && msgCount < minMsg {
		return false
	}
	if h.db == nil || sessionID == "" {
		return true
	}
	maxPer := h.loadInt(tenantID, "handoff.max_per_session", h.config.MaxPerSession)
	if n, err := h.db.GetHandoffCount(ctx, sessionID); err == nil && maxPer > 0 && n >= maxPer {
		return false
	}
	cooldown := h.loadInt(tenantID, "handoff.cooldown_seconds", h.config.CooldownSeconds)
	if active, err := h.db.IsHandoffCooldownActive(ctx, sessionID, cooldown); err == nil && active {
		return false
	}
	return true
}

func (h *TriggerHook) buildRequestSummary(ctx context.Context, req *Request, engine SummaryEngine) string {
	conversation := extractConversation(req.Body)
	if conversation == "" {
		return h.buildSummary(ctx, &response.InterceptRequest{
			TenantID: req.TenantID, ClientModel: req.ClientModel, TokensUsed: req.TokenEstimate,
			ContextWindow: req.ContextWindow, MessageCount: req.MessageCount,
		}, engine)
	}
	maxTokens := h.loadInt(req.TenantID, "handoff.summary_max_tokens", h.config.MaxSummaryTokens)
	if maxTokens <= 0 {
		maxTokens = 2000
	}
	maxChars := maxTokens * 4
	if len(conversation) > maxChars*8 {
		conversation = conversation[len(conversation)-maxChars*8:]
	}
	if engine != SummaryRule && h.config.LLMCaller != nil {
		model := h.loadString(req.TenantID, "handoff.summary_model", h.config.SummaryModel)
		prompt := h.loadString(req.TenantID, "handoff.summary_prompt_tpl", h.config.SummaryPromptTpl)
		if prompt == "" {
			prompt = defaultSummaryPrompt
		}
		out, err := h.callRequestSummaryLLM(ctx, model, []map[string]string{
			{"role": "system", "content": "Summarize this conversation for a fresh gateway session. Preserve current intent, decisions, exact identifiers, paths, errors, pending work, and tool-result references. Never include credentials."},
			{"role": "user", "content": prompt + "\n\n# Conversation\n" + conversation},
		}, req.UpstreamAPIKey)
		if err == nil && strings.TrimSpace(out) != "" {
			return truncateRunes(redactResumeSensitive(strings.TrimSpace(out)), maxChars)
		}

	}
	return truncateRunes(conversation, maxChars)
}

func (h *TriggerHook) callRequestSummaryLLM(ctx context.Context, model string, messages []map[string]string, upstreamAPIKey string) (string, error) {
	if caller, ok := h.config.LLMCaller.(KeyedLLMCaller); ok && upstreamAPIKey != "" {
		if out, err := caller.CallLLMWithAPIKey(ctx, model, messages, upstreamAPIKey); err == nil {
			return out, nil
		}
	}
	return h.config.LLMCaller.CallLLM(ctx, model, messages)
}

func hasSkillInvocation(body []byte, skill string) bool {
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		if payload.Messages[i].Role == "user" {
			return strings.HasPrefix(strings.TrimSpace(payload.Messages[i].Content), "/"+skill)
		}
	}
	return false
}

func extractConversation(body []byte) string {
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	var b strings.Builder
	for _, message := range payload.Messages {
		if message.Content != "" {
			fmt.Fprintf(&b, "%s: %s\n", message.Role, message.Content)
		}
	}
	return redactResumeSensitive(strings.TrimSpace(b.String()))
}

func redactResumeSensitive(text string) string {
	for _, pattern := range resumeSensitivePatterns {
		text = pattern.ReplaceAllString(text, "[redacted]")
	}
	return text
}

func stripSkillInvocation(body []byte, skill string) []byte {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	var messages []json.RawMessage
	if json.Unmarshal(payload["messages"], &messages) != nil || len(messages) == 0 {
		return body
	}
	var last struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if json.Unmarshal(messages[len(messages)-1], &last) != nil || last.Role != "user" {
		return body
	}
	last.Content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(last.Content), "/"+skill))
	updated, err := json.Marshal(last)
	if err != nil {
		return body
	}
	messages[len(messages)-1] = updated
	payload["messages"], err = json.Marshal(messages)
	if err != nil {
		return body
	}
	clean, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return clean
}
