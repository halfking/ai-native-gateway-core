package sanitize

import (
	"context"
	"testing"
)

func TestNewSanitizer_NilDetector(t *testing.T) {
	_, err := NewSanitizer(nil)
	if err == nil {
		t.Fatal("NewSanitizer(nil) expected error")
	}
}

func TestSanitizeInput_NoPII(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	result, err := s.SanitizeInput(ctx, "今天天气真好")
	if err != nil {
		t.Fatalf("SanitizeInput() error = %v", err)
	}
	if result.SanitizedText != "今天天气真好" {
		t.Errorf("SanitizedText = %v, want original text", result.SanitizedText)
	}
	if len(result.SanitizeMap) != 0 {
		t.Errorf("SanitizeMap = %v, want empty", result.SanitizeMap)
	}
}

func TestSanitizeInput_PhoneAndEmail(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	input := "手机13800138000，邮箱test@example.com"
	result, err := s.SanitizeInput(ctx, input)
	if err != nil {
		t.Fatalf("SanitizeInput() error = %v", err)
	}

	expected := "手机{SENSITIVE:phone:1}，邮箱{SENSITIVE:email:1}"
	if result.SanitizedText != expected {
		t.Errorf("SanitizedText = %q, want %q", result.SanitizedText, expected)
	}

	if len(result.SanitizeMap) != 2 {
		t.Fatalf("SanitizeMap = %v, want 2 entries", result.SanitizeMap)
	}
	if result.SanitizeMap["{SENSITIVE:phone:1}"] != "13800138000" {
		t.Errorf("phone map = %v, want 13800138000", result.SanitizeMap["{SENSITIVE:phone:1}"])
	}
	if result.SanitizeMap["{SENSITIVE:email:1}"] != "test@example.com" {
		t.Errorf("email map = %v, want test@example.com", result.SanitizeMap["{SENSITIVE:email:1}"])
	}
}

func TestSanitizeInput_MultipleSameType(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	input := "手机13800138000和13912345678"
	result, err := s.SanitizeInput(ctx, input)
	if err != nil {
		t.Fatalf("SanitizeInput() error = %v", err)
	}

	expected := "手机{SENSITIVE:phone:1}和{SENSITIVE:phone:2}"
	if result.SanitizedText != expected {
		t.Errorf("SanitizedText = %q, want %q", result.SanitizedText, expected)
	}

	if result.SanitizeMap["{SENSITIVE:phone:1}"] != "13800138000" {
		t.Errorf("phone:1 map = %v, want 13800138000", result.SanitizeMap["{SENSITIVE:phone:1}"])
	}
	if result.SanitizeMap["{SENSITIVE:phone:2}"] != "13912345678" {
		t.Errorf("phone:2 map = %v, want 13912345678", result.SanitizeMap["{SENSITIVE:phone:2}"])
	}
}

func TestSanitizeInput_NilReceiver(t *testing.T) {
	var s *Sanitizer
	_, err := s.SanitizeInput(context.Background(), "test")
	if err == nil {
		t.Fatal("SanitizeInput() on nil receiver expected error")
	}
}

func TestRestoreOutput_ExactMatch(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	sm := SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
		"{SENSITIVE:email:1}": "test@example.com",
	}

	input := "您查询的手机{SENSITIVE:phone:1}对应的订单已发货，确认邮件已发至{SENSITIVE:email:1}"
	expected := "您查询的手机13800138000对应的订单已发货，确认邮件已发至test@example.com"

	result, err := s.RestoreOutput(ctx, input, sm)
	if err != nil {
		t.Fatalf("RestoreOutput() error = %v", err)
	}
	if result != expected {
		t.Errorf("RestoreOutput() = %q, want %q", result, expected)
	}
}

func TestRestoreOutput_UnknownPlaceholder(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	sm := SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"}
	input := "手机{SENSITIVE:phone:1}和未知占位符{SENSITIVE:email:2}"

	result, err := s.RestoreOutput(ctx, input, sm)
	if err != nil {
		t.Fatalf("RestoreOutput() error = %v", err)
	}

	expected := "手机13800138000和未知占位符{SENSITIVE:email:2}"
	if result != expected {
		t.Errorf("RestoreOutput() = %q, want %q", result, expected)
	}
}

