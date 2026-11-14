package sessioncontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

func newTestStore(t *testing.T) *largebody.Store {
	t.Helper()
	s, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestReduce_StringPromptWrapping — P2-2：/v1/completions prompt 的 string
// 形态（协议合法）此前触发 ErrProtectedContext（只认数组）。
//
// 2026-11-14 审计轮改判：原钉测把 interim 状态（大 string prompt 返回
// 「wrap + reduce not yet implemented」）钉为预期——与同批提交的
// TestReduceStringPromptFitsBudget（要求缩减成功）互相矛盾，且按
// r1051/r1052/r1053 族裁决，fail-closed 于可缩减内容=会话砖化（>1MiB 纯文本
// prompt 422 且客户端无法 split）。终态＝大 string prompt 走裸字符串摘录
// 缩减（string_field.go 机器），输出保持 prompt 为 JSON string。
func TestReduce_StringPromptWrapping(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// 场景：completions 的 string prompt（1.2MB 超 largeMemoryThreshold）
	longPrompt := strings.Repeat("This is a long prompt. ", 60000) // ~1.4MB
	body := map[string]any{
		"model":      "gpt-3.5-turbo-instruct",
		"prompt":     longPrompt,
		"max_tokens": 100,
	}
	bodyBytes, _ := json.Marshal(body)

	// 写入 store
	ref, err := store.Capture(ctx, largebody.Identity{
		TenantID:  "test-tenant",
		SessionID: "test-session-1",
		RequestID: "test-prompt-1",
	}, strings.NewReader(string(bodyBytes)))
	if err != nil {
		t.Fatalf("store.Capture failed: %v", err)
	}

	// Reduce 必须缩减成功（2026-11-14 审计轮收口）：预算内、prompt 保持
	// JSON string 原形、带披露标记。
	maxBytes := 500000 // 500KB 预算
	reduced, report, err := Reduce(ctx, store, ref, maxBytes)
	if err != nil {
		t.Fatalf("large string prompt must reduce, not reject: %v", err)
	}
	if !report.Reduced || len(reduced) > maxBytes {
		t.Fatalf("bad reduction: reduced=%v len=%d", report.Reduced, len(reduced))
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(reduced, &out) != nil {
		t.Fatal("output must stay an object")
	}
	var prompt string
	if json.Unmarshal(out["prompt"], &prompt) != nil {
		t.Fatalf("prompt must remain a JSON string, got %s", string(out["prompt"])[:min(40, len(out["prompt"]))])
	}
	if !strings.Contains(prompt, stringExcerptMarker[:40]) {
		t.Error("excerpt must carry the disclosure marker")
	}
	t.Logf("large string prompt reduced: %d→%d bytes", ref.Size, len(reduced))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestReduce_StringPromptSmall — P2-2 核心修复：小 string prompt（≤maxBytes）
// 包装为 messages 数组后直接返回（不走缩减）。
func TestReduce_StringPromptSmall(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	body := map[string]any{
		"model":      "gpt-3.5-turbo-instruct",
		"prompt":     "Short prompt",
		"max_tokens": 100,
	}
	bodyBytes, _ := json.Marshal(body)
	ref, _ := store.Capture(ctx, largebody.Identity{
		TenantID:  "test-tenant",
		SessionID: "test-session-2",
		RequestID: "test-prompt-2",
	}, strings.NewReader(string(bodyBytes)))

	maxBytes := 10000 // 足够大
	reduced, report, err := Reduce(ctx, store, ref, maxBytes)
	if err != nil {
		t.Fatalf("Reduce failed: %v", err)
	}

	// 小 body 应该包装后返回
	var out map[string]any
	json.Unmarshal(reduced, &out)
	if _, hasPrompt := out["prompt"]; hasPrompt {
		t.Error("small prompt still has prompt field (should be wrapped)")
	}
	msgsRaw, hasMessages := out["messages"]
	if !hasMessages {
		t.Fatal("small prompt missing messages field (wrap failed)")
	}
	
	// 验证 messages 是数组且有内容
	var messages []map[string]any
	msgsJSON, _ := json.Marshal(msgsRaw)
	if err := json.Unmarshal(msgsJSON, &messages); err != nil {
		t.Fatalf("messages is not an array: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages array has %d items (expected 1)", len(messages))
	}
	if role, _ := messages[0]["role"].(string); role != "user" {
		t.Errorf("wrapped message role=%q (expected 'user')", role)
	}
	if content, _ := messages[0]["content"].(string); content != "Short prompt" {
		t.Errorf("wrapped message content=%q (expected 'Short prompt')", content)
	}
	
	if report.Reduced {
		t.Error("small prompt should not be marked as Reduced (wrap-only, no reduction)")
	}
	t.Logf("small string prompt wrap-only: %d→%d bytes", ref.Size, len(reduced))
}

// TestReduce_ResponsesStringInput — P2-2 扩展：/v1/responses input string 形态。
// 2026-11-14 审计轮改判：与 prompt 同一收口——大 input 必须摘录缩减成功
// （保持 string 原形），不再返回 interim 报错。
func TestReduce_ResponsesStringInput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	longInput := strings.Repeat("Summarize this text. ", 60000) // ~1.3MB
	body := map[string]any{
		"model":             "gpt-4",
		"input":             longInput,
		"max_output_tokens": 1024,
	}
	bodyBytes, _ := json.Marshal(body)
	ref, _ := store.Capture(ctx, largebody.Identity{
		TenantID:  "test-tenant",
		SessionID: "test-session-3",
		RequestID: "test-input-1",
	}, strings.NewReader(string(bodyBytes)))

	maxBytes := 500000
	reduced, report, err := Reduce(ctx, store, ref, maxBytes)
	if err != nil {
		t.Fatalf("large string input must reduce, not reject: %v", err)
	}
	if !report.Reduced || len(reduced) > maxBytes {
		t.Fatalf("bad reduction: reduced=%v len=%d", report.Reduced, len(reduced))
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(reduced, &out) != nil {
		t.Fatal("output must stay an object")
	}
	var input string
	if json.Unmarshal(out["input"], &input) != nil {
		t.Fatal("input must remain a JSON string")
	}
	t.Logf("large string input reduced: %d→%d bytes", ref.Size, len(reduced))
}
