// Package promptinjection - LLM 客户端完整实现
// 用途：使用网关自身的 API 进行错误分析
package promptinjection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultAnalyzerTimeout = 60 * time.Second
	maxResponseBytes       = 2 << 20 // 2MB
)

// HTTPLLMClient 基于 HTTP 的 LLM 客户端实现
type HTTPLLMClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewHTTPLLMClient 创建 HTTP LLM 客户端
// baseURL: 网关地址，例如 "http://localhost:8080"
// apiKey: 网关 API key
// model: 分析使用的模型，例如 "claude-3-5-sonnet"
func NewHTTPLLMClient(baseURL, apiKey, model string) *HTTPLLMClient {
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	if model == "" {
		model = "claude-3-5-sonnet"
	}

	return &HTTPLLMClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: defaultAnalyzerTimeout},
	}
}

// Analyze 实现 LLMClient 接口
func (c *HTTPLLMClient) Analyze(ctx context.Context, prompt string) (string, int, error) {
	// 构建请求体
	reqBody := map[string]interface{}{
		"model": c.model,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0.3,  // 降低随机性，提高一致性
		"max_tokens":  4000, // 足够的响应空间
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建 HTTP 请求
	url := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llm-gateway-pi-analyzer/1.0")

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", 0, fmt.Errorf("failed to read response: %w", err)
	}

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", 0, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(apiResp.Choices) == 0 {
		return "", 0, fmt.Errorf("no choices in response")
	}

	content := apiResp.Choices[0].Message.Content
	tokens := apiResp.Usage.TotalTokens

	return content, tokens, nil
}

// SetTimeout 设置超时时间
func (c *HTTPLLMClient) SetTimeout(timeout time.Duration) {
	c.httpClient.Timeout = timeout
}

// SetModel 设置模型
func (c *HTTPLLMClient) SetModel(model string) {
	c.model = model
}
