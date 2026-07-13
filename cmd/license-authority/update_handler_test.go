package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock store for testing
type mockUpdateCheckStore struct {
	release *autoupdate.Release
	err     error
}

func (m *mockUpdateCheckStore) GetLatestReleaseAfter(ctx context.Context, channel autoupdate.Channel, currentBuildSeq int) (*autoupdate.Release, error) {
	return m.release, m.err
}

func (m *mockUpdateCheckStore) CreateRelease(ctx context.Context, rel *autoupdate.Release) error {
	return nil
}

func (m *mockUpdateCheckStore) GetRelease(ctx context.Context, version string) (*autoupdate.Release, error) {
	return nil, nil
}

func (m *mockUpdateCheckStore) GetLatestRelease(ctx context.Context, channel autoupdate.Channel) (*autoupdate.Release, error) {
	return nil, nil
}

func (m *mockUpdateCheckStore) ListReleases(ctx context.Context, channel autoupdate.Channel, offset, limit int) ([]autoupdate.Release, int, error) {
	return nil, 0, nil
}

func (m *mockUpdateCheckStore) UpdateReleaseStatus(ctx context.Context, id int64, published bool) error {
	return nil
}

func (m *mockUpdateCheckStore) CreateGrayRule(ctx context.Context, rule *autoupdate.GrayReleaseRule) error {
	return nil
}

func (m *mockUpdateCheckStore) GetGrayRule(ctx context.Context, releaseID int64) (*autoupdate.GrayReleaseRule, error) {
	return nil, nil
}

func (m *mockUpdateCheckStore) UpdateGrayPhase(ctx context.Context, releaseID int64, phase autoupdate.Phase, percent int) error {
	return nil
}

func (m *mockUpdateCheckStore) CreateUpgradeLog(ctx context.Context, instanceID string, oldVer, newVer string) (int64, error) {
	return 0, nil
}

func (m *mockUpdateCheckStore) UpdateUpgradeLog(ctx context.Context, id int64, status string, err string, completedAt time.Time) error {
	return nil
}

func (m *mockUpdateCheckStore) GetUpgradeHistory(ctx context.Context, instanceID string, offset, limit int) ([]autoupdate.ReleaseStatus, int, error) {
	return nil, 0, nil
}

func (m *mockUpdateCheckStore) GetInstanceStatus(ctx context.Context, instanceID string) (*autoupdate.ReleaseStatus, error) {
	return nil, nil
}

func (m *mockUpdateCheckStore) UpdateInstanceStatus(ctx context.Context, status *autoupdate.ReleaseStatus) error {
	return nil
}

func (m *mockUpdateCheckStore) RecordUpdateReport(ctx context.Context, report *autoupdate.UpdateReportData) error {
	return nil
}

func TestUpdateHandler_HandleCheckUpdates(t *testing.T) {
	privKey, serverPubKey := generateTestUpdateKeyPair(t)

	t.Run("returns latest version when update available", func(t *testing.T) {
		now := time.Now()
		mockStore := &mockUpdateCheckStore{
			release: &autoupdate.Release{
				ID:          1,
				Version:     "v1.14.0",
				BuildSeq:    800,
				Channel:     "stable",
				Title:       "Release 1.14.0",
				Description: "New features",
				Changelog:   "Bug fixes",
				ImageTag:    "kx/gateway:1.14.0",
				ImageDigest: "sha256:abc",
				MinVersion:  "v1.0.0",
				Mandatory:   false,
				CreatedBy:   "admin",
				CreatedAt:   now,
				PublishedAt: &now,
			},
			err: nil,
		}

		handler := NewUpdateHandler(mockStore, serverPubKey)

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/updates/latest?current_version=v1.13.0&channel=stable", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		token := generateTestUpdateTokenWithKey(t, "test-instance-001", privKey)
		req.Header.Set("Authorization", "Bearer "+token)

		err := handler.HandleCheckUpdates(c)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err = json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)

		assert.Equal(t, "v1.14.0", resp["version"])
		assert.Equal(t, float64(800), resp["build_seq"])
		assert.Equal(t, false, resp["mandatory"])
		assert.NotEmpty(t, resp["manifest_url"])
		assert.Contains(t, resp["manifest_url"], "v1.14.0")
	})

	t.Run("returns up_to_date when already on latest", func(t *testing.T) {
		mockStore := &mockUpdateCheckStore{
			release: nil,
			err:     assert.AnError,
		}

		handler := NewUpdateHandler(mockStore, serverPubKey)

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/updates/latest?current_version=v1.14.0&channel=stable", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		token := generateTestUpdateTokenWithKey(t, "test-instance-001", privKey)
		req.Header.Set("Authorization", "Bearer "+token)

		err := handler.HandleCheckUpdates(c)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]interface{}
		err = json.Unmarshal(rec.Body.Bytes(), &resp)
		require.NoError(t, err)

		assert.Equal(t, true, resp["up_to_date"])
	})

	t.Run("returns 401 when token missing", func(t *testing.T) {
		mockStore := &mockUpdateCheckStore{}
		handler := NewUpdateHandler(mockStore, serverPubKey)

		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/updates/latest?current_version=v1.13.0", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := handler.HandleCheckUpdates(c)
		assert.Error(t, err)

		httpErr, ok := err.(*echo.HTTPError)
		require.True(t, ok)
		assert.Equal(t, http.StatusUnauthorized, httpErr.Code)
	})
}

func generateTestUpdateKeyPair(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv, pub
}

func generateTestUpdateTokenWithKey(t *testing.T, instanceID string, privKey ed25519.PrivateKey) string {
	t.Helper()

	claims := jwt.RegisteredClaims{
		Subject:   "instance:" + instanceID,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		Issuer:    "llm.kxpms.cn",
		Audience:  jwt.ClaimStrings{"license-authority-instance-api"},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenString, err := token.SignedString(privKey)
	require.NoError(t, err)

	return tokenString
}
