package anthropic

import "testing"

func TestIsJSONErrorBodyRequiresErrorSignal(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "bare message metadata", body: `{"message":"normal metadata"}`, want: false},
		{name: "wrapped error type", body: `{"error":{"type":"service_unavailable","message":"try later"}}`, want: true},
		{name: "wrapped error code", body: `{"error":{"code":"insufficient_quota","message":"quota"}}`, want: true},
		{name: "bare type", body: `{"type":"upstream_error","message":"failed"}`, want: true},
		{name: "bare code", body: `{"code":"rate_limit","message":"slow down"}`, want: true},
		{name: "invalid json", body: `{"message":`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _ := isJSONErrorBody([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("isJSONErrorBody(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
