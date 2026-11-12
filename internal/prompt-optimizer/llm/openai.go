// Package llm 提供提示词优化模块的 LLM 调用通道。
//
// 目标里的"内部小型大模型"落在这一层：优化器不直接依赖任何特定厂商 SDK，
// 而是走 OpenAI Chat Completions 协议 —— 本仓网关本身就是 OpenAI 兼容的，
// 因此把 BaseURL 指向网关、用网关路由到内部小模型即可，无需新增依赖。
//
// 协议参考：POST {base}/v1/chat/completions
//
//	{"model":"...","messages":[{"role":"user","content":"..."}],
//	 "temperature":0.7,"max_tokens":2000}
//
// 响应取 choices[0].message.content。
package llm

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

// Options 生成选项。
type Options struct {
	// Model 模型名（网关侧可路由到内部小模型）。
	Model string

	// Temperature 采样温度。
	Temperature float64

	// MaxTokens 最大生成 token 数。
	MaxTokens int
}

// Client OpenAI 兼容客户端。
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// Config 客户端配置。
type Config struct {
	// BaseURL 形如 http://host:port/v1 （末尾的 /v1 可省略，会自动补）。
	BaseURL string

	// APIKey 可选；网关开了鉴权时必填。
	APIKey string

	// Model 默认模型名。
	Model string

	// Timeout 单次请求超时。
	Timeout time.Duration

	// HTTPClient 可选，自定义 transport（例如走内部网络策略）。
	HTTPClient *http.Client
}

// New 构造客户端。
func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("llm: base url must not be empty")
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		base += "/v1"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	model := cfg.Model
	if model == "" {
		model = "gpt-4o-mini"
	}

	return &Client{
		baseURL: base,
		apiKey:  cfg.APIKey,
		model:   model,
		http:    httpClient,
	}, nil
}

// Model 返回默认模型名。
func (c *Client) Model() string { return c.model }

// chatRequest 请求体。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

// chatMessage 单条消息。
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse 响应体（只取需要的字段）。
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Generate 生成文本。
func (c *Client) Generate(ctx context.Context, prompt string, opts Options) (string, error) {
	model := opts.Model
	if model == "" {
		model = c.model
	}

	body := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("llm: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm: upstream returned %d: %s", resp.StatusCode, truncate(string(raw), 500))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("llm: decode response: %w (body: %s)", err, truncate(string(raw), 300))
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("llm: upstream error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm: response has no choices: %s", truncate(string(raw), 300))
	}

	return parsed.Choices[0].Message.Content, nil
}

// truncate 截断长字符串，便于放进错误信息。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
