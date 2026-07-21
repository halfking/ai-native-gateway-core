package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// InferenceCheckResult represents the result of an AI inference check.
type InferenceCheckResult struct {
	Success      bool
	Latency      time.Duration
	Error        error
	CheckedAt    time.Time
	ResponseText string
	TokenCount   int
	CheckType    string // "light" or "heavy"
}

// InferenceChecker performs AI inference health checks.
type InferenceChecker struct {
	client      *http.Client
	lightPrompt string
	heavyPrompt string
}

// OpenAIRequest represents a minimal OpenAI-compatible request
type OpenAIRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIResponse represents a minimal OpenAI-compatible response
type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// NewInferenceChecker creates a new inference checker.
func NewInferenceChecker(lightTimeout, heavyTimeout time.Duration) *InferenceChecker {
	if lightTimeout == 0 {
		lightTimeout = 10 * time.Second
	}
	if heavyTimeout == 0 {
		heavyTimeout = 30 * time.Second
	}

	return &InferenceChecker{
		client: &http.Client{
			Timeout: heavyTimeout, // Use the longer timeout
		},
		lightPrompt: "1+1=?",               // Simple arithmetic
		heavyPrompt: generateHeavyPrompt(), // ~20K tokens
	}
}

// CheckLight performs a lightweight inference check (simple prompt, expected <1s).
func (c *InferenceChecker) CheckLight(ctx context.Context, apiURL, apiKey, model string) InferenceCheckResult {
	return c.doInferenceCheck(ctx, apiURL, apiKey, model, c.lightPrompt, 10, "light")
}

// CheckHeavy performs a heavy-load inference check (20K tokens context, expected <20s).
func (c *InferenceChecker) CheckHeavy(ctx context.Context, apiURL, apiKey, model string) InferenceCheckResult {
	return c.doInferenceCheck(ctx, apiURL, apiKey, model, c.heavyPrompt, 100, "heavy")
}

func (c *InferenceChecker) doInferenceCheck(
	ctx context.Context,
	apiURL string,
	apiKey string,
	model string,
	prompt string,
	maxTokens int,
	checkType string,
) InferenceCheckResult {
	start := time.Now()
	result := InferenceCheckResult{
		CheckedAt: start,
		CheckType: checkType,
	}

	// Build request
	reqBody := OpenAIRequest{
		Model: model,
		Messages: []Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens: maxTokens,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		result.Error = fmt.Errorf("marshal request: %w", err)
		result.Latency = time.Since(start)
		return result
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Error = fmt.Errorf("create request: %w", err)
		result.Latency = time.Since(start)
		return result
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "LLM-Gateway-InferenceChecker/1.0")

	// Execute request
	resp, err := c.client.Do(req)
	result.Latency = time.Since(start)

	if err != nil {
		result.Error = fmt.Errorf("request failed: %w", err)
		return result
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Error = fmt.Errorf("read response: %w", err)
		return result
	}

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
		return result
	}

	// Parse response
	var openaiResp OpenAIResponse
	if err := json.Unmarshal(respBody, &openaiResp); err != nil {
		result.Error = fmt.Errorf("parse response: %w", err)
		return result
	}

	// Extract response text
	if len(openaiResp.Choices) > 0 {
		result.ResponseText = openaiResp.Choices[0].Message.Content
	}
	result.TokenCount = openaiResp.Usage.TotalTokens

	// Validate response is not empty
	if result.ResponseText == "" {
		result.Error = fmt.Errorf("empty response from model")
		return result
	}

	result.Success = true
	return result
}

// generateHeavyPrompt generates a ~20K token prompt for heavy load testing.
func generateHeavyPrompt() string {
	// Each paragraph is ~100 tokens, repeat 200 times for ~20K tokens
	paragraph := `The development of artificial intelligence has been one of the most significant technological advances of the 21st century. Machine learning algorithms have revolutionized various industries, from healthcare to finance, enabling unprecedented levels of automation and decision-making capabilities. Deep learning, a subset of machine learning, has particularly excelled in areas such as computer vision, natural language processing, and speech recognition. `

	prompt := "Please summarize the following text:\n\n"
	for i := 0; i < 200; i++ {
		prompt += paragraph
	}
	prompt += "\n\nSummary:"

	return prompt
}

// EvaluateLatency evaluates inference latency and determines health status.
// For light checks: <5s = Active, >5s = should trigger heavy check
// For heavy checks: <10s = Active, 10-20s = Degraded, >20s = Unhealthy
func EvaluateLatency(result InferenceCheckResult) (status string, shouldTriggerHeavy bool) {
	if !result.Success {
		return "Unhealthy", false
	}

	if result.CheckType == "light" {
		if result.Latency < 5*time.Second {
			return "Active", false
		}
		return "Degraded", true // Too slow, trigger heavy check
	}

	// Heavy check
	if result.Latency < 10*time.Second {
		return "Active", false
	}
	if result.Latency < 20*time.Second {
		return "Degraded", false
	}
	return "Unhealthy", false
}
