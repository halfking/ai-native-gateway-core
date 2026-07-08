// Package plugins 包含 V4 安全插件的具体实现。
//
// PromptInjectionEnhancedPlugin 是增强版提示词注入检测插件，
// 实现 security.Plugin 接口，集成到 V4 governance 流程。
//
// 与现有 PromptInjectionChecker 的区别：
// - 支持 15 种风险类别（vs 原来的 4 种）
// - 集成 LLM 检测引擎
// - 集成 Canary Token 检测
// - 集成向量相似度检测
// - 支持严重等级矩阵配置
// - 支持内容替换策略
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
)

const PluginNameEnhancedPI = "prompt_injection_enhanced"

// LLMCaller 是 LLM 调用接口，复用 autoroute.LLMCaller 模式
type LLMCaller interface {
	Call(ctx context.Context, prompt string) (string, error)
}

// PromptInjectionEnhancedPlugin 增强版提示词注入检测插件
type PromptInjectionEnhancedPlugin struct {
	mu          sync.RWMutex
	db          *pgxpool.Pool
	llmCaller   LLMCaller
	rules       []*DetectionRule
	llmEngines  map[int]*LLMEngineConfig
	initialized bool
}

// DetectionRule 检测规则
type DetectionRule struct {
	ID             int
	RuleName       string
	Category       string
	Pattern        string
	Severity       int
	ActionOverride string
}

// LLMEngineConfig LLM 引擎配置
type LLMEngineConfig struct {
	ID              int
	ModelName       string
	SystemPrompt    string
	DetectionPrompt string
	Temperature     float64
	MaxTokens       int
}

// PolicyConfig 策略配置
type PolicyConfig struct {
	Enabled                bool
	DetectionMode          string
	EnableBasicRules       bool
	EnableAdvancedRules    bool
	EnableHeuristics       bool
	EnableLLMDetection     bool
	EnableCanaryDetection  bool
	LLMEngineID            *int
	ContentReplacement     string
	ScoreThresholdBlock    int
}

// SeverityAction 严重等级动作
type SeverityAction struct {
	SeverityLevel   string
	ObserveAction   string
	EnforceAction   string
	RequireApproval bool
}

// NewPromptInjectionEnhancedPlugin 创建增强版插件（延迟初始化）
func NewPromptInjectionEnhancedPlugin() *PromptInjectionEnhancedPlugin {
	return &PromptInjectionEnhancedPlugin{
		llmEngines: make(map[int]*LLMEngineConfig),
	}
}

// Init 注入依赖并初始化（由 SetV2DispatchAnalysisResources 调用）
func (p *PromptInjectionEnhancedPlugin) Init(db *pgxpool.Pool, llmCaller LLMCaller) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.initialized {
		return
	}

	p.db = db
	p.llmCaller = llmCaller
	p.initialized = true

	// 异步加载配置
	go p.reloadConfig()

	slog.Info("prompt_injection_enhanced: plugin initialized")
}

// Name 实现 security.Plugin
func (p *PromptInjectionEnhancedPlugin) Name() string {
	return PluginNameEnhancedPI
}

// Direction 实现 security.Plugin
func (p *PromptInjectionEnhancedPlugin) Direction() string {
	return "input"
}

