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

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/redis/go-redis/v9"
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

// signDingTalkBody 复刻生产代码的 HMAC 输入：timestamp + "\n" + secret + "\n" + body。
func signDingTalkBody(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret + "\n" + string(body)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestNewDingTalkCallbackHandler(t *testing.T) {
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, "test_secret", nil)

	assert.NotNil(t, handler)
	assert.Equal(t, "test_secret", handler.appSecret)
	assert.Equal(t, manager, handler.approvalManager)
	assert.Nil(t, handler.redisClient)
}

func TestDingTalkCallbackHandler_VerifySignature(t *testing.T) {
	appSecret := "test_secret_123"
	handler := NewDingTalkCallbackHandler(&MockApprovalManager{}, appSecret, nil)

	// 任意 body 都参与 HMAC（验证 material 必须包含 body）；这里固定一个常量便于断言。
	body := []byte(`{"approval_id":"approval_xyz"}`)

	now := time.Now()
	tests := []struct {
		name      string
		timestamp string
		sign      string
		want      bool
	}{
		{
			name:      "valid signature",
			timestamp: strconv.FormatInt(now.UnixMilli(), 10),
			sign:      "", // 在测试循环里计算
			want:      true,
		},
		{
			name:      "missing timestamp",
			timestamp: "",
			sign:      "dummy",
			want:      false,
		},
		{
			name:      "missing signature",
			timestamp: strconv.FormatInt(now.UnixMilli(), 10),
			sign:      "",
			want:      false,
		},
		{
			name:      "invalid signature",
			timestamp: strconv.FormatInt(now.UnixMilli(), 10),
			sign:      "invalid_signature",
			want:      false,
		},
		{
			name:      "expired timestamp",
			timestamp: strconv.FormatInt(now.Add(-2*time.Hour).UnixMilli(), 10),
			sign:      "",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timestamp := tt.timestamp
			sign := tt.sign

			if tt.want && tt.sign == "" && tt.timestamp != "" {
				sign = signDingTalkBody(appSecret, timestamp, body)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/dingtalk/approval-callback", bytes.NewReader(body))
			q := req.URL.Query()
			if timestamp != "" {
				q.Set("timestamp", timestamp)
			}
			if sign != "" {
				q.Set("sign", sign)
			}
			req.URL.RawQuery = q.Encode()

			got := handler.verifySignature(req, body)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDingTalkCallbackHandler_VerifySignature_BodyBinding 显式验证 body 被 HMAC 绑定：
// 同一 timestamp+sign 配不同 body 必须失败。
func TestDingTalkCallbackHandler_VerifySignature_BodyBinding(t *testing.T) {
	appSecret := "test_secret_123"
	handler := NewDingTalkCallbackHandler(&MockApprovalManager{}, appSecret, nil)

	bodyA := []byte(`{"approval_id":"a"}`)
	bodyB := []byte(`{"approval_id":"b"}`)
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := signDingTalkBody(appSecret, timestamp, bodyA)

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/dingtalk/approval-callback?timestamp="+timestamp+"&sign="+url.QueryEscape(sign), nil)
	assert.True(t, handler.verifySignature(req, bodyA), "bodyA 必须通过（签的就是 bodyA）")
	assert.False(t, handler.verifySignature(req, bodyB), "bodyB 必须被拒（sign 是 bodyA 的）")
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
		{name: "approve", result: "agree", expectApprove: true, expectReject: false},
		{name: "reject", result: "refuse", expectApprove: false, expectReject: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callbackReq := DingTalkCallbackRequest{
				EventType:  "approval_result",
				TimeStamp:  time.Now().UnixMilli(),
				EventID:    "evt_" + tt.name, // P0-2: 必须带 event_id
				ApprovalID: "approval_123",
				TenantID:   "tenant_1",
				UserID:     "user_1",
				Result:     tt.result,
				Comment:    "test comment",
			}

			body, _ := json.Marshal(callbackReq)
			timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
			sign := signDingTalkBody(appSecret, timestamp, body)

			req := httptest.NewRequest(http.MethodPost,
				fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
				bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			if tt.expectApprove {
				manager.On("Approve", mock.Anything, "approval_123", "tenant_1", "user_1", "test comment").Return(nil).Once()
			}
			if tt.expectReject {
				manager.On("Reject", mock.Anything, "approval_123", "tenant_1", "user_1", "test comment").Return(nil).Once()
			}

			w := httptest.NewRecorder()
			handler.HandleApprovalCallback(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			var resp DingTalkCallbackResponse
			err := json.Unmarshal(w.Body.Bytes(), &resp)
			assert.NoError(t, err)
			assert.Equal(t, 0, resp.ErrCode)
			assert.Equal(t, "success", resp.ErrMsg)
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
		TimeStamp:  time.Now().UnixMilli(),
		EventID:    "evt_invalid_sig",
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "agree",
		Comment:    "test comment",
	}
	body, _ := json.Marshal(callbackReq)
	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, "invalid_sign"),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler.HandleApprovalCallback(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp DingTalkCallbackResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 401, resp.ErrCode)
	assert.Equal(t, "Invalid signature", resp.ErrMsg)
	manager.AssertNotCalled(t, "Approve")
	manager.AssertNotCalled(t, "Reject")
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_MissingEventID(t *testing.T) {
	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, nil)

	callbackReq := DingTalkCallbackRequest{
		EventType:  "approval_result",
		TimeStamp:  time.Now().UnixMilli(),
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "agree",
		Comment:    "test comment",
		// EventID 故意留空
	}
	body, _ := json.Marshal(callbackReq)
	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
	sign := signDingTalkBody(appSecret, timestamp, body)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler.HandleApprovalCallback(w, req)

	// P0-2: 缺失 event_id 必须返 400，且 ApprovalManager 不应被调用。
	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp DingTalkCallbackResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 400, resp.ErrCode)
	assert.Equal(t, "Missing event_id", resp.ErrMsg)
	manager.AssertNotCalled(t, "Approve")
	manager.AssertNotCalled(t, "Reject")
}

func TestDingTalkCallbackHandler_HandleApprovalCallback_ReplayRejected(t *testing.T) {
	// 使用 miniredis 提供真实的 Redis 行为（SETNX）。
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	appSecret := "test_secret_123"
	manager := &MockApprovalManager{}
	handler := NewDingTalkCallbackHandler(manager, appSecret, rdb)

	callbackReq := DingTalkCallbackRequest{
		EventType:  "approval_result",
		TimeStamp:  time.Now().UnixMilli(),
		EventID:    "evt_replay_1",
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "agree",
		Comment:    "test comment",
	}
	body, _ := json.Marshal(callbackReq)
	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
	sign := signDingTalkBody(appSecret, timestamp, body)

	// 第一次：成功，ApprovalManager.Approve 被调一次。
	manager.On("Approve", mock.Anything, "approval_123", "tenant_1", "user_1", "test comment").Return(nil).Once()

	req1 := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
		bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	handler.HandleApprovalCallback(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code, "首次请求必须通过")

	// 第二次（同 event_id 同 body）：必须被 Redis SETNX 拦下 → 409，ApprovalManager 不再被调。
	req2 := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
		bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	handler.HandleApprovalCallback(w2, req2)

	assert.Equal(t, http.StatusConflict, w2.Code, "重放必须被 Redis SETNX 拦下")
	var resp DingTalkCallbackResponse
	err = json.Unmarshal(w2.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Equal(t, 409, resp.ErrCode)
	assert.Equal(t, "Duplicate event", resp.ErrMsg)
	manager.AssertExpectations(t) // Approve 只被调用过一次
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
		{name: "missing approval_id", approvalID: "", tenantID: "tenant_1", userID: "user_1", expectError: true},
		{name: "missing tenant_id", approvalID: "approval_123", tenantID: "", userID: "user_1", expectError: true},
		{name: "missing user_id", approvalID: "approval_123", tenantID: "tenant_1", userID: "", expectError: true},
		{name: "all fields present", approvalID: "approval_123", tenantID: "tenant_1", userID: "user_1", expectError: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callbackReq := DingTalkCallbackRequest{
				EventType:  "approval_result",
				TimeStamp:  time.Now().UnixMilli(),
				EventID:    "evt_" + tt.name, // P0-2: 必须带 event_id
				ApprovalID: tt.approvalID,
				TenantID:   tt.tenantID,
				UserID:     tt.userID,
				Result:     "agree",
				Comment:    "test comment",
			}
			body, _ := json.Marshal(callbackReq)
			timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
			sign := signDingTalkBody(appSecret, timestamp, body)

			req := httptest.NewRequest(http.MethodPost,
				fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
				bytes.NewReader(body))
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

	body := []byte("{invalid json")
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sign := signDingTalkBody(appSecret, timestamp, body)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
		bytes.NewReader(body))
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

	RegisterDingTalkRoutes(mux, manager, appSecret, nil)

	callbackReq := DingTalkCallbackRequest{
		EventType:  "approval_result",
		TimeStamp:  time.Now().UnixMilli(),
		EventID:    "evt_register_test",
		ApprovalID: "approval_123",
		TenantID:   "tenant_1",
		UserID:     "user_1",
		Result:     "agree",
		Comment:    "test",
	}
	body, _ := json.Marshal(callbackReq)
	timestamp := strconv.FormatInt(callbackReq.TimeStamp, 10)
	sign := signDingTalkBody(appSecret, timestamp, body)

	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/webhooks/dingtalk/approval-callback?timestamp=%s&sign=%s", timestamp, url.QueryEscape(sign)),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	manager.On("Approve", mock.Anything, "approval_123", "tenant_1", "user_1", "test").Return(nil).Once()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	manager.AssertExpectations(t)
}
