// Package outputcompliance 实现输出合规监控
// 检测 LLM 输出中的 PII、毒性内容、偏见、幻觉提示
package outputcompliance

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Checker 输出合规检查器
type Checker struct {
	db           *sql.DB
	piiPatterns  []*PIIPattern
	toxicWords   map[string]*ToxicKeyword
	biasDetector *BiasDetector
}

// PIIPattern PII 检测模式
type PIIPattern struct {
	ID           int
	PatternName  string
	PatternType  string // 'email'/'phone'/'id_card'/'credit_card'/'ssn'
	RegexPattern string
	Description  string
	Enabled      bool
	Severity     int
	RedactFormat string
	regex        *regexp.Regexp
}

// ToxicKeyword 毒性关键词
type ToxicKeyword struct {
	ID       int
	Keyword  string
	Category string // 'profanity'/'hate_speech'/'violence'/'sexual'
	Severity int
	Language string
	Enabled  bool
}

// Policy 输出合规策略
type Policy struct {
	TenantID              string
	Enabled               bool
	EnforcementMode       string
	CheckPII              bool
	CheckToxicity         bool
	CheckBias             bool
	CheckHallucination    bool
	PIIThreshold          float64
	ToxicityThreshold     float64
	BiasThreshold         float64
	HallucinationThreshold float64
	ActionOnPII           string
	ActionOnToxicity      string
	ActionOnBias          string
	ActionOnHallucination string
	AutoRedact            bool
	RedactEmail           bool
	RedactPhone           bool
	RedactIDCard          bool
	RedactCreditCard      bool
	StrictMode            bool
	LogAllOutputs         bool
	WhitelistPatterns     []string
}

// ComplianceResult 合规检查结果
type ComplianceResult struct {
	Compliant      bool              `json:"compliant"`
	Issues         []ComplianceIssue `json:"issues"`
	RedactedOutput string            `json:"redacted_output"`
	Blocked        bool              `json:"blocked"`
}

// ComplianceIssue 合规问题
type ComplianceIssue struct {
	Type        string  `json:"type"`     // 'pii'/'toxic'/'bias'/'hallucination'
	Subtype     string  `json:"subtype"`  // 'email'/'phone'/'profanity' 等
	Severity    int     `json:"severity"` // 1-10
	Location    string  `json:"location"` // "char:120-145"
	Content     string  `json:"content"`  // 脱敏后的内容
	Score       float64 `json:"score"`
	Redacted    bool    `json:"redacted"`
	Description string  `json:"description"`
}

// NewChecker 创建检查器
func NewChecker(db *sql.DB) (*Checker, error) {
	c := &Checker{
		db:           db,
		toxicWords:   make(map[string]*ToxicKeyword),
		biasDetector: NewBiasDetector(),
	}

	if err := c.loadPIIPatterns(); err != nil {
		return nil, fmt.Errorf("failed to load PII patterns: %w", err)
	}

	if err := c.loadToxicKeywords(); err != nil {
		return nil, fmt.Errorf("failed to load toxic keywords: %w", err)
	}

	return c, nil
}

// Check 检查输出合规性
func (c *Checker) Check(ctx context.Context, tenantID, output string) (*ComplianceResult, error) {
	policy, err := c.getPolicy(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get policy: %w", err)
	}

	if !policy.Enabled {
		return &ComplianceResult{
			Compliant:      true,
			RedactedOutput: output,
		}, nil
	}

	result := &ComplianceResult{
		Issues:         []ComplianceIssue{},
		RedactedOutput: output,
	}

	// PII 检测
	if policy.CheckPII {
		piiIssues := c.detectPII(output, policy)
		result.Issues = append(result.Issues, piiIssues...)
	}

	// 毒性检测
	if policy.CheckToxicity {
		toxicIssues := c.detectToxicity(output, policy)
		result.Issues = append(result.Issues, toxicIssues...)
	}

	// 偏见检测
	if policy.CheckBias {
		biasIssues := c.biasDetector.Detect(output, policy.BiasThreshold)
		result.Issues = append(result.Issues, biasIssues...)
	}

	// 自动脱敏
	if policy.AutoRedact && len(result.Issues) > 0 {
		result.RedactedOutput = c.redactOutput(output, result.Issues, policy)
	}

	result.Compliant = len(result.Issues) == 0 || !policy.StrictMode
	result.Blocked = c.shouldBlock(result.Issues, policy)

	return result, nil
}

// CheckAndLog 检查并记录到数据库
func (c *Checker) CheckAndLog(ctx context.Context, tenantID, requestID, sessionKey, output, model, clientIP string) (*ComplianceResult, error) {
	result, err := c.Check(ctx, tenantID, output)
	if err != nil {
		return nil, err
	}

	// 记录每个合规问题
	for _, issue := range result.Issues {
		query := `
			INSERT INTO output_compliance_audit (
				tenant_id, request_id, session_key, issue_type, issue_subtype, severity,
				evidence, location, score, action_taken, redacted, blocked, model, client_ip
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`

		_, err := c.db.ExecContext(ctx, query,
			tenantID, requestID, sessionKey, issue.Type, issue.Subtype, issue.Severity,
			issue.Content, issue.Location, issue.Score,
			c.getActionTaken(issue, result.Blocked),
			issue.Redacted, result.Blocked, model, clientIP,
		)

		if err != nil {
			fmt.Printf("warn: failed to log compliance issue: %v\n", err)
		}
	}

	return result, nil
}

