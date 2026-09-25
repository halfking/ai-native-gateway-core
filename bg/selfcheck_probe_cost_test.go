package bg

// P0-1 + P0-3 unit tests — docs/03-design/perf-2026-09-25-probe-cost-optimization.md §8.1:
//   - TestEnsureSystemAPIKey_SetsSystemTier  (P0-1: 新建 key key_tier='system')
//   - TestSystemKeyTierSelfHeal              (P0-1: 存量 default-tier 系统 key 自愈，幂等)
//   - TestSelfcheckRateLimitAbort            (P0-3: 429 → fallback 终止 + 周期间记忆 + 开关 off=现行行为)

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/pashagolub/pgxmock/v4"
)

// ── P0-1 ────────────────────────────────────────────────────────────────────

// TestEnsureSystemAPIKey_SetsSystemTier pins the P0-1 fix: a freshly generated
// self-check system key must land in the 'system' tier (300 RPM). The column
// default 'default' (12 RPM) was the storm source — 86% of self-check pings
// bounced off the gateway's own rate limiter (probe-cost-optimization §2.3).
func TestEnsureSystemAPIKey_SetsSystemTier(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// No existing key for this worker → generate a new one.
	mock.ExpectQuery("SELECT key_ciphertext").
		WillReturnError(pgx.ErrNoRows)
	// The INSERT must carry key_tier='system' explicitly — a bare INSERT that
	// relies on the column default is the exact bug this test guards against,
	// so the match regex rejects any INSERT without it.
	mock.ExpectExec("INSERT INTO api_keys[\\s\\S]*key_tier[\\s\\S]*'system'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	encKey := make([]byte, 32)
	for i := range encKey {
		encKey[i] = byte(i)
	}
	raw, err := EnsureSystemAPIKey(context.Background(), mock, encKey, nil, "test-secret")
	if err != nil {
		t.Fatalf("EnsureSystemAPIKey: %v", err)
	}
	if !strings.HasPrefix(raw, "sk-selfcheck-") {
		t.Errorf("generated key %q does not carry the sk-selfcheck- prefix", raw)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestSystemKeyTierSelfHeal drives HealSelfCheckSystemKeyTier over pgxmock:
// legacy default-tier system keys get promoted to 'system', and a second pass
// affects 0 rows (the WHERE clause makes the heal idempotent).
func TestSystemKeyTierSelfHeal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// The UPDATE must stay scoped to the self-check workers' system keys in
	// the default tier — pin all three filters in the match regex.
	const healSQL = "UPDATE api_keys[\\s\\S]*COALESCE\\(is_system, FALSE\\) = TRUE[\\s\\S]*" +
		"owner_user IN \\('self-check-worker', 'credential-selfcheck-worker'\\)[\\s\\S]*" +
		"COALESCE\\(key_tier, 'default'\\) = 'default'"

	mock.ExpectExec(healSQL).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	n, err := HealSelfCheckSystemKeyTier(context.Background(), mock)
	if err != nil {
		t.Fatalf("HealSelfCheckSystemKeyTier (first pass): %v", err)
	}
	if n != 3 {
		t.Errorf("first pass healed %d keys, want 3", n)
	}

	// Idempotency: once healed, the second pass is a no-op.
	mock.ExpectExec(healSQL).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	n, err = HealSelfCheckSystemKeyTier(context.Background(), mock)
	if err != nil {
		t.Fatalf("HealSelfCheckSystemKeyTier (second pass): %v", err)
	}
	if n != 0 {
		t.Errorf("second pass healed %d keys, want 0 (idempotent)", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ── P0-3 ────────────────────────────────────────────────────────────────────

// probeCostSettingsBackend is a minimal in-memory settings backend (same
// pattern as domains/hooks/observability/telemetry's fakeSettingsBackend) so
// the abort switch can be pinned per test.
type probeCostSettingsBackend struct {
	store map[string][]byte
}

func (f *probeCostSettingsBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *probeCostSettingsBackend) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}
func (f *probeCostSettingsBackend) GetTenant(tenantID, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *probeCostSettingsBackend) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}

// withProbeCostSettings swaps settings.Global for a registry carrying the
// probe specs with probe.selfcheck.ratelimit_abort pinned to the given value.
func withProbeCostSettings(t *testing.T, abortEnabled bool) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &probeCostSettingsBackend{store: map[string][]byte{
		"probe.selfcheck.ratelimit_abort": []byte(boolToString(abortEnabled)),
	}})
	for _, spec := range settings.ProbeSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// newRateLimitedSelfcheckStub spins up a gateway stub that answers every
// /chat/completions with 429 rate_limit_error (the gw_rpm_exceeded shape the
// storm produced) and counts the requests it sees.
func newRateLimitedSelfcheckStub(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit exceeded"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// expectPickModelsThree wires the pickModels sequence so the credential has a
// featured primary (model-a) plus two due failed bindings (model-b, model-c).
func expectPickModelsThree(mock pgxmock.PgxPoolIface, credID int) {
	mock.ExpectQuery("SELECT c.tenant_id").WithArgs(credID).
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id"}).AddRow("default"))
	mock.ExpectQuery("COALESCE\\(cmb.available, FALSE\\) = \\$2").WithArgs(credID, true).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name", "standardized_name"}).
			AddRow("model-a", "model-a"))
	mock.ExpectQuery("COALESCE\\(cmb.available, FALSE\\) = \\$2").WithArgs(credID, false).
		WillReturnRows(pgxmock.NewRows([]string{"raw_model_name", "standardized_name"}).
			AddRow("model-b", "model-b").AddRow("model-c", "model-c"))
	mock.ExpectQuery("FROM routing_policy").WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{"featured_models"}).AddRow([]string{"model-a"}))
	// No request_logs_hot expectation: with no Redis ranking (w.redis nil)
	// recentInUsageWindow short-circuits on the empty recent set.
}

