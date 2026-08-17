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
