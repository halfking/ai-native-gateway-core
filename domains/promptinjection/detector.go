// Package promptinjection 实现提示词注入检测
// 参考 Langfuse 安全架构、LLM Guard、Rebuff，支持多层检测和租户级策略配置
package promptinjection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Detector 提示词注入检测器
type Detector struct {
	db              *sql.DB
	basicRules      []*Rule
	advancedRules   []*Rule
	heuristicEngine *HeuristicEngine
	llmEngine       *LLMDetectionEngine
	canaryDetector  *CanaryDetector
	vectorMatcher   *VectorMatcher
}

// Rule 检测规则
type Rule struct {
	ID             int
	RuleName       string
	RuleType       string
	Category       string
	CategoryNew    string
	Pattern        string
	Description    string
	Severity       int
	Enabled        bool
	CaseSensitive  bool
	ActionOverride string
	Tags           []string
	regex          *regexp.Regexp
}

// Policy 租户策略
type Policy struct {
	TenantID               string
	Enabled                bool
	DetectionMode          string
	EnableBasicRules       bool
	EnableAdvancedRules    bool
	EnableHeuristics       bool
	EnableMLModel          bool
	EnableLLMDetection     bool
	EnableCanaryDetection  bool
	EnableVectorSimilarity bool
	LLMEngineID            *int
	ContentReplacement     string
	MaxInputLength         int
	AutoLearnEnabled       bool
	DetectionTimeoutMs     int
	ScoreThresholdLog      int
	ScoreThresholdWarn     int
	ScoreThresholdSanitize int
	ScoreThresholdBlock    int
	ActionOnLowRisk        string
	ActionOnMediumRisk     string
	ActionOnHighRisk       string
	WhitelistPatterns      []string
	WhitelistUsers         []string
}

// SeverityAction 严重等级动作配置
type SeverityAction struct {
	SeverityLevel          string
	ObserveAction          string
	EnforceAction          string
	RequireApproval        bool
	ApprovalTimeoutMinutes int
	NotifyOnDetect         bool
	NotifyChannels         []string
	AffectSessionHealth    bool
	SessionHealthPenalty   int
	TerminateOnRepeat      bool
	RepeatThreshold        int
}

