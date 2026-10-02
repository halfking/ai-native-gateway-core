// Package admin — node_operations_responses_test.go
//
// 2026-09-25 vapeur/hxt-local gpt-5.6-terra 回归：openai-responses 供应商的
// session-ping 必须走原生 /v1/responses（{"input","max_output_tokens"}）。
// 旧实现恒走 /v1/chat/completions + max_tokens=1，vapeur 直接 400
// "Could not finish the message because max_tokens or model output limit
// was reached"（用户报障原文）。
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunCredentialSessionPing_ResponsesProtocol(t *testing.T) {
	t.Run("sends responses-native body when protocol is openai-responses", func(t *testing.T) {
		var payload map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				t.Fatalf("openai-responses ping must hit /v1/responses, got %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"pong"}]}]}`))
		}))
		defer server.Close()

		status, code, message := (&Handler{}).runCredentialSessionPing(
			context.Background(), server.URL+"/v1", "openai-responses", "", "test-key", "gpt-5.6-terra")
		if status != "healthy" || code != "" || message != "" {
			t.Fatalf("result = (%q, %q, %q)", status, code, message)
		}
		if payload["model"] != "gpt-5.6-terra" || payload["input"] != "ping" {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		// Responses API 下限 16（vapeur 实测 400 "Expected >= 16"）
		mot, ok := payload["max_output_tokens"].(float64)
		if !ok || mot < 16 {
			t.Fatalf("max_output_tokens = %#v, want >= 16", payload["max_output_tokens"])
		}
		if _, hasMessages := payload["messages"]; hasMessages {
			t.Fatalf("responses ping must not carry chat \"messages\" field: %#v", payload)
		}
	})

	t.Run("normalizes alias protocol spellings to responses endpoint", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/responses" {
				t.Fatalf("alias protocol must resolve to /v1/responses, got %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","status":"completed","output":[]}`))
		}))
		defer server.Close()

		// "openai-response"（单数）是 2026-09-23 vapeur 事故中的真实错误拼写，
		// providers.protocol 无 CHECK 约束，历史行可能残留。
		status, code, _ := (&Handler{}).runCredentialSessionPing(
			context.Background(), server.URL+"/v1", "openai-response", "", "test-key", "gpt-5.6-terra")
		if status != "healthy" || code != "" {
			t.Fatalf("result = (%q, %q)", status, code)
		}
	})

	t.Run("treats incomplete responses envelope as healthy", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 输出预算被推理耗尽时上游返回 200 + status=incomplete——
			// 仍证明 key/端点/模型全部可用。
			_, _ = w.Write([]byte(`{"id":"resp_3","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`))
		}))
		defer server.Close()

		status, code, _ := (&Handler{}).runCredentialSessionPing(
			context.Background(), server.URL+"/v1", "openai-responses", "", "test-key", "gpt-5.6-terra")
		if status != "healthy" || code != "" {
			t.Fatalf("result = (%q, %q)", status, code)
		}
	})
}

func TestIsChatPingResponse_ResponsesShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"chat completion", `{"object":"chat.completion","choices":[{"message":{"content":"pong"}}]}`, true},
		{"anthropic message", `{"type":"message","content":[{"type":"text","text":"pong"}]}`, true},
		{"responses completed", `{"object":"response","status":"completed","output":[{"type":"message"}]}`, true},
		{"responses incomplete", `{"object":"response","status":"incomplete","output":[]}`, true},
		{"responses error body", `{"error":{"message":"boom"}}`, false},
		{"empty body", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChatPingResponse([]byte(tc.body)); got != tc.want {
				t.Fatalf("isChatPingResponse(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
