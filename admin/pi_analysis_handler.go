package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domains/promptinjection"
)

// PIAnalysisHandler PI 分析管理接口
type PIAnalysisHandler struct {
	collector *promptinjection.ErrorCollector
	analyzer  *promptinjection.ErrorAnalyzer
	db        *sql.DB

	// ★ 2026-11-17 审计轮 P0：鉴权材料。
	//
	//	这几个端点原先**完全没有任何鉴权** ⇒ 任何人拿到公网入口就能
	//	① 读走错误队列（内部错误消息 + 租户/会话/请求 id）、
	//	② **POST /trigger** 触发 LLM 分析 ⇒ 免费的算力放大器 + DoS。
	//
	//	★ 为什么「挂��� /api/admin/ 前缀」并不能自动获得鉴权：
	//	  main.go 用 `mux.Handle("/api/admin/", e)` 把 **Echo 实例**挂在
	//	  该前缀上（Echo 侧带 admin JWT 中间件），但 Go 的 ServeMux
	//	  按**最长模式**派发 ⇒ `/api/admin/pi-analysis/queue` 这条
	//	  **精确路由更长** ⇒ 请求**不会**进 Echo ⇒ **绕过 JWT 中间件**。
	//	⇒ 必须显式用 AdminMiddleware 包住。
	pool   *pgxpool.Pool
	secret string

	// prefix 是本 handler 注册时用的路由前缀（RegisterRoutes 写入）。
	prefix string

	// detectionChainWired 报告**检测链是否接线**（即是否存在生产者）。
	//
	// ★ 2026-11-18 审计轮 P1：PI 错误采集的生产者原本**只有**
	//	promptinjection.Detector（d.collectError），而检测器只在
	//	LLM_GATEWAY_USE_V2_PIPELINE 开启时才被构造（默认关）
	//	⇒ 默认形态下采集队列**恒为空**，且「空」与「没有生产者」
	//	在运行期**不可区分**（运维会读成「没有错误发生过」）。
	//	⇒ 把这条链路状态暴露到 /stats，让管理面能如实区分。
	//
	// ★ 2026-11-20 PI 接入轮：生产者不再只有检测器——输出守卫格式类拦截
	//	（output_guard_blocked，经 streaming.SetOutputGuardPISink 注入的
	//	投递口）也会入队，且这条源**不依赖 v2 pipeline**（装配在
	//	piErrorCollectorGlobal 初始化处）。chain_status 只反映检测器接线，
	//	不代表队列无生产者；队列详情/统计按 error_type 如实展示。
	detectionChainWired bool

	// ruleRefresher 在「应用修复」改了 prompt_injection_rules 之后被调用，
	// 让在役 detector 的内存规则缓存立即刷新。
	//
	// ★ 2026-11-18 PI 分析管理面优化轮：detector 把规则缓存在内存里
	//	（basicRules/advancedRules），改库不刷新 ⇒「修了 ≠ 生效」。
	//	promptinjection.Detector.RefreshRules 此前**零生产调用方**（死面）
	//	⇒ 本字段是它的第一个接线点（由 cmd/gateway 装配侧注入）。
	ruleRefresher func() error

	// ★ 2026-11-25 取证轮：取证采集面 + 复测用的在线检测器（见 pi_analysis_forensics.go）。
	//   两者都由装配侧注入；nil ⇒ 对应端点如实回 503 / 对应复测项如实报不可用。
	evidence    *PIEvidenceGatherer
	replayJudge *promptinjection.Detector

	// 复测的两个调用面：默认指向 promptinjection 的实现；单测注入桩
	// （规则重放要读十来张表，用 sqlmock 逐列钉只会钉住实现细节而不是聚合规则）。
	replayRulesFn  piReplayRulesFunc
	recheckLayerFn piRecheckLayerFunc
}

type (
	piReplayRulesFunc  func(ctx context.Context, db *sql.DB, tenantID, input string) (*promptinjection.RulesReplayResult, error)
	piRecheckLayerFunc func(ctx context.Context, db *sql.DB, errorType, tenantID, input string, opts promptinjection.RecheckOptions) (*promptinjection.LayerRecheckResult, error)
)

func (h *PIAnalysisHandler) replayRules(ctx context.Context, db *sql.DB, tenantID, input string) (*promptinjection.RulesReplayResult, error) {
	if h.replayRulesFn != nil {
		return h.replayRulesFn(ctx, db, tenantID, input)
	}
	return promptinjection.ReplayRules(ctx, db, tenantID, input)
}

func (h *PIAnalysisHandler) recheckLayer(ctx context.Context, db *sql.DB, errorType, tenantID, input string, opts promptinjection.RecheckOptions) (*promptinjection.LayerRecheckResult, error) {
	if h.recheckLayerFn != nil {
		return h.recheckLayerFn(ctx, db, errorType, tenantID, input, opts)
	}
	return promptinjection.RecheckLayer(ctx, db, errorType, tenantID, input, opts)
}

