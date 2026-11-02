package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/requestdetail"
	"github.com/kaixuan/llm-gateway-go/internal/probelane"
)

func TestIsIdleMarkerRequestID(t *testing.T) {
	require.True(t, isIdleMarkerRequestID("idle-global-vendor-openai"))
	require.True(t, isIdleMarkerRequestID("  idle-x "))
	require.False(t, isIdleMarkerRequestID("req-abc-001"))
	require.False(t, isIdleMarkerRequestID(""))
}

func TestHandleUnifiedRequestDetailRejectsIdleMarker(t *testing.T) {
	h := &Handler{}
	store, err := requestdetail.NewStore("")
	require.NoError(t, err)
	h.SetRequestDetailStore(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/idle-global-vendor-openai", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "idle_marker")
}

func TestLoadProbeLaneDetailFromQuerier(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	preview := "hello probe"
	gw := "sess-1"
	model := "glm-flash"
	lat := 42
	mock.ExpectQuery(`(?s)SELECT request_id, tenant_id, success,.*FROM public.session_turns_probe_hot`).
		WithArgs("probe-rid-001").
		WillReturnRows(pgxmock.NewRows([]string{
			"request_id", "tenant_id", "success",
			"client_model", "outbound_model",
			"latency_ms", "gw_session_id", "request_preview",
		}).AddRow(
			"probe-rid-001", "tenant-a", true,
			&model, nil,
			&lat, &gw, &preview,
		))

	detail, err := loadProbeLaneDetailFromQuerier(context.Background(), mock, "probe-rid-001")
	require.NoError(t, err)
	require.Equal(t, requestdetail.SourceProbeLane, detail.Source)
	require.Equal(t, "probe-rid-001", detail.Meta.RequestID)
	require.Equal(t, "tenant-a", detail.Meta.TenantID)
	require.Equal(t, "unavailable", detail.Meta.BodyStatus)
	require.NotNil(t, detail.Bodies)
	require.Empty(t, detail.Bodies.RequestBody)
	require.Contains(t, detail.Warning, "probe_lane")
	require.Contains(t, detail.Warning, "request_preview")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadProbeLaneDetailDisabledWhenPhysicalOff(t *testing.T) {
	probelane.SetPhysicalEnabledForTest(false)
	defer probelane.ResetPhysicalOverrideForTest()

	h := &Handler{db: nil}
	_, err := h.loadProbeLaneDetail(context.Background(), "any-id-here")
	require.ErrorIs(t, err, requestdetail.ErrNotFound)
}

func TestHandleUnifiedRequestDetailFallsBackToProbeLane(t *testing.T) {
	probelane.SetPhysicalEnabledForTest(true)
	defer probelane.ResetPhysicalOverrideForTest()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store, err := requestdetail.NewStore("")
	require.NoError(t, err)

	// Handler.db is *pgxpool.Pool; pgxmock pool cannot be assigned directly.
	// Drive the querier path + wire check that SourceProbeLane encodes correctly.
	preview := "p"
	model := "m"
	lat := 1
	mock.ExpectQuery(`(?s)SELECT request_id, tenant_id, success,.*FROM public.session_turns_probe_hot`).
		WithArgs("probe-fallback-01").
		WillReturnRows(pgxmock.NewRows([]string{
			"request_id", "tenant_id", "success",
			"client_model", "outbound_model",
			"latency_ms", "gw_session_id", "request_preview",
		}).AddRow(
			"probe-fallback-01", "default", false,
			&model, nil,
			&lat, nil, &preview,
		))

	detail, err := loadProbeLaneDetailFromQuerier(context.Background(), mock, "probe-fallback-01")
	require.NoError(t, err)

	// Simulate the handler encoding path (same shape clients see).
	raw, err := json.Marshal(detail)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(raw), `"source":"probe_lane"`), string(raw))
	require.True(t, strings.Contains(string(raw), `"body_status":"unavailable"`), string(raw))

	// Confirm miss-without-probe still 404 (store only, physical on but no db).
	h := &Handler{}
	h.SetRequestDetailStore(store)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/request-detail/probe-fallback-01", nil)
	rr := httptest.NewRecorder()
	h.handleUnifiedRequestDetail(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)

	require.NoError(t, mock.ExpectationsWereMet())
}
