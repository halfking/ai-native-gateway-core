package freediscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/safehttpclient"
)

// anthropicScannerForTest returns a scanner whose allowlist contains the httptest server host.
func anthropicScannerForTest(t *testing.T, baseURL string) *AnthropicScanner {
	t.Helper()
	safe := safehttpclient.NewWithAllowlist(30*time.Second, []string{"127.0.0.1"})
	return &AnthropicScanner{doer: safe.Do}
}

func TestAnthropicScanner_ScanModels_HappyPath(t *testing.T) {
	var sawAPIKey, sawVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("limit") == "" {
			t.Errorf("missing limit param")
		}
		sawAPIKey = r.Header.Get("x-api-key")
		sawVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-3-5-sonnet-20241022","type":"model","display_name":"Claude 3.5 Sonnet (New)","created_at":"2024-10-22T00:00:00Z"},
			{"id":"claude-3-opus-20240229","type":"model","display_name":"Claude 3 Opus","created_at":"2024-02-29T00:00:00Z"}
		],"has_more":false,"first_id":"claude-3-5-sonnet-20241022","last_id":"claude-3-opus-20240229"}`))
	}))
	defer srv.Close()

	s := anthropicScannerForTest(t, srv.URL)
	tpl := &ProviderTemplate{
		ID: 3, ProviderCode: "anthropic", DisplayName: "Anthropic",
		BaseURL: srv.URL, APIType: APITypeAnthropic,
	}
	models, err := s.ScanModels(context.Background(), tpl, "sk-ant-test")
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	if sawAPIKey != "sk-ant-test" {
		t.Fatalf("x-api-key header missing, got %q", sawAPIKey)
	}
	if sawVersion != "2023-06-01" {
		t.Fatalf("anthropic-version header missing, got %q", sawVersion)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 models, got %d", len(models))
	}
	if models[0].ModelID != "claude-3-5-sonnet-20241022" {
		t.Fatalf("model id: %q", models[0].ModelID)
	}
	if models[0].DisplayName != "Claude 3.5 Sonnet (New)" {
		t.Fatalf("display name: %q", models[0].DisplayName)
	}
	if models[0].RawMetadata == nil {
		t.Fatal("raw metadata must be preserved")
	}
}

func TestAnthropicScanner_ScanModels_Pagination(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		switch pages {
		case 1:
			_, _ = w.Write([]byte(`{"data":[{"id":"m1","type":"model","display_name":"M1"}],"has_more":true,"last_id":"m1"}`))
		case 2:
			if r.URL.Query().Get("after_id") != "m1" {
				t.Errorf("second page must pass after_id=m1, got %q", r.URL.Query().Get("after_id"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"m2","type":"model","display_name":"M2"}],"has_more":false,"last_id":"m2"}`))
		default:
			t.Errorf("unexpected extra page %d", pages)
		}
	}))
	defer srv.Close()

	s := anthropicScannerForTest(t, srv.URL)
	tpl := &ProviderTemplate{ID: 3, ProviderCode: "anthropic", BaseURL: srv.URL, APIType: APITypeAnthropic}
	models, err := s.ScanModels(context.Background(), tpl, "sk-ant-test")
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	if len(models) != 2 || models[0].ModelID != "m1" || models[1].ModelID != "m2" {
		t.Fatalf("paginated models wrong: %+v", models)
	}
}

func TestAnthropicScanner_ScanModels_RequiresAPIKey(t *testing.T) {
	s := &AnthropicScanner{}
	tpl := &ProviderTemplate{ID: 3, ProviderCode: "anthropic", BaseURL: "https://api.anthropic.com", APIType: APITypeAnthropic}
	_, err := s.ScanModels(context.Background(), tpl, "")
	if err == nil || !strings.Contains(err.Error(), "api key required") {
		t.Fatalf("missing api key must fail loudly, got %v", err)
	}
}

func TestAnthropicScanner_ScanModels_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	s := anthropicScannerForTest(t, srv.URL)
	tpl := &ProviderTemplate{ID: 3, ProviderCode: "anthropic", BaseURL: srv.URL, APIType: APITypeAnthropic}
	_, err := s.ScanModels(context.Background(), tpl, "bad-key")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("upstream 401 must fail loudly, got %v", err)
	}
}
