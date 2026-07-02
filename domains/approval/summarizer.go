package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/redis/go-redis/v9"
)

// LLMClient defines the interface for calling LLM services to generate summaries.
type LLMClient interface {
	// GenerateSummary calls an LLM to analyze messages and generate a structured summary.
	GenerateSummary(ctx context.Context, messages []ir.Message) (*LLMSummaryResponse, error)
}

// Cache defines the caching interface for storing session summaries.
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
}

// SessionSummarizer generates summaries of conversation sessions.
type SessionSummarizer struct {
	llmClient LLMClient
	cache     Cache
	cacheTTL  time.Duration
}

// LLMSummaryResponse represents the structured response from LLM summarization.
type LLMSummaryResponse struct {
	UserIntent     string   `json:"user_intent"`
	Topics         []string `json:"topics"`
	KeyPoints      []string `json:"key_points"`
	RiskAssessment string   `json:"risk_assessment"` // LOW/MEDIUM/HIGH
}

// NewSessionSummarizer creates a new session summarizer with the given LLM client and cache.
func NewSessionSummarizer(llmClient LLMClient, cache Cache) *SessionSummarizer {
	return &SessionSummarizer{
		llmClient: llmClient,
		cache:     cache,
		cacheTTL:  time.Hour, // 1 hour cache TTL
	}
}

// Summarize generates a summary of the conversation session.
// It first checks cache, then tries LLM generation, and falls back to rule-based extraction.
func (s *SessionSummarizer) Summarize(ctx context.Context, sessionID string, messages []ir.Message, sessionDuration time.Duration) (*SessionSummary, error) {
	// Check cache first
	if s.cache != nil {
		cached, err := s.getFromCache(ctx, sessionID)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// Generate summary
	summary, err := s.generateSummary(ctx, messages, sessionDuration)
	if err != nil {
		return nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	// Cache the result
	if s.cache != nil {
		_ = s.setToCache(ctx, sessionID, summary)
	}

	return summary, nil
}

// generateSummary attempts LLM-based generation first, then falls back to rule-based extraction.
func (s *SessionSummarizer) generateSummary(ctx context.Context, messages []ir.Message, sessionDuration time.Duration) (*SessionSummary, error) {
	// Calculate basic statistics
	messageCount := len(messages)
	totalTokens := estimateTokenCount(messages)
	duration := formatDuration(sessionDuration)
	lastMessages := extractLastMessages(messages, 3)

	// Try LLM-based summarization first
	if s.llmClient != nil {
		llmSummary, err := s.tryLLMSummarization(ctx, messages)
		if err == nil {
			return &SessionSummary{
				MessageCount:   messageCount,
				TotalTokens:    totalTokens,
				Duration:       duration,
				Topics:         llmSummary.Topics,
				UserIntent:     llmSummary.UserIntent,
				LastMessages:   lastMessages,
				RiskAssessment: llmSummary.RiskAssessment,
			}, nil
		}
		// Log error but continue to fallback
	}

	// Fallback to rule-based extraction
	return s.fallbackExtraction(messages, messageCount, totalTokens, duration, lastMessages), nil
}

// tryLLMSummarization attempts to use LLM for generating the summary.
func (s *SessionSummarizer) tryLLMSummarization(ctx context.Context, messages []ir.Message) (*LLMSummaryResponse, error) {
	// Limit to last 10 messages to avoid excessive token usage
	limitedMessages := messages
	if len(messages) > 10 {
		limitedMessages = messages[len(messages)-10:]
	}

	// Call LLM client
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return s.llmClient.GenerateSummary(ctx, limitedMessages)
}

// fallbackExtraction provides rule-based summary extraction when LLM is unavailable.
func (s *SessionSummarizer) fallbackExtraction(messages []ir.Message, messageCount, totalTokens int, duration string, lastMessages []string) *SessionSummary {
	// Extract topics using simple keyword analysis
	topics := extractTopicsFromMessages(messages)

	// Generate user intent from first user message
	userIntent := extractUserIntent(messages)

	// Simple risk assessment based on heuristics
	riskAssessment := assessRiskHeuristic(messages)

	return &SessionSummary{
		MessageCount:   messageCount,
		TotalTokens:    totalTokens,
		Duration:       duration,
		Topics:         topics,
		UserIntent:     userIntent,
		LastMessages:   lastMessages,
		RiskAssessment: riskAssessment,
	}
}

// getFromCache retrieves a cached summary.
func (s *SessionSummarizer) getFromCache(ctx context.Context, sessionID string) (*SessionSummary, error) {
	key := fmt.Sprintf("session_summary:%s", sessionID)
	data, err := s.cache.Get(ctx, key)
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var summary SessionSummary
	if err := json.Unmarshal([]byte(data), &summary); err != nil {
		return nil, err
	}

	return &summary, nil
}

// setToCache stores a summary in cache.
func (s *SessionSummarizer) setToCache(ctx context.Context, sessionID string, summary *SessionSummary) error {
	key := fmt.Sprintf("session_summary:%s", sessionID)
	data, err := json.Marshal(summary)
	if err != nil {
		return err
	}

	return s.cache.Set(ctx, key, string(data), s.cacheTTL)
}

// Helper functions

// estimateTokenCount provides a rough estimate of total tokens in messages.
func estimateTokenCount(messages []ir.Message) int {
	totalChars := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.Type == "text" {
				totalChars += len(block.Text)
			}
		}
	}
	// Rough estimation: 1 token ≈ 4 characters
	return totalChars / 4
}

