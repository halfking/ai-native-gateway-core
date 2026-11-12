package promptoptimizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Optimizer 提示词优化器核心接口
type Optimizer interface {
	// Optimize 优化提示词
	Optimize(ctx context.Context, prompt string, opts OptimizeOptions) (*OptimizedPrompt, error)

	// OptimizeWithEvaluation 优化提示词并进行评估
	OptimizeWithEvaluation(ctx context.Context, prompt string, evalOpts EvaluationOptions) (*OptimizationResult, error)

	// OptimizeBatch 批量优化提示词
	OptimizeBatch(ctx context.Context, prompts []string, opts OptimizeOptions) ([]*OptimizedPrompt, error)
}

// OptimizeOptions 优化选项
type OptimizeOptions struct {
	// 任务类型: translation, code_generation, qa, analysis, etc.
	TaskType string

	// 领域: technical, medical, legal, general, etc.
	Domain string

	// 目标模型
	TargetModel string

	// 优化策略列表
	Strategies []string

	// 是否使用Memora历史
	UseMemora bool

	// 是否注入Harness要素
	InjectHarness bool

	// 最大迭代次数
	MaxIterations int

	// TestCases 供迭代优化策略评估候选变体。
	//
	// 为空时迭代优化退化为 no-op —— 这是刻意的：没有度量就没有优化目标，
	// 与其瞎改不如不动（参见 README "度量的局限与不做的事"）。
	TestCases []TestCase

	// 超时设置
	Timeout time.Duration
}

// OptimizedPrompt 优化后的提示词
type OptimizedPrompt struct {
	// 原始提示词
	Original string `json:"original"`

	// 优化后的提示词
	Optimized string `json:"optimized"`

	// 应用的策略
	AppliedStrategies []string `json:"applied_strategies"`

	// 注入的Harness要素
	HarnessElements []string `json:"harness_elements"`

	// 使用的上下文
	ContextSources []string `json:"context_sources"`

	// 优化过程元数据
	Metadata OptimizationMetadata `json:"metadata"`
}

// OptimizationMetadata 优化元数据
type OptimizationMetadata struct {
	// 优化开始时间
	StartTime time.Time `json:"start_time"`

	// 优化结束时间
	EndTime time.Time `json:"end_time"`

	// 优化耗时（序列化为可读字符串，如 "67.125µs"）
	Duration Duration `json:"duration"`

	// 迭代次数
	Iterations int `json:"iterations"`

	// 使用的Token数
	TokensUsed int `json:"tokens_used"`

	// 置信度分数 (0-1)
	ConfidenceScore float64 `json:"confidence_score"`

	// 优化历史
	History []OptimizationStep `json:"history"`
}

// Duration 带 JSON 序列化的时长类型。
//
// 为什么不用裸 time.Duration：time.Duration 的零值 JSON 表示是纳秒整数
// （如 67125），对 API 消费方不可读；本类型输出 "67.125µs" 这类可读串。
type Duration time.Duration

// String 返回可读的时长字符串。
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON 序列化为可读字符串。
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// OptimizationStep 优化步骤
type OptimizationStep struct {
	Step     int      `json:"step"`
	Strategy string   `json:"strategy"`
	Prompt   string   `json:"prompt"`
	Score    float64  `json:"score"`
	Changes  []string `json:"changes"`
}

// EvaluationOptions 评估选项
type EvaluationOptions struct {
	// 测试用例
	TestCases []TestCase

	// 评估指标
	Metrics []string

	// 是否进行A/B测试
	ABTest bool

	// 样本大小
	SampleSize int
}

// TestCase 测试用例
type TestCase struct {
	ID          string                 `json:"id"`
	Input       string                 `json:"input"`
	Expected    string                 `json:"expected"`
	Context     map[string]interface{} `json:"context,omitempty"`
	Constraints []string               `json:"constraints,omitempty"`
}

