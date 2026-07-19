package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
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

// ProviderQualityResponse 供应商质量画像响应
type ProviderQualityResponse struct {
	ProviderID   int64                 `json:"provider_id"`
	ProviderName string                `json:"provider_name"`
	Models       []ModelQualityProfile `json:"models"`
}

// ModelQualityProfile 模型质量画像
type ModelQualityProfile struct {
	ModelName     string        `json:"model_name"`
	QualityScore  float64       `json:"quality_score"`
	QualityGrade  string        `json:"quality_grade"`
	Scores        QualityScores `json:"scores"`
	CalculatedAt  time.Time     `json:"calculated_at"`
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

// ServeHTTP 实现 http.Handler 接口
func (h *QualityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 路由分发
	path := r.URL.Path

	if path == "/api/quality/ranking" {
		// GET /api/quality/ranking
		if r.Method != http.MethodGet {
			h.writeError(w, http.StatusMethodNotAllowed, 40501, "方法不允许")
			return
		}
		h.handleGetRanking(w, r)
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
func (h *QualityHandler) handleGetProviderQuality(w http.ResponseWriter, r *http.Request) {
	// 解析 provider_id (从 /api/quality/providers/:id/quality 提取)
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

	modelName := r.URL.Query().Get("model_name")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 查询供应商名称
	var providerName string
	err = h.db.QueryRowContext(ctx, "SELECT name FROM providers WHERE id = $1", providerID).Scan(&providerName)
	if err == sql.ErrNoRows {
		h.writeError(w, http.StatusNotFound, 40401, "供应商不存在")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}

	// 查询质量画像
	var query string
	var args []interface{}

	if modelName != "" {
		query = `
SELECT 
    model_name,
    quality_score,
    quality_grade,
    availability_score,
    performance_score,
    stability_score,
    cost_efficiency_score,
    updated_at
FROM provider_quality_profiles
WHERE provider_id = $1 AND model_name = $2
`
		args = []interface{}{providerID, modelName}
	} else {
		query = `
SELECT 
    model_name,
    quality_score,
    quality_grade,
    availability_score,
    performance_score,
    stability_score,
    cost_efficiency_score,
    updated_at
FROM provider_quality_profiles
WHERE provider_id = $1
ORDER BY quality_score DESC
`
		args = []interface{}{providerID}
	}

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, 50001, "服务器内部错误")
		return
	}
	defer rows.Close()

	var models []ModelQualityProfile
	for rows.Next() {
		var m ModelQualityProfile
		var scores QualityScores

		err := rows.Scan(
			&m.ModelName,
			&m.QualityScore,
			&m.QualityGrade,
			&scores.Availability,
			&scores.Performance,
			&scores.Stability,
			&scores.CostEfficiency,
			&m.CalculatedAt,
		)
		if err != nil {
			continue
		}

		m.Scores = scores
		models = append(models, m)
	}

	if len(models) == 0 {
		h.writeError(w, http.StatusNotFound, 40402, "暂无质量数据")
		return
	}

	h.writeJSON(w, http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data: ProviderQualityResponse{
			ProviderID:   providerID,
			ProviderName: providerName,
			Models:       models,
		},
	})
}

// handleGetRanking 查询质量排行榜
func (h *QualityHandler) handleGetRanking(w http.ResponseWriter, r *http.Request) {
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

	// 验证 order_by 字段
	validOrderBy := map[string]bool{
		"quality_score":      true,
		"availability_score": true,
		"performance_score":  true,
	}
	if !validOrderBy[orderBy] {
		orderBy = "quality_score"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sqlQuery := `
SELECT 
    p.provider_id,
    COALESCE(pr.name, '') as provider_name,
    p.model_name,
    p.quality_score,
    p.quality_grade,
    p.availability_score,
    p.performance_score,
    p.updated_at
FROM provider_quality_profiles p
LEFT JOIN providers pr ON p.provider_id = pr.id
WHERE ($1::text IS NULL OR $1 = '' OR p.model_name = $1)
  AND p.quality_score >= $2
ORDER BY p.` + orderBy + ` DESC
LIMIT $3
`

	rows, err := h.db.QueryContext(ctx, sqlQuery, modelName, minScore, limit)
	if err != nil {
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

	// 调用 ProfileUpdater 手动更新
	err = h.profileUpdater.UpdateOne(ctx, providerID, req.ModelName)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, 50001, "质量画像计算失败: "+err.Error())
		return
	}

	// 查询最新数据
	var result struct {
		QualityScore float64   `json:"quality_score"`
		QualityGrade string    `json:"quality_grade"`
		CalculatedAt time.Time `json:"calculated_at"`
	}

	query := `
SELECT quality_score, quality_grade, updated_at
FROM provider_quality_profiles
WHERE provider_id = $1 AND model_name = $2
`

	err = h.db.QueryRowContext(ctx, query, providerID, req.ModelName).Scan(
		&result.QualityScore,
		&result.QualityGrade,
		&result.CalculatedAt,
	)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, 50001, "查询计算结果失败")
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
			"updated_at": result.CalculatedAt,
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
