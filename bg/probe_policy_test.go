package bg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
	"github.com/kaixuan/llm-gateway-go/recentmodels"
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
	// 冻结 113 列视图变体（R49 自纠；R52 审计补全 actor）：origin_stage 不在
	// 视图契约内，视图读面只能用 flags+task_type+origin_actor 三臂（全部
	// 113 列在册）。
	//
	// 2026-10-23 审计：actor 列表不再硬编码，改由 internal/probekind 渲染
	// —— 此前 Go 侧（domains/stats/minute_entry.go）与 SQL 侧各写一份，
	// 漂移过一次（self-check-worker / model-quality-worker 只在 Go 侧缺席 ⇒
	// stats 把它们写出的探测行当业务流量计入，板上"总请求数"虚高）。
	// 本断言改为"形状 + 列表来源"两段：形状仍钉死，列表交给
	// probekind.SQLActorList()（它与 domains/stats 侧同源）。
	if got, want := ProbeTrafficExclusionPredicateView,
		"(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE) AND COALESCE(%s.task_type, '') <> 'probe_triggered'"+
			" AND COALESCE(%s.origin_actor, '') NOT IN ("+probekind.SQLActorList()+"))"; got != want {
		t.Fatalf("probeTrafficExclusionView = %q, want %q", got, want)
	}
	// 形状不变式：三条臂、三个 format 动词、actor 臂在、origin_stage 臂不在。
	if got := strings.Count(ProbeTrafficExclusionPredicateView, "%s"); got != 3 {
		t.Errorf("view predicate has %d format verbs, want 3", got)
	}
	if !strings.Contains(ProbeTrafficExclusionPredicateView, "origin_actor") {
		t.Error("view predicate lost its origin_actor arm")
	}
	if strings.Contains(ProbeTrafficExclusionPredicateView, "origin_stage") {
		t.Error("view predicate must not have an origin_stage arm (not in the 113-column contract)")
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
//
// ★ 2026-10-20（迁移 890，v1 族退役）：usage 证据源从 v1 热表换成**会话族热腿**，
// 探针排除谓词随之从 v1 物理双臂（quality_flags + origin_stage）换成**会话族**
// 三臂（origin_stage + task_type + origin_actor）——会话族没有 quality_flags 列。
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
		// ★ 890：usage 证据源是会话族热腿（v1 热表已 DROP）。
		"SELECT 1 FROM `+db.SessionFamilyTurnsHotSourceSQL()+` rl",
		"probeUsageWindowInterval",
		// ★ 890：会话族没有 quality_flags ⇒ 必须用会话族那条谓词。
		`fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("featuredCycle SQL missing policy fragment %q", want)
		}
	}
	// 负向：不得再用 v1 物理谓词（它引用 quality_flags ⇒ 会话族 42703）。
	// ⚠ 正向对照：上面那条会话族谓词断言。
	// ⚠ 边界：`probeTrafficExclusionPredicateSession,` 里不含
	// `probeTrafficExclusionPredicate,` ⇒ 不会误判。
	if strings.Contains(body, "fmt.Sprintf(probeTrafficExclusionPredicate,") {
		t.Error("featuredCycle 的读面是会话族，不得使用引用 quality_flags 的 v1 物理谓词")
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
//   - 会话族原生源（session_turns_hot，**没有 quality_flags**）→
//     probeTrafficExclusionPredicateSession（origin_stage + task_type +
//     origin_actor）；
//   - canonical 冻结 113 列视图（request_logs_with_current_month，
//     仍投影 quality_flags / task_type / origin_actor）→
//     ProbeTrafficExclusionPredicateView。R50 首版把物理谓词铺到
//     shared_pick/passive_probe_listener 的视图查询上，必 42703——本测试
//     按读面分治后的真实不变量钉桩。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 族整体被 DROP ⇒ 原先「物理 v1 表」
// 这一类读面**整类消失**，直接读 v1 物理表的调用点全部改读会话族原生源 ⇒
// 本门的「physical」一栏改成「session」（会话族谓词），「view」一栏不变
// （canonical 视图仍投影那三列，视图谓词照旧可用）。
func TestProbeExclusionPredicateCallSitesR50(t *testing.T) {
	cases := []struct {
		file    string
		session int // minimum session-family predicate sites (native session readers)
		view    int // minimum view-predicate (frozen 113-col contract) sites
	}{
		{"credential_selfcheck.go", 1, 0}, // recentUsageModels + selfcheckRecentFallback — session
		{"model_probe.go", 2, 0},          // featuredCycle + watchdog — session
		{"today_success_probe.go", 1, 0},  // recovery recheck — session
		{"model_tier.go", 1, 0},           // Top-N usage — session
		{"daily_probe_audit.go", 0, 1},    // 72h usage — canonical view
		{"shared_pick.go", 0, 1},          // most-used pick — native narrow projection (R170)
		{"passive_probe_listener.go", 0, 3},
	}
	for _, tc := range cases {
		src, err := readSource(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		// ★ 计数用**不依赖别名实参**的调用形态（`fmt.Sprintf(<谓词>`），
		//   而不是把 `"rl", "rl", "rl"` 写死 —— R170 起 shared_pick.go 的
		//   别名变成了变量（`alias`），写死实参的判据会把「别名变了」
		//   误报成「谓词被换掉了」⇒ ⇒ 判据必须落在**谓词身份**上。
		if got := strings.Count(src, `fmt.Sprintf(probeTrafficExclusionPredicateSession`); got < tc.session {
			t.Errorf("%s has %d session-family exclusion sites, want >= %d", tc.file, got, tc.session)
		}
		if got := strings.Count(src, `fmt.Sprintf(ProbeTrafficExclusionPredicateView`); got < tc.view {
			t.Errorf("%s has %d frozen-view exclusion sites, want >= %d", tc.file, got, tc.view)
		}
	}
	// ★ 890 之后新增的一条**方向性**门：直读会话族原生源的读面**不得**用
	// 视图谓词（视图谓词引用 quality_flags，会话族没有这一列 ⇒ 每次 42703）。
	// ⚠ 正向对照：上表里 session 一栏 > 0 的那几个文件都必须命中会话族谓词。
	// ⚠ 边界：`ProbeTrafficExclusionPredicateView,` 与
	// `probeTrafficExclusionPredicateSession,` 是两个不同的标识符 ⇒ 不互含。
	for _, file := range []string{
		"credential_selfcheck.go",
		"model_probe.go",
		"today_success_probe.go",
		"model_tier.go",
	} {
		src, err := readSource(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if strings.Contains(src, "ProbeTrafficExclusionPredicateView") {
			t.Errorf("%s 直读会话族原生源（无 quality_flags 列），不得使用引用 quality_flags 的视图谓词", file)
		}
		// 反向：canonical 视图读面不得用会话族谓词？—— 会话族谓词的三列
		// （origin_stage / task_type / origin_actor）在 canonical 视图里都在，
		// 所以那不是 42703 风险；这里只做记录，不断言。
	}
	// ★ R170 补上**另一侧**的方向性门：shared_pick.go 在 R170 改成读
	// **会话族窄投影**（db.SessionFamilyMostUsedClientModelSourceSQL），
	// 那个投影**带 details 腿的 quality_flags**、但**不带 origin_stage**
	// ⇒ ⇒ 它**必须**用 quality_flags 变体（视图变体），
	// 且**不许**用 origin_stage 变体（会话族谓词）—— 否则真库上每次 42703。
	//
	// ★ 与上面那组互为镜像：上面守「有 quality_flags 却没有 origin_stage 的
	//   读面不许用视图谓词」，这里守「有 quality_flags 但没有 origin_stage 的
	//   窄投影读面不许用会话族谓词」。★ 两边都判的是「**这个读面有哪些列**」，
	//   不是「它读的是不是视图」—— R170 把这条判据从「读面身份」改成「列集合」，
	//   因为窄投影既不是视图也不是原生会话表。
	{
		src, err := readSource("shared_pick.go")
		if err != nil {
			t.Fatalf("read shared_pick.go: %v", err)
		}
		if !strings.Contains(src, "fmt.Sprintf(ProbeTrafficExclusionPredicateView") {
			t.Errorf("shared_pick.go 的窄投影带 quality_flags ⇒ 必须用 quality_flags 变体谓词")
		}
		if strings.Contains(src, "probeTrafficExclusionPredicateSession") {
			t.Errorf("shared_pick.go 的窄投影**没有** origin_stage 列 ⇒ " +
				"不得使用引用 origin_stage 的会话族谓词（真库上每次 42703）")
		}
	}
	// The selfcheck usage scan must NOT carry the old single-arm spelling.
	selfcheckSrc, err := readSource("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(selfcheckSrc, "AND NOT COALESCE('probe' = ANY(rl.quality_flags), FALSE)") {
		t.Errorf("credential_selfcheck.go still has a single-arm usage scan; use probeTrafficExclusionPredicateSession")
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
	const wantView = `fmt.Sprintf(ProbeTrafficExclusionPredicateView, "rl", "rl", "rl")`
	if !strings.Contains(src, wantView) {
		t.Fatalf("daily_probe_audit usage scan must use the frozen-view predicate %q", wantView)
	}
	if strings.Contains(src, `fmt.Sprintf(probeTrafficExclusionPredicate,`) {
		t.Fatalf("daily_probe_audit must not reference the physical-table predicate (origin_stage is not in the view's 113-column contract)")
	}
}

// ─── R52 审计修正轮（2026-09-22） ─────────────────────────────────────

// TestFilterRecentEntriesByUsage pins the selfcheck primary 3-day filter:
// the shared Redis ranking (TTL=7d) may carry models last used 4-7 days ago —
// those must not become the selfcheck primary (INV-3 使用范围). Normalization
// matches recentmodels.Normalize (lowercase, whitespace-stripped); an empty
// usage set filters everything out (the caller then falls through to the
// 3-day DB fallback).
func TestFilterRecentEntriesByUsage(t *testing.T) {
	recent := []recentmodels.Entry{
		{Model: "GLM-5.3", Count: 100},
		{Model: "deepseek v3", Count: 50},
		{Model: "old-model", Count: 200},
	}
	// Normalize 只折叠空白+小写（不去连字符），used 集合必须与 entry 同源
	// 归一，否则窗口匹配静默丢真——这正是本函数要在生产里防住的坑。
	used := map[string]struct{}{
		recentmodels.Normalize("GLM 5.3"):    {},
		recentmodels.Normalize("DeepSeekV3"): {},
	}
	if _, ok := used[recentmodels.Normalize("GLM-5.3")]; ok {
		t.Fatal("normalize assumption broken: 'GLM 5.3' and 'GLM-5.3' unexpectedly equal")
	}
	used[recentmodels.Normalize("GLM-5.3")] = struct{}{}
	got := filterRecentEntriesByUsage(recent, used)
	if len(got) != 2 {
		t.Fatalf("filtered = %+v, want 2 in-window entries", got)
	}
	models := map[string]bool{}
	for _, e := range got {
		models[e.Model] = true
	}
	if !models["GLM-5.3"] || !models["deepseek v3"] {
		t.Fatalf("filtered = %+v, want GLM-5.3 + deepseek v3 (old-model dropped)", got)
	}
	if out := filterRecentEntriesByUsage(recent, map[string]struct{}{}); out != nil {
		t.Fatalf("empty usage set must filter to nil, got %+v", out)
	}
	if out := filterRecentEntriesByUsage(nil, used); out != nil {
		t.Fatalf("empty ranking must stay nil, got %+v", out)
	}
}

// TestSelfcheckPrimaryGatedByThreeDayUsage pins the R52 wiring: pickModels must
// narrow the shared 7d Redis ranking through recentInUsageWindow before primary
// selection, and the filter query must carry the probe traffic exclusion
// (probe traffic must not keep a model "in-window").
//
// ★ 2026-10-20（迁移 890，v1 族退役）：过滤查询的读面已迁到会话族 ⇒ 排除谓词
// 必须用会话族那条（origin_stage + task_type + origin_actor）；会话族没有
// quality_flags 列，用 v1 那条会 42703。
func TestSelfcheckPrimaryGatedByThreeDayUsage(t *testing.T) {
	src, err := readSource("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"w.recentInUsageWindow(queryCtx, credentialID, tenant, recentmodels.Read(queryCtx, w.redis, tenant, 10))",
		"probeUsageWindowInterval",
		`fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("credential_selfcheck.go missing 3-day primary gate fragment %q", want)
		}
	}
}

// ─── 两条探针排除谓词的 actor 词表锁步（R148 审计轮，2026-10-27） ───────────

// actorListFromPredicate extracts the origin_actor NOT IN (...) list from a
// rendered probe-exclusion predicate. Both arms share the same arm shape
// ("COALESCE(<alias>.origin_actor, '') NOT IN (...)"), so one extractor
// serves both.
//
// Error-returning (not *testing.T.Fatal) so the same helper can be reused by
// the negative control below without a live *testing.T.
func actorListFromPredicate(rendered string) ([]string, error) {
	const marker = "origin_actor, '') NOT IN ("
	i := strings.Index(rendered, marker)
	if i < 0 {
		return nil, fmt.Errorf("rendered predicate has no origin_actor NOT IN arm: %s", rendered)
	}
	rest := rendered[i+len(marker):]
	j := strings.Index(rest, ")")
	if j < 0 {
		return nil, fmt.Errorf("rendered predicate has an unterminated NOT IN list: %s", rendered)
	}
	items := strings.Split(rest[:j], ",")
	out := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		it = strings.Trim(it, "'")
		if it != "" {
			out = append(out, it)
		}
	}
	return out, nil
}

// actorListMatchesCanonical compares a rendered predicate's actor list with
// the canonical registry list. Returned error is non-nil when they differ.
func actorListMatchesCanonical(rendered string, canonical []string) error {
	got, err := actorListFromPredicate(rendered)
	if err != nil {
		return err
	}
	if len(got) != len(canonical) {
		return fmt.Errorf("lists %d actors (%v), want canonical %d (%v)",
			len(got), got, len(canonical), canonical)
	}
	for i, a := range got {
		if a != canonical[i] {
			return fmt.Errorf("actor[%d] = %q, want %q", i, a, canonical[i])
		}
	}
	return nil
}

// TestProbeTrafficExclusionPredicateActorListsStayInLockstep pins that BOTH
// probe-exclusion SQL arms — the view-family one and the session-family one —
// render the canonical actor list from internal/probekind.
//
// ★ 2026-10-27（R148 审计轮）：同一次「actor 列表不再手写」的订正只迁了
// 视图臂，会话臂被落下了 —— 它手写着 2026-09-22 R52 那次的四元组快照，
// 而规范登记表（probekind.ProbeActors）此后已长到七个 ⇒ 会话臂少了
// self-check-worker / model-quality-worker / mock-probe-client。
//
// 影响的是三个活读面：settle 基线 cohort（⇒ reward 基线）、
// candidate failure monitor 的 staleness 判定、凭据自检的「近 3 天业务
// 使用集合」。这三处只有 origin_actor 臂兜底 origin_stage 缺失的行。
//
// 与 candidate_query_sql_shape_test.go 里那条「modality 词表锁步」判据同款：
// 两条臂共享同一份事实源，判据钉住它们不许各写一份。
func TestProbeTrafficExclusionPredicateActorListsStayInLockstep(t *testing.T) {
	view := fmt.Sprintf(ProbeTrafficExclusionPredicateView, "rl", "rl", "rl")
	session := fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")

	for _, tc := range []struct {
		name     string
		rendered string
	}{
		{"view-family arm", view},
		{"session-family arm", session},
	} {
		if err := actorListMatchesCanonical(tc.rendered, probekind.ProbeActors); err != nil {
			t.Fatalf("%s actor list drifted from internal/probekind: %v", tc.name, err)
		}
	}

	// 负控（量具必须看得见差异）：把名单砍成三个 ⇒ 判据必须报错。
	// ★ 这条负控是本判据「有牙」的证明；没有它，上面两条断言可能在
	// 比较逻辑恒真时一起绿掉（§419：断言恒真比恒假更隐蔽）。
	const narrowed = "COALESCE(rl.origin_actor, '') NOT IN ('node-probe-worker', 'active-probe-worker', 'probe-service')"
	if err := actorListMatchesCanonical(narrowed, probekind.ProbeActors); err == nil {
		t.Fatal("negative control did not flip: a narrowed actor list was accepted as canonical ⇒ this guard has no teeth")
	}
}

