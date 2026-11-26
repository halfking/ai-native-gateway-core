package bg

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// withProbeScopeSettings swaps settings.Global for a registry carrying the
// probe specs with the two scope knobs pinned (2026-11-26 scope round).
func withProbeScopeSettings(t *testing.T, fullProbe, multimodalProbe bool) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &probeCostSettingsBackend{store: map[string][]byte{
		"probe.full_model_probe":        []byte(boolToString(fullProbe)),
		"probe.multimodal_auto_probe":   []byte(boolToString(multimodalProbe)),
		"probe.network_chain_long_tail": []byte(boolToString(true)),
	}})
	for _, spec := range settings.ProbeSpecs() {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
}

// withScopeTier installs a global ModelTier snapshot for the duration of the
// test and restores the previous one afterwards.
func withScopeTier(t *testing.T, tier *ModelTier) {
	t.Helper()
	prev := globalModelTier.Load()
	t.Cleanup(func() { globalModelTier.Store(prev) })
	globalModelTier.Store(tier)
}

// TestAutomaticProbeRecentUsageModelsSQLUses7DayWindowAndSessionExclusion
// pins the **shape** of the 7-day usage query that feeds the scope gate's
// cache.
//
// ★ 2026-11-26 变更：窗口从「名义 24h、实际只有热腿 8h」改成 7 天，
//   读源从热腿改成「热腿 ∪ 父表 + 分区键下界裁剪」，判据源从「逐模型
//   EXISTS」改成「一次 DISTINCT 取回清单」。
func TestAutomaticProbeRecentUsageModelsSQLUses7DayWindowAndSessionExclusion(t *testing.T) {
	sql := automaticProbeRecentUsageModelsSQL()

	if !strings.Contains(sql, probeScopeUsageWindowInterval) {
		t.Fatalf("scope SQL missing usage window %q", probeScopeUsageWindowInterval)
	}
	if probeScopeUsageWindowInterval != "interval '168 hours'" {
		t.Fatalf("scope window = %q, want 7 days (168 hours)", probeScopeUsageWindowInterval)
	}
	if probeScopeUsageWindowHours != 168 {
		t.Fatalf("scope window hours = %d, want 168", probeScopeUsageWindowHours)
	}
	// The retired 24h scope window must not come back.
	if strings.Contains(sql, "interval '24 hours'") {
		t.Fatal("scope SQL must not use the retired 24h window")
	}
	// Probe traffic must not count as usage.
	if !strings.Contains(sql, "origin_stage") || !strings.Contains(sql, "probe_triggered") {
		t.Fatal("scope SQL must exclude probe traffic via session-family predicate")
	}
	// Partition-key lower bound (keeps the 7-day scan off older month partitions).
	if !strings.Contains(sql, "partition_date >=") {
		t.Fatal("scope SQL must carry the partition-key lower bound")
	}
	if !strings.Contains(sql, "AT TIME ZONE 'UTC'") {
		t.Fatal("scope SQL lower bound must be timezone-explicit (UTC) to match the ts predicate")
	}
	for _, col := range []string{"rl.raw_model_name", "rl.outbound_model", "rl.canonical_model"} {
		if !strings.Contains(sql, col) {
			t.Fatalf("scope SQL missing model column %q", col)
		}
	}
	// ★2026-11-26：瘦源不带 client_model（它只在 details 表上 ⇒ 会付 60 倍
	//   查询代价）。抽样5,000 行实测 raw_model_name 与 client_model 仅 1.9%
	//   不同 ⇒ 等价替身成立。这条断言防止「哪天顺手把 client_model 加回来」
	//   —— 那会把0.57s 的缓存填充打回 34s。
	if strings.Contains(sql, "rl.client_model") {
		t.Fatal("scope SQL must not read client_model — it lives on the details table and costs 60x (0.57s → 34.33s measured)")
	}
}

