package streaming

import (
	"context"
	"net/http"

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
