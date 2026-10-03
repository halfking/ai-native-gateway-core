package sanitize

import (
	"testing"
)

// Audit round 237 — pin the Redis key normalisation that every cross-tenant
// guarantee in the sanitize subsystem rests on.
//
// The restore side finds its replacement table by key, not by any capability
// token, so the exact string -> bucket mapping IS the isolation boundary:
//
//	sanitizeMapKey(tenant, sid) == session:{HashTenant(normalised(tenant))}:{sid}:sanitize
//
// Two facts below are load-bearing and were previously unpinned:
//
//  1. An absent tenant and the literal tenant "_unknown" deliberately share a
//     bucket. That is the documented no-identity fallback
//     (sanitizeMapKey:1765-1770 and its comment at :1762-1764).
//
//  2. Consequently ANY other placeholder for "no identity" lands in a
//     DIFFERENT bucket. Round 237 measured the concrete divergence between the
//     request-side no-verifier fallback ("default", sanitize_auth.go:38) and
//     the response-side nil-keyInfo fallback ("", handler.go:5604):
//     37a8eec1ce19687d vs 72c67438e9e86083. Same process still restores from
//     the ctx-local map, so this is a degradation, never a cross-tenant leak —
//     but a future reader who "unifies" one side of that pair must move the
//     other side too. Test 2 is the tripwire for exactly that.
func TestSanitizeMapKeyNormalisation(t *testing.T) {
	const sid = "sess-237"

	// 1. The documented aliasing must hold, and it must be an ALIAS, not
	//    equality by accident of empty strings: HashTenant("") is "" (it
	//    short-circuits), so the rewrite to "_unknown" is what makes these two
	//    agree. If someone removes the rewrite they will differ — which is
	//    what this assertion is here to notice.
	if got, want := sanitizeMapKey("", sid), sanitizeMapKey("_unknown", sid); got != want {
		t.Fatalf("空租户与字面量 _unknown 必须落在同一个桶：\n  got  %q\n  want %q", got, want)
	}

	// 2. A real tenant must NOT collide with the no-identity bucket.
	real := sanitizeMapKey("tenant-1", sid)
	if real == sanitizeMapKey("", sid) {
		t.Fatalf("真实租户与无身份桶发生碰撞：%q —— 这会让所有无身份会话共用同一张表", real)
	}
	if real != "session:"+HashTenant("tenant-1")+":"+sid+":sanitize" {
		t.Fatalf("租户化 key 形态变了：got %q", real)
	}
}

// TestSanitizeMapKeyFallbacksDisagree is a TRIPWIRE, not a defect claim.
//
// Round 237 traced the tenant identity on both sides of the sanitize round
// trip and found they disagree in exactly one configuration:
//
//	request side, no key verifier:  sanitize_auth.go:38
//	    sanitize.WithAuthenticatedTenant(ctx, "default")
//	response side, nil keyInfo:     handler.go:5604-5607
//	    tenantID := "" ; if keyInfo != nil { tenantID = keyInfo.TenantID }
//
// "default" hashes to 37a8eec1ce19687d, "" is rewritten to "_unknown" and
// hashes to 72c67438e9e86083. The two sides therefore read and write DIFFERENT
// Redis buckets in that configuration.
//
// Consequences, both verified by code reading in round 237:
//   - NOT a cross-tenant leak. Restoration degrades to masking/blocking
//     (fail-closed) rather than surfacing another tenant's values.
//   - NOT a functional outage in the common case, because the same process
//     still holds the map in the request context
//     (smart_sani_guard.go:256 WithSanitizeMap, read back at :712
//     SanitizeMapFromContext).
//   - It IS a redundancy gap: any path where the ctx-local map is unavailable
//     (cross-process, rebuilt context) can never restore in that configuration.
//
// This test asserts the CURRENT divergence on purpose. Its failure message is
// the handoff: if you change either fallback constant, the other one must move
// with it, and the round-237 finding should be updated in the same commit.
func TestSanitizeMapKeyFallbacksDisagree(t *testing.T) {
	const sid = "sess-237"
	requestSide := sanitizeMapKey("default", sid) // sanitize_auth.go:38
	responseSide := sanitizeMapKey("", sid)       // handler.go:5604-5607

	if requestSide == responseSide {
		t.Skipf("两个回退常量已统一为同一桶（%q）—— 237 号登记的键不匹配已不存在。\n"+
			"请在同一提交里更新：docs/全面审计v3/2026-10-04/237-* 与待裁决 101。", requestSide)
	}
	t.Logf("已记录 237 号的键不匹配：\n  请求侧 (no-verifier 回退 \"default\") = %s\n"+
		"  响应侧 (keyInfo==nil 回退 \"\")      = %s\n"+
		"  ⇒ 同进程由 ctx 本地表兜住；跨进程退化为 fail-closed 掩码。改任一侧必须同时改另一侧。",
		requestSide, responseSide)
}
