// Package promptinjection 实现提示词注入检测
// 参考 Langfuse 安全架构，支持多层检测和租户级策略配置
package promptinjection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// Detector 提示词注入检测器
type Detector struct {
	db              *sql.DB
	basicRules      []*Rule
	advancedRules   []*Rule
	heuristicEngine *HeuristicEngine
}

// Rule 检测规则
type Rule struct {
	ID            int
	RuleName      string
	RuleType      string
	Category      string
	Pattern       string
	Description   string
	Severity      int
	Enabled       bool
	CaseSensitive bool
	regex         *regexp.Regexp
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

// DetectionResult 检测结果
type DetectionResult struct {
	Score           int             `json:"score"`
	RiskLevel       string          `json:"risk_level"`
	MatchedRules    []MatchedRule   `json:"matched_rules"`
	DetectionLayers map[string]bool `json:"detection_layers"`
	ActionTaken     string          `json:"action_taken"`
	Blocked         bool            `json:"blocked"`
	Evidence        string          `json:"evidence"`
	Recommendation  string          `json:"recommendation"`
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
	}

	if policy.EnableBasicRules {
		basicMatches := d.detectWithRules(input, d.basicRules)
		result.MatchedRules = append(result.MatchedRules, basicMatches...)
		result.DetectionLayers["basic"] = true
	}

	if policy.EnableAdvancedRules {
		advancedMatches := d.detectWithRules(input, d.advancedRules)
		result.MatchedRules = append(result.MatchedRules, advancedMatches...)
		result.DetectionLayers["advanced"] = true
	}

	if policy.EnableHeuristics {
		heuristicMatches := d.heuristicEngine.Detect(input)
		result.MatchedRules = append(result.MatchedRules, heuristicMatches...)
		result.DetectionLayers["heuristics"] = true
	}

	result.Score = d.calculateScore(result.MatchedRules)
	result.RiskLevel = d.calculateRiskLevel(result.Score)
	result.ActionTaken = d.decideAction(result.Score, result.RiskLevel, policy)
	result.Blocked = (result.ActionTaken == "block")
	result.Recommendation = d.generateRecommendation(result)
	result.Evidence = d.extractEvidence(input, result.MatchedRules)

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
		inputHash := fmt.Sprintf("%x", result.Score) // 简化的哈希

		query := `
			INSERT INTO prompt_injection_detections (
				tenant_id, request_id, session_key, detection_score, risk_level,
				matched_rules, matched_rules_count, detection_layers,
				action_taken, blocked, evidence_text, input_hash,
				client_ip, user_agent
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`

		_, err := d.db.ExecContext(ctx, query,
			tenantID, requestID, sessionKey, result.Score, result.RiskLevel,
			matchedRulesJSON, len(result.MatchedRules), detectionLayersJSON,
			result.ActionTaken, result.Blocked, result.Evidence, inputHash,
			clientIP, userAgent,
		)

		if err != nil {
			fmt.Printf("warn: failed to log detection: %v\n", err)
		}

		// 更新策略统计
		d.updatePolicyStats(ctx, tenantID, result.Blocked)
	}

	return result, nil
}

// loadRules 从数据库加载规则；表不存在时自动创建并加载默认规则。
func (d *Detector) loadRules() error {
	query := `
		SELECT id, rule_name, rule_type, category, pattern, description, severity, enabled, case_sensitive
		FROM prompt_injection_rules
		WHERE enabled = true
		ORDER BY severity DESC
	`

	rows, err := d.db.Query(query)
	if err != nil {
		if isTableNotFound(err, "prompt_injection_rules") {
			if createErr := d.ensureRulesTable(); createErr != nil {
				slog.Warn("prompt_injection: cannot auto-create rules table", "error", createErr)
				return nil // 空规则继续，不阻塞启动
			}
			return d.loadRules() // 表创建成功，重试
		}
		return err
	}
	defer rows.Close()

	for rows.Next() {
		rule := &Rule{}
		if err := rows.Scan(&rule.ID, &rule.RuleName, &rule.RuleType, &rule.Category,
			&rule.Pattern, &rule.Description, &rule.Severity, &rule.Enabled, &rule.CaseSensitive); err != nil {
			return err
		}

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

// ensureRulesTable 自动创建 prompt_injection_rules 表并插入默认规则。
// 用于表不存在时的自愈兜底，不阻塞启动。
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

// isTableNotFound reports whether err is a PostgreSQL "relation does not exist" error.
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

			matches = append(matches, MatchedRule{
				RuleName:    rule.RuleName,
				Category:    rule.Category,
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
		enable_heuristics, enable_ml_model, score_threshold_log, score_threshold_warn,
		score_threshold_sanitize, score_threshold_block, action_on_low_risk,
		action_on_medium_risk, action_on_high_risk
		FROM prompt_injection_policies WHERE tenant_id = $1`

	policy := &Policy{}
	err := d.db.QueryRowContext(ctx, query, tenantID).Scan(
		&policy.TenantID, &policy.Enabled, &policy.DetectionMode,
		&policy.EnableBasicRules, &policy.EnableAdvancedRules,
		&policy.EnableHeuristics, &policy.EnableMLModel,
		&policy.ScoreThresholdLog, &policy.ScoreThresholdWarn,
		&policy.ScoreThresholdSanitize, &policy.ScoreThresholdBlock,
		&policy.ActionOnLowRisk, &policy.ActionOnMediumRisk, &policy.ActionOnHighRisk,
	)

	if err == sql.ErrNoRows {
		// 返回默认策略
		return &Policy{
			TenantID:               tenantID,
			Enabled:                true,
			DetectionMode:          "observe",
			EnableBasicRules:       true,
			EnableAdvancedRules:    true,
			EnableHeuristics:       true,
			EnableMLModel:          false,
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

// decideAction 决定响应动作
func (d *Detector) decideAction(score int, riskLevel string, policy *Policy) string {
	switch {
	case score >= policy.ScoreThresholdBlock:
		if policy.DetectionMode == "enforce" {
			return "block"
		}
		return "warn"
	case score >= policy.ScoreThresholdSanitize:
		if policy.DetectionMode == "enforce" {
			return "sanitize"
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

// generateRecommendation 生成建议
func (d *Detector) generateRecommendation(result *DetectionResult) string {
	if len(result.MatchedRules) == 0 {
		return ""
	}

	categories := make(map[string]bool)
	for _, match := range result.MatchedRules {
		categories[match.Category] = true
	}

	recommendations := []string{}
	if categories["role_hijack"] {
		recommendations = append(recommendations, "检测到角色劫持尝试，建议加强系统提示词的防护")
	}
	if categories["instruction_leak"] {
		recommendations = append(recommendations, "检测到指令泄漏尝试，建议隐藏系统提示词")
	}
	if categories["dan"] {
		recommendations = append(recommendations, "检测到 DAN 越狱尝试，建议启用严格模式")
	}
	if categories["bypass"] {
		recommendations = append(recommendations, "检测到绕过尝试，建议审查输入过滤规则")
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
	d.db.ExecContext(ctx, query, blockedCount, tenantID)
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
			Category:    "bypass",
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

// RefreshRules 刷新规则缓存
func (d *Detector) RefreshRules() error {
	d.basicRules = nil
	d.advancedRules = nil
	return d.loadRules()
}
