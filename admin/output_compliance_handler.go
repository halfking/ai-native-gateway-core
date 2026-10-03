package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lib/pq"
)

// OutputComplianceHandler 输出合规管理 API
// 提供策略配置、自定义敏感词、复核队列、反馈等管理端点。
type OutputComplianceHandler struct {
	pool   *pgxpool.Pool
	secret string
}

// NewOutputComplianceHandler 创建 Handler
func NewOutputComplianceHandler(pool *pgxpool.Pool, secret string) *OutputComplianceHandler {
	return &OutputComplianceHandler{pool: pool, secret: secret}
}

// RegisterRoutes 注册 /api/admin/output-compliance/* 路由。
// 所有路由统一套用 AdminMiddleware（JWT/管理 Key 认证），与 prompt-injection
// 等其他 admin handler 同一套鉴权链路。
//
// 2026-10-03 P1 根修（R38 窗口复审 WP-B 发现）：此前 8 个端点裸挂 mux，
// 而全局 auth 中间件对 /api/ 前缀显式旁路（middleware/auth_mw.go 自证的
// 不变式），等于未认证可读 default 租户合规记录、policy 可未认证写。
func (h *OutputComplianceHandler) RegisterRoutes(mux *http.ServeMux) {
	wrap := func(fn http.HandlerFunc) http.HandlerFunc {
		return AdminMiddleware(fn, h.pool, h.secret)
	}

	mux.HandleFunc("/api/admin/output-compliance/policy", wrap(h.handlePolicy))
	mux.HandleFunc("/api/admin/output-compliance/policy/", wrap(h.handlePolicy))

	mux.HandleFunc("/api/admin/output-compliance/keywords", wrap(h.handleKeywords))
	mux.HandleFunc("/api/admin/output-compliance/keywords/", wrap(h.handleKeywordSubrouter))

	mux.HandleFunc("/api/admin/output-compliance/review-queue", wrap(h.handleReviewQueue))
	mux.HandleFunc("/api/admin/output-compliance/review-queue/", wrap(h.handleReviewQueueSubrouter))

	mux.HandleFunc("/api/admin/output-compliance/feedback", wrap(h.handleFeedback))

	mux.HandleFunc("/api/admin/output-compliance/stats", wrap(h.handleStats))
	mux.HandleFunc("/api/admin/output-compliance/records", wrap(h.handleRecords))
}

// ==================== 数据模型 ====================

// OutputCompliancePolicy 输出合规策略（与 DB 字段对齐）
type OutputCompliancePolicy struct {
	ID                                   int             `json:"id"`
	TenantID                             string          `json:"tenant_id"`
	PolicyName                           string          `json:"policy_name"`
	Enabled                              bool            `json:"enabled"`
	EnforcementMode                      string          `json:"enforcement_mode"`
	PIIEngine                            string          `json:"pii_engine"`
	ToxicityEngine                       string          `json:"toxicity_engine"`
	LLMEngineID                          *int            `json:"llm_engine_id"`
	CheckPII                             bool            `json:"check_pii"`
	CheckToxicity                        bool            `json:"check_toxicity"`
	CheckBias                            bool            `json:"check_bias"`
	CheckHallucination                   bool            `json:"check_hallucination"`
	CheckSecrets                         bool            `json:"check_secrets"`
	CheckInternalIP                      bool            `json:"check_internal_ip"`
	CheckJailbreakResponse               bool            `json:"check_jailbreak_response"`
	CheckInstructionInjectionResponse    bool            `json:"check_instruction_injection_response"`
	PIIThreshold                         float64         `json:"pii_threshold"`
	ToxicityThreshold                    float64         `json:"toxicity_threshold"`
	BiasThreshold                        float64         `json:"bias_threshold"`
	HallucinationThreshold               float64         `json:"hallucination_threshold"`
	SecretsThreshold                     float64         `json:"secrets_threshold"`
	InternalIPThreshold                  float64         `json:"internal_ip_threshold"`
	ActionOnPII                          string          `json:"action_on_pii"`
	ActionOnToxicity                     string          `json:"action_on_toxicity"`
	ActionOnBias                         string          `json:"action_on_bias"`
	ActionOnHallucination                string          `json:"action_on_hallucination"`
	ActionOnSecrets                      string          `json:"action_on_secrets"`
	ActionOnInternalIP                   string          `json:"action_on_internal_ip"`
	ActionOnJailbreakResponse            string          `json:"action_on_jailbreak_response"`
	ActionOnInstructionInjectionResponse string          `json:"action_on_instruction_injection_response"`
	AutoRedact                           bool            `json:"auto_redact"`
	RedactEmail                          bool            `json:"redact_email"`
	RedactPhone                          bool            `json:"redact_phone"`
	RedactIDCard                         bool            `json:"redact_id_card"`
	RedactCreditCard                     bool            `json:"redact_credit_card"`
	RedactBankCard                       bool            `json:"redact_bank_card"`
	RedactJWT                            bool            `json:"redact_jwt"`
	RedactPassword                       bool            `json:"redact_password"`
	ToxicReplacement                     string          `json:"toxic_replacement"`
	BlockMessage                         string          `json:"block_message"`
	StrictMode                           bool            `json:"strict_mode"`
	WhitelistKeywords                    []string        `json:"whitelist_keywords"`
	ExceptionRules                       json.RawMessage `json:"exception_rules"`
	NotificationChannels                 json.RawMessage `json:"notification_channels"`
	RealtimeAlertEnabled                 bool            `json:"realtime_alert_enabled"`
	AlertThresholdSeverity               int             `json:"alert_threshold_severity"`
	AlertAggregationWindowMinutes        int             `json:"alert_aggregation_window_minutes"`
	SamplingRate                         float64         `json:"sampling_rate"`
	AutoReviewQueueEnabled               bool            `json:"auto_review_queue_enabled"`
	FeedbackLoopEnabled                  bool            `json:"feedback_loop_enabled"`
	SkillGenerationEnabled               bool            `json:"skill_generation_enabled"`
	AutoThresholdTuningEnabled           bool            `json:"auto_threshold_tuning_enabled"`
	RetentionDays                        int             `json:"retention_days"`
	TotalDetections                      int             `json:"total_detections"`
	TotalBlocks                          int             `json:"total_blocks"`
	LastDetectionAt                      *string         `json:"last_detection_at"`
	CreatedAt                            string          `json:"created_at"`
	UpdatedAt                            string          `json:"updated_at"`
}

