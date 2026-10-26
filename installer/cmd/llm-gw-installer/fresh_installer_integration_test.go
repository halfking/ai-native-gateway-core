//go:build integration

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

const installerFreshDBURL = "TEST_INSTALLER_FRESH_DB_URL"

// TestFreshInstallerSessionTurnsHotBootstrap applies the exact fresh-installer
// sequence to an empty, dedicated database and verifies the Session V2 hot-path
// objects that the runtime needs. The target must be disposable: the test rejects
// a database that already contains public relations.
func TestFreshInstallerSessionTurnsHotBootstrap(t *testing.T) {
	dsn := os.Getenv(installerFreshDBURL)
	if dsn == "" {
		t.Skipf("%s not set; skipping fresh installer integration test", installerFreshDBURL)
	}
	if got := psqlScalar(t, dsn, `
		SELECT count(*)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
		  AND NOT EXISTS (
		    SELECT 1 FROM pg_depend d
		    WHERE d.objid = c.oid AND d.deptype = 'e'
		  );`); got != "0" {
		t.Fatalf("%s must point to an empty dedicated database; found %s non-extension public relations", installerFreshDBURL, got)
	}

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up installer SQL directory: %v", err)
	}
	defer cleanup()

	for _, name := range []string{"00-prereqs.sql", "01-schema.sql", "02-seed.sql"} {
		applyInstallerSQL(t, dsn, filepath.Join(sqlDir, name))
	}
	runner := dbinit.NewRunner("", "", "", sqlDir)
	for _, name := range runner.StartupFiles {
		applyInstallerSQL(t, dsn, filepath.Join(sqlDir, "startup", name))
	}

	for name, query := range map[string]string{
		"hot table":              `SELECT to_regclass('public.session_turns_hot') IS NOT NULL`,
		"current-month view":     `SELECT to_regclass('public.session_turns_with_current_month') IS NOT NULL`,
		"advisory lock function": `SELECT to_regprocedure('public.session_turns_advisory_lock_key(text,text)') IS NOT NULL`,
		"promotion function":     `SELECT to_regprocedure('public.promote_session_turns_hot_to_partition(interval,integer)') IS NOT NULL`,
		"hot table RLS":          `SELECT relrowsecurity FROM pg_class WHERE oid = 'public.session_turns_hot'::regclass`,
		// ★ 2026-10-21（R74 轮）改瞄：这条 pin 从「hot 表列数 = 106」改成
		//   「**hot 表列数 == 父表列数**」。
		//
		//   为什么必须改（实测，不是推断）：R74 轮在**门自己建的库**上真跑这道门，
		//   它**今天必然红**：`got "f"`（106 ≠ 实测 108）。
		//   ⇒ 差的 2 列来自 885（host_tags）/ 887（search_text）/ 889（tools_population）
		//   这一串会话族扩列。
		//
		//   为什么「绝对列数」这个 pin 本身就该退休：
		//   它在 2026-10-20 已经被订正过一次（104 → 106），**每加一列就红一次**
		//   ⇒ 它是**必然腐烂的 pin**，而它要守的性质（hot 面不是残废的 stub、
		//   而是父表的完整投影）已经被下面那条 **四元组逐列 parity** 严格更强地覆盖。
		//   ⇒ 保留「与父表列数相等」这一条**形态**断言，去掉会腐烂的绝对数。
		//
		//   security_invoker note: the 526-era bootstrap created this view WITH
		// (security_invoker = true), but 713's in-place rebuild dropped the
		// option — live production has it unset today (reloptions empty).
		// Restoring it via a 625/627-style hardening step is a candidate
		// follow-up pending a ruling; until then absence is the canonical
		// state and is covered by the "current-month view" existence check.
		"hot table column count matches parent": `
			SELECT (SELECT count(*) FROM information_schema.columns
			         WHERE table_schema = 'public' AND table_name = 'session_turns_hot')
			     = (SELECT count(*) FROM information_schema.columns
			         WHERE table_schema = 'public' AND table_name = 'session_turns')`,
		"view column count": `SELECT count(*) = 55 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'session_turns_with_current_month'`,
		"nullable JSONB digest": `
			SELECT count(*) = 2
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name IN ('session_turns', 'session_turns_hot')
			  AND column_name = 'digest'
			  AND data_type = 'jsonb'
			  AND is_nullable = 'YES'`,
		"parent hot column parity": `
			WITH parent_columns AS (
				SELECT column_name, data_type, is_nullable, ordinal_position
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'session_turns'
			), hot_columns AS (
				SELECT column_name, data_type, is_nullable, ordinal_position
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'session_turns_hot'
			)
			SELECT NOT EXISTS (
				(SELECT * FROM parent_columns EXCEPT SELECT * FROM hot_columns)
				UNION ALL
				(SELECT * FROM hot_columns EXCEPT SELECT * FROM parent_columns)
			)`,
	} {
		if got := psqlScalar(t, dsn, query); got != "t" {
			t.Errorf("fresh installer %s check failed: got %q", name, got)
		}
	}

	if got := psqlScalar(t, dsn, `
		SELECT count(*) = 3
		FROM pg_policies
		WHERE schemaname = 'public'
		  AND tablename = 'session_turns_hot'`); got != "t" {
		t.Errorf("fresh installer hot table policies: got %q, want exactly three", got)
	}

	// 2026-10-04 (§9.101): 467 wired as baseline-gap class. The baseline
	// CREATE TABLE public.sessions carries neither title nor user_tags and no
	// other registered migration adds them, so before this was wired a fresh
	// install could never satisfy SessionAggregator.GetSessionMetadata /
	// UpdateSessionMetadata — both read and write sessions.title +
	// sessions.user_tags, and both failed 42703 on every call. Production was
	// unaffected (it has the columns), which is exactly why it stayed
	// invisible. Pinned here so the gap cannot reopen silently.
	for name, query := range map[string]string{
		"467 sessions.title": `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='sessions' AND column_name='title')`,
		"467 sessions.user_tags": `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='sessions' AND column_name='user_tags')`,
		// The two statements that were failing, exercised as statements rather
		// than as a column check: a column can exist and the SQL still be
		// unparseable (that is exactly what the turn_writer 42P08 was).
		"GetSessionMetadata select parses": `SELECT count(*) >= 0 FROM (
			SELECT COALESCE(task_type,''), COALESCE(client_type,''), COALESCE(topic,''),
			       COALESCE(intent,''), COALESCE(title,''), COALESCE(user_tags, ARRAY[]::text[])
			FROM public.sessions WHERE tenant_id = 'x' AND session_id = 'y') q`,
	} {
		if got := psqlScalar(t, dsn, query); got != "t" {
			t.Errorf("fresh install %s check failed: got %q, want \"t\"", name, got)
		}
	}

	// 2026-09-07 embeddata sync (632 + 666..681 wired into StartupFiles): a
	// fresh install must expose every object the runtime expects from those
	// migrations. 665/661 are intentionally absent (audit ruling: 664 + 681 is
	// authoritative; 661 repairs存量库 whose 563 source has already been fixed).
	for name, query := range map[string]string{
		"632 fs-cleanup ledger table": `SELECT to_regclass('public.audit_attachments_filesystem_cleanup') IS NOT NULL`,
		"666 llm_hourly_stats table":  `SELECT to_regclass('public.llm_hourly_stats') IS NOT NULL`,
		"666 orchestration table":     `SELECT to_regclass('public.orchestration_runtime_instances') IS NOT NULL`,
		"667 normalize function":      `SELECT to_regprocedure('public.normalize_hour_timestamp(text)') IS NOT NULL`,
		"668 batch upsert function":   `SELECT to_regprocedure('public.upsert_llm_hourly_stats_batch(jsonb)') IS NOT NULL`,
		"669 annotations table":       `SELECT to_regclass('public.training_human_annotations') IS NOT NULL`,
		"673 annotation_stats view":   `SELECT to_regclass('public.annotation_stats') IS NOT NULL`,
		"674 annotations unique index": `
			SELECT count(*) = 1 FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'uq_training_human_annotations_request_id'`,
		"670 routing opt state table": `SELECT to_regclass('public.routing_optimization_state') IS NOT NULL`,
		"670 feedback log table":      `SELECT to_regclass('public.routing_feedback_log') IS NOT NULL`,
		"676 single-active index": `
			SELECT count(*) = 1 FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'idx_opt_state_single_active'`,
		"671 local catalog capabilities": `
			SELECT count(*) = 5 FROM provider_catalog
			WHERE kind = 'local' AND capabilities ? 'hosting_type'`,
		"672 title/summary local routes": `
			SELECT count(*) = 2 FROM work_type_model_route
			WHERE work_type_key IN ('session_title', 'session_summary')
			  AND canonical_name = '4-bit' AND weight = 10.0 AND enabled`,
		"675 qwen3.8 family row": `
			SELECT count(*) = 1 FROM model_families WHERE id = 'qwen3.8' AND vendor = 'Alibaba'`,
		"679 local credential unique index": `
			SELECT count(*) = 1 FROM pg_indexes
			WHERE schemaname = 'public' AND indexname = 'uq_credentials_local_placeholder_per_provider'`,
		"680 current-month view": `SELECT to_regclass('public.request_logs_with_current_month') IS NOT NULL`,
		"681 fingerprint index is 8-part (no error_message)": `
			SELECT position('error_message' in pg_get_indexdef(
				'public.idx_provider_error_details_tenant_cred_fingerprint'::regclass)) = 0`,
	} {
		if got := psqlScalar(t, dsn, query); got != "t" {
			t.Errorf("fresh installer 66x-68x sync %s check failed: got %q", name, got)
		}
	}

	// 2026-10-01 802 form adjudication (docs/audit/2026-10-01-802-form-adjudication.md):
	// a fresh install must land the 733+802 chain with the session_turn_details
	// family in the recursive partitioned-index form. Health criteria are
	// pg_index.indisvalid plus the pg_inherits attach count ONLY —
	// pg_indexes.indexdef renders partitioned parent indexes as "ON ONLY"
	// regardless of how they were created (adjudication §2.4), so indexdef
	// text is only meaningful for the partial predicate, never the form.
	for name, query := range map[string]string{
		"details parent is partitioned": `
			SELECT relkind = 'p' FROM pg_class
			WHERE oid = 'public.session_turn_details'::regclass`,
		// ★ 2026-10-21（R74 轮）改瞄：从「分区数 = 2」改成
		//   「**当月 + 次月分区按名字存在**」。
		//   ⇒ 实测（门自己建的库）：分区是 `session_turn_details_2026_10`（当月）
		//     + `session_turn_details_2026_11`（次月）+ `session_turn_details_default`
		//     = **3 个** ⇒ 「= 2」的绝对数 pin 今天就红。
		//   ⇒ 这条断言的**本意**是「当月与次月分区已预建」（否则写路径 23514），
		//     而「一共几个分区」是**会随 default 分区加入而变**的量
		//     ⇒ 改成按名字断言当月/次月 ⇒ 既守住本意，又不腐烂。
		"details partitions pre-created (current + next month)": `
			SELECT count(*) = 2
			FROM pg_inherits i
			JOIN pg_class c ON c.oid = i.inhrelid
			WHERE i.inhparent = 'public.session_turn_details'::regclass
			  AND c.relname IN (
			    'session_turn_details_' || to_char(current_date, 'YYYY_MM'),
			    'session_turn_details_' || to_char((current_date + interval '1 month')::date, 'YYYY_MM')
			  )`,
		"802 parent index valid": `
			SELECT i.indisvalid FROM pg_index i
			WHERE i.indexrelid = 'public.idx_session_turn_details_tenant_gw_task_id'::regclass`,
		"802 index attached on every partition": `
			SELECT
			  (SELECT count(*) FROM pg_inherits
			   WHERE inhparent = 'public.session_turn_details'::regclass)
			= (SELECT count(*) FROM pg_inherits
			   WHERE inhparent = 'public.idx_session_turn_details_tenant_gw_task_id'::regclass)`,
		"802 leaf indexes valid": `
			SELECT count(*) = 0 FROM pg_index i
			JOIN pg_inherits inh ON inh.inhrelid = i.indexrelid
			WHERE inh.inhparent = 'public.idx_session_turn_details_tenant_gw_task_id'::regclass
			  AND NOT i.indisvalid`,
		"802 hot-side index exists": `
			SELECT to_regclass('public.idx_session_turn_details_hot_tenant_gw_task_id') IS NOT NULL`,
		"802 index keeps its partial predicate": `
			SELECT position('gw_task_id IS NOT NULL' in pg_get_indexdef(
				'public.idx_session_turn_details_tenant_gw_task_id'::regclass)) > 0`,
		"details ensure/promote functions exist": `
			SELECT to_regprocedure('public.ensure_session_turn_details_partition(date)') IS NOT NULL
			   AND to_regprocedure('public.promote_session_turn_details_hot_to_partition(interval,integer)') IS NOT NULL`,
		"801 lossless drain semantics present (final promote form)": `
			SELECT position('v_deleted' in pg_get_functiondef(
				'public.promote_session_turn_details_hot_to_partition(interval,integer)'::regprocedure)) > 0
			   AND position('v_inserted' in pg_get_functiondef(
				'public.promote_session_turn_details_hot_to_partition(interval,integer)'::regprocedure)) > 0`,
		"details ON CONFLICT targets exist (parent + hot)": `
			SELECT count(*) = 2 FROM pg_constraint
			WHERE conname IN ('session_turn_details_request_id_key',
			                  'session_turn_details_hot_request_key')`,
		"details parent/hot column parity": `
			WITH parent_columns AS (
				SELECT column_name, data_type, is_nullable, ordinal_position
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'session_turn_details'
			), hot_columns AS (
				SELECT column_name, data_type, is_nullable, ordinal_position
				FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'session_turn_details_hot'
			)
			SELECT NOT EXISTS (
				(SELECT * FROM parent_columns EXCEPT SELECT * FROM hot_columns)
				UNION ALL
				(SELECT * FROM hot_columns EXCEPT SELECT * FROM parent_columns)
			)`,
		"details promote callable on fresh install": `
			SELECT promote_session_turn_details_hot_to_partition('8 hours'::interval, 1000) = 0`,
		// ★ 2026-10-21（R74 轮）再订正：上一条订正（2026-10-20）的**前提本身**
		//   已被 **迁移 890** 推翻 ⇒ 它写下的名字今天也不存在了。
		//   - 860 把 request_logs 族改名成 `_` 前缀的「待删标志」形态
		//     （`_request_logs` / `_request_logs_hot` / …）—— 那一刻是对的；
		//   - 但 **890_drop_request_logs_v1_family.sql**（链内 index 282）把
		//     **整个 v1 族真删了**（DROP 名单里含 `request_logs` 与 `_request_logs`），
		//     连同 `request_logs_bodies_with_current_month` 等 4 个 v1 视图。
		//   ⇒ 今天的形态是：**v1 族彻底不存在，canonical 视图改读会话族**。
		//   ⇒ R74 轮实测：这道门**真跑必然红**，
		//     报错原文 = `ERROR: relation "public._request_logs" does not exist`。
		//   ⇒ 本条断言的**本意**是「details join 的视图链可 plan（不是 42P01）」，
		//     而它的读法引用了一个**已退役对象** ⇒ 改成读**退役后的 canonical 视图**
		//     （`request_logs_with_current_month`，链内 680 建、884 改读会话族）。
		//   ⇒ 口径不变：只是把「读哪张表」跟到今天真正在的那张。
		"734 details join keeps the request_logs view chain planable": `
			SELECT count(*) >= 0 FROM public.request_logs_with_current_month`,
	} {
		if got := psqlScalar(t, dsn, query); got != "t" {
			t.Errorf("fresh installer 733+802 chain %s check failed: got %q", name, got)
		}
	}
}

