package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type OpenAICompatClient struct {
	endpoint, apiKey string
	http             *http.Client
}

func NewOpenAICompatClient(endpoint, apiKey string, timeout time.Duration) *OpenAICompatClient {
	return &OpenAICompatClient{endpoint: strings.TrimRight(endpoint, "/"), apiKey: apiKey, http: &http.Client{Timeout: timeout}}
}
func (c *OpenAICompatClient) Complete(ctx context.Context, model string, messages []ChatMessage, maxTokens int, temperature float64) (string, float64, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "temperature": temperature, "max_tokens": maxTokens, "stream": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	latency := float64(time.Since(start).Milliseconds())
	if err != nil {
		return "", latency, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return "", latency, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", latency, err
	}
	if len(parsed.Choices) == 0 {
		return "", latency, fmt.Errorf("empty choices")
	}
	content := parsed.Choices[0].Message.Content
	if content == "" {
		content = parsed.Choices[0].Message.ReasoningContent
	}
	return content, latency, nil
}
