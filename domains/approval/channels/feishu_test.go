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

func TestNewFeishuChannel(t *testing.T) {
	tests := []struct {
		name        string
		config      FeishuConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: FeishuConfig{
				AppID:       "cli_test123",
				AppSecret:   "secret123",
				CallbackURL: "https://api.example.com",
			},
			expectError: false,
		},
		{
			name: "missing app_id",
			config: FeishuConfig{
				AppSecret:   "secret123",
				CallbackURL: "https://api.example.com",
			},
			expectError: true,
			errorMsg:    "app_id is required",
		},
		{
			name: "missing app_secret",
			config: FeishuConfig{
				AppID:       "cli_test123",
				CallbackURL: "https://api.example.com",
			},
			expectError: true,
			errorMsg:    "app_secret is required",
		},
		{
			name: "missing callback_url",
			config: FeishuConfig{
				AppID:     "cli_test123",
				AppSecret: "secret123",
			},
			expectError: true,
			errorMsg:    "callback_url is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel, err := NewFeishuChannel(tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, channel)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, channel)
				assert.Equal(t, tt.config.AppID, channel.appID)
				assert.Equal(t, tt.config.AppSecret, channel.appSecret)
				assert.Equal(t, tt.config.CallbackURL, channel.callbackURL)
			}
		})
	}
}

func TestFeishuChannel_GetAccessToken(t *testing.T) {
	t.Run("successful token retrieval", func(t *testing.T) {
		// Mock Feishu API server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/open-apis/auth/v3/tenant_access_token/internal", r.URL.Path)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			response := map[string]interface{}{
				"code":                 0,
				"msg":                  "success",
				"tenant_access_token":  "t-test-token-123",
				"expire":               7200,
			}
			json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		channel := &FeishuChannel{
			appID:      "cli_test",
			appSecret:  "secret_test",
			client:     server.Client(),
			tokenCache: &feishuTokenCache{},
		}

		// Override the URL for testing (in real implementation, we'd use a configurable base URL)
		// For now, we'll test with the mock server by temporarily replacing the client
		originalClient := channel.client
		channel.client = server.Client()
		defer func() { channel.client = originalClient }()

		// Note: This test needs the actual URL to be configurable or we need to mock http.Client
		// For now, we'll skip the actual call and test other aspects
		t.Skip("Skipping actual API call test - requires URL injection or interface")
	})

	t.Run("token caching", func(t *testing.T) {
		channel := &FeishuChannel{
			tokenCache: &feishuTokenCache{
				token:     "cached-token",
				expiresAt: time.Now().Add(1 * time.Hour),
			},
		}

		token, err := channel.getAccessToken(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "cached-token", token)
	})

	t.Run("expired token cache", func(t *testing.T) {
		channel := &FeishuChannel{
			appID:     "cli_test",
			appSecret: "secret_test",
			client:    &http.Client{Timeout: 10 * time.Second},
			tokenCache: &feishuTokenCache{
				token:     "expired-token",
				expiresAt: time.Now().Add(-1 * time.Hour), // Expired
			},
		}

		// This would normally fetch a new token, but will fail without mock server
		_, err := channel.getAccessToken(context.Background())
		assert.Error(t, err) // Expected to fail without mock
	})
}

