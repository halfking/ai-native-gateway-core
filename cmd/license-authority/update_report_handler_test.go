package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoupdate"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateReportHandler_Success(t *testing.T) {
	// Setup
	e := echo.New()
	mockUpdateStore := &mockUpdateStore{}
	handler := NewUpdateReportHandler(mockUpdateStore)

	// Prepare request
	report := UpdateReport{
		InstanceID:  "test-instance-1",
		FromVersion: "v1.0.0",
		ToVersion:   "v1.1.0",
		Status:      "success",
		DurationMS:  5000,
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/updates/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute
	err := handler.HandleReport(c)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]interface{}
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp["ack"].(bool))

	// Verify store was called
	assert.True(t, mockUpdateStore.recordReportCalled)
}

func TestUpdateReportHandler_Failed(t *testing.T) {
	// Setup
	e := echo.New()
	mockUpdateStore := &mockUpdateStore{}
	handler := NewUpdateReportHandler(mockUpdateStore)

	// Prepare request with failed status
	report := UpdateReport{
		InstanceID:  "test-instance-2",
		FromVersion: "v1.0.0",
		ToVersion:   "v1.1.0",
		Status:      "failed",
		DurationMS:  2000,
		Error:       "download timeout",
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/updates/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute
	err := handler.HandleReport(c)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, mockUpdateStore.recordReportCalled)
}

func TestUpdateReportHandler_RolledBack(t *testing.T) {
	// Setup
	e := echo.New()
	mockUpdateStore := &mockUpdateStore{}
	handler := NewUpdateReportHandler(mockUpdateStore)

	// Prepare request with rolled_back status
	report := UpdateReport{
		InstanceID:  "test-instance-3",
		FromVersion: "v1.1.0",
		ToVersion:   "v1.0.0",
		Status:      "rolled_back",
		DurationMS:  3000,
		Error:       "startup failed",
	}
	body, _ := json.Marshal(report)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/updates/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute
	err := handler.HandleReport(c)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, mockUpdateStore.recordReportCalled)
}

func TestUpdateReportHandler_InvalidRequest(t *testing.T) {
	// Setup
	e := echo.New()
	mockUpdateStore := &mockUpdateStore{}
	handler := NewUpdateReportHandler(mockUpdateStore)

	// Prepare invalid request
	req := httptest.NewRequest(http.MethodPost, "/api/v1/updates/report", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute
	err := handler.HandleReport(c)

	// Assert
	assert.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
}

// Mock stores
type mockUpdateStore struct {
	recordReportCalled bool
}

func (m *mockUpdateStore) RecordUpdateReport(ctx context.Context, report *autoupdate.UpdateReportData) error {
	m.recordReportCalled = true
	return nil
}

func (m *mockUpdateStore) CreateRelease(ctx context.Context, rel *autoupdate.Release) error {
	return nil
}

func (m *mockUpdateStore) GetRelease(ctx context.Context, version string) (*autoupdate.Release, error) {
	return nil, nil
}

func (m *mockUpdateStore) GetLatestRelease(ctx context.Context, channel autoupdate.Channel) (*autoupdate.Release, error) {
	return nil, nil
}

func (m *mockUpdateStore) GetLatestReleaseAfter(ctx context.Context, channel autoupdate.Channel, currentBuildSeq int) (*autoupdate.Release, error) {
	return nil, nil
}

func (m *mockUpdateStore) ListReleases(ctx context.Context, channel autoupdate.Channel, offset, limit int) ([]autoupdate.Release, int, error) {
	return nil, 0, nil
}

func (m *mockUpdateStore) UpdateReleaseStatus(ctx context.Context, id int64, published bool) error {
	return nil
}

func (m *mockUpdateStore) CreateGrayRule(ctx context.Context, rule *autoupdate.GrayReleaseRule) error {
	return nil
}

func (m *mockUpdateStore) ListGrayRules(ctx context.Context, limit int) ([]autoupdate.GrayReleaseRuleView, error) {
	return nil, nil
}

func (m *mockUpdateStore) GetGrayRule(ctx context.Context, releaseID int64) (*autoupdate.GrayReleaseRule, error) {
	return nil, nil
}

func (m *mockUpdateStore) UpdateGrayPhase(ctx context.Context, releaseID int64, phase autoupdate.Phase, percent int) error {
	return nil
}

func (m *mockUpdateStore) CreateUpgradeLog(ctx context.Context, instanceID string, oldVer, newVer string) (int64, error) {
	return 0, nil
}

func (m *mockUpdateStore) UpdateUpgradeLog(ctx context.Context, id int64, status string, err string, completedAt time.Time) error {
	return nil
}

func (m *mockUpdateStore) GetUpgradeHistory(ctx context.Context, instanceID string, offset, limit int) ([]autoupdate.ReleaseStatus, int, error) {
	return nil, 0, nil
}

func (m *mockUpdateStore) GetInstanceStatus(ctx context.Context, instanceID string) (*autoupdate.ReleaseStatus, error) {
	return nil, nil
}

func (m *mockUpdateStore) UpdateInstanceStatus(ctx context.Context, status *autoupdate.ReleaseStatus) error {
	return nil
}
