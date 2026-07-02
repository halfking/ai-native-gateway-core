package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// MockApprovalManager is a simple mock implementation without external dependencies.
type MockApprovalManager struct {
	approveFunc            func(ctx context.Context, requestID, tenantID, approvedBy, reason string) error
	rejectFunc             func(ctx context.Context, requestID, tenantID, approvedBy, reason string) error
	getApprovalByRequestID func(ctx context.Context, requestID string) (string, error)
	
	// Track calls for verification
	approveCalls []approveCall
	rejectCalls  []rejectCall
}

type approveCall struct {
	requestID  string
	tenantID   string
	approvedBy string
	reason     string
}

type rejectCall struct {
	requestID  string
	tenantID   string
	approvedBy string
	reason     string
}

func (m *MockApprovalManager) Approve(ctx context.Context, requestID, tenantID, approvedBy, reason string) error {
	m.approveCalls = append(m.approveCalls, approveCall{requestID, tenantID, approvedBy, reason})
	if m.approveFunc != nil {
		return m.approveFunc(ctx, requestID, tenantID, approvedBy, reason)
	}
	return nil
}

func (m *MockApprovalManager) Reject(ctx context.Context, requestID, tenantID, approvedBy, reason string) error {
	m.rejectCalls = append(m.rejectCalls, rejectCall{requestID, tenantID, approvedBy, reason})
	if m.rejectFunc != nil {
		return m.rejectFunc(ctx, requestID, tenantID, approvedBy, reason)
	}
	return nil
}

func (m *MockApprovalManager) GetApprovalByRequestID(ctx context.Context, requestID string) (string, error) {
	if m.getApprovalByRequestID != nil {
		return m.getApprovalByRequestID(ctx, requestID)
	}
	return "tenant_default", nil
}

func TestNewFeishuCallbackHandler(t *testing.T) {
	mockManager := &MockApprovalManager{}
	config := FeishuCallbackConfig{
		Manager:     mockManager,
		VerifyToken: "test_token",
		EncryptKey:  "test_key",
	}

	handler := NewFeishuCallbackHandler(config)
	if handler == nil {
		t.Fatal("handler should not be nil")
	}
	if handler.manager != mockManager {
		t.Error("manager not set correctly")
	}
	if handler.verifyToken != "test_token" {
		t.Error("verifyToken not set correctly")
	}
	if handler.encryptKey != "test_key" {
		t.Error("encryptKey not set correctly")
	}
}

func TestFeishuCallbackHandler_HandleCallback_MethodNotAllowed(t *testing.T) {
	mockManager := &MockApprovalManager{}
	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/webhooks/feishu/approval-callback", nil)
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
	if !strings.Contains(w.Body.String(), "method not allowed") {
		t.Error("response should contain 'method not allowed'")
	}
}

func TestFeishuCallbackHandler_HandleURLVerification(t *testing.T) {
	mockManager := &MockApprovalManager{}
	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type:      "url_verification",
		Challenge: "test_challenge_123",
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if response["challenge"] != "test_challenge_123" {
		t.Errorf("expected challenge 'test_challenge_123', got '%s'", response["challenge"])
	}
}

func TestFeishuCallbackHandler_HandleEventCallback_Approve(t *testing.T) {
	mockManager := &MockApprovalManager{}
	mockManager.getApprovalByRequestID = func(ctx context.Context, requestID string) (string, error) {
		if requestID == "req_123" {
			return "tenant_456", nil
		}
		return "", errors.New("not found")
	}

	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			Type:   "card.action.trigger",
			UserID: "user_789",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=approve&request_id=req_123", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if response["success"] != true {
		t.Error("expected success=true")
	}
	if response["action"] != "approved" {
		t.Errorf("expected action='approved', got '%v'", response["action"])
	}

	// Verify Approve was called
	if len(mockManager.approveCalls) != 1 {
		t.Errorf("expected 1 approve call, got %d", len(mockManager.approveCalls))
	} else {
		call := mockManager.approveCalls[0]
		if call.requestID != "req_123" {
			t.Errorf("expected requestID 'req_123', got '%s'", call.requestID)
		}
		if call.tenantID != "tenant_456" {
			t.Errorf("expected tenantID 'tenant_456', got '%s'", call.tenantID)
		}
		if call.approvedBy != "user_789" {
			t.Errorf("expected approvedBy 'user_789', got '%s'", call.approvedBy)
		}
	}
}

