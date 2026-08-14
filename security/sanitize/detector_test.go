package sanitize

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPatternDetector_Detect(t *testing.T) {
	d := NewPatternDetector()
	ctx := context.Background()

	tests := []struct {
		name string
		text string
		want int
	}{
		{"phone CN mobile", "我的手机是13800138000", 1},
		{"id card CN", "身份证号110101199001011234", 2}, // id_card + phone substring overlap; sanitizer position-dedup handles it
		{"email", "联系邮箱test@example.com", 1},
		{"multiple PII", "手机13800138000，邮箱test@example.com", 2},
		{"no PII", "今天天气真好", 0},
		{"empty", "", 0},
		{"secret key", "API key is sk-abcdef1234567890abcdef12", 1},
		{"internal IP RFC1918", "内网地址192.168.1.1", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.Detect(ctx, tt.text)
			if err != nil {
				t.Fatalf("Detect() error = %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("Detect() got %d fragments, want %d; fragments=%v", len(got), tt.want, got)
			}
		})
	}
}

func TestPatternDetector_FragmentDetails(t *testing.T) {
	d := NewPatternDetector()
	ctx := context.Background()

	got, err := d.Detect(ctx, "手机13800138000，结束")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Detect() got %d fragments, want 1; fragments=%v", len(got), got)
	}
	f := got[0]
	if f.Type != TypePhone {
		t.Errorf("fragment type = %v, want %v", f.Type, TypePhone)
	}
	if f.Value != "13800138000" {
		t.Errorf("fragment value = %v, want %v", f.Value, "13800138000")
	}
	// "手机" is 6 bytes in UTF-8 (3 bytes per Chinese character)
	if f.Start != 6 {
		t.Errorf("fragment start = %d, want 6", f.Start)
	}
	if f.End != 17 {
		t.Errorf("fragment end = %d, want 17", f.End)
	}
}

func TestPatternDetector_NilReceiver(t *testing.T) {
	var d *PatternDetector
	got, err := d.Detect(context.Background(), "13800138000")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Detect() on nil receiver = %v, want empty", got)
	}
}

func TestCustomDetector(t *testing.T) {
	d := NewCustomDetector("test-custom")
	err := d.AddPattern(TypeCustom, `CUS-\d{4,}`)
	if err != nil {
		t.Fatalf("AddPattern() error = %v", err)
	}

	ctx := context.Background()
	got, err := d.Detect(ctx, "订单号CUS-1234已创建")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Detect() got %d fragments, want 1", len(got))
	}
	if got[0].Value != "CUS-1234" {
		t.Errorf("fragment value = %v, want CUS-1234", got[0].Value)
	}
}

func TestCustomDetector_NilReceiver(t *testing.T) {
	var d *CustomDetector
	got, err := d.Detect(context.Background(), "test")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Detect() on nil receiver = %v, want empty", got)
	}
}

func TestCompositeDetector(t *testing.T) {
	pattern := NewPatternDetector()
	custom := NewCustomDetector("contract")
	custom.AddPattern(TypeCustom, `CT-\d{6,}`)

	comp := NewCompositeDetector(pattern, custom)
	ctx := context.Background()

	got, err := comp.Detect(ctx, "手机13800138000，合同CT-123456")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Detect() got %d fragments, want 2; fragments=%v", len(got), got)
	}
}

func TestCompositeDetector_AddDetector(t *testing.T) {
	comp := NewCompositeDetector()
	comp.AddDetector(NewPatternDetector())

	ctx := context.Background()
	got, err := comp.Detect(ctx, "13800138000")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Detect() got %d fragments, want 1", len(got))
	}
}

func TestCompositeDetector_NilReceiver(t *testing.T) {
	var d *CompositeDetector
	got, err := d.Detect(context.Background(), "test")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Detect() on nil receiver = %v, want empty", got)
	}
}

