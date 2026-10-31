package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// geminiNativePaths are the Gemini-native ingress paths the gateway serves
// (domains/streaming/handler_gemini.go). The retained vendor standard docs
// (docs/vendor-formats/gemini.md §HTTP 形态, docs/archive/process/format-conversion/
// 厂家数据格式/gemini/generate-content.md §认证) define the credential forms
// a Gemini client may use:
//
//	POST /v1beta/models/{MODEL}:generateContent?key={API_KEY}
//	Header: x-goog-api-key: {API_KEY}
//
// Before this round the global static-key gate only knew
// `Authorization: Bearer` and `x-api-key` (Anthropic) ⇒ every Gemini-native
// client died here with 401 missing_key before the Gemini handler ever ran.
func TestAuthMiddleware_AcceptsGeminiNativeCredentialForms(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		setup func(r *http.Request)
	}{
		{
			name: "x-goog-api-key header",
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-goog-api-key", "secret-key")
			},
		},
		{
			name:  "key query parameter",
			path:  "/v1beta/models/gemini-2.5-pro:generateContent?key=secret-key",
			setup: func(r *http.Request) {},
		},
		{
			name:  "key query parameter on /v1 path",
			path:  "/v1/models/gemini-2.5-pro:streamGenerateContent?key=secret-key",
			setup: func(r *http.Request) {},
		},
		{
			name: "x-goog-api-key header on /v1 path",
			path: "/v1/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-goog-api-key", "secret-key")
			},
		},
		{
			// ★ 2026-10-28 补：Gemini 原生**模型列表**（GET /v1beta/models，
			//   它有自己的 mux 挂载，不在 "/v1beta/models/" 子树下）也是一个
			//   被服务的 Gemini 路由 ⇒ 同一个原生客户端的 ?key= 形态必须
			//   在这里也通 ⇒ 否则「同一个客户端、同一个 key，列表 401 而
			//   generateContent 通」＝ 认证看起来是坏的。
			//   判定源：internal/auth/clientkey.go 的 geminiNativePathRe 的
			//   精确锚定分支 |/v1beta/models$。
			name:  "key query parameter on the Gemini-native model list",
			path:  "/v1beta/models?key=secret-key",
			setup: func(r *http.Request) {},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			mw := NewAuthMiddleware("secret-key")
			handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.Header.Set("Content-Type", "application/json")
			tc.setup(req)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if !called {
				t.Fatalf("Gemini-native credential must pass the gate, got status=%d body=%s",
					rr.Code, rr.Body.String())
			}
			if rr.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rr.Code)
			}
		})
	}
}

// TestAuthMiddleware_QueryKeyOnlyCountsOnGeminiNativePaths is the reverse
// nail: `key` is a GENERIC query-parameter name. Accepting it as a
// credential source on every route would let it shadow a future
// non-credential `?key=` parameter, so the query-string credential form is
// scoped to the Gemini-native paths where the vendor contract puts it.
//
// (Same discipline as TestAuthMiddleware_RootAssetBypassDoesNotLeakNeighbors:
// asserting "the accepted one is in" is not enough — the "not accepted" side
// must be nailed too, or deleting the rule from the other end keeps the
// judgement green.)
//
// ★ 2026-10-28（12h 审计轮）订正：本表原先**含** "/v1beta/models?key="，
//
//	断言它「必须不被接受」⇒ 该断言在 659ad160b（2026-10-27 02:48「Gemini
//	剩余登记项处置轮」）之后**一直是红的**。根因不是生产代码错，而是
//	**这张表被上一轮漏改**：659ad160b 有意把 `/v1beta/models$`（Gemini 原生
//	模型列表，它有自己的 mux 挂载 cmd/gateway/main.go 的
//	`mux.HandleFunc("/v1beta/models", modelsHandler.ServeGeminiModels)`）
//	加进凭据收口面（internal/auth/clientkey.go 的 geminiNativePathRe），
//	**同型断言在 internal/auth/clientkey_test.go 里已被同步改正**
//	（"/v1beta/models?key=" 从「不覆盖」挪到「覆盖」，并新增
//	"/v1beta/modelsXYZ?key=" 作为「更长但不同路由」的负例），
//	⇒ **唯独漏了这张 middleware 表**。
//	⇒ 同一个契约在仓内有**两份**断言，其中一份随实现改动被改正、另一份没有
//	  ⇒ 这正是本仓反复出现的那一类（「同一句话不许两份」）。
//	⇒ 本轮把本表对齐到已生效的设计（模型列表在收口面内），
//	  并**保留负例强度**：把 "/v1beta/modelsXYZ?key="（精确锚定 ⇒ 不在收口面内）
//	  与 "/v1/models?key="（OpenAI 形状的列表 ⇒ 不在收口面内）留在负例侧，
//	  使「收窄」这一变异臂仍然会被抓住。
func TestAuthMiddleware_QueryKeyOnlyCountsOnGeminiNativePaths(t *testing.T) {
	cases := []string{
		"/v1/chat/completions?key=secret-key",
		"/v1/messages?key=secret-key",
		// "/v1/models" is the OpenAI-SHAPED model list (no trailing slash) ⇒
		// deliberately NOT Gemini-native ⇒ its ?key= is not a credential.
		"/v1/models?key=secret-key",
		// The Gemini-native model list alternative is EXACTLY anchored
		// ("/v1beta/models$") ⇒ a longer path that merely starts with
		// "/v1beta/models" is a DIFFERENT route ⇒ must stay out of scope.
		"/v1beta/modelsXYZ?key=secret-key",
	}

	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			called := false
			mw := NewAuthMiddleware("secret-key")
			handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if called {
				t.Fatalf("?key= must NOT be a credential source off the Gemini-native paths")
			}
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestAuthMiddleware_BearerStillWinsOverGeminiForms pins the precedence:
// an explicit Authorization: Bearer must keep winning over the Google
// header / query forms (a client sending both must not be re-pointed at the
// other credential).
func TestAuthMiddleware_BearerStillWinsOverGeminiForms(t *testing.T) {
	mw := NewAuthMiddleware("secret-key")
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent?key=wrong-key", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	req.Header.Set("x-goog-api-key", "wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}
