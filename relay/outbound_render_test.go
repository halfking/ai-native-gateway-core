package relay

import "testing"

func TestOutboundModelForLog(t *testing.T) {
	tests := []struct {
		name      string
		client    string
		explicit  string
		candidate string
		want      string
	}{
		{
			name:      "transform differs from client",
			client:    "minimax-m3",
			explicit:  "MiniMax-M3",
			candidate: "MiniMax-M3",
			want:      "MiniMax-M3",
		},
		{
			name:      "same explicit uses candidate raw",
			client:    "minimax-m3",
			explicit:  "minimax-m3",
			candidate: "MiniMax-M3",
			want:      "MiniMax-M3",
		},
		{
			name:      "empty explicit uses candidate raw",
			client:    "glm-5.1",
			explicit:  "",
			candidate: "z-ai/glm-5.1",
			want:      "z-ai/glm-5.1",
		},
		{
			name:      "all same falls back to client",
			client:    "mimo-v2.5-pro",
			explicit:  "mimo-v2.5-pro",
			candidate: "mimo-v2.5-pro",
			want:      "mimo-v2.5-pro",
		},
		{
			name:      "explicit differs by case only",
			client:    "MiniMax-M3",
			explicit:  "minimax-m3",
			candidate: "MiniMax-M3",
			want:      "minimax-m3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outboundModelForLog(tt.client, tt.explicit, tt.candidate)
			if got != tt.want {
				t.Fatalf("outboundModelForLog(%q, %q, %q) = %q, want %q",
					tt.client, tt.explicit, tt.candidate, got, tt.want)
			}
		})
	}
}
