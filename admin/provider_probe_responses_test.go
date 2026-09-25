package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression for the 2026-09-23 vapeur incident: the credential health
// check must probe openai-responses providers with the Responses-API wire
// shape ({"model","input"}) instead of the chat shape ({"model","messages"}).
// The old unconditional chat probe hit /chat/completions even for
// openai-responses credentials.
func TestDoResponsesProbe_SendsResponsesBodyShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","model":"grok-4.6","status":"completed"}`))
	}))
	t.Cleanup(srv.Close)

	result, err := doResponsesProbe(context.Background(), srv.URL+"/responses", "test-key", "grok-4.6")
	if err != nil {
		t.Fatalf("doResponsesProbe() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("probe path = %q, want /responses", gotPath)
	}
	if _, ok := gotBody["input"]; !ok {
		t.Fatalf("request body missing \"input\" field: %v", gotBody)
	}
	if _, ok := gotBody["messages"]; ok {
		t.Fatalf("request body must not contain chat \"messages\" field: %v", gotBody)
	}
	// 2026-09-25: Responses API 下限 16（vapeur 实测 "Expected >= 16"），
	// 旧值 5 会把健康凭据误报成 warning。
	if mot, ok := gotBody["max_output_tokens"].(float64); !ok || mot < 16 {
		t.Fatalf("max_output_tokens = %v, want >= 16", gotBody["max_output_tokens"])
	}
	if result.statusCode != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200", result.statusCode)
	}
	if result.modelInResponse != "grok-4.6" {
		t.Fatalf("modelInResponse = %q, want grok-4.6", result.modelInResponse)
	}
}

func TestDoResponsesProbe_PropagatesUpstreamErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"an internal error occurred","code":"internal_error"}}`))
	}))
	t.Cleanup(srv.Close)

	result, err := doResponsesProbe(context.Background(), srv.URL, "test-key", "grok-4.6")
	if err != nil {
		t.Fatalf("doResponsesProbe() error = %v", err)
	}
	if result.statusCode != http.StatusInternalServerError {
		t.Fatalf("statusCode = %d, want 500", result.statusCode)
	}
	if !strings.Contains(result.errorMessage, "internal error") {
		t.Fatalf("errorMessage = %q, want upstream reason", result.errorMessage)
	}
}
