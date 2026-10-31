package outbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/prometheus/client_golang/prometheus"
)

// TestDispatcher_Config tests dispatcher configuration defaults.
func TestDispatcher_Config(t *testing.T) {
	cfg := DispatcherConfig{
		DB:          &sql.DB{},
		ASMEndpoint: "http://asm:8080/internal/v1/events",
		HMACSecret:  "test-secret",
	}

	d := NewDispatcher(cfg)

	// Check defaults
	if d.pollInterval != 5*time.Second {
		t.Errorf("pollInterval = %v, want 5s", d.pollInterval)
	}
	if d.maxAttempts != 5 {
		t.Errorf("maxAttempts = %d, want 5", d.maxAttempts)
	}
	if d.logger == nil {
		t.Error("logger should default to slog.Default()")
	}
}

// TestDispatcher_ConfigCustom tests custom configuration.
func TestDispatcher_ConfigCustom(t *testing.T) {
	logger := slog.Default()
	cfg := DispatcherConfig{
		DB:           &sql.DB{},
		ASMEndpoint:  "http://custom:9090/events",
		HMACSecret:   "custom-secret",
		PollInterval: 10 * time.Second,
		MaxAttempts:  3,
		Logger:       logger,
	}

	d := NewDispatcher(cfg)

	if d.pollInterval != 10*time.Second {
		t.Errorf("pollInterval = %v, want 10s", d.pollInterval)
	}
	if d.maxAttempts != 3 {
		t.Errorf("maxAttempts = %d, want 3", d.maxAttempts)
	}
	if d.logger != logger {
		t.Error("logger should be custom logger")
	}
	if d.asmEndpoint != "http://custom:9090/events" {
		t.Errorf("asmEndpoint = %s", d.asmEndpoint)
	}
}

func TestDispatcherUpdateGaugeMetricsConcurrentThrottle(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM outbox_events WHERE status = 'pending'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM outbox_events WHERE status = 'dlq'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	d := NewDispatcher(DispatcherConfig{DB: mockDB, ASMEndpoint: "http://asm", HMACSecret: "test"})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.updateGaugeMetrics(context.Background())
		}()
	}
	wg.Wait()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDispatcher_Start tests that Start respects context cancellation.
func TestDispatcher_Start(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cfg := DispatcherConfig{
		// A DB whose connector always errors. The original fixture used a
		// zero-value &sql.DB{}, but database/sql panics (nil connector) on the
		// first QueryContext of such a value, which crashes the test instead
		// of exercising Start's "keep polling despite query errors" path.
		// errorConnector makes QueryContext return a real error, matching the
		// documented intent of this test.
		DB:           sql.OpenDB(errorConnector{}),
		ASMEndpoint:  "http://localhost:9999/events",
		HMACSecret:   "test",
		PollInterval: 100 * time.Millisecond,
	}

	d := NewDispatcher(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Start should block until ctx is cancelled
	err := d.Start(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Start() error = %v, want context.DeadlineExceeded", err)
	}
}

// TestDispatcher_EnvelopeMarshaling tests that dispatch correctly marshals EventEnvelope.
func TestDispatcher_EnvelopeMarshaling(t *testing.T) {
	env := EventEnvelope{
		EventID:          "evt-001",
		EventType:        "request.completed.v1",
		SchemaVersion:    1,
		TenantID:         "tenant-001",
		AggregateID:      "req-001",
		AggregateVersion: 1,
		OccurredAt:       time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
		Payload: map[string]any{
			"session_id": "session-001",
			"status":     "succeeded",
		},
	}

	// Marshal envelope as Dispatcher would
	envelopeJSON, err := json.Marshal(struct {
		EventID          string         `json:"event_id"`
		EventType        string         `json:"event_type"`
		SchemaVersion    int            `json:"schema_version"`
		TenantID         string         `json:"tenant_id"`
		AggregateID      string         `json:"aggregate_id"`
		AggregateVersion int            `json:"aggregate_version"`
		OccurredAt       time.Time      `json:"occurred_at"`
		Payload          map[string]any `json:"payload"`
	}{
		EventID:          env.EventID,
		EventType:        env.EventType,
		SchemaVersion:    env.SchemaVersion,
		TenantID:         env.TenantID,
		AggregateID:      env.AggregateID,
		AggregateVersion: env.AggregateVersion,
		OccurredAt:       env.OccurredAt,
		Payload:          env.Payload,
	})

	if err != nil {
		t.Fatalf("marshal envelope failed: %v", err)
	}

	// Verify JSON structure
	var parsed map[string]any
	if err := json.Unmarshal(envelopeJSON, &parsed); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}

	if parsed["event_id"] != "evt-001" {
		t.Errorf("event_id = %v", parsed["event_id"])
	}
	if parsed["event_type"] != "request.completed.v1" {
		t.Errorf("event_type = %v", parsed["event_type"])
	}
	if parsed["tenant_id"] != "tenant-001" {
		t.Errorf("tenant_id = %v", parsed["tenant_id"])
	}

	payload, ok := parsed["payload"].(map[string]any)
	if !ok {
		t.Fatal("payload is not a map")
	}
	if payload["session_id"] != "session-001" {
		t.Errorf("payload.session_id = %v", payload["session_id"])
	}
}

