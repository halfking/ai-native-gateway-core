package memora

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 字节，满足 memora 的 sharedSecret 长度要求

// decodeJWTPart 解出 JWT 的 header/payload 段（不做签名校验，仅用于断言结构）。
func decodeJWTPart(t *testing.T, token string, idx int) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token should have 3 segments, got %d: %q", len(parts), token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[idx])
	if err != nil {
		t.Fatalf("decode jwt segment %d: %v", idx, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal jwt segment %d: %v", idx, err)
	}
	return out
}

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:   baseURL,
		JWTSecret: testSecret,
		ProjectID: "prompt-optimizer",
		TenantID:  "default",
		ActorID:   "local-ops",
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"missing base url", Config{JWTSecret: testSecret, ProjectID: "p"}, "base url"},
		{"missing secret", Config{BaseURL: "http://x", ProjectID: "p"}, "jwt secret"},
		{"missing project", Config{BaseURL: "http://x", JWTSecret: testSecret}, "project id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x/", JWTSecret: testSecret, ProjectID: "p"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.issuer != DefaultIssuer {
		t.Errorf("issuer = %q, want %q", c.issuer, DefaultIssuer)
	}
	if c.audience != DefaultAudience {
		t.Errorf("audience = %q, want %q", c.audience, DefaultAudience)
	}
	if c.tenantID != DefaultTenantID {
		t.Errorf("tenant = %q, want %q", c.tenantID, DefaultTenantID)
	}
	if c.baseURL != "http://x" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
	}
}

func TestSearchContract(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotProj   string
		gotBody   map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotProj = r.Header.Get("X-Project-ID")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"degraded":false,"items":[
			{"source":"memory","memory_id":"m-1","scope":"user","score":0.75,
			 "snippet":"提示词优化要保留 role","metadata":{"tags":["t1"],"source_type":"note"}},
			{"source":"memory","memory_id":"m-2","scope":"project","score":0.25,
			 "snippet":"   ","metadata":{"tags":[]}}
		]},"request_id":"req-1"}`))
	}))
	defer srv.Close()

	c, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := c.Search(context.Background(), "local-ops", "提示词优化", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v2/memories/search" {
		t.Errorf("path = %q, want /api/v2/memories/search", gotPath)
	}
	if gotProj != "prompt-optimizer" {
		t.Errorf("X-Project-ID = %q", gotProj)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("Authorization header missing bearer: %q", gotAuth)
	}
	if gotBody["query"] != "提示词优化" {
		t.Errorf("body.query = %v", gotBody["query"])
	}
	if gotBody["top_k"] != float64(3) {
		t.Errorf("body.top_k = %v, want 3", gotBody["top_k"])
	}
	scope, _ := gotBody["scope_chain"].(map[string]any)
	if scope == nil {
		t.Fatalf("body.scope_chain missing")
	}
	if scope["tenant_id"] != "default" || scope["project_id"] != "prompt-optimizer" {
		t.Errorf("scope_chain = %v", scope)
	}

	// 空 snippet 的条目必须被丢弃（否则会把噪声当历史喂给优化器）。
	if len(got) != 1 {
		t.Fatalf("expected 1 memory (blank snippet dropped), got %d: %+v", len(got), got)
	}
	if got[0].ID != "m-1" || got[0].Text != "提示词优化要保留 role" || got[0].Score != 0.75 {
		t.Errorf("mapped memory = %+v", got[0])
	}
	if got[0].CubeID != "user" {
		t.Errorf("CubeID = %q, want user", got[0].CubeID)
	}
}

func TestServiceTokenClaims(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"items":[]}}`))
	}))
	defer srv.Close()

	c, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Search(context.Background(), "u", "q", 1); err != nil {
		t.Fatalf("Search: %v", err)
	}

	token := strings.TrimPrefix(auth, "Bearer ")
	header := decodeJWTPart(t, token, 0)
	if header["alg"] != "HS256" {
		t.Errorf("alg = %v, want HS256", header["alg"])
	}
	payload := decodeJWTPart(t, token, 1)
	for key, want := range map[string]any{
		"iss":        DefaultIssuer,
		"aud":        DefaultAudience,
		"tenant_id":  "default",
		"actor_id":   "local-ops",
		"actor_type": "service",
		"sub":        "service:local-auth:local-ops",
	} {
		if payload[key] != want {
			t.Errorf("claim %s = %v, want %v", key, payload[key], want)
		}
	}
	scope, _ := payload["scope"].(string)
	if !strings.Contains(scope, "memory.read") || !strings.Contains(scope, "memory.write") {
		t.Errorf("scope = %q, want memory.read and memory.write", scope)
	}
	if exp, ok := payload["exp"].(float64); !ok || exp <= float64(time.Now().Unix()) {
		t.Errorf("exp = %v, want a future timestamp", payload["exp"])
	}
}

