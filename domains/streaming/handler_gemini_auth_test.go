package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/auth"
)

// TestNewGeminiSyntheticRequest_NormalizesGeminiCredential pins the
// ingress-boundary normalization.
//
// The Gemini-native ingress (docs/vendor-formats/gemini.md §HTTP 形态:
// `POST /v1beta/models/{MODEL}:generateContent?key={API_KEY}`, and
// docs/QUICK_REFERENCE.md §Gemini协议: `x-goog-api-key: xxx`) carries the
// client credential in a vendor-specific shape. The Gemini handler then
// converts the request into a SYNTHETIC /v1/chat/completions request and
// hands it to ChatHandler, which is OpenAI-shaped end to end.
//
// Before this round the synthetic request carried the Google header through
// (headers are cloned) but DROPPED the query string ⇒ the `?key=` form was
// invisible to the downstream key verifier, and the header form was
// invisible too because extractBearerToken only knew Bearer / x-api-key.
// Either way the request died with 401 missing_key.
//
// The fix normalizes the credential ONCE, at the boundary, into the
// canonical `Authorization: Bearer` shape so every downstream consumer
// (key verifier, static data-plane key, request-meta, sanitizer) sees it
// without knowing anything about Google's header names.
func TestNewGeminiSyntheticRequest_NormalizesGeminiCredential(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		setup func(r *http.Request)
		want  string
	}{
		{
			name: "x-goog-api-key header",
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-goog-api-key", "sk-gemini-key")
			},
			want: "Bearer sk-gemini-key",
		},
		{
			name:  "key query parameter",
			path:  "/v1beta/models/gemini-2.5-pro:generateContent?key=sk-gemini-key",
			setup: func(r *http.Request) {},
			want:  "Bearer sk-gemini-key",
		},
		{
			name:  "streamGenerateContent with query parameter",
			path:  "/v1/models/gemini-2.5-pro:streamGenerateContent?key=sk-gemini-key",
			setup: func(r *http.Request) {},
			want:  "Bearer sk-gemini-key",
		},
		{
			name: "explicit bearer wins",
			path: "/v1beta/models/gemini-2.5-pro:generateContent?key=sk-other",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer sk-primary")
				r.Header.Set("x-goog-api-key", "sk-other")
			},
			want: "Bearer sk-primary",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := httptest.NewRequest(http.MethodPost, tc.path, nil)
			tc.setup(orig)

			synth := newGeminiSyntheticRequest(orig, []byte(`{}`))
			if got := synth.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
			if got := extractBearerToken(synth); got != "" && got != "sk-primary" &&
				got != "sk-gemini-key" {
				t.Errorf("extractBearerToken(synth) = %q, want the normalized key", got)
			}
		})
	}
}

// TestExtractBearerToken_GeminiCredentialForms pins the shared extractor in
// the data-plane package: the Google header must be found there too, so a
// Gemini request that reaches the chat path directly (not through the
// Gemini handler) is authenticated the same way.
func TestExtractBearerToken_GeminiCredentialForms(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		setup func(r *http.Request)
		want  string
	}{
		{
			name: "x-goog-api-key header",
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-goog-api-key", "sk-gemini-key")
			},
			want: "sk-gemini-key",
		},
		{
			name:  "key query parameter on gemini path",
			path:  "/v1beta/models/gemini-2.5-pro:generateContent?key=sk-gemini-key",
			setup: func(r *http.Request) {},
			want:  "sk-gemini-key",
		},
		{
			name: "bearer still wins",
			path: "/v1beta/models/gemini-2.5-pro:generateContent?key=sk-other",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer sk-primary")
			},
			want: "sk-primary",
		},
		{
			name: "x-api-key still wins over google header",
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-api-key", "sk-anthropic")
				r.Header.Set("x-goog-api-key", "sk-google")
			},
			want: "sk-anthropic",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			tc.setup(r)
			if got := extractBearerToken(r); got != tc.want {
				t.Errorf("extractBearerToken = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewGeminiSyntheticRequest_LeavesAnthropicCredentialAlone is the
// reverse nail for the normalization scope.
//
// ★ The normalization must touch ONLY the two forms this round added
// (x-goog-api-key / ?key=). The Anthropic `x-api-key` form already reaches
// the downstream extractor (the synthetic request clones the original
// headers) ⇒ rewriting it into `Authorization` would change ANOTHER
// existing path's behaviour (with no verifier, handoff forwards the
// Authorization credential to the summary upstream — see
// trustedHandoffUpstreamAPIKey).
//
// ⇒ A behaviour change nobody asked for is a behaviour change; the round
// scope says "make the Gemini forms work", not "re-shape every form".
func TestNewGeminiSyntheticRequest_LeavesAnthropicCredentialAlone(t *testing.T) {
	orig := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)
	orig.Header.Set("x-api-key", "sk-ant-abc")

	synth := newGeminiSyntheticRequest(orig, []byte(`{}`))
	if got := synth.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (x-api-key must not be rewritten)", got)
	}
	if got := extractBearerToken(synth); got != "sk-ant-abc" {
		t.Errorf("extractBearerToken(synth) = %q, want the original x-api-key value", got)
	}
}

