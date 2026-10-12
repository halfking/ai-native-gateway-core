package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func readMigration840(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(b)
}

// TestMigration840_AnalyzeStatsThrottleSlot pins migration 840, which adds a
// CROSS-INSTANCE shared throttle slot for analyze_llm_gateway_table_stats.
//
// What it fixes (252 production, runbook §10.107):
//
//	§10.53 shipped an advisory lock for the analyze pass, and it was live on
//	2026-10-07. Post-deploy measurement showed the lock NEVER fires:
//	  · it is xact-scoped, so it lives only until this pass commits
//	    (pg_stat_statements mean_exec_time = 93.81 s)
//	  · `try` semantics means "skip this round", not "wait for your turn"
//	  · the two instances run on INDEPENDENT promote ticks, measured 6m01s
//	    apart (154 12:42:00 / 245 12:48:01)
//	⇒ offset (6 min) ≫ hold time (94 s) ⇒ never contended ⇒ still 2 passes/hour.
//
// The pass is the #1 consumer of database time (81.0%, ~75 min/day), so
// halving it is the single largest remaining item.
//
// The fix is NOT another lock and NOT aligning the tickers. It is a one-row
// shared table whose claim is atomic via
//
//	INSERT … ON CONFLICT (task_name) DO UPDATE … WHERE … RETURNING
//
// Two instances racing on that statement: exactly one gets a row back.
//
// ★ These assertions pin the MECHANISM (the statement shape). The EFFECT
//
//	("only one of two racing callers wins") can only be proven against a real
//	database — see bg/analyze_throttle_realdb_test.go, which races two
//	concurrent claims and requires exactly one true. That split is deliberate:
//	after §10.107 we know a green mechanism gate is not evidence of effect.
func TestMigration840_AnalyzeStatsThrottleSlot(t *testing.T) {
	up := readMigration840(t, "840_analyze_stats_throttle_slot.sql")
	down := readMigration840(t, "840_analyze_stats_throttle_slot.down.sql")

	// ── 共享表 ────────────────────────────────────────────────────────────
	require.Contains(t, up, "CREATE TABLE IF NOT EXISTS public.llm_gateway_task_state",
		"840 must create the shared state table idempotently (installer re-runs every file)")
	require.Contains(t, up, "task_name         text        PRIMARY KEY",
		"task_name must be the primary key — it is what ON CONFLICT (task_name) targets")
	require.Contains(t, up, "last_started_at   timestamptz NOT NULL",
		"last_started_at must be NOT NULL: a NULL start would make the age predicate NULL and silently always-proceed")
	require.Contains(t, up, "last_completed_at timestamptz",
		"last_completed_at must exist so a crashed run can fall back to its start time")

	// ── 原子占槽：形状就是全部 ────────────────────────────────────────────
	// ★ 这三行必须同时在。少任何一行，跨实例互斥就不成立：
	//   少 ON CONFLICT → 并发会各插一行；少 WHERE → 两台都更新并都拿到返回行；
	//   少 RETURNING → 调用方无法区分「拿到槽」与「没拿到」。
	require.Contains(t, up, "ON CONFLICT (task_name) DO UPDATE",
		"claim must be an upsert; a bare INSERT would race into duplicate rows")
	require.Contains(t, up, "WHERE COALESCE(s.last_completed_at, s.last_started_at) < now() - p_min_interval",
		"the guard must use COALESCE(completed, started): a run that crashed after claiming "+
			"left completed_at NULL, and without COALESCE the NULL comparison would yield NULL "+
			"(not TRUE) so that task could never be claimed again — a permanently wedged slot")
	require.Contains(t, up, "RETURNING TRUE INTO got",
		"claim must RETURN so the caller can tell 'got the slot' from 'someone else has it'")
	require.Contains(t, up, "RETURN COALESCE(got, false)",
		"no row back ⇒ got stays NULL ⇒ must be reported as false, not NULL "+
			"(a NULL boolean would make the caller's if-not-locked check silently false-negative)")

	// 阈值是参数而不是硬编码：调用方（Go）决定 50 分钟，SQL 只执行它。
	require.Contains(t, up, "p_min_interval interval DEFAULT interval '50 minutes'",
		"the window must be a parameter; hardcoding it in SQL would make the Go constant a lie")

	// ── 参数校验：空 task_name 会造出垃圾行，必须在 DB 侧挡住 ────────────
	require.Contains(t, up, "RAISE EXCEPTION 'claim_llm_gateway_task_slot: p_task_name 不能为空'")
	require.Contains(t, up, "RAISE EXCEPTION 'complete_llm_gateway_task_slot: p_task_name 不能为空'")

	// ── 完成时刻只是记账，不参与准入 ─────────────────────────────────────
	require.Contains(t, up, "CREATE OR REPLACE FUNCTION public.complete_llm_gateway_task_slot(p_task_name text)",
		"completion stamping must exist — it is the human-verifiable signal that the slot is cycling")
	require.NotContains(t, up, "last_completed_at < now()",
		"completion must NOT become part of admission; it is bookkeeping only")

	// ── 不许把 §10.53 那把锁删掉：两者正交 ──────────────────────────────
	require.NotContains(t, up, "pg_advisory",
		"840 must not drop or reimplement the advisory lock — the lock guards true "+
			"overlap (both instances in the same second), the slot guards staggered ticks")

	// ── down 只删本迁移自建的对象 ────────────────────────────────────────
	require.Contains(t, down, "DROP FUNCTION IF EXISTS public.complete_llm_gateway_task_slot(text)")
	require.Contains(t, down, "DROP FUNCTION IF EXISTS public.claim_llm_gateway_task_slot(text, interval)")
	require.Contains(t, down, "DROP TABLE IF EXISTS public.llm_gateway_task_state")
	require.NotContains(t, down, "analyze_llm_gateway_table_stats(",
		"down must not touch the analyze function itself — 840 never modified it")
}

