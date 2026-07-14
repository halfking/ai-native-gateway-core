package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

type TrialHandler struct {
	store      licensing.Store
	duration   time.Duration
	maxDevices int
	limiter    trialLimiter
}

type trialLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
	ReserveEmail(ctx context.Context, email string) (bool, error)
	ReleaseEmail(ctx context.Context, email string) error
}

type trialRequestBody struct {
	Email string `json:"email"`
	Agree bool   `json:"agree"`
}

type trialResponse struct {
	Success    bool   `json:"success"`
	LicenseKey string `json:"license_key,omitempty"`
	Message    string `json:"message,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func NewTrialHandler(store licensing.Store, redisClient *redis.Client) *TrialHandler {
	days := 15
	if value := getEnv("LICENSE_TRIAL_DAYS", ""); value != "" {
		if _, err := fmt.Sscan(value, &days); err != nil || days < 1 || days > 90 {
			days = 15
		}
	}
	return &TrialHandler{
		store:      store,
		duration:   time.Duration(days) * 24 * time.Hour,
		maxDevices: 1,
		limiter:    newRedisTrialLimiter(redisClient),
	}
}

func newTrialHandlerForTest(store licensing.Store) *TrialHandler {
	h := NewTrialHandler(store, nil)
	h.limiter = &memoryTrialLimiter{requests: make(map[string]memoryTrialRequest), issued: make(map[string]struct{})}
	return h
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
	if !body.Agree {
		return c.JSON(http.StatusBadRequest, trialResponse{Message: "terms acceptance is required"})
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return c.JSON(http.StatusBadRequest, trialResponse{Message: "valid email is required"})
	}

	key := clientKey(c)
	if h.limiter == nil {
		return c.JSON(http.StatusServiceUnavailable, trialResponse{Message: "trial activation is temporarily unavailable"})
	}
	allowed, err := h.limiter.Allow(c.Request().Context(), key)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, trialResponse{Message: "trial activation is temporarily unavailable"})
	}
	if !allowed {
		return c.JSON(http.StatusTooManyRequests, trialResponse{Message: "trial request limit exceeded"})
	}
	reserved, err := h.limiter.ReserveEmail(c.Request().Context(), email)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, trialResponse{Message: "trial activation is temporarily unavailable"})
	}
	if !reserved {
		return c.JSON(http.StatusConflict, trialResponse{Message: "a trial license already exists for this email"})
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
		_ = h.limiter.ReleaseEmail(c.Request().Context(), email)
		return c.JSON(http.StatusInternalServerError, trialResponse{Message: "unable to create trial license"})
	}
	return c.JSON(http.StatusCreated, trialResponse{
		Success:    true,
		LicenseKey: license.LicenseKey,
		Message:    "trial license created",
		ExpiresAt:  expiresAt.Format(time.RFC3339),
	})
}

type redisTrialLimiter struct {
	client *redis.Client
}

var trialRateScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return count
`)

func newRedisTrialLimiter(client *redis.Client) trialLimiter {
	if client == nil {
		return nil
	}
	return &redisTrialLimiter{client: client}
}

func (l *redisTrialLimiter) Allow(ctx context.Context, key string) (bool, error) {
	count, err := trialRateScript.Run(ctx, l.client, []string{"license:trial:rate:" + key}, int(time.Hour/time.Second)).Int64()
	if err != nil {
		return false, err
	}
	return count <= 3, nil
}

func (l *redisTrialLimiter) ReserveEmail(ctx context.Context, email string) (bool, error) {
	key := "license:trial:issued:" + hashTrialValue(email)
	return l.client.SetNX(ctx, key, "1", 365*24*time.Hour).Result()
}

func (l *redisTrialLimiter) ReleaseEmail(ctx context.Context, email string) error {
	return l.client.Del(ctx, "license:trial:issued:"+hashTrialValue(email)).Err()
}

type memoryTrialLimiter struct {
	requests map[string]memoryTrialRequest
	issued   map[string]struct{}
}

type memoryTrialRequest struct {
	count int
	reset time.Time
}

func (l *memoryTrialLimiter) Allow(_ context.Context, key string) (bool, error) {
	now := time.Now()
	entry := l.requests[key]
	if entry.reset.Before(now) {
		entry = memoryTrialRequest{reset: now.Add(time.Hour)}
	}
	if entry.count >= 3 {
		return false, nil
	}
	entry.count++
	l.requests[key] = entry
	return true, nil
}

func (l *memoryTrialLimiter) ReserveEmail(_ context.Context, email string) (bool, error) {
	if _, exists := l.issued[email]; exists {
		return false, nil
	}
	l.issued[email] = struct{}{}
	return true, nil
}

func (l *memoryTrialLimiter) ReleaseEmail(_ context.Context, email string) error {
	delete(l.issued, email)
	return nil
}

func clientKey(c echo.Context) string {
	return hashTrialValue(c.RealIP())
}

func hashTrialValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newTrialLicenseKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable")
	}
	return "TRIAL-" + hex.EncodeToString(b)
}
