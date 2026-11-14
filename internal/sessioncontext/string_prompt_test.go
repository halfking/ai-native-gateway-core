package sessioncontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
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

// TestReduce_StringPromptSmall — P2-2 小 body 直通语义。
//
// 2026-11-14 审计收口改判：原钉测把「包装为 messages 数组后返回」钉为
// 预期——但包装改变请求形状，构成真实回归（证据见
// TestReduce_ResponsesSmallStringInputPassthrough /
// TestReduce_PromptEchoDoesNotClobberMessages 的取证注释）：
// ① responses 入站解析（ir.ParseResponses）只读 input 不读 messages，
//
//	包装让 256KB–1MiB 的 string input 在入口整段丢失（出站零内容）；
//
// ② 「prompt 回声 + 真 messages」并存形态（parse_openai 明文支持
//
//	messages 获胜）被回声覆盖（r1048 同族：改写层制造入口自己都不会
//	放的形状）。IR parse 对 string 形态原生统一（parse_openai.go /
//	parse_responses.go）⇒ 小 body 直通即语义等价，包装没有存在必要。
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

	// 小 body 直通：输出与输入逐字节相同（prompt 原样保留，不改形状）。
	if string(reduced) != string(bodyBytes) {
		t.Fatalf("small string prompt must pass through unchanged:\n got %s\nwant %s", reduced, bodyBytes)
	}
	if report.Reduced {
		t.Error("pass-through must not be marked as Reduced")
	}
	t.Logf("small string prompt pass-through: %d→%d bytes", ref.Size, len(reduced))
}

// TestReduce_ResponsesSmallStringInputPassthrough — P2-2 回归钉（Bug A）：
// /v1/responses + 256KB–1MiB string input 是生产在役区间（Reduce 在
// >256KB 阈值必被调用、ContextBytes 默认 1MiB）。中途版本的包装把 input
// 删成 messages，而 ir.ParseResponses 只读 input——实证输出 messages=0，
// 用户输入在入口整段丢失。钉定：直通 + 真实入站解析器看到完整 user 内容。
func TestReduce_ResponsesSmallStringInputPassthrough(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	longInput := strings.Repeat("Summarize this text. ", 15000) // ~300KB
	bodyBytes, _ := json.Marshal(map[string]any{
		"model": "gpt-6-astra",
		"input": longInput,
	})
	ref, err := store.Capture(ctx, largebody.Identity{
		TenantID:  "test-tenant",
		SessionID: "test-session-4",
		RequestID: "test-input-2",
	}, strings.NewReader(string(bodyBytes)))
	if err != nil {
		t.Fatalf("store.Capture failed: %v", err)
	}

	const contextBytes = 1 << 20 // 生产默认 ContextBytes
	out, _, err := Reduce(ctx, store, ref, contextBytes)
	if err != nil {
		t.Fatalf("Reduce failed: %v", err)
	}
	if string(out) != string(bodyBytes) {
		t.Fatal("small string input must pass through unchanged (wrap would destroy it at the responses entry parser)")
	}

	// 下游合同：Reduce 输出喂真实入站解析器必须看到完整 user 内容。
	req, perr := ir.ParseResponses(out)
	if perr != nil {
		t.Fatalf("ParseResponses failed on Reduce output: %v", perr)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("entry parser must see the user message, got %d messages", len(req.Messages))
	}
	got, _ := json.Marshal(req.Messages[0].Content)
	if !strings.Contains(string(got), "Summarize this text.") {
		t.Errorf("user content truncated at the entry parser: %.80s", got)
	}
}

// TestReduce_PromptEchoDoesNotClobberMessages — P2-2 回归钉（Bug B）：
// 「prompt 冗余回声 + 真 messages」并存是 parse_openai 明文支持的形态
// （messages 获胜、prompt 是冗余回声）。中途版本的包装无条件覆盖
// root["messages"]，真内容被回声销毁。钉定：messages 原样保留。
func TestReduce_PromptEchoDoesNotClobberMessages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	bodyBytes, _ := json.Marshal(map[string]any{
		"model":    "gpt-3.5-turbo-instruct",
		"prompt":   "legacy echo of the question",
		"messages": []map[string]any{{"role": "user", "content": "What is the capital of France?"}},
	})
	ref, err := store.Capture(ctx, largebody.Identity{
		TenantID:  "test-tenant",
		SessionID: "test-session-5",
		RequestID: "test-prompt-3",
	}, strings.NewReader(string(bodyBytes)))
	if err != nil {
		t.Fatalf("store.Capture failed: %v", err)
	}

	out, _, err := Reduce(ctx, store, ref, 1<<20)
	if err != nil {
		t.Fatalf("Reduce failed: %v", err)
	}
	if string(out) != string(bodyBytes) {
		t.Fatalf("both-fields body must pass through unchanged, got %s", out)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("not an object")
	}
	raw, _ := json.Marshal(m["messages"])
	if !strings.Contains(string(raw), "capital of France") {
		t.Fatalf("real messages content must survive, got %s", raw)
	}
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