// DetectionResult 检测结果
type DetectionResult struct {
	Score              int                    `json:"score"`
	RiskLevel          string                 `json:"risk_level"`
	Categories         []string               `json:"categories"`
	MatchedRules       []MatchedRule          `json:"matched_rules"`
	DetectionLayers    map[string]bool        `json:"detection_layers"`
	ActionTaken        string                 `json:"action_taken"`
	Blocked            bool                   `json:"blocked"`
	RequireApproval    bool                   `json:"require_approval"`
	ApprovalTimeoutMin int                    `json:"approval_timeout_minutes"`
	Evidence           string                 `json:"evidence"`
	Recommendation     string                 `json:"recommendation"`
	ReplacedContent    string                 `json:"replaced_content,omitempty"`
	CanaryTokenLeaked  string                 `json:"canary_token_leaked,omitempty"`
	SimilarAttackID    int64                  `json:"similar_attack_id,omitempty"`
	LLMConfidence      float64                `json:"llm_confidence,omitempty"`
	LLMReason          string                 `json:"llm_reason,omitempty"`
	SessionHealthDelta int                    `json:"session_health_delta,omitempty"`
	TerminateSession   bool                   `json:"terminate_session,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

// MatchedRule 匹配的规则
type MatchedRule struct {
	RuleName    string `json:"rule_name"`
	Category    string `json:"category"`
	Severity    int    `json:"severity"`
	Evidence    string `json:"evidence"`
	Description string `json:"description"`
}

// NewDetector 创建检测器
func NewDetector(db *sql.DB) (*Detector, error) {
	d := &Detector{
		db:              db,
		heuristicEngine: NewHeuristicEngine(),
		llmEngine:       NewLLMDetectionEngine(db),
		canaryDetector:  NewCanaryDetector(db),
		vectorMatcher:   NewVectorMatcher(db),
	}

	if err := d.loadRules(); err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	return d, nil
}

// Detect 执行检测
func (d *Detector) Detect(ctx context.Context, tenantID, input string) (*DetectionResult, error) {
	policy, err := d.getPolicy(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get policy: %w", err)
	}

	if !policy.Enabled {
		return &DetectionResult{
			Score:       0,
			RiskLevel:   "low",
			ActionTaken: "pass",
			Blocked:     false,
		}, nil
	}

	// 检查输入长度
	if policy.MaxInputLength > 0 && len(input) > policy.MaxInputLength {
		input = input[:policy.MaxInputLength]
	}

	if d.isWhitelisted(input, policy) {
		return &DetectionResult{
			Score:       0,
			RiskLevel:   "low",
			ActionTaken: "whitelisted",
			Blocked:     false,
		}, nil
	}

	result := &DetectionResult{
		MatchedRules:    []MatchedRule{},
		DetectionLayers: make(map[string]bool),
		Categories:      []string{},
		Metadata:        make(map[string]interface{}),
	}

	// Layer 1: 基础规则检测
	if policy.EnableBasicRules {
		basicMatches := d.detectWithRules(input, d.basicRules)
		result.MatchedRules = append(result.MatchedRules, basicMatches...)
		result.DetectionLayers["basic"] = true
	}

	// Layer 2: 高级规则检测
	if policy.EnableAdvancedRules {
		advancedMatches := d.detectWithRules(input, d.advancedRules)
		result.MatchedRules = append(result.MatchedRules, advancedMatches...)
		result.DetectionLayers["advanced"] = true
	}

	// Layer 3: 启发式检测
	if policy.EnableHeuristics {
		heuristicMatches := d.heuristicEngine.Detect(input)
		result.MatchedRules = append(result.MatchedRules, heuristicMatches...)
		result.DetectionLayers["heuristics"] = true
	}

	// Layer 4: Canary Token 检测
	if policy.EnableCanaryDetection {
		canaryResult, err := d.canaryDetector.Detect(ctx, tenantID, input)
		if err != nil {
			slog.Warn("prompt_injection: canary detection failed", "error", err)
		} else if canaryResult != nil {
			result.MatchedRules = append(result.MatchedRules, canaryResult.MatchedRules...)
			result.CanaryTokenLeaked = canaryResult.TokenValue
			result.DetectionLayers["canary"] = true
		}
	}

	// Layer 5: 向量相似度检测
	if policy.EnableVectorSimilarity {
		vectorResult, err := d.vectorMatcher.FindSimilar(ctx, tenantID, input, 0.85)
		if err != nil {
			slog.Warn("prompt_injection: vector matching failed", "error", err)
		} else if vectorResult != nil {
			result.MatchedRules = append(result.MatchedRules, vectorResult.MatchedRules...)
			result.SimilarAttackID = vectorResult.AttackID
			result.DetectionLayers["vector"] = true
		}
	}

	// Layer 6: LLM 智能检测
	if policy.EnableLLMDetection && policy.LLMEngineID != nil {
		llmResult, err := d.llmEngine.Detect(ctx, *policy.LLMEngineID, input)
		if err != nil {
			slog.Warn("prompt_injection: LLM detection failed", "error", err)
		} else if llmResult != nil {
			result.LLMConfidence = llmResult.Confidence
			result.LLMReason = llmResult.Reason
			if llmResult.IsInjection {
				// LLM 检测结果作为额外规则
				result.MatchedRules = append(result.MatchedRules, MatchedRule{
					RuleName:    "llm_detection",
					Category:    strings.Join(llmResult.Categories, ","),
					Severity:    int(llmResult.Confidence * 10),
					Evidence:    llmResult.Evidence,
					Description: fmt.Sprintf("LLM 检测置信度: %.2f", llmResult.Confidence),
				})
			}
			result.DetectionLayers["llm"] = true
		}
	}

	// 计算综合评分和风险等级
	result.Score = d.calculateScore(result.MatchedRules)
	result.RiskLevel = d.calculateRiskLevel(result.Score)
	result.Categories = d.extractCategories(result.MatchedRules)

	// 获取严重等级动作配置
	severityAction, err := d.getSeverityAction(ctx, tenantID, result.RiskLevel)
	if err != nil {
		slog.Warn("prompt_injection: failed to get severity action", "error", err)
	}

	// 决定处理动作
	result.ActionTaken = d.decideAction(result.Score, result.RiskLevel, policy, severityAction)
	result.Blocked = (result.ActionTaken == "block" || result.ActionTaken == "reject")

	// 审批配置
	if severityAction != nil && severityAction.RequireApproval {
		result.RequireApproval = true
		result.ApprovalTimeoutMin = severityAction.ApprovalTimeoutMinutes
		if result.ActionTaken != "block" && result.ActionTaken != "reject" {
			result.ActionTaken = "approve"
		}
	}

	// 会话影响
	if severityAction != nil {
		result.SessionHealthDelta = severityAction.SessionHealthPenalty
		if severityAction.TerminateOnRepeat {
			// 检查是否达到重复阈值
			repeatCount, _ := d.getRepeatCount(ctx, tenantID, "")
			if repeatCount >= severityAction.RepeatThreshold {
				result.TerminateSession = true
				result.ActionTaken = "terminate"
			}
		}
	}

	// 内容替换
	if result.ActionTaken == "replace" || result.ActionTaken == "redact" || result.ActionTaken == "remove" {
		replaced, err := d.replaceContent(ctx, policy.ContentReplacement, input, result.MatchedRules)
		if err != nil {
			slog.Warn("prompt_injection: content replacement failed", "error", err)
		} else {
			result.ReplacedContent = replaced
		}
	}

	result.Recommendation = d.generateRecommendation(result)
	result.Evidence = d.extractEvidence(input, result.MatchedRules)

	// 自动学习：将攻击模式加入向量库
	if policy.AutoLearnEnabled && result.Score >= policy.ScoreThresholdWarn {
		go d.learnAttackPattern(context.Background(), tenantID, input, result)
	}

	return result, nil
}

// DetectAndLog 检测并记录到数据库
func (d *Detector) DetectAndLog(ctx context.Context, tenantID, requestID, sessionKey, input, clientIP, userAgent string) (*DetectionResult, error) {
	result, err := d.Detect(ctx, tenantID, input)
	if err != nil {
		return nil, err
	}

	if result.Score > 0 {
		matchedRulesJSON, _ := json.Marshal(result.MatchedRules)
		detectionLayersJSON, _ := json.Marshal(result.DetectionLayers)
		categoriesJSON, _ := json.Marshal(result.Categories)
		inputHash := fmt.Sprintf("%x", result.Score)

		query := `
			INSERT INTO prompt_injection_detections (
				tenant_id, request_id, session_key, detection_score, risk_level,
				matched_rules, matched_rules_count, detection_layers,
				action_taken, blocked, evidence_text, input_hash,
				client_ip, user_agent, categories,
				llm_confidence, llm_reason, canary_token_leaked,
				approval_id, replaced_content
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		`

		_, err := d.db.ExecContext(ctx, query,
			tenantID, requestID, sessionKey, result.Score, result.RiskLevel,
			matchedRulesJSON, len(result.MatchedRules), detectionLayersJSON,
			result.ActionTaken, result.Blocked, result.Evidence, inputHash,
			clientIP, userAgent, categoriesJSON,
			result.LLMConfidence, result.LLMReason, result.CanaryTokenLeaked,
			nil, result.ReplacedContent,
		)

		if err != nil {
			slog.Warn("prompt_injection: failed to log detection", "error", err)
		}

		// 更新策略统计
		d.updatePolicyStats(ctx, tenantID, result.Blocked)

		// 创建审批请求
		if result.RequireApproval && result.ActionTaken == "approve" {
			go d.createApprovalRequest(context.Background(), tenantID, requestID, sessionKey, input, result, clientIP, userAgent)
		}
	}

	return result, nil
}

// loadRules 从数据库加载规则
func (d *Detector) loadRules() error {
	query := `
		SELECT id, rule_name, rule_type, category, COALESCE(category_new::text, ''),
			pattern, description, severity, enabled, case_sensitive,
			COALESCE(action_override::text, ''), COALESCE(tags, '{}')
		FROM prompt_injection_rules
		WHERE enabled = true
		ORDER BY severity DESC
	`

	rows, err := d.db.Query(query)
	if err != nil {
		if isTableNotFound(err, "prompt_injection_rules") {
			if createErr := d.ensureRulesTable(); createErr != nil {
				slog.Warn("prompt_injection: cannot auto-create rules table", "error", createErr)
				return nil
			}
			return d.loadRules()
		}
		return err
	}
	defer func() { _ = rows.Close() }()

	d.basicRules = nil
	d.advancedRules = nil

	for rows.Next() {
		rule := &Rule{}
		var actionOverride sql.NullString
		var tags []string
		if err := rows.Scan(&rule.ID, &rule.RuleName, &rule.RuleType, &rule.Category,
			&rule.CategoryNew, &rule.Pattern, &rule.Description, &rule.Severity,
			&rule.Enabled, &rule.CaseSensitive, &actionOverride, pq.Array(&tags)); err != nil {
			return err
		}

		if actionOverride.Valid {
			rule.ActionOverride = actionOverride.String
		}
		rule.Tags = tags

		flags := ""
		if !rule.CaseSensitive {
			flags = "(?i)"
		}
		rule.regex, err = regexp.Compile(flags + rule.Pattern)
		if err != nil {
			slog.Warn("prompt_injection: invalid regex for rule", "rule", rule.RuleName, "error", err)
			continue
		}

		if rule.RuleType == "basic" {
			d.basicRules = append(d.basicRules, rule)
		} else {
			d.advancedRules = append(d.advancedRules, rule)
		}
	}

	return rows.Err()
}

// ensureRulesTable 自动创建表
func (d *Detector) ensureRulesTable() error {
	ddl := `
	CREATE TABLE IF NOT EXISTS prompt_injection_rules (
		id SERIAL PRIMARY KEY,
		rule_name VARCHAR(100) NOT NULL UNIQUE,
		rule_type VARCHAR(50) NOT NULL,
		category VARCHAR(50) NOT NULL,
		pattern TEXT NOT NULL,
		description TEXT,
		severity INT NOT NULL CHECK (severity >= 1 AND severity <= 10),
		enabled BOOLEAN DEFAULT true,
		case_sensitive BOOLEAN DEFAULT false,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);
	INSERT INTO prompt_injection_rules (rule_name, rule_type, category, pattern, description, severity) VALUES
	('role_hijack_ignore_previous', 'basic', 'role_hijack', '(?i)(ignore|forget|disregard).*(previous|above|prior).*(instruction|prompt|rule)', '尝试让模型忽略之前的指令', 9),
	('role_hijack_you_are_now', 'basic', 'role_hijack', '(?i)you are now (a|an) .*(admin|root|system|god mode|developer)', '尝试切换模型角色为特权用户', 10)
	ON CONFLICT (rule_name) DO NOTHING;`
	if _, err := d.db.Exec(ddl); err != nil {
		return err
	}
	slog.Info("prompt_injection_rules table auto-created")
	return nil
}

// isTableNotFound checks if error is a "relation does not exist" error
func isTableNotFound(err error, table string) bool {
	return strings.Contains(err.Error(), "relation \""+table+"\" does not exist")
}

// detectWithRules 使用规则集检测
func (d *Detector) detectWithRules(input string, rules []*Rule) []MatchedRule {
	matches := []MatchedRule{}

	for _, rule := range rules {
		if rule.regex.MatchString(input) {
			evidence := rule.regex.FindString(input)
			if len(evidence) > 100 {
				evidence = evidence[:100] + "..."
			}

			category := rule.Category
			if rule.CategoryNew != "" {
				category = rule.CategoryNew
			}

			matches = append(matches, MatchedRule{
				RuleName:    rule.RuleName,
				Category:    category,
				Severity:    rule.Severity,
				Evidence:    evidence,
				Description: rule.Description,
			})
		}
	}

	return matches
}

// getPolicy 获取租户策略
func (d *Detector) getPolicy(ctx context.Context, tenantID string) (*Policy, error) {
	query := `SELECT tenant_id, enabled, detection_mode, enable_basic_rules, enable_advanced_rules,
		enable_heuristics, enable_ml_model, COALESCE(enable_llm_detection, true),
		COALESCE(enable_canary_detection, true), COALESCE(enable_vector_similarity, false),
		llm_engine_id, COALESCE(content_replacement_strategy, 'llm_rewrite'),
		COALESCE(max_input_length, 50000), COALESCE(auto_learn_enabled, false),
		COALESCE(detection_timeout_ms, 5000),
		score_threshold_log, score_threshold_warn,
		score_threshold_sanitize, score_threshold_block, action_on_low_risk,
		action_on_medium_risk, action_on_high_risk
		FROM prompt_injection_policies WHERE tenant_id = $1`

	policy := &Policy{}
	err := d.db.QueryRowContext(ctx, query, tenantID).Scan(
		&policy.TenantID, &policy.Enabled, &policy.DetectionMode,
		&policy.EnableBasicRules, &policy.EnableAdvancedRules,
		&policy.EnableHeuristics, &policy.EnableMLModel,
		&policy.EnableLLMDetection, &policy.EnableCanaryDetection,
		&policy.EnableVectorSimilarity, &policy.LLMEngineID,
		&policy.ContentReplacement, &policy.MaxInputLength,
		&policy.AutoLearnEnabled, &policy.DetectionTimeoutMs,
		&policy.ScoreThresholdLog, &policy.ScoreThresholdWarn,
		&policy.ScoreThresholdSanitize, &policy.ScoreThresholdBlock,
		&policy.ActionOnLowRisk, &policy.ActionOnMediumRisk, &policy.ActionOnHighRisk,
	)

	if err == sql.ErrNoRows {
		return &Policy{
			TenantID:               tenantID,
			Enabled:                true,
			DetectionMode:          "observe",
			EnableBasicRules:       true,
			EnableAdvancedRules:    true,
			EnableHeuristics:       true,
			EnableMLModel:          false,
			EnableLLMDetection:     true,
			EnableCanaryDetection:  true,
			EnableVectorSimilarity: false,
			ContentReplacement:     "llm_rewrite",
			MaxInputLength:         50000,
			DetectionTimeoutMs:     5000,
			ScoreThresholdLog:      3,
			ScoreThresholdWarn:     6,
			ScoreThresholdSanitize: 8,
			ScoreThresholdBlock:    10,
			ActionOnLowRisk:        "log",
			ActionOnMediumRisk:     "warn",
			ActionOnHighRisk:       "block",
		}, nil
	}

	return policy, err
}

// getSeverityAction 获取严重等级动作配置
func (d *Detector) getSeverityAction(ctx context.Context, tenantID, riskLevel string) (*SeverityAction, error) {
	query := `SELECT severity_level, observe_action::text, enforce_action::text,
		require_approval, approval_timeout_minutes,
		notify_on_detect, COALESCE(notify_channels, '[]'),
		affect_session_health, session_health_penalty,
		terminate_session_on_repeat, repeat_threshold
		FROM severity_action_matrix WHERE tenant_id = $1 AND severity_level = $2`

	sa := &SeverityAction{}
	var channelsJSON string
	err := d.db.QueryRowContext(ctx, query, tenantID, riskLevel).Scan(
		&sa.SeverityLevel, &sa.ObserveAction, &sa.EnforceAction,
		&sa.RequireApproval, &sa.ApprovalTimeoutMinutes,
		&sa.NotifyOnDetect, &channelsJSON,
		&sa.AffectSessionHealth, &sa.SessionHealthPenalty,
		&sa.TerminateOnRepeat, &sa.RepeatThreshold,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(channelsJSON), &sa.NotifyChannels)
	return sa, nil
}

// isWhitelisted 检查是否在白名单中
func (d *Detector) isWhitelisted(input string, policy *Policy) bool {
	for _, pattern := range policy.WhitelistPatterns {
		regex, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if regex.MatchString(input) {
			return true
		}
	}
	return false
}

// calculateScore 计算综合评分
func (d *Detector) calculateScore(matches []MatchedRule) int {
	maxSeverity := 0
	for _, match := range matches {
		if match.Severity > maxSeverity {
			maxSeverity = match.Severity
		}
	}
	return maxSeverity
}

// calculateRiskLevel 计算风险等级
func (d *Detector) calculateRiskLevel(score int) string {
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

// extractCategories 提取所有风险类别
func (d *Detector) extractCategories(matches []MatchedRule) []string {
	seen := make(map[string]bool)
	var categories []string
	for _, match := range matches {
		for _, cat := range strings.Split(match.Category, ",") {
			cat = strings.TrimSpace(cat)
			if cat != "" && !seen[cat] {
				seen[cat] = true
				categories = append(categories, cat)
			}
		}
	}
	return categories
}

// decideAction 决定响应动作
func (d *Detector) decideAction(score int, riskLevel string, policy *Policy, severityAction *SeverityAction) string {
	// 如果有严重等级动作配置，优先使用
	if severityAction != nil {
		if policy.DetectionMode == "enforce" {
			return severityAction.EnforceAction
		}
		return severityAction.ObserveAction
	}

	// 否则使用策略配置
	switch {
	case score >= policy.ScoreThresholdBlock:
		if policy.DetectionMode == "enforce" {
			return "block"
		}
		return "warn"
	case score >= policy.ScoreThresholdSanitize:
		if policy.DetectionMode == "enforce" {
			return "replace"
		}
		return "warn"
	case score >= policy.ScoreThresholdWarn:
		return "warn"
	case score >= policy.ScoreThresholdLog:
		return "log"
	default:
		return "pass"
	}
}

// replaceContent 内容替换
func (d *Detector) replaceContent(ctx context.Context, strategy string, input string, matches []MatchedRule) (string, error) {
	switch strategy {
	case "llm_rewrite":
		return d.llmRewriteContent(ctx, input, matches)
	case "pattern_redact":
		return d.patternRedactContent(input, matches)
	case "keyword_remove":
		return d.keywordRemoveContent(input, matches)
	default:
		return d.keywordRemoveContent(input, matches)
	}
}

// llmRewriteContent 使用 LLM 重写安全版本
func (d *Detector) llmRewriteContent(ctx context.Context, input string, matches []MatchedRule) (string, error) {
	// 构建重写提示词
	evidenceList := make([]string, len(matches))
	for i, m := range matches {
		evidenceList[i] = fmt.Sprintf("- %s: %s", m.Category, m.Evidence)
	}

	prompt := fmt.Sprintf(`请将以下用户输入中的恶意内容移除或重写为安全版本，同时保留用户的原始意图。

检测到的恶意内容：
%s

用户输入：
%s

请返回安全版本的输入内容，不要添加任何解释。`, strings.Join(evidenceList, "\n"), input)

	// TODO: 调用 LLM 引擎
	_ = prompt
	return input, nil
}

// patternRedactContent 正则脱敏
func (d *Detector) patternRedactContent(input string, matches []MatchedRule) (string, error) {
	result := input
	for _, match := range matches {
		if match.Evidence != "" {
			result = strings.ReplaceAll(result, match.Evidence, "[REDACTED]")
		}
	}
	return result, nil
}

// keywordRemoveContent 关键词移除
func (d *Detector) keywordRemoveContent(input string, matches []MatchedRule) (string, error) {
	result := input
	for _, match := range matches {
		if match.Evidence != "" {
			result = strings.ReplaceAll(result, match.Evidence, "")
		}
	}
	// 清理多余空格
	result = strings.Join(strings.Fields(result), " ")
	return result, nil
}

// generateRecommendation 生成建议
func (d *Detector) generateRecommendation(result *DetectionResult) string {
	if len(result.MatchedRules) == 0 {
		return ""
	}

	recommendations := []string{}
	for _, cat := range result.Categories {
		switch cat {
		case "role_hijack":
			recommendations = append(recommendations, "检测到角色劫持尝试，建议加强系统提示词的防护")
		case "instruction_leak":
			recommendations = append(recommendations, "检测到指令泄漏尝试，建议隐藏系统提示词")
		case "jailbreak":
			recommendations = append(recommendations, "检测到越狱尝试，建议启用严格模式")
		case "bypass":
			recommendations = append(recommendations, "检测到绕过尝试，建议审查输入过滤规则")
		case "data_exfiltration":
			recommendations = append(recommendations, "检测到数据窃取尝试，建议限制工具调用权限")
		case "tool_abuse":
			recommendations = append(recommendations, "检测到工具滥用尝试，建议加强工具调用审计")
		}
	}

	return strings.Join(recommendations, "; ")
}

// extractEvidence 提取证据
func (d *Detector) extractEvidence(input string, matches []MatchedRule) string {
	if len(matches) == 0 {
		return ""
	}

	evidence := matches[0].Evidence
	if len(evidence) > 500 {
		evidence = evidence[:500] + "..."
	}
	return evidence
}

// updatePolicyStats 更新策略统计
func (d *Detector) updatePolicyStats(ctx context.Context, tenantID string, blocked bool) {
	query := `
		UPDATE prompt_injection_policies
		SET total_detections = total_detections + 1,
			total_blocks = total_blocks + $1,
			last_detection_at = NOW()
		WHERE tenant_id = $2
	`
	blockedCount := 0
	if blocked {
		blockedCount = 1
	}
	_, _ = d.db.ExecContext(ctx, query, blockedCount, tenantID)
}

// getRepeatCount 获取重复检测次数
func (d *Detector) getRepeatCount(ctx context.Context, tenantID, sessionKey string) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM prompt_injection_detections
		WHERE tenant_id = $1 AND session_key = $2 AND detected_at > NOW() - INTERVAL '1 hour'`,
		tenantID, sessionKey).Scan(&count)
	return count, err
}

