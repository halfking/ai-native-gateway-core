package center

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// AdminAPI Admin API Handler
type AdminAPI struct {
	server *Server
	store  Store
}

// NewAdminAPI 创建Admin API
func NewAdminAPI(server *Server, store Store) *AdminAPI {
	return &AdminAPI{
		server: server,
		store:  store,
	}
}

// RegisterRoutes 注册路由
func (a *AdminAPI) RegisterRoutes(g *echo.Group) {
	g.GET("/instances", a.ListInstances)
	g.GET("/instances/:id", a.GetInstance)
	g.DELETE("/instances/:id", a.DeleteInstance)
	g.POST("/instances/:id/command", a.IssueCommand)
	g.GET("/instances/:id/heartbeats", a.GetHeartbeats)
	g.GET("/instances/:id/status", a.GetStatus)
	g.GET("/instances/:id/runtime-metrics", a.GetRuntimeMetrics)
	g.GET("/instances/:id/runtime-alerts", a.GetRuntimeAlerts)
	g.GET("/commands/:id", a.GetCommand)
	g.GET("/commands/:id/status", a.GetCommandStatus)
	g.GET("/dashboard/stats", a.GetDashboardStats)
	g.GET("/alerts", a.ListAlerts)
	g.POST("/alerts/:id/acknowledge", a.AcknowledgeAlert)
	g.POST("/alerts/:id/resolve", a.ResolveAlert)
	g.POST("/alerts/:id/suppress", a.SuppressAlert)
}

