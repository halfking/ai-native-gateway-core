package experiment

// executor.go 提供实验用的执行模型通道。
//
// 执行模型与优化模型可以是同一个（自优化场景），也可以不同。
// 本包不关心这件事 —— 它只关心"用 OpenAI 兼容协议把提示词发给某个模型，
// 把输出拿回来"。因此内部小模型（本地 ollama）、网关路由后的内部模型、
// 云端模型都能用同一套代码接进来。

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
)

// LLMExecutor 是基于 OpenAI 兼容端点的执行模型。
type LLMExecutor struct {
	client      *llm.Client
	model       string
	temperature float64
	maxTokens   int
}

// ExecutorConfig 是执行模型配置。
type ExecutorConfig struct {
	// BaseURL OpenAI 兼容端点，例如 http://127.0.0.1:11434/v1
	BaseURL string
	// APIKey 可选。
	APIKey string
	// Model 模型名。
	Model string
	// Temperature 采样温度。实验默认 0（确定性）。
	Temperature float64
	// MaxTokens 单次输出上限。
	MaxTokens int
	// Timeout 单次请求超时。
	Timeout int
}

// NewLLMExecutor 构造执行模型。
func NewLLMExecutor(cfg ExecutorConfig) (*LLMExecutor, error) {
	client, err := llm.New(llm.Config{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("experiment: executor init failed: %w", err)
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = client.Model()
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	return &LLMExecutor{
		client:      client,
		model:       model,
		temperature: cfg.Temperature,
		maxTokens:   maxTokens,
	}, nil
}

// Model 返回执行模型名。
func (e *LLMExecutor) Model() string { return e.model }

// Complete 执行一次补全。
func (e *LLMExecutor) Complete(ctx context.Context, prompt string) (string, error) {
	out, err := e.client.Generate(ctx, prompt, llm.Options{
		Model:       e.model,
		Temperature: e.temperature,
		MaxTokens:   e.maxTokens,
	})
	if err != nil {
		return "", err
	}
	return out, nil
}
