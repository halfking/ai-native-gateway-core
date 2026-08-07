package sanitize

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestSanitizerInputHook_Name(t *testing.T) {
	h, _ := NewSanitizerInputHook(NewNoopSanitizer())
	if h.Name() != "sanitizer.input" {
		t.Errorf("Name() = %v, want sanitizer.input", h.Name())
	}
}

func TestSanitizerInputHook_Priority(t *testing.T) {
	h, _ := NewSanitizerInputHook(NewNoopSanitizer())
	if h.Priority() != 10 {
		t.Errorf("Priority() = %d, want 10", h.Priority())
	}
}

func TestSanitizerInputHook_NewNilSanitizer(t *testing.T) {
	_, err := NewSanitizerInputHook(nil)
	if err == nil {
		t.Fatal("NewSanitizerInputHook(nil) expected error")
	}
}

func TestSanitizerInputHook_Enabled(t *testing.T) {
	h, _ := NewSanitizerInputHook(NewNoopSanitizer())
	ctx := context.Background()

	tests := []struct {
		name string
		env  *domain.PipelineRequest
		want bool
	}{
		{"nil env", nil, false},
		{"empty request", &domain.PipelineRequest{}, false},
		{"has request", &domain.PipelineRequest{TransformedRequest: []byte("test")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.Enabled(ctx, tt.env); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizerInputHook_Execute_NoPII(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerInputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		TransformedRequest: []byte("今天天气真好"),
		Metadata:           make(map[string]any),
	}

	err := h.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if string(env.TransformedRequest) != "今天天气真好" {
		t.Errorf("TransformedRequest changed to %q", string(env.TransformedRequest))
	}

	_, ok := env.Metadata[MetadataKeySanitizeMap]
	if ok {
		t.Errorf("Metadata sanitize_map should not exist when no PII")
	}
}

func TestSanitizerInputHook_Execute_WithPII(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerInputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		TransformedRequest: []byte("手机13800138000，邮箱test@example.com"),
		Metadata:           make(map[string]any),
	}

	err := h.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "手机{SENSITIVE:phone:1}，邮箱{SENSITIVE:email:1}"
	if string(env.TransformedRequest) != expected {
		t.Errorf("TransformedRequest = %q, want %q", string(env.TransformedRequest), expected)
	}

	smRaw, ok := env.Metadata[MetadataKeySanitizeMap]
	if !ok {
		t.Fatal("Metadata sanitize_map not found")
	}
	sm, ok := smRaw.(SanitizeMap)
	if !ok {
		t.Fatalf("Metadata sanitize_map type = %T, want SanitizeMap", smRaw)
	}
	if sm["{SENSITIVE:phone:1}"] != "13800138000" {
		t.Errorf("phone map = %v, want 13800138000", sm["{SENSITIVE:phone:1}"])
	}
}

func TestSanitizerInputHook_Execute_NilMetadata(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerInputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		TransformedRequest: []byte("手机13800138000"),
	}

	err := h.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	_, ok := env.Metadata[MetadataKeySanitizeMap]
	if !ok {
		t.Fatal("Metadata should be created when nil")
	}
}

func TestSanitizerInputHook_Execute_NilReceiver(t *testing.T) {
	var h *SanitizerInputHook
	err := h.Execute(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("test"),
	})
	if err != nil {
		t.Fatalf("Execute() on nil receiver error = %v", err)
	}
}

func TestSanitizerInputHook_OnError(t *testing.T) {
	h, _ := NewSanitizerInputHook(NewNoopSanitizer())
	err := h.OnError(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("OnError() = %v, want nil", err)
	}
}

func TestSanitizerOutputHook_Name(t *testing.T) {
	h, _ := NewSanitizerOutputHook(NewNoopSanitizer())
	if h.Name() != "sanitizer.output" {
		t.Errorf("Name() = %v, want sanitizer.output", h.Name())
	}
}

func TestSanitizerOutputHook_Priority(t *testing.T) {
	h, _ := NewSanitizerOutputHook(NewNoopSanitizer())
	// Priority 改为 50：先于 OutputComplianceHook (Priority 100) 执行还原，
	// 让 compliance checker 审查还原后的真实内容，而不是占位符。
	if h.Priority() != 50 {
		t.Errorf("Priority() = %d, want 50", h.Priority())
	}
}

