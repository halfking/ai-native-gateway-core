package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

func TestGWSessionTaskFromRequestSanitizesCorrelationIDs(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Gw-Session-Id", "gw_valid-session")
	r.Header.Set("X-Gw-Task-Id", "auto-title:gw_valid-session")

	sessionID, taskID := gwSessionTaskFromRequest(r, nil)
	if sessionID != "gw_valid-session" || taskID != "auto-title:gw_valid-session" {
		t.Fatalf("valid correlation IDs changed: session=%q task=%q", sessionID, taskID)
	}

	r.Header.Set("X-Gw-Session-Id", "gw_bad\nvalue")
	r.Header.Set("X-Gw-Task-Id", strings.Repeat("x", maxRequestCorrelationIDLen+1))
	sessionID, taskID = gwSessionTaskFromRequest(r, &session.Session{
		SessionID: "gw_bad fallback",
		TaskID:    "bad fallback",
	})
	if sessionID != "" || taskID != "" {
		t.Fatalf("invalid correlation IDs were retained: session=%q task=%q", sessionID, taskID)
	}
}

// TestGWAgentAttributionFromRequest —— R50 F15 采集点钉桩：730 sessions
// 归因前两列的头解析（第三列 parent_task_id 复用 GwTaskID，见
// gwSessionTaskFromRequest）。声明 > Source-Actor 推断；未声明/非法一律
// 空串（SQL 侧归一为 'main' 列默认），父会话 ID 走关联 ID 消毒。
func TestGWAgentAttributionFromRequest(t *testing.T) {
	cases := []struct {
		name           string
		role           string
		actor          string
		parent         string
		wantRole       string
		wantParent     string
	}{
		{"declared worker", "Worker", "", "gw_p1", "worker", "gw_p1"},
		{"actor inferred", "", "auto-title-generator", "", "worker", ""},
		{"undeclared degrades empty", "", "", "", "", ""},
		{"invalid role degrades empty", "hustler", "", "", "", ""},
		{"parent id sanitized", "planner", "", "bad id with spaces!!", "planner", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
			if tc.role != "" {
				r.Header.Set("X-Gw-Agent-Role", tc.role)
			}
			if tc.actor != "" {
				r.Header.Set("X-Gw-Source-Actor", tc.actor)
			}
			if tc.parent != "" {
				r.Header.Set("X-Gw-Parent-Session-Id", tc.parent)
			}
			role, parent := gwAgentAttributionFromRequest(r)
			if role != tc.wantRole {
				t.Errorf("role: got %q, want %q", role, tc.wantRole)
			}
			if parent != tc.wantParent {
				t.Errorf("parent: got %q, want %q", parent, tc.wantParent)
			}
		})
	}
	if role, parent := gwAgentAttributionFromRequest(nil); role != "" || parent != "" {
		t.Fatalf("nil request must be safe: got %q/%q", role, parent)
	}
}
