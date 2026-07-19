package routingtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientChatRecordsPendingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Gw-Session-Id") != "session-1" {
			t.Fatalf("missing session header")
		}
		writer.Header().Set("X-Gw-Pending", "session-1")
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	result := NewClient(server.URL, "test-key", time.Second).Chat(context.Background(), Request{
		Model:    "minimax-m3",
		Messages: []Message{{Role: "user", Content: "ping"}},
	}, "session-1", 1)

	if result.Status != http.StatusAccepted || !result.Pending || result.Error != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientChatRecordsGatewayError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":{"type":"upstream_down","message":"unavailable"}}`))
	}))
	defer server.Close()

	result := NewClient(server.URL, "test-key", time.Second).Chat(context.Background(), Request{
		Model:    "glm-5.2",
		Messages: []Message{{Role: "user", Content: "ping"}},
	}, "", 1)

	if result.Status != http.StatusBadGateway || result.Error != "upstream_down: unavailable" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
