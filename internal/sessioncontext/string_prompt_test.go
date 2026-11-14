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

// TestReduce_StringPromptWrapping — P2-2（2026-11-14 部分修复）：/v1/completions
// prompt 与 /v1/responses input 的 string 形态（协议合法）此前触发
// ErrProtectedContext（只认数组）。
//
// 当前实现：小 string prompt (≤maxBytes) 包装返回；**大 string prompt
// 暂不支持缩减**（返回明确错误，移交下一轮实现内存缩减逻辑）。
//
// 本测试验证大 string prompt 的错误提示（而非成功缩减）。
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

	// Reduce 应该返回明确错误（大 string prompt 缩减未实现）
	maxBytes := 500000 // 500KB 预算
	_, _, err = Reduce(ctx, store, ref, maxBytes)
	if err == nil {
		t.Fatal("Reduce succeeded on large string prompt (expected 'wrap + reduce not yet implemented' error)")
	}
	if !strings.Contains(err.Error(), "wrap + reduce not yet implemented") {
		t.Errorf("unexpected error: %v (expected 'wrap + reduce not yet implemented')", err)
	}
	t.Logf("large string prompt correctly rejected: %v", err)
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
// 大 input 暂不支持缩减（与 prompt 同理）。
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
	_, _, err := Reduce(ctx, store, ref, maxBytes)
	if err == nil {
		t.Fatal("Reduce succeeded on large string input (expected error)")
	}
	if !strings.Contains(err.Error(), "wrap + reduce not yet implemented") {
		t.Errorf("unexpected error: %v", err)
	}
	t.Logf("large string input correctly rejected: %v", err)
}
