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
	// ★ 2026-11-18 审计轮 P1：PI 错误采集的**唯一生产者**是
	//	promptinjection.Detector（d.collectError），而检测器只在
	//	LLM_GATEWAY_USE_V2_PIPELINE 开启时才被构造（默认关）
	//	⇒ 默认形态下采集队列**恒为空**，且「空」与「没有生产者」
	//	在运行期**不可区分**（运维会读成「没有错误发生过」）。
	//	⇒ 把这条链路状态暴露到 /stats，让管理面能如实区分。
	detectionChainWired bool
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
	mux.HandleFunc(prefix+"/trigger", wrap(h.TriggerAnalysis))
	mux.HandleFunc(prefix+"/stats", wrap(h.GetStats))
}

func (h *PIAnalysisHandler) handleResults(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.resultsPathPrefix())
	if path == "" || path == "/" {
		h.GetResults(w, r)
		return
	}

	// 处理 /results/{queue_id}
	queueIDStr := strings.TrimPrefix(path, "/")
	queueID, err := strconv.ParseInt(queueIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid queue_id", http.StatusBadRequest)
		return
	}
	h.GetResult(w, r, queueID)
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
			ID         int64
			ErrorType  string
			ErrorHash  string
			Priority   int
			TenantID   string
			SessionID  sql.NullString
			RequestID  sql.NullString
			ErrorMsg   string
			Status     string
			RetryCount int
			CreatedAt  string
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

	// 获取总数
	countQuery := `SELECT COUNT(*) FROM error_analysis_queue WHERE 1=1`
	if status != "" {
		countQuery += " AND status = $1"
		var total int64
		h.db.QueryRowContext(r.Context(), countQuery, status).Scan(&total)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": errors,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	} else {
		var total int64
		h.db.QueryRowContext(r.Context(), countQuery).Scan(&total)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": errors,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
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

	query := `
		SELECT 
			r.id, r.analysis_id, r.queue_id,
			r.root_cause, r.error_category, r.is_false_positive,
			r.confidence_score, r.severity_level, r.suggested_fix,
			q.error_type, q.tenant_id, r.created_at
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		ORDER BY r.created_at DESC
		LIMIT $1 OFFSET $2
	`

	rows, err := h.db.QueryContext(r.Context(), query, limit, offset)
	if err != nil {
		writeInternalTextErr(w, "query error analysis results failed", err)
		return
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var r struct {
			ID              int64
			AnalysisID      string
			QueueID         int64
			RootCause       string
			ErrorCategory   string
			IsFalsePositive bool
			ConfidenceScore float64
			SeverityLevel   string
			SuggestedFix    string
			ErrorType       string
			TenantID        string
			CreatedAt       string
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

	// 获取总数
	var total int64
	h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM error_analysis_results`).Scan(&total)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// GetResult 获取单个分析结果详情
func (h *PIAnalysisHandler) GetResult(w http.ResponseWriter, r *http.Request, queueID int64) {
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
			q.error_type, q.error_message, q.tenant_id, q.input_text
		FROM error_analysis_results r
		JOIN error_analysis_queue q ON r.queue_id = q.id
		WHERE r.queue_id = $1
	`

	var result struct {
		ID                    int64
		AnalysisID            string
		QueueID               int64
		RootCause             string
		ErrorCategory         string
		IsFalsePositive       bool
		ConfidenceScore       float64
		SuggestedFix          string
		RuleAdjustment        json.RawMessage
		CodeChanges           json.RawMessage
		SeverityLevel         string
		ImpactScope           string
		AffectedUsersEstimate int
		SimilarCases          json.RawMessage
		PatternFrequency      int
		AnalysisDurationMs    int
		LLMTokensUsed         int
		CreatedAt             string
		ErrorType             string
		ErrorMessage          string
		TenantID              string
		InputText             sql.NullString
	}

	err := h.db.QueryRowContext(r.Context(), query, queueID).Scan(
		&result.ID, &result.AnalysisID, &result.QueueID,
		&result.RootCause, &result.ErrorCategory, &result.IsFalsePositive,
		&result.ConfidenceScore, &result.SuggestedFix,
		&result.RuleAdjustment, &result.CodeChanges,
		&result.SeverityLevel, &result.ImpactScope, &result.AffectedUsersEstimate,
		&result.SimilarCases, &result.PatternFrequency,
		&result.AnalysisDurationMs, &result.LLMTokensUsed,
		&result.CreatedAt,
		&result.ErrorType, &result.ErrorMessage, &result.TenantID, &result.InputText,
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