func TestIngestContract(t *testing.T) {
	var (
		gotPath string
		gotIdem string
		gotCorr string
		gotProj string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotIdem = r.Header.Get("Idempotency-Key")
		gotCorr = r.Header.Get("X-Correlation-ID")
		gotProj = r.Header.Get("X-Project-ID")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"data":{"accepted":1,"degraded":0,"duplicates":0},"request_id":"r1","correlation_id":"c1"}`))
	}))
	defer srv.Close()

	c, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := c.Ingest(context.Background(), "gateway", []IngestItem{{
		SourceType:     "session_summary",
		SourceID:       "src-1",
		ContentRef:     "prompts/x",
		ContentPreview: "内容预览",
		Tags:           []string{"a"},
		RunID:          "run-1",
	}})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if gotPath != "/api/v2/memories/ingest" {
		t.Errorf("path = %q", gotPath)
	}
	if gotIdem == "" {
		t.Error("Idempotency-Key header missing (memora returns 422 without it)")
	}
	if gotCorr == "" {
		t.Error("X-Correlation-ID header missing (memora returns 422 without it)")
	}
	if gotProj != "prompt-optimizer" {
		t.Errorf("X-Project-ID = %q", gotProj)
	}
	if gotBody["source_system"] != "gateway" {
		t.Errorf("source_system = %v", gotBody["source_system"])
	}
	items, _ := gotBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d", len(items))
	}
	it, _ := items[0].(map[string]any)
	if it["source_type"] != "session_summary" || it["source_id"] != "src-1" {
		t.Errorf("item = %v", it)
	}
	if it["content_preview"] != "内容预览" {
		t.Errorf("content_preview = %v", it["content_preview"])
	}
	scope, _ := it["scope_chain"].(map[string]any)
	if scope["project_id"] != "prompt-optimizer" || scope["run_id"] != "run-1" {
		t.Errorf("item scope_chain = %v", scope)
	}
	if res.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", res.Accepted)
	}
	if res.CorrelationID != "c1" {
		t.Errorf("correlation_id = %q", res.CorrelationID)
	}
}

func TestPostSessionAnalysisIdempotencyKeySuffix(t *testing.T) {
	var gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIdem = r.Header.Get("Idempotency-Key")
		_, _ = w.Write([]byte(`{"data":{"fact_memory_id":"f1","experience_memory_id":"e1","manifest_id":"m1","manifest_version":1,"idempotency_key":"k"},"request_id":"r","correlation_id":"c"}`))
	}))
	defer srv.Close()

	c, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := c.PostSessionAnalysis(context.Background(), SessionAnalysisPayload{
		AnalysisKind: "session_summary",
		DispatchID:   "dispatch-9",
		Summary:      "会话摘要",
	})
	if err != nil {
		t.Fatalf("PostSessionAnalysis: %v", err)
	}
	// memora 要求幂等键形如 <dispatch_id>-<analysis_kind>，否则 422。
	if gotIdem != "dispatch-9-session_summary" {
		t.Errorf("Idempotency-Key = %q, want dispatch-9-session_summary", gotIdem)
	}
	if res.FactMemoryID != "f1" || res.ManifestID != "m1" {
		t.Errorf("result = %+v", res)
	}
}

func TestPostSessionAnalysisRequiresKind(t *testing.T) {
	c, err := New(testConfig("http://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.PostSessionAnalysis(context.Background(), SessionAnalysisPayload{}); err == nil {
		t.Fatal("expected error for empty analysis kind")
	}
}

func TestAPIErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"project_forbidden","error":"project access denied","reason":"project_not_found"}`))
	}))
	defer srv.Close()

	c, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Search(context.Background(), "u", "q", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v should unwrap to *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "project_not_found") {
		t.Errorf("body should carry upstream reason, got %q", apiErr.Body)
	}
}

func TestEmptyQueryReturnsNil(t *testing.T) {
	c, err := New(testConfig("http://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.Search(context.Background(), "u", "   ", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty query, got %v", got)
	}
}