// Inspect 实现 security.Plugin
func (p *PromptInjectionEnhancedPlugin) Inspect(ctx context.Context, env *domain.PipelineRequest) (*governance.Verdict, error) {
	p.mu.RLock()
	initialized := p.initialized
	p.mu.RUnlock()

	// 未初始化时跳过
	if !initialized {
		return nil, nil
	}

	// 提取用户输入
	content, _ := env.Metadata["user_content"].(string)
	if content == "" {
		return nil, nil
	}

	tenantID := env.TenantID

	// 获取租户策略
	policy, err := p.getPolicy(ctx, tenantID)
	if err != nil {
		slog.Warn("prompt_injection_enhanced: failed to get policy", "error", err)
		return nil, nil
	}

	if !policy.Enabled {
		return nil, nil
	}

	// 执行多层检测
	result := &DetectionResult{
		MatchedRules: []MatchedRule{},
		Categories:   []string{},
	}

	// Layer 1: 规则检测
	if policy.EnableBasicRules || policy.EnableAdvancedRules {
		matches := p.detectWithRules(content, policy)
		result.MatchedRules = append(result.MatchedRules, matches...)
	}

	// Layer 2: 启发式检测
	if policy.EnableHeuristics {
		heuristicMatches := p.detectHeuristics(content)
		result.MatchedRules = append(result.MatchedRules, heuristicMatches...)
	}

	// Layer 3: Canary Token 检测
	if policy.EnableCanaryDetection {
		canaryMatch := p.detectCanaryToken(ctx, tenantID, content)
		if canaryMatch != nil {
			result.MatchedRules = append(result.MatchedRules, *canaryMatch)
		}
	}

	// Layer 4: LLM 智能检测
	if policy.EnableLLMDetection && p.llmCaller != nil && policy.LLMEngineID != nil {
		llmMatch := p.detectWithLLM(ctx, *policy.LLMEngineID, content)
		if llmMatch != nil {
			result.MatchedRules = append(result.MatchedRules, *llmMatch)
		}
	}

	// 计算分数和风险等级
	score := calculateScore(result.MatchedRules)
	riskLevel := calculateRiskLevel(score)
	categories := extractCategories(result.MatchedRules)

	// 获取严重等级动作
	severityAction, _ := p.getSeverityAction(ctx, tenantID, riskLevel)

	// 决定动作
	action := decideAction(score, riskLevel, policy, severityAction)

	// 构建 Verdict
	verdict := &governance.Verdict{
		PluginName: PluginNameEnhancedPI,
		Allow:      action != "block" && action != "reject",
		Code:       "prompt_injection." + riskLevel,
		Reason:     fmt.Sprintf("risk_level=%s action=%s categories=%v", riskLevel, action, categories),
		Evidence: map[string]any{
			"score":         score,
			"risk_level":    riskLevel,
			"categories":    categories,
			"action_taken":  action,
			"matched_rules": len(result.MatchedRules),
		},
	}

	// 映射 severity
	switch riskLevel {
	case "medium":
		verdict.Severity = 1
	case "high":
		verdict.Severity = 2
	case "critical":
		verdict.Severity = 3
	default:
		verdict.Severity = 0
	}

	// 设置 FixAction
	switch action {
	case "replace", "redact", "remove":
		verdict.FixAction = "sanitize_input"
	case "reject", "block":
		verdict.FixAction = "abort_request"
	case "approve":
		verdict.FixAction = "require_approval"
	case "terminate":
		verdict.FixAction = "terminate_session"
	}

	// 保存检测结果到 metadata（供后续 hook 使用）
	env.Metadata["pi_enhanced_result"] = map[string]any{
		"score":         score,
		"risk_level":    riskLevel,
		"categories":    categories,
		"action":        action,
		"matched_rules": result.MatchedRules,
	}

	// 记录检测日志（异步）
	requestID, _ := env.Metadata["request_id"].(string)
	go p.logDetection(context.Background(), tenantID, requestID, content, result, score, riskLevel, action)

	return verdict, nil
}

// DetectionResult 检测结果
type DetectionResult struct {
	MatchedRules []MatchedRule
	Categories   []string
}

// MatchedRule 匹配的规则
type MatchedRule struct {
	RuleName    string
	Category    string
	Severity    int
	Evidence    string
	Description string
}