// OutputComplianceKeyword 自定义敏感词
type OutputComplianceKeyword struct {
	ID          int    `json:"id"`
	TenantID    string `json:"tenant_id"`
	Keyword     string `json:"keyword"`
	Category    string `json:"category"`
	Severity    int    `json:"severity"`
	Action      string `json:"action"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// OutputComplianceReviewQueueItem 复核队列项
type OutputComplianceReviewQueueItem struct {
	ID            int     `json:"id"`
	TenantID      string  `json:"tenant_id"`
	AuditID       int64   `json:"audit_id"`
	RequestID     string  `json:"request_id"`
	SessionKey    string  `json:"session_key,omitempty"`
	IssueType     string  `json:"issue_type"`
	IssueSubtype  string  `json:"issue_subtype,omitempty"`
	Severity      int     `json:"severity"`
	Status        string  `json:"status"`
	Reviewer      string  `json:"reviewer,omitempty"`
	ReviewComment string  `json:"review_comment,omitempty"`
	CreatedAt     string  `json:"created_at"`
	ReviewedAt    *string `json:"reviewed_at,omitempty"`
}

// OutputComplianceFeedback 误报/漏报反馈
type OutputComplianceFeedback struct {
	ID           int    `json:"id"`
	TenantID     string `json:"tenant_id"`
	AuditID      int64  `json:"audit_id"`
	FeedbackType string `json:"feedback_type"`
	Reporter     string `json:"reporter,omitempty"`
	Comment      string `json:"comment,omitempty"`
	CreatedAt    string `json:"created_at"`
}

const outputCompliancePolicyColumns = `
	id, tenant_id, policy_name, enabled, enforcement_mode,
	pii_engine, toxicity_engine, llm_engine_id,
	check_pii, check_toxicity, check_bias, check_hallucination,
	check_secrets, check_internal_ip, check_jailbreak_response, check_instruction_injection_response,
	pii_threshold, toxicity_threshold, bias_threshold, hallucination_threshold,
	secrets_threshold, internal_ip_threshold,
	action_on_pii, action_on_toxicity, action_on_bias, action_on_hallucination,
	action_on_secrets, action_on_internal_ip, action_on_jailbreak_response, action_on_instruction_injection_response,
	auto_redact, redact_email, redact_phone, redact_id_card, redact_credit_card,
	redact_bank_card, redact_jwt, redact_password, toxic_replacement, block_message,
	strict_mode,
	COALESCE(whitelist_keywords, '{}'), COALESCE(exception_rules, '[]'), COALESCE(notification_channels, '[]'),
	realtime_alert_enabled, alert_threshold_severity, alert_aggregation_window_minutes,
	sampling_rate, auto_review_queue_enabled, feedback_loop_enabled, skill_generation_enabled, auto_threshold_tuning_enabled,
	retention_days, total_detections, total_blocks, last_detection_at, created_at, updated_at
`

// ==================== 策略配置 ====================

func (h *OutputComplianceHandler) handlePolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getPolicy(w, r)
	case http.MethodPut, http.MethodPost:
		h.updatePolicy(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *OutputComplianceHandler) getPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	tenantID := GetTenantID(r)
	policy, err := h.fetchPolicy(r.Context(), tenantID)
	if err != nil {
		writeInternalErrStr(w, "Failed to get policy", err)
		return
	}

	writeJSON(w, http.StatusOK, policy)
}

func (h *OutputComplianceHandler) fetchPolicy(ctx context.Context, tenantID string) (*OutputCompliancePolicy, error) {
	query := "SELECT " + outputCompliancePolicyColumns + " FROM output_compliance_policies WHERE tenant_id = $1"
	row := h.pool.QueryRow(ctx, query, tenantID)
	p, err := scanOutputCompliancePolicy(row)
	if err == pgx.ErrNoRows {
		return defaultOutputCompliancePolicy(tenantID), nil
	}
	return p, err
}

func scanOutputCompliancePolicy(row pgx.Row) (*OutputCompliancePolicy, error) {
	p := &OutputCompliancePolicy{}
	var lastDetection sql.NullString
	err := row.Scan(
		&p.ID, &p.TenantID, &p.PolicyName, &p.Enabled, &p.EnforcementMode,
		&p.PIIEngine, &p.ToxicityEngine, &p.LLMEngineID,
		&p.CheckPII, &p.CheckToxicity, &p.CheckBias, &p.CheckHallucination,
		&p.CheckSecrets, &p.CheckInternalIP, &p.CheckJailbreakResponse, &p.CheckInstructionInjectionResponse,
		&p.PIIThreshold, &p.ToxicityThreshold, &p.BiasThreshold, &p.HallucinationThreshold,
		&p.SecretsThreshold, &p.InternalIPThreshold,
		&p.ActionOnPII, &p.ActionOnToxicity, &p.ActionOnBias, &p.ActionOnHallucination,
		&p.ActionOnSecrets, &p.ActionOnInternalIP, &p.ActionOnJailbreakResponse, &p.ActionOnInstructionInjectionResponse,
		&p.AutoRedact, &p.RedactEmail, &p.RedactPhone, &p.RedactIDCard, &p.RedactCreditCard,
		&p.RedactBankCard, &p.RedactJWT, &p.RedactPassword, &p.ToxicReplacement, &p.BlockMessage,
		&p.StrictMode,
		&p.WhitelistKeywords, &p.ExceptionRules, &p.NotificationChannels,
		&p.RealtimeAlertEnabled, &p.AlertThresholdSeverity, &p.AlertAggregationWindowMinutes,
		&p.SamplingRate, &p.AutoReviewQueueEnabled, &p.FeedbackLoopEnabled, &p.SkillGenerationEnabled, &p.AutoThresholdTuningEnabled,
		&p.RetentionDays, &p.TotalDetections, &p.TotalBlocks, &lastDetection,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		p = defaultOutputCompliancePolicy(tenantFromRow(row))
		return p, nil
	}
	if lastDetection.Valid {
		s := lastDetection.String
		p.LastDetectionAt = &s
	}
	return p, err
}

// tenantFromRow 无状态占位，仅用于返回默认策略；实际 tenant 从请求取得。
func tenantFromRow(row pgx.Row) string { return "default" }

func defaultOutputCompliancePolicy(tenantID string) *OutputCompliancePolicy {
	zero := 0
	_ = zero
	return &OutputCompliancePolicy{
		TenantID: tenantID, PolicyName: "default", Enabled: true, EnforcementMode: "observe",
		PIIEngine: "regex", ToxicityEngine: "keyword", LLMEngineID: nil,
		CheckPII: true, CheckToxicity: true, CheckBias: false, CheckHallucination: false,
		CheckSecrets: true, CheckInternalIP: true, CheckJailbreakResponse: false, CheckInstructionInjectionResponse: false,
		PIIThreshold: 0.7, ToxicityThreshold: 0.7, BiasThreshold: 0.6, HallucinationThreshold: 0.7,
		SecretsThreshold: 0.7, InternalIPThreshold: 0.7,
		ActionOnPII: "redact", ActionOnToxicity: "warn", ActionOnBias: "log", ActionOnHallucination: "log",
		ActionOnSecrets: "redact", ActionOnInternalIP: "redact", ActionOnJailbreakResponse: "block", ActionOnInstructionInjectionResponse: "block",
		AutoRedact: true, RedactEmail: true, RedactPhone: true, RedactIDCard: true, RedactCreditCard: true,
		RedactBankCard: false, RedactJWT: true, RedactPassword: true,
		ToxicReplacement: "[内容已过滤]", BlockMessage: "响应因合规策略被阻断",
		StrictMode:        false,
		WhitelistKeywords: []string{}, ExceptionRules: []byte("[]"), NotificationChannels: []byte("[]"),
		RealtimeAlertEnabled: false, AlertThresholdSeverity: 7, AlertAggregationWindowMinutes: 5,
		SamplingRate: 1.0, AutoReviewQueueEnabled: false, FeedbackLoopEnabled: false,
		SkillGenerationEnabled: false, AutoThresholdTuningEnabled: false, RetentionDays: 90,
	}
}

func (h *OutputComplianceHandler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	tenantID := GetTenantID(r)
	adminUser := authEmail(r)

	var req OutputCompliancePolicy
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}

	if req.EnforcementMode != "" && req.EnforcementMode != "observe" && req.EnforcementMode != "enforce" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enforcement_mode must be observe or enforce"})
		return
	}

	now := time.Now()
	_ = now
	query := `INSERT INTO output_compliance_policies (
			tenant_id, policy_name, enabled, enforcement_mode,
			pii_engine, toxicity_engine, llm_engine_id,
			check_pii, check_toxicity, check_bias, check_hallucination,
			check_secrets, check_internal_ip, check_jailbreak_response, check_instruction_injection_response,
			pii_threshold, toxicity_threshold, bias_threshold, hallucination_threshold,
			secrets_threshold, internal_ip_threshold,
			action_on_pii, action_on_toxicity, action_on_bias, action_on_hallucination,
			action_on_secrets, action_on_internal_ip, action_on_jailbreak_response, action_on_instruction_injection_response,
			auto_redact, redact_email, redact_phone, redact_id_card, redact_credit_card,
			redact_bank_card, redact_jwt, redact_password, toxic_replacement, block_message,
			strict_mode, whitelist_keywords, exception_rules, notification_channels,
			realtime_alert_enabled, alert_threshold_severity, alert_aggregation_window_minutes,
			sampling_rate, auto_review_queue_enabled, feedback_loop_enabled, skill_generation_enabled, auto_threshold_tuning_enabled,
			retention_days, created_by, updated_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,$50,$51,$52,$53,$54,$55,$56)
		ON CONFLICT (tenant_id) DO UPDATE SET
			policy_name=EXCLUDED.policy_name, enabled=EXCLUDED.enabled, enforcement_mode=EXCLUDED.enforcement_mode,
			pii_engine=EXCLUDED.pii_engine, toxicity_engine=EXCLUDED.toxicity_engine, llm_engine_id=EXCLUDED.llm_engine_id,
			check_pii=EXCLUDED.check_pii, check_toxicity=EXCLUDED.check_toxicity, check_bias=EXCLUDED.check_bias, check_hallucination=EXCLUDED.check_hallucination,
			check_secrets=EXCLUDED.check_secrets, check_internal_ip=EXCLUDED.check_internal_ip, check_jailbreak_response=EXCLUDED.check_jailbreak_response, check_instruction_injection_response=EXCLUDED.check_instruction_injection_response,
			pii_threshold=EXCLUDED.pii_threshold, toxicity_threshold=EXCLUDED.toxicity_threshold, bias_threshold=EXCLUDED.bias_threshold, hallucination_threshold=EXCLUDED.hallucination_threshold,
			secrets_threshold=EXCLUDED.secrets_threshold, internal_ip_threshold=EXCLUDED.internal_ip_threshold,
			action_on_pii=EXCLUDED.action_on_pii, action_on_toxicity=EXCLUDED.action_on_toxicity, action_on_bias=EXCLUDED.action_on_bias, action_on_hallucination=EXCLUDED.action_on_hallucination,
			action_on_secrets=EXCLUDED.action_on_secrets, action_on_internal_ip=EXCLUDED.action_on_internal_ip, action_on_jailbreak_response=EXCLUDED.action_on_jailbreak_response, action_on_instruction_injection_response=EXCLUDED.action_on_instruction_injection_response,
			auto_redact=EXCLUDED.auto_redact, redact_email=EXCLUDED.redact_email, redact_phone=EXCLUDED.redact_phone, redact_id_card=EXCLUDED.redact_id_card, redact_credit_card=EXCLUDED.redact_credit_card,
			redact_bank_card=EXCLUDED.redact_bank_card, redact_jwt=EXCLUDED.redact_jwt, redact_password=EXCLUDED.redact_password, toxic_replacement=EXCLUDED.toxic_replacement, block_message=EXCLUDED.block_message,
			strict_mode=EXCLUDED.strict_mode, whitelist_keywords=EXCLUDED.whitelist_keywords, exception_rules=EXCLUDED.exception_rules, notification_channels=EXCLUDED.notification_channels,
			realtime_alert_enabled=EXCLUDED.realtime_alert_enabled, alert_threshold_severity=EXCLUDED.alert_threshold_severity, alert_aggregation_window_minutes=EXCLUDED.alert_aggregation_window_minutes,
			sampling_rate=EXCLUDED.sampling_rate, auto_review_queue_enabled=EXCLUDED.auto_review_queue_enabled, feedback_loop_enabled=EXCLUDED.feedback_loop_enabled, skill_generation_enabled=EXCLUDED.skill_generation_enabled, auto_threshold_tuning_enabled=EXCLUDED.auto_threshold_tuning_enabled,
			retention_days=EXCLUDED.retention_days, updated_by=EXCLUDED.updated_by, updated_at=EXCLUDED.updated_at
		RETURNING id`

	var policyID int
	var exRules, notifyChan any
	if len(req.ExceptionRules) > 0 {
		exRules = req.ExceptionRules
	} else {
		exRules = []byte("[]")
	}
	if len(req.NotificationChannels) > 0 {
		notifyChan = req.NotificationChannels
	} else {
		notifyChan = []byte("[]")
	}

	err := h.pool.QueryRow(r.Context(), query,
		tenantID, defaultVal(req.PolicyName, "default"), req.Enabled, defaultVal(req.EnforcementMode, "observe"),
		defaultVal(req.PIIEngine, "regex"), defaultVal(req.ToxicityEngine, "keyword"), req.LLMEngineID,
		req.CheckPII, req.CheckToxicity, req.CheckBias, req.CheckHallucination,
		req.CheckSecrets, req.CheckInternalIP, req.CheckJailbreakResponse, req.CheckInstructionInjectionResponse,
		req.PIIThreshold, req.ToxicityThreshold, req.BiasThreshold, req.HallucinationThreshold,
		req.SecretsThreshold, req.InternalIPThreshold,
		defaultVal(req.ActionOnPII, "redact"), defaultVal(req.ActionOnToxicity, "warn"), defaultVal(req.ActionOnBias, "log"), defaultVal(req.ActionOnHallucination, "log"),
		defaultVal(req.ActionOnSecrets, "redact"), defaultVal(req.ActionOnInternalIP, "redact"), defaultVal(req.ActionOnJailbreakResponse, "block"), defaultVal(req.ActionOnInstructionInjectionResponse, "block"),
		req.AutoRedact, req.RedactEmail, req.RedactPhone, req.RedactIDCard, req.RedactCreditCard,
		req.RedactBankCard, req.RedactJWT, req.RedactPassword, defaultVal(req.ToxicReplacement, "[内容已过滤]"), defaultVal(req.BlockMessage, "响应因合规策略被阻断"),
		req.StrictMode, pq.Array(req.WhitelistKeywords), exRules, notifyChan,
		req.RealtimeAlertEnabled, req.AlertThresholdSeverity, req.AlertAggregationWindowMinutes,
		req.SamplingRate, req.AutoReviewQueueEnabled, req.FeedbackLoopEnabled, req.SkillGenerationEnabled, req.AutoThresholdTuningEnabled,
		req.RetentionDays, adminUser, adminUser, now, now,
	).Scan(&policyID)
	if err != nil {
		writeInternalErrStr(w, "Failed to update policy", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Policy updated successfully", "policy_id": policyID})
}

func defaultVal[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// ==================== 自定义敏感词 ====================

func (h *OutputComplianceHandler) handleKeywords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listKeywords(w, r)
	case http.MethodPost:
		h.createKeyword(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *OutputComplianceHandler) handleKeywordSubrouter(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/toggle") {
		h.toggleKeyword(w, r)
		return
	}

	rest := strings.TrimPrefix(path, "/api/admin/output-compliance/keywords/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing keyword ID"})
		return
	}

	if r.Method == http.MethodDelete {
		h.deleteKeyword(w, r)
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
}

func (h *OutputComplianceHandler) listKeywords(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	category := r.URL.Query().Get("category")
	query := `SELECT id, tenant_id, keyword, category, severity, action, enabled, description, created_at, updated_at
		FROM output_compliance_custom_keywords WHERE tenant_id = $1`
	args := []any{tenantID}
	if category != "" {
		query += " AND category = $2"
		args = append(args, category)
	}
	query += " ORDER BY updated_at DESC"

	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeInternalErrStr(w, "Failed to list keywords", err)
		return
	}
	defer rows.Close()

	keywords := []OutputComplianceKeyword{}
	for rows.Next() {
		k, err := scanKeyword(rows)
		if err != nil {
			writeInternalErrStr(w, "Failed to scan keyword", err)
			return
		}
		keywords = append(keywords, *k)
	}
	// 违禁词清单少一截 = 该词不再被拦截 = 静默放行。审计读面不返回半份。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate keywords", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"keywords": keywords})
}

func scanKeyword(rows pgx.Rows) (*OutputComplianceKeyword, error) {
	k := &OutputComplianceKeyword{}
	err := rows.Scan(&k.ID, &k.TenantID, &k.Keyword, &k.Category, &k.Severity, &k.Action, &k.Enabled, &k.Description, &k.CreatedAt, &k.UpdatedAt)
	return k, err
}

func (h *OutputComplianceHandler) createKeyword(w http.ResponseWriter, r *http.Request) {
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	tenantID := GetTenantID(r)
	adminUser := authEmail(r)

	var req OutputComplianceKeyword
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}
	if req.Keyword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keyword is required"})
		return
	}
	if req.Severity < 1 || req.Severity > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "severity must be 1-10"})
		return
	}

	var id int
	query := `INSERT INTO output_compliance_custom_keywords
		(tenant_id, keyword, category, severity, action, enabled, description, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (tenant_id, keyword) DO UPDATE SET
			category=EXCLUDED.category, severity=EXCLUDED.severity, action=EXCLUDED.action,
			enabled=EXCLUDED.enabled, description=EXCLUDED.description, updated_by=EXCLUDED.updated_by, updated_at=NOW()
		RETURNING id`
	err := h.pool.QueryRow(r.Context(), query,
		tenantID, req.Keyword, defaultVal(req.Category, "custom"), req.Severity, defaultVal(req.Action, "warn"), req.Enabled, req.Description, adminUser,
	).Scan(&id)
	if err != nil {
		writeInternalErrStr(w, "Failed to save keyword", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Keyword saved successfully", "id": id})
}

func (h *OutputComplianceHandler) deleteKeyword(w http.ResponseWriter, r *http.Request) {
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	tenantID := GetTenantID(r)
	idStr := extractPathParam(r.URL.Path, "/api/admin/output-compliance/keywords/")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid keyword ID"})
		return
	}

	_, err = h.pool.Exec(r.Context(), "DELETE FROM output_compliance_custom_keywords WHERE id = $1 AND tenant_id = $2", id, tenantID)
	if err != nil {
		writeInternalErrStr(w, "Failed to delete keyword", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Keyword deleted"})
}

func (h *OutputComplianceHandler) toggleKeyword(w http.ResponseWriter, r *http.Request) {
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	tenantID := GetTenantID(r)
	idStr := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/admin/output-compliance/keywords/"), "/toggle")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid keyword ID"})
		return
	}

	var current bool
	err = h.pool.QueryRow(r.Context(), "SELECT enabled FROM output_compliance_custom_keywords WHERE id=$1 AND tenant_id=$2", id, tenantID).Scan(&current)
	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Keyword not found"})
		return
	}
	if err != nil {
		writeInternalErrStr(w, "Failed to query keyword", err)
		return
	}

	_, err = h.pool.Exec(r.Context(), "UPDATE output_compliance_custom_keywords SET enabled = $1, updated_at=NOW() WHERE id=$2 AND tenant_id=$3", !current, id, tenantID)
	if err != nil {
		writeInternalErrStr(w, "Failed to toggle keyword", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "enabled": !current})
}

// ==================== 复核队列 ====================

func (h *OutputComplianceHandler) handleReviewQueue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listReviewQueue(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *OutputComplianceHandler) handleReviewQueueSubrouter(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/approve") || strings.HasSuffix(path, "/reject") {
		h.reviewItem(w, r)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown endpoint"})
}

func (h *OutputComplianceHandler) listReviewQueue(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	limit, offset := parsePagination(r, 20, 0)

	query := `SELECT id, tenant_id, audit_id, request_id, session_key, issue_type, issue_subtype, severity, status, reviewer, review_comment, created_at, reviewed_at
		FROM output_compliance_review_queue
		WHERE tenant_id = $1 AND status = $2
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`

	rows, err := h.pool.Query(r.Context(), query, tenantID, status, limit, offset)
	if err != nil {
		writeInternalErrStr(w, "Failed to list review queue", err)
		return
	}
	defer rows.Close()

	items := []OutputComplianceReviewQueueItem{}
	for rows.Next() {
		it, err := scanReviewQueueItem(rows)
		if err != nil {
			writeInternalErrStr(w, "Failed to scan review item", err)
			return
		}
		items = append(items, *it)
	}
	// 待审队列少一截 = 待人工复核的合规问题被静默从队列里消失。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate review queue", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "status": status, "limit": limit, "offset": offset})
}

func scanReviewQueueItem(rows pgx.Rows) (*OutputComplianceReviewQueueItem, error) {
	it := &OutputComplianceReviewQueueItem{}
	var reviewedAt sql.NullString
	err := rows.Scan(&it.ID, &it.TenantID, &it.AuditID, &it.RequestID, &it.SessionKey, &it.IssueType, &it.IssueSubtype, &it.Severity, &it.Status, &it.Reviewer, &it.ReviewComment, &it.CreatedAt, &reviewedAt)
	if reviewedAt.Valid {
		it.ReviewedAt = &reviewedAt.String
	}
	return it, err
}

func (h *OutputComplianceHandler) reviewItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	tenantID := GetTenantID(r)
	reviewer := authEmail(r)

	path := r.URL.Path
	status := "approved"
	if strings.HasSuffix(path, "/reject") {
		status = "rejected"
	}

	idStr := extractPathParam(path, "/api/admin/output-compliance/review-queue/")
	idStr = strings.TrimSuffix(strings.TrimSuffix(idStr, "/approve"), "/reject")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid review queue ID"})
		return
	}

	var comment string
	if r.ContentLength > 0 {
		var req struct {
			Comment string `json:"comment"`
		}
		_ = readJSONRequired(r, &req)
		comment = req.Comment
	}

	_, err = h.pool.Exec(r.Context(),
		`UPDATE output_compliance_review_queue SET status=$1, reviewer=$2, review_comment=$3, reviewed_at=NOW()
		WHERE id=$4 AND tenant_id=$5`,
		status, reviewer, comment, id, tenantID)
	if err != nil {
		writeInternalErrStr(w, "Failed to update review item", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "status": status})
}

// ==================== 反馈 ====================

func (h *OutputComplianceHandler) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.listFeedback(w, r)
		return
	}
	if r.Method == http.MethodPost {
		h.createFeedback(w, r)
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
}

func (h *OutputComplianceHandler) listFeedback(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	feedbackType := r.URL.Query().Get("type")
	limit, offset := parsePagination(r, 20, 0)

	query := `SELECT id, tenant_id, audit_id, feedback_type, reporter, comment, created_at
		FROM output_compliance_feedback
		WHERE tenant_id = $1`
	args := []any{tenantID, limit, offset}
	argNum := 2
	if feedbackType != "" {
		query += fmt.Sprintf(" AND feedback_type = $%d", argNum)
		args = append(args[:argNum-1], feedbackType, args[argNum-1], args[argNum])
		argNum++
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argNum, argNum+1)

	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeInternalErrStr(w, "Failed to list feedback", err)
		return
	}
	defer rows.Close()

	items := []OutputComplianceFeedback{}
	for rows.Next() {
		fb := &OutputComplianceFeedback{}
		if err := rows.Scan(&fb.ID, &fb.TenantID, &fb.AuditID, &fb.FeedbackType, &fb.Reporter, &fb.Comment, &fb.CreatedAt); err != nil {
			writeInternalErrStr(w, "Failed to scan feedback", err)
			return
		}
		items = append(items, *fb)
	}
	// 合规反馈少一截 = 复核结论被静默丢弃。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate feedback", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"feedback": items, "limit": limit, "offset": offset})
}

func (h *OutputComplianceHandler) createFeedback(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	reporter := authEmail(r)

	var req OutputComplianceFeedback
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}
	if req.AuditID == 0 || req.FeedbackType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audit_id and feedback_type are required"})
		return
	}

	var id int64
	query := `INSERT INTO output_compliance_feedback (tenant_id, audit_id, feedback_type, reporter, comment)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`
	err := h.pool.QueryRow(r.Context(), query, tenantID, req.AuditID, req.FeedbackType, reporter, req.Comment).Scan(&id)
	if err != nil {
		writeInternalErrStr(w, "Failed to save feedback", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "message": "Feedback recorded"})
}

// ==================== 统计 ====================

func (h *OutputComplianceHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	tenantID := GetTenantID(r)

	var totalIssues, blocked int
	err := h.pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FILTER (WHERE tenant_id=$1), COUNT(*) FILTER (WHERE tenant_id=$1 AND blocked=true)
		FROM output_compliance_audit`, tenantID).Scan(&totalIssues, &blocked)
	if err != nil {
		writeInternalErrStr(w, "Failed to query stats", err)
		return
	}

	pendingReviews := 0
	_ = h.pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM output_compliance_review_queue WHERE tenant_id=$1 AND status='pending'`, tenantID).Scan(&pendingReviews)

	// 2026-10-03：视图（web/src/views/OutputComplianceView.vue）一直按下面这组
	// 字段读 stats，之前接口只返回 {total_issues, blocked, pending_reviews}，
	// 字段对不上 → 模板对 undefined 调 toLocaleString → 整页白屏。
	//
	// issue_type 的取值域是**核实过的**，不是猜的：写库处只有
	// domains/outputcompliance/checker.go 的五处 Type 字面量
	// pii / toxic / secret / internal_ip / bias。
	// 视图里的 toxicity_hits 对应库里的 toxic；jailbreak_hits 对应的检查
	// 本仓根本不存在（无 jailbreak 检测器），恒为 0，这里显式写 0 而不是
	// 编一个数出来。avg_latency_ms 在审计表里没有对应列，同样返回 0。
	var piiHits, secretHits, toxicHits int
	var lastDetected *time.Time
	_ = h.pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FILTER (WHERE tenant_id=$1 AND issue_type='pii'),
		        COUNT(*) FILTER (WHERE tenant_id=$1 AND issue_type='secret'),
		        COUNT(*) FILTER (WHERE tenant_id=$1 AND issue_type='toxic'),
		        MAX(detected_at)
		 FROM output_compliance_audit`, tenantID).
		Scan(&piiHits, &secretHits, &toxicHits, &lastDetected)

	lastUpdated := ""
	if lastDetected != nil {
		lastUpdated = lastDetected.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		// 原有字段保持不变（可能有别的调用方）
		"total_issues":    totalIssues,
		"blocked":         blocked,
		"pending_reviews": pendingReviews,
		// 视图契约
		"total_checks":   totalIssues,
		"pii_hits":       piiHits,
		"secret_hits":    secretHits,
		"toxicity_hits":  toxicHits,
		"jailbreak_hits": 0, // 本仓无 jailbreak 检测器，诚实报 0
		"avg_latency_ms": 0, // 审计表无此列
		"last_updated":   lastUpdated,
	})
}