// TestDispatcher_SignatureGeneration tests that dispatcher generates valid signatures.
func TestDispatcher_SignatureGeneration(t *testing.T) {
	secret := "test-hmac-secret"
	data := []byte(`{"event_id":"evt-001","tenant_id":"tenant-001"}`)

	// Compute signature as Dispatcher would
	signature := computeHMAC(data, secret)

	// Signature should be 64-char hex
	if len(signature) != 64 {
		t.Errorf("signature length = %d, want 64", len(signature))
	}

	// ASM should be able to verify
	if !VerifyHMAC(data, secret, signature) {
		t.Error("ASM would reject this signature")
	}
}

// errorConnector is a database/sql/driver.Connector whose Connect always fails.
// It backs a *sql.DB that returns query errors instead of panicking, so tests
// can exercise error-handling paths (e.g. the dispatcher's "keep polling"
// resilience) without a live database.
type errorConnector struct{}

func (errorConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("test: database connector unavailable")
}
func (errorConnector) Driver() driver.Driver { return nil }

// -----------------------------------------------------------------------
// Metrics wiring tests
//
// These tests pin the contract that dispatchOne records Prometheus
// counters/histograms on every outcome path:
//   - success  → RecordEventSent + ObserveDeliveryDuration(success=true)
//   - http fail (not at max) → RecordEventFailed(reason) + RecordEventRetried
//   - http fail (at max, moved to DLQ) → RecordEventFailed(reason) only
//   - corrupt payload → RecordEventFailed("validation") only — markDLQ
//     directly, no retry (a malformed payload can never succeed)
//   - no claimable event → no metric changes
//
// Without these, the dispatcher could ship without ever incrementing the
// counters that the alert rules and Grafana dashboards depend on.
// -----------------------------------------------------------------------

// outboxCounters captures the four writable metrics under test.
type outboxCounters struct {
	sent    float64
	failed  float64 // summed across all reason labels
	retried float64
}

// sumMetricVecName sums a Counter / CounterVec / HistogramVec across every
// observed label combination by draining it through the default Prometheus
// gatherer.  testutil.ToFloat64 panics on CounterVec labels that haven't
// been observed yet, so we gather+sum instead.
func sumMetricVecName(name string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			switch {
			case m.GetCounter() != nil:
				total += m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				total += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return total
}

func readOutboxCounters(t *testing.T) outboxCounters {
	t.Helper()
	return outboxCounters{
		sent:    sumMetricVecName("outbox_events_sent_total"),
		failed:  sumMetricVecName("outbox_events_failed_total"),
		retried: sumMetricVecName("outbox_events_retried_total"),
	}
}

// snapshot counter deltas around the action under test.
func counterDelta(t *testing.T, action func()) (before, after outboxCounters) {
	t.Helper()
	before = readOutboxCounters(t)
	action()
	after = readOutboxCounters(t)
	return before, after
}

// newDispatcherWithMockDB wires a sqlmock-backed *sql.DB into a real
// Dispatcher, plus a control function for the ASM HTTP server.
func newDispatcherWithMockDB(t *testing.T) (*Dispatcher, sqlmock.Sqlmock, *atomic.Int32) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	hits := new(atomic.Int32)
	mux := http.NewServeMux()
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Default: 200; tests can override via the response-capture on the
		// httptest server they wrap this Dispatcher in. We use a bare 200
		// here because success-path tests assert against the actual response.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":{"accepted":1,"duplicates":0,"failed":[]}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := NewDispatcher(DispatcherConfig{
		DB:           db,
		ASMEndpoint:  srv.URL + "/events",
		HMACSecret:   "test-secret",
		PollInterval: time.Hour, // disabled; we drive dispatchOne manually
		MaxAttempts:  3,
		HTTPTimeout:  2 * time.Second,
		Logger:       slog.Default(),
	})
	return d, mock, hits
}

