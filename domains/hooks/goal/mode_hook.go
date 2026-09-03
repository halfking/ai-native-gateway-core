// Package goal implements goal-oriented automatic session management.
package goal

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
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
	Enabled               bool
	DetectionMode         DetectionMode
	AutoSelectRecommended bool
	AutoContinueOnPause   bool
	MaxRetryCount         int
	MaxAutoContinueCount  int
	UseAudit              bool
	UseAutorouteForAudit  bool
	UseAutorouteForIntent bool
	FallbackAuditModel    string
	AutoFixEnabled        bool
	SettingsGetter        SettingsGetter

	// ── Loop-detection & model switching (2026-07-06) ────────────────────
	// When the continue budget is exhausted OR the model repeats itself,
	// the hook can switch to a different model before retrying. This avoids
	// the classic "infinite loop on the same model" failure: a stuck model
	// gets N continues, then we rotate to a fallback model and reset the
	// continue budget so the new model has a fresh attempt.
	ModelSwitchOnLoop      bool
	MaxModelSwitchCount    int
	FallbackModels         []string // candidate models to rotate through; "auto" routes via autoroute
	RepeatDetectionEnabled bool
	RepeatThreshold        int  // consecutive identical responses ⇒ considered a loop
	RepeatResetOnProgress  bool // reset the repeat counter when a response differs
	// CompletionConfidence is the minimum confidence for the LLM-based
	// completion verdict to count as "done". Lower = more eager to declare
	// completion (fewer continues), higher = more conservative.
	CompletionConfidence float64

	// Client-driven signal settings. These are goal-local gates; request
	// capability booleans are supplied by the response layer and never read
	// from the streaming package here.
	ClientSignalEnabled          bool
	ClientSignalMode             string
	HandoffSignalThresholdTokens int
	// MaxFollowUpDepth / MaxFollowUpsPerSession override the hard-coded
	// follow-up engine limits in domains/streaming. Zero = use the engine
	// defaults (MaxFollowUpDepth=15, MaxFollowUpsPerSession=50).
	MaxFollowUpDepth       int
	MaxFollowUpsPerSession int

	// ── Cost Control & Budget (2026-07-19, Phase 0) ──────────────────────
	// Three-tier cost mode system: users select one of minimal/balanced/aggressive
	// instead of tuning individual settings. Each mode presets all the fields
	// below. See: docs/会话优化v2/18-Goal模式成本控制与分级方案.md

	// Retry settings (Phase 1 will implement network-layer retry)
	RetryOnError      bool // enable auto-retry on LLM errors
	RetryDelaySeconds int  // delay between retries (seconds)
	RetryTotalTimeout int  // total timeout for all retries (seconds)

	// Budget limits
	MonthlyTokenLimit  int     // tenant-level monthly token cap (0 = unlimited)
	SessionTokenBudget int     // single goal session token cap (0 = unlimited)
	CostAlertThreshold float64 // alert when usage reaches this ratio (0.0-1.0)
	DowngradeOnBudget  bool    // auto-downgrade to minimal mode when approaching limit
}

// SettingsGetter retrieves tenant-specific settings.
type SettingsGetter interface {
	GetBool(tenantID, key string, defaultValue bool) bool
	GetInt(tenantID, key string, defaultValue int) int
	GetFloat(tenantID, key string, defaultValue float64) float64
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

	// ── Client-signal state (2026-09-03) ────────────────────────────────────
	// ContinueAttempt is the number of client-driven continue signals claimed
	// for this session. It is hydrated from goal_sessions so signal budgets
	// survive process restarts.
	ContinueAttempt         int
	LastCompletionJudgement string
	SubAgentsTotal          int
	SubAgentsCompleted      int
	SubAgentsPending        int

	// ── Loop-detection tracking (2026-07-06) ─────────────────────────────
	// ModelSwitchCount     how many times we've rotated to a fallback model.
	//                       Bounded by MaxModelSwitchCount.
	// RepeatCount          consecutive identical responses observed.
	// LastResponseHash     sha256 of the last assistant reply (for repeat detection).
	// CurrentModel         the model currently driving this session; changes
	//                       on each rotation so we can pick a *different* one next.
	ModelSwitchCount int
	RepeatCount      int
	LastResponseHash string
	CurrentModel     string
}

