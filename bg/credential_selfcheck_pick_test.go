package bg

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// newSelfcheckMock builds a CredentialSelfcheckWorker backed by a pgxmock
// pool so pickModels can be exercised without a real database.
func newSelfcheckMock(t *testing.T) (*CredentialSelfcheckWorker, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	w := &CredentialSelfcheckWorker{
		db: mock,
		// rng must be non-nil — pickModels shuffles the fallback pool.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	return w, mock
}

// scErrNoRows mirrors the pgx sentinel used in shared_pick_test.go so the
// "most_used" QueryRow falls through cleanly when there is no 24h traffic.
var scErrNoRows = pgx.ErrNoRows

// TestPickModels_FeaturedPreferredOverMostUsed is the core 2026-07-15
// directive: a credential that serves featured models MUST probe on a
// featured model, even when it also has a 24h most-used (non-featured)
// model.  Featured is tier 1; most_used is only tier 2.
func TestPickModels_FeaturedPreferredOverMostUsed(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 101
	// Tier 1: featured query returns one featured model.
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("gpt-4o"))
	// Tier 2: most_used returns a non-featured model.
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("obscure-internal-model"))
	// Tier 3: routable pool returns both.
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("gpt-4o").
			AddRow("obscure-internal-model"))

	got, err := w.pickModels(context.Background(), credID)
	if err != nil {
		t.Fatalf("pickModels: %v", err)
	}
	if got.strategy != "featured" {
		t.Errorf("strategy: got %q want %q (featured must win over most_used)",
			got.strategy, "featured")
	}
	if len(got.models) == 0 || got.models[0] != "gpt-4o" {
		t.Errorf("models[0]: got %v want gpt-4o (featured must be position 0)", got.models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickModels_MultipleFeaturedFillFallbackSlots verifies that when a
// credential serves several featured models, they occupy the leading
// slots (0..N) before any non-featured model is considered.
func TestPickModels_MultipleFeaturedFillFallbackSlots(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 102
	// Tier 1: two featured models.
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("claude-3-5-sonnet-20241022").
			AddRow("gpt-4o"))
	// Tier 2: no most_used traffic.
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnError(scErrNoRows)
	// Tier 3: routable pool includes the two featured + one obscure.
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("claude-3-5-sonnet-20241022").
			AddRow("gpt-4o").
			AddRow("legacy-sandbox-v1"))

	got, err := w.pickModels(context.Background(), credID)
	if err != nil {
		t.Fatalf("pickModels: %v", err)
	}
	if got.strategy != "featured" {
		t.Errorf("strategy: got %q want %q", got.strategy, "featured")
	}
	if len(got.models) != 3 {
		t.Fatalf("models: got %d want 3 (capped at maxFallbackAttempts)", len(got.models))
	}
	// First two must be the featured models (in featured query order).
	if got.models[0] != "claude-3-5-sonnet-20241022" || got.models[1] != "gpt-4o" {
		t.Errorf("featured slots: got %v want [claude-3-5-sonnet-20241022, gpt-4o, ...]",
			got.models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickModels_NoFeatured_FallsToMostUsed verifies tier 2: when no
// featured model is bound, the 24h most-used model becomes position 0.
func TestPickModels_NoFeatured_FallsToMostUsed(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 103
	// Tier 1: no featured rows.
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"})) // empty
	// Tier 2: most_used returns a model.
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("deepseek-chat"))
	// Tier 3: routable pool.
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("deepseek-chat").
			AddRow("other-model"))

	got, err := w.pickModels(context.Background(), credID)
	if err != nil {
		t.Fatalf("pickModels: %v", err)
	}
	if got.strategy != "most_used" {
		t.Errorf("strategy: got %q want %q", got.strategy, "most_used")
	}
	if len(got.models) == 0 || got.models[0] != "deepseek-chat" {
		t.Errorf("models[0]: got %v want deepseek-chat", got.models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickModels_NoFeaturedNoTraffic_FallsToRandom verifies tier 3: a
// never-used credential with no featured bindings picks randomly from
// the routable pool.  strategy must be "random".
func TestPickModels_NoFeaturedNoTraffic_FallsToRandom(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 104
	// Tier 1: no featured rows.
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"})) // empty
	// Tier 2: no most_used traffic.
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnError(scErrNoRows)
	// Tier 3: routable pool with two models.
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("model-a").
			AddRow("model-b"))

	got, err := w.pickModels(context.Background(), credID)
	if err != nil {
		t.Fatalf("pickModels: %v", err)
	}
	if got.strategy != "random" {
		t.Errorf("strategy: got %q want %q", got.strategy, "random")
	}
	if len(got.models) != 2 {
		t.Fatalf("models: got %d want 2 (both pool entries, no dedup needed)", len(got.models))
	}
	// Both candidates must come from the pool.
	seen := map[string]bool{}
	for _, m := range got.models {
		seen[m] = true
	}
	if !seen["model-a"] || !seen["model-b"] {
		t.Errorf("models: got %v, expected both model-a and model-b", got.models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickModels_NoRoutableModels_ReturnsEmpty ensures the worker does
// not crash when a credential has zero routable bindings.
func TestPickModels_NoRoutableModels_ReturnsEmpty(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 105
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"})) // empty
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnError(scErrNoRows)
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"})) // empty

	got, err := w.pickModels(context.Background(), credID)
	if err != nil {
		t.Fatalf("pickModels: %v", err)
	}
	if len(got.models) != 0 {
		t.Errorf("models: got %v want empty (no routable bindings)", got.models)
	}
	if got.strategy != "" {
		t.Errorf("strategy: got %q want empty (nothing selected)", got.strategy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestPickModels_DedupFeaturedAndMostUsed verifies that when the same
// model appears in both the featured list and the most_used result, it
// is not duplicated in the output.
func TestPickModels_DedupFeaturedAndMostUsed(t *testing.T) {
	w, mock := newSelfcheckMock(t)
	defer mock.Close()

	const credID = 106
	// Tier 1: featured returns gpt-4o.
	mock.ExpectQuery("pol.tenant_id = 'default'").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("gpt-4o"))
	// Tier 2: most_used returns the SAME model (gpt-4o is both featured and hot).
	mock.ExpectQuery("credential_most_used_model").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("gpt-4o"))
	// Tier 3: pool has gpt-4o + claude-3-5-sonnet-20241022.
	mock.ExpectQuery("c.id = \\$1").
		WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name"}).
			AddRow("gpt-4o").
			AddRow("claude-3-5-sonnet-20241022"))

	got, err := w.pickModels(context.Background(), credID)
	if err != nil {
		t.Fatalf("pickModels: %v", err)
	}
	if got.strategy != "featured" {
		t.Errorf("strategy: got %q want %q", got.strategy, "featured")
	}
	// gpt-4o must appear exactly once.
	count := 0
	for _, m := range got.models {
		if m == "gpt-4o" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("gpt-4o appears %d times, want 1 (must be deduplicated)", count)
	}
	if len(got.models) != 2 {
		t.Errorf("models: got %v want 2 unique models", got.models)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
