package maasapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/commercial"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/domains/ledger"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
)

// Server wires the MaaS domain ports to HTTP transport per OpenAPI contract v0.1.
// HTTP is the optional process boundary; all routes live under the /v0.1 group.
type Server struct {
	inv   maascontracts.InventoryPort
	quote maascontracts.QuotePort
	ent   maascontracts.EntitlementPort
	led   maascontracts.LedgerPort
}

// NewServer constructs a Server from concrete domain services.
func NewServer(
	inv *inventory.Service,
	quote *commercial.Service,
	ent *trading.Service,
	led *ledger.Service,
) *Server {
	return &Server{inv: inv, quote: quote, ent: ent, led: led}
}

// RegisterRoutes mounts all contract v0.1 endpoints on the given Echo group.
// Action-style contract paths (/holds/{id}:confirm) map to REST subpaths
// (/holds/{id}/confirm) because Echo v5 treats ":x" as a parameter token.
// Metrics middleware is attached first so every request is observed.
func (s *Server) RegisterRoutes(g *echo.Group) {
	g.Use(metricsMiddleware())
	g.Use(tracingMiddleware())

	// inventory
	g.POST("/sellable:query", s.handleQuerySellable)
	g.POST("/holds", s.handleHoldCreate)
	g.POST("/holds/:hold_id/confirm", s.handleHoldConfirm)
	g.POST("/holds/:hold_id/release", s.handleHoldRelease)
	g.POST("/holds/:hold_id/expire", s.handleHoldExpire)

	// quoting
	g.POST("/quotes:estimate", s.handleEstimateQuote)

	// entitlement
	g.POST("/entitlements:activate", s.handleActivateEntitlement)

	// ledger
	g.POST("/usage-holds", s.handleUsageHold)
	g.POST("/usage-holds/:usage_hold_id/settle", s.handleUsageSettle)
	g.POST("/ledger:correct", s.handleUsageCorrect)
}

// --- request decoding helpers ------------------------------------------------

// decodeBody binds the JSON request body into req. Returns 400 on malformed JSON.
func decodeBody(c *echo.Context, req any) error {
	if err := json.NewDecoder(c.Request().Body).Decode(req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body: "+err.Error())
	}
	return nil
}

// authFromContext builds AuthContext from trusted headers. Tenant and subject are
// extracted from the verified auth layer (never from the body, per contract).
func authFromContext(c *echo.Context) maascontracts.AuthContext {
	return maascontracts.AuthContext{
		TenantID: c.Request().Header.Get("X-Tenant-ID"),
		Subject:  c.Request().Header.Get("X-Subject"),
		Scopes:   splitScopes(c.Request().Header.Get("X-Scopes")),
		TraceID:  c.Request().Header.Get("X-Trace-ID"),
		SpanID:   c.Request().Header.Get("X-Span-ID"),
	}
}

// commandMetaFromContext extracts the mandatory Idempotency-Key header.
func commandMetaFromContext(c *echo.Context) (maascontracts.CommandMeta, error) {
	key := c.Request().Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		return maascontracts.CommandMeta{}, echo.NewHTTPError(http.StatusBadRequest,
			"Idempotency-Key header required (8-128 chars)")
	}
	return maascontracts.CommandMeta{IdempotencyKey: key}, nil
}

func splitScopes(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, s := range splitCSV(raw) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitCSV(raw string) []string {
	var out []string
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == ',' {
			out = append(out, raw[start:i])
			start = i + 1
		}
	}
	out = append(out, raw[start:])
	return out
}

// --- error mapping ------------------------------------------------------------

// respondError maps domain errors to HTTP status codes per contract v0.1.
// 401/403 for auth, 409 for conflicts, 422 for validation, 500 otherwise.
func respondError(c *echo.Context, err error) error {
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		return c.JSON(httpErr.Code, errorBody(httpErr.Message, ""))
	}

	var domainErr *maascontracts.Error
	if errors.As(err, &domainErr) {
		RecordDomainError(domainErr.Code) // Prometheus: maas_domain_errors_total{code=...}
		traceID := ""
		if domainErr.TraceID != nil {
			traceID = *domainErr.TraceID
		}
		return c.JSON(statusForDomainError(domainErr), errorBody(domainErr.Message, traceID))
	}

	return c.JSON(http.StatusInternalServerError,
		errorBody("internal error", ""))
}

func statusForDomainError(e *maascontracts.Error) int {
	switch e.Code {
	case maascontracts.ErrorUnauthorizedScope:
		return http.StatusForbidden
	case maascontracts.ErrorIdempotencyPayloadConflict,
		maascontracts.ErrorConflictRevision,
		maascontracts.ErrorSellableInsufficient,
		maascontracts.ErrorHoldFenceMismatch,
		maascontracts.ErrorAlreadySold:
		return http.StatusConflict
	case maascontracts.ErrorHoldExpired,
		maascontracts.ErrorReserveNotCommitted,
		maascontracts.ErrorStaleObservation,
		maascontracts.ErrorReconfirmRequired,
		maascontracts.ErrorLegalNotBinding,
		maascontracts.ErrorUsageAlreadySettled,
		maascontracts.ErrorDoubleSettlementRejected:
		return http.StatusUnprocessableEntity // 422
	case maascontracts.ErrorRetryableUnknown:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

func errorBody(message, traceID string) map[string]any {
	body := map[string]any{"message": message}
	if traceID != "" {
		body["trace_id"] = traceID
	}
	return body
}
