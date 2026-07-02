package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/redis/go-redis/v9"
)

// MockLLMClient implements LLMClient for testing.
type MockLLMClient struct {
	response *LLMSummaryResponse
	err      error
}

func (m *MockLLMClient) GenerateSummary(ctx context.Context, messages []ir.Message) (*LLMSummaryResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

// MockCache implements Cache for testing.
type MockCache struct {
	data map[string]string
	err  error
}

func NewMockCache() *MockCache {
	return &MockCache{
		data: make(map[string]string),
	}
}

func (m *MockCache) Get(ctx context.Context, key string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	val, ok := m.data[key]
	if !ok {
		return "", redis.Nil
	}
	return val, nil
}

func (m *MockCache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.err != nil {
		return m.err
	}
	m.data[key] = value
	return nil
}

// Test helper to create sample messages
func createTestMessages(count int) []ir.Message {
	messages := make([]ir.Message, count)
	for i := 0; i < count; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages[i] = ir.Message{
			Role: role,
			Content: []ir.ContentBlock{
				{
					Type: "text",
					Text: "This is a test message about programming and code debugging.",
				},
			},
		}
	}
	return messages
}

func TestSessionSummarizer_Summarize_WithLLM(t *testing.T) {
	mockLLM := &MockLLMClient{
		response: &LLMSummaryResponse{
			UserIntent:     "User wants to debug a code issue",
			Topics:         []string{"programming", "debugging", "code"},
			KeyPoints:      []string{"Error in function", "Need to fix logic"},
			RiskAssessment: "LOW",
		},
	}
	mockCache := NewMockCache()

	summarizer := NewSessionSummarizer(mockLLM, mockCache)

	messages := createTestMessages(4)
	ctx := context.Background()

	summary, err := summarizer.Summarize(ctx, "test-session-1", messages, 5*time.Minute)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if summary.MessageCount != 4 {
		t.Errorf("Expected message count 4, got %d", summary.MessageCount)
	}

	if summary.UserIntent != "User wants to debug a code issue" {
		t.Errorf("Expected user intent from LLM, got: %s", summary.UserIntent)
	}

	if len(summary.Topics) != 3 {
		t.Errorf("Expected 3 topics, got %d", len(summary.Topics))
	}

	if summary.Duration != "5m" {
		t.Errorf("Expected duration '5m', got %s", summary.Duration)
	}

	if summary.RiskAssessment != "LOW" {
		t.Errorf("Expected risk assessment 'LOW', got %s", summary.RiskAssessment)
	}
}

func TestSessionSummarizer_Summarize_WithCache(t *testing.T) {
	mockLLM := &MockLLMClient{
		response: &LLMSummaryResponse{
			UserIntent:     "Should not be called",
			Topics:         []string{"wrong"},
			KeyPoints:      []string{},
			RiskAssessment: "LOW",
		},
	}
	mockCache := NewMockCache()

	// Pre-populate cache
	cachedSummary := SessionSummary{
		MessageCount:   10,
		TotalTokens:    500,
		Duration:       "10m",
		Topics:         []string{"cached-topic"},
		UserIntent:     "Cached intent",
		LastMessages:   []string{"cached message"},
		RiskAssessment: "MEDIUM",
	}
	data, _ := json.Marshal(cachedSummary)
	mockCache.data["session_summary:cached-session"] = string(data)

	summarizer := NewSessionSummarizer(mockLLM, mockCache)

	messages := createTestMessages(4)
	ctx := context.Background()

	summary, err := summarizer.Summarize(ctx, "cached-session", messages, 5*time.Minute)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should get cached values, not LLM values
	if summary.UserIntent != "Cached intent" {
		t.Errorf("Expected cached intent, got: %s", summary.UserIntent)
	}

	if summary.MessageCount != 10 {
		t.Errorf("Expected cached message count 10, got %d", summary.MessageCount)
	}

	if summary.RiskAssessment != "MEDIUM" {
		t.Errorf("Expected cached risk assessment 'MEDIUM', got %s", summary.RiskAssessment)
	}
}