// TestAutomaticProbeScopeSQLReferencesOnlyKnownTurnsColumns is the guard that
// the 2026-11-05 audit added after the first version of this file shipped.
//
// ★ 背景：scope SQL 原本比对 `rl.model`，而会话族投影（118 个 canonical
// 别名）里**没有**裸 `model`（基表那一列在投影里被 AS 成 outbound_model）。
// ⇒ 真库上直接 `ERROR: column rl.model does not exist`
// ⇒ 每次判定都读失败 ⇒ 这道门会把**所有**自动探测挡掉。
// ⇒ 而当时的测试**只做字符串断言**（"SQL 里含 rl.model 就算对"）⇒ 全绿。
//
// ★ 方法论：字符串断言只能证明「写下了什么」，不能证明「能跑」。
// ⇒ 这条门改为**对着投影真身**核对。
//
// ★★ 2026-11-26：判据源从「全投影源」换成「瘦源」
//   （db.SessionFamilyTurnsModelNamesSourceSQL）⇒ ⇒ 守卫也跟着换：
//   瘦源读的是**基表列**（t.<col>），不是投影别名 ⇒ ⇒ 守卫分成两半：
//
//	(a) scope SQL 引用的每个 `rl.<col>` 必须是**瘦源暴露的别名**；
//	(b) 瘦源引用的每个 `t.<col>` 必须是**全投影源也从 turns 表读的列**
//	    ⇒ ⇒ 瘦源不可能凭空读出一个不存在的列。
var projectedAliasRe = regexp.MustCompile(`(?m)\bAS\s+([a-z_0-9]+)\s*,?`)
var turnsColumnRe = regexp.MustCompile(`\bt\.([a-z_0-9]+)`)
var rlAliasRe = regexp.MustCompile(`\brl\.([a-z_0-9]+)`)

// sessionFamilyTurnsTableColumns returns the turns-table columns the
// canonical session-family projection actually reads (the `t.<col>`
// occurrences). Parsed from the rendered SQL so it tracks the real projection
// instead of a hand-kept copy of the column list.
func sessionFamilyTurnsTableColumns(t *testing.T) map[string]bool {
	t.Helper()
	src := db.SessionFamilyTurnsSourceLowerBoundSQL("(NOW() - interval '168 hours') AT TIME ZONE 'UTC'")
	out := map[string]bool{}
	for _, m := range turnsColumnRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	if len(out) < 20 {
		t.Fatalf("parsed only %d turns-table columns from the session-family source — parser is stale", len(out))
	}
	return out
}

// sessionFamilyLeanSourceAliases returns the aliases the lean model-name
// source exposes (the `AS <alias>` list).
func sessionFamilyLeanSourceAliases(t *testing.T) map[string]bool {
	t.Helper()
	src := db.SessionFamilyTurnsModelNamesSourceSQL("(NOW() - interval '168 hours') AT TIME ZONE 'UTC'")
	out := map[string]bool{}
	for _, m := range projectedAliasRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	return out
}

