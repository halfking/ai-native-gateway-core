package relay

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/audit"
)

func TestExtractAnthropicStreamUsage_WithValues(t *testing.T) {
	in := 42
	out := 17
	capture := &audit.StreamCapture{
		InputTokens:  &in,
		OutputTokens: &out,
	}
	gotIn, gotOut := extractAnthropicStreamUsage(capture)
	if gotIn == nil || *gotIn != 42 {
		t.Errorf("InputTokens = %v, want pointer to 42", gotIn)
	}
	if gotOut == nil || *gotOut != 17 {
		t.Errorf("OutputTokens = %v, want pointer to 17", gotOut)
	}
}

func TestExtractAnthropicStreamUsage_NilCapture(t *testing.T) {
	gotIn, gotOut := extractAnthropicStreamUsage(nil)
	if gotIn != nil {
		t.Errorf("InputTokens = %v, want nil", gotIn)
	}
	if gotOut != nil {
		t.Errorf("OutputTokens = %v, want nil", gotOut)
	}
}

func TestExtractAnthropicStreamUsage_EmptyCapture(t *testing.T) {
	capture := &audit.StreamCapture{}
	gotIn, gotOut := extractAnthropicStreamUsage(capture)
	if gotIn != nil {
		t.Errorf("InputTokens = %v, want nil for empty capture", gotIn)
	}
	if gotOut != nil {
		t.Errorf("OutputTokens = %v, want nil for empty capture", gotOut)
	}
}
