package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
)

// PromptInjectionHandler 提示词注入管理 Handler
// 遵循 admin 包惯例：使用 http.ResponseWriter 模式，与 admin/auth.go 的 handleLogin 保持一致。
type PromptInjectionHandler struct {
	db *sql.DB
}

// NewPromptInjectionHandler 创建 Handler
func NewPromptInjectionHandler(db *sql.DB) *PromptInjectionHandler {
	return &PromptInjectionHandler{db: db}
}

// PromptInjectionPolicy 策略配置
type PromptInjectionPolicy struct {
	ID                     int      `json:"id"`
	TenantID               string   `json:"tenant_id"`
	Enabled                bool     `json:"enabled"`
	DetectionMode          string   `json:"detection_mode"`
	EnableBasicRules       bool     `json:"enable_basic_rules"`
	EnableAdvancedRules    bool     `json:"enable_advanced_rules"`
	EnableHeuristics       bool     `json:"enable_heuristics"`
	EnableMLModel          bool     `json:"enable_ml_model"`
	ScoreThresholdLog      int      `json:"score_threshold_log"`
	ScoreThresholdWarn     int      `json:"score_threshold_warn"`
	ScoreThresholdSanitize int      `json:"score_threshold_sanitize"`
	ScoreThresholdBlock    int      `json:"score_threshold_block"`
	ActionOnLowRisk        string   `json:"action_on_low_risk"`
	ActionOnMediumRisk     string   `json:"action_on_medium_risk"`
	ActionOnHighRisk       string   `json:"action_on_high_risk"`
	WhitelistPatterns      []string `json:"whitelist_patterns"`
	WhitelistUsers         []string `json:"whitelist_users"`
	NotifyOnDetection      bool     `json:"notify_on_detection"`
	NotificationWebhook    string   `json:"notification_webhook"`
	NotificationEmail      string   `json:"notification_email"`
	TotalDetections        int      `json:"total_detections"`
	TotalBlocks            int      `json:"total_blocks"`
	LastDetectionAt        *string  `json:"last_detection_at"`
	CreatedAt              string   `json:"created_at"`
	UpdatedAt              string   `json:"updated_at"`
}

