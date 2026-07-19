package distribution

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type AdminAPI struct {
	store Store
}

func NewAdminAPI(store Store) *AdminAPI {
	return &AdminAPI{store: store}
}

func (api *AdminAPI) RegisterRoutes(g *echo.Group) {
	g.GET("/stats", api.GetStats)
	g.GET("/license-holders", api.ListHolders)
	g.GET("/license-holders/:id", api.GetHolder)
	g.GET("/artifacts", api.ListArtifacts)
	g.POST("/artifacts", api.UpsertArtifact)
}

func (api *AdminAPI) GetStats(c echo.Context) error {
	st, err := api.store.GetDownloadStats(c.Request().Context())
	if err != nil {
		slog.Error("download stats failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "stats unavailable"})
	}
	return c.JSON(http.StatusOK, st)
}

func (api *AdminAPI) ListHolders(c echo.Context) error {
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 {
		limit = 50
	}
	holders, total, err := api.store.ListHolders(c.Request().Context(), offset, limit, c.QueryParam("query"))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"holders": holders,
		"total":   total,
		"offset":  offset,
		"limit":   limit,
	})
}

func (api *AdminAPI) GetHolder(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	h, err := api.store.GetHolder(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, h)
}

func (api *AdminAPI) ListArtifacts(c echo.Context) error {
	version := c.QueryParam("version")
	if version == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "version required"})
	}
	items, err := api.store.ListArtifacts(c.Request().Context(), version)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	if items == nil {
		items = []ReleaseArtifact{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": items, "version": version})
}

func (api *AdminAPI) UpsertArtifact(c echo.Context) error {
	var req ReleaseArtifact
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	if req.ReleaseVersion == "" || req.Platform == "" || req.ArtifactName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "release_version, platform, artifact_name required"})
	}
	if req.Edition == "" {
		req.Edition = "customer"
	}
	if err := api.store.UpsertArtifact(c.Request().Context(), &req); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "upsert failed"})
	}
	return c.JSON(http.StatusCreated, req)
}
