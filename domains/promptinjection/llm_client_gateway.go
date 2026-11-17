// Package promptinjection - LLM 客户端实现
// 用途：集成网关已有的 LLM 调用能力，供错误分析器使用
package promptinjection

import (
	"context"
	"log/slog"
)

// GatewayLLMClient 使用网关自身能力的 LLM 客户端
type GatewayLLMClient struct {
	model    string
	endpoint string // 例如：/v1/chat/completions
	apiKey   string
	baseURL  string
}

// NewGatewayLLMClient 创建网关 LLM 客户端
func NewGatewayLLMClient(model, endpoint, apiKey, baseURL string) *GatewayLLMClient {
	return &GatewayLLMClient{
		model:    model,
		endpoint: endpoint,
		apiKey:   apiKey,
		baseURL:  baseURL,
	}
}

// Analyze 调用 LLM 进行分析
func (c *GatewayLLMClient) Analyze(ctx context.Context, prompt string) (string, int, error) {
	// TODO: 实现实际的 LLM 调用
	// 构建请求示例（暂未使用）：
	// request := map[string]interface{}{
	// 	"model": c.model,
	// 	"messages": []map[string]string{
	// 		{"role": "user", "content": prompt},
	// 	},
	// 	"temperature": 0.3,
	// 	"max_tokens":  4000,
	// }
	// requestBody, err := json.Marshal(request)
	// if err != nil {
	// 	return "", 0, fmt.Errorf("failed to marshal request: %w", err)
	// }
	//
	// 实现方式选项：
	// 1. 直接调用网关自己的 /v1/chat/completions（回环）
	// 2. 或者直接调用底层的 executor
	// 3. 或者调用外部专用的分析模型

	slog.Info("llm_client: sending analysis request",
		"model", c.model,
		"prompt_length", len(prompt))

	// 示例：需要实际实现
	response := ""
	tokensUsed := 0

	return response, tokensUsed, nil
}

// 建议的实现方式：

// 方案 A: 回环调用（简单但可能有循环依赖）
// func (c *GatewayLLMClient) callLoopback(ctx context.Context, body []byte) (string, int, error) {
//     req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+c.endpoint, bytes.NewReader(body))
//     req.Header.Set("Authorization", "Bearer "+c.apiKey)
//     req.Header.Set("Content-Type", "application/json")
//     // ... 执行请求并解析响应
// }

// 方案 B: 直接调用 executor（推荐，避免循环）
// func (c *GatewayLLMClient) callExecutor(ctx context.Context, request *ir.Request) (string, int, error) {
//     // 直接使用 domains/streaming/executor
//     // 需要注入 executor 依赖
// }

// 方案 C: 专用外部 API（最安全，完全独立）
// func (c *GatewayLLMClient) callExternalAPI(ctx context.Context, body []byte) (string, int, error) {
//     // 调用专门用于分析的外部 LLM API
//     // 例如：Anthropic API, OpenAI API 等
// }
