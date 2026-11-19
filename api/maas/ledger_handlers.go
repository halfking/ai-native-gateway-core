package maasapi

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
)

// handleUsageHold implements POST /v0.1/usage-holds.
// Request-level hold against an activated entitlement (not a second decrement of H).
func (s *Server) handleUsageHold(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.UsageHoldRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.EntitlementID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "entitlement_id is required")
	}
	if req.RequestID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "request_id is required")
	}
	if req.Quantity.Amount == "" || req.Quantity.Unit == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "quantity.amount and quantity.unit are required")
	}

	resp, err := s.led.UsageHold(c.Request().Context(), authFromContext(c), meta, req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// handleUsageSettle implements POST /v0.1/usage-holds/{usage_hold_id}:settle.
// Settles actual usage and releases unused remainder.
func (s *Server) handleUsageSettle(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.UsageSettleRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.Actual.Amount == "" || req.Actual.Unit == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "actual.amount and actual.unit are required")
	}

	resp, err := s.led.UsageSettle(c.Request().Context(), authFromContext(c), meta,
		c.Param("usage_hold_id"), req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

// handleUsageCorrect implements POST /v0.1/ledger:correct.
// Append-only correction (reversal); never overwrite posted entries.
func (s *Server) handleUsageCorrect(c *echo.Context) error {
	meta, err := commandMetaFromContext(c)
	if err != nil {
		return err
	}
	var req maascontracts.UsageCorrectRequest
	if err := decodeBody(c, &req); err != nil {
		return err
	}
	if req.EntryID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "entry_id is required")
	}
	if req.Reversal.Amount == "" || req.Reversal.Unit == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "reversal.amount and reversal.unit are required")
	}

	resp, err := s.led.UsageCorrect(c.Request().Context(), authFromContext(c), meta, req)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