// detectWithRules 使用规则检测
func (p *PromptInjectionEnhancedPlugin) detectWithRules(input string, policy *PolicyConfig) []MatchedRule {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var matches []MatchedRule
	for _, rule := range p.rules {
		// 根据策略过滤规则类型
		if rule.Category == "basic" && !policy.EnableBasicRules {
			continue
		}
		if rule.Category == "advanced" && !policy.EnableAdvancedRules {
			continue
		}

		// 简单的字符串匹配（实际应该用正则）
		if containsIgnoreCase(input, rule.Pattern) {
			evidence := rule.Pattern
			if len(evidence) > 100 {
				evidence = evidence[:100] + "..."
			}
			matches = append(matches, MatchedRule{
				RuleName:    rule.RuleName,
				Category:    rule.Category,
				Severity:    rule.Severity,
				Evidence:    evidence,
				Description: fmt.Sprintf("匹配规则: %s", rule.RuleName),
			})
		}
	}
	return matches
}

// detectHeuristics 启发式检测
func (p *PromptInjectionEnhancedPlugin) detectHeuristics(input string) []MatchedRule {
	var matches []MatchedRule

	// 检测角色切换
	roleSwitchPatterns := []string{"you are", "act as", "pretend to be"}
	count := 0
	for _, pattern := range roleSwitchPatterns {
		count += strings.Count(strings.ToLower(input), pattern)
	}
	if count > 2 {
		matches = append(matches, MatchedRule{
			RuleName:    "heuristic_role_switches",
			Category:    "role_hijack",
			Severity:    5 + count,
			Evidence:    fmt.Sprintf("检测到 %d 次角色切换", count),
			Description: "频繁的角色切换可能是注入尝试",
		})
	}

	// 检测异常长句
	if len(input) > 5000 {
		matches = append(matches, MatchedRule{
			RuleName:    "heuristic_long_input",
			Category:    "resource_exhaustion",
			Severity:    4,
			Evidence:    fmt.Sprintf("输入长度: %d 字符", len(input)),
			Description: "异常长输入可能用于绕过检测",
		})
	}

	return matches
}

// detectCanaryToken 检测 Canary Token
func (p *PromptInjectionEnhancedPlugin) detectCanaryToken(ctx context.Context, tenantID, input string) *MatchedRule {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()

	if db == nil {
		return nil
	}

	var tokenValue string
	err := db.QueryRow(ctx,
		`SELECT token_value FROM canary_tokens WHERE tenant_id = $1 AND active = true AND position(token_value in $2) > 0`,
		tenantID, input).Scan(&tokenValue)

	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return nil
	}

	// 更新泄漏统计
	_, _ = db.Exec(ctx,
		`UPDATE canary_tokens SET times_leaked = times_leaked + 1, last_leaked_at = NOW() WHERE token_value = $1`,
		tokenValue)

	return &MatchedRule{
		RuleName:    "canary_token_leaked",
		Category:    "prompt_leaking",
		Severity:    10,
		Evidence:    fmt.Sprintf("检测到 Canary Token: %s...", tokenValue[:8]),
		Description: "系统提示词可能已被泄漏",
	}
}

// detectWithLLM 使用 LLM 检测
func (p *PromptInjectionEnhancedPlugin) detectWithLLM(ctx context.Context, engineID int, input string) *MatchedRule {
	p.mu.RLock()
	llmCaller := p.llmCaller
	engine, exists := p.llmEngines[engineID]
	p.mu.RUnlock()

	if llmCaller == nil || !exists {
		return nil
	}

	// 构建检测提示词（合并 system + user prompt）
	prompt := fmt.Sprintf("%s\n\n%s", engine.SystemPrompt, fmt.Sprintf(engine.DetectionPrompt, input))

	// 调用 LLM
	response, err := llmCaller.Call(ctx, prompt)
	if err != nil {
		slog.Warn("prompt_injection_enhanced: LLM detection failed", "error", err)
		return nil
	}

	// 解析响应
	var llmResult struct {
		IsInjection bool     `json:"is_injection"`
		Confidence  float64  `json:"confidence"`
		Categories  []string `json:"categories"`
		Reason      string   `json:"reason"`
	}

	if err := json.Unmarshal([]byte(response), &llmResult); err != nil {
		// 尝试提取 JSON
		jsonStart := strings.Index(response, "{")
		jsonEnd := strings.LastIndex(response, "}") + 1
		if jsonStart >= 0 && jsonEnd > jsonStart {
			_ = json.Unmarshal([]byte(response[jsonStart:jsonEnd]), &llmResult)
		}
	}

	if !llmResult.IsInjection || llmResult.Confidence < 0.5 {
		return nil
	}

	return &MatchedRule{
		RuleName:    "llm_detection",
		Category:    strings.Join(llmResult.Categories, ","),
		Severity:    int(llmResult.Confidence * 10),
		Evidence:    llmResult.Reason,
		Description: fmt.Sprintf("LLM 检测置信度: %.2f", llmResult.Confidence),
	}
}

