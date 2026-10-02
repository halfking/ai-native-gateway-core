package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func TestOutputSensitiveMasksNewValuesAndRestoresKnownValues(t *testing.T) {
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
	if !strings.Contains(body, "13800138000") || !strings.Contains(body, "[REDACTED]") {
		t.Fatal("known restoration or new-value masking missing")
	}
	for _, secret := range []string{"FreshSecret!", "203.0.113.8", "username=ops"} {
		if strings.Contains(body, secret) {
			t.Error("new sensitive output leaked")
		}
	}
}

func TestOutputSensitiveSplitStreamMaskAndRestore(t *testing.T) {
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
	s, _ := NewSanitizer(NewPatternDetector())
	result, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: []byte(`{"error":{"message":"connection password=NewSecret server=203.0.113.8","type":"provider_error"}}`)})
	if err != nil || result == nil || result.ShouldBlock || strings.Contains(string(result.ModifiedBody), "NewSecret") || strings.Contains(string(result.ModifiedBody), "203.0.113.8") || !strings.Contains(string(result.ModifiedBody), "provider_error") {
		t.Fatal("provider error sensitive text was not masked safely")
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
