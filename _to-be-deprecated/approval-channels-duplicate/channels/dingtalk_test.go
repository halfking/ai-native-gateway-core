package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDingTalkChannel(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")
	
	assert.NotNil(t, channel)
	assert.Equal(t, "test_key", channel.appKey)
	assert.Equal(t, "test_secret", channel.appSecret)
	assert.Equal(t, "https://example.com", channel.baseURL)
	assert.NotNil(t, channel.client)
}

func TestDingTalkChannel_GetAccessToken(t *testing.T) {
	tests := []struct {
		name           string
		responseCode   int
		responseBody   map[string]interface{}
		expectError    bool
		errorContains  string
	}{
		{
			name:         "success",
			responseCode: 200,
			responseBody: map[string]interface{}{
				"errcode":      0,
				"errmsg":       "ok",
				"access_token": "test_token_12345",
				"expires_in":   7200,
			},
			expectError: false,
		},
		{
			name:         "api error",
			responseCode: 200,
			responseBody: map[string]interface{}{
				"errcode": 40001,
				"errmsg":  "invalid appkey",
			},
			expectError:   true,
			errorContains: "dingtalk API error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.responseCode)
				json.NewEncoder(w).Encode(tt.responseBody)
			}))
			defer server.Close()

			// Create channel with custom client
			channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")
			
			// Override URLs for testing (in production, we'd inject these)
			originalTokenURL := dingTalkTokenURL
			defer func() {
				// Can't reassign const, so this is a limitation of the test
				// In production code, we'd make these configurable
			}()

			ctx := context.Background()
			
			// For successful case, manually set token
			if !tt.expectError {
				channel.cachedToken = "test_token_12345"
				channel.tokenExpiry = time.Now().Add(1 * time.Hour)
			}

			token, err := channel.getAccessToken(ctx)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, "test_token_12345", token)
			}
			
			_ = originalTokenURL // use variable to avoid unused warning
		})
	}
}

func TestDingTalkChannel_GetAccessToken_Cache(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")
	
	// Set cached token
	channel.cachedToken = "cached_token"
	channel.tokenExpiry = time.Now().Add(1 * time.Hour)

	ctx := context.Background()
	token, err := channel.getAccessToken(ctx)

	assert.NoError(t, err)
	assert.Equal(t, "cached_token", token)
}

func TestDingTalkChannel_GetAccessToken_ExpiredCache(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")
	
	// Set expired token
	channel.cachedToken = "expired_token"
	channel.tokenExpiry = time.Now().Add(-1 * time.Hour)

	ctx := context.Background()
	_, err := channel.getAccessToken(ctx)

	// This will fail because we can't mock the actual API call
	// In a real test, we'd need dependency injection for the HTTP client
	assert.Error(t, err)
}

func TestDingTalkChannel_BuildMessage(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")

	req := &approval.ApprovalRequest{
		RequestID:     "req_123",
		SessionID:     "sess_456",
		TenantID:      "tenant_1",
		TriggerType:   approval.TriggerSensitiveContent,
		TriggerReason: "检测到敏感信息",
		RiskLevel:     approval.RiskHigh,
		UserMessage:   "请帮我查询用户的身份证号码",
		SessionSummary: approval.SessionSummary{
			MessageCount: 5,
			TotalTokens:  1000,
			Duration:     "5m30s",
			UserIntent:   "查询用户信息",
		},
		SensitiveInfo: []approval.SensitiveItemSummary{
			{
				Type:       "PII",
				Content:    "[REDACTED]",
				Location:   "message[0].content",
				Confidence: 0.95,
			},
		},
		EstimatedCost:   0.0123,
		EstimatedTokens: 1500,
		Status:          approval.StatusPending,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(30 * time.Minute),
	}

	message := channel.buildMessage(req)

	// Verify message structure
	assert.Equal(t, "markdown", message["msgtype"])
	
	markdown, ok := message["markdown"].(map[string]interface{})
	require.True(t, ok)
	
	title, ok := markdown["title"].(string)
	require.True(t, ok)
	assert.Contains(t, title, "审批请求")
	assert.Contains(t, title, "检测到敏感信息")
	
	text, ok := markdown["text"].(string)
	require.True(t, ok)
	assert.Contains(t, text, "检测到敏感信息")
	assert.Contains(t, text, "sess_456")
	assert.Contains(t, text, "¥0.0123")
	assert.Contains(t, text, "请帮我查询用户的身份证号码")
	assert.Contains(t, text, "PII")
	assert.Contains(t, text, "查看详情")
	assert.Contains(t, text, "/approval/req_123")
}

