package specboost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockLLMResponse is the JSON shape the LLM endpoint must return.
type mockLLMResponse struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Examples    []Example      `json:"examples,omitempty"`
	DiffSummary string         `json:"diff_summary"`
	Confidence  float64        `json:"confidence"`
}

// newMockLLM starts an httptest server that responds with the given payload
// (status 200) and returns its URL. Call close() when done.
func newMockLLM(t *testing.T, resp mockLLMResponse) (url string, close func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate the request shape a bit.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("expected Authorization 'Bearer test-key', got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv.URL, srv.Close
}

// TestEnhance_EmptyDescription_EnhancedNonEmpty verifies that a tool with an
// empty description gets a non-empty one after enhancement.
func TestEnhance_EmptyDescription_EnhancedNonEmpty(t *testing.T) {
	enhancedDesc := "Searches the web for real-time information and returns ranked results."
	u, close := newMockLLM(t, mockLLMResponse{
		Description: enhancedDesc,
		DiffSummary: "Added a description.",
		Confidence:  0.9,
	})
	defer close()

	original := ToolSpec{Name: "web_search", Description: ""}
	res, err := Enhance(context.Background(), original, EnhanceOptions{Endpoint: u, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("Enhance returned error: %v", err)
	}
	if res.Enhanced.Description == "" {
		t.Error("expected enhanced description to be non-empty")
	}
	if res.Enhanced.Description != enhancedDesc {
		t.Errorf("expected %q, got %q", enhancedDesc, res.Enhanced.Description)
	}
}

// TestEnhance_WithDescription_GetsLongerAndExamples verifies that an existing
// description is enriched (longer) and that examples are populated.
func TestEnhance_WithDescription_GetsLongerAndExamples(t *testing.T) {
	u, close := newMockLLM(t, mockLLMResponse{
		Description: "This is a much longer and more detailed description that explains the tool thoroughly.",
		Examples: []Example{
			{Input: map[string]any{"query": "golang testing"}, Output: "results..."},
		},
		DiffSummary: "Expanded description and added an example.",
		Confidence:  0.85,
	})
	defer close()

	original := ToolSpec{
		Name:        "web_search",
		Description: "Searches the web.",
	}
	res, err := Enhance(context.Background(), original, EnhanceOptions{Endpoint: u, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("Enhance returned error: %v", err)
	}
	if len(res.Enhanced.Description) <= len(original.Description) {
		t.Errorf("expected enhanced description to be longer than original: got %d <= %d",
			len(res.Enhanced.Description), len(original.Description))
	}
	if len(res.Enhanced.Examples) < 1 {
		t.Error("expected at least 1 example after enhancement")
	}
}

// TestEnhance_MalformedJSON_ReturnsError verifies graceful degradation when
// the LLM returns invalid JSON.
func TestEnhance_MalformedJSON_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "this is { not valid json")
	}))
	defer srv.Close()

	original := ToolSpec{Name: "broken", Description: "x"}
	_, err := Enhance(context.Background(), original, EnhanceOptions{Endpoint: srv.URL, APIKey: "test-key"})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "decode") && !strings.Contains(err.Error(), "parse") &&
		!strings.Contains(err.Error(), "json") && !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error about JSON parsing, got: %v", err)
	}
}

// TestEnhance_OverlongResponse_Truncates verifies that a response exceeding
// MaxResponseBytes is rejected rather than consuming unbounded memory.
func TestEnhance_OverlongResponse_Truncates(t *testing.T) {
	// Generate a response body larger than 4096 bytes.
	huge := strings.Repeat("a", 8192)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a valid JSON object but with a massive description field.
		_ = json.NewEncoder(w).Encode(mockLLMResponse{
			Description: huge,
			Confidence:  0.5,
		})
	}))
	defer srv.Close()

	original := ToolSpec{Name: "big", Description: "x"}
	res, err := Enhance(context.Background(), original, EnhanceOptions{
		Endpoint:         srv.URL,
		APIKey:           "test-key",
		MaxResponseBytes: 512, // deliberately tiny to force the cap
	})
	// The cap is on the HTTP body reader, so either an error is returned OR
	// the description is truncated. Accept both as "did not OOM".
	if err != nil {
		return // graceful: the read was capped and decode failed — acceptable
	}
	if len(res.Enhanced.Description) > 512 {
		t.Errorf("expected description to be truncated to <= 512 bytes, got %d",
			len(res.Enhanced.Description))
	}
}

// TestEnhance_LLMHTTPError_Propagates verifies that non-200 LLM responses
// produce an error (no silent success).
func TestEnhance_LLMHTTPError_Propagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "internal error")
	}))
	defer srv.Close()

	original := ToolSpec{Name: "err", Description: "x"}
	_, err := Enhance(context.Background(), original, EnhanceOptions{Endpoint: srv.URL, APIKey: "test-key"})
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

// TestEnhance_TemplateVersionRecorded verifies the EnhancedSpec carries the
// prompt template version used, so quality regressions are attributable.
func TestEnhance_TemplateVersionRecorded(t *testing.T) {
	u, close := newMockLLM(t, mockLLMResponse{
		Description: "x",
		Confidence:  0.8,
	})
	defer close()

	res, err := Enhance(context.Background(), ToolSpec{Name: "t"}, EnhanceOptions{Endpoint: u, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("Enhance returned error: %v", err)
	}
	if res.TemplateVer != PromptTemplateV1 {
		t.Errorf("expected template %q, got %q", PromptTemplateV1, res.TemplateVer)
	}
}

// TestEnhanceContext_Cancelled verifies context cancellation is honored.
func TestEnhanceContext_Cancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := Enhance(ctx, ToolSpec{Name: "slow"}, EnhanceOptions{Endpoint: srv.URL, APIKey: "test-key"})
	if err == nil {
		t.Fatal("expected context deadline error, got nil")
	}
}
