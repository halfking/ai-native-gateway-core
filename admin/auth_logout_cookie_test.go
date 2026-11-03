package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleLogoutClearsCookieWithRequestSecurity 钉住「退出登录」发出来的删除指令。
//
// ★★ 2026-11-03（12h 审计 C9）**判据更新**：本仓把 `llmgw_session` 的作用域从
//
//	**host-only** 改成了 **domain cookie**（Domain=.itestu.cn）⇒ ⇒ 而 cookie 的
//	身份键是 **name + domain + path** ⇒ ⇒ 两种作用域是 cookie jar 里**两个不同的键**
//	⇒ ⇒ 只发一条带 Domain 的删除指令 ⇒ ⇒ **升级前签发的 host-only 那条仍留在
//	⇒ ⇒ 浏览器里** ⇒ ⇒ 用户点了「退出登录」仍处于登录态**。
//	⇒ ⇒ 正确形态是**两条都发**：一条带 Domain（当前形态）、一条不带 Domain
//	⇒ ⇒ （host-only，升级前存量会话用的那把钥匙）。
//
// ★ 本测试此前断言的是「**恰好一条**」⇒ ⇒ 那条断言**编码了缺陷本身**
//
//	⇒ ⇒ 本轮把它改成「两条，且**两条都是删除指令**（MaxAge=-1、值空）」。
func TestHandleLogoutClearsCookieWithRequestSecurity(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COOKIE_SECURE", "")

	req := httptest.NewRequest("POST", "http://127.0.0.1:8782/api/auth/logout", nil)
	response := httptest.NewRecorder()
	(&Handler{}).handleLogout(response, req)

	cookies := response.Result().Cookies()
	// ★ 循环回环请求的 Host 是 127.0.0.1 ⇒ ⇒ `cookieDomain()` 返回 ""
	//   ⇒ ⇒ 本部署**不在用域作用域** ⇒ ⇒ 只发**一条** host-only 删除指令
	//   ⇒ ⇒ （域作用域那条见下面那条专门用 .itestu.cn 域名的测试。）
	if len(cookies) != 1 {
		t.Fatalf("expected one session cookie deletion in host-only mode, got %d: %#v",
			len(cookies), cookies)
	}

	c := cookies[0]
	if c.Secure {
		t.Fatal("logout cookie security must match an HTTP loopback session cookie")
	}
	if c.Name != CookieName || c.Path != "/" || c.MaxAge != -1 || c.Value != "" {
		t.Fatalf("unexpected session cookie deletion attributes: %#v", c)
	}
	if c.Domain != "" {
		t.Fatalf("★ host-only 模式下不应带 Domain（实际 %q）", c.Domain)
	}
}

// TestClearSessionCookieClearsBothScopesInDomainMode 钉住「**域作用域**部署下两条都发」。
//
// ★ 这是 C9 的**本体**：本仓把作用域从 host-only 改成 domain cookie 之后，
//
//	存量会话的钥匙是「host-only」⇒ ⇒ 只发带 Domain 的删除指令清不掉它 ⇒ ⇒
//	⇒ 用户点了退出仍是登录态 ⇒ ⇒ 域作用域部署下**必须两条都发**。
func TestClearSessionCookieClearsBothScopesInDomainMode(t *testing.T) {
	req := httptest.NewRequest("POST", "http://llm.itestu.cn/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	clearSessionCookie(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected two deletions (domain scope + host-only scope), got %d: %#v", len(cookies), cookies)
	}
	var sawDomain, sawHostOnly bool
	for _, c := range cookies {
		if c.Domain == "" {
			sawHostOnly = true
		} else {
			sawDomain = true
		}
	}
	if !sawDomain {
		t.Error("★ 缺少「域作用域」的删除指令 ⇒ ⇒ 升级后签发的会话不会被清掉")
	}
	if !sawHostOnly {
		t.Error("★ 缺少「host-only 作用域」的删除指令 ⇒ ⇒ ★ 升级前签发的存量会话不会被清掉 ⇒ ⇒ 用户点了退出仍是登录态")
	}
}

// TestClearSessionCookieEmitsDeletionOnly 钉住「两条都必须是**删除**指令」。
//
// ★ 为什么要单独一条：上面那条只数了条数与属性 ⇒ ⇒ 如果将来其中一条被误改成
// ★ 「签发」形态（值非空 / MaxAge≥0），上面那条**仍会绿** ⇒ ⇒ 这里单独钉死。
func TestClearSessionCookieEmitsDeletionOnly(t *testing.T) {
	req := httptest.NewRequest("POST", "http://llm.itestu.cn/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	clearSessionCookie(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected two deletion cookies (domain mode), got %d: %#v", len(cookies), cookies)
	}
	for _, c := range cookies {
		if c.MaxAge != -1 {
			t.Errorf("★ cookie %q 的 MaxAge=%d ⇒ 它**不是**删除指令 ⇒ ⇒ 会在浏览器里留一条同名的会话 cookie", c.Name, c.MaxAge)
		}
		if c.Value != "" {
			t.Errorf("★ cookie %q 的值非空 ⇒ ⇒ 它不是删除指令", c.Name)
		}
		if c.Path != "/" {
			t.Errorf("★ cookie %q 的 Path=%q ⇒ ⇒ 作用域与签发侧不一致 ⇒ ⇒ 可能删不到", c.Name, c.Path)
		}
	}
}

// TestClearSessionCookieHostOnlyEntryHasNoDomain 钉住「host-only 那条**不带 Domain**」。
//
// ★ 为什么单独钉：Go 的 `http.SetCookie` 在 `Domain == ""` 时**不写** Domain 属性
//
//	⇒ ⇒ 浏览器据此按 host-only 存 ⇒ ⇒ 与升级前签发的那把钥匙一致 ⇒ ⇒ 才真的删得掉。
//	⇒ ⇒ 若哪天有人给「host-only 那条」也填上 Domain ⇒ ⇒ 两条会变成同一把钥匙
//	⇒ ⇒ 存量会话又清不掉了 ⇒ ⇒ 所以必须钉。
func TestClearSessionCookieHostOnlyEntryHasNoDomain(t *testing.T) {
	req := httptest.NewRequest("POST", "http://llm.itestu.cn/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	clearSessionCookie(rec, req)

	cookies := rec.Result().Cookies()
	var hostOnly *http.Cookie
	for _, c := range cookies {
		if c.Domain == "" {
			hostOnly = c
			break
		}
	}
	if hostOnly == nil {
		t.Fatalf("★ 没有找到 host-only 那条删除指令（cookies=%#v）", cookies)
	}
	if hostOnly.Domain != "" {
		t.Errorf("★ host-only 那条带了 Domain=%q ⇒ ⇒ 它与「域作用域那条」变成同一把钥匙 ⇒ ⇒ 存量 host-only 会话清不掉", hostOnly.Domain)
	}
}
