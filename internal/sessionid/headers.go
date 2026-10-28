package sessionid

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// HeaderXGw is the canonical gateway session header. It is always the first
// entry of Headers() and outranks every legacy/native header.
const HeaderXGw = "X-Gw-Session-Id"

// DefaultHeadersPriority is the built-in header table (W1.2).
//
// Order is part of the contract: X-Gw first, then legacy X-Session-Id, then
// native Agent headers (Conversation / Chat / Thread), then DeepSeek Harness.
// http.Header.Get is case-insensitive, so "X-Session-ID" matches "X-Session-Id".
var DefaultHeadersPriority = []string{
	"X-Gw-Session-Id",
	"X-Session-Id",
	"X-Conversation-Id",
	"X-Chat-Session-Id",
	"X-Thread-Id",
	"X-Deepseek-Harness-Session-Id",
}

var (
	headerOverrideMu sync.RWMutex
	headerOverrides  []string
)

// SetHeaderOverrides replaces the process-level header alias list (merged
// after DefaultHeadersPriority). Pass nil/empty to clear. Hot-reloadable via
// settings key session.id_headers as well.
func SetHeaderOverrides(keys []string) {
	headerOverrideMu.Lock()
	defer headerOverrideMu.Unlock()
	headerOverrides = append([]string(nil), keys...)
}

// Headers returns the effective header priority table: defaults, then
// process overrides, then hot settings — de-duplicated, X-Gw pinned first.
func Headers() []string {
	seen := make(map[string]struct{}, len(DefaultHeadersPriority)+4)
	out := make([]string, 0, len(DefaultHeadersPriority)+4)
	appendHeader := func(name string) {
		canon := canonicalizeHeaderName(name)
		if canon == "" {
			return
		}
		key := strings.ToLower(canon)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, canon)
	}

	appendHeader(HeaderXGw)
	for _, h := range DefaultHeadersPriority {
		appendHeader(h)
	}
	headerOverrideMu.RLock()
	local := append([]string(nil), headerOverrides...)
	headerOverrideMu.RUnlock()
	for _, h := range local {
		appendHeader(h)
	}
	for _, h := range headerSettings() {
		appendHeader(h)
	}
	return out
}

func canonicalizeHeaderName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	lower := strings.ToLower(name)
	for _, def := range DefaultHeadersPriority {
		if strings.ToLower(def) == lower {
			return def
		}
	}
	return name
}

func headerSettings() []string {
	if settings.Global == nil {
		return nil
	}
	raw, _, err := settings.Global.EffectiveValue(settings.ScopePlatform, "session.id_headers", "")
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

func splitCSV(raw string) []string {
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

// FromHeaders walks Headers() and returns the first non-empty sanitized
// value. X-Gw values are gateway-sanitized; other headers use the correlation
// sanitizer (bare UUID kept as-is — DeriveGatewayID is the caller's job when
// they want the gw_ namespace). Provenance distinguishes X-Gw from the rest.
func FromHeaders(h http.Header) (string, Provenance) {
	if h == nil {
		return "", ProvenanceNone
	}
	for _, header := range Headers() {
		value := h.Get(header)
		if value == "" {
			continue
		}
		if strings.EqualFold(header, HeaderXGw) {
			if id := SanitizeGwHeader(value); id != "" {
				return id, ProvenanceHeaderGw
			}
			continue
		}
		if id := SanitizeCorrelationID(value); id != "" {
			return id, ProvenanceHeaderNative
		}
	}
	return "", ProvenanceNone
}