// ListInstances 列出实例
func (a *AdminAPI) ListInstances(c echo.Context) error {
	status := c.QueryParam("status")
	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	instances, total, err := a.server.ListInstances(c.Request().Context(), status, offset, limit)
	if err != nil {
		slog.Error("list instances failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list instances failed"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  instances,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// GetInstance 获取实例详情
func (a *AdminAPI) GetInstance(c echo.Context) error {
	instanceID := c.Param("id")

	instance, err := a.server.GetInstance(c.Request().Context(), instanceID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "instance not found"})
	}

	return c.JSON(http.StatusOK, instance)
}

// DeleteInstance 删除实例
func (a *AdminAPI) DeleteInstance(c echo.Context) error {
	instanceID := c.Param("id")

	if err := a.store.DeleteInstance(c.Request().Context(), instanceID); err != nil {
		slog.Error("delete instance failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete instance failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "instance deleted"})
}

// IssueCommand 下发命令
func (a *AdminAPI) IssueCommand(c echo.Context) error {
	instanceID := c.Param("id")

	var req struct {
		Command  string            `json:"command" validate:"required"`
		Args     map[string]string `json:"args"`
		IssuedBy string            `json:"issued_by" validate:"required"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	cmd, err := a.server.IssueCommand(c.Request().Context(), instanceID, req.Command, req.Args, req.IssuedBy)
	if err != nil {
		slog.Error("issue command failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "issue command failed"})
	}

	return c.JSON(http.StatusCreated, cmd)
}

// GetHeartbeats 获取心跳历史
func (a *AdminAPI) GetHeartbeats(c echo.Context) error {
	instanceID := c.Param("id")

	since := time.Now().Add(-24 * time.Hour)
	if sinceStr := c.QueryParam("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}

	limit := 100
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	heartbeats, err := a.server.GetInstanceMetrics(c.Request().Context(), instanceID, since, limit)
	if err != nil {
		slog.Error("get heartbeats failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get heartbeats failed"})
	}

	return c.JSON(http.StatusOK, heartbeats)
}

// GetStatus 获取实例状态
func (a *AdminAPI) GetStatus(c echo.Context) error {
	instanceID := c.Param("id")

	status, err := a.store.GetLatestStatus(c.Request().Context(), instanceID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "status not found"})
	}

	return c.JSON(http.StatusOK, status)
}

// GetRuntimeMetrics returns recent runtime_metrics samples for one instance.
func (a *AdminAPI) GetRuntimeMetrics(c echo.Context) error {
	instanceID := c.Param("id")
	pgxStore, ok := a.store.(*PgxStore)
	if !ok {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "runtime metrics unavailable"})
	}

	since := time.Now().Add(-24 * time.Hour)
	if sinceStr := c.QueryParam("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}
	limit := 120
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	items, err := pgxStore.ListRuntimeMetricsForInstance(c.Request().Context(), instanceID, since, limit)
	if err != nil {
		slog.Error("list runtime metrics failed", "error", err, "instance_id", instanceID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list runtime metrics failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}

// GetRuntimeAlerts returns open/recent runtime alerts for one instance.
func (a *AdminAPI) GetRuntimeAlerts(c echo.Context) error {
	instanceID := c.Param("id")
	pgxStore, ok := a.store.(*PgxStore)
	if !ok {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "runtime alerts unavailable"})
	}
	limit := 20
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	items, err := pgxStore.ListRuntimeAlertsForInstance(c.Request().Context(), instanceID, limit)
	if err != nil {
		slog.Error("list runtime alerts failed", "error", err, "instance_id", instanceID)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list runtime alerts failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}

// GetCommand 获取命令详情
func (a *AdminAPI) GetCommand(c echo.Context) error {
	commandID := c.Param("id")

	cmd, err := a.server.GetCommandStatus(c.Request().Context(), commandID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "command not found"})
	}

	return c.JSON(http.StatusOK, cmd)
}

// GetCommandStatus 获取命令状态
func (a *AdminAPI) GetCommandStatus(c echo.Context) error {
	commandID := c.Param("id")

	cmd, err := a.server.GetCommandStatus(c.Request().Context(), commandID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "command not found"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"command_id":  cmd.CommandID,
		"status":      cmd.Status,
		"result":      cmd.Result,
		"executed_at": cmd.ExecutedAt,
	})
}

// GetDashboardStats 获取仪表盘统计
func (a *AdminAPI) GetDashboardStats(c echo.Context) error {
	stats, err := a.server.GetDashboardStats(c.Request().Context())
	if err != nil {
		slog.Error("get dashboard stats failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get dashboard stats failed"})
	}

	return c.JSON(http.StatusOK, stats)
}

// ListAlerts returns derived alerts from instance heartbeat/status and persisted runtime alerts.
func (a *AdminAPI) ListAlerts(c echo.Context) error {
	instances, _, err := a.server.ListInstances(c.Request().Context(), "", 0, 500)
	if err != nil {
		slog.Error("list alerts failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list alerts failed"})
	}

	now := time.Now()
	var alerts []OpsAlert
	for _, inst := range instances {
		age := now.Sub(inst.LastHeartbeat)
		switch inst.Status {
		case StatusOffline:
			alerts = append(alerts, OpsAlert{
				ID:         "center-offline-" + inst.InstanceID,
				Severity:   "critical",
				Title:      "实例离线",
				Message:    fmt.Sprintf("%s (%s) 超过 %d 分钟无心跳", inst.Hostname, inst.InstanceID, int(age.Minutes())),
				Source:     "center",
				Status:     "triggered",
				InstanceID: inst.InstanceID,
				DetectedAt: inst.LastHeartbeat,
			})
		case StatusDegraded:
			alerts = append(alerts, OpsAlert{
				ID:         "center-degraded-" + inst.InstanceID,
				Severity:   "warning",
				Title:      "实例降级",
				Message:    fmt.Sprintf("%s 心跳延迟 %d 秒", inst.InstanceID, int(age.Seconds())),
				Source:     "center",
				Status:     "triggered",
				InstanceID: inst.InstanceID,
				DetectedAt: inst.LastHeartbeat,
			})
		}
	}

	if pgxStore, ok := a.store.(*PgxStore); ok {
		runtimeAlerts, err := pgxStore.ListOpenRuntimeAlerts(c.Request().Context(), 200)
		if err != nil {
			slog.Error("list runtime alerts failed", "error", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list runtime alerts failed"})
		}
		for _, evt := range runtimeAlerts {
			alerts = append(alerts, runtimeAlertToOpsAlert(evt))
		}
	}

	if alerts == nil {
		alerts = []OpsAlert{}
	}
	open := 0
	for _, alert := range alerts {
		if alert.Status == "triggered" || alert.Status == "acknowledged" || alert.Status == "suppressed" {
			open++
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  alerts,
		"total":  len(alerts),
		"open":   open,
	})
}

type alertActionRequest struct {
	Actor    string `json:"actor"`
	Hours    int    `json:"hours"`
	Duration int    `json:"duration_hours"`
}

// AcknowledgeAlert acknowledges a persisted runtime alert.
func (a *AdminAPI) AcknowledgeAlert(c echo.Context) error {
	id, err := parseRuntimeAlertID(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	pgxStore, ok := a.store.(*PgxStore)
	if !ok {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "runtime alerts unavailable"})
	}
	var req alertActionRequest
	_ = c.Bind(&req)
	actor := req.Actor
	if actor == "" {
		actor = "admin"
	}
	if err := pgxStore.AcknowledgeRuntimeAlert(c.Request().Context(), id, actor); err != nil {
		slog.Error("acknowledge runtime alert failed", "error", err, "id", id)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "acknowledge failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ResolveAlert resolves a persisted runtime alert.
func (a *AdminAPI) ResolveAlert(c echo.Context) error {
	id, err := parseRuntimeAlertID(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	pgxStore, ok := a.store.(*PgxStore)
	if !ok {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "runtime alerts unavailable"})
	}
	var req alertActionRequest
	_ = c.Bind(&req)
	actor := req.Actor
	if actor == "" {
		actor = "admin"
	}
	if err := pgxStore.ResolveRuntimeAlert(c.Request().Context(), id, actor); err != nil {
		slog.Error("resolve runtime alert failed", "error", err, "id", id)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "resolve failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// SuppressAlert suppresses a persisted runtime alert for a duration.
func (a *AdminAPI) SuppressAlert(c echo.Context) error {
	id, err := parseRuntimeAlertID(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	pgxStore, ok := a.store.(*PgxStore)
	if !ok {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "runtime alerts unavailable"})
	}
	var req alertActionRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	hours := req.Hours
	if hours <= 0 {
		hours = req.Duration
	}
	if hours <= 0 {
		hours = 24
	}
	actor := req.Actor
	if actor == "" {
		actor = "admin"
	}
	until := time.Now().UTC().Add(time.Duration(hours) * time.Hour)
	if err := pgxStore.SuppressRuntimeAlert(c.Request().Context(), id, until, actor); err != nil {
		slog.Error("suppress runtime alert failed", "error", err, "id", id)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "suppress failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "suppressed_until": until})
}

func parseRuntimeAlertID(raw string) (int64, error) {
	const prefix = "runtime-"
	if !strings.HasPrefix(raw, prefix) {
		return 0, fmt.Errorf("unsupported alert id")
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(raw, prefix), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid runtime alert id")
	}
	return id, nil
}
