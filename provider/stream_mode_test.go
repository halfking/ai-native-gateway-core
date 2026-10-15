package provider

import "testing"

func TestStreamModeAllowsRequestedTransport(t *testing.T) {
	tests := []struct {
		name     string
		mode     StreamMode
		stream   bool
		want     bool
		wantMode StreamMode
	}{
		{name: "legacy empty defaults to both non-stream", mode: "", stream: false, want: true, wantMode: StreamModeBoth},
		{name: "legacy empty defaults to both stream", mode: "", stream: true, want: true, wantMode: StreamModeBoth},
		{name: "both non-stream", mode: StreamModeBoth, stream: false, want: true, wantMode: StreamModeBoth},
		{name: "both stream", mode: StreamModeBoth, stream: true, want: true, wantMode: StreamModeBoth},
		{name: "stream-only stream", mode: StreamModeStreamOnly, stream: true, want: true, wantMode: StreamModeStreamOnly},
		{name: "stream-only rejects non-stream", mode: StreamModeStreamOnly, stream: false, want: false, wantMode: StreamModeStreamOnly},
		{name: "non-stream-only non-stream", mode: StreamModeNonStreamOnly, stream: false, want: true, wantMode: StreamModeNonStreamOnly},
		{name: "non-stream-only rejects stream", mode: StreamModeNonStreamOnly, stream: true, want: false, wantMode: StreamModeNonStreamOnly},
		{name: "unknown mode fails closed", mode: StreamMode("unexpected"), stream: true, want: false, wantMode: StreamMode("unexpected")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.Normalized(); got != tt.wantMode {
				t.Fatalf("Normalized() = %q, want %q", got, tt.wantMode)
			}
			if got := tt.mode.Allows(tt.stream); got != tt.want {
				t.Fatalf("Allows(stream=%v) = %v, want %v", tt.stream, got, tt.want)
			}
		})
	}
}

func TestParseStreamModeRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"both", "stream_only", "non_stream_only"} {
		mode, err := ParseStreamMode(value)
		if err != nil {
			t.Fatalf("ParseStreamMode(%q): %v", value, err)
		}
		if string(mode) != value {
			t.Fatalf("ParseStreamMode(%q) = %q", value, mode)
		}
	}

	if _, err := ParseStreamMode("sometimes"); err == nil {
		t.Fatal("ParseStreamMode accepted an unknown value")
	}
}
