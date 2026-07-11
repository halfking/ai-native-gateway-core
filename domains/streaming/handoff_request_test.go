package streaming

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedHandoffUpstreamAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer upstream-key")

	if got := trustedHandoffUpstreamAPIKey(true, req); got != "" {
		t.Fatalf("gateway key must not be forwarded, got %q", got)
	}
	if got := trustedHandoffUpstreamAPIKey(false, req); got != "upstream-key" {
		t.Fatalf("expected direct upstream key, got %q", got)
	}
}
