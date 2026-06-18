package admin

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultAdminLLMTask_SessionTitle(t *testing.T) {
	cfg := defaultAdminLLMTasks[adminLLMTaskSessionTitle]
	if cfg.DefaultProfile != "cost_first" {
		t.Fatalf("profile = %q, want cost_first", cfg.DefaultProfile)
	}
	if cfg.TaskHint != "creative" {
		t.Fatalf("task hint = %q, want creative", cfg.TaskHint)
	}
	if !strings.Contains(cfg.SystemPrompt, "标题") {
		t.Fatal("expected title prompt in system_prompt")
	}
}

func TestAdminLLMShouldRetryExplicit(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"no_candidate for model", true},
		{"no available provider for model", true},
		{"auto_route_unavailable", true},
		{"timeout", false},
	}
	for _, tc := range cases {
		if got := adminLLMShouldRetryExplicit(errors.New(tc.err)); got != tc.want {
			t.Fatalf("retry(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestLoadAdminLLMTask_NoDBUsesDefaults(t *testing.T) {
	h := &Handler{}
	cfg := h.loadAdminLLMTask(t.Context(), adminLLMTaskSessionTitle)
	if cfg.Key != adminLLMTaskSessionTitle {
		t.Fatalf("key = %q", cfg.Key)
	}
	if cfg.DeviceSeed != "admin-session-title" {
		t.Fatalf("device seed = %q", cfg.DeviceSeed)
	}
}

func TestResolveAdminLLMFallbackModel_EnvOverride(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ADMIN_LLM_FALLBACK_MODEL", "glm-5.1")
	h := &Handler{}
	got := h.resolveAdminLLMFallbackModel(t.Context(), adminLLMTaskSessionTitle)
	if got != "glm-5.1" {
		t.Fatalf("got %q, want glm-5.1", got)
	}
}

func TestResolveAdminLLMFallbackModel_NoDBDefault(t *testing.T) {
	h := &Handler{}
	got := h.resolveAdminLLMFallbackModel(t.Context(), adminLLMTaskSessionTitle)
	if got != "minimax-m2.7" {
		t.Fatalf("got %q, want minimax-m2.7", got)
	}
}
