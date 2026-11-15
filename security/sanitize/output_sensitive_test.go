package sanitize

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func TestOutputSensitiveMasksNewValuesAndRestoresKnownValues(t *testing.T) {
	// r1045：遮蔽改写默认关闭；本测试钉 opt-in（sanitize.output_mask_enabled=true）
	// 时的遮蔽语义。默认面（原样下发+observe 台账）在 r1045 钉测里。
	enableOutputMaskingForTest(t)
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	result, err := chain.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID: "gw_output01", TenantID: "tenant-output",
		ResponseBody: []byte(`{"choices":[{"message":{"content":"known {SENSITIVE:phone:1}; password=FreshSecret! host=203.0.113.8 username=ops"}}]}`),
	})
	if err != nil || result == nil || result.ShouldBlock {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	body := string(result.ModifiedBody)
	if !strings.Contains(body, "13800138000") || !strings.Contains(body, RedactionMarker(TypeSecret)) {
		t.Fatal("known restoration or new-value masking missing")
	}
	// 2026-10-10: the address left this list with the rest of the redaction
	// scope. Model-authored text mentioning a host is operational output, not
	// a leak; the exfiltration risk lives in tool arguments and is guarded
	// under ScopeEgress (Test154PublicIPToolCallStillBlocks).
	for _, secret := range []string{"FreshSecret!", "username=ops"} {
		if strings.Contains(body, secret) {
			t.Error("new sensitive output leaked")
		}
	}
	if !strings.Contains(body, "203.0.113.8") {
		t.Error("address text must survive output redaction under ScopeRedact")
	}
}

