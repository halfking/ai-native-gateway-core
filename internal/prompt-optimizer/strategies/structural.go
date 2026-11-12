package strategies

import (
	"context"
	"fmt"
	"strings"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// StructuralStrategy 结构化增强策略
// 基于研究论文的最佳实践，将提示词重构为结构化格式
type StructuralStrategy struct {
	config StructuralConfig
}

// StructuralConfig 结构化策略配置
type StructuralConfig struct {
	// 是否添加角色定义
	AddRoleDefinition bool

	// 是否使用分层指令
	UseHierarchicalInstructions bool

	// 是否注入约束
	InjectConstraints bool

	// 是否添加输出格式说明
	AddOutputFormat bool

	// 是否添加思维链提示
	AddChainOfThought bool
}

// NewStructural 创建结构化策略
func NewStructural(cfg StructuralConfig) optimizer.Strategy {
	return &StructuralStrategy{
		config: cfg,
	}
}

// Name 策略名称
func (s *StructuralStrategy) Name() string {
	return "structural"
}

// Apply 应用结构化策略
func (s *StructuralStrategy) Apply(ctx context.Context, prompt string, strategyCtx optimizer.StrategyContext) (string, error) {
	builder := &PromptBuilder{}

	// 1. 添加角色定义
	if s.config.AddRoleDefinition {
		role := s.generateRoleDefinition(strategyCtx)
		builder.AddSection("角色定义", role)
	}

	// 2. 分析原始提示词并重构
	analyzed := s.analyzePrompt(prompt)

	// 3. 添加任务目标
	builder.AddSection("任务目标", analyzed.Objective)

	// 4. 添加分层指令
	if s.config.UseHierarchicalInstructions && len(analyzed.Steps) > 0 {
		builder.AddSection("执行步骤", s.formatHierarchicalSteps(analyzed.Steps))
	}

	// 5. 添加约束
	if s.config.InjectConstraints {
		constraints := s.generateConstraints(strategyCtx)
		if len(constraints) > 0 {
			builder.AddSection("约束条件", strings.Join(constraints, "\n"))
		}
	}

	// 6. 添加输出格式
	if s.config.AddOutputFormat {
		format := s.generateOutputFormat(strategyCtx)
		builder.AddSection("输出格式", format)
	}

	// 7. 添加思维链提示
	if s.config.AddChainOfThought {
		builder.AddSection("思考过程", "请逐步思考并说明你的推理过程。")
	}

	// 8. 添加质量检查
	builder.AddSection("质量检查", "完成后请检查:\n1. 是否完全满足了任务目标\n2. 是否遵守了所有约束条件\n3. 输出格式是否正确")

	return builder.Build(), nil
}

// Score 评估提示词质量
func (s *StructuralStrategy) Score(ctx context.Context, prompt string, testCase optimizer.TestCase) (float64, error) {
	score := 0.0
	maxScore := 5.0

	// 检查结构化程度
	if strings.Contains(prompt, "角色定义") || strings.Contains(prompt, "你是") {
		score += 1.0
	}
	if strings.Contains(prompt, "任务目标") || strings.Contains(prompt, "目标") {
		score += 1.0
	}
	if strings.Contains(prompt, "步骤") || strings.Contains(prompt, "流程") {
		score += 1.0
	}
	if strings.Contains(prompt, "约束") || strings.Contains(prompt, "限制") {
		score += 1.0
	}
	if strings.Contains(prompt, "格式") || strings.Contains(prompt, "输出") {
		score += 1.0
	}

	return score / maxScore, nil
}

// generateRoleDefinition 生成角色定义
func (s *StructuralStrategy) generateRoleDefinition(ctx optimizer.StrategyContext) string {
	role := "你是一个"

	// 根据任务类型定义角色
	switch ctx.TaskType {
	case "translation":
		role += "专业翻译专家，精通多语言转换，能准确传达原文含义和语境"
	case "code_generation":
		role += "资深软件工程师，拥有丰富的编程经验，熟悉多种编程语言和最佳实践"
	case "qa":
		role += "知识渊博的问答专家，能够准确理解问题并提供详细、准确的答案"
	case "analysis":
		role += "数据分析专家，擅长从复杂数据中提取洞察并给出可操作的建议"
	default:
		role += "经验丰富的AI助手，能够准确理解需求并提供高质量的帮助"
	}

	// 添加领域专长
	if ctx.Domain != "" && ctx.Domain != "general" {
		role += fmt.Sprintf("，特别精通%s领域", ctx.Domain)
	}

	role += "。"

	return role
}

// analyzePrompt 分析提示词
func (s *StructuralStrategy) analyzePrompt(prompt string) *PromptAnalysis {
	analysis := &PromptAnalysis{
		Objective: prompt,
		Steps:     []string{},
	}

	// 简单的步骤提取（实际应该更智能）
	lines := strings.Split(prompt, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 检测步骤标记
		if strings.HasPrefix(line, "1.") || strings.HasPrefix(line, "2.") ||
			strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") {
			analysis.Steps = append(analysis.Steps, line)
		}
	}

	return analysis
}