// OptimizationResult 优化结果（含评估）
type OptimizationResult struct {
	// 优化后的提示词
	Prompt *OptimizedPrompt `json:"prompt"`

	// 基线分数（原始提示词）
	BaselineScore float64 `json:"baseline_score"`

	// 优化后分数
	OptimizedScore float64 `json:"optimized_score"`

	// 提升幅度 (%)
	Improvement float64 `json:"improvement"`

	// 绝对分差 (优化后 - 基线)。
	//
	// 当 BaselineScore == 0 时相对提升无定义（Improvement 保持 0），
	// 此时用绝对分差表达真实差异，避免用 NaN/Inf 掩盖信息。
	AbsoluteDelta float64 `json:"absolute_delta"`

	// 各项指标得分
	MetricScores map[string]float64 `json:"metric_scores"`

	// 测试结果详情
	TestResults []TestResult `json:"test_results"`

	// 建议
	Recommendations []string `json:"recommendations"`
}

// TestResult 测试结果
type TestResult struct {
	TestCaseID string             `json:"test_case_id"`
	Passed     bool               `json:"passed"`
	Score      float64            `json:"score"`
	Output     string             `json:"output"`
	Error      string             `json:"error"`
	Metrics    map[string]float64 `json:"metrics"`
}

// Strategy 优化策略接口
type Strategy interface {
	// Name 策略名称
	Name() string

	// Apply 应用策略
	Apply(ctx context.Context, prompt string, context StrategyContext) (string, error)

	// Score 评估提示词质量
	Score(ctx context.Context, prompt string, testCase TestCase) (float64, error)
}

// StrategyContext 策略上下文
type StrategyContext struct {
	TaskType    string
	Domain      string
	TargetModel string
	History     []string
	Constraints []string
	Tools       []ToolDefinition
	Examples    []Example

	// TestCases 供迭代优化策略评估候选变体。
	//
	// 没有测试用例时迭代优化会退化为 no-op（拿不到分数就无法选优）。
	TestCases []TestCase
}

// ToolDefinition 工具定义
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Constraints []string               `json:"constraints,omitempty"`
}

// Example Few-shot示例
type Example struct {
	Input       string  `json:"input"`
	Output      string  `json:"output"`
	Explanation string  `json:"explanation,omitempty"`
	Quality     float64 `json:"quality"`
}

// optimizer 优化器实现
type optimizer struct {
	strategies      []Strategy
	contextManager  ContextManager
	harnessInjector HarnessInjector
	evaluator       Evaluator
	config          Config
}

// Config 优化器配置
type Config struct {
	// 默认策略
	DefaultStrategies []string

	// Memora配置
	MemoraConfig MemoraConfig

	// Harness配置
	HarnessConfig HarnessConfig

	// 评估配置
	EvaluationConfig EvaluationConfig

	// 迭代配置
	IterationConfig IterationConfig
}

// MemoraConfig Memora配置
type MemoraConfig struct {
	Enabled             bool
	MaxHistory          int
	SimilarityThreshold float64
	CacheEnabled        bool
}

// HarnessConfig Harness配置
type HarnessConfig struct {
	InjectTools  bool
	InjectSafety bool
	InjectErrors bool
	ToolRegistry string
}

// EvaluationConfig 评估配置
type EvaluationConfig struct {
	Enabled    bool
	Metrics    []string
	Concurrent bool
	MaxWorkers int
}

// IterationConfig 迭代配置
type IterationConfig struct {
	MaxIterations        int
	ConvergenceThreshold float64
	EarlyStoppingEnabled bool
	ImprovementThreshold float64
}

// NewOptimizer 创建新的优化器
func NewOptimizer(opts ...OptimizerOption) Optimizer {
	opt := &optimizer{
		config: defaultConfig(),
	}

	for _, o := range opts {
		o(opt)
	}

	return opt
}

// OptimizerOption 优化器选项函数
type OptimizerOption func(*optimizer)

// WithStrategies 设置优化策略
func WithStrategies(strategies ...Strategy) OptimizerOption {
	return func(o *optimizer) {
		o.strategies = append(o.strategies, strategies...)
	}
}

// WithContextManager 设置上下文管理器
func WithContextManager(cm ContextManager) OptimizerOption {
	return func(o *optimizer) {
		o.contextManager = cm
	}
}

// WithHarnessInjector 设置Harness注入器
func WithHarnessInjector(hi HarnessInjector) OptimizerOption {
	return func(o *optimizer) {
		o.harnessInjector = hi
	}
}

// WithEvaluator 设置评估器
func WithEvaluator(eval Evaluator) OptimizerOption {
	return func(o *optimizer) {
		o.evaluator = eval
	}
}