// complianceRecordPreviewLimit 命中记录里预览文本的最大长度。
// 预览只在引擎已脱敏的行上出现（见 handleRecords），这里只管截断长度。
const complianceRecordPreviewLimit = 120

// handleRecords: GET /api/admin/output-compliance/records
//
// 2026-10-03 新增。视图的「命中记录」表一直在调这个端点，而后端从未实现
// （RegisterRoutes 里也没有），于是该 tab 永远 404。
//
// 取值映射（全部来自 output_compliance_audit 的现有列，无新表）：
//
//	session_key → session_id
//	issue_type  → check_type     （pii / toxic / secret / internal_ip / bias）
//	issue_subtype → hit_type
//	detected_at → created_at
//	redacted    → redacted
//	content_preview ← redacted_output，且**仅在 redacted=true 时取**
//
// 最后一条是安全约束，不是风格选择：compliance 引擎判定某处需要脱敏时会把
// 原文替换掉，并把 redacted 置 true；evidence 列存的是命中的原始片段。
// 这里只回显引擎自己脱敏后的 redacted_output，未脱敏的行一律返回空预览——
// 否则这个列表就成了一条「把 PII/密钥原文摊平给人看」的通道，
// 与合规模块存在的目的正好相反。
func (h *OutputComplianceHandler) handleRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	tenantID := GetTenantID(r)
	limit, offset := parsePagination(r, 50, 0)

	where := []string{"tenant_id = $1"}
	args := []interface{}{tenantID}
	if ct := strings.TrimSpace(r.URL.Query().Get("check_type")); ct != "" {
		args = append(args, ct)
		where = append(where, fmt.Sprintf("issue_type = $%d", len(args)))
	}
	if ht := strings.TrimSpace(r.URL.Query().Get("hit_type")); ht != "" {
		args = append(args, ht)
		where = append(where, fmt.Sprintf("issue_subtype = $%d", len(args)))
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := h.pool.QueryRow(r.Context(),
		fmt.Sprintf(`SELECT COUNT(*) FROM output_compliance_audit WHERE %s`, cond), args...).
		Scan(&total); err != nil {
		writeInternalErrStr(w, "Failed to count compliance records", err)
		return
	}

	args = append(args, limit, offset)
	// 占位符：args = [过滤条件…, limit, offset]
	//   len(args)-1 → limit 的序号，len(args) → offset 的序号
	// 截断长度用编译期常量直接内联（不是用户输入，内联安全），
	// 绝不能占一个 $n —— 那样会跟 limit/offset 的序号抢位。
	rows, err := h.pool.Query(r.Context(), fmt.Sprintf(`
		SELECT id, COALESCE(session_key, ''), issue_type, COALESCE(issue_subtype, ''),
		       COALESCE(severity, 0), COALESCE(redacted, false),
		       CASE WHEN COALESCE(redacted, false)
		            THEN left(COALESCE(redacted_output, ''), %d)
		            ELSE '' END,
		       detected_at
		FROM output_compliance_audit
		WHERE %s
		ORDER BY detected_at DESC, id DESC
		LIMIT $%d OFFSET $%d
	`, complianceRecordPreviewLimit, cond, len(args)-1, len(args)), args...)
	if err != nil {
		writeInternalErrStr(w, "Failed to list compliance records", err)
		return
	}
	defer rows.Close()

	records := []map[string]interface{}{}
	for rows.Next() {
		var (
			id                             int64
			sessionKey, checkType, hitType string
			severity                       int
			redacted                       bool
			preview                        string
			createdAt                      time.Time
		)
		if err := rows.Scan(&id, &sessionKey, &checkType, &hitType,
			&severity, &redacted, &preview, &createdAt); err != nil {
			warnRowSkip("outputCompliance.records", err)
			continue
		}
		records = append(records, map[string]interface{}{
			"id":              id,
			"session_id":      sessionKey,
			"tenant_id":       tenantID,
			"check_type":      checkType,
			"hit_type":        hitType,
			"severity":        severity,
			"redacted":        redacted,
			"content_preview": preview,
			"created_at":      createdAt.UTC().Format(time.RFC3339),
		})
	}
	if rerr := rows.Err(); rerr != nil {
		writeInternalErrStr(w, "Failed to iterate compliance records", rerr)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records": records,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// ==================== 辅助函数 ====================

func parsePagination(r *http.Request, defaultLimit, defaultOffset int) (int, int) {
	limit := defaultLimit
	offset := defaultOffset
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}
