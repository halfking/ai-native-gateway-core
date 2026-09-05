package main

// goal_control.go — wires the goal/audit response interceptors into the
// streaming ChatHandler.
//
// This is the "last mile" connection that activates the goal feature. The
// hook implementations (domains/hooks/goal) and the interceptor plumbing
// (domains/hooks/response + streaming.response_interceptor_helpers) already
// exist; this file just constructs them with the right stores/caller and
// hands the chain to ChatHandler.SetResponseInterceptor.
//
// Safety: every layer defaults to disabled, so building + running this code
// has zero effect unless an operator opts in via:
//   - LLM_GATEWAY_GOAL_ENABLED=true (or goal.enabled setting, platform-scoped),
//     AND
//   - LLMGatewayAutoLLMEndpoint=... (so the completion-detection/audit LLM
//     judgement calls have somewhere to go)
// The follow-up engine itself enforces MaxFollowUpDepth / MaxFollowUpsPerSession
// regardless of configuration.

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/handoff"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
)

// settingsAdapter adapts the global settings registry to goal.SettingsGetter.
// It reads tenant-scoped values (goal.* / handoff.*) and falls back to the
// provided default when unset or unparseable.
type settingsAdapter struct{}

func (settingsAdapter) settingScope(key string) settings.Scope {
	if spec := settings.Global.Spec(key); spec != nil {
		return spec.Scope
	}
	return settings.ScopeTenant
}

