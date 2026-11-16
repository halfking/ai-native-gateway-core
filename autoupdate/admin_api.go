package autoupdate

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
)

// AdminAPI Admin API Handler
type AdminAPI struct {
	store      Store
	downloader *Downloader
	installer  *Installer
	rollback   *Rollback
	// checker 远端版本检索器（可空：未配置远端源时「立即检查」返回明确说明）。
	checker *Checker
	// executor 安装执行器（可空：未配置时安装端点返回明确说明）。
	executor *Executor
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

// SetChecker 注入版本检索器。
func (a *AdminAPI) SetChecker(checker *Checker) {
	a.checker = checker
}

// SetExecutor 注入安装执行器。
func (a *AdminAPI) SetExecutor(executor *Executor) {
	a.executor = executor
}

// RegisterRoutes 注册路由（认证由 Echo 桥接层统一 JWT 中间件处理）
func (a *AdminAPI) RegisterRoutes(g *echo.Group) {
	g.POST("", a.CreateRelease)
	g.GET("", a.ListReleases)
	// Static paths must be registered before /:version to avoid param capture.
	g.GET("/gray-rules", a.ListGrayRules)
	g.GET("/upgrade-logs", a.GetUpgradeLogs)
	g.POST("/rollback", a.RollbackRelease)
	// ── 自更新执行面（静态路径必须排在 /:version 之前，否则被参数路由吞掉）──
	g.POST("/check", a.CheckLatest)
	g.GET("/check-status", a.GetCheckStatus)
	g.GET("/install-status", a.GetInstallStatus)
	g.GET("/notifications", a.GetNotifications)
	g.GET("/:version", a.GetRelease)
	g.GET("/:version/rollout-status", a.GetRolloutStatus)
	g.POST("/:version/publish", a.PublishRelease)
	g.POST("/:version/unpublish", a.UnpublishRelease)
	g.GET("/:version/gray", a.GetGrayRelease)
	g.POST("/:version/gray", a.CreateGrayRelease)
	g.PATCH("/:version/gray", a.UpdateGrayPhase)
	g.POST("/:version/gray/pause", a.PauseGrayRelease)
	g.POST("/:version/gray/resume", a.ResumeGrayRelease)
	g.POST("/:version/install", a.InstallRelease)
	g.POST("/:version/source-build", a.SourceBuildRelease)
}

// CreateRelease 创建发布版本
func (a *AdminAPI) CreateRelease(c *echo.Context) error {
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
func (a *AdminAPI) ListReleases(c *echo.Context) error {
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
func (a *AdminAPI) GetRelease(c *echo.Context) error {
	version := c.Param("version")

	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}

	return c.JSON(http.StatusOK, rel)
}

// PublishRelease 发布版本
func (a *AdminAPI) PublishRelease(c *echo.Context) error {
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
func (a *AdminAPI) UnpublishRelease(c *echo.Context) error {
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
func (a *AdminAPI) CreateGrayRelease(c *echo.Context) error {
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
func (a *AdminAPI) UpdateGrayPhase(c *echo.Context) error {
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

	currentRule, _ := a.store.GetGrayRule(c.Request().Context(), rel.ID)
	if currentRule != nil && req.Percent > currentRule.Percent {
		stats, statsErr := a.store.GetRolloutStats(c.Request().Context(), version)
		if statsErr != nil {
			slog.Error("rollout stats lookup failed", "error", statsErr, "version", version)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "rollout stats lookup failed"})
		}
		gate := EvaluateRolloutGate(stats)
		if !gate.Allowed {
			if pauseErr := a.store.UpdateGrayRuleStatus(c.Request().Context(), rel.ID, "paused"); pauseErr != nil {
				slog.Error("auto pause gray rule failed", "error", pauseErr, "version", version)
			}
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":  "rollout gate blocked phase advance",
				"reason": gate.Reason,
				"gate":   gate,
			})
		}
	}

	if err := a.store.UpdateGrayPhase(c.Request().Context(), rel.ID, req.Phase, req.Percent); err != nil {
		slog.Error("update gray phase failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update gray phase failed"})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "gray phase updated"})
}