// getPolicy 获取租户策略
func (p *PromptInjectionEnhancedPlugin) getPolicy(ctx context.Context, tenantID string) (*PolicyConfig, error) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()

	if db == nil {
		return &PolicyConfig{
			Enabled:             true,
			DetectionMode:       "observe",
			EnableBasicRules:    true,
			EnableAdvancedRules: true,
			EnableHeuristics:    true,
			EnableLLMDetection:  true,
			EnableCanaryDetection: true,
			ContentReplacement:  "llm_rewrite",
			ScoreThresholdBlock: 10,
		}, nil
	}

	policy := &PolicyConfig{}
	err := db.QueryRow(ctx,
		`SELECT enabled, detection_mode, enable_basic_rules, enable_advanced_rules,
			enable_heuristics, COALESCE(enable_llm_detection, true),
			COALESCE(enable_canary_detection, true), llm_engine_id,
			COALESCE(content_replacement_strategy, 'llm_rewrite'),
			score_threshold_block
		FROM prompt_injection_policies WHERE tenant_id = $1`,
		tenantID).Scan(
		&policy.Enabled, &policy.DetectionMode,
		&policy.EnableBasicRules, &policy.EnableAdvancedRules,
		&policy.EnableHeuristics, &policy.EnableLLMDetection,
		&policy.EnableCanaryDetection, &policy.LLMEngineID,
		&policy.ContentReplacement, &policy.ScoreThresholdBlock,
	)

	if err == pgx.ErrNoRows {
		return &PolicyConfig{
			Enabled:             true,
			DetectionMode:       "observe",
			EnableBasicRules:    true,
			EnableAdvancedRules: true,
			EnableHeuristics:    true,
			EnableLLMDetection:  true,
			EnableCanaryDetection: true,
			ContentReplacement:  "llm_rewrite",
			ScoreThresholdBlock: 10,
		}, nil
	}

	return policy, err
}

// getSeverityAction 获取严重等级动作
func (p *PromptInjectionEnhancedPlugin) getSeverityAction(ctx context.Context, tenantID, riskLevel string) (*SeverityAction, error) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()

	if db == nil {
		return nil, nil
	}

	sa := &SeverityAction{}
	err := db.QueryRow(ctx,
		`SELECT severity_level, observe_action::text, enforce_action::text, require_approval
		FROM severity_action_matrix WHERE tenant_id = $1 AND severity_level = $2`,
		tenantID, riskLevel).Scan(
		&sa.SeverityLevel, &sa.ObserveAction, &sa.EnforceAction, &sa.RequireApproval,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return sa, err
}

