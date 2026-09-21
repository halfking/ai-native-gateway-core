package bg

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── 共享谓词钉桩（2026-09-20 探测量策略） ─────────────────────────────

func TestProbePolicyPredicatesShape(t *testing.T) {
	// INV-1 healthy-parked: last probe succeeded AND no error AND no pending
	// failure counter — the exact shape success writers produce.
	if got, want := nodeProbeHealthyParkedSQL("nps"),
		"(nps.last_direct_ok = TRUE AND COALESCE(nps.last_err_code, '') = '' AND COALESCE(nps.consecutive_failures, 0) = 0)"; got != want {
		t.Fatalf("healthyParked = %q, want %q", got, want)
	}
	// Row-level error evidence: err code OR failure counter OR never
	// confirmed healthy. Must be the complement that keeps ladder rows and
	// unverified rows pumpable while healthy-parked rows stay out.
	ev := nodeProbeErrorEvidenceSQL("nps")
	for _, frag := range []string{
		"COALESCE(nps.last_err_code, '') <> ''",
		"COALESCE(nps.consecutive_failures, 0) > 0",
		"nps.last_direct_ok IS DISTINCT FROM TRUE",
	} {
		if !strings.Contains(ev, frag) {
			t.Fatalf("errorEvidence missing %q: %s", frag, ev)
		}
	}
	// INV-4 gate: DISTINCT ON latest run per model FIRST, success filter
	// AFTER — a model whose most recent run failed must not count.
	gate := credentialTwoProbeSuccessGateSQL("c.id")
	for _, frag := range []string{
		"npr.credential_id = c.id",
		"npr.completed_at > now() - interval '24 hours'",
		"DISTINCT ON (npr.raw_model_name)",
		"WHERE latest_run.success = TRUE",
		"HAVING count(*) >= 2",
	} {
		if !strings.Contains(gate, frag) {
			t.Fatalf("twoSuccessGate missing %q: %s", frag, gate)
		}
	}
	// INV-5: failure evidence correlates on the credential id expression.
	fe := credentialFailureEvidenceSQL("c.id", "interval '24 hours'")
	if !strings.Contains(fe, "cfl.credential_id = c.id") || !strings.Contains(fe, "interval '24 hours'") {
		t.Fatalf("failureEvidence malformed: %s", fe)
	}
	// INV-3: probe traffic exclusion predicate — 双臂（R49 审计修复）：
	// quality_flags 直探轮合成行 + origin_stage 网关轮落点，缺一不可。
	// 仅限物理表（hot/parent/分区，428 起有 origin_stage 列）。
	if got, want := probeTrafficExclusionPredicate,
		"(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE) AND COALESCE(%s.origin_stage, 'business') = 'business')"; got != want {
		t.Fatalf("probeTrafficExclusion = %q, want %q", got, want)
	}
	// 冻结 113 列视图变体（R49 自纠）：origin_stage 不在视图契约内，视图
	// 读面只能用 flags+task_type+origin_actor 三臂（全部 113 列在册）。
	if got, want := probeTrafficExclusionPredicateView,
		"(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE) AND COALESCE(%s.task_type, '') <> 'probe_triggered'"+
			" AND COALESCE(%s.origin_actor, '') NOT IN ('node-probe-worker', 'active-probe-worker'))"; got != want {
		t.Fatalf("probeTrafficExclusionView = %q, want %q", got, want)
	}
	if probeUsageWindowInterval != "interval '3 days'" {
		t.Fatalf("usage window = %q, want 3 days", probeUsageWindowInterval)
	}
}

// ─── pump 错误证据门 ──────────────────────────────────────────────────

// TestPumpDueStatesSQLErrorEvidenceGate pins INV-1's scheduler half: the pump
// must only enqueue node_probe_state rows with row-level error evidence.
// Healthy-parked rows (including legacy +1h rows that have since elapsed)
// must not be re-enqueued just because their next_retry_at matured.
func TestPumpDueStatesSQLErrorEvidenceGate(t *testing.T) {
	sql := pumpDueStatesSQL()
	if !strings.Contains(sql, nodeProbeErrorEvidenceSQL("nps")) {
		t.Fatalf("pumpDueStatesSQL lost the error-evidence gate:\n%s", sql)
	}
	for _, want := range []string{"nps.paused = FALSE", "nps.next_retry_at <= now()", "LIMIT $1"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("pumpDueStatesSQL missing %q:\n%s", want, sql)
		}
	}
}