// TestGeminiNativePathMatchesHandlerParser is the drift gate between the
// two copies of "what is a Gemini-native path".
//
// internal/auth.IsGeminiNativePath decides whether the `?key=` credential
// form applies; domains/streaming/handler_gemini.go's extractGeminiPath
// decides whether the request is a Gemini-native route at all. They live in
// different packages (internal/auth must stay importable by middleware,
// which may not import domains/), so the two lists can drift.
//
// ⇒ The invariant is about the routes the handler is actually REACHED
//
//	with: the Gemini handler is mounted on two mux subtrees
//	("/v1beta/models/", "/v1/models/" — cmd/gateway/main.go), so only a
//	path under one of those prefixes can ever reach it.
//
//	- Every SERVED path must be covered by the credential scope (a narrower
//	  scope leaves a served Gemini route unable to authenticate — exactly
//	  the defect this round fixes).
//	- The scope must not cover a path the handler cannot parse (a wider
//	  scope would let `?key=` act as a credential on a route that is not a
//	  Gemini route).
//
// ★ A path the handler's parser accepts but that is NOT under a mounted
//
//	prefix (e.g. "/prefix/v1beta/models/x:generateContent") is never served
//	— the mux 404s it — so the scope is free to leave it out.
//
// (Same discipline as the repo's "两侧都有门盯着": asserting one direction
// is not enough — a scope that silently widens to every path would still
// pass a one-way assertion.)
func TestGeminiNativePathMatchesHandlerParser(t *testing.T) {
	// The two mux subtrees that carry the Gemini handler.
	// Pinned against cmd/gateway/main.go by
	// TestGeminiHandlerIsMountedOnBothVendorPrefixes (cmd/gateway).
	geminiMuxPrefixes := []string{"/v1beta/models/", "/v1/models/"}

	// The Gemini-native MODEL LIST is a served Gemini route that is NOT
	// under either mux subtree: it has its own mux entry
	// (cmd/gateway/main.go: mux.HandleFunc("/v1beta/models",
	// modelsHandler.ServeGeminiModels)), because it is a LIST, not a
	// generateContent action.
	//
	// The mount itself is pinned against main.go by
	// TestGeminiModelListIsMounted (cmd/gateway); the serving handler lives
	// in THIS package (domains/streaming/models.go) and is asserted below.
	//
	// It must be in the credential scope: a native Gemini client presents
	// its key as "?key=" (docs/vendor-formats/gemini.md), so without the
	// scope the list 401s while generateContent (same client, header form)
	// works.
	geminiNativeExactRoutes := map[string]bool{
		"/v1beta/models": true,
	}

	served := func(p string) bool {
		for _, prefix := range geminiMuxPrefixes {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}
		return false
	}

	paths := []string{
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"/v1beta/models/gemini-2.5-pro:streamGenerateContent",
		"/v1/models/gemini-2.5-pro:generateContent",
		"/v1/models/gemini-2.5-pro:streamGenerateContent",
		"/v1beta/models/gemini:generateContent",
		"/v1/models/gemini-pro:streamGenerateContent",
		// The handler's parser is a substring match ⇒ it also serves these
		// malformed-but-matched shapes. The credential scope must cover
		// them too, otherwise a served Gemini route cannot authenticate.
		"/v1beta/models/gemini-2.5-pro:generateContent/extra",
		"/v1/models/gemini:generateContentXYZ",
		// Under a mounted prefix but not a Gemini action — neither the
		// handler nor the scope may treat them as Gemini routes.
		"/v1beta/models/gemini-2.5-pro:countTokens",
		"/v1beta/models/gemini-2.5-pro",
		"/v1beta/models/a/b:generateContent",
		"/v1beta/models/",
		// Not under a mounted prefix — never served as a Gemini route.
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/models",
		"/prefix/v1beta/models/gemini:generateContent",
		"/healthz",
		// The Gemini-native MODEL LIST is served on its own mux entry
		// (not under the "/v1beta/models/" subtree), so the prefix check
		// above cannot see it -- it is listed as a served route below.
		"/v1beta/models",
		// A longer path that merely starts with "/v1beta/models" is a
		// DIFFERENT route, and nothing serves it.
		"/v1beta/modelsXYZ",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			_, _, handlerOK := extractGeminiPath(p)
			scopeOK := auth.IsGeminiNativePath(p)

			// A path is a SERVED Gemini route when it is under a mounted
			// mux prefix AND the handler's parser accepts it, or when it
			// is one of the exact Gemini-native routes mounted on their
			// own mux entries (the model list).
			// (A path under the prefix that the parser rejects is simply
			// not a Gemini route -- the handler answers 404 for it.)
			servedRoute := (served(p) && handlerOK) || geminiNativeExactRoutes[p]

			if servedRoute {
				if !scopeOK {
					t.Errorf("served Gemini route is not covered by the credential scope: %q", p)
				}
				return
			}
			// Not a served Gemini route -- the scope must not cover it.
			if scopeOK {
				t.Errorf("credential scope covers a path that is not a served Gemini route: %q", p)
			}
		})
	}
}

