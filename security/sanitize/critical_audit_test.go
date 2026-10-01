package sanitize

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func TestCriticalMixedMarkerDoesNotExemptSecret(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, detector := range []*PatternDetector{NewPatternDetector(), configured} {
		s, _ := NewSanitizer(detector)
		for _, text := range []string{`password="FreshSecret{SENSITIVE:phone:1}tail"`, `password={SENSITIVE:phone:1}FreshSecret`, strings.Join([]string{"-----BEGIN", "PRIVATE KEY-----"}, " ") + "\nFreshSecret{SENSITIVE:phone:1}tail\n-----END PRIVATE KEY-----"} {
			result, err := s.SanitizeInput(context.Background(), text)
			if err != nil || strings.Contains(result.SanitizedText, "FreshSecret") || strings.Contains(result.SanitizedText, "tail") {
				t.Errorf("mixed marker exempted sensitive text: err=%v", err)
			}
			checked, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: marshalAuditBody(t, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}})})
			if err != nil || checked == nil || checked.ShouldBlock || strings.Contains(string(checked.ModifiedBody), "FreshSecret") || strings.Contains(string(checked.ModifiedBody), "tail") {
				t.Errorf("mixed marker escaped output gate: %+v %v", checked, err)
			}
		}
	}
}

func marshalAuditBody(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCriticalNestedToolUnicodeMarkerIsReserved(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	args := `{"note":"\u007bSENSITIVE:secret:1\u007d","password":"safephrase"}`
	body := marshalAuditBody(t, map[string]any{"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"arguments": args}}}}}})
	called := false
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		mapping, _ := SanitizeMapFromContext(r.Context())
		if _, ok := mapping["{SENSITIVE:secret:1}"]; ok || mapping["{SENSITIVE:secret:2}"] != "safephrase" {
			t.Error("nested escaped literal aliased a credential")
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))
	if rec.Code != 200 || !called {
		t.Fatalf("handler not exercised: status=%d", rec.Code)
	}
}

func TestCriticalUnsupportedCredentialShapesFailClosed(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	for _, input := range []any{map[string]any{"password": 123}, map[string]any{"password": true}, map[string]any{"password": map[string]any{"value": "short"}}, map[string]any{"password": []any{123}}, map[string]any{"account": 42}} {
		called := false
		h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		body := marshalAuditBody(t, map[string]any{"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"arguments": string(marshalAuditBody(t, input))}}}}}})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))
		if called || rec.Code != 400 {
			t.Errorf("unsupported credential shape left gateway: status=%d", rec.Code)
		}
		for _, output := range []any{
			map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": string(marshalAuditBody(t, input))}}}}}}},
			map[string]any{"type": "message", "content": []any{map[string]any{"type": "tool_use", "input": input}}},
		} {
			result, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: marshalAuditBody(t, output)})
			if err != nil || result == nil || !result.ShouldBlock {
				t.Errorf("typed credential operation passed gate: %+v %v", result, err)
			}
		}
	}
}

func TestCriticalMediaShapedToolDoesNotHideText(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	body := []byte(`{"type":"message","content":[{"type":"tool_use","input":{"type":"image","password":"short","data":"opaque"}}]}`)
	result, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: body})
	if err != nil || result == nil || !result.ShouldBlock {
		t.Fatalf("model discriminator bypassed tool gate: %+v %v", result, err)
	}
}

func TestCriticalCustomToolOperationsBlocked(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	result, err := guard.InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: []byte(`{"object":"response","output":[{"type":"custom_tool_call","input":"connect password=FreshSecret"}]}`)})
	if err != nil || result == nil || !result.ShouldBlock {
		t.Errorf("modified custom operation released: %+v %v", result, err)
	}
	meta := &response.StreamMeta{State: response.NewStreamState()}
	resultChunk, err := guard.InterceptStreamChunk(context.Background(), []byte("data: {\"type\":\"response.custom_tool_call_input.delta\",\"delta\":\"connect password=FreshSecret\"}\n\n"), meta)
	if err == nil && (resultChunk == nil || !resultChunk.ShouldBlock) {
		resultChunk, err = guard.InterceptStreamChunk(context.Background(), []byte("data: {\"type\":\"response.completed\"}\n\n"), meta)
	}
	if err != nil || resultChunk == nil || !resultChunk.ShouldBlock {
		t.Errorf("stream custom operation released: %+v %v", resultChunk, err)
	}
}