// createApprovalRequest 创建审批请求
func (d *Detector) createApprovalRequest(ctx context.Context, tenantID, requestID, sessionKey, input string, result *DetectionResult, clientIP, userAgent string) {
	approvalID := fmt.Sprintf("pi-approval-%d", time.Now().UnixNano())
	inputPreview := input
	if len(inputPreview) > 200 {
		inputPreview = inputPreview[:200] + "..."
	}

	matchedRulesJSON, _ := json.Marshal(result.MatchedRules)
	detectionDetailsJSON, _ := json.Marshal(result)

	query := `INSERT INTO prompt_injection_approvals (
		id, tenant_id, request_id, session_key,
		detection_score, risk_level, matched_rules, detection_details,
		input_preview, status, client_ip, user_agent
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', $10, $11)`

	_, err := d.db.ExecContext(ctx, query,
		approvalID, tenantID, requestID, sessionKey,
		result.Score, result.RiskLevel, matchedRulesJSON, detectionDetailsJSON,
		inputPreview, clientIP, userAgent)

	if err != nil {
		slog.Warn("prompt_injection: failed to create approval request", "error", err)
	}
}

// learnAttackPattern 自动学习攻击模式
func (d *Detector) learnAttackPattern(ctx context.Context, tenantID, input string, result *DetectionResult) {
	if len(input) < 10 || len(input) > 10000 {
		return
	}

	hash := fmt.Sprintf("%x", []byte(input))
	categoriesJSON, _ := json.Marshal(result.Categories)

	query := `INSERT INTO injection_attack_vectors (
		tenant_id, attack_text, attack_hash, categories, severity, source, detected_at
	) VALUES ($1, $2, $3, $4, $5, 'detection', NOW())
	ON CONFLICT (tenant_id, attack_hash) DO NOTHING`

	_, err := d.db.ExecContext(ctx, query, tenantID, input, hash, categoriesJSON, result.Score)
	if err != nil {
		slog.Warn("prompt_injection: failed to learn attack pattern", "error", err)
	}
}

