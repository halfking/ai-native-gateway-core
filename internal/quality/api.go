package quality

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/logger"
)

// APIHandler HTTP API 处理器
type APIHandler struct {
	db         *sql.DB
	calculator *ProfileCalculator
	logger     logger.Logger
}

// NewAPIHandler 创建 API 处理器
func NewAPIHandler(db *sql.DB) *APIHandler {
	return &APIHandler{
		db:         db,
		calculator: NewProfileCalculator(db),
		logger:     logger.New("quality-api"),
	}
}

// RegisterRoutes 注册路由
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/quality/profile", h.GetProfile)
	mux.HandleFunc("/api/quality/calculate", h.CalculateNow)
	mux.HandleFunc("/api/quality/list", h.ListProfiles)
}

// GetProfile 获取质量画像
// GET /api/quality/profile?provider_id=1&model=gpt-4
func (h *APIHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析参数
	providerIDStr := r.URL.Query().Get("provider_id")
	modelName := r.URL.Query().Get("model")

	if providerIDStr == "" || modelName == "" {
		h.respondError(w, http.StatusBadRequest, "provider_id and model are required")
		return
	}

	providerID, err := strconv.ParseInt(providerIDStr, 10, 64)
	if err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid provider_id")
		return
	}

	h.logger.Info("get profile request",
		"provider_id", providerID,
		"model", modelName,
	)

	// 查询数据库
	profile, err := h.getProfileFromDB(r.Context(), providerID, modelName)
	if err != nil {
		if err == sql.ErrNoRows {
			h.respondError(w, http.StatusNotFound, "profile not found")
			return
		}
		h.logger.Error("failed to get profile",
			"provider_id", providerID,
			"model", modelName,
			"error", err.Error(),
		)
		h.respondError(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.respondJSON(w, http.StatusOK, profile)
}

// CalculateNow 立即计算质量评分
// POST /api/quality/calculate?provider_id=1&model=gpt-4
func (h *APIHandler) CalculateNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析参数
	providerIDStr := r.URL.Query().Get("provider_id")
	modelName := r.URL.Query().Get("model")

	if providerIDStr == "" || modelName == "" {
		h.respondError(w, http.StatusBadRequest, "provider_id and model are required")
		return
	}

	providerID, err := strconv.ParseInt(providerIDStr, 10, 64)
	if err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid provider_id")
		return
	}

	h.logger.Info("calculate now request",
		"provider_id", providerID,
		"model", modelName,
	)

	// 计算质量评分
	result, err := h.calculator.Calculate(r.Context(), providerID, modelName)
	if err != nil {
		h.logger.Error("failed to calculate",
			"provider_id", providerID,
			"model", modelName,
			"error", err.Error(),
		)
		h.respondError(w, http.StatusInternalServerError, "calculation failed")
		return
	}

	// 保存到数据库
	if err := h.saveProfile(r.Context(), result); err != nil {
		h.logger.Error("failed to save profile",
			"provider_id", providerID,
			"model", modelName,
			"error", err.Error(),
		)
		h.respondError(w, http.StatusInternalServerError, "save failed")
		return
	}

	h.respondJSON(w, http.StatusOK, result)
}

// ListProfiles 列出所有质量画像
// GET /api/quality/list
func (h *APIHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	h.logger.Debug("list profiles request")

	// 查询数据库
	profiles, err := h.listProfilesFromDB(r.Context())
	if err != nil {
		h.logger.Error("failed to list profiles", "error", err.Error())
		h.respondError(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.respondJSON(w, http.StatusOK, map[string]interface{}{
		"profiles": profiles,
		"count":    len(profiles),
	})
}

// ProfileResponse API 响应格式
type ProfileResponse struct {
	ProviderID          int64     `json:"provider_id"`
	ModelName           string    `json:"model_name"`
	AvailabilityScore   float64   `json:"availability_score"`
	PerformanceScore    float64   `json:"performance_score"`
	ReliabilityScore    float64   `json:"reliability_score"`
	StabilityScore      float64   `json:"stability_score"`
	CostEfficiencyScore float64   `json:"cost_efficiency_score"`
	QualityScore        float64   `json:"quality_score"`
	CalculatedAt        time.Time `json:"calculated_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// getProfileFromDB 从数据库获取质量画像
func (h *APIHandler) getProfileFromDB(ctx context.Context, providerID int64, modelName string) (*ProfileResponse, error) {
	query := `
SELECT
    provider_id,
    model_name,
    availability_score,
    performance_score,
    reliability_score,
    stability_score,
    cost_efficiency_score,
    quality_score,
    calculated_at,
    updated_at
FROM provider_quality_profiles
WHERE provider_id = $1 AND model_name = $2
`

	var p ProfileResponse
	err := h.db.QueryRowContext(ctx, query, providerID, modelName).Scan(
		&p.ProviderID,
		&p.ModelName,
		&p.AvailabilityScore,
		&p.PerformanceScore,
		&p.ReliabilityScore,
		&p.StabilityScore,
		&p.CostEfficiencyScore,
		&p.QualityScore,
		&p.CalculatedAt,
		&p.UpdatedAt,
	)

	return &p, err
}

// listProfilesFromDB 从数据库列出所有质量画像
func (h *APIHandler) listProfilesFromDB(ctx context.Context) ([]ProfileResponse, error) {
	query := `
SELECT
    provider_id,
    model_name,
    availability_score,
    performance_score,
    reliability_score,
    stability_score,
    cost_efficiency_score,
    quality_score,
    calculated_at,
    updated_at
FROM provider_quality_profiles
ORDER BY quality_score DESC, provider_id, model_name
LIMIT 100
`

	rows, err := h.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []ProfileResponse
	for rows.Next() {
		var p ProfileResponse
		if err := rows.Scan(
			&p.ProviderID,
			&p.ModelName,
			&p.AvailabilityScore,
			&p.PerformanceScore,
			&p.ReliabilityScore,
			&p.StabilityScore,
			&p.CostEfficiencyScore,
			&p.QualityScore,
			&p.CalculatedAt,
			&p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}

	return profiles, rows.Err()
}

// saveProfile 保存质量画像到数据库
func (h *APIHandler) saveProfile(ctx context.Context, result *ScoreResult) error {
	query := `
INSERT INTO provider_quality_profiles (
    provider_id,
    model_name,
    availability_score,
    performance_score,
    reliability_score,
    stability_score,
    cost_efficiency_score,
    quality_score,
    calculated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
ON CONFLICT (provider_id, model_name)
DO UPDATE SET
    availability_score = EXCLUDED.availability_score,
    performance_score = EXCLUDED.performance_score,
    reliability_score = EXCLUDED.reliability_score,
    stability_score = EXCLUDED.stability_score,
    cost_efficiency_score = EXCLUDED.cost_efficiency_score,
    quality_score = EXCLUDED.quality_score,
    calculated_at = EXCLUDED.calculated_at,
    updated_at = NOW()
`

	_, err := h.db.ExecContext(ctx, query,
		result.ProviderID,
		result.ModelName,
		result.AvailabilityScore,
		result.PerformanceScore,
		result.ReliabilityScore,
		result.StabilityScore,
		result.CostEfficiencyScore,
		result.QualityScore,
	)

	return err
}

// respondJSON 返回 JSON 响应
func (h *APIHandler) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// respondError 返回错误响应
func (h *APIHandler) respondError(w http.ResponseWriter, status int, message string) {
	h.respondJSON(w, status, map[string]string{
		"error": message,
	})
}
