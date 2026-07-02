package approval

import (
	"fmt"
	"sync"
)

// ModelPricing 模型定价信息
type ModelPricing struct {
	ModelName       string  // 模型名称
	InputPrice      float64 // 输入 token 价格（$/1K tokens）
	OutputPrice     float64 // 输出 token 价格（$/1K tokens）
	CachedPrice     float64 // 缓存读取价格（$/1K tokens），为 0 表示不支持缓存
	PricePerRequest float64 // 每次请求固定费用
}

// CostEstimator 成本估算器
type CostEstimator struct {
	mu           sync.RWMutex
	pricingTable map[string]ModelPricing
}

// NewCostEstimator 创建成本估算器
func NewCostEstimator() *CostEstimator {
	e := &CostEstimator{
		pricingTable: make(map[string]ModelPricing),
	}
	e.loadDefaultPricing()
	return e
}

// loadDefaultPricing 加载默认定价
func (e *CostEstimator) loadDefaultPricing() {
	// OpenAI GPT-4 系列
	e.pricingTable["gpt-4"] = ModelPricing{
		ModelName:   "gpt-4",
		InputPrice:  0.03,  // $0.03 / 1K tokens
		OutputPrice: 0.06,  // $0.06 / 1K tokens
	}
	e.pricingTable["gpt-4-32k"] = ModelPricing{
		ModelName:   "gpt-4-32k",
		InputPrice:  0.06,
		OutputPrice: 0.12,
	}
	e.pricingTable["gpt-4-turbo"] = ModelPricing{
		ModelName:   "gpt-4-turbo",
		InputPrice:  0.01,
		OutputPrice: 0.03,
	}
	e.pricingTable["gpt-4-turbo-preview"] = ModelPricing{
		ModelName:   "gpt-4-turbo-preview",
		InputPrice:  0.01,
		OutputPrice: 0.03,
	}
	e.pricingTable["gpt-4o"] = ModelPricing{
		ModelName:    "gpt-4o",
		InputPrice:   0.005,  // $0.005 / 1K tokens
		OutputPrice:  0.015,  // $0.015 / 1K tokens
		CachedPrice:  0.0025, // 缓存读取价格
	}
	e.pricingTable["gpt-4o-mini"] = ModelPricing{
		ModelName:    "gpt-4o-mini",
		InputPrice:   0.00015,
		OutputPrice:  0.0006,
		CachedPrice:  0.000075,
	}

	// OpenAI GPT-3.5 系列
	e.pricingTable["gpt-3.5-turbo"] = ModelPricing{
		ModelName:   "gpt-3.5-turbo",
		InputPrice:  0.0005,
		OutputPrice: 0.0015,
	}
	e.pricingTable["gpt-3.5-turbo-16k"] = ModelPricing{
		ModelName:   "gpt-3.5-turbo-16k",
		InputPrice:  0.003,
		OutputPrice: 0.004,
	}

	// Anthropic Claude 系列
	e.pricingTable["claude-3-opus"] = ModelPricing{
		ModelName:   "claude-3-opus",
		InputPrice:  0.015,
		OutputPrice: 0.075,
	}
	e.pricingTable["claude-3-sonnet"] = ModelPricing{
		ModelName:   "claude-3-sonnet",
		InputPrice:  0.003,
		OutputPrice: 0.015,
	}
	e.pricingTable["claude-3-haiku"] = ModelPricing{
		ModelName:   "claude-3-haiku",
		InputPrice:  0.00025,
		OutputPrice: 0.00125,
	}
	e.pricingTable["claude-3-5-sonnet"] = ModelPricing{
		ModelName:   "claude-3-5-sonnet",
		InputPrice:  0.003,
		OutputPrice: 0.015,
	}

	// Anthropic Claude 2 系列
	e.pricingTable["claude-2"] = ModelPricing{
		ModelName:   "claude-2",
		InputPrice:  0.008,
		OutputPrice: 0.024,
	}
	e.pricingTable["claude-2.1"] = ModelPricing{
		ModelName:   "claude-2.1",
		InputPrice:  0.008,
		OutputPrice: 0.024,
	}
	e.pricingTable["claude-instant-1.2"] = ModelPricing{
		ModelName:   "claude-instant-1.2",
		InputPrice:  0.0008,
		OutputPrice: 0.0024,
	}

	// Google Gemini 系列
	e.pricingTable["gemini-pro"] = ModelPricing{
		ModelName:   "gemini-pro",
		InputPrice:  0.00025,
		OutputPrice: 0.0005,
	}
	e.pricingTable["gemini-pro-vision"] = ModelPricing{
		ModelName:   "gemini-pro-vision",
		InputPrice:  0.00025,
		OutputPrice: 0.0005,
	}
	e.pricingTable["gemini-1.5-pro"] = ModelPricing{
		ModelName:   "gemini-1.5-pro",
		InputPrice:  0.0035,
		OutputPrice: 0.0105,
	}
	e.pricingTable["gemini-1.5-flash"] = ModelPricing{
		ModelName:   "gemini-1.5-flash",
		InputPrice:  0.000075,
		OutputPrice: 0.0003,
	}

	// 添加常见别名
	e.addAlias("gpt-4-turbo-2024-04-09", "gpt-4-turbo")
	e.addAlias("gpt-4-0125-preview", "gpt-4-turbo-preview")
	e.addAlias("gpt-4-1106-preview", "gpt-4-turbo-preview")
	e.addAlias("gpt-4o-2024-05-13", "gpt-4o")
	e.addAlias("gpt-4o-mini-2024-07-18", "gpt-4o-mini")
	e.addAlias("gpt-3.5-turbo-0125", "gpt-3.5-turbo")
	e.addAlias("gpt-3.5-turbo-1106", "gpt-3.5-turbo")
	e.addAlias("claude-3-opus-20240229", "claude-3-opus")
	e.addAlias("claude-3-sonnet-20240229", "claude-3-sonnet")
	e.addAlias("claude-3-haiku-20240307", "claude-3-haiku")
	e.addAlias("claude-3-5-sonnet-20240620", "claude-3-5-sonnet")
}

