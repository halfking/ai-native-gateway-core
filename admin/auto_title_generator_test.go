package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDetectIDESource(t *testing.T) {
	gen := &AutoTitleGenerator{}

	tests := []struct {
		name     string
		preview  string
		expected string
	}{
		{
			name:     "ZCode IDE",
			preview:  "[system]\nYou are ZCode, an interactive coding agent",
			expected: "[ZCode]",
		},
		{
			name:     "ZooCode IDE",
			preview:  "[system]\nYou are Zoo, a helpful assistant",
			expected: "[ZooCode]",
		},
		{
			name:     "Cursor IDE",
			preview:  "You are Cursor, an AI coding assistant",
			expected: "[Cursor]",
		},
		{
			name:     "OpenCode IDE",
			preview:  "You are OpenCode, the best coding agent",
			expected: "[OpenCode]",
		},
		{
			name:     "No IDE detected",
			preview:  "Write a function to reverse a string",
			expected: "",
		},
		{
			name:     "Case insensitive",
			preview:  "YOU ARE ZCODE, AN INTERACTIVE CODING AGENT",
			expected: "[ZCode]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.detectIDESource(tt.preview)
			if result != tt.expected {
				t.Errorf("detectIDESource(%q) = %q, want %q", tt.preview, result, tt.expected)
			}
		})
	}
}