// SetRuleRefresher 注入规则热刷新回调（由装配侧在构造出 detector 后调用）。
func (h *PIAnalysisHandler) SetRuleRefresher(fn func() error) {
	if h == nil {
		return
	}
	h.ruleRefresher = fn
}

// SetDetectionChainWired 记录检测链是否接线（由装配侧注入）。
func (h *PIAnalysisHandler) SetDetectionChainWired(wired bool) {
	if h == nil {
		return
	}
	h.detectionChainWired = wired
}

// NewPIAnalysisHandler 创建处理器
//
// ★ 2026-11-17 审计轮 P0：pool / secret 是**鉴权必需**（不是可选）——
//
//	少了它们，RegisterRoutes 会注册**裸 handler**（无鉴权）。
//	⇒ 这里不做「nil ⇒ 不鉴权」的降级：**鉴权缺失即拒绝注册**。
func NewPIAnalysisHandler(
	collector *promptinjection.ErrorCollector,
	analyzer *promptinjection.ErrorAnalyzer,
	db *sql.DB,
	pool *pgxpool.Pool,
	secret string,
) *PIAnalysisHandler {
	return &PIAnalysisHandler{
		collector: collector,
		analyzer:  analyzer,
		db:        db,
		pool:      pool,
		secret:    secret,
	}
}

// RegisterRoutes 注册路由
//
// ★ 2026-11-17 审计轮 P0：全部端点经 AdminMiddleware 鉴权。
//
//	★ 鉴权缺失 ⇒ 不注册（fail-closed）：宁可端点 404，
//	  也不能让一个「忘了传 secret」的部署把内部错误队列敞在公网上。
func (h *PIAnalysisHandler) RegisterRoutes(mux *http.ServeMux, prefix string) {
	if h.pool == nil || strings.TrimSpace(h.secret) == "" {
		// 无鉴权材料 ⇒ **拒绝注册**。
		slog.Error("pi_analysis: admin auth material missing; endpoints NOT registered",
			"prefix", prefix, "has_pool", h.pool != nil, "has_secret", strings.TrimSpace(h.secret) != "")
		return
	}
	h.prefix = prefix
	wrap := func(fn http.HandlerFunc) http.HandlerFunc {
		return AdminMiddleware(fn, h.pool, h.secret)
	}
	mux.HandleFunc(prefix+"/queue", wrap(h.GetQueue))
	// ★ 2026-11-18 PI 分析管理面优化轮：`/queue/{id}` 单条详情（前缀模式）。
	//	详情页需要 queue 行的**全量上下文**（matched_rules / detection_layers /
	//	policy_config / error_details / 时间线），列表查询不取这些列。
	mux.HandleFunc(prefix+"/queue/", wrap(h.handleQueueItem))
	mux.HandleFunc(prefix+"/results", wrap(h.handleResults))
	// ★ 2026-11-17 审计轮 P1：详情路由 `/results/{queue_id}` **原先从未注册**。
	//
	//	原实现只注册了 `prefix+"/results"`（**精确**模式）⇒
	//	Go 的 ServeMux 对 `/results/1` **不匹配**它（精确 ≠ 前缀）
	//	⇒ 请求落到更长的前缀路由（或 404）
	//	⇒ handleResults 里那段「解析 {queue_id}」的代码是**死代码**
	//	⇒ 前端页面的「查看详情」按钮永远打不开。
	//	⇒ 补注册 `prefix+"/results/"`（**前缀**模式）⇒
	//	  `/results` 仍由精确模式命中（列表），`/results/{id}` 由前缀模式命中（详情）。
	mux.HandleFunc(prefix+"/results/", wrap(h.handleResults))
	// ★ 2026-11-25 取证轮：批量重读「解析失败」的存量结果。必须注册成**精确**模式——
	//	它落在 /results/ 前缀路由里，不单独注册会被当成 queue_id="reparse-failed"。
	mux.HandleFunc(prefix+"/results/reparse-failed", wrap(h.handleReparseFailed))
	mux.HandleFunc(prefix+"/trigger", wrap(h.TriggerAnalysis))
	mux.HandleFunc(prefix+"/stats", wrap(h.GetStats))
}