// TestMigration840_RegisteredInAutoStartupSequence pins that 840 rides the
// normal startup path. A migration that exists but is never applied is worse
// than no migration: the Go caller would then permanently take the
// "unthrottled" degrade branch and nobody would notice (§10.99.3 face).
func TestMigration840_RegisteredInAutoStartupSequence(t *testing.T) {
	mainGo := readMigration840(t, "../../../installer/cmd/llm-gw-installer/main.go")
	runnerGo := readMigration840(t, "../../../installer/internal/dbinit/runner.go")

	require.Contains(t, mainGo, "//go:embed embeddata/startup/840_analyze_stats_throttle_slot.sql",
		"840 must be embedded or the installer's map lookup misses it")
	require.Contains(t, mainGo, "analyzeStatsThrottleSlot840 []byte")
	require.Contains(t, mainGo, `"startup/840_analyze_stats_throttle_slot.sql":`,
		"840 must be in embeddedSQLFiles")
	require.Contains(t, runnerGo, `"840_analyze_stats_throttle_slot.sql"`,
		"840 must be in dbinit's startup file list, not just the embed map")

	// embeddata 副本必须与 canonical 逐字节相同（仓里另有 schema 对账门，
	// 但本门就地把这条不变量钉在 840 上，免得对账门漏掉 startup/ 这一段）。
	src, err := os.ReadFile("840_analyze_stats_throttle_slot.sql")
	require.NoError(t, err)
	cp, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/840_analyze_stats_throttle_slot.sql")
	require.NoError(t, err, "embeddata copy must exist — //go:embed fails the build without it")
	require.Equal(t, string(src), string(cp),
		"embeddata copy drifted from the canonical migration; the installer would run different SQL than the repo holds")
}

// TestMigration840_GoCallerDegradesOnMissingTable pins the degrade branch.
//
// ★ This is the safety property that makes 840's .down.sql a real rollback:
//
//	after `down`, the table is gone and every claim raises 42P01. If the Go
//	caller treated that as fatal, rolling back would stop the analyze pass
//	entirely — a worse outage than the duplication we were fixing.
func TestMigration840_GoCallerDegradesOnMissingTable(t *testing.T) {
	pm := readMigration840(t, "../../../bg/partition_manager.go")

	require.Regexp(t, regexp.MustCompile(`if isUndefinedTable\(err\) \{`), pm,
		"the caller must branch on undefined_table")
	require.Contains(t, pm, `slog.Warn("partition_manager: analyze throttle table missing (migration 840 not applied?) — running unthrottled")`,
		"the degrade branch must be loud — a silent fallback is indistinguishable from working")
	require.Contains(t, pm, "slotClaimed = true",
		"★ the degrade branch must run UNTHROTTLED. Setting it false would invert the "+
			"branch: a missing table would then silence analyze entirely (permanent outage) "+
			"rather than restoring pre-840 behaviour (duplication).")

	require.Contains(t, pm, "func isUndefinedTable(err error) bool",
		"42P01 detection must exist as a named helper")
	require.Contains(t, pm, `pgErr.Code == "42P01"`,
		"42P01 is the SQLSTATE for undefined_table")

	// 完成记账必须在 commit 之后、且失败不致命。
	completeIdx := strings.Index(pm, "complete_llm_gateway_task_slot")
	commitIdx := strings.LastIndex(pm, "if cerr := tx.Commit(timeoutCtx); cerr != nil {")
	require.Positive(t, completeIdx, "the caller must stamp completion")
	require.Positive(t, commitIdx, "commit must exist")
	require.Greater(t, completeIdx, commitIdx,
		"★ completion must be stamped AFTER commit: inside the transaction it would be "+
			"rolled back with it and the slot would only ever carry start times")
	require.Contains(t, pm, "analyze slot completion failed (non-fatal)",
		"a failed stamp must not abort the already-committed analyze pass")
}