func expectRunBookkeeping(mock pgxmock.PgxPoolIface, credID int, wantStatus string) {
	mock.ExpectQuery("INSERT INTO self_check_runs").
		WithArgs("cred-7", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(42)))
	mock.ExpectExec("UPDATE self_check_runs SET[\\s\\S]*attempted_models = \\$13::text::jsonb").
		WithArgs(
			int64(42),
			pgxmock.AnyArg(), // completed_at
			pgxmock.AnyArg(), // duration_ms
			wantStatus,
			pgxmock.AnyArg(), // rounds_total
			pgxmock.AnyArg(), // rounds_success
			pgxmock.AnyArg(), // had_tool_call
			pgxmock.AnyArg(), // total_tokens
			pgxmock.AnyArg(), // avg_latency_ms
			pgxmock.AnyArg(), // error_type
			pgxmock.AnyArg(), // error_detail
			pgxmock.AnyArg(), // selection_strategy
			pgxmock.AnyArg(), // attempted_models
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("INSERT INTO system_probe_runs").
		WithArgs(
			int64(42), "chat_tool", "automatic", credID, "model-a",
			"legacy_selfcheck", "credential-selfcheck-worker", wantStatus,
			pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

// TestSelfcheckRateLimitAbort covers P0-3 §8.1:
//  1. primary 429 → the fallback loop terminates and the remaining candidates
//     are never requested (server sees exactly one request);
//  2. the inter-cycle memory kicks in — the cycle after an abort probes only
//     the primary model, and clears once a cycle finishes without a 429;
//  3. probe.selfcheck.ratelimit_abort=false restores the legacy walk-everything
//     behavior (0 = 现行行为).
func TestSelfcheckRateLimitAbort(t *testing.T) {
	const credID = 7

	t.Run("primary_429_aborts_fallback", func(t *testing.T) {
		srv, hits := newRateLimitedSelfcheckStub(t)
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()
		expectPickModelsThree(mock, credID)
		expectRunBookkeeping(mock, credID, "failed")

		w := &CredentialSelfcheckWorker{db: mock, baseURL: srv.URL, client: srv.Client()}
		if err := w.runOne(context.Background(), credID); err != nil {
			t.Fatalf("runOne: %v", err)
		}
		if got := atomic.LoadInt64(hits); got != 1 {
			t.Errorf("gateway saw %d requests, want 1 (model-b/model-c must not be requested after the 429)", got)
		}
		if !w.lastCycleRateLimited {
			t.Error("lastCycleRateLimited = false, want true (cycle must be remembered as aborted)")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("cycle_memory_primary_only_then_clears", func(t *testing.T) {
		var hits int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&hits, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"t1"}]}}],"usage":{"total_tokens":7}}`))
		}))
		defer srv.Close()

		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()
		expectPickModelsThree(mock, credID)
		expectRunBookkeeping(mock, credID, "success")

		w := &CredentialSelfcheckWorker{db: mock, baseURL: srv.URL, client: srv.Client()}
		w.lastCycleRateLimited = true // previous cycle aborted
		if err := w.runOne(context.Background(), credID); err != nil {
			t.Fatalf("runOne: %v", err)
		}
		// Primary only: ping + tool round = 2 requests; the two fallback
		// candidates (4 more rounds) must never fire.
		if got := atomic.LoadInt64(&hits); got != 2 {
			t.Errorf("gateway saw %d requests, want 2 (primary-only cycle memory)", got)
		}
		if w.lastCycleRateLimited {
			t.Error("lastCycleRateLimited still true after a clean cycle — memory must clear on success")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("switch_off_restores_legacy_fallback", func(t *testing.T) {
		withProbeCostSettings(t, false)

		srv, hits := newRateLimitedSelfcheckStub(t)
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		defer mock.Close()
		expectPickModelsThree(mock, credID)
		expectRunBookkeeping(mock, credID, "failed")

		w := &CredentialSelfcheckWorker{db: mock, baseURL: srv.URL, client: srv.Client()}
		if err := w.runOne(context.Background(), credID); err != nil {
			t.Fatalf("runOne: %v", err)
		}
		// Legacy behavior: every candidate is tried (1 ping each), no abort,
		// no inter-cycle memory.
		if got := atomic.LoadInt64(hits); got != 3 {
			t.Errorf("gateway saw %d requests, want 3 (switch off = walk all candidates)", got)
		}
		if w.lastCycleRateLimited {
			t.Error("lastCycleRateLimited must stay false when the abort switch is off")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

// TestSelfcheckRound429Classification pins the detection contract: the stub's
// 429 rate_limit_error body must be recognized by isSelfcheckRateLimit, while
// unrelated failures (401 auth, 500) must not trigger the abort.
func TestSelfcheckRound429Classification(t *testing.T) {
	body := `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit exceeded"}}`
	if !isSelfcheckRateLimit(http.StatusTooManyRequests, "other_kind", body) {
		t.Error("429 status must be classified as rate-limit regardless of kind")
	}
	if !isSelfcheckRateLimit(http.StatusForbidden, "other_kind", `upstream detail mentions gw_rpm_exceeded`) {
		t.Error("gateway throttle markers in detail must be recognized on non-429 status")
	}
	if isSelfcheckRateLimit(http.StatusUnauthorized, "upstream_credential_invalid", `{"error":{"message":"bad key"}}`) {
		t.Error("401 auth failure must not be classified as rate-limit")
	}
	if isSelfcheckRateLimit(http.StatusInternalServerError, "upstream_down", "boom") {
		t.Error("500 must not be classified as rate-limit")
	}
}
