package modelquality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// MockModelInvoker 模拟的模型调用器(用于测试)
type MockModelInvoker struct {
	// 模拟不同供应商的质量特征
	providerQuality map[string]*ProviderQuality
}

// ProviderQuality 供应商质量特征
type ProviderQuality struct {
	BaseAccuracy  float64 // 基础准确率 0-1
	BaseLatency   int64   // 基础延迟(毫秒)
	ErrorRate     float64 // 错误率 0-1
	LatencyJitter int64   // 延迟抖动(毫秒)
}

// NewMockModelInvoker 创建模拟调用器
func NewMockModelInvoker() *MockModelInvoker {
	return &MockModelInvoker{
		providerQuality: map[string]*ProviderQuality{
			// 模拟高质量供应商(如OpenAI GPT-4)
			"openai": {
				BaseAccuracy:  0.92,
				BaseLatency:   1200,
				ErrorRate:     0.01,
				LatencyJitter: 300,
			},
			// 模拟中等质量供应商(如某国产模型)
			"domestic_a": {
				BaseAccuracy:  0.78,
				BaseLatency:   800,
				ErrorRate:     0.05,
				LatencyJitter: 200,
			},
			// 模拟质量下降的供应商(渗水场景)
			"domestic_b": {
				BaseAccuracy:  0.65, // 准确率较低
				BaseLatency:   2500, // 延迟较高
				ErrorRate:     0.12, // 错误率高
				LatencyJitter: 800,
			},
			// 模拟稳定供应商
			"claude": {
				BaseAccuracy:  0.90,
				BaseLatency:   1000,
				ErrorRate:     0.02,
				LatencyJitter: 150,
			},
		},
	}
}

// InvokeModel 模拟调用模型
func (m *MockModelInvoker) InvokeModel(ctx context.Context, provider string, modelName string, prompt string) (response string, tokenUsage int, latency time.Duration, err error) {
	// 获取供应商质量特征
	quality, exists := m.providerQuality[provider]
	if !exists {
		// 默认中等质量
		quality = &ProviderQuality{
			BaseAccuracy:  0.75,
			BaseLatency:   1500,
			ErrorRate:     0.08,
			LatencyJitter: 400,
		}
	}

	// 模拟网络延迟
	latencyMs := quality.BaseLatency + rand.Int63n(quality.LatencyJitter*2) - quality.LatencyJitter
	if latencyMs < 100 {
		latencyMs = 100
	}

	// 检查是否模拟错误
	if rand.Float64() < quality.ErrorRate {
		time.Sleep(time.Duration(latencyMs) * time.Millisecond)
		return "", 0, time.Duration(latencyMs) * time.Millisecond, fmt.Errorf("模拟错误: 模型调用失败 (provider=%s)", provider)
	}

	// 解析问题，提取正确答案(从prompt中)
	correctAnswer := extractCorrectAnswerFromPrompt(prompt)

	// 根据准确率决定是否返回正确答案
	var answer string
	if rand.Float64() < quality.BaseAccuracy {
		// 返回正确答案
		answer = correctAnswer
	} else {
		// 返回错误答案
		wrongOptions := []string{"A", "B", "C", "D"}
		filtered := []string{}
		for _, opt := range wrongOptions {
			if opt != correctAnswer {
				filtered = append(filtered, opt)
			}
		}
		if len(filtered) > 0 {
			answer = filtered[rand.Intn(len(filtered))]
		} else {
			answer = "A"
		}
	}

	// 模拟处理时间
	time.Sleep(time.Duration(latencyMs) * time.Millisecond)

	// 模拟token使用量
	tokenUsage = 100 + rand.Intn(50)

	return answer, tokenUsage, time.Duration(latencyMs) * time.Millisecond, nil
}

// SetProviderQuality 动态设置供应商质量(用于模拟质量变化)
func (m *MockModelInvoker) SetProviderQuality(provider string, quality *ProviderQuality) {
	m.providerQuality[provider] = quality
}

// SimulateQualityDrop 模拟质量下降(渗水场景)
func (m *MockModelInvoker) SimulateQualityDrop(provider string, accuracyDrop float64, latencyIncrease int64) {
	if quality, exists := m.providerQuality[provider]; exists {
		quality.BaseAccuracy -= accuracyDrop
		quality.BaseLatency += latencyIncrease
		quality.ErrorRate += accuracyDrop * 0.5

		if quality.BaseAccuracy < 0 {
			quality.BaseAccuracy = 0.1
		}
		if quality.ErrorRate > 0.3 {
			quality.ErrorRate = 0.3
		}

		slog.Info("mock model quality drop simulated",
			"provider", provider,
			"accuracy", quality.BaseAccuracy,
			"latency_ms", quality.BaseLatency)
	}
}