// WithConfig 设置配置
func WithConfig(cfg Config) OptimizerOption {
	return func(o *optimizer) {
		o.config = cfg
	}
}

// Optimize 实现优化接口
func (o *optimizer) Optimize(ctx context.Context, prompt string, opts OptimizeOptions) (*OptimizedPrompt, error) {
	startTime := time.Now()

	// 1. 分析意图
	intent, err := o.analyzeIntent(ctx, prompt, opts)
	if err != nil {
		return nil, fmt.Errorf("analyze intent: %w", err)
	}

	// 2. 准备上下文
	strategyCtx, err := o.prepareContext(ctx, intent, opts)
	if err != nil {
		return nil, fmt.Errorf("prepare context: %w", err)
	}

	// 3. 选择策略
	strategies := o.selectStrategies(intent, opts)

	// 4. 应用策略
	optimized := prompt
	appliedStrategies := []string{}
	history := []OptimizationStep{}

	for i, strategy := range strategies {
		result, err := strategy.Apply(ctx, optimized, strategyCtx)
		if err != nil {
			return nil, fmt.Errorf("apply strategy %s: %w", strategy.Name(), err)
		}

		optimized = result
		appliedStrategies = append(appliedStrategies, strategy.Name())

		// 记录历史
		history = append(history, OptimizationStep{
			Step:     i + 1,
			Strategy: strategy.Name(),
			Prompt:   optimized,
			Score:    0.0, // 如果需要可以评分
		})
	}

	// 5. 注入Harness要素
	harnessElements := []string{}
	if opts.InjectHarness {
		optimized, harnessElements, err = o.injectHarness(ctx, optimized, strategyCtx)
		if err != nil {
			return nil, fmt.Errorf("inject harness: %w", err)
		}
	}

	// 6. 构建结果
	result := &OptimizedPrompt{
		Original:          prompt,
		Optimized:         optimized,
		AppliedStrategies: appliedStrategies,
		HarnessElements:   harnessElements,
		ContextSources:    strategyCtx.contextSources(),
		Metadata: OptimizationMetadata{
			StartTime:       startTime,
			EndTime:         time.Now(),
			Duration:        Duration(time.Since(startTime)),
			Iterations:      len(strategies),
			ConfidenceScore: 0.85, // 可以根据实际情况计算
			History:         history,
		},
	}

	return result, nil
}

// OptimizeWithEvaluation 实现带评估的优化
func (o *optimizer) OptimizeWithEvaluation(ctx context.Context, prompt string, evalOpts EvaluationOptions) (*OptimizationResult, error) {
	// 0. 评估器是必需依赖：缺失时明确报错，而不是 nil 接口调用 panic。
	if o.evaluator == nil {
		return nil, fmt.Errorf("evaluate: evaluator is not configured (use WithEvaluator)")
	}
	if len(evalOpts.TestCases) == 0 {
		return nil, fmt.Errorf("evaluate: test cases must not be empty")
	}

	// 1. 优化提示词
	optimized, err := o.Optimize(ctx, prompt, OptimizeOptions{
		UseMemora:     true,
		InjectHarness: true,
	})
	if err != nil {
		return nil, fmt.Errorf("optimize: %w", err)
	}

	// 2. 评估原始提示词
	baselineScore, err := o.evaluator.Evaluate(ctx, prompt, evalOpts)
	if err != nil {
		return nil, fmt.Errorf("evaluate baseline: %w", err)
	}

	// 3. 评估优化后提示词
	optimizedScore, err := o.evaluator.Evaluate(ctx, optimized.Optimized, evalOpts)
	if err != nil {
		return nil, fmt.Errorf("evaluate optimized: %w", err)
	}

	// 4. 计算提升。
	//
	// 基线为 0 时相对提升无定义（0/0 = NaN，x/0 = +Inf），
	// 直接返回会让 JSON 序列化出 NaN（标准库会报错，替代实现会写出非法 JSON）。
	// 因此此时只报告绝对差值，并显式标注相对提升不可用。
	improvement := 0.0
	if baselineScore != 0 {
		improvement = ((optimizedScore - baselineScore) / baselineScore) * 100
	}

	result := &OptimizationResult{
		Prompt:         optimized,
		BaselineScore:  baselineScore,
		OptimizedScore: optimizedScore,
		Improvement:    improvement,
		MetricScores:   make(map[string]float64),
		AbsoluteDelta:  optimizedScore - baselineScore,
	}

	// 5. 填充逐指标得分（评估器支持时）。
	if agg, ok := o.evaluator.(interface {
		MetricBreakdown(ctx context.Context, prompt string, opts EvaluationOptions) (map[string]float64, error)
	}); ok {
		metrics, err := agg.MetricBreakdown(ctx, optimized.Optimized, evalOpts)
		if err == nil {
			result.MetricScores = metrics
		}
	}

	return result, nil
}

