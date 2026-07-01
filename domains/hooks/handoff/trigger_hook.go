// Package handoff implements automatic session handoff when context limits are reached.
package handoff

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TriggerConfig contains configuration for handoff triggering.
type TriggerConfig struct {
	Enabled             bool
	AbsoluteThreshold   int     // Tokens
	PercentageThreshold float64 // 0.0-1.0
	MessageThreshold    int     // 0 = disabled
	SkillName           string
	SettingsGetter      SettingsGetter // Optional: for dynamic tenant-specific settings
}

// SettingsGetter retrieves tenant-specific settings.
type SettingsGetter interface {
	GetBool(tenantID, key string, defaultValue bool) bool
	GetInt(tenantID, key string, defaultValue int) int
	GetFloat(tenantID, key string, defaultValue float64) float64
	GetString(tenantID, key string, defaultValue string) string
}

// TriggerHook implements response.ResponseInterceptor for automatic handoff.
type TriggerHook struct {
	config TriggerConfig
	db     HandoffStore
}

// HandoffStore defines the interface for persisting handoff events.
type HandoffStore interface {
	RecordHandoff(ctx context.Context, record *HandoffRecord) error
	GetSessionTokenCount(ctx context.Context, sessionID string) (int, error)
	UpdateSessionHandoffCount(ctx context.Context, sessionID string) error
}

// HandoffRecord represents a handoff event.
type HandoffRecord struct {
	SessionID       string
	TenantID        string
	TriggerReason   string
	TokensAtHandoff int
	ContextWindow   int
	HandoffPrompt   string
	CreatedAt       time.Time
}

// NewTriggerHook creates a new handoff trigger hook.
func NewTriggerHook(config TriggerConfig, db HandoffStore) *TriggerHook {
	return &TriggerHook{
		config: config,
		db:     db,
	}
}

// InterceptNonStream checks if handoff should trigger for non-streaming responses.
func (h *TriggerHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	enabled := h.loadSetting(req.TenantID, "handoff.enabled", h.config.Enabled).(bool)
	if !enabled {
		return nil, nil
	}

	shouldTrigger, reason := h.shouldTriggerHandoff(ctx, req)
	if !shouldTrigger {
		return nil, nil
	}

	slog.Info("handoff_triggered",
		"session_id", req.SessionID,
		"tenant_id", req.TenantID,
		"reason", reason,
		"tokens_used", req.TokensUsed,
	)

	handoffMsg := h.buildHandoffMessage(req)

	if h.db != nil {
		record := &HandoffRecord{
			SessionID:       req.SessionID,
			TenantID:        req.TenantID,
			TriggerReason:   reason,
			TokensAtHandoff: req.TokensUsed,
			ContextWindow:   req.ContextWindow,
			HandoffPrompt:   string(handoffMsg),
			CreatedAt:       time.Now(),
		}
		_ = h.db.RecordHandoff(ctx, record)
		_ = h.db.UpdateSessionHandoffCount(ctx, req.SessionID)
	}

	return &response.InterceptResult{
		InjectFollowUp: handoffMsg,
		Action:         "handoff",
		Metadata: map[string]interface{}{
			"trigger_reason":    reason,
			"tokens_at_handoff": req.TokensUsed,
		},
	}, nil
}

// InterceptStreamChunk is a no-op for handoff.
func (h *TriggerHook) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd checks if handoff should trigger after streaming completes.
func (h *TriggerHook) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	enabled := h.loadSetting(meta.TenantID, "handoff.enabled", h.config.Enabled).(bool)
	if !enabled {
		return nil, nil
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

	shouldTrigger, reason := h.shouldTriggerHandoff(ctx, req)
	if !shouldTrigger {
		return nil, nil
	}

	slog.Info("handoff_triggered_after_stream", "session_id", meta.SessionID, "reason", reason)

	handoffMsg := h.buildHandoffMessage(req)

	if h.db != nil {
		record := &HandoffRecord{
			SessionID:       meta.SessionID,
			TenantID:        meta.TenantID,
			TriggerReason:   reason,
			TokensAtHandoff: meta.TokensUsed,
			ContextWindow:   meta.ContextWindow,
			HandoffPrompt:   string(handoffMsg),
			CreatedAt:       time.Now(),
		}
		_ = h.db.RecordHandoff(ctx, record)
		_ = h.db.UpdateSessionHandoffCount(ctx, meta.SessionID)
	}

	return &response.EndResult{
		InjectFollowUp: handoffMsg,
		Action:         "handoff",
		Metadata:       map[string]interface{}{"trigger_reason": reason},
	}, nil
}