func (a settingsAdapter) GetBool(tenantID, key string, def bool) bool {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(a.settingScope(key), key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out bool
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

func (a settingsAdapter) GetInt(tenantID, key string, def int) int {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(a.settingScope(key), key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out int
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

func (a settingsAdapter) GetFloat(tenantID, key string, def float64) float64 {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(a.settingScope(key), key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out float64
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

func (a settingsAdapter) GetString(tenantID, key string, def string) string {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(a.settingScope(key), key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out string
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

// initGoalControl constructs the goal + audit + handoff interceptor chain and
// registers it on the chat handler. It also registers the goal/handoff
// configuration specs with the global settings registry so the goal.* /
// handoff.* keys resolve.
//
// Safe to call when db is nil (no-op). The feature still honours the
// goal.enabled flag at runtime via settingsAdapter, so even with the chain
// installed the hooks are inert until enabled.
//
// NOTE: This function is only invoked in non-data-plane mode (see
// cmd/gateway/main.go). The settings specs alone are registered
// unconditionally via registerAutoControlSettings so admins can configure
// handoff.* via the UI even in data-plane mode (where the runtime hook is
// not wired).
func initGoalControl(db *sql.DB, chatHandler *streaming.ChatHandler) {
	if db == nil {
		slog.Info("goal_control: disabled (no DB)")
		return
	}

	// 1. Register configuration specs (idempotent).
	for _, spec := range settings.AutoControlSpecs() {
		s := spec // take a stable address
		if err := settings.Global.RegisterSpec(&s); err != nil {
			// Already-registered is fine (e.g. restart); only warn on real errors.
			slog.Debug("goal_control: spec register skipped",
				"key", spec.Key, "error", err)
		}
	}

	// 2. Build the stores.
	goalStore := goal.NewPGStore(db)
	historyStore := goal.NewPGHistoryStore(db)

	// 3. Build the LLM caller used by completion detection + audit. Reuse the
	// same endpoint/key wiring as autoroute (LLMGatewayAutoLLM* env vars). When
	// no endpoint is configured, install a no-op caller — the keyword/heuristic
	// completion strategies still work, and audit simply skips its LLM step.
	caller := buildGoalLLMCaller()

	// 4. Settings adapter shared by both hooks.
	adapter := settingsAdapter{}

	// 5. Goal retry policy resolver (2026-07-23)
	retryResolver := newGoalRetryPolicyResolver(adapter)

	// 5. Goal mode hook: drives activation, completion detection, and the
	//    "please continue" auto-follow-up, including model switching on loops.
	//
	//    Cost Mode Integration (2026-07-19, Phase 0):
	//    The config below is seeded from environment variables BUT can be
	//    overridden by cost_mode presets at runtime (per-tenant). The preset
	//    system (docs/会话优化v2/18-Goal模式成本控制与分级方案.md) lets
	//    users select "minimal", "balanced", or "aggressive" instead of tuning
	//    dozens of individual knobs.
	//
	//    At boot, we infer the cost mode from env vars for backward compat
	//    (if LLM_GATEWAY_GOAL_AUTO_FIX=true → aggressive, etc.). At runtime,
	//    ModeHook.loadCostMode reads the tenant's goal.cost_mode setting and
	//    applies the full preset.
	//
	//    Env var precedence (for explicit overrides by advanced users):
	//      1. Explicit goal.cost_mode setting (tenant-scoped)
	//      2. Inferred mode from legacy env vars (boot-time fallback)
	//      3. Individual env var overrides (LLM_GATEWAY_GOAL_MAX_RETRY, etc.)
	//
	// Infer cost mode from env vars (backward compat).
	inferredCostMode := goal.InferCostMode(
		getEnv("LLM_GATEWAY_GOAL_COST_MODE", ""),
		getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false),
		getEnvBool("LLM_GATEWAY_GOAL_AUTO_CONTINUE", false),
		getEnvBool("LLM_GATEWAY_GOAL_RETRY_ON_ERROR", false),
	)

	// Load the preset as the baseline configuration.
	preset := goal.GetPreset(inferredCostMode)

	// Build config: preset defaults + env var overrides.
	goalCfg := goal.ModeConfig{
		Enabled:               getEnvBool("LLM_GATEWAY_GOAL_ENABLED", false),
		DetectionMode:         goal.DetectionMode(getEnv("LLM_GATEWAY_GOAL_DETECTION_MODE", preset.DetectionMode)),
		AutoSelectRecommended: getEnvBool("LLM_GATEWAY_GOAL_AUTO_SELECT", true),

		// Retry settings from preset (overridable by env)
		RetryOnError:      getEnvBool("LLM_GATEWAY_GOAL_RETRY_ON_ERROR", preset.RetryEnabled),
		MaxRetryCount:     getEnvInt("LLM_GATEWAY_GOAL_MAX_RETRY", preset.MaxRetryCount),
		RetryDelaySeconds: getEnvInt("LLM_GATEWAY_GOAL_RETRY_DELAY_SECONDS", preset.RetryDelaySeconds),
		RetryTotalTimeout: getEnvInt("LLM_GATEWAY_GOAL_RETRY_TOTAL_TIMEOUT", preset.RetryTotalTimeout),

		// Auto-continue settings from preset
		AutoContinueOnPause:  getEnvBool("LLM_GATEWAY_GOAL_AUTO_CONTINUE", preset.AutoContinue),
		MaxAutoContinueCount: getEnvInt("LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE", preset.MaxContinueCount),
		CompletionConfidence: getEnvFloat("LLM_GATEWAY_GOAL_COMPLETION_CONFIDENCE", preset.CompletionConfidence),

		// Client-driven control signals remain opt-in on both sides: the
		// tenant setting / env default enables production behavior, while
		// X-Gw-Capabilities authorizes it per request.
		ClientSignalEnabled:          getEnvBool("LLM_GATEWAY_GOAL_CLIENT_DRIVEN", false),
		ClientSignalMode:             getEnv("LLM_GATEWAY_GOAL_CLIENT_SIGNAL_MODE", "auto"),
		HandoffSignalThresholdTokens: getEnvInt("LLM_GATEWAY_GOAL_HANDOFF_SIGNAL_THRESHOLD", 200000),

		// Audit/Fix settings from preset
		UseAudit:             getEnvBool("LLM_GATEWAY_GOAL_AUDIT_ENABLED", preset.UseAudit),
		UseAutorouteForAudit: getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT", preset.UseAutorouteAudit),

		AutoFixEnabled: getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", preset.AutoFixEnabled),

		// Loop detection from preset
		ModelSwitchOnLoop:      getEnvBool("LLM_GATEWAY_GOAL_MODEL_SWITCH_ON_LOOP", preset.LoopDetectionEnabled),
		MaxModelSwitchCount:    getEnvInt("LLM_GATEWAY_GOAL_MAX_MODEL_SWITCH", preset.MaxModelSwitch),
		RepeatDetectionEnabled: getEnvBool("LLM_GATEWAY_GOAL_REPEAT_DETECTION", preset.LoopDetectionEnabled),
		RepeatThreshold:        getEnvInt("LLM_GATEWAY_GOAL_REPEAT_THRESHOLD", preset.LoopThreshold),

		// Budget limits from preset
		MonthlyTokenLimit:  getEnvInt("LLM_GATEWAY_GOAL_MONTHLY_TOKEN_LIMIT", preset.MonthlyTokenLimit),
		SessionTokenBudget: getEnvInt("LLM_GATEWAY_GOAL_SESSION_TOKEN_BUDGET", preset.SessionTokenBudget),
		CostAlertThreshold: getEnvFloat("LLM_GATEWAY_GOAL_COST_ALERT_THRESHOLD", preset.CostAlertThreshold),
		DowngradeOnBudget:  getEnvBool("LLM_GATEWAY_GOAL_DOWNGRADE_ON_BUDGET", preset.DowngradeOnBudget),

		// Legacy settings (not in preset)
		UseAutorouteForIntent:  getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_INTENT", true),
		FallbackAuditModel:     getEnv("LLM_GATEWAY_GOAL_FALLBACK_AUDIT_MODEL", "auto"),
		FallbackModels:         parseModelList(getEnv("LLM_GATEWAY_GOAL_FALLBACK_MODELS", "")),
		RepeatResetOnProgress:  true,
		MaxFollowUpDepth:       getEnvInt("LLM_GATEWAY_GOAL_MAX_FOLLOW_UP_DEPTH", 15),
		MaxFollowUpsPerSession: getEnvInt("LLM_GATEWAY_GOAL_MAX_FOLLOW_UPS_PER_SESSION", 50),

		SettingsGetter: adapter,
	}

	slog.Info("goal_control: cost_mode configured",
		"inferred_mode", inferredCostMode,
		"retry_enabled", goalCfg.RetryOnError,
		"max_retry", goalCfg.MaxRetryCount,
		"auto_continue", goalCfg.AutoContinueOnPause,
		"max_continue", goalCfg.MaxAutoContinueCount,
		"auto_fix", goalCfg.AutoFixEnabled,
		"monthly_limit", goalCfg.MonthlyTokenLimit,
	)

	// Enforce the budget-exhaustion invariant: the loop detector's
	// budgetExhausted branch only fires when MaxFollowUpDepth can accommodate
	// MaxAutoContinueCount × (MaxModelSwitchCount + 1). Otherwise the
	// depth-guardrail truncates the loop before budget exhaustion, and the
	// model-switch fallback never fires (see 903dc8b4d follow-up audit).
	//
	// The adjustment mutates a copy so per-tenant runtime overrides remain
	// authoritative inside the hook; the boot value is only the baseline.
	enforceFollowUpDepthBudgetInvariant(&goalCfg)

	// Apply the follow-up engine limits so the loop guardrails honour the
	// goal config from boot. Per-tenant runtime overrides still apply inside
	// the hook via settings; this just sets a sane process-wide default.
	streaming.SetFollowUpLimits(goalCfg.MaxFollowUpDepth, goalCfg.MaxFollowUpsPerSession)

	goalHook := goal.NewModeHookWithHistory(goalCfg, goalStore, caller, historyStore)

	// 6. Audit hook: runs after completion, uses the full conversation
	//    transcript (historyStore) and a separate audit model.
	auditCfg := goal.AuditConfig{
		Enabled:        goalCfg.UseAudit,
		UseAutoroute:   goalCfg.UseAutorouteForAudit,
		FallbackModel:  goalCfg.FallbackAuditModel,
		AutoFixEnabled: goalCfg.AutoFixEnabled,
		MinConfidence:  0.7,
		SettingsGetter: adapter,
	}
	auditHook := goal.NewAuditHookWithHistory(goalStore, caller, auditCfg, goalHook.History())

	// 6b. Handoff hook: triggers when a session approaches its context
	//     window, injecting a handoff skill invocation to start a fresh
	//     context. Placed LAST in the chain below so that when both it and
	//     goal mode fire on the same response, the handoff follow-up wins
	//     (InterceptResult.InjectFollowUp is last-writer-wins): rotating to
	//     a new session takes priority over nudging a near-full context.
	//     Defaults to disabled (LLM_GATEWAY_HANDOFF_ENABLED=false) — inert
	//     unless an operator opts in.
	//
	// Restored 2026-07-09: was parked at _to-be-deprecated/hooks-handoff-20260706/
	// because the original SQL referenced a non-existent `sessions` master
	// table. The replacement uses session_summaries + adds 8 new tunable
	// settings (summary engine, model, cooldown, max_per_session, etc.).
	handoffStore := handoff.NewPGStore(db)
	goalHandoffTrigger := handoff.NewMemoryHandoffTrigger(5*time.Minute, goalStore)
	goalStateSerializer := handoff.NewMemoryGoalStateSerializer(goalStore)
	goalHook.SetOutcomeObserver(goalHandoffTrigger)
	handoffCfg := handoff.TriggerConfig{
		Enabled:             getEnvBool("LLM_GATEWAY_HANDOFF_ENABLED", false),
		TriggerMode:         handoff.TriggerMode(getEnv("LLM_GATEWAY_HANDOFF_TRIGGER_MODE", "auto")),
		AbsoluteThreshold:   getEnvInt("LLM_GATEWAY_HANDOFF_ABSOLUTE_THRESHOLD", 180000),
		PercentageThreshold: getEnvFloat("LLM_GATEWAY_HANDOFF_PERCENTAGE_THRESHOLD", 0.8),
		MessageThreshold:    getEnvInt("LLM_GATEWAY_HANDOFF_MESSAGE_THRESHOLD", 0),
		IdleMinutes:         getEnvInt("LLM_GATEWAY_HANDOFF_IDLE_MINUTES", 0),
		MinMessages:         getEnvInt("LLM_GATEWAY_HANDOFF_MIN_MESSAGES", 10),
		SkillName:           getEnv("LLM_GATEWAY_HANDOFF_SKILL_NAME", "handoff"),
		SummaryEngine:       handoff.SummaryEngine(getEnv("LLM_GATEWAY_HANDOFF_SUMMARY_ENGINE", "llm")),
		SummaryModel:        getEnv("LLM_GATEWAY_HANDOFF_SUMMARY_MODEL", ""),
		KeepRecentN:         getEnvInt("LLM_GATEWAY_HANDOFF_SUMMARY_KEEP_RECENT_N", 4),
		MaxSummaryTokens:    getEnvInt("LLM_GATEWAY_HANDOFF_SUMMARY_MAX_TOKENS", 2000),
		SummaryPromptTpl:    getEnv("LLM_GATEWAY_HANDOFF_SUMMARY_PROMPT_TPL", ""),
		ExtractFacts:        getEnvBool("LLM_GATEWAY_HANDOFF_SUMMARY_EXTRACT_FACTS", true),
		CooldownSeconds:     getEnvInt("LLM_GATEWAY_HANDOFF_COOLDOWN_SECONDS", 60),
		MaxPerSession:       getEnvInt("LLM_GATEWAY_HANDOFF_MAX_PER_SESSION", 5),
		RetryOnFailure:      getEnvInt("LLM_GATEWAY_HANDOFF_RETRY_ON_FAILURE", 1),
		NotifyLevel:         handoff.NotifyLevel(getEnv("LLM_GATEWAY_HANDOFF_NOTIFY_LEVEL", "warn")),
		NotifyWebhook:       getEnv("LLM_GATEWAY_HANDOFF_NOTIFY_WEBHOOK", ""),
		ContinueHintTpl:     getEnv("LLM_GATEWAY_HANDOFF_CONTINUE_HINT_TPL", ""),
		ContextMonitor:      handoff.NewMemoryContextMonitor(0, 0),
		GoalStateSerializer: goalStateSerializer,
		GoalTrigger:         goalHandoffTrigger,
		MessageBuilder:      handoff.NewMemoryHandoffMessageBuilder(0),
		GoalCostMode: func(tenantID string) string {
			return adapter.GetString(tenantID, "goal.cost_mode", string(inferredCostMode))
		},
		SettingsGetter: adapter,
		// LLMCaller shared with goal mode — reuses LLMGatewayAutoLLM* env vars
		// via the same HTTPLlmCallerConfig. When no endpoint is configured,
		// the hook auto-degrades to rule-based extraction.
		LLMCaller: buildHandoffLLMCaller(),
	}
	handoffHook := handoff.NewTriggerHook(handoffCfg, handoffStore)

	// 7. Handoff runs before provider dispatch so a threshold hit rewrites the
	// current request and rotates the real gateway session. Keep it out of the
	// response chain: response-side follow-ups duplicate an already completed
	// turn and cannot change the client-visible session.
	chatHandler.SetHandoffHook(handoffHook)
	interceptors := []response.ResponseInterceptor{goalHook, auditHook}

	// 7b. Output compliance interceptor（输出合规/脱敏，2026-07-09）。
	// 用同一 *sql.DB 构造 checker；ownerFn 从 session_dim 查询 dataOwner +
	// 从最新 request_log 的 api_key_owner_user 取 callerOwner。
	// db 为 nil 时 buildOutputComplianceInterceptor 返回 nil，链自动跳过。
	if ocHook := buildOutputComplianceInterceptor(db); ocHook != nil {
		interceptors = append(interceptors, ocHook)
	}

	// SmartSaniGuard 的输入/输出接入已迁移到 installSmartSaniGuard
	// （在 initGoalControl 返回后由 main.go 显式调用），不再受本函数
	// 的 db!=nil gate 限制——data-plane 模式（bgDataPlaneOnly=true）
	// 下也能启用脱敏能力。

	chain := response.NewInterceptorChain(interceptors...)
	chatHandler.SetResponseInterceptor(chain)

	// 7a. Handoff fallback API key (2026-07-11, handoff self-call fix).
	chatHandler.SetHandoffFallbackAPIKey(strings.TrimSpace(os.Getenv("LLM_GATEWAY_HANDOFF_FALLBACK_API_KEY")))

	// 7b. Wire Goal retry policy resolver and recorder (2026-07-23)
	chatHandler.SetGoalRetryPolicyResolver(retryResolver)
	chatHandler.SetGoalRetryRecorder(goalStore)
	chatHandler.SetGoalOutcomeObserver(goalHandoffTrigger)

	handoffEnabled := handoffCfg.Enabled
	ocEnabled := len(interceptors) > 3
	slog.Info("goal_control: interceptors installed",
		"goal_enabled", goalCfg.Enabled,
		"detection_mode", goalCfg.DetectionMode,
		"audit_enabled", auditCfg.Enabled,
		"handoff_enabled", handoffEnabled,
		"handoff_trigger_mode", string(handoffCfg.TriggerMode),
		"handoff_summary_engine", string(handoffCfg.SummaryEngine),
		"output_compliance_enabled", ocEnabled,
		"model_switch_on_loop", goalCfg.ModelSwitchOnLoop,
		"max_model_switch", goalCfg.MaxModelSwitchCount,
		"fallback_models", goalCfg.FallbackModels,
		"repeat_threshold", goalCfg.RepeatThreshold,
		"completion_confidence", goalCfg.CompletionConfidence,
		"llm_caller_configured", llmCallerConfigured(),
	)
}

// minBudgetExhaustionMargin is the slack added on top of the strict
// max-auto-continue × (max-model-switch + 1) bound when the boot invariant
// lifts MaxFollowUpDepth. It guarantees a small buffer for follow-up
// messages that aren't continue-budget events (e.g. handoff follow-ups),
// so the depth guardrail still terminates deterministically after at most
// (continue slots + margin) iterations.
const minBudgetExhaustionMargin = 3

// enforceFollowUpDepthBudgetInvariant adjusts cfg.MaxFollowUpDepth upward
// when it is strictly less than MaxAutoContinueCount × (MaxModelSwitchCount+1).
//
// Why this exists:
//   - loop_detector.go declares budgetExhausted=true once the continue counter
//     reaches MaxAutoContinueCount, then triggers applyModelSwitch.
//   - Each model-switch resets the continue counter to zero and rolls forward
//     to the next fallback model. The full chain therefore consumes up to
//     MaxAutoContinueCount × (MaxModelSwitchCount + 1) follow-up slots.
//   - If MaxFollowUpDepth is below that product, the depth guardrail aborts
//     the loop before budgetExhausted can fire — silently disabling the
//     budget-exhaustion model-switch path.
//
// When the invariant is violated (typically under the cost_mode `balanced`
// or `aggressive` presets whose MaxAutoContinueCount=5/10 was chosen
// before 903dc8b4d tightened the depth default), this helper lifts the
// depth so the user gets the documented behaviour. A warning is logged at
// boot so operators can diagnose the lifting without surprise.
//
// Negative or zero values disable the check (treat as "inactive path");
// the depth at zero or below is not a valid config in practice.
func enforceFollowUpDepthBudgetInvariant(cfg *goal.ModeConfig) {
	if cfg == nil {
		return
	}
	if cfg.MaxAutoContinueCount <= 0 || cfg.MaxModelSwitchCount < 0 {
		// Auto-continue off or model-switch disabled: invariant does not
		// apply. Nothing to adjust.
		return
	}
	required := cfg.MaxAutoContinueCount * (cfg.MaxModelSwitchCount + 1)
	if cfg.MaxFollowUpDepth >= required {
		// Invariant already satisfied.
		return
	}
	oldDepth := cfg.MaxFollowUpDepth
	cfg.MaxFollowUpDepth = required + minBudgetExhaustionMargin
	slog.Warn("goal_control: follow_up_depth_below_budget_invariant; auto-lifting",
		"old_depth", oldDepth,
		"max_auto_continue", cfg.MaxAutoContinueCount,
		"max_model_switch", cfg.MaxModelSwitchCount,
		"required_minimum", required,
		"new_depth", cfg.MaxFollowUpDepth,
		"reason", "without this lift, the depth guardrail truncates the loop before budgetExhausted can fire (903dc8b4d audit-fix followup)")
}

// buildHandoffLLMCaller builds the LLMCaller used by the handoff hook's
// summary step. Reuses the same LLMGatewayAutoLLM* env vars as the goal
// hook so operators only configure one endpoint. Returns a no-op caller
// when no endpoint is configured, in which case the handoff hook auto-
// degrades to rule-based extraction (handoff.summary_engine=rule).
func buildHandoffLLMCaller() handoff.LLMCaller {
	cfg, ok := buildGoalLLMConfig()
	if !ok {
		return handoff.NoopLLMCaller{}
	}
	return handoff.NewChatLLMCallerFromConfig(cfg)
}

// parseModelList splits a comma-separated model list (e.g. "auto,gpt-4o,claude-3-5-sonnet")
// into a clean slice. Empty input yields nil so the hook falls back to "auto".
func parseModelList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// buildGoalLLMCaller builds the LLMCaller for goal judgement calls from the
// shared LLMGatewayAutoLLM* env vars. When no endpoint is set, returns a no-op
// caller so the feature can be enabled (keyword detection / continue logic
// don't need an LLM) without a hard runtime dependency.
// buildSanitizeRestoreInterceptor 构造 SmartSaniGuard 占位符还原拦截器。
// 返回 nil 时表示该能力未启用（Redis 不可用或 sanitizer 失败）。
func buildSanitizeRestoreInterceptor(redisClient *redis.Client, detector *sanitize.PatternDetector) (response.ResponseInterceptor, error) {
	if redisClient == nil {
		return nil, nil
	}
	s, err := sanitize.NewSanitizer(detector)
	if err != nil {
		return nil, err
	}
	return sanitize.NewSanitizeRestoreInterceptor(s, redisClient, 30*time.Minute)
}

// buildSanitizeInputMiddleware 构造 SmartSaniGuard 输入脱敏中间件。
// 返回的函数可直接传给 chatHandler.SetSanitizeInputMiddleware。
func buildSanitizeInputMiddleware(redisClient *redis.Client, detector *sanitize.PatternDetector) (func(http.Handler) http.Handler, error) {
	if redisClient == nil {
		return nil, nil
	}
	s, err := sanitize.NewSanitizer(detector)
	if err != nil {
		return nil, err
	}
	mw, err := sanitize.NewSanitizeInputMiddleware(s, redisClient, 30*time.Minute)
	if err != nil {
		return nil, err
	}
	return mw.Wrap, nil
}

func newSanitizePatternDetector() *sanitize.PatternDetector {
	const configPath = "configs/sensitive_patterns.yaml"
	detector, err := sanitize.NewPatternDetectorFromFile(configPath)
	if err != nil {
		slog.Warn("sanitize pattern config unavailable; using built-in fallback", "path", configPath, "error", err)
		return sanitize.NewPatternDetector()
	}
	slog.Info("sanitize pattern detector ready", "path", configPath)
	return detector
}

// installSmartSaniGuard 在 chatHandler 上挂载 SmartSaniGuard 的请求侧
// 中间件（输入脱敏）与响应侧占位符还原拦截器。
//
// 设计要点：
//   - 仅依赖 Redis，与 DB / goal / audit / output_compliance 完全解耦
//   - 因此可以在 bgDataPlaneOnly=true（纯数据面模式）下也启用，
//     不被 initGoalControl 的 db!=nil gate 限制
//   - 当 redisClient 为 nil 时为 no-op（不挂任何东西）
//   - 输入中间件 + 还原拦截器同时挂上，二者缺一不可（中间件负责
//     写 Redis map，还原拦截器负责读 Redis map + 还原响应）
//
// 入口：main.go 在调用 initGoalControl 之外单独调用本函数，
//
//	bgDataPlaneOnly 与 !bgDataPlaneOnly 两个分支都会执行。
func installSmartSaniGuard(chatHandler *streaming.ChatHandler, redisClient *redis.Client) *sanitize.PatternDetector {
	if chatHandler == nil || redisClient == nil {
		return nil
	}
	detector := newSanitizePatternDetector()

	// 1. 输入侧中间件（chatHandler.ServeHTTP 入口处生效）
	mwFn, mwErr := buildSanitizeInputMiddleware(redisClient, detector)
	if mwErr != nil || mwFn == nil {
		slog.Warn("smart_sani_guard: input middleware init failed, skip request-side",
			"error", mwErr)
	} else {
		chatHandler.SetSanitizeInputMiddleware(mwFn)
		slog.Info("smart_sani_guard: input middleware wired")
	}

	// 2. 响应侧还原拦截器：把链挂到 chatHandler 已有的 response chain
	//    之后（保证 output_compliance 先于 sanitize_restore 执行）。
	restoreHook, restoreErr := buildSanitizeRestoreInterceptor(redisClient, detector)
	if restoreErr != nil || restoreHook == nil {
		slog.Warn("smart_sani_guard: restore interceptor init failed, skip response-side",
			"error", restoreErr)
		return detector
	}

	// 把现有 chain + 还原拦截器重组成新 chain，保持插入顺序。
	// chain.go.NewInterceptorChain 内部只是把切片存起来，不做其他副作用，
	// 因此可以安全重建。
	existing := chatHandler.ResponseInterceptorForWire()
	if existing == nil {
		// 无现有 chain（例如 data-plane 模式未注册 goal/audit），
		// 单独创建一个只含 sanitize_restore 的 chain。
		chatHandler.SetResponseInterceptor(response.NewInterceptorChain(restoreHook))
	} else {
		// 追加到现有 chain 末尾（在 output_compliance 之后）。
		chatHandler.SetResponseInterceptor(response.NewInterceptorChain(append(existing.ListInterceptors(), restoreHook)...))
	}
	slog.Info("smart_sani_guard: restore interceptor wired",
		"chain_length", len(existing.ListInterceptors())+1)
	return detector
}

// buildGoalLLMCaller builds the LLMCaller used by completion detection + audit.
func buildGoalLLMCaller() goal.LLMCaller {
	cfg, ok := buildGoalLLMConfig()
	if !ok {
		return goal.NoopLLMCaller()
	}
	return goal.NewChatLLMCaller(cfg)
}

// llmCallerConfigured reports whether the LLM endpoint env var is present.
func llmCallerConfigured() bool {
	_, ok := buildGoalLLMConfig()
	return ok
}

// buildGoalLLMConfig reads the shared autoroute LLM env vars into a
// HTTPLlmCallerConfig (with goal-appropriate MaxTokens/Timeout defaults applied)
// and reports whether an endpoint was configured.
func buildGoalLLMConfig() (autoroute.HTTPLlmCallerConfig, bool) {
	endpoint := strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMEndpoint"))
	if endpoint == "" {
		return autoroute.HTTPLlmCallerConfig{}, false
	}
	cfg := autoroute.HTTPLlmCallerConfig{
		Endpoint: endpoint,
		APIKey:   strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMApiKey")),
		Model:    strings.TrimSpace(os.Getenv("LLMGatewayAutoLLMModel")),
	}
	goal.ApplyHTTPLlmCallerDefaults(&cfg)
	return cfg, true
}

func getEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