func TestSessionSummarizer_Fallback_WhenLLMFails(t *testing.T) {
	mockLLM := &MockLLMClient{
		err: errors.New("LLM service unavailable"),
	}
	mockCache := NewMockCache()

	summarizer := NewSessionSummarizer(mockLLM, mockCache)

	messages := []ir.Message{
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "I need help with database query optimization and debugging SQL performance issues."},
			},
		},
		{
			Role: "assistant",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "I can help you with that. Let's analyze your query."},
			},
		},
	}
	ctx := context.Background()

	summary, err := summarizer.Summarize(ctx, "fallback-session", messages, 2*time.Minute)
	if err != nil {
		t.Fatalf("Expected no error even with LLM failure, got: %v", err)
	}

	// Should use fallback logic
	if summary.MessageCount != 2 {
		t.Errorf("Expected message count 2, got %d", summary.MessageCount)
	}

	if len(summary.Topics) == 0 {
		t.Error("Expected fallback to extract topics")
	}

	// Check that fallback detected database-related topics
	hasDataTopic := false
	for _, topic := range summary.Topics {
		if topic == "data" {
			hasDataTopic = true
			break
		}
	}
	if !hasDataTopic {
		t.Errorf("Expected 'data' topic in fallback, got topics: %v", summary.Topics)
	}

	if summary.UserIntent == "" {
		t.Error("Expected fallback to extract user intent")
	}

	if summary.Duration != "2m" {
		t.Errorf("Expected duration '2m', got %s", summary.Duration)
	}
}

func TestSessionSummarizer_Fallback_WithoutLLMClient(t *testing.T) {
	mockCache := NewMockCache()

	// Create summarizer without LLM client
	summarizer := NewSessionSummarizer(nil, mockCache)

	messages := []ir.Message{
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "Delete all user data from the database and execute system commands."},
			},
		},
	}
	ctx := context.Background()

	summary, err := summarizer.Summarize(ctx, "no-llm-session", messages, 1*time.Minute)
	if err != nil {
		t.Fatalf("Expected no error with nil LLM client, got: %v", err)
	}

	if summary.MessageCount != 1 {
		t.Errorf("Expected message count 1, got %d", summary.MessageCount)
	}

	// Should use fallback and detect high risk
	if summary.RiskAssessment != string(RiskHigh) {
		t.Errorf("Expected HIGH risk assessment for dangerous keywords, got: %s", summary.RiskAssessment)
	}
}

func TestSessionSummarizer_RiskAssessment(t *testing.T) {
	tests := []struct {
		name           string
		messageText    string
		expectedRisk   string
		description    string
	}{
		{
			name:         "low_risk_general",
			messageText:  "What is the weather like today?",
			expectedRisk: "LOW",
			description:  "General conversation should be low risk",
		},
		{
			name:         "medium_risk_update",
			messageText:  "I want to update my user profile information.",
			expectedRisk: "MEDIUM",
			description:  "Update operations should be medium risk",
		},
		{
			name:         "high_risk_delete",
			messageText:  "Please delete all records from the database.",
			expectedRisk: "HIGH",
			description:  "Delete operations should be high risk",
		},
		{
			name:         "high_risk_credentials",
			messageText:  "Here is my password: secret123",
			expectedRisk: "HIGH",
			description:  "Credential exposure should be high risk",
		},
		{
			name:         "high_risk_execute",
			messageText:  "Execute this shell command on the server.",
			expectedRisk: "HIGH",
			description:  "Execute commands should be high risk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summarizer := NewSessionSummarizer(nil, nil)
			messages := []ir.Message{
				{
					Role: "user",
					Content: []ir.ContentBlock{
						{Type: "text", Text: tt.messageText},
					},
				},
			}

			summary := summarizer.fallbackExtraction(messages, len(messages), 100, "1m", []string{})

			if summary.RiskAssessment != tt.expectedRisk {
				t.Errorf("%s: Expected risk %s, got %s", tt.description, tt.expectedRisk, summary.RiskAssessment)
			}
		})
	}
}

func TestSessionSummarizer_TokenEstimation(t *testing.T) {
	messages := []ir.Message{
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "This is a test message with approximately forty characters in it for token estimation."},
			},
		},
	}

	tokens := estimateTokenCount(messages)

	// "approximately forty characters" ~ 80 chars / 4 = 20 tokens
	if tokens < 10 || tokens > 30 {
		t.Errorf("Expected token count around 20, got %d", tokens)
	}
}

