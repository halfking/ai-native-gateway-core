package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/settings"
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

var defaultSessionFieldPriority = []string{
	"gwsessionid",
	"sessionid",
	"session",
	"conversationid",
	"chatsessionid",
	"threadid",
}

var sessionBodyKeyOverrides []string

func SetSessionIDBodyKeys(keys []string) {
	sessionBodyKeyOverrides = append([]string(nil), keys...)
}

func configuredSessionFieldPriority() []string {
	seen := make(map[string]struct{}, len(defaultSessionFieldPriority))
	merged := make([]string, 0, len(defaultSessionFieldPriority)+4)
	for _, key := range defaultSessionFieldPriority {
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	for _, item := range sessionBodyKeyOverrides {
		appendSessionFieldAlias(&merged, seen, item)
	}
	for _, item := range sessionBodyKeySettings() {
		appendSessionFieldAlias(&merged, seen, item)
	}
	return merged
}

func appendSessionFieldAlias(target *[]string, seen map[string]struct{}, item string) {
	normalized := normalizeSessionFieldName(item)
	if normalized == "" {
		return
	}
	if _, ok := seen[normalized]; ok {
		return
	}
	seen[normalized] = struct{}{}
	*target = append(*target, normalized)
}

func sessionBodyKeySettings() []string {
	if settings.Global == nil {
		return nil
	}
	raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, "session.id_body_keys", "")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return splitSessionFieldList(single)
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	return nil
}

func splitSessionFieldList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func extractSessionIDFromRequest(r *http.Request, body []byte) string { //nolint:unused
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
		for _, key := range configuredSessionFieldPriority() {
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
