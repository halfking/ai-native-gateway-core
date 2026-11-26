package sanitize

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// r1059（2026-11-25，211 ZCode 会话砖化轮）签名 thinking 分级阻断的**类型作用域**钉测。
//
// 生产取证（request 180d27c2-22f8-4499-a1d2-a1e5efa34897，8.9MB /v1/messages，
// gateway 400 "Invalid request body"；日志 action=block / types=account /
// rules=pii.account；同一会话 16:07/16:20/16:31/16:37 四连挡 ⇒ 会话砖化）：
//
//	强拒闸此前对**所有检出类型**跑「不透明令牌」形状判据（≥24 且含数字 /
//	JWT 三段点）：
//	  · account：长用户名 / 带点账号名被判成强凭据（生产实证 types=account）；
//	  · email：多点分邮箱被判成 JWT 形态（本文件钉测复现 types=email）；
//	  · id_card / credit_card：18 位身份证、16-19 位银行卡本就含数字，
//	    凡长度过线即「强凭据」。
//
// 契约（反向收紧，不逐一打地鼠）：
//
//	· 只有 secret（口令/密钥/令牌/JWT）参与强拒——「凭据外泄」风险只对
//	  凭据成立（签名可伪造 ⇒ 不验签上游等于明文出网）；
//	· 其余类型（account/email/phone/id_card/credit_card/custom…）一律降
//	  weak：放行 + ERROR 观测（PII 出网是「脱敏旁路」可见面，不是凭据泄露）；
//	· secret 面形状判据不动：sk-/ghp_/AKIA 等厂商前缀照旧强拒（D1 契约
//	  不变，TestD1SignedThinkingCredentialRejected 继续守着）。

// TestR1059SignedThinkingAccountNotRejected 端到端：带签名的 thinking 里只有
// 非凭据类 PII（长用户名 / 多点分邮箱，形状上会被 strongCredentialCandidate
// 判真）⇒ 请求必须放行（200），下游 handler 必须被调用，body 逐字未动。
//
// 修复前此测试红（400 会话砖化——account 形态是生产事故本体，email 形态是
// 钉测复现出的第二形态）；它是 r1059 的回归门。
func TestR1059SignedThinkingAccountNotRejected(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSanitizer(configured)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// 两个值都刻意取「旧强凭据判据必真」的形状：
	//  · kaixuan_deploy_user_2024_prod：29 字符 ≥24 且含数字、非 hex 簇 ⇒ 旧判据④真
	//  · svc.account@prod.internal.kaixuan：≥16 且 ≥2 个点 ⇒ 旧判据③（JWT 形态）真
	// 两条都是合法账号字面量（accountLiteralPattern 字形内），生产里就是登录名。
	body := `{"model":"claude-sonnet-5-5","stream":true,"messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"部署账号 kaixuan_deploy_user_2024_prod；服务账号 svc.account@prod.internal.kaixuan","signature":"sig_forged_by_client"}]}]}`

	var nextCalled bool
	var nextBody string
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		b, _ := io.ReadAll(r.Body)
		nextBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "gw_r1059_account_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("账号型签名 thinking 不得拒整请求（否则重演 211 会话砖化），got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !nextCalled {
		t.Fatal("下游 handler 必须被调用")
	}
	if nextBody != body {
		t.Fatalf("body 被改写：\n got: %s\nwant: %s", nextBody, body)
	}
}

// TestR1059SecretCoPresentStillRejected 端到端负控：账号与真凭据**同现**时
// 仍然强拒——account 豁免不得把「user= 语境里的厂商密钥」一并放走。
// 密钥由 secret/api_key 族独立捕获（与 account 片段互不隶属），照旧进强凭据闸。
func TestR1059SecretCoPresentStillRejected(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSanitizer(configured)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewSanitizeInputMiddleware(s, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"model":"claude-sonnet-5-5","stream":true,"messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"登录名 kaixuan_deploy_user_2024_prod，密钥 sk-ant-api03-9f2K7Lx4Qw8ErT5yU2iO6pL1kJ8hG0fD","signature":"sig_forged_by_client"}]}]}`

	var nextCalled bool
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "gw_r1059_cosecret_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("与账号同现的真凭据必须仍被拒（400），got status=%d body=%s", rec.Code, rec.Body.String())
	}
	if nextCalled {
		t.Fatal("请求被拒时下游 handler 不得被调用（凭据不得出网）")
	}
}

// TestR1059ClassifyTypeScope 分类单测：同一段「强凭据形状」的值，挂在
// secret 类型上必须进 strong（强拒面），挂在非凭据类型（account/email 等）
// 上必须进 weak（放行面）——钉住「类型作用域」这一判据本身。
func TestR1059ClassifyTypeScope(t *testing.T) {
	// ★ 2026-11-26（r1060 收紧 strongCredentialCandidate 之后同步）：
	//   旧夹具 "kaixuan_deploy_user_2024_prod"（31 字符 / 全小写）在新判据下
	//   **不再是**强凭据形状（不透明令牌档已收紧为 ≥32 字符 + 含数字 + 大小写混合）
	//   ⇒ 它的前置断言必然红 ⇒ 那是**夹具陈旧**，不是判据坏了。
	//   ⇒ 本轮只把夹具换成新判据下**确实成立**的形状（35 字符 / 含数字 / 大小写混合），
	//   **判据本身一个字没动** ⇒ 本测试要钉的仍是「类型作用域」这一条。
	shaped := "kaixuan_deploy_user_2024_PROD_KEY" // strongCredentialCandidate 必真的形状
	if !strongCredentialCandidate(shaped) {
		t.Fatalf("前置失效：%q 应被强凭据形状判据判真（否则测试失去对照意义）", shaped)
	}
	fragments := []SensitiveFragment{
		{Type: TypeAccount, Value: shaped, RuleID: "pii.account"},
		{Type: TypeEmail, Value: shaped, RuleID: "pii.email"},
		{Type: TypeSecret, Value: shaped, RuleID: "secret.api_key"},
	}
	strong, weak := classifySignedThinkingFragments(fragments)
	if len(strong) != 1 || strong[0].Type != TypeSecret {
		t.Fatalf("secret 类型必须进 strong 面，got strong=%d weak=%d", len(strong), len(weak))
	}
	if len(weak) != 2 {
		t.Fatalf("account/email 必须进 weak 面（永不强拒），got strong=%d weak=%d", len(strong), len(weak))
	}
	for _, f := range weak {
		if f.Type == TypeSecret {
			t.Fatalf("secret 片段不得进 weak 面")
		}
	}
}
