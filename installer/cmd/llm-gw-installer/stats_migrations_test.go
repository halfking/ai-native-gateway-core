package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/installer/internal/dbinit"
)

// TestStatsStartupMigrationsMatchCanonicalSources 自 24h 审计第二十八轮
// （2026-10-02，遗留#2 收口）起反转为 embeddedSQLFiles 全清单驱动：旧版手工
// map 覆盖 174/200，25 条注册迁移（含生产阻断修复 534/612）无持续 cmp 守卫，
// embed 副本漂移无人发现。新守卫遍历 embeddedSQLFiles 的 startup/*.sql
// （.down.sql 除外——installer 从不回滚，down 镜像由下方的 DownMigrations
// 守卫管），逐字节对照 canonical。豁免两条且各有专属守卫：
//   - 600_outbound_body_to_bodies_hot.sql：canonical 位于 up/ 子目录（deploy 线收编）；
//   - session_turns_hot_bootstrap.sql：installer-only 终态资产，无 canonical 副本，
//     由 TestSessionTurnsHotBootstrapIsFinalStateAsset 的 marker 守卫。
func TestStatsStartupMigrationsMatchCanonicalSources(t *testing.T) {
	t.Helper()

	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	checked := 0
	for key, embedded := range embeddedSQLFiles {
		if !strings.HasPrefix(key, "startup/") {
			// 00-prereqs / 01-schema / 02-seed 的 canonical 在 sql/schema/，
			// 由 verify-stats-schema-mirror.sh 守护，不在本测试范围。
			continue
		}
		name := strings.TrimPrefix(key, "startup/")
		if strings.HasSuffix(name, ".down.sql") || name == "session_turns_hot_bootstrap.sql" {
			continue
		}
		rel := name
		if name == "600_outbound_body_to_bodies_hot.sql" {
			rel = filepath.Join("up", name)
		}
		canonical, err := os.ReadFile(filepath.Join(canonicalDir, rel))
		if err != nil {
			t.Fatalf("read canonical migration %s: %v", name, err)
		}
		if !bytes.Equal(embedded, canonical) {
			t.Fatalf("embedded migration %s differs from canonical source", name)
		}
		checked++
	}
	// TSV 注册 200 条 = 199 条 canonical 镜像 + 1 条 installer-only bootstrap
	// （session_turns_hot_bootstrap.sql，上面已豁免）。
	if checked < 199 {
		t.Fatalf("parity sweep covered only %d startup migrations; embeddedSQLFiles lost entries", checked)
	}
}

// TestDownMigrationsMirrorCanonicalSources：.down.sql 不入 StartupFiles/TSV
// （installer 从不应用回滚，embeddata 的 down 是操作员参考件），曾因此漏镜像
// 且无人发现——612 的 down 只有 canonical 一侧、文件头错号写成 611（24h
// 审计第二十八轮修复）；649 的 down 曾在 canonical 侧补 DROP VIEW 而副本
// 未跟（同轮守卫首跑即抓到）。守卫范围如实：
//   - embeddata 已有的每个 down 必须与 canonical 字节一致（漂移即红）；
//   - 数量只增不减（≥77 棘轮；810/811/812 三件随第二十九轮入组）。
//
// canonical 侧另有 245 个 down 按历史选择性约定未镜像（322−77，含 802-804/807），
// 不在本守卫强制范围；新迁移的 down 应循 808/809/730/612 先例双侧镜像。
func TestDownMigrationsMirrorCanonicalSources(t *testing.T) {
	t.Helper()

	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	entries, err := os.ReadDir(filepath.Join("embeddata", "startup"))
	if err != nil {
		t.Fatalf("read embeddata/startup: %v", err)
	}
	mirrored := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".down.sql") {
			continue
		}
		embedded, err := os.ReadFile(filepath.Join("embeddata", "startup", name))
		if err != nil {
			t.Fatalf("read embedded down migration %s: %v", name, err)
		}
		canonical, err := os.ReadFile(filepath.Join(canonicalDir, name))
		if err != nil {
			t.Fatalf("embedded down migration %s has no canonical source: %v", name, err)
		}
		if !bytes.Equal(embedded, canonical) {
			t.Fatalf("embedded down migration %s differs from canonical source", name)
		}
		mirrored++
	}
	if mirrored < 77 {
		t.Fatalf("only %d down migrations mirrored in embeddata; the mirror set must not shrink", mirrored)
	}
}

