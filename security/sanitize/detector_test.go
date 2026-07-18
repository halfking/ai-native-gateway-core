package sanitize

import (
	"context"
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