// shouldTriggerHandoff determines if handoff should occur.
func (h *TriggerHook) shouldTriggerHandoff(ctx context.Context, req *response.InterceptRequest) (bool, string) {
	sessionTokens := req.TokensUsed
	if h.db != nil {
		if count, err := h.db.GetSessionTokenCount(ctx, req.SessionID); err == nil && count > 0 {
			sessionTokens = count
		}
	}

	absThreshold := h.loadSetting(req.TenantID, "handoff.absolute_threshold", h.config.AbsoluteThreshold).(int)
	if absThreshold > 0 && sessionTokens >= absThreshold {
		return true, fmt.Sprintf("absolute_threshold:%d", absThreshold)
	}

	if req.ContextWindow > 0 {
		pctThreshold := h.loadSetting(req.TenantID, "handoff.percentage_threshold", h.config.PercentageThreshold).(float64)
		threshold := float64(req.ContextWindow) * pctThreshold
		if sessionTokens >= int(threshold) {
			return true, fmt.Sprintf("percentage_threshold:%.0f%%", pctThreshold*100)
		}
	}

	msgThreshold := h.loadSetting(req.TenantID, "handoff.message_threshold", h.config.MessageThreshold).(int)
	if msgThreshold > 0 && req.MessageCount >= msgThreshold {
		return true, fmt.Sprintf("message_threshold:%d", msgThreshold)
	}

	return false, ""
}

// buildHandoffMessage constructs the handoff request.
func (h *TriggerHook) buildHandoffMessage(req *response.InterceptRequest) []byte {
	skillName := h.loadSetting(req.TenantID, "handoff.skill_name", h.config.SkillName).(string)

	prompts := goal.NewPrompts(goal.LocaleZhCN) // Default to Chinese
	content := fmt.Sprintf("/%s\n\n%s\nCurrent: tokens=%d, context=%d, messages=%d",
		skillName,
		prompts.HandoffPrompt(fmt.Sprintf("tokens=%d", req.TokensUsed)),
		req.TokensUsed, req.ContextWindow, req.MessageCount)

	reqBody := map[string]interface{}{
		"model":    req.ClientModel,
		"messages": []map[string]string{{"role": "user", "content": content}},
		"stream":   false,
	}
	body, _ := json.Marshal(reqBody)
	return body
}

// loadSetting retrieves tenant-specific setting.
func (h *TriggerHook) loadSetting(tenantID, key string, defaultValue interface{}) interface{} {
	if h.config.SettingsGetter == nil {
		return defaultValue
	}

	switch v := defaultValue.(type) {
	case bool:
		return h.config.SettingsGetter.GetBool(tenantID, key, v)
	case int:
		return h.config.SettingsGetter.GetInt(tenantID, key, v)
	case float64:
		return h.config.SettingsGetter.GetFloat(tenantID, key, v)
	case string:
		return h.config.SettingsGetter.GetString(tenantID, key, v)
	default:
		return defaultValue
	}
}

// PGStore implements HandoffStore using PostgreSQL.
type PGStore struct {
	db *sql.DB
}

// NewPGStore creates a new PostgreSQL-backed handoff store.
func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

// RecordHandoff records a handoff event.
func (s *PGStore) RecordHandoff(ctx context.Context, record *HandoffRecord) error {
	query := `INSERT INTO handoff_logs (session_id, tenant_id, trigger_reason, tokens_at_handoff, context_window, handoff_prompt, created_at)
	          VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := s.db.ExecContext(ctx, query, record.SessionID, record.TenantID, record.TriggerReason,
		record.TokensAtHandoff, record.ContextWindow, record.HandoffPrompt, record.CreatedAt)
	return err
}

// GetSessionTokenCount gets cumulative token count for a session.
func (s *PGStore) GetSessionTokenCount(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(total_tokens_used, 0) FROM sessions WHERE session_id = $1", sessionID).Scan(&count)
	return count, err
}

// UpdateSessionHandoffCount increments handoff count.
func (s *PGStore) UpdateSessionHandoffCount(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET handoff_count = handoff_count + 1, last_handoff_at = NOW() WHERE session_id = $1", sessionID)
	return err
}
