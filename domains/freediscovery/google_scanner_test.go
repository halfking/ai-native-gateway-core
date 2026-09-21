package freediscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGoogleScanner_RawMetadataAndFiltering covers the Generative Language
// protocol scan end to end: gemini models with generateContent are free
// candidates, everything else is filtered, and RawMetadata must carry the raw
// upstream entry (it was previously always nil — decodeGoogleEntries was never
// called from ScanModels).
func TestGoogleScanner_RawMetadataAndFiltering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models": [
			{"name": "models/gemini-2.0-flash", "displayName": "Gemini 2.0 Flash",
			 "inputTokenLimit": 1048576, "outputTokenLimit": 8192,
			 "supportedGenerationMethods": ["generateContent", "countTokens"]},
			{"name": "models/embedding-001",
			 "supportedGenerationMethods": ["embedContent"]},
			{"name": "models/gemini-1.5-pro",
			 "supportedGenerationMethods": ["countTokens"]}
		]}`))
	}))
	defer srv.Close()

	s := NewGoogleGenerativeAIScanner(srv.Client())
	tpl := &ProviderTemplate{
		ProviderCode: "google-ai-studio",
		BaseURL:      srv.URL,
		APIType:      APITypeGoogleGenerativeAI,
	}
	models, err := s.ScanModels(context.Background(), tpl, "key")
	if err != nil {
		t.Fatalf("ScanModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected exactly 1 free model, got %d: %+v", len(models), models)
	}
	m := models[0]
	if m.ModelID != "gemini-2.0-flash" {
		t.Fatalf("model id = %q, want gemini-2.0-flash (models/ prefix stripped)", m.ModelID)
	}
	if m.ContextWindow != 1048576 || m.MaxTokens != 8192 {
		t.Fatalf("token limits = %d/%d, want 1048576/8192", m.ContextWindow, m.MaxTokens)
	}
	if m.PoolKey != "google-aistudio-free-pool" || m.DailyTokens != 50000 {
		t.Fatalf("pool/quota estimate = %q/%d", m.PoolKey, m.DailyTokens)
	}
	if m.RawMetadata == nil || m.RawMetadata["name"] != "models/gemini-2.0-flash" {
		t.Fatalf("RawMetadata must carry the raw upstream entry, got %#v", m.RawMetadata)
	}
}

// TestGoogleScanner_ErrorStatus fails loudly on a non-200 upstream.
func TestGoogleScanner_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "bad key"}}`))
	}))
	defer srv.Close()

	s := NewGoogleGenerativeAIScanner(srv.Client())
	_, err := s.ScanModels(context.Background(), &ProviderTemplate{
		ProviderCode: "google-ai-studio", BaseURL: srv.URL,
	}, "key")
	if err == nil {
		t.Fatalf("non-200 upstream must error")
	}
}