// GetRolloutStatus returns rollout gate evaluation for a release version.
func (a *AdminAPI) GetRolloutStatus(c *echo.Context) error {
	version := c.Param("version")
	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}
	stats, err := a.store.GetRolloutStats(c.Request().Context(), version)
	if err != nil {
		slog.Error("rollout stats lookup failed", "error", err, "version", version)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "rollout stats lookup failed"})
	}
	gate := EvaluateRolloutGate(stats)
	ruleStatus := ""
	if rule, ruleErr := a.store.GetGrayRule(c.Request().Context(), rel.ID); ruleErr == nil && rule != nil {
		ruleStatus = rule.Status
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"version":     version,
		"rule_status": ruleStatus,
		"gate":        gate,
	})
}

// PauseGrayRelease pauses an active gray rollout.
func (a *AdminAPI) PauseGrayRelease(c *echo.Context) error {
	version := c.Param("version")
	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}
	if err := a.store.UpdateGrayRuleStatus(c.Request().Context(), rel.ID, "paused"); err != nil {
		slog.Error("pause gray rule failed", "error", err, "version", version)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "pause gray rule failed"})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "gray rollout paused"})
}

// ResumeGrayRelease resumes a paused gray rollout after gate re-check.
func (a *AdminAPI) ResumeGrayRelease(c *echo.Context) error {
	version := c.Param("version")
	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}
	stats, err := a.store.GetRolloutStats(c.Request().Context(), version)
	if err != nil {
		slog.Error("rollout stats lookup failed", "error", err, "version", version)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "rollout stats lookup failed"})
	}
	gate := EvaluateRolloutGate(stats)
	if !gate.Allowed {
		return c.JSON(http.StatusConflict, map[string]interface{}{
			"error":  "rollout gate still blocking resume",
			"reason": gate.Reason,
			"gate":   gate,
		})
	}
	if err := a.store.UpdateGrayRuleStatus(c.Request().Context(), rel.ID, "active"); err != nil {
		slog.Error("resume gray rule failed", "error", err, "version", version)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "resume gray rule failed"})
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "gray rollout resumed"})
}

// ListGrayRules lists all gray rollout rules.
func (a *AdminAPI) ListGrayRules(c *echo.Context) error {
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	rules, err := a.store.ListGrayRules(c.Request().Context(), limit)
	if err != nil {
		slog.Error("list gray rules failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list gray rules failed"})
	}
	if rules == nil {
		rules = []GrayReleaseRuleView{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": rules, "total": len(rules)})
}

// GetGrayRelease returns the active gray rule for a release version.
func (a *AdminAPI) GetGrayRelease(c *echo.Context) error {
	version := c.Param("version")
	rel, err := a.store.GetRelease(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "release not found"})
	}
	rule, err := a.store.GetGrayRule(c.Request().Context(), rel.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "gray rule not found"})
	}
	return c.JSON(http.StatusOK, rule)
}

// GetUpgradeLogs 获取升级日志
func (a *AdminAPI) GetUpgradeLogs(c *echo.Context) error {
	instanceID := c.QueryParam("instance_id")
	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	history, total, err := a.store.GetUpgradeHistory(c.Request().Context(), instanceID, offset, limit)
	if err != nil {
		slog.Error("get upgrade history failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get upgrade history failed"})
	}

	if history == nil {
		history = []ReleaseStatus{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":  history,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// RollbackRelease 回滚版本
func (a *AdminAPI) RollbackRelease(c *echo.Context) error {
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

// ── 自更新执行面 ────────────────────────────────────────────────────────

// CheckLatest 立即向远端发布源检索最新版本。
//
// 这是「手动检查」入口；后台周期检索由 Checker 负责，两者共用同一个 Checker 实例，
// 所以手动检查产生的提醒与自动检查产生的提醒是同一套记录，不会重复。
func (a *AdminAPI) CheckLatest(c *echo.Context) error {
	if a.checker == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "version check is not configured on this node",
		})
	}
	result := a.checker.CheckOnce(c.Request().Context())
	if result.Error != "" {
		return c.JSON(http.StatusBadGateway, map[string]interface{}{
			"error":  "version check failed",
			"detail": result.Error,
			"result": result,
		})
	}
	return c.JSON(http.StatusOK, result)
}

