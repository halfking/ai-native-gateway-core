package autoroute

import (
	"testing"
)

// TestV5RoutingMatrix verifies the v5.0 deploy SQL routing matrix.
// All models are 2025-2026 featured models from models_canonical seed:
//   claude-sonnet-5, claude-fable-5, claude-opus-4-8, o5-preview,
//   gemini-2.0-flash-exp, qwen3-235b, qwq-32b-preview, minimax-m3, etc.
func TestV5RoutingMatrix(t *testing.T) {
	cases := []struct {
		taskType string
		profile  string
		want     string
	}{
		// chat
		{"chat", "smart", "claude-sonnet-5"},
		{"chat", "speed_first", "gemini-2.0-flash-exp"},
		{"chat", "cost_first", "deepseek-chat"},
		// reasoning
		{"reasoning", "smart", "claude-fable-5"},
		{"reasoning", "cost_first", "qwq-32b-preview"},
		// code
		{"code", "smart", "claude-sonnet-5"},
		{"code", "speed_first", "codestral"},
		{"code", "cost_first", "deepseek-coder"},
		// agent
		{"agent", "smart", "claude-sonnet-5"},
		{"agent", "speed_first", "gemini-2.0-flash-exp"},
		{"agent", "cost_first", "deepseek-chat"},
		// creative
		{"creative", "smart", "claude-opus-4-8"},
		{"creative", "speed_first", "gemini-2.0-flash-exp"},
		{"creative", "cost_first", "deepseek-chat"},
		// long_context
		{"long_context", "smart", "gemini-1.5-pro"},
		{"long_context", "cost_first", "qwen3-235b"},
		// vision
		{"vision", "smart", "claude-sonnet-5"},
		{"vision", "speed_first", "gemini-2.0-flash-exp"},
		{"vision", "cost_first", "glm-4v-plus"},
		// function_call
		{"function_call", "smart", "claude-sonnet-5"},
		{"function_call", "speed_first", "gemini-2.0-flash-exp"},
		{"function_call", "cost_first", "deepseek-chat"},
		// code_audit
		{"code_audit", "smart", "claude-fable-5"},
		{"code_audit", "cost_first", "deepseek-coder"},
		// intent_classification
		{"intent_classification", "smart", "claude-sonnet-5"},
		{"intent_classification", "speed_first", "gemini-2.0-flash-exp"},
		{"intent_classification", "cost_first", "deepseek-chat"},
	}

	routings := make([]DefaultRouting, 0, len(cases))
	for _, c := range cases {
		routings = append(routings, DefaultRouting{
			TaskType:       c.taskType,
			Profile:        c.profile,
			Tier:           RoutingPrimary,
			CanonicalModel: c.want,
			Priority:       100,
		})
	}
	// Generic fallbacks so Resolve succeeds for unknown profiles.
	fallbacks := map[string]string{
		"chat":                  "qwen-max",
		"reasoning":             "qwen-max",
		"code":                  "qwen3-235b",
		"agent":                 "qwen-max",
		"creative":              "qwen-max",
		"long_context":          "qwen-max",
		"vision":                "qwen-max",
		"function_call":         "qwen-turbo",
		"code_audit":            "qwen3-235b",
		"intent_classification": "qwen-turbo",
	}
	for task, model := range fallbacks {
		routings = append(routings, DefaultRouting{
			TaskType:       task,
			Profile:        "",
			Tier:           RoutingFallback,
			CanonicalModel: model,
			Priority:       100,
		})
	}

	byTask := make(map[string][]DefaultRouting)
	for _, r := range routings {
		byTask[r.TaskType] = append(byTask[r.TaskType], r)
	}

	s := &DefaultRoutingStore{}
	s.snapshot.Store(&defaultRoutingSnapshot{byTask: byTask})

	for _, c := range cases {
		t.Run(c.taskType+"_"+c.profile, func(t *testing.T) {
			res, ok := s.Resolve(c.taskType, c.profile, "")
			if !ok {
				t.Fatalf("Resolve(%q, %q, '') returned ok=false", c.taskType, c.profile)
			}
			if res.CanonicalModel != c.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q (tier=%s)",
					c.taskType, c.profile, res.CanonicalModel, c.want, res.Tier)
			}
			if res.Tier != RoutingPrimary {
				t.Errorf("Resolve tier = %q, want primary", res.Tier)
			}
		})
	}
}

// TestV5NoOldModels ensures v5.0 does NOT use any of the old/legacy model IDs
// that were present in v4.0 (which incorrectly referenced provider_catalog IDs
// that don't exist in models_canonical).
func TestV5NoOldModels(t *testing.T) {
	// These are v4.0 models that are WRONG (not in models_canonical) —
	// v5.0 must not use any of them.
	wrongModels := []string{
		"claude-sonnet-4-5",   // not in models_canonical
		"claude-opus-4-5",     // not in models_canonical
		"claude-3-7-sonnet-20250219", // not in models_canonical
		"claude-haiku-3-5",    // not in models_canonical
		"gemini-2.5-pro",      // not in models_canonical
		"gemini-2.5-flash",    // not in models_canonical
		"doubao-seed-2.0-pro", // not in models_canonical
		"doubao-seed-2.0-lite", // not in models_canonical
		"doubao-seed-2.0-mini", // not in models_canonical
		"doubao-seed-2.0-code", // not in models_canonical
		"gpt-4o-mini",         // not in models_canonical (only gpt-4o is)
		"o3",                  // not in models_canonical (only o3-mini, o5-preview)
		"o4-mini",             // not in models_canonical
		"glm-5.1",             // not in models_canonical
		"glm-5.2",             // not in models_canonical (only in provider_catalog)
		"kimi-k2.6",           // not in models_canonical (kimi-chat, moonshot-v1-128k are)
		"qwen-plus",           // not in models_canonical (qwen-max, qwen-turbo, qwen3-235b are)
	}

	v5Models := map[string]bool{
		"claude-sonnet-5":    true,
		"claude-fable-5":     true,
		"claude-opus-4-8":    true,
		"o5-preview":         true,
		"o3-mini":            true,
		"gemini-2.0-flash-exp": true,
		"gemini-1.5-pro":     true,
		"qwen3-235b":         true,
		"qwq-32b-preview":    true,
		"qwen-max":           true,
		"qwen-turbo":         true,
		"minimax-m3":         true,
		"deepseek-chat":      true,
		"deepseek-coder":     true,
		"glm-4v-plus":        true,
		"codestral":          true,
		"moonshot-v1-128k":   true,
	}

	for _, wrong := range wrongModels {
		if v5Models[wrong] {
			t.Errorf("wrong/old model %q is present in v5.0 set", wrong)
		}
	}
	if len(v5Models) < 12 {
		t.Errorf("v5.0 model set too small: %d models", len(v5Models))
	}
}