func TestFeishuCallbackHandler_HandleEventCallback_Reject(t *testing.T) {
	mockManager := &MockApprovalManager{}
	mockManager.getApprovalByRequestID = func(ctx context.Context, requestID string) (string, error) {
		return "tenant_456", nil
	}

	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			Type:   "card.action.trigger",
			UserID: "user_789",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=reject&request_id=req_123", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Verify Reject was called
	if len(mockManager.rejectCalls) != 1 {
		t.Errorf("expected 1 reject call, got %d", len(mockManager.rejectCalls))
	}
}

func TestFeishuCallbackHandler_HandleEventCallback_MissingRequestID(t *testing.T) {
	mockManager := &MockApprovalManager{}
	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			Type:   "card.action.trigger",
			UserID: "user_789",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=approve", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing request_id") {
		t.Error("response should contain 'missing request_id'")
	}
}

func TestFeishuCallbackHandler_HandleEventCallback_InvalidAction(t *testing.T) {
	mockManager := &MockApprovalManager{}
	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			Type:   "card.action.trigger",
			UserID: "user_789",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=invalid&request_id=req_123", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid action") {
		t.Error("response should contain 'invalid action'")
	}
}

func TestFeishuCallbackHandler_InvalidJSON(t *testing.T) {
	mockManager := &MockApprovalManager{}
	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback", bytes.NewReader([]byte("invalid json")))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON") {
		t.Error("response should contain 'invalid JSON'")
	}
}

func TestFeishuCallbackHandler_UnknownCallbackType(t *testing.T) {
	mockManager := &MockApprovalManager{}
	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "unknown_type",
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if response["status"] != "ignored" {
		t.Errorf("expected status='ignored', got '%s'", response["status"])
	}
}

func TestFeishuCallbackHandler_ApprovalNotFound(t *testing.T) {
	mockManager := &MockApprovalManager{}
	mockManager.getApprovalByRequestID = func(ctx context.Context, requestID string) (string, error) {
		return "", errors.New("not found")
	}

	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			UserID: "user_123",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=approve&request_id=req_404", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "approval not found") {
		t.Error("response should contain 'approval not found'")
	}
}

func TestFeishuCallbackHandler_ApproveError(t *testing.T) {
	mockManager := &MockApprovalManager{}
	mockManager.getApprovalByRequestID = func(ctx context.Context, requestID string) (string, error) {
		return "tenant_456", nil
	}
	mockManager.approveFunc = func(ctx context.Context, requestID, tenantID, approvedBy, reason string) error {
		return errors.New("approval failed")
	}

	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			UserID: "user_123",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=approve&request_id=req_123", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "failed to approve") {
		t.Error("response should contain 'failed to approve'")
	}
}

func TestFeishuCallbackHandler_RejectError(t *testing.T) {
	mockManager := &MockApprovalManager{}
	mockManager.getApprovalByRequestID = func(ctx context.Context, requestID string) (string, error) {
		return "tenant_456", nil
	}
	mockManager.rejectFunc = func(ctx context.Context, requestID, tenantID, approvedBy, reason string) error {
		return errors.New("rejection failed")
	}

	handler := NewFeishuCallbackHandler(FeishuCallbackConfig{
		Manager: mockManager,
	})

	callback := FeishuCallback{
		Type: "event_callback",
		Event: &FeishuEvent{
			UserID: "user_123",
		},
	}

	body, _ := json.Marshal(callback)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/feishu/approval-callback?action=reject&request_id=req_123", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleCallback(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "failed to reject") {
		t.Error("response should contain 'failed to reject'")
	}
}