func TestSanitizerOutputHook_NewNilSanitizer(t *testing.T) {
	_, err := NewSanitizerOutputHook(nil)
	if err == nil {
		t.Fatal("NewSanitizerOutputHook(nil) expected error")
	}
}

func TestSanitizerOutputHook_Enabled(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerOutputHook(s)
	ctx := context.Background()

	tests := []struct {
		name string
		env  *domain.PipelineRequest
		want bool
	}{
		{"nil env", nil, false},
		{"no response", &domain.PipelineRequest{Metadata: map[string]any{MetadataKeySanitizeMap: SanitizeMap{}}}, false},
		{"no sanitize map", &domain.PipelineRequest{UpstreamResponse: []byte("ok")}, false},
		{"has both", &domain.PipelineRequest{
			UpstreamResponse: []byte("ok"),
			Metadata:         map[string]any{MetadataKeySanitizeMap: SanitizeMap{}},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.Enabled(ctx, tt.env); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizerOutputHook_Execute_Restore(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerOutputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		UpstreamResponse: []byte("手机{SENSITIVE:phone:1}已处理"),
		Metadata: map[string]any{
			MetadataKeySanitizeMap: SanitizeMap{
				"{SENSITIVE:phone:1}": "13800138000",
			},
		},
	}

	err := h.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	expected := "手机13800138000已处理"
	if string(env.UpstreamResponse) != expected {
		t.Errorf("UpstreamResponse = %q, want %q", string(env.UpstreamResponse), expected)
	}
}

func TestSanitizerOutputHook_Execute_NoMap(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerOutputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		UpstreamResponse: []byte("手机{SENSITIVE:phone:1}"),
		Metadata:         make(map[string]any),
	}

	err := h.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if string(env.UpstreamResponse) != "手机{SENSITIVE:phone:1}" {
		t.Errorf("UpstreamResponse changed to %q", string(env.UpstreamResponse))
	}
}

func TestSanitizerOutputHook_Execute_WrongMapType(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	h, _ := NewSanitizerOutputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		UpstreamResponse: []byte("手机{SENSITIVE:phone:1}"),
		Metadata: map[string]any{
			MetadataKeySanitizeMap: "not a sanitize map",
		},
	}

	err := h.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if string(env.UpstreamResponse) != "手机{SENSITIVE:phone:1}" {
		t.Errorf("UpstreamResponse changed to %q", string(env.UpstreamResponse))
	}
}

func TestSanitizerOutputHook_Execute_NilReceiver(t *testing.T) {
	var h *SanitizerOutputHook
	err := h.Execute(context.Background(), &domain.PipelineRequest{
		UpstreamResponse: []byte("test"),
	})
	if err != nil {
		t.Fatalf("Execute() on nil receiver error = %v", err)
	}
}

func TestSanitizerOutputHook_OnError(t *testing.T) {
	h, _ := NewSanitizerOutputHook(NewNoopSanitizer())
	err := h.OnError(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("OnError() = %v, want nil", err)
	}
}

func TestSanitizerHooks_RoundTrip(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	inputHook, _ := NewSanitizerInputHook(s)
	outputHook, _ := NewSanitizerOutputHook(s)
	ctx := context.Background()

	env := &domain.PipelineRequest{
		TransformedRequest: []byte("用户手机13800138000，邮箱test@example.com"),
		Metadata:           make(map[string]any),
	}

	if err := inputHook.Execute(ctx, env); err != nil {
		t.Fatalf("inputHook.Execute() error = %v", err)
	}

	sm := env.Metadata[MetadataKeySanitizeMap].(SanitizeMap)

	env.UpstreamResponse = []byte("订单已发送至{SENSITIVE:email:1}，短信通知{SENSITIVE:phone:1}")
	env.Metadata[MetadataKeySanitizeMap] = sm

	if err := outputHook.Execute(ctx, env); err != nil {
		t.Fatalf("outputHook.Execute() error = %v", err)
	}

	expected := "订单已发送至test@example.com，短信通知13800138000"
	if string(env.UpstreamResponse) != expected {
		t.Errorf("RoundTrip = %q, want %q", string(env.UpstreamResponse), expected)
	}
}