func TestOutputSensitiveSplitStreamMaskAndRestore(t *testing.T) {
	// r1045：mask-on 子模式（同上）。
	enableOutputMaskingForTest(t)
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	meta := &response.StreamMeta{SessionID: "gw_stream01", TenantID: "tenant-stream", State: response.NewStreamState()}
	frames := []string{
		`data: {"choices":[{"index":0,"delta":{"content":"password=Fresh"}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"content":"Secret! known {SENSITIVE:phone:1}"}}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	var wire strings.Builder
	for i, frame := range frames {
		result, err := chain.InterceptStreamChunk(ctx, []byte(frame), meta)
		if err != nil || result != nil && result.ShouldBlock {
			t.Fatalf("frame=%d result=%+v err=%v", i, result, err)
		}
		if result == nil {
			wire.WriteString(frame)
		} else if len(result.ModifiedChunk) > 0 {
			wire.Write(result.ModifiedChunk)
		} else if !result.SuppressChunk {
			wire.WriteString(frame)
		}
		if i < 2 && wire.Len() != 0 {
			t.Fatal("unchecked text released before terminal")
		}
	}
	if strings.Contains(wire.String(), "Fresh") || strings.Contains(wire.String(), "Secret!") || strings.Contains(wire.String(), "SENSITIVE") {
		t.Fatal("raw sensitive output or unresolved marker leaked")
	}
	if !strings.Contains(wire.String(), "13800138000") || !strings.Contains(wire.String(), "[DONE]") {
		t.Fatal("known restoration/terminal lost")
	}
}

func TestOutputSensitiveBlockAndDetectionFailure(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	for _, guard := range []response.ResponseInterceptor{NewOutputSensitiveInterceptor(s, OutputBlock), NewOutputSensitiveInterceptor(nil, OutputMask)} {
		result, err := guard.InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: []byte(`{"choices":[{"message":{"content":"password=NewSecret"}}]}`)})
		if err != nil || result == nil || !result.ShouldBlock || len(result.ModifiedBody) > 0 {
			t.Fatalf("sensitive output/failure not blocked: %+v %v", result, err)
		}
	}
}

func TestProviderErrorMessageIsSensitiveOutput(t *testing.T) {
	enableOutputMaskingForTest(t) // r1045：遮蔽族钉测显式 opt-in（默认已关）
	s, _ := NewSanitizer(NewPatternDetector())
	result, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: []byte(`{"error":{"message":"connection password=NewSecret server=203.0.113.8","type":"provider_error"}}`)})
	if err != nil || result == nil || result.ShouldBlock || strings.Contains(string(result.ModifiedBody), "NewSecret") || !strings.Contains(string(result.ModifiedBody), "provider_error") {
		t.Fatal("provider error sensitive text was not masked safely")
	}
	// The credential is masked; the host in the same message is not, for the
	// same reason as TestOutputSensitiveMasksNewValuesAndRestoresKnownValues.
	if !strings.Contains(string(result.ModifiedBody), "203.0.113.8") {
		t.Fatal("provider error host must survive redaction under ScopeRedact")
	}
}

func TestUnknownEnvelopeRestorationPreservesJSON(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:secret:1}": "quote\"slash\\newline\n"})
	result, err := restore.InterceptNonStream(ctx, &response.InterceptRequest{ResponseBody: []byte(`{"error":{"message":"known {SENSITIVE:secret:1}"}}`)})
	if err != nil || result == nil || result.ShouldBlock || !json.Valid(result.ModifiedBody) {
		t.Fatalf("invalid restored JSON: %+v %v", result, err)
	}
}

func TestOutputSensitiveEOFStillRunsRestoration(t *testing.T) {
	// r1045：mask-on 子模式（同上）。
	enableOutputMaskingForTest(t)
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	meta := &response.StreamMeta{SessionID: "gw_eof01", State: response.NewStreamState()}
	frame := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"known {SENSITIVE:phone:1}; password=NewSecret\"}}]}\n\n")
	result, err := chain.InterceptStreamChunk(ctx, frame, meta)
	if err != nil || result == nil || !result.SuppressChunk {
		t.Fatalf("frame not held: %+v %v", result, err)
	}
	wire, err := chain.FlushStreamPending(ctx, meta)
	if err != nil || !strings.Contains(string(wire), "13800138000") || strings.Contains(string(wire), "NewSecret") || strings.Contains(string(wire), "SENSITIVE") {
		t.Fatal("EOF release bypassed restoration/masking")
	}
}

func TestGeneratedSensitiveToolOperationsAreBlocked(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	bodies := []string{
		`{"type":"message","content":[{"type":"tool_use","input":{"password":["short"]}}]}`,
		`{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"{\"password\":[\"short\"]}"}}]}}]}`,
		`{"choices":[{"message":{"tool_calls":[{"function":{"name":"connect","arguments":"{\"password\":\"generated value with spaces\"}"}}]}}]}`,
		`{"type":"message","content":[{"type":"tool_use","id":"call","name":"connect","input":{"password":"short","username":"ops"}}]}`,
		`{"object":"response","output":[{"type":"function_call","name":"connect","arguments":"{\"host\":\"203.0.113.8\"}"}]}`,
	}
	for _, body := range bodies {
		result, err := guard.InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: []byte(body)})
		if err != nil || result == nil || !result.ShouldBlock {
			t.Fatalf("unsafe tool operation not blocked: %+v %v", result, err)
		}
	}
}

func TestLegacyCompletionAndReasoningStreamsMaskAndRestore(t *testing.T) {
	enableOutputMaskingForTest(t) // r1045：遮蔽族钉测显式 opt-in（默认已关）
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	for _, lane := range []string{"text", "reasoning_content"} {
		t.Run(lane, func(t *testing.T) {
			restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
			chain := response.NewInterceptorChain(NewOutputSensitiveInterceptor(s, OutputMask), restore)
			meta := &response.StreamMeta{State: response.NewStreamState()}
			var wire strings.Builder
			for _, text := range []string{"known {SENSITIVE:pho", "ne:1}; password=NewSecret"} {
				choice := map[string]any{"index": 0}
				if lane == "text" {
					choice[lane] = text
				} else {
					choice["delta"] = map[string]any{lane: text}
				}
				body, _ := json.Marshal(map[string]any{"choices": []any{choice}})
				result, err := chain.InterceptStreamChunk(ctx, append(append([]byte("data: "), body...), '\n', '\n'), meta)
				if err != nil || result == nil || result.ShouldBlock || !result.SuppressChunk {
					t.Fatalf("frame not safely held: %+v %v", result, err)
				}
			}
			result, err := chain.InterceptStreamChunk(ctx, []byte("data: [DONE]\n\n"), meta)
			if err != nil || result == nil || result.ShouldBlock {
				t.Fatalf("release failed: %+v %v", result, err)
			}
			wire.Write(result.ModifiedChunk)
			if !strings.Contains(wire.String(), "13800138000") || strings.Contains(wire.String(), "NewSecret") || strings.Contains(wire.String(), "SENSITIVE") {
				t.Fatal("legacy/reasoning lane bypassed guard or restoration")
			}
		})
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	result, err := guard.InterceptNonStream(ctx, &response.InterceptRequest{ResponseBody: []byte(`{"choices":[{"text":"password=NewSecret"}]}`)})
	if err != nil || result == nil || result.ShouldBlock || strings.Contains(string(result.ModifiedBody), "NewSecret") {
		t.Fatal("legacy nonstream output bypassed guard")
	}
}

// TestOutputMaskDistinctValuesGetDistinctMarkers 钉 2026-11-15 用户裁决
// 「脱敏不可把不同值脱成同一个值」：同一响应内同一类型出现 ≥2 个不同自造值
// ⇒ 带序号形态且互异（[REDACTED:secret:1] / [REDACTED:secret:2]）；同值重复
// ⇒ 同序号（可关联）；单值 type ⇒ 经典形态（无序号，客户端解析兼容）。
func TestOutputMaskDistinctValuesGetDistinctMarkers(t *testing.T) {
	enableOutputMaskingForTest(t)
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(guard, restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{})
	result, err := chain.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID: "gw_output02", TenantID: "tenant-output",
		// 同类型（secret）两个不同的自造值；AlphaSecret1 出现两次；email 单值。
		ResponseBody: []byte(`{"choices":[{"message":{"content":"password=AlphaSecret1 password=AlphaSecret1 token=BetaSecret2 mail=bob@example.com"}}]}`),
	})
	if err != nil || result == nil || result.ShouldBlock {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	body := string(result.ModifiedBody)
	for _, secret := range []string{"AlphaSecret1", "BetaSecret2"} {
		if strings.Contains(body, secret) {
			t.Fatalf("new sensitive output leaked: %s", body)
		}
	}
	// 两个不同 secret 值 → 两个互异的带序号标记。
	if !strings.Contains(body, indexedMarker("secret", 1)) || !strings.Contains(body, indexedMarker("secret", 2)) {
		t.Fatalf("distinct values must get distinct indexed markers: %s", body)
	}
	// 同值重复 → 同序号（secret:1 恰好出现两次）。
	if got := strings.Count(body, indexedMarker("secret", 1)); got != 2 {
		t.Fatalf("same value must reuse the same marker (want 2, got %d): %s", got, body)
	}
	if strings.Contains(body, indexedMarker("secret", 3)) {
		t.Fatalf("only two distinct secret values expected: %s", body)
	}
	// 单值 email → 经典形态（无序号）。
	if !strings.Contains(body, RedactionMarker(TypeEmail)) || strings.Contains(body, indexedMarker("email", 1)) {
		t.Fatalf("single-value type must keep the classic marker: %s", body)
	}
}

func indexedMarker(typ SensitiveType, n int) string {
	return "[REDACTED:" + string(typ) + ":" + strconv.Itoa(n) + "]"
}
