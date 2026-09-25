// Package bg — probe_cost_p02_test.go — P0-2 契约测试（短梯长尾化 +
// request_failure pair 级频控 + 队列任务代际 attempt 重置），方案：
// docs/03-design/perf-2026-09-25-probe-cost-optimization.md §5 P0-2 与
// r0925 handoff 遗留 1/2（docs/handoff/20260925-probe-healthy-zero-probe-audit.md）。
//
// 三件事共用这一批测试：
//  1. TestNetworkChain_LongTailSink —— 网络类 attempt 1..4 仍走
//     5s/15s/30s/60s 短梯；attempt 5+ 沿 5m→1h→2h→6h 长尾沉底；
//     probe.network_chain_long_tail=false 恢复 60s 永续旧行为。
//  2. TestRequestFailure_MinGapSkip —— 频控门接线（跳过时不入队）；
//     gap=0 完全关闭；periodic 泵路径不受频控影响。
//  3. TestProbeServiceAttemptPersistsAcrossGenerations —— Run 的 attempt
//     取 max(task.Attempt, state.consecutive_failures+1)，梯位跨任务代
//     保持；state 不可读时 fail-open 回落任务自身计数。
//
// 谓词 SQL 的语义（排程中/健康停放/刚探过三臂）由
// TestRequestFailureThrottlePredicateSQL 对真实 PG（TEST_PG_URL，一次性库
// 铁律）钉桩；未设置 TEST_PG_URL 时跳过。
package bg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// withP02Settings swaps settings.Global for a registry carrying the probe
// specs with the two P0-2 knobs pinned (same pattern as withProbeCostSettings
// in selfcheck_probe_cost_test.go).
func withP02Settings(t *testing.T, longTail bool, minGapSeconds int) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &probeCostSettingsBackend{store: map[string][]byte{
		"probe.network_chain_long_tail":         []byte(boolToString(longTail)),
		"probe.request_failure_min_gap_seconds": []byte(strconv.Itoa(minGapSeconds)),
	}})
	for _, spec := range settings.ProbeSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// TestNetworkChain_LongTailSink pins the P0-2 long-tail ladder for every
// error class that rides the network/transient short chain, through both
// entry points (kind + err-code mapping).
func TestNetworkChain_LongTailSink(t *testing.T) {
	withP02Settings(t, true, 60)

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 5 * time.Second},
		{2, 15 * time.Second},
		{3, 30 * time.Second},
		{4, 60 * time.Second},
		{5, 5 * time.Minute},
		{6, time.Hour},
		{7, 2 * time.Hour},
		{8, 6 * time.Hour},
		{9, 6 * time.Hour}, // 封顶
		{50, 6 * time.Hour},
	}
	kinds := []errorsx.ErrorKind{
		errorsx.KindNetwork, errorsx.KindTimeout, errorsx.KindTransient,
		errorsx.KindUpstreamDown, errorsx.KindStreamTimeout,
		errorsx.KindUpstreamOverloaded, errorsx.KindEmptyResponse,
		errorsx.KindUpstreamContextLoss,
	}
	for _, k := range kinds {
		for _, tc := range cases {
			if got := ProbeBackoffForKind(k, tc.attempt); got != tc.want {
				t.Fatalf("ProbeBackoffForKind(%s, %d) = %v, want %v", k, tc.attempt, got, tc.want)
			}
		}
	}
	// err-code mapping rides the same ladder (2026-09-25 实测滞留构成：
	// connection_error/503/timeout/500 全部在网络短梯类)。
	for code, k := range map[string]errorsx.ErrorKind{
		"connection_error": errorsx.KindNetwork,
		"network_error":    errorsx.KindNetwork,
		"timeout":          errorsx.KindTimeout,
		"http_503":         errorsx.KindUpstreamDown,
		"http_500":         errorsx.KindUpstreamDown,
		"http_502":         errorsx.KindUpstreamDown,
	} {
		for _, tc := range cases {
			if got := ProbeBackoffForErrCode(code, tc.attempt); got != tc.want {
				t.Fatalf("ProbeBackoffForErrCode(%s, %d) = %v, want %v (kind %s)", code, tc.attempt, got, tc.want, k)
			}
		}
	}
	// 通用梯不受长尾化影响（auth 类沿 7 步梯沉底，是 2026-09-25 复审确认的现行行为）。
	if got := ProbeBackoffForKind("", 7); got != 6*time.Hour {
		t.Fatalf("generic ladder attempt 7 = %v, want 6h (must be unchanged)", got)
	}

	// 开关关闭 = 旧短梯行为（60s 永续），供回滚。
	withP02Settings(t, false, 60)
	for _, attempt := range []int{5, 8, 50} {
		if got := ProbeBackoffForKind(errorsx.KindNetwork, attempt); got != 60*time.Second {
			t.Fatalf("long-tail OFF: ProbeBackoffForKind(network, %d) = %v, want 60s", attempt, got)
		}
	}
}