func TestSessionTurnsHotBootstrapIsFinalStateAsset(t *testing.T) {
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS public.session_turns_hot",
		"digest JSONB",
		"CREATE VIEW public.session_turns_with_current_month",
		"security_invoker = true",
		"CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition",
		"installer session-turns bootstrap requires nullable JSONB digest",
	} {
		if !bytes.Contains(sessionTurnsHotBootstrap, []byte(marker)) {
			t.Fatalf("session turns hot bootstrap is missing final-state marker %q", marker)
		}
	}
}

func TestStatsStartupMigrationsAreWrittenToInstallerDirectories(t *testing.T) {
	expected := []string{
		"511_state_transitions_table.sql",
		"515_state_transitions_seq_unique.sql",
		"521_repair_state_transitions_tenant.sql",
		"530_request_journey_contract.sql",
		"531_request_journey_tenant_uniqueness.sql",
		"536_stats_analytics_foundation.sql",
		"537_usage_facts.sql",
		"539_stats_reconciliation_tenant.sql",
		"540_stats_event_inbox_consumer.sql",
		"544_stats_adjustments_alignment.sql",
		"545_stats_reconciliation_phantom_resolution.sql",
		"546_stats_reconciliation_diffs_unique.sql",
		"547_session_project_attribution.sql",
		"548_stats_reconciliation_diffs_identity.sql",
		"552_request_journey_durable_outbox.sql",
		"553_approval_resume_claim.sql",
		"554_goal_runs.sql",
		"555_goal_run_actions_lease_fencing.sql",
		"560_session_summaries_tenant_uniqueness.sql",
		"561_request_logs_view_origin_actor.sql",
		"562_fix_request_logs_bodies_partitions_heap.sql",
		"563_session_summary_trigger_on_hot.sql",
		"564_session_summary_backfill_safe.sql",
		"565_cost_usd_pricing_backfill.sql",
		"566_credentials_governor_revision.sql",
		"567_session_analysis_metadata.sql",
		"568_credential_priority_flag.sql",
		"569_candidate_binding_scope_revision_canonical.sql",
		"570_model_offers_insert_priority_passthrough.sql",
		"571_candidate_binding_scope_revision_canonical_priority_hash.sql",
		"600_outbound_body_to_bodies_hot.sql",
		"601_request_logs_bodies_drop_metadata.sql",
		"602_request_logs_promote_atomic.sql",
		"618_request_journey_snapshot_receipts.sql",
		"649_routing_analytics_probe_filter.sql",
		"650_auto_route_selection_treatment_attribution.sql",
		"session_turns_hot_bootstrap.sql",
	}

	seen := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		if _, ok := seen[name]; ok {
			t.Fatalf("duplicate installer migration %s", name)
		}
		seen[name] = struct{}{}
	}
	order := []string{
		"552_request_journey_durable_outbox.sql",
		"553_approval_resume_claim.sql",
		"600_outbound_body_to_bodies_hot.sql",
		"601_request_logs_bodies_drop_metadata.sql",
		"602_request_logs_promote_atomic.sql",
		"618_request_journey_snapshot_receipts.sql",
	}
	positions := make(map[string]int, len(expected))
	for i, name := range expected {
		positions[name] = i
	}
	for i := 1; i < len(order); i++ {
		if positions[order[i-1]] >= positions[order[i]] {
			t.Fatalf("installer migration order is invalid: %s before %s", order[i-1], order[i])
		}
	}

	tmp := t.TempDir()
	if err := copySQLBackup(tmp); err != nil {
		t.Fatalf("copy SQL backup: %v", err)
	}

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up SQL dir: %v", err)
	}
	defer cleanup()

	for _, name := range expected {
		backupPath := filepath.Join(tmp, "db", "init", "startup", name)
		setupPath := filepath.Join(sqlDir, "startup", name)
		backup, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("read backup migration %s: %v", name, err)
		}
		setup, err := os.ReadFile(setupPath)
		if err != nil {
			t.Fatalf("read setup migration %s: %v", name, err)
		}
		if !bytes.Equal(backup, setup) {
			t.Fatalf("installer migration %s differs between backup and setup directories", name)
		}
	}
}