// RefreshRules 刷新规则缓存
func (d *Detector) RefreshRules() error {
	d.basicRules = nil
	d.advancedRules = nil
	return d.loadRules()
}

// HeuristicEngine 启发式检测引擎
type HeuristicEngine struct{}

func NewHeuristicEngine() *HeuristicEngine {
	return &HeuristicEngine{}
}

// Detect 启发式检测
func (h *HeuristicEngine) Detect(input string) []MatchedRule {
	matches := []MatchedRule{}

	if count := h.countRoleSwitches(input); count > 2 {
		matches = append(matches, MatchedRule{
			RuleName:    "heuristic_multiple_role_switches",
			Category:    "role_hijack",
			Severity:    5 + count,
			Evidence:    fmt.Sprintf("检测到 %d 次角色切换", count),
			Description: "频繁的角色切换可能是注入尝试",
		})
	}

	if h.hasAbnormalLongSentence(input, 500) {
		matches = append(matches, MatchedRule{
			RuleName:    "heuristic_abnormal_sentence_length",
			Category:    "resource_exhaustion",
			Severity:    3,
			Evidence:    "检测到超长单句（>500字符无标点）",
			Description: "异常长句可能用于绕过检测",
		})
	}

	return matches
}

func (h *HeuristicEngine) countRoleSwitches(input string) int {
	patterns := []string{
		`(?i)you are`,
		`(?i)act as`,
		`(?i)pretend to be`,
	}

	count := 0
	for _, pattern := range patterns {
		regex := regexp.MustCompile(pattern)
		count += len(regex.FindAllString(input, -1))
	}
	return count
}

