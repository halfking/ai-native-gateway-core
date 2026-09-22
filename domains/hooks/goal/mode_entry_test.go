package goal

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// Wave 1 A5 回归钉桩：X-Gw-Goal-Mode: managed 是设计 §5.12 的 goal 模式
// 契约入口，与 body {"goal":true} 并存。

func TestDetectExplicit_HeaderManagedActivates(t *testing.T) {
	h := &ModeHook{config: ModeConfig{}}
	req := &response.InterceptRequest{GoalModeHeader: "managed", ResponseBody: []byte(`{}`)}
	got, reason := h.detectExplicit(req)
	if !got {
		t.Fatal("header managed should activate, got false")
	}
	if reason != "header:managed" {
		t.Errorf("reason = %q, want header:managed", reason)
	}
}

func TestDetectExplicit_HeaderCaseInsensitiveAndTrimmed(t *testing.T) {
	h := &ModeHook{config: ModeConfig{}}
	for _, v := range []string{"Managed", " MANAGED ", "managed"} {
		req := &response.InterceptRequest{GoalModeHeader: v, ResponseBody: []byte(`{}`)}
		if got, _ := h.detectExplicit(req); !got {
			t.Errorf("header %q should activate", v)
		}
	}
}

func TestDetectExplicit_HeaderNotManagedFallsToBody(t *testing.T) {
	h := &ModeHook{config: ModeConfig{}}
	if got, _ := h.detectExplicit(&response.InterceptRequest{GoalModeHeader: "off", ResponseBody: []byte(`{}`)}); got {
		t.Error("header=off must not activate")
	}
	// body goal:true 仍独立生效（并存语义）
	got, reason := h.detectExplicit(&response.InterceptRequest{ResponseBody: []byte(`{"goal":true}`)})
	if !got || reason != "body_field" {
		t.Errorf("body field should activate with reason body_field, got %v/%q", got, reason)
	}
	// header 与 body 并存时 header 标记优先命中
	got, reason = h.detectExplicit(&response.InterceptRequest{GoalModeHeader: "managed", ResponseBody: []byte(`{"goal":true}`)})
	if !got || reason != "header:managed" {
		t.Errorf("coexist case: got %v/%q", got, reason)
	}
}
