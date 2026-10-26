package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExtractClientKey covers every presentation form the gateway accepts.
//
// The forms and their vendor sources:
//
//	Authorization: Bearer  → generic
//	x-api-key             → Anthropic (docs/vendor-formats/anthropic.md)
//	x-goog-api-key        → Google / Gemini (docs/vendor-formats/gemini.md,
//	                        docs/供应商协议优化.md §8.3, docs/QUICK_REFERENCE.md)
//	?key=                  → Gemini query string (docs/vendor-formats/gemini.md
//	                        §HTTP 形态, 厂家数据格式/gemini/generate-content.md §认证)
func TestExtractClientKey(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		setup      func(r *http.Request)
		wantKey    string
		wantSource CredentialSource
	}{
		{
			name:       "no credential at all",
			path:       "/v1/chat/completions",
			setup:      func(r *http.Request) {},
			wantKey:    "",
			wantSource: SourceNone,
		},
		{
			name: "bearer",
			path: "/v1/chat/completions",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer sk-abc")
			},
			wantKey:    "sk-abc",
			wantSource: SourceBearer,
		},
		{
			name: "lowercase bearer scheme",
			path: "/v1/chat/completions",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "bearer sk-abc")
			},
			wantKey:    "sk-abc",
			wantSource: SourceBearer,
		},
		{
			name: "anthropic x-api-key",
			path: "/v1/messages",
			setup: func(r *http.Request) {
				r.Header.Set("x-api-key", "sk-ant-abc")
			},
			wantKey:    "sk-ant-abc",
			wantSource: SourceAPIKey,
		},
		{
			name: "google x-goog-api-key",
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-goog-api-key", "AIza-google")
			},
			wantKey:    "AIza-google",
			wantSource: SourceGoogAPIKey,
		},
		{
			name: "gemini key query on gemini path",
			path: "/v1beta/models/gemini-2.5-pro:generateContent?key=AIza-query",
			setup: func(r *http.Request) {
			},
			wantKey:    "AIza-query",
			wantSource: SourceQueryKey,
		},
		{
			name: "bearer wins over every other form",
			path: "/v1beta/models/gemini-2.5-pro:generateContent?key=AIza-query",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer sk-primary")
				r.Header.Set("x-api-key", "sk-ant")
				r.Header.Set("x-goog-api-key", "AIza-google")
			},
			wantKey:    "sk-primary",
			wantSource: SourceBearer,
		},
		{
			name: "x-api-key wins over x-goog-api-key",
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			setup: func(r *http.Request) {
				r.Header.Set("x-api-key", "sk-ant")
				r.Header.Set("x-goog-api-key", "AIza-google")
			},
			wantKey:    "sk-ant",
			wantSource: SourceAPIKey,
		},
		{
			name: "x-goog-api-key wins over the query form",
			path: "/v1beta/models/gemini-2.5-pro:generateContent?key=AIza-query",
			setup: func(r *http.Request) {
				r.Header.Set("x-goog-api-key", "AIza-google")
			},
			wantKey:    "AIza-google",
			wantSource: SourceGoogAPIKey,
		},
		{
			name: "non-bearer authorization is not a credential",
			path: "/v1/chat/completions",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
			},
			wantKey:    "",
			wantSource: SourceNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			tc.setup(r)
			key, source := ExtractClientKey(r)
			if key != tc.wantKey {
				t.Errorf("key = %q, want %q", key, tc.wantKey)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

// TestExtractClientKey_QueryKeyIsScopedToGeminiNativePaths is the reverse
// nail for the generic parameter name: `key` must NOT become a credential
// source on routes where the vendor contract does not define it as one.
//
// ★ The scope covers exactly the routes the Gemini handler serves — which
// includes the malformed-but-served shapes (trailing suffix), because the
// handler's own parser is a substring match. A NARROWER scope would leave
// a served Gemini route unable to authenticate (exactly the defect this
// round fixes).
func TestExtractClientKey_QueryKeyIsScopedToGeminiNativePaths(t *testing.T) {
	notCovered := []string{
		"/v1/chat/completions?key=AIza-query",
		"/v1/messages?key=AIza-query",
		"/v1/models?key=AIza-query",
		"/v1beta/modelsXYZ?key=AIza-query",
		"/v1beta/models/gemini-2.5-pro?key=AIza-query",
		"/v1beta/models/gemini-2.5-pro:countTokens?key=AIza-query",
		"/v1beta/models/a/b:generateContent?key=AIza-query",
		"/prefix/v1beta/models/gemini:generateContent?key=AIza-query",
	}
	for _, p := range notCovered {
		t.Run(p, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, p, nil)
			key, source := ExtractClientKey(r)
			if key != "" || source != SourceNone {
				t.Errorf("?key= must not be a credential off the Gemini-native paths: key=%q source=%q", key, source)
			}
		})
	}

	// Served by the Gemini handler ⇒ the credential form must apply.
	covered := []string{
		"/v1beta/models/gemini-2.5-pro:generateContent?key=AIza-query",
		"/v1beta/models/gemini-2.5-pro:streamGenerateContent?key=AIza-query",
		"/v1/models/gemini-2.5-pro:generateContent?key=AIza-query",
		"/v1beta/models/gemini-2.5-pro:generateContent/extra?key=AIza-query",
		// The Gemini-native model list (its own mux entry) is a served
		// Gemini route => the credential form must apply there too.
		"/v1beta/models?key=AIza-query",
	}
	for _, p := range covered {
		t.Run(p, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, p, nil)
			key, source := ExtractClientKey(r)
			if key != "AIza-query" || source != SourceQueryKey {
				t.Errorf("?key= must be a credential on a served Gemini route: key=%q source=%q", key, source)
			}
		})
	}
}