// outboxRow models the SELECT shape produced by dispatcher's claim query.
func outboxRow(id int64, eventID string, attempts int) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "event_id", "event_type", "schema_version", "tenant_id",
		"aggregate_id", "aggregate_version", "occurred_at", "payload",
		"status", "attempts", "last_error", "next_retry_at",
	}).AddRow(
		id, eventID, "request.completed.v1", 1, "default",
		"gw_session_001", 1, time.Now(),
		[]byte(`{"session_id":"gw_session_001","status":"succeeded"}`),
		"pending", attempts, nil, nil,
	)
}

func TestDispatcher_DispatchOne_SuccessIncrementsSent(t *testing.T) {
	d, mock, _ := newDispatcherWithMockDB(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(1, "evt-success", 1))
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'sent', last_attempt_at = NOW(), last_error = NULL,\n\t\t    next_retry_at = NULL, updated_at = NOW()\n\t\tWHERE id = $1").WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	before, after := counterDelta(t, func() {
		outcome, err := d.dispatchOne(context.Background())
		if err != nil {
			t.Fatalf("dispatchOne: %v", err)
		}
		if outcome != dispatchOutcomeSent {
			t.Errorf("outcome = %v, want dispatchOutcomeSent", outcome)
		}
	})

	if got := after.sent - before.sent; got != 1 {
		t.Errorf("RecordEventSent delta = %v, want 1", got)
	}
	if got := after.failed - before.failed; got != 0 {
		t.Errorf("RecordEventFailed should not increment on success: delta = %v", got)
	}
	if got := after.retried - before.retried; got != 0 {
		t.Errorf("RecordEventRetried should not increment on success: delta = %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestDispatcher_DispatchOne_HTTPFailureIncrementsFailedAndRetried(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Point at a closed-loopback port: deterministic connection refused.
	d := NewDispatcher(DispatcherConfig{
		DB:           db,
		ASMEndpoint:  "http://127.0.0.1:1/events",
		HMACSecret:   "test-secret",
		PollInterval: time.Hour,
		MaxAttempts:  3,
		HTTPTimeout:  500 * time.Millisecond,
		Logger:       slog.Default(),
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(2, "evt-fail", 1))
	// attempts+1 = 2 < maxAttempts = 3, so markFailed schedules a retry.
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'failed', attempts = $1, last_error = $2,\n\t\t    last_attempt_at = NOW(), next_retry_at = $3, updated_at = NOW()\n\t\tWHERE id = $4").WithArgs(2, sqlmock.AnyArg(), sqlmock.AnyArg(), int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	before, after := counterDelta(t, func() {
		outcome, err := d.dispatchOne(context.Background())
		if err != nil {
			t.Fatalf("dispatchOne: %v", err)
		}
		if outcome != dispatchOutcomeFailed {
			t.Errorf("outcome = %v, want dispatchOutcomeFailed", outcome)
		}
	})

	if got := after.failed - before.failed; got != 1 {
		t.Errorf("RecordEventFailed delta = %v, want 1", got)
	}
	if got := after.retried - before.retried; got != 1 {
		t.Errorf("RecordEventRetried delta = %v, want 1 (attempts=1, max=3)", got)
	}
	if got := after.sent - before.sent; got != 0 {
		t.Errorf("RecordEventSent should not increment on failure: delta = %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestDispatcher_DispatchOne_FailureAtMaxAttemptsNoRetry(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	d := NewDispatcher(DispatcherConfig{
		DB:           db,
		ASMEndpoint:  "http://127.0.0.1:1/events",
		HMACSecret:   "test-secret",
		PollInterval: time.Hour,
		MaxAttempts:  3,
		HTTPTimeout:  500 * time.Millisecond,
		Logger:       slog.Default(),
	})

	// attempts=2 → markFailed uses newAttempts=3 == maxAttempts → DLQ path,
	// no retry counter increment.
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(3, "evt-dlq", 2))
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'dlq', attempts = $1, last_error = $2,\n\t\t    last_attempt_at = NOW(), next_retry_at = NULL, updated_at = NOW()\n\t\tWHERE id = $3").WithArgs(3, sqlmock.AnyArg(), int64(3)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	before, after := counterDelta(t, func() {
		outcome, err := d.dispatchOne(context.Background())
		if err != nil {
			t.Fatalf("dispatchOne: %v", err)
		}
		if outcome != dispatchOutcomeFailed {
			t.Errorf("outcome = %v, want dispatchOutcomeFailed", outcome)
		}
	})

	if got := after.failed - before.failed; got != 1 {
		t.Errorf("RecordEventFailed delta = %v, want 1", got)
	}
	if got := after.retried - before.retried; got != 0 {
		t.Errorf("RecordEventRetried should NOT increment at max attempts: delta = %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestDispatcher_DispatchOne_NoRowsNoMetricChange(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	d := NewDispatcher(DispatcherConfig{
		DB:           db,
		ASMEndpoint:  "http://127.0.0.1:1/events",
		HMACSecret:   "test-secret",
		PollInterval: time.Hour,
		MaxAttempts:  3,
		HTTPTimeout:  500 * time.Millisecond,
		Logger:       slog.Default(),
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnError(sql.ErrNoRows)

	before, after := counterDelta(t, func() {
		outcome, err := d.dispatchOne(context.Background())
		if err != nil {
			t.Fatalf("dispatchOne: %v", err)
		}
		if outcome != dispatchOutcomeNone {
			t.Errorf("outcome = %v, want dispatchOutcomeNone", outcome)
		}
	})

	if before.sent != after.sent || before.failed != after.failed || before.retried != after.retried {
		t.Errorf("no-rows path must not touch counters: before=%+v after=%+v", before, after)
	}
}

func TestDispatcher_DispatchOne_DurationHistogramObserved(t *testing.T) {
	// We don't compare bucket counts (verbose, default-bucket dependent).
	// Instead we pin that ObserveDeliveryDuration is called on the success
	// path by counting the histogram's series post-call.
	d, mock, _ := newDispatcherWithMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(4, "evt-duration", 1))
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'sent', last_attempt_at = NOW(), last_error = NULL,\n\t\t    next_retry_at = NULL, updated_at = NOW()\n\t\tWHERE id = $1").WithArgs(int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	outcome, err := d.dispatchOne(context.Background())
	if err != nil {
		t.Fatalf("dispatchOne: %v", err)
	}
	if outcome != dispatchOutcomeSent {
		t.Fatalf("outcome = %v, want dispatchOutcomeSent", outcome)
	}
	// After one observation the histogram must have at least one labelled
	// series. Use Gather rather than CollectAndCount to avoid relying on a
	// specific metric-name string format.
	if got := sumMetricVecName("outbox_delivery_duration_seconds"); got == 0 {
		t.Errorf("expected at least one histogram observation after success")
	}
}