func TestRestoreOutput_NoPlaceholders(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	result, err := s.RestoreOutput(ctx, "普通文本，没有占位符", SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	if err != nil {
		t.Fatalf("RestoreOutput() error = %v", err)
	}
	if result != "普通文本，没有占位符" {
		t.Errorf("RestoreOutput() = %q, want original text", result)
	}
}

func TestRestoreOutput_EmptyMap(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	result, err := s.RestoreOutput(ctx, "文本{SENSITIVE:phone:1}", make(SanitizeMap))
	if err != nil {
		t.Fatalf("RestoreOutput() error = %v", err)
	}
	if result != "文本{SENSITIVE:phone:1}" {
		t.Errorf("RestoreOutput() = %q, want unchanged", result)
	}
}

func TestRestoreOutput_NilReceiver(t *testing.T) {
	var s *Sanitizer
	_, err := s.RestoreOutput(context.Background(), "test", SanitizeMap{})
	if err == nil {
		t.Fatal("RestoreOutput() on nil receiver expected error")
	}
}

func TestRestoreOutputOrMask_UnknownPlaceholder(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	result, err := s.RestoreOutputOrMask(ctx,
		"未知{SENSITIVE:email:1}已被屏蔽",
		SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"},
	)
	if err != nil {
		t.Fatalf("RestoreOutputOrMask() error = %v", err)
	}

	expected := "未知[REDACTED]已被屏蔽"
	if result != expected {
		t.Errorf("RestoreOutputOrMask() = %q, want %q", result, expected)
	}
}

func TestRestoreOutputOrMask_KnownPlaceholder(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	result, err := s.RestoreOutputOrMask(ctx,
		"手机{SENSITIVE:phone:1}已还原",
		SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"},
	)
	if err != nil {
		t.Fatalf("RestoreOutputOrMask() error = %v", err)
	}

	expected := "手机13800138000已还原"
	if result != expected {
		t.Errorf("RestoreOutputOrMask() = %q, want %q", result, expected)
	}
}

func TestRestoreOutputOrMask_EmptyMap(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	result, err := s.RestoreOutputOrMask(ctx,
		"手机{SENSITIVE:phone:1}被屏蔽",
		make(SanitizeMap),
	)
	if err != nil {
		t.Fatalf("RestoreOutputOrMask() error = %v", err)
	}

	expected := "手机[REDACTED]被屏蔽"
	if result != expected {
		t.Errorf("RestoreOutputOrMask() = %q, want %q", result, expected)
	}
}

func TestRestoreOutputOrMask_NilReceiver(t *testing.T) {
	var s *Sanitizer
	_, err := s.RestoreOutputOrMask(context.Background(), "test", SanitizeMap{})
	if err == nil {
		t.Fatal("RestoreOutputOrMask() on nil receiver expected error")
	}
}

func TestRoundTrip(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	ctx := context.Background()

	originalInput := "用户手机13800138000，邮箱test@example.com"

	result, err := s.SanitizeInput(ctx, originalInput)
	if err != nil {
		t.Fatalf("SanitizeInput() error = %v", err)
	}

	llmOutput := "订单已发送至{SENSITIVE:email:1}，短信通知{SENSITIVE:phone:1}"

	finalOutput, err := s.RestoreOutput(ctx, llmOutput, result.SanitizeMap)
	if err != nil {
		t.Fatalf("RestoreOutput() error = %v", err)
	}

	expected := "订单已发送至test@example.com，短信通知13800138000"
	if finalOutput != expected {
		t.Errorf("RoundTrip = %q, want %q", finalOutput, expected)
	}
}

func TestNewNoopSanitizer(t *testing.T) {
	s := NewNoopSanitizer()
	ctx := context.Background()

	result, err := s.SanitizeInput(ctx, "手机13800138000")
	if err != nil {
		t.Fatalf("SanitizeInput() error = %v", err)
	}
	if result.SanitizedText != "手机13800138000" {
		t.Errorf("noop sanitization changed text to %q", result.SanitizedText)
	}
}

func TestSanitizerName(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	if s.Name() != "sanitize" {
		t.Errorf("Name() = %v, want sanitize", s.Name())
	}
}
