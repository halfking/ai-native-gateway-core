package promptoptimizer

import "context"

// ContextManager 上下文管理器接口
// 负责管理和获取优化所需的上下文信息
type ContextManager interface {
	// GetRelevantHistory 获取相关历史
	GetRelevantHistory(ctx context.Context, intent *Intent) ([]string, error)

	// GetExamples 获取示例
	GetExamples(ctx context.Context, taskType, domain string) ([]Example, error)

	// GetConstraints 获取约束
	GetConstraints(ctx context.Context, domain string) ([]string, error)
}

// HarnessInjector Harness注入器接口
type HarnessInjector interface {
	// Inject 注入Harness要素
	Inject(ctx context.Context, prompt string, strategyCtx StrategyContext) (string, []string, error)
}

// Evaluator 评估器接口
type Evaluator interface {
	// Evaluate 评估提示词（跑模型比输出，需要 LLM 通道）。
	Evaluate(ctx context.Context, prompt string, opts EvaluationOptions) (float64, error)

	// EvaluatePrompt 评估单个提示词（跑模型比输出，需要 LLM 通道）。
	EvaluatePrompt(ctx context.Context, prompt string, testCase TestCase) (float64, error)

	// EvaluatePromptQuality 评估提示词的**结构质量**（不需要 LLM 通道）。
	//
	// 衡量的是"该有的要素齐不齐"，不是"效果好不好"。
	EvaluatePromptQuality(ctx context.Context, prompt string) (float64, error)
}
