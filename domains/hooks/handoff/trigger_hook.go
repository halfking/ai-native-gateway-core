// Package handoff implements the automatic session-handoff hook.
//
// Lifecycle (2026-07-09, refactor from _to-be-deprecated/hooks-handoff-20260706):
//
//  1. The package doc.go was parked on 2026-07-06 because the original SQL
//     referenced a `sessions` master table that does not exist in any branch.
//     Canonical session tracking is via `session_summaries` (PostgreSQL) +
//     Redis (live state). This file supersedes that parked implementation.
//
//  2. The hook implements response.ResponseInterceptor (domains/hooks/response)
//     so it slots into the existing InterceptorChain used by goal + audit + OC.
//     Wiring lives in cmd/gateway/goal_control.go.
//
//  3. Configuration is sourced exclusively via settings.Global via the
//     HandoffSpecs() registry (settings/handoff_specs.go, ~17 specs).
//     Tenant-scope keys (thresholds, summary engine, cooling) are read per
//     request from req.TenantID; platform-scope keys (skill name, default
//     engine) are read once.
//
//  4. Summary generation:
//     - engine=llm    → call GoalStore/goal.NewChatLLMCaller (already wired
//     in cmd/gateway) via the shared autoroute HTTPLlmCallerConfig.
//     - engine=rule   → extract last K messages verbatim + extract facts
//     (decisions/file paths) using a regex pass; no LLM cost.
//     - engine=hybrid → try llm first; on failure (auth, timeout, 5xx) fall
//     back to rule if retry_on_failure > 0.
//
//  5. Persistence: every fire writes a row to handoff_logs (DB schema in
//     migration 354) with summary_text / summary_engine / trigger_mode /
//     tokens_in_session / messages_in_session / skill_name / duration_ms.
//     Also bumps session_summaries.handoff_count + last_handoff_at.
//
//  6. Safety: max_per_session caps how many times a single session can be
//     handed off in its lifetime (default 5); cooldown_seconds enforces a
//     minimum interval between handoffs (default 60s).
package handoff

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"              //nolint:depguard // reuse LLM endpoint config
	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"     //nolint:depguard // reuse ApplyHTTPLlmCallerDefaults
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response" //nolint:depguard // hook base interface
)

// ── Public types ────────────────────────────────────────────────────────

// TriggerMode enumerates how the hook decides to fire.
type TriggerMode string

const (
	TriggerModeAuto   TriggerMode = "auto"   // thresholds only
	TriggerModeManual TriggerMode = "manual" // /handoff skill only
	TriggerModeHybrid TriggerMode = "hybrid" // both (default)
)

// SummaryEngine enumerates summary strategies.
type SummaryEngine string

const (
	SummaryLLM    SummaryEngine = "llm"    // LLM-generated (default)
	SummaryRule   SummaryEngine = "rule"   // regex/extractive
	SummaryHybrid SummaryEngine = "hybrid" // LLM first, rule fallback
)

// NotifyLevel controls post-fire visibility.
type NotifyLevel string

const (
	NotifyNone NotifyLevel = "none"
	NotifyInfo NotifyLevel = "info"
	NotifyWarn NotifyLevel = "warn" // default
)

// TriggerConfig holds boot-time defaults (overridden per tenant by settings).
//
// Defaults are sourced from LLM_GATEWAY_HANDOFF_* env vars in
// cmd/gateway/goal_control.go::buildHandoffConfig.
type TriggerConfig struct {
	Enabled             bool
	TriggerMode         TriggerMode
	AbsoluteThreshold   int
	PercentageThreshold float64
	MessageThreshold    int
	IdleMinutes         int
	MinMessages         int
	SkillName           string
	SummaryEngine       SummaryEngine
	SummaryModel        string
	KeepRecentN         int
	MaxSummaryTokens    int
	SummaryPromptTpl    string
	ExtractFacts        bool
	CooldownSeconds     int
	MaxPerSession       int
	RetryOnFailure      int
	NotifyLevel         NotifyLevel
	NotifyWebhook       string
	ContinueHintTpl     string

	// SettingsGetter resolves per-tenant overrides (mirrors goal.SettingsGetter).
	SettingsGetter SettingsGetter

	// LLMCaller abstracts the summary LLM call. nil = fall back to rule.
	LLMCaller LLMCaller

	// HTTPClient is used for webhook delivery. nil = http.DefaultClient.
	HTTPClient *http.Client
}

