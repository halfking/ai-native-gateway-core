package vibecoding

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/labstack/echo/v4"
)

// AdminAPI Admin API Handler
type AdminAPI struct {
	projectManager *ProjectManager
	sessionManager *SessionManager
	reviewManager  *ReviewManager
}

// NewAdminAPI 创建Admin API
func NewAdminAPI(projectManager *ProjectManager, sessionManager *SessionManager, reviewManager *ReviewManager) *AdminAPI {
	return &AdminAPI{
		projectManager: projectManager,
		sessionManager: sessionManager,
		reviewManager:  reviewManager,
	}
}

// RegisterRoutes 注册路由
func (a *AdminAPI) RegisterRoutes(g *echo.Group) {
	// Project routes
	g.POST("/projects", a.CreateProject)
	g.GET("/projects", a.ListProjects)
	g.GET("/projects/:id", a.GetProject)
	g.PUT("/projects/:id", a.UpdateProject)
	g.POST("/projects/:id/archive", a.ArchiveProject)
	g.DELETE("/projects/:id", a.DeleteProject)
	g.GET("/projects/stats", a.GetProjectStats)

	// Session routes
	g.POST("/sessions", a.CreateSession)
	g.GET("/sessions", a.ListSessions)
	g.GET("/sessions/:id", a.GetSession)
	g.POST("/sessions/:id/messages", a.AddMessage)
	g.POST("/sessions/:id/complete", a.CompleteSession)
	g.POST("/sessions/:id/cancel", a.CancelSession)
	g.GET("/sessions/stats", a.GetSessionStats)

	// Review routes
	g.POST("/reviews", a.CreateReview)
	g.GET("/reviews", a.ListReviews)
	g.GET("/reviews/:id", a.GetReview)
	g.GET("/reviews/stats", a.GetReviewStats)
}

// CreateProject 创建项目
func (a *AdminAPI) CreateProject(c echo.Context) error {
	var req struct {
		Name        string `json:"name" validate:"required"`
		Description string `json:"description"`
		Language    string `json:"language"`
		Framework   string `json:"framework"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	auth := admin.GetAuthContext(c.Request())
	if auth == nil || auth.Username == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}
	project, err := a.projectManager.CreateProject(
		c.Request().Context(),
		admin.EffectiveTenantID(c.Request()), req.Name, req.Description, req.Language, req.Framework, auth.Username,
	)
	if err != nil {
		slog.Error("create project failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create project failed"})
	}

	return c.JSON(http.StatusCreated, project)
}

// ListProjects 列出项目
func (a *AdminAPI) ListProjects(c echo.Context) error {
	tenantID := c.QueryParam("tenant_id")
	status := ProjectStatus(c.QueryParam("status"))
	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	projects, total, err := a.projectManager.ListProjects(c.Request().Context(), tenantID, status, offset, limit)
	if err != nil {
		slog.Error("list projects failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list projects failed"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  projects,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// GetProject 获取项目详情
func (a *AdminAPI) GetProject(c echo.Context) error {
	var id int64
	_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)

	project, err := a.projectManager.GetProject(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
	}

	return c.JSON(http.StatusOK, project)
}

// UpdateProject 更新项目
func (a *AdminAPI) UpdateProject(c echo.Context) error {
	var id int64
	_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)

	project, err := a.projectManager.GetProject(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
	}

	var req struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Language    string                 `json:"language"`
		Framework   string                 `json:"framework"`
		Status      ProjectStatus          `json:"status"`
		Settings    map[string]interface{} `json:"settings"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	if req.Name != "" {
		project.Name = req.Name
	}
	if req.Description != "" {
		project.Description = req.Description
	}
	if req.Language != "" {
		project.Language = req.Language
	}
	if req.Framework != "" {
		project.Framework = req.Framework
	}
	if req.Status != "" {
		project.Status = req.Status
	}
	if req.Settings != nil {
		project.Settings = req.Settings
	}

	if err := a.projectManager.UpdateProject(c.Request().Context(), project); err != nil {
		slog.Error("update project failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update project failed"})
	}

	return c.JSON(http.StatusOK, project)
}

// ArchiveProject 归档项目
func (a *AdminAPI) ArchiveProject(c echo.Context) error {
	var id int64
	_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)

	if err := a.projectManager.ArchiveProject(c.Request().Context(), id); err != nil {
		slog.Error("archive project failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "archive project failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "project archived"})
}

// DeleteProject 删除项目
func (a *AdminAPI) DeleteProject(c echo.Context) error {
	var id int64
	_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)

	if err := a.projectManager.DeleteProject(c.Request().Context(), id); err != nil {
		slog.Error("delete project failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete project failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "project deleted"})
}

// GetProjectStats 获取项目统计
func (a *AdminAPI) GetProjectStats(c echo.Context) error {
	tenantID := c.QueryParam("tenant_id")

	stats, err := a.projectManager.GetProjectStats(c.Request().Context(), tenantID)
	if err != nil {
		slog.Error("get project stats failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get project stats failed"})
	}

	return c.JSON(http.StatusOK, stats)
}