func TestDispatcher_DispatchOne_TerminalHTTPFailureMovesDirectlyToDLQ(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "schema invalid", http.StatusUnprocessableEntity)
	}))
	defer server.Close()

	d := NewDispatcher(DispatcherConfig{
		DB: db, ASMEndpoint: server.URL, HMACSecret: "test-secret",
		PollInterval: time.Hour, MaxAttempts: 5, HTTPTimeout: time.Second,
	})
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(5, "evt-terminal", 0))
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'dlq', attempts = $1, last_error = $2,\n\t\t    last_attempt_at = NOW(), next_retry_at = NULL, updated_at = NOW()\n\t\tWHERE id = $3").WithArgs(1, sqlmock.AnyArg(), int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	before, after := counterDelta(t, func() {
		outcome, err := d.dispatchOne(context.Background())
		if err != nil {
			t.Fatalf("dispatchOne: %v", err)
		}
		if outcome != dispatchOutcomeFailed {
			t.Fatalf("outcome = %v, want dispatchOutcomeFailed", outcome)
		}
	})
	if got := after.retried - before.retried; got != 0 {
		t.Fatalf("terminal response retry metric delta = %v, want 0", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// TestClassifyError covers the reason labels emitted by RecordEventFailed.
// Keeps alert/dashboard wiring honest if a future edit broadens the label set.
func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "unknown"},
		{"timeout", fmt.Errorf("context deadline exceeded"), "timeout"},
		{"network", fmt.Errorf("connection refused"), "network"},
		{"hmac_mismatch", fmt.Errorf("401 unauthorized: signature mismatch"), "hmac_mismatch"},
		{"tenant_mismatch", fmt.Errorf("403 forbidden: tenant not allowed"), "tenant_mismatch"},
		{"validation", fmt.Errorf("422 unprocessable: validation failed"), "validation"},
		{"duplicate", fmt.Errorf("409 conflict: duplicate event_id"), "duplicate"},
		{"http_5xx", fmt.Errorf("asm returned 502: bad gateway"), "http_5xx"},
		{"http_4xx_fallback", fmt.Errorf("asm returned 418: teapot"), "http_4xx"},
		{"http_error_other", fmt.Errorf("asm returned 301: moved"), "http_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyError(tt.err); got != tt.want {
				t.Errorf("classifyError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestDispatcher_DispatchOne_BackpressureNeverDLQs —— 2026-10-28 生产审计轮：
// asm 就绪门抖动（503 ingress_unavailable / event_retryable）以分钟计，而
// maxAttempts=5 × 指数退避（≈31s 窗口）曾把 34,793 条**完全合法**的事件永久打进
// DLQ。⇒ 背压类失败改为「不计数转 DLQ、64s 封顶退避无限重试」：
// 本用例钉「在 maxAttempts 处背压 503 仍走 failed 退避而不是 dlq」。
func TestDispatcher_DispatchOne_BackpressureNeverDLQs(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// 真 503 + asm 背压错误码（连接拒绝对 DeliveryError 形态不可达 ⇒ 用 httptest）。
	var delivererTarget string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"ingress_unavailable","message":"event consumer is unavailable"}`))
	}))
	t.Cleanup(srv.Close)
	delivererTarget = srv.URL + "/events"

	d := NewDispatcher(DispatcherConfig{
		DB:           db,
		ASMEndpoint:  delivererTarget,
		HMACSecret:   "test-secret",
		PollInterval: time.Hour,
		MaxAttempts:  3,
		HTTPTimeout:  500 * time.Millisecond,
		Logger:       slog.Default(),
	})

	// attempts=2 → attempts+1=3 == maxAttempts=3：旧行为会走 markDLQ，
	// 新行为（背压）必须走 failed 退避 —— 期待的是 failed UPDATE 且 **不是** dlq UPDATE。
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(7, "evt-bp", 2))
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'failed', attempts = $1, last_error = $2,\n\t\t    last_attempt_at = NOW(), next_retry_at = $3, updated_at = NOW()\n\t\tWHERE id = $4").WithArgs(3, sqlmock.AnyArg(), sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	outcome, err := d.dispatchOne(context.Background())
	if err != nil {
		t.Fatalf("dispatchOne: %v", err)
	}
	if outcome != dispatchOutcomeBackpressure {
		t.Errorf("outcome = %v, want dispatchOutcomeBackpressure", outcome)
	}
	if !d.backpressurePaused(time.Now()) {
		t.Error("backpressure failure must trip the dispatcher-level pause")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations（背压在 maxAttempts 处必须走 failed 退避，绝不落 dlq）: %v", err)
	}
}

// TestDispatcher_BackpressureStopsBatch —— 熔断的核心契约：批内**第一条**背压
// 必须停批，积压里剩下的可 claim 事件**一次都不许被碰**（2026-10-28 生产实测：
// 4191 条积压 × 各自 64s 退避 = ~65 req/s 敲一个关门的入口，asm inbox 排空 0）。
// sqlmock 按序断言：恰好一次 claim + 一次背压落账，第二条事件绝不出现。
func TestDispatcher_BackpressureStopsBatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"event_retryable","message":"event consumption will be retried"}`))
	}))
	t.Cleanup(srv.Close)

	d := NewDispatcher(DispatcherConfig{
		DB:           db,
		ASMEndpoint:  srv.URL + "/events",
		HMACSecret:   "test-secret",
		PollInterval: time.Hour,
		MaxAttempts:  5,
		HTTPTimeout:  500 * time.Millisecond,
		Logger:       slog.Default(),
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, event_id, event_type, schema_version, tenant_id,\n\t\t       aggregate_id, aggregate_version, occurred_at, payload,\n\t\t       status, attempts, last_error, next_retry_at\n\t\tFROM outbox_events\n\t\tWHERE (status = 'pending' OR (status = 'failed' AND next_retry_at <= NOW()))\n\t\tORDER BY occurred_at ASC\n\t\tLIMIT 1\n\t\tFOR UPDATE SKIP LOCKED").WillReturnRows(outboxRow(7, "evt-bp-1", 0))
	mock.ExpectExec("UPDATE outbox_events\n\t\tSET status = 'failed', attempts = $1, last_error = $2,\n\t\t    last_attempt_at = NOW(), next_retry_at = $3, updated_at = NOW()\n\t\tWHERE id = $4").WithArgs(1, sqlmock.AnyArg(), sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	// 注意：这里**故意不**再给第二条 claim 的期望 —— 停批契约下它不该被
	// 触碰；若实现回退成逐条重试，sqlmock 会以「unexpected call」报红。

	if err := d.dispatchBatch(context.Background()); err != nil {
		t.Fatalf("dispatchBatch: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations（第一条背压后必须停批，不得 claim 第二条）: %v", err)
	}
	if !d.backpressurePaused(time.Now()) {
		t.Error("batch abort on backpressure must leave the loop paused")
	}
}

// TestDispatcher_BackpressurePauseWindow —— 暂停窗口语义：未触发 ⇒ 不暂停；
// 触发 ⇒ 窗口内暂停、窗口外恢复；窗口内重复触发 ⇒ 延长窗口（不重启计时）。
func TestDispatcher_BackpressurePauseWindow(t *testing.T) {
	db, _, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	d := NewDispatcher(DispatcherConfig{
		DB:                   db,
		ASMEndpoint:          "http://asm/events",
		HMACSecret:           "test-secret",
		PollInterval:         time.Hour,
		BackpressureCooldown: time.Minute,
		Logger:               slog.Default(),
	})

	base := time.Unix(1_800_000_000, 0)
	if d.backpressurePaused(base) {
		t.Error("fresh dispatcher must not be paused")
	}

	d.tripBackpressurePause(base)
	if !d.backpressurePaused(base.Add(59 * time.Second)) {
		t.Error("must stay paused just before cooldown expiry")
	}
	if d.backpressurePaused(base.Add(61 * time.Second)) {
		t.Error("must resume after cooldown expiry")
	}

	// 窗口内的第二次触发：窗口从**触发时刻**起重新计算（延长），不是从
	// 原窗口末尾叠倍 —— 否则长抖动会把暂停拖成分钟级盲区。
	// 判别点选在 90s~120s 之间：延长 ⇒ 90s 结束（100s 已恢复）；
	// 叠倍 ⇒ 120s 结束（100s 仍在暂停）。
	d.tripBackpressurePause(base.Add(30 * time.Second))
	if !d.backpressurePaused(base.Add(89 * time.Second)) {
		t.Error("second trip window (until 30s+60s=90s) must still be active at 89s")
	}
	if d.backpressurePaused(base.Add(100 * time.Second)) {
		t.Error("second trip must extend from the trip moment (ends at 90s), not stack on the old window (120s)")
	}
}

// TestDispatcher_Start_PauseProbesOncePerCooldown —— Start 级别的风暴抑制：
// 暂停窗口内，Start 的 tick 循环不许发出任何 HTTP 探测（旧行为每 tick 一次
// ⇒ 4191 条积压时 ~65 req/s）。此处不测「cooldown 到期后重新探测」的恢复
// 路径 —— 那是时间驱动的正常轮询，由 TestDispatcher_BackpressurePauseWindow
// 的窗口断言与既有 dispatchOne 覆盖。
func TestDispatcher_Start_PauseProbesOncePerCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive in short mode")
	}

	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"event_retryable","message":"event consumption will be retried"}`))
	}))
	t.Cleanup(srv.Close)

	// 真 DB 不参与断言：暂停判据在 HTTP 之前，claim 根本不会发生。
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// 暂停期内每个 tick 仍会刷新仪表（30s 节流 ⇒ 全程恰好一对 COUNT）。
	mock.ExpectQuery("SELECT COUNT(*) FROM outbox_events WHERE status = 'pending'").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4191))
	mock.ExpectQuery("SELECT COUNT(*) FROM outbox_events WHERE status = 'dlq'").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(34183))

	d := NewDispatcher(DispatcherConfig{
		DB:                   db,
		ASMEndpoint:          srv.URL + "/events",
		HMACSecret:           "test-secret",
		PollInterval:         20 * time.Millisecond,
		BackpressureCooldown: 300 * time.Millisecond,
		HTTPTimeout:          500 * time.Millisecond,
		Logger:               slog.Default(),
	})
	d.tripBackpressurePause(time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	_ = d.Start(ctx) // ends with context.DeadlineExceeded

	if got := attempts.Load(); got != 0 {
		t.Errorf("HTTP attempts during pause = %d, want 0（暂停期内一次请求都不该发出）", got)
	}
}

// TestIsBackpressure_ClassifiesASMCodes —— 判据自身：503 + asm 两个背压码才
// 算背压；其它 5xx、其它码、连接错误都不算。
func TestIsBackpressure_ClassifiesASMCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"503 ingress_unavailable", &DeliveryError{StatusCode: 503, Body: `{"code":"ingress_unavailable","message":"event consumer is unavailable"}`}, true},
		{"503 event_retryable", &DeliveryError{StatusCode: 503, Body: `{"code":"event_retryable","message":"event consumption will be retried"}`}, true},
		{"503 other code", &DeliveryError{StatusCode: 503, Body: `{"code":"rate_limited"}`}, false},
		{"500 ingress_unavailable（非 503）", &DeliveryError{StatusCode: 500, Body: `{"code":"ingress_unavailable"}`}, false},
		{"502 bad gateway", &DeliveryError{StatusCode: 502, Body: "bad gateway"}, false},
		{"连接错误（非 DeliveryError）", fmt.Errorf("post delivery request: dial tcp: connect refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := IsBackpressure(tc.err); got != tc.want {
			t.Errorf("%s: IsBackpressure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBackpressureBackoff_Exponential —— 2026-10-28 生产审计轮：固定 64s 在
// 长窗口背压下退化为「61 req/s 永久锤击」（asm 连续 503 十小时、积压 3.9k 条、
// 8 分钟 2.9 万次失败尝试）。退避改为 8s × 2^(attempts-1)、封顶 10min：
// 本表钉「首拍 8s / 每次翻倍 / 大 attempts 与溢出都落 cap」三个承重点。
func TestBackpressureBackoff_Exponential(t *testing.T) {
	cases := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{"首次背压 attempts=1 → 8s", 1, 8 * time.Second},
		{"attempts=2 → 16s", 2, 16 * time.Second},
		{"attempts=3 → 32s", 3, 32 * time.Second},
		{"attempts=4 → 64s（旧封顶值只是一档）", 4, 64 * time.Second},
		{"attempts=6 → 256s", 6, 256 * time.Second},
		{"attempts=8 越过 cap → 10min", 8, 10 * time.Minute},
		{"老积压 attempts=665 → 10min（不溢出）", 665, 10 * time.Minute},
		{"防御 attempts=0 → 地板 8s", 0, 8 * time.Second},
	}
	for _, tc := range cases {
		if got := backpressureBackoff(tc.attempts); got != tc.want {
			t.Errorf("%s: backpressureBackoff(%d) = %v, want %v", tc.name, tc.attempts, got, tc.want)
		}
	}
}
