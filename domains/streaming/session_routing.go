package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

var SessionHeadersPriority = []string{
	"X-Gw-Session-Id",
	"X-Session-Id",
	"X-Conversation-Id",
	"X-Chat-Session-Id",
	"X-Thread-Id",
}

func extractSessionIDFromHeaders(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range SessionHeadersPriority {
		value := r.Header.Get(header)
		if value == "" {
			continue
		}
		if header == "X-Gw-Session-Id" {
			return sanitizeGwSessionHeader(value)
		}
		return value
	}
	return ""
}

var sessionFieldPriority = []string{
	"gwsessionid",
	"sessionid",
	"session",
	"conversationid",
	"chatsessionid",
	"threadid",
}

func extractSessionIDFromRequest(r *http.Request, body []byte) string {
	if sessionID := extractSessionIDFromHeaders(r); sessionID != "" {
		return sessionID
	}
	return extractSessionIDFromBody(body)
}

func extractSessionIDFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return findSessionIDValue(payload)
}

func findSessionIDValue(node any) string {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range sessionFieldPriority {
			for rawKey, rawValue := range typed {
				if normalizeSessionFieldName(rawKey) != key {
					continue
				}
				if sessionID := sessionValueString(rawValue); sessionID != "" {
					return sessionID
				}
			}
		}
		for _, rawValue := range typed {
			if sessionID := findSessionIDValue(rawValue); sessionID != "" {
				return sessionID
			}
		}
	case []any:
		for _, item := range typed {
			if sessionID := findSessionIDValue(item); sessionID != "" {
				return sessionID
			}
		}
	}
	return ""
}

func sessionValueString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "gw_") {
		return sanitizeGwSessionHeader(text)
	}
	return text
}

func normalizeSessionFieldName(value string) string {
	replacer := strings.NewReplacer("-", "", "_", "", ".", "", " ", "")
	return strings.ToLower(replacer.Replace(strings.TrimSpace(value)))
}

func detectAndHandleModelSwitch(
	ctx context.Context,
	sessionPref *session.SessionPreference,
	sessionID string,
	clientModel string,
) (modelChanged bool, previousModel string) {
	if sessionPref == nil || sessionID == "" || clientModel == "" {
		return false, ""
	}
	val, found := sessionPref.Get(ctx, sessionID)
	if !found || val == nil || val.Model == "" {
		return false, ""
	}
	if val.Model == clientModel {
		return false, ""
	}
	prevModel := val.Model
	_ = sessionPref.ClearOnModelSwitch(ctx, sessionID, prevModel, clientModel)
	return true, prevModel
}

func generateSystemSessionID() string {
	return "gw_" + uuid.New().String()
}
