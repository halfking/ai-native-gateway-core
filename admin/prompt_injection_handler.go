package admin

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
	"github.com/kaixuan/llm-gateway-go/internal/jsoncol"
	"github.com/lib/pq"
)

// PromptInjectionHandler 提示词注入管理 Handler
// 遵循 admin 包惯例：使用 http.ResponseWriter 模式，与 admin/auth.go 的 handleLogin 保持一致。
type PromptInjectionHandler struct {
	pool   *pgxpool.Pool
	secret string
}

// promptInjectionDetectionsSelectCols 是检测列表的**正典形状**投影。
//
// 2026-10-20 复核轮：这段投影原先写的是历史迁移 315 的形状
// （detection_score / matched_rules / matched_rules_count / risk_level varchar），
// 而**正典表**（sql/schema/01-schema.sql 与
// sql/objects/tables/prompt_injection_detections.sql）从未采用该形状：
// 正典 risk_level 是 **integer 分数（1..10，带 CHECK 约束）**，
// 且没有 detection_score / matched_rules / matched_rules_count 三列。
// 315 的 `CREATE TABLE IF NOT EXISTS` 在正典库上是**空操作**
// （表已存在 ⇒ 那几列从未被创建）⇒ 旧投影恒报 42703 ⇒ 端点 500。
// 现在按正典形状读：
//
//	· 分数（detection_score）取 risk_level（integer 1..10）；
//	· 等级字符串（risk_level）由同一套阈值派生
//	  （domains/promptinjection.RiskLevelSQLExpr，与写侧
//	   RiskLevelFromScore 同源 —— 落库的分数与页面显示的等级不能各说各话）；
//	· matched_rules ← 命中规则的 rule_name；
//	· matched_rules_count ← categories 的元素个数。
//
// 响应字段名与 JSON 契约保持不变（前端不改）。
//
// 提成包级常量是为了让真库判据能**逐字**跑它
// （见 prompt_injection_canonical_shape_test.go），而不是让判据自己抄一份。
const promptInjectionDetectionsSelectCols = `SELECT id, tenant_id, request_id, COALESCE(session_key, '') AS session_key, detected_at,
		risk_level AS detection_score,
		` + promptinjection.RiskLevelSQLExpr + ` AS risk_level,
		COALESCE(rule_name, '') AS matched_rules,
		COALESCE(array_length(categories, 1), 0) AS matched_rules_count,
		action_taken, blocked, COALESCE(evidence_text, '') AS evidence_text,
		COALESCE(categories::text[], '{}'::text[]), llm_confidence, COALESCE(llm_reason, ''),
		COALESCE(canary_token_leaked, ''), COALESCE(approval_id, ''),
		COALESCE(replaced_content, ''), COALESCE(client_ip, ''), COALESCE(user_agent, '')
		FROM prompt_injection_detections WHERE `

// promptInjectionDetectionsCountFrom 是检测列表的计数查询前缀。
//
// 2026-10-20 复核轮：原先这里用 strings.Replace 把 SELECT 换成
// "SELECT COUNT(*)"。那是**字符串级替换**：只要 SELECT 文本与模板有一个
// 字符不同（换行、空格、字段顺序），替换就静默不生效 ⇒ count 查询仍返回
// 整行集 ⇒ Scan(&total) 报 "cannot scan ..."。计数与列表因此共用一段脆弱的
// 文本耦合。改为**显式 count 查询 + 共享 WHERE**。
const promptInjectionDetectionsCountFrom = "SELECT COUNT(*) FROM prompt_injection_detections WHERE "

// promptInjectionStatsSQL 是检测统计的**正典形状**聚合。
//
// 2026-10-20 复核轮：原先读视图 prompt_injection_stats_enhanced（迁移 364）。
// 该视图**在任何正典库上都建不出来**：它引用 d.detection_score，而正典表
// 没有这一列 ⇒ 42P01 ⇒ 端点 500。且视图的严重等级分桶写的是
// `(risk_level)::text = 'critical'`，而正典 risk_level 是 integer ⇒ 即使
// 视图建出来，那 4 个分桶也**恒为 0**。
// ⇒ 改为直接在正典表上聚合，阈值与写侧 RiskLevelFromScore 一致
// （10/8/6，与 domains/promptinjection.RiskLevelSQLExpr 同源）。
// 聚合查询恒返回一行（无 GROUP BY）⇒ 「今天没有命中」= 全零，
// 不再需要靠 ErrNoRows 兜底。
const promptInjectionStatsSQL = `SELECT COUNT(*) AS total_detections,
			COUNT(*) FILTER (WHERE blocked) AS blocked_count,
			COUNT(*) FILTER (WHERE risk_level >= 10) AS critical_count,
			COUNT(*) FILTER (WHERE risk_level >= 8 AND risk_level < 10) AS high_count,
			COUNT(*) FILTER (WHERE risk_level >= 6 AND risk_level < 8) AS medium_count,
			COUNT(*) FILTER (WHERE risk_level < 6) AS low_count,
			COUNT(*) FILTER (WHERE action_taken = 'approve') AS approval_count,
			COUNT(*) FILTER (WHERE action_taken = 'replace') AS replaced_count,
			COUNT(*) FILTER (WHERE action_taken = 'terminate') AS terminated_count,
			COUNT(*) FILTER (WHERE canary_token_leaked IS NOT NULL) AS canary_leak_count,
			COALESCE(AVG(risk_level), 0)::double precision AS avg_score,
			COALESCE(MAX(risk_level), 0) AS max_score,
			COALESCE(AVG(llm_confidence), 0)::double precision AS avg_llm_confidence,
			COUNT(DISTINCT session_key) AS affected_sessions
		FROM prompt_injection_detections
		WHERE tenant_id = $1 AND detected_at >= CURRENT_DATE`

// NewPromptInjectionHandler 创建 Handler
func NewPromptInjectionHandler(pool *pgxpool.Pool, secret string) *PromptInjectionHandler {
	return &PromptInjectionHandler{pool: pool, secret: secret}
}

