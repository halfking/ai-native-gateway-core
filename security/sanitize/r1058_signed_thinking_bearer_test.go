package sanitize

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1058（2026-11-18，211 生产事故）：ZCode 报障「provider=new-provider-2
// model=claude-sonnet-5-5 reason=invalid_request status=400 Invalid request
// body」（request 8c7f0963-2b75-4338-889f-552ca82c2f0d，7.66MB / 3049 条消息
// 的会话）。
//
// 取证链（日志 + session-archive 归档件解码重放）：
//  1. 网关自身发出 400（85 字节响应体 = writeSanitizeFailure 的
//     {"type":"error",...,"message":"Invalid request body"}），无任何 upstream
//     尝试；sanitize WARN「invalid sanitizable request: sensitive signed
//     thinking」。
//  2. 归档件重放定案：最后一个（带签名的）thinking 块里，模型引用代码注释
//     「line 38: `Bearer token` 检查」，bearer 规则把普通英文单词 "token"
//     （LLM token 的本义）当成凭据值 ⇒ 签名 thinking 不可改写 ⇒ 整请求被拒。
//     客户端每次重发同一份会话 ⇒ retryable=false ⇒ 会话砖化。
//
// 双根修：bearer 规则引擎级可信度守卫（本文件的规则面钉测）＋ 签名 thinking
// 「检测不改写、放行＋WARN」（TestR1058SignedThinkingNoLongerBricksRequest）。
func TestR1058BearerWordAfterBearerIsNotCredential(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// 托管快照路径：发布与生产 DB（sanitize_rule_versions id=2）逐字同文的
	// bearer 规则，验证 isBearerRegex 的 pattern-文本识别对快照同样生效。
	managedDet, managed := managedFixture(t)
	managedCfg := managed.Current().Config()
	managedCfg.Rules = append(managedCfg.Rules,
		sanitizerules.Rule{ID: "secret.bearer", Name: "secret.bearer", Type: "secret", Kind: "pattern", Group: 1, Pattern: `(?i)\bBearer\s+([A-Za-z0-9._~+/=-]+)`, Scopes: []string{"input", "output"}, Action: "mask", Enabled: true},
		sanitizerules.Rule{ID: "secret.bearer.egress", Name: "secret.bearer.egress", Type: "secret", Kind: "pattern", Group: 1, Pattern: `(?i)\bBearer\s+([A-Za-z0-9._~+/=-]+)`, Scopes: []string{"egress"}, Action: "block", Enabled: true},
	)
	if _, err := managed.Publish(context.Background(), 1, managedCfg, "tester", "r1058 fixture"); err != nil {
		t.Fatal(err)
	}

	for name, detector := range map[string]*PatternDetector{
		"fallback":   NewPatternDetector(),
		"configured": configured,
		"managed":    managedDet,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSanitizer(detector)
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithAuthenticatedTenant(context.Background(), "default")

			// 必须继续检出（含既有钉测形态 operational_patterns_test.go 的
			// `Bearer abc.DEF-123`，保持两代钉测同向）。
			mustDetect := []string{
				"Authorization: Bearer abc.DEF-123",
				"curl -H 'Authorization: Bearer sk-ant-api03-x7Kq2mW9eLbT4vRrN8zA1cD5fGhJ6uM'",
			}
			for _, text := range mustDetect {
				result, err := s.SanitizeInput(ctx, text)
				if err != nil {
					t.Fatalf("detect error on %q: %v", text, err)
				}
				if len(result.SanitizeMap) == 0 {
					t.Errorf("real credential no longer detected: %q", text)
				}
			}

			// 必须不再命中：跟在 Bearer 后面的普通英文词（事故形态）。
			// 事故原文（归档件 msg[3047].blk[0] 的 thinking 摘录）逐字保留。
			proseNotSecret := []string{
				"test_puller.py` 显示拉取器支持鉴权（line 21: `self.required_token`，line 38: Bearer token 检查）",
				"Bearer authentication scheme requires the header",
				"count Bearer tokens before the request",
				"Bearer internationalization is rare",
				"Bearer e.g. the Authorization header",
			}
			for _, text := range proseNotSecret {
				result, err := s.SanitizeInput(ctx, text)
				if err != nil {
					t.Fatalf("detect error on %q: %v", text, err)
				}
				if result.SanitizedText != text {
					t.Errorf("prose after Bearer wrongly sanitized: %q -> %q", text, result.SanitizedText)
				}
			}
		})
	}
}

// TestR1058SignedThinkingProductionReplay：事故 thinking 块原形重放。
// 带签名 ⇒ 不改写、不报错、不铸占位符（旧契约在这里 400 砖化整个会话）。
func TestR1058SignedThinkingProductionReplay(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSanitizer(configured)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthenticatedTenant(context.Background(), "default")

	blockJSON := `{"type":"thinking","thinking":"测试文件 test_puller.py` + "`" + ` 显示拉取器支持鉴权（line 21: ` + "`" + `self.required_token` + "`" + `，line 38: Bearer token 检查）。需要确认关键路径。","signature":"EqoBCkgIBRgCKkCC..."}`

	sanitizer := &requestInputSanitizer{
		ctx:       ctx,
		sanitizer: s,
		offset:    make(map[SensitiveType]int),
		mapping:   make(SanitizeMap),
		usedCount: make(map[string]int),
		existing:  sanitizeReuseIndex{},
	}
	out, changed, err := sanitizer.sanitizeContentBlock([]byte(blockJSON))
	if err != nil {
		t.Fatalf("signed thinking block rejected: %v", err)
	}
	if changed {
		t.Fatal("signed thinking block must pass through verbatim (changed=true)")
	}
	if string(out) != blockJSON {
		t.Fatalf("signed thinking block not byte-identical:\n got: %s\nwant: %s", out, blockJSON)
	}
	if len(sanitizer.mapping) != 0 {
		t.Fatalf("verbatim pass-through must not mint placeholders, got %v", sanitizer.mapping)
	}
	if len(sanitizer.offset) != 0 {
		t.Fatalf("verbatim pass-through must not advance offsets, got %v", sanitizer.offset)
	}
}

// TestR1058SignedThinkingNoLongerBricksRequest：中间件整链钉测——
// 旧契约（r1057 遗留）里这形态的请求收到 400「Invalid request body」；
// 现在必须到达下游 handler，且 body 逐字未动（签名 thinking 原样放行）。
func TestR1058SignedThinkingNoLongerBricksRequest(t *testing.T) {
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
		`{"role":"assistant","content":[{"type":"thinking","thinking":"line 38: Bearer token 检查；password=MySecretPass123","signature":"sig_abc123"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"x"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`

	var nextCalled bool
	var nextBody string
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		b, _ := io.ReadAll(r.Body)
		nextBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "gw_r1058_test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("request bricked by sanitize middleware: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !nextCalled {
		t.Fatal("downstream handler never reached")
	}
	if nextBody != body {
		t.Fatalf("body mutated by middleware:\n got: %s\nwant: %s", nextBody, body)
	}
}
