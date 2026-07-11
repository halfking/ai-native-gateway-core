package center

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
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
	g.GET("/commands/:id", a.GetCommand)
	g.GET("/commands/:id/status", a.GetCommandStatus)
	g.GET("/dashboard/stats", a.GetDashboardStats)
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
		Command string            `json:"command" validate:"required"`
		Args    map[string]string `json:"args"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	auth := admin.GetAuthContext(c.Request())
	if auth == nil || auth.Username == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}

	cmd, err := a.server.IssueCommand(c.Request().Context(), instanceID, req.Command, req.Args, auth.Username)
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