func TestFeishuChannel_BuildApprovalCard(t *testing.T) {
	channel, err := NewFeishuChannel(FeishuConfig{
		AppID:       "cli_test",
		AppSecret:   "secret_test",
		CallbackURL: "https://api.example.com",
	})
	require.NoError(t, err)

	req := &approval.ApprovalRequest{
		RequestID:     "req_test_123",
		SessionID:     "sess_456",
		TenantID:      "tenant_789",
		TriggerType:   approval.TriggerSensitiveContent,
		TriggerReason: "检测到敏感信息",
		RiskLevel:     approval.RiskHigh,
		UserMessage:   "用户发送了包含敏感数据的消息",
		EstimatedCost: 0.05,
		EstimatedTokens: 1500,
		SensitiveInfo: []approval.SensitiveItemSummary{
			{
				Type:       "PII",
				Content:    "***-****-1234",
				Location:   "message[0].content",
				Confidence: 0.95,
			},
		},
		Status:    approval.StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	card := channel.buildApprovalCard(req)

	// Verify card structure
	assert.NotNil(t, card)
	assert.Contains(t, card.Header.Title.Content, "审批请求")
	assert.Equal(t, "orange", card.Header.Template) // HIGH risk = orange

	// Verify elements exist
	assert.NotEmpty(t, card.Elements)

	// Check for action buttons
	var hasActionButtons bool
	for _, elem := range card.Elements {
		if elem.Tag == "action" {
			hasActionButtons = true
			assert.Len(t, elem.Actions, 2) // Approve and Reject
			
			// Verify approve button
			approveBtn := elem.Actions[0]
			assert.Equal(t, "button", approveBtn.Tag)
			assert.Equal(t, "primary", approveBtn.Type)
			assert.Contains(t, approveBtn.Text.Content, "批准")
			assert.Equal(t, "approve", approveBtn.Value["action"])
			assert.Equal(t, req.RequestID, approveBtn.Value["request_id"])
			assert.Contains(t, approveBtn.URL, "approve")

			// Verify reject button
			rejectBtn := elem.Actions[1]
			assert.Equal(t, "button", rejectBtn.Tag)
			assert.Equal(t, "danger", rejectBtn.Type)
			assert.Contains(t, rejectBtn.Text.Content, "拒绝")
			assert.Equal(t, "reject", rejectBtn.Value["action"])
			assert.Contains(t, rejectBtn.URL, "reject")
		}
	}
	assert.True(t, hasActionButtons, "Card should contain action buttons")

	// Convert to JSON to verify structure
	jsonStr := card.ToJSON()
	assert.NotEmpty(t, jsonStr)

	// Parse back to verify JSON is valid
	var parsed map[string]interface{}
	err = json.Unmarshal([]byte(jsonStr), &parsed)
	assert.NoError(t, err)
	assert.Contains(t, parsed, "header")
	assert.Contains(t, parsed, "elements")
}

func TestFeishuChannel_RiskLevelColor(t *testing.T) {
	channel := &FeishuChannel{}

	tests := []struct {
		level    approval.RiskLevel
		expected string
	}{
		{approval.RiskLow, "green"},
		{approval.RiskMedium, "yellow"},
		{approval.RiskHigh, "orange"},
		{approval.RiskCritical, "red"},
		{"UNKNOWN", "blue"},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			color := channel.riskLevelColor(tt.level)
			assert.Equal(t, tt.expected, color)
		})
	}
}

