package maasapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	maas "github.com/kaixuan/llm-gateway-go/api/maas"
	"github.com/kaixuan/llm-gateway-go/domains/commercial"
	"github.com/kaixuan/llm-gateway-go/domains/inventory"
	"github.com/kaixuan/llm-gateway-go/domains/ledger"
	"github.com/kaixuan/llm-gateway-go/domains/trading"
	"github.com/kaixuan/llm-gateway-go/internal/testutil"
)

// newTestServer boots a PostgreSQL testcontainer, applies schema migrations,
// wires all four domain services and returns an Echo instance with MaaS routes.
func newTestServer(t *testing.T) *echo.Echo {
	t.Helper()
	db := testutil.SetupTestDB(t)

	srv := maas.NewServer(
		inventory.NewService(db.Pool),
		commercial.NewService(db.Pool),
		trading.NewService(db.Pool),
		ledger.NewService(db.Pool),
	)

	e := echo.New()
	srv.RegisterRoutes(e.Group("/v0.1"))
	return e
}

// doJSON issues a JSON request against the test server with auth + idempotency headers.
// Returns the recorder (for status) and the decoded JSON body (empty map on non-JSON).
func doJSON(t *testing.T, e *echo.Echo, method, path, body string, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", "ten_http_001")
	req.Header.Set("X-Trace-ID", "trace_http_001")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

// seedQuota inserts a quota row for the given entitlement so UsageHold passes
// the maas_check_quota_available gate. (Production seeds quota at activation.)
func seedQuota(t *testing.T, db *testutil.TestDatabase, entID string) {
	t.Helper()
	ctx := context.Background()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO maas_entitlement_quotas (
			entitlement_id, model_sku,
			allocated_amount, allocated_unit,
			consumed_unit, held_unit
		) VALUES ($1, 'gpt-4', '1000000', 'token', 'token', 'token')
		ON CONFLICT (entitlement_id, model_sku) DO NOTHING
	`, entID)
	require.NoError(t, err)
}

// createCommittedHold drives HoldCreate → HoldConfirm over HTTP and returns the
// hold id plus fencing token. Requires a healthy inventory snapshot in seed data.
func createCommittedHold(t *testing.T, e *echo.Echo, idemPrefix, amount string) (string, string) {
	t.Helper()
	expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	rec, createResp := doJSON(t, e, "POST", "/v0.1/holds",
		`{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"`+amount+`","unit":"token"}}],"expires_at":"`+expires+`"}`,
		map[string]string{"Idempotency-Key": idemPrefix + "_create"})
	require.Equal(t, 201, rec.Code, createResp)

	holdID := createResp["hold_id"].(string)
	fencing := createResp["fencing_token"].(string)

	rec, confirmResp := doJSON(t, e, "POST", "/v0.1/holds/"+holdID+"/confirm",
		`{"fencing_token":"`+fencing+`"}`,
		map[string]string{"Idempotency-Key": idemPrefix + "_confirm"})
	require.Equal(t, 200, rec.Code, confirmResp)
	require.Equal(t, "committed", confirmResp["status"])

	return holdID, fencing
}

// activateEntitlement activates an entitlement on a committed hold via HTTP.
func activateEntitlement(t *testing.T, e *echo.Echo, idemPrefix, holdID string) string {
	t.Helper()
	rec, actResp := doJSON(t, e, "POST", "/v0.1/entitlements:activate",
		`{"hold_id":"`+holdID+`"}`,
		map[string]string{"Idempotency-Key": idemPrefix + "_activate"})
	require.Equal(t, 200, rec.Code, actResp)
	require.Equal(t, "active", actResp["status"])
	return actResp["entitlement_id"].(string)
}

const window = `"monthly"`

// TestHTTP_QuerySellable exercises POST /v0.1/sellable:query against seed data.
func TestHTTP_QuerySellable(t *testing.T) {
	e := newTestServer(t)

	rec, out := doJSON(t, e, "POST", "/v0.1/sellable:query",
		`{"model_sku":"gpt-4","dimensions":{"commitment":"hard"}}`, nil)

	require.Equal(t, 200, rec.Code, out)
	assert.NotEmpty(t, out["resource_snapshot_id"])
	assert.NotEmpty(t, out["items"])
}

// TestHTTP_HoldLifecycle exercises the hold state machine over HTTP:
// create (201) → confirm (200 committed).
func TestHTTP_HoldLifecycle(t *testing.T) {
	e := newTestServer(t)

	expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	createBody := `{
		"lines": [{
			"model_sku": "gpt-4",
			"quantity": {"amount": "1000", "unit": "token", "window": ` + window + `}
		}],
		"expires_at": "` + expires + `"
	}`

	rec, createResp := doJSON(t, e, "POST", "/v0.1/holds", createBody,
		map[string]string{"Idempotency-Key": "idem_http_create_001"})
	require.Equal(t, 201, rec.Code, createResp)
	assert.Equal(t, "pending", createResp["status"])
	holdID := createResp["hold_id"].(string)
	fencing := createResp["fencing_token"].(string)

	rec, confirmResp := doJSON(t, e, "POST", "/v0.1/holds/"+holdID+"/confirm",
		`{"fencing_token":"`+fencing+`"}`,
		map[string]string{"Idempotency-Key": "idem_http_confirm_001"})
	require.Equal(t, 200, rec.Code, confirmResp)
	assert.Equal(t, "committed", confirmResp["status"])
	assert.NotNil(t, confirmResp["committed_at"])
}

// TestHTTP_HoldCreate_Idempotency verifies same idempotency key returns same hold.
func TestHTTP_HoldCreate_Idempotency(t *testing.T) {
	e := newTestServer(t)

	expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	body := `{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"1000","unit":"token"}}],"expires_at":"` + expires + `"}`
	headers := map[string]string{"Idempotency-Key": "idem_http_idem_001"}

	_, resp1 := doJSON(t, e, "POST", "/v0.1/holds", body, headers)
	require.NotEmpty(t, resp1["hold_id"], "first create should return a hold_id")
	_, resp2 := doJSON(t, e, "POST", "/v0.1/holds", body, headers)

	// Same key must not mint a second hold (either replay of cached result or identical hold id).
	if hold1, ok := resp1["hold_id"].(string); ok {
		if hold2, ok2 := resp2["hold_id"].(string); ok2 {
			assert.Equal(t, hold1, hold2, "same idempotency key must not create a second hold")
		}
	}
}

// TestHTTP_HoldCreate_ValidationErrors covers 400/422/409 validation paths.
func TestHTTP_HoldCreate_ValidationErrors(t *testing.T) {
	e := newTestServer(t)

	t.Run("missing idempotency key returns 400", func(t *testing.T) {
		expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
		body := `{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"1","unit":"token"}}],"expires_at":"` + expires + `"}`
		rec, out := doJSON(t, e, "POST", "/v0.1/holds", body, nil)
		require.Equal(t, 400, rec.Code, out)
	})

	t.Run("short idempotency key returns 400", func(t *testing.T) {
		expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
		body := `{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"1","unit":"token"}}],"expires_at":"` + expires + `"}`
		rec, out := doJSON(t, e, "POST", "/v0.1/holds", body,
			map[string]string{"Idempotency-Key": "short"})
		require.Equal(t, 400, rec.Code, out)
	})

	t.Run("empty lines returns 400", func(t *testing.T) {
		expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
		body := `{"lines":[],"expires_at":"` + expires + `"}`
		rec, out := doJSON(t, e, "POST", "/v0.1/holds", body,
			map[string]string{"Idempotency-Key": "idem_empty_lines"})
		require.Equal(t, 400, rec.Code, out)
	})

	t.Run("past expires_at returns 422", func(t *testing.T) {
		past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
		body := `{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"1","unit":"token"}}],"expires_at":"` + past + `"}`
		rec, out := doJSON(t, e, "POST", "/v0.1/holds", body,
			map[string]string{"Idempotency-Key": "idem_past_expiry"})
		require.Equal(t, 422, rec.Code, out)
	})

	t.Run("oversell returns 409", func(t *testing.T) {
		expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
		// Seed inventory is 1,000,000,000; request 10^12 to trigger insufficient stock.
		body := `{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"1000000000000","unit":"token"}}],"expires_at":"` + expires + `"}`
		rec, out := doJSON(t, e, "POST", "/v0.1/holds", body,
			map[string]string{"Idempotency-Key": "idem_oversell"})
		require.Equal(t, 409, rec.Code, out)
	})
}

// TestHTTP_HoldConfirm_WrongFencingToken covers 409 fence mismatch over HTTP.
func TestHTTP_HoldConfirm_WrongFencingToken(t *testing.T) {
	e := newTestServer(t)

	expires := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	_, createResp := doJSON(t, e, "POST", "/v0.1/holds",
		`{"lines":[{"model_sku":"gpt-4","quantity":{"amount":"500","unit":"token"}}],"expires_at":"`+expires+`"}`,
		map[string]string{"Idempotency-Key": "idem_fence_create"})
	holdID := createResp["hold_id"].(string)

	rec, out := doJSON(t, e, "POST", "/v0.1/holds/"+holdID+"/confirm",
		`{"fencing_token": "wrong_token_value"}`,
		map[string]string{"Idempotency-Key": "idem_fence_confirm"})
	require.Equal(t, 409, rec.Code, out)
}

// TestHTTP_EstimateQuote_NeverCreatesHold exercises the contract invariant:
// quotes over HTTP must return hold_created=false and must not persist any hold.
func TestHTTP_EstimateQuote_NeverCreatesHold(t *testing.T) {
	db := testutil.SetupTestDB(t)
	srv := maas.NewServer(
		inventory.NewService(db.Pool),
		commercial.NewService(db.Pool),
		trading.NewService(db.Pool),
		ledger.NewService(db.Pool),
	)
	e := echo.New()
	srv.RegisterRoutes(e.Group("/v0.1"))

	rec, out := doJSON(t, e, "POST", "/v0.1/quotes:estimate",
		`{"model_sku":"gpt-4","quantity":{"amount":"1000000","unit":"token"},"resource_snapshot_id":"snap_test_001","price_version_id":"prv_2026_11_initial"}`,
		nil)
	require.Equal(t, 200, rec.Code, out)
	assert.Equal(t, false, out["hold_created"], "EstimateQuote must never create holds")

	var holdCount int
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM maas_holds WHERE tenant_id = 'ten_http_001'`).Scan(&holdCount))
	assert.Equal(t, 0, holdCount, "no holds may exist after EstimateQuote")
}

