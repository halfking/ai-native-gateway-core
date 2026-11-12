package harness

import (
	"context"
	"fmt"
	"strings"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// Injector Harness要素注入器
// 自动注入工具定义、安全约束、错误处理等要素
type Injector struct {
	config       InjectorConfig
	toolRegistry ToolRegistry
	safetyRules  SafetyRules
}

// InjectorConfig 注入器配置
type InjectorConfig struct {
	InjectTools         bool
	InjectSafety        bool
	InjectErrors        bool
	InjectObservability bool
}

// ToolRegistry 工具注册表接口
type ToolRegistry interface {
	GetAvailableTools(ctx context.Context) ([]optimizer.ToolDefinition, error)
	GetToolsByCategory(ctx context.Context, category string) ([]optimizer.ToolDefinition, error)
}

// SafetyRules 安全规则接口
type SafetyRules interface {
	GetRules(ctx context.Context, domain string) ([]string, error)
}

// NewInjector 创建新的注入器
func NewInjector(cfg InjectorConfig, registry ToolRegistry, rules SafetyRules) *Injector {
	return &Injector{
		config:       cfg,
		toolRegistry: registry,
		safetyRules:  rules,
	}
}

// Inject 注入Harness要素
func (inj *Injector) Inject(ctx context.Context, prompt string, strategyCtx optimizer.StrategyContext) (string, []string, error) {
	injected := []string{}
	builder := &HarnessBuilder{basePrompt: prompt}

	// 1. 注入工具定义
	if inj.config.InjectTools && inj.toolRegistry != nil {
		tools := strategyCtx.Tools
		if len(tools) == 0 {
			// 从注册表获取工具
			var err error
			tools, err = inj.toolRegistry.GetAvailableTools(ctx)
			if err != nil {
				return "", nil, fmt.Errorf("get tools: %w", err)
			}
		}

		if len(tools) > 0 {
			builder.AddTools(tools)
			injected = append(injected, "tools")
		}
	}

	// 2. 注入安全约束
	if inj.config.InjectSafety && inj.safetyRules != nil {
		rules, err := inj.safetyRules.GetRules(ctx, strategyCtx.Domain)
		if err != nil {
			return "", nil, fmt.Errorf("get safety rules: %w", err)
		}

		if len(rules) > 0 {
			builder.AddSafetyRules(rules)
			injected = append(injected, "safety")
		}
	}

	// 3. 注入错误处理指令
	if inj.config.InjectErrors {
		builder.AddErrorHandling()
		injected = append(injected, "error_handling")
	}

	// 4. 注入可观测性要素
	if inj.config.InjectObservability {
		builder.AddObservability()
		injected = append(injected, "observability")
	}

	result := builder.Build()
	return result, injected, nil
}

// HarnessBuilder Harness构建器
type HarnessBuilder struct {
	basePrompt string
	sections   []HarnessSection
}

// HarnessSection Harness章节
type HarnessSection struct {
	Title   string
	Content string
	Order   int
}

// AddTools 添加工具定义
func (hb *HarnessBuilder) AddTools(tools []optimizer.ToolDefinition) {
	if len(tools) == 0 {
		return
	}

	var builder strings.Builder
	builder.WriteString("你可以使用以下工具来完成任务：\n\n")

	for _, tool := range tools {
		builder.WriteString(fmt.Sprintf("### %s\n", tool.Name))
		builder.WriteString(fmt.Sprintf("**描述:** %s\n\n", tool.Description))

		if len(tool.Parameters) > 0 {
			builder.WriteString("**参数:**\n")
			for name, param := range tool.Parameters {
				builder.WriteString(fmt.Sprintf("- `%s`: %v\n", name, param))
			}
			builder.WriteString("\n")
		}

		if len(tool.Constraints) > 0 {
			builder.WriteString("**约束:**\n")
			for _, constraint := range tool.Constraints {
				builder.WriteString(fmt.Sprintf("- %s\n", constraint))
			}
			builder.WriteString("\n")
		}

		builder.WriteString(fmt.Sprintf("**用法:** `%s(...)`\n\n", tool.Name))
	}

	builder.WriteString("使用工具时:\n")
	builder.WriteString("1. 仔细阅读工具的描述和约束\n")
	builder.WriteString("2. 确保提供所有必需的参数\n")
	builder.WriteString("3. 验证参数类型和格式\n")
	builder.WriteString("4. 检查工具调用结果并处理可能的错误\n")

	hb.sections = append(hb.sections, HarnessSection{
		Title:   "可用工具",
		Content: builder.String(),
		Order:   10,
	})
}

// AddSafetyRules 添加安全规则
func (hb *HarnessBuilder) AddSafetyRules(rules []string) {
	if len(rules) == 0 {
		return
	}

	var builder strings.Builder
	builder.WriteString("执行任务时必须严格遵守以下安全规则：\n\n")

	for i, rule := range rules {
		builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, rule))
	}

	builder.WriteString("\n**违反安全规则的后果:**\n")
	builder.WriteString("- 任务将被立即终止\n")
	builder.WriteString("- 不得输出任何违规内容\n")
	builder.WriteString("- 必须向用户说明违规原因\n")

	hb.sections = append(hb.sections, HarnessSection{
		Title:   "安全规则",
		Content: builder.String(),
		Order:   5,
	})
}

