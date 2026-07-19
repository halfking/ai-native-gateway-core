package distribution

import (
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/maas"
)

type PublicAPI struct {
	store   Store
	catalog *CatalogService
	tickets *TicketSigner
	payment maas.PaymentProvider
}

func NewPublicAPI(store Store, catalog *CatalogService, tickets *TicketSigner, payment maas.PaymentProvider) *PublicAPI {
	return &PublicAPI{store: store, catalog: catalog, tickets: tickets, payment: payment}
}

func (api *PublicAPI) RegisterRoutes(g *echo.Group) {
	g.GET("/catalog", api.GetCatalog)
	g.POST("/ticket", api.CreateTicket)
	g.POST("/events", api.RecordEvent)
}

type DonationAPI struct {
	store   Store
	payment maas.PaymentProvider
}

func NewDonationAPI(store Store, payment maas.PaymentProvider) *DonationAPI {
	return &DonationAPI{store: store, payment: payment}
}

func (api *DonationAPI) RegisterRoutes(g *echo.Group) {
	g.POST("", api.CreateDonation)
	g.GET("/:order_no/status", api.GetDonationStatus)
	g.POST("/:order_no/confirm-stub", api.ConfirmStubDonation)
}

func (api *PublicAPI) GetCatalog(c echo.Context) error {
	cat, err := api.catalog.BuildCatalog(c.Request().Context())
	if err != nil {
		slog.Error("build catalog failed", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "catalog unavailable"})
	}
	return c.JSON(http.StatusOK, cat)
}

func (api *PublicAPI) CreateTicket(c echo.Context) error {
	var req struct {
		Version    string `json:"version"`
		Platform   string `json:"platform"`
		Arch       string `json:"arch"`
		Email      string `json:"email"`
		DonationID *int64 `json:"donation_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	if req.Platform == "" || req.Arch == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "platform and arch required"})
	}

	ctx := c.Request().Context()
	cat, _ := api.catalog.BuildCatalog(ctx)
	version := req.Version
	if version == "" {
		version = cat.Version
	}

	requestID := uuid.New().String()
	var holderID *int64
	if email := strings.TrimSpace(req.Email); email != "" {
		if h, err := api.store.EnsureHolderByEmail(ctx, email, ""); err == nil {
			holderID = &h.ID
		}
	}

	ev := &DownloadEvent{
		RequestID:      requestID,
		ReleaseVersion: version,
		Platform:       req.Platform,
		Arch:           req.Arch,
		Edition:        "customer",
		Channel:        "stable",
		HolderID:       holderID,
		DonationID:     req.DonationID,
		Result:         "started",
		Source:         "web",
	}
	if err := api.store.RecordDownloadEvent(ctx, ev); err != nil {
		slog.Error("record download event failed", "error", err)
	}

	fileName, url := api.catalog.ArtifactURL(version, req.Platform, req.Arch)
	token, expiresAt, err := api.tickets.Issue(requestID, version, req.Platform, req.Arch)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ticket issue failed"})
	}

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	signedURL := url + sep + "ticket=" + token

	return c.JSON(http.StatusOK, DownloadTicket{
		RequestID: requestID,
		URL:       signedURL,
		ExpiresAt: expiresAt,
		FileName:  fileName,
	})
}

func (api *PublicAPI) RecordEvent(c echo.Context) error {
	var req struct {
		RequestID  string `json:"request_id"`
		Result     string `json:"result"`
		DurationMS int    `json:"duration_ms"`
	}
	if err := c.Bind(&req); err != nil || req.RequestID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "request_id required"})
	}
	result := req.Result
	if result == "" {
		result = "completed"
	}
	if err := api.store.UpdateDownloadResult(c.Request().Context(), req.RequestID, result, req.DurationMS); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update failed"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (api *DonationAPI) CreateDonation(c echo.Context) error {
	var req struct {
		Email       string `json:"email"`
		AmountCents int    `json:"amount_cents"`
		Channel     string `json:"channel"`
		TierLabel   string `json:"tier_label"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	if req.AmountCents <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "amount_cents must be positive"})
	}
	channel := req.Channel
	if channel == "" {
		channel = string(maas.PaymentAlipay)
	}

	ctx := c.Request().Context()
	var holderID *int64
	if email := strings.TrimSpace(req.Email); email != "" {
		if h, err := api.store.EnsureHolderByEmail(ctx, email, ""); err == nil {
			holderID = &h.ID
		}
	}

	d := &Donation{
		HolderID:    holderID,
		Email:       req.Email,
		AmountCents: req.AmountCents,
		Channel:     channel,
		TierLabel:   req.TierLabel,
	}
	if err := api.store.CreateDonation(ctx, d); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create donation failed"})
	}

	qr := api.payment.GenerateQR(d.OrderNo, d.AmountCents, maas.PaymentChannel(channel))
	return c.JSON(http.StatusCreated, map[string]interface{}{
		"donation": d,
		"payment":  qr,
	})
}

func (api *DonationAPI) GetDonationStatus(c echo.Context) error {
	d, err := api.store.GetDonationByOrderNo(c.Request().Context(), c.Param("order_no"))
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, d)
}

func (api *DonationAPI) ConfirmStubDonation(c echo.Context) error {
	if os.Getenv("ALLOW_STUB_DONATION_CONFIRM") != "true" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "stub confirm disabled"})
	}
	orderNo := c.Param("order_no")
	if err := api.store.MarkDonationPaid(c.Request().Context(), orderNo); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "confirm failed"})
	}
	d, _ := api.store.GetDonationByOrderNo(c.Request().Context(), orderNo)
	return c.JSON(http.StatusOK, d)
}