// handleQueueItem 派发 /queue/{id}（单条队列详情）及其子动作。
func (h *PIAnalysisHandler) handleQueueItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.prefix+"/queue")
	rest := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(rest, "/", 2)
	queueID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid queue_id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 {
		h.GetQueueItem(w, r, queueID)
		return
	}
	// ★ 2026-11-25 取证轮：详情之下的三个子动作（见 pi_analysis_forensics.go）。
	//	★ 2026-11-26：evidence 是读；analyze / verify 会花模型调用、往队列行写留痕
	//	⇒ 写动作统一过超级管理员闸（见 piRequireSuperAdminForWrite）。
	switch parts[1] {
	case "evidence":
		h.handleQueueEvidence(w, r, queueID)
	case "analyze":
		if piRequireSuperAdminForWrite(w, r) {
			return
		}
		h.handleQueueAnalyze(w, r, queueID)
	case "verify":
		if piRequireSuperAdminForWrite(w, r) {
			return
		}
		h.handleQueueVerify(w, r, queueID)
	default:
		http.Error(w, "unknown sub-action", http.StatusNotFound)
	}
}

func (h *PIAnalysisHandler) handleResults(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.resultsPathPrefix())
	if path == "" || path == "/" {
		h.GetResults(w, r)
		return
	}

	// 处理 /results/{queue_id} 与 /results/{queue_id}/apply|revert
	rest := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(rest, "/", 2)
	queueID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid queue_id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 {
		h.GetResult(w, r, queueID)
		return
	}
	// ★ 2026-11-18 PI 分析管理面优化轮：修复应用/回滚子动作。
	//	★ 2026-11-26：apply / revert / reparse 都是写动作 ⇒ 统一过超级管理员闸
	//	（见 piRequireSuperAdminForWrite）。apply / revert 会改**全平台**的 PI 规则。
	switch parts[1] {
	case "apply":
		if piRequireSuperAdminForWrite(w, r) {
			return
		}
		h.ApplyFix(w, r, queueID)
	case "revert":
		if piRequireSuperAdminForWrite(w, r) {
			return
		}
		h.RevertFix(w, r, queueID)
	// ★ 2026-11-25 取证轮：用当前解析器重读这条结果的原始响应（不调模型）。
	case "reparse":
		if piRequireSuperAdminForWrite(w, r) {
			return
		}
		h.handleResultReparse(w, r, queueID)
	default:
		http.Error(w, "unknown sub-action", http.StatusNotFound)
	}
}

// resultsPathPrefix 返回结果列表路由的路径前缀。
//
// ★ 原实现把 "/api/admin/pi-analysis/results" **写死**在 handleResults 里，
//
//	而 RegisterRoutes 的 prefix 是**参数** ⇒ 两者可以不一致
//	⇒ 改前缀时详情路由会静默失配（/results/123 会被当成列表）。
//	⇒ 现在从实际注册的 prefix 推导。
func (h *PIAnalysisHandler) resultsPathPrefix() string {
	return h.prefix + "/results"
}

// GetQueue 获取错误队列
func (h *PIAnalysisHandler) GetQueue(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}

	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// 构建查询
	query := `
		SELECT 
			id, error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			error_message, status, retry_count, created_at
		FROM error_analysis_queue
		WHERE 1=1
	`
	args := []interface{}{}
	argPos := 1

	// ★ 2026-11-26 决策 1：租户过滤 ——
	//   super_admin 看全平台数据（监控用），tenant_admin 只看自己租户的数据（安全/隐私）。
	//   input_text 包含用户提示词，跨租户可见属于数据泄漏。
	tenantFilter := EffectiveTenantIDAll(r)
	if tenantFilter != "" {
		query += fmt.Sprintf(" AND tenant_id = $%d", argPos)
		args = append(args, tenantFilter)
		argPos++
	}

	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argPos)
		args = append(args, status)
		argPos++
	}

	query += " ORDER BY priority DESC, created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argPos, argPos+1)
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeInternalTextErr(w, "query error analysis queue failed", err)
		return
	}
	defer rows.Close()

	var errors []map[string]interface{}
	for rows.Next() {
		var e struct {
			ID         int64        `json:"id"`
			ErrorType  string       `json:"error_type"`
			ErrorHash  string       `json:"error_hash"`
			Priority   int          `json:"priority"`
			TenantID   string       `json:"tenant_id"`
			SessionID  PiNullString `json:"session_id"`
			RequestID  PiNullString `json:"request_id"`
			ErrorMsg   string
			Status     string `json:"status"`
			RetryCount int    `json:"retry_count"`
			CreatedAt  string `json:"created_at"`
		}

		if err := rows.Scan(
			&e.ID, &e.ErrorType, &e.ErrorHash, &e.Priority,
			&e.TenantID, &e.SessionID, &e.RequestID,
			&e.ErrorMsg, &e.Status, &e.RetryCount, &e.CreatedAt,
		); err != nil {
			continue
		}

		errors = append(errors, map[string]interface{}{
			"id":            e.ID,
			"error_type":    e.ErrorType,
			"error_hash":    e.ErrorHash,
			"priority":      e.Priority,
			"tenant_id":     e.TenantID,
			"session_id":    e.SessionID.String,
			"request_id":    e.RequestID.String,
			"error_message": e.ErrorMsg,
			"status":        e.Status,
			"retry_count":   e.RetryCount,
			"created_at":    e.CreatedAt,
		})
	}
	// ★ 2026-11-18 审计轮：rows 循环的**终检**（internal/rowsguard 门）。
	//   rows.Next() 返回 false 有两种原因：正常读完，或**中途出错**。
	//   不查 rows.Err() ⇒ 连接中断时会把「读了一半」的队列当成「队列就这些」
	//   返回 ⇒ 管理面给出**静默截断**的结果（且此后无任何告警）。
	if err := rows.Err(); err != nil {
		writeInternalTextErr(w, "iterate error analysis queue failed", err)
		return
	}

	// 获取总数（同样需要租户过滤）
	countQuery := `SELECT COUNT(*) FROM error_analysis_queue WHERE 1=1`
	countArgs := []interface{}{}
	countPos := 1

	if tenantFilter != "" {
		countQuery += fmt.Sprintf(" AND tenant_id = $%d", countPos)
		countArgs = append(countArgs, tenantFilter)
		countPos++
	}

	if status != "" {
		countQuery += fmt.Sprintf(" AND status = $%d", countPos)
		countArgs = append(countArgs, status)
	}

	var total int64
	h.db.QueryRowContext(r.Context(), countQuery, countArgs...).Scan(&total)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"errors": errors,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetResults 获取分析结果列表
