package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// OpenAIClient implements LLMClient using OpenAI's API.
type OpenAIClient struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOpenAIClient creates a new OpenAI client for summarization.
func NewOpenAIClient(apiKey, baseURL, model string) *OpenAIClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-3.5-turbo" // Fast and cheap
	}

	return &OpenAIClient{
		apiKey:     apiKey,
		baseURL:    baseURL,
		model:      model,
		httpClient: &http.Client{},
	}
}

// GenerateSummary calls OpenAI API to generate a conversation summary.
func (c *OpenAIClient) GenerateSummary(ctx context.Context, messages []ir.Message) (*LLMSummaryResponse, error) {
	// Build the prompt
	prompt := c.buildPrompt(messages)

	// Prepare OpenAI API request
	reqBody := map[string]interface{}{
		"model": c.model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a conversation analysis assistant. Analyze conversations and provide structured summaries in JSON format.",
			},
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.3,
		"max_tokens":  500,
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(reqData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	// Parse the JSON response from LLM
	content := apiResp.Choices[0].Message.Content
	
	// Try to extract JSON from the response (it might be wrapped in markdown code blocks)
	content = extractJSON(content)

	var summary LLMSummaryResponse
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return &summary, nil
}

// buildPrompt constructs the summarization prompt from messages.
func (c *OpenAIClient) buildPrompt(messages []ir.Message) string {
	var sb strings.Builder

	sb.WriteString("You are a conversation analysis assistant. Please analyze the following conversation and extract:\n")
	sb.WriteString("1. User intent (1 sentence summary)\n")
	sb.WriteString("2. Main topics (3-5 tags)\n")
	sb.WriteString("3. Key information points (bullet points)\n")
	sb.WriteString("4. Risk assessment (whether it involves sensitive operations)\n\n")
	sb.WriteString("Conversation content:\n")

	for i, msg := range messages {
		content := extractMessageContentForPrompt(msg)
		sb.WriteString(fmt.Sprintf("\n[Message %d - %s]: %s", i+1, msg.Role, content))
	}

	sb.WriteString("\n\nPlease respond in JSON format:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"user_intent\": \"...\",\n")
	sb.WriteString("  \"topics\": [\"topic1\", \"topic2\"],\n")
	sb.WriteString("  \"key_points\": [\"point1\", \"point2\"],\n")
	sb.WriteString("  \"risk_assessment\": \"LOW/MEDIUM/HIGH\"\n")
	sb.WriteString("}")

	return sb.String()
}

// extractMessageContentForPrompt extracts text content from a message for the prompt.
func extractMessageContentForPrompt(msg ir.Message) string {
	var parts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		} else if block.Type == "tool_use" && block.ToolUse != nil {
			parts = append(parts, fmt.Sprintf("[Tool call: %s]", block.ToolUse.Name))
		} else if block.Type == "tool_result" {
			parts = append(parts, "[Tool result]")
		}
	}

	content := strings.Join(parts, " ")
	// Limit length to avoid excessive token usage
	if len(content) > 1000 {
		content = content[:1000] + "..."
	}
	return content
}

// extractJSON attempts to extract JSON from markdown code blocks.
func extractJSON(content string) string {
	// Remove markdown code block markers if present
	content = strings.TrimSpace(content)
	
	// Check for ```json ... ``` or ``` ... ```
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSpace(content)
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSpace(content)
	}
	
	if strings.HasSuffix(content, "```") {
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}
	
	return content
}
