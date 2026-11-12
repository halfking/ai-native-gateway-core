package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGenerateParsesChatCompletion 钉住能正确解析 OpenAI 兼容响应。
func TestGenerateParsesChatCompletion(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	var gotMessages []chatMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		gotMessages = req.Messages

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-1",
			"object": "chat.completion",
			"model":  req.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "优化后的提示词"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "small-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out, err := c.Generate(context.Background(), "原始提示词", Options{Temperature: 0.7, MaxTokens: 100})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "优化后的提示词" {
		t.Errorf("输出不对: %q", out)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("路径不对: %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("鉴权头不对: %q", gotAuth)
	}
	if gotModel != "small-model" {
		t.Errorf("模型不对: %q", gotModel)
	}
	if len(gotMessages) != 1 || gotMessages[0].Content != "原始提示词" {
		t.Errorf("消息不对: %+v", gotMessages)
	}
}

// TestNewAppendsV1Path 钉住 base URL 自动补 /v1。
func TestNewAppendsV1Path(t *testing.T) {
	c, err := New(Config{BaseURL: "http://gateway:8080"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != "http://gateway:8080/v1" {
		t.Errorf("应补 /v1，实际 %q", c.baseURL)
	}

	// 已经带 /v1 的不应重复补。
	c2, err := New(Config{BaseURL: "http://gateway:8080/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c2.baseURL != "http://gateway:8080/v1" {
		t.Errorf("不应重复补 /v1，实际 %q", c2.baseURL)
	}
}

// TestNewRejectsEmptyBaseURL 钉住空 base URL 报错。
func TestNewRejectsEmptyBaseURL(t *testing.T) {
	if _, err := New(Config{BaseURL: "  "}); err == nil {
		t.Fatal("空 base URL 必须报错")
	}
}

// TestGeneratePropagatesUpstreamError 钉住上游错误码被如实上报。
func TestGeneratePropagatesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{BaseURL: srv.URL})
	_, err := c.Generate(context.Background(), "x", Options{})
	if err == nil {
		t.Fatal("上游错误必须上报")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("错误信息应含状态码: %v", err)
	}
}

// TestGeneratePropagatesErrorPayload 钉住 200 但 body 里带 error 时也报错。
func TestGeneratePropagatesErrorPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{BaseURL: srv.URL})
	_, err := c.Generate(context.Background(), "x", Options{})
	if err == nil {
		t.Fatal("body 里的 error 必须被识别")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("错误信息应含上游原因: %v", err)
	}
}

// TestGenerateRejectsEmptyChoices 钉住空 choices 报错。
func TestGenerateRejectsEmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	c, _ := New(Config{BaseURL: srv.URL})
	if _, err := c.Generate(context.Background(), "x", Options{}); err == nil {
		t.Fatal("空 choices 必须报错")
	}
}