// RegisterRoutes 注册路由。
// 所有路由统一套用 AdminMiddleware（JWT/管理 Key 认证），
// 与其他 admin handler 走同一套鉴权链路。
func (h *PromptInjectionHandler) RegisterRoutes(mux *http.ServeMux) {
	// wrap：每个 handler 都过 AdminMiddleware，保证 AuthContext 注入。
	wrap := func(fn http.HandlerFunc) http.HandlerFunc {
		return AdminMiddleware(fn, h.pool, h.secret)
	}

	// 策略配置
	mux.HandleFunc("/api/admin/prompt-injection/policy", wrap(h.handlePolicy))
	mux.HandleFunc("/api/admin/prompt-injection/policy/", wrap(h.handlePolicy))

	// 检测规则
	mux.HandleFunc("/api/admin/prompt-injection/rules", wrap(h.handleRules))
	mux.HandleFunc("/api/admin/prompt-injection/rules/", wrap(h.handleRuleSubrouter))

	// 检测日志
	mux.HandleFunc("/api/admin/prompt-injection/detections", wrap(h.handleDetections))

	// 统计
	mux.HandleFunc("/api/admin/prompt-injection/stats", wrap(h.handleStats))

	// LLM 引擎管理
	mux.HandleFunc("/api/admin/prompt-injection/engines", wrap(h.handleEngines))
	mux.HandleFunc("/api/admin/prompt-injection/engines/", wrap(h.handleEngineSubrouter))

	// 严重等级矩阵
	mux.HandleFunc("/api/admin/prompt-injection/severity-matrix", wrap(h.handleSeverityMatrix))

	// Canary Token 管理
	mux.HandleFunc("/api/admin/prompt-injection/canary-tokens", wrap(h.handleCanaryTokens))
	mux.HandleFunc("/api/admin/prompt-injection/canary-tokens/", wrap(h.handleCanaryTokenSubrouter))

	// 审批队列 - 复用现有 /api/admin/session-approvals 系统
	// 不在此处注册审批相关路由

	// 攻击向量库
	mux.HandleFunc("/api/admin/prompt-injection/attack-vectors", wrap(h.handleAttackVectors))
}

// ==================== 策略配置 ====================

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
	EnableLLMDetection     bool     `json:"enable_llm_detection"`
	EnableCanaryDetection  bool     `json:"enable_canary_detection"`
	EnableVectorSimilarity bool     `json:"enable_vector_similarity"`
	LLMEngineID            *int     `json:"llm_engine_id"`
	ContentReplacement     string   `json:"content_replacement_strategy"`
	MaxInputLength         int      `json:"max_input_length"`
	AutoLearnEnabled       bool     `json:"auto_learn_enabled"`
	DetectionTimeoutMs     int      `json:"detection_timeout_ms"`
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

func (h *PromptInjectionHandler) handlePolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getPolicy(w, r)
	case http.MethodPut, http.MethodPost:
		h.updatePolicy(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *PromptInjectionHandler) getPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)

	query := `SELECT id, tenant_id, enabled, detection_mode,
		enable_basic_rules, enable_advanced_rules, enable_heuristics, enable_ml_model,
		COALESCE(enable_llm_detection, true), COALESCE(enable_canary_detection, true),
		COALESCE(enable_vector_similarity, false), llm_engine_id,
		COALESCE(content_replacement_strategy, 'llm_rewrite'),
		COALESCE(max_input_length, 50000), COALESCE(auto_learn_enabled, false),
		COALESCE(detection_timeout_ms, 5000),
		score_threshold_log, score_threshold_warn, score_threshold_sanitize, score_threshold_block,
		action_on_low_risk, action_on_medium_risk, action_on_high_risk,
		COALESCE(whitelist_patterns, '{}'), COALESCE(whitelist_users, '{}'),
		notify_on_detection, COALESCE(notification_webhook, ''), COALESCE(notification_email, ''),
		total_detections, total_blocks, last_detection_at, created_at, updated_at
		FROM prompt_injection_policies WHERE tenant_id = $1`

	policy := &PromptInjectionPolicy{}
	err := h.pool.QueryRow(r.Context(), query, tenantID).Scan(
		&policy.ID, &policy.TenantID, &policy.Enabled, &policy.DetectionMode,
		&policy.EnableBasicRules, &policy.EnableAdvancedRules, &policy.EnableHeuristics, &policy.EnableMLModel,
		&policy.EnableLLMDetection, &policy.EnableCanaryDetection, &policy.EnableVectorSimilarity,
		&policy.LLMEngineID, &policy.ContentReplacement, &policy.MaxInputLength,
		&policy.AutoLearnEnabled, &policy.DetectionTimeoutMs,
		&policy.ScoreThresholdLog, &policy.ScoreThresholdWarn, &policy.ScoreThresholdSanitize, &policy.ScoreThresholdBlock,
		&policy.ActionOnLowRisk, &policy.ActionOnMediumRisk, &policy.ActionOnHighRisk,
		&policy.WhitelistPatterns, &policy.WhitelistUsers,
		&policy.NotifyOnDetection, &policy.NotificationWebhook, &policy.NotificationEmail,
		&policy.TotalDetections, &policy.TotalBlocks, &policy.LastDetectionAt,
		&policy.CreatedAt, &policy.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		policy = &PromptInjectionPolicy{
			TenantID: tenantID, Enabled: true, DetectionMode: "observe",
			EnableBasicRules: true, EnableAdvancedRules: true, EnableHeuristics: true, EnableMLModel: false,
			EnableLLMDetection: true, EnableCanaryDetection: true, EnableVectorSimilarity: false,
			ContentReplacement: "llm_rewrite", MaxInputLength: 50000, DetectionTimeoutMs: 5000,
			ScoreThresholdLog: 3, ScoreThresholdWarn: 6, ScoreThresholdSanitize: 8, ScoreThresholdBlock: 10,
			ActionOnLowRisk: "log", ActionOnMediumRisk: "warn", ActionOnHighRisk: "block",
			WhitelistPatterns: []string{}, WhitelistUsers: []string{},
		}
		writeJSON(w, http.StatusOK, policy)
		return
	}

	if err != nil {
		writeInternalErrStr(w, "Failed to get policy", err)
		return
	}

	writeJSON(w, http.StatusOK, policy)
}

