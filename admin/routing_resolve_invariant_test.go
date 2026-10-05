package admin

// 2026-10-01 批判式审计：把「resolve('X') 的 candidates 是否全部属于 X」
// 这条不变式**做成门**，而不是只在 /tmp 里跑一次性脚本。
//
// 背景：2026-09-29 的修复声称这条不变式可作为回归口径，但仓库里实际
// 没有任何东西能断言它 ——
//   * 5 个既有测试全部只测纯函数 filterResolveCandidatesByCid 与
//     NormalizeRouteKeyAliasesNoStrip，从不碰 DB；
//   * 真正做判定的 resolveInputCanonicalID 接收 *pgxpool.Pool，零覆盖；
//   * 唯一跑过不变式的是一个 /tmp 下的 python 脚本 + 11 个手挑模型，
//     不在仓库、不可重复、模型集合会漂移。
//
// 本文件补两层：
//   1. 纯函数红门：canonicalIDByVariantPriority 的 dot/dash 碰撞场景。
//      这一条在旧实现（SQL `ORDER BY id LIMIT 1`）下会**转红**——旧实现
//      根本不经过这个纯函数，所以「变异验证」的方式是把优先级顺序反过来。
//   2. 真库集成门：对真实 models_canonical 跑全量不变式，DB 不可用时
//      显式 skip 并声明「跳过不构成证据」。

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// ── 第一层：纯函数红门 ──────────────────────────────────────────────

// canonicalIDByVariantPriority 必须在 dot/dash 两种拼写同时入库时
// 选「与输入完全一致」的那个，而不是 id 最小的那个。
//
// 旧实现（`= ANY($1) ORDER BY id LIMIT 1`）在这个场景返回 50（错），
// 因为 `= ANY()` 是集合成员判定、不带顺序，`ORDER BY id` 只能按 id 排。
func TestCanonicalIDByVariantPriority_ExactFormBeatsLowerID(t *testing.T) {
	// 输入 'glm-5.3-flash' 的 strict 变体：精确形在前，dot↔dash 桥在后
	variants := modelname.NormalizeRouteKeyAliasesNoStrip("glm-5.3-flash")
	if len(variants) < 2 {
		t.Fatalf("expected the dash bridge to be present, got %v", variants)
	}
	if variants[0] != "glm-5.3-flash" {
		t.Fatalf("variants[0] must be the exact input form, got %q", variants[0])
	}
	// 两条拼写都在目录里，且「精确形」的 id 更大 —— 旧实现会选错
	matches := []canonicalNameID{
		{id: 50, name: "glm-5-3-flash"},  // 桥形，id 小
		{id: 100, name: "glm-5.3-flash"}, // 精确形，id 大
	}
	got, ok := canonicalIDByVariantPriority(variants, matches)
	if !ok {
		t.Fatal("expected a match")
	}
	if got != 100 {
		t.Fatalf("exact form must win over lower id: got %d want 100 "+
			"(variants=%v matches=%+v) — this is the ORDER BY id defect", got, variants, matches)
	}
}

// 反向：输入写 dash 形时，dash 形必须赢，哪怕 dot 形 id 更小。
func TestCanonicalIDByVariantPriority_DashInputWins(t *testing.T) {
	variants := modelname.NormalizeRouteKeyAliasesNoStrip("glm-5-3-flash")
	if variants[0] != "glm-5-3-flash" {
		t.Fatalf("variants[0] = %q, want the exact input 'glm-5-3-flash'", variants[0])
	}
	matches := []canonicalNameID{
		{id: 7, name: "glm-5.3-flash"}, // 点形，id 小
		{id: 900, name: "glm-5-3-flash"},
	}
	got, ok := canonicalIDByVariantPriority(variants, matches)
	if !ok || got != 900 {
		t.Fatalf("dash input must resolve to the dash row: got (%d,%v) want (900,true)", got, ok)
	}
}

// 同名重复（生产不存在，防卫性）：稳定取最小 id，不得随机。
func TestCanonicalIDByVariantPriority_TieBreaksOnLowestID(t *testing.T) {
	variants := []string{"glm-5.3"}
	matches := []canonicalNameID{
		{id: 300, name: "glm-5.3"},
		{id: 200, name: "glm-5.3"},
	}
	for i := 0; i < 20; i++ { // 多次调用必须同解
		got, ok := canonicalIDByVariantPriority(variants, matches)
		if !ok || got != 200 {
			t.Fatalf("tie must resolve deterministically to lowest id, got (%d,%v)", got, ok)
		}
	}
}

func TestCanonicalIDByVariantPriority_NoMatch(t *testing.T) {
	if _, ok := canonicalIDByVariantPriority([]string{"glm-5.3"}, nil); ok {
		t.Fatal("empty matches must report not-found (caller then fails open)")
	}
	// 目录里有形似但不在变体集里的名字 —— SQL 已限制，这里是纯函数层的兜底
	_, ok := canonicalIDByVariantPriority(
		[]string{"glm-5.3"},
		[]canonicalNameID{{id: 1, name: "glm-4"}},
	)
	if ok {
		t.Fatal("a row outside the variant set must not be selected")
	}
}

// ── 第二层：真库不变式门 ────────────────────────────────────────────

// resolveInvariantDBEnv is the DSN the live gate reads. It is opt-in: the
// gate SKIPS (loudly) when unset, and the skip message states that a skip is
// not evidence.
const resolveInvariantDBEnv = "TEST_RESOLVE_INVARIANT_DB_URL"

func openResolveInvariantPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(resolveInvariantDBEnv)
	if dsn == "" {
		t.Skipf("%s not set — SKIPPING the resolve-candidates invariant gate. "+
			"A skip is NOT evidence: the invariant stays unproven until this runs "+
			"against a real models_canonical.", resolveInvariantDBEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

// TestResolveCandidatesInvariant_Live walks every catalog model whose name
// carries a removableWrapperTokens wrapper and asserts the invariant the
// 2026-09-29 changelog claimed but never gated:
//
//	resolve('X') 的所有 candidate.canonical_id 都属于 resolve_input('X')
//
// It reproduces the resolve query's own candidate set (the same three
// branches handleRoutingResolve uses) rather than HTTP-calling the endpoint,
// so it needs no auth and no running gateway — but it DOES need a real
// database, because the whole failure mode was a join against real catalog
// rows.
func TestResolveCandidatesInvariant_Live(t *testing.T) {
	pool := openResolveInvariantPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT canonical_name FROM models_canonical
		WHERE canonical_name IS NOT NULL AND canonical_name <> ''
	`)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("catalog rows: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("empty models_canonical — the gate would be vacuous")
	}

	// Wrapper-carrying models are exactly the ones the 2026-09-29 audit found
	// polluted (11/72). Keep the whole catalog so a new wrapper product is
	// covered without editing this test.
	var checked int
	for _, model := range names {
		t.Run(model, func(t *testing.T) {
			inputCid, ok := resolveInputCanonicalID(ctx, pool, model)
			if !ok {
				return // fail-open path: nothing asserted, and nothing to assert
			}
			checked++
			// Reproduce the candidate set resolve WOULD return: the same three
			// WHERE branches handleRoutingResolve uses...
			rawModels := modelname.NormalizeRouteKeyAliases(model)
			candRows, err := pool.Query(ctx, `
				SELECT v.canonical_id
				FROM v_routable_credential_models v
				LEFT JOIN model_offers mo ON mo.credential_id = v.credential_id
					AND mo.raw_model_name = v.raw_model_name
				LEFT JOIN models_canonical mc ON mc.id = v.canonical_id
				WHERE (
				      lower(v.raw_model_name) = ANY($1)
				   OR lower(COALESCE(mo.standardized_name, v.raw_model_name)) = ANY($1)
				   OR lower(mc.canonical_name) = ANY($1)
				)
			`, rawModels)
			if err != nil {
				t.Fatalf("candidate query: %v", err)
			}
			pre := make([]resolveCandidate, 0, 16)
			for candRows.Next() {
				var c resolveCandidate
				if err := candRows.Scan(&c.CanonicalID); err != nil {
					candRows.Close()
					t.Fatalf("scan candidate: %v", err)
				}
				pre = append(pre, c)
			}
			candRows.Close()
			if err := candRows.Err(); err != nil {
				t.Fatalf("candidate rows: %v", err)
			}
			// ...AND the post-query filter step. Without this the gate measures
			// the PRE-filter set, which is *expected* to contain foreign
			// candidates — that is precisely what the filter exists to remove.
			// (First draft of this gate omitted the filter and reported ~30
			// false "INVARIANT BROKEN" models; a red gate that measures the
			// wrong stage is worse than no gate.)
			post := filterResolveCandidatesByCid(pre, inputCid)
			var foreign []int64
			for _, c := range post {
				// NULL canonical_id = legacy binding, deliberately kept.
				if c.CanonicalID == nil || *c.CanonicalID == inputCid {
					continue
				}
				foreign = append(foreign, *c.CanonicalID)
			}
			if len(foreign) > 0 {
				sort.Slice(foreign, func(i, j int) bool { return foreign[i] < foreign[j] })
				t.Errorf("INVARIANT BROKEN for %q: resolve_input canonical_id=%d but "+
					"post-filter candidates carry foreign canonical_id(s) %v "+
					"(pre-filter had %d candidates)", model, inputCid, foreign, len(pre))
			}
		})
	}
	if checked == 0 {
		t.Fatal("no model resolved to a canonical_id — the gate proved nothing")
	}
	t.Logf("invariant verified for %d/%d catalog models", checked, len(names))
}

// TestResolveRawModelsStillLeak documents the KNOWN, DELIBERATELY UNFIXED
// gap so it cannot be forgotten: resolveInputCanonicalID no longer lets a
// foreign canonical into `candidates`, but the response's `raw_models` still
// carries the wrapper-stripped forms.
//
// The dashboard compensates in web/src/utils/modelScopeOwnership.ts; any other
// raw_models consumer is still exposed. This test asserts the current state so
// that closing the gap is a deliberate, visible change rather than a silent
// one — and so nobody "fixes" the compensation while the upstream still lies.
func TestResolveRawModelsStillLeak(t *testing.T) {
	got := modelname.NormalizeRouteKeyAliases("glm-5.3-flash")
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "glm-5.3") {
		t.Skipf("raw_models no longer leaks the base model (%v) — if this is now "+
			"intentional, close the gap in modelScopeOwnership.ts too and update "+
			"the 2026-09-29 changelog's '已知边界' section", got)
	}
	t.Logf("KNOWN GAP (documented, compensated downstream in modelScopeOwnership.ts): "+
		"raw_models for 'glm-5.3-flash' still contains the base model: %v", got)
}
