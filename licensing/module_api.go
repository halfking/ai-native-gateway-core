package licensing

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

type ModuleAdminHandler struct {
	store Store
}

func NewModuleAdminHandler(store Store) *ModuleAdminHandler {
	return &ModuleAdminHandler{store: store}
}

func (h *ModuleAdminHandler) RegisterRoutes(g *echo.Group) {
	g.GET("/modules", h.ListProductModules)
	g.GET("/tiers", h.ListTiers)
	g.GET("/licenses/:id/modules", h.ListLicenseModules)
	g.POST("/licenses/:id/modules", h.UpsertLicenseModule)
	g.DELETE("/licenses/:id/modules/:key", h.DeleteLicenseModule)
}

func (h *ModuleAdminHandler) ListProductModules(c echo.Context) error {
	modules, err := h.store.ListProductModules(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	features, err := h.store.ListProductModuleFeatures(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// Group features by module_key
	featuresByModule := make(map[string][]ProductModuleFeature)
	for _, f := range features {
		featuresByModule[f.ModuleKey] = append(featuresByModule[f.ModuleKey], f)
	}

	type moduleWithFeatures struct {
		ProductModule
		Features []ProductModuleFeature `json:"features"`
	}

	result := make([]moduleWithFeatures, 0, len(modules))
	for _, m := range modules {
		result = append(result, moduleWithFeatures{
			ProductModule: m,
			Features:      featuresByModule[m.Key],
		})
	}

	if result == nil {
		result = []moduleWithFeatures{}
	}

	return c.JSON(http.StatusOK, result)
}

func (h *ModuleAdminHandler) ListTiers(c echo.Context) error {
	tiers, err := h.store.ListSubscriptionTiers(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	maps, err := h.store.ListTierModuleMaps(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	// Group module keys by tier
	modulesByTier := make(map[string][]string)
	for _, m := range maps {
		modulesByTier[m.TierCode] = append(modulesByTier[m.TierCode], m.ModuleKey)
	}

	type tierWithModules struct {
		SubscriptionTier
		ModuleKeys []string `json:"module_keys"`
	}

	result := make([]tierWithModules, 0, len(tiers))
	for _, t := range tiers {
		keys := modulesByTier[t.Code]
		if keys == nil {
			keys = []string{}
		}
		result = append(result, tierWithModules{
			SubscriptionTier: t,
			ModuleKeys:       keys,
		})
	}

	return c.JSON(http.StatusOK, result)
}

func (h *ModuleAdminHandler) ListLicenseModules(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}

	mods, err := h.store.ListLicenseModulesByID(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if mods == nil {
		mods = []LicenseModule{}
	}

	return c.JSON(http.StatusOK, mods)
}

func (h *ModuleAdminHandler) UpsertLicenseModule(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}

	var req struct {
		ModuleKey string          `json:"module_key"`
		Enabled   bool            `json:"enabled"`
		Config    json.RawMessage `json:"config,omitempty"`
		ExpiresAt *time.Time      `json:"expires_at,omitempty"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if req.ModuleKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "module_key is required"})
	}

	var configBytes []byte
	if len(req.Config) > 0 {
		configBytes = []byte(req.Config)
	}

	lm := &LicenseModule{
		LicenseID: id,
		ModuleKey: req.ModuleKey,
		Enabled:   req.Enabled,
		Config:    configBytes,
		ExpiresAt: req.ExpiresAt,
	}

	if err := h.store.UpsertLicenseModule(c.Request().Context(), lm); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, lm)
}

func (h *ModuleAdminHandler) DeleteLicenseModule(c echo.Context) error {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid license id"})
	}
	moduleKey := c.Param("key")
	if moduleKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "module key is required"})
	}

	if err := h.store.DeleteLicenseModule(c.Request().Context(), id, moduleKey); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "module override removed"})
}