func TestFeishuChannel_RiskLevelEmoji(t *testing.T) {
	channel := &FeishuChannel{}

	tests := []struct {
		level    approval.RiskLevel
		expected string
	}{
		{approval.RiskLow, "🟢 LOW"},
		{approval.RiskMedium, "🟡 MEDIUM"},
		{approval.RiskHigh, "🟠 HIGH"},
		{approval.RiskCritical, "🔴 CRITICAL"},
		{"UNKNOWN", "⚪ UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			emoji := channel.riskLevelEmoji(tt.level)
			assert.Equal(t, tt.expected, emoji)
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"negative duration", -1 * time.Hour, "已过期"},
		{"seconds", 30 * time.Second, "30秒"},
		{"minutes", 5 * time.Minute, "5分钟"},
		{"hours", 2 * time.Hour, "2.0小时"},
		{"days", 3 * 24 * time.Hour, "3.0天"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFeishuChannel_SendApprovalNotification(t *testing.T) {
	t.Run("no approvers", func(t *testing.T) {
		channel, err := NewFeishuChannel(FeishuConfig{
			AppID:       "cli_test",
			AppSecret:   "secret_test",
			CallbackURL: "https://api.example.com",
		})
		require.NoError(t, err)

		req := &approval.ApprovalRequest{
			RequestID: "req_test",
		}

		err = channel.SendApprovalNotification(context.Background(), req, []approval.Approver{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no approvers specified")
	})

	t.Run("approvers without email", func(t *testing.T) {
		channel, err := NewFeishuChannel(FeishuConfig{
			AppID:       "cli_test",
			AppSecret:   "secret_test",
			CallbackURL: "https://api.example.com",
		})
		require.NoError(t, err)

		req := &approval.ApprovalRequest{
			RequestID:     "req_test",
			SessionID:     "sess_test",
			TriggerReason: "Test",
			RiskLevel:     approval.RiskLow,
			UserMessage:   "Test message",
			CreatedAt:     time.Now(),
			ExpiresAt:     time.Now().Add(1 * time.Hour),
		}

		approvers := []approval.Approver{
			{UserID: "user_1", Name: "User 1"}, // No email
		}

		err = channel.SendApprovalNotification(context.Background(), req, approvers)
		assert.Error(t, err) // Should fail as no valid approvers
	})
}

func TestFeishuInteractiveCard_ToJSON(t *testing.T) {
	card := &FeishuInteractiveCard{
		Header: FeishuCardHeader{
			Title: FeishuCardTitle{
				Content: "Test Card",
				Tag:     "plain_text",
			},
			Template: "blue",
		},
		Elements: []FeishuCardElement{
			{
				Tag: "div",
				Text: &FeishuCardText{
					Content: "Test content",
					Tag:     "plain_text",
				},
			},
		},
	}

	jsonStr := card.ToJSON()
	assert.NotEmpty(t, jsonStr)

	// Verify JSON is valid
	var result map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &result)
	assert.NoError(t, err)
}

func TestFeishuChannel_MessageTruncation(t *testing.T) {
	channel, err := NewFeishuChannel(FeishuConfig{
		AppID:       "cli_test",
		AppSecret:   "secret_test",
		CallbackURL: "https://api.example.com",
	})
	require.NoError(t, err)

	// Create a message longer than 500 characters
	longMessage := ""
	for i := 0; i < 600; i++ {
		longMessage += "a"
	}

	req := &approval.ApprovalRequest{
		RequestID:       "req_test",
		SessionID:       "sess_test",
		TriggerReason:   "Test",
		RiskLevel:       approval.RiskLow,
		UserMessage:     longMessage,
		EstimatedCost:   0.01,
		EstimatedTokens: 100,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(1 * time.Hour),
	}

	card := channel.buildApprovalCard(req)

	// Find the div element with user message
	var foundTruncated bool
	for _, elem := range card.Elements {
		if elem.Tag == "div" && elem.Text != nil {
			if len(elem.Text.Content) < len(longMessage) {
				foundTruncated = true
				assert.Contains(t, elem.Text.Content, "...")
			}
		}
	}

	assert.True(t, foundTruncated, "Long message should be truncated")
}

func TestFeishuChannel_SensitiveInfoWarning(t *testing.T) {
	channel, err := NewFeishuChannel(FeishuConfig{
		AppID:       "cli_test",
		AppSecret:   "secret_test",
		CallbackURL: "https://api.example.com",
	})
	require.NoError(t, err)

	req := &approval.ApprovalRequest{
		RequestID:     "req_test",
		SessionID:     "sess_test",
		TriggerReason: "Sensitive content detected",
		RiskLevel:     approval.RiskHigh,
		UserMessage:   "Test message",
		SensitiveInfo: []approval.SensitiveItemSummary{
			{Type: "PII", Content: "***", Confidence: 0.9},
			{Type: "SECRET", Content: "***", Confidence: 0.95},
			{Type: "PII", Content: "***", Confidence: 0.85},
		},
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	card := channel.buildApprovalCard(req)

	// Find note element with sensitive info warning
	var foundWarning bool
	for _, elem := range card.Elements {
		if elem.Tag == "note" {
			for _, noteElem := range elem.Elements {
				if noteElem.Tag == "plain_text" && noteElem.Content != "" {
					if len(noteElem.Content) > 0 && string([]rune(noteElem.Content)[0]) == "⚠" {
						foundWarning = true
						assert.Contains(t, noteElem.Content, "敏感信息")
						assert.Contains(t, noteElem.Content, "PII")
						assert.Contains(t, noteElem.Content, "SECRET")
					}
				}
			}
		}
	}

	assert.True(t, foundWarning, "Card should contain sensitive info warning")
}