// ModeHook implements response.ResponseInterceptor for goal mode management.
type ModeHook struct {
	config          ModeConfig
	db              GoalStore
	llmCaller       LLMCaller
	detector        *CompletionDetector
	history         HistoryStore
	outcomeObserver OutcomeObserver
}

// GoalStore defines the interface for persisting goal sessions.
type GoalStore interface {
	GetSession(ctx context.Context, tenantID, sessionID string) (*Session, error)
	CreateSession(ctx context.Context, session *Session) error
	UpdateSessionState(ctx context.Context, tenantID, sessionID string, state State) error
	// CompareAndSetState atomically transitions state to target only when the
	// current state is in allowedFrom. Returns (true, nil) on success,
	// (false, nil) when the row is in another state (including a terminal
	// state already set by a concurrent writer), and (false, err) on DB error.
	// Used to keep terminal states (completed/failed) sticky against racing
	// retry-exhaustion writes.
	CompareAndSetState(ctx context.Context, tenantID, sessionID string, allowedFrom []State, target State) (bool, error)
	IncrementAutoContinueCount(ctx context.Context, tenantID, sessionID string) error
	IncrementDecisionCount(ctx context.Context, tenantID, sessionID string) error
	UpdateSessionAudit(ctx context.Context, tenantID, sessionID string, auditResult []byte) (bool, error)
	AtomicAutoContinue(ctx context.Context, tenantID, sessionID string, maxAllowed int) (bool, error)

	// ── Loop-detection & model switching ────────────────────────────────
	// RecordResponse hashes the assistant reply, updates RepeatCount
	// (increment on identical hash, reset on a new hash when configured),
	// and persists LastResponseHash. Returns the new RepeatCount.
	RecordResponse(ctx context.Context, tenantID, sessionID string, responseHash string, resetOnProgress bool) (int, error)
	// AtomicModelSwitch atomically bumps model_switch_count if under maxAllowed,
	// sets current_model to newModel, and resets auto_continue_count to 0 so the
	// rotated model gets a fresh continue budget. Returns true if this caller won
	// the rotation (false = already at the switch cap).
	AtomicModelSwitch(ctx context.Context, tenantID, sessionID, newModel string, maxAllowed int) (bool, error)

	// Client-signal persistence is optional so existing GoalStore test doubles
	// and handoff-only stores remain source-compatible.
}

// GoalClientSignalStore provides optional durable client-signal state.
type GoalClientSignalStore interface {
	ClaimContinueAttempt(ctx context.Context, tenantID, sessionID string, maxAllowed int) (attempt int, claimed bool, err error)
	RecordSubAgents(ctx context.Context, tenantID, sessionID string, total, completed, pending int) error
	RecordLastCompletionJudgement(ctx context.Context, tenantID, sessionID, judgement string) error
}

// allowedNonTerminalStates lists the source states from which a transition
// into a terminal state (completed/failed) is permitted. Empty string is
// included so the CAS gracefully no-ops when the goal session row does not
// yet exist (the proposal may not have created a goal_sessions row before the
// failure path runs).
func allowedNonTerminalStates() []State {
	return []State{StateActive, StateRetrying, StatePaused, ""}
}

// LLMCaller abstracts LLM invocation.
type LLMCaller interface {
	CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error)
}

// NewModeHook creates a new goal mode hook. history may be nil — the hook
// degrades gracefully (completion detection and continue logic don't need it;
// only the audit hook uses history).
func NewModeHook(config ModeConfig, db GoalStore, llmCaller LLMCaller) *ModeHook {
	return NewModeHookWithHistory(config, db, llmCaller, nil)
}

// NewModeHookWithHistory is like NewModeHook but wires a HistoryStore so the
// goal/audit feature can reason over the full conversation transcript instead
// of only the last assistant reply.
func NewModeHookWithHistory(config ModeConfig, db GoalStore, llmCaller LLMCaller, history HistoryStore) *ModeHook {
	detector := NewCompletionDetector(db, llmCaller)
	if history == nil {
		history = NoopHistoryStore()
	}
	return &ModeHook{
		config:    config,
		db:        db,
		llmCaller: llmCaller,
		detector:  detector,
		history:   history,
	}
}

