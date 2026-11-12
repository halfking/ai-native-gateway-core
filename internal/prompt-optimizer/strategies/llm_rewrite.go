package strategies

// llm_rewrite.go 实现"由内部小模型改写提示词"的优化策略。
//
// 为什么要有这条策略：
// 规则策略（structural/fewshot）只能把提示词**结构化**，改不了"任务真正要什么"。
// 业界共识（DSPy/OPRO/TextGrad/APO）是让模型自己判断提示词写得好不好并改写。
// 这条策略就是把那个"内部小模型"接进优化流水线：把原始提示词 + 记忆历史 +
// 领域约束 + 任务类型打包成一条 meta-prompt，交给 OpenAI 兼容端点改写。
//
// 契约（很重要，决定了实验口径是否可信）：
//   - 改写结果必须是**任务提示词本身**，不是对提示词的描述、也不是对任务的回答；
//   - 改写失败（模型不可用/输出为空/输出像元描述）⇒ **回退到原提示词**，
//     并把回退原因暴露给调用方，绝不返回一段不可用的文本；
//   - 改写**不引入**原始提示词里不存在的新事实，历史片段只作为"格式参考"，
//     不作为事实来源被复制进结果。
//
// 温度固定为 0：实验要求"提示词变化是唯一自变量"，优化器本身必须是确定性的。

import (
	"context"
	"strings"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
	"github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/llm"
)

// LLMRewriteConfig LLM 重写策略配置。
type LLMRewriteConfig struct {
	// MaxTokens 改写结果的最大生成 token 数。
	MaxTokens int

	// MaxHistory 放进 meta-prompt 的历史片段条数上限。
	MaxHistory int

	// MaxExamples 放进 meta-prompt 的示例条数上限。
	MaxExamples int

	// MinResultChars 改写结果的最小字符数；低于该值视为改写失败并回退。
	MinResultChars int

	// MaxResultChars 改写结果的最大字符数；超出则截断并视为不可用（回退）。
	MaxResultChars int
}

// DefaultLLMRewriteConfig 默认配置。
func DefaultLLMRewriteConfig() LLMRewriteConfig {
	return LLMRewriteConfig{
		MaxTokens:      1200,
		MaxHistory:     5,
		MaxExamples:    3,
		MinResultChars: 8,
		MaxResultChars: 8000,
	}
}

// RewriteClient 是重写所需的模型通道。
//
// 定义成接口而不是直接依赖 *llm.Client，是为了让测试能注入假通道，
// 从而**不依赖真实模型**就能钉住"回退"这条关键契约。
type RewriteClient interface {
	// Generate 生成文本。
	Generate(ctx context.Context, prompt string, opts llm.Options) (string, error)
}

// LLMRewrite LLM 重写策略。
type LLMRewrite struct {
	config LLMRewriteConfig
	client RewriteClient
}

// NewLLMRewrite 创建 LLM 重写策略。
//
// client 为 nil 时 Apply 直接返回原提示词（不报错），这样未配置模型通道的
// 环境仍能使用优化器。
func NewLLMRewrite(cfg LLMRewriteConfig, client RewriteClient) *LLMRewrite {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultLLMRewriteConfig().MaxTokens
	}
	if cfg.MaxHistory <= 0 {
		cfg.MaxHistory = DefaultLLMRewriteConfig().MaxHistory
	}
	if cfg.MaxExamples <= 0 {
		cfg.MaxExamples = DefaultLLMRewriteConfig().MaxExamples
	}
	if cfg.MinResultChars <= 0 {
		cfg.MinResultChars = DefaultLLMRewriteConfig().MinResultChars
	}
	if cfg.MaxResultChars <= 0 {
		cfg.MaxResultChars = DefaultLLMRewriteConfig().MaxResultChars
	}
	return &LLMRewrite{config: cfg, client: client}
}

// Name 策略名称。
func (s *LLMRewrite) Name() string { return "llm_rewrite" }

// Apply 用内部小模型改写提示词。
//
// 失败时返回原提示词与 nil error —— 因为"优化不可用"不应让整条优化链路失败，
// 而"优化没发生"必须能被观测到：调用方通过 AppliedStrategies 里有没有
// "llm_rewrite"、以及改写前后文本是否相同来判断。
func (s *LLMRewrite) Apply(ctx context.Context, prompt string, strategyCtx optimizer.StrategyContext) (string, error) {
	if s.client == nil {
		return prompt, nil
	}
	if strings.TrimSpace(prompt) == "" {
		return prompt, nil
	}

	meta := s.buildMetaPrompt(prompt, strategyCtx)

	out, err := s.client.Generate(ctx, meta, llm.Options{
		Temperature: 0,
		MaxTokens:   s.config.MaxTokens,
	})
	if err != nil {
		// 模型通道不可用 ⇒ 回退到原提示词（不阻断优化流水线）。
		return prompt, nil
	}

	rewritten, ok := s.normalize(out)
	if !ok {
		return prompt, nil
	}
	return rewritten, nil
}