func (h *PromptInjectionHandler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	adminUser := authEmail(r)

	var req PromptInjectionPolicy
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}

	// 验证阈值
	if req.ScoreThresholdLog < 0 || req.ScoreThresholdLog > 10 ||
		req.ScoreThresholdWarn < 0 || req.ScoreThresholdWarn > 10 ||
		req.ScoreThresholdSanitize < 0 || req.ScoreThresholdSanitize > 10 ||
		req.ScoreThresholdBlock < 0 || req.ScoreThresholdBlock > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Score thresholds must be between 0-10"})
		return
	}

	query := `INSERT INTO prompt_injection_policies (
		tenant_id, enabled, detection_mode, enable_basic_rules, enable_advanced_rules,
		enable_heuristics, enable_ml_model, enable_llm_detection, enable_canary_detection,
		enable_vector_similarity, llm_engine_id, content_replacement_strategy,
		max_input_length, auto_learn_enabled, detection_timeout_ms,
		score_threshold_log, score_threshold_warn, score_threshold_sanitize, score_threshold_block,
		action_on_low_risk, action_on_medium_risk, action_on_high_risk,
		whitelist_patterns, whitelist_users, notify_on_detection, notification_webhook, notification_email,
		created_by, updated_by
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$28)
	ON CONFLICT (tenant_id) DO UPDATE SET
		enabled=EXCLUDED.enabled, detection_mode=EXCLUDED.detection_mode,
		enable_basic_rules=EXCLUDED.enable_basic_rules, enable_advanced_rules=EXCLUDED.enable_advanced_rules,
		enable_heuristics=EXCLUDED.enable_heuristics, enable_ml_model=EXCLUDED.enable_ml_model,
		enable_llm_detection=EXCLUDED.enable_llm_detection, enable_canary_detection=EXCLUDED.enable_canary_detection,
		enable_vector_similarity=EXCLUDED.enable_vector_similarity, llm_engine_id=EXCLUDED.llm_engine_id,
		content_replacement_strategy=EXCLUDED.content_replacement_strategy,
		max_input_length=EXCLUDED.max_input_length, auto_learn_enabled=EXCLUDED.auto_learn_enabled,
		detection_timeout_ms=EXCLUDED.detection_timeout_ms,
		score_threshold_log=EXCLUDED.score_threshold_log, score_threshold_warn=EXCLUDED.score_threshold_warn,
		score_threshold_sanitize=EXCLUDED.score_threshold_sanitize, score_threshold_block=EXCLUDED.score_threshold_block,
		action_on_low_risk=EXCLUDED.action_on_low_risk, action_on_medium_risk=EXCLUDED.action_on_medium_risk,
		action_on_high_risk=EXCLUDED.action_on_high_risk,
		whitelist_patterns=EXCLUDED.whitelist_patterns, whitelist_users=EXCLUDED.whitelist_users,
		notify_on_detection=EXCLUDED.notify_on_detection, notification_webhook=EXCLUDED.notification_webhook,
		notification_email=EXCLUDED.notification_email, updated_by=EXCLUDED.updated_by, updated_at=NOW()
	RETURNING id`

	var policyID int
	err := h.pool.QueryRow(r.Context(), query,
		tenantID, req.Enabled, req.DetectionMode, req.EnableBasicRules, req.EnableAdvancedRules,
		req.EnableHeuristics, req.EnableMLModel, req.EnableLLMDetection, req.EnableCanaryDetection,
		req.EnableVectorSimilarity, req.LLMEngineID, req.ContentReplacement,
		req.MaxInputLength, req.AutoLearnEnabled, req.DetectionTimeoutMs,
		req.ScoreThresholdLog, req.ScoreThresholdWarn, req.ScoreThresholdSanitize, req.ScoreThresholdBlock,
		req.ActionOnLowRisk, req.ActionOnMediumRisk, req.ActionOnHighRisk,
		pq.Array(req.WhitelistPatterns), pq.Array(req.WhitelistUsers),
		req.NotifyOnDetection, req.NotificationWebhook, req.NotificationEmail, adminUser,
	).Scan(&policyID)

	if err != nil {
		writeInternalErrStr(w, "Failed to update policy", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Policy updated successfully", "policy_id": policyID})
}

// ==================== 检测规则 ====================

// PromptInjectionRule 检测规则
type PromptInjectionRule struct {
	ID             int      `json:"id"`
	RuleName       string   `json:"rule_name"`
	RuleType       string   `json:"rule_type"`
	Category       string   `json:"category"`
	CategoryNew    string   `json:"category_new"`
	Pattern        string   `json:"pattern"`
	Description    string   `json:"description"`
	Severity       int      `json:"severity"`
	Enabled        bool     `json:"enabled"`
	CaseSensitive  bool     `json:"case_sensitive"`
	IsSystem       bool     `json:"is_system"`
	ActionOverride string   `json:"action_override"`
	Tags           []string `json:"tags"`
	Examples       []string `json:"examples"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// PromptInjectionRulePatch 是 PUT /rules/{id} 的**部分更新**载荷。
//
// ★ 2026-10-20 修复：updateRule 过去把 body 解码成 PromptInjectionRule（全是值
// 类型字段）后**无条件 SET 8 列** ⇒ 任何**部分**载荷都会把未传的字段清零。
// 现存爆炸点不是理论上的：web/src/components/PromptInjectionConfigPanel.vue 的
// 严重度滑杆调 updateRule(id, { severity })，对自定义规则会把 pattern/description
// 清空、enabled 置 false、tags/examples 变空数组；对系统规则则整条 404。
//
// 改成指针字段后「未传（nil）」与「传零值（&false / &0 / &[]string{}）」可以区分，
// 语义即 PATCH —— 正好也是 web/src/api/promptInjection.ts 里 UpdateRulePatch
// 一直宣称的语义（此前只是后端没兑现）。
type PromptInjectionRulePatch struct {
	Pattern        *string   `json:"pattern"`
	Description    *string   `json:"description"`
	Severity       *int      `json:"severity"`
	Enabled        *bool     `json:"enabled"`
	CaseSensitive  *bool     `json:"case_sensitive"`
	ActionOverride *string   `json:"action_override"`
	Tags           *[]string `json:"tags"`
	Examples       *[]string `json:"examples"`
}

// promptInjectionRuleSelectCols 是规则列表与单条读取共用的投影。
// 抽成常量是为了让「详情」端点与「列表」端点返回**同一形状**——
// 否则前端详情页会拿到比列表少的字段（例如 false_positive_rate 在 DB 里存在，
// 但两个端点都没投影它；这是有意的：不在结构体里的列不进 API 契约）。
const promptInjectionRuleSelectCols = `id, rule_name, rule_type, category, COALESCE(category_new::text, ''),
	pattern, description, severity, enabled, case_sensitive,
	COALESCE(is_system, true), COALESCE(action_override::text, ''),
	COALESCE(tags, '{}'), COALESCE(examples, '{}'),
	created_at, updated_at`

// enumNullIfEmpty 把枚举列上的空串换成 NULL。
//
// ★ 2026-10-20 浏览器验收时实测到的真缺陷：
// prompt_injection_rules.category_new 是 injection_category 枚举、
// action_override 是 injection_action 枚举，**两列都允许 NULL**。
// 但前端表单在「没选」时给的是**空串**而不是 null（新建规则的表单默认值就是
// ""）⇒ 直接把空串塞进 SQL 会得到
//
//	ERROR: invalid input value for enum injection_action: "" (SQLSTATE 22P02)
//
// 表现是 500，而且**从 UI 新建自定义规则必失败**（每次都带上 action_override:""）。
// 同样的坑在 updateRule 上：把动作覆盖清空再保存，同样 500。
//
// ⇒ 空串的语义是「不覆盖 / 没有覆盖动作」，落库必须是 NULL。
func enumNullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (h *PromptInjectionHandler) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listRules(w, r)
	case http.MethodPost:
		h.createRule(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *PromptInjectionHandler) handleRuleSubrouter(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasSuffix(path, "/toggle") {
		h.toggleRule(w, r)
		return
	}

	// 提取 rule ID
	parts := strings.Split(strings.TrimPrefix(path, "/api/admin/prompt-injection/rules/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing rule ID"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getRule(w, r, parts[0])
	case http.MethodPut:
		h.updateRule(w, r, parts[0])
	case http.MethodDelete:
		h.deleteRule(w, r, parts[0])
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

// getRule 返回单条规则的完整详情。
//
// 2026-10-20 新增：「点开看规则详情」需要一个**有独立 404 语义**的单条读取端点
// —— 列表端点已经返回全部可展示字段（复用同一份投影），但「按 id 取一条」在
// 详情弹窗场景下才是正确的契约：id 不存在时必须能区分「没有这条规则」与
// 「规则被删了/列表缓存过期」。
func (h *PromptInjectionHandler) getRule(w http.ResponseWriter, r *http.Request, ruleID string) {
	row := h.pool.QueryRow(r.Context(),
		`SELECT `+promptInjectionRuleSelectCols+` FROM prompt_injection_rules WHERE id=$1`, ruleID)

	rule := PromptInjectionRule{}
	if err := row.Scan(&rule.ID, &rule.RuleName, &rule.RuleType, &rule.Category, &rule.CategoryNew,
		&rule.Pattern, &rule.Description, &rule.Severity, &rule.Enabled, &rule.CaseSensitive,
		&rule.IsSystem, &rule.ActionOverride, &rule.Tags, &rule.Examples,
		&rule.CreatedAt, &rule.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Rule not found"})
			return
		}
		writeInternalErrStr(w, "Failed to get rule", err)
		return
	}

	writeJSON(w, http.StatusOK, rule)
}

func (h *PromptInjectionHandler) listRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ruleType := q.Get("type")
	category := q.Get("category")
	enabled := q.Get("enabled")
	search := q.Get("search")

	query := `SELECT ` + promptInjectionRuleSelectCols + `
		FROM prompt_injection_rules WHERE 1=1`
	args := []interface{}{}
	argCount := 1

	if ruleType != "" {
		query += " AND rule_type = $" + strconv.Itoa(argCount)
		args = append(args, ruleType)
		argCount++
	}
	if category != "" {
		query += " AND (category = $" + strconv.Itoa(argCount) + " OR category_new::text = $" + strconv.Itoa(argCount) + ")"
		args = append(args, category)
		argCount++
	}
	if enabled != "" {
		query += " AND enabled = $" + strconv.Itoa(argCount)
		args = append(args, enabled == "true")
		argCount++
	}
	if search != "" {
		query += " AND (rule_name ILIKE $" + strconv.Itoa(argCount) + " OR description ILIKE $" + strconv.Itoa(argCount) + ")"
		args = append(args, "%"+search+"%")
		argCount++
	}

	query += " ORDER BY severity DESC, rule_type, category"

	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeInternalErrStr(w, "Failed to list rules", err)
		return
	}
	defer func() { rows.Close() }()

	rules := []PromptInjectionRule{}
	for rows.Next() {
		rule := PromptInjectionRule{}
		if err := rows.Scan(&rule.ID, &rule.RuleName, &rule.RuleType, &rule.Category, &rule.CategoryNew,
			&rule.Pattern, &rule.Description, &rule.Severity, &rule.Enabled, &rule.CaseSensitive,
			&rule.IsSystem, &rule.ActionOverride, &rule.Tags, &rule.Examples,
			&rule.CreatedAt, &rule.UpdatedAt); err != nil {
			writeInternalErrStr(w, "Failed to scan rule", err)
			return
		}
		rules = append(rules, rule)
	}
	// 迭代中断只让 Next() 返回 false。不查 Err() 就 200，规则清单会
	// 静默少一截 —— 安全规则的缺失等于检测被静默关闭。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate rules", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"rules": rules, "count": len(rules)})
}

func (h *PromptInjectionHandler) createRule(w http.ResponseWriter, r *http.Request) {
	var req PromptInjectionRule
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}

	if req.RuleName == "" || req.Pattern == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rule_name and pattern are required"})
		return
	}

	query := `INSERT INTO prompt_injection_rules (rule_name, rule_type, category, category_new, pattern, description, severity, enabled, case_sensitive, is_system, action_override, tags, examples)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, false, $10, $11, $12)
		RETURNING id`

	var ruleID int
	err := h.pool.QueryRow(r.Context(), query,
		req.RuleName, req.RuleType, req.Category, enumNullIfEmpty(req.CategoryNew), req.Pattern, req.Description,
		req.Severity, req.Enabled, req.CaseSensitive, enumNullIfEmpty(req.ActionOverride),
		pq.Array(req.Tags), pq.Array(req.Examples),
	).Scan(&ruleID)

	if err != nil {
		writeInternalErrStr(w, "Failed to create rule", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{"message": "Rule created", "rule_id": ruleID})
}

func (h *PromptInjectionHandler) updateRule(w http.ResponseWriter, r *http.Request, ruleID string) {
	// 2026-10-20：改成 PATCH 语义（指针字段 + 动态 SET）。
	// 旧实现无条件 SET 8 列，任何部分载荷都会把未传字段清零（详见
	// PromptInjectionRulePatch 的注释）。全量载荷仍然照旧生效。
	var patch PromptInjectionRulePatch
	if err := readJSONRequired(r, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}

	sets := make([]string, 0, 8)
	args := make([]interface{}, 0, 9)
	add := func(col string, val interface{}) {
		args = append(args, val)
		sets = append(sets, col+"=$"+strconv.Itoa(len(args)))
	}
	if patch.Pattern != nil {
		add("pattern", *patch.Pattern)
	}
	if patch.Description != nil {
		add("description", *patch.Description)
	}
	if patch.Severity != nil {
		add("severity", *patch.Severity)
	}
	if patch.Enabled != nil {
		add("enabled", *patch.Enabled)
	}
	if patch.CaseSensitive != nil {
		add("case_sensitive", *patch.CaseSensitive)
	}
	if patch.ActionOverride != nil {
		add("action_override", enumNullIfEmpty(*patch.ActionOverride))
	}
	if patch.Tags != nil {
		add("tags", pq.Array(*patch.Tags))
	}
	if patch.Examples != nil {
		add("examples", pq.Array(*patch.Examples))
	}
	if len(sets) == 0 {
		// 空补丁：旧实现会把整行清零（pattern=''、enabled=false…）。现在明确拒绝。
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no updatable fields supplied (expected at least one of: pattern, description, severity, enabled, case_sensitive, action_override, tags, examples)",
		})
		return
	}

	args = append(args, ruleID)
	query := `UPDATE prompt_injection_rules SET ` + strings.Join(sets, ", ") +
		`, updated_at=NOW() WHERE id=$` + strconv.Itoa(len(args)) +
		` AND COALESCE(is_system, true) = false`

	result, err := h.pool.Exec(r.Context(), query, args...)
	if err != nil {
		writeInternalErrStr(w, "Failed to update rule", err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Rule not found or is a system rule"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Rule updated"})
}

func (h *PromptInjectionHandler) deleteRule(w http.ResponseWriter, r *http.Request, ruleID string) {
	result, err := h.pool.Exec(r.Context(),
		`DELETE FROM prompt_injection_rules WHERE id=$1 AND COALESCE(is_system, true) = false`, ruleID)

	if err != nil {
		writeInternalErrStr(w, "Failed to delete rule", err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Rule not found or is a system rule"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Rule deleted"})
}

func (h *PromptInjectionHandler) toggleRule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	// 从路径提取 ruleID
	path := r.URL.Path
	path = strings.TrimSuffix(path, "/toggle")
	parts := strings.Split(strings.TrimPrefix(path, "/api/admin/prompt-injection/rules/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing rule ID"})
		return
	}
	ruleID := parts[0]

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	result, err := h.pool.Exec(r.Context(),
		`UPDATE prompt_injection_rules SET enabled = $1, updated_at = NOW() WHERE id = $2`,
		req.Enabled, ruleID)
	if err != nil {
		writeInternalErrStr(w, "Failed to toggle rule", err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Rule not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Rule toggled"})
}

// ==================== 检测日志 ====================

// PromptInjectionDetection 检测日志
type PromptInjectionDetection struct {
	ID         int    `json:"id"`
	TenantID   string `json:"tenant_id"`
	RequestID  string `json:"request_id"`
	SessionKey string `json:"session_key"`
	// 2026-10-20 复核轮：原先是 string。timestamptz 扫不进 *string
	//（pgx 二进制格式报 "cannot scan timestamptz (OID 1184) ... into *string"），
	// 而旧查询在更早的 42703 就挂了 ⇒ 这个缺陷被掩盖着。
	// 改成 *time.Time：JSON 仍是**字符串**（RFC3339，前端 fmtTime 可解析），
	// 且列可空时如实给 null，不编造 1970。
	DetectedAt        *time.Time `json:"detected_at"`
	DetectionScore    int        `json:"detection_score"`
	RiskLevel         string     `json:"risk_level"`
	MatchedRules      string     `json:"matched_rules"`
	MatchedRulesCount int        `json:"matched_rules_count"`
	ActionTaken       string     `json:"action_taken"`
	Blocked           bool       `json:"blocked"`
	EvidenceText      string     `json:"evidence_text"`
	Categories        []string   `json:"categories"`
	LLMConfidence     *float64   `json:"llm_confidence"`
	LLMReason         string     `json:"llm_reason"`
	CanaryTokenLeaked string     `json:"canary_token_leaked"`
	ApprovalID        string     `json:"approval_id"`
	ReplacedContent   string     `json:"replaced_content"`
	ClientIP          string     `json:"client_ip"`
	UserAgent         string     `json:"user_agent"`
}

func (h *PromptInjectionHandler) handleDetections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	tenantID := GetTenantID(r)
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
	action := q.Get("action")
	sessionKey := q.Get("session_key")
	category := q.Get("category")

	// 2026-10-20 复核轮：这段查询原先查的是历史迁移 315 的形状
	// （detection_score / matched_rules / matched_rules_count / risk_level varchar），
	// 而**正典表**（sql/schema/01-schema.sql 与
	// sql/objects/tables/prompt_injection_detections.sql）从未采用该形状：
	// 正典 risk_level 是 **integer 分数（1..10，带 CHECK 约束）**，
	// 且没有 detection_score / matched_rules / matched_rules_count 三列。
	// 315 的 `CREATE TABLE IF NOT EXISTS` 在正典库上是**空操作**
	// （表已存在 ⇒ 那几列从未被创建）⇒ 旧查询恒报 42703 ⇒ 端点 500。
	// 现在按**正典形状**读：
	//   · 分数（detection_score）取 risk_level（integer 1..10）；
	//   · 等级字符串（risk_level）由同一套阈值派生
	//     （domains/promptinjection.RiskLevelSQLExpr，与写侧
	//      RiskLevelFromScore 同源 —— 落库的分数与页面显示的等级不能各说各话）；
	//   · matched_rules ← 命中规则的 rule_name；
	//   · matched_rules_count ← categories 的元素个数。
	// 响应字段名与 JSON 契约保持不变（前端不改）。
	selectCols := promptInjectionDetectionsSelectCols

	where := "tenant_id = $1"
	args := []interface{}{tenantID}
	argCount := 2

	if riskLevel != "" {
		where += " AND " + promptinjection.RiskLevelSQLExpr + " = $" + strconv.Itoa(argCount)
		args = append(args, riskLevel)
		argCount++
	}
	if blocked != "" {
		where += " AND blocked = $" + strconv.Itoa(argCount)
		args = append(args, blocked == "true")
		argCount++
	}
	if action != "" {
		where += " AND action_taken = $" + strconv.Itoa(argCount)
		args = append(args, action)
		argCount++
	}
	if sessionKey != "" {
		where += " AND session_key = $" + strconv.Itoa(argCount)
		args = append(args, sessionKey)
		argCount++
	}
	if category != "" {
		where += " AND $" + strconv.Itoa(argCount) + " = ANY(categories)"
		args = append(args, category)
		argCount++
	}

	// 计算总数
	//
	// 2026-10-20 复核轮：原先这里用 strings.Replace 把 SELECT 换成
	// "SELECT COUNT(*)"。那是**字符串级替换**：只要 SELECT 文本与模板有
	// 一个字符不同（换行、空格、字段顺序），替换就静默不生效 ⇒ count 查询
	// 仍返回整行集 ⇒ Scan(&total) 报 "cannot scan ..."。计数与列表因此
	// 共用一段脆弱的文本耦合。改为**显式 count 查询 + 共享 WHERE**。
	countQuery := promptInjectionDetectionsCountFrom + where
	var total int
	if err := h.pool.QueryRow(r.Context(), countQuery, args...).Scan(&total); err != nil {
		writeInternalErrStr(w, "Failed to count detections", err)
		return
	}

	query := selectCols + where
	query += " ORDER BY detected_at DESC LIMIT $" + strconv.Itoa(argCount) + " OFFSET $" + strconv.Itoa(argCount+1)
	args = append(args, pageSize, offset)

	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeInternalErrStr(w, "Failed to list detections", err)
		return
	}
	defer func() { rows.Close() }()

	detections := []PromptInjectionDetection{}
	for rows.Next() {
		d := PromptInjectionDetection{}
		if err := rows.Scan(&d.ID, &d.TenantID, &d.RequestID, &d.SessionKey, &d.DetectedAt,
			&d.DetectionScore, &d.RiskLevel, &d.MatchedRules, &d.MatchedRulesCount,
			&d.ActionTaken, &d.Blocked, &d.EvidenceText, &d.Categories,
			&d.LLMConfidence, &d.LLMReason, &d.CanaryTokenLeaked, &d.ApprovalID,
			&d.ReplacedContent, &d.ClientIP, &d.UserAgent); err != nil {
			writeInternalErrStr(w, "Failed to scan detection", err)
			return
		}
		detections = append(detections, d)
	}
	// 检测记录是安全审计证据链：静默截断会让"这批请求没有注入命中"
	// 成为假结论。上抛，不返回半份 detections。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate detections", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"detections": detections, "page": page, "page_size": pageSize, "total": total,
	})
}

// ==================== 统计 ====================

type DetectionStats struct {
	TotalDetections  int     `json:"total_detections"`
	BlockedCount     int     `json:"blocked_count"`
	CriticalCount    int     `json:"critical_count"`
	HighCount        int     `json:"high_count"`
	MediumCount      int     `json:"medium_count"`
	LowCount         int     `json:"low_count"`
	ApprovalCount    int     `json:"approval_count"`
	ReplacedCount    int     `json:"replaced_count"`
	TerminatedCount  int     `json:"terminated_count"`
	CanaryLeakCount  int     `json:"canary_leak_count"`
	AvgScore         float64 `json:"avg_score"`
	MaxScore         int     `json:"max_score"`
	AvgLLMConf       float64 `json:"avg_llm_confidence"`
	AffectedSessions int     `json:"affected_sessions"`
}

func (h *PromptInjectionHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	tenantID := GetTenantID(r)

	stats := &DetectionStats{}
	// 2026-10-20 复核轮：原先读视图 prompt_injection_stats_enhanced
	//（迁移 364），该视图在任何正典库上都建不出来（见 promptInjectionStatsSQL
	// 的注释）⇒ 42P01 ⇒ 端点 500。改为直接在正典表上聚合。
	err := h.pool.QueryRow(r.Context(), promptInjectionStatsSQL, tenantID).Scan(&stats.TotalDetections, &stats.BlockedCount,
		&stats.CriticalCount, &stats.HighCount, &stats.MediumCount, &stats.LowCount,
		&stats.ApprovalCount, &stats.ReplacedCount, &stats.TerminatedCount,
		&stats.CanaryLeakCount, &stats.AvgScore, &stats.MaxScore,
		&stats.AvgLLMConf, &stats.AffectedSessions)

	if err == pgx.ErrNoRows {
		stats = &DetectionStats{}
	} else if err != nil {
		writeInternalErrStr(w, "Failed to get stats", err)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// ==================== LLM 引擎管理 ====================

type LLMEngine struct {
	ID               int     `json:"id"`
	TenantID         string  `json:"tenant_id"`
	EngineName       string  `json:"engine_name"`
	Description      string  `json:"description"`
	ModelCanonicalID *int    `json:"model_canonical_id"`
	ModelName        string  `json:"model_name"`
	CredentialID     *int    `json:"credential_id"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"max_tokens"`
	TimeoutMs        int     `json:"timeout_ms"`
	MaxRetries       int     `json:"max_retries"`
	SystemPrompt     string  `json:"system_prompt"`
	DetectionPrompt  string  `json:"detection_prompt"`
	Priority         int     `json:"priority"`
	Enabled          bool    `json:"enabled"`
	TotalCalls       int     `json:"total_calls"`
	TotalDetections  int     `json:"total_detections"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	ErrorCount       int     `json:"error_count"`
	LastCalledAt     *string `json:"last_called_at"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func (h *PromptInjectionHandler) handleEngines(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listEngines(w, r)
	case http.MethodPost:
		h.createEngine(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *PromptInjectionHandler) handleEngineSubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/prompt-injection/engines/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing engine ID"})
		return
	}

	engineID := parts[0]

	if len(parts) > 1 && parts[1] == "test" {
		h.testEngine(w, r, engineID)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getEngine(w, r, engineID)
	case http.MethodPut:
		h.updateEngine(w, r, engineID)
	case http.MethodDelete:
		h.deleteEngine(w, r, engineID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *PromptInjectionHandler) listEngines(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)

	rows, err := h.pool.Query(r.Context(),
		`SELECT e.id, e.tenant_id, e.engine_name, COALESCE(e.description, ''),
			e.model_canonical_id, COALESCE(m.canonical_name, ''),
			e.credential_id, e.temperature, e.max_tokens, e.timeout_ms, e.max_retries,
			e.system_prompt, e.detection_prompt, e.priority, e.enabled,
			e.total_calls, e.total_detections, e.avg_latency_ms, e.error_count,
			e.last_called_at, e.created_at, e.updated_at
		FROM prompt_injection_llm_engines e
		LEFT JOIN models_canonical m ON e.model_canonical_id = m.id
		WHERE e.tenant_id = $1 ORDER BY e.priority DESC, e.engine_name`, tenantID)
	if err != nil {
		writeInternalErrStr(w, "Failed to list engines", err)
		return
	}
	defer func() { rows.Close() }()

	engines := []LLMEngine{}
	for rows.Next() {
		e := LLMEngine{}
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EngineName, &e.Description,
			&e.ModelCanonicalID, &e.ModelName, &e.CredentialID,
			&e.Temperature, &e.MaxTokens, &e.TimeoutMs, &e.MaxRetries,
			&e.SystemPrompt, &e.DetectionPrompt, &e.Priority, &e.Enabled,
			&e.TotalCalls, &e.TotalDetections, &e.AvgLatencyMs, &e.ErrorCount,
			&e.LastCalledAt, &e.CreatedAt, &e.UpdatedAt); err != nil {
			writeInternalErrStr(w, "Failed to scan engine", err)
			return
		}
		engines = append(engines, e)
	}
	// 引擎清单少一截 → 优先级排序后派发到哪个 LLM 引擎的结论失真。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate engines", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"engines": engines, "count": len(engines)})
}

func (h *PromptInjectionHandler) getEngine(w http.ResponseWriter, r *http.Request, engineID string) {
	e := &LLMEngine{}
	err := h.pool.QueryRow(r.Context(),
		`SELECT e.id, e.tenant_id, e.engine_name, COALESCE(e.description, ''),
			e.model_canonical_id, COALESCE(m.canonical_name, ''),
			e.credential_id, e.temperature, e.max_tokens, e.timeout_ms, e.max_retries,
			e.system_prompt, e.detection_prompt, e.priority, e.enabled,
			e.total_calls, e.total_detections, e.avg_latency_ms, e.error_count,
			e.last_called_at, e.created_at, e.updated_at
		FROM prompt_injection_llm_engines e
		LEFT JOIN models_canonical m ON e.model_canonical_id = m.id
		WHERE e.id = $1`, engineID,
	).Scan(&e.ID, &e.TenantID, &e.EngineName, &e.Description,
		&e.ModelCanonicalID, &e.ModelName, &e.CredentialID,
		&e.Temperature, &e.MaxTokens, &e.TimeoutMs, &e.MaxRetries,
		&e.SystemPrompt, &e.DetectionPrompt, &e.Priority, &e.Enabled,
		&e.TotalCalls, &e.TotalDetections, &e.AvgLatencyMs, &e.ErrorCount,
		&e.LastCalledAt, &e.CreatedAt, &e.UpdatedAt)

	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Engine not found"})
		return
	}
	if err != nil {
		writeInternalErrStr(w, "Failed to get engine", err)
		return
	}

	writeJSON(w, http.StatusOK, e)
}

func (h *PromptInjectionHandler) createEngine(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	adminUser := authEmail(r)

	var req LLMEngine
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}

	if req.EngineName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "engine_name is required"})
		return
	}

	query := `INSERT INTO prompt_injection_llm_engines (
		tenant_id, engine_name, description, model_canonical_id, credential_id,
		temperature, max_tokens, timeout_ms, max_retries,
		system_prompt, detection_prompt, priority, enabled, created_by
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`

	var engineID int
	err := h.pool.QueryRow(r.Context(), query,
		tenantID, req.EngineName, req.Description, req.ModelCanonicalID, req.CredentialID,
		req.Temperature, req.MaxTokens, req.TimeoutMs, req.MaxRetries,
		req.SystemPrompt, req.DetectionPrompt, req.Priority, req.Enabled, adminUser,
	).Scan(&engineID)

	if err != nil {
		writeInternalErrStr(w, "Failed to create engine", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{"message": "Engine created", "engine_id": engineID})
}

func (h *PromptInjectionHandler) updateEngine(w http.ResponseWriter, r *http.Request, engineID string) {
	var req LLMEngine
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request: " + err.Error()})
		return
	}

	query := `UPDATE prompt_injection_llm_engines SET
		engine_name=$1, description=$2, model_canonical_id=$3, credential_id=$4,
		temperature=$5, max_tokens=$6, timeout_ms=$7, max_retries=$8,
		system_prompt=$9, detection_prompt=$10, priority=$11, enabled=$12
	WHERE id=$13`

	result, err := h.pool.Exec(r.Context(), query,
		req.EngineName, req.Description, req.ModelCanonicalID, req.CredentialID,
		req.Temperature, req.MaxTokens, req.TimeoutMs, req.MaxRetries,
		req.SystemPrompt, req.DetectionPrompt, req.Priority, req.Enabled, engineID,
	)

	if err != nil {
		writeInternalErrStr(w, "Failed to update engine", err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Engine not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Engine updated"})
}

func (h *PromptInjectionHandler) deleteEngine(w http.ResponseWriter, r *http.Request, engineID string) {
	result, err := h.pool.Exec(r.Context(),
		`DELETE FROM prompt_injection_llm_engines WHERE id=$1`, engineID)

	if err != nil {
		writeInternalErrStr(w, "Failed to delete engine", err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Engine not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Engine deleted"})
}

func (h *PromptInjectionHandler) testEngine(w http.ResponseWriter, r *http.Request, engineID string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	var req struct {
		TestInput string `json:"test_input"`
	}
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	// 获取引擎配置
	e := &LLMEngine{}
	err := h.pool.QueryRow(r.Context(),
		`SELECT id, engine_name, model_canonical_id, system_prompt, detection_prompt
		FROM prompt_injection_llm_engines WHERE id=$1`, engineID,
	).Scan(&e.ID, &e.EngineName, &e.ModelCanonicalID, &e.SystemPrompt, &e.DetectionPrompt)

	if err == pgx.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Engine not found"})
		return
	}

	// TODO: 调用 LLM 进行测试
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"engine_id":   e.ID,
		"engine_name": e.EngineName,
		"test_input":  req.TestInput,
		"status":      "pending",
		"message":     "LLM test integration pending",
	})
}

// ==================== 严重等级矩阵 ====================

type SeverityAction struct {
	ID                     int      `json:"id"`
	TenantID               string   `json:"tenant_id"`
	SeverityLevel          string   `json:"severity_level"`
	ObserveAction          string   `json:"observe_action"`
	EnforceAction          string   `json:"enforce_action"`
	RequireApproval        bool     `json:"require_approval"`
	ApprovalTimeoutMinutes int      `json:"approval_timeout_minutes"`
	NotifyOnDetect         bool     `json:"notify_on_detect"`
	NotifyChannels         []string `json:"notify_channels"`
	AffectSessionHealth    bool     `json:"affect_session_health"`
	SessionHealthPenalty   int      `json:"session_health_penalty"`
	TerminateOnRepeat      bool     `json:"terminate_session_on_repeat"`
	RepeatThreshold        int      `json:"repeat_threshold"`
}

func (h *PromptInjectionHandler) handleSeverityMatrix(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)

	switch r.Method {
	case http.MethodGet:
		rows, err := h.pool.Query(r.Context(),
			`SELECT id, tenant_id, severity_level, observe_action::text, enforce_action::text,
				require_approval, approval_timeout_minutes,
				notify_on_detect, COALESCE(notify_channels, '[]'),
				affect_session_health, session_health_penalty,
				terminate_session_on_repeat, repeat_threshold
			FROM severity_action_matrix WHERE tenant_id = $1 ORDER BY
			CASE severity_level WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'critical' THEN 4 END`,
			tenantID)
		if err != nil {
			writeInternalErrStr(w, "Failed to get matrix", err)
			return
		}
		defer func() { rows.Close() }()

		matrix := []SeverityAction{}
		for rows.Next() {
			s := SeverityAction{}
			var channelsJSON string
			if err := rows.Scan(&s.ID, &s.TenantID, &s.SeverityLevel, &s.ObserveAction, &s.EnforceAction,
				&s.RequireApproval, &s.ApprovalTimeoutMinutes,
				&s.NotifyOnDetect, &channelsJSON,
				&s.AffectSessionHealth, &s.SessionHealthPenalty,
				&s.TerminateOnRepeat, &s.RepeatThreshold); err != nil {
				writeInternalErrStr(w, "Failed to scan", err)
				return
			}
			jsoncol.Decode("admin.promptInjection.severityMatrix/notify_channels", []byte(channelsJSON), &s.NotifyChannels)
			matrix = append(matrix, s)
		}
		// 处置矩阵缺行 = 某个 severity 没有动作 = 该级别命中后"不处置"。
		if err := rows.Err(); err != nil {
			writeInternalErrStr(w, "Failed to iterate severity matrix", err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"matrix": matrix})

	case http.MethodPut:
		var req []SeverityAction
		if err := readJSONRequired(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}

		for _, s := range req {
			channelsJSON, _ := json.Marshal(s.NotifyChannels)
			_, err := h.pool.Exec(r.Context(),
				`UPDATE severity_action_matrix SET
					observe_action=$1, enforce_action=$2, require_approval=$3,
					approval_timeout_minutes=$4, notify_on_detect=$5, notify_channels=$6::jsonb,
					affect_session_health=$7, session_health_penalty=$8,
					terminate_session_on_repeat=$9, repeat_threshold=$10
				WHERE tenant_id=$11 AND severity_level=$12`,
				s.ObserveAction, s.EnforceAction, s.RequireApproval,
				s.ApprovalTimeoutMinutes, s.NotifyOnDetect, string(channelsJSON),
				s.AffectSessionHealth, s.SessionHealthPenalty,
				s.TerminateOnRepeat, s.RepeatThreshold,
				tenantID, s.SeverityLevel)
			if err != nil {
				writeInternalErrStr(w, "Failed to update", err)
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"message": "Severity matrix updated"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

// ==================== Canary Token 管理 ====================

type CanaryToken struct {
	ID            int     `json:"id"`
	TenantID      string  `json:"tenant_id"`
	TokenValue    string  `json:"token_value"`
	TokenType     string  `json:"token_type"`
	TokenName     string  `json:"token_name"`
	Description   string  `json:"description"`
	LeakAction    string  `json:"leak_action"`
	NotifyOnLeak  bool    `json:"notify_on_leak"`
	Active        bool    `json:"active"`
	ExpiresAt     *string `json:"expires_at"`
	TimesInjected int     `json:"times_injected"`
	TimesLeaked   int     `json:"times_leaked"`
	LastLeakedAt  *string `json:"last_leaked_at"`
	CreatedAt     string  `json:"created_at"`
}

func (h *PromptInjectionHandler) handleCanaryTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listCanaryTokens(w, r)
	case http.MethodPost:
		h.createCanaryToken(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *PromptInjectionHandler) handleCanaryTokenSubrouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/prompt-injection/canary-tokens/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing token ID"})
		return
	}

	tokenID := parts[0]

	switch r.Method {
	case http.MethodDelete:
		h.deleteCanaryToken(w, r, tokenID)
	case http.MethodPut:
		h.updateCanaryToken(w, r, tokenID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *PromptInjectionHandler) listCanaryTokens(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)

	rows, err := h.pool.Query(r.Context(),
		`SELECT id, tenant_id, token_value, token_type, COALESCE(token_name, ''),
			COALESCE(description, ''), leak_action::text, notify_on_leak,
			active, expires_at, times_injected, times_leaked, last_leaked_at, created_at
		FROM canary_tokens WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		writeInternalErrStr(w, "Failed to list tokens", err)
		return
	}
	defer func() { rows.Close() }()

	tokens := []CanaryToken{}
	for rows.Next() {
		t := CanaryToken{}
		if err := rows.Scan(&t.ID, &t.TenantID, &t.TokenValue, &t.TokenType, &t.TokenName,
			&t.Description, &t.LeakAction, &t.NotifyOnLeak,
			&t.Active, &t.ExpiresAt, &t.TimesInjected, &t.TimesLeaked, &t.LastLeakedAt, &t.CreatedAt); err != nil {
			writeInternalErrStr(w, "Failed to scan token", err)
			return
		}
		tokens = append(tokens, t)
	}
	// 蜜罐 token 清单少一截 = 泄露探针少一批 = 泄露事件被静默漏报。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate tokens", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"tokens": tokens, "count": len(tokens)})
}

