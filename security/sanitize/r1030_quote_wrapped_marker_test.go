package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// 2026-10-14 — r1030：模型把占位符「加引号改写」后回显，还原不得失败。
//
// 实测（本地网关 :8782，MiniMax-M3 经 minimax，/v1/responses 流式，
// raw-logs request 42cbe78a4fa8c2db22005c759e42a97a）：
// 模型在 run_ssh_command 参数里回显占位符时，password 值被多包了一层
// 转义引号——
//
//	"password": "{\"SENSITIVE:secret:1\"}", "username": "{SENSITIVE:account:1}"
//
// loose 文法不接受花括号内侧的引号字节 → 还原 miss → 输出合规层看到
// 「非占位符的密码样值」→ 整流 output_policy_blocked（客户端拿到
// response.failed，tool call 丢失）。username（干净形式）本可还原，
// password（引号包裹形式）结构性不可能还原——同一响应内两种命运。
//
// 修复：span 解析与流式前缀扣留容忍花括号内侧的一层引号包裹
// （`"`、`'`、`\"`）；配套超长释放阀防止 `{"` 起头的普通长 JSON 字符串
// 被误遮（r1020 惰性内容工具的 Write 参数主体就是这种形态）。
//
// 判据纪律：帧序列取自真实捕获（判据纪律第 6 条），配前提在场断言
// （第 3 条）——未映射的引号包裹 marker 必须仍 fail-closed，普通 JSON
// 不得被误判为 marker。

func clamp120(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

func r1030InterceptorWithMap(t *testing.T, session string, sm SanitizeMap) (*SanitizeRestoreInterceptor, *response.StreamMeta) {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	it, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	meta := &response.StreamMeta{
		SessionID: session,
		RequestID: session,
		State:     response.NewStreamState(),
	}
	return it, meta
}

// r1030FeedArgsDeltas 按序喂 response.function_call_arguments.delta 帧，
// 返回每帧实际下发的 delta 文本（nil 结果 = 原样透传）。
func r1030FeedArgsDeltas(t *testing.T, it *SanitizeRestoreInterceptor, meta *response.StreamMeta, ctx context.Context, deltas []string) []string {
	t.Helper()
	var emitted []string
	for i, d := range deltas {
		payload, err := json.Marshal(map[string]any{
			"type":         "response.function_call_arguments.delta",
			"item_id":      "toolu_r1030",
			"output_index": 0,
			"delta":        d,
		})
		if err != nil {
			t.Fatalf("marshal frame %d: %v", i, err)
		}
		frame := append(append([]byte("data: "), payload...), '\n', '\n')
		result, err := it.InterceptStreamChunk(ctx, frame, meta)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if result == nil {
			emitted = append(emitted, d)
			continue
		}
		if result.ShouldBlock {
			t.Fatalf("frame %d: unexpectedly blocked", i)
		}
		var decoded map[string]any
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(result.ModifiedChunk)), "data: "))
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("frame %d output not JSON: %v (%q)", i, err, string(result.ModifiedChunk))
		}
		out, _ := decoded["delta"].(string)
		emitted = append(emitted, out)
	}
	return emitted
}

// TestR1030LiveCapturedQuoteWrappedMarkerRestores — 真实捕获帧序列回放：
// password 为引号包裹 marker，username 为干净 marker，两者都必须还原成
// 真值，拼接后的参数必须仍是合法 JSON。
func TestR1030LiveCapturedQuoteWrappedMarkerRestores(t *testing.T) {
	sm := SanitizeMap{
		"{SENSITIVE:secret:1}":  "Xk9#mZ2pQ8",
		"{SENSITIVE:account:1}": "deploy",
		"{SENSITIVE:account:2}": "gw_admin",
		"{SENSITIVE:secret:2}":  "Pr0d-Gw#2026",
	}
	it, meta := r1030InterceptorWithMap(t, "r1030-live", sm)
	ctx := WithSanitizeMap(context.Background(), sm)

	// raw-logs 42cbe78a 的 arguments delta 序列（原样转录）。
	deltas := []string{
		`{"command": "git status -sb && git log --oneline -3", "host": "10.24`,
		`.7.91"`,
		`, "password": "{\"SENSITIVE:secret:`,
		`1\"}", "username": "{`,
		`SENSITIVE:account:1}"`,
		`}`,
	}
	emitted := r1030FeedArgsDeltas(t, it, meta, ctx, deltas)
	joined := strings.Join(emitted, "")

	if !json.Valid([]byte(joined)) {
		t.Fatalf("accumulated tool args are invalid JSON after restore: %q", joined)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(joined), &parsed); err != nil {
		t.Fatalf("accumulated tool args do not parse: %v (%q)", err, joined)
	}
	if pw, _ := parsed["password"].(string); pw != "Xk9#mZ2pQ8" {
		t.Fatalf("quote-wrapped password marker not restored: %q (joined=%q)", pw, joined)
	}
	if user, _ := parsed["username"].(string); user != "deploy" {
		t.Fatalf("username marker not restored: %q (joined=%q)", user, joined)
	}
	if strings.Contains(joined, "SENSITIVE") {
		t.Fatalf("reserved marker word leaked to the client: %q", joined)
	}
}