func (h *HeuristicEngine) hasAbnormalLongSentence(input string, threshold int) bool {
	sentences := regexp.MustCompile(`[.!?;。！？；]`).Split(input, -1)
	for _, sentence := range sentences {
		if len(strings.TrimSpace(sentence)) > threshold {
			return true
		}
	}
	return false
}

// LLMDetectionEngine LLM 检测引擎
type LLMDetectionEngine struct {
	db *sql.DB
}

type LLMDetectionResult struct {
	IsInjection bool     `json:"is_injection"`
	Confidence  float64  `json:"confidence"`
	Categories  []string `json:"categories"`
	Reason      string   `json:"reason"`
	Evidence    string   `json:"evidence"`
}

func NewLLMDetectionEngine(db *sql.DB) *LLMDetectionEngine {
	return &LLMDetectionEngine{db: db}
}

func (e *LLMDetectionEngine) Detect(ctx context.Context, engineID int, input string) (*LLMDetectionResult, error) {
	// 获取引擎配置
	var systemPrompt, detectionPrompt string
	err := e.db.QueryRowContext(ctx,
		`SELECT system_prompt, detection_prompt FROM prompt_injection_llm_engines WHERE id = $1 AND enabled = true`,
		engineID).Scan(&systemPrompt, &detectionPrompt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// 构建检测请求
	_ = systemPrompt
	_ = detectionPrompt

	// TODO: 调用 LLM 进行检测
	// 这里应该调用配置的 LLM 引擎
	return nil, nil
}

// CanaryDetector Canary Token 检测器
type CanaryDetector struct {
	db *sql.DB
}

type CanaryDetectionResult struct {
	TokenValue  string
	MatchedRules []MatchedRule
}

func NewCanaryDetector(db *sql.DB) *CanaryDetector {
	return &CanaryDetector{db: db}
}

func (d *CanaryDetector) Detect(ctx context.Context, tenantID, input string) (*CanaryDetectionResult, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, token_value FROM canary_tokens WHERE tenant_id = $1 AND active = true`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int
		var tokenValue string
		if err := rows.Scan(&id, &tokenValue); err != nil {
			continue
		}

		if strings.Contains(input, tokenValue) {
			// 更新泄漏统计
			_, _ = d.db.ExecContext(ctx,
				`UPDATE canary_tokens SET times_leaked = times_leaked + 1, last_leaked_at = NOW() WHERE id = $1`, id)

			return &CanaryDetectionResult{
				TokenValue: tokenValue,
				MatchedRules: []MatchedRule{{
					RuleName:    "canary_token_leaked",
					Category:    "prompt_leaking",
					Severity:    10,
					Evidence:    fmt.Sprintf("检测到 Canary Token 泄漏: %s", tokenValue[:8]+"..."),
					Description: "系统提示词可能已被泄漏",
				}},
			}, nil
		}
	}

	return nil, nil
}

// VectorMatcher 向量相似度匹配器
type VectorMatcher struct {
	db *sql.DB
}

type VectorMatchResult struct {
	AttackID     int64
	MatchedRules []MatchedRule
}

func NewVectorMatcher(db *sql.DB) *VectorMatcher {
	return &VectorMatcher{db: db}
}

func (m *VectorMatcher) FindSimilar(ctx context.Context, tenantID, input string, threshold float64) (*VectorMatchResult, error) {
	// TODO: 实现向量相似度匹配
	// 需要先生成输入的向量嵌入，然后使用 pgvector 进行相似度搜索
	return nil, nil
}
