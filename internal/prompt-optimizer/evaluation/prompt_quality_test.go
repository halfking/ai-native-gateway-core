package evaluation

import (
	"context"
	"strings"
	"testing"
)

// TestScorePromptQualityDetectsStructure 钉住结构质量分能区分有无结构的提示词。
func TestScorePromptQualityDetectsStructure(t *testing.T) {
	structured := `# 角色定义

你是一个专业翻译专家。

# 任务目标

翻译这段文字。

# 约束条件

- 必须保持原文语气

# 输出格式

[主要内容]

# 质量检查

完成后请检查。
`

	score := ScorePromptQuality(structured)
	if score <= 0.5 {
		t.Errorf("结构完整的提示词应得较高分，实际 %.2f", score)
	}

	plain := ScorePromptQuality("帮我翻译一下")
	if plain >= score {
		t.Errorf("无结构提示词应得分更低: plain=%.2f structured=%.2f", plain, score)
	}
	if plain != 0 {
		t.Errorf("无结构提示词应得 0 分，实际 %.2f", plain)
	}
}

// TestScorePromptQualityEmptyPrompt 钉住空提示词得 0 分。
func TestScorePromptQualityEmptyPrompt(t *testing.T) {
	if got := ScorePromptQuality("   "); got != 0 {
		t.Errorf("空提示词应得 0 分，实际 %.2f", got)
	}
}

// TestScorePromptQualityIsBounded 钉住分数落在 [0,1]。
func TestScorePromptQualityIsBounded(t *testing.T) {
	inputs := []string{
		"",
		"x",
		"# 角色定义\n# 任务目标\n# 约束条件\n# 输出格式\n# 执行步骤\n# 质量检查\n# 参考示例\n# 错误处理",
		strings.Repeat("必须 ", 100),
	}
	for _, in := range inputs {
		got := ScorePromptQuality(in)
		if got < 0 || got > 1 {
			t.Errorf("分数越界 %.4f（输入 %.20q）", got, in)
		}
	}
}

// TestEvaluatePromptQualityRequiresNonEmpty 钉住空提示词报错。
func TestEvaluatePromptQualityRequiresNonEmpty(t *testing.T) {
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), nil)

	if _, err := e.EvaluatePromptQuality(context.Background(), "  "); err == nil {
		t.Fatal("空提示词必须报错")
	}
}

// TestEvaluatePromptQualityWorksWithoutLLM 钉住不接 LLM 也能评估。
//
// 这是 /api/v1/compare 默认口径能开箱可用的前提。
func TestEvaluatePromptQualityWorksWithoutLLM(t *testing.T) {
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), nil)

	score, err := e.EvaluatePromptQuality(context.Background(), "# 角色定义\n你是一个助手\n# 任务目标\n做事")
	if err != nil {
		t.Fatalf("无 LLM 客户端时结构质量评估应可用: %v", err)
	}
	if score <= 0 {
		t.Errorf("应有正分，实际 %.2f", score)
	}
}

// TestPromptQualityBreakdownReportsMissingAspects 钉住能定位缺哪一块。
func TestPromptQualityBreakdownReportsMissingAspects(t *testing.T) {
	e := NewEvaluator(DefaultEvaluatorConfig(), DefaultMetrics(), nil)

	breakdown, err := e.PromptQualityBreakdown(context.Background(), "# 角色定义\n你是一个助手")
	if err != nil {
		t.Fatalf("PromptQualityBreakdown: %v", err)
	}
	if !breakdown["角色定义"] {
		t.Error("应识别出角色定义存在")
	}
	if breakdown["错误处理"] {
		t.Error("应识别出错误处理缺失")
	}
}