// PromptInjectionRule 检测规则
type PromptInjectionRule struct {
	ID            int    `json:"id"`
	RuleName      string `json:"rule_name"`
	RuleType      string `json:"rule_type"`
	Category      string `json:"category"`
	Pattern       string `json:"pattern"`
	Description   string `json:"description"`
	Severity      int    `json:"severity"`
	Enabled       bool   `json:"enabled"`
	CaseSensitive bool   `json:"case_sensitive"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// PromptInjectionDetection 检测日志
type PromptInjectionDetection struct {
	ID                int    `json:"id"`
	TenantID          string `json:"tenant_id"`
	RequestID         string `json:"request_id"`
	SessionKey        string `json:"session_key"`
	DetectedAt        string `json:"detected_at"`
	DetectionScore    int    `json:"detection_score"`
	RiskLevel         string `json:"risk_level"`
	MatchedRules      string `json:"matched_rules"`
	MatchedRulesCount int    `json:"matched_rules_count"`
	ActionTaken       string `json:"action_taken"`
	Blocked           bool   `json:"blocked"`
	EvidenceText      string `json:"evidence_text"`
	ClientIP          string `json:"client_ip"`
	UserAgent         string `json:"user_agent"`
}

// DetectionStats 检测统计
type DetectionStats struct {
	TotalDetections int     `json:"total_detections"`
	BlockedCount    int     `json:"blocked_count"`
	CriticalCount   int     `json:"critical_count"`
	HighCount       int     `json:"high_count"`
	MediumCount     int     `json:"medium_count"`
	LowCount        int     `json:"low_count"`
	AvgScore        float64 `json:"avg_score"`
	MaxScore        int     `json:"max_score"`
}

// getTenantID 从请求中提取租户 ID
func (h *PromptInjectionHandler) getTenantID(r *http.Request) string {
	if tid := r.Header.Get("X-Tenant-ID"); tid != "" {
		return tid
	}
	return "default"
}

// getUserEmail 从请求中提取当前用户邮箱
func (h *PromptInjectionHandler) getUserEmail(r *http.Request) string {
	if u := r.Header.Get("X-User-Email"); u != "" {
		return u
	}
	return "system"
}

// writeJSON 统一的 JSON 响应写入器
func (h *PromptInjectionHandler) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 统一错误响应
func (h *PromptInjectionHandler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]interface{}{"error": msg})
}

// GetPolicy 获取策略配置
func (h *PromptInjectionHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := h.getTenantID(r)

	query := `SELECT id, tenant_id, enabled, detection_mode, enable_basic_rules, enable_advanced_rules, enable_heuristics, enable_ml_model, score_threshold_log, score_threshold_warn, score_threshold_sanitize, score_threshold_block, action_on_low_risk, action_on_medium_risk, action_on_high_risk, COALESCE(whitelist_patterns, '{}'), COALESCE(whitelist_users, '{}'), notify_on_detection, COALESCE(notification_webhook, ''), COALESCE(notification_email, ''), total_detections, total_blocks, last_detection_at, created_at, updated_at FROM prompt_injection_policies WHERE tenant_id = $1`

	policy := &PromptInjectionPolicy{}
	err := h.db.QueryRowContext(r.Context(), query, tenantID).Scan(
		&policy.ID, &policy.TenantID, &policy.Enabled, &policy.DetectionMode,
		&policy.EnableBasicRules, &policy.EnableAdvancedRules, &policy.EnableHeuristics, &policy.EnableMLModel,
		&policy.ScoreThresholdLog, &policy.ScoreThresholdWarn, &policy.ScoreThresholdSanitize, &policy.ScoreThresholdBlock,
		&policy.ActionOnLowRisk, &policy.ActionOnMediumRisk, &policy.ActionOnHighRisk,
		&policy.WhitelistPatterns, &policy.WhitelistUsers,
		&policy.NotifyOnDetection, &policy.NotificationWebhook, &policy.NotificationEmail,
		&policy.TotalDetections, &policy.TotalBlocks, &policy.LastDetectionAt,
		&policy.CreatedAt, &policy.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		policy = &PromptInjectionPolicy{
			TenantID: tenantID, Enabled: true, DetectionMode: "observe",
			EnableBasicRules: true, EnableAdvancedRules: true, EnableHeuristics: true, EnableMLModel: false,
			ScoreThresholdLog: 3, ScoreThresholdWarn: 6, ScoreThresholdSanitize: 8, ScoreThresholdBlock: 10,
			ActionOnLowRisk: "log", ActionOnMediumRisk: "warn", ActionOnHighRisk: "block",
			WhitelistPatterns: []string{}, WhitelistUsers: []string{}, NotifyOnDetection: false,
		}
		h.writeJSON(w, http.StatusOK, policy)
		return
	}

	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to get policy: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, policy)
}

// UpdatePolicy 更新策略配置
func (h *PromptInjectionHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := h.getTenantID(r)
	adminUser := h.getUserEmail(r)

	var req PromptInjectionPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request: "+err.Error())
		return
	}

	if req.ScoreThresholdLog < 0 || req.ScoreThresholdLog > 10 ||
		req.ScoreThresholdWarn < 0 || req.ScoreThresholdWarn > 10 ||
		req.ScoreThresholdSanitize < 0 || req.ScoreThresholdSanitize > 10 ||
		req.ScoreThresholdBlock < 0 || req.ScoreThresholdBlock > 10 {
		h.writeError(w, http.StatusBadRequest, "Score thresholds must be between 0-10")
		return
	}

	query := `INSERT INTO prompt_injection_policies (tenant_id, enabled, detection_mode, enable_basic_rules, enable_advanced_rules, enable_heuristics, enable_ml_model, score_threshold_log, score_threshold_warn, score_threshold_sanitize, score_threshold_block, action_on_low_risk, action_on_medium_risk, action_on_high_risk, whitelist_patterns, whitelist_users, notify_on_detection, notification_webhook, notification_email, created_by, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $20) ON CONFLICT (tenant_id) DO UPDATE SET enabled = EXCLUDED.enabled, detection_mode = EXCLUDED.detection_mode, enable_basic_rules = EXCLUDED.enable_basic_rules, enable_advanced_rules = EXCLUDED.enable_advanced_rules, enable_heuristics = EXCLUDED.enable_heuristics, enable_ml_model = EXCLUDED.enable_ml_model, score_threshold_log = EXCLUDED.score_threshold_log, score_threshold_warn = EXCLUDED.score_threshold_warn, score_threshold_sanitize = EXCLUDED.score_threshold_sanitize, score_threshold_block = EXCLUDED.score_threshold_block, action_on_low_risk = EXCLUDED.action_on_low_risk, action_on_medium_risk = EXCLUDED.action_on_medium_risk, action_on_high_risk = EXCLUDED.action_on_high_risk, whitelist_patterns = EXCLUDED.whitelist_patterns, whitelist_users = EXCLUDED.whitelist_users, notify_on_detection = EXCLUDED.notify_on_detection, notification_webhook = EXCLUDED.notification_webhook, notification_email = EXCLUDED.notification_email, updated_by = EXCLUDED.updated_by, updated_at = NOW() RETURNING id`

	var policyID int
	err := h.db.QueryRowContext(r.Context(), query, tenantID, req.Enabled, req.DetectionMode, req.EnableBasicRules, req.EnableAdvancedRules, req.EnableHeuristics, req.EnableMLModel, req.ScoreThresholdLog, req.ScoreThresholdWarn, req.ScoreThresholdSanitize, req.ScoreThresholdBlock, req.ActionOnLowRisk, req.ActionOnMediumRisk, req.ActionOnHighRisk, req.WhitelistPatterns, req.WhitelistUsers, req.NotifyOnDetection, req.NotificationWebhook, req.NotificationEmail, adminUser).Scan(&policyID)

	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to update policy: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Policy updated successfully", "policy_id": policyID})
}

// ListRules 列出所有检测规则
func (h *PromptInjectionHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ruleType := q.Get("type")
	category := q.Get("category")
	enabled := q.Get("enabled")

	query := `SELECT id, rule_name, rule_type, category, pattern, description, severity, enabled, case_sensitive, created_at, updated_at FROM prompt_injection_rules WHERE 1=1`
	args := []interface{}{}
	argCount := 1

	if ruleType != "" {
		query += " AND rule_type = $" + strconv.Itoa(argCount)
		args = append(args, ruleType)
		argCount++
	}
	if category != "" {
		query += " AND category = $" + strconv.Itoa(argCount)
		args = append(args, category)
		argCount++
	}
	if enabled != "" {
		query += " AND enabled = $" + strconv.Itoa(argCount)
		args = append(args, enabled == "true")
	}

	query += " ORDER BY severity DESC, rule_type, category"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to list rules: "+err.Error())
		return
	}
	defer rows.Close()

	rules := []PromptInjectionRule{}
	for rows.Next() {
		rule := PromptInjectionRule{}
		if err := rows.Scan(&rule.ID, &rule.RuleName, &rule.RuleType, &rule.Category, &rule.Pattern, &rule.Description, &rule.Severity, &rule.Enabled, &rule.CaseSensitive, &rule.CreatedAt, &rule.UpdatedAt); err != nil {
			h.writeError(w, http.StatusInternalServerError, "Failed to scan rule: "+err.Error())
			return
		}
		rules = append(rules, rule)
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{"rules": rules, "count": len(rules)})
}

// ToggleRule 启用/禁用规则
// 路径形如 /admin/prompt-injection/rules/{id}/toggle
func (h *PromptInjectionHandler) ToggleRule(w http.ResponseWriter, r *http.Request) {
	// 从路径提取 ruleID: .../rules/{id}/toggle
	path := r.URL.Path
	idx := stringsLastIndex(path, "/")
	if idx == -1 || path[idx+1:] != "toggle" {
		h.writeError(w, http.StatusBadRequest, "Invalid path: must end with /toggle")
		return
	}
	parent := path[:idx]
	idx2 := stringsLastIndex(parent, "/")
	if idx2 == -1 {
		h.writeError(w, http.StatusBadRequest, "Invalid path: missing rule id")
		return
	}
	ruleID := parent[idx2+1:]

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	result, err := h.db.ExecContext(r.Context(), `UPDATE prompt_injection_rules SET enabled = $1, updated_at = NOW() WHERE id = $2`, req.Enabled, ruleID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to toggle rule: "+err.Error())
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		h.writeError(w, http.StatusNotFound, "Rule not found")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Rule toggled successfully"})
}

// stringsLastIndex 从末尾开始查找子串
func stringsLastIndex(s, substr string) int {
	for i := len(s) - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// ListDetections 列出检测日志
func (h *PromptInjectionHandler) ListDetections(w http.ResponseWriter, r *http.Request) {
	tenantID := h.getTenantID(r)

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	riskLevel := q.Get("risk_level")
	blocked := q.Get("blocked")
	sessionKey := q.Get("session_key")

	query := `SELECT id, tenant_id, request_id, session_key, detected_at, detection_score, risk_level, matched_rules, matched_rules_count, action_taken, blocked, evidence_text, client_ip, user_agent FROM prompt_injection_detections WHERE tenant_id = $1`
	args := []interface{}{tenantID}
	argCount := 2

	if riskLevel != "" {
		query += " AND risk_level = $" + strconv.Itoa(argCount)
		args = append(args, riskLevel)
		argCount++
	}
	if blocked != "" {
		query += " AND blocked = $" + strconv.Itoa(argCount)
		args = append(args, blocked == "true")
		argCount++
	}
	if sessionKey != "" {
		query += " AND session_key = $" + strconv.Itoa(argCount)
		args = append(args, sessionKey)
		argCount++
	}

	query += " ORDER BY detected_at DESC LIMIT $" + strconv.Itoa(argCount) + " OFFSET $" + strconv.Itoa(argCount+1)
	args = append(args, pageSize, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to list detections: "+err.Error())
		return
	}
	defer rows.Close()

	detections := []PromptInjectionDetection{}
	for rows.Next() {
		detection := PromptInjectionDetection{}
		if err := rows.Scan(&detection.ID, &detection.TenantID, &detection.RequestID, &detection.SessionKey, &detection.DetectedAt, &detection.DetectionScore, &detection.RiskLevel, &detection.MatchedRules, &detection.MatchedRulesCount, &detection.ActionTaken, &detection.Blocked, &detection.EvidenceText, &detection.ClientIP, &detection.UserAgent); err != nil {
			h.writeError(w, http.StatusInternalServerError, "Failed to scan detection: "+err.Error())
			return
		}
		detections = append(detections, detection)
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM prompt_injection_detections WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to count detections: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{"detections": detections, "page": page, "page_size": pageSize, "total": total})
}

// GetStats 获取检测统计
func (h *PromptInjectionHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	tenantID := h.getTenantID(r)

	stats := &DetectionStats{}
	err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(total_detections, 0), COALESCE(blocked_count, 0), COALESCE(critical_count, 0), COALESCE(high_count, 0), COALESCE(medium_count, 0), COALESCE(low_count, 0), COALESCE(avg_score, 0), COALESCE(max_score, 0) FROM prompt_injection_stats_today WHERE tenant_id = $1`, tenantID).Scan(&stats.TotalDetections, &stats.BlockedCount, &stats.CriticalCount, &stats.HighCount, &stats.MediumCount, &stats.LowCount, &stats.AvgScore, &stats.MaxScore)

	if err == sql.ErrNoRows {
		stats = &DetectionStats{}
	} else if err != nil {
		h.writeError(w, http.StatusInternalServerError, "Failed to get stats: "+err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, stats)
}