// TestStartupFilesAreAllEmbedded guards against two drift directions:
//
//  1. The 2026-08-31 audit gap: dbinit.Runner.StartupFiles referenced
//     migrations (614/615/619 and later 620-626) that were never added to the
//     embed maps, so a fresh install would fail in applySQL with "file not
//     found". Checked as StartupFiles ⊆ setupSQLDir output.
//  2. The 2026-09-07 audit gap: 632_audit_attachments_filesystem_cleanup.sql
//     sat in embeddata/startup without a go:embed var or StartupFiles entry,
//     so fresh installs silently lacked the table its runtime writer expects.
//     Checked as embeddata/startup ReadDir ⊆ StartupFiles (*.down.sql exempt —
//     the installer never applies rollbacks).
func TestStartupFilesAreAllEmbedded(t *testing.T) {
	t.Helper()

	sqlDir, cleanup, err := setupSQLDir()
	if err != nil {
		t.Fatalf("set up SQL dir: %v", err)
	}
	defer cleanup()

	runner := dbinit.NewRunner("", "", "", "")
	if len(runner.StartupFiles) == 0 {
		t.Fatal("dbinit.Runner.StartupFiles is empty")
	}
	registered := make(map[string]struct{}, len(runner.StartupFiles))
	for _, name := range runner.StartupFiles {
		registered[name] = struct{}{}
		path := filepath.Join(sqlDir, "startup", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("StartupFiles entry %q is not provided by setupSQLDir — add the file to installer/cmd/llm-gw-installer/embeddata/startup/, the go:embed vars, and the embeddedSQLFiles map in main.go: %v", name, err)
		}
	}

	entries, err := os.ReadDir(filepath.Join("embeddata", "startup"))
	if err != nil {
		t.Fatalf("read embeddata/startup: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		if _, ok := registered[name]; !ok {
			t.Errorf("embeddata/startup file %q is not registered in dbinit.Runner.StartupFiles — wire it into runner.go StartupFiles plus the go:embed var and embeddedSQLFiles entry in main.go, or delete the stray copy", name)
		}
	}
}

// psqlConcurrencyRequired lists canonical startup migrations that may NOT be
// synced into the installer five points: they use CREATE INDEX CONCURRENTLY
// (727/728 via \gexec), which PostgreSQL refuses inside a transaction block —
// and the installer's dbinit runner applies every file through
// `psql --single-transaction` (installer/internal/dbinit/runner.go). Copying
// them in would abort every FRESH INSTALL at that migration (R49 audit,
// 2026-09-20: the naive five-point sync would have been a P0). These files
// ship exclusively through the revision-sequence channel
// (scripts/apply-db-revision-sequence.sh, psql -f without a transaction
// wrapper) against EXISTING databases; fresh installs skip the indexes, which
// are performance-only — a non-concurrent variant may be added later if a
// fresh install ever needs them on day one.
//
// 728 (2026-09-21, R50 follow-up): same \gexec + CONCURRENTLY shape as 727;
// sql/migrations/startup/728_sql_audit_request_logs_credential_model_index.sql
// header comment self-certifies "实现约束与 727 相同".
//
// 729 (2026-09-21, 252 部署验证轮): same three-phase \gexec + CONCURRENTLY
// shape as 727/728 (header comment "实现约束与 727/728 相同"); ships
// exclusively through the revision-sequence channel like its predecessors.
//
// 744 (2026-09-24, 252 SQL 日志审计第六轮): same shape (outbox done-trim
// partial index + session_turns digest-NULL three-phase partial indexes);
// unlike 727/728/729 it also carries a Go ensure mirror
// (db.ensureSqlAuditPartialIndexes) so existing databases converge at boot,
// but the canonical file still must not enter the single-transaction
// installer channel.
//
// 749 (2026-09-26, R68 usage_facts 分区轮): occurred_at 索引三段式
// CREATE INDEX CONCURRENTLY；与 727/728/729 同类，只走 revision-sequence
// 通道（R71 审计补登记豁免——749 落地时漏更本表，守卫自落地起恒红）。
var psqlConcurrencyRequired = map[string]string{
	"727_sql_audit_slow_query_indexes.sql":                  "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"728_sql_audit_request_logs_credential_model_index.sql": "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"729_sql_audit_session_turns_credential_ts_index.sql":   "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"744_sql_audit_partial_indexes.sql":                     "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
	"749_usage_facts_occurred_at_index.sql":                 "CREATE INDEX CONCURRENTLY (\\gexec) cannot run inside the installer's psql --single-transaction",
}

