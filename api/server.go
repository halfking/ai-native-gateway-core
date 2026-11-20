package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/contracts/maascontracts"
	"github.com/kaixuan/llm-gateway-go/domains/commercial"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/domains/ledger"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
)

// Server holds all domain services and provides HTTP API
type Server struct {
	pool          *pgxpool.Pool
	inventorySvc  *inventory.Service
	commercialSvc *commercial.Service
	tradingSvc    *trading.Service
	ledgerSvc     *ledger.Service
}

// NewServer creates a new API server with all domain services
func NewServer(pool *pgxpool.Pool) *Server {
	return &Server{
		pool:          pool,
		inventorySvc:  inventory.NewService(pool),
		commercialSvc: commercial.NewService(pool),
		tradingSvc:    trading.NewService(pool),
		ledgerSvc:     ledger.NewService(pool),
	}
}

// SetupRouter configures all API routes
func (s *Server) SetupRouter() *gin.Engine {
	r := gin.Default()

	// Health check
	r.GET("/health", s.healthCheck)

	// API v1
	v1 := r.Group("/api/v1")
	{
		// Inventory endpoints
		inventory := v1.Group("/inventory")
		{
			inventory.POST("/sellable/query", s.querySellable)
			inventory.POST("/holds", s.createHold)
			inventory.POST("/holds/:holdID/confirm", s.confirmHold)
			inventory.POST("/holds/:holdID/release", s.releaseHold)
			inventory.POST("/holds/:holdID/expire", s.expireHold)
		}

		// Commercial endpoints
		commercial := v1.Group("/commercial")
		{
			commercial.POST("/quotes/estimate", s.estimateQuote)
		}

		// Trading endpoints
		trading := v1.Group("/trading")
		{
			trading.POST("/entitlements", s.activateEntitlement)
		}

		// Ledger endpoints
		ledger := v1.Group("/ledger")
		{
			ledger.POST("/usage/hold", s.usageHold)
			ledger.POST("/usage/:usageHoldID/settle", s.usageSettle)
			ledger.POST("/usage/correct", s.usageCorrect)
		}
	}

	return r
}

// healthCheck returns server health status
func (s *Server) healthCheck(c *gin.Context) {
	// Check database connection
	if err := s.pool.Ping(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"error":  "database connection failed",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().UTC(),
	})
}

// extractAuth extracts authentication context from request headers
func extractAuth(c *gin.Context) maascontracts.AuthContext {
	return maascontracts.AuthContext{
		TenantID: c.GetHeader("X-Tenant-ID"),
		TraceID:  c.GetHeader("X-Trace-ID"),
		// Scopes would be extracted from JWT or similar
	}
}

// extractCommandMeta extracts command metadata from request headers
func extractCommandMeta(c *gin.Context) maascontracts.CommandMeta {
	return maascontracts.CommandMeta{
		IdempotencyKey: c.GetHeader("X-Idempotency-Key"),
	}
}

// respondWithError sends an error response
func respondWithError(c *gin.Context, err error) {
	if maasErr, ok := err.(*maascontracts.Error); ok {
		statusCode := http.StatusBadRequest
		if maasErr.Code == maascontracts.ErrorUnauthorizedScope {
			statusCode = http.StatusForbidden
		} else if maasErr.Code == maascontracts.ErrorReserveNotCommitted ||
			maasErr.Code == "MAAS_ENTITLEMENT_NOT_FOUND" {
			statusCode = http.StatusNotFound
		} else if maasErr.Code == maascontracts.ErrorConflictRevision ||
			maasErr.Code == maascontracts.ErrorHoldFenceMismatch {
			statusCode = http.StatusConflict
		}

		c.JSON(statusCode, gin.H{
			"error": gin.H{
				"code":      maasErr.Code,
				"message":   maasErr.Message,
				"retryable": maasErr.Retryable,
			},
		})
		return
	}

	c.JSON(http.StatusInternalServerError, gin.H{
		"error": gin.H{
			"code":    "INTERNAL_ERROR",
			"message": err.Error(),
		},
	})
}

// querySellable handles POST /api/v1/inventory/sellable/query
func (s *Server) querySellable(c *gin.Context) {
	var req maascontracts.QuerySellableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	resp, err := s.inventorySvc.QuerySellable(c.Request.Context(), auth, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// createHold handles POST /api/v1/inventory/holds
func (s *Server) createHold(c *gin.Context) {
	var req maascontracts.HoldCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.inventorySvc.HoldCreate(c.Request.Context(), auth, meta, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// confirmHold handles POST /api/v1/inventory/holds/:holdID/confirm
func (s *Server) confirmHold(c *gin.Context) {
	holdID := c.Param("holdID")

	var req maascontracts.HoldConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.inventorySvc.HoldConfirm(c.Request.Context(), auth, meta, holdID, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// releaseHold handles POST /api/v1/inventory/holds/:holdID/release
func (s *Server) releaseHold(c *gin.Context) {
	holdID := c.Param("holdID")

	var req maascontracts.HoldReleaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.inventorySvc.HoldRelease(c.Request.Context(), auth, meta, holdID, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// expireHold handles POST /api/v1/inventory/holds/:holdID/expire
func (s *Server) expireHold(c *gin.Context) {
	holdID := c.Param("holdID")

	var req maascontracts.HoldExpireRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.inventorySvc.HoldExpire(c.Request.Context(), auth, meta, holdID, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// estimateQuote handles POST /api/v1/commercial/quotes/estimate
func (s *Server) estimateQuote(c *gin.Context) {
	var req maascontracts.EstimateQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	resp, err := s.commercialSvc.EstimateQuote(c.Request.Context(), auth, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// activateEntitlement handles POST /api/v1/trading/entitlements
func (s *Server) activateEntitlement(c *gin.Context) {
	var req maascontracts.ActivateEntitlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.tradingSvc.ActivateEntitlement(c.Request.Context(), auth, meta, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// usageHold handles POST /api/v1/ledger/usage/hold
func (s *Server) usageHold(c *gin.Context) {
	var req maascontracts.UsageHoldRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.ledgerSvc.UsageHold(c.Request.Context(), auth, meta, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// usageSettle handles POST /api/v1/ledger/usage/:usageHoldID/settle
func (s *Server) usageSettle(c *gin.Context) {
	usageHoldID := c.Param("usageHoldID")

	var req maascontracts.UsageSettleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.ledgerSvc.UsageSettle(c.Request.Context(), auth, meta, usageHoldID, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

// usageCorrect handles POST /api/v1/ledger/usage/correct
func (s *Server) usageCorrect(c *gin.Context) {
	var req maascontracts.UsageCorrectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	auth := extractAuth(c)
	meta := extractCommandMeta(c)

	resp, err := s.ledgerSvc.UsageCorrect(c.Request.Context(), auth, meta, req)
	if err != nil {
		respondWithError(c, err)
		return
	}

	c.JSON(http.StatusCreated, resp)
}

// Helper functions
func ptrString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
