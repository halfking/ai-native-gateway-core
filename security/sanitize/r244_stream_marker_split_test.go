package sanitize

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// 2026-10-10 — 流式占位符跨 chunk 断裂时的 marker 起始识别。
//
// 实测（154 生产，minimax-m3）：模型把 `{SENSITIVE:phone:1}` 改写成
// `(SENSITIVE:` 并跨 SSE chunk 下发。旧的 incompletePlaceholderStart 只从
// '{' 起算，于是这个残片被当作普通文本直接放行，续包（`phone:1}`）到达时
// 已无缓冲可接 —— 内部 token 就这样到达了客户端。
//
// 修复新增 parenMarkerStart。两条边界必须同时成立：
//   * 改写形态要被缓冲（否则泄漏）
//   * 普通圆括号散文不能被缓冲（否则流式输出会被无故扣住）

func TestIncompletePlaceholderStart(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantStart int
		wantOK    bool
	}{
		// 原有契约：'{' 形态不变
		{"canonical partial", " {SENSITIVE:phone:", 1, true},
		{"canonical mid word", "normal text then {SENSIT", 17, true},
		{"canonical with paren", "({SENSITIVE", 1, true},

		// 本次修复：'(' 改写形态必须被识别
		{"respelled open paren", " (SENSITIVE:", 1, true},
		{"respelled mid word", " (SENS", 1, true},
		{"respelled single char", " (s", 1, true},

		// 普通圆括号不得被缓冲
		{"prose paren word", "hello (world", 0, false},
		{"prose paren mid", "a(b)c", 0, false},
		{"prose chinese paren", "普通文本 (", 0, false},
		{"prose list marker", "see (1) below", 0, false},

		// 无 marker
		{"no marker", "no marker here at all", 0, false},
		{"empty", "", 0, false},
		// 完整 marker 已落地，不该被当作"仍在到达"而扣住
		{"complete marker", " {SENSITIVE:phone:1}", 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, ok := incompletePlaceholderStart(c.text)
			if ok != c.wantOK {
				t.Fatalf("incompletePlaceholderStart(%q) ok = %v, want %v", c.text, ok, c.wantOK)
			}
			if ok && start != c.wantStart {
				t.Errorf("incompletePlaceholderStart(%q) start = %d, want %d", c.text, start, c.wantStart)
			}
		})
	}
}

// TestParenMarkerStartDoesNotShadowBrace pins the precedence: '{' is the
// canonical spelling and must keep winning, so a body holding both openers
// still resolves at the brace.
func TestParenMarkerStartDoesNotShadowBrace(t *testing.T) {
	text := "value (note) and {SENSITIVE:phone:"
	start, ok := incompletePlaceholderStart(text)
	if !ok {
		t.Fatal("expected the trailing brace marker to be recognised")
	}
	if want := strings.LastIndexByte(text, '{'); start != want {
		t.Errorf("start = %d, want the brace at %d", start, want)
	}
}

// TestStreamRedactsGarbageContinuationAfterReservedMarker covers the gap the
// paren fix exposed, found only by running the real package suite.
//
// Frame 1 buffers "{SENSITIVE:phone:". Frame 2 appends "13800138000x tail",
// which does not complete the marker. restoreText then masks the marker word
// and returns "{[REDACTED]13800138000x tail" — text that differs from the
// input, so the old guard (`restored == combined`, which only fires when the
// restore pass leaves the text untouched) never ran and the half-masked token
// plus the continuation reached the client.
//
// The redacting branch must therefore key on "restoreText had to mask", not on
// "the text changed": the latter also fires on every legitimate split-across-
// -chunks restore, which must pass through untouched.
func TestStreamRedactsGarbageContinuationAfterReservedMarker(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	it, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	meta := &response.StreamMeta{
		SessionID: "r244-garbage-continuation",
		RequestID: "r244-garbage-continuation",
		State:     response.NewStreamState(),
	}

	first := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:\"}}]}\n\n")
	if _, err := it.InterceptStreamChunk(context.Background(), first, meta); err != nil {
		t.Fatalf("frame 1: %v", err)
	}

	garbage := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"13800138000x tail\"}}]}\n\n")
	result, err := it.InterceptStreamChunk(context.Background(), garbage, meta)
	if err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if result == nil {
		t.Fatal("expected the delta to be rewritten")
	}
	out := string(result.ModifiedChunk)
	if strings.Contains(out, "13800138000") {
		t.Errorf("garbage continuation reached the client: %s", out)
	}
	if strings.Contains(out, "{SENSITIVE:") {
		t.Errorf("reserved marker prefix leaked: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected a redaction, got: %s", out)
	}
	if result.ShouldBlock {
		t.Error("a malformed marker must not terminate the client stream")
	}
}
