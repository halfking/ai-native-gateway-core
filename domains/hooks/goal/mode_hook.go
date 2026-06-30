// Package goal implements goal-oriented automatic session management.
package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// State represents the current state of a goal session.
type State string

const (
	StateActive    State = "active"
	StatePaused    State = "paused"
	StateRetrying  State = "retrying"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
)

// DetectionMode specifies how goal mode is activated.
type DetectionMode string

const (
	ModeKeyword  DetectionMode = "keyword"
	ModeExplicit DetectionMode = "explicit"
	ModeLLM      DetectionMode = "llm"
	ModeHybrid   DetectionMode = "hybrid"
)

// ModeConfig contains goal mode configuration.
type ModeConfig struct {
	Enabled                bool
	DetectionMode          DetectionMode
	AutoSelectRecommended  bool
	AutoContinueOnPause    bool
	MaxRetryCount          int
	MaxAutoContinueCount   int
	UseAutorouteForAudit   bool
	UseAutorouteForIntent  bool
	FallbackAuditModel     string
	AutoFixEnabled         bool
	SettingsGetter         SettingsGetter
}

// SettingsGetter retrieves tenant-specific settings.
type SettingsGetter interface {
	GetBool(tenantID, key string, defaultValue bool) bool
	GetInt(tenantID, key string, defaultValue int) int
	GetString(tenantID, key string, defaultValue string) string
}

// Session represents a goal-oriented session.
type Session struct {
	SessionID         string
	TenantID          string
	State             State
	OriginalGoal      string
	RetryCount        int
	DecisionCount     int
	AutoContinueCount int
	LastActivityAt    time.Time
	CompletedAt       *time.Time
	AuditResult       json.RawMessage
	CreatedAt         time.Time
}

// ModeHook implements response.ResponseInterceptor for goal mode management.
type ModeHook struct {
	config    ModeConfig
	db        GoalStore
	llmCaller LLMCaller
	detector  *CompletionDetector
}

// GoalStore defines the interface for persisting goal sessions.
type GoalStore interface {
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	CreateSession(ctx context.Context, session *Session) error
	UpdateSessionState(ctx context.Context, sessionID string, state State) error
	IncrementAutoContinueCount(ctx context.Context, sessionID string) error
	IncrementDecisionCount(ctx context.Context, sessionID string) error
}

// LLMCaller abstracts LLM invocation.
type LLMCaller interface {
	CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error)
}

// NewModeHook creates a new goal mode hook.
func NewModeHook(config ModeConfig, db GoalStore, llmCaller LLMCaller) *ModeHook {
	detector := NewCompletionDetector(db, llmCaller)
	return &ModeHook{
		config:    config,
		db:        db,
		llmCaller: llmCaller,
		detector:  detector,
	}
}

// InterceptNonStream handles goal mode logic for non-streaming responses.
func (h *ModeHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	enabled := h.loadBool(req.TenantID, "goal.enabled", h.config.Enabled)
	if !enabled {
		return nil, nil
	}

	goalSession, err := h.db.GetSession(ctx, req.SessionID)
	if err != nil || goalSession == nil {
		shouldActivate, reason := h.shouldActivateGoalMode(ctx, req)
		if !shouldActivate {
			return nil, nil
		}

		slog.Info("goal_mode_activated", "session_id", req.SessionID, "method", reason)

		goalSession = &Session{
			SessionID:      req.SessionID,
			TenantID:       req.TenantID,
			State:          StateActive,
			OriginalGoal:   "Goal from session",
			LastActivityAt: time.Now(),
			CreatedAt:      time.Now(),
		}
		if err := h.db.CreateSession(ctx, goalSession); err != nil {
			slog.Warn("failed to create goal session", "error", err)
			return nil, nil
		}
	}

	// Check if task is completed
	completed, confidence, reason := h.detector.IsCompleted(ctx, req)
	if completed {
		slog.Info("task_completed", "session_id", req.SessionID, "confidence", confidence, "reason", reason)
		_ = h.db.UpdateSessionState(ctx, req.SessionID, StateCompleted)
		
		useAutoroute := h.loadBool(req.TenantID, "goal.use_autoroute_for_audit", h.config.UseAutorouteForAudit)
		if useAutoroute {
			return h.triggerAudit(ctx, req, goalSession)
		}
	}

	// Check if we need to auto-continue
	if req.FinishReason == "length" || req.FinishReason == "" {
		autoContinue := h.loadBool(req.TenantID, "goal.auto_continue_on_pause", h.config.AutoContinueOnPause)
		maxContinue := h.loadInt(req.TenantID, "goal.max_auto_continue_count", h.config.MaxAutoContinueCount)

		if autoContinue && goalSession.AutoContinueCount < maxContinue {
			_ = h.db.IncrementAutoContinueCount(ctx, req.SessionID)
			return &response.InterceptResult{
				InjectFollowUp: h.buildContinueMessage(req),
				Action:         "goal_continue",
			}, nil
		}
	}

	return nil, nil
}