// History returns the wired HistoryStore (used by AuditHook to share one store
// instance instead of re-creating it).
func (h *ModeHook) History() HistoryStore { return h.history }

// InterceptNonStream handles goal mode logic for non-streaming responses.
func (h *ModeHook) InterceptNonStream(ctx context.Context, req *response.InterceptRequest) (*response.InterceptResult, error) {
	if strings.HasPrefix(req.FollowUpAction, "audit") {
		return nil, nil
	}
	enabled := h.loadBool(req.TenantID, "goal.enabled", h.config.Enabled)
	if !enabled {
		return nil, nil
	}

	goalSession, err := h.db.GetSession(ctx, req.TenantID, req.SessionID)
	if err != nil || goalSession == nil {
		shouldActivate, reason := h.shouldActivateGoalMode(ctx, req)
		if !shouldActivate {
			return nil, nil
		}

		slog.Info("goal_mode_activated", "session_id", req.SessionID, "method", reason)

		goalSession = &Session{
			SessionID:          req.SessionID,
			TenantID:           req.TenantID,
			State:              StateActive,
			OriginalGoal:       "Goal from session",
			LastActivityAt:     time.Now(),
			CreatedAt:          time.Now(),
			SubAgentsTotal:     req.SubAgentsTotal,
			SubAgentsCompleted: req.SubAgentsCompleted,
			SubAgentsPending:   req.SubAgentsPending,
		}
		if err := h.db.CreateSession(ctx, goalSession); err != nil {
			slog.Warn("failed to create goal session", "error", err)
			return nil, nil
		}
	}

	// Check if task is completed. The completion detector runs over the
	// response body, so it works whether finish_reason is "stop", "length",
	// or even "tool_calls" (structured status field). Completion short-circuits
	// the auto-continue path: a finished task should be audited, not nudged.
	//
	// Resolve the tenant's configured completion-confidence threshold per
	// request and pass it explicitly to the detector. The detector is a
	// singleton on ChatHandler, so any per-instance threshold state would race
	// between concurrent tenants — passing it as an argument keeps each
	// tenant's verdict isolated.
	minConfidence := h.loadFloat(req.TenantID, "goal.completion_confidence", h.config.CompletionConfidence)
	completed, confidence, reason := h.detector.IsCompletedWithSubAgents(ctx, req, minConfidence, goalSession.SubAgentsPending)
	if completed {
		slog.Info("task_completed", "session_id", req.SessionID, "confidence", confidence, "reason", reason)
		// CompareAndSetState guards against the loop-exhaustion / provider-retry
		// paths racing in to overwrite "completed" with "failed". A concurrent
		// failure write simply no-ops; the task stays marked completed.
		if won, err := h.db.CompareAndSetState(ctx, req.TenantID, req.SessionID, allowedNonTerminalStates(), StateCompleted); err != nil {
			slog.Warn("goal_completion_state_persist_failed", "session_id", req.SessionID, "tenant_id", req.TenantID, "error", err)
		} else if !won {
			slog.Info("goal_completion_state_already_terminal", "session_id", req.SessionID, "tenant_id", req.TenantID)
		}
		h.observeOutcome(ctx, Outcome{
			Kind: OutcomeCompleted, SessionID: req.SessionID, TenantID: req.TenantID,
			Reason: reason, Source: "completion_detector", RetryCount: goalSession.RetryCount,
			RepeatCount: goalSession.RepeatCount, ModelSwitchCount: goalSession.ModelSwitchCount,
		})

		if h.loadBool(req.TenantID, "goal.audit_enabled", h.config.UseAudit) {
			return h.triggerAudit(ctx, req, goalSession)
		}
		return nil, nil
	}

	// Record this response for repeat-detection, then decide what to do next.
	// The decision may be: continue on the same model, rotate to a fallback
	// model and continue with a fresh budget, or give up (both budgets spent).
	// We already ran IsCompleted above and know the task is NOT done, so pass
	// that down to avoid a duplicate LLM judgement call on every stop turn.
	return h.decideAndContinue(ctx, req, goalSession, true)
}