// 755 removes a legacy function in upgraded databases. Its SQL contains an
// explicit transaction, so running it under the installer's single transaction
// would commit the surrounding install early. Applying it on an upgrade also
// requires an operator check for external jobs (see its migration header).
// Fresh installs have no legacy function to remove.
var operatorGatedCleanup = map[string]string{
	"755_drop_dead_cleanup_expired_session_turn_logs.sql": "legacy cleanup with explicit BEGIN/COMMIT and external-job check",
}

// 810/811/812 (2026-10-02, R20 252 SQL-log audit round) are one-shot repairs
// for upgraded databases: toastless-heap empty partitions (810), UTC-midnight
// bound pollution (811), columnar probe-run partitions (812). They ship via
// the revision-sequence channel only (files array, registered 2026-10-02).
//
// 豁免的真实理由（第三十轮订正——原注释两处失实）：
//   1. 它们是存量缺陷的一次性修复 + 755 同型的操作员门性质，installer 的
//      fresh-install 链没有"升级库存量"可修，注册进 StartupFiles 只会
//      让全新装空跑一遍 DETACH/重建（810/812）或边界重写（811）。
//   2. "显式 BEGIN/COMMIT 不能进 --single-transaction" 不是机制障碍——
//      psql 对内层 BEGIN/COMMIT 仅产生 WARNING 且 rc=0（全链 129 个
//      embedded 文件带显式事务、一直这么骑）。真正的问题是嵌套事务下
//      DETACH/ATTACH 回退与 SET LOCAL 的语义不再成立。
//      （另注：fresh 基线并非完全无遗产——sql/schema/01-schema.sql 仍烤有
//      473 型 08:00 边界与列存 probe-run 分区，见第三十轮登记项；这些
//      遗产全部在过去时间窗内、有 808 default 分区兜底、且 810-812 在
//      序列通道首跑即自愈。）
var sequenceChannelRepairs = map[string]string{
	"810_heap_partitions_toastless_heal.sql":            "one-shot legacy repair (DETACH/ATTACH rollback needs its own transaction); fresh installs have no toastless-heap legacy",
	"811_partition_bounds_shanghai_midnight_repair.sql": "one-shot legacy repair (SET LOCAL TIME ZONE + bound rebuild); fresh installs self-heal via sequence channel",
	"812_model_probe_runs_partitions_heap.sql":          "one-shot legacy repair (to_regclass guard + DETACH/ATTACH); fresh installs self-heal via sequence channel",
}

// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered (R34, 2026-09-17
// audit) closes the drift direction no test covered: a canonical migration
// that never reached the installer (704/705/709/710 drifted out — R30
// leftover #8; 714 landed with no installer copy and no Go ensure mirror).
// From 704 onward every canonical up-migration must be registered in
// StartupFiles; anything below 704 is legacy history (pre-703 shapes are
// superseded or Go-ensure-backed) and stays exempt.
func TestCanonicalStartupMigrationsAtOrAbove704AreRegistered(t *testing.T) {
	t.Helper()

	canonicalDir := filepath.Join("..", "..", "..", "sql", "migrations", "startup")
	entries, err := os.ReadDir(canonicalDir)
	if err != nil {
		t.Fatalf("read canonical startup dir: %v", err)
	}

	runner := dbinit.NewRunner("", "", "", "")
	registered := make(map[string]struct{}, len(runner.StartupFiles))
	for _, name := range runner.StartupFiles {
		registered[name] = struct{}{}
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".down.sql") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix := name
		if i := strings.Index(name, "_"); i > 0 {
			prefix = name[:i]
		}
		num, err := strconv.Atoi(prefix)
		if err != nil {
			continue // non-numeric asset (e.g. dated repair scripts)
		}
		if num < 704 {
			continue
		}
		if reason, exempt := psqlConcurrencyRequired[name]; exempt {
			// Deliberate channel split, not drift — but keep it visible so the
			// exemption is re-evaluated whenever the file set changes.
			t.Logf("canonical startup migration %q intentionally not in installer: %s", name, reason)
			continue
		}
		if reason, exempt := operatorGatedCleanup[name]; exempt {
			t.Logf("canonical startup migration %q intentionally operator-gated: %s", name, reason)
			continue
		}
		if reason, exempt := sequenceChannelRepairs[name]; exempt {
			t.Logf("canonical startup migration %q intentionally sequence-channel-only: %s", name, reason)
			continue
		}
		if _, ok := registered[name]; !ok {
			t.Errorf("canonical startup migration %q (>=704) is not registered in dbinit.Runner.StartupFiles — run the five-point sync (embeddata copy, go:embed var + embeddedSQLFiles map in main.go, StartupFiles entry, parity map here), see llm-gateway-installer-migration-3way-sync", name)
		}
	}
}

