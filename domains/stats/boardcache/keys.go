package boardcache

import (
	"fmt"
	"strings"
)

const (
	keyBaselinePrefix = "llmgw:stats:baseline:"
	keyBoardPrefix    = "llmgw:stats:board:"
	keyDeltaPrefix    = "llmgw:stats:delta:"
	keyDirtyPrefix    = "llmgw:stats:dirty:"
	keyRebuildLock    = "llmgw:stats:rebuild:lock"
	keyRebuildLast    = "llmgw:stats:rebuild:last"
	keyQPSWindow      = "llmgw:stats:qps:window"
)

// Scope identifies a stats partition: global (all tenants) or one tenant.
type Scope string

const ScopeGlobal Scope = "global"

// ScopeTenant returns the Redis scope for a tenant id.
func ScopeTenant(tenantID string) Scope {
	if tenantID == "" {
		tenantID = "default"
	}
	return Scope("tenant:" + tenantID)
}

// ScopesForEntry returns scopes that should receive increments for a request.
func ScopesForEntry(tenantID string) []Scope {
	if tenantID == "" {
		tenantID = "default"
	}
	return []Scope{ScopeGlobal, ScopeTenant(tenantID)}
}

// TenantFilter maps a scope to SQL tenant filter ("" = all tenants).
func TenantFilter(scope Scope) string {
	s := string(scope)
	if s == string(ScopeGlobal) {
		return ""
	}
	const p = "tenant:"
	if strings.HasPrefix(s, p) {
		return strings.TrimPrefix(s, p)
	}
	return ""
}

func baselineKey(scope Scope, days int) string {
	return fmt.Sprintf("%s%s:%dd", keyBaselinePrefix, scope, days)
}

func baselineMetaKey(scope Scope, days int) string {
	return baselineKey(scope, days) + ":meta"
}

func cacheKeyBoard(scope Scope, days int, providerID int64) string {
	if providerID > 0 {
		return fmt.Sprintf("%s%s:%dd:p:%d", keyBoardPrefix, scope, days, providerID)
	}
	return fmt.Sprintf("%s%s:%dd", keyBoardPrefix, scope, days)
}

func cacheKeyBoardMeta(scope Scope, days int, providerID int64) string {
	return cacheKeyBoard(scope, days, providerID) + ":meta"
}

func drillKey(scope Scope, days int, errorKind, dimension string) string {
	return fmt.Sprintf("llmgw:stats:drill:%s:%dd:%s:%s", scope, days, errorKind, dimension)
}

func boardKey(scope Scope, days int) string {
	return cacheKeyBoard(scope, days, 0)
}

func boardMetaKey(scope Scope, days int) string {
	return cacheKeyBoardMeta(scope, days, 0)
}

func deltaKey(scope Scope, bucketID string) string {
	return fmt.Sprintf("%s%s:b:%s", keyDeltaPrefix, scope, bucketID)
}

func dirtyKey(scope Scope) string {
	return keyDirtyPrefix + string(scope)
}

// BoardDaysPresets are rebuilt and folded for each scope.
var BoardDaysPresets = []int{1, 7, 30, 90}