// decideAndContinue is the unified continue/loop-handling entry point shared by
// the streaming and non-streaming interception paths. It returns a follow-up
// result (continue or model-switched continue) or nil when the session should
// stop being nudged.
//
// Flow:
//  1. Record the response hash (updates RepeatCount).
//  2. If the turn is unfinished (length/empty/stop-but-not-done) AND we have
//     continue budget on the current model → continue on the same model.
//  3. Else if a loop is detected (budget exhausted or repeating) AND model
//     switching is available → rotate to a fallback model, reset the continue
//     budget, and continue on the new model.
//  4. Else → give up (the client/operator can inspect the stuck session).
//
// alreadyKnownIncomplete: when true, the caller already ran IsCompleted and got
// false, so shouldAutoContinue skips its own completion re-check for the "stop"
// finish reason — avoiding a duplicate LLM judgement call on every stop turn.
func (h *ModeHook) decideAndContinue(ctx context.Context, req *response.InterceptRequest, sess *Session, alreadyKnownIncomplete bool) (*response.InterceptResult, error) {
	// Always record the response so repeat-detection stays current, even when
	// we ultimately decide not to continue.
	decision := h.recordAndDetect(ctx, sess, req)

	// Path 1: still within budget and the turn looks unfinished → normal continue.
	if decision.canContinue && h.shouldAutoContinue(ctx, req, sess, req.FinishReason, alreadyKnownIncomplete) {
		if signal := h.tryBuildClientSignal(ctx, req, sess); signal != nil {
			return signal, nil
		}
		if followUp := h.tryAtomicContinue(ctx, req, ""); followUp != nil {

			return followUp, nil
		}
		return nil, nil
	}

	// Path 2: loop detected and we have a model to rotate to.
	if decision.switchModel != "" {
		if h.applyModelSwitch(ctx, req, sess, decision.switchModel) {
			slog.Info("goal_model_switched",
				"session_id", req.SessionID,
				"new_model", decision.switchModel,
				"switch_count", sess.ModelSwitchCount,
				"reason", decision.reason)
			h.observeOutcome(ctx, Outcome{
				Kind: OutcomeDegraded, SessionID: req.SessionID, TenantID: req.TenantID,
				Reason: "model_switched", Source: "loop_detector", RetryCount: sess.RetryCount,
				RepeatCount: sess.RepeatCount, ModelSwitchCount: sess.ModelSwitchCount,
				MaxModelSwitchCount: h.loadInt(req.TenantID, "goal.max_model_switch_count", h.config.MaxModelSwitchCount),
			})
			// Build the follow-up targeting the new model WITHOUT consuming a
			// continue-budget slot: applyModelSwitch already reset the count to
			// 0, so this rotation IS the new model's first attempt. tryAtomicContinue
			// would bump the count to 1 immediately, leaving the new model with
			// max-1 effective budget — wrong.
			return &response.InterceptResult{
				InjectFollowUp: h.buildContinueMessage(ctx, req, decision.switchModel),
				Action:         "goal_model_switch",
			}, nil
		}
	}

	if decision.giveUp {
		slog.Info("goal_giving_up",
			"session_id", req.SessionID,
			"reason", decision.reason,
			"auto_continue_count", sess.AutoContinueCount,
			"model_switch_count", sess.ModelSwitchCount)
		// CompareAndSetState preserves a concurrently-set terminal state. If a
		// completion verdict already won the race, the failed write is dropped
		// (logged below) — completed is sticky and audit will follow.
		if won, err := h.db.CompareAndSetState(ctx, req.TenantID, req.SessionID, allowedNonTerminalStates(), StateFailed); err != nil {
			slog.Warn("goal_failed_state_persist_failed", "session_id", req.SessionID, "error", err)
		} else if !won {
			slog.Info("goal_failed_state_skipped_terminal", "session_id", req.SessionID, "tenant_id", req.TenantID)
		}
		h.observeOutcome(ctx, Outcome{
			Kind: OutcomeFailed, SessionID: req.SessionID, TenantID: req.TenantID,
			Reason: decision.reason, Source: "loop_detector", RetryCount: sess.RetryCount,
			RepeatCount: sess.RepeatCount, ModelSwitchCount: sess.ModelSwitchCount,
			MaxModelSwitchCount: h.loadInt(req.TenantID, "goal.max_model_switch_count", h.config.MaxModelSwitchCount),
		})
	}
	return nil, nil
}