func TestDeduplicateFragments(t *testing.T) {
	input := []SensitiveFragment{
		{Type: TypePhone, Start: 0, End: 11},
		{Type: TypeEmail, Start: 0, End: 11},
		{Type: TypePhone, Start: 12, End: 20},
	}
	got := deduplicateFragments(input)
	if len(got) != 2 {
		t.Fatalf("deduplicateFragments() = %d, want 2", len(got))
	}
	if got[0].Type != TypePhone {
		t.Errorf("first fragment type = %v, want phone", got[0].Type)
	}
	if got[1].Start != 12 {
		t.Errorf("second fragment start = %d, want 12", got[1].Start)
	}
}

func TestDeduplicateFragments_Empty(t *testing.T) {
	got := deduplicateFragments(nil)
	if len(got) != 0 {
		t.Errorf("deduplicateFragments(nil) = %v, want empty", got)
	}
}

func TestDetectorInterfaceCompileTime(t *testing.T) {
	var _ Detector = (*PatternDetector)(nil)
	var _ Detector = (*CustomDetector)(nil)
	var _ Detector = (*CompositeDetector)(nil)
	_ = t
}

func TestPatternDetector_BuildFromFileAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive_patterns.yaml")
	if err := os.WriteFile(path, []byte(`
pii:
  enabled: true
  patterns:
    phone:
      regex: 'PHONE-[0-9]{4}'
    address:
      regex: 'ADDR-[0-9]{4}'
secret:
  enabled: true
  patterns:
    api_key:
      regex: 'KEY-[A-Z]{4}'
`), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	d, err := NewPatternDetectorFromFile(path)
	if err != nil {
		t.Fatalf("NewPatternDetectorFromFile() error = %v", err)
	}
	assertDetectedType(t, d, "PHONE-1234", TypePhone)
	assertDetectedType(t, d, "KEY-ABCD", TypeSecret)
	assertDetectedType(t, d, "ADDR-1234", TypeCustom)

	if err := os.WriteFile(path, []byte(`
pii:
  enabled: true
  patterns:
    email:
      regex: 'MAIL-[A-Z]{3}'
`), 0o600); err != nil {
		t.Fatalf("replace config failed: %v", err)
	}
	if err := d.ReloadFromFile(); err != nil {
		t.Fatalf("ReloadFromFile() error = %v", err)
	}
	assertDetectedType(t, d, "MAIL-ABC", TypeEmail)
	if got, _ := d.Detect(context.Background(), "PHONE-1234"); len(got) != 0 {
		t.Fatalf("stale pattern remained after reload: %v", got)
	}
}

func TestPatternDetector_BuildFromFileRejectsInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive_patterns.yaml")
	if err := os.WriteFile(path, []byte("pii:\n  patterns:\n    phone:\n      regex: '['\n"), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	if _, err := NewPatternDetectorFromFile(path); err == nil {
		t.Fatal("NewPatternDetectorFromFile() should reject invalid regex")
	}
}

func TestPatternDetector_ReloadFailureKeepsLastValidPatterns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive_patterns.yaml")
	if err := os.WriteFile(path, []byte(`
pii:
  enabled: true
  patterns:
    phone:
      regex: 'PHONE-[0-9]{4}'
`), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	d, err := NewPatternDetectorFromFile(path)
	if err != nil {
		t.Fatalf("NewPatternDetectorFromFile() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("pii:\n  patterns:\n    phone:\n      regex: '['\n"), 0o600); err != nil {
		t.Fatalf("replace config failed: %v", err)
	}
	if err := d.ReloadFromFile(); err == nil {
		t.Fatal("ReloadFromFile() should reject invalid config")
	}
	assertDetectedType(t, d, "PHONE-1234", TypePhone)
}

func assertDetectedType(t *testing.T, d *PatternDetector, text string, want SensitiveType) {
	t.Helper()
	got, err := d.Detect(context.Background(), text)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(got) != 1 || got[0].Type != want {
		t.Fatalf("Detect(%q) = %v, want one %s fragment", text, got, want)
	}
}