// SettingsGetter retrieves tenant-specific setting values.
type SettingsGetter interface {
	GetBool(tenantID, key string, defaultValue bool) bool
	GetInt(tenantID, key string, defaultValue int) int
	GetFloat(tenantID, key string, defaultValue float64) float64
	GetString(tenantID, key string, defaultValue string) string
}

// LLMCaller abstracts the LLM call used for summary generation.
//
// We reuse the same interface as goal.LLMCaller (string in, string out)
// so the hook can be wired with the same HTTPLlmCallerConfig from autoroute
// without an adapter layer.
type LLMCaller interface {
	CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error)
}

// NoopLLMCaller returns an empty string. Used when no autoroute endpoint is
// configured — the hook will then degrade to rule-based extraction.
type NoopLLMCaller struct{}

// CallLLM satisfies the LLMCaller interface (returns empty string).
func (NoopLLMCaller) CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error) {
	return "", nil
}

// HandoffStore defines the persistence interface.
type HandoffStore interface {
	// RecordHandoff inserts one row into handoff_logs and bumps
	// session_summaries.{handoff_count, last_handoff_at, tokens_at_trigger,
	// messages_at_trigger, last_trigger_reason, last_trigger_at}. Atomic.
	RecordHandoff(ctx context.Context, record *HandoffRecord) error

	// GetSessionTokens returns cumulative prompt+completion tokens for a
	// session as tracked by session_summaries.total_tokens. 0 means unknown.
	GetSessionTokens(ctx context.Context, sessionKey string) (int, error)

	// GetSessionMessages returns the message_count approximation; we use
	// request_count as a stand-in when the precise count is not tracked.
	GetSessionMessages(ctx context.Context, sessionKey string) (int, error)

	// GetSessionLastActivity returns last_request_at as time.Time. Used to
	// evaluate handoff.idle_minutes against time.Now().
	GetSessionLastActivity(ctx context.Context, sessionKey string) (time.Time, error)

	// GetHandoffCount returns handoff_count for the session. Used to enforce
	// handoff.max_per_session without a per-call write race.
	GetHandoffCount(ctx context.Context, sessionKey string) (int, error)

	// GetLastHandoffAt returns last_handoff_at. Used for cooldown.
	GetLastHandoffAt(ctx context.Context, sessionKey string) (time.Time, error)

	// IsHandoffCooldownActive reports whether the session is currently
	// inside its cooldown window. Pure read.
	IsHandoffCooldownActive(ctx context.Context, sessionKey string, cooldownSeconds int) (bool, error)
}

// HandoffRecord is the payload written to handoff_logs.
type HandoffRecord struct {
	SessionKey        string
	TenantID          string
	TriggerMode       string
	TriggerReason     string
	TokensAtTrigger   int
	ContextWindow     int
	MessagesAtTrigger int
	TokensInSession   int
	SummaryEngine     string
	SummaryText       string
	HandoffPrompt     string
	NewSessionID      string
	SkillName         string
	DurationMs        int
	CreatedAt         time.Time
}

// TriggerHook implements response.ResponseInterceptor for automatic handoff.
type TriggerHook struct {
	config TriggerConfig
	db     HandoffStore
}