func (h *PromptInjectionHandler) createCanaryToken(w http.ResponseWriter, r *http.Request) {
	tenantID := GetTenantID(r)
	adminUser := authEmail(r)

	var req CanaryToken
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	// 自动生成 token value
	tokenValue := req.TokenValue
	if tokenValue == "" {
		tokenValue = fmt.Sprintf("canary-%x", sha256.Sum256([]byte(fmt.Sprintf("%s-%d-%s", tenantID, time.Now().UnixNano(), adminUser))))
	}

	query := `INSERT INTO canary_tokens (tenant_id, token_value, token_type, token_name, description, leak_action, notify_on_leak, active, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`

	var tokenID int
	err := h.pool.QueryRow(r.Context(), query,
		tenantID, tokenValue, req.TokenType, req.TokenName, req.Description,
		req.LeakAction, req.NotifyOnLeak, req.Active, req.ExpiresAt, adminUser,
	).Scan(&tokenID)

	if err != nil {
		writeInternalErrStr(w, "Failed to create token", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{"message": "Token created", "token_id": tokenID, "token_value": tokenValue})
}

func (h *PromptInjectionHandler) updateCanaryToken(w http.ResponseWriter, r *http.Request, tokenID string) {
	var req CanaryToken
	if err := readJSONRequired(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	_, err := h.pool.Exec(r.Context(),
		`UPDATE canary_tokens SET token_name=$1, description=$2, leak_action=$3, notify_on_leak=$4, active=$5, expires_at=$6 WHERE id=$7`,
		req.TokenName, req.Description, req.LeakAction, req.NotifyOnLeak, req.Active, req.ExpiresAt, tokenID)

	if err != nil {
		writeInternalErrStr(w, "Failed to update token", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Token updated"})
}

func (h *PromptInjectionHandler) deleteCanaryToken(w http.ResponseWriter, r *http.Request, tokenID string) {
	result, err := h.pool.Exec(r.Context(), `DELETE FROM canary_tokens WHERE id=$1`, tokenID)
	if err != nil {
		writeInternalErrStr(w, "Failed to delete token", err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Token not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Token deleted"})
}

// 审批相关功能已迁移到现有审批系统 (/api/admin/session-approvals)
// 详见 domains/sessionaudit/approval_manager.go

// ==================== 攻击向量库 ====================

type AttackVector struct {
	ID         int      `json:"id"`
	TenantID   string   `json:"tenant_id"`
	AttackText string   `json:"attack_text"`
	AttackHash string   `json:"attack_hash"`
	Categories []string `json:"categories"`
	Severity   int      `json:"severity"`
	Source     string   `json:"source"`
	RequestID  string   `json:"request_id"`
	DetectedAt *string  `json:"detected_at"`
	CreatedAt  string   `json:"created_at"`
}

func (h *PromptInjectionHandler) handleAttackVectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}

	tenantID := GetTenantID(r)
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

	rows, err := h.pool.Query(r.Context(),
		`SELECT id, tenant_id, attack_text, attack_hash,
			COALESCE(categories::text[], '{}'), COALESCE(severity, 0),
			source, COALESCE(request_id, ''), detected_at, created_at
		FROM injection_attack_vectors WHERE tenant_id = $1
		ORDER BY severity DESC, created_at DESC LIMIT $2 OFFSET $3`,
		tenantID, pageSize, offset)
	if err != nil {
		writeInternalErrStr(w, "Failed to list vectors", err)
		return
	}
	defer func() { rows.Close() }()

	vectors := []AttackVector{}
	for rows.Next() {
		v := AttackVector{}
		if err := rows.Scan(&v.ID, &v.TenantID, &v.AttackText, &v.AttackHash,
			&v.Categories, &v.Severity, &v.Source, &v.RequestID, &v.DetectedAt, &v.CreatedAt); err != nil {
			writeInternalErrStr(w, "Failed to scan", err)
			return
		}
		vectors = append(vectors, v)
	}
	// 攻击向量清单是威胁情报读面，截断即"没有更多攻击手法"的假结论。
	if err := rows.Err(); err != nil {
		writeInternalErrStr(w, "Failed to iterate attack vectors", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"vectors": vectors, "page": page, "page_size": pageSize})
}

// ==================== 辅助函数 ====================

// authEmail 返回当前请求的管理员标识（从 AuthContext 提取）。
// AdminMiddleware 已注入 AuthContext；若不存在则回退到 header / "system"。
func authEmail(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil && auth.Username != "" {
		return auth.Username
	}
	if u := r.Header.Get("X-User-Email"); u != "" {
		return u
	}
	return "system"
}