// ─── featuredCycle 三门控 ─────────────────────────────────────────────

// TestFeaturedCycleErrorGated pins the 2026-09-20 re-scope of the 常用模型
// deep-ping: 3-day per-credential usage (probe traffic excluded), credential
// failure evidence, and the two-consecutive-success gate.
func TestFeaturedCycleErrorGated(t *testing.T) {
	src, err := readSource("model_probe.go")
	if err != nil {
		t.Fatalf("read model_probe.go: %v", err)
	}
	const anchor = "func (r *ModelProbeRunner) featuredCycle"
	idx := strings.Index(src, anchor)
	if idx < 0 {
		t.Fatal("featuredCycle not found")
	}
	end := strings.Index(src[idx:], "func (r *ModelProbeRunner) computeConsensus")
	if end < 0 {
		t.Fatal("featuredCycle end anchor not found")
	}
	body := src[idx : idx+end]
	// Source-grep: match the helper CALLS (rendered SQL only exists at
	// runtime) plus the raw fragments embedded in the query text.
	for _, want := range []string{
		`credentialFailureEvidenceSQL("c.id", probeFailureEvidenceWindowSQL)`,
		`credentialTwoProbeSuccessGateSQL("c.id")`,
		"SELECT 1 FROM request_logs_hot rl",
		"probeUsageWindowInterval",
		`fmt.Sprintf(probeTrafficExclusionPredicate, "rl", "rl")`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("featuredCycle SQL missing policy fragment %q", want)
		}
	}
}

// ─── 必要性门禁条件③（两连成功早停） ─────────────────────────────────

func TestProbeNecessitySkipsWhenTwoSiblingModelsProbeVerified(t *testing.T) {
	// 条件③：redis 证据过期（条件① fail-open），但 DB 里同凭据已有两个模型
	// 探测成功、且当前对自身无错误状态 → 跳过。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: false}, // TTL expired → condition ① cannot prove skip
	}}
	service, removal, rounds := newNecessityTestService(src, nil, nil)
	service.SetTwoSiblingSuccessesFn(func(context.Context, int, string) (bool, error) {
		return true, nil
	})

	task := necessityTask()
	_, err := service.Run(context.Background(), task)
	if !errors.Is(err, ErrProbeNotNecessary) {
		t.Fatalf("err = %v, want ErrProbeNotNecessary", err)
	}
	if *rounds != 0 {
		t.Fatalf("probe executed %d round(s), want 0", *rounds)
	}
	_, reason := removal.single(t)
	if !strings.HasPrefix(reason, SkipReasonTwoModelSuccesses) {
		t.Fatalf("removal reason = %q, want prefix %q", reason, SkipReasonTwoModelSuccesses)
	}
}

func TestProbeNecessityRunsWhenPairHasErrorStateDespiteSiblingSuccesses(t *testing.T) {
	// 条件③豁免：当前对在错误梯子（绑定不可用 / nps 错误行）→ 兄弟模型的
	// 成功不能短路它，探测必须执行。
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {Known: false},
	}}
	service, removal, rounds := newNecessityTestService(src, nil, nil)
	service.SetTwoSiblingSuccessesFn(func(context.Context, int, string) (bool, error) {
		return false, nil // pair carries error state → not skippable by ③
	})

	if _, err := service.Run(context.Background(), necessityTask()); err != nil {
		t.Fatal(err)
	}
	if *rounds != 1 {
		t.Fatalf("probe rounds = %d, want 1 (error-state pair is exempt from ③)", *rounds)
	}
	if removal.wasCalled() {
		t.Fatal("probe removed despite error-state pair")
	}
}

func TestProbeNecessityConditionThreeErrorDoesNotHideConditionTwo(t *testing.T) {
	// 条件③证据读失败必须独立 fail-open：不得吞掉条件②的跳过机会。
	probeDone := time.Now().Add(-30 * time.Minute)
	src := &staticEvidenceSource{evidence: map[string]NodeHealthEvidence{
		"model-a": {
			Known:              true,
			Healthy:            true,
			LastRequestErrorAt: probeDone.Add(-5 * time.Minute),
		},
		"model-bad": {Known: true, Healthy: false}, // 条件① 不成立
	}}
	service, removal, rounds := newNecessityTestService(src, []string{"model-bad"}, &nodeProbeRunSummary{Success: true, CompletedAt: probeDone})
	service.SetTwoSiblingSuccessesFn(func(context.Context, int, string) (bool, error) {
		return false, errors.New("db down")
	})

	_, err := service.Run(context.Background(), necessityTask())
	if !errors.Is(err, ErrProbeNotNecessary) {
		t.Fatalf("err = %v, want ErrProbeNotNecessary (condition ② must still fire)", err)
	}
	if *rounds != 0 {
		t.Fatal("probe executed despite condition ② eligibility")
	}
	_, reason := removal.single(t)
	if !strings.HasPrefix(reason, SkipReasonLastProbeHealthy) {
		t.Fatalf("removal reason = %q, want prefix %q", reason, SkipReasonLastProbeHealthy)
	}
}

