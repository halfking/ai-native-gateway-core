package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// 2026-10-15 — r1032b：chat 腿（grok-4.6 经 vapeur，native responses 上游 +
// responsesToChatStream 回桥）多轮会话的第 2/3 轮，上游参数帧完整
// （raw-logs 506f8a80/9bd6ccf5，password/username 为正确占位符），客户端
// 只收到工具名、零 arguments 字节——参数在还原拦截器里被吞。
// 本文件按原样转录两轮的客户端帧序列（role → name → args → finish → DONE），
// 断言 arguments 帧必须完整下发（占位符还原为真值，JSON 结构不变）。

func r1032bChatFrame(t *testing.T, delta any, finish any) []byte {
	t.Helper()
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != nil {
		choice["finish_reason"] = finish
	}
	payload := map[string]any{
		"id": "chatcmpl-r1032b", "object": "chat.completion.chunk", "created": 1792040029,
		"model": "grok-4.6", "choices": []any{choice},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(append([]byte("data: "), data...), '\n', '\n')
}

// r1032bFeedChatRound 按序喂一轮 chat 流，返回 arguments lane 实际下发的字节拼接。
func r1032bFeedChatRound(t *testing.T, it *SanitizeRestoreInterceptor, meta *response.StreamMeta, ctx context.Context, nameID, args string) string {
	t.Helper()
	frames := [][]byte{
		r1032bChatFrame(t, map[string]any{"role": "assistant"}, nil),
		r1032bChatFrame(t, map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": nameID, "type": "function",
			"function": map[string]any{"name": "run_ssh_command", "arguments": ""},
		}}}, nil),
		r1032bChatFrame(t, map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "function": map[string]any{"arguments": args},
		}}}, nil),
		r1032bChatFrame(t, map[string]any{}, "tool_calls"),
	}
	var emitted strings.Builder
	for i, f := range frames {
		result, err := it.InterceptStreamChunk(ctx, f, meta)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if result == nil {
			// 原样透传
			if i == 2 {
				emitted.WriteString(args)
			}
			continue
		}
		if result.ShouldBlock {
			t.Fatalf("frame %d: unexpectedly blocked (args=%q)", i, args)
		}
		if i != 2 {
			continue
		}
		var decoded map[string]any
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(result.ModifiedChunk)), "data: "))
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("args frame output not JSON: %v (%q)", err, string(result.ModifiedChunk))
		}
		choices, _ := decoded["choices"].([]any)
		if len(choices) == 0 {
			t.Fatalf("args frame lost choices: %q", string(result.ModifiedChunk))
		}
		ch, _ := choices[0].(map[string]any)
		d, _ := ch["delta"].(map[string]any)
		tcs, _ := d["tool_calls"].([]any)
		outArgs := ""
		for _, tcAny := range tcs {
			tc, _ := tcAny.(map[string]any)
			fn, _ := tc["function"].(map[string]any)
			if s, ok := fn["arguments"].(string); ok {
				outArgs += s
			}
		}
		emitted.WriteString(outArgs)
	}
	return emitted.String()
}

func TestR1032BGrokChatArgsRoundSurvivesRestore(t *testing.T) {
	sm := SanitizeMap{
		"{SENSITIVE:secret:1}":  "Xk9#mZ2pQ8",
		"{SENSITIVE:account:1}": "deploy",
	}
	// raw-logs 9bd6ccf5（round 1，线上客户端确实收到）与 506f8a80（round 2，
	// 线上客户端只收到名字）的 arguments 原样转录。
	round1 := `{"command":"echo HOME=$HOME; echo USER=$(whoami); ls -la ~; ls -la ~/workspace 2>/dev/null; ls -la /home 2>/dev/null; find / -name 'pocket-opencode' -type d 2>/dev/null | head -20","host":"10.24.7.91","password":"{SENSITIVE:secret:1}","username":"{SENSITIVE:account:1}"}`
	round2 := `{"command":"echo HOME=$HOME; echo USER=$(whoami); ls -la $HOME; ls -la $HOME/workspace 2>/dev/null; ls -la $HOME/workspace/pocket-opencode 2>/dev/null; git -C $HOME/workspace/pocket-opencode status 2>/dev/null; find / -maxdepth 5 -type d -name 'pocket-opencode' 2>/dev/null | head","host":"10.24.7.91","password":"{SENSITIVE:secret:1}","username":"{SENSITIVE:account:1}"}`

	it, meta := r1030InterceptorWithMap(t, "r1032b-grok", sm)
	ctx := WithSanitizeMap(context.Background(), sm)

	for i, args := range []string{round1, round2, round2} {
		got := r1032bFeedChatRound(t, it, meta, ctx, "call_r1032b_"+string(rune('a'+i)), args)
		if !strings.Contains(got, `"password":"Xk9#mZ2pQ8"`) {
			t.Fatalf("round %d: password not restored in emitted args: %q", i, got)
		}
		if !strings.Contains(got, `"username":"deploy"`) {
			t.Fatalf("round %d: username not restored in emitted args: %q", i, got)
		}
		if !strings.Contains(got, `"host":"10.24.7.91"`) {
			t.Fatalf("round %d: host literal missing from emitted args: %q", i, got)
		}
		if strings.Contains(got, "SENSITIVE") {
			t.Fatalf("round %d: marker leaked: %q", i, got)
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("round %d: emitted args invalid JSON: %q", i, got)
		}
	}
}
