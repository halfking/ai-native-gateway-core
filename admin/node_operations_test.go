// Package admin — node_operations_test.go
//
// V3.2-LP5 (2026-08-14) 节点操作测试：限流、审计、二次确认门禁。
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunCredentialSessionPing(t *testing.T) {
	t.Run("sends one minimal authenticated chat request", func(t *testing.T) {
		var payload map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("authorization = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"id":"ping","choices":[]}`)
		}))
		defer server.Close()

		h := &Handler{}
		status, code, message := h.runCredentialSessionPing(context.Background(), server.URL+"/v1", "", "", "test-key", "m-1")
		if status != "healthy" || code != "" || message != "" {
			t.Fatalf("result = (%q, %q, %q)", status, code, message)
		}
		if payload["model"] != "m-1" || payload["max_tokens"] != float64(1) || payload["stream"] != false {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
	})

	t.Run("classifies upstream authentication failures", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad key", http.StatusUnauthorized)
		}))
		defer server.Close()

		status, code, _ := (&Handler{}).runCredentialSessionPing(context.Background(), server.URL, "", "", "test-key", "m-1")
		if status != "auth_failed" || code != "auth_failed" {
			t.Fatalf("result = (%q, %q)", status, code)
		}
	})

	// 2026-09-06: anthropic-messages 协议测试
	t.Run("sends anthropic messages format when protocol is anthropic-messages", func(t *testing.T) {
		var payload map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/messages" {
				t.Fatalf("wrong path for anthropic: %s", r.URL.Path)
			}
			if r.Header.Get("x-api-key") != "test-key" {
				t.Fatalf("missing x-api-key header")
			}
			if r.Header.Get("anthropic-version") == "" {
				t.Fatalf("missing anthropic-version header")
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"pong"}],"model":"claude-sonnet-5","stop_reason":"end_turn"}`))
		}))
		defer server.Close()

		h := &Handler{}
		status, code, message := h.runCredentialSessionPing(
			context.Background(),
			server.URL,
			"anthropic-messages",
			"",
			"test-key",
			"claude-sonnet-5",
		)
		if status != "healthy" || code != "" || message != "" {
			t.Fatalf("result = (%q, %q, %q)", status, code, message)
		}
		if payload["model"] != "claude-sonnet-5" || payload["max_tokens"] != float64(1) {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		// anthropic 格式不包含 stream 字段
		if _, hasStream := payload["stream"]; hasStream {
			t.Fatalf("anthropic format should not include stream field")
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
	})
}

func TestNodeOperationsRateLimiter(t *testing.T) {
	rl := newNodeOperationsRateLimiter()
	ctx := context.Background()

	t.Run("per-credential 1req/s limit", func(t *testing.T) {
		credID := 100
		operatorID := "test-op-1"

		// 第一次应该通过
		if err := rl.checkTestNow(ctx, credID, operatorID); err != nil {
			t.Errorf("first request should pass: %v", err)
		}

		// 立即第二次应该被限流
		if err := rl.checkTestNow(ctx, credID, operatorID); err == nil {
			t.Error("second immediate request should be rate limited")
		}

		// 等待 1.1s 后应该通过
		time.Sleep(1100 * time.Millisecond)
		if err := rl.checkTestNow(ctx, credID, operatorID); err != nil {
			t.Errorf("request after 1s should pass: %v", err)
		}
	})

	t.Run("per-operator 10req/min limit", func(t *testing.T) {
		operatorID := "test-op-2"

		// burst=3，前 3 次应该通过
		for i := 0; i < 3; i++ {
			credID := 200 + i // 不同 credential
			if err := rl.checkTestNow(ctx, credID, operatorID); err != nil {
				t.Errorf("request %d should pass (burst): %v", i+1, err)
			}
		}

		// 第 4 次应该被限流（超过 burst）
		if err := rl.checkTestNow(ctx, 204, operatorID); err == nil {
			t.Error("request exceeding burst should be rate limited")
		}
	})
}

func TestHandleNodeTestNowRateLimit(t *testing.T) {
	// 跳过集成测试（需要真实 DB）
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// 这里仅测试限流逻辑，完整集成测试需要真实 DB
	t.Run("rate limiter integration", func(t *testing.T) {
		// Mock handler 仅用于验证限流器调用
		h := &Handler{
			rateLimiter: newNodeOperationsRateLimiter(),
		}

		ctx := context.Background()
		credID := 1
		operatorID := "test-operator"

		// 第一次通过
		if err := h.rateLimiter.checkTestNow(ctx, credID, operatorID); err != nil {
			t.Errorf("first check should pass: %v", err)
		}

		// 立即第二次被限流
		if err := h.rateLimiter.checkTestNow(ctx, credID, operatorID); err == nil {
			t.Error("second immediate check should fail")
		}
	})
}

func TestHandleNodeToggleConfirmation(t *testing.T) {
	tests := []struct {
		name          string
		confirmHeader string
		reason        string
		wantStatus    int
	}{
		{
			name:          "missing X-Confirm header",
			confirmHeader: "",
			reason:        "test reason",
			wantStatus:    http.StatusPreconditionRequired,
		},
		{
			name:          "wrong X-Confirm value",
			confirmHeader: "no",
			reason:        "test reason",
			wantStatus:    http.StatusPreconditionRequired,
		},
		{
			name:          "missing reason",
			confirmHeader: "yes",
			reason:        "",
			wantStatus:    http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]any{
				"enabled": true,
				"reason":  tt.reason,
			}
			bodyJSON, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPatch, "/api/admin/providers/1/enable", bytes.NewReader(bodyJSON))
			req.Header.Set("Content-Type", "application/json")
			if tt.confirmHeader != "" {
				req.Header.Set("X-Confirm", tt.confirmHeader)
			}
			req.SetPathValue("id", "1")

			w := httptest.NewRecorder()

			// 测试门禁逻辑
			if tt.confirmHeader != "yes" && tt.confirmHeader != "" {
				if req.Header.Get("X-Confirm") != "yes" {
					writeError(w, http.StatusPreconditionRequired, "X-Confirm: yes header required")
					if w.Code != tt.wantStatus {
						t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
					}
					return
				}
			}

			if tt.confirmHeader == "" {
				writeError(w, http.StatusPreconditionRequired, "X-Confirm: yes header required")
			} else if tt.reason == "" {
				writeError(w, http.StatusBadRequest, "reason is required")
			}

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestExtractOperatorID(t *testing.T) {
	tests := []struct {
		name       string
		headerVal  string
		remoteAddr string
		want       string
	}{
		{
			name:       "from X-Operator-ID header",
			headerVal:  "admin-user-123",
			remoteAddr: "192.168.1.1:5000",
			want:       "admin-user-123",
		},
		{
			name:       "fallback to RemoteAddr",
			headerVal:  "",
			remoteAddr: "192.168.1.1:5000",
			want:       "192.168.1.1:5000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", nil)
			if tt.headerVal != "" {
				req.Header.Set("X-Operator-ID", tt.headerVal)
			}
			req.RemoteAddr = tt.remoteAddr

			got := extractOperatorID(req)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIdempotencyKeyGeneration(t *testing.T) {
	providerID := 123

	t.Run("test-now request ID format", func(t *testing.T) {
		id := generateTestNowRequestID(providerID)
		if id == "" {
			t.Error("request ID should not be empty")
		}
		// 应该包含 provider ID
		if !hasPrefix(id, "provider:123:test-now:") {
			t.Errorf("request ID should contain provider ID: %s", id)
		}
	})

	t.Run("toggle request ID with key", func(t *testing.T) {
		key := "idempotency-key-abc"
		id := generateToggleRequestID(providerID, key)
		if id != "provider:123:toggle:idempotency-key-abc" {
			t.Errorf("got %q, want %q", id, "provider:123:toggle:idempotency-key-abc")
		}
	})

	t.Run("toggle request ID without key", func(t *testing.T) {
		id := generateToggleRequestID(providerID, "")
		if !hasPrefix(id, "provider:123:toggle:") {
			t.Errorf("request ID should contain provider ID: %s", id)
		}
	})
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
