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
func TestAuthMiddleware_QueryKeyOnlyCountsOnGeminiNativePaths(t *testing.T) {
	cases := []string{
		"/v1/chat/completions?key=secret-key",
		"/v1/messages?key=secret-key",
		"/v1/models?key=secret-key",
		"/v1beta/models?key=secret-key",
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
