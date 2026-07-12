package sessionforensics_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

// TestMutate_AllKinds_NoPanic 不做断言，只确保 7 个 mutation kind
// 都能跑通而不 panic。这是更复杂的语义测试的前置检查。
func TestMutate_AllKinds_NoPanic(t *testing.T) {
	kinds := []struct {
		name string
		kind sessionforensics.MutationKind
		at   int
		ext  string
	}{
		{"M1_swap_at_5", sessionforensics.MKindModelSwap, 5, "gpt-4o"},
		{"M2_tool_at_3", sessionforensics.MKindToolTruncated, 3, ""},
		{"M3_cut_at_4", sessionforensics.MKindCutAndAppend, 4, "请总结一下"},
		{"M4_thinking_at_5", sessionforensics.MKindThinkingInject, 5, ""},
		{"M5_vision_at_6", sessionforensics.MKindVisionContent, 6, ""},
		{"M6_long_prompt", sessionforensics.MKindLongSystemPrompt, 0, "30000"},
		{"M7_empty_at_4", sessionforensics.MKindEmptyMessagesAt, 4, ""},
	}

	// 构造一个简单的 10-turn SessionPack（OpenAI Chat Completions 风格）。
	pack := buildMockPack(t, 10)
	for _, kc := range kinds {
		t.Run(kc.name, func(t *testing.T) {
			p2 := deepCopyPack(t, pack)
			err := sessionforensics.Mutate(p2, sessionforensics.Mutation{
				Kind:   kc.kind,
				AtTurn: kc.at,
				Extra:  kc.ext,
			})
			if err != nil {
				t.Fatalf("mutate %s: %v", kc.kind, err)
			}
			if len(p2.Messages) == 0 && kc.kind != sessionforensics.MKindCutAndAppend {
				t.Errorf("messages cleared unexpectedly")
			}
		})
	}
}

// TestMutate_NilPack_ReturnsError
func TestMutate_NilPack(t *testing.T) {
	err := sessionforensics.Mutate(nil, sessionforensics.Mutation{})
	if err == nil {
		t.Error("expected error for nil pack")
	}
}

// TestMutate_InvalidTurn 返回 ErrInvalidTurn
func TestMutate_InvalidTurn(t *testing.T) {
	pack := buildMockPack(t, 5)
	err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindModelSwap,
		AtTurn: 999,
	})
	if err == nil {
		t.Error("expected out-of-range error")
	}
}

// TestMutate_M1_ModelSwap 验证 model 字段被替换。
func TestMutate_M1_ModelSwap(t *testing.T) {
	pack := buildMockPack(t, 5)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindModelSwap,
		AtTurn: 3,
		Extra:  "claude-sonnet-5",
	}); err != nil {
		t.Fatal(err)
	}
	// turn 1-2 保持 gpt-4o；turn 3-5 应改成 claude-sonnet-5
	for i, m := range pack.Messages {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal([]byte(m.Content), &body)
		want := "gpt-4o"
		if i >= 2 {
			want = "claude-sonnet-5"
		}
		if body.Model != want {
			t.Errorf("turn %d model = %q, want %q", i+1, body.Model, want)
		}
	}
}

func TestMutate_GlobalModelSwapStartsAtFirstTurn(t *testing.T) {
	pack := buildMockPack(t, 3)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind: sessionforensics.MKindModelSwap, Extra: "claude-sonnet-5",
	}); err != nil {
		t.Fatal(err)
	}
	for _, message := range pack.Messages {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(message.Content), &body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "claude-sonnet-5" {
			t.Fatalf("turn %d model = %q", message.Turn, body.Model)
		}
	}
}

// TestMutate_M2_ToolTruncated 验证 tools 字段被删。
func TestMutate_M2_ToolTruncated(t *testing.T) {
	pack := buildMockPackWithTools(t, 5)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindToolTruncated,
		AtTurn: 3,
	}); err != nil {
		t.Fatal(err)
	}
	for i, m := range pack.Messages {
		var body map[string]any
		_ = json.Unmarshal([]byte(m.Content), &body)
		_, hasTools := body["tools"]
		if i < 2 && !hasTools {
			t.Errorf("turn %d should still have tools", i+1)
		}
		if i >= 2 && hasTools {
			t.Errorf("turn %d tools should be removed", i+1)
		}
	}
}

// TestMutate_M3_CutAndAppend 验证 M3 后 turn 数等于原 -2 + 2。
func TestMutate_M3_CutAndAppend(t *testing.T) {
	pack := buildMockPack(t, 10)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindCutAndAppend,
		AtTurn: 5,
		Extra:  "new question",
	}); err != nil {
		t.Fatal(err)
	}
	// 原 10 turn → 8 (cut 2) + 2 (append) = 10
	if len(pack.Messages) != 10 {
		t.Errorf("expected 10 turns after M3, got %d", len(pack.Messages))
	}
	// index 4 = userTurn with "new question"，index 5 = assistantTurn with "好的"
	var userMsg struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal([]byte(pack.Messages[4].Content), &userMsg)
	foundNewQ := false
	for _, m := range userMsg.Messages {
		if m.Content == "new question" {
			foundNewQ = true
		}
	}
	if !foundNewQ {
		t.Errorf("index 4 (user turn after cut+append) should contain \"new question\"")
	}
}

