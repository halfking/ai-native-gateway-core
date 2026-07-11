package analysis

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAIClientCompleteWithConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model       string              `json:"model"`
			Messages    []map[string]string `json:"messages"`
			MaxTokens   int                 `json:"max_tokens"`
			Temperature float64             `json:"temperature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "summary-fast" || request.MaxTokens != 500 || request.Temperature != 0.3 {
			t.Fatalf("request = %+v", request)
		}
		if len(request.Messages) != 2 || request.Messages[0]["role"] != "system" {
			t.Fatalf("messages = %+v", request.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{"message": map[string]any{"content": "done"}},
		}})
	}))
	defer server.Close()

	client := NewOpenAIClientWithNetworkPolicy(server.URL, "test-key", time.Second, true)
	result, err := client.CompleteWithConfig(t.Context(), "prompt", CompletionRequest{
		Model:        "summary-fast",
		MaxTokens:    500,
		Temperature:  0.3,
		SystemPrompt: "system",
	})
	if err != nil || result != "done" {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

func TestOpenAIClientEmbedBatchRestoresIndexOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var request struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "embedding-fast" || request.Dimensions != 1024 || len(request.Input) != 2 {
			t.Fatalf("request = %+v", request)
		}
		first := make([]float32, 1024)
		second := make([]float32, 1024)
		first[0] = 1
		second[0] = 2
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": second},
			map[string]any{"index": 0, "embedding": first},
		}})
	}))
	defer server.Close()

	client := NewOpenAIClientWithNetworkPolicy(server.URL, "test-key", time.Second, true)
	vectors, err := client.EmbedBatch(t.Context(), "embedding-fast", []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("vectors returned out of order: %v %v", vectors[0][0], vectors[1][0])
	}
}

func TestOpenAIClientRejectsWrongDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 0, "embedding": []float32{1, 2}},
		}})
	}))
	defer server.Close()

	client := NewOpenAIClientWithNetworkPolicy(server.URL, "test-key", time.Second, true)
	if _, err := client.Embed(t.Context(), "embedding-fast", "text"); err == nil {
		t.Fatal("expected dimensions error")
	}
}

func TestParseVectorText(t *testing.T) {
	vector, err := parseVectorText("[0.1, -0.2,3]")
	if err != nil {
		t.Fatal(err)
	}
	if len(vector) != 3 || vector[0] != float32(0.1) || vector[1] != float32(-0.2) || vector[2] != 3 {
		t.Fatalf("vector = %v", vector)
	}
}
