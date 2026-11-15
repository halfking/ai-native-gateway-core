package casdoor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVerifyWebhookSignatureV2(t *testing.T) {
	client := NewClient(Config{
		CallbackSecret: "test-secret",
		CallbackKid:    "test-kid-1",
	})

	body := []byte(`{"eventId":"evt-123","status":"approved"}`)
	eventID := "evt-123"
	timestamp := time.Now().Format(time.RFC3339)

	// Calculate valid signature
	bodyHash := sha256.Sum256(body)
	message := "v1\n" + eventID + "\n" + timestamp + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(message))
	validSignature := "v1=" + hex.EncodeToString(mac.Sum(nil))

	t.Run("valid signature", func(t *testing.T) {
		err := client.VerifyWebhookSignatureV2(body, validSignature, eventID, timestamp, "test-kid-1")
		if err != nil {
			t.Errorf("VerifyWebhookSignatureV2() error = %v, want nil", err)
		}
	})

	t.Run("invalid signature", func(t *testing.T) {
		err := client.VerifyWebhookSignatureV2(body, "v1=invalid", eventID, timestamp, "test-kid-1")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for invalid signature")
		}
	})

	t.Run("kid mismatch", func(t *testing.T) {
		err := client.VerifyWebhookSignatureV2(body, validSignature, eventID, timestamp, "wrong-kid")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for kid mismatch")
		}
		if err != nil && err.Error() != "kid mismatch: expected \"test-kid-1\", got \"wrong-kid\"" {
			t.Errorf("Error message = %v", err)
		}
	})

	t.Run("timestamp too old", func(t *testing.T) {
		oldTimestamp := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
		
		// Recalculate signature with old timestamp
		message := "v1\n" + eventID + "\n" + oldTimestamp + "\n" + hex.EncodeToString(bodyHash[:])
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write([]byte(message))
		oldSignature := "v1=" + hex.EncodeToString(mac.Sum(nil))

		err := client.VerifyWebhookSignatureV2(body, oldSignature, eventID, oldTimestamp, "test-kid-1")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for old timestamp")
		}
	})

	t.Run("timestamp in future", func(t *testing.T) {
		futureTimestamp := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
		
		bodyHash := sha256.Sum256(body)
		message := "v1\n" + eventID + "\n" + futureTimestamp + "\n" + hex.EncodeToString(bodyHash[:])
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write([]byte(message))
		futureSignature := "v1=" + hex.EncodeToString(mac.Sum(nil))

		err := client.VerifyWebhookSignatureV2(body, futureSignature, eventID, futureTimestamp, "test-kid-1")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for future timestamp")
		}
	})

	t.Run("invalid timestamp format", func(t *testing.T) {
		err := client.VerifyWebhookSignatureV2(body, validSignature, eventID, "invalid-timestamp", "test-kid-1")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for invalid timestamp format")
		}
	})

	t.Run("wrong signature format", func(t *testing.T) {
		err := client.VerifyWebhookSignatureV2(body, "invalid-format", eventID, timestamp, "test-kid-1")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for wrong signature format")
		}
	})

	t.Run("empty kid when not configured", func(t *testing.T) {
		clientNoKid := NewClient(Config{
			CallbackSecret: "test-secret",
		})

		err := clientNoKid.VerifyWebhookSignatureV2(body, validSignature, eventID, timestamp, "")
		if err != nil {
			t.Errorf("VerifyWebhookSignatureV2() error = %v, want nil when kid not configured", err)
		}
	})

	t.Run("body tampered", func(t *testing.T) {
		tamperedBody := []byte(`{"eventId":"evt-123","status":"rejected"}`)
		err := client.VerifyWebhookSignatureV2(tamperedBody, validSignature, eventID, timestamp, "test-kid-1")
		if err == nil {
			t.Error("VerifyWebhookSignatureV2() expected error for tampered body")
		}
	})
}