// TestMutate_M4_ThinkingInject 不 panic，且 msg 内容仍是合法 JSON。
func TestMutate_M4_ThinkingInject(t *testing.T) {
	pack := buildMockPack(t, 5)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindThinkingInject,
		AtTurn: 3,
	}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(pack.Messages[2].Content), &body); err != nil {
		t.Fatalf("invalid JSON after mutation: %v", err)
	}
	if len(body.Messages) == 0 {
		t.Errorf("expected messages after thinking inject")
	}
}

// TestMutate_M5_VisionContent 把 user content 改成 vision 数组。
func TestMutate_M5_VisionContent(t *testing.T) {
	pack := buildMockPack(t, 5)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindVisionContent,
		AtTurn: 1, // 全局生效
	}); err != nil {
		t.Fatal(err)
	}
	for i, m := range pack.Messages {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal([]byte(m.Content), &body)
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				var arr []map[string]any
				if err := json.Unmarshal(msg.Content, &arr); err != nil {
					t.Errorf("turn %d user content is not vision array: %v", i+1, err)
				} else {
					// 应当包含 text + image_url
					foundImage := false
					for _, c := range arr {
						if c["type"] == "image_url" {
							foundImage = true
						}
					}
					if !foundImage {
						t.Errorf("turn %d user content has no image_url", i+1)
					}
				}
			}
		}
	}
}

// TestMutate_M6_LongSystemPrompt 把 system prompt 拉长到 ~50KB。
func TestMutate_M6_LongSystemPrompt(t *testing.T) {
	pack := buildMockPack(t, 3)
	origSize := len(pack.Messages[0].Content)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:  sessionforensics.MKindLongSystemPrompt,
		Extra: "50000",
	}); err != nil {
		t.Fatal(err)
	}
	newSize := len(pack.Messages[0].Content)
	if newSize < origSize+30_000 {
		t.Errorf("system prompt not expanded: orig=%d new=%d", origSize, newSize)
	}
}

// TestMutate_M7_EmptyMessages 把指定 turn messages 设为空。
func TestMutate_M7_EmptyMessages(t *testing.T) {
	pack := buildMockPack(t, 5)
	if err := sessionforensics.Mutate(pack, sessionforensics.Mutation{
		Kind:   sessionforensics.MKindEmptyMessagesAt,
		AtTurn: 3,
	}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte(pack.Messages[2].Content), &body)
	if len(body.Messages) != 0 {
		t.Errorf("turn 3 messages should be empty, got %d", len(body.Messages))
	}
}

// ── Scenario: baseline vs mutated replay reports shouldn't panic ────────
//
// 跑 baseline + 所有 mutation 后回放，验证 SessionCompressor.Prepare 不 panic。
// 这里用 in-memory L1 cache，每条 mutation 重置 cache。

func TestScenario_MutatedReplay_DoesNotPanic(t *testing.T) {
	pack := buildMockPack(t, 6)

	mu := []sessionforensics.MutationKind{
		sessionforensics.MKindModelSwap,
		sessionforensics.MKindToolTruncated,
		sessionforensics.MKindCutAndAppend,
		sessionforensics.MKindThinkingInject,
		sessionforensics.MKindVisionContent,
		sessionforensics.MKindLongSystemPrompt,
		sessionforensics.MKindEmptyMessagesAt,
	}
	for _, kind := range mu {
		t.Run(string(kind), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("replay panicked for %s: %v", kind, r)
				}
			}()
			packC := deepCopyPack(t, pack)
			if err := sessionforensics.Mutate(packC, sessionforensics.Mutation{
				Kind:   kind,
				AtTurn: 1,
			}); err != nil {
				t.Fatalf("mutate: %v", err)
			}
			// 跑回放
			ctx := context.Background()
			rp := sessionforensics.NewReplayer()
			rp.Replay(ctx, packC, sessionforensics.ReplayOptions{
				TenantID: "default", ContextWindow: 128_000,
			})
		})
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func buildMockPack(t *testing.T, n int) *sessionforensics.SessionPack {
	t.Helper()
	msgs := make([]sessionforensics.ExportMessage, 0, n)
	for i := 1; i <= n; i++ {
		msgs = append(msgs, sessionforensics.ExportMessage{
			Turn: i, Role: "user",
			Content: mustBuildChatBody(i, false),
		})
	}
	return &sessionforensics.SessionPack{
		SessionMeta: sessionforensics.SessionMeta{ID: "gxw_mock_001"},
		Messages:    msgs,
	}
}

func buildMockPackWithTools(t *testing.T, n int) *sessionforensics.SessionPack {
	t.Helper()
	msgs := make([]sessionforensics.ExportMessage, 0, n)
	for i := 1; i <= n; i++ {
		msgs = append(msgs, sessionforensics.ExportMessage{
			Turn: i, Role: "user",
			Content: mustBuildChatBody(i, true),
		})
	}
	return &sessionforensics.SessionPack{
		SessionMeta: sessionforensics.SessionMeta{ID: "gxw_mock_tools_001"},
		Messages:    msgs,
	}
}

func mustBuildChatBody(turn int, withTools bool) string {
	body := map[string]any{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a helpful assistant"},
			{"role": "user", "content": "Q for turn " + itoa(turn)},
			{"role": "assistant", "content": "A for turn " + itoa(turn)},
		},
	}
	if withTools {
		body["tools"] = []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "a", "parameters": map[string]any{"type": "object"}},
		}}
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	const digit = "0123456789"
	out := ""
	for n > 0 {
		out = string(digit[n%10]) + out
		n /= 10
	}
	return out
}

func deepCopyPack(t *testing.T, p *sessionforensics.SessionPack) *sessionforensics.SessionPack {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out sessionforensics.SessionPack
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}
