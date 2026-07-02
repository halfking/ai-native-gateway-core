package credential

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// newPGMockStore 构造一个带 pgxmock 后端的 PostgresReputationStore
func newPGMockStore(t *testing.T) (*PostgresReputationStore, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock new: %v", err)
	}
	t.Cleanup(mock.Close)
	return newPostgresReputationStoreWithDB(mock), mock
}

func TestPostgresStore_GetTimeseries_Success(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()

	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{
		"id", "provider_id", "model", "date",
		"reliability_score", "avg_latency_ms", "error_rate",
		"request_count", "success_count",
		"bandit_alpha", "bandit_beta", "success_rate",
		"rate_limit_errors", "quota_errors", "auth_errors",
		"timeout_errors", "network_errors", "other_errors",
		"created_at", "updated_at",
	}).AddRow(
		int64(1), "p1", "m1", now.AddDate(0, 0, -1),
		0.95, 250.0, 0.05,
		int64(100), int64(95),
		10.0, 2.0, 0.95,
		0, 0, 0, 0, 0, 0,
		now, now,
	)

	mock.ExpectQuery(`SELECT id, provider_id, model, date`).
		WithArgs("p1", "m1", pgxmock.AnyArg()).
		WillReturnRows(rows)

	out, err := store.GetTimeseries(ctx, "p1", "m1", 7)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 row, got %d", len(out))
	}
	if out[0].ProviderID != "p1" || out[0].Model != "m1" {
		t.Errorf("unexpected row: %+v", out[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPostgresStore_GetTimeseries_QueryError(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	mock.ExpectQuery(`SELECT id, provider_id, model, date`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("connection refused"))
	if _, err := store.GetTimeseries(ctx, "p", "m", 7); err == nil {
		t.Error("expected error")
	}
}

func TestPostgresStore_SaveTimeseries_Success(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	mock.ExpectExec(`INSERT INTO provider_reputation_timeseries`).
		WithArgs(
			"p", "m", pgxmock.AnyArg(), // provider, model, date
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	row := &TimeseriesRow{
		ProviderID: "p", Model: "m",
		Date:             time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		ReliabilityScore: 0.95, AvgLatencyMs: 200, ErrorRate: 0.05,
		RequestCount: 100, SuccessCount: 95,
	}
	if err := store.SaveTimeseries(ctx, row); err != nil {
		t.Fatalf("err: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestPostgresStore_SaveTimeseries_Validation(t *testing.T) {
	store, _ := newPGMockStore(t)
	ctx := context.Background()
	if err := store.SaveTimeseries(ctx, nil); err == nil {
		t.Error("expected error for nil row")
	}
	if err := store.SaveTimeseries(ctx, &TimeseriesRow{Model: "m"}); err == nil {
		t.Error("expected error for missing provider")
	}
}

func TestPostgresStore_ListProviderModelPairs_Success(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	rows := pgxmock.NewRows([]string{"provider_id", "model"}).
		AddRow("a", "m1").
		AddRow("b", "m2")
	mock.ExpectQuery(`SELECT DISTINCT provider_id, model`).
		WillReturnRows(rows)
	pairs, err := store.ListProviderModelPairs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Errorf("expected 2 pairs, got %d", len(pairs))
	}
}

func TestPostgresStore_GetRecentIncidents_WithModel(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()

	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{
		"id", "provider_id", "model", "incident_type", "impact_level",
		"description", "started_at", "ended_at", "duration_seconds",
		"affected_requests", "affected_tenants",
		"resolved", "resolution_notes",
		"created_at", "updated_at",
	}).AddRow(
		int64(1), "p", "m", "outage", "high",
		"description", now.Add(-1*time.Hour), now.Add(-30*time.Minute), int(1800),
		int64(100), 2,
		true, "fixed",
		now, now,
	)
	mock.ExpectQuery(`FROM provider_incidents`).
		WithArgs("p", "m", pgxmock.AnyArg()).
		WillReturnRows(rows)

	out, err := store.GetRecentIncidents(ctx, "p", "m", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(out))
	}
	if out[0].Type != IncidentOutage {
		t.Errorf("type mismatch: %s", out[0].Type)
	}
	if out[0].Impact != ImpactHigh {
		t.Errorf("impact mismatch: %s", out[0].Impact)
	}
	if out[0].Duration != 30*time.Minute {
		t.Errorf("duration mismatch: %v", out[0].Duration)
	}
}

func TestPostgresStore_GetRecentIncidents_NoModel(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	rows := pgxmock.NewRows([]string{
		"id", "provider_id", "model", "incident_type", "impact_level",
		"description", "started_at", "ended_at", "duration_seconds",
		"affected_requests", "affected_tenants",
		"resolved", "resolution_notes",
		"created_at", "updated_at",
	})
	mock.ExpectQuery(`FROM provider_incidents`).
		WithArgs("p", pgxmock.AnyArg()).
		WillReturnRows(rows)
	out, err := store.GetRecentIncidents(ctx, "p", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0, got %d", len(out))
	}
}

func TestPostgresStore_GetUnresolvedIncidents_WithModel(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	rows := pgxmock.NewRows([]string{
		"id", "provider_id", "model", "incident_type", "impact_level",
		"description", "started_at", "ended_at", "duration_seconds",
		"affected_requests", "affected_tenants",
		"resolved", "resolution_notes",
		"created_at", "updated_at",
	})
	mock.ExpectQuery(`FROM provider_incidents`).
		WithArgs("p", "m").
		WillReturnRows(rows)
	out, err := store.GetUnresolvedIncidents(ctx, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0, got %d", len(out))
	}
}

func TestPostgresStore_GetUnresolvedIncidents_NoModel(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	rows := pgxmock.NewRows([]string{
		"id", "provider_id", "model", "incident_type", "impact_level",
		"description", "started_at", "ended_at", "duration_seconds",
		"affected_requests", "affected_tenants",
		"resolved", "resolution_notes",
		"created_at", "updated_at",
	})
	mock.ExpectQuery(`FROM provider_incidents`).
		WithArgs("p").
		WillReturnRows(rows)
	if _, err := store.GetUnresolvedIncidents(ctx, "p", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStore_RecordIncident_Success(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	// INSERT ... RETURNING id, created_at, updated_at
	mock.ExpectQuery(`INSERT INTO provider_incidents`).
		WithArgs(
			"p", "m", "outage", "high",
			"desc", pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			false, "",
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(int64(42), now, now))

	inc := &Incident{
		ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh,
		Description: "desc", StartedAt: now,
	}
	if err := store.RecordIncident(ctx, inc); err != nil {
		t.Fatal(err)
	}
	if inc.ID != 42 {
		t.Errorf("expected ID=42, got %d", inc.ID)
	}
}

func TestPostgresStore_RecordIncident_Validation(t *testing.T) {
	store, _ := newPGMockStore(t)
	if err := store.RecordIncident(context.Background(), nil); err == nil {
		t.Error("expected error for nil incident")
	}
	if err := store.RecordIncident(context.Background(), &Incident{ProviderID: "p"}); err == nil {
		t.Error("expected error for missing type")
	}
}

func TestPostgresStore_ResolveIncident_Success(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	mock.ExpectExec(`UPDATE provider_incidents`).
		WithArgs(int64(7), "fixed").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.ResolveIncident(ctx, 7, "fixed"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStore_ResolveIncident_NotFound(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	mock.ExpectExec(`UPDATE provider_incidents`).
		WithArgs(int64(99), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if err := store.ResolveIncident(ctx, 99, "x"); !errors.Is(err, ErrReputationNotFound) {
		t.Errorf("expected ErrReputationNotFound, got %v", err)
	}
}

func TestPostgresStore_RecordIncidentIfNotExists_New(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	// 1) dedupe SELECT (returns no rows)
	mock.ExpectQuery(`SELECT id FROM provider_incidents`).
		WithArgs("p", "m", "outage", pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	// 2) INSERT ... RETURNING
	mock.ExpectQuery(`INSERT INTO provider_incidents`).
		WithArgs(
			"p", "m", "outage", "high",
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			false, "",
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(int64(1), now, now))

	inc := &Incident{
		ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh,
		StartedAt: now,
	}
	created, err := store.RecordIncidentIfNotExists(ctx, inc, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Errorf("expected created=true")
	}
}

func TestPostgresStore_RecordIncidentIfNotExists_Dedup(t *testing.T) {
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	// dedupe SELECT returns existing id
	mock.ExpectQuery(`SELECT id FROM provider_incidents`).
		WithArgs("p", "m", "outage", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(99)))

	inc := &Incident{ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh}
	created, err := store.RecordIncidentIfNotExists(ctx, inc, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Errorf("expected dedupe (created=false)")
	}
}

func TestPostgresStore_RecordIncidentIfNotExists_NoModel(t *testing.T) {
	// 当 model 为空时跳过 dedupe 直接插入
	store, mock := newPGMockStore(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`INSERT INTO provider_incidents`).
		WithArgs(
			"p", nil, "outage", "high",
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(),
			false, "",
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(int64(1), now, now))

	inc := &Incident{ProviderID: "p", Type: IncidentOutage, Impact: ImpactHigh, StartedAt: now}
	created, err := store.RecordIncidentIfNotExists(ctx, inc, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Errorf("expected created=true (model empty bypasses dedupe)")
	}
}

// pgxRowNotFound removed — using pgx.ErrNoRows directly.
