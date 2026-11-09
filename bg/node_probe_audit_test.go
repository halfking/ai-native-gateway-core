package bg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
	"github.com/pashagolub/pgxmock/v4"
)

func TestEmitSyncAuditReturnsInsertFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec("INSERT INTO node_probe_runs").
		WithArgs(
			7, "raw-model",
			true, 200, "", 15, "",
			true, 200, "", 16, "",
			true, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			"raw-model", "outbound-model", 7,
			"https://provider.example/v1/chat/completions", pgxmock.AnyArg(), "request", "response",
			15, false,
			"parent-request",
		).
		WillReturnError(errors.New("audit schema unavailable"))

	worker := &NodeProbeWorker{auditDB: mock}
	err = worker.emitSyncAudit(context.Background(), 7, "raw-model",
		nodeProbeRoundResult{
			ok: true, providerID: 7, outboundModel: "outbound-model", httpStatus: 200,
			latencyMs: 15, requestURL: "https://provider.example/v1/chat/completions",
			requestHeaders: map[string]string{"Authorization": "secret", "X-Test": "allowed"},
			requestBody:    "request", responseBody: "response",
		},
		nodeProbeRoundResult{ok: true, httpStatus: 200, latencyMs: 16},
		time.Now().Add(-time.Second), "parent-request")
	if err == nil || !strings.Contains(err.Error(), "audit schema unavailable") {
		t.Fatalf("emitSyncAudit error = %v, want wrapped insert failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestProbeHeadersJSONReturnsJSONText(t *testing.T) {
	if got := probeHeadersJSON(nil); got != "{}" {
		t.Fatalf("probeHeadersJSON(nil) = %q, want {}", got)
	}
	got := probeHeadersJSON(map[string]string{"X-Test": "allowed"})
	if got != `{"X-Test":"allowed"}` {
		t.Fatalf("probeHeadersJSON() = %q", got)
	}
}

// Exercise the actual legacy caller: endpoint construction fails before any
// headers exist, but the failed round still needs a durable audit row.
func TestRunOneEmptyHeadersAuditPG17(t *testing.T) {
	dsn := os.Getenv("NODE_PROBE_AUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("NODE_PROBE_AUDIT_TEST_DSN unset: legacy runOne JSONB persistence not verified")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if (cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost") || !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("requires a dedicated loopback *_test database")
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback.Host != "127.0.0.1" && fallback.Host != "localhost" {
			t.Fatal("requires loopback-only fallback hosts")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	schema := fmt.Sprintf("probe_audit_%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	ddl := schemaobj.Table(t, "../sql/objects/tables/node_probe_state.sql") +
		schemaobj.Table(t, "../sql/objects/tables/node_probe_runs.sql", "../sql/objects/sequences/node_probe_runs_id_seq.sql")
	if _, err := pool.Exec(ctx, strings.ReplaceAll(ddl, "public.", schema+".")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO node_probe_state(credential_id,raw_model_name) VALUES(82,'audit-model')`); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	t.Cleanup(gateway.Close)
	w := NewNodeProbeWorker(pool, nil, nil, "test-key", gateway.URL, nil, 1000)
	w.resolveDirectTargetFn = func(context.Context, int, string) (string, string, string, string, int, string, error) {
		return "", "", "", "", 0, "", errors.New("configured endpoint is unavailable")
	}
	w.loadStateFn = func(context.Context, int, string) (nodeProbeStateRow, error) {
		return nodeProbeStateRow{}, nil
	}
	if err := w.runOne(ctx, 82, "audit-model", "request_failure", nodeProbeTrigger{tenantID: "default"}); err != nil {
		t.Fatalf("headerless legacy probe failed to persist audit: %v", err)
	}
	var headers, directError string
	if err := pool.QueryRow(ctx, `SELECT request_headers::text, direct_err_code FROM node_probe_runs WHERE credential_id=82`).Scan(&headers, &directError); err != nil {
		t.Fatal(err)
	}
	if headers != "{}" || directError != "endpoint_build" {
		t.Fatalf("persisted headers=%q direct_error=%q, want {}/endpoint_build", headers, directError)
	}
}

// R12 P2: node_probe_runs_attempt_check rejects attempt outside 1..7, and the
// legacy runOne attempt (consecutive_failures+1) grows past 7 once the 7-attempt
// pause was removed. The audit write must clamp instead of failing (23514).
func TestClampAuditAttempt(t *testing.T) {
	cases := []struct{ in, want int }{
		{-3, 1}, {0, 1}, {1, 1}, {4, 4}, {7, 7}, {8, 7}, {12, 7},
	}
	for _, c := range cases {
		if got := clampAuditAttempt(c.in); got != c.want {
			t.Fatalf("clampAuditAttempt(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestInsertNodeProbeRunClampsAttempt(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec("INSERT INTO node_probe_runs").
		WithArgs(
			7, "raw-model", "request_failure", 7, 300,
			false, 0, "", 0, "",
			false, 0, "", 0, "",
			false, pgxmock.AnyArg(), pgxmock.AnyArg(), 5,
			"raw-model", "", 0,
			"", "{}", "", "",
			0, false,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	worker := &NodeProbeWorker{auditDB: mock}
	// attempt=11 would violate the CHECK (BETWEEN 1 AND 7); the insert must
	// receive the clamped value 7.
	if err := worker.insertNodeProbeRun(context.Background(), 7, "raw-model", "request_failure",
		11, 300, nodeProbeRoundResult{}, nodeProbeRoundResult{}, false,
		time.Now().Add(-5*time.Millisecond), time.Now(), 5); err != nil {
		t.Fatalf("insertNodeProbeRun(attempt=11) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// R51 审计 P3：panic 重启退避策略。原实现间隔只增不减（几次 panic 后永久
// 钉在"封顶"值——且封顶判断有误，240s×2=8min 超过宣称的 5min）；现健康
// 运行（≥3 tick）后的 panic 重置到起步值，短命 panic 指数翻倍且真正封顶。
func TestNextProbeLoopBackoff(t *testing.T) {
	cases := []struct {
		name        string
		prevBackoff time.Duration
		previousRun time.Duration
		want        time.Duration
	}{
		{"first short panic doubles", nodeProbeLoopInitialBackoff, 0, 60 * time.Second},
		{"short run keeps doubling", 4 * time.Minute, time.Second, nodeProbeLoopMaxBackoff},
		{"cap is a true 5min", nodeProbeLoopMaxBackoff, time.Second, nodeProbeLoopMaxBackoff},
		{"never exceeds 5min (240s case)", 4 * time.Minute, 0, nodeProbeLoopMaxBackoff},
		{"healthy run resets to initial", nodeProbeLoopMaxBackoff, 3 * nodeProbeTickInterval, nodeProbeLoopInitialBackoff},
		{"healthy run after one panic resets", 60 * time.Second, 91 * time.Second, nodeProbeLoopInitialBackoff},
		{"just under healthy age still doubles", nodeProbeLoopInitialBackoff, 89 * time.Second, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := nextProbeLoopBackoff(tc.prevBackoff, tc.previousRun); got != tc.want {
			t.Errorf("%s: nextProbeLoopBackoff(%v, %v) = %v, want %v",
				tc.name, tc.prevBackoff, tc.previousRun, got, tc.want)
		}
	}
}