// addAlias 添加模型别名
func (e *CostEstimator) addAlias(alias, modelName string) {
	if pricing, ok := e.pricingTable[modelName]; ok {
		aliasPricing := pricing
		aliasPricing.ModelName = alias
		e.pricingTable[alias] = aliasPricing
	}
}

// Estimate 估算成本
func (e *CostEstimator) Estimate(model string, inputTokens, outputTokens int) float64 {
	return e.EstimateWithCache(model, inputTokens, outputTokens, 0)
}

// EstimateWithCache 估算成本（包括缓存 tokens）
func (e *CostEstimator) EstimateWithCache(model string, inputTokens, outputTokens, cachedTokens int) float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	pricing, ok := e.pricingTable[model]
	if !ok {
		// 未知模型使用默认定价（GPT-4 价格作为保守估计）
		pricing = ModelPricing{
			InputPrice:  0.03,
			OutputPrice: 0.06,
		}
	}

	cost := pricing.PricePerRequest

	// 计算输入 token 成本
	if inputTokens > 0 {
		cost += float64(inputTokens) / 1000.0 * pricing.InputPrice
	}

	// 计算输出 token 成本
	if outputTokens > 0 {
		cost += float64(outputTokens) / 1000.0 * pricing.OutputPrice
	}

	// 计算缓存 token 成本（如果支持）
	if cachedTokens > 0 && pricing.CachedPrice > 0 {
		cost += float64(cachedTokens) / 1000.0 * pricing.CachedPrice
	}

	return cost
}

// EstimateInputOnly 仅估算输入成本（用于预估）
func (e *CostEstimator) EstimateInputOnly(model string, inputTokens int) float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	pricing, ok := e.pricingTable[model]
	if !ok {
		pricing = ModelPricing{
			InputPrice: 0.03,
		}
	}

	cost := pricing.PricePerRequest
	if inputTokens > 0 {
		cost += float64(inputTokens) / 1000.0 * pricing.InputPrice
	}

	return cost
}

// EstimateWithRatio 根据输入 token 和输出比例估算
// ratio 为输出/输入的比例，例如 1.5 表示输出是输入的 1.5 倍
func (e *CostEstimator) EstimateWithRatio(model string, inputTokens int, outputRatio float64) float64 {
	estimatedOutputTokens := int(float64(inputTokens) * outputRatio)
	return e.Estimate(model, inputTokens, estimatedOutputTokens)
}

// GetPricing 获取模型定价信息
func (e *CostEstimator) GetPricing(model string) (ModelPricing, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	pricing, ok := e.pricingTable[model]
	return pricing, ok
}

// SetPricing 设置模型定价（用于动态更新价格）
func (e *CostEstimator) SetPricing(model string, pricing ModelPricing) {
	e.mu.Lock()
	defer e.mu.Unlock()

	pricing.ModelName = model
	e.pricingTable[model] = pricing
}

// UpdatePricing 批量更新定价
func (e *CostEstimator) UpdatePricing(pricingMap map[string]ModelPricing) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for model, pricing := range pricingMap {
		pricing.ModelName = model
		e.pricingTable[model] = pricing
	}
}

// GetAllPricing 获取所有定价信息
func (e *CostEstimator) GetAllPricing() map[string]ModelPricing {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make(map[string]ModelPricing, len(e.pricingTable))
	for k, v := range e.pricingTable {
		result[k] = v
	}
	return result
}

// FormatCost 格式化成本为可读字符串
func FormatCost(cost float64) string {
	if cost < 0.001 {
		return fmt.Sprintf("$%.6f", cost)
	} else if cost < 1.0 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}
