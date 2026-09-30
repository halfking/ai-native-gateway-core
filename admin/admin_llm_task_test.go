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
		// R78：原先这里喂的是 "auto_route_unavailable" —— 一个**全仓无产生方**的
		// 幽灵错误码。函数对假字符串返回 true，测试一直是绿的，恰好盖住了真实缺陷
		// （生产永不产生该串 ⇒ 该分支恒 false）。改用真实错误码后才有判别力。
		{"auto_route_decider_failed", true},
		// 反向：retired 的幽灵码现在必须判 false，防止它被重新引入。
		{"auto_route_unavailable", false},
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
