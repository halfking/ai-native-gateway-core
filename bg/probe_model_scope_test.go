package bg

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestAutomaticProbeRecentUsageExistsSQLUses24hAndSessionExclusion(t *testing.T) {
	sql := automaticProbeRecentUsageExistsSQL()
	if !strings.Contains(sql, probeUsageWindowInterval) {
		t.Fatalf("scope SQL missing usage window %q", probeUsageWindowInterval)
	}
	if strings.Contains(sql, "interval '3 days'") {
		t.Fatal("scope SQL must not use the retired 3-day window")
	}
	if !strings.Contains(sql, "origin_stage") || !strings.Contains(sql, "probe_triggered") {
		t.Fatal("scope SQL must exclude probe traffic via session-family predicate")
	}
	for _, col := range []string{"rl.raw_model_name", "rl.canonical_model"} {
		if !strings.Contains(sql, col) {
			t.Fatalf("scope SQL missing model column match %q", col)
		}
	}
}

// TestAutomaticProbeScopeSQLReferencesOnlyProjectedColumns is the guard that
// the 2026-11-05 audit added after the first version of this file shipped.
//
// ★ 背景：scope SQL 原本比对 `rl.model`，而会话族投影（118 个 canonical
// 别名）里**没有**裸 `model`（基表那一列在投影里被 AS 成 outbound_model）。
// ⇒ 真库上直接 `ERROR: column rl.model does not exist`
// ⇒ 每次判定都读失败 ⇒ 这道门把所有自动探测都挡掉。
// ⇒ 而当时的测试**只做字符串断言**（"SQL 里含 rl.model 就算对"）⇒ 全绿。
//
// ★ 方法论：字符串断言只能证明「写下了什么」，不能证明「能跑」。
// ⇒ 这条门改为**对着投影真身**核对：把 scope SQL 里引用的每个 `rl.<col>`
// 与 `db.SessionFamilyTurnsHotSourceSQL()` 实际暴露的别名集合求差
// ⇒ 差集非空即红。列名写错、列被改名、投影缩列，都会当场变红。
var projectedAliasRe = regexp.MustCompile(`(?m)\bAS\s+([a-z_0-9]+)\s*,?`)

// sessionFamilyProjectedAliases returns the column aliases the session-family
// source actually exposes. Parsed from the rendered SQL so it tracks the real
// projection instead of a hand-kept copy of the column list.
func sessionFamilyProjectedAliases(t *testing.T) map[string]bool {
	t.Helper()
	src := db.SessionFamilyTurnsHotSourceSQL()
	out := map[string]bool{}
	for _, m := range projectedAliasRe.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	if len(out) < 100 {
		t.Fatalf("parsed only %d projected aliases from the session-family source — parser is stale", len(out))
	}
	return out
}

func TestAutomaticProbeScopeSQLReferencesOnlyProjectedColumns(t *testing.T) {
	aliases := sessionFamilyProjectedAliases(t)
	sql := automaticProbeRecentUsageExistsSQL()

	// Every `rl.<col>` the scope SQL reads must be a projected alias.
	refRe := regexp.MustCompile(`\brl\.([a-z_0-9]+)`)
	var missing []string
	for _, m := range refRe.FindAllStringSubmatch(sql, -1) {
		if !aliases[m[1]] {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("scope SQL references columns the session-family projection does NOT expose: %v\n"+
			"⇒ 真库上会 42P03（column does not exist）⇒ 每次判定都读失败 ⇒ 这道门会挡掉所有自动探测。\n"+
			"⇒ 修法：改用投影里真实存在的模型名列（raw_model_name / outbound_model / client_model / canonical_model）。",
			missing)
	}

	// The bare `model` alias must NOT come back: the base table has it, the
	// projection renames it. Guard the regression explicitly so the mistake
	// cannot be reintroduced by "just add rl.model back".
	if strings.Contains(sql, "rl.model ") || strings.Contains(sql, "rl.model)") || strings.Contains(sql, "rl.model,") {
		t.Fatal("scope SQL must not reference rl.model — the session-family projection has no bare 'model' alias")
	}
}

func TestProbeRecoveryMaxBackoffCapsNetworkLongTail(t *testing.T) {
	withP02Settings(t, true, 60)
	got := ProbeBackoffForKind(errorsx.KindNetwork, 8)
	if got != 5*time.Minute {
		t.Fatalf("network attempt 8 = %v, want 5m recovery cap", got)
	}
	got = ProbeBackoffForErrCode("http_503", 20)
	if got != 5*time.Minute {
		t.Fatalf("http_503 attempt 20 = %v, want 5m recovery cap", got)
	}
	// Permanent park still long.
	if got := ProbeBackoffForErrCode("http_404", 3); got != modelNotServedRecheckInterval {
		t.Fatalf("model-not-served park = %v, want %v", got, modelNotServedRecheckInterval)
	}
}

// TestAutomaticProbeModelInScopeFailsOpenOnDBError pins the fail-open
// semantics the 2026-11-05 audit restored.
//
// ★ 为什么要有这条门：scope 门的**目的**是「别让恢复探测被卡住」
// （INV-3 收紧的是**常态**探测量，不是恢复路径）。原先的实现读失败时
// 返回 err，两个调用点都把 err 当「拒绝」⇒ **fail-closed**
// ⇒ 一次瞬时读抖动就把恢复探测挡在门外 ⇒ 与门的意图相反。
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
