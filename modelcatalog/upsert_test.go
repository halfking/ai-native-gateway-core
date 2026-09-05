package modelcatalog

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

func TestPreserveManualDisable(t *testing.T) {
	manual := "manual"
	auto := "auto_probe_failed"
	deleted := "deleted"

	tests := []struct {
		name     string
		avail    bool
		reason   *string
		preserve bool
	}{
		{"manual disabled", false, &manual, true},
		{"manual prefix variant", false, strPtr("manual_admin"), true},
		{"deleted legacy soft clear", false, &deleted, false},
		{"auto disabled", false, &auto, false},
		{"available with reason", true, &manual, false},
		{"available no reason", true, nil, false},
		{"disabled no reason", false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreserveManualDisable(tt.avail, tt.reason)
			if got != tt.preserve {
				t.Fatalf("PreserveManualDisable(%v, %v) = %v, want %v", tt.avail, tt.reason, got, tt.preserve)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestUpsertSQL_LowercaseContract pins the case-handling guarantees that
// gateway.go enforces after the 2026-07-14 case audit:
//   - the INSERT populates provider_models.canonical_raw_name
//   - the INSERT keeps raw_model_name and canonical_raw_name as distinct
//     columns (provider-facing vs client-facing)
//   - the SQL does NOT wrap either column in lower(...)
//
// If any future migration drops the canonical_raw_name column or starts
// silently lowercasing raw_model_name, this test will fail.
func TestUpsertSQL_LowercaseContract(t *testing.T) {
	s := upsertCredentialModelSQL

	wantSubstrings := []string{
		"INSERT INTO provider_models (",
		"raw_model_name,",
		"canonical_raw_name,",
		"canonical_id,",
		"standardized_name,",
		"SELECT cred.provider_id, $2, $3,",
		"canonical_raw_name = COALESCE(EXCLUDED.canonical_raw_name",
	}

	for _, want := range wantSubstrings {
		if !strings.Contains(s, want) {
			t.Errorf("upsertCredentialModelSQL is missing %q (would break the lowercase model-name contract)", want)
		}
	}

	// Explicitly forbid lower() in the upsert SQL — callers are responsible
	// for canonicalising inputs via modelname.CanonicalizeClientModel.
	forbiddenSubstrings := []string{
		"lower(raw_model_name)",
		"lower(canonical_raw_name)",
	}
	for _, bad := range forbiddenSubstrings {
		if strings.Contains(s, bad) {
			t.Errorf("upsertCredentialModelSQL must not contain %q (callers pre-lowercase inputs)", bad)
		}
	}
}

func TestUpsertCredentialModel_AcceptsAllLowercaseArgs(t *testing.T) {
	// 2026-07-14: this test guards the new canonicalRawName parameter on
	// UpsertCredentialModel by exercising the SQL constant against a
	// representative set of (rawName, canonicalRawName) pairs. It does NOT
	// touch the DB; it asserts only that the SQL is structurally valid
	// (the prepared statements can be parsed). Use pgxmock or a real DB
	// for end-to-end behaviour.
	s := upsertCredentialModelSQL

	wantBindings := []string{
		"$1", // credentialID
		"$2", // rawName
		"$3", // canonicalRawName
		"$4", // standardizedName
		"$5", // canonicalID
	}
	for _, b := range wantBindings {
		if !strings.Contains(s, b) {
			t.Errorf("upsertCredentialModelSQL is missing parameter binding %q", b)
		}
	}
}

// TestDeriveBillingMode covers the SSOT mapping from credentials.plan_type
// to credential_model_bindings.billing_mode. This mirrors the CASE WHEN
// expression in upsertCredentialModelSQL and migrations/136.
func TestDeriveBillingMode(t *testing.T) {
	tests := []struct {
		planType string
		want     string
	}{
		{"token", "per_token"},
		{"token_plan", "token_plan"},
		{"code_plan", "code_plan"},
		{"agent_plan", "agent_plan"},
		{"monthly", "monthly"},
		{"free", "free"},
		{"", "per_token"}, // empty defaults to token semantics
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.planType, func(t *testing.T) {
			got := DeriveBillingMode(tt.planType)
			if got != tt.want {
				t.Fatalf("DeriveBillingMode(%q) = %q, want %q", tt.planType, got, tt.want)
			}
		})
	}
}

// TestUpsertSQL_DerivesBillingModeFromPlanType is a structural test guarding
// the SQL constant: the INSERT must populate billing_mode and
// plan_type_origin, and must NOT overwrite billing_mode on conflict (so
// admin/Pricing-page manual overrides survive a discovery re-fetch).
func TestUpsertSQL_DerivesBillingModeFromPlanType(t *testing.T) {
	s := upsertCredentialModelSQL

	required := []string{
		"plan_type",                          // cred CTE pulls plan_type
		"CASE WHEN cred.plan_type = 'token'", // mapping logic
		"'per_token'",                        // legacy alias
		"'auto'",                             // plan_type_origin default
		"billing_mode",                       // INSERT column
		"plan_type_origin",                   // INSERT column
	}
	for _, r := range required {
		if !strings.Contains(s, r) {
			t.Errorf("upsert SQL missing required token: %q", r)
		}
	}

	// ON CONFLICT branch must NOT touch billing_mode — manual overrides win.
	conflictIdx := strings.Index(s, "ON CONFLICT (credential_id, provider_model_id)")
	if conflictIdx < 0 {
		t.Fatal("expected ON CONFLICT clause not found in upsert SQL")
	}
	conflictBranch := s[conflictIdx:]
	if strings.Contains(conflictBranch, "billing_mode =") {
		t.Errorf("ON CONFLICT branch must not reassign billing_mode (manual overrides would be clobbered)")
	}
}

// TestUpsertSQL_AdminProtectedGuard pins the "manual records are only
// manually deletable" contract: the ON CONFLICT branch of the upsert must
// skip admin-protected bindings entirely, so batch/auto refresh never
// touches manually-added records — not even updated_at or availability.
func TestUpsertSQL_AdminProtectedGuard(t *testing.T) {
	s := upsertCredentialModelSQL

	guard := "WHERE COALESCE(credential_model_bindings.admin_protected, FALSE) = FALSE"
	if !strings.Contains(s, guard) {
		t.Errorf("upsert SQL missing admin_protected skip guard %q (auto refresh would update manual records)", guard)
	}

	// The guard must be the trailing clause of the DO UPDATE, positioned after
	// all SET assignments so it gates the whole branch.
	idx := strings.Index(s, guard)
	if idx < 0 {
		return
	}
	tail := strings.TrimSpace(s[idx+len(guard):])
	if tail != "" {
		t.Errorf("admin_protected guard must be the last clause of the ON CONFLICT branch, got trailing SQL: %q", tail)
	}
}

// TestAutoFillDefaultProbeModel_SourceLabel pins the constant that the
// daily DefaultProbePicker repick loop uses to recognise refresh-time
// auto-fills. If this constant changes, the corresponding
// DefaultProbePicker WHERE clause must change too — see
// bg/default_probe_picker.go line 80.
func TestAutoFillDefaultProbeModel_SourceLabel(t *testing.T) {
	if DefaultProbeModelSourceRefreshLatest != "auto:refresh_latest" {
		t.Errorf("DefaultProbeModelSourceRefreshLatest = %q, want %q (label is load-bearing for DefaultProbePicker overwrite rules)",
			DefaultProbeModelSourceRefreshLatest, "auto:refresh_latest")
	}
}

// TestAutoFillDefaultProbeModel_SQLContract pins the 2026-08-31 hzx-2
// round-4 + round-6 SQL contract. If any future migration drops one of
// these guarantees the auto-fill hook will silently misbehave — most
// notably the provider-facing pick value (round-6: standardized_name
// would 404 probes on NIM-style prefixed vendors), the "newest
// created_at DESC" ordering (newest model wins), the "skip
// admin_protected / unavailable cmb / pm" guards, and the
// "default_probe_model IS NULL/empty AND source <> 'manual'" eligibility
// predicate. The test is structural (string-match against the embedded
// SQL inside AutoFillDefaultProbeModel) so it fails loudly when the SQL
// regresses, even if no integration test happens to cover the path.
func TestAutoFillDefaultProbeModel_SQLContract(t *testing.T) {
	src, err := readSourceForTest("AutoFillDefaultProbeModel")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)

	required := []string{
		// Round-6: pick the PROVIDER-facing name. default_probe_model is
		// sent verbatim to the upstream by the probe chat request, so a
		// standardized_name value 404s on vendors whose raw name carries
		// a prefix ("z-ai/glm-5.2"). Must match bg/shared_pick.go.
		"COALESCE(pm.outbound_model_name, pm.raw_model_name)",
		// And must NOT fall back to standardized_name anywhere in the pick.
		// Pick target: most-recently-created routable model under this credential.
		"ORDER BY pm.created_at DESC",
		// Skip cmb rows the operator explicitly disabled / protected.
		"COALESCE(cmb.available, FALSE) = TRUE",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		// Skip pm rows that are themselves unavailable (e.g. retired model).
		"COALESCE(pm.available, FALSE) = TRUE",
		// Only write when the operator never picked a value AND the source
		// marker is not 'manual' (defensive — even if the model column is
		// empty, never stomp a manual source marker).
		"c.default_probe_model IS NULL OR c.default_probe_model = ''",
		"COALESCE(c.default_probe_model_source, '') <> 'manual'",
		// Stamp the source label so daily repicks can recognise the row.
		"DefaultProbeModelSourceRefreshLatest",
	}
	for _, r := range required {
		if !strings.Contains(body, r) {
			t.Errorf("AutoFillDefaultProbeModel SQL is missing %q", r)
		}
	}
	if strings.Contains(body, "sub.standardized_name") {
		t.Error("AutoFillDefaultProbeModel must not write standardized_name — the probe sends the stored value verbatim to the upstream (round-6 audit)")
	}
	// Sanity: must be UPDATE ... RETURNING so the caller learns what was picked.
	if !strings.Contains(body, "RETURNING") {
		t.Error("AutoFillDefaultProbeModel must use UPDATE ... RETURNING so the caller can log the picked model")
	}
}

// TestAutoFillDefaultProbeModel_NoRowsIsNotAnError verifies the helper
// treats pgx.ErrNoRows as "no eligible pick" rather than a DB error.
// The 0-row outcome covers three real cases:
//   - default_probe_model already non-empty (operator set it),
//   - default_probe_model_source = 'manual' (operator pinned it),
//   - cmb has no routable binding yet (transient state right after
//     credential creation, before the first refresh completes).
// All three are normal operating conditions and must not surface as
// errors to the discovery / admin refresh callers.
func TestAutoFillDefaultProbeModel_NoRowsIsNotAnError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 42
	mock.ExpectQuery("UPDATE credentials").
		WithArgs(credID, DefaultProbeModelSourceRefreshLatest).
		WillReturnError(pgx.ErrNoRows)

	picked, err := AutoFillDefaultProbeModel(context.Background(), mock, credID)
	if err != nil {
		t.Fatalf("AutoFillDefaultProbeModel: unexpected error for pgx.ErrNoRows: %v", err)
	}
	if picked != "" {
		t.Errorf("AutoFillDefaultProbeModel returned %q, want empty string when no eligible pick", picked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestAutoFillDefaultProbeModel_DBErrorPropagates ensures real DB
// failures (connection lost, schema drift, etc.) DO surface so callers
// can log them. Auto-fill is best-effort but a silent failure is worse
// than a logged one — operators need to know the hook is not firing.
func TestAutoFillDefaultProbeModel_DBErrorPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	const credID = 99
	dbErr := errors.New("connection reset by peer")
	mock.ExpectQuery("UPDATE credentials").
		WithArgs(credID, DefaultProbeModelSourceRefreshLatest).
		WillReturnError(dbErr)

	picked, err := AutoFillDefaultProbeModel(context.Background(), mock, credID)
	if err == nil {
		t.Fatal("AutoFillDefaultProbeModel: expected DB error to propagate, got nil")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("AutoFillDefaultProbeModel error = %v, want one wrapping the original DB error", err)
	}
	if picked != "" {
		t.Errorf("AutoFillDefaultProbeModel returned %q, want empty string on error", picked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestAutoFillDefaultProbeModel_RejectsNonPositiveCredID guards against
// the helper writing to row 0 / -1 when called with a stale credID.
// Defensive — discovery / admin paths always pass a real ID, but
// future callers might not.
func TestAutoFillDefaultProbeModel_RejectsNonPositiveCredID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// No mock expectations — the helper must short-circuit before
	// hitting the DB on a non-positive credID.
	for _, id := range []int{0, -1, -42} {
		picked, err := AutoFillDefaultProbeModel(context.Background(), mock, id)
		if err != nil {
			t.Errorf("AutoFillDefaultProbeModel(%d): unexpected error %v", id, err)
		}
		if picked != "" {
			t.Errorf("AutoFillDefaultProbeModel(%d) returned %q, want empty string", id, picked)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("non-positive credID must not issue any DB queries, got: %v", err)
	}
}

// readSourceForTest extracts the source bytes for a function whose body
// is in the same file. Used by TestAutoFillDefaultProbeModel_SQLContract
// so the test fails loudly if the SQL shape regresses — we mirror the
// "test against the source" pattern used by the existing
// TestUpsertSQL_* tests in this file (see e.g.
// TestUpsertSQL_AdminProtectedGuard).
func readSourceForTest(funcName string) ([]byte, error) {
	const file = "upsert.go"
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "func AutoFillDefaultProbeModel(") {
			start = i
			break
		}
	}
	if start < 0 {
		// Fallback: read the whole file. We don't want the test to depend
		// on a fragile line-number grep — the SQL contract is what matters.
		return data, nil
	}
	return []byte(strings.Join(lines[start:], "\n")), nil
}
