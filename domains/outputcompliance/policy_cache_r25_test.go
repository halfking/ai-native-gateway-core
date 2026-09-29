package outputcompliance

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// R25-E regression pin: with a request-scoped policy cache in the context,
// getPolicy must load the tenant policy exactly once per request no matter
// how many visible text fields the compliance chain checks. Before R25-E
// WithRequestPolicyCache injected a cache value that nothing ever read, so
// every field (and every SSE frame) re-queried the policy table.
func TestGetPolicyReusesRequestScopedCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	checker := &Checker{db: db}

	rows := sqlmock.NewRows([]string{
		"tenant_id", "enabled", "enforcement_mode", "check_pii", "check_toxicity", "check_bias",
		"check_hallucination", "pii_threshold", "toxicity_threshold", "bias_threshold", "hallucination_threshold",
		"action_on_pii", "action_on_toxicity", "action_on_bias", "action_on_hallucination",
		"auto_redact", "redact_email", "redact_phone", "redact_id_card", "redact_credit_card",
		"strict_mode", "log_all_outputs",
	}).AddRow("tenant-1", true, "enforce", true, false, false, false,
		0.8, 0.8, 0.8, 0.8,
		"redact", "log", "log", "log",
		true, true, true, true, true,
		false, false)
	mock.ExpectQuery(`SELECT tenant_id, enabled, enforcement_mode`).
		WithArgs("tenant-1").
		WillReturnRows(rows)

	ctx := WithRequestPolicyCache(context.Background())
	first, err := checker.getPolicy(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("first getPolicy: %v", err)
	}
	second, err := checker.getPolicy(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("second getPolicy: %v", err)
	}
	if first != second {
		t.Fatal("cached policy must be the same instance across fields")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("policy query ran more than once per request: %v", err)
	}
}

// Without the cache in the context the legacy behavior (one query per call)
// must be preserved — the cache is opt-in per request lifecycle.
func TestGetPolicyWithoutCacheQueriesEachCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	checker := &Checker{db: db}

	row := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{
			"tenant_id", "enabled", "enforcement_mode", "check_pii", "check_toxicity", "check_bias",
			"check_hallucination", "pii_threshold", "toxicity_threshold", "bias_threshold", "hallucination_threshold",
			"action_on_pii", "action_on_toxicity", "action_on_bias", "action_on_hallucination",
			"auto_redact", "redact_email", "redact_phone", "redact_id_card", "redact_credit_card",
			"strict_mode", "log_all_outputs",
		}).AddRow("tenant-1", true, "enforce", true, false, false, false,
			0.8, 0.8, 0.8, 0.8,
			"redact", "log", "log", "log",
			true, true, true, true, true,
			false, false)
	}
	mock.ExpectQuery(`SELECT tenant_id, enabled, enforcement_mode`).WithArgs("tenant-1").WillReturnRows(row())
	mock.ExpectQuery(`SELECT tenant_id, enabled, enforcement_mode`).WithArgs("tenant-1").WillReturnRows(row())

	ctx := context.Background()
	if _, err := checker.getPolicy(ctx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.getPolicy(ctx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A transient DB failure must not be pinned for the rest of the request:
// only error-free loads enter the cache, so the next field retries.
func TestGetPolicyDoesNotCacheFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	checker := &Checker{db: db}

	rows := sqlmock.NewRows([]string{
		"tenant_id", "enabled", "enforcement_mode", "check_pii", "check_toxicity", "check_bias",
		"check_hallucination", "pii_threshold", "toxicity_threshold", "bias_threshold", "hallucination_threshold",
		"action_on_pii", "action_on_toxicity", "action_on_bias", "action_on_hallucination",
		"auto_redact", "redact_email", "redact_phone", "redact_id_card", "redact_credit_card",
		"strict_mode", "log_all_outputs",
	}).AddRow("tenant-1", true, "enforce", true, false, false, false,
		0.8, 0.8, 0.8, 0.8,
		"redact", "log", "log", "log",
		true, true, true, true, true,
		false, false)
	mock.ExpectQuery(`SELECT tenant_id, enabled, enforcement_mode`).
		WithArgs("tenant-1").
		WillReturnError(errTransient)
	mock.ExpectQuery(`SELECT tenant_id, enabled, enforcement_mode`).
		WithArgs("tenant-1").
		WillReturnRows(rows)

	ctx := WithRequestPolicyCache(context.Background())
	if _, err := checker.getPolicy(ctx, "tenant-1"); err == nil {
		t.Fatal("first call must surface the transient error")
	}
	if _, err := checker.getPolicy(ctx, "tenant-1"); err != nil {
		t.Fatalf("second call must retry after failure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

var errTransient = &transientError{}

type transientError struct{}

func (*transientError) Error() string { return "transient db failure" }