// formatHierarchicalSteps 格式化分层步骤
func (s *StructuralStrategy) formatHierarchicalSteps(steps []string) string {
	if len(steps) == 0 {
		return "1. 理解任务需求\n2. 制定执行方案\n3. 逐步实施\n4. 验证结果"
	}

	formatted := ""
	for i, step := range steps {
		// 清理步骤文本
		step = strings.TrimSpace(step)
		step = strings.TrimPrefix(step, "-")
		step = strings.TrimPrefix(step, "*")
		step = strings.TrimSpace(step)

		formatted += fmt.Sprintf("%d. %s\n", i+1, step)
	}

	return strings.TrimSpace(formatted)
}

// generateConstraints 生成约束条件
func (s *StructuralStrategy) generateConstraints(ctx optimizer.StrategyContext) []string {
	constraints := []string{}

	// 添加预定义约束
	if len(ctx.Constraints) > 0 {
		constraints = append(constraints, ctx.Constraints...)
	}

	// 添加通用约束
	constraints = append(constraints,
		"- 确保输出内容准确、完整",
		"- 使用清晰、易懂的语言",
		"- 避免歧义和模糊表述",
	)

	// 根据任务类型添加特定约束
	switch ctx.TaskType {
	case "code_generation":
		constraints = append(constraints,
			"- 代码必须遵循最佳实践和编码规范",
			"- 添加必要的注释和文档",
			"- 考虑错误处理和边界情况",
		)
	case "translation":
		constraints = append(constraints,
			"- 保持原文的语气和风格",
			"- 确保专业术语翻译准确",
			"- 注意文化差异和语境",
		)
	}

	return constraints
}

// generateOutputFormat 生成输出格式说明
func (s *StructuralStrategy) generateOutputFormat(ctx optimizer.StrategyContext) string {
	format := "请按以下格式输出:\n\n"

	switch ctx.TaskType {
	case "code_generation":
		format += "```[语言]\n[代码]\n```\n\n说明: [代码说明]"
	case "analysis":
		format += "## 分析结果\n\n### 关键发现\n- [发现1]\n- [发现2]\n\n### 建议\n- [建议1]\n- [建议2]"
	default:
		format += "[主要内容]\n\n[补充说明]"
	}

	return format
}

// PromptAnalysis 提示词分析结果
type PromptAnalysis struct {
	Objective   string
	Steps       []string
	Constraints []string
	Context     string
}

// PromptBuilder 提示词构建器
type PromptBuilder struct {
	sections []Section
}

// Section 提示词章节
type Section struct {
	Title   string
	Content string
}

// AddSection 添加章节
func (pb *PromptBuilder) AddSection(title, content string) {
	if content == "" {
		return
	}
	pb.sections = append(pb.sections, Section{
		Title:   title,
		Content: content,
	})
}

// Build 构建最终提示词
func (pb *PromptBuilder) Build() string {
	if len(pb.sections) == 0 {
		return ""
	}

	var builder strings.Builder

	for i, section := range pb.sections {
		builder.WriteString(fmt.Sprintf("# %s\n\n", section.Title))
		builder.WriteString(section.Content)

		if i < len(pb.sections)-1 {
			builder.WriteString("\n\n")
		}
	}

	return builder.String()
}

// DefaultStructuralConfig 默认结构化配置
func DefaultStructuralConfig() StructuralConfig {
	return StructuralConfig{
		AddRoleDefinition:           true,
		UseHierarchicalInstructions: true,
		InjectConstraints:           true,
		AddOutputFormat:             true,
		AddChainOfThought:           false, // 可选开启
	}
}
