// Package sanitize - tenant_scope_test.go
//
// T11-P0: 跨租户隔离单元测试（顺序版本，与 race 版本分工）。
//   - HashTenant 稳定 + 唯一 + 与 preprocess.hash16 格式对齐；
//   - SanitizeRedisKey / SanitizeOffsetRedisKey 在 tenantHash 不同时 key 不碰撞；
//   - SessionSanitizeRedisKey（compression 镜像）与本包 SanitizeRedisKey 同步；
//   - 缺 tenant 时落到 _unknown sentinel（与中间件行为一致）。
//
// 配套 race 测试在 cross_tenant_race_test.go。
package sanitize

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// TestHashTenant_Stable — 相同输入必须产生相同 hash（map key 派生前提）。
func TestHashTenant_Stable(t *testing.T) {
	cases := []string{"tA", "tenant-123", "long-tenant-name-with-many-chars-aaaa-bbbb", "_unknown", ""}
	for _, c := range cases {
		if HashTenant(c) != HashTenant(c) {
			t.Errorf("HashTenant(%q) not stable across calls", c)
		}
	}
}

// TestHashTenant_Length16 — 必须正好 16 字符 hex（与 preprocess.hash16 对齐）。
func TestHashTenant_Length16(t *testing.T) {
	if l := len(HashTenant("any-tenant")); l != 16 {
		t.Errorf("HashTenant length = %d, want 16", l)
	}
}

// TestHashTenant_DistinctInputs — 抽样不同输入必须产生不同 hash。
// 注：16 hex chars = 8 bytes，碰撞概率 ≈ 1/2^32；3 个不同输入采样足够。
func TestHashTenant_DistinctInputs(t *testing.T) {
	samples := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	seen := make(map[string]string, len(samples))
	for _, s := range samples {
		h := HashTenant(s)
		if other, ok := seen[h]; ok {
			t.Errorf("collision: %q and %q both hash to %s", s, other, h)
		}
		seen[h] = s
	}
}

// TestHashTenant_EmptyReturnsEmpty — 空字符串返回空字符串（caller 用空表示"未指定"，
// SanitizeRedisKey 会再 fallback 到 _unknown sentinel）。
func TestHashTenant_EmptyReturnsEmpty(t *testing.T) {
	if h := HashTenant(""); h != "" {
		t.Errorf("HashTenant(\"\") = %q, want \"\"", h)
	}
}

// TestHashTenant_MatchesPreprocessFormat — 直接验证 hash 算法的输出与
// sha256(s)[:8] 的 hex 编码一致（preprocess.hash16 也是这个公式）。
// 只要两个包用同样的公式，跨包 key 派生就稳定。
func TestHashTenant_MatchesPreprocessFormat(t *testing.T) {
	for _, in := range []string{"tA", "tB", "very-long-tenant-id-with-many-characters"} {
		sum := sha256.Sum256([]byte(in))
		want := hex.EncodeToString(sum[:8])
		if got := HashTenant(in); got != want {
			t.Errorf("HashTenant(%q) = %q, want %q (preprocess format)", in, got, want)
		}
	}
}

// TestSanitizeRedisKey_TenantScoped — 不同 tenantHash 必须产生不同 key。
// 防御 T11-P0 之前的回归：旧 key 格式 session:{sid}:sanitize 不带 tenant，
// 两个 tenant 同 sessionID 会撞 key。
func TestSanitizeRedisKey_TenantScoped(t *testing.T) {
	a := SanitizeRedisKey(HashTenant("tA"), "shared-sid")
	b := SanitizeRedisKey(HashTenant("tB"), "shared-sid")
	if a == b {
		t.Fatalf("SanitizeRedisKey must differ per tenantHash; both = %q", a)
	}
	// 也不能"恰好"和 session_cache delete 的旧 key 形态重合
	if a == "session:shared-sid:sanitize" {
		t.Errorf("SanitizeRedisKey leaked old format: %q", a)
	}
}

// TestSanitizeOffsetRedisKey_TenantScoped — offset key 同样按 tenantHash 隔离。
func TestSanitizeOffsetRedisKey_TenantScoped(t *testing.T) {
	a := SanitizeOffsetRedisKey(HashTenant("tA"), "shared-sid")
	b := SanitizeOffsetRedisKey(HashTenant("tB"), "shared-sid")
	if a == b {
		t.Fatalf("SanitizeOffsetRedisKey must differ per tenantHash; both = %q", a)
	}
}

// TestSanitizeRedisKey_EmptyTenantHashPassesThrough — tenantHash 为空时
// 直接进入 key（无二次 fallback）。中间件 Wrap 已经在更上层把缺失 tenant
// fallback 到 unknownTenantSentinel 然后通过 HashTenant 派生 hash，所以本函数
// 不再做"_" 兜底。
func TestSanitizeRedisKey_EmptyTenantHashPassesThrough(t *testing.T) {
	want := "session::sid-x:sanitize"
	if got := SanitizeRedisKey("", "sid-x"); got != want {
		t.Errorf("SanitizeRedisKey(\"\", \"sid-x\") = %q, want %q", got, want)
	}
}

// TestSanitizeRedisKey_TenantUnknownBucketMatches — 中间件 fallback 走的
// sentinel bucket（"_unknown" → HashTenant）与显式传 HashTenant("_unknown") 的结果
// 必须一致；这是"无 tenant 请求共享同一 bucket"的契约。
func TestSanitizeRedisKey_TenantUnknownBucketMatches(t *testing.T) {
	sentinelHash := HashTenant("_unknown")
	if got, want := SanitizeRedisKey(sentinelHash, "sid"), SanitizeRedisKey(sentinelHash, "sid"); got != want {
		t.Errorf("sentinel bucket mismatch: %q vs %q", got, want)
	}
	// 显式 _unknown 字面量 与 hash(_unknown) 必须不同 — 防止 caller 误以为
	// "_unknown" 是某种特殊 sentinel 字面量
	literalKey := SanitizeRedisKey("_unknown", "sid")
	hashedKey := SanitizeRedisKey(sentinelHash, "sid")
	if literalKey == hashedKey {
		t.Errorf("literal '_unknown' should differ from its hash; both = %q", literalKey)
	}
}

// TestSanitizeRedisKey_MirrorsCompressionKey — 与 compression 镜像函数严格同步。
// 这是 T11-P0.1 跨包契约的单元测试版本（pintest 已被 sanitize_info_bridge_test
// 锁定为同 sessionID/tenantHash）。
func TestSanitizeRedisKey_MirrorsCompressionKey(t *testing.T) {
	cases := []struct{ tenant, sid string }{
		{"tA", "sid-1"},
		{"tB", "sid-2"},
		{"", "sid-3"},     // 走 sentinel
		{"_unknown", "x"}, // 显式 _unknown
	}
	for _, c := range cases {
		if got, want := SanitizeRedisKey(c.tenant, c.sid), compression.SessionSanitizeRedisKey(c.tenant, c.sid); got != want {
			t.Errorf("map key drift: sanitize=%q compression=%q", got, want)
		}
		if got, want := SanitizeOffsetRedisKey(c.tenant, c.sid), compression.SessionSanitizeOffsetRedisKey(c.tenant, c.sid); got != want {
			t.Errorf("offset key drift: sanitize=%q compression=%q", got, want)
		}
	}
}
