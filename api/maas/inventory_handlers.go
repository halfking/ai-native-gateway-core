package maasapi

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// handleQuerySellable implements POST /v0.1/sellable:query (read-only).
func (s *Server) handleQuerySellable(c *echo.Context) error {
	var req maascontracts.QuerySellableRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}

	resp, err := s.inv.QuerySellable(c.Request().Context(), authFromContext(c), req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

// handleHoldCreate implements POST /v0.1/holds (soft reserve, requires Idempotency-Key).
func (s *Server) handleHoldCreate(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.HoldCreateRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if len(req.Lines) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "lines must contain at least 1 item")
	}
	if req.ExpiresAt.IsZero() {
		return echo.NewHTTPError(http.StatusBadRequest, "expires_at is required")
	}
	if time.Until(req.ExpiresAt) <= 0 {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "expires_at must be in the future")
	}

	resp, err := s.inv.HoldCreate(c.Request().Context(), authFromContext(c), meta, req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// handleHoldConfirm implements POST /v0.1/holds/{hold_id}:confirm (pending → committed).
func (s *Server) handleHoldConfirm(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.HoldConfirmRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.FencingToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "fencing_token is required")
	}

	resp, err := s.inv.HoldConfirm(c.Request().Context(), authFromContext(c), meta,
		c.Param("hold_id"), req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

// handleHoldRelease implements POST /v0.1/holds/{hold_id}:release.
func (s *Server) handleHoldRelease(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.HoldReleaseRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}

	resp, err := s.inv.HoldRelease(c.Request().Context(), authFromContext(c), meta,
		c.Param("hold_id"), req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

// handleHoldExpire implements POST /v0.1/holds/{hold_id}:expire.
// Clients may trigger, but inventory clock makes the authoritative decision.
func (s *Server) handleHoldExpire(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.HoldExpireRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}

	resp, err := s.inv.HoldExpire(c.Request().Context(), authFromContext(c), meta,
		c.Param("hold_id"), req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
