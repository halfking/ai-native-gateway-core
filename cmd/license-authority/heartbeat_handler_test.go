package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/center"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeartbeatHandler_HandleHeartbeat(t *testing.T) {
	// Setup test database
	dbURL := getEnv("TEST_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()

	// Setup
	store := center.NewPgxStore(pool)
	serverPrivKey, serverPubKey, err := LoadOrGenerateServerKeys("./testdata")
	require.NoError(t, err)

	handler := NewHeartbeatHandler(store, serverPubKey)

	// Create test instance
	instanceID := "test-instance-heartbeat-" + time.Now().Format("20060102150405")
	instanceToken, err := SignInstanceToken(instanceID, "test-license-hash", serverPrivKey)
	require.NoError(t, err)

	instance := &center.InstanceInfo{
		InstanceID:     instanceID,
		Hostname:       "test-host",
		IPAddress:      "192.168.1.100",
		Version:        "v1.0.0",
		BuildSeq:       1,
		Status:         "online",
		StartedAt:      time.Now(),
		LicenseKeyHash: "test-license-hash",
		HardwareHash:   "test-hardware-hash",
		PublicKey:      "test-public-key",
		InstanceToken:  instanceToken,
	}
	require.NoError(t, store.RegisterInstance(ctx, instance))

	// Test heartbeat payload
	payload := center.HeartbeatPayload{
		UptimeSecs:   3600,
		GoVersion:    "go1.21.0",
		NumGoroutine: 42,
		AllocMB:      128.5,
		TotalAllocMB: 256.0,
		SysMB:        512.0,
		CPUCores:     8,
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	// Create Echo context
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/heartbeat", bytes.NewReader(payloadJSON))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer "+instanceToken)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute
	err = handler.HandleHeartbeat(c)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]interface{}
	err = json.Unmarshal(rec.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.True(t, response["ack"].(bool))
	assert.Equal(t, float64(60), response["next_heartbeat_in_secs"].(float64))

	// Verify database record
	lastHeartbeat, err := store.GetLastHeartbeat(ctx, instanceID)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), lastHeartbeat, 5*time.Second)

	// Verify heartbeat history
	history, err := store.GetHeartbeatHistory(ctx, instanceID, time.Now().Add(-1*time.Hour), 10)
	require.NoError(t, err)
	assert.NotEmpty(t, history)
	assert.Equal(t, int64(3600), history[0].UptimeSecs)
	assert.Equal(t, 42, history[0].NumGoroutine)
}

func TestHeartbeatHandler_HandleHeartbeat_InvalidToken(t *testing.T) {
	dbURL := getEnv("TEST_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()

	store := center.NewPgxStore(pool)
	_, serverPubKey, err := LoadOrGenerateServerKeys("./testdata")
	require.NoError(t, err)

	handler := NewHeartbeatHandler(store, serverPubKey)

	payload := center.HeartbeatPayload{UptimeSecs: 100}
	payloadJSON, _ := json.Marshal(payload)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/heartbeat", bytes.NewReader(payloadJSON))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.HandleHeartbeat(c)
	assert.Error(t, err)
}

func TestHeartbeatHandler_HandleHeartbeat_MissingAuthorization(t *testing.T) {
	dbURL := getEnv("TEST_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()

	store := center.NewPgxStore(pool)
	_, serverPubKey, err := LoadOrGenerateServerKeys("./testdata")
	require.NoError(t, err)

	handler := NewHeartbeatHandler(store, serverPubKey)

	payload := center.HeartbeatPayload{UptimeSecs: 100}
	payloadJSON, _ := json.Marshal(payload)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/heartbeat", bytes.NewReader(payloadJSON))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err = handler.HandleHeartbeat(c)
	assert.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, httpErr.Code)
}
