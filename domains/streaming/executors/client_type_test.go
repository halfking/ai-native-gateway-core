package executors

import (
	"net/http/httptest"
	"testing"
)

func TestClientTokenUsesBoundedClientType(t *testing.T) {
	for _, tt := range []struct {
		header string
		want   string
	}{
		{" Cursor ", "alice|cursor"},
		{"CLAUDE-CODE", "alice|claude-code"},
		{"my-custom-agent", "alice|unknown"},
		{"cursor|other", "alice|unknown"},
		{"   ", "alice|unknown"},
	} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		req.Header.Set("X-Gw-Client-Type", tt.header)
		if got := clientTokenOf("alice", extractClientType(req)); got != tt.want {
			t.Errorf("header %q produced holder %q, want %q", tt.header, got, tt.want)
		}
	}
}