// InterceptStreamChunk is a no-op for goal mode.
func (h *ModeHook) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd handles goal mode logic after stream completes.
func (h *ModeHook) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	enabled := h.loadBool(meta.TenantID, "goal.enabled", h.config.Enabled)
	if !enabled {
		return nil, nil
	}

	goalSession, err := h.db.GetSession(ctx, meta.SessionID)
	if err != nil || goalSession == nil {
		return nil, nil
	}

	autoContinue := h.loadBool(meta.TenantID, "goal.auto_continue_on_pause", h.config.AutoContinueOnPause)
	maxContinue := h.loadInt(meta.TenantID, "goal.max_auto_continue_count", h.config.MaxAutoContinueCount)

	if autoContinue && goalSession.AutoContinueCount < maxContinue {
		_ = h.db.IncrementAutoContinueCount(ctx, meta.SessionID)
		req := &response.InterceptRequest{SessionID: meta.SessionID, TenantID: meta.TenantID, ClientModel: meta.ClientModel}
		return &response.EndResult{
			InjectFollowUp: h.buildContinueMessage(req),
			Action:         "goal_continue",
		}, nil
	}

	return nil, nil
}

// shouldActivateGoalMode determines if goal mode should be activated.
func (h *ModeHook) shouldActivateGoalMode(ctx context.Context, req *response.InterceptRequest) (bool, string) {
	mode := h.loadString(req.TenantID, "goal.detection_mode", string(h.config.DetectionMode))

	switch DetectionMode(mode) {
	case ModeExplicit:
		return h.detectExplicit(req)
	case ModeKeyword:
		return h.detectKeyword(req)
	case ModeHybrid:
		if explicit, reason := h.detectExplicit(req); explicit {
			return true, "explicit:" + reason
		}
		if keyword, reason := h.detectKeyword(req); keyword {
			return true, "keyword:" + reason
		}
	}
	return false, ""
}

// detectExplicit checks for explicit goal mode markers.
func (h *ModeHook) detectExplicit(req *response.InterceptRequest) (bool, string) {
	var body map[string]interface{}
	if err := json.Unmarshal(req.ResponseBody, &body); err == nil {
		if goal, ok := body["goal"].(bool); ok && goal {
			return true, "body_field"
		}
	}
	return false, ""
}

// detectKeyword checks for goal-related keywords.
func (h *ModeHook) detectKeyword(req *response.InterceptRequest) (bool, string) {
	keywords := []string{"goal", "目标", "完整执行", "一直执行", "持续执行"}
	content := strings.ToLower(string(req.ResponseBody))
	for _, kw := range keywords {
		if strings.Contains(content, strings.ToLower(kw)) {
			return true, kw
		}
	}
	return false, ""
}

// buildContinueMessage constructs a continuation request.
func (h *ModeHook) buildContinueMessage(req *response.InterceptRequest) []byte {
	prompts := NewPrompts(LocaleZhCN) // Default to Chinese, can be enhanced to detect from request
	reqBody := map[string]interface{}{
		"model":    req.ClientModel,
		"messages": []map[string]string{{"role": "user", "content": prompts.ContinueNextStep()}},
		"stream":   false,
	}
	body, _ := json.Marshal(reqBody)
	return body
}

// triggerAudit initiates audit using autoroute.
func (h *ModeHook) triggerAudit(ctx context.Context, req *response.InterceptRequest, session *Session) (*response.InterceptResult, error) {
	model := h.loadString(req.TenantID, "goal.fallback_audit_model", h.config.FallbackAuditModel)
	if h.config.UseAutorouteForAudit {
		model = "auto"
	}

	prompts := NewPrompts(LocaleZhCN)
	auditPrompt := prompts.AuditStarted() + "\n" + fmt.Sprintf(`Review the task execution and return JSON:
{"issues": [{"severity": "high/medium/low", "description": "...", "fix": "..."}], "summary": "Overall assessment"}`)

	reqBody := map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": auditPrompt}},
		"stream":   false,
		"metadata": map[string]string{"task_type_hint": "code_audit"},
	}

	body, _ := json.Marshal(reqBody)
	return &response.InterceptResult{InjectFollowUp: body, Action: "audit"}, nil
}

func (h *ModeHook) loadBool(tenantID, key string, def bool) bool {
	if h.config.SettingsGetter != nil {
		return h.config.SettingsGetter.GetBool(tenantID, key, def)
	}
	return def
}

func (h *ModeHook) loadInt(tenantID, key string, def int) int {
	if h.config.SettingsGetter != nil {
		return h.config.SettingsGetter.GetInt(tenantID, key, def)
	}
	return def
}

func (h *ModeHook) loadString(tenantID, key string, def string) string {
	if h.config.SettingsGetter != nil {
		return h.config.SettingsGetter.GetString(tenantID, key, def)
	}
	return def
}
