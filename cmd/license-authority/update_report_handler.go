package main

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
)

// UpdateReport 升级结果上报请求
type UpdateReport struct {
	InstanceID  string `json:"instance_id"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Status      string `json:"status"` // success / rolled_back / failed
	DurationMS  int    `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
}

// UpdateReportHandler 升级结果上报处理器
type UpdateReportHandler struct {
	updateStore autoupdate.Store
}

// NewUpdateReportHandler 创建升级结果上报处理器
func NewUpdateReportHandler(updateStore autoupdate.Store) *UpdateReportHandler {
	return &UpdateReportHandler{
		updateStore: updateStore,
	}
}

// HandleReport 处理 POST /api/v1/updates/report
func (h *UpdateReportHandler) HandleReport(c echo.Context) error {
	var report UpdateReport
	if err := c.Bind(&report); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	// 验证必填字段
	if report.InstanceID == "" || report.ToVersion == "" || report.Status == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing required fields")
	}
	if subject, ok := c.Get("instance_id").(string); ok && subject != "" {
		const prefix = "instance:"
		if strings.HasPrefix(subject, prefix) {
			subject = strings.TrimPrefix(subject, prefix)
		}
		if report.InstanceID != subject {
			return echo.NewHTTPError(http.StatusForbidden, "instance identity mismatch")
		}
	}

	// 验证 status 枚举值
	validStatuses := map[string]bool{
		autoupdate.StatusSuccess:  true,
		autoupdate.StatusRollback: true,
		autoupdate.StatusFailed:   true,
	}
	if !validStatuses[report.Status] {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid status value")
	}

	// 记录升级结果
	if err := h.updateStore.RecordUpdateReport(c.Request().Context(), &autoupdate.UpdateReportData{
		InstanceID:  report.InstanceID,
		FromVersion: report.FromVersion,
		ToVersion:   report.ToVersion,
		Status:      report.Status,
		DurationMS:  report.DurationMS,
		Error:       report.Error,
	}); err != nil {
		slog.Error("failed to record update report",
			"error", err,
			"instance_id", report.InstanceID,
			"from_version", report.FromVersion,
			"to_version", report.ToVersion,
			"status", report.Status)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to record update report")
	}

	slog.Info("update report recorded",
		"instance_id", report.InstanceID,
		"from_version", report.FromVersion,
		"to_version", report.ToVersion,
		"status", report.Status,
		"duration_ms", report.DurationMS)

	return c.JSON(http.StatusOK, map[string]bool{"ack": true})
}

// RegisterRoutes 注册路由
func (h *UpdateReportHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/report", h.HandleReport)
}
