package maasapi

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// handleEstimateQuote implements POST /v0.1/quotes:estimate.
// Per contract v0.1: this MUST NOT create an inventory hold (pure estimate).
func (s *Server) handleEstimateQuote(c *echo.Context) error {
	var req maascontracts.EstimateQuoteRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.ModelSKU == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "model_sku is required")
	}
	if req.ResourceSnapshotID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "resource_snapshot_id is required")
	}

	resp, err := s.quote.EstimateQuote(c.Request().Context(), authFromContext(c), req)
	if err != nil {
		return respondError(c, err)
	}
	// Defense in depth: contract mandates hold_created == false.
	resp.HoldCreated = false
	return c.JSON(http.StatusOK, resp)
}

// handleActivateEntitlement implements POST /v0.1/entitlements:activate.
// Precondition: referenced hold must be in committed status.
func (s *Server) handleActivateEntitlement(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.ActivateEntitlementRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.HoldID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "hold_id is required")
	}

	resp, err := s.ent.ActivateEntitlement(c.Request().Context(), authFromContext(c), meta, req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
