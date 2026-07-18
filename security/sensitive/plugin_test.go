package sensitive

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/security"
)

func TestPluginBasics(t *testing.T) {
	e := buildTestEngine(t)
	in := NewSensitiveWordInputPlugin(e)
	out := NewSensitiveWordOutputPlugin(e)

	if in.Direction() != security.DirectionInput {
		t.Errorf("input direction = %q", in.Direction())
	}
	if out.Direction() != security.DirectionOutput {
		t.Errorf("output direction = %q", out.Direction())
	}
	if in.Name() != "sensitive_word_input" {
		t.Errorf("input name = %q", in.Name())
	}
	if out.Name() != "sensitive_word_output" {
		t.Errorf("output name = %q", out.Name())
	}
}

func TestPluginInput_Pass(t *testing.T) {
	e := buildTestEngine(t)
	p := NewSensitiveWordInputPlugin(e)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("正常内容"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Allow {
		t.Errorf("expected Allow=true, got Allow=%v", v.Allow)
	}
}

func TestPluginInput_Block(t *testing.T) {
	e := buildTestEngine(t)
	p := NewSensitiveWordInputPlugin(e)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("这是一个色情视频"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Allow {
		t.Errorf("expected Allow=false for P0")
	}
	if v.Severity != 2 {
		t.Errorf("expected severity=2 for P0, got %d", v.Severity)
	}
	if v.Code != "sensitive_word.P0" {
		t.Errorf("code = %q", v.Code)
	}
}

func TestPluginInput_NilBody(t *testing.T) {
	e := buildTestEngine(t)
	p := NewSensitiveWordInputPlugin(e)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Allow {
		t.Errorf("expected Allow=true for nil body")
	}
}

func TestPluginOutput_Pass(t *testing.T) {
	e := buildTestEngine(t)
	p := NewSensitiveWordOutputPlugin(e)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{
		UpstreamResponse: []byte("正常响应"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Allow {
		t.Errorf("expected Allow=true")
	}
}

func TestPluginOutput_Block(t *testing.T) {
	e := buildTestEngine(t)
	p := NewSensitiveWordOutputPlugin(e)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{
		UpstreamResponse: []byte("输出的暴力内容"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Allow {
		t.Errorf("expected Allow=false for P0 output")
	}
}

func TestPluginEvidence(t *testing.T) {
	e := buildTestEngine(t)
	p := NewSensitiveWordInputPlugin(e)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("包含色情和暴力内容"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Evidence == nil {
		t.Fatal("expected non-nil Evidence")
	}
	words, ok := v.Evidence["matched_words"].([]string)
	if !ok {
		t.Fatalf("matched_words not a []string: %T", v.Evidence["matched_words"])
	}
	if len(words) < 2 {
		t.Errorf("expected >=2 matched words, got %v", words)
	}
}

func TestPluginNilEngine(t *testing.T) {
	p := NewSensitiveWordInputPlugin(nil)
	v, err := p.Inspect(context.Background(), &domain.PipelineRequest{
		TransformedRequest: []byte("anything"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !v.Allow {
		t.Errorf("expected Allow=true when engine is nil")
	}
}

func TestPluginDirection(t *testing.T) {
	e := buildTestEngine(t)
	in := NewSensitiveWordInputPlugin(e)
	out := NewSensitiveWordOutputPlugin(e)
	if in.Direction() != "input" {
		t.Errorf("expected input, got %s", in.Direction())
	}
	if out.Direction() != "output" {
		t.Errorf("expected output, got %s", out.Direction())
	}
}

func TestPluginSeverityMap(t *testing.T) {
	tests := []struct {
		level AlertLevel
		want  int
	}{
		{LevelP0, 2},
		{LevelP1, 1},
		{LevelP2, 0},
	}
	for _, tc := range tests {
		got := worstLevelToSeverity(tc.level)
		if got != tc.want {
			t.Errorf("worstLevelToSeverity(%v) = %d, want %d", tc.level, got, tc.want)
		}
	}
}
