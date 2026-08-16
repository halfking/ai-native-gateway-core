package store

import (
	"fmt"
	"strconv"
	"strings"
)

type ParsedNodeKey struct {
	TenantID     string
	CredentialID int
	RawModel     string
}

func NodeKeyForTenant(prefix, tenant string, cid int, raw string) string {
	if tenant == "" {
		return NodeKey(prefix, cid, raw)
	}
	if !isNumericTenant(tenant) {
		return fmt.Sprintf("%snode:%s:%d:%s", prefix, tenantKeyPart(tenant), cid, raw)
	}
	return fmt.Sprintf("%snode:t:%s:%d:%s", prefix, tenantKeyPart(tenant), cid, raw)
}

func NodeKey(prefix string, cid int, raw string) string {
	return fmt.Sprintf("%snode:%d:%s", prefix, cid, raw)
}

// ParseNodeKey decodes the tagged tenant-scoped node:t:<tenant>:<credential>:<model>
// form and the legacy node:<credential>:<model> form. It also accepts the prior
// untagged string-tenant form for persistence compatibility; numeric tenants
// must use the tagged form so they cannot collide with legacy credential IDs.
func ParseNodeKey(prefix, key string) (ParsedNodeKey, bool) {
	base := prefix + "node:"
	if !strings.HasPrefix(key, base) {
		return ParsedNodeKey{}, false
	}
	parts := strings.Split(strings.TrimPrefix(key, base), ":")
	if len(parts) < 2 {
		return ParsedNodeKey{}, false
	}
	if parts[0] == "t" && len(parts) >= 4 && isNumericTenant(parts[1]) {
		if parts[1] == "" {
			return ParsedNodeKey{}, false
		}
		credentialID, err := strconv.Atoi(parts[2])
		if err != nil || credentialID <= 0 {
			return ParsedNodeKey{}, false
		}
		raw := strings.Join(parts[3:], ":")
		return ParsedNodeKey{TenantID: parts[1], CredentialID: credentialID, RawModel: raw}, raw != ""
	}
	if credentialID, err := strconv.Atoi(parts[0]); err == nil && credentialID > 0 {
		raw := strings.Join(parts[1:], ":")
		return ParsedNodeKey{CredentialID: credentialID, RawModel: raw}, raw != ""
	}
	if len(parts) < 3 || parts[0] == "" {
		return ParsedNodeKey{}, false
	}
	credentialID, err := strconv.Atoi(parts[1])
	if err != nil || credentialID <= 0 {
		return ParsedNodeKey{}, false
	}
	raw := strings.Join(parts[2:], ":")
	return ParsedNodeKey{TenantID: parts[0], CredentialID: credentialID, RawModel: raw}, raw != ""
}

func WindowKeyForTenant(prefix, tenant string, cid int, raw, bucket string) string {
	if tenant == "" {
		return WindowKey(prefix, cid, raw, bucket)
	}
	if !isNumericTenant(tenant) {
		return fmt.Sprintf("%swin:%s:%s:%d:%s", prefix, bucket, tenantKeyPart(tenant), cid, raw)
	}
	return fmt.Sprintf("%swin:%s:t:%s:%d:%s", prefix, bucket, tenantKeyPart(tenant), cid, raw)
}

func WindowKey(prefix string, cid int, raw, bucket string) string {
	return fmt.Sprintf("%swin:%s:%d:%s", prefix, bucket, cid, raw)
}

func tenantKeyPart(tenant string) string {
	return tenant
}

func isNumericTenant(tenant string) bool {
	if tenant == "" {
		return false
	}
	for _, r := range tenant {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func BindingKey(prefix string, cid int, raw string) string {
	return fmt.Sprintf("%sbinding:%d:%s", prefix, cid, raw)
}

func CredentialKey(prefix string, cid int) string {
	return fmt.Sprintf("%scredential:%d", prefix, cid)
}

func ProviderKey(prefix string, pid int) string {
	return fmt.Sprintf("%sprovider:%d", prefix, pid)
}

func CandidateIndexKey(prefix, tenant, canonical, profile, modality string) string {
	return fmt.Sprintf("%sidx:model:%s:%s:%s:%s", prefix, tenant, canonical, profile, modality)
}

func ReadyKey(prefix string) string {
	return fmt.Sprintf("%smeta:ready", prefix)
}

func EpochKey(prefix string) string {
	return fmt.Sprintf("%smeta:epoch", prefix)
}

// CoverageKey stores the expected tenant-aware node keys written by the
// cutover migration. Authoritative startup validates every listed key before
// opening the recovery gate.
func CoverageKey(prefix string) string {
	return fmt.Sprintf("%smeta:coverage", prefix)
}

// RecoveryDebounceKey is the cluster-wide coordination key used by
// Manager.MarkClosedDebounced. When set (with TTL), all subsequent
// callers see "already debounced" and skip the EnterRecovery write
// — this caps the cluster's epoch counter inflation to one bump per
// debounce window regardless of how many gateway instances are
// simultaneously observing Redis health failures.
func RecoveryDebounceKey(prefix string) string {
	return fmt.Sprintf("%smeta:recovery_debounce", prefix)
}

func NodeInvalidationChannel(prefix string) string {
	return fmt.Sprintf("%smeta:node_invalidation", prefix)
}

// NodeInvalidationPayload is the wire format published on the
// NodeInvalidationChannel. The first line is the tenant ID (may be empty
// for legacy single-tenant deployments), the second the credential ID, and
// the third the raw model name. Keeping the layout here in lockstep with
// Manager.invalidateNode guarantees subscribers can decode without
// speculative parsing.
type NodeInvalidationPayload struct {
	TenantID     string
	CredentialID int
	RawModel     string
}

func (p NodeInvalidationPayload) String() string {
	return fmt.Sprintf("%s\n%d\n%s", p.TenantID, p.CredentialID, p.RawModel)
}

func ParseNodeInvalidation(payload string) (NodeInvalidationPayload, bool) {
	parts := strings.SplitN(payload, "\n", 3)
	if len(parts) != 3 {
		return NodeInvalidationPayload{}, false
	}
	credentialID, err := strconv.Atoi(parts[1])
	if err != nil || credentialID <= 0 || parts[2] == "" {
		return NodeInvalidationPayload{}, false
	}
	return NodeInvalidationPayload{TenantID: parts[0], CredentialID: credentialID, RawModel: parts[2]}, true
}