func TestAutomaticProbeScopeSQLReferencesOnlyKnownTurnsColumns(t *testing.T) {
	aliases := sessionFamilyLeanSourceAliases(t)
	sql := automaticProbeRecentUsageModelsSQL()

	// (a) Every `rl.<col>` the scope SQL reads must be an alias the lean
	// source actually exposes.
	var missing []string
	for _, m := range rlAliasRe.FindAllStringSubmatch(sql, -1) {
		if !aliases[m[1]] {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("scope SQL references aliases the lean source does NOT expose: %v\n"+
			"⇒ 真库上会 42P03（column does not exist）⇒ 缓存永远装不上 ⇒ 作用域门一直 fail-open。",
			missing)
	}

	// (b) Every `t.<col>` the lean source reads must be a column the canonical
	// projection also reads from the turns table — i.e. the lean source can
	// never invent a column that does not exist.
	known := sessionFamilyTurnsTableColumns(t)
	lean := db.SessionFamilyTurnsModelNamesSourceSQL("(NOW() - interval '168 hours') AT TIME ZONE 'UTC'")
	var unknown []string
	for _, m := range turnsColumnRe.FindAllStringSubmatch(lean, -1) {
		if !known[m[1]] {
			unknown = append(unknown, m[1])
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Fatalf("lean source reads turns columns the canonical projection does not read: %v\n"+
			"⇒ 这些列在 turns 表上未必存在 ⇒ 真库上 42P03。", unknown)
	}

	// The bare `model` alias must NOT come back: the base table has it, the
	// projection renames it. Guard the regression explicitly so the mistake
	// cannot be reintroduced by "just add rl.model back".
	if strings.Contains(sql, "rl.model ") || strings.Contains(sql, "rl.model)") || strings.Contains(sql, "rl.model,") {
		t.Fatal("scope SQL must not reference rl.model — the session-family projection has no bare 'model' alias")
	}
}

// TestAutomaticProbeScopeGateSemantics pins the four gates of the automatic
// probe scope (2026-11-26 scope round) and their order.
//
// ★ The order is the contract:
//  ① multimodal gate (media models are never auto-probed by default)
//  ② full-probe switch (everything is a candidate again)
//  ③ featured whitelist (operator-curated)
//  ④ 7-day business usage set
func TestAutomaticProbeScopeGateSemantics(t *testing.T) {
	q := &ProbeQueue{db: &pgxpool.Pool{}}

	t.Run("multimodal gate blocks media models by default", func(t *testing.T) {
		withProbeScopeSettings(t, false, false)
		withScopeTier(t, newTierWithSet(nil, nil, "gpt-image-2"))
		for _, m := range []string{"gpt-image-2", "sora-2", "wan3.0-video-cn", "gemini-3-pro-image"} {
			in, err := q.automaticProbeModelInScope(context.Background(), m)
			if err != nil {
				t.Fatalf("%s: unexpected error %v", m, err)
			}
			if in {
				t.Errorf("%s: media model must be OUT of automatic scope while probe.multimodal_auto_probe=false", m)
			}
		}
	})

	t.Run("multimodal gate also blocks media models when full probe is on", func(t *testing.T) {
		// The two knobs are orthogonal: full_model_probe widens the model
		// universe, multimodal_auto_probe controls the media cost class.
		withProbeScopeSettings(t, true, false)
		withScopeTier(t, newTierWithSet(nil, nil))
		in, _ := q.automaticProbeModelInScope(context.Background(), "gpt-image-2")
		if in {
			t.Error("gpt-image-2 must stay out of scope while probe.multimodal_auto_probe=false")
		}
	})

	t.Run("multimodal switch on lets media models through", func(t *testing.T) {
		withProbeScopeSettings(t, false, true)
		withScopeTier(t, newTierWithSet(nil, nil, "gpt-image-2"))
		in, _ := q.automaticProbeModelInScope(context.Background(), "gpt-image-2")
		if !in {
			t.Error("gpt-image-2 must be in scope once probe.multimodal_auto_probe=true and it has recent usage")
		}
	})

	t.Run("unknown model is out of scope by default", func(t *testing.T) {
		withProbeScopeSettings(t, false, false)
		withScopeTier(t, newTierWithSet(nil, nil, "gpt-4o"))
		in, err := q.automaticProbeModelInScope(context.Background(), "some-unknown-model")
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if in {
			t.Error("a model with no recent business usage must be OUT of scope by default")
		}
	})

	t.Run("model used within 7 days is in scope", func(t *testing.T) {
		withProbeScopeSettings(t, false, false)
		withScopeTier(t, newTierWithSet(nil, nil, "gpt-4o"))
		in, err := q.automaticProbeModelInScope(context.Background(), "gpt-4o")
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if !in {
			t.Error("a model with recent business usage must be IN scope")
		}
	})

	t.Run("featured whitelist stays exempt", func(t *testing.T) {
		withProbeScopeSettings(t, false, false)
		withScopeTier(t, newTierWithSet([]string{"glm-5.3-flash"}, nil))
		in, err := q.automaticProbeModelInScope(context.Background(), "glm-5.3-flash")
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if !in {
			t.Error("featured models must stay in scope (operator-curated whitelist)")
		}
	})

	t.Run("full probe switch admits unknown models", func(t *testing.T) {
		withProbeScopeSettings(t, true, false)
		withScopeTier(t, newTierWithSet(nil, nil))
		in, err := q.automaticProbeModelInScope(context.Background(), "some-unknown-model")
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if !in {
			t.Error("probe.full_model_probe=true must admit models with unknown status")
		}
	})

	t.Run("empty model name is rejected", func(t *testing.T) {
		withProbeScopeSettings(t, false, false)
		withScopeTier(t, newTierWithSet(nil, nil))
		in, _ := q.automaticProbeModelInScope(context.Background(), "   ")
		if in {
			t.Error("an empty model name must never be in scope")
		}
	})
}

// TestAutomaticProbeModelInScopeFailsOpenWhenUsageSetUnavailable pins the
// fail-open semantics for the **cache** arm (2026-11-26 scope round).
//
// ★ The gate's purpose is to shrink steady-state probe volume, NOT to block
//   recovery. ⇒ If the usage set cannot answer (tier not loaded / refresh
//   never succeeded), the gate must ALLOW (and surface the error so the call
//   sites log it) — never reject.
func TestAutomaticProbeModelInScopeFailsOpenWhenUsageSetUnavailable(t *testing.T) {
	withProbeScopeSettings(t, false, false)
	// A tier that never published a snapshot ⇒ no usable usage set.
	withScopeTier(t, NewModelTier(nil, ModelTierConfig{}))

	q := &ProbeQueue{db: &pgxpool.Pool{}}
	in, err := q.automaticProbeModelInScope(context.Background(), "gpt-4o")
	if !in {
		t.Fatal("scope check must fail OPEN when the usage set is unavailable — rejecting strands recovery")
	}
	if err == nil {
		t.Fatal("scope check must surface the 'cannot judge' error so the call sites can audit it")
	}
}

// TestAutomaticProbeModelInScopeFailsOpenOnDBError pins the fail-open
// semantics the 2026-11-05 audit restored.
//
// ★ 为什么要有这条门：scope 门的**目的**是「别让恢复探测被卡住」
// （INV-3 收紧的是**常态**探测量，不是恢复路径）。原先的实现读失败时
// 返回 err，两个调用点都把 err 当「拒绝」⇒ **fail-closed**
// ⇒一次瞬时读抖动就把恢复探测挡在门外 ⇒ 与门的意图相反。
// ⇒ 现在读失败时按「在范围内」放行 + 告警。
func TestAutomaticProbeModelInScopeFailsOpenOnDBError(t *testing.T) {
	// A queue whose db is nil ⇒ the usage check cannot run at all.
	q := &ProbeQueue{}
	inScope, err := q.automaticProbeModelInScope(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("nil-db scope check must not surface an error to the caller: %v", err)
	}
	if !inScope {
		t.Fatal("nil-db scope check must fail OPEN (allow the probe) — rejecting here strands recovery")
	}

	// The read-error arm shares the same shape (fail open + surface the error),
	// and it is pinned by the call-site guards below: both callers must treat
	// a non-nil scopeErr as "log it", never as "reject the probe".
	for _, tc := range []struct{ file, needle string }{
		{"probe_queue.go", "allowing automatic probe"},
		{"probe_service.go", "allowing automatic probe"},
	} {
		body := mustReadSourceBg(t, tc.file)
		if !strings.Contains(body, tc.needle) {
			t.Fatalf("%s: model-scope failure must be logged as %q, not treated as a rejection", tc.file, tc.needle)
		}
		// A rejection branch must not be reachable from the scopeErr arm.
		if strings.Contains(body, "scopeErr != nil {\n\t\t\treturn 0, false, scopeErr") {
			t.Fatalf("%s: scopeErr arm must not return the error as a rejection", tc.file)
		}
	}
}

// TestAutomaticProbeSourceBypassesModelScope_StaysClosed pins the recovery
// exemption set. It must stay a **closed** set: every entry must be a source
// whose rows provably carry failure evidence.
//
// ★ 2026-11-26 变更：多模态门**不在**豁免范围内 —— 恢复复检同样是真实的
//   出图/出片请求，而 2026-11-26 那场 gpt-image 风暴的触发源正是
//   periodic / credential_recovery 这两条恢复路径 ⇒ ⇒ 豁免若覆盖多模态门，
//   等于这次修正对风暴源头完全无效。
func TestAutomaticProbeSourceBypassesModelScope_StaysClosed(t *testing.T) {
	for _, s := range []string{"periodic", "credential_recovery"} {
		if !automaticProbeSourceBypassesModelScope(s) {
			t.Errorf("%s must bypass the model scope gate (recovery re-check)", s)
		}
	}
	for _, s := range []string{"", "request_failure", "selfcheck", "manual", "unknown"} {
		if automaticProbeSourceBypassesModelScope(s) {
			t.Errorf("%s must NOT bypass the model scope gate", s)
		}
	}
}

func TestProbeRecoveryMaxBackoffCapsNetworkLongTail(t *testing.T) {
	withP02Settings(t, true, 60)
	// ★ 2026-11-26：持续失败阈值由 21 改为 15（3 代 × 5 档，与新的通用梯
	//  末档 1h 同步）⇒ ⇒ 「阈值以下恒5m、阈值以上升 sustained 档」的
	//   分界也跟着前移 ⇒ 断言必须按**阈值**写，不能钉死某个 attempt 数。
	threshold := sustainedFailureAttemptThreshold()
	if threshold != 15 {
		t.Fatalf("sustained failure threshold = %d, want 15 (3 generations x 5 rungs)", threshold)
	}
	got := ProbeBackoffForKind(errorsx.KindNetwork, threshold-1)
	if got != 5*time.Minute {
		t.Fatalf("network attempt %d = %v, want 5m recovery cap", threshold-1, got)
	}
	got = ProbeBackoffForErrCode("http_503", threshold)
	if got != sustainedFailureRecheck() {
		t.Fatalf("http_503 attempt %d = %v, want sustained recheck %v", threshold, got, sustainedFailureRecheck())
	}
	// Permanent park still long.
	if got := ProbeBackoffForErrCode("http_404", 3); got != modelNotServedRecheckInterval {
		t.Fatalf("model-not-served park = %v, want %v", got, modelNotServedRecheckInterval)
	}
}
