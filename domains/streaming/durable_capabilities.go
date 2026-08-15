package streaming

import (
	"net/http"
	"strings"
)

const durableRecoveryCapability = "durable-recovery"

// GatewayCapabilities contains explicitly negotiated gateway protocol features.
type GatewayCapabilities struct {
	StatusEvents    bool
	DurableRecovery bool
}

// ParseGatewayCapabilities parses comma-separated capability tokens without
// treating unknown tokens as errors.
func ParseGatewayCapabilities(r *http.Request) GatewayCapabilities {
	var caps GatewayCapabilities
	if r == nil {
		return caps
	}
	for _, line := range r.Header.Values("X-Gw-Capabilities") {
		for _, raw := range strings.Split(line, ",") {
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case "status-events":
				caps.StatusEvents = true
			case durableRecoveryCapability:
				caps.DurableRecovery = true
			}
		}
	}
	return caps
}

// PrefersRespondAsync reports whether Prefer contains the respond-async token.
func PrefersRespondAsync(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, line := range r.Header.Values("Prefer") {
		for _, raw := range strings.Split(line, ",") {
			token := strings.TrimSpace(raw)
			if i := strings.IndexByte(token, ';'); i >= 0 {
				token = token[:i]
			}
			if strings.EqualFold(strings.TrimSpace(token), "respond-async") {
				return true
			}
		}
	}
	return false
}

// RequestsDurable requires the explicit boolean durable request header.
func RequestsDurable(r *http.Request) bool {
	return r != nil && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Gw-Durable")), "true")
}

// DurableEligibilityInput contains only trusted and explicitly negotiated
// values needed to decide whether a request may create a durable task.
type DurableEligibilityInput struct {
	DurableEnabled            bool
	TenantAllowed             bool
	DurableHeader             bool
	Stream                    bool
	DurableRecoveryCapability bool
	PreferRespondAsync        bool
	APIKeyID                  int
	TenantID                  string
	SessionExists             bool
	SessionAPIKeyID           int
	SessionTenantID           string
}

// DurableEligibility is the fail-closed durable authorization verdict.
type DurableEligibility struct {
	Allowed bool
	Reason  string
}

// EvaluateDurableEligibility enforces feature, tenant, handshake and persisted
// session ownership gates before a durable task may be created.
func EvaluateDurableEligibility(in DurableEligibilityInput) DurableEligibility {
	switch {
	case !in.DurableEnabled:
		return DurableEligibility{Reason: "durable_disabled"}
	case !in.TenantAllowed:
		return DurableEligibility{Reason: "tenant_not_allowed"}
	case !in.DurableHeader:
		return DurableEligibility{Reason: "durable_not_requested"}
	case in.APIKeyID <= 0 || in.TenantID == "":
		return DurableEligibility{Reason: "trusted_identity_required"}
	case !in.SessionExists:
		return DurableEligibility{Reason: "session_not_persisted"}
	case in.SessionAPIKeyID != in.APIKeyID || in.SessionTenantID != in.TenantID:
		return DurableEligibility{Reason: "session_owner_mismatch"}
	case !in.Stream && !in.DurableRecoveryCapability && !in.PreferRespondAsync:
		return DurableEligibility{Reason: "async_capability_required"}
	default:
		return DurableEligibility{Allowed: true}
	}
}
