package errorsx

import "testing"

// TestFinishReasonVendorFailureKind pins the GLM error-channel detection
// (Wave4-D3 single table): the three canonical values plus misspelling
// tolerance, with canonical completion values staying unmatched.
func TestFinishReasonVendorFailureKind(t *testing.T) {
	tests := []struct {
		name string
		fr   string
		want ErrorKind
		ok   bool
	}{
		{"canonical network_error", "network_error", KindNetwork, true},
		{"canonical sensitive", "sensitive", KindContentFilter, true},
		{"canonical model_context_window_exceeded", "model_context_window_exceeded", KindContextLength, true},
		{"misspelled exeated tail", "model_context_window_exeated", KindContextLength, true},
		{"bare misspelling exeated", "exeated", KindContextLength, true},
		{"exeeded variant", "context_window_exeeded", KindContextLength, true},
		{"misspelled sensitive", "sensetive", KindContentFilter, true},
		{"misspelled network_eror", "network_eror", KindNetwork, true},
		{"case/whitespace normalized", "  Network_Error ", KindNetwork, true},
		{"completion stop", "stop", "", false},
		{"completion length", "length", "", false},
		{"completion tool_calls", "tool_calls", "", false},
		{"abnormal completion content_filter is NOT failure", "content_filter", "", false},
		{"abnormal completion refusal is NOT failure", "refusal", "", false},
		{"context_length_exceeded is NOT vendor failure", "context_length_exceeded", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := FinishReasonVendorFailureKind(tt.fr)
			if ok != tt.ok || kind != tt.want {
				t.Fatalf("FinishReasonVendorFailureKind(%q) = (%q,%v), want (%q,%v)", tt.fr, kind, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestFinishReasonAbnormalKind pins the refuse-clean-success set: the vendor
// failure channel plus abnormal-but-parseable completions.
func TestFinishReasonAbnormalKind(t *testing.T) {
	tests := []struct {
		fr   string
		want ErrorKind
		ok   bool
	}{
		{"content_filter", KindContentFilter, true},
		{"refusal", KindContentFilter, true},
		{"error", KindUpstreamDown, true},
		{"context_length_exceeded", KindContextLength, true},
		{"network_error", KindNetwork, true},
		{"sensitive", KindContentFilter, true},
		{"model_context_window_exceeded", KindContextLength, true},
		{"stop", "", false},
		{"length", "", false},
		{"tool_calls", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		kind, ok := FinishReasonAbnormalKind(tt.fr)
		if ok != tt.ok || kind != tt.want {
			t.Fatalf("FinishReasonAbnormalKind(%q) = (%q,%v), want (%q,%v)", tt.fr, kind, ok, tt.want, tt.ok)
		}
	}
}

// TestMiniMaxBaseRespStatusCodeKind pins the base_resp status-code table
// (moved verbatim from vendorstrip).
func TestMiniMaxBaseRespStatusCodeKind(t *testing.T) {
	tests := []struct {
		code int
		want ErrorKind
	}{
		{0, ""},
		{1002, KindRateLimit},
		{1004, KindAuth},
		{1008, KindQuota},
		{1027, KindContentFilter},
		{1039, KindContextLength},
		{1001, KindTimeout},
		{2013, KindClientBug},
		{1000, KindUpstreamDown},
		{1013, KindUpstreamDown},
		{9999, KindUpstreamDown},
	}
	for _, tt := range tests {
		if got := MiniMaxBaseRespStatusCodeKind(tt.code); got != tt.want {
			t.Fatalf("MiniMaxBaseRespStatusCodeKind(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}
