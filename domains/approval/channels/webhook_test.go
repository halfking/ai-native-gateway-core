package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

func TestWebhookChannel_SendApprovalNotification(t *testing.T) {
	// Create mock webhook server
	var receivedRequests []webhookRequest
	var mu sync.Mutex
	secret := "test-secret-key"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read body
		body, _ := io.ReadAll(r.Body)

		// Store request
		mu.Lock()
		receivedRequests = append(receivedRequests, webhookRequest{
			method:    r.Method,
			headers:   r.Header.Clone(),
			body:      body,
			signature: r.Header.Get("X-Webhook-Signature"),
		})
		mu.Unlock()

		// Respond success
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "received"}`))
	}))
	defer server.Close()

	// Create webhook channel
	channel := NewWebhookChannel(server.URL, secret)

	// Create test approval request
	req := &approval.ApprovalRequest{
		RequestID:       "req-123",
		SessionID:       "sess-456",
		TenantID:        "tenant-789",
		TriggerType:     approval.TriggerSensitiveContent,
		TriggerReason:   "Detected PII in message",
		RiskLevel:       approval.RiskHigh,
		UserMessage:     "My SSN is 123-45-6789",
		EstimatedCost:   0.05,
		EstimatedTokens: 500,
		Status:          approval.StatusPending,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(1 * time.Hour),
	}

	approvers := []approval.Approver{
		{
			UserID:  "user-1",
			Name:    "John Doe",
			Email:   "john@example.com",
			Role:    "admin",
			Enabled: true,
		},
	}

	// Send notification
	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, approvers)
	if err != nil {
		t.Fatalf("failed to send notification: %v", err)
	}

	// Verify request was received
	mu.Lock()
	defer mu.Unlock()

	if len(receivedRequests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(receivedRequests))
	}

	webhookReq := receivedRequests[0]

	// Verify HTTP method
	if webhookReq.method != http.MethodPost {
		t.Errorf("expected POST method, got %s", webhookReq.method)
	}

	// Verify content type
	contentType := webhookReq.headers.Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", contentType)
	}

	// Verify signature
	if webhookReq.signature == "" {
		t.Error("expected signature header to be present")
	}

	// Verify signature is valid
	expectedSig := generateTestSignature(webhookReq.body, secret)
	if webhookReq.signature != expectedSig {
		t.Errorf("signature mismatch: expected %s, got %s", expectedSig, webhookReq.signature)
	}

	// Verify payload
	var payload WebhookPayload
	err = json.Unmarshal(webhookReq.body, &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.Event != "approval.created" {
		t.Errorf("expected event 'approval.created', got %s", payload.Event)
	}

	if payload.Request.RequestID != req.RequestID {
		t.Errorf("expected request ID %s, got %s", req.RequestID, payload.Request.RequestID)
	}

	if len(payload.Approvers) != len(approvers) {
		t.Errorf("expected %d approvers, got %d", len(approvers), len(payload.Approvers))
	}
}

func TestWebhookChannel_Retry(t *testing.T) {
	attemptCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptCount++
		currentAttempt := attemptCount
		mu.Unlock()

		// Fail first 2 attempts, succeed on 3rd
		if currentAttempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "temporary error"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "received"}`))
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")
	channel.SetMaxRetries(3)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
		SessionID: "sess-456",
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if attemptCount != 3 {
		t.Errorf("expected 3 attempts, got %d", attemptCount)
	}
}