// OptimizeBatch 批量优化
func (o *optimizer) OptimizeBatch(ctx context.Context, prompts []string, opts OptimizeOptions) ([]*OptimizedPrompt, error) {
	results := make([]*OptimizedPrompt, len(prompts))

	for i, prompt := range prompts {
		result, err := o.Optimize(ctx, prompt, opts)
		if err != nil {
			return nil, fmt.Errorf("optimize prompt %d: %w", i, err)
		}
		results[i] = result
	}

	return results, nil
}

// 辅助方法

// analyzeIntent 分析待优化提示词的意图。
//
// 当前实现是"确定性启发式"（不调用 LLM）：
//   - 显式传入的 TaskType/Domain 优先（调用方已知意图）；
//   - 未传入时，从提示词正文做关键词推断（中文 + 英文）；
//   - 从正文里抽取显式约束句（"必须…"、"不得…"、"不要…"）。
//
// 为什么不用 LLM 推断：意图分析的输入只有一段提示词，调用 LLM 会把
// 每次优化的成本翻倍，而关键词足以覆盖绝大多数分类场景；且确定性输出
// 便于测试钉住。若后续要上 LLM 推断，应做成可选策略而非替换本实现。
func (o *optimizer) analyzeIntent(ctx context.Context, prompt string, opts OptimizeOptions) (*Intent, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("analyze intent: prompt must not be empty")
	}

	intent := &Intent{
		TaskType: opts.TaskType,
		Domain:   opts.Domain,
	}

	if intent.TaskType == "" {
		intent.TaskType = inferTaskType(prompt)
	}
	if intent.Domain == "" {
		intent.Domain = inferDomain(prompt)
	}
	intent.Constraints = extractConstraints(prompt)

	return intent, nil
}

// taskTypeHints 任务类型关键词表。顺序即优先级（先匹配到的胜出）。
var taskTypeHints = []struct {
	taskType string
	keywords []string
}{
	{"translation", []string{"翻译", "translate", "译文", "译成"}},
	{"code_generation", []string{"代码", "函数", "实现", "写一个程序", "重构", "debug", "code", "function", "implement"}},
	{"analysis", []string{"分析", "统计", "评估", "指标", "analyze", "evaluate"}},
	{"summarization", []string{"总结", "摘要", "概括", "summarize", "summary", "tldr"}},
	{"qa", []string{"回答", "请问", "解释", "什么是", "why", "what is", "explain", "answer"}},
	{"generation", []string{"生成", "写一份", "创作", "generate", "create", "write"}},
}

// inferTaskType 从提示词推断任务类型。
func inferTaskType(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, hint := range taskTypeHints {
		for _, kw := range hint.keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return hint.taskType
			}
		}
	}
	return "general"
}

// domainHints 领域关键词表。
var domainHints = []struct {
	domain   string
	keywords []string
}{
	{"medical", []string{"医疗", "临床", "患者", "诊断", "medical", "clinical", "patient"}},
	{"legal", []string{"法律", "合同", "诉讼", "法条", "legal", "contract", "lawsuit"}},
	{"financial", []string{"金融", "财务", "收益", "投资", "financial", "revenue", "investment"}},
	{"security", []string{"安全", "漏洞", "攻击", "渗透", "security", "vulnerability", "exploit"}},
	{"technical", []string{"代码", "API", "数据库", "部署", "服务器", "code", "api", "database", "deploy", "server"}},
}

// inferDomain 从提示词推断领域。
func inferDomain(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, hint := range domainHints {
		for _, kw := range hint.keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return hint.domain
			}
		}
	}
	return "general"
}

