package fault

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type AdminHandler struct {
	store      Store
	detector   *Detector
	ruleEngine *RuleEngine
}

func NewAdminHandler(store Store, detector *Detector, ruleEngine *RuleEngine) *AdminHandler {
	return &AdminHandler{
		store:      store,
		detector:   detector,
		ruleEngine: ruleEngine,
	}
}

func (h *AdminHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/rules", h.CreateRule)
	g.GET("/rules", h.ListRules)
	g.GET("/rules/:id", h.GetRule)
	g.PUT("/rules/:id", h.UpdateRule)
	g.DELETE("/rules/:id", h.DeleteRule)

	g.GET("/events", h.ListEvents)
	g.GET("/events/:id", h.GetEvent)
	g.POST("/events/:id/acknowledge", h.AcknowledgeEvent)
	g.POST("/events/:id/resolve", h.ResolveEvent)

	g.GET("/stats", h.GetDashboardStats)
	g.POST("/reload", h.ReloadRules)
}

func (h *AdminHandler) CreateRule(c echo.Context) error {
	var rule Rule
	if err := c.Bind(&rule); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.ruleEngine.ValidateRule(&rule); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.store.CreateRule(c.Request().Context(), &rule); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	if rule.Enabled {
		_ = h.detector.ReloadRules(c.Request().Context())
	}

	return c.JSON(http.StatusCreated, rule)
}

func (h *AdminHandler) ListRules(c echo.Context) error {
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit == 0 {
		limit = 20
	}

	rules, total, err := h.store.ListAllRules(c.Request().Context(), offset, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"rules":  rules,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

func (h *AdminHandler) GetRule(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	rule, err := h.store.GetRule(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, rule)
}

func (h *AdminHandler) UpdateRule(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	var rule Rule
	if err := c.Bind(&rule); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	rule.ID = id

	if err := h.ruleEngine.ValidateRule(&rule); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.store.UpdateRule(c.Request().Context(), &rule); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	_ = h.detector.ReloadRules(c.Request().Context())

	return c.JSON(http.StatusOK, rule)
}

func (h *AdminHandler) DeleteRule(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	if err := h.store.DeleteRule(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	_ = h.detector.ReloadRules(c.Request().Context())

	return c.JSON(http.StatusOK, map[string]string{"message": "rule deleted"})
}

func (h *AdminHandler) ListEvents(c echo.Context) error {
	statusStr := c.QueryParam("status")
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit == 0 {
		limit = 20
	}

	events, total, err := h.store.ListEvents(c.Request().Context(), EventStatus(statusStr), offset, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"events": events,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

func (h *AdminHandler) GetEvent(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	event, err := h.store.GetEvent(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, event)
}

func (h *AdminHandler) AcknowledgeEvent(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	var req struct {
		Actor string `json:"actor"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.store.UpdateEventStatus(c.Request().Context(), id, EventStatusAck, req.Actor); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "event acknowledged"})
}

func (h *AdminHandler) ResolveEvent(c echo.Context) error {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)

	var req struct {
		Actor string `json:"actor"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if err := h.store.UpdateEventStatus(c.Request().Context(), id, EventStatusResolved, req.Actor); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "event resolved"})
}

func (h *AdminHandler) GetDashboardStats(c echo.Context) error {
	stats, err := h.store.GetDashboardStats(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, stats)
}

func (h *AdminHandler) ReloadRules(c echo.Context) error {
	if err := h.detector.ReloadRules(c.Request().Context()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "rules reloaded"})
}
