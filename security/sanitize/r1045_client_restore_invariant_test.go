package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// enableOutputMaskingForTest 把 sanitize.output_mask_enabled 显式置 true
// （r1045 后默认 false：owner 裁决「客户端一级一定要还原」⇒ 客户端可见遮蔽
// 改写默认关闭）。钉「遮蔽行为」本身的既有测试（编码盲区对照、mask-and-restore
// 族）调用本helper 进入 opt-in 子模式；默认面由
// domains/hooks/outputcompliance 的 r1045 钉测负责。
func enableOutputMaskingForTest(t *testing.T) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	rows := map[string][]byte{"sanitize.output_mask_enabled": []byte("true")}
	r := settings.NewRegistry()
	for _, sp := range settings.ModuleSpecs() {
		r.MustRegisterSpec(sp)
	}
	r.RegisterBackend(settings.ScopePlatform, &r1045SettingsBackend{rows: rows})
	settings.Global = r
}

type r1045SettingsBackend struct {
	rows map[string][]byte
}

func (b *r1045SettingsBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	if v, ok := b.rows[key]; ok {
		return v, nil
	}
	return nil, nil
}

func (b *r1045SettingsBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func (b *r1045SettingsBackend) GetTenant(_ string, _ string) ([]byte, error) {
	return nil, nil
}

func (b *r1045SettingsBackend) SetTenant(_ string, _ string, _ any) ([]byte, error) {
	return nil, nil
}

// ──────────────────────────────────────────────────────────────────────────
// r1045 客户端还原不变量（2026-11-13 owner 裁决：「对于脱敏的信息在客户端一级
// 一定要还原，不然执行工具或显示内容都会出错」）。
//
// 不变量：凡网关在输入侧铸造过占位符的值（字典有条目），在**任何客户端可见
// lane**（chat 文本/工具参数/函数调用、anthropic tool input）与**降级条件**下
// （Redis 字典中途丢失——请求级 ctx 字典兜底）都必须以**原值**到达客户端——
// 既不允许裸占位符，也不允许 [REDACTED:*] 替代。
// ──────────────────────────────────────────────────────────────────────────

func r1045StreamMeta() *response.StreamMeta {
	return &response.StreamMeta{State: response.NewStreamState(), SessionID: "s-r1045-inv", TenantID: "t-r1045"}
}

func r1045SSE(t *testing.T, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte("data: "), raw...), '\n', '\n')
}

// assertNoMaskOrMarker 断言客户端字节里既无遮蔽标记也无裸占位符。
func assertNoMaskOrMarker(t *testing.T, clientBytes string) {
	t.Helper()
	if strings.Contains(clientBytes, "[REDACTED") {
		t.Fatalf("client bytes carry a mask: %q", clientBytes)
	}
	if strings.Contains(clientBytes, "{SENSITIVE:") {
		t.Fatalf("client bytes carry a raw placeholder: %q", clientBytes)
	}
}

// TestR1045_ChatStreamEveryLaneRestoresOriginalValue chat 流式全 lane：
// 文本增量、tool_calls.arguments（跨 chunk 撕开的占位符）、function_call。
func TestR1045_ChatStreamEveryLaneRestoresOriginalValue(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "t-r1045", "s-r1045-inv"
	secretValue := "hunter2secret"
	if err := rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:secret:1}", secretValue).Err(); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(NewPatternDetector())
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 请求级 ctx 字典（输入中间件在请求入口挂的形态）。
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:secret:1}": secretValue})
	meta := r1045StreamMeta()

	var client strings.Builder
	choice := func(delta map[string]any) []byte {
		return r1045SSE(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
	}
	frames := [][]byte{
		choice(map[string]any{"content": "the password is {SENSITIVE:secret:1}", "role": "assistant"}),
		// 工具参数：占位符被流式边界撕开（`{SENSITIVE:sec` / `ret:1}`）。
		choice(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{
			"name": "deploy", "arguments": `{"password":"{SENSITIVE:sec`}}}}),
		choice(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{
			"arguments": `ret:1}","host":"internal"}`}}}}),
		// function_call（legacy 形态：name/arguments 直接平铺，不套 function 层）。
		choice(map[string]any{"function_call": map[string]any{"name": "run", "arguments": `{"pw":"{SENSITIVE:secret:1}"}`}}),
		choice(map[string]any{"finish_reason": "tool_calls"}),
	}
	for i, frame := range frames {
		result, err := it.InterceptStreamChunk(ctx, frame, meta)
		if err != nil {
			t.Fatalf("frame %d err=%v", i, err)
		}
		if result != nil && result.ShouldBlock {
			t.Fatalf("frame %d blocked: %+v raw=%s", i, result, frame)
		}
		if result != nil && result.ModifiedChunk != nil {
			client.Write(result.ModifiedChunk)
		} else if result == nil {
			client.Write(frame)
		}
	}
	assembled := client.String()
	assertNoMaskOrMarker(t, assembled)
	if !strings.Contains(assembled, "the password is "+secretValue) {
		t.Fatalf("text lane not restored: %q", assembled)
	}
	// 工具参数：占位符被流式边界撕开时，还原值同样跨帧拼接——帧 1 以
	// `{"password":"` 收尾、帧 2 以原值开头。按拼接后的 arguments 判定。
	if !strings.Contains(assembled, `{\"password\":\"`) || !strings.Contains(assembled, secretValue+`\",\"host\":\"internal\"}`) {
		t.Fatalf("tool args not restored: %q", assembled)
	}
	if !strings.Contains(assembled, `\"pw\":\"`+secretValue+`\"`) {
		t.Fatalf("function_call args not restored: %q", assembled)
	}
}