// TestGeminiNativeModelListIsServedAndScoped is the model-list counterpart
// of the drift gate above.
//
// The two sides of the model list:
//
//   - SERVED: domains/streaming/models.go ServeGeminiModels, mounted at
//     cmd/gateway/main.go as mux.HandleFunc("/v1beta/models", ...).
//     Pinned against main.go by TestGeminiModelListIsMounted (cmd/gateway).
//   - AUTHENTICATED: the credential scope (internal/auth.IsGeminiNativePath)
//     must cover it, otherwise a native client that presents "?key=" gets
//     401 on the list while generateContent works with the same key.
//
// This test asserts the half that lives in THIS package: the serving
// handler exists here, the credential scope covers the path, and the
// generateContent parser does NOT claim it (the two routes are distinct --
// a parser that matched the list would also make the scope wrong).
func TestGeminiNativeModelListIsServedAndScoped(t *testing.T) {
	const modelListPath = "/v1beta/models"

	// The serving handler lives in this package. (Compile-time: if it is
	// moved or renamed, this file stops compiling -- the mount in
	// cmd/gateway/main.go would then point at nothing.)
	var _ = (*ModelsHandler).ServeGeminiModels

	if !auth.IsGeminiNativePath(modelListPath) {
		t.Errorf("credential scope does not cover the Gemini-native model list %q", modelListPath)
	}

	// Reverse nail: the generateContent parser must NOT claim the list.
	// If it did, the two routes would share one parser and the credential
	// scope could not tell them apart.
	if _, _, ok := extractGeminiPath(modelListPath); ok {
		t.Errorf("the generateContent parser claims the Gemini-native model list %q", modelListPath)
	}

	// Reverse nail: the OpenAI-shaped list keeps its own contract and stays
	// OUT of the Gemini credential scope (a "?key=" on it must not become a
	// credential).
	if auth.IsGeminiNativePath("/v1/models") {
		t.Errorf("credential scope covers the OpenAI-shaped model list")
	}
}