// TestRequestFailure_MinGapSkip pins the wiring of the request_failure
// pair-level frequency gate: throttled triggers must not reach Enqueue, the
// gate must be fully disabled at gap=0, and the periodic pump path must
// bypass it.
func TestRequestFailure_MinGapSkip(t *testing.T) {
	newWorker := func(throttle func(int) bool, gap int) (*NodeProbeWorker, *int) {
		enqueueCalls := 0
		w := &NodeProbeWorker{
			probeQueue: &ProbeQueue{}, // non-nil so submitViaQueueSource takes the queue path
			requestFailureThrottleFn: func(context.Context, int, string, int) bool {
				return throttle(gap)
			},
			enqueueFn: func(context.Context, ProbeQueueTask) (int64, bool, error) {
				enqueueCalls++
				return 1, true, nil
			},
		}
		return w, &enqueueCalls
	}

	t.Run("throttled skips enqueue", func(t *testing.T) {
		withP02Settings(t, true, 60)
		w, enqueueCalls := newWorker(func(int) bool { return true }, 60)
		inserted, err := w.submitViaQueueSource(42, "m", "default", "", "request_failure")
		if err != nil || inserted {
			t.Fatalf("throttled submit = (%v, %v), want (false, nil)", inserted, err)
		}
		if *enqueueCalls != 0 {
			t.Fatalf("throttled trigger still enqueued %d time(s)", *enqueueCalls)
		}
	})

	t.Run("allowed passes through", func(t *testing.T) {
		withP02Settings(t, true, 60)
		w, enqueueCalls := newWorker(func(int) bool { return false }, 60)
		inserted, err := w.submitViaQueueSource(42, "m", "default", "", "request_failure")
		if err != nil || !inserted {
			t.Fatalf("allowed submit = (%v, %v), want (true, nil)", inserted, err)
		}
		if *enqueueCalls != 1 {
			t.Fatalf("allowed trigger enqueue count = %d, want 1", *enqueueCalls)
		}
	})

	t.Run("gap zero disables gate", func(t *testing.T) {
		withP02Settings(t, true, 0)
		w, enqueueCalls := newWorker(func(int) bool { return true }, 0)
		inserted, err := w.submitViaQueueSource(42, "m", "default", "", "request_failure")
		if err != nil || !inserted {
			t.Fatalf("gap=0 submit = (%v, %v), want (true, nil) — 0 must restore legacy behavior", inserted, err)
		}
		if *enqueueCalls != 1 {
			t.Fatalf("gap=0 enqueue count = %d, want 1 (gate must be bypassed, not consulted)", *enqueueCalls)
		}
	})

	t.Run("periodic pump bypasses gate", func(t *testing.T) {
		withP02Settings(t, true, 60)
		w, enqueueCalls := newWorker(func(int) bool { return true }, 60)
		inserted, err := w.submitViaQueueSource(42, "m", "default", "", "periodic")
		if err != nil || !inserted {
			t.Fatalf("periodic submit = (%v, %v), want (true, nil) — pump must not be throttled", inserted, err)
		}
		if *enqueueCalls != 1 {
			t.Fatalf("periodic enqueue count = %d, want 1", *enqueueCalls)
		}
	})
}