// detectPII PII 检测
func (c *Checker) detectPII(output string, policy *Policy) []ComplianceIssue {
	issues := []ComplianceIssue{}

	for _, pattern := range c.piiPatterns {
		if !pattern.Enabled {
			continue
		}

		if !c.shouldCheckPIIType(pattern.PatternType, policy) {
			continue
		}

		matches := pattern.regex.FindAllStringIndex(output, -1)
		for _, match := range matches {
			start, end := match[0], match[1]
			content := output[start:end]

			issues = append(issues, ComplianceIssue{
				Type:        "pii",
				Subtype:     pattern.PatternType,
				Severity:    pattern.Severity,
				Location:    fmt.Sprintf("char:%d-%d", start, end),
				Content:     c.maskPII(content, pattern.PatternType),
				Score:       1.0,
				Redacted:    policy.AutoRedact,
				Description: pattern.Description,
			})
		}
	}

	return issues
}

// detectToxicity 毒性检测
func (c *Checker) detectToxicity(output string, policy *Policy) []ComplianceIssue {
	issues := []ComplianceIssue{}
	outputLower := strings.ToLower(output)

	for keyword, toxicWord := range c.toxicWords {
		if !toxicWord.Enabled {
			continue
		}

		if strings.Contains(outputLower, strings.ToLower(keyword)) {
			index := strings.Index(outputLower, strings.ToLower(keyword))

			issues = append(issues, ComplianceIssue{
				Type:        "toxic",
				Subtype:     toxicWord.Category,
				Severity:    toxicWord.Severity,
				Location:    fmt.Sprintf("char:%d-%d", index, index+len(keyword)),
				Content:     strings.Repeat("*", len(keyword)),
				Score:       1.0,
				Redacted:    policy.ActionOnToxicity == "redact",
				Description: fmt.Sprintf("检测到%s内容", c.getCategoryLabel(toxicWord.Category)),
			})
		}
	}

	return issues
}

// loadPIIPatterns 加载 PII 模式
func (c *Checker) loadPIIPatterns() error {
	query := `
		SELECT id, pattern_name, pattern_type, regex_pattern, description, enabled, severity, redact_format
		FROM pii_patterns
		WHERE enabled = true
		ORDER BY severity DESC
	`

	rows, err := c.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		pattern := &PIIPattern{}
		if err := rows.Scan(
			&pattern.ID, &pattern.PatternName, &pattern.PatternType,
			&pattern.RegexPattern, &pattern.Description, &pattern.Enabled,
			&pattern.Severity, &pattern.RedactFormat,
		); err != nil {
			return err
		}

		pattern.regex, err = regexp.Compile(pattern.RegexPattern)
		if err != nil {
			fmt.Printf("warn: invalid PII pattern %s: %v\n", pattern.PatternName, err)
			continue
		}

		c.piiPatterns = append(c.piiPatterns, pattern)
	}

	return rows.Err()
}

// loadToxicKeywords 加载毒性关键词
func (c *Checker) loadToxicKeywords() error {
	query := `
		SELECT id, keyword, category, severity, language, enabled
		FROM toxic_keywords
		WHERE enabled = true
	`

	rows, err := c.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		word := &ToxicKeyword{}
		if err := rows.Scan(&word.ID, &word.Keyword, &word.Category, &word.Severity, &word.Language, &word.Enabled); err != nil {
			return err
		}

		c.toxicWords[word.Keyword] = word
	}

	return rows.Err()
}

// getPolicy 获取策略
func (c *Checker) getPolicy(ctx context.Context, tenantID string) (*Policy, error) {
	query := `SELECT tenant_id, enabled, enforcement_mode, check_pii, check_toxicity, check_bias, 
		check_hallucination, pii_threshold, toxicity_threshold, bias_threshold, hallucination_threshold,
		action_on_pii, action_on_toxicity, action_on_bias, action_on_hallucination,
		auto_redact, redact_email, redact_phone, redact_id_card, redact_credit_card,
		strict_mode, log_all_outputs
		FROM output_compliance_policies WHERE tenant_id = $1`

	policy := &Policy{}
	err := c.db.QueryRowContext(ctx, query, tenantID).Scan(
		&policy.TenantID, &policy.Enabled, &policy.EnforcementMode,
		&policy.CheckPII, &policy.CheckToxicity, &policy.CheckBias, &policy.CheckHallucination,
		&policy.PIIThreshold, &policy.ToxicityThreshold, &policy.BiasThreshold, &policy.HallucinationThreshold,
		&policy.ActionOnPII, &policy.ActionOnToxicity, &policy.ActionOnBias, &policy.ActionOnHallucination,
		&policy.AutoRedact, &policy.RedactEmail, &policy.RedactPhone, &policy.RedactIDCard, &policy.RedactCreditCard,
		&policy.StrictMode, &policy.LogAllOutputs,
	)

	if err == sql.ErrNoRows {
		return &Policy{
			TenantID:              tenantID,
			Enabled:               true,
			EnforcementMode:       "observe",
			CheckPII:              true,
			CheckToxicity:         true,
			CheckBias:             false,
			CheckHallucination:    false,
			PIIThreshold:          0.7,
			ToxicityThreshold:     0.7,
			BiasThreshold:         0.6,
			HallucinationThreshold: 0.7,
			ActionOnPII:           "redact",
			ActionOnToxicity:      "warn",
			ActionOnBias:          "log",
			ActionOnHallucination: "log",
			AutoRedact:            true,
			RedactEmail:           true,
			RedactPhone:           true,
			RedactIDCard:          true,
			RedactCreditCard:      true,
			StrictMode:            false,
			LogAllOutputs:         false,
		}, nil
	}

	return policy, err
}