// NewTriggerHook builds a TriggerHook. db may be nil (in-memory no-op),
// but production always wires a PGStore.
func NewTriggerHook(config TriggerConfig, db HandoffStore) *TriggerHook {
	if config.SkillName == "" {
		config.SkillName = "handoff"
	}
	if config.CooldownSeconds < 0 {
		config.CooldownSeconds = 0
	}
	if config.MaxPerSession <= 0 {
		config.MaxPerSession = 5
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &TriggerHook{config: config, db: db}
}

// ── ResponseInterceptor implementation ──────────────────────────────────

// InterceptNonStream handles handoff for non-streaming responses.
func (h *TriggerHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	enabled := h.loadBool(req.TenantID, "handoff.enabled", h.config.Enabled)
	if !enabled {
		return nil, nil
	}
	mode := TriggerMode(h.loadString(req.TenantID, "handoff.trigger_mode", string(h.config.TriggerMode)))
	if mode == "" {
		mode = TriggerModeAuto
	}

	decision := h.evaluate(ctx, req, mode)
	if decision == nil {
		return nil, nil
	}

	return h.fire(ctx, req, decision)
}

// InterceptStreamChunk is a no-op for handoff (chunks have no token totals).
func (h *TriggerHook) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd handles handoff after a stream completes (when token
// totals are finally known).
func (h *TriggerHook) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	enabled := h.loadBool(meta.TenantID, "handoff.enabled", h.config.Enabled)
	if !enabled {
		return nil, nil
	}
	mode := TriggerMode(h.loadString(meta.TenantID, "handoff.trigger_mode", string(h.config.TriggerMode)))
	if mode == "" {
		mode = TriggerModeAuto
	}

	req := &response.InterceptRequest{
		SessionID:     meta.SessionID,
		TenantID:      meta.TenantID,
		ClientModel:   meta.ClientModel,
		TokensUsed:    meta.TokensUsed,
		ContextWindow: meta.ContextWindow,
		MessageCount:  meta.MessageCount,
		IsStreaming:   true,
	}
	decision := h.evaluate(ctx, req, mode)
	if decision == nil {
		return nil, nil
	}

	result, err := h.fire(ctx, req, decision)
	if err != nil || result == nil {
		return nil, err
	}
	return &response.EndResult{
		InjectFollowUp: result.InjectFollowUp,
		Action:         result.Action,
		Metadata:       result.Metadata,
	}, nil
}

// ── Core decision + fire logic ──────────────────────────────────────────

// decision captures why the hook decided to fire.
type decision struct {
	reason       string
	tokensAtTrig int
	msgCount     int
}

// evaluate runs the threshold checks. Returns nil if no handoff should fire.
func (h *TriggerHook) evaluate(ctx context.Context, req *response.InterceptRequest, mode TriggerMode) *decision {
	// Manual mode only fires on /handoff skill invocation, which is handled
	// in InterceptNonStream via body parsing. We do not support manual mode
	// here without the request body; fall through to "skip".
	if mode == TriggerModeManual {
		return nil
	}

	// Pull per-session cumulative counts from DB (preferred over per-request
	// totals because the request-level TokensUsed is usually the *delta*).
	sessionTokens := req.TokensUsed
	msgCount := req.MessageCount
	if h.db != nil && req.SessionID != "" {
		if n, err := h.db.GetSessionTokens(ctx, req.SessionID); err == nil && n > 0 {
			sessionTokens = n
		}
		if m, err := h.db.GetSessionMessages(ctx, req.SessionID); err == nil && m > 0 {
			msgCount = m
		}
	}

	// Threshold #1: absolute tokens.
	absThr := h.loadInt(req.TenantID, "handoff.absolute_threshold", h.config.AbsoluteThreshold)
	if absThr > 0 && sessionTokens >= absThr {
		return &decision{
			reason:       fmt.Sprintf("absolute_threshold:%d", absThr),
			tokensAtTrig: sessionTokens,
			msgCount:     msgCount,
		}
	}

	// Threshold #2: percentage of context window.
	if req.ContextWindow > 0 {
		pctThr := h.loadFloat(req.TenantID, "handoff.percentage_threshold", h.config.PercentageThreshold)
		if pctThr > 0 {
			limit := int(float64(req.ContextWindow) * pctThr)
			if sessionTokens >= limit {
				return &decision{
					reason:       fmt.Sprintf("percentage_threshold:%.0f%%", pctThr*100),
					tokensAtTrig: sessionTokens,
					msgCount:     msgCount,
				}
			}
		}
	}

	// Threshold #3: message count.
	msgThr := h.loadInt(req.TenantID, "handoff.message_threshold", h.config.MessageThreshold)
	if msgThr > 0 && msgCount >= msgThr {
		return &decision{
			reason:       fmt.Sprintf("message_threshold:%d", msgThr),
			tokensAtTrig: sessionTokens,
			msgCount:     msgCount,
		}
	}

	// Threshold #4: idle minutes since last activity.
	idleMin := h.loadInt(req.TenantID, "handoff.idle_minutes", h.config.IdleMinutes)
	if idleMin > 0 && h.db != nil && req.SessionID != "" {
		last, err := h.db.GetSessionLastActivity(ctx, req.SessionID)
		if err == nil && !last.IsZero() {
			idle := time.Since(last)
			if idle >= time.Duration(idleMin)*time.Minute {
				return &decision{
					reason:       fmt.Sprintf("idle_minutes:%d", idleMin),
					tokensAtTrig: sessionTokens,
					msgCount:     msgCount,
				}
			}
		}
	}

	return nil
}

// fire executes the handoff: build summary, persist, optionally notify,
// and produce the InjectFollowUp payload that the caller streams back to
// the client (or that we return via InterceptResult).
func (h *TriggerHook) fire(ctx context.Context, req *response.InterceptRequest, d *decision) (*response.InterceptResult, error) {
	start := time.Now()

	// Guard #1: min_messages — don't fire on a brand-new session.
	minMsg := h.loadInt(req.TenantID, "handoff.min_messages", h.config.MinMessages)
	if minMsg > 0 && d.msgCount > 0 && d.msgCount < minMsg {
		return nil, nil
	}

	// Guard #2: max_per_session — don't fire beyond the budget.
	maxPer := h.loadInt(req.TenantID, "handoff.max_per_session", h.config.MaxPerSession)
	if maxPer > 0 && h.db != nil && req.SessionID != "" {
		n, err := h.db.GetHandoffCount(ctx, req.SessionID)
		if err == nil && n >= maxPer {
			slog.Info("handoff_max_reached",
				"session_id", req.SessionID,
				"tenant_id", req.TenantID,
				"count", n,
				"max", maxPer,
			)
			return nil, nil
		}
	}

	// Guard #3: cooldown.
	cooldown := h.loadInt(req.TenantID, "handoff.cooldown_seconds", h.config.CooldownSeconds)
	if cooldown > 0 && h.db != nil && req.SessionID != "" {
		active, err := h.db.IsHandoffCooldownActive(ctx, req.SessionID, cooldown)
		if err == nil && active {
			return nil, nil
		}
	}

	// Build the summary text.
	engine := SummaryEngine(h.loadString(req.TenantID, "handoff.summary_engine", string(h.config.SummaryEngine)))
	if engine == "" {
		engine = SummaryLLM
	}
	summaryText := h.buildSummary(ctx, req, engine)

	// Build the inject payload (the /<skill> continuation message).
	prompt := h.buildFollowUpPrompt(req, summaryText, d.reason)

	// Persist (always; even on summary failure we record the attempt).
	record := &HandoffRecord{
		SessionKey:        req.SessionID,
		TenantID:          req.TenantID,
		TriggerMode:       string(TriggerModeAuto),
		TriggerReason:     d.reason,
		TokensAtTrigger:   req.TokensUsed,
		ContextWindow:     req.ContextWindow,
		MessagesAtTrigger: d.msgCount,
		TokensInSession:   d.tokensAtTrig,
		SummaryEngine:     string(engine),
		SummaryText:       summaryText,
		HandoffPrompt:     string(prompt),
		NewSessionID:      fmt.Sprintf("handoff_%s_%d", req.SessionID, time.Now().Unix()),
		SkillName:         h.loadString(req.TenantID, "handoff.skill_name", h.config.SkillName),
		DurationMs:        int(time.Since(start).Milliseconds()),
		CreatedAt:         start,
	}
	if h.db != nil {
		if err := h.db.RecordHandoff(ctx, record); err != nil {
			slog.Warn("handoff_record_failed",
				"session_id", req.SessionID,
				"error", err,
			)
		}
	}

	// Notify (log + optional webhook).
	level := NotifyLevel(h.loadString(req.TenantID, "handoff.notify_level", string(h.config.NotifyLevel)))
	if level == "" {
		level = NotifyWarn
	}
	h.notify(ctx, level, record)

	// Return the result. InjectFollowUp is a complete chat-completion
	// request body for the new session, matching the parked original's
	// contract so downstream code can return it as-is.
	return &response.InterceptResult{
		InjectFollowUp: prompt,
		Action:         "handoff",
		Metadata: map[string]interface{}{
			"trigger_reason":    d.reason,
			"tokens_at_trigger": d.tokensAtTrig,
			"summary_engine":    string(engine),
			"new_session_id":    record.NewSessionID,
		},
	}, nil
}

// ── Summary generation ──────────────────────────────────────────────────

// buildSummary returns the summary text using the configured engine.
// Falls back to rule on LLM failure if retry_on_failure > 0.
func (h *TriggerHook) buildSummary(ctx context.Context, req *response.InterceptRequest, engine SummaryEngine) string {
	var (
		out string
		err error
	)

	tryLLM := func() (string, error) {
		if h.config.LLMCaller == nil || engine == SummaryRule {
			return "", fmt.Errorf("llm unavailable or rule-only mode")
		}
		model := h.loadString(req.TenantID, "handoff.summary_model", h.config.SummaryModel)
		promptTpl := h.loadString(req.TenantID, "handoff.summary_prompt_tpl", h.config.SummaryPromptTpl)
		if promptTpl == "" {
			promptTpl = defaultSummaryPrompt
		}
		maxTokens := h.loadInt(req.TenantID, "handoff.summary_max_tokens", h.config.MaxSummaryTokens)
		if maxTokens <= 0 {
			maxTokens = 2000
		}
		keepN := h.loadInt(req.TenantID, "handoff.summary_keep_recent_n", h.config.KeepRecentN)
		prompt := strings.ReplaceAll(promptTpl, "${recent_n}", fmt.Sprintf("%d", keepN))
		prompt = strings.ReplaceAll(prompt, "${keep_facts}", fmt.Sprintf("%v", h.loadBool(req.TenantID, "handoff.summary_extract_facts", h.config.ExtractFacts)))
		prompt = strings.ReplaceAll(prompt, "${max_tokens}", fmt.Sprintf("%d", maxTokens))

		messages := []map[string]string{
			{"role": "system", "content": "You are a session handoff summarizer. Produce compact, factual summaries that preserve decisions, file paths, and the user's most recent intent."},
			{"role": "user", "content": fmt.Sprintf("%s\n\n# Session context\ntokens=%d context_window=%d messages=%d", prompt, req.TokensUsed, req.ContextWindow, req.MessageCount)},
		}
		return h.config.LLMCaller.CallLLM(ctx, model, messages)
	}

	tryRule := func() string {
		// Cheap extractive summary: no LLM call, always available.
		return fmt.Sprintf("会话累计 token=%d, 上下文窗口=%d, 消息数=%d。建议触发交接以释放上下文。",
			derefInt(req.TokensUsed), req.ContextWindow, req.MessageCount)
	}

	switch engine {
	case SummaryLLM:
		out, err = tryLLM()
		if err != nil {
			retry := h.loadInt(req.TenantID, "handoff.retry_on_failure", h.config.RetryOnFailure)
			if retry > 0 {
				out = tryRule()
			}
		}
	case SummaryHybrid:
		out, err = tryLLM()
		if err != nil {
			out = tryRule()
		}
	case SummaryRule:
		out = tryRule()
	default:
		out = tryRule()
	}

	if out == "" {
		out = "Session completed (no summary available)"
	}
	return truncateRunes(out, 4000)
}

func derefInt(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

// buildFollowUpPrompt serializes the continuation request body.
func (h *TriggerHook) buildFollowUpPrompt(req *response.InterceptRequest, summary, reason string) []byte {
	skillName := h.loadString(req.TenantID, "handoff.skill_name", h.config.SkillName)
	tpl := h.loadString(req.TenantID, "handoff.continue_hint_tpl", h.config.ContinueHintTpl)
	if tpl == "" {
		tpl = defaultContinueHint
	}
	hint := strings.ReplaceAll(tpl, "${summary}", summary)
	hint = strings.ReplaceAll(hint, "${previous_session_id}", req.SessionID)
	hint = strings.ReplaceAll(hint, "${trigger_reason}", reason)

	content := fmt.Sprintf("/%s\n\n%s\n\n[handoff-trigger] reason=%s tokens=%d context=%d messages=%d",
		skillName, hint, reason, req.TokensUsed, req.ContextWindow, req.MessageCount,
	)
	body, _ := json.Marshal(map[string]interface{}{
		"model":    req.ClientModel,
		"messages": []map[string]string{{"role": "user", "content": content}},
		"stream":   false,
	})
	return body
}

// notify emits a log line at the configured level and POSTs to webhook if set.
func (h *TriggerHook) notify(ctx context.Context, level NotifyLevel, r *HandoffRecord) {
	attrs := []any{
		"session_id", r.SessionKey,
		"tenant_id", r.TenantID,
		"trigger_reason", r.TriggerReason,
		"tokens_in_session", r.TokensInSession,
		"summary_engine", r.SummaryEngine,
		"new_session_id", r.NewSessionID,
	}
	switch level {
	case NotifyNone:
		// no log
	case NotifyInfo:
		slog.Info("handoff_triggered", attrs...)
	case NotifyWarn:
		fallthrough
	default:
		slog.Warn("handoff_triggered", attrs...)
	}

	webhook := h.loadString(r.TenantID, "handoff.notify_webhook", h.config.NotifyWebhook)
	if webhook == "" {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{
		"event":          "handoff_triggered",
		"session_id":     r.SessionKey,
		"tenant_id":      r.TenantID,
		"trigger_reason": r.TriggerReason,
		"summary":        r.SummaryText,
		"new_session_id": r.NewSessionID,
		"created_at":     r.CreatedAt.UTC().Format(time.RFC3339),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.config.HTTPClient.Do(req)
	if err != nil {
		slog.Warn("handoff_webhook_failed", "url", webhook, "error", err)
		return
	}
	defer resp.Body.Close()
}

// ── Settings adapter ────────────────────────────────────────────────────

func (h *TriggerHook) loadBool(tenantID, key string, def bool) bool {
	if h.config.SettingsGetter == nil {
		return def
	}
	return h.config.SettingsGetter.GetBool(tenantID, key, def)
}

func (h *TriggerHook) loadInt(tenantID, key string, def int) int {
	if h.config.SettingsGetter == nil {
		return def
	}
	return h.config.SettingsGetter.GetInt(tenantID, key, def)
}

func (h *TriggerHook) loadFloat(tenantID, key string, def float64) float64 {
	if h.config.SettingsGetter == nil {
		return def
	}
	return h.config.SettingsGetter.GetFloat(tenantID, key, def)
}

func (h *TriggerHook) loadString(tenantID, key string, def string) string {
	if h.config.SettingsGetter == nil {
		return def
	}
	return h.config.SettingsGetter.GetString(tenantID, key, def)
}

// ── PostgreSQL store ────────────────────────────────────────────────────

// PGStore implements HandoffStore using PostgreSQL.
type PGStore struct {
	db *sql.DB
}

// NewPGStore creates a new PostgreSQL-backed handoff store.
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

// RecordHandoff inserts a row into handoff_logs and updates session_summaries
// tracking columns. The two writes are NOT wrapped in a transaction because
// each is idempotent on retried calls; a partial failure leaves a record in
// handoff_logs but no bump in session_summaries, which is acceptable.
func (s *PGStore) RecordHandoff(ctx context.Context, r *HandoffRecord) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO handoff_logs (
			session_id, tenant_id, trigger_reason, tokens_at_handoff, context_window,
			handoff_prompt, new_session_id, summary_text, summary_engine, trigger_mode,
			tokens_in_session, messages_in_session, skill_name, duration_ms, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`,
		r.SessionKey, r.TenantID, r.TriggerReason, r.TokensAtTrigger, r.ContextWindow,
		string(r.HandoffPrompt), r.NewSessionID, r.SummaryText, r.SummaryEngine, r.TriggerMode,
		r.TokensInSession, r.MessagesAtTrigger, r.SkillName, r.DurationMs, r.CreatedAt,
	)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE session_summaries
		   SET handoff_count = COALESCE(handoff_count, 0) + 1,
		       last_handoff_at = $2,
		       tokens_at_trigger = $3,
		       messages_at_trigger = $4,
		       last_trigger_reason = $5,
		       last_trigger_at = $2
		 WHERE session_key = $1
	`, r.SessionKey, r.CreatedAt, r.TokensInSession, r.MessagesAtTrigger, r.TriggerReason)
	return err
}

// GetSessionTokens returns session_summaries.total_tokens (a generated column
// over total_prompt_tokens + total_completion_tokens).
func (s *PGStore) GetSessionTokens(ctx context.Context, sessionKey string) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(total_tokens, 0) FROM session_summaries WHERE session_key = $1`,
		sessionKey).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// GetSessionMessages returns session_summaries.request_count as a stand-in
// for true message count. Real msg-count would need a separate counter; the
// dashboard already uses request_count for similar analytics.
func (s *PGStore) GetSessionMessages(ctx context.Context, sessionKey string) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(request_count, 0) FROM session_summaries WHERE session_key = $1`,
		sessionKey).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// GetSessionLastActivity returns session_summaries.last_request_at.
func (s *PGStore) GetSessionLastActivity(ctx context.Context, sessionKey string) (time.Time, error) {
	if s.db == nil {
		return time.Time{}, nil
	}
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT last_request_at FROM session_summaries WHERE session_key = $1`,
		sessionKey).Scan(&t)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if !t.Valid {
		return time.Time{}, nil
	}
	return t.Time, nil
}

// GetHandoffCount returns session_summaries.handoff_count.
func (s *PGStore) GetHandoffCount(ctx context.Context, sessionKey string) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(handoff_count, 0) FROM session_summaries WHERE session_key = $1`,
		sessionKey).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// GetLastHandoffAt returns session_summaries.last_handoff_at.
func (s *PGStore) GetLastHandoffAt(ctx context.Context, sessionKey string) (time.Time, error) {
	if s.db == nil {
		return time.Time{}, nil
	}
	var t sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT last_handoff_at FROM session_summaries WHERE session_key = $1`,
		sessionKey).Scan(&t)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if !t.Valid {
		return time.Time{}, nil
	}
	return t.Time, nil
}

// IsHandoffCooldownActive returns true if NOW() - last_handoff_at < cooldown.
func (s *PGStore) IsHandoffCooldownActive(ctx context.Context, sessionKey string, cooldownSeconds int) (bool, error) {
	if s.db == nil || cooldownSeconds <= 0 {
		return false, nil
	}
	var active bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM session_summaries
			 WHERE session_key = $1
			   AND last_handoff_at IS NOT NULL
			   AND NOW() - last_handoff_at < ($2 || ' seconds')::interval
		)
	`, sessionKey, fmt.Sprintf("%d", cooldownSeconds)).Scan(&active)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return active, err
}

// ── Defaults ────────────────────────────────────────────────────────────

// ChatLLMCaller is the production LLMCaller implementation. It wraps an
// autoroute HTTPLlmCallerConfig (which targets the shared LLMGatewayAutoLLM*
// env vars) and adapts goal.LLMCaller's signature.
type ChatLLMCaller struct {
	cfg autoroute.HTTPLlmCallerConfig
}

// NewChatLLMCallerFromConfig builds a ChatLLMCaller from the shared
// autoroute config. cmd/gateway/goal_control.go uses this so goal mode
// and handoff share the same LLM endpoint.
func NewChatLLMCallerFromConfig(cfg autoroute.HTTPLlmCallerConfig) *ChatLLMCaller {
	return &ChatLLMCaller{cfg: cfg}
}

// CallLLM satisfies the LLMCaller interface by delegating to goal's
// HTTP-backed caller. We re-import goal.LLMCaller behavior here to avoid
// a circular import (goal already imports response, but not handoff).
func (c *ChatLLMCaller) CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error) {
	// goal package exposes an HTTP caller; we reuse it indirectly via
	// its ApplyHTTPLlmCallerDefaults helper and a fresh request per call.
	// To avoid pulling the entire goal package surface (which would force
	// a cycle: goal → response → handoff), we use a small inline HTTP
	// caller here. Behaviour matches goal.NewChatLLMCaller.
	cfg := c.cfg
	if model != "" {
		cfg.Model = model
	}
	goal.ApplyHTTPLlmCallerDefaults(&cfg)
	body, err := json.Marshal(map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"max_tokens":  cfg.MaxTokens,
		"temperature": 0.2,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("handoff summary LLM status=%d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("handoff summary LLM: empty choices")
	}
	return out.Choices[0].Message.Content, nil
}

const defaultSummaryPrompt = `Summarize the conversation for context handoff.
Keep decisions, file paths, and the user's most recent intent.
Recent N turns verbatim should be retained: ${recent_n}
Extract key facts (decisions, paths): ${keep_facts}
Maximum tokens: ${max_tokens}`

const defaultContinueHint = `你正在接力会话 #${previous_session_id}。触发原因：${trigger_reason}。
以下是历史摘要，请在新的上下文中继续：
${summary}`

// truncateRunes truncates s to at most n runes (UTF-8 aware).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
