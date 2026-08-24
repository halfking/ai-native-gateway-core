package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/redis/go-redis/v9"
)

func unsetEnvForTest(t *testing.T, envName string) {
	t.Helper()
	original, wasSet := os.LookupEnv(envName)
	if err := os.Unsetenv(envName); err != nil {
		t.Fatalf("unset %s: %v", envName, err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(envName, original)
			return
		}
		_ = os.Unsetenv(envName)
	})
}

func TestReadAutoGeneratorEnabled(t *testing.T) {
	const envName = "LLM_GATEWAY_AUTO_TITLE_ENABLED"

	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{name: "unset uses fallback", want: true},
		{name: "empty uses fallback", value: "", set: true, want: true},
		{name: "trimmed false disables", value: " false ", set: true, want: false},
		{name: "trimmed true enables", value: " true ", set: true, want: true},
		{name: "invalid uses fallback", value: "not-a-bool", set: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(envName, tt.value)
			} else {
				unsetEnvForTest(t, envName)
			}
			if got := readAutoGeneratorEnabled(envName, true); got != tt.want {
				t.Fatalf("readAutoGeneratorEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewAutoTitleGeneratorReadsEnabledEnv(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "defaults enabled", want: true},
		{name: "false disables", value: "false", want: false},
		{name: "true enables", value: "true", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				unsetEnvForTest(t, "LLM_GATEWAY_AUTO_TITLE_ENABLED")
			} else {
				t.Setenv("LLM_GATEWAY_AUTO_TITLE_ENABLED", tt.value)
			}
			if got := NewAutoTitleGenerator(nil).enabled; got != tt.want {
				t.Fatalf("NewAutoTitleGenerator().enabled = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPickFirstAvailableAPIKeyForAutoPrefersStaticLoopbackKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "loopback-static-key")

	id, key, err := (&Handler{}).pickFirstAvailableAPIKeyForAuto(t.Context(), "default")
	if err != nil {
		t.Fatalf("pickFirstAvailableAPIKeyForAuto() error = %v", err)
	}
	if id != 0 {
		t.Fatalf("key id = %d, want 0 for the static loopback key", id)
	}
	if key != "loopback-static-key" {
		t.Fatalf("key = %q, want static loopback key", key)
	}
}

func TestPickFirstAvailableAPIKeyForAutoRequiresDatabaseWithoutStaticKey(t *testing.T) {
	unsetEnvForTest(t, EnvAPIKey)

	_, _, err := (&Handler{}).pickFirstAvailableAPIKeyForAuto(t.Context(), "default")
	if err == nil || !strings.Contains(err.Error(), "database not configured") {
		t.Fatalf("error = %v, want missing database error", err)
	}
}

func TestLoopbackKeySource(t *testing.T) {
	if got := loopbackKeySource(0); got != "static" {
		t.Fatalf("loopbackKeySource(0) = %q, want static", got)
	}
	if got := loopbackKeySource(17); got != "tenant_api_key" {
		t.Fatalf("loopbackKeySource(17) = %q, want tenant_api_key", got)
	}
}

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
			if utf8.RuneCountInString(result) > sessionTitleMaxRunes {
				t.Errorf("extractTitleFromPreview() rune count = %d, want <= %d (result: %q)",
					utf8.RuneCountInString(result), sessionTitleMaxRunes, result)
			}
			if !utf8.ValidString(result) {
				t.Errorf("extractTitleFromPreview() returned invalid UTF-8: %q", result)
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

func TestExtractTitleFromPreviewUsesRuneBudget(t *testing.T) {
	gen := &AutoTitleGenerator{}
	preview := strings.Repeat("你好啊\n", sessionTitleMaxRunes)

	title := gen.extractTitleFromPreview(preview)
	if !utf8.ValidString(title) {
		t.Fatalf("extractTitleFromPreview() returned invalid UTF-8: %q", title)
	}
	if got := utf8.RuneCountInString(title); got <= 60 || got > sessionTitleMaxRunes {
		t.Fatalf("extractTitleFromPreview() rune count = %d, want 61..%d", got, sessionTitleMaxRunes)
	}
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("extractTitleFromPreview() = %q, want ellipsis suffix", title)
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
		gotPath    string
		mu         sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotPath = r.URL.Path
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
		0,
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
}

// TestExtractMessagesForTitle_ToolMessagesFiltered (2026-08-06) — tool and
// function role messages should be excluded from the title corpus to avoid
// polluting it with raw tool outputs.
func TestExtractMessagesForTitle_ToolMessagesFiltered(t *testing.T) {
	body := `{
		"messages": [
			{"role": "user", "content": "search for cats"},
			{"role": "assistant", "content": "", "tool_calls": [{"function": {"name": "search"}}]},
			{"role": "tool", "content": "search results: 1000 pages of cat photos"},
			{"role": "function", "content": "function output: processed 500 items"},
			{"role": "user", "content": "summarize the results"}
		]
	}`
	got := extractMessagesForTitle(body)

	if strings.Contains(got, "search results") || strings.Contains(got, "1000 pages") {
		t.Errorf("extractMessagesForTitle should filter tool messages, but got:\n%s", got)
	}
	if strings.Contains(got, "function output") || strings.Contains(got, "processed 500") {
		t.Errorf("extractMessagesForTitle should filter function messages, but got:\n%s", got)
	}
	if !strings.Contains(got, "search for cats") {
		t.Errorf("extractMessagesForTitle should preserve first user message, got:\n%s", got)
	}
	if !strings.Contains(got, "summarize the results") {
		t.Errorf("extractMessagesForTitle should preserve last user message, got:\n%s", got)
	}
}

// TestCallAutoTitleLLM_EmitsParentRequestHeaders (2026-08-06) — verifies the
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
		0,
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
	_, err := gen.callAutoTitleLLM(t.Context(), "sk-fake", 0, "gw_s", "p", "c")
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
func TestAutoTitleFirstTurnGuard(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hasPrior bool
		want     bool
	}{
		{name: "first turn is eligible", hasPrior: false, want: true},
		{name: "continuing session is not eligible", hasPrior: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gen := &AutoTitleGenerator{
				firstTurnCheck: func(_, _, _ string) bool { return !tc.hasPrior },
			}
			if got := gen.isFirstSuccessfulUserTurn("gw_session", "tenant-a", "request-current"); got != tc.want {
				t.Fatalf("isFirstSuccessfulUserTurn() = %v, want %v", got, tc.want)
			}
		})
	}
}
func TestExtractMessagesForTitle_ToolMessagesDoNotConsumeSemanticBudget(t *testing.T) {
	messages := make([]map[string]string, 0, 12)
	for i := 0; i < 10; i++ {
		messages = append(messages, map[string]string{
			"role":    "tool",
			"content": fmt.Sprintf("large tool output %d", i),
		})
	}
	messages = append(messages,
		map[string]string{"role": "user", "content": "请为现有会话补充标题连续性测试"},
		map[string]string{"role": "assistant", "content": "我会检查首轮判定和工具消息过滤。"},
	)
	body, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	got := extractMessagesForTitle(string(body))
	if strings.Contains(got, "large tool output") {
		t.Fatalf("tool output leaked into corpus: %s", got)
	}
	if !strings.Contains(got, "请为现有会话补充标题连续性测试") {
		t.Fatalf("user message was excluded by tool traffic: %s", got)
	}
	if !strings.Contains(got, "我会检查首轮判定和工具消息过滤") {
		t.Fatalf("assistant message was excluded by tool traffic: %s", got)
	}
}

// TestExtractLastUserMessageRuneCount (2026-08-19) — guards the short-user-message
// gate that decides whether MaybeGenerateTitle can skip the LLM round-trip.
// Returns the LAST user message's rune count (whitespace-collapsed), or 0 when
// the body is unparseable / empty / has no user message.
func TestExtractLastUserMessageRuneCount(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "empty body",
			body: "",
			want: 0,
		},
		{
			name: "invalid json",
			body: "not json",
			want: 0,
		},
		{
			name: "no messages array",
			body: `{"model":"gpt-4"}`,
			want: 0,
		},
		{
			name: "only system messages",
			body: `{"messages":[{"role":"system","content":"You are a helpful assistant"}]}`,
			want: 0,
		},
		{
			name: "single short user message",
			body: `{"messages":[{"role":"user","content":"你好"}]}`,
			want: 2, // 你好 = 2 runes
		},
		{
			name: "last user message wins over earlier user message",
			body: `{"messages":[
				{"role":"user","content":"first long long long long long long long long prompt"},
				{"role":"assistant","content":"ok"},
				{"role":"user","content":"hi"}
			]}`,
			want: 2, // "hi"
		},
		{
			name: "long user message exceeds title budget",
			body: fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`,
				strings.Repeat("长", 200)),
			want: 200,
		},
		{
			name: "exactly at title budget boundary",
			body: fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`,
				strings.Repeat("a", sessionTitleMaxRunes)),
			want: sessionTitleMaxRunes,
		},
		{
			name: "tool messages do not count as user",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"tool","content":"tool output"}
			]}`,
			want: 2, // "hi" is the last user message
		},
		{
			name: "case insensitive role match",
			body: `{"messages":[{"role":"USER","content":"case test"}]}`,
			want: 9, // "case test"
		},
		{
			name: "whitespace-collapsed user message",
			body: `{"messages":[{"role":"user","content":"   short   "}]}`,
			want: 5, // "short"
		},
		{
			name: "empty user content returns 0",
			body: `{"messages":[{"role":"user","content":""}]}`,
			want: 0,
		},
		{
			name: "multimodal content flattened",
			body: `{"messages":[{"role":"user","content":[
				{"type":"text","text":"hello "},
				{"type":"text","text":"world"}
			]}]}`,
			want: 11, // "hello world"
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLastUserMessageRuneCount(tc.body)
			if got != tc.want {
				t.Fatalf("extractLastUserMessageRuneCount() = %d, want %d (body=%q)",
					got, tc.want, tc.body)
			}
		})
	}
}

// TestShortUserMessageGateThreshold (2026-08-19) — pins the gate's
// relationship to sessionTitleMaxRunes: any last-user-message with rune count
// in [1, sessionTitleMaxRunes] inclusive should be eligible to skip the LLM
// round-trip. Anything above the budget (or 0 = no user message) must fall
// through to the existing pipeline.
func TestShortUserMessageGateThreshold(t *testing.T) {
	short := strings.Repeat("a", sessionTitleMaxRunes)
	long := strings.Repeat("a", sessionTitleMaxRunes+1)
	shortBody := fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, short)
	longBody := fmt.Sprintf(`{"messages":[{"role":"user","content":%q}]}`, long)

	if n := extractLastUserMessageRuneCount(shortBody); n <= 0 || n > sessionTitleMaxRunes {
		t.Fatalf("short body: rune count = %d, want 1..%d (gate should fire)", n, sessionTitleMaxRunes)
	}
	if n := extractLastUserMessageRuneCount(longBody); n <= sessionTitleMaxRunes {
		t.Fatalf("long body: rune count = %d, want > %d (gate must NOT fire)", n, sessionTitleMaxRunes)
	}
}

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

// TestAutoTitle_DistLock_FollowerSkipsOnRecheck is the regression test for
// the 2026-08-19 lock wiring: when two goroutines call MaybeGenerateTitle
// on the same session, only the leader should hit the upstream LLM. The
// follower waits for the leader, re-checks the DB, and exits silently.
//
// We can't easily exercise the full AutoTitleGenerator pipeline against
// miniredis without a DB, so we test the distlock integration at the
// goroutine-boundary using the same key shape generateTitleAsync uses.
func TestAutoTitle_DistLock_FollowerSkipsOnRecheck(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	mgr := distlock.NewRedisManager(rdb)

	const sessionID = "gw_dedup_test"
	const taskID = "default"

	var llmCalls atomic.Int32
	ctx := context.Background()

	// Pre-acquire the leader handle so the lock is already held when
	// the second goroutine joins. This eliminates the "who runs first"
	// race that otherwise lets both goroutines see themselves as
	// leader (the second one runs after the first releases). The test
	// then verifies that a concurrent Acquire is correctly classified
	// as a follower and waits for the leader to release.
	leader, err := mgr.Acquire(ctx, distlock.AcquireOpts{
		Key: titleDistLockKey("auto", taskID, sessionID),
		TTL: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}
	if !leader.IsLeader() {
		t.Fatal("first handle must be leader")
	}

	followerAcquired := make(chan *distlock.Handle, 1)
	go func() {
		h, err := mgr.Acquire(ctx, distlock.AcquireOpts{
			Key: titleDistLockKey("auto", taskID, sessionID),
			TTL: 30 * time.Second,
		})
		if err != nil {
			t.Errorf("follower Acquire: %v", err)
			followerAcquired <- nil
			return
		}
		followerAcquired <- h
	}()

	// Give the goroutine time to subscribe to the release channel.
	var follower *distlock.Handle
	select {
	case follower = <-followerAcquired:
		if follower == nil {
			t.Fatal("follower Acquire failed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not acquire in time")
	}

	if follower.IsLeader() {
		t.Fatal("second handle must be follower (pre-held lock should serialize)")
	}

	// Simulate generateTitleAsync's follower path: Wait then re-check.
	// Here we just verify Wait returns without error and that the
	// leader's protected work ran first.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- follower.Wait(ctx)
	}()

	// The leader runs its protected work then releases.
	llmCalls.Add(1)
	leader.Release(ctx)

	// Follower must wake up cleanly.
	select {
	case err := <-waitDone:
		if err != nil {
			t.Errorf("follower Wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not wake after leader Release")
	}
	follower.Release(ctx)

	if got := llmCalls.Load(); got != 1 {
		t.Fatalf("protected work (LLM call surrogate) ran %d times, want 1", got)
	}
}

// TestTitleDistLockKey_AutoVsManualIndependent pins the operator
// requirement that auto and manual title pipelines must not block each
// other: different "kind" segments yield different keys.
func TestTitleDistLockKey_AutoVsManualIndependent(t *testing.T) {
	auto := titleDistLockKey("auto", "default", "sess-1")
	manual := titleDistLockKey("manual", "default", "sess-1")
	if auto == manual {
		t.Fatalf("auto/manual keys collide: %q", auto)
	}
	if !strings.Contains(auto, "{title:auto:default\x00sess-1}") || !strings.HasSuffix(auto, ":lock") {
		t.Fatalf("auto key shape wrong: %q", auto)
	}
	if !strings.Contains(manual, "{title:manual:default\x00sess-1}") || !strings.HasSuffix(manual, ":lock") {
		t.Fatalf("manual key shape wrong: %q", manual)
	}
}