// readSource loads a bg package source file for structural greps.
func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ─── R50：探测排除谓词全调用面闭环 ────────────────────────────────────

// TestProbeExclusionPredicateCallSitesR50 pins the R50 closure of the probe
// traffic exclusion — PER READ FACE (R49 自审轮勘误)：排除闭合按读面分治，
// 不变量不是"都用物理双臂谓词"，而是"读面对象有什么列就用什么谓词"：
//   - 物理表（request_logs_hot，428 起有 origin_stage 且网关探测轮落值）→
//     probeTrafficExclusionPredicate（flags + origin_stage='business'）；
//   - canonical 冻结 113 列视图（request_logs_with_current_month，
//     origin_stage 不在契约）→ probeTrafficExclusionPredicateView
//     （flags + task_type + origin_actor）。R50 首版把物理谓词铺到
//     shared_pick/passive_probe_listener 的视图查询上，必 42703——本测试
//     按读面分治后的真实不变量钉桩。
func TestProbeExclusionPredicateCallSitesR50(t *testing.T) {
	cases := []struct {
		file     string
		physical int // minimum physical-predicate (request_logs_hot) sites
		view     int // minimum view-predicate (frozen 113-col contract) sites
	}{
		{"credential_selfcheck.go", 1, 0}, // selfcheckRecentFallback — hot
		{"model_probe.go", 2, 0},          // featuredCycle + watchdog — hot
		{"today_success_probe.go", 1, 0},  // recovery recheck — hot
		{"model_tier.go", 1, 0},           // Top-N usage — hot
		{"daily_probe_audit.go", 0, 1},    // 72h usage — canonical view
		{"shared_pick.go", 0, 1},          // most-used pick — canonical view
		{"passive_probe_listener.go", 0, 3},
	}
	for _, tc := range cases {
		src, err := readSource(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if got := strings.Count(src, `fmt.Sprintf(probeTrafficExclusionPredicate, "rl", "rl")`); got < tc.physical {
			t.Errorf("%s has %d physical dual-arm exclusion sites, want >= %d", tc.file, got, tc.physical)
		}
		if got := strings.Count(src, `fmt.Sprintf(probeTrafficExclusionPredicateView, "rl", "rl", "rl")`); got < tc.view {
			t.Errorf("%s has %d frozen-view exclusion sites, want >= %d", tc.file, got, tc.view)
		}
	}
	// The selfcheck usage scan must NOT carry the old single-arm spelling.
	selfcheckSrc, err := readSource("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(selfcheckSrc, "AND NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)") {
		t.Errorf("credential_selfcheck.go still has a single-arm usage scan; use probeTrafficExclusionPredicate")
	}
}

// TestProbeUsageViewScanUsesFrozenContractPredicate（R49 自纠守卫，2026-09-20）：
// origin_stage 不在 canonical 视图的冻结 113 列契约内——凡以
// request_logs_with_current_month 为源的 bg usage 扫描必须用视图变体谓词
// （flags+task_type+origin_actor），物理表变体对视图必 42703（上一轮
// a7baa1f5a 曾把物理谓词用到视图上，真库验证时才炸出）。
func TestProbeUsageViewScanUsesFrozenContractPredicate(t *testing.T) {
	src, err := readSource("daily_probe_audit.go")
	if err != nil {
		t.Fatalf("read daily_probe_audit.go: %v", err)
	}
	const wantView = `fmt.Sprintf(probeTrafficExclusionPredicateView, "rl", "rl", "rl")`
	if !strings.Contains(src, wantView) {
		t.Fatalf("daily_probe_audit usage scan must use the frozen-view predicate %q", wantView)
	}
	if strings.Contains(src, `fmt.Sprintf(probeTrafficExclusionPredicate,`) {
		t.Fatalf("daily_probe_audit must not reference the physical-table predicate (origin_stage is not in the view's 113-column contract)")
	}
}
