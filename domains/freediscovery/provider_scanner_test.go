package freediscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testTemplate() *ProviderTemplate {
	return &ProviderTemplate{
		ID: 1, ProviderCode: "groq", DisplayName: "Groq",
		BaseURL: "https://api.groq.test/openai/v1",
		APIType: APITypeOpenAICompletions, ModelsEndpoint: "/models",
	}
}

func TestHTTPScanner_ScanModels_OpenAIEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("missing bearer auth: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "llama-3.1-8b-instant", "display_name": "Llama 3.1 8B Instant", "context_window": 131072, "max_output_tokens": 8192},
				{"id": "compound-beta", "display_name": "Compound Beta", "context_window": 128000, "max_output_tokens": 8192}
			]
		}`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL + "/openai/v1"

	// 通过引擎的 scannerFor 获取 Groq 装配 (FreeOf=全部模型) — 真实路径
	engine := NewDiscoveryEngine(nil, nil)
	models, err := engine.scannerFor(tpl).ScanModels(context.Background(), tpl, "sk-test")
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 models, got %d", len(models))
	}
	if models[0].ModelID != "llama-3.1-8b-instant" || models[0].DisplayName != "Llama 3.1 8B Instant" {
		t.Fatalf("fields lost: %+v", models[0])
	}
	if models[0].ContextWindow != 131072 || models[0].MaxTokens != 8192 {
		t.Fatalf("window/tokens lost: %+v", models[0])
	}
	if models[0].RawMetadata == nil {
		t.Fatal("raw metadata must be preserved")
	}
	// Groq 预设钩子: 全部模型免费层可用 + 每日配额估算
	if models[0].DailyTokens != 14400 {
		t.Fatalf("groq quota hook missing: %+v", models[0])
	}
}

func TestHTTPScanner_ScanModels_OpenRouterFreeOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": [
			{"id": "google/gemma-4-31b-it:free", "name": "Gemma 4 31B (free)", "context_length": 131072,
			 "pricing_prompt": "0", "pricing_completion": "0"},
			{"id": "openai/gpt-5", "name": "GPT-5", "context_length": 1048576,
			 "pricing_prompt": "0.001", "pricing_completion": "0.002"},
			{"id": "meta-llama/broken", "name": "malformed-no-pricing", "context_length": 4096}
		]}`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.ProviderCode = "openrouter"
	tpl.BaseURL = srv.URL

	s := NewHTTPScanner(nil)
	models, err := s.ScanModels(context.Background(), tpl, "sk-or")
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	// 只保留 :free 后缀 (零定价判定的另一分支: pricing 均 0 也算, 但 gpt-5 有非零定价;
	// meta-llama/broken 无定价字段 → pricing 为空 → 零定价判定 true 但 :free 缺失;
	// 默认规则: HasSuffix(":free") OR (pricing_prompt != "" AND 零定价) → broken 被排除)
	if len(models) != 1 || models[0].ModelID != "google/gemma-4-31b-it:free" {
		t.Fatalf("want only the :free model, got %+v", models)
	}
}

func TestHTTPScanner_ScanModels_KeylessOmitsAuthHeader(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data": []}`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL

	s := NewHTTPScanner(nil)
	if _, err := s.ScanModels(context.Background(), tpl, ""); err != nil {
		t.Fatalf("keyless scan: %v", err)
	}
	if sawAuth != "" {
		t.Fatalf("keyless template must not send auth header, got %q", sawAuth)
	}
}

func TestHTTPScanner_ScanModels_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": "invalid api key"}`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL

	s := NewHTTPScanner(nil)
	_, err := s.ScanModels(context.Background(), tpl, "bad-key")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("upstream 401 must fail loudly, got %v", err)
	}
}

func TestHTTPScanner_ScanModels_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL

	s := NewHTTPScanner(nil)
	_, err := s.ScanModels(context.Background(), tpl, "k")
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("malformed response must fail, got %v", err)
	}
}

func TestHTTPScanner_ScanModels_BareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id": "m-1:free", "pricing_prompt": "0", "pricing_completion": "0"}]`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL

	s := NewHTTPScanner(nil)
	models, err := s.ScanModels(context.Background(), tpl, "k")
	if err != nil {
		t.Fatalf("bare array must parse: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("want 1, got %d", len(models))
	}
}

func TestHTTPScanner_ScanModels_ContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	s := NewHTTPScanner(nil)
	if _, err := s.ScanModels(ctx, tpl, "k"); err == nil {
		t.Fatal("deadline must produce error")
	}
}

func TestHTTPScanner_CustomHooks(t *testing.T) {
	s := NewHTTPScanner(nil)
	s.poolKeyOf = func(m modelEntry) string { return "custom-pool" }
	s.quotaEstimator = func(m modelEntry) (int64, int64) { return 100, 50 }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": [{"id": "x:free", "pricing_prompt": "0", "pricing_completion": "0"}]}`))
	}))
	defer srv.Close()

	tpl := testTemplate()
	tpl.BaseURL = srv.URL

	models, err := s.ScanModels(context.Background(), tpl, "k")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if models[0].PoolKey != "custom-pool" || models[0].MonthlyTokens != 100 || models[0].DailyTokens != 50 {
		t.Fatalf("hooks not applied: %+v", models[0])
	}
}

func TestJoinURL(t *testing.T) {
	cases := []struct{ base, ep, want string }{
		{"https://a.com/v1", "/models", "https://a.com/v1/models"},
		{"https://a.com/v1/", "models", "https://a.com/v1/models"},
		{"https://a.com", "/models", "https://a.com/models"},
	}
	for _, c := range cases {
		got, err := joinURL(c.base, c.ep)
		if err != nil || got != c.want {
			t.Errorf("joinURL(%q,%q) = %q, %v; want %q", c.base, c.ep, got, err, c.want)
		}
	}
}

func TestParseModelsResponse_EmptyData(t *testing.T) {
	entries, err := parseModelsResponse([]byte(`{"data": []}`))
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty data: %v, %d", err, len(entries))
	}
}
