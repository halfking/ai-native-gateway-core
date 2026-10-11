package streaming

// /v1/models 的鉴权契约。
//
// 2026-10-11 生产实测的缺口（154 与 245 均复现）：
//
//	不带 Authorization                 → 401
//	Bearer sk-totally-bogus（伪造）     → 200，返回全量 615 个模型
//	已吊销的 key                        → 200
//	同一 token 打 /v1/chat/completions  → 401
//
// 成因不是「鉴权没开」，是**委托链断了一节**：middleware/auth_mw.go 明确规定
// sk-* 由各 handler 自行调用 KeyVerifier（Rule 20 §2，注释里写得很清楚），而
// ModelsHandler 从未接上。中间件为内部调用者开的那扇门于是对外敞开——任何
// sk- 开头的字符串都能通行。
//
// 判据写法沿用本仓纪律：断言「没被鉴权放过」，并额外断言「校验器确实被调用过」，
// 排除「401 来自别处 / 压根没走到鉴权」这两种假绿。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

type modelsAuthStub struct {
	enabled bool
	valid   map[string]bool
	status  string
	calls   int
	seen    []string
}

func (s *modelsAuthStub) Enabled() bool { return s.enabled }

func (s *modelsAuthStub) Verify(_ context.Context, rawKey string) (*authentication.KeyInfo, error) {
	s.calls++
	s.seen = append(s.seen, rawKey)
	if s.valid[rawKey] {
		return &authentication.KeyInfo{ID: 1, Status: s.status}, nil
	}
	return nil, context.Canceled // 任意非 nil error 都算校验失败
}

func (s *modelsAuthStub) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return nil, nil
}
func (s *modelsAuthStub) CheckBudget(context.Context, int) error { return nil }
func (s *modelsAuthStub) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

// setKeyVerifierForTest 与 embeddings_test.go 的同名注入点同构：测试文件内定义，
// 同包可见。
func (h *ModelsHandler) setKeyVerifierForTest(v requestKeyVerifier) { h.keyVerifier = v }

func newModelsHandlerForAuth(v requestKeyVerifier) *ModelsHandler {
	h := NewModelsHandler()
	h.setKeyVerifierForTest(v)
	return h
}

func probeModelsAuth(t *testing.T, v requestKeyVerifier, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	newModelsHandlerForAuth(v).ServeHTTP(rec, req)
	return rec
}

// TestModelsRejectsUnverifiedSkShapedBearer 是本文件的主契约：中间件放行的
// sk-* 形态，handler 必须自己验。
func TestModelsRejectsUnverifiedSkShapedBearer(t *testing.T) {
	stub := &modelsAuthStub{enabled: true, valid: map[string]bool{}}

	for _, bearer := range []string{
		"sk-totally-bogus",
		"sk-",
		"sk-1RYdP00xxxxxxxxxxxxxxxx",
		"sk-revoked-but-well-formed",
	} {
		rec := probeModelsAuth(t, stub, bearer)

		// 前提仍在场：校验器必须真的被调用过，否则 401 可能来自别处，
		// 而「没调用却也 401」不是本文件要钉的契约。
		found := false
		for _, got := range stub.seen {
			if got == bearer {
				found = true
			}
		}
		if !found {
			t.Errorf("KeyVerifier 未被调用，判据失去意义: bearer=%q", bearer)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("伪造/吊销的 key 未被拒绝（HTTP %d）: bearer=%q", rec.Code, bearer)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
			t.Errorf("缺少 WWW-Authenticate 头: bearer=%q got=%q", bearer, got)
		}
	}
}

// TestModelsRejectsMissingBearer 固化缺凭据时的响应。
func TestModelsRejectsMissingBearer(t *testing.T) {
	stub := &modelsAuthStub{enabled: true, valid: map[string]bool{}}
	rec := probeModelsAuth(t, stub, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据应 401，得到 %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v", err)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "missing_key" {
		t.Errorf("错误码应为 missing_key，得到 %v", errObj["code"])
	}
}

// TestModelsAcceptsVerifiedKey 反向契约：合法 key 必须放行，否则这次修复就把
// 所有模型发现客户端打死。
func TestModelsAcceptsVerifiedKey(t *testing.T) {
	stub := &modelsAuthStub{enabled: true, valid: map[string]bool{"sk-real-key-abc": true}}
	rec := probeModelsAuth(t, stub, "sk-real-key-abc")
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("合法 key 被拒（HTTP %d）: %s", rec.Code, rec.Body.String())
	}
	if stub.calls != 1 {
		t.Errorf("合法 key 应恰好校验一次，实际 %d 次", stub.calls)
	}
}

// TestModelsWithoutVerifierKeepsLegacyBehaviour 固化 lite / 本地无校验器模式的
// 行为不变——这次修复不能把无库模式一起打死。
func TestModelsWithoutVerifierKeepsLegacyBehaviour(t *testing.T) {
	for name, v := range map[string]requestKeyVerifier{
		"nil verifier":      nil,
		"verifier disabled": &modelsAuthStub{enabled: false, valid: map[string]bool{}},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewModelsHandler()
			if v != nil {
				h.setKeyVerifierForTest(v)
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer sk-anything")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("无校验器模式下不应 401，得到 %d", rec.Code)
			}
		})
	}
}

// TestModelsAuthErrorShapeMatchesOpenAI 钉住错误响应结构：客户端会因为结构
// 不同而走不同分支，改结构等于改契约。
func TestModelsAuthErrorShapeMatchesOpenAI(t *testing.T) {
	stub := &modelsAuthStub{enabled: true, valid: map[string]bool{}}
	rec := probeModelsAuth(t, stub, "sk-totally-bogus")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 error 对象: %s", rec.Body.String())
	}
	if errObj["type"] != "authentication_error" {
		t.Errorf("type 应为 authentication_error，得到 %v", errObj["type"])
	}
	if errObj["code"] != "invalid_key" {
		t.Errorf("code 应为 invalid_key，得到 %v", errObj["code"])
	}
	if msg, _ := errObj["message"].(string); strings.TrimSpace(msg) == "" {
		t.Error("message 为空")
	}
}

// TestModelsThrottledKeyStillGetsCatalog 钉住一处**有意的行为偏差**。
//
// 数据面其它端点对 `throttled` 的 key 返 429（handler.go:2376），而 /v1/models
// 只做认证、不看状态——被标记为异常用量的 key 仍然能拉到模型目录。
//
// 这是刻意的：模型目录是只读发现端点，限流管的是**生成容量**；在这里 429 会让
// 客户端在限流尖峰期连「有哪些模型可用」都探不到，反而退化得更厉害。
//
// 钉住它是为了防止以后有人看到「与 chat 面不一致」就顺手改成 429——那是一次
// 行为变更，需要单独评估客户端兼容性，不该混在鉴权修复里。
func TestModelsThrottledKeyStillGetsCatalog(t *testing.T) {
	stub := &modelsAuthStub{enabled: true, valid: map[string]bool{"sk-throttled-key": true}, status: "throttled"}
	rec := probeModelsAuth(t, stub, "sk-throttled-key")
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("throttled key 被当成未鉴权拒了（HTTP %d）: %s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("throttled key 拿到 429 —— 这是刻意保留的偏差，见本用例注释；要改需单独评估兼容性")
	}
	if stub.calls != 1 {
		t.Errorf("应当恰好校验一次，实际 %d 次", stub.calls)
	}
}
