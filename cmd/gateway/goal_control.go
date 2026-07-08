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
//   - LLM_GATEWAY_GOAL_ENABLED=true (or per-tenant goal.enabled setting), AND
//   - LLMGatewayAutoLLMEndpoint=... (so the completion-detection/audit LLM
//     judgement calls have somewhere to go)
// The follow-up engine itself enforces MaxFollowUpDepth / MaxFollowUpsPerSession
// regardless of configuration.

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/settings"
	streaming "github.com/kaixuan/llm-gateway-go/domains/streaming"
)

// settingsAdapter adapts the global settings registry to goal.SettingsGetter.
// It reads tenant-scoped values (goal.* / handoff.*) and falls back to the
// provided default when unset or unparseable.
type settingsAdapter struct{}

func (settingsAdapter) GetBool(tenantID, key string, def bool) bool {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out bool
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

func (settingsAdapter) GetInt(tenantID, key string, def int) int {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out int
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

func (settingsAdapter) GetFloat(tenantID, key string, def float64) float64 {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out float64
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

func (settingsAdapter) GetString(tenantID, key string, def string) string {
	if settings.Global == nil {
		return def
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return def
	}
	var out string
	if err := json.Unmarshal(val, &out); err != nil {
		return def
	}
	return out
}

// initGoalControl constructs the goal + audit interceptor chain and registers
// it on the chat handler. It also registers the goal/handoff configuration
// specs with the global settings registry so the goal.* keys resolve.
//
// Safe to call when db is nil (no-op). The feature still honours the
// goal.enabled flag at runtime via settingsAdapter, so even with the chain
// installed the hooks are inert until enabled.
func initGoalControl(db *sql.DB, chatHandler *streaming.ChatHandler) {
	if db == nil {
		slog.Info("goal_control: disabled (no DB)")
		return
	}

	// 1. Register configuration specs so settings.Global knows goal.* keys.
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

	// 5. Goal mode hook: drives activation, completion detection, and the
	//    "please continue" auto-follow-up, including model switching on loops.
	goalCfg := goal.ModeConfig{
		Enabled:               getEnvBool("LLM_GATEWAY_GOAL_ENABLED", false),
		DetectionMode:         goal.DetectionMode(getEnv("LLM_GATEWAY_GOAL_DETECTION_MODE", "hybrid")),
		AutoSelectRecommended: getEnvBool("LLM_GATEWAY_GOAL_AUTO_SELECT", true),
		AutoContinueOnPause:   getEnvBool("LLM_GATEWAY_GOAL_AUTO_CONTINUE", true),
		MaxRetryCount:         getEnvInt("LLM_GATEWAY_GOAL_MAX_RETRY", 3),
		MaxAutoContinueCount:  getEnvInt("LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE", 3),
		UseAutorouteForAudit:  getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT", true),
		UseAutorouteForIntent: getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_INTENT", true),
		FallbackAuditModel:    getEnv("LLM_GATEWAY_GOAL_FALLBACK_AUDIT_MODEL", "auto"),
		AutoFixEnabled:        getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false),
		SettingsGetter:        adapter,

		// Loop-detection & model switching (all also overridable per-tenant
		// via settings; these env defaults seed the boot-time config).
		ModelSwitchOnLoop:      getEnvBool("LLM_GATEWAY_GOAL_MODEL_SWITCH_ON_LOOP", true),
		MaxModelSwitchCount:    getEnvInt("LLM_GATEWAY_GOAL_MAX_MODEL_SWITCH", 3),
		FallbackModels:         parseModelList(getEnv("LLM_GATEWAY_GOAL_FALLBACK_MODELS", "")),
		RepeatDetectionEnabled: getEnvBool("LLM_GATEWAY_GOAL_REPEAT_DETECTION", true),
		RepeatThreshold:        getEnvInt("LLM_GATEWAY_GOAL_REPEAT_THRESHOLD", 3),
		RepeatResetOnProgress:  true,
		CompletionConfidence:   getEnvFloat("LLM_GATEWAY_GOAL_COMPLETION_CONFIDENCE", goal.DefaultCompletionConfidence),
		MaxFollowUpDepth:       getEnvInt("LLM_GATEWAY_GOAL_MAX_FOLLOW_UP_DEPTH", 15),
		MaxFollowUpsPerSession: getEnvInt("LLM_GATEWAY_GOAL_MAX_FOLLOW_UPS_PER_SESSION", 50),
	}

	// Apply the follow-up engine limits so the loop guardrails honour the
	// goal config from boot. Per-tenant runtime overrides still apply inside
	// the hook via settings; this just sets a sane process-wide default.
	streaming.SetFollowUpLimits(goalCfg.MaxFollowUpDepth, goalCfg.MaxFollowUpsPerSession)

	goalHook := goal.NewModeHookWithHistory(goalCfg, goalStore, caller, historyStore)

	// 6. Audit hook: runs after completion, uses the full conversation
	//    transcript (historyStore) and a separate audit model.
	auditCfg := goal.AuditConfig{
		Enabled:        getEnvBool("LLM_GATEWAY_GOAL_AUDIT_ENABLED", false),
		UseAutoroute:   getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT", true),
		FallbackModel:  getEnv("LLM_GATEWAY_GOAL_AUDIT_MODEL", "auto"),
		AutoFixEnabled: getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false),
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
	//     Defaults to disabled — inert unless an operator opts in.
	//
	// NOTE: handoff TriggerHook 实现 (domains/hooks/handoff/trigger_hook.go)
	// 在历史重构中被移除（仅保留 doc.go），但 goal_control.go 仍引用它，导致
	// cmd/gateway 编译失败。此处临时禁用 handoff 接入（功能本就默认 Enabled=false），
	// 待 handoff 实现恢复后重新接入。见 commit b807d30e / 2ad2c479。
	// handoffStore := handoff.NewPGStore(db)
	// handoffCfg := handoff.TriggerConfig{...}
	// handoffHook := handoff.NewTriggerHook(handoffCfg, handoffStore)

	// 7. Chain and install. Order matters: goal continue → audit → output_compliance.
	// handoff 暂未接入（实现缺失），chain 含 goal + audit + output_compliance。
	interceptors := []response.ResponseInterceptor{goalHook, auditHook}

	// 7b. Output compliance interceptor（输出合规/脱敏，2026-07-09）。
	// 用同一 *sql.DB 构造 checker；ownerFn 从 session_dim 查询 dataOwner +
	// 从最新 request_log 的 api_key_owner_user 取 callerOwner。
	// db 为 nil 时 buildOutputComplianceInterceptor 返回 nil，链自动跳过。
	if ocHook := buildOutputComplianceInterceptor(db); ocHook != nil {
		interceptors = append(interceptors, ocHook)
	}

	chain := response.NewInterceptorChain(interceptors...)
	chatHandler.SetResponseInterceptor(chain)

	ocEnabled := len(interceptors) > 2
	slog.Info("goal_control: interceptors installed",
		"goal_enabled", goalCfg.Enabled,
		"detection_mode", goalCfg.DetectionMode,
		"audit_enabled", auditCfg.Enabled,
		"handoff_enabled", false, // 实现缺失，强制禁用
		"output_compliance_enabled", ocEnabled,
		"model_switch_on_loop", goalCfg.ModelSwitchOnLoop,
		"max_model_switch", goalCfg.MaxModelSwitchCount,
		"fallback_models", goalCfg.FallbackModels,
		"repeat_threshold", goalCfg.RepeatThreshold,
		"completion_confidence", goalCfg.CompletionConfidence,
		"llm_caller_configured", llmCallerConfigured())
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
