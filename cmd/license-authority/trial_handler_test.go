package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/licensing"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type trialStore struct {
	lic     *licensing.License
	consent *licensing.TrialConsent
	count   int
}

func (s *trialStore) CreateLicense(_ context.Context, lic *licensing.License) error {
	s.count++
	s.lic = lic
	return nil
}

func (s *trialStore) CreateTrialLicenseWithConsent(_ context.Context, lic *licensing.License, consent *licensing.TrialConsent) error {
	s.count++
	s.lic = lic
	s.consent = consent
	return nil
}

func (s *trialStore) GetLicense(context.Context, string) (*licensing.License, error) { return nil, nil }
func (s *trialStore) GetLicenseByID(context.Context, int64) (*licensing.License, error) {
	return nil, nil
}
func (s *trialStore) GetLicenseByHardwareHash(context.Context, string) (*licensing.License, error) {
	return nil, nil
}
func (s *trialStore) UpdateLicense(context.Context, *licensing.License) error { return nil }
func (s *trialStore) RevokeLicense(context.Context, string) error             { return nil }
func (s *trialStore) ActivateDeviceIfUnderLimit(context.Context, *licensing.Device, int) error {
	return nil
}
func (s *trialStore) GetActiveDevices(context.Context, string) ([]licensing.Device, error) {
	return nil, nil
}
func (s *trialStore) GetDeviceByHardwareHash(context.Context, string, string) (*licensing.Device, error) {
	return nil, nil
}
func (s *trialStore) ActivateDevice(context.Context, *licensing.Device) error        { return nil }
func (s *trialStore) DeactivateDevice(context.Context, string, string, string) error { return nil }
func (s *trialStore) UpdateHeartbeat(context.Context, string, string) error          { return nil }
func (s *trialStore) CreateOfflineRequest(context.Context, *licensing.OfflineRequest) error {
	return nil
}
func (s *trialStore) GetOfflineRequest(context.Context, string) (*licensing.OfflineRequest, error) {
	return nil, nil
}
func (s *trialStore) ApproveOfflineRequest(context.Context, string, *licensing.SignedLicense, string) error {
	return nil
}
func (s *trialStore) GetOfflineActivationCode(context.Context, string) (string, error) {
	return "", nil
}
func (s *trialStore) ListOfflineRequests(context.Context) ([]licensing.OfflineRequest, error) {
	return nil, nil
}
func (s *trialStore) RejectOfflineRequest(context.Context, string, string) error { return nil }
func (s *trialStore) CountActiveDevices(context.Context, string) (int, error)    { return 0, nil }
func (s *trialStore) ListAllLicenses(context.Context, int, int, string, string) ([]licensing.License, int, error) {
	return nil, 0, nil
}
func (s *trialStore) ListAllDevices(context.Context, string) ([]licensing.Device, error) {
	return nil, nil
}
func (s *trialStore) GetLicenseModules(context.Context, string) (map[string]*licensing.LicenseModule, error) {
	return nil, nil
}
func (s *trialStore) ListProductModules(context.Context) ([]licensing.ProductModule, error) {
	return nil, nil
}
func (s *trialStore) ListProductModuleFeatures(context.Context) ([]licensing.ProductModuleFeature, error) {
	return nil, nil
}
func (s *trialStore) ListSubscriptionTiers(context.Context) ([]licensing.SubscriptionTier, error) {
	return nil, nil
}
func (s *trialStore) ListTierModuleMaps(context.Context) ([]licensing.TierModuleMap, error) {
	return nil, nil
}
func (s *trialStore) ListLicenseModulesByID(context.Context, int64) ([]licensing.LicenseModule, error) {
	return nil, nil
}
func (s *trialStore) UpsertLicenseModule(context.Context, *licensing.LicenseModule) error { return nil }
func (s *trialStore) DeleteLicenseModule(context.Context, int64, string) error            { return nil }

func TestTrialHandlerRejectsInvalidEmailWithoutCreatingLicense(t *testing.T) {
	store := &trialStore{}
	h := newTrialHandlerForTest(store)
	e := echo.New()
	h.RegisterRoutes(e.Group("/api/v1"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/license/trial", strings.NewReader(`{"email":"not-an-email"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 0, store.count)
}

func TestTrialHandlerRejectsMissingTermsAcceptance(t *testing.T) {
	store := &trialStore{}
	h := newTrialHandlerForTest(store)
	e := echo.New()
	h.RegisterRoutes(e.Group("/api/v1"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/license/trial", strings.NewReader(`{"email":"user@example.com"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 0, store.count)
}

func TestTrialHandlerCreatesConfiguredTrialLicense(t *testing.T) {
	store := &trialStore{}
	h := newTrialHandlerForTest(store)
	h.duration = 14 * 24 * time.Hour
	e := echo.New()
	h.RegisterRoutes(e.Group("/api/v1"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/license/trial", strings.NewReader(`{"email":"User@Example.com","agree":true}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var body trialResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, http.StatusCreated, rec.Code)
	require.True(t, body.Success)
	require.Equal(t, "trial", store.lic.SubscriptionTier)
	require.Equal(t, "user@example.com", store.lic.CustomerEmail)
	require.Equal(t, "TRIAL-", store.lic.LicenseKey[:6])
	require.WithinDuration(t, time.Now().Add(14*24*time.Hour), store.lic.ExpiresAt, time.Second)
	require.Equal(t, trialAgreementVersion, store.consent.AgreementVersion)
	require.Equal(t, "trial_api", store.consent.Source)
	require.WithinDuration(t, time.Now().UTC(), store.consent.AcceptedAt, time.Second)
}

func TestTrialHandlerLimitsRepeatedRequests(t *testing.T) {
	store := &trialStore{}
	h := newTrialHandlerForTest(store)
	e := echo.New()
	h.RegisterRoutes(e.Group("/api/v1"))

	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/license/trial", strings.NewReader(fmt.Sprintf(`{"email":"user-%d@example.com","agree":true}`, i)))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if i < 3 {
			require.Equal(t, http.StatusCreated, rec.Code)
		} else {
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
		}
	}
	require.Equal(t, 3, store.count)
}

func TestTrialHandlerAllowsOnlyOneTrialPerEmail(t *testing.T) {
	store := &trialStore{}
	h := newTrialHandlerForTest(store)
	e := echo.New()
	h.RegisterRoutes(e.Group("/api/v1"))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/license/trial", strings.NewReader(`{"email":"User@Example.com","agree":true}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(echo.HeaderXForwardedFor, "192.0.2.10")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if i == 0 {
			require.Equal(t, http.StatusCreated, rec.Code)
		} else {
			require.Equal(t, http.StatusConflict, rec.Code)
		}
	}
	require.Equal(t, 1, store.count)
}

func TestTrialHandlerFailsClosedWithoutRedis(t *testing.T) {
	store := &trialStore{}
	h := NewTrialHandler(store, nil)
	e := echo.New()
	h.RegisterRoutes(e.Group("/api/v1"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/license/trial", strings.NewReader(`{"email":"user@example.com","agree":true}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 0, store.count)
}
