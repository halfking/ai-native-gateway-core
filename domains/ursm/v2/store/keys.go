package store

import "fmt"

func NodeKeyForTenant(prefix, tenant string, cid int, raw string) string {
	if tenant == "" {
		return NodeKey(prefix, cid, raw)
	}
	return fmt.Sprintf("%snode:%s:%d:%s", prefix, tenantKeyPart(tenant), cid, raw)
}

func NodeKey(prefix string, cid int, raw string) string {
	return fmt.Sprintf("%snode:%d:%s", prefix, cid, raw)
}

func WindowKeyForTenant(prefix, tenant string, cid int, raw, bucket string) string {
	if tenant == "" {
		return WindowKey(prefix, cid, raw, bucket)
	}
	return fmt.Sprintf("%swin:%s:%s:%d:%s", prefix, bucket, tenantKeyPart(tenant), cid, raw)
}

func WindowKey(prefix string, cid int, raw, bucket string) string {
	return fmt.Sprintf("%swin:%s:%d:%s", prefix, bucket, cid, raw)
}

func tenantKeyPart(tenant string) string {
	return tenant
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

// RecoveryDebounceKey is the cluster-wide coordination key used by
// Manager.MarkClosedDebounced. When set (with TTL), all subsequent
// callers see "already debounced" and skip the EnterRecovery write
// — this caps the cluster's epoch counter inflation to one bump per
// debounce window regardless of how many gateway instances are
// simultaneously observing Redis health failures.
func RecoveryDebounceKey(prefix string) string {
	return fmt.Sprintf("%smeta:recovery_debounce", prefix)
}