func (h *PIAnalysisHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 200 {
			limit = l
		}
	}

	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// ★ 2026-11-26 决策 1：租户过滤（与 GetQueue 同理）。
	tenantFilter := EffectiveTenantIDAll(r)
	query := `
		SELECT 
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.severity_level, r.suggested_fix,
			q.error_type, q.tenant_id, r.created_at
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
	`
	args := []interface{}{}
	argPos := 1

	if tenantFilter != "" {
		query += fmt.Sprintf(" WHERE q.tenant_id = $%d", argPos)
		args = append(args, tenantFilter)
		argPos++
	}

	query += " ORDER BY r.created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argPos, argPos+1)
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeInternalTextErr(w, "query error analysis results failed", err)
		return
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var r struct {
			ID              int64   `json:"id"`
			AnalysisID      string  `json:"analysis_id"`
			QueueID         int64   `json:"queue_id"`
			RootCause       string  `json:"root_cause"`
			ErrorCategory   string  `json:"error_category"`
			IsFalsePositive bool    `json:"is_false_positive"`
			ConfidenceScore float64 `json:"confidence_score"`
			SeverityLevel   string  `json:"severity_level"`
			SuggestedFix    string  `json:"suggested_fix"`
			ErrorType       string  `json:"error_type"`
			TenantID        string  `json:"tenant_id"`
			CreatedAt       string  `json:"created_at"`
		}

		if err := rows.Scan(
			&r.ID, &r.AnalysisID, &r.QueueID,
			&r.RootCause, &r.ErrorCategory, &r.IsFalsePositive,
			&r.ConfidenceScore, &r.SeverityLevel, &r.SuggestedFix,
			&r.ErrorType, &r.TenantID, &r.CreatedAt,
		); err != nil {
			continue
		}

		results = append(results, map[string]interface{}{
			"id":                r.ID,
			"analysis_id":       r.AnalysisID,
			"queue_id":          r.QueueID,
			"root_cause":        r.RootCause,
			"error_category":    r.ErrorCategory,
			"is_false_positive": r.IsFalsePositive,
			"confidence_score":  r.ConfidenceScore,
			"severity_level":    r.SeverityLevel,
			"suggested_fix":     r.SuggestedFix,
			"error_type":        r.ErrorType,
			"tenant_id":         r.TenantID,
			"created_at":        r.CreatedAt,
		})
	}
	// ★ 2026-11-18 审计轮：rows 循环的**终检**（internal/rowsguard 门）。
	//   不查 rows.Err() ⇒ 连接中断时把「读了一半」的结果当成完整结果返回。
	if err := rows.Err(); err != nil {
		writeInternalTextErr(w, "iterate error analysis results failed", err)
		return
	}

	// 获取总数（同样需要租户过滤）
	var total int64
	countQuery := `
		SELECT COUNT(*) 
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
	`
	if tenantFilter != "" {
		countQuery += " WHERE q.tenant_id = $1"
		h.db.QueryRowContext(r.Context(), countQuery, tenantFilter).Scan(&total)
	} else {
		h.db.QueryRowContext(r.Context(), countQuery).Scan(&total)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// GetResult 获取单个分析结果详情。
//
// ★ 2026-11-18 PI 分析管理面优化轮：补全「前因后果」的全量材料 ——
//
//	原查询只取 22 列（分析侧 + 4 列队列上下文），详情页要讲的
//	「前因」（命中了什么规则 / 走了哪些检测层 / 当时的策略快照 /
//	会话与请求归属 / 时间线）一概没取 ⇒ 前端只能展示半张图。
//	现在取全：queue 侧 14 列（含 error_details / matched_rules /
//	detection_layers / policy_config / started_at / completed_at /
//	session_id / request_id / input_hash / priority / retry_count），
//	results 侧补 analyzer_model / analyzer_version / analysis_details /
//	llm_response_raw / auto_applied / applied_at / applied_by /
//	applied_actions（迁移 979 的留痕列，缺席时为 null——
//	迁移未上线的库上详情仍可读，只是没有留痕）。
//
// ★ 2026-11-26 决策 1：租户过滤 —— 结果详情同样需要租户校验。
func (h *PIAnalysisHandler) GetResult(w http.ResponseWriter, r *http.Request, queueID int64) {
	tenantFilter := EffectiveTenantIDAll(r)
	query := `
		SELECT
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.suggested_fix,
			r.rule_adjustment, r.code_changes,
			r.severity_level, r.impact_scope, r.affected_users_estimate,
			r.similar_cases, r.pattern_frequency,
			r.analysis_duration_ms, r.llm_tokens_used,
			r.created_at,
			r.analyzer_model, r.analyzer_version,
			r.analysis_details, r.llm_response_raw,
			r.auto_applied, r.applied_at, r.applied_by, r.applied_actions,
			q.error_type, q.error_hash, q.priority,
			q.tenant_id, q.session_id, q.request_id,
			q.input_text, q.input_hash, q.error_message, q.error_details,
			q.detection_layers, q.matched_rules, q.policy_config,
			q.status, q.retry_count,
			q.created_at, q.started_at, q.completed_at
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		WHERE r.queue_id = $1
	`
	args := []interface{}{queueID}

	if tenantFilter != "" {
		query += " AND q.tenant_id = $2"
		args = append(args, tenantFilter)
	}

	var result struct {
		ID                    int64        `json:"id"`
		AnalysisID            string       `json:"analysis_id"`
		QueueID               int64        `json:"queue_id"`
		RootCause             string       `json:"root_cause"`
		ErrorCategory         string       `json:"error_category"`
		IsFalsePositive       bool         `json:"is_false_positive"`
		ConfidenceScore       float64      `json:"confidence_score"`
		SuggestedFix          string       `json:"suggested_fix"`
		RuleAdjustment        NullableJSON `json:"rule_adjustment"`
		CodeChanges           NullableJSON `json:"code_changes"`
		SeverityLevel         string       `json:"severity_level"`
		ImpactScope           string       `json:"impact_scope"`
		AffectedUsersEstimate int          `json:"affected_users_estimate"`
		SimilarCases          NullableJSON `json:"similar_cases"`
		PatternFrequency      int          `json:"pattern_frequency"`
		AnalysisDurationMs    int          `json:"analysis_duration_ms"`
		LLMTokensUsed         int          `json:"llm_tokens_used"`
		CreatedAt             string       `json:"created_at"`
		AnalyzerModel         string       `json:"analyzer_model"`
		AnalyzerVersion       PiNullString `json:"analyzer_version"`
		AnalysisDetails       NullableJSON `json:"analysis_details"`
		LLMResponseRaw        PiNullString `json:"llm_response_raw"`
		AutoApplied           bool         `json:"auto_applied"`
		AppliedAt             PiNullString `json:"applied_at"`
		AppliedBy             PiNullString `json:"applied_by"`
		AppliedActions        NullableJSON `json:"applied_actions"`
		ErrorType             string       `json:"error_type"`
		ErrorHash             string       `json:"error_hash"`
		Priority              int          `json:"priority"`
		TenantID              string       `json:"tenant_id"`
		SessionID             PiNullString `json:"session_id"`
		RequestID             PiNullString `json:"request_id"`
		InputText             PiNullString `json:"input_text"`
		InputHash             PiNullString `json:"input_hash"`
		ErrorMessage          string       `json:"error_message"`
		ErrorDetails          NullableJSON `json:"error_details"`
		DetectionLayers       NullableJSON `json:"detection_layers"`
		MatchedRules          NullableJSON `json:"matched_rules"`
		PolicyConfig          NullableJSON `json:"policy_config"`
		Status                string       `json:"status"`
		RetryCount            int          `json:"retry_count"`
		QCreatedAt            string       `json:"queued_at"`
		StartedAt             PiNullString `json:"started_at"`
		CompletedAt           PiNullString `json:"completed_at"`
	}

	err := h.db.QueryRowContext(r.Context(), query, args...).Scan(
		&result.ID, &result.AnalysisID, &result.QueueID,
		&result.RootCause, &result.ErrorCategory, &result.IsFalsePositive,
		&result.ConfidenceScore, &result.SuggestedFix,
		&result.RuleAdjustment, &result.CodeChanges,
		&result.SeverityLevel, &result.ImpactScope, &result.AffectedUsersEstimate,
		&result.SimilarCases, &result.PatternFrequency,
		&result.AnalysisDurationMs, &result.LLMTokensUsed,
		&result.CreatedAt,
		&result.AnalyzerModel, &result.AnalyzerVersion,
		&result.AnalysisDetails, &result.LLMResponseRaw,
		&result.AutoApplied, &result.AppliedAt, &result.AppliedBy, &result.AppliedActions,
		&result.ErrorType, &result.ErrorHash, &result.Priority,
		&result.TenantID, &result.SessionID, &result.RequestID,
		&result.InputText, &result.InputHash, &result.ErrorMessage, &result.ErrorDetails,
		&result.DetectionLayers, &result.MatchedRules, &result.PolicyConfig,
		&result.Status, &result.RetryCount,
		&result.QCreatedAt, &result.StartedAt, &result.CompletedAt,
	)

	if err == sql.ErrNoRows {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	if err != nil {
		// ★ 迁移 979 未上线的库：applied_actions 列不存在（42703）⇒
		//   降级成**不含留痕列**的旧投影重查一次，而不是让详情页 500。
		//   （与 promptinjection 的 42703 降级读面同款纪律。）
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "42703") {
			h.getResultLegacy(w, r, queueID)
			return
		}
		writeInternalTextErr(w, "query analysis result failed", err)
		return
	}

	// ★ 2026-11-25 取证轮：附上「修正方案视图」（能不能自动修正、为什么不能）——
	//   由后端单点判定，按钮状态与 ApplyFix 的闸门同源。
	h.writeResultWithPlan(w, result)
}

// getResultLegacy 是 applied_actions 列缺席（迁移 979 未应用）时的降级详情。
//
// ★ 2026-11-26 决策 1：租户过滤 —— 降级路径同样需要租户校验。
func (h *PIAnalysisHandler) getResultLegacy(w http.ResponseWriter, r *http.Request, queueID int64) {
	tenantFilter := EffectiveTenantIDAll(r)
	query := `
		SELECT
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.suggested_fix,
			r.rule_adjustment, r.code_changes,
			r.severity_level, r.impact_scope, r.affected_users_estimate,
			r.similar_cases, r.pattern_frequency,
			r.analysis_duration_ms, r.llm_tokens_used,
			r.created_at,
			r.analyzer_model, r.analyzer_version,
			r.analysis_details, r.llm_response_raw,
			r.auto_applied, r.applied_at, r.applied_by,
			q.error_type, q.error_hash, q.priority,
			q.tenant_id, q.session_id, q.request_id,
			q.input_text, q.input_hash, q.error_message, q.error_details,
			q.detection_layers, q.matched_rules, q.policy_config,
			q.status, q.retry_count,
			q.created_at, q.started_at, q.completed_at
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		WHERE r.queue_id = $1
	`
	args := []interface{}{queueID}

	if tenantFilter != "" {
		query += " AND q.tenant_id = $2"
		args = append(args, tenantFilter)
	}

	var result struct {
		ID                    int64        `json:"id"`
		AnalysisID            string       `json:"analysis_id"`
		QueueID               int64        `json:"queue_id"`
		RootCause             string       `json:"root_cause"`
		ErrorCategory         string       `json:"error_category"`
		IsFalsePositive       bool         `json:"is_false_positive"`
		ConfidenceScore       float64      `json:"confidence_score"`
		SuggestedFix          string       `json:"suggested_fix"`
		RuleAdjustment        NullableJSON `json:"rule_adjustment"`
		CodeChanges           NullableJSON `json:"code_changes"`
		SeverityLevel         string       `json:"severity_level"`
		ImpactScope           string       `json:"impact_scope"`
		AffectedUsersEstimate int          `json:"affected_users_estimate"`
		SimilarCases          NullableJSON `json:"similar_cases"`
		PatternFrequency      int          `json:"pattern_frequency"`
		AnalysisDurationMs    int          `json:"analysis_duration_ms"`
		LLMTokensUsed         int          `json:"llm_tokens_used"`
		CreatedAt             string       `json:"created_at"`
		AnalyzerModel         string       `json:"analyzer_model"`
		AnalyzerVersion       PiNullString `json:"analyzer_version"`
		AnalysisDetails       NullableJSON `json:"analysis_details"`
		LLMResponseRaw        PiNullString `json:"llm_response_raw"`
		AutoApplied           bool         `json:"auto_applied"`
		AppliedAt             PiNullString `json:"applied_at"`
		AppliedBy             PiNullString `json:"applied_by"`
		ErrorType             string       `json:"error_type"`
		ErrorHash             string       `json:"error_hash"`
		Priority              int          `json:"priority"`
		TenantID              string       `json:"tenant_id"`
		SessionID             PiNullString `json:"session_id"`
		RequestID             PiNullString `json:"request_id"`
		InputText             PiNullString `json:"input_text"`
		InputHash             PiNullString `json:"input_hash"`
		ErrorMessage          string       `json:"error_message"`
		ErrorDetails          NullableJSON `json:"error_details"`
		DetectionLayers       NullableJSON `json:"detection_layers"`
		MatchedRules          NullableJSON `json:"matched_rules"`
		PolicyConfig          NullableJSON `json:"policy_config"`
		Status                string       `json:"status"`
		RetryCount            int          `json:"retry_count"`
		QCreatedAt            string       `json:"queued_at"`
		StartedAt             PiNullString `json:"started_at"`
		CompletedAt           PiNullString `json:"completed_at"`
	}

	err := h.db.QueryRowContext(r.Context(), query, args...).Scan(
		&result.ID, &result.AnalysisID, &result.QueueID,
		&result.RootCause, &result.ErrorCategory, &result.IsFalsePositive,
		&result.ConfidenceScore, &result.SuggestedFix,
		&result.RuleAdjustment, &result.CodeChanges,
		&result.SeverityLevel, &result.ImpactScope, &result.AffectedUsersEstimate,
		&result.SimilarCases, &result.PatternFrequency,
		&result.AnalysisDurationMs, &result.LLMTokensUsed,
		&result.CreatedAt,
		&result.AnalyzerModel, &result.AnalyzerVersion,
		&result.AnalysisDetails, &result.LLMResponseRaw,
		&result.AutoApplied, &result.AppliedAt, &result.AppliedBy,
		&result.ErrorType, &result.ErrorHash, &result.Priority,
		&result.TenantID, &result.SessionID, &result.RequestID,
		&result.InputText, &result.InputHash, &result.ErrorMessage, &result.ErrorDetails,
		&result.DetectionLayers, &result.MatchedRules, &result.PolicyConfig,
		&result.Status, &result.RetryCount,
		&result.QCreatedAt, &result.StartedAt, &result.CompletedAt,
	)

	if err == sql.ErrNoRows {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalTextErr(w, "query analysis result failed", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// GetQueueItem 获取单条错误队列的全量上下文（详情页的「前因」材料）。
//
// ★ 2026-11-18 PI 分析管理面优化轮：列表查询只取 11 列；「点击行看前因」
//
//	需要 matched_rules / detection_layers / policy_config / error_details /
//	input_text / input_hash / 时间线（started_at / completed_at）。
//	未分析的行（pending/failed）也能看详情 ⇒ 错误一进队列就可追因，
//	不必等分析完成。
//
// ★ 2026-11-26 决策 1：租户过滤 —— 单项详情同样需要租户校验。
func (h *PIAnalysisHandler) GetQueueItem(w http.ResponseWriter, r *http.Request, queueID int64) {
	tenantFilter := EffectiveTenantIDAll(r)
	query := `
		SELECT
			id, error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			input_text, input_hash, error_message, error_details,
			detection_layers, matched_rules, policy_config,
			status, retry_count, max_retries,
			created_at, started_at, completed_at
		FROM error_analysis_queue
		WHERE id = $1
	`
	args := []interface{}{queueID}

	if tenantFilter != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantFilter)
	}

	var item struct {
		ID              int64        `json:"id"`
		ErrorType       string       `json:"error_type"`
		ErrorHash       string       `json:"error_hash"`
		Priority        int          `json:"priority"`
		TenantID        string       `json:"tenant_id"`
		SessionID       PiNullString `json:"session_id"`
		RequestID       PiNullString `json:"request_id"`
		InputText       PiNullString `json:"input_text"`
		InputHash       PiNullString `json:"input_hash"`
		ErrorMessage    string       `json:"error_message"`
		ErrorDetails    NullableJSON `json:"error_details"`
		DetectionLayers NullableJSON `json:"detection_layers"`
		MatchedRules    NullableJSON `json:"matched_rules"`
		PolicyConfig    NullableJSON `json:"policy_config"`
		Status          string       `json:"status"`
		RetryCount      int          `json:"retry_count"`
		MaxRetries      int          `json:"max_retries"`
		CreatedAt       string       `json:"created_at"`
		StartedAt       PiNullString `json:"started_at"`
		CompletedAt     PiNullString `json:"completed_at"`
	}

	err := h.db.QueryRowContext(r.Context(), query, args...).Scan(
		&item.ID, &item.ErrorType, &item.ErrorHash, &item.Priority,
		&item.TenantID, &item.SessionID, &item.RequestID,
		&item.InputText, &item.InputHash, &item.ErrorMessage, &item.ErrorDetails,
		&item.DetectionLayers, &item.MatchedRules, &item.PolicyConfig,
		&item.Status, &item.RetryCount, &item.MaxRetries,
		&item.CreatedAt, &item.StartedAt, &item.CompletedAt,
	)

	if err == sql.ErrNoRows {
		http.Error(w, "queue item not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalTextErr(w, "query error analysis queue item failed", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// TriggerAnalysis 手动触发分析
//
// ★ 2026-11-17 审计轮 P1：三个真实缺陷在此收口。
//
//	① **nil analyzer ⇒ 进程崩溃**：原实现直接在 goroutine 里调
//	   `h.analyzer.ProcessBatch(ctx)` ⇒ analyzer 为 nil 时
//	   **goroutine 里的 nil 解引用 panic 会终止整个网关进程**
//	   （goroutine panic 不被 recover ⇒ 进程退出）。
//	   而 analyzer 在「只开采集、不开分析」时**就是 nil**（这是设计上的常态）。
//	② **裸 go func 没有 panic 收口**：ProcessBatch 内部一旦 panic
//	   ⇒ 同样杀掉整个进程。本仓的 bg.SpawnLoop 自带 recover。
//	③ **batch_size 是假的**：请求里的 batch_size 被解析却**从未使用**
//	   （ProcessBatch 用的是分析器自己的 batchSize）⇒ API 在骗调用方。
//	⇒ 修：nil ⇒ 503；用 bg.SpawnLoop；batch_size 真的落进分析器。
func (h *PIAnalysisHandler) TriggerAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 2026-11-26：批量分析是写动作（花模型调用、改写结果行）⇒ 过超级管理员闸。
	if piRequireSuperAdminForWrite(w, r) {
		return
	}
	if h.analyzer == nil {
		// 分析器未配置（只开了采集，或分析端点未配置）⇒ 明确拒绝。
		http.Error(w, "PI error analyzer is not configured", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		BatchSize int `json:"batch_size"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.BatchSize = 10
	}

	if req.BatchSize <= 0 || req.BatchSize > 100 {
		req.BatchSize = 10
	}

	// ★ batch_size 真的生效（原来只是被解析然后丢掉）。
	//   ★ 2026-11-17 第57增量：改走 ProcessBatchN（局部 limit）——
	//     原实现 SetBatchSize 改共享字段 ⇒ ① 与后台 worker 读并发（data race）
	//     ② 每次手动触发永久改变后台 worker 的批大小。
	batchLimit := req.BatchSize

	// ★ 用 bg.SpawnLoop（自带 panic recover）而不是裸 go func ——
	//   goroutine 里的 panic 会**终止整个进程**。
	bg.SpawnLoop(context.Background(), "pi_analysis.manual_trigger", func(ctx context.Context) {
		processed, err := h.analyzer.ProcessBatchN(ctx, batchLimit)
		if err != nil {
			slog.Error("pi_analysis: manual analysis failed", "error", err)
		} else {
			slog.Info("pi_analysis: manual analysis completed", "processed", processed)
		}
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":    "Analysis triggered",
		"batch_size": req.BatchSize,
	})
}

// GetStats 获取统计信息
func (h *PIAnalysisHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	if h.collector == nil {
		// 采集器未配置 ⇒ 没有可观测的采集面（不是故障，是「没开」）。
		http.Error(w, "PI error collector is not configured", http.StatusServiceUnavailable)
		return
	}
	stats, err := h.collector.GetStats(r.Context())
	if err != nil {
		writeInternalTextErr(w, "query error analysis stats failed", err)
		return
	}

	// ★ 2026-11-18 审计轮 P1：把「链路状态」与「队列内容」一起返回。
	//
	//	否则「队列为空」有两种完全不同的含义（没有错误发生 / 根本没有
	//	生产者），管理面无法区分 ⇒ 运维会把「功能没接线」读成
	//	「系统很健康」。
	//
	//	文案**不在后端给**：后端只给稳定的状态码（chain_status），
	//	展示文案由前端按 i18n 渲染（后端给中文会让非中文界面拿到中文，
	//	也让文案有两份真值）。
	if stats == nil {
		stats = map[string]interface{}{}
	}
	stats["collector_enabled"] = h.collector.Enabled()
	stats["detection_chain_wired"] = h.detectionChainWired
	if !h.detectionChainWired {
		stats["chain_status"] = "not_wired"
	} else {
		stats["chain_status"] = "wired"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}