// TestProbeServiceAttemptPersistsAcrossGenerations pins the P0-2 fix for the
// queue-task generation attempt reset (r0925 handoff 遗留 1): a freshly
// pumped task (Attempt=1) for a pair with state.consecutive_failures=6 must
// ladder from rung 7 (2h long-tail), not restart at 5s; the ladder position
// survives generation death. Fail-open: an unreadable state falls back to
// the task's own attempt.
func TestProbeServiceAttemptPersistsAcrossGenerations(t *testing.T) {
	withP02Settings(t, true, 60)

	run := func(taskAttempt, stateCF int, stateOK bool) (ProbeQueueResult, probeOutcome) {
		t.Helper()
		rec := &probeOutcomeRecorder{}
		service := newTestProbeService(
			nodeProbeRoundResult{errCode: "connection_error"},
			gatewayProbeResult{round: nodeProbeRoundResult{errCode: "http_503"}, pinned: true},
			rec.apply,
		)
		service.stateFailuresFn = func(context.Context, int, string) (int, bool) {
			return stateCF, stateOK
		}
		result, err := service.Run(context.Background(), probeServiceTask(taskAttempt))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		outcome := rec.single(t)
		if outcome.attempt == 0 {
			t.Fatal("outcome not recorded")
		}
		return result, outcome
	}

	t.Run("state ladder position wins over fresh task attempt", func(t *testing.T) {
		before := time.Now()
		result, outcome := run(1, 6, true)
		if outcome.attempt != 7 {
			t.Fatalf("attempt = %d, want 7 (cf=6 + 1)", outcome.attempt)
		}
		// rung 7 of 5s→15s→30s→60s→5m→1h→2h→(6h cap)
		assertRetryDelay(t, result.NextRunAt, before, 2*time.Hour)
	})

	t.Run("sustained failure sinks to the 6h cap across generations", func(t *testing.T) {
		before := time.Now()
		result, outcome := run(3, 49, true)
		if outcome.attempt != 50 {
			t.Fatalf("attempt = %d, want 50", outcome.attempt)
		}
		assertRetryDelay(t, result.NextRunAt, before, 6*time.Hour)
	})

	t.Run("task attempt kept when state outranked by task counter", func(t *testing.T) {
		before := time.Now()
		result, outcome := run(5, 1, true)
		if outcome.attempt != 5 {
			t.Fatalf("attempt = %d, want 5 (max of task 5 and cf+1=2)", outcome.attempt)
		}
		// rung 5 of 5s→15s→30s→60s→5m→…
		assertRetryDelay(t, result.NextRunAt, before, 5*time.Minute)
	})

	t.Run("unreadable state fails open to task attempt", func(t *testing.T) {
		before := time.Now()
		result, outcome := run(1, 99, false)
		if outcome.attempt != 1 {
			t.Fatalf("attempt = %d, want 1 (fail-open)", outcome.attempt)
		}
		assertRetryDelay(t, result.NextRunAt, before, 5*time.Second)
	})
}

// TestProbeRoundRootCauseReachesCaller pins the 2026-09-26 P1 fix: the
// root-cause classification defer in probeGateway (and structurally
// probeDirect) must mutate the NAMED return slot. Live evidence of the bug
// on build 2251/2252: llmgw_node_probe_root_cause_total carried zero
// protocol/gateway rows and logs said "node/upstream fault" for
// endpoint_build/decrypt rounds — every `return r` copied the unnamed
// result before the defer classified it.
func TestProbeRoundRootCauseReachesCaller(t *testing.T) {
	t.Run("gateway 404 classifies protocol end to end", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
		}))
		t.Cleanup(srv.Close)
		w := &NodeProbeWorker{baseURL: srv.URL, apiKey: "test-key", client: srv.Client()}
		res := w.probeGateway(context.Background(), 42, "m")
		if res.ok {
			t.Fatal("404 stub must fail the round")
		}
		if res.errCode != "http_404" {
			t.Fatalf("errCode = %q, want http_404", res.errCode)
		}
		if res.rootCause != ProbeRootCauseProtocol {
			t.Fatalf("rootCause = %q, want protocol — the defer's classification must reach the caller", res.rootCause)
		}
		if !strings.Contains(res.errDetail, "(root_cause=protocol)") {
			t.Fatalf("errDetail missing annotation: %q", res.errDetail)
		}
	})
	t.Run("gateway endpoint_build classifies gateway end to end", func(t *testing.T) {
		// Unreachable endpoint → transport error classifies node; use a
		// request_build shape instead: malformed baseURL forces
		// http.NewRequestWithContext failure inside probeGateway.
		w := &NodeProbeWorker{baseURL: "http://[::1]:namedport", apiKey: "k", client: http.DefaultClient}
		res := w.probeGateway(context.Background(), 42, "m")
		if res.ok || res.errCode != "request_build" {
			t.Fatalf("round = (ok=%v, errCode=%q), want request_build failure", res.ok, res.errCode)
		}
		if res.rootCause != ProbeRootCauseGateway {
			t.Fatalf("rootCause = %q, want gateway", res.rootCause)
		}
	})

	t.Run("probeDirect/probeGateway use named returns (structural)", func(t *testing.T) {
		src, err := os.ReadFile("node_probe.go")
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range []string{"probeDirect", "probeGateway"} {
			re := regexp.MustCompile(`func \(w \*NodeProbeWorker\) ` + fn + `\(ctx context\.Context, credID int, model string\) \(r nodeProbeRoundResult\)`)
			if !re.Match(src) {
				t.Fatalf("%s must keep the NAMED return (r nodeProbeRoundResult) — an unnamed return copies the result before the classify defer runs (2026-09-26 P1)", fn)
			}
		}
	})
}