func TestWebhookChannel_MaxRetriesExceeded(t *testing.T) {
	attemptCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptCount++
		mu.Unlock()

		// Always fail
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "persistent error"}`))
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")
	channel.SetMaxRetries(3)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})
	if err == nil {
		t.Fatal("expected error after max retries")
	}

	if !strings.Contains(err.Error(), "failed after") {
		t.Errorf("unexpected error message: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Should attempt: initial + 3 retries = 4 total
	if attemptCount != 4 {
		t.Errorf("expected 4 attempts, got %d", attemptCount)
	}
}

func TestWebhookChannel_NonRetryableError(t *testing.T) {
	attemptCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptCount++
		mu.Unlock()

		// Return 400 Bad Request (non-retryable)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")
	channel.SetMaxRetries(3)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})
	if err == nil {
		t.Fatal("expected error for bad request")
	}

	mu.Lock()
	defer mu.Unlock()

	// Should not retry on 4xx errors
	if attemptCount != 1 {
		t.Errorf("expected 1 attempt for non-retryable error, got %d", attemptCount)
	}
}

func TestWebhookChannel_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow server
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")
	channel.SetTimeout(500 * time.Millisecond)
	channel.SetMaxRetries(0) // No retries for this test

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestWebhookChannel_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestWebhookChannel_SignatureGeneration(t *testing.T) {
	secret := "my-secret-key"
	channel := NewWebhookChannel("http://example.com", secret)

	payload := []byte(`{"test": "data"}`)
	signature := channel.generateSignature(payload)

	// Verify signature is hex-encoded
	_, err := hex.DecodeString(signature)
	if err != nil {
		t.Errorf("signature should be hex-encoded: %v", err)
	}

	// Verify signature length (SHA256 = 32 bytes = 64 hex chars)
	if len(signature) != 64 {
		t.Errorf("expected signature length 64, got %d", len(signature))
	}

	// Verify signature is deterministic
	signature2 := channel.generateSignature(payload)
	if signature != signature2 {
		t.Error("signature generation should be deterministic")
	}

	// Verify different payload produces different signature
	differentPayload := []byte(`{"test": "different"}`)
	differentSignature := channel.generateSignature(differentPayload)
	if signature == differentSignature {
		t.Error("different payloads should produce different signatures")
	}
}

func TestWebhookChannel_EmptySecret(t *testing.T) {
	channel := NewWebhookChannel("http://example.com", "")

	payload := []byte(`{"test": "data"}`)
	signature := channel.generateSignature(payload)

	if signature != "" {
		t.Error("expected empty signature when secret is empty")
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	secret := "test-secret"
	payload := []byte(`{"test": "data"}`)

	// Generate valid signature
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	validSignature := hex.EncodeToString(h.Sum(nil))

	tests := []struct {
		name      string
		payload   []byte
		signature string
		secret    string
		want      bool
	}{
		{
			name:      "valid signature",
			payload:   payload,
			signature: validSignature,
			secret:    secret,
			want:      true,
		},
		{
			name:      "invalid signature",
			payload:   payload,
			signature: "invalid",
			secret:    secret,
			want:      false,
		},
		{
			name:      "wrong secret",
			payload:   payload,
			signature: validSignature,
			secret:    "wrong-secret",
			want:      false,
		},
		{
			name:      "empty secret",
			payload:   payload,
			signature: validSignature,
			secret:    "",
			want:      false,
		},
		{
			name:      "empty signature",
			payload:   payload,
			signature: "",
			secret:    secret,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VerifyWebhookSignature(tt.payload, tt.signature, tt.secret)
			if got != tt.want {
				t.Errorf("VerifyWebhookSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebhookError(t *testing.T) {
	err := &WebhookError{
		StatusCode: 500,
		Status:     "500 Internal Server Error",
		Body:       `{"error": "something went wrong"}`,
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "500") {
		t.Errorf("error message should contain status code: %s", errMsg)
	}

	if !IsWebhookError(err) {
		t.Error("IsWebhookError should return true for WebhookError")
	}

	var genericErr error = nil
	if IsWebhookError(genericErr) {
		t.Error("IsWebhookError should return false for nil error")
	}
}

func TestWebhookChannel_RateLimitRetry(t *testing.T) {
	attemptCount := 0
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attemptCount++
		currentAttempt := attemptCount
		mu.Unlock()

		// Return 429 for first attempt, then succeed
		if currentAttempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": "rate limited"}`))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "received"}`))
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")
	channel.SetMaxRetries(3)

	req := &approval.ApprovalRequest{
		RequestID: "req-123",
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})
	if err != nil {
		t.Fatalf("expected success after retry on rate limit, got error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if attemptCount != 2 {
		t.Errorf("expected 2 attempts (initial + 1 retry), got %d", attemptCount)
	}
}

func TestWebhookChannel_PayloadStructure(t *testing.T) {
	var receivedPayload WebhookPayload
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		json.Unmarshal(body, &receivedPayload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel := NewWebhookChannel(server.URL, "secret")

	now := time.Now()
	req := &approval.ApprovalRequest{
		RequestID:       "req-123",
		SessionID:       "sess-456",
		TenantID:        "tenant-789",
		TriggerType:     approval.TriggerHighCost,
		TriggerReason:   "Cost exceeds threshold",
		RiskLevel:       approval.RiskMedium,
		UserMessage:     "Test message",
		EstimatedCost:   10.50,
		EstimatedTokens: 50000,
		Status:          approval.StatusPending,
		CreatedAt:       now,
		ExpiresAt:       now.Add(2 * time.Hour),
	}

	approvers := []approval.Approver{
		{
			UserID:   "user-1",
			Name:     "Alice",
			Email:    "alice@example.com",
			Role:     "admin",
			Priority: 1,
			Enabled:  true,
		},
		{
			UserID:   "user-2",
			Name:     "Bob",
			Email:    "bob@example.com",
			Role:     "manager",
			Priority: 2,
			Enabled:  true,
		},
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, approvers)
	if err != nil {
		t.Fatalf("failed to send notification: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Verify payload structure
	if receivedPayload.Event != "approval.created" {
		t.Errorf("expected event 'approval.created', got %s", receivedPayload.Event)
	}

	if receivedPayload.Timestamp == 0 {
		t.Error("timestamp should be set")
	}

	if receivedPayload.Request == nil {
		t.Fatal("request should not be nil")
	}

	if receivedPayload.Request.RequestID != req.RequestID {
		t.Errorf("expected request ID %s, got %s", req.RequestID, receivedPayload.Request.RequestID)
	}

	if receivedPayload.Request.EstimatedCost != req.EstimatedCost {
		t.Errorf("expected cost %.2f, got %.2f", req.EstimatedCost, receivedPayload.Request.EstimatedCost)
	}

	if len(receivedPayload.Approvers) != 2 {
		t.Errorf("expected 2 approvers, got %d", len(receivedPayload.Approvers))
	}

	if receivedPayload.Approvers[0].Name != "Alice" {
		t.Errorf("expected first approver name Alice, got %s", receivedPayload.Approvers[0].Name)
	}
}

// Helper types and functions

type webhookRequest struct {
	method    string
	headers   http.Header
	body      []byte
	signature string
}

func generateTestSignature(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