// constraintMarkers 显式约束的句首标记。
var constraintMarkers = []string{"必须", "不得", "不要", "禁止", "应该", "务必", "严禁", "must", "must not", "do not", "never", "always"}

// extractConstraints 抽取提示词中显式写下的约束句。
//
// 只保留整句包含约束标记的条目；标记出现在句首时该句几乎必然是约束。
func extractConstraints(prompt string) []string {
	sentences := splitSentences(prompt)
	constraints := []string{}
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		lower := strings.ToLower(s)
		for _, marker := range constraintMarkers {
			if strings.Contains(lower, marker) {
				constraints = append(constraints, s)
				break
			}
		}
	}
	return constraints
}

// splitSentences 按中英文句末标点切句。
func splitSentences(text string) []string {
	replacer := strings.NewReplacer("。", "\n", "！", "\n", "？", "\n", "；", "\n", ". ", "\n", "! ", "\n", "? ", "\n", "; ", "\n")
	return strings.Split(replacer.Replace(text), "\n")
}

func (o *optimizer) prepareContext(ctx context.Context, intent *Intent, opts OptimizeOptions) (StrategyContext, error) {
	strategyCtx := StrategyContext{
		TaskType:    intent.TaskType,
		Domain:      intent.Domain,
		TargetModel: opts.TargetModel,
		TestCases:   opts.TestCases,
	}

	// 记忆后端（memora）上下文。
	//
	// 契约：后端不可用 ⇒ 退化为"无历史"继续优化（不阻断）；
	// 后端调用失败 ⇒ 报错，让调用方能区分"查不到"与"查失败"。
	if opts.UseMemora && o.contextManager != nil {
		history, err := o.contextManager.GetRelevantHistory(ctx, intent)
		if err != nil {
			return strategyCtx, fmt.Errorf("get history: %w", err)
		}
		strategyCtx.History = history

		examples, err := o.contextManager.GetExamples(ctx, intent.TaskType, intent.Domain)
		if err != nil {
			return strategyCtx, fmt.Errorf("get examples: %w", err)
		}
		strategyCtx.Examples = examples

		constraints, err := o.contextManager.GetConstraints(ctx, intent.Domain)
		if err != nil {
			return strategyCtx, fmt.Errorf("get constraints: %w", err)
		}
		strategyCtx.Constraints = constraints
	}

	return strategyCtx, nil
}

func (o *optimizer) selectStrategies(intent *Intent, opts OptimizeOptions) []Strategy {
	// 如果指定了策略，使用指定的
	if len(opts.Strategies) > 0 {
		selected := []Strategy{}
		for _, s := range o.strategies {
			for _, name := range opts.Strategies {
				if s.Name() == name {
					selected = append(selected, s)
					break
				}
			}
		}
		return selected
	}

	// 否则返回所有策略
	return o.strategies
}

func (o *optimizer) injectHarness(ctx context.Context, prompt string, strategyCtx StrategyContext) (string, []string, error) {
	if o.harnessInjector == nil {
		return prompt, nil, nil
	}

	return o.harnessInjector.Inject(ctx, prompt, strategyCtx)
}

func (sc StrategyContext) contextSources() []string {
	sources := []string{}
	if len(sc.History) > 0 {
		sources = append(sources, "memora_history")
	}
	if len(sc.Tools) > 0 {
		sources = append(sources, "tool_registry")
	}
	if len(sc.Examples) > 0 {
		sources = append(sources, "few_shot_examples")
	}
	return sources
}

// Intent 意图分析结果
type Intent struct {
	TaskType    string
	Domain      string
	Complexity  string
	Constraints []string
}

// defaultConfig 默认配置
func defaultConfig() Config {
	return Config{
		DefaultStrategies: []string{"structural", "fewshot"},
		MemoraConfig: MemoraConfig{
			Enabled:             true,
			MaxHistory:          10,
			SimilarityThreshold: 0.7,
		},
		HarnessConfig: HarnessConfig{
			InjectTools:  true,
			InjectSafety: true,
			InjectErrors: true,
		},
		EvaluationConfig: EvaluationConfig{
			Enabled: true,
			Metrics: []string{"accuracy", "completeness"},
		},
		IterationConfig: IterationConfig{
			MaxIterations:        3,
			ConvergenceThreshold: 0.05,
		},
	}
}
