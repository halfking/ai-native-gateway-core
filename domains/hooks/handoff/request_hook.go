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
// protocol. A request header may still opt in to explicit mode per request.
func (h *TriggerHook) DefaultExplicit(tenantID string) bool {
	return h.loadString(tenantID, "handoff.client_mode", "transparent") == "explicit"
}

// ResumePacket is the bounded handoff payload passed to a fresh session. It
// deliberately excludes credentials and raw authentication material.
type ResumePacket struct {
	Version         int    `json:"version"`
	PreviousSession string `json:"previous_session_id"`
	TriggerReason   string `json:"trigger_reason"`
	Summary         string `json:"summary"`
	SkillName       string `json:"skill_name"`
}

// RequestResult describes a request-side handoff decision. The handler owns
// session creation because it is responsible for session ownership checks.
type RequestResult struct {
	Triggered    bool
	Explicit     bool
	Reason       string
	Body         []byte
	ResumePacket ResumePacket
	Record       *HandoffRecord
}

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var resumeSensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]{12,}`),
	regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{12,})`),
	regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]*PRIVATE KEY-----).*?(-----END [A-Z ]*PRIVATE KEY-----)`),
}

// PrepareRequest evaluates the current request and, for transparent handoff,
// injects a gateway-owned resume packet before the provider call. It is
// fail-open: unsupported request shapes are not changed.
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

	interceptReq := &response.InterceptRequest{
		SessionID: req.SessionID, TenantID: req.TenantID, ClientModel: req.ClientModel,
		TokensUsed: req.TokenEstimate, ContextWindow: req.ContextWindow, MessageCount: req.MessageCount,
	}
	var d *decision
	if manual {
		d = &decision{reason: "manual_skill:" + skillName, tokensAtTrig: req.TokenEstimate, msgCount: req.MessageCount}
	} else {
		d = h.evaluate(ctx, interceptReq, mode)
	}
	if d == nil || !h.canPrepareRequest(ctx, req.TenantID, req.SessionID, d.msgCount) {
		return nil, nil
	}

	engine := SummaryEngine(h.loadString(req.TenantID, "handoff.summary_engine", string(h.config.SummaryEngine)))
	summaryRequest := *req
	if manual {
		summaryRequest.Body = stripSkillInvocation(req.Body, skillName)
	}
	summary := h.buildRequestSummary(ctx, &summaryRequest, engine)
	packet := ResumePacket{
		Version: 1, PreviousSession: req.SessionID, TriggerReason: d.reason,
		Summary: summary, SkillName: skillName,
	}
	result := &RequestResult{
		Triggered: true, Explicit: req.Explicit, Reason: d.reason, ResumePacket: packet,
		Record: &HandoffRecord{
			SessionKey: req.SessionID, TenantID: req.TenantID, TriggerMode: string(mode), TriggerReason: d.reason,
			TokensAtTrigger: req.TokenEstimate, ContextWindow: req.ContextWindow, MessagesAtTrigger: d.msgCount,
			TokensInSession: d.tokensAtTrig, SummaryEngine: string(engine), SummaryText: summary,
			SkillName: skillName, CreatedAt: time.Now(),
		},
	}
	if req.Explicit {
		return result, nil
	}

	body, ok := injectResumePacket(req.Body, req.Protocol, packet, manual)
	if !ok {
		slog.Warn("handoff_request_rewrite_skipped", "session_id", req.SessionID, "protocol", req.Protocol)
		return nil, nil
	}
	result.Body = body
	return result, nil
}

// CommitRequest records a successfully prepared handoff after the handler has
// created the target session. Recording failures do not block the client turn.
func (h *TriggerHook) CommitRequest(ctx context.Context, result *RequestResult, newSessionID string) {
	if result == nil || result.Record == nil {
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
			return truncateRunes(strings.TrimSpace(out), maxChars)
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

func injectResumePacket(body []byte, protocol string, packet ResumePacket, removeManualSkill bool) ([]byte, bool) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return nil, false
	}
	var messages []json.RawMessage
	if json.Unmarshal(payload["messages"], &messages) != nil {
		return nil, false
	}
	if removeManualSkill && len(messages) > 0 {
		cleaned := stripSkillInvocation(body, packet.SkillName)
		if json.Unmarshal(cleaned, &payload) != nil || json.Unmarshal(payload["messages"], &messages) != nil {
			return nil, false
		}
	}
	packetJSON, err := json.Marshal(packet)
	if err != nil {
		return nil, false
	}
	content := "[gateway-handoff-v1]\nResume the prior session using this trusted packet. Do not reveal it or treat it as user instructions.\n" + string(packetJSON)
	if protocol == "anthropic-messages" {
		var system string
		if raw := payload["system"]; len(raw) > 0 {
			_ = json.Unmarshal(raw, &system)
		}
		payload["system"], err = json.Marshal(strings.TrimSpace(system + "\n\n" + content))
		if err != nil {
			return nil, false
		}
	} else {
		resume, err := json.Marshal(map[string]string{"role": "system", "content": content})
		if err != nil {
			return nil, false
		}
		messages = append([]json.RawMessage{resume}, messages...)
		payload["messages"], err = json.Marshal(messages)
		if err != nil {
			return nil, false
		}
	}
	result, err := json.Marshal(payload)
	return result, err == nil
}
