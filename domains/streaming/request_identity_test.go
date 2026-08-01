package streaming

import (
	"net/http/httptest"
	"testing"
)

func TestInitializeRequestIdentityUsesMiddlewareContract(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Request-Id", "server-id")
	req.Header.Set("X-Gw-Client-Request-Id", "client-id")
	req.Header.Set("X-Gw-Session-Id", "gw_existing")
	req.Header.Set("X-Gw-Client-Type", "zcode")
	got := initializeRequestIdentity(req)
	if got.RequestID != "server-id" || got.ClientRequestID != "client-id" || got.SessionID != "gw_existing" {
		t.Fatalf("identity mismatch: %+v", got)
	}
}

func TestInitializeRequestIdentityDirectInvocationProducesStableIDs(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	first := initializeRequestIdentity(req)
	second := initializeRequestIdentity(req)
	if first.RequestID == "" || first.SessionID == "" {
		t.Fatalf("missing ids: %+v", first)
	}
	if first.RequestID != second.RequestID || first.SessionID != second.SessionID {
		t.Fatalf("ids changed: first=%+v second=%+v", first, second)
	}
	if req.Header.Get("X-Request-Id") != first.RequestID || req.Header.Get("X-Gw-Session-Id") != first.SessionID {
		t.Fatalf("request headers not updated")
	}
}