// TestDurableFamilyPrerequisitesRegistered (R42, 2026-09-18 audit) pins the
// fresh-install ordering invariant the ≥704 floor above cannot see: 657 and
// 722 unconditionally ALTER/reference durable_llm_tasks, whose only creators
// are 516 (tasks/events) and 520 (settlement intents, FK → tasks). Before
// R42 the installer chain shipped 657/722 without 516/520, so every fresh
// install aborted with 42P01 at 657. Any future migration that touches the
// durable family must keep its base-table creators ahead of it in
// StartupFiles.
func TestDurableFamilyPrerequisitesRegistered(t *testing.T) {
	t.Helper()

	runner := dbinit.NewRunner("", "", "", "")
	position := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		position[name] = i
	}

	for _, base := range []string{
		"516_durable_llm_tasks.sql",
		"520_durable_task_settlement_intents.sql",
	} {
		if _, ok := position[base]; !ok {
			t.Errorf("%s is not registered in dbinit.Runner.StartupFiles — 657/722 ALTER/reference durable_llm_tasks and a fresh install cannot succeed without it", base)
		}
	}
	for _, dependent := range []string{
		"657_durable_llm_tasks_decision_history.sql",
		"722_durable_family_schema_convergence.sql",
	} {
		pos, ok := position[dependent]
		if !ok {
			continue // covered by the ≥704 registration test
		}
		// R43 (2026-09-18): pin ordering against 520 too — its FK references
		// durable_llm_tasks, so a reorder that slides dependents between 516
		// and 520 (or past 520) must fail here, not at install time.
		if base516 := position["516_durable_llm_tasks.sql"]; ok && pos < base516 {
			t.Errorf("%s (pos %d) must come after 516_durable_llm_tasks.sql (pos %d) in StartupFiles", dependent, pos, base516)
		}
		if base520 := position["520_durable_task_settlement_intents.sql"]; ok && pos < base520 {
			t.Errorf("%s (pos %d) must come after 520_durable_task_settlement_intents.sql (pos %d) in StartupFiles", dependent, pos, base520)
		}
	}
}

// TestHandoffFamilyPrerequisitesRegistered (R32, 2026-10-02; 12h 审计 P2-E)：
// handoff 族的 fresh-install 顺序不变量。527 是 517 的**补完**迁移——517 只建
// 半成品表（CHECK 缺 accounting_confirmed 等值、缺 goal_state/restore_* 列），
// 而 confirmation_pg.go 的正常写路径直写这些列/值；527<704 不受
// TestCanonicalStartupMigrationsAtOrAbove704AreRegistered 的注册底线覆盖，
// 「同时删两文件+两处注册」或「把 527 挪到 517 之前」零门红，直到某个
// fresh-install 在 42703 上炸掉（runner.go:167-180 注释记载的正是这段历史）。
// durable 族的 TestDurableFamilyPrerequisitesRegistered 是同款守卫的先例。
func TestHandoffFamilyPrerequisitesRegistered(t *testing.T) {
	t.Helper()

	runner := dbinit.NewRunner("", "", "", "")
	position := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		position[name] = i
	}

	base := "517_handoff_pending_confirmations.sql"
	completer := "527_handoff_durable_goal_state.sql"
	basePos, ok := position[base]
	if !ok {
		t.Fatalf("%s is not registered in dbinit.Runner.StartupFiles — 527 completes its half-built table and a fresh install cannot succeed without it", base)
	}
	compPos, ok := position[completer]
	if !ok {
		t.Fatalf("%s is not registered in dbinit.Runner.StartupFiles — 517 alone hands dependents a table whose CHECK/goal_state contract it does not have (SQLSTATE 42703)", completer)
	}
	if compPos < basePos {
		t.Errorf("%s (pos %d) must come after %s (pos %d) in StartupFiles", completer, compPos, base, basePos)
	}
}
