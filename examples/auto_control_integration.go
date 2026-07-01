//go:build ignore
// +build ignore

// Package main shows how to integrate the auto-control system.
//
// This is a reference implementation for cmd/gateway/main.go or cmd/gateway-v2/main.go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/handoff"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// SettingsAdapter adapts settings.Global to the SettingsGetter interface.
type SettingsAdapter struct{}

func (a *SettingsAdapter) GetBool(tenantID, key string, defaultValue bool) bool {
	if settings.Global == nil {
		return defaultValue
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return defaultValue
	}
	var result bool
	if err := json.Unmarshal(val, &result); err != nil {
		return defaultValue
	}
	return result
}

func (a *SettingsAdapter) GetInt(tenantID, key string, defaultValue int) int {
	if settings.Global == nil {
		return defaultValue
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return defaultValue
	}
	var result int
	if err := json.Unmarshal(val, &result); err != nil {
		return defaultValue
	}
	return result
}

func (a *SettingsAdapter) GetFloat(tenantID, key string, defaultValue float64) float64 {
	if settings.Global == nil {
		return defaultValue
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return defaultValue
	}
	var result float64
	if err := json.Unmarshal(val, &result); err != nil {
		return defaultValue
	}
	return result
}

func (a *SettingsAdapter) GetString(tenantID, key string, defaultValue string) string {
	if settings.Global == nil {
		return defaultValue
	}
	val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, key, tenantID)
	if err != nil || len(val) == 0 {
		return defaultValue
	}
	var result string
	if err := json.Unmarshal(val, &result); err != nil {
		return defaultValue
	}
	return result
}

// IntegrateAutoControlSystem sets up the auto-control system hooks.
// chatHandler should have SetResponseInterceptor(response.ResponseInterceptor).
func IntegrateAutoControlSystem(db *sql.DB, chatHandler interface {
	SetResponseInterceptor(response.ResponseInterceptor)
}) {
	// 1. Register configuration specs
	log.Println("Registering auto-control configuration specs...")
	for _, spec := range settings.AutoControlSpecs() {
		s := spec // take address
		if err := settings.Global.RegisterSpec(&s); err != nil {
			log.Printf("Warning: failed to register spec %s: %v", spec.Key, err)
		}
	}

	// 2. Create settings adapter
	settingsAdapter := &SettingsAdapter{}

	// 3. Create database stores
	handoffStore := handoff.NewPGStore(db)
	goalStore := goal.NewPGStore(db)

	// 4. Create LLM caller (placeholder - implement based on your needs)
	llmCaller := &SimpleLLMCaller{} // You need to implement this

	// 5. Configure handoff hook
	handoffConfig := handoff.TriggerConfig{
		Enabled:             getEnvBool("LLM_GATEWAY_HANDOFF_ENABLED", false),
		AbsoluteThreshold:   getEnvInt("LLM_GATEWAY_HANDOFF_ABSOLUTE_THRESHOLD", 180000),
		PercentageThreshold: getEnvFloat("LLM_GATEWAY_HANDOFF_PERCENTAGE_THRESHOLD", 0.8),
		MessageThreshold:    getEnvInt("LLM_GATEWAY_HANDOFF_MESSAGE_THRESHOLD", 0),
		SkillName:           getEnv("LLM_GATEWAY_HANDOFF_SKILL_NAME", "handoff"),
		SettingsGetter:      settingsAdapter,
	}
	handoffHook := handoff.NewTriggerHook(handoffConfig, handoffStore)

	// 6. Configure goal mode hook
	goalConfig := goal.ModeConfig{
		Enabled:               getEnvBool("LLM_GATEWAY_GOAL_ENABLED", false),
		DetectionMode:         goal.DetectionMode(getEnv("LLM_GATEWAY_GOAL_DETECTION_MODE", "hybrid")),
		AutoSelectRecommended: getEnvBool("LLM_GATEWAY_GOAL_AUTO_SELECT", true),
		AutoContinueOnPause:   getEnvBool("LLM_GATEWAY_GOAL_AUTO_CONTINUE", true),
		MaxRetryCount:         getEnvInt("LLM_GATEWAY_GOAL_MAX_RETRY", 3),
		MaxAutoContinueCount:  getEnvInt("LLM_GATEWAY_GOAL_MAX_AUTO_CONTINUE", 10),
		UseAutorouteForAudit:  getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT", true),
		UseAutorouteForIntent: getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_INTENT", true),
		FallbackAuditModel:    getEnv("LLM_GATEWAY_GOAL_FALLBACK_AUDIT_MODEL", "auto"),
		AutoFixEnabled:        getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false),
		SettingsGetter:        settingsAdapter,
	}
	goalHook := goal.NewModeHook(goalConfig, goalStore, llmCaller)

	// 7. Create audit hook
	auditConfig := goal.AuditConfig{
		Enabled:        getEnvBool("LLM_GATEWAY_GOAL_AUDIT_ENABLED", false),
		UseAutoroute:   getEnvBool("LLM_GATEWAY_GOAL_USE_AUTOROUTE_AUDIT", true),
		FallbackModel:  getEnv("LLM_GATEWAY_GOAL_AUDIT_MODEL", "auto"),
		AutoFixEnabled: getEnvBool("LLM_GATEWAY_GOAL_AUTO_FIX", false),
		MinConfidence:  0.7,
		SettingsGetter: settingsAdapter,
	}
	auditHook := goal.NewAuditHook(goalStore, llmCaller, auditConfig)

	// 8. Build interceptor chain
	interceptorChain := response.NewInterceptorChain(
		handoffHook,
		goalHook,
		auditHook,
	)

	// 9. Set interceptor to chat handler
	chatHandler.SetResponseInterceptor(interceptorChain)

	log.Println("Auto-control system initialized successfully")
	log.Printf("  - Handoff enabled: %v (threshold: %d tokens or %.0f%%)",
		handoffConfig.Enabled, handoffConfig.AbsoluteThreshold, handoffConfig.PercentageThreshold*100)
	log.Printf("  - Goal mode enabled: %v (detection: %s)", goalConfig.Enabled, goalConfig.DetectionMode)
}

// SimpleLLMCaller is a placeholder implementation.
// Replace with your actual LLM client implementation.
type SimpleLLMCaller struct{}

func (c *SimpleLLMCaller) CallLLM(ctx context.Context, model string, messages []map[string]string) (string, error) {
	// TODO: Implement actual LLM calling logic
	return `{"completed": false, "confidence": 0.5, "reason": "not implemented"}`, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if val == "true" || val == "1" {
		return true
	}
	if val == "false" || val == "0" {
		return false
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return i
}

func getEnvFloat(key string, defaultVal float64) float64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return defaultVal
	}
	return f
}