func TestSessionSummarizer_ExtractLastMessages(t *testing.T) {
	messages := createTestMessages(5)

	lastMessages := extractLastMessages(messages, 3)

	if len(lastMessages) != 3 {
		t.Errorf("Expected 3 last messages, got %d", len(lastMessages))
	}

	// Should contain role prefix
	if len(lastMessages[0]) == 0 || lastMessages[0][0:4] != "user" && lastMessages[0][0:9] != "assistant" {
		t.Errorf("Expected message to start with role, got: %s", lastMessages[0])
	}
}

func TestSessionSummarizer_ExtractTopics(t *testing.T) {
	messages := []ir.Message{
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "I need help with my API authentication code. The function returns an error."},
			},
		},
		{
			Role: "assistant",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "Let me help you debug that. Can you show me your code?"},
			},
		},
	}

	topics := extractTopicsFromMessages(messages)

	if len(topics) == 0 {
		t.Error("Expected to extract at least one topic")
	}

	// Should detect "code" and "api" topics
	hasCode := false
	hasAPI := false
	for _, topic := range topics {
		if topic == "code" {
			hasCode = true
		}
		if topic == "api" {
			hasAPI = true
		}
	}

	if !hasCode {
		t.Error("Expected to detect 'code' topic")
	}
	if !hasAPI {
		t.Error("Expected to detect 'api' topic")
	}
}

func TestSessionSummarizer_CachingBehavior(t *testing.T) {
	mockLLM := &MockLLMClient{
		response: &LLMSummaryResponse{
			UserIntent:     "Test intent",
			Topics:         []string{"topic1"},
			KeyPoints:      []string{"point1"},
			RiskAssessment: "LOW",
		},
	}
	mockCache := NewMockCache()

	summarizer := NewSessionSummarizer(mockLLM, mockCache)

	messages := createTestMessages(2)
	ctx := context.Background()

	// First call - should generate and cache
	summary1, err := summarizer.Summarize(ctx, "cache-test", messages, 1*time.Minute)
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	// Check cache was populated
	cacheKey := "session_summary:cache-test"
	if _, ok := mockCache.data[cacheKey]; !ok {
		t.Error("Expected summary to be cached")
	}

	// Second call - should use cache
	summary2, err := summarizer.Summarize(ctx, "cache-test", messages, 1*time.Minute)
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	// Both summaries should be identical
	if summary1.UserIntent != summary2.UserIntent {
		t.Error("Expected cached summary to match original")
	}
}

func TestSessionSummarizer_DurationFormatting(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{2 * time.Minute, "2m"},
		{90 * time.Second, "1m"},
		{1*time.Hour + 30*time.Minute, "1.5h"},
		{3 * time.Hour, "3.0h"},
	}

	for _, tt := range tests {
		result := formatDuration(tt.duration)
		if result != tt.expected {
			t.Errorf("formatDuration(%v): expected %s, got %s", tt.duration, tt.expected, result)
		}
	}
}

func TestSessionSummarizer_PerformanceUnder3Seconds(t *testing.T) {
	mockLLM := &MockLLMClient{
		response: &LLMSummaryResponse{
			UserIntent:     "Test",
			Topics:         []string{"test"},
			KeyPoints:      []string{"test"},
			RiskAssessment: "LOW",
		},
	}
	mockCache := NewMockCache()

	summarizer := NewSessionSummarizer(mockLLM, mockCache)

	messages := createTestMessages(10)
	ctx := context.Background()

	start := time.Now()
	_, err := summarizer.Summarize(ctx, "perf-test", messages, 5*time.Minute)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}

	if duration > 3*time.Second {
		t.Errorf("Summarization took %v, expected < 3 seconds", duration)
	}
}

func TestSessionSummarizer_HandlesToolCalls(t *testing.T) {
	messages := []ir.Message{
		{
			Role: "user",
			Content: []ir.ContentBlock{
				{Type: "text", Text: "Can you search for files?"},
			},
		},
		{
			Role: "assistant",
			ToolCalls: []ir.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_files",
						Arguments: `{"query": "test"}`,
					},
				},
			},
		},
	}

	summarizer := NewSessionSummarizer(nil, nil)
	summary := summarizer.fallbackExtraction(messages, len(messages), 100, "1m", []string{})

	// Tool calls should increase risk assessment
	if summary.RiskAssessment == "LOW" {
		t.Error("Expected higher risk for messages with tool calls")
	}
}