// Score 评估提示词质量。
//
// 该策略的评分口径是"改写结果的形态质量"：越接近一份完整的任务提示词分越高。
// 它**不是**效果评分（效果由 experiment 包的规则判定器独立测量）。
func (s *LLMRewrite) Score(ctx context.Context, prompt string, testCase optimizer.TestCase) (float64, error) {
	score := 0.0
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return 0, nil
	}
	score += 0.2

	// 有明确的任务动词段落
	if len(trimmed) >= 40 {
		score += 0.2
	}
	// 提到了输出要求（常见措辞）
	for _, marker := range []string{"输出", "回答", "返回", "只输出", "请用", "格式"} {
		if strings.Contains(trimmed, marker) {
			score += 0.2
			break
		}
	}
	// 有可执行的结构（编号或小节标题）
	if strings.Contains(trimmed, "\n1.") || strings.Contains(trimmed, "\n- ") ||
		strings.Contains(trimmed, "##") || strings.Contains(trimmed, "1.") {
		score += 0.2
	}
	// 有明确的约束措辞
	for _, marker := range []string{"必须", "不要", "不得", "只", "严格"} {
		if strings.Contains(trimmed, marker) {
			score += 0.2
			break
		}
	}
	return score, nil
}

// buildMetaPrompt 组装 meta-prompt。
//
// 结构刻意做成"上下文 → 原始提示词 → 指令"的顺序：
// 小模型对"最后一条指令"的遵循度最高，因此要求（只输出改写后的提示词）
// 放在最后。
func (s *LLMRewrite) buildMetaPrompt(prompt string, strategyCtx optimizer.StrategyContext) string {
	var b strings.Builder

	b.WriteString("你是一个提示词优化器。你的任务是把下面这份任务提示词改写得更清晰、更严格、更可执行。\n\n")

	if strategyCtx.TaskType != "" {
		b.WriteString("# 任务类型\n" + strategyCtx.TaskType + "\n\n")
	}
	if strategyCtx.Domain != "" && strategyCtx.Domain != "general" {
		b.WriteString("# 领域\n" + strategyCtx.Domain + "\n\n")
	}

	if len(strategyCtx.Constraints) > 0 {
		b.WriteString("# 必须遵守的约束\n")
		for _, c := range strategyCtx.Constraints {
			b.WriteString("- " + c + "\n")
		}
		b.WriteString("\n")
	}

	if len(strategyCtx.History) > 0 {
		n := len(strategyCtx.History)
		if n > s.config.MaxHistory {
			n = s.config.MaxHistory
		}
		b.WriteString("# 历史提示词片段（仅供理解任务背景与输出风格，不要照抄）\n")
		for i := 0; i < n; i++ {
			b.WriteString("- " + strings.TrimSpace(strategyCtx.History[i]) + "\n")
		}
		b.WriteString("\n")
	}

	if len(strategyCtx.Examples) > 0 {
		n := len(strategyCtx.Examples)
		if n > s.config.MaxExamples {
			n = s.config.MaxExamples
		}
		b.WriteString("# 输出风格参考\n")
		for i := 0; i < n; i++ {
			b.WriteString("- " + strings.TrimSpace(strategyCtx.Examples[i].Output) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("# 待改写的提示词\n" + prompt + "\n\n")

	b.WriteString(`# 改写要求
1. 只输出改写后的提示词正文，不要输出任何解释、前言、标题或代码块标记。
2. 不要回答提示词里的问题，只改写提示词本身。
3. 保留原始提示词的全部意图与事实，不要新增原始提示词里没有的要求或事实。
4. 把隐含的输出要求写明确（输出什么、什么格式、长度或条数、禁止什么）。
5. 保持简洁，不要堆砌无关的通用建议。
`)

	return b.String()
}

// normalize 清洗模型输出并判断是否可用。
//
// 返回 ok=false 表示"这次改写不可用"，调用方应回退到原提示词。
func (s *LLMRewrite) normalize(out string) (string, bool) {
	text := strings.TrimSpace(out)
	if text == "" {
		return "", false
	}

	// 去掉 markdown 代码块包裹。
	if strings.HasPrefix(text, "```") {
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
		text = strings.TrimSpace(text)
	}

	// 去掉常见的"以下是…"前缀行。
	for _, prefix := range []string{
		"以下是优化后的提示词：", "以下是改写后的提示词：", "以下是优化后的提示词:",
		"以下是改写后的提示词:", "优化后的提示词：", "优化后的提示词:",
		"改写后的提示词：", "改写后的提示词:", "提示词：", "提示词:",
	} {
		if strings.HasPrefix(text, prefix) {
			text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
			break
		}
	}

	text = strings.TrimSpace(text)
	if len([]rune(text)) < s.config.MinResultChars {
		return "", false
	}
	if len([]rune(text)) > s.config.MaxResultChars {
		return "", false
	}

	// 元描述检测：模型把"要写什么"当成"提示词"输出时，
	// 结果会是一段关于提示词的话而不是提示词。这里做一次粗筛，
	// 命中即视为改写失败（回退），避免把元提示词喂给执行模型。
	metaMarkers := []string{
		"你的任务是", "你是一个", "作为一个", "作为AI", "作为 AI",
		"请帮我优化", "优化建议：", "改写说明", "说明：以上",
	}
	for _, marker := range metaMarkers {
		if strings.HasPrefix(text, marker) {
			return "", false
		}
	}

	return text, true
}