// TestRequestFailureThrottlePredicateSQL pins the throttle predicate's SQL
// semantics against a real Postgres. TEST_PG_URL must point at a disposable
// database (R60 lesson: never at a shared/prod DB) — the test creates the
// node_probe_state shape it needs and inserts explicit fixture rows.
func TestRequestFailureThrottlePredicateSQL(t *testing.T) {
	url := os.Getenv("TEST_PG_URL")
	if url == "" {
		t.Skip("TEST_PG_URL not set — SQL semantics pin skipped (wire-level behavior is covered by TestRequestFailure_MinGapSkip)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect TEST_PG_URL: %v", err)
	}
	defer pool.Close()

	// The production table shape this predicate reads (subset of \d
	// node_probe_state; extra columns get NULL/defaulted).
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS node_probe_state (
		credential_id bigint NOT NULL,
		raw_model_name text NOT NULL,
		consecutive_failures integer NOT NULL DEFAULT 0,
		last_attempt_at timestamptz,
		next_retry_at timestamptz NOT NULL DEFAULT now(),
		next_retry_seconds integer NOT NULL DEFAULT 5,
		paused boolean NOT NULL DEFAULT false,
		last_direct_ok boolean,
		last_gateway_ok boolean,
		last_err_code text,
		last_err_detail text,
		in_flight_until timestamptz,
		updated_at timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (credential_id, raw_model_name)
	)`); err != nil {
		t.Fatalf("create node_probe_state: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DROP TABLE IF EXISTS node_probe_state`) })

	fixtures := []struct {
		model    string
		cf       int
		rowSQL   string
		throttle bool
		note     string
	}{
		{"mid-ladder-scheduled", 2, `'connection_error', now() - interval '90 seconds', now() + interval '1 hour'`, true,
			"排程中（next_retry_at 未到）+ 错误证据 → 跳过（频控主臂）"},
		{"healthy-parked", 0, `NULL, now() - interval '10 seconds', now() + interval '30 days'`, false,
			"健康停放（无错误证据）→ 不跳过：新失败必须立即重武装（INV-2）"},
		{"due-but-just-probed", 3, `'http_503', now() - interval '10 seconds', now() - interval '1 minute'`, true,
			"已到期但距上次探测 < gap → 跳过（min-gap 臂）"},
		{"due-and-stale", 3, `'http_503', now() - interval '10 minutes', now() - interval '1 minute'`, false,
			"已到期且距上次探测 > gap → 放行触发"},
	}

	for _, f := range fixtures {
		if _, err := pool.Exec(ctx, `
			INSERT INTO node_probe_state (credential_id, raw_model_name, consecutive_failures, last_err_code, last_attempt_at, next_retry_at, last_direct_ok)
			VALUES (420926, $1, $2, `+f.rowSQL+`, FALSE)
			ON CONFLICT (credential_id, raw_model_name) DO NOTHING`, f.model, f.cf); err != nil {
			t.Fatalf("insert fixture %s: %v", f.model, err)
		}
	}
	// counter-only evidence row: err code NULL but cf>0, probe scheduled ahead.
	if _, err := pool.Exec(ctx, `
		INSERT INTO node_probe_state (credential_id, raw_model_name, consecutive_failures, last_err_code, last_attempt_at, next_retry_at, last_direct_ok)
		VALUES (420926, 'counter-only-evidence', 2, NULL, now() - interval '90 seconds', now() + interval '1 hour', FALSE)
		ON CONFLICT (credential_id, raw_model_name) DO NOTHING`); err != nil {
		t.Fatalf("insert counter-only fixture: %v", err)
	}

	check := func(model string, want bool) {
		t.Helper()
		var got bool
		err := pool.QueryRow(ctx, requestFailureThrottleSQL(), 420926, model, 60).Scan(&got)
		if err != nil {
			t.Fatalf("predicate for %s: %v", model, err)
		}
		if got != want {
			t.Fatalf("predicate(%s) = %v, want %v", model, got, want)
		}
	}
	check("mid-ladder-scheduled", true)
	check("healthy-parked", false)
	check("due-but-just-probed", true)
	check("due-and-stale", false)
	check("counter-only-evidence", true) // cf>0 也算错误证据（pumpDueStatesSQL 同口径）

	// Absent row: the helper itself must fail open (pgx.ErrNoRows → false),
	// not error out.
	w := &NodeProbeWorker{db: pool}
	if w.requestFailureTriggerThrottled(ctx, 420926, "absent-row", 60) {
		t.Fatal("absent row must fail open (no throttle), got throttled")
	}
}
