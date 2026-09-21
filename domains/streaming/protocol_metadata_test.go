package streaming

import "testing"

func TestProtocolConversionFlag(t *testing.T) {
	tests := []struct {
		name, client, upstream string
		want                   *bool
	}{
		{name: "openai to anthropic", client: "openai-completions", upstream: "anthropic-messages", want: protocolBoolPtr(true)},
		{name: "same protocol", client: "anthropic-messages", upstream: "anthropic-messages", want: protocolBoolPtr(false)},
		{name: "missing client", upstream: "anthropic-messages"},
		{name: "missing upstream", client: "openai-completions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocolConversionFlag(tt.client, tt.upstream)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("protocolConversionFlag() = %v, want %v", got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Fatalf("protocolConversionFlag() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func protocolBoolPtr(v bool) *bool { return &v }
