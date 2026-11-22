// Package plugins 包含 V4 安全插件的具体实现。
//
// PromptInjectionEnhancedPlugin 是增强版提示词注入检测插件，
// 实现 security.Plugin 接口，集成到 V4 governance 流程。
//
// 设计原则：薄适配层（thin adapter）。
// 本插件不重复实现检测逻辑，而是复用 domains/promptinjection.Detector
// 已有的 6 层检测能力（规则/启发式/Canary/向量/LLM/内容替换）。
// 本插件仅负责：
//  1. 依赖注入（*sql.DB）到核心 Detector
//  2. 将 DetectionResult 转换为 governance.Verdict
//
// 接入链：Plugin → security.Registry → SecurityHook → Pipeline(PhaseGovernance)
//
// ★★★ 权威判定方的定稿（2026-11-23 查实，★ 主档
// docs/operations/2026-11-23-211配置优化执行报告.md §9.19·附2 / §9.19·附5）
//
// ★ 「同一输入被两套 PI 实现重复判定」这件事（N8）**查实了**，★ 不是推测：
//
//	· `prompt_injection`（本插件的邻居、★ 包装 v3 `domains/hooks/security` 的
//	  ThreatDetector）：★ 词表 + PII 正则、★ **阈值 7**、★ **命中即 deny sev3**
//	  ⇒ ★ **它是治理链里唯一的硬阻断方**。
//	· **本插件（`prompt_injection_enhanced`）**：★ 核心 `promptinjection.Detector`
//	  的 6 层 + 评分；★ severity 映射 medium=1 / high=2 / critical=3。
//
// ★ ★ **关键事实（本插件在生产上从不 deny）**：★ 动作来源是
//
//	`severity_action_matrix`（★ observe 模式取 observe_action）；★ 而
//	`Blocked = (ActionTaken == "block" || "reject")` ⇒ ★ `replace`/`terminate`/
//	`warn`/`log` **都不算 deny** ⇒ ★ 线上 `detection_mode=observe` +
//	medium/high/critical 的 observe_action **全是 warn** ⇒ ★ ★ **本插件在生产上
//	永远给 allow** ⇒ ★ **它不可能是权威判定方**。
//
// ★ ⇒ ★ **定稿**：★ 词表 `prompt_injection` 作硬阻断权威、★ **本插件保持
//
//	observe 观测/佐证**（★ 已等于 observe 语义、★ **零配置改动**）。
//
// ★ ★ 两条不可省的前提（★ 改任一条、结论就要重查）：
//
//	① 本插件**依赖 DB 初始化**：★ `sqlDB == nil` 时**静默跳过**
//	   （★ `Inspect` 直接返回 nil）⇒ ★ **DB 抖动时它根本不在链子里说话**。
//	② 「形态 b」（★ 有人判该拦、★ 链子却不拦）★ **当前不可达**：★ 需
//	   medium 的动作被配成 block/reject、★ 或 enforce + BlockThreshold > 2
//	   ⇒ ★ 届时本插件就会 deny ⇒ ★ **它就具备阻断能力了** ⇒ ★ 结论要重查。
//	   （★ 可达性有可复跑诊断：★ scripts/governance-intercept-composition.sh
//	     的「★ PI 分歧形态 b 可达性」一节。）
//
// ★ ★ 决策来源标注：★ 本段定稿采用的是**自动选择的推荐项**（★ 用户 60 秒内
//
//	未回复）⇒ ★ **不是用户明确确认** ⇒ ★ 若 owner 要改（★ 例如让本插件具备
//	阻断能力、★ 或把它降级为纯日志），★ 以 owner 的新指示为准。
package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
	promptinjection "github.com/kaixuan/llm-gateway-go/domains/promptinjection" //nolint:depguard // 复用核心检测器
)

const PluginNameEnhancedPI = "prompt_injection_enhanced"

// PromptInjectionEnhancedPlugin 增强版提示词注入检测插件（薄适配层）。
//
// 它包装 promptinjection.Detector，把检测结果映射为 governance.Verdict。
// 检测、评分、规则加载、策略读取、Canary/LLM/向量等全部复用核心 Detector，
// 避免与核心检测器重复造轮子。
type PromptInjectionEnhancedPlugin struct {
	mu          sync.RWMutex
	detector    *promptinjection.Detector
	initialized bool
}

// NewPromptInjectionEnhancedPlugin 创建插件（延迟初始化，Init 之后才生效）。
func NewPromptInjectionEnhancedPlugin() *PromptInjectionEnhancedPlugin {
	return &PromptInjectionEnhancedPlugin{}
}