// extractCorrectAnswerFromPrompt 从prompt中推断正确答案(仅用于模拟)
// 真实场景中模型不知道正确答案
func extractCorrectAnswerFromPrompt(prompt string) string {
	// 这里简化处理：随机返回一个答案
	// 真实场景中，这个函数不应该存在，模型需要真正推理
	options := []string{"A", "B", "C", "D"}

	// 使用prompt的hash作为种子，确保同一问题返回相同答案
	seed := int64(0)
	for _, ch := range prompt {
		seed += int64(ch)
	}
	rng := rand.New(rand.NewSource(seed))

	return options[rng.Intn(len(options))]
}

// GatewayModelInvoker 网关模型调用器(真实实现)
// 通过HTTP调用网关API进行测试
type GatewayModelInvoker struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewGatewayModelInvoker 创建网关模型调用器
// timeout: HTTP客户端超时，0则使用默认30秒
func NewGatewayModelInvoker(baseURL, apiKey string, timeout time.Duration) *GatewayModelInvoker {
	if baseURL == "" {
		baseURL = "http://localhost:8787" // 默认本地网关
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &GatewayModelInvoker{
		baseURL: strings.TrimSpace(baseURL),
		apiKey:  strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// InvokeModel 调用网关模型
func (g *GatewayModelInvoker) InvokeModel(ctx context.Context, provider string, modelName string, prompt string) (response string, tokenUsage int, latency time.Duration, err error) {
	startTime := time.Now()

	// 构造OpenAI格式的请求
	payload := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  50,  // 质量测试只需要简短回答
		"temperature": 0.1, // 低温度保证稳定性
		"stream":      false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, time.Since(startTime), fmt.Errorf("marshal request: %w", err)
	}

	// 构造请求URL
	url := g.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", 0, time.Since(startTime), fmt.Errorf("create request: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	req.Header.Set("X-Gateway-Quality-Test", "true") // 标识为质量测试请求

	// 发送请求
	resp, err := g.httpClient.Do(req)
	latency = time.Since(startTime)

	if err != nil {
		return "", 0, latency, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", 0, latency, fmt.Errorf("gateway returned HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, latency, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", 0, latency, fmt.Errorf("no choices in response")
	}

	response = strings.TrimSpace(result.Choices[0].Message.Content)
	tokenUsage = result.Usage.TotalTokens

	return response, tokenUsage, latency, nil
}

// HTTPModelInvoker HTTP直连模型调用器(绕过网关,直接调用供应商API)
type HTTPModelInvoker struct {
	apiKeys  map[string]string // provider -> api_key
	baseURLs map[string]string // provider -> base_url
}

// NewHTTPModelInvoker 创建HTTP调用器
func NewHTTPModelInvoker(apiKeys map[string]string) *HTTPModelInvoker {
	baseURLs := map[string]string{
		"openai":    "https://api.openai.com/v1",
		"anthropic": "https://api.anthropic.com/v1",
		// 可以添加更多供应商
	}

	return &HTTPModelInvoker{
		apiKeys:  apiKeys,
		baseURLs: baseURLs,
	}
}

// InvokeModel 直接HTTP调用模型
func (h *HTTPModelInvoker) InvokeModel(ctx context.Context, provider string, modelName string, prompt string) (response string, tokenUsage int, latency time.Duration, err error) {
	// TODO: 实现真实的HTTP调用
	// 1. 构造OpenAI格式的请求
	// 2. 发送HTTP POST请求
	// 3. 解析响应
	// 4. 提取answer和token使用量

	return "", 0, 0, fmt.Errorf("HTTPModelInvoker not implemented yet - use MockModelInvoker for testing")
}

// extractAnswerFromResponse 从模型响应中提取答案
func extractAnswerFromResponse(response string) string {
	response = strings.TrimSpace(response)
	response = strings.ToUpper(response)

	// 尝试多种解析策略
	if len(response) == 1 && response >= "A" && response <= "D" {
		return response
	}

	if len(response) > 0 && response[0] >= 'A' && response[0] <= 'D' {
		return string(response[0])
	}

	return ""
}
