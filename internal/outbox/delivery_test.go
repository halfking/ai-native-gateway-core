package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPDeliverer_DeliversContractBatchWithExactSignature(t *testing.T) {
	fixedNow := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
	const nonce = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get(HeaderTimestamp); got != "2026-08-15T04:05:06Z" {
			t.Errorf("%s = %q", HeaderTimestamp, got)
		}
		if got := r.Header.Get(HeaderNonce); got != nonce {
			t.Errorf("%s = %q", HeaderNonce, got)
		}
		if got := r.Header.Get(HeaderCorrelationID); got != "corr-001" {
			t.Errorf("%s = %q", HeaderCorrelationID, got)
		}
		if got := r.Header.Get(HeaderSignature); !VerifySignature("test-secret", r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderNonce), receivedBody, got) {
			t.Errorf("invalid %s = %q", HeaderSignature, got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":{"accepted":1,"duplicates":0,"failed":[]}}`)
	}))
	defer server.Close()

	deliverer := NewHTTPDeliverer(HTTPDelivererConfig{
		Endpoint:   server.URL + "/internal/v1/events",
		Secret:     "test-secret",
		HTTPClient: server.Client(),
		Now:        func() time.Time { return fixedNow },
		Nonce:      func() (string, error) { return nonce, nil },
	})
	env := EventEnvelope{
		EventID:          "evt-001",
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         "tenant-001",
		AggregateID:      "session-001",
		AggregateVersion: 1,
		OccurredAt:       time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC),
		RequestID:        "request-001",
		CorrelationID:    "corr-001",
		SourceSystem:     "gateway",
		Payload:          map[string]any{"status": "success"},
	}
	if err := deliverer.Deliver(context.Background(), env); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(receivedBody, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events length = %d, want 1", len(body.Events))
	}
	event := body.Events[0]
	if event["schema_version"] != "1.0" {
		t.Errorf("schema_version = %v, want 1.0", event["schema_version"])
	}
	if event["type"] != "request.completed.v1" {
		t.Errorf("type = %v, want request.completed.v1", event["type"])
	}
	if _, exists := event["event_type"]; exists {
		t.Error("wire event must use type, not event_type")
	}
}

func TestHTTPDelivererRejectsFailedOrMalformedAcknowledgment(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "failed event", body: `{"data":{"accepted":0,"duplicates":0,"failed":[{"event_id":"evt-1","code":"event_dead_lettered","message":"terminal"}]}}`},
		{name: "malformed success", body: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			err := NewHTTPDeliverer(HTTPDelivererConfig{Endpoint: server.URL, Secret: "secret", HTTPClient: server.Client()}).Deliver(context.Background(), EventEnvelope{EventID: "evt-1", EventType: "request.completed.v1", TenantID: "tenant-1", AggregateID: "session-1", OccurredAt: time.Now(), Payload: map[string]any{}})
			if err == nil {
				t.Fatal("Deliver unexpectedly accepted acknowledgment")
			}
		})
	}
}

func TestHTTPDelivererDuplicateRequiresExplicitOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":{"accepted":0,"duplicates":1,"failed":[]}}`)
	}))
	defer server.Close()
	env := EventEnvelope{EventID: "evt-1", EventType: "request.completed.v1", TenantID: "tenant-1", AggregateID: "session-1", OccurredAt: time.Now(), Payload: map[string]any{}}
	if err := NewHTTPDeliverer(HTTPDelivererConfig{Endpoint: server.URL, Secret: "secret", HTTPClient: server.Client()}).Deliver(context.Background(), env); err == nil {
		t.Fatal("manual replay deliverer accepted duplicate")
	}
	if err := NewHTTPDeliverer(HTTPDelivererConfig{Endpoint: server.URL, Secret: "secret", AcceptDuplicate: true, HTTPClient: server.Client()}).Deliver(context.Background(), env); err != nil {
		t.Fatalf("dispatcher deliverer rejected duplicate: %v", err)
	}
}

func TestDeliveryRetryableClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "success", err: nil, want: false},
		{name: "network", err: io.ErrUnexpectedEOF, want: true},
		{name: "401", err: &DeliveryError{StatusCode: http.StatusUnauthorized}, want: false},
		{name: "409", err: &DeliveryError{StatusCode: http.StatusConflict}, want: false},
		{name: "422", err: &DeliveryError{StatusCode: http.StatusUnprocessableEntity}, want: false},
		{name: "429", err: &DeliveryError{StatusCode: http.StatusTooManyRequests}, want: false},
		{name: "500", err: &DeliveryError{StatusCode: http.StatusInternalServerError}, want: true},
		{name: "503 wrapped", err: fmt.Errorf("deliver: %w", &DeliveryError{StatusCode: http.StatusServiceUnavailable}), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Retryable(tt.err); got != tt.want {
				t.Fatalf("Retryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDeliveryErrorBoundsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	}))
	defer server.Close()

	deliverer := NewHTTPDeliverer(HTTPDelivererConfig{
		Endpoint:   server.URL,
		Secret:     "test-secret",
		HTTPClient: server.Client(),
	})
	err := deliverer.Deliver(context.Background(), EventEnvelope{
		EventID: "evt-bounded", EventType: "request.completed.v1", TenantID: "tenant-1",
		AggregateID: "session-1", AggregateVersion: 1, OccurredAt: time.Now(), Payload: map[string]any{},
	})
	var deliveryErr *DeliveryError
	if !AsDeliveryError(err, &deliveryErr) {
		t.Fatalf("Deliver error = %T %v, want DeliveryError", err, err)
	}
	if len(deliveryErr.Body) != 4096 {
		t.Fatalf("response body length = %d, want 4096", len(deliveryErr.Body))
	}
}
