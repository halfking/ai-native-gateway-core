package credentialquota

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/clienttype"
)

// allowedClientTypes mirrors the clienttype closed set used by FpSlot and
// request logging. Unknown values normalise to "unknown" but never create
// additional entries.
var allowedClientTypes = map[string]struct{}{
	"cursor": {}, "claude-code": {}, "opencode": {}, "zcode": {}, "codex": {},
	"roocode": {}, "vscode": {}, "copilot": {}, "windsurf": {}, "zed": {},
	"jetbrains": {}, "unknown": {},
}

func normalizeClientType(value string) string {
	if value == "" {
		return clienttype.Unknown
	}
	value = clienttype.Normalize(strings.TrimSpace(value))
	if _, ok := allowedClientTypes[value]; !ok {
		return clienttype.Unknown
	}
	return value
}

func policyKey(credentialID int64, clientType string) policyKeyValue {
	return policyKeyValue{
		credentialID: credentialID,
		clientType:   normalizeClientType(clientType),
	}
}

type policyKeyValue struct {
	credentialID int64
	clientType   string
}

// normalizePolicy validates and normalises a single policy record. Rows
// without a positive MaxConcurrent are treated as unlimited.
func normalizePolicy(in Policy) (Policy, bool) {
	in.ClientType = normalizeClientType(in.ClientType)
	if in.CredentialID <= 0 {
		return Policy{}, false
	}
	if in.MaxConcurrent <= 0 && in.MaxFPSlots <= 0 {
		return Policy{}, false
	}
	return in, true
}

// normalizePolicies deduplicates by (credential_id, client_type) and keeps the
// last-seen row per key. Returning the duplicate key allows the resolver to
// refuse publishing the snapshot.
func normalizePolicies(in []Policy) (out []Policy, duplicate error) {
	seen := make(map[policyKeyValue]struct{}, len(in))
	for _, raw := range in {
		normalized, ok := normalizePolicy(raw)
		if !ok {
			continue
		}
		key := policyKey(normalized.CredentialID, normalized.ClientType)
		if _, dup := seen[key]; dup {
			return nil, errDuplicatePolicy{key: key}
		}
		seen[key] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

type errDuplicatePolicy struct {
	key policyKeyValue
}

func (e errDuplicatePolicy) Error() string {
	return "duplicate credential client quota key"
}
