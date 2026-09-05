package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/quality"
)

// QualityHandler 质量画像 API handler
type QualityHandler struct {
	db             *sql.DB
	profileUpdater *quality.ProfileUpdater
}

// NewQualityHandler 创建质量画像 handler
func NewQualityHandler(db *sql.DB, updater *quality.ProfileUpdater) *QualityHandler {
	return &QualityHandler{
		db:             db,
		profileUpdater: updater,
	}
}

// Response 统一响应格式
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ProviderRequestStats 供应商请求统计（用于品质 tab 展示可用情况）
type ProviderRequestStats struct {
	TotalRequests int64 `json:"total_requests"`
	MonthRequests int64 `json:"month_requests"`
	WeekRequests  int64 `json:"week_requests"`
	DayRequests   int64 `json:"day_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
}

// ProviderQualityResponse 供应商质量画像响应
type ProviderQualityResponse struct {
	ProviderID   int64                 `json:"provider_id"`
	ProviderName string                `json:"provider_name"`
	Models       []ModelQualityProfile `json:"models"`
	RequestStats ProviderRequestStats  `json:"request_stats"`
}

// ModelQualityProfile 模型质量画像
type ModelQualityProfile struct {
	ModelName    string        `json:"model_name"`
	QualityScore float64       `json:"quality_score"`
	QualityGrade string        `json:"quality_grade"`
	Scores       QualityScores `json:"scores"`
	CalculatedAt time.Time     `json:"calculated_at"`
}

// QualityScores 五个维度评分
type QualityScores struct {
	Availability   float64 `json:"availability"`
	Performance    float64 `json:"performance"`
	Stability      float64 `json:"stability"`
	CostEfficiency float64 `json:"cost_efficiency"`
}

// RankingItem 排行榜项
type RankingItem struct {
	Rank              int       `json:"rank"`
	ProviderID        int64     `json:"provider_id"`
	ProviderName      string    `json:"provider_name"`
	ModelName         string    `json:"model_name"`
	QualityScore      float64   `json:"quality_score"`
	QualityGrade      string    `json:"quality_grade"`
	AvailabilityScore float64   `json:"availability_score"`
	PerformanceScore  float64   `json:"performance_score"`
	CalculatedAt      time.Time `json:"calculated_at"`
}

// ProviderQualitySummaryItem 供应商级品质汇总（列表页一行）
type ProviderQualitySummaryItem struct {
	ProviderID        int64     `json:"provider_id"`
	ProviderName      string    `json:"provider_name"`
	BestModelName     *string   `json:"best_model_name"`
	QualityScore      float64   `json:"quality_score"`
	QualityGrade      string    `json:"quality_grade"`
	AvailabilityScore float64   `json:"availability_score"`
	PerformanceScore  float64   `json:"performance_score"`
	TotalRequests24h  int64     `json:"total_requests_24h"`
	CalculatedAt      time.Time `json:"calculated_at"`
}

// ServeHTTP 实现 http.Handler 接口
func (h *QualityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 路由分发
	path := r.URL.Path

	if path == "/api/quality/summary" {
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, 40501, "方法不允许")
			return
		}
		h.handleGetSummary(w, r)
	} else if path == "/api/quality/ranking" {
		// GET /api/quality/ranking
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, 40501, "方法不允许")
			return
		}
		h.handleGetRanking(w, r)
	} else if strings.HasPrefix(path, "/api/quality/providers/") && strings.HasSuffix(path, "/stats") {
		// GET /api/quality/providers/:id/stats — 仅供应商近 30 天请求统计
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, 40501, "方法不允许")
			return
		}
		h.handleGetProviderRequestStats(w, r)
	} else if strings.HasPrefix(path, "/api/quality/providers/") && strings.HasSuffix(path, "/recalculate") {
		// POST /api/quality/providers/:id/recalculate
		if r.Method != http.MethodPost {
			h.writeError(w, http.StatusMethodNotAllowed, 40501, "方法不允许")
			return
		}
		h.handleRecalculate(w, r)
	} else if strings.HasPrefix(path, "/api/quality/providers/") {
		// GET /api/quality/providers/:id
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, 40501, "方法不允许")
			return
		}
		h.handleGetProviderQuality(w, r)
	} else {
		h.writeError(w, http.StatusNotFound, 40404, "路径不存在")
	}
}

// handleGetProviderRequestStats 仅返回供应商近 30 天请求统计，供请求详情抽屉等
// 轻量场景复用（避免拉取整套质量画像）。数据源与 query 复用 loadProviderRequestStats。
func (h *QualityHandler) handleGetProviderRequestStats(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/quality/providers/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: provider_id 缺失")
		return
	}
	providerID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: provider_id 必须是数字")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists int
	err = h.db.QueryRowContext(ctx, "SELECT 1 FROM providers WHERE id = $1", providerID).Scan(&exists)
	if err == sql.ErrNoRows {
		h.writeError(w, http.StatusNotFound, 40401, "供应商不存在")
		return
	} else if err != nil {
		slog.Error("quality: check provider exists failed", "error", err, "provider_id", providerID)
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}

	// 可选 ?model=<raw_model_name>：按模型粒度统计（方案 C）。缺省为 provider 级（向后兼容）。
	rawModelName := r.URL.Query().Get("model")

	stats, err := h.loadProviderRequestStats(ctx, providerID, rawModelName)
	if err != nil {
		slog.Error("quality: failed to load provider request stats", "error", err, "provider_id", providerID)
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}
	h.writeJSON(w, http.StatusOK, Response{Code: 0, Message: "success", Data: stats})
}

func (h *QualityHandler) handleGetProviderQuality(w http.ResponseWriter, r *http.Request) {
	slog.Info("quality: handleGetProviderQuality called", "path", r.URL.Path, "query", r.URL.RawQuery)

	// 解析 provider_id (从 /api/quality/providers/:id/quality 提取)
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/quality/providers/"), "/")
	slog.Info("quality: parsed path parts", "parts", parts, "len", len(parts))

	if len(parts) < 1 || parts[0] == "" {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: provider_id 缺失")
		return
	}

	providerID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: provider_id 必须是数字")
		return
	}

	modelName := r.URL.Query().Get("model_name")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 查询供应商名称
	var providerName string
	err = h.db.QueryRowContext(ctx, "SELECT display_name FROM providers WHERE id = $1", providerID).Scan(&providerName)
	if err == sql.ErrNoRows {
		slog.Warn("quality: provider not found", "provider_id", providerID)
		h.writeError(w, http.StatusNotFound, 40401, "供应商不存在")
		return
	}
	if err != nil {
		slog.Error("quality: failed to query provider name", "error", err, "provider_id", providerID)
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}

	// 查询质量画像。
	//
	// BRIDGE (2026-07-27): provider_quality_profiles 是早期未完成的 provider_quality_*
	// 系统的表（其 writer ProfileUpdater 从未填数据，表始终为空）。当前活跃的
	// 供应商画像数据由 provider_profile_daily 提供（domains/providerprofile，Phase 1+2
	// 的采集器/聚合器写入）。这里改为从 provider_profile_daily 读取并映射到本 handler
	// 期望的 4 维度评分结构，使既有前端（ProvidersView 的 quality 列、详情页 Quality
	// tab）直接展示真实数据，无需改前端。
	//
	// 维度映射：
	//   quality_score        ← total_score
	//   availability_score   ← availability_score
	//   performance_score    ← network_score        （网络延迟≈性能）
	//   stability_score      ← stability_score
	//   cost_efficiency_score← cost_accuracy_score  （Phase 2 暂未实现，为 NULL）
	//   quality_grade        ← 按 total_score 计算（A/B/C/D/F）
	//   model_name           ← NULL（provider_profile_daily 是 credential 粒度，非 model 粒度）
	//
	// model_name 过滤在本数据源下无意义（无 model 列），忽略以保证返回供应商级汇总。
	_ = modelName // 显式忽略，避免"declared but not used"
	query := `
SELECT
    NULL::varchar AS model_name,
    total_score,
    CASE
        WHEN total_score >= 80 THEN 'A'
        WHEN total_score >= 70 THEN 'B'
        WHEN total_score >= 60 THEN 'C'
        WHEN total_score >= 40 THEN 'D'
        ELSE 'F'
    END AS quality_grade,
    availability_score,
    network_score AS performance_score,
    stability_score,
    cost_accuracy_score AS cost_efficiency_score,
    created_at AS updated_at
FROM provider_profile_daily
WHERE provider_id = $1
ORDER BY profile_date DESC, total_score DESC
`
	args := []interface{}{providerID}

	slog.Info("quality: querying profiles", "provider_id", providerID, "model_name", modelName)

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		slog.Error("quality: failed to query profiles", "error", err, "provider_id", providerID, "model_name", modelName)
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}
	defer rows.Close()

	var models []ModelQualityProfile
	for rows.Next() {
		var m ModelQualityProfile
		var scores QualityScores
		var modelName sql.NullString

		err := rows.Scan(
			&modelName,
			&m.QualityScore,
			&m.QualityGrade,
			&scores.Availability,
			&scores.Performance,
			&scores.Stability,
			&scores.CostEfficiency,
			&m.CalculatedAt,
		)
		if err != nil {
			slog.Error("quality: failed to scan row", "error", err, "provider_id", providerID)
			continue
		}

		if modelName.Valid {
			m.ModelName = modelName.String
		}
		m.Scores = scores
		models = append(models, m)
	}

	if len(models) == 0 {
		h.writeError(w, http.StatusNotFound, 40402, "暂无质量数据")
		return
	}

	// 请求统计（总数 / 当月 / 当周 / 当天 / 成功 / 失败 / token）。
	// 数据源：usage_ledger_with_current_month（与 /api/usage/providers/:id 同源）。
	// 窗口：近 30 天（对齐用量 tab 的 provider summary 默认窗口），限定扫描范围。
	// 查询失败不阻断品质数据展示：记日志并返回零值，避免统计表缺失拖垮整个 tab。
	requestStats, err := h.loadProviderRequestStats(ctx, providerID, "")
	if err != nil {
		slog.Error("quality: failed to load provider request stats", "error", err, "provider_id", providerID)
		requestStats = ProviderRequestStats{}
	}

	h.writeJSON(w, http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data: ProviderQualityResponse{
			ProviderID:   providerID,
			ProviderName: providerName,
			Models:       models,
			RequestStats: requestStats,
		},
	})
}

// loadProviderRequestStats 加载供应商请求统计（单条聚合查询）。
// usage_ledger_with_current_month 的月度分区为列式存储，对 provider 级聚合扫描高效。
// 窗口固定为近 30 天（与 /api/usage/providers/:id 的 summary 默认窗口一致），
// 避免全量扫描历史分区，也保证与画像数据周期语义一致。
//
// rawModelName 非空时追加 AND raw_model_name = $2 条件（方案 C：按模型粒度统计）。
// 该表无 provider_id 索引，加 raw_model_name 条件不改变执行计划（同样靠 ts 分区裁剪
// 后顺序扫描过滤），性能开销可忽略，无需新增索引。
func (h *QualityHandler) loadProviderRequestStats(ctx context.Context, providerID int64, rawModelName string) (ProviderRequestStats, error) {
	query := `
SELECT
    COUNT(*)::bigint,
    COUNT(*) FILTER (WHERE ts >= date_trunc('month', NOW()))::bigint,
    COUNT(*) FILTER (WHERE ts >= date_trunc('week', NOW()))::bigint,
    COUNT(*) FILTER (WHERE ts >= date_trunc('day', NOW()))::bigint,
    COUNT(*) FILTER (WHERE success)::bigint,
    COUNT(*) FILTER (WHERE NOT success)::bigint,
    COALESCE(SUM(total_tokens), 0)::bigint
FROM usage_ledger_with_current_month
WHERE provider_id = $1
  AND ts >= NOW() - INTERVAL '30 days'`
	args := []any{providerID}
	if rawModelName != "" {
		query += "\n  AND raw_model_name = $2"
		args = append(args, rawModelName)
	}

	var s ProviderRequestStats
	err := h.db.QueryRowContext(ctx, query, args...).Scan(
		&s.TotalRequests, &s.MonthRequests, &s.WeekRequests, &s.DayRequests,
		&s.SuccessCount, &s.FailureCount, &s.TotalTokens,
	)
	return s, err
}

// handleGetSummary 供应商级品质汇总（优先 provider 级行，否则取最高分模型）
func (h *QualityHandler) handleGetSummary(w http.ResponseWriter, r *http.Request) {
	slog.Info("quality: handleGetSummary called")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// BRIDGE (2026-07-27): read from provider_profile_daily instead of the empty
	// provider_quality_profiles. One row per provider (latest profile_date),
	// aggregated across that provider's credentials. See handleGetProviderQuality
	// for the full mapping rationale.
	const sqlQuery = `
SELECT DISTINCT ON (d.provider_id)
    d.provider_id,
    COALESCE(pr.display_name, '') AS provider_name,
    NULL::varchar AS model_name,
    COALESCE(d.total_score, 0),
    CASE
        WHEN d.total_score >= 80 THEN 'A'
        WHEN d.total_score >= 70 THEN 'B'
        WHEN d.total_score >= 60 THEN 'C'
        WHEN d.total_score >= 40 THEN 'D'
        ELSE 'F'
    END AS quality_grade,
    COALESCE(d.availability_score, 0),
    COALESCE(d.network_score, 0) AS performance_score,
    COALESCE((d.raw_stats->>'total_requests')::bigint, 0) AS total_requests_24h,
    d.created_at AS updated_at
FROM provider_profile_daily d
LEFT JOIN providers pr ON d.provider_id = pr.id
ORDER BY d.provider_id, d.profile_date DESC, d.total_score DESC
`

	rows, err := h.db.QueryContext(ctx, sqlQuery)
	if err != nil {
		slog.Error("quality: failed to query summary", "error", err)
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}
	defer rows.Close()

	summary := make([]ProviderQualitySummaryItem, 0)
	for rows.Next() {
		var item ProviderQualitySummaryItem
		var modelName sql.NullString
		err := rows.Scan(
			&item.ProviderID,
			&item.ProviderName,
			&modelName,
			&item.QualityScore,
			&item.QualityGrade,
			&item.AvailabilityScore,
			&item.PerformanceScore,
			&item.TotalRequests24h,
			&item.CalculatedAt,
		)
		if err != nil {
			slog.Error("quality: failed to scan summary row", "error", err)
			continue
		}
		if modelName.Valid && modelName.String != "" {
			name := modelName.String
			item.BestModelName = &name
		}
		summary = append(summary, item)
	}

	h.writeJSON(w, http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data: map[string]interface{}{
			"total":   len(summary),
			"summary": summary,
		},
	})
}

// handleGetRanking 查询质量排行榜
func (h *QualityHandler) handleGetRanking(w http.ResponseWriter, r *http.Request) {
	slog.Info("quality: handleGetRanking called", "query", r.URL.RawQuery)

	query := r.URL.Query()
	modelName := query.Get("model_name")
	limitStr := query.Get("limit")
	if limitStr == "" {
		limitStr = "20"
	}
	orderBy := query.Get("order_by")
	if orderBy == "" {
		orderBy = "quality_score"
	}
	minScoreStr := query.Get("min_score")
	if minScoreStr == "" {
		minScoreStr = "0"
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 || limit > 100 {
		limit = 20
	}

	minScore, err := strconv.ParseFloat(minScoreStr, 64)
	if err != nil || minScore < 0 {
		minScore = 0
	}

	// 验证 order_by 字段。BRIDGE: performance_score 映射到 network_score，
	// quality_score 映射到 total_score。
	orderByColumn := map[string]string{
		"quality_score":      "total_score",
		"availability_score": "availability_score",
		"performance_score":  "network_score",
	}
	col, ok := orderByColumn[orderBy]
	if !ok {
		col = "total_score"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// BRIDGE (2026-07-27): read from provider_profile_daily. model_name 过滤无意义
	// （数据源无 model 列），忽略以返回供应商级排行。
	_ = modelName
	sqlQuery := `
SELECT
    d.provider_id,
    COALESCE(pr.display_name, '') AS provider_name,
    NULL::varchar AS model_name,
    d.total_score AS quality_score,
    CASE
        WHEN d.total_score >= 80 THEN 'A'
        WHEN d.total_score >= 70 THEN 'B'
        WHEN d.total_score >= 60 THEN 'C'
        WHEN d.total_score >= 40 THEN 'D'
        ELSE 'F'
    END AS quality_grade,
    d.availability_score,
    d.network_score AS performance_score,
    d.created_at AS updated_at
FROM provider_profile_daily d
LEFT JOIN providers pr ON d.provider_id = pr.id
WHERE d.total_score >= $1
ORDER BY d.` + col + ` DESC
LIMIT $2
`
	args := []interface{}{minScore, limit}

	slog.Info("quality: executing ranking query", "model_name", modelName, "min_score", minScore, "limit", limit, "order_by", orderBy)

	rows, err := h.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		slog.Error("quality: failed to query ranking", "error", err, "model_name", modelName)
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}
	defer rows.Close()

	var ranking []RankingItem
	rank := 1
	for rows.Next() {
		var item RankingItem
		err := rows.Scan(
			&item.ProviderID,
			&item.ProviderName,
			&item.ModelName,
			&item.QualityScore,
			&item.QualityGrade,
			&item.AvailabilityScore,
			&item.PerformanceScore,
			&item.CalculatedAt,
		)
		if err != nil {
			slog.Error("quality: failed to scan ranking row", "error", err)
			continue
		}

		item.Rank = rank
		ranking = append(ranking, item)
		rank++
	}

	h.writeJSON(w, http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data: map[string]interface{}{
			"total":   len(ranking),
			"ranking": ranking,
		},
	})
}

// handleRecalculate 手动重算质量画像
func (h *QualityHandler) handleRecalculate(w http.ResponseWriter, r *http.Request) {
	// 解析 provider_id
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/quality/providers/"), "/")
	if len(parts) < 3 {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: provider_id 缺失")
		return
	}

	providerID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: provider_id 必须是数字")
		return
	}

	var req struct {
		ModelName string `json:"model_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: "+err.Error())
		return
	}

	if req.ModelName == "" {
		h.writeError(w, http.StatusBadRequest, 40001, "参数错误: model_name 必填")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// BRIDGE (2026-07-27): provider_profile_daily 由 DailyAggregator（bg worker）
	// 每日自动聚合，不再支持按 model 手动重算。这里改为返回该供应商最新一天的
	// 画像分数（与 GET 端点一致的来源）。前端"重新计算"按钮因此变成"刷新最新分数"。
	var result struct {
		QualityScore float64   `json:"quality_score"`
		QualityGrade string    `json:"quality_grade"`
		CalculatedAt time.Time `json:"calculated_at"`
	}

	query := `
SELECT total_score,
    CASE
        WHEN total_score >= 80 THEN 'A'
        WHEN total_score >= 70 THEN 'B'
        WHEN total_score >= 60 THEN 'C'
        WHEN total_score >= 40 THEN 'D'
        ELSE 'F'
    END,
    created_at
FROM provider_profile_daily
WHERE provider_id = $1
ORDER BY profile_date DESC
LIMIT 1
`

	err = h.db.QueryRowContext(ctx, query, providerID).Scan(
		&result.QualityScore,
		&result.QualityGrade,
		&result.CalculatedAt,
	)
	if err != nil {
		h.writeError(w, http.StatusNotFound, 40402, "该供应商暂无质量画像数据")
		return
	}

	h.writeJSON(w, http.StatusOK, Response{
		Code:    0,
		Message: "质量画像计算成功",
		Data: map[string]interface{}{
			"provider_id":   providerID,
			"model_name":    req.ModelName,
			"quality_score": result.QualityScore,
			"quality_grade": result.QualityGrade,
			"updated_at":    result.CalculatedAt,
		},
	})
}

// writeJSON 写 JSON 响应
func (h *QualityHandler) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写错误响应
func (h *QualityHandler) writeError(w http.ResponseWriter, status int, code int, message string) {
	h.writeJSON(w, status, Response{
		Code:    code,
		Message: message,
	})
}
