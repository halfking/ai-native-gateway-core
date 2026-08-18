package cache

import (
	"fmt"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// keyf 是包内 fmt.Sprintf 别名,集中 key 构造便于审计。
func keyf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// nodeMirrorKeyForTenant mirrors store.NodeKeyForTenant while keeping the
// cache package independent from Manager configuration.
func nodeMirrorKeyForTenant(tenant string, credID int, raw string) string {
	return nodeMirrorKeyForTenantWithPrefix("ursm:v2:", tenant, credID, raw)
}

func nodeMirrorKeyForTenantWithPrefix(prefix, tenant string, credID int, raw string) string {
	return store.NodeKeyForTenant(prefix, tenant, credID, raw)
}

func nodeMirrorKey(credID int, raw string) string {
	return nodeMirrorKeyForTenantWithPrefix("ursm:v2:", "", credID, raw)
}

// StickyKey 复刻 domains/routing/sticky.go buildStickyKeys 的 key,
// 但加 ursm:v2:sticky: 前缀,与旧 sticky_sessions 表物理隔离。
// level=1/2/3 对应 L1/L2/L3。
func StickyKey(level int, raw string) string {
	return keyf("ursm:v2:sticky:L%d:%s", level, raw)
}

// IntentKey 是 session intent 的 Redis key。
func IntentKey(sessionID string) string {
	return keyf("ursm:v2:intent:%s", sessionID)
}