func TestParseWebhookEvent(t *testing.T) {
	t.Run("valid event", func(t *testing.T) {
		body := []byte(`{
			"eventId": "evt-123",
			"eventVersion": 2,
			"org": "admin",
			"application": "test-app",
			"taskId": 456,
			"externalId": "sess-123:req-456",
			"status": "approved",
			"action": "approve",
			"actor": "admin/approver1",
			"resourceType": "llm_session",
			"resourceId": "sess-123",
			"decisionVersion": 1,
			"comment": "looks good"
		}`)

		event, err := ParseWebhookEvent(body)
		if err != nil {
			t.Fatalf("ParseWebhookEvent() error = %v", err)
		}

		if event.EventID != "evt-123" {
			t.Errorf("EventID = %v, want evt-123", event.EventID)
		}
		if event.TaskID != 456 {
			t.Errorf("TaskID = %v, want 456", event.TaskID)
		}
		if event.Status != "approved" {
			t.Errorf("Status = %v, want approved", event.Status)
		}
		if event.ExternalID != "sess-123:req-456" {
			t.Errorf("ExternalID = %v, want sess-123:req-456", event.ExternalID)
		}
		if event.Actor != "admin/approver1" {
			t.Errorf("Actor = %v, want admin/approver1", event.Actor)
		}
		if event.Comment != "looks good" {
			t.Errorf("Comment = %v, want 'looks good'", event.Comment)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		body := []byte(`{invalid json}`)
		_, err := ParseWebhookEvent(body)
		if err == nil {
			t.Error("ParseWebhookEvent() expected error for invalid json")
		}
	})

	t.Run("minimal event", func(t *testing.T) {
		body := []byte(`{"eventId":"evt-1","status":"rejected"}`)
		event, err := ParseWebhookEvent(body)
		if err != nil {
			t.Fatalf("ParseWebhookEvent() error = %v", err)
		}
		if event.EventID != "evt-1" {
			t.Errorf("EventID = %v, want evt-1", event.EventID)
		}
		if event.Status != "rejected" {
			t.Errorf("Status = %v, want rejected", event.Status)
		}
	})
}

func TestHandleWebhook(t *testing.T) {
	client := NewClient(Config{
		CallbackSecret: "test-secret",
		CallbackKid:    "test-kid",
	})

	t.Run("valid webhook", func(t *testing.T) {
		eventData := WebhookEvent{
			EventID:    "evt-123",
			Status:     "approved",
			ExternalID: "sess-123:req-456",
		}
		body, _ := json.Marshal(eventData)

		// Calculate signature
		timestamp := time.Now().Format(time.RFC3339)
		bodyHash := sha256.Sum256(body)
		message := "v1\n" + eventData.EventID + "\n" + timestamp + "\n" + hex.EncodeToString(bodyHash[:])
		mac := hmac.New(sha256.New, []byte("test-secret"))
		mac.Write([]byte(message))
		signature := "v1=" + hex.EncodeToString(mac.Sum(nil))

		// Create request
		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-Casdoor-Signature", signature)
		req.Header.Set("X-Casdoor-Event-Id", eventData.EventID)
		req.Header.Set("X-Casdoor-Timestamp", timestamp)
		req.Header.Set("X-Casdoor-Kid", "test-kid")

		event, err := client.HandleWebhook(req)
		if err != nil {
			t.Fatalf("HandleWebhook() error = %v", err)
		}

		if event.EventID != "evt-123" {
			t.Errorf("Event.EventID = %v, want evt-123", event.EventID)
		}
		if event.Status != "approved" {
			t.Errorf("Event.Status = %v, want approved", event.Status)
		}
	})

	t.Run("signature verification failure", func(t *testing.T) {
		body := []byte(`{"eventId":"evt-123","status":"approved"}`)

		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		req.Header.Set("X-Casdoor-Signature", "v1=invalid")
		req.Header.Set("X-Casdoor-Event-Id", "evt-123")
		req.Header.Set("X-Casdoor-Timestamp", time.Now().Format(time.RFC3339))
		req.Header.Set("X-Casdoor-Kid", "test-kid")

		_, err := client.HandleWebhook(req)
		if err == nil {
			t.Error("HandleWebhook() expected signature verification error")
		}
	})

	t.Run("body too large", func(t *testing.T) {
		// Create a body larger than 1MB
		largeBody := bytes.Repeat([]byte("x"), 2*1024*1024)

		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(largeBody))
		req.Header.Set("X-Casdoor-Signature", "v1=anything")
		req.Header.Set("X-Casdoor-Event-Id", "evt-123")
		req.Header.Set("X-Casdoor-Timestamp", time.Now().Format(time.RFC3339))
		req.Header.Set("X-Casdoor-Kid", "test-kid")

		// Should handle gracefully (signature will fail on truncated body)
		_, err := client.HandleWebhook(req)
		if err == nil {
			t.Error("HandleWebhook() expected error for large body")
		}
	})

	t.Run("missing headers", func(t *testing.T) {
		body := []byte(`{"eventId":"evt-123","status":"approved"}`)

		req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(body))
		// Missing headers

		_, err := client.HandleWebhook(req)
		if err == nil {
			t.Error("HandleWebhook() expected error for missing headers")
		}
	})
}

func TestWebhookIdempotency(t *testing.T) {
	// Test that the same eventId can be processed multiple times
	// (actual idempotency is in the provider layer, this just tests parsing)

	eventData := WebhookEvent{
		EventID:    "evt-same",
		Status:     "approved",
		ExternalID: "sess-123:req-456",
	}

	body1, _ := json.Marshal(eventData)
	body2, _ := json.Marshal(eventData)

	event1, _ := ParseWebhookEvent(body1)
	event2, _ := ParseWebhookEvent(body2)

	if event1.EventID != event2.EventID {
		t.Error("EventID should be same for idempotency")
	}
}
