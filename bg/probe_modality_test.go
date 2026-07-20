package bg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeModality_TextModel_NoProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called for text modality")
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "key", "gpt-3.5-turbo", "text", false)
	if !result.Supported {
		t.Errorf("text modality should return Supported=true, got %v", result.Supported)
	}
	if result.Modality != "text" {
		t.Errorf("modality=%q want text", result.Modality)
	}
}

func TestProbeModality_EmbeddingModel_NoProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called for embedding modality")
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "key", "text-embedding-3-small", "embedding", false)
	if !result.Supported {
		t.Errorf("embedding modality should return Supported=true, got %v", result.Supported)
	}
}

func TestProbeModality_Vision_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "image_url") {
			t.Errorf("expected image_url in vision probe payload, got: %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "."}}},
		})
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "sk-test", "gpt-4o", "vision", false)
	if !result.Supported {
		t.Errorf("vision modality should be Supported on 200, got errCode=%q errMsg=%q", result.ErrCode, result.ErrMsg)
	}
	if result.HTTPStatus != 200 {
		t.Errorf("HTTPStatus=%d want 200", result.HTTPStatus)
	}
}

func TestProbeModality_Vision_RejectedByUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"code":    "invalid_request_error",
				"message": "vision not supported for this model",
			},
		})
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "sk-test", "gpt-3.5-turbo", "vision", false)
	if result.Supported {
		t.Errorf("vision should be Supported=false when upstream rejects with vision keyword")
	}
	if result.ErrCode != "modality_unsupported" {
		t.Errorf("ErrCode=%q want modality_unsupported", result.ErrCode)
	}
}

func TestProbeModality_Vision_GenericError_KeepExisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"rate_limit","message":"too many requests"}}`))
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "sk-test", "gpt-4o", "vision", false)
	if !result.Supported {
		t.Errorf("generic 400 should not change modality (Supported=true)")
	}
	if result.ErrCode != "http_4xx" {
		t.Errorf("ErrCode=%q want http_4xx", result.ErrCode)
	}
}

func TestProbeModality_AuthError_DontChangeModality(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "bad-key", "gpt-4o", "vision", false)
	if !result.Supported {
		t.Errorf("auth error should not change modality")
	}
	if result.ErrCode != "auth" {
		t.Errorf("ErrCode=%q want auth", result.ErrCode)
	}
}

func TestProbeModality_ServerError_DontChangeModality(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal"}`))
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "key", "gpt-4o", "vision", false)
	if !result.Supported {
		t.Errorf("5xx should not change modality")
	}
	if result.ErrCode != "http_5xx" {
		t.Errorf("ErrCode=%q want http_5xx", result.ErrCode)
	}
}

func TestProbeModality_Audio_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "input_audio") {
			t.Errorf("expected input_audio in audio probe payload")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"text": "."})
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "key", "whisper-1", "audio", false)
	if !result.Supported {
		t.Errorf("audio should be Supported on 200, got errCode=%q", result.ErrCode)
	}
}

func TestProbeModality_Anthropic_Vision(t *testing.T) {
	var capturedAuth string
	var capturedVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("x-api-key")
		capturedVersion = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"source"`) {
			t.Errorf("expected 'source' field in Anthropic vision payload")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "msg_test",
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]string{{"type": "text", "text": "."}},
		})
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "sk-ant-test", "claude-sonnet-4", "vision", true)
	if !result.Supported {
		t.Errorf("Anthropic vision should be Supported on 200, got errCode=%q", result.ErrCode)
	}
	if capturedAuth != "sk-ant-test" {
		t.Errorf("expected x-api-key=%q, got %q", "sk-ant-test", capturedAuth)
	}
	if capturedVersion == "" {
		t.Errorf("expected anthropic-version header to be set")
	}
}

func TestProbeModality_NetworkError(t *testing.T) {
	result := ProbeModality(context.Background(), "http://127.0.0.1:1", "key", "gpt-4o", "vision", false)
	if result.Supported {
		t.Errorf("network error should not change modality")
	}
	if result.ErrCode != "network" {
		t.Errorf("ErrCode=%q want network", result.ErrCode)
	}
}

func TestProbeModality_Multimodal_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "."}}}})
	}))
	defer server.Close()

	result := ProbeModality(context.Background(), server.URL, "key", "gemini-1.5-pro", "multimodal", false)
	if !result.Supported {
		t.Errorf("multimodal should be Supported on 200")
	}
}
