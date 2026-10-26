package middleware

// maintain_api_bypass_test.go — 「/maintain-api 必须显式登记在全局 API-key 门的
// bypass 名单里」静态契约腿。
//
// ★ 为什么静态腿与行为腿分开（behavioral leg 在 cmd/gateway/maintain_api_bypass_contract_test.go）：
//   行为腿证明「匿名 /maintain-api/* 拿到 503 maintain.not_configured 而不是 401」；
//   静态腿证明「名单里两条都在」——因为 "/maintain/" 前缀**不匹配** "/maintain-api/..."
//   ⇒ 只登记一条时行为腿会红，但「谁改的、改了哪条」只能靠静态腿定位。
//
// ★ 维护契约：maintain_proxy.go 以后若改名/换前缀（例如 /maintain-api → /ops-api），
//   ① 本腿（改这里）② 行为腿的路径表（cmd/gateway/…）③ auth_mw.go 的 bypass 名单
//   三处必须同步改，否则本腿会红。

import "testing"

func TestMaintainAPIPathsAreRegisteredInAuthBypassRule(t *testing.T) {
	rule := NewAuthMiddleware("test-global-api-key").bypass

	hasExact := false
	for _, p := range rule.ExactPaths {
		if p == "/maintain-api" {
			hasExact = true
			break
		}
	}
	if !hasExact {
		t.Fatal(`bypass ExactPaths must contain "/maintain-api" (bare root path); ` +
			"缺它 ⇒ 裸 /maintain-api 被全局 API-key 门拦成 401 missing_key")
	}

	hasPrefix := false
	for _, p := range rule.PathPrefixes {
		if p == "/maintain-api/" {
			hasPrefix = true
			break
		}
	}
	if !hasPrefix {
		t.Fatal(`bypass PathPrefixes must contain "/maintain-api/" (canonical API subtree); ` +
			`注意 "/maintain/" 前缀**不匹配** "/maintain-api/..."（"-" 不是 "/"）`)
	}
}

// 反向腿：豁免必须精确到 /maintain-api/*，不得把相邻前缀一起放开。
// ★ 尤其 "/maintain" 与 "/maintain-api" 是**两个**独立前缀：
//
//	把 "/maintain-api/" 写成 "/maintain" 之类的宽前缀 ⇒ 会连带放开
//	/maintainXYZ 这类未登记路径 ⇒ 门形同虚设。
func TestMaintainAPIBypassDoesNotOverreachAdjacentPaths(t *testing.T) {
	rule := NewAuthMiddleware("test-global-api-key").bypass

	for _, p := range rule.PathPrefixes {
		if p == "/maintain-api" {
			t.Fatal(`PathPrefixes must not contain bare "/maintain-api" (missing trailing slash) ` +
				"⇒ 它会连带匹配 /maintain-apixxx 之类未登记路径")
		}
	}
	for _, p := range rule.ExactPaths {
		if p == "/maintain-api/" {
			t.Fatal(`ExactPaths must not contain "/maintain-api/" (trailing slash) ` +
				"⇒ 精确匹配永远不命中 ⇒ 该前缀下的所有路径仍然 401")
		}
	}
}
