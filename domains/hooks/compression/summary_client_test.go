package compression

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	summarymodel "github.com/kaixuan/llm-gateway-go/domains/hooks/compression/summary"
)

type fakeSummaryProvider struct {
	candidates []ProviderCandidate
	models     []string
	profiles   []string
}

func (f *fakeSummaryProvider) Enabled() bool { return true }

func (f *fakeSummaryProvider) GetCandidates(ctx context.Context, model, profile string) ([]ProviderCandidate, error) {
	f.models = append(f.models, model)
	f.profiles = append(f.profiles, profile)
	if len(f.candidates) > 0 {
		return f.candidates, nil
	}
	return []ProviderCandidate{{RawModel: model, BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", Available: true}}, nil
}

func TestSummaryClientAdapterRequiresModel(t *testing.T) {
	provider := &fakeSummaryProvider{}
	client := newSummaryClientAdapter(&Dependencies{Provider: provider}, "", "tenant-a")

	got, err := client.Complete(context.Background(), "prompt")
	if err == nil || got != "" {
		t.Fatalf("missing model should fail, got (%q, %v)", got, err)
	}
}

func TestSummaryClientAdapterOpenAICandidateFallback(t *testing.T) {
	oldDoer := defaultHTTPDoer
	t.Cleanup(func() { defaultHTTPDoer = oldDoer })
	calls := 0
	defaultHTTPDoer = func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(`{"error":"first failed"}`)), Header: make(http.Header)}, nil
		}
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": " ok "}}}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	}

	provider := &fakeSummaryProvider{candidates: []ProviderCandidate{
		{RawModel: "raw-a", BaseURL: "https://example.invalid", APIKey: "key-a", Protocol: "openai-completions", Available: true},
		{RawModel: "raw-b", BaseURL: "https://example.invalid", APIKey: "key-b", Protocol: "openai-completions", Available: true},
	}}
	client := newSummaryClientAdapter(&Dependencies{Provider: provider}, "", "tenant-a")
	got, err := client.Complete(context.Background(), "prompt", summarymodel.WithModel("model-a"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "ok" {
		t.Fatalf("summary = %q, want ok", got)
	}
	if calls != 2 {
		t.Fatalf("http calls = %d, want 2", calls)
	}
	if len(provider.models) != 1 || provider.models[0] != "model-a" {
		t.Fatalf("provider models = %v, want [model-a]", provider.models)
	}
}

func TestSummaryClientAdapterAnthropicCompletion(t *testing.T) {
	oldDoer := defaultHTTPDoer
	t.Cleanup(func() { defaultHTTPDoer = oldDoer })
	defaultHTTPDoer = func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/v1/messages") {
			t.Fatalf("path = %q, want /v1/messages", req.URL.Path)
		}
		if got := req.Header.Get("x-api-key"); got != "anthropic-key" {
			t.Fatalf("x-api-key = %q", got)
		}
		if got := req.Header.Get("anthropic-version"); got == "" {
			t.Fatalf("anthropic-version header missing")
		}
		var payload struct {
			Model     string `json:"model"`
			System    string `json:"system"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Model != "claude-raw" || payload.System != "system prompt" || payload.MaxTokens != 777 {
			t.Fatalf("payload = %+v", payload)
		}
		body := `{"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}

	provider := &fakeSummaryProvider{candidates: []ProviderCandidate{
		{RawModel: "claude-raw", BaseURL: "https://anthropic.invalid", APIKey: "anthropic-key", Protocol: "anthropic-messages", Available: true},
	}}
	client := newSummaryClientAdapter(&Dependencies{Provider: provider}, "profile-a", "tenant-a")
	got, err := client.Complete(context.Background(), "prompt", summarymodel.WithModel("claude-alias"), summarymodel.WithSystemPrompt("system prompt"), summarymodel.WithMaxTokens(777))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("summary = %q", got)
	}
	if len(provider.profiles) != 1 || provider.profiles[0] != "profile-a" {
		t.Fatalf("profiles = %v, want [profile-a]", provider.profiles)
	}
}

func TestSummaryClientAdapterSkipsSmallContextCandidate(t *testing.T) {
	oldDoer := defaultHTTPDoer
	t.Cleanup(func() { defaultHTTPDoer = oldDoer })
	calls := 0
	defaultHTTPDoer = func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"choices":[{"message":{"content":"ok"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}

	smallWindow := defaultCompactionMinWindow - 1
	provider := &fakeSummaryProvider{candidates: []ProviderCandidate{
		{RawModel: "too-small", BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", ContextWindow: &smallWindow, Available: true},
		{RawModel: "large-enough", BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", Available: true},
	}}
	client := newSummaryClientAdapter(&Dependencies{Provider: provider}, "", "tenant-a")
	got, err := client.Complete(context.Background(), "prompt", summarymodel.WithModel("model-a"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "ok" || calls != 1 {
		t.Fatalf("summary/calls = %q/%d, want ok/1", got, calls)
	}
}

type fakeTenantSummaryProvider struct {
	tenantID string
}

func (f *fakeTenantSummaryProvider) Enabled() bool { return true }

func (f *fakeTenantSummaryProvider) GetCandidates(ctx context.Context, model, profile string) ([]ProviderCandidate, error) {
	return nil, nil
}

func (f *fakeTenantSummaryProvider) GetCandidatesForTenant(ctx context.Context, model, profile, tenantID string) ([]ProviderCandidate, error) {
	f.tenantID = tenantID
	return []ProviderCandidate{{RawModel: model, BaseURL: "https://example.invalid", APIKey: "key", Protocol: "openai-completions", Available: true}}, nil
}

func TestSummaryClientAdapterUsesTenantAwareProvider(t *testing.T) {
	provider := &fakeTenantSummaryProvider{}
	candidates, err := getSummaryCandidates(context.Background(), provider, "model-a", "profile-a", "tenant-a")
	if err != nil {
		t.Fatalf("getSummaryCandidates: %v", err)
	}
	if len(candidates) != 1 || provider.tenantID != "tenant-a" {
		t.Fatalf("candidates=%v tenant=%q, want one candidate for tenant-a", candidates, provider.tenantID)
	}
}
