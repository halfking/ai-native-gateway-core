package evaluation

import (
	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// optimizerEvalOpts 返回一份标准的评估选项（含 2 条测试用例）。
func optimizerEvalOpts() optimizer.EvaluationOptions {
	return optimizer.EvaluationOptions{
		TestCases: []optimizer.TestCase{
			{ID: "t1", Input: "输入1", Expected: "期望输出"},
			{ID: "t2", Input: "输入2", Expected: "期望输出"},
		},
		Metrics: []string{"accuracy", "completeness"},
	}
}

// emptyEvalOpts 返回空测试集的评估选项。
func emptyEvalOpts() optimizer.EvaluationOptions {
	return optimizer.EvaluationOptions{TestCases: nil}
}
