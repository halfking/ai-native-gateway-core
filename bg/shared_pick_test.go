package bg

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// TestPickProbeModelForCredential_ManualSource_PreservesAdminPin verifies that a
// credential with default_probe_model_source='manual' returns immediately with
// the admin-pinned model — none of the auto-pick branches should fire.
func TestPickProbeModelForCredential_ManualSource_PreservesAdminPin(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 11
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("claude-opus-4.7", "manual", "active", "active", false))

	// No further queries expected — manual pin short-circuits everything.

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "claude-opus-4.7" || got.Source != "manual" {
		t.Errorf("got %+v, want {Model:claude-opus-4.7 Source:manual}", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickProbeModelForCredential_RequestLog_TopHit verifies Priority 1:
// the most-used client_model from request_logs (7d) wins when its binding
// is still available.
func TestPickProbeModelForCredential_RequestLog_TopHit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 12
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "active", false))

	// Priority 1: top client_model from request_logs.
	mock.ExpectQuery("SELECT client_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"client_model"}).AddRow("gpt-4o"))

	// bindingAvailableForModel — EXISTS check that returns true.
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(credID, "gpt-4o").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "gpt-4o" || got.Source != "auto:request_log" {
		t.Errorf("got %+v, want {Model:gpt-4o Source:auto:request_log}", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickProbeModelForCredential_DomesticFeatured_StandardizedNameHit is the
// core new behaviour: a domestic provider whose binding has a
// standardized_name that appears in routing_policy.featured_models must
// surface that model — even if the random-pick fallback would have chosen
// a different (legacy / obscure) binding.
func TestPickProbeModelForCredential_DomesticFeatured_StandardizedNameHit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 21
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "active", false))

	// Priority 1: no request_logs hits.
	mock.ExpectQuery("SELECT client_model").
		WithArgs(credID).
		WillReturnError(errNoRows)

	// Provider is domestic.
	mock.ExpectQuery("SELECT p\\.domestic").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"domestic"}).AddRow(true))

	// Priority 2a: featured query. Expect the bind to pick a featured
	// standardized_name (sorted by standardized_name, LIMIT 1).
	mock.ExpectQuery("pm\\.standardized_name = ANY").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"probe_model"}).AddRow("gpt-4o"))

	// Priority 2b must NOT fire — featured hit is final.
	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "gpt-4o" {
		t.Errorf("Model: got %q want %q", got.Model, "gpt-4o")
	}
	if got.Source != "auto:domestic_featured" {
		t.Errorf("Source: got %q want %q (featured pick must beat random fallback)", got.Source, "auto:domestic_featured")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickProbeModelForCredential_DomesticFeatured_RawModelNameFallback
// confirms the OR clause that keeps raw_model_name matches working for
// providers whose standardized_name is NULL (older discovery data).
func TestPickProbeModelForCredential_DomesticFeatured_RawModelNameFallback(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 22
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "active", false))

	mock.ExpectQuery("SELECT client_model").
		WithArgs(credID).
		WillReturnError(errNoRows)

	mock.ExpectQuery("SELECT p\\.domestic").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"domestic"}).AddRow(true))

	// Featured query still hits because raw_model_name matches featured_models
	// even though standardized_name is NULL — the OR clause catches it.
	mock.ExpectQuery("pm\\.standardized_name = ANY").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"probe_model"}).AddRow("claude-3-5-sonnet-20241022"))

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("Model: got %q want %q", got.Model, "claude-3-5-sonnet-20241022")
	}
	if got.Source != "auto:domestic_featured" {
		t.Errorf("Source: got %q want %q", got.Source, "auto:domestic_featured")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickProbeModelForCredential_DomesticFeatured_MultipleHitsStableOrder
// guarantees that when a credential has multiple featured bindings, the
// picker is deterministic (ORDER BY standardized_name LIMIT 1) — otherwise
// repickAll cycles would flip the model every hour and confuse audits.
func TestPickProbeModelForCredential_DomesticFeatured_MultipleHitsStableOrder(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 23
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "active", false))

	mock.ExpectQuery("SELECT client_model").
		WithArgs(credID).
		WillReturnError(errNoRows)

	mock.ExpectQuery("SELECT p\\.domestic").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"domestic"}).AddRow(true))

	// SQL has ORDER BY COALESCE(pm.standardized_name, pm.raw_model_name)
	// LIMIT 1 — the mock returns what the database would return *after*
	// that ordering (the single sorted winner).
	mock.ExpectQuery("pm\\.standardized_name = ANY").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"probe_model"}).AddRow("claude-3-5-sonnet-20241022"))

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("Model: got %q want %q", got.Model, "claude-3-5-sonnet-20241022")
	}
	if got.Source != "auto:domestic_featured" {
		t.Errorf("Source: got %q want %q", got.Source, "auto:domestic_featured")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickProbeModelForCredential_DomesticFeatured_NoMatch_DegradesToRandom
// verifies the safety net: a domestic credential with no featured bindings
// must still get a default_probe_model assigned (via the original random
// pick over available bindings).
func TestPickProbeModelForCredential_DomesticFeatured_NoMatch_DegradesToRandom(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 31
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "active", false))

	mock.ExpectQuery("SELECT client_model").
		WithArgs(credID).
		WillReturnError(errNoRows)

	mock.ExpectQuery("SELECT p\\.domestic").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"domestic"}).AddRow(true))

	// Priority 2a: featured query — returns nothing (no rows).
	mock.ExpectQuery("pm\\.standardized_name = ANY").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"probe_model"})) // empty

	// Priority 2b: safety-net random pick across all available bindings.
	// Returns a single legacy model — the picker will pick it regardless
	// of len(candidates) mod time.Now().UnixNano() because candidates=1.
	mock.ExpectQuery("FROM credential_model_bindings cmb").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"probe_model"}).AddRow("legacy-model-v1"))

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "legacy-model-v1" {
		t.Errorf("Model: got %q want %q", got.Model, "legacy-model-v1")
	}
	if got.Source != "auto:domestic_random" {
		t.Errorf("Source: got %q want %q (no featured → must fall back to random)",
			got.Source, "auto:domestic_random")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickProbeModelForCredential_NonDomestic_NoFeaturedRun ensures the
// featured query does NOT execute for non-domestic providers — overseas
// providers go through the existing 2-step probe with whatever the
// request_log hit was, or no model at all.
func TestPickProbeModelForCredential_NonDomestic_NoFeaturedRun(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 41
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "active", false))

	mock.ExpectQuery("SELECT client_model").
		WithArgs(credID).
		WillReturnError(errNoRows)

	mock.ExpectQuery("SELECT p\\.domestic").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"domestic"}).AddRow(false))

	// No further queries expected: non-domestic returns empty immediately.

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "" || got.Source != "" {
		t.Errorf("non-domestic should yield empty result, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (featured/random queries must NOT run for non-domestic): %v", err)
	}
}

// TestPickProbeModelForCredential_InactiveLifecycle_NoPick checks that
// suspended / disabled credentials are skipped entirely.
func TestPickProbeModelForCredential_InactiveLifecycle_NoPick(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 51
	mock.ExpectQuery("SELECT COALESCE\\(default_probe_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{
			"default_probe_model", "default_probe_model_source",
			"status", "lifecycle_status", "manual_disabled",
		}).AddRow("", "", "active", "retired", false))

	// No further queries: lifecycle_status='retired' short-circuits.

	got, err := PickProbeModelForCredential(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("PickProbeModelForCredential: %v", err)
	}
	if got.Model != "" || got.Source != "" {
		t.Errorf("retired credential should yield empty result, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// errNoRows is the pgx sentinel returned by QueryRow().Scan when the query
// produced no rows. We use it to drive the picker down the next branch
// without needing a real database.
var errNoRows = pgx.ErrNoRows
