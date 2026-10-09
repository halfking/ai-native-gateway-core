package admin

import "strings"

// Valid credential routing_scope values (migration 847).
//
//	all         — chat + media (default, backward compatible)
//	chat_only   — text/embedding/chat only; never selected for audio/video
//	media_only  — audio/vision/video only; never selected for standard chat
const (
	routingScopeAll       = "all"
	routingScopeChatOnly  = "chat_only"
	routingScopeMediaOnly = "media_only"
)

func isValidRoutingScope(v string) bool {
	switch strings.TrimSpace(v) {
	case routingScopeAll, routingScopeChatOnly, routingScopeMediaOnly:
		return true
	default:
		return false
	}
}

func normalizeRoutingScope(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return routingScopeAll
	}
	return s
}