// shouldAutoContinue reports whether the current turn should trigger an
// automatic "please continue" follow-up. It encodes the policy described above
// and is shared by the streaming and non-streaming interception paths so the
// two stay in sync.
// alreadyKnownIncomplete: when true the caller confirmed the task is not done,
// so the "stop" branch trusts that and skips re-running IsCompleted (which
// would repeat the LLM judgement call). False = legacy path that re-checks.
func (h *ModeHook) shouldAutoContinue(ctx context.Context, req *response.InterceptRequest, goalSession *Session, finishReason string, alreadyKnownIncomplete bool) bool {
	autoContinue := h.loadBool(req.TenantID, "goal.auto_continue_on_pause", h.config.AutoContinueOnPause)
	if !autoContinue {
		return false
	}
	maxContinue := h.loadInt(req.TenantID, "goal.max_auto_continue_count", h.config.MaxAutoContinueCount)
	if goalSession.AutoContinueCount >= maxContinue {
		slog.Debug("goal_continue_limit_reached",
			"session_id", req.SessionID,
			"count", goalSession.AutoContinueCount,
			"max", maxContinue)
		return false
	}

	switch finishReason {
	case "length", "":
		// Truncated or unknown — always continue (the model ran out of room).
		return true
	case "stop":
		// Model ended cleanly. If the caller already confirmed the task is NOT
		// done, continue without re-checking (avoid a duplicate LLM call).
		if alreadyKnownIncomplete {
			return true
		}
		// Otherwise re-check completion so we don't loop forever nudging a
		// finished task (legacy stream path only). Threshold is resolved per
		// request; see InterceptNonStream for the rationale.
		minConfidence := h.loadFloat(req.TenantID, "goal.completion_confidence", h.config.CompletionConfidence)
		completed, _, _ := h.detector.IsCompleted(ctx, req, minConfidence)
		if completed {
			return false
		}
		return true
	default:
		// "tool_calls" (model wants to invoke a tool — let the client drive),
		// or other reasons — do not inject a follow-up.
		return false
	}
}

// tryAtomicContinue performs the atomic increment-and-continue dance and, on
// success, returns the goal_continue follow-up. Returns nil when the continue
// budget was already consumed by a concurrent request.
//
// modelOverride routes the follow-up to a specific model (used after a model
// rotation); empty means use the original client model.
func (h *ModeHook) tryAtomicContinue(ctx context.Context, req *response.InterceptRequest, modelOverride string) *response.InterceptResult {
	maxContinue := h.loadInt(req.TenantID, "goal.max_auto_continue_count", h.config.MaxAutoContinueCount)
	won, err := h.db.AtomicAutoContinue(ctx, req.TenantID, req.SessionID, maxContinue)
	if err != nil {
		slog.Warn("atomic_auto_continue_failed", "error", err, "session_id", req.SessionID)
		_ = h.db.IncrementAutoContinueCount(ctx, req.TenantID, req.SessionID)
		won = true
	}
	if !won {
		return nil
	}
	return &response.InterceptResult{
		InjectFollowUp: h.buildContinueMessage(ctx, req, modelOverride),
		Action:         "goal_continue",
	}
}

