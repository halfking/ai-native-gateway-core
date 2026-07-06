// admin/session_health_api.go — Session Health Score API Endpoints
//
// 实现健康评分 API，提供单会话健康详情与强制重算功能。
// Ref: docs/session-management-analytics-plan.md 第 11.2.9 节
//
// 路由（在 cmd/gateway/main.go 注册）：
//   GET    /api/admin/sessions/<id>/health           → HandleSessionHealth
//   POST   /api/admin/sessions/<id>/recompute-health → HandleRecomputeSessionHealth

package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ── Prometheus Metrics ────────────────────────────────────────────────

var (
	sessionHealthComputedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_health_computed_total",
			Help: "Total number of session health score computations",
		},
		[]string{"source"}, // source: api / worker / snapshot
	)
)

// ── Response Types ────────────────────────────────────────────────────

// SessionHealthResponse 会话健康详情响应
type SessionHealthResponse struct {
	GwSessionID   string        `json:"gw_session_id"`
	HealthScore   int           `json:"health_score"`
	HealthGrade   string        `json:"health_grade"`
	Outcome       string        `json:"outcome"`
	OutcomeReason string        `json:"outcome_reason"`
	ErrorRate     float64       `json:"error_rate"`
	AvgLatencyMs  int           `json:"avg_latency_ms"`
	ComputedAt    time.Time     `json:"computed_at"`
	Penalties     []PenaltyItem `json:"penalties"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// HandleSessionHealth GET /api/admin/sessions/<id>/health
//
// 返回单会话的健康评分详情。若会话尚未计算健康分，则实时计算并写入。
func (h *Handler) HandleSessionHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/sessions/", 0)
	if gwSessionID == "" || gwSessionID == "health" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	health, err := h.getOrComputeHealth(ctx, gwSessionID, tenantID, "api")
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to get health: "+err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, health)
}

// HandleRecomputeSessionHealth POST /api/admin/sessions/<id>/recompute-health
//
// 强制重新计算会话健康分（管理员手动触发）。
func (h *Handler) HandleRecomputeSessionHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not available")
		return
	}

	gwSessionID := pathSegment(r.URL.Path, "/api/admin/sessions/", 0)
	if gwSessionID == "" {
		writeError(w, http.StatusBadRequest, "gw_session_id is required")
		return
	}

	tenantID := EffectiveTenantIDAll(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 获取会话摘要
	summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, tenantID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		}
		return
	}

	// 计算健康分
	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	// 写入数据库
	if err := h.updateSessionHealth(ctx, gwSessionID, health); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}

	sessionHealthComputedTotal.WithLabelValues("api").Inc()
	slog.Info("session health recomputed", "gw_session_id", gwSessionID, "score", health.HealthScore)

	response := SessionHealthResponse{
		GwSessionID:   gwSessionID,
		HealthScore:   health.HealthScore,
		HealthGrade:   health.HealthGrade,
		Outcome:       health.Outcome,
		OutcomeReason: health.OutcomeReason,
		ErrorRate:     health.ErrorRate,
		AvgLatencyMs:  health.AvgLatencyMs,
		ComputedAt:    time.Now(),
		Penalties:     health.Penalties,
	}

	writeJSON(w, http.StatusOK, response)
}

// ── Helper Functions ──────────────────────────────────────────────────

// getOrComputeHealth 获取或计算会话健康分
//
// 若 health_score 为 NULL，则实时计算并写入；否则返回已存储的值。
func (h *Handler) getOrComputeHealth(ctx context.Context, gwSessionID, tenantID, source string) (SessionHealthResponse, error) {
	// 1. 尝试从数据库获取已计算的健康分
	query := `
		SELECT health_score, health_grade, outcome, last_health_at
		FROM session_summaries
		WHERE session_key = $1
	`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	var score *int
	var grade *string
	var outcome *string
	var lastHealthAt *time.Time
	err := h.db.QueryRow(ctx, query, args...).Scan(&score, &grade, &outcome, &lastHealthAt)
	if err != nil {
		return SessionHealthResponse{}, err
	}

	// 2. 若已存在健康分，返回（需要重新查询完整摘要以获取 penalties）
	if score != nil {
		summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, tenantID)
		if err != nil {
			return SessionHealthResponse{}, err
		}
		config := DefaultHealthScoreConfig()
		health := ComputeHealth(summary, config)

		return SessionHealthResponse{
			GwSessionID:   gwSessionID,
			HealthScore:   *score,
			HealthGrade:   *grade,
			Outcome:       *outcome,
			OutcomeReason: health.OutcomeReason,
			ErrorRate:     health.ErrorRate,
			AvgLatencyMs:  health.AvgLatencyMs,
			ComputedAt:    *lastHealthAt,
			Penalties:     health.Penalties,
		}, nil
	}

	// 3. 若 health_score 为 NULL，实时计算并写入
	summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, tenantID)
	if err != nil {
		return SessionHealthResponse{}, err
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	if err := h.updateSessionHealth(ctx, gwSessionID, health); err != nil {
		slog.Warn("failed to persist health score", "gw_session_id", gwSessionID, "error", err)
	}

	sessionHealthComputedTotal.WithLabelValues(source).Inc()

	return SessionHealthResponse{
		GwSessionID:   gwSessionID,
		HealthScore:   health.HealthScore,
		HealthGrade:   health.HealthGrade,
		Outcome:       health.Outcome,
		OutcomeReason: health.OutcomeReason,
		ErrorRate:     health.ErrorRate,
		AvgLatencyMs:  health.AvgLatencyMs,
		ComputedAt:    time.Now(),
		Penalties:     health.Penalties,
	}, nil
}

// fetchSessionSummaryByID 从数据库获取会话摘要（用于健康计算）
func (h *Handler) fetchSessionSummaryByID(ctx context.Context, gwSessionID, tenantID string) (AnalyticsSessionSummary, error) {
	query := "SELECT " + sessionSummarySelectCols +
		" FROM session_summaries ss" +
		" LEFT JOIN session_dim sd ON sd.gw_session_id = ss.session_key" +
		" WHERE ss.session_key = $1"
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND ss.tenant_id = $2"
		args = append(args, tenantID)
	}

	return scanSessionSummary(h.db.QueryRow(ctx, query, args...))
}

// updateSessionHealth 更新会话健康分到数据库
func (h *Handler) updateSessionHealth(ctx context.Context, gwSessionID string, health SessionHealth) error {
	query := `
		UPDATE session_summaries
		SET health_score = $1,
		    health_grade = $2,
		    outcome = $3,
		    last_health_at = NOW(),
		    updated_at = NOW()
		WHERE session_key = $4
	`
	_, err := h.db.Exec(ctx, query, health.HealthScore, health.HealthGrade, health.Outcome, gwSessionID)
	return err
}

// ComputeAndPersistHealth 计算并持久化健康分（供外部调用，如会话停止时）
//
// 返回计算的健康分，若失败则记录警告但不阻塞调用方。
func (h *Handler) ComputeAndPersistHealth(ctx context.Context, gwSessionID string, source string) (*SessionHealth, error) {
	summary, err := h.fetchSessionSummaryByID(ctx, gwSessionID, "")
	if err != nil {
		return nil, fmt.Errorf("fetch summary failed: %w", err)
	}

	config := DefaultHealthScoreConfig()
	health := ComputeHealth(summary, config)

	if err := h.updateSessionHealth(ctx, gwSessionID, health); err != nil {
		return nil, fmt.Errorf("update health failed: %w", err)
	}

	sessionHealthComputedTotal.WithLabelValues(source).Inc()
	slog.Info("session health computed", "gw_session_id", gwSessionID, "score", health.HealthScore, "source", source)

	return &health, nil
}