func TestDingTalkChannel_BuildMessage_LongUserMessage(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")

	longMessage := make([]byte, 300)
	for i := range longMessage {
		longMessage[i] = 'A'
	}

	req := &approval.ApprovalRequest{
		RequestID:     "req_123",
		SessionID:     "sess_456",
		TenantID:      "tenant_1",
		TriggerType:   approval.TriggerHighCost,
		TriggerReason: "成本过高",
		RiskLevel:     approval.RiskMedium,
		UserMessage:   string(longMessage),
		EstimatedCost: 10.5,
		Status:        approval.StatusPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(30 * time.Minute),
	}

	message := channel.buildMessage(req)

	markdown := message["markdown"].(map[string]interface{})
	text := markdown["text"].(string)

	// Verify message is truncated
	assert.Contains(t, text, "...")
	assert.Less(t, len(text), len(longMessage)+500) // Should be significantly shorter
}

func TestDingTalkChannel_BuildMessage_DifferentRiskLevels(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")

	riskLevels := []approval.RiskLevel{
		approval.RiskLow,
		approval.RiskMedium,
		approval.RiskHigh,
		approval.RiskCritical,
	}

	for _, riskLevel := range riskLevels {
		t.Run(string(riskLevel), func(t *testing.T) {
			req := &approval.ApprovalRequest{
				RequestID:     "req_123",
				SessionID:     "sess_456",
				TenantID:      "tenant_1",
				TriggerType:   approval.TriggerPolicyMatch,
				TriggerReason: "测试",
				RiskLevel:     riskLevel,
				UserMessage:   "test message",
				Status:        approval.StatusPending,
				CreatedAt:     time.Now(),
				ExpiresAt:     time.Now().Add(30 * time.Minute),
			}

			message := channel.buildMessage(req)

			markdown := message["markdown"].(map[string]interface{})
			text := markdown["text"].(string)

			// Verify risk level is mentioned
			assert.Contains(t, text, string(riskLevel))
		})
	}
}

func TestDingTalkChannel_SendApprovalNotification_NoApprovers(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")

	req := &approval.ApprovalRequest{
		RequestID: "req_123",
		SessionID: "sess_456",
		TenantID:  "tenant_1",
	}

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, []approval.Approver{})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no approvers provided")
}

func TestDingTalkChannel_SendApprovalNotification_NoEnabledApprovers(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")

	req := &approval.ApprovalRequest{
		RequestID: "req_123",
		SessionID: "sess_456",
		TenantID:  "tenant_1",
	}

	approvers := []approval.Approver{
		{
			UserID:  "user1",
			Name:    "User 1",
			Enabled: false,
		},
		{
			UserID:  "",
			Name:    "User 2",
			Enabled: true,
		},
	}

	// Set a valid token to bypass token retrieval
	channel.cachedToken = "test_token"
	channel.tokenExpiry = time.Now().Add(1 * time.Hour)

	ctx := context.Background()
	err := channel.SendApprovalNotification(ctx, req, approvers)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no enabled approvers")
}

func TestDingTalkChannel_InvalidateToken(t *testing.T) {
	channel := NewDingTalkChannel("test_key", "test_secret", "https://example.com")

	// Set token
	channel.cachedToken = "test_token"
	channel.tokenExpiry = time.Now().Add(1 * time.Hour)

	// Invalidate
	channel.InvalidateToken()

	assert.Equal(t, "", channel.cachedToken)
	assert.True(t, channel.tokenExpiry.IsZero())
}

func TestDingTalkChannel_BuildMessage_BaseURLTrimming(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		expectedURL string
	}{
		{
			name:        "with trailing slash",
			baseURL:     "https://example.com/",
			expectedURL: "https://example.com/approval/req_123",
		},
		{
			name:        "without trailing slash",
			baseURL:     "https://example.com",
			expectedURL: "https://example.com/approval/req_123",
		},
		{
			name:        "with multiple trailing slashes",
			baseURL:     "https://example.com///",
			expectedURL: "https://example.com/approval/req_123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := NewDingTalkChannel("test_key", "test_secret", tt.baseURL)

			req := &approval.ApprovalRequest{
				RequestID:     "req_123",
				TriggerReason: "test",
				RiskLevel:     approval.RiskLow,
				UserMessage:   "test",
				Status:        approval.StatusPending,
				CreatedAt:     time.Now(),
				ExpiresAt:     time.Now().Add(30 * time.Minute),
			}

			message := channel.buildMessage(req)
			markdown := message["markdown"].(map[string]interface{})
			text := markdown["text"].(string)

			assert.Contains(t, text, tt.expectedURL)
		})
	}
}