func TestCriticalReasoningTextStreamIsCheckedAndRestored(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	chain := response.NewInterceptorChain(NewOutputSensitiveInterceptor(s, OutputMask), restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	meta := &response.StreamMeta{State: response.NewStreamState()}
	frame := []byte("data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"known {SENSITIVE:phone:1}; password=FreshSecret\"}\n\n")
	first, err := chain.InterceptStreamChunk(ctx, frame, meta)
	if err != nil || first == nil || first.ShouldBlock || !first.SuppressChunk {
		t.Fatalf("reasoning not held: %+v %v", first, err)
	}
	end, err := chain.InterceptStreamChunk(ctx, []byte("data: {\"type\":\"response.completed\"}\n\n"), meta)
	if err != nil || end == nil || end.ShouldBlock || !strings.Contains(string(end.ModifiedChunk), "13800138000") || strings.Contains(string(end.ModifiedChunk), "FreshSecret") {
		t.Fatalf("reasoning lane failed: %+v %v", end, err)
	}
}

func TestCriticalToolRestorePreservesNumberAndEscapes(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
	secret := "two words and \"quoted\"\\line\n"
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:secret:1}": secret})
	args := `{"password":"{SENSITIVE:secret:1}","limit":9007199254740993}`
	body := marshalAuditBody(t, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": args}}}}}}})
	result, err := restore.InterceptNonStream(ctx, &response.InterceptRequest{ResponseBody: body})
	if err != nil || result == nil || result.ShouldBlock || !strings.Contains(string(result.ModifiedBody), "9007199254740993") {
		t.Errorf("round-trip changed integer: %+v %v", result, err)
	}
	meta := &response.StreamMeta{State: response.NewStreamState()}
	var arguments strings.Builder
	for _, part := range []string{`{"password":"{SENSITIVE:sec`, `ret:1}","limit":9007199254740993}`} {
		payload := marshalAuditBody(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": part}}}}}}})
		chunk := append(append([]byte("data: "), payload...), '\n', '\n')
		changed, err := restore.InterceptStreamChunk(ctx, chunk, meta)
		if err != nil || changed != nil && changed.ShouldBlock {
			t.Fatalf("tool stream blocked: %+v %v", changed, err)
		}
		if changed != nil && len(changed.ModifiedChunk) > 0 {
			chunk = changed.ModifiedChunk
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(string(chunk), "data: "))), &frame); err != nil {
			t.Fatal(err)
		}
		arguments.WriteString(frame["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string))
	}
	var decoded struct {
		Password string      `json:"password"`
		Limit    json.Number `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments.String()), &decoded); err != nil || decoded.Password != secret || decoded.Limit.String() != "9007199254740993" {
		t.Fatalf("reassembled arguments invalid or altered: %v", err)
	}
}

func TestCriticalCleanTurnKeepsMappingGeneration(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	var generation string
	round := 0
	cache := compression.NewSessionCache(nil, nil)
	compressor := compression.NewSessionCompressor(compression.SessionCompressorDeps{Cache: cache})
	const tenant, session = "tenant-clean", "gw_cleanturn01"
	h := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		info, ok := compression.SanitizeInfoFromContext(r.Context())
		if !ok || info.MapGeneration == "" {
			t.Error("clean turn lost generation")
			return
		}
		if round == 0 {
			generation = info.MapGeneration
			old := []byte(`{"messages":[{"role":"user","content":"[smm_v1:old]\nprior safe summary {SENSITIVE:phone:1}"},{"role":"assistant","content":"retained anchor"}]}`)
			if err := cache.Set(r.Context(), tenant, session, &compression.SessionState{MsgCount: 2, SanitizeMapGeneration: generation}, old); err != nil {
				t.Fatal(err)
			}
		} else {
			if info.Stats.SanitizedAt != 0 || info.Stats.PlaceholderCount != 0 {
				t.Error("clean turn reported cumulative sanitization")
			}
			if info.MapGeneration != generation {
				t.Error("clean turn changed live generation")
			}
			mapping, _ := SanitizeMapFromContext(r.Context())
			if mapping["{SENSITIVE:phone:1}"] != "13800138000" {
				t.Error("clean turn did not capture request dictionary")
			}
			prepared := compressor.Prepare(r.Context(), body, tenant, session, "openai", 0, false)
			st, _, err := cache.GetOrLoad(r.Context(), tenant, session)
			if err != nil || st == nil || st.SanitizeStats.PlaceholderCount != 0 || st.CompressionSourceSnapshot.MessageCount != 3 {
				t.Error("clean cached state lost scope or retained old stats")
			}
			if !strings.Contains(string(prepared.OutboundBody), "prior safe summary") {
				t.Error("live-generation clean turn dropped compressed history")
			}
		}
	}))
	for _, body := range []string{`{"messages":[{"role":"user","content":"phone 13800138000"}]}`, `{"messages":[{"role":"assistant","content":"retained anchor"},{"role":"user","content":"next ordinary question"}]}`} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("X-Gw-Session-Id", session)
		req = req.WithContext(WithAuthenticatedTenant(context.Background(), tenant))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d", rec.Code)
		}
		round++
	}
}

func TestCriticalSingleFrameJSONToolAndOpaqueBoundaries(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	for _, args := range []string{`{"\u0070assword":"short"}`, `{"data":{"password":{"value":"short"}}}`} {
		gate := NewOutputSensitiveInterceptor(s, OutputMask)
		payload := marshalAuditBody(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": args}}}}}}})
		meta := &response.StreamMeta{State: response.NewStreamState()}
		first, err := gate.InterceptStreamChunk(context.Background(), append(append([]byte("data: "), payload...), '\n', '\n'), meta)
		if err != nil || first != nil && first.ShouldBlock {
			continue
		}
		last, err := gate.InterceptStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
		if err == nil && (last == nil || !last.ShouldBlock) {
			t.Errorf("single-frame unsafe tool released: %s", args)
		}
	}
	for _, input := range []any{map[string]any{"data": map[string]any{"password": map[string]any{"value": "short"}}}, map[string]any{"data": []any{map[string]any{"password": true}}}} {
		body := marshalAuditBody(t, map[string]any{"type": "message", "content": []any{map[string]any{"type": "tool_use", "input": input}}})
		out, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: body})
		if err == nil && (out == nil || !out.ShouldBlock) {
			t.Errorf("structured data bypassed gate: %s", body)
		}
	}
	body := marshalAuditBody(t, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"arguments": `{"data":"opaque13800138000","note":"ordinary"}`}}}}}}})
	out, err := NewOutputSensitiveInterceptor(s, OutputMask).InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: body})
	if err != nil || out != nil && (out.ShouldBlock || len(out.ModifiedBody) > 0) {
		t.Errorf("opaque binary changed or blocked: %+v %v", out, err)
	}
}

func TestCriticalCachedStructuredToolIsRecheckedBeforePrepare(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	called := false
	h := mw.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		info, ok := compression.SanitizeInfoFromContext(r.Context())
		if !ok {
			t.Fatal("real sanitizer context absent")
		}
		for _, args := range []string{`{"data":{"password":{"value":"short"}}}`, `{"password":123}`, `{"\u0070assword":"short"}`} {
			cache := compression.NewSessionCache(nil, nil)
			old := marshalAuditBody(t, map[string]any{"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"arguments": args}}}}, map[string]any{"role": "assistant", "content": "retained anchor"}}})
			if err := cache.Set(r.Context(), "tenant", "gw_cached_guard01", &compression.SessionState{MsgCount: 2, SanitizeMapGeneration: info.MapGeneration}, old); err != nil {
				t.Fatal(err)
			}
			input := []byte(`{"messages":[{"role":"assistant","content":"retained anchor"},{"role":"user","content":"ordinary question"}]}`)
			result := compression.NewSessionCompressor(compression.SessionCompressorDeps{Cache: cache}).Prepare(r.Context(), input, "tenant", "gw_cached_guard01", "openai", 0, false)
			effective := result.OutboundBody
			if effective == nil {
				effective = input
			}
			if strings.Contains(string(effective), "tool_calls") {
				t.Fatalf("unchecked cache reached prepared body: %s", args)
			}
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"phone 13800138000"}]}`)))
	if !called || rec.Code != 200 {
		t.Fatalf("middleware not exercised: %d", rec.Code)
	}
}