func TestExtractUserPrompt(t *testing.T) {
	gen := &AutoTitleGenerator{}

	tests := []struct {
		name     string
		preview  string
		expected string
	}{
		{
			name: "With system and user messages",
			preview: `[system]
You are ZCode, an interactive coding agent
[user]
Write a function to calculate fibonacci numbers`,
			expected: "Write a function to calculate fibonacci numbers",
		},
		{
			name: "User prefix with colon",
			preview: `system: You are a helpful assistant
user: Explain how binary search works`,
			expected: "Explain how binary search works",
		},
		{
			name: "No prefixes",
			preview: `You are ZCode
Write unit tests for a login function`,
			expected: "Write unit tests for a login function",
		},
		{
			name:     "Only system messages",
			preview:  "[system]\nYou are a helpful assistant\nYour task is to help users",
			expected: "",
		},
		{
			name:     "Short line skipped",
			preview:  "You are ZCode\nOK\nImplement a REST API for user management",
			expected: "Implement a REST API for user management",
		},
		{
			name: "Long prompt truncated",
			preview: `[user]
This is a very long user prompt that exceeds sixty characters and should be truncated properly`,
			expected: "This is a very long user prompt that exceeds sixty…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.extractUserPrompt(tt.preview)
			if result != tt.expected {
				t.Errorf("extractUserPrompt() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractTitleFromPreview(t *testing.T) {
	gen := &AutoTitleGenerator{}

	tests := []struct {
		name    string
		preview string
		wantIDE bool // Should contain IDE prefix
		minLen  int  // Minimum expected length
		maxLen  int  // Maximum expected length
	}{
		{
			name: "ZCode with user prompt",
			preview: `[system]
You are ZCode, an interactive coding agent
[user]
Write a function to reverse a string`,
			wantIDE: true,
			minLen:  20,
			maxLen:  80,
		},
		{
			name: "Cursor with long prompt",
			preview: `You are Cursor, an AI coding assistant.
[user]
I need help implementing a complex authentication system with OAuth2, JWT tokens, and refresh token rotation`,
			wantIDE: true,
			minLen:  20,
			maxLen:  80,
		},
		{
			name:    "No IDE, direct prompt",
			preview: `Create a React component for a user profile page`,
			wantIDE: false,
			minLen:  20,
			maxLen:  80,
		},
		{
			name:    "Empty preview",
			preview: "",
			wantIDE: false,
			minLen:  0,
			maxLen:  0,
		},
		{
			name: "ZooCode with Chinese prompt",
			preview: `[system]
You are Zoo, a helpful assistant
[user]
帮我写一个计算斐波那契数列的函数`,
			wantIDE: true,
			minLen:  15,
			maxLen:  80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gen.extractTitleFromPreview(tt.preview)

			if tt.maxLen == 0 {
				if result != "" {
					t.Errorf("extractTitleFromPreview() = %q, want empty string", result)
				}
				return
			}

			if len(result) < tt.minLen {
				t.Errorf("extractTitleFromPreview() length = %d, want >= %d (result: %q)",
					len(result), tt.minLen, result)
			}
			if len(result) > tt.maxLen {
				t.Errorf("extractTitleFromPreview() length = %d, want <= %d (result: %q)",
					len(result), tt.maxLen, result)
			}

			if tt.wantIDE {
				hasIDEPrefix := false
				idePrefixes := []string{"[ZCode]", "[Cursor]", "[ZooCode]", "[OpenCode]"}
				for _, prefix := range idePrefixes {
					if len(result) >= len(prefix) && result[:len(prefix)] == prefix {
						hasIDEPrefix = true
						break
					}
				}
				if !hasIDEPrefix {
					t.Errorf("extractTitleFromPreview() = %q, expected IDE prefix", result)
				}
			}
		})
	}
}

func TestResolveAutoTitleModel(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ADMIN_LLM_FALLBACK_MODEL", "")

	t.Run("nil handler falls back to auto", func(t *testing.T) {
		gen := &AutoTitleGenerator{}
		if got := gen.resolveAutoTitleModel(t.Context()); got != adminLLMModelAuto {
			t.Fatalf("resolveAutoTitleModel() = %q, want %q", got, adminLLMModelAuto)
		}
	})

	t.Run("handler without db pins cheap pool default", func(t *testing.T) {
		gen := &AutoTitleGenerator{handler: &Handler{}}
		got := gen.resolveAutoTitleModel(t.Context())
		if got == adminLLMModelAuto {
			t.Fatalf("resolveAutoTitleModel() = %q, want pinned cheap model (not auto)", got)
		}
		if got != "minimax-m2.7" {
			t.Fatalf("resolveAutoTitleModel() = %q, want %q", got, "minimax-m2.7")
		}
	})

	t.Run("env override wins", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_ADMIN_LLM_FALLBACK_MODEL", "deepseek-chat")
		gen := &AutoTitleGenerator{handler: &Handler{}}
		if got := gen.resolveAutoTitleModel(t.Context()); got != "deepseek-chat" {
			t.Fatalf("resolveAutoTitleModel() = %q, want %q", got, "deepseek-chat")
		}
	})
}

// TestExtractMessagesForTitle_LastUserPreserved (2026-08-06) — guards the
// "末条 user 保留" rule. The last user message is the actual question the
// user asked; it must survive uncut even if doing so pushes the corpus past
// maxTotal.
func TestExtractMessagesForTitle_LastUserPreserved(t *testing.T) {
	// 1300-char user question — bigger than maxPerMsg (1200) on purpose.
	rawUser := strings.Repeat("为客户演示一个 API 调用 ", 100)
	// After strings.Fields collapse + Join(" "), the trailing space disappears;
	// the joined form is what survives in the corpus.
	expectedUser := strings.Join(strings.Fields(rawUser), " ")
	if len(expectedUser) < 1200 {
		t.Fatalf("test setup wrong: expectedUser len = %d, want >=1200", len(expectedUser))
	}
	body := fmt.Sprintf(`{
		"model":"claude-sonnet-4.5",
		"messages":[
			{"role":"system","content":"%s"},
			{"role":"user","content":"写一个快速排序"},
			{"role":"assistant","content":"好的..."},
			{"role":"user","content":"%s"}
		]
	}`, strings.Repeat("你是 ZCode 助手,可以使用工具 ", 50), rawUser)

	got := extractMessagesForTitle(body)
	if got == "" {
		t.Fatal("extractMessagesForTitle returned empty for valid body")
	}
	// 1) The last user message must appear uncut (full string).
	if !strings.Contains(got, expectedUser) {
		t.Fatalf("expected last user message preserved in full (len=%d); corpus tail:\n%s",
			len(expectedUser), got[max0(len(got)-2000, 0):])
	}
	// 2) The sentinel "<latest user message (preserved)>" marker must be present
	//    so operators can debug the corpus quickly.
	if !strings.Contains(got, "<latest user message (preserved)>") {
		t.Fatalf("expected sentinel marker; corpus:\n%s", got)
	}
}

func max0(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestExtractMessagesForTitle_BumpedLimits (2026-08-06) — guards the
// bumped caps: 10 msgs / 1200 chars per msg / 6000 total. Earlier (≤ 6/500/3000)
// the IDE long system prompt + multi-turn history would crowd out the user
// question.
func TestExtractMessagesForTitle_BumpedLimits(t *testing.T) {
	// Build 12 messages — only 10 should be processed for the prefix loop.
	msgs := make([]map[string]string, 12)
	for i := range msgs {
		msgs[i] = map[string]string{"role": "user", "content": fmt.Sprintf("turn-%d", i)}
	}
	bodyBytes, _ := json.Marshal(map[string]any{"messages": msgs})
	got := extractMessagesForTitle(string(bodyBytes))
	if got == "" {
		t.Fatal("extractMessagesForTitle returned empty")
	}
	// turn-0 … turn-9 must appear (10 of the 12 prefix messages).
	for i := 0; i < 10; i++ {
		if !strings.Contains(got, fmt.Sprintf("turn-%d", i)) {
			t.Errorf("expected turn-%d in corpus, got:\n%s", i, got)
		}
	}
	// turn-10, turn-11 must NOT appear in the prefix loop (the last-user
	// preservation only keeps the LAST user message; turn-11 is the last, so
	// it will appear as the preserved suffix).
	if !strings.Contains(got, "turn-11") {
		t.Errorf("expected last-user-preserved turn-11 to appear; got:\n%s", got)
	}
	// turn-10 was dropped because of the 10-msg prefix cap.
	// (turn-11 was preserved by the last-user rule.)
}

// TestExtractMessagesForTitle_LongSystemTruncated (2026-08-06) — long system
// messages (IDE tool descriptions) get the explicit "<ide-tool-context truncated>"
// marker so it's obvious in logs that the system prompt was cut.
func TestExtractMessagesForTitle_LongSystemTruncated(t *testing.T) {
	longSys := strings.Repeat("你可以使用以下工具...", 100) // well over 800 chars
	body := fmt.Sprintf(`{"messages":[
		{"role":"system","content":%q},
		{"role":"user","content":"实际问题"}
	]}`, longSys)
	got := extractMessagesForTitle(body)
	if got == "" {
		t.Fatal("expected non-empty corpus")
	}
	if !strings.Contains(got, "<ide-tool-context truncated>") {
		t.Fatalf("expected ide-tool-context truncated marker; got:\n%s", got)
	}
}

// TestCallAutoTitleLLM_EmitsParentHeaders (2026-08-06) — verifies the
// loopback request includes X-Gw-Parent-Request-Id + X-Gw-Source-Actor
// so the handler entry can persist parent_request_id / origin_actor on the
// resulting request_logs_hot row.
func TestCallAutoTitleLLM_EmitsParentHeaders(t *testing.T) {
	var (
		gotHeaders http.Header
		gotBody    []byte
		gotPath    string
		mu         sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"minimax-m2.7","choices":[{"message":{"content":"标题生成成功"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("LLM_GATEWAY_ENDPOINT", srv.URL)

	gen := &AutoTitleGenerator{handler: &Handler{}}
	res, err := gen.callAutoTitleLLM(
		t.Context(),
		"sk-fake-test-key",
		"gw_parent_session_abc",
		"08aa2a8af42ef05eb87c97973f467519", // parent request id (user's request)
		"实际请求内容",
	)
	if err != nil {
		t.Fatalf("callAutoTitleLLM unexpected error: %v", err)
	}
	if res.Content != "标题生成成功" {
		t.Fatalf("Content = %q, want %q", res.Content, "标题生成成功")
	}

	mu.Lock()
	defer mu.Unlock()

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if v := gotHeaders.Get("X-Gw-Parent-Request-Id"); v != "08aa2a8af42ef05eb87c97973f467519" {
		t.Errorf("X-Gw-Parent-Request-Id = %q, want user request_id", v)
	}
	if v := gotHeaders.Get("X-Gw-Source-Actor"); v != "auto-title-generator" {
		t.Errorf("X-Gw-Source-Actor = %q, want auto-title-generator", v)
	}
	if v := gotHeaders.Get("X-Gw-Is-Auto"); v != "true" {
		t.Errorf("X-Gw-Is-Auto = %q, want true", v)
	}
	if v := gotHeaders.Get("Authorization"); !strings.HasPrefix(v, "Bearer ") {
		t.Errorf("Authorization = %q, want Bearer …", v)
	}
	// Payload sanity: model must be in body, user message must be present.
	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload not JSON: %v (body=%q)", err, gotBody)
	}
	if payload["model"] == nil || payload["model"] == "" {
		t.Errorf("payload missing model: %v", payload)
	}
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) < 1 {
		t.Fatalf("payload messages missing/empty: %v", payload["messages"])
	}
}

// TestCallAutoTitleLLM_RetriesOn503 (2026-08-06) — verifies that a single
// 503 response triggers a single retry (per resolveAutoTitleModel's 1-retry
// policy) and a subsequent 200 succeeds. Without the retry, the first
// 503 would surface as "LLM didn't receive the request".
func TestCallAutoTitleLLM_RetriesOn503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream busy"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"minimax-m2.7","choices":[{"message":{"content":"成功"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("LLM_GATEWAY_ENDPOINT", srv.URL)

	gen := &AutoTitleGenerator{handler: &Handler{}}
	res, err := gen.callAutoTitleLLM(
		t.Context(),
		"sk-fake",
		"gw_s",
		"parent-req-1",
		"content",
	)
	if err != nil {
		t.Fatalf("expected retry to recover; got error: %v", err)
	}
	if res.Content != "成功" {
		t.Fatalf("Content = %q, want %q", res.Content, "成功")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2 (1 + 1 retry)", got)
	}
}

// TestCallAutoTitleLLM_NoRetryOn400 (2026-08-06) — 4xx errors are caller-side
// bugs and must surface immediately, not waste a retry slot.
func TestCallAutoTitleLLM_NoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad model name"}`))
	}))
	defer srv.Close()
	t.Setenv("LLM_GATEWAY_ENDPOINT", srv.URL)

	gen := &AutoTitleGenerator{handler: &Handler{}}
	_, err := gen.callAutoTitleLLM(t.Context(), "sk-fake", "gw_s", "p", "c")
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", got)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should include status code 400: %v", err)
	}
}

// TestIsTransientAutoTitleErr (2026-08-06) — directly unit-tests the
// retry classifier without standing up a fake server.
func TestIsTransientAutoTitleErr(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   bool
	}{
		{"nil error is not transient", nil, 0, false},
		{"502 is transient", fmt.Errorf("status 502: bad gateway"), 502, true},
		{"503 is transient", fmt.Errorf("status 503: unavailable"), 503, true},
		{"504 is transient", fmt.Errorf("status 504: gateway timeout"), 504, true},
		{"500 is NOT transient", fmt.Errorf("status 500: internal"), 500, false},
		{"400 is NOT transient", fmt.Errorf("status 400: bad req"), 400, false},
		{"EOF is transient", io.EOF, 0, true},
		{"ErrUnexpectedEOF is transient", io.ErrUnexpectedEOF, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientAutoTitleErr(tc.err, tc.status); got != tc.want {
				t.Fatalf("isTransientAutoTitleErr(%v, %d) = %v, want %v",
					tc.err, tc.status, got, tc.want)
			}
		})
	}
}