// Init 注入依赖并初始化。
// 由 pipeline builder（SetV2DispatchAnalysisResources）在 DB 可用时调用，
// 不直接对接 main 业务逻辑，遵循 hook/plugin 链式接入架构。
//
// 实现细节：接收 *sql.DB，由调用方通过 db.Stdlib() 从 pgxpool 桥接而来，
// 与核心 Detector 共享 database/sql 接口。
func (p *PromptInjectionEnhancedPlugin) Init(sqlDB *sql.DB) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.initialized {
		return
	}
	if sqlDB == nil {
		slog.Warn("prompt_injection_enhanced: sqlDB is nil, plugin stays inactive")
		return
	}

	// 复用核心 Detector：避免重复造轮子。
	// 核心 Detector 已实现规则/启发式/Canary/向量/LLM 6 层检测 + 评分 + 决策。
	detector, err := promptinjection.NewDetector(sqlDB)
	if err != nil {
		slog.Error("prompt_injection_enhanced: failed to create core detector", "error", err)
		return
	}
	p.detector = detector
	p.initialized = true
	slog.Info("prompt_injection_enhanced: plugin initialized (wraps core Detector)")
}

// NewPromptInjectionEnhancedPluginFromDB 直接从 *sql.DB 构造（测试与显式装配用）。
func NewPromptInjectionEnhancedPluginFromDB(sqlDB *sql.DB) (*PromptInjectionEnhancedPlugin, error) {
	detector, err := promptinjection.NewDetector(sqlDB)
	if err != nil {
		return nil, fmt.Errorf("failed to create core detector: %w", err)
	}
	return &PromptInjectionEnhancedPlugin{detector: detector, initialized: true}, nil
}

// Name 实现 security.Plugin。
func (p *PromptInjectionEnhancedPlugin) Name() string { return PluginNameEnhancedPI }

// Direction 实现 security.Plugin（仅检测输入）。
func (p *PromptInjectionEnhancedPlugin) Direction() string { return "input" }

// Inspect 实现 security.Plugin。
// 从 metadata 读取 user_content，委托核心 Detector 检测，
// 再把结果转换为 governance.Verdict。
func (p *PromptInjectionEnhancedPlugin) Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error) {
	p.mu.RLock()
	detector := p.detector
	p.mu.RUnlock()

	// 未初始化（DB 不可用）时静默跳过，不影响主流程。
	if detector == nil {
		return nil, nil
	}

	content, _ := env.Metadata["user_content"].(string)
	if content == "" {
		return nil, nil
	}

	// 委托核心 Detector：复用规则/启发式/Canary/向量/LLM 全部检测能力。
	result, err := detector.Detect(ctx, env.TenantID, content)
	if err != nil {
		slog.Warn("prompt_injection_enhanced: detect failed",
			"tenant_id", env.TenantID, "error", err)
		return nil, nil
	}
	if result == nil || result.ActionTaken == "pass" || result.ActionTaken == "whitelisted" {
		return nil, nil
	}

	// 把 DetectionResult 映射为 governance.Verdict。
	verdict := &governance.Verdict{
		PluginName: PluginNameEnhancedPI,
		Allow:      !result.Blocked,
		Code:       "prompt_injection." + result.RiskLevel,
		Reason: fmt.Sprintf("risk_level=%s action=%s score=%d categories=%v",
			result.RiskLevel, result.ActionTaken, result.Score, result.Categories),
		Evidence: map[string]any{
			"score":          result.Score,
			"risk_level":     result.RiskLevel,
			"categories":     result.Categories,
			"action_taken":   result.ActionTaken,
			"matched_rules":  result.MatchedRules,
			"canary_leaked":  result.CanaryTokenLeaked,
			"llm_confidence": result.LLMConfidence,
		},
	}

	// severity 映射（governance 约定 0=info 1=warn 2=block 3=critical）。
	switch result.RiskLevel {
	case "medium":
		verdict.Severity = 1
	case "high":
		verdict.Severity = 2
	case "critical":
		verdict.Severity = 3
	default:
		verdict.Severity = 0
	}

	// FixAction 映射，供 dispatch gate / interception engine 使用。
	switch result.ActionTaken {
	case "replace", "redact", "remove":
		verdict.FixAction = "sanitize_input"
	case "reject", "block":
		verdict.FixAction = "abort_request"
	case "approve":
		verdict.FixAction = "require_approval"
	case "terminate":
		verdict.FixAction = "terminate_session"
	}

	// 写回 metadata，供后续 hook / dispatch gate 使用。
	env.Metadata["pi_enhanced_result"] = map[string]any{
		"score":            result.Score,
		"risk_level":       result.RiskLevel,
		"categories":       result.Categories,
		"action":           result.ActionTaken,
		"blocked":          result.Blocked,
		"require_approval": result.RequireApproval,
		"replaced_content": result.ReplacedContent,
		"canary_leaked":    result.CanaryTokenLeaked,
	}

	return verdict, nil
}