// TestR1045_RestoreSurvivesMidStreamMapLoss 降级条件：Redis 字典在流中途消失
// （TTL/重启），请求级 ctx 字典（输入中间件挂入 r.Context() 的那份）必须继续
// 承担还原——可还原值绝不允许降级成遮蔽标记。
func TestR1045_RestoreSurvivesMidStreamMapLoss(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "t-r1045", "s-r1045-inv2"
	secretValue := "TrulySensitive987!"
	if err := rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:secret:1}", secretValue).Err(); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(NewPatternDetector())
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:secret:1}": secretValue})
	meta := r1045StreamMeta()

	chunk1 := r1045SSE(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
		"content": "first {SENSITIVE:secret:1}"}}}})
	result1, err := it.InterceptStreamChunk(ctx, chunk1, meta)
	if err != nil {
		t.Fatal(err)
	}
	if result1 == nil || !strings.Contains(string(result1.ModifiedChunk), "first "+secretValue) {
		t.Fatalf("chunk1 not restored: %+v", result1)
	}

	// 流中途：Redis 字典整键消失（TTL 到期/清库的形态）。
	if err := rdb.Del(context.Background(), sanitizeMapKey(tenant, session)).Err(); err != nil {
		t.Fatal(err)
	}

	chunk2 := r1045SSE(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
		"content": " second {SENSITIVE:secret:1}"}}}})
	result2, err := it.InterceptStreamChunk(ctx, chunk2, meta)
	if err != nil {
		t.Fatal(err)
	}
	var clientBytes string
	if result2 != nil && result2.ModifiedChunk != nil {
		clientBytes = string(result2.ModifiedChunk)
	} else if result2 == nil {
		clientBytes = string(chunk2)
	} else {
		clientBytes = string(chunk2)
	}
	assertNoMaskOrMarker(t, clientBytes)
	if !strings.Contains(clientBytes, "second "+secretValue) {
		t.Fatalf("mid-stream map loss degraded a restorable value: %q", clientBytes)
	}
}

// TestR1045_AnthropicToolInputLaneRestores anthropic 流式 tool input
// （content_block_delta + input_json_delta.partial_json）lane。
func TestR1045_AnthropicToolInputLaneRestores(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	const tenant, session = "t-r1045", "s-r1045-inv3"
	secretValue := "S3cr3tValue!"
	if err := rdb.HSet(context.Background(), sanitizeMapKey(tenant, session),
		"{SENSITIVE:secret:2}", secretValue).Err(); err != nil {
		t.Fatal(err)
	}
	s, _ := NewSanitizer(NewPatternDetector())
	it, err := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:secret:2}": secretValue})
	meta := r1045StreamMeta()

	frames := [][]byte{
		r1045SSE(t, map[string]any{"type": "content_block_start", "index": 1,
			"content_block": map[string]any{"type": "tool_use", "id": "tu_1", "name": "Write", "input": map[string]any{}}}),
		r1045SSE(t, map[string]any{"type": "content_block_delta", "index": 1,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"content":"{SENSITIVE:secret:2}"}`}}),
		r1045SSE(t, map[string]any{"type": "content_block_stop", "index": 1}),
	}
	var client strings.Builder
	for _, frame := range frames {
		result, err := it.InterceptStreamChunk(ctx, frame, meta)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if result != nil && result.ShouldBlock {
			t.Fatalf("anthropic stream blocked: %+v", result)
		}
		if result != nil && result.ModifiedChunk != nil {
			client.Write(result.ModifiedChunk)
		} else if result == nil {
			client.Write(frame)
		}
	}
	assembled := client.String()
	assertNoMaskOrMarker(t, assembled)
	if !strings.Contains(assembled, secretValue) {
		t.Fatalf("anthropic tool input not restored: %q", assembled)
	}
}
