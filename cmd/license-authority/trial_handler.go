package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
)

type TrialHandler struct {
	store      licensing.Store
	duration   time.Duration
	maxDevices int
	mu         sync.Mutex
	requests   map[string]trialRequest
}

type trialRequest struct {
	count int
	reset time.Time
}

type trialRequestBody struct {
	Email string `json:"email"`
}

type trialResponse struct {
	Success    bool   `json:"success"`
	LicenseKey string `json:"license_key,omitempty"`
	Message    string `json:"message,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func NewTrialHandler(store licensing.Store) *TrialHandler {
	days := 14
	if value := getEnv("LICENSE_TRIAL_DAYS", ""); value != "" {
		if _, err := fmt.Sscan(value, &days); err != nil || days < 1 || days > 90 {
			days = 14
		}
	}
	return &TrialHandler{
		store:      store,
		duration:   time.Duration(days) * 24 * time.Hour,
		maxDevices: 1,
		requests:   make(map[string]trialRequest),
	}
}

func (h *TrialHandler) RegisterRoutes(g *echo.Group) {
	g.POST("/license/trial", h.handleTrial)
}

func (h *TrialHandler) handleTrial(c echo.Context) error {
	var body trialRequestBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, trialResponse{Message: "invalid request body"})
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return c.JSON(http.StatusBadRequest, trialResponse{Message: "valid email is required"})
	}

	key := clientKey(c, email)
	if !h.allow(key) {
		return c.JSON(http.StatusTooManyRequests, trialResponse{Message: "trial request limit exceeded"})
	}

	expiresAt := time.Now().UTC().Add(h.duration)
	license := &licensing.License{
		LicenseKey:       newTrialLicenseKey(),
		CustomerName:     email,
		CustomerEmail:    email,
		MaxDevices:       h.maxDevices,
		SubscriptionTier: "trial",
		Features:         []string{},
		ExpiresAt:        expiresAt,
	}
	if err := h.store.CreateLicense(c.Request().Context(), license); err != nil {
		return c.JSON(http.StatusInternalServerError, trialResponse{Message: "unable to create trial license"})
	}
	return c.JSON(http.StatusCreated, trialResponse{
		Success:    true,
		LicenseKey: license.LicenseKey,
		Message:    "trial license created",
		ExpiresAt:  expiresAt.Format(time.RFC3339),
	})
}

func (h *TrialHandler) allow(key string) bool {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.requests[key]
	if entry.reset.Before(now) {
		entry = trialRequest{reset: now.Add(time.Hour)}
	}
	if entry.count >= 3 {
		h.requests[key] = entry
		return false
	}
	entry.count++
	h.requests[key] = entry
	return true
}

func clientKey(c echo.Context, email string) string {
	return c.RealIP() + "\x00" + email
}

func newTrialLicenseKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable")
	}
	return "TRIAL-" + hex.EncodeToString(b)
}
