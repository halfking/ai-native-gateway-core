package release

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Handler HTTP处理器
type Handler struct {
	service *Service
}

// NewHandler 创建处理器
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	// 公开API（无需认证）
	public := r.Group("/releases")
	{
		public.GET("", h.ListReleases)
		public.GET("/latest", h.GetLatestRelease)
		public.GET("/:id", h.GetRelease)
		public.GET("/:id/files", h.GetFiles)
		public.POST("/:id/download", h.RecordDownload)
	}

	// 管理API（需要认证）
	admin := r.Group("/admin/releases")
	// admin.Use(middleware.RequireAdmin()) // 添加认证中间件
	{
		admin.POST("", h.CreateRelease)
		admin.POST("/:id/publish", h.PublishRelease)
		admin.POST("/:id/deprecate", h.DeprecateRelease)
		admin.POST("/:id/files", h.AddFile)
		admin.POST("/:id/tests", h.AddTest)
		admin.GET("/:id/tests", h.GetTests)
	}
}

// CreateRelease 创建版本
// @Summary 创建版本
// @Tags releases
// @Accept json
// @Produce json
// @Param request body CreateReleaseRequest true "创建版本请求"
// @Success 200 {object} Release
// @Router /admin/releases [post]
func (h *Handler) CreateRelease(c *gin.Context) {
	var req CreateReleaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 从上下文获取当前用户（这里简化处理）
	createdBy := c.GetString("user_id")
	if createdBy == "" {
		createdBy = "system"
	}

	release, err := h.service.CreateRelease(c.Request.Context(), req, createdBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": release})
}

// GetRelease 获取版本详情
// @Summary 获取版本详情
// @Tags releases
// @Produce json
// @Param id path int true "版本ID"
// @Success 200 {object} Release
// @Router /releases/{id} [get]
func (h *Handler) GetRelease(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	release, err := h.service.GetRelease(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": release})
}

// GetLatestRelease 获取最新版本
// @Summary 获取最新版本
// @Tags releases
// @Produce json
// @Param is_public query bool false "是否公开版本"
// @Success 200 {object} Release
// @Router /releases/latest [get]
func (h *Handler) GetLatestRelease(c *gin.Context) {
	isPublic := c.DefaultQuery("is_public", "true") == "true"

	release, err := h.service.GetLatestRelease(c.Request.Context(), isPublic)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": release})
}

// ListReleases 列表查询
// @Summary 列表查询
// @Tags releases
// @Produce json
// @Param status query string false "状态"
// @Param release_type query string false "发布类型"
// @Param is_public query bool false "是否公开"
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {object} object
// @Router /releases [get]
func (h *Handler) ListReleases(c *gin.Context) {
	var query ListReleasesQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	releases, total, err := h.service.ListReleases(c.Request.Context(), query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": releases,
		"pagination": gin.H{
			"total":     total,
			"page":      query.Page,
			"page_size": query.PageSize,
		},
	})
}

// PublishRelease 发布版本
// @Summary 发布版本
// @Tags releases
// @Produce json
// @Param id path int true "版本ID"
// @Success 200 {object} object
// @Router /admin/releases/{id}/publish [post]
func (h *Handler) PublishRelease(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	updatedBy := c.GetString("user_id")
	if updatedBy == "" {
		updatedBy = "system"
	}

	err = h.service.PublishRelease(c.Request.Context(), id, updatedBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "release published successfully"})
}

// DeprecateRelease 废弃版本
// @Summary 废弃版本
// @Tags releases
// @Produce json
// @Param id path int true "版本ID"
// @Success 200 {object} object
// @Router /admin/releases/{id}/deprecate [post]
func (h *Handler) DeprecateRelease(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	updatedBy := c.GetString("user_id")
	if updatedBy == "" {
		updatedBy = "system"
	}

	err = h.service.DeprecateRelease(c.Request.Context(), id, updatedBy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "release deprecated successfully"})
}

// AddFile 添加文件
// @Summary 添加文件
// @Tags releases
// @Accept json
// @Produce json
// @Param id path int true "版本ID"
// @Param request body AddFileRequest true "添加文件请求"
// @Success 200 {object} ReleaseFile
// @Router /admin/releases/{id}/files [post]
func (h *Handler) AddFile(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req AddFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	file, err := h.service.AddFile(c.Request.Context(), id, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": file})
}

// GetFiles 获取文件列表
// @Summary 获取文件列表
// @Tags releases
// @Produce json
// @Param id path int true "版本ID"
// @Success 200 {object} object
// @Router /releases/{id}/files [get]
func (h *Handler) GetFiles(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	files, err := h.service.GetFiles(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": files})
}

// RecordDownload 记录下载
// @Summary 记录下载
// @Tags releases
// @Produce json
// @Param id path int true "版本ID"
// @Param file_id query int false "文件ID"
// @Success 200 {object} object
// @Router /releases/{id}/download [post]
func (h *Handler) RecordDownload(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	fileID, _ := strconv.ParseInt(c.Query("file_id"), 10, 64)

	err = h.service.RecordDownload(c.Request.Context(), id, fileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "download recorded"})
}

// AddTest 添加测试记录
// @Summary 添加测试记录
// @Tags releases
// @Accept json
// @Produce json
// @Param id path int true "版本ID"
// @Param request body AddTestRequest true "添加测试请求"
// @Success 200 {object} ReleaseTest
// @Router /admin/releases/{id}/tests [post]
func (h *Handler) AddTest(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req AddTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	test, err := h.service.AddTest(c.Request.Context(), id, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": test})
}

// GetTests 获取测试列表
// @Summary 获取测试列表
// @Tags releases
// @Produce json
// @Param id path int true "版本ID"
// @Success 200 {object} object
// @Router /admin/releases/{id}/tests [get]
func (h *Handler) GetTests(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	tests, err := h.service.GetTests(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": tests})
}