// AddErrorHandling 添加错误处理指令
func (hb *HarnessBuilder) AddErrorHandling() {
	content := `遇到错误时请按以下方式处理：

## 错误类型

### 1. 工具调用失败
- 检查参数是否正确
- 尝试使用备选方案
- 如果无法恢复，向用户说明情况

### 2. 数据验证失败
- 明确指出哪些数据不符合要求
- 给出修正建议
- 等待用户确认

### 3. 权限不足
- 说明需要的权限
- 引导用户授权
- 不得尝试绕过权限检查

### 4. 超时或资源限制
- 给出当前进度
- 建议分批处理或简化任务
- 保存中间结果

## 错误响应格式

遇到错误时，使用以下格式回复：

` + "```" + `
错误类型: [具体错误类型]
错误描述: [清晰的错误说明]
已完成: [已完成的部分]
建议方案: [恢复或解决建议]
` + "```" + `

记住：**友好、清晰、可操作的错误提示比隐藏错误更重要。**`

	hb.sections = append(hb.sections, HarnessSection{
		Title:   "错误处理",
		Content: content,
		Order:   15,
	})
}

// AddObservability 添加可观测性要素
func (hb *HarnessBuilder) AddObservability() {
	content := `为了提高任务执行的可追踪性，请遵循以下规范：

## 执行追踪

在执行关键步骤时，输出结构化的追踪信息：

` + "```" + `
[TRACE] 步骤: [步骤名称]
[TRACE] 输入: [关键输入参数]
[TRACE] 状态: [开始/进行中/完成/失败]
[TRACE] 输出: [关键输出]
` + "```" + `

## 性能标记

对于耗时操作，记录时间信息：

` + "```" + `
[PERF] 操作: [操作名称]
[PERF] 耗时: [毫秒数]ms
[PERF] 资源: [使用的资源]
` + "```" + `

## 决策日志

对于重要决策，记录决策依据：

` + "```" + `
[DECISION] 选项A vs 选项B
[DECISION] 选择: [选择的选项]
[DECISION] 理由: [选择理由]
` + "```" + `

这些信息将帮助：
- 问题诊断和调试
- 性能优化
- 审计和合规检查`

	hb.sections = append(hb.sections, HarnessSection{
		Title:   "可观测性",
		Content: content,
		Order:   20,
	})
}

// Build 构建最终提示词
func (hb *HarnessBuilder) Build() string {
	if len(hb.sections) == 0 {
		return hb.basePrompt
	}

	// 按顺序排序章节
	sections := make([]HarnessSection, len(hb.sections))
	copy(sections, hb.sections)

	for i := 0; i < len(sections); i++ {
		for j := i + 1; j < len(sections); j++ {
			if sections[j].Order < sections[i].Order {
				sections[i], sections[j] = sections[j], sections[i]
			}
		}
	}

	var builder strings.Builder

	// 添加基础提示词
	builder.WriteString(hb.basePrompt)
	builder.WriteString("\n\n")

	// 添加分隔符
	builder.WriteString("---\n\n")
	builder.WriteString("# 系统能力与约束\n\n")

	// 添加各个章节
	for _, section := range sections {
		builder.WriteString(fmt.Sprintf("## %s\n\n", section.Title))
		builder.WriteString(section.Content)
		builder.WriteString("\n\n")
	}

	return builder.String()
}

