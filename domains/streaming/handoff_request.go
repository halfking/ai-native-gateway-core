package streaming

import (
	"net/http"
	"strings"
)

// estimateRequestTokens is intentionally conservative. The session compressor
// replaces it with a protocol-aware estimate later; this value is only used to
// decide whether a pre-compression handoff needs enough headroom to proceed.
func estimateRequestTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	return (len(body) + 3) / 4
}

func protocolForHandoff(path string) string {
	if isAnthropicMessagesPath(path) {
		return "anthropic-messages"
	}
	return "openai"
}

func handoffDeviceSeed(r *http.Request) string {
	if r == nil {
		return "handoff"
	}
	if seed := r.Header.Get("X-Device-Seed"); seed != "" {
		return seed
	}
	if seed := r.Header.Get("X-Machine-Id"); seed != "" {
		return seed
	}
	return "handoff"
}

// trustedHandoffUpstreamAPIKey returns a client credential only when gateway
// key verification is disabled. Once the gateway verifier is enabled, the
// Authorization header is a gateway Key and must never be forwarded to a
// summary provider. Dedicated direct-provider authentication can pass a key
// through a future explicit credential context instead.
func trustedHandoffUpstreamAPIKey(gatewayAuthEnabled bool, r *http.Request) string {
	if gatewayAuthEnabled || r == nil {
		return ""
	}
	auth := r.Header.Get("Authorization")
	if len(auth) < len("Bearer ") || !strings.EqualFold(auth[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(auth[len("Bearer "):])
}