// GetCheckStatus 返回最近一次版本检索结论。
func (a *AdminAPI) GetCheckStatus(c *echo.Context) error {
	if a.checker == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"configured": false,
		})
	}
	result := a.checker.LastResult()
	if result == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"configured":      true,
			"checked":         false,
			"interval_secs":   int64(a.checker.Interval().Seconds()),
			"current_version": "",
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"configured":    true,
		"checked":       true,
		"interval_secs": int64(a.checker.Interval().Seconds()),
		"result":        result,
	})
}

// GetInstallStatus 返回当前/最近一次安装任务快照。
func (a *AdminAPI) GetInstallStatus(c *echo.Context) error {
	if a.executor == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"configured": false,
		})
	}
	job := a.executor.Current()
	if job == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"configured": true,
			"running":    false,
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"configured": true,
		"running":    !isTerminalStatus(job.Status),
		"job":        job,
	})
}

// GetNotifications 列出「有新版待处理」的提醒。
//
// 提醒 = upgrade_logs 里仍处于**可行动**状态的行（pending / failed / rolled_back）。
//
// ★ 为什么不只取 pending：
//
//	回滚（装了但服务起不来 ⇒ 已还原成旧二进制）之后，新版**仍然待处理**——
//	节点还停在旧版本上，正是最需要提醒的场景。此时那一行的状态是 rolled_back，
//	若只筛 pending，提醒就彻底消失了 ⇒ 再也没人会来升这一级。
//	同理，failed（下载失败/校验失败）也仍然待处理。
//	只有 success（真的装成了）才算告一段落。
func (a *AdminAPI) GetNotifications(c *echo.Context) error {
	if a.store == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{"items": []any{}, "total": 0})
	}
	instanceID := c.QueryParam("instance_id")
	offset := 0
	limit := 50
	_, _ = fmt.Sscanf(c.QueryParam("offset"), "%d", &offset)
	_, _ = fmt.Sscanf(c.QueryParam("limit"), "%d", &limit)

	history, historyTotal, err := a.store.GetUpgradeHistory(c.Request().Context(), instanceID, offset, limit)
	if err != nil {
		slog.Error("get notifications failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "get notifications failed"})
	}

	pending := make([]ReleaseStatus, 0, len(history))
	for _, h := range history {
		if IsActionableStatus(h.Status) {
			pending = append(pending, h)
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":         pending,
		"total":         len(pending),
		"history_total": historyTotal,
	})
}

// InstallRelease 执行一次「点击安装升级」（下载制品 → 校验 → 替换 → 迁移 → 重启）。
//
// 立即返回任务快照；实际安装在后台 goroutine 里跑。
func (a *AdminAPI) InstallRelease(c *echo.Context) error {
	if a.executor == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "install executor is not configured on this node",
		})
	}
	version := c.Param("version")

	var req InstallRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	// URL 上的版本是权威，body 里的版本只是冗余。
	req.Version = version
	if req.Mode == "" {
		req.Mode = InstallModeArtifact
	}

	job, err := a.executor.Start(c.Request().Context(), req)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"job": job,
	})
}

// SourceBuildRelease 从本地源码编译出二进制并安装（即「本地自动编译然后启动」）。
func (a *AdminAPI) SourceBuildRelease(c *echo.Context) error {
	if a.executor == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "install executor is not configured on this node",
		})
	}
	version := c.Param("version")

	var req InstallRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	req.Version = version
	req.Mode = InstallModeSource

	job, err := a.executor.Start(c.Request().Context(), req)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]interface{}{
			"error": err.Error(),
		})
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"job": job,
	})
}
