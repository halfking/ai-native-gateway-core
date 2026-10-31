package sessionid

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// Gate facts (body.go):
// 1. Callers: session_routing.go extractSessionIDFromBody (L150) → FromBody;
//    handler.go:2787; durable_wiring.go:274; Resolve in sessionid.go.
// 2. No existing internal/sessionid/body.go; only session_routing.go findSessionIDValue.
// 3. No production data files; reads settings session.id_body_keys CSV e.g. "workspaceId,room_session_key".
// 4. User: "新建 internal/sessionid… body>X-Gw>legacy/native>assignment>provisional…"

var defaultBodyFieldPriority = []string{
	"gwsessionid",
	"sessionid",
	"session",
	"conversationid",
	"chatsessionid",
	"threadid",
}

var (
	bodyOverrideMu sync.RWMutex
	bodyOverrides  []string
)

// sessionFieldNameReplacer is package-level (goroutine-safe); building it
// per request showed up in alloc profiles on the streaming hot path.
var sessionFieldNameReplacer = strings.NewReplacer("-", "", "_", "", ".", "", " ", "")

// SetBodyKeyOverrides replaces the process-level body-field alias list
// (merged after defaults). Pass nil/empty to clear. Also hot-reloadable via
// settings key session.id_body_keys.
func SetBodyKeyOverrides(keys []string) {
	bodyOverrideMu.Lock()
	defer bodyOverrideMu.Unlock()
	bodyOverrides = append([]string(nil), keys...)
}

// BodyFieldPriority returns the effective body-field name list (normalized).
func BodyFieldPriority() []string {
	seen := make(map[string]struct{}, len(defaultBodyFieldPriority)+4)
	merged := make([]string, 0, len(defaultBodyFieldPriority)+4)
	for _, key := range defaultBodyFieldPriority {
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	bodyOverrideMu.RLock()
	local := append([]string(nil), bodyOverrides...)
	bodyOverrideMu.RUnlock()
	for _, item := range local {
		appendBodyFieldAlias(&merged, seen, item)
	}
	for _, item := range bodyKeySettings() {
		appendBodyFieldAlias(&merged, seen, item)
	}
	return merged
}

func appendBodyFieldAlias(target *[]string, seen map[string]struct{}, item string) {
	normalized := normalizeBodyFieldName(item)
	if normalized == "" {
		return
	}
	if _, ok := seen[normalized]; ok {
		return
	}
	seen[normalized] = struct{}{}
	*target = append(*target, normalized)
}

func bodyKeySettings() []string {
	if settings.Global == nil {
		return nil
	}
	raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, "session.id_body_keys", "")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return splitCSV(single)
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	return nil
}

func normalizeBodyFieldName(value string) string {
	return strings.ToLower(sessionFieldNameReplacer.Replace(strings.TrimSpace(value)))
}

// FromBody extracts a client session id from a JSON request body by walking
// configured field aliases (session_id / sessionId / conversationId / …).
// Returned values are sanitized but NOT gw_-derived.
func FromBody(body []byte) string {
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
		for _, key := range BodyFieldPriority() {
			for rawKey, rawValue := range typed {
				if normalizeBodyFieldName(rawKey) != key {
					continue
				}
				if sessionID := bodyValueString(rawValue); sessionID != "" {
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

func bodyValueString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = SanitizeCorrelationID(text)
	if strings.HasPrefix(text, "gw_") {
		return SanitizeGwHeader(text)
	}
	return text
}