func (h *ModeHook) tryBuildClientSignal(ctx context.Context, req *response.InterceptRequest, sess *Session) *response.InterceptResult {
	if !h.clientSignalEnabled(req.TenantID) || !req.ClientSignalAllowed {
		return nil
	}
	mode := h.loadString(req.TenantID, "goal.client_signal_mode", h.config.ClientSignalMode)
	kind := "gw-continue"
	if req.HandoffSignalAllowed && req.TokensUsed >= h.loadInt(req.TenantID, "goal.handoff_signal_threshold_tokens", h.config.HandoffSignalThresholdTokens) && (mode == "auto" || mode == "handoff" || mode == "both") {
		kind = "gw-handoff"
	} else if mode == "handoff" {
		return nil
	}
	if kind == "gw-continue" && !(mode == "" || mode == "auto" || mode == "continue" || mode == "both") {
		return nil
	}
	max := h.loadInt(req.TenantID, "goal.max_auto_continue_count", h.config.MaxAutoContinueCount)
	attempt := sess.ContinueAttempt
	if kind == "gw-continue" {
		var claimed bool
		var err error
		if signalStore, ok := h.db.(GoalClientSignalStore); ok {
			attempt, claimed, err = signalStore.ClaimContinueAttempt(ctx, req.TenantID, req.SessionID, max)
		} else {
			return nil
		}
		if err != nil || !claimed {
			return nil
		}
	}
	payload := map[string]interface{}{
		"type": "gw_continue", "version": 1, "reason": "goal_incomplete",
		"request_id": req.RequestID, "session_id": req.SessionID, "attempt": attempt,
		"max_attempts": max, "hint": "请继续下一步", "tokens_used": req.TokensUsed,
		"context_window": req.ContextWindow, "sub_agents_pending": sess.SubAgentsPending,
	}
	if kind == "gw-handoff" {
		payload["type"] = "gw_handoff"
		payload["reason"] = "context_near_limit"
		payload["handoff_threshold"] = h.loadInt(req.TenantID, "goal.handoff_signal_threshold_tokens", h.config.HandoffSignalThresholdTokens)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return &response.InterceptResult{ClientSignalKind: kind, ClientSignalPayload: body, ClientSignalAttempts: attempt, Action: kind}
}

func (h *ModeHook) clientSignalEnabled(tenantID string) bool {
	return h.loadBool(tenantID, "goal.client_signal_enabled", h.config.ClientSignalEnabled)
}

func (h *ModeHook) InterceptStreamChunk(ctx context.Context, chunk []byte, meta *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

// InterceptStreamEnd handles goal mode logic after a stream completes.
//
// When the caller (handler.go) reassembles the streamed chunks into a full
// response body, it populates meta.ResponseBody and meta.FinishReason. With
// those, this path mirrors InterceptNonStream: run completion detection, and
// if the task isn't done, inject a "please continue" follow-up. When the body
// is NOT available (older callers), it falls back to the legacy length-based
// continue behaviour so we don't regress.
func (h *ModeHook) InterceptStreamEnd(ctx context.Context, meta *response.StreamMeta) (*response.EndResult, error) {
	if strings.HasPrefix(meta.FollowUpAction, "audit") {
		return nil, nil
	}
	enabled := h.loadBool(meta.TenantID, "goal.enabled", h.config.Enabled)
	if !enabled {
		return nil, nil
	}

	goalSession, err := h.db.GetSession(ctx, meta.TenantID, meta.SessionID)
	if err != nil || goalSession == nil {
		return nil, nil
	}

	req := &response.InterceptRequest{
		SessionID:            meta.SessionID,
		RequestID:            meta.RequestID,
		TenantID:             meta.TenantID,
		ClientModel:          meta.ClientModel,
		ResponseBody:         meta.ResponseBody,
		FinishReason:         meta.FinishReason,
		IsStreaming:          true,
		TokensUsed:           meta.TokensUsed,
		ContextWindow:        meta.ContextWindow,
		MessageCount:         meta.MessageCount,
		FollowUpAction:       meta.FollowUpAction,
		ClientSignalAllowed:  meta.ClientSignalAllowed,
		HandoffSignalAllowed: meta.HandoffSignalAllowed,
	}

	// If we have a reassembled body, treat this exactly like the non-stream
	// completion path: detect completion, otherwise hand off to the unified
	// decideAndContinue (which also handles model switching on loops).
	if len(meta.ResponseBody) > 0 {
		minConfidence := h.loadFloat(meta.TenantID, "goal.completion_confidence", h.config.CompletionConfidence)
		completed, confidence, reason := h.detector.IsCompletedWithSubAgents(ctx, req, minConfidence, goalSession.SubAgentsPending)
		if completed {
			slog.Info("task_completed_stream", "session_id", meta.SessionID, "confidence", confidence, "reason", reason)
			if won, _ := h.db.CompareAndSetState(ctx, meta.TenantID, meta.SessionID, allowedNonTerminalStates(), StateCompleted); !won {
				slog.Info("goal_completion_state_already_terminal", "session_id", meta.SessionID, "tenant_id", meta.TenantID, "source", "stream")
			}
			h.observeOutcome(ctx, Outcome{
				Kind: OutcomeCompleted, SessionID: meta.SessionID, TenantID: meta.TenantID,
				Reason: reason, Source: "completion_detector", RetryCount: goalSession.RetryCount,
				RepeatCount: goalSession.RepeatCount, ModelSwitchCount: goalSession.ModelSwitchCount,
			})
			// Audit is triggered on the non-stream follow-up path; for the
			// stream path we also return an audit follow-up so the audit runs.
			if h.loadBool(meta.TenantID, "goal.audit_enabled", h.config.UseAudit) {
				auditResult := h.triggerAuditEndResult(ctx, meta, goalSession)
				return auditResult, nil
			}
			return nil, nil
		}
		// Body path already ran IsCompleted and got false → known-incomplete.
		if res, _ := h.decideAndContinue(ctx, req, goalSession, true); res != nil {
			return &response.EndResult{
				InjectFollowUp:       res.InjectFollowUp,
				Action:               res.Action,
				Metadata:             res.Metadata,
				ClientSignalKind:     res.ClientSignalKind,
				ClientSignalPayload:  res.ClientSignalPayload,
				ClientSignalAttempts: res.ClientSignalAttempts,
			}, nil
		}
		return nil, nil
	}

	// Legacy fallback: no reassembled body — only continue on length/empty,
	// matching the pre-enhancement behaviour.
	finishReason := meta.FinishReason
	if finishReason != "length" && finishReason != "" {
		return nil, nil
	}
	// Legacy fallback has no body so completion wasn't pre-checked.
	if !h.shouldAutoContinue(ctx, req, goalSession, finishReason, false) {
		return nil, nil
	}
	if followUp := h.tryAtomicContinue(ctx, req, ""); followUp != nil {
		return &response.EndResult{
			InjectFollowUp: followUp.InjectFollowUp,
			Action:         followUp.Action,
		}, nil
	}
	return nil, nil
}

// triggerAuditEndResult is the stream-end variant of triggerAudit: it builds
// the same audit follow-up but returns an EndResult (the shape
// InterceptStreamEnd yields) instead of an InterceptResult.
func (h *ModeHook) triggerAuditEndResult(ctx context.Context, meta *response.StreamMeta, session *Session) *response.EndResult {
	req := &response.InterceptRequest{
		SessionID:    meta.SessionID,
		RequestID:    meta.RequestID,
		TenantID:     meta.TenantID,
		ClientModel:  meta.ClientModel,
		ResponseBody: meta.ResponseBody,
	}
	res, err := h.triggerAudit(ctx, req, session)
	if err != nil || res == nil {
		return nil
	}
	return &response.EndResult{
		InjectFollowUp: res.InjectFollowUp,
		Action:         res.Action,
		Metadata:       res.Metadata,
	}
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

// buildContinueMessage constructs a continuation request. When modelOverride
// is non-empty the follow-up is sent to that model (used after a model
// rotation); otherwise it targets the original client model.
func (h *ModeHook) buildContinueMessage(ctx context.Context, req *response.InterceptRequest, modelOverride string) []byte {
	prompts := NewPromptsFromContext(ctx) // locale resolved from request context
	model := req.ClientModel
	if modelOverride != "" {
		model = modelOverride
	}
	reqBody := map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompts.ContinueNextStep()}},
		"stream":   false,
	}
	body, _ := json.Marshal(reqBody)
	return body
}

// triggerAudit initiates audit using autoroute.
func (h *ModeHook) triggerAudit(ctx context.Context, req *response.InterceptRequest, session *Session) (*response.InterceptResult, error) {
	model := h.loadString(req.TenantID, "goal.fallback_audit_model", h.config.FallbackAuditModel)
	if h.loadBool(req.TenantID, "goal.use_autoroute_for_audit", h.config.UseAutorouteForAudit) {
		model = "auto"
	}

	prompts := NewPromptsFromContext(ctx) // locale resolved from request context
	auditPrompt := prompts.AuditStarted() + "\n" + `Review the task execution and return JSON:
{"issues": [{"severity": "high/medium/low", "description": "...", "fix": "..."}], "summary": "Overall assessment"}`

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

func (h *ModeHook) loadFloat(tenantID, key string, def float64) float64 {
	if h.config.SettingsGetter != nil {
		return h.config.SettingsGetter.GetFloat(tenantID, key, def)
	}
	return def
}

func (h *ModeHook) loadString(tenantID, key string, def string) string {
	if h.config.SettingsGetter != nil {
		return h.config.SettingsGetter.GetString(tenantID, key, def)
	}
	return def
}