// CreateSession 创建会话
func (a *AdminAPI) CreateSession(c echo.Context) error {
	var req struct {
		TaskType  string `json:"task_type" validate:"required"`
		ProjectID *int64 `json:"project_id"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	if admin.GetAuthContext(c.Request()) == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}
	session, err := a.sessionManager.CreateSession(c.Request().Context(), admin.EffectiveTenantID(c.Request()), req.TaskType, req.ProjectID)
	if err != nil {
		slog.Error("create session failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create session failed"})
	}

	return c.JSON(http.StatusCreated, session)
}

// ListSessions 列出会话
func (a *AdminAPI) ListSessions(c echo.Context) error {
	var projectID *int64
	if pidStr := c.QueryParam("project_id"); pidStr != "" {
		var pid int64
		_, _ = fmt.Sscanf(pidStr, "%d", &pid)
		projectID = &pid
	}

	status := SessionStatus(c.QueryParam("status"))
	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	sessions, total, err := a.sessionManager.ListSessions(c.Request().Context(), projectID, status, offset, limit)
	if err != nil {
		slog.Error("list sessions failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list sessions failed"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  sessions,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// GetSession 获取会话详情
func (a *AdminAPI) GetSession(c echo.Context) error {
	var id int64
	_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)

	session, err := a.sessionManager.GetSession(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}

	return c.JSON(http.StatusOK, session)
}

// AddMessage 添加消息
func (a *AdminAPI) AddMessage(c echo.Context) error {
	sessionID := c.Param("id")

	var req struct {
		Role    string `json:"role" validate:"required"`
		Content string `json:"content" validate:"required"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	if err := a.sessionManager.AddMessage(c.Request().Context(), sessionID, req.Role, req.Content); err != nil {
		slog.Error("add message failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "add message failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "message added"})
}

// CompleteSession 完成会话
func (a *AdminAPI) CompleteSession(c echo.Context) error {
	sessionID := c.Param("id")

	if err := a.sessionManager.CompleteSession(c.Request().Context(), sessionID); err != nil {
		slog.Error("complete session failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "complete session failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "session completed"})
}

// CancelSession 取消会话
func (a *AdminAPI) CancelSession(c echo.Context) error {
	sessionID := c.Param("id")

	if err := a.sessionManager.CancelSession(c.Request().Context(), sessionID); err != nil {
		slog.Error("cancel session failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "cancel session failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "session cancelled"})
}

// GetSessionStats 获取会话统计
func (a *AdminAPI) GetSessionStats(c echo.Context) error {
	var projectID *int64
	if pidStr := c.QueryParam("project_id"); pidStr != "" {
		var pid int64
		_, _ = fmt.Sscanf(pidStr, "%d", &pid)
		projectID = &pid
	}

	stats, err := a.sessionManager.GetSessionStats(c.Request().Context(), projectID)
	if err != nil {
		slog.Error("get session stats failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get session stats failed"})
	}

	return c.JSON(http.StatusOK, stats)
}

// CreateReview 创建代码审查
func (a *AdminAPI) CreateReview(c echo.Context) error {
	var req struct {
		SessionID *int64 `json:"session_id"`
		TenantID  string `json:"tenant_id" validate:"required"`
		FilePath  string `json:"file_path"`
		Language  string `json:"language" validate:"required"`
		Code      string `json:"code" validate:"required"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	review, err := a.reviewManager.CreateReview(
		c.Request().Context(),
		req.SessionID, req.TenantID, req.FilePath, req.Language, req.Code,
	)
	if err != nil {
		slog.Error("create review failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create review failed"})
	}

	return c.JSON(http.StatusCreated, review)
}

// ListReviews 列出代码审查
func (a *AdminAPI) ListReviews(c echo.Context) error {
	var sessionID *int64
	if sidStr := c.QueryParam("session_id"); sidStr != "" {
		var sid int64
		_, _ = fmt.Sscanf(sidStr, "%d", &sid)
		sessionID = &sid
	}

	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	reviews, total, err := a.reviewManager.ListReviews(c.Request().Context(), sessionID, offset, limit)
	if err != nil {
		slog.Error("list reviews failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list reviews failed"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  reviews,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// GetReview 获取代码审查详情
func (a *AdminAPI) GetReview(c echo.Context) error {
	var id int64
	_, _ = fmt.Sscanf(c.Param("id"), "%d", &id)

	review, err := a.reviewManager.GetReview(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "review not found"})
	}

	return c.JSON(http.StatusOK, review)
}

// GetReviewStats 获取审查统计
func (a *AdminAPI) GetReviewStats(c echo.Context) error {
	var sessionID *int64
	if sidStr := c.QueryParam("session_id"); sidStr != "" {
		var sid int64
		_, _ = fmt.Sscanf(sidStr, "%d", &sid)
		sessionID = &sid
	}

	stats, err := a.reviewManager.GetReviewStats(c.Request().Context(), sessionID)
	if err != nil {
		slog.Error("get review stats failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get review stats failed"})
	}

	return c.JSON(http.StatusOK, stats)
}