// TestIsGeminiNativePath pins the credential-scoping predicate.
func TestIsGeminiNativePath(t *testing.T) {
	cases := map[string]bool{
		"/v1beta/models/gemini-2.5-pro:generateContent":       true,
		"/v1beta/models/gemini-2.5-pro:streamGenerateContent": true,
		"/v1/models/gemini-2.5-pro:generateContent":           true,
		"/v1/models/gemini-2.5-pro:streamGenerateContent":     true,
		"/v1beta/models/gemini:generateContent":               true,
		// Served by the Gemini handler (substring parser) ⇒ covered.
		"/v1beta/models/gemini-2.5-pro:generateContent/extra": true,
		"/v1/models/gemini:generateContentXYZ":                true,
		// The Gemini-NATIVE model list (domains/streaming/models.go:
		// ServeGeminiModels, mounted at cmd/gateway/main.go as its own
		// mux entry) is covered by the credential scope.
		//
		// Why: a native Gemini client (google-genai SDK) presents its
		// credential as "?key=" (docs/vendor-formats/gemini.md). Without
		// the scope the list 401s while generateContent (same client,
		// header form) works -- the same "half-fixed" shape as the 433
		// defect, one endpoint over.
		"/v1beta/models": true,
		// Not Gemini-native routes ⇒ not covered.
		"/v1/models":           false,
		"/v1/chat/completions": false,
		"/v1/messages":         false,
		"/v1beta/models/gemini-2.5-pro:countTokens":    false,
		"/prefix/v1beta/models/gemini:generateContent": false,
		"/v1beta/models/a/b:generateContent":           false,
		// The model-list alternative is EXACTLY anchored: a longer path
		// that merely starts with "/v1beta/models" is a DIFFERENT route
		// (the Gemini handler does not serve it), so it must not be in
		// scope.
		"/v1beta/modelsXYZ": false,
	}
	for p, want := range cases {
		if got := IsGeminiNativePath(p); got != want {
			t.Errorf("IsGeminiNativePath(%q) = %v, want %v", p, got, want)
		}
	}
}

// TestRedactQueryCredentials pins the log hygiene: a credential-bearing
// query parameter must never reach the access log in cleartext, while every
// other parameter must stay byte-identical.
func TestRedactQueryCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "no credential parameter",
			in:   "model=gpt-4o&stream=true",
			want: "model=gpt-4o&stream=true",
		},
		{
			name: "gemini key parameter",
			in:   "key=AIzaSy-secret-value&alt=sse",
			want: "key=REDACTED&alt=sse",
		},
		{
			name: "admin token parameter",
			in:   "token=eyJhbGciOi&limit=10",
			want: "token=REDACTED&limit=10",
		},
		{
			name: "identifier parameters are NOT credentials",
			in:   "api_key_id=42&credential_id=7",
			want: "api_key_id=42&credential_id=7",
		},
		{
			name: "a parameter merely containing key is not the key parameter",
			in:   "apikey=AIza-secret&monkey=1",
			want: "apikey=AIza-secret&monkey=1",
		},
		{
			name: "url-encoded parameter name",
			in:   "%6bey=AIza-secret",
			want: "%6bey=REDACTED",
		},
		{
			name: "credential without value",
			in:   "key=",
			want: "key=",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactQueryCredentials(tc.in); got != tc.want {
				t.Errorf("RedactQueryCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactQueryCredentials_NeverLeaksTheSecret is the property the whole
// helper exists for: the secret value must not survive anywhere in the
// output.
func TestRedactQueryCredentials_NeverLeaksTheSecret(t *testing.T) {
	const secret = "AIzaSySUPERSECRET-VALUE-1234567890"
	raw := "key=" + secret + "&model=gemini-2.5-pro"
	got := RedactQueryCredentials(raw)
	if strings.Contains(got, secret) {
		t.Fatalf("redacted query still carries the secret: %q", got)
	}
	if !strings.Contains(got, "model=gemini-2.5-pro") {
		t.Fatalf("redaction must preserve the other parameters: %q", got)
	}
}
