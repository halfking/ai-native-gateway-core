package distribution

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/kaixuan/llm-gateway-go/licensing"
)

// OfflinePublicAPI exposes center-side offline activation portal endpoints.
type OfflinePublicAPI struct {
	offline *licensing.OfflineManager
	store   licensing.Store
}

func NewOfflinePublicAPI(offline *licensing.OfflineManager, store licensing.Store) *OfflinePublicAPI {
	return &OfflinePublicAPI{offline: offline, store: store}
}

func (api *OfflinePublicAPI) RegisterRoutes(g *echo.Group) {
	g.POST("/submit", api.Submit)
	g.GET("/status/:request_id", api.Status)
	g.GET("/response/:request_id", api.Response)
}

func (api *OfflinePublicAPI) Submit(c echo.Context) error {
	var req struct {
		SignedRequest string `json:"signed_request"`
		RequestID     string `json:"request_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	ctx := c.Request().Context()

	if strings.TrimSpace(req.SignedRequest) != "" {
		offReq, err := api.offline.ImportSignedRequest(ctx, req.SignedRequest)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"request_id": offReq.RequestID,
			"status":     offReq.Status,
			"message":    "request registered, awaiting approval",
		})
	}

	rid := strings.TrimSpace(req.RequestID)
	if rid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "signed_request or request_id required"})
	}
	offReq, err := api.store.GetOfflineRequest(ctx, rid)
	if err != nil || offReq == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "request not found"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"request_id": offReq.RequestID,
		"status":     offReq.Status,
	})
}

func (api *OfflinePublicAPI) Status(c echo.Context) error {
	offReq, err := api.store.GetOfflineRequest(c.Request().Context(), c.Param("request_id"))
	if err != nil || offReq == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"request_id":    offReq.RequestID,
		"status":        offReq.Status,
		"license_key":   maskKey(offReq.LicenseKey),
		"device_name":   offReq.DeviceName,
		"reject_reason": offReq.RejectReason,
	})
}

func (api *OfflinePublicAPI) Response(c echo.Context) error {
	offReq, err := api.store.GetOfflineRequest(c.Request().Context(), c.Param("request_id"))
	if err != nil || offReq == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if offReq.Status != "approved" || offReq.ApprovedLicense == nil {
		return c.JSON(http.StatusAccepted, map[string]interface{}{
			"request_id": offReq.RequestID,
			"status":     offReq.Status,
			"message":    "not yet approved",
		})
	}
	signed, err := licensing.MarshalToBase64(offReq.ApprovedLicense)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "encode failed"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"request_id":      offReq.RequestID,
		"activation_code": offReq.ActivationCode,
		"signed_license":  signed,
	})
}

func maskKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