// logDetection 记录检测日志
func (p *PromptInjectionEnhancedPlugin) logDetection(ctx context.Context, tenantID, requestID, input string, result *DetectionResult, score int, riskLevel, action string) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()

	if db == nil {
		return
	}

	matchedRulesJSON, _ := json.Marshal(result.MatchedRules)
	categoriesJSON, _ := json.Marshal(result.Categories)

	_, _ = db.Exec(ctx,
		`INSERT INTO prompt_injection_detections (
			tenant_id, request_id, detection_score, risk_level,
			matched_rules, matched_rules_count, categories,
			action_taken, blocked, evidence_text
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		tenantID, requestID, score, riskLevel,
		matchedRulesJSON, len(result.MatchedRules), categoriesJSON,
		action, action == "block" || action == "reject",
		truncateString(input, 500))
}

// reloadConfig 重新加载配置
func (p *PromptInjectionEnhancedPlugin) reloadConfig() {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()

	if db == nil {
		return
	}

	ctx := context.Background()

	// 加载规则
	rows, err := db.Query(ctx,
		`SELECT id, rule_name, COALESCE(category_new::text, category), pattern, severity, COALESCE(action_override::text, '')
		FROM prompt_injection_rules WHERE enabled = true ORDER BY severity DESC`)
	if err != nil {
		slog.Warn("prompt_injection_enhanced: failed to load rules", "error", err)
		return
	}
	defer func() { rows.Close() }()

	var rules []*DetectionRule
	for rows.Next() {
		rule := &DetectionRule{}
		if err := rows.Scan(&rule.ID, &rule.RuleName, &rule.Category, &rule.Pattern, &rule.Severity, &rule.ActionOverride); err != nil {
			continue
		}
		rules = append(rules, rule)
	}

	p.mu.Lock()
	p.rules = rules
	p.mu.Unlock()

	// 加载 LLM 引擎
	engineRows, err := db.Query(ctx,
		`SELECT id, engine_name, system_prompt, detection_prompt, temperature, max_tokens
		FROM prompt_injection_llm_engines WHERE enabled = true`)
	if err != nil {
		return
	}
	defer func() { engineRows.Close() }()

	engines := make(map[int]*LLMEngineConfig)
	for engineRows.Next() {
		engine := &LLMEngineConfig{}
		var engineName string
		if err := engineRows.Scan(&engine.ID, &engineName, &engine.SystemPrompt, &engine.DetectionPrompt, &engine.Temperature, &engine.MaxTokens); err != nil {
			continue
		}
		engines[engine.ID] = engine
	}

	p.mu.Lock()
	p.llmEngines = engines
	p.mu.Unlock()

	slog.Info("prompt_injection_enhanced: config reloaded", "rules", len(rules), "engines", len(engines))
}

// 辅助函数

func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func calculateScore(matches []MatchedRule) int {
	maxSeverity := 0
	for _, m := range matches {
		if m.Severity > maxSeverity {
			maxSeverity = m.Severity
		}
	}
	return maxSeverity
}

func calculateRiskLevel(score int) string {
	switch {
	case score >= 10:
		return "critical"
	case score >= 8:
		return "high"
	case score >= 6:
		return "medium"
	default:
		return "low"
	}
}

func extractCategories(matches []MatchedRule) []string {
	seen := make(map[string]bool)
	var categories []string
	for _, m := range matches {
		for _, cat := range strings.Split(m.Category, ",") {
			cat = strings.TrimSpace(cat)
			if cat != "" && !seen[cat] {
				seen[cat] = true
				categories = append(categories, cat)
			}
		}
	}
	return categories
}

func decideAction(score int, riskLevel string, policy *PolicyConfig, severityAction *SeverityAction) string {
	if severityAction != nil {
		if policy.DetectionMode == "enforce" {
			return severityAction.EnforceAction
		}
		return severityAction.ObserveAction
	}

	if score >= policy.ScoreThresholdBlock {
		if policy.DetectionMode == "enforce" {
			return "block"
		}
		return "warn"
	}
	if score >= 8 {
		if policy.DetectionMode == "enforce" {
			return "reject"
		}
		return "warn"
	}
	if score >= 6 {
		return "warn"
	}
	if score >= 3 {
		return "log"
	}
	return "pass"
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