// TestR1030ParseLoosePlaceholderToleratesQuoteWrap — 引号包裹层的三种拼写
// （裸引号/转义引号/单引号）都能解析出 (type,index)。
func TestR1030ParseLoosePlaceholderToleratesQuoteWrap(t *testing.T) {
	cases := []struct {
		candidate string
		typ       SensitiveType
		index     int
	}{
		{`{SENSITIVE:secret:1}`, "secret", 1},
		{`{"SENSITIVE:secret:1"}`, "secret", 1},
		{`{\"SENSITIVE:secret:1\"}`, "secret", 1},
		{`{'SENSITIVE:account:2'}`, "account", 2},
		{`{ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１}`, "secret", 1},
	}
	for _, c := range cases {
		typ, index, ok := parseLoosePlaceholder(c.candidate)
		if !ok || typ != c.typ || index != c.index {
			t.Fatalf("parseLoosePlaceholder(%q) = (%v,%d,%v), want (%v,%d,true)",
				c.candidate, typ, index, ok, c.typ, c.index)
		}
	}
}

// TestR1030OrdinaryJSONIsNotAMarker — 前提在场（反方向）：普通 JSON 的
// 花括号+引号形态不得因引号容忍而被误判为 marker。
func TestR1030OrdinaryJSONIsNotAMarker(t *testing.T) {
	for _, text := range []string{
		`{"a":1}`,
		`{"key": "value"}`,
		`{"sensitive": "not-a-marker"}`,
		`{"SENSITIVE_notes": true}`,
	} {
		if _, _, ok := parseLoosePlaceholder(text); ok {
			t.Fatalf("ordinary JSON %q parsed as marker", text)
		}
		if looksLikeMarkerSpan(text) {
			t.Fatalf("ordinary JSON %q classified as marker-shaped span", text)
		}
	}
}

// TestR1030UnmappedQuoteWrappedStillFailsClosed — 未映射的引号包裹 marker
// 必须被遮蔽而非原样透出（fail-closed 契约保持）。
func TestR1030UnmappedQuoteWrappedStillFailsClosed(t *testing.T) {
	it, meta := r1030InterceptorWithMap(t, "r1030-unmapped", SanitizeMap{})
	ctx := context.Background()
	deltas := []string{
		`{"password": "{\"SENSITIVE:secret:9`,
		`\"}"}`,
	}
	emitted := r1030FeedArgsDeltas(t, it, meta, ctx, deltas)
	joined := strings.Join(emitted, "")
	if strings.Contains(joined, "SENSITIVE") {
		t.Fatalf("unmapped quote-wrapped marker leaked verbatim: %q", joined)
	}
	if strings.Contains(joined, "\"password\": \"\"") {
		t.Fatalf("password value vanished entirely instead of being masked: %q", joined)
	}
}

// TestR1030LongQuoteRunNotRedacted — 释放阀：被扣留的 `{\"` 前缀以引号
// 跑列继续增长、超过 maxPlaceholderTailBytes 仍无闭括号时（marker 词从未
// 开始），必须原样释放而非 [REDACTED]。普通长文本不会走到这里——第二个
// 字节就会否定 marker 前缀——只有引号跑列能保持「语法未决」状态到超长。
func TestR1030LongQuoteRunNotRedacted(t *testing.T) {
	sm := SanitizeMap{"{SENSITIVE:secret:1}": "Xk9#mZ2pQ8"}
	it, meta := r1030InterceptorWithMap(t, "r1030-longquotes", sm)
	ctx := WithSanitizeMap(context.Background(), sm)

	quoteRun := strings.Repeat(`\"`, 130) // 260 字节，远超 maxPlaceholderTailBytes
	deltas := []string{
		`{"content": "{\"`,
		quoteRun,
		` done\"}"`,
	}
	emitted := r1030FeedArgsDeltas(t, it, meta, ctx, deltas)
	joined := strings.Join(emitted, "")
	if strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("quote-run tail was redacted by the hold path: %q", clamp120(joined))
	}
	if !strings.Contains(joined, quoteRun) {
		t.Fatalf("quote-run bytes not preserved verbatim: %q", clamp120(joined))
	}
}

// TestR1030ValidPrefixHoldsQuoteWrapArrival — 流式前缀扣留对引号包裹形态
// 的判定：`{"` 与 `{"SENS` 扣留、`{"abc` 放行。
func TestR1030ValidPrefixHoldsQuoteWrapArrival(t *testing.T) {
	cases := []struct {
		candidate string
		hold      bool
	}{
		{`{"`, true},
		{`{\"`, true},
		{`{"SENS`, true},
		{`{\"SENSITIVE:secret:`, true},
		{`{SENSITIVE:secret:1`, true},
		{`{"SENSITIVE:secret:1\"`, true},
		{`{"abc`, false},
		{`{"key": "val`, false},
		{`{"SENSITIVE:secret:1"}`, false}, // 已闭合：还原通道接管
	}
	for _, c := range cases {
		if got := validLoosePlaceholderPrefix(c.candidate); got != c.hold {
			t.Fatalf("validLoosePlaceholderPrefix(%q) = %v, want %v", c.candidate, got, c.hold)
		}
	}
}