func applyInstallerSQL(t *testing.T, dsn, path string) {
	t.Helper()
	// Mirror dbinit.Runner.applySQL's transaction routing: files carrying the
	// dbinit:no-transaction marker (e.g. 718's DROP INDEX CONCURRENTLY) must
	// NOT be wrapped in --single-transaction, or a fresh install fails exactly
	// where the marker's comment says it would.
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	args := []string{"-v", "ON_ERROR_STOP=1"}
	if !dbinit.RequiresNoTransaction(content) {
		args = append(args, "--single-transaction")
	}
	args = append(args, "--dbname", dsn, "-f", path)
	cmd := exec.Command("psql", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply %s: %v\n%s", filepath.Base(path), err, out)
	}
}

// psqlScalar returns the single value produced by `query`.
//
// stdout only: citus emits background-worker warnings on stderr
// ("could not start maintenance background worker") whenever a connection
// touches a database that has the citus extension, and CombinedOutput would fold
// those into the scalar. The caller's equality checks then fail on a database
// that is in fact in the expected state -- the empty-database precondition check
// reported "found WARNING: ... 0 non-extension public relations" for a genuinely
// empty database. A query failure is still fatal, but it is reported with the
// stderr text so the reason is not lost.
func psqlScalar(t *testing.T, dsn, query string) string {
	t.Helper()
	cmd := exec.Command("psql", "-At", "-v", "ON_ERROR_STOP=1", "--dbname", dsn, "-c", query)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("query installer integration database: %v\n%s", err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}
