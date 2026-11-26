package streaming

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"unicode/utf8"
)

// messageForEstimation 用于成本预估的消息结构
type messageForEstimation struct {
	Content interface{} `json:"content"` // 可能是string或array（multimodal）
	Role    string      `json:"role"`
}

// EstimateRequestCostCredits 预估请求成本（单位：积分）
//
// 预估策略（简化版）：
//  1. 输入token: 基于 messages 粗略估算（1个字符≈1token）
//  2. 输出token: 基于 max_tokens 或默认值（2048）
//  3. 使用默认费率（10000积分/百万token）
//  4. 加安全余量（+20%），宁可高估也不能低估
//
// 注意：这是粗略预估，真实扣费在请求完成后基于实际token数
func (h *ChatHandler) EstimateRequestCostCredits(r *http.Request, modelName string) int64 {
	// 1. 解析请求体（需要恢复body供后续使用）
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Warn("failed to read request body for cost estimation", "error", err)
		return getDefaultEstimate(modelName)
	}
	r.Body = io.NopCloser(bytes.NewReader(body)) // 恢复body

	var req struct {
		Messages            []messageForEstimation `json:"messages"`
		MaxTokens           int                    `json:"max_tokens"`
		MaxCompletionTokens int                    `json:"max_completion_tokens"` // 有些模型用这个
	}

	if err := json.Unmarshal(body, &req); err != nil {
		slog.Warn("failed to parse request body for cost estimation", "error", err)
		return getDefaultEstimate(modelName)
	}

	// 2. 估算输入token
	estimatedInputTokens := estimateInputTokens(req.Messages)

	// 3. 估算输出token
	estimatedOutputTokens := int64(req.MaxTokens)
	if estimatedOutputTokens == 0 {
		estimatedOutputTokens = int64(req.MaxCompletionTokens)
	}
	if estimatedOutputTokens == 0 {
		estimatedOutputTokens = 2048 // 默认预估
	}

	// 4. 使用默认费率（TODO: 未来从数据库查询模型实际费率）
	defaultRate := int64(10000) // 10000积分/百万token

	// 5. 计算预估成本
	estimatedCost := (estimatedInputTokens + estimatedOutputTokens) * defaultRate / 1_000_000

	// 6. 加安全余量（+20%），确保预检不会低估
	estimatedCostWithMargin := estimatedCost * 120 / 100

	// 7. 最小预估成本（避免免费调用绕过预检）
	if estimatedCostWithMargin < 10 {
		estimatedCostWithMargin = 10
	}

	slog.Debug("estimated request cost",
		"model", modelName,
		"input_tokens", estimatedInputTokens,
		"output_tokens", estimatedOutputTokens,
		"estimated_cost", estimatedCost,
		"with_margin", estimatedCostWithMargin)

	return estimatedCostWithMargin
}

// estimateInputTokens 估算输入token数
func estimateInputTokens(messages []messageForEstimation) int64 {
	totalChars := 0

	for _, msg := range messages {
		switch content := msg.Content.(type) {
		case string:
			// 纯文本消息
			totalChars += utf8.RuneCountInString(content)

		case []interface{}:
			// Multimodal消息（可能包含text、image_url等）
			for _, part := range content {
				if partMap, ok := part.(map[string]interface{}); ok {
					if textContent, exists := partMap["text"]; exists {
						if textStr, ok := textContent.(string); ok {
							totalChars += utf8.RuneCountInString(textStr)
						}
					}
					// 图片token暂不估算（通常很多，但预估复杂）
					// 保守起见，图片按5000 token估算
					if _, exists := partMap["image_url"]; exists {
						totalChars += 5000 * 2 // 转换为字符数等价物
					}
				}
			}

		default:
			// 未知格式，按空处理
			slog.Debug("unknown message content type", "type", content)
		}
	}

	// 粗略转换：英文1个字符≈0.5token，中文1个字符≈1token
	// 这里保守估算：1个字符≈1token（高估）
	estimatedTokens := int64(totalChars)

	// 加上系统prompt和格式开销（约10%）
	estimatedTokens = estimatedTokens * 110 / 100

	// 最小值
	if estimatedTokens < 100 {
		estimatedTokens = 100
	}

	return estimatedTokens
}

// getDefaultEstimate 返回默认预估（查询失败时的fallback）
func getDefaultEstimate(modelName string) int64 {
	// 保守估算：假设输入2000 token + 输出2000 token，单价10000积分/百万token
	// (2000 + 2000) * 10000 / 1_000_000 = 40 积分
	// 加20%余量 = 48 积分
	defaultEstimate := int64(50)

	slog.Warn("using default cost estimate",
		"model", modelName,
		"estimate", defaultEstimate)

	return defaultEstimate
}