// formatDuration formats a duration in a human-readable way.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// extractLastMessages gets the last N messages as strings (redacted).
func extractLastMessages(messages []ir.Message, n int) []string {
	start := 0
	if len(messages) > n {
		start = len(messages) - n
	}

	result := make([]string, 0, n)
	for i := start; i < len(messages); i++ {
		msg := messages[i]
		content := extractMessageContent(msg)
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		result = append(result, fmt.Sprintf("%s: %s", msg.Role, content))
	}

	return result
}

// extractMessageContent extracts text content from a message.
func extractMessageContent(msg ir.Message) string {
	var parts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, " ")
}

// extractTopicsFromMessages uses simple keyword frequency analysis to extract topics.
func extractTopicsFromMessages(messages []ir.Message) []string {
	if len(messages) == 0 {
		return []string{}
	}

	// Collect all text content
	var allText strings.Builder
	for _, msg := range messages {
		content := extractMessageContent(msg)
		allText.WriteString(content)
		allText.WriteString(" ")
	}

	text := strings.ToLower(allText.String())

	// Simple topic detection based on keywords
	topics := []string{}
	topicKeywords := map[string][]string{
		"code":     {"code", "function", "class", "programming", "debug", "error"},
		"data":     {"data", "database", "query", "table", "sql"},
		"security": {"password", "credential", "token", "auth", "security", "permission"},
		"api":      {"api", "endpoint", "request", "response", "http"},
		"file":     {"file", "upload", "download", "document", "pdf"},
	}

	for topic, keywords := range topicKeywords {
		for _, keyword := range keywords {
			if strings.Contains(text, keyword) {
				topics = append(topics, topic)
				break
			}
		}
	}

	// Limit to 5 topics
	if len(topics) > 5 {
		topics = topics[:5]
	}

	if len(topics) == 0 {
		topics = []string{"general"}
	}

	return topics
}

// extractUserIntent attempts to extract the user's primary intent from the conversation.
func extractUserIntent(messages []ir.Message) string {
	// Find the first user message
	for _, msg := range messages {
		if msg.Role == "user" {
			content := extractMessageContent(msg)
			if len(content) > 0 {
				// Take first sentence or 100 chars
				if len(content) > 100 {
					content = content[:100] + "..."
				}
				return content
			}
		}
	}

	return "Unknown intent"
}

// assessRiskHeuristic provides a simple heuristic-based risk assessment.
func assessRiskHeuristic(messages []ir.Message) string {
	riskScore := 0

	for _, msg := range messages {
		content := strings.ToLower(extractMessageContent(msg))

		// High-risk keywords (3 points each)
		highRiskKeywords := []string{
			"delete", "drop", "remove", "destroy", "terminate",
			"password", "secret", "credential", "token", "private key",
			"execute", "eval", "exec", "system", "shell",
		}

		for _, keyword := range highRiskKeywords {
			if strings.Contains(content, keyword) {
				riskScore += 3
			}
		}

		// Medium-risk keywords (2 points each)
		mediumRiskKeywords := []string{
			"update", "modify", "change", "alter",
			"payment", "transaction", "financial",
			"user data", "personal information",
		}

		for _, keyword := range mediumRiskKeywords {
			if strings.Contains(content, keyword) {
				riskScore += 2
			}
		}

		// Check for tool calls (higher risk)
		if len(msg.ToolCalls) > 0 {
			riskScore += 3
		}
	}

	if riskScore >= 3 {
		return string(RiskHigh)
	}
	if riskScore >= 2 {
		return string(RiskMedium)
	}
	return string(RiskLow)
}
