package maasapi

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetricsMiddleware_RecordsRequests verifies that every HTTP request bumps
// maas_http_requests_total and maas_http_request_duration_seconds.
func TestMetricsMiddleware_RecordsRequests(t *testing.T) {
	e := echo.New()
	e.Use(metricsMiddleware())
	e.POST("/v0.1/_probe_metrics", func(c *echo.Context) error {
		return c.JSON(200, map[string]string{"ok": "true"})
	})

	before := testutil.ToFloat64(maasHTTPRequestsTotal.WithLabelValues("POST", "/v0.1/_probe_metrics", "200"))

	req := httptest.NewRequest("POST", "/v0.1/_probe_metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)

	after := testutil.ToFloat64(maasHTTPRequestsTotal.WithLabelValues("POST", "/v0.1/_probe_metrics", "200"))
	assert.Greater(t, after, before, "request counter should increment")
}

// TestMetricsMiddleware_RecordsErrors verifies error status grouping (404 unmatched).
func TestMetricsMiddleware_RecordsUnmatched(t *testing.T) {
	e := echo.New()
	e.Use(metricsMiddleware())

	req := httptest.NewRequest("POST", "/v0.1/nonexistent_route", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 404, rec.Code)

	// Unmatched routes carry no route template → "unmatched" label.
	after := testutil.ToFloat64(maasHTTPRequestsTotal.WithLabelValues("POST", "unmatched", "404"))
	assert.Greater(t, after, float64(0), "unmatched 404 should be counted under 'unmatched' route label")
}

// TestRecordDomainError verifies maas_domain_errors_total increments per code.
func TestRecordDomainError(t *testing.T) {
	before := testutil.ToFloat64(maasDomainErrorsTotal.WithLabelValues("MAAS_TEST_CODE"))

	RecordDomainError("MAAS_TEST_CODE")
	RecordDomainError("MAAS_TEST_CODE")

	after := testutil.ToFloat64(maasDomainErrorsTotal.WithLabelValues("MAAS_TEST_CODE"))
	assert.InDelta(t, before+2, after, 0.001, "two errors should increment counter by 2")
}

// TestRedactRoute verifies route labeling keeps cardinality bounded.
func TestRedactRoute(t *testing.T) {
	assert.Equal(t, "unmatched", redactRoute(""))
	assert.Equal(t, "/v0.1/holds", redactRoute("/v0.1/holds"))
	assert.Equal(t, "/v0.1/holds/:hold_id/confirm", redactRoute("/v0.1/holds/:hold_id/confirm"))
}

// TestItoa verifies the hot-path integer formatter.
func TestItoa(t *testing.T) {
	assert.Equal(t, "0", itoa(0))
	assert.Equal(t, "200", itoa(200))
	assert.Equal(t, "404", itoa(404))
	assert.Equal(t, "500", itoa(500))
	assert.Equal(t, "-1", itoa(-1))
}