// shouldCheckPIIType 判断是否检查某类 PII
func (c *Checker) shouldCheckPIIType(piiType string, policy *Policy) bool {
	switch piiType {
	case "email":
		return policy.RedactEmail
	case "phone":
		return policy.RedactPhone
	case "id_card":
		return policy.RedactIDCard
	case "credit_card":
		return policy.RedactCreditCard
	default:
		return true
	}
}

// maskPII 脱敏 PII
func (c *Checker) maskPII(content, piiType string) string {
	switch piiType {
	case "email":
		parts := strings.Split(content, "@")
		if len(parts) == 2 {
			user := parts[0]
			domain := parts[1]
			if len(user) > 1 {
				user = string(user[0]) + "***"
			}
			if len(domain) > 4 {
				domain = string(domain[0]) + "***" + domain[len(domain)-4:]
			}
			return user + "@" + domain
		}
		return "***@***.com"

	case "phone":
		if len(content) >= 11 {
			return content[:3] + "****" + content[len(content)-4:]
		}
		return "***-****-****"

	case "id_card":
		if len(content) >= 18 {
			return "******" + content[6:10] + "******"
		}
		return "******19******"

	case "credit_card":
		if len(content) >= 16 {
			return "****-****-****-" + content[len(content)-4:]
		}
		return "****-****-****-****"

	default:
		return strings.Repeat("*", len(content))
	}
}

// redactOutput 脱敏输出
func (c *Checker) redactOutput(output string, issues []ComplianceIssue, policy *Policy) string {
	redacted := output

	for _, issue := range issues {
		if issue.Type == "pii" && policy.AutoRedact {
			redacted = strings.ReplaceAll(redacted, issue.Content, "[已脱敏]")
		} else if issue.Type == "toxic" && policy.ActionOnToxicity == "redact" {
			redacted = strings.ReplaceAll(redacted, issue.Content, "[内容已过滤]")
		}
	}

	return redacted
}

// shouldBlock 判断是否阻断
func (c *Checker) shouldBlock(issues []ComplianceIssue, policy *Policy) bool {
	if !policy.StrictMode || policy.EnforcementMode != "enforce" {
		return false
	}

	for _, issue := range issues {
		if issue.Severity >= 9 {
			return true
		}
	}

	return false
}

// getActionTaken 获取执行的动作
func (c *Checker) getActionTaken(issue ComplianceIssue, blocked bool) string {
	if blocked {
		return "blocked"
	}
	if issue.Redacted {
		return "redacted"
	}
	if issue.Severity >= 7 {
		return "warned"
	}
	return "logged"
}

// getCategoryLabel 获取分类标签
func (c *Checker) getCategoryLabel(category string) string {
	labels := map[string]string{
		"profanity":   "辱骂",
		"hate_speech": "仇恨言论",
		"violence":    "暴力",
		"sexual":      "色情",
	}
	if label, ok := labels[category]; ok {
		return label
	}
	return category
}

// BiasDetector 偏见检测器
type BiasDetector struct{}

func NewBiasDetector() *BiasDetector {
	return &BiasDetector{}
}

func (b *BiasDetector) Detect(output string, threshold float64) []ComplianceIssue {
	issues := []ComplianceIssue{}

	biasKeywords := map[string]int{
		"性别歧视": 8,
		"种族歧视": 9,
		"年龄歧视": 7,
	}

	for keyword, severity := range biasKeywords {
		if strings.Contains(output, keyword) {
			issues = append(issues, ComplianceIssue{
				Type:        "bias",
				Subtype:     "discrimination",
				Severity:    severity,
				Content:     keyword,
				Score:       0.9,
				Description: "检测到潜在偏见内容",
			})
		}
	}

	return issues
}

// RefreshPatterns 刷新检测模式
func (c *Checker) RefreshPatterns() error {
	c.piiPatterns = nil
	c.toxicWords = make(map[string]*ToxicKeyword)

	if err := c.loadPIIPatterns(); err != nil {
		return err
	}

	return c.loadToxicKeywords()
}