// DefaultInjectorConfig 默认注入器配置
func DefaultInjectorConfig() InjectorConfig {
	return InjectorConfig{
		InjectTools:         true,
		InjectSafety:        true,
		InjectErrors:        true,
		InjectObservability: false, // 可选开启
	}
}

// ProductionInjectorConfig 生产安全注入器配置。
//
// 与 DefaultInjectorConfig 的唯一区别：**不注入工具章节**。
//
// 原因（2026-11-12 实测发现）：本仓目前唯一的工具注册表是
// MockToolRegistry —— 它带的是 search / calculate 两个**占位工具**。
// 把占位工具写进真实网关请求的 system prompt，等于告诉模型
// "你可以使用以下工具"，而上游根本不存在这些工具 ⇒ 模型会编造工具调用、
// 或在回答里虚构工具结果。
//
// 因此：真实接线路径必须用本配置（安全约束 + 错误处理），
// 等接上**真实**工具注册表（网关自身的 function/tool 定义）后，
// 再按需把 InjectTools 打开。
//
// 注意：这不改变"该注入的东西被注入了吗"这个问题 —— 工具章节与
// 安全/错误处理章节是分开计数的（OptimizedPrompt.HarnessElements）。
func ProductionInjectorConfig() InjectorConfig {
	return InjectorConfig{
		InjectTools:         false,
		InjectSafety:        true,
		InjectErrors:        true,
		InjectObservability: false,
	}
}

// MockToolRegistry 模拟工具注册表（用于测试）
//
// ⚠️ **不可用于生产路径**：它带的是 search / calculate 两个占位工具，
// 注入真实请求会告诉模型"你有这些工具"而上游并不存在。
// 生产路径用 ProductionInjectorConfig（不注入工具章节），
// 直到接上真实工具注册表为止。
type MockToolRegistry struct {
	tools []optimizer.ToolDefinition
}

// NewMockToolRegistry 创建模拟工具注册表
func NewMockToolRegistry() *MockToolRegistry {
	return &MockToolRegistry{
		tools: []optimizer.ToolDefinition{
			{
				Name:        "search",
				Description: "搜索相关信息",
				Parameters: map[string]interface{}{
					"query": "搜索查询字符串",
					"limit": "返回结果数量(可选，默认10)",
				},
				Constraints: []string{
					"查询长度不超过200字符",
					"limit不超过100",
				},
			},
			{
				Name:        "calculate",
				Description: "执行数学计算",
				Parameters: map[string]interface{}{
					"expression": "数学表达式",
				},
				Constraints: []string{
					"仅支持基本算术运算(+,-,*,/)",
					"不支持复杂函数",
				},
			},
		},
	}
}

// GetAvailableTools 实现接口
func (m *MockToolRegistry) GetAvailableTools(ctx context.Context) ([]optimizer.ToolDefinition, error) {
	return m.tools, nil
}

// GetToolsByCategory 实现接口
func (m *MockToolRegistry) GetToolsByCategory(ctx context.Context, category string) ([]optimizer.ToolDefinition, error) {
	return m.tools, nil
}

// MockSafetyRules 模拟安全规则（用于测试）
type MockSafetyRules struct{}

// NewMockSafetyRules 创建模拟安全规则
func NewMockSafetyRules() *MockSafetyRules {
	return &MockSafetyRules{}
}

// GetRules 实现接口
func (m *MockSafetyRules) GetRules(ctx context.Context, domain string) ([]string, error) {
	rules := []string{
		"不得访问未授权的数据或资源",
		"不得执行可能造成损害的操作",
		"必须验证所有用户输入",
		"敏感信息必须脱敏处理",
		"遵守数据隐私和保护法规",
	}

	// 根据领域添加特定规则
	switch domain {
	case "medical":
		rules = append(rules, "必须遵守医疗信息隐私法规(HIPAA等)")
	case "financial":
		rules = append(rules, "必须遵守金融数据保护规定")
	case "legal":
		rules = append(rules, "必须保护律师-客户特权信息")
	}

	return rules, nil
}