// TestHTTP_EntitlementActivate_FullChain exercises the serial gate over HTTP:
// committed hold → ActivateEntitlement (200 active).
func TestHTTP_EntitlementActivate_FullChain(t *testing.T) {
	e := newTestServer(t)

	holdID, _ := createCommittedHold(t, e, "idem_ent_chain", "2000")

	rec, actResp := doJSON(t, e, "POST", "/v0.1/entitlements:activate",
		`{"hold_id":"`+holdID+`","contract_id":"ctr_http_001"}`,
		map[string]string{"Idempotency-Key": "idem_ent_activate"})
	require.Equal(t, 200, rec.Code, actResp)
	assert.Equal(t, "active", actResp["status"])
	assert.Equal(t, holdID, actResp["hold_id"])
}

// TestHTTP_LedgerUsageFlow exercises UsageHold → UsageSettle over HTTP,
// verifying the balance invariant held = settled + released end to end.
func TestHTTP_LedgerUsageFlow(t *testing.T) {
	db := testutil.SetupTestDB(t)
	srv := maas.NewServer(
		inventory.NewService(db.Pool),
		commercial.NewService(db.Pool),
		trading.NewService(db.Pool),
		ledger.NewService(db.Pool),
	)
	e := echo.New()
	srv.RegisterRoutes(e.Group("/v0.1"))

	holdID, _ := createCommittedHold(t, e, "idem_led_flow", "5000")
	entID := activateEntitlement(t, e, "idem_led_flow", holdID)
	seedQuota(t, db, entID)

	// UsageHold 1000 tokens.
	rec, holdResp := doJSON(t, e, "POST", "/v0.1/usage-holds",
		`{"entitlement_id":"`+entID+`","request_id":"req_http_001","quantity":{"amount":"1000","unit":"token"},"model_sku":"gpt-4"}`,
		map[string]string{"Idempotency-Key": "idem_usage_hold"})
	require.Equal(t, 201, rec.Code, holdResp)
	usageHoldID := holdResp["usage_hold_id"].(string)

	// Settle 800 of 1000 → released must be 200.
	rec, settleResp := doJSON(t, e, "POST", "/v0.1/usage-holds/"+usageHoldID+"/settle",
		`{"actual":{"amount":"800","unit":"token"},"stream_interrupted":false}`,
		map[string]string{"Idempotency-Key": "idem_usage_settle"})
	require.Equal(t, 200, rec.Code, settleResp)
	settled := settleResp["settled"].(map[string]any)
	released := settleResp["released"].(map[string]any)
	assert.Equal(t, "800", settled["amount"])
	assert.Equal(t, "200", released["amount"])

	// Double settlement → 422 MAAS_USAGE_ALREADY_SETTLED.
	rec, dupResp := doJSON(t, e, "POST", "/v0.1/usage-holds/"+usageHoldID+"/settle",
		`{"actual":{"amount":"100","unit":"token"}}`,
		map[string]string{"Idempotency-Key": "idem_usage_settle_dup"})
	require.Equal(t, 422, rec.Code, dupResp)
}

