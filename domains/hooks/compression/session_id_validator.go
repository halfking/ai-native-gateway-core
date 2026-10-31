package compression

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidateSessionID checks if gwSessionID follows the expected format.
// Returns error if the ID is too short, contains only digits (likely a
// collision-prone auto-increment), or is a common placeholder.
//
// This validation prevents session cross-talk by rejecting weak session IDs
// that could collide across different clients or tenants.
func ValidateSessionID(gwSessionID string) error {
	if len(gwSessionID) < 8 {
		return fmt.Errorf("session_id too short: %s", gwSessionID)
	}

	// Reject numeric-only IDs (e.g., "12345678") as they are collision-prone
	// if multiple clients use simple auto-increment.
	if matched, _ := regexp.MatchString(`^\d+$`, gwSessionID); matched {
		return fmt.Errorf("session_id is numeric-only (collision risk): %s", gwSessionID)
	}

	// Reject common placeholders that indicate the client didn't generate
	// proper unique ID. Only "session" gets the prefix+entropy exemption:
	// session_<random> / session-<uuid> are legitimate stable client
	// conventions (211 实测：客户端发 session_<22hex>，经 DeriveGatewayID 映成
	// gw_session_*，此前每轮压缩都被误拒 ⇒ 压缩历史断链)；test/demo/default/
	// temp/placeholder 从不作为真实生成前缀出现，任何位置命中即拒。
	forbidden := []string{"test", "demo", "default", "session", "temp", "placeholder"}
	lower := strings.ToLower(gwSessionID)
	for _, f := range forbidden {
		if !strings.Contains(lower, f) {
			continue
		}
		if f == "session" && keywordPrefixWithEntropy(lower, f) {
			continue
		}
		return fmt.Errorf("session_id contains forbidden keyword '%s': %s", f, gwSessionID)
	}

	return nil
}

// keywordPrefixWithEntropy reports whether the ONLY occurrence shape of the
// "session" keyword is "session + separator + >=8 chars of non-placeholder
// tail" at the very start of the id (after the gw_/gt_/gs_ derivation
// prefixes). Such ids carry real client entropy and cannot collide across
// tenants; mid-string occurrences ("user-session-789") and short tails
// ("session_ab12") stay rejected.
func keywordPrefixWithEntropy(lower, keyword string) bool {
	s := lower
	for _, p := range []string{"gw_", "gt_", "gs_"} {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	if !strings.HasPrefix(s, keyword) {
		return false
	}
	tail := s[len(keyword):]
	if tail == "" {
		return false // bare keyword, e.g. "session"
	}
	sep := tail[0]
	if sep != '-' && sep != '_' && sep != '.' {
		return false // "session1234"-style suffix without separator
	}
	tail = tail[1:]
	if len(tail) < 8 {
		return false // not enough entropy to rule out placeholders
	}
	for _, f := range []string{"test", "demo", "default", "session", "temp", "placeholder"} {
		if strings.Contains(tail, f) {
			return false // tail itself is placeholder-ish
		}
	}
	return true
}
