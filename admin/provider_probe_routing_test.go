package admin

import "testing"

func TestProbeSuccessEnrollsDiscoveredModels(t *testing.T) {
	tests := []struct {
		name   string
		source string
		models []string
		want   bool
	}{
		{name: "live API models", source: "api", models: []string{"tplink-chat"}, want: true},
		{name: "live API plus manifest", source: "api+manifest", models: []string{"tplink-chat"}, want: true},
		{name: "manifest fallback", source: "manifest_only", models: []string{"tplink-chat"}, want: true},
		{name: "probe failed", source: "none", models: []string{"tplink-chat"}, want: false},
		{name: "no models", source: "api", models: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probeModelsEligibleForRouting(tt.source, tt.models); got != tt.want {
				t.Fatalf("probeModelsEligibleForRouting(%q, %v) = %v, want %v", tt.source, tt.models, got, tt.want)
			}
		})
	}
}
