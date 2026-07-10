package autoupdate

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

// AdminAPI Admin API Handler
type AdminAPI struct {
	store      Store
	downloader *Downloader
	installer  *Installer
	rollback   *Rollback
}

// NewAdminAPI 创建Admin API
func NewAdminAPI(store Store, downloader *Downloader, installer *Installer, rollback *Rollback) *AdminAPI {
	return &AdminAPI{
		store:      store,
		downloader: downloader,
		installer:  installer,
		rollback:   rollback,
	}
}

// RegisterRoutes 注册路由
func (a *AdminAPI) RegisterRoutes(g *echo.Group) {
	g.POST("/releases", a.CreateRelease)
	g.GET("/releases", a.ListReleases)
	g.GET("/releases/:version", a.GetRelease)
	g.POST("/releases/:version/publish", a.PublishRelease)
	g.POST("/releases/:version/unpublish", a.UnpublishRelease)
	g.POST("/releases/:version/gray", a.CreateGrayRelease)
	g.PATCH("/releases/:version/gray", a.UpdateGrayPhase)
	g.GET("/upgrade-logs", a.GetUpgradeLogs)
	g.POST("/rollback", a.RollbackRelease)
}

// CreateRelease 创建发布版本
func (a *AdminAPI) CreateRelease(c echo.Context) error {
	var req struct {
		Version     string  `json:"version" validate:"required"`
		BuildSeq    int     `json:"build_seq" validate:"required"`
		Channel     Channel `json:"channel" validate:"required"`
		Title       string  `json:"title" validate:"required"`
		Description string  `json:"description"`
		Changelog   string  `json:"changelog"`
		ImageTag    string  `json:"image_tag" validate:"required"`
		ImageDigest string  `json:"image_digest"`
		MinVersion  string  `json:"min_version"`
		Mandatory   bool    `json:"mandatory"`
		CreatedBy   string  `json:"created_by" validate:"required"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	// 验证版本号格式
	if err := ValidateVersion(req.Version); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	rel := &Release{
		Version:     req.Version,
		BuildSeq:    req.BuildSeq,
		Channel:     req.Channel,
		Title:       req.Title,
		Description: req.Description,
		Changelog:   req.Changelog,
		ImageTag:    req.ImageTag,
		ImageDigest: req.ImageDigest,
		MinVersion:  req.MinVersion,
		Mandatory:   req.Mandatory,
		CreatedBy:   req.CreatedBy,
	}

	if err := a.store.CreateRelease(c.Request().Context(), rel); err != nil {
		slog.Error("create release failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create release failed"})
	}

	return c.JSON(http.StatusCreated, rel)
}

// ListReleases 列出发布版本
func (a *AdminAPI) ListReleases(c echo.Context) error {
	channel := Channel(c.QueryParam("channel"))
	if channel == "" {
		channel = ChannelStable
	}

	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	releases, total, err := a.store.ListReleases(c.Request().Context(), channel, offset, limit)
	if err != nil {
		slog.Error("list releases failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list releases failed"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  releases,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// GetRelease 获取发布版本详情
func (a *AdminAPI) GetRelease(c echo.Context) error {
	version := c.Param("version")

	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}

	return c.JSON(http.StatusOK, rel)
}

// PublishRelease 发布版本
func (a *AdminAPI) PublishRelease(c echo.Context) error {
	version := c.Param("version")

	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}

	if err := a.store.UpdateReleaseStatus(c.Request().Context(), rel.ID, true); err != nil {
		slog.Error("publish release failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "publish failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "release published"})
}

// UnpublishRelease 取消发布
func (a *AdminAPI) UnpublishRelease(c echo.Context) error {
	version := c.Param("version")

	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}

	if err := a.store.UpdateReleaseStatus(c.Request().Context(), rel.ID, false); err != nil {
		slog.Error("unpublish release failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unpublish failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "release unpublished"})
}

// CreateGrayRelease 创建灰度发布
func (a *AdminAPI) CreateGrayRelease(c echo.Context) error {
	version := c.Param("version")

	var req struct {
		Phase     Phase  `json:"phase" validate:"required"`
		Percent   int    `json:"percent" validate:"required,min=0,max=100"`
		Selectors []byte `json:"selectors"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}

	rule := &GrayReleaseRule{
		ReleaseID: rel.ID,
		Phase:     req.Phase,
		Percent:   req.Percent,
		Selectors: req.Selectors,
		Status:    "active",
	}

	if err := a.store.CreateGrayRule(c.Request().Context(), rule); err != nil {
		slog.Error("create gray rule failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create gray rule failed"})
	}

	return c.JSON(http.StatusCreated, rule)
}

// UpdateGrayPhase 更新灰度阶段
func (a *AdminAPI) UpdateGrayPhase(c echo.Context) error {
	version := c.Param("version")

	var req struct {
		Phase   Phase `json:"phase" validate:"required"`
		Percent int   `json:"percent" validate:"required,min=0,max=100"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}

	if err := a.store.UpdateGrayPhase(c.Request().Context(), rel.ID, req.Phase, req.Percent); err != nil {
		slog.Error("update gray phase failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update gray phase failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "gray phase updated"})
}

// GetUpgradeLogs 获取升级日志
func (a *AdminAPI) GetUpgradeLogs(c echo.Context) error {
	instanceID := c.QueryParam("instance_id")
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	history, err := a.store.GetUpgradeHistory(c.Request().Context(), instanceID, limit)
	if err != nil {
		slog.Error("get upgrade history failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get upgrade history failed"})
	}

	return c.JSON(http.StatusOK, history)
}

// RollbackRelease 回滚版本
func (a *AdminAPI) RollbackRelease(c echo.Context) error {
	var req struct {
		TargetVersion string `json:"target_version" validate:"required"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	// 执行回滚
	result, err := a.rollback.Rollback(c.Request().Context(), req.TargetVersion)
	if err != nil {
		slog.Error("rollback failed", "error", err, "target_version", req.TargetVersion)
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":  "rollback failed",
			"detail": result,
		})
	}

	return c.JSON(http.StatusOK, result)
}