// TestHTTP_QuotaExceeded verifies the quota gate over HTTP (400 MAAS_QUOTA_EXCEEDED).
func TestHTTP_QuotaExceeded(t *testing.T) {
	db := testutil.SetupTestDB(t)
	srv := maas.NewServer(
		inventory.NewService(db.Pool),
		commercial.NewService(db.Pool),
		trading.NewService(db.Pool),
		ledger.NewService(db.Pool),
	)
	e := echo.New()
	srv.RegisterRoutes(e.Group("/v0.1"))

	holdID, _ := createCommittedHold(t, e, "idem_quota", "3000")
	entID := activateEntitlement(t, e, "idem_quota", holdID)
	seedQuota(t, db, entID)

	// Request 2M tokens against 1M allocation → quota exceeded.
	rec, out := doJSON(t, e, "POST", "/v0.1/usage-holds",
		`{"entitlement_id":"`+entID+`","request_id":"req_quota_exceed","quantity":{"amount":"2000000","unit":"token"},"model_sku":"gpt-4"}`,
		map[string]string{"Idempotency-Key": "idem_quota_exceed"})
	require.Equal(t, 400, rec.Code, out)
}

// TestHTTP_ErrorBodyShape verifies the error envelope shape.
func TestHTTP_ErrorBodyShape(t *testing.T) {
	e := newTestServer(t)

	rec, out := doJSON(t, e, "POST", "/v0.1/holds", `{not-json}`, nil)
	require.Equal(t, 400, rec.Code)
	assert.NotEmpty(t, out["message"])
}
