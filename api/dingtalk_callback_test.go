package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockApprovalManager is a mock implementation of ApprovalManager.
type MockApprovalManager struct {
	mock.Mock
}

func (m *MockApprovalManager) GetForTenant(ctx context.Context, approvalID, tenantID string) (*sessionaudit.ApprovalRecord, error) {
	args := m.Called(ctx, approvalID, tenantID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sessionaudit.ApprovalRecord), args.Error(1)
}

func (m *MockApprovalManager) List(ctx context.Context, filter *sessionaudit.ApprovalFilter) ([]*sessionaudit.ApprovalRecord, error) {
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*sessionaudit.ApprovalRecord), args.Error(1)
}

func (m *MockApprovalManager) Approve(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
	args := m.Called(ctx, approvalID, tenantID, approvedBy, reason)
	return args.Error(0)
}

func (m *MockApprovalManager) Reject(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error {
	args := m.Called(ctx, approvalID, tenantID, approvedBy, reason)
	return args.Error(0)
}

func TestNewDingTalkCallbackHandler(t *testing.T) {
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, "test_secret", nil)

	assert.NotNil(t, handler)
	assert.Equal(t, manager, handler.approvalManager)
}

func TestDingTalkCallbackHandler_VerifySignature(t *testing.T) {
	appSecret := "test_secret_123"
	handler := NewDingTalkCallbackHandler(&MockApprovalManager{}, appSecret, nil)

	tests := []struct {
		name          string
		timestamp     string
		sign          string
		calculateSign bool
		want          bool
	}{
		{
			name:          "valid signature",
			timestamp:     strconv.FormatInt(time.Now().Unix()*1000, 10),
			calculateSign: true,
			want:          true,
		},
		{
			name:      "missing timestamp",
			timestamp: "",
			sign:      "dummy",
			want:      false,
		},
		{
			name:      "missing signature",
			timestamp: strconv.FormatInt(time.Now().Unix()*1000, 10),
			sign:      "",
			want:      false,
		},
		{
			name:      "invalid signature",
			timestamp: strconv.FormatInt(time.Now().Unix()*1000, 10),
			sign:      "invalid_signature",
			want:      false,
		},
		{
			name:          "expired timestamp",
			timestamp:     strconv.FormatInt((time.Now().Unix()-7200)*1000, 10),
			calculateSign: true,
			want:          false,
		},
		{
			name:          "timestamp outside ten minute replay window",
			timestamp:     strconv.FormatInt(time.Now().Add(-dingTalkCallbackMaxAge-time.Second).UnixMilli(), 10),
			calculateSign: true,
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Calculate valid signature if needed
			timestamp := tt.timestamp
			sign := tt.sign

			if tt.calculateSign {
				stringToSign := timestamp + "\n" + appSecret
				mac := hmac.New(sha256.New, []byte(appSecret))
				mac.Write([]byte(stringToSign))
				// Do not URL-encode here; q.Encode() will handle it automatically
				sign = base64.StdEncoding.EncodeToString(mac.Sum(nil))
			}

			// Create request
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/dingtalk/approval-callback", nil)
			q := req.URL.Query()
			if timestamp != "" {
				q.Set("timestamp", timestamp)
			}
			if sign != "" {
				q.Set("sign", sign)
			}
			req.URL.RawQuery = q.Encode()

			got := handler.verifySignature(req)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_Success(t *testing.T) {
	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, nil)

	tests := []struct {
		name          string
		result        string
		expectApprove bool
		expectReject  bool
	}{
		{
			name:          "approve",
			result:        "agree",
			expectApprove: true,
			expectReject:  false,
		},
		{
			name:          "reject",
			result:        "refuse",
			expectApprove: false,
			expectReject:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Prepare callback request
			callbackReq := DingTalkCallbackRequest{
				EventType:  "approval_result",
				TimeStamp:  time.Now().Unix() * 1000,
				ApprovalID: "approval_123",
				TenantID:   "tenant_1",
				UserID:     "user_1",
				Result:     tt.result,
				Comment:    "test comment",
			}

			body, _ := json.Marshal(callbackReq)

			// Calculate signature
			timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
			stringToSign := timestamp + "\n" + appSecret
			mac := hmac.New(sha256.New, []byte(appSecret))
			mac.Write([]byte(stringToSign))
			// URL-encode the signature because we're embedding it directly in the URL string
			sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))

			// Create HTTP request
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, sign), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			// Set up mock expectations
			if tt.expectApprove {
				manager.On("Approve", mock.Anything, "approval_123", "tenant_1", "user_1", "test comment").Return(nil).Once()
			}
			if tt.expectReject {
				manager.On("Reject", mock.Anything, "approval_123", "tenant_1", "user_1", "test comment").Return(nil).Once()
			}

			// Execute request
			w := httptest.NewRecorder()
			handler.HandleApprovalCallback(w, req)

			// Verify response
			assert.Equal(t, http.StatusOK, w.Code)

			var resp DingTalkCallbackResponse
			err := json.Unmarshal(w.Body.Bytes(), &resp)
			assert.NoError(t, err)
			assert.Equal(t, 0, resp.ErrCode)
			assert.Equal(t, "success", resp.ErrMsg)

			// Verify mock expectations
			manager.AssertExpectations(t)
		})
	}
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_InvalidSignature(t *testing.T) {
	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, nil)

	callbackReq := DingTalkCallbackRequest{
		EventType:  "approval_result",
		TimeStamp:  time.Now().Unix() * 1000,
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "agree",
		Comment:    "test comment",
	}

	body, _ := json.Marshal(callbackReq)

	// Create request with invalid signature
	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, "invalid_sign"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler.HandleApprovalCallback(w, req)

	// Verify response
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	var resp DingTalkCallbackResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 401, resp.ErrCode)
	assert.Equal(t, "Invalid signature", resp.ErrMsg)

	// Manager should not be called
	manager.AssertNotCalled(t, "Approve")
	manager.AssertNotCalled(t, "Reject")
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_MissingFields(t *testing.T) {
	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, nil)

	tests := []struct {
		name        string
		approvalID  string
		tenantID    string
		userID      string
		expectError bool
	}{
		{
			name:        "missing approval_id",
			approvalID:  "",
			tenantID:    "tenant_1",
			userID:      "user_1",
			expectError: true,
		},
		{
			name:        "missing tenant_id",
			approvalID:  "approval_123",
			tenantID:    "",
			userID:      "user_1",
			expectError: true,
		},
		{
			name:        "missing user_id",
			approvalID:  "approval_123",
			tenantID:    "tenant_1",
			userID:      "",
			expectError: true,
		},
		{
			name:        "all fields present",
			approvalID:  "approval_123",
			tenantID:    "tenant_1",
			userID:      "user_1",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callbackReq := DingTalkCallbackRequest{
				EventType:  "approval_result",
				TimeStamp:  time.Now().Unix() * 1000,
				ApprovalID: tt.approvalID,
				TenantID:   tt.tenantID,
				UserID:     tt.userID,
				Result:     "agree",
				Comment:    "test comment",
			}

			body, _ := json.Marshal(callbackReq)

			// Calculate valid signature
			timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
			stringToSign := timestamp + "\n" + appSecret
			mac := hmac.New(sha256.New, []byte(appSecret))
			mac.Write([]byte(stringToSign))
			sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))

			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, sign), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			if !tt.expectError {
				manager.On("Approve", mock.Anything, tt.approvalID, tt.tenantID, tt.userID, "test comment").Return(nil).Once()
			}

			w := httptest.NewRecorder()
			handler.HandleApprovalCallback(w, req)

			if tt.expectError {
				assert.Equal(t, http.StatusBadRequest, w.Code)
				var resp DingTalkCallbackResponse
				json.Unmarshal(w.Body.Bytes(), &resp)
				assert.Equal(t, 400, resp.ErrCode)
			} else {
				assert.Equal(t, http.StatusOK, w.Code)
			}

			if !tt.expectError {
				manager.AssertExpectations(t)
			}
		})
	}
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_InvalidJSON(t *testing.T) {
	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, nil)

	// Invalid JSON body
	body := []byte("{invalid json")

	timestamp := strconv.FormatInt(time.Now().Unix()*1000, 10)
	stringToSign := timestamp + "\n" + appSecret
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(stringToSign))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, sign), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler.HandleApprovalCallback(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp DingTalkCallbackResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 400, resp.ErrCode)
	assert.Contains(t, resp.ErrMsg, "Invalid request format")
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_RejectsUserOutsideAllowlist(t *testing.T) {
	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, []string{"allowed_user"})
	callbackReq := DingTalkCallbackRequest{
		TimeStamp:  time.Now().UnixMilli(),
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "other_user",
		Result:     "agree",
	}
	body, err := json.Marshal(callbackReq)
	if err != nil {
		t.Fatal(err)
	}

	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(timestamp + "\n" + appSecret))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, sign), bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleApprovalCallback(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	manager.AssertNotCalled(t, "Approve")
	manager.AssertNotCalled(t, "Reject")
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_RejectsOversizedBody(t *testing.T) {
	appSecret := "test_secret_123"
	handler := NewDingTalkCallbackHandler(&MockApprovalManager{}, appSecret, nil)
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(timestamp + "\n" + appSecret))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, sign), bytes.NewReader(bytes.Repeat([]byte("a"), maxDingTalkCallbackBodyBytes+1)))
	w := httptest.NewRecorder()

	handler.HandleApprovalCallback(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDingTalkCallbackHandler_UsesCurrentSecret(t *testing.T) {
	secret := "first_secret"
	handler := newDingTalkCallbackHandler(&MockApprovalManager{}, func() string { return secret }, nil)
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/callback?timestamp="+timestamp+"&sign="+url.QueryEscape(sign), nil)
	assert.True(t, handler.verifySignature(req))

	secret = ""
	assert.False(t, handler.verifySignature(req))
}

func TestDingTalkCallbackHandler_ProcessApprovalResult_UnknownResult(t *testing.T) {
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, "test_secret", nil)

	req := &DingTalkCallbackRequest{
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "unknown_result",
		Comment:    "test",
	}

	err := handler.processApprovalResult(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown approval result")
}

func TestRegisterDingTalkRoutes(t *testing.T) {
	mux := http.NewServeMux()
	manager := &MockApprovalManager{}
	appSecret := "test_secret"

	RegisterDingTalkRoutes(mux, manager, func() string { return appSecret }, nil)

	// Verify route is registered by making a test request
	callbackReq := DingTalkCallbackRequest{
		EventType:  "approval_result",
		TimeStamp:  time.Now().Unix() * 1000,
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "agree",
		Comment:    "test",
	}

	body, _ := json.Marshal(callbackReq)

	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
	stringToSign := timestamp + "\n" + appSecret
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(stringToSign))
	sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, sign), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	manager.On("Approve", mock.Anything, "approval_123", "tenant_1", "user_1", "test").Return(nil).Once()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	manager.AssertExpectations(t)
}
