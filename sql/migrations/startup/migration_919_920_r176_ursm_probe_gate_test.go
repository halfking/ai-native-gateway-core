package startup

// migration_917_918_r176_ursm_probe_gate_test.go —— 迁移 919/918 的静态契约门（R176 轮）。
//
// # 背景
//
// 252 SQL 日志 26.4h 窗（2026-10-29 审计）两大主修复：
//
//   · 917：P11 第二期 —— ursm_snapshot_slot_daily 补 active_minutes 列，填充函数
//     升级为同步产出它（签名不变）。巡检 ursm-snapshot-health.sh 的 base_active
//     基线由此走汇总表（独立臂 EXPLAIN ANALYZE 158.1s ⇒ 汇总表路毫秒级；
//     主判据 WITH win 族 22 次/5,339.5s/26.4h）。
//   · 918：node_probe_runs 补 (credential_id, completed_at) INCLUDE (...) 覆盖索引，
//     承接 INV-4「双模型探测成功门」（bg/probe_policy.go credentialTwoProbeSuccessGateSQL）
//     的 24h 关联子查询。252 A/B 实测：旧路 18,885 buffers / 3.8 万行全史扫描
//     ⇒ 新路 321 buffers（58×）；慢查 325 次 + 取消 224 次/26.4h 应随之塌缩。
//
// # 这道门钉什么
//
//   919-A 函数体必须同步产出 active_minutes（漏了 ⇒ 巡检探测失败 ⇒ 永远走源表慢路）；
//   919-B ON CONFLICT 必须同步更新 active_minutes（漏了 ⇒ 迟到行留下「行数新、
//         分钟数旧」的半截基线 ⇒ SPARSE/SLOW 分型失真）；
//   919-C down 必须先还原函数再删列（顺序反了 ⇒ 刷新作业在删除后仍写该列 ⇒ 报错）；
//   919-D 巡检脚本必须有 V2 变体 + active 探针 + 三态选择（V2 缺失 ⇒ 917 白落；
//         探针缺失 ⇒ 列不存在时整条主判据 SQL 炸 ⇒ 巡检失效）；
//   919-E 巡检 V2 与 892 的 slot 表达式必须逐字一致（分叉 ⇒ 汇总表行的 slot
//         与查询算出的 slot 对不上 ⇒ 判定输入悄悄变样）；
//   920-A 索引键序与 INCLUDE 列必须钉死（前导 credential_id 是门的关联键；
//         completed_at 第二键是时间谓词；INCLUDE 三列是 DISTINCT ON 的输出列
//         ⇒ Index Only Scan 的前提）；
//   920-B 必须有承重查询（probe_policy.go 的门在位；888 立的规矩）；
//   920-C 918 含 CONCURRENTLY ⇒ 首行 dbinit:no-transaction + 会话级锁守卫
//         （914/916 实测：SET LOCAL 在本通道完全失效）；
//   920-D 登记面齐全（runner.go / apply-db-revision-sequence.sh / db-changelog.md
//         含当前文件 sha256 / concurrency gate 计数）。
//
// ★ 全部静态（不需要 DSN ⇒ CI 可跑）；真库行为由 252 预应用记录在审计档。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

const (
	mig919R176File     = "919_ursm_slot_daily_active_minutes.sql"
	mig919R176DownFile = "919_ursm_slot_daily_active_minutes.down.sql"
	mig920R176File     = "920_node_probe_runs_cred_completed_idx.sql"
	mig920R176DownFile = "920_node_probe_runs_cred_completed_idx.down.sql"

	healthScriptR176 = "../../../scripts/252-monitor/ursm-snapshot-health.sh"
	probePolicyFileR176 = "../../../bg/probe_policy.go"
)

func readRepoR176(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// ---- 919-A/B：函数体同步产出并同步更新 active_minutes -----------------------

func TestMigration919FunctionProducesActiveMinutes(t *testing.T) {
	body := stripSQLComments(readRepoR176(t, mig919R176File))

	for _, frag := range []string{
		"ADD COLUMN IF NOT EXISTS active_minutes integer",
		"active_minutes = EXCLUDED.active_minutes",
		"count(DISTINCT date_trunc(''minute'', s.snapshot_ts))",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("917 函数体缺 %q\n★ 漏了它 ⇒ 巡检的 active 探针永远探不到值 ⇒ 永远回退源表慢路（917 白落）", frag)
		}
	}
	// minute 粒度恒 1（与巡检 G_ACTIVE 对 minute 的 `1::bigint` 同一语义）。
	if !strings.Contains(body, "v_active := '1'") {
		t.Errorf("917 函数体缺 minute 粒度的 active 恒 1 分支 ⇒ minute 汇总行的 active_minutes 全 NULL ⇒ 巡检 minute 粒度探测失败")
	}
	// 签名不得变（调用方零改动的承诺）。
	if !strings.Contains(body, "p_days_back integer DEFAULT 1") ||
		!strings.Contains(body, "p_granularity text DEFAULT 'hour'") {
		t.Errorf("917 函数签名漂移 ⇒ 调用方（slot-refresh.sh / 手工回填）全部炸")
	}
}

// ---- 919-C：down 的顺序（先还原函数，再删列）--------------------------------

func TestMigration919DownRestoresFunctionBeforeDroppingColumn(t *testing.T) {
	down := stripSQLComments(readRepoR176(t, mig919R176DownFile))
	fnIdx := strings.Index(down, "CREATE OR REPLACE FUNCTION public.refresh_ursm_snapshot_slot_daily")
	colIdx := strings.Index(down, "DROP COLUMN IF EXISTS active_minutes")
	if fnIdx < 0 || colIdx < 0 {
		t.Fatalf("917 down 缺关键语句（fn=%d col=%d）", fnIdx, colIdx)
	}
	if fnIdx > colIdx {
		t.Errorf("917 down 顺序错了：必须先还原 892 函数体（不再写该列）再 DROP COLUMN\n★ 顺序反了 ⇒ 删列后旧函数仍在 ⇒ 下一次刷新 INSERT 引用不存在的列 ⇒ 42P01")
	}
	if strings.Contains(down, "active_minutes") && strings.Contains(down, "EXCLUDED.active_minutes") {
		t.Errorf("917 down 还原的函数体不得再产出 active_minutes（那会让删列后的刷新再次报错）")
	}
}

// ---- 919-D：巡检脚本的三件套 ------------------------------------------------

func TestMigration919PatrolHasV2VariantAndProbe(t *testing.T) {
	body := readRepoR176(t, healthScriptR176)

	// ① V2 变体在位，且过去天读汇总表。
	for _, frag := range []string{
		"BUCKETS_SQL_DAILY_V2=\"",
		"FROM public.ursm_snapshot_slot_daily d",
		"d.active_minutes AS a",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("巡检脚本缺 V2 变体片段 %q ⇒ 917 的 active_minutes 没有读点 ⇒ 白落", frag)
		}
	}
	// ② active 探针在位（三失败形态：列缺/NULL/无行）。
	for _, frag := range []string{
		"active_probe=$(de ",
		"AND active_minutes IS NULL",
		"ACTIVE_SRC=\"daily_v2\"",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("巡检脚本缺 active 探针片段 %q ⇒ 探测链断裂（列不存在时整条主判据 SQL 会 42703 炸 ⇒ 巡检 exit 3 风暴）", frag)
		}
	}
	// ③ 三态选择：V2 只在 daily 之上叠加（base_buckets 探针是前置条件）。
	selIdx := strings.Index(body, "if [ \"$BASE_SRC\" = \"daily\" ]; then")
	v2Idx := strings.Index(body, "BUCKETS_SQL=\"$BUCKETS_SQL_DAILY_V2\"")
	if selIdx < 0 || v2Idx < 0 || v2Idx < selIdx {
		t.Errorf("巡检脚本的三态选择缺失或次序不对 ⇒ V2 可能在 base_buckets 未覆盖时被选中（那半段也要读汇总表）")
	}
	// ④ 探测失败必须回退到上一代 DAILY（不是 SOURCE）—— SOURCE 连 base_buckets 也扫源表。
	if !strings.Contains(body, "BUCKETS_SQL=\"$BUCKETS_SQL_DAILY\"") {
		t.Errorf("巡检脚本缺 DAILY 回退臂 ⇒ active 探针失败时会错退到全源表路（丢掉 892 的收益）")
	}
}

// ---- 919-E：V2 与 892 的 slot/bucket 表达式逐字一致 --------------------------

func TestMigration919V2SlotExpressionsMatchMigration892(t *testing.T) {
	mig892 := stripSQLComments(readRepoR176(t, "892_ursm_snapshot_slot_daily.sql"))
	mig917 := stripSQLComments(readRepoR176(t, mig919R176File))
	patrol := readRepoR176(t, healthScriptR176)

	// 892 与 917 的函数体里，hour 粒度的 slot 表达式必须逐字一致。
	const slotExpr = "'extract(hour from date_trunc(''hour'', snapshot_ts))::int'"
	if strings.Count(mig892, slotExpr) < 1 || strings.Count(mig917, slotExpr) < 1 {
		t.Errorf("892/917 的 hour slot 表达式分叉 ⇒ 汇总表行的 slot 与巡检修查询算的 slot 对不上 ⇒ 判定输入悄悄变样")
	}
	// 巡检 G_SLOT_TS（hour 粒度）与函数体的裸形态必须同族（引号风格不同：shell 单引号）。
	const patrolSlot = "G_SLOT_TS=\"extract(hour from date_trunc('hour', snapshot_ts))::int\""
	if !strings.Contains(patrol, patrolSlot) {
		t.Errorf("巡检 G_SLOT_TS 表达式漂移（应含 %s）⇒ 汇总表 slot 与查询 slot 不一致", patrolSlot)
	}
	// V2 的 base_active 今天腿必须用可下推的范围谓词（R97 的改写，不是列上函数）。
	v2Start := strings.Index(patrol, "BUCKETS_SQL_DAILY_V2=\"")
	if v2Start >= 0 && !strings.Contains(patrol[v2Start:], "snapshot_ts >= (now() AT TIME ZONE current_setting('TimeZone'))::date::timestamptz") {
		t.Errorf("V2 的 base_active 今天腿没有用 R97 的范围谓词下推形态 ⇒ 8 个分区全扫（R97 轮实测过的整族退化）")
	}
}

// ---- 920-A：索引形态钉死 ----------------------------------------------------

func TestMigration920IndexShape(t *testing.T) {
	body := stripSQLComments(readRepoR176(t, mig920R176File))
	const want = "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_node_probe_runs_cred_completed\n    ON public.node_probe_runs (credential_id, completed_at)\n    INCLUDE (raw_model_name, started_at, success)"
	if !strings.Contains(body, want) {
		t.Errorf("918 索引形态漂移。\n★ 期望：%s\n★ 前导 credential_id=门的关联键；第二键 completed_at=时间谓词（Index Cond）；\n  INCLUDE 三列=DISTINCT ON 的输出列（Index Only Scan 的前提）——\n  任何一项变了，252 实测的 58× buffer 缩减就不成立。", want)
	}
}

// ---- 920-B：承重查询在位 ----------------------------------------------------

func TestMigration920HasBearingQuery(t *testing.T) {
	policy := readRepoR176(t, probePolicyFileR176)
	for _, frag := range []string{
		"func credentialTwoProbeSuccessGateSQL",
		"FROM node_probe_runs npr",
		"AND npr.completed_at > now() - interval '24 hours'",
	} {
		if !strings.Contains(policy, frag) {
			t.Errorf("INV-4 门的承重片段消失：%q ⇒ 918 成了没有查询承重的索引（877 先例：该删）", frag)
		}
	}
	// 门必须仍以 credential 前导等值关联（若改成 provider 维度，本索引不再承重）。
	if !strings.Contains(policy, "npr.credential_id = ` + credIDExpr + `") {
		t.Errorf("INV-4 门不再按 credential_id 关联 ⇒ 918 的前导键失去承重 ⇒ 需要重新评估这条索引")
	}
}

// ---- 920-C：通道与锁守卫 ----------------------------------------------------

func TestMigration920ChannelAndLocks(t *testing.T) {
	raw := readRepoR176(t, mig920R176File)
	lines := strings.Split(strings.TrimLeft(raw, "\n"), "\n")
	body := stripSQLComments(raw)

	if !strings.Contains(lines[0], "dbinit:no-transaction") {
		t.Errorf("918 首行必须有 dbinit:no-transaction（含 CREATE INDEX CONCURRENTLY ⇒ installer 默认 --single-transaction 会让它失败）")
	}
	if !strings.Contains(body, "SET lock_timeout = '2s'") {
		t.Error("918 必须设会话级 lock_timeout（本通道不在事务块内 ⇒ SET LOCAL 完全失效 ⇒ 914/916 实测）")
	}
	if strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Error("918 不得用 SET LOCAL lock_timeout（本通道完全失效）")
	}

	down := stripSQLComments(readRepoR176(t, mig920R176DownFile))
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_node_probe_runs_cred_completed") {
		t.Error("918 down 必须删除这条索引")
	}
	if strings.Contains(down, "DROP INDEX CONCURRENTLY") {
		t.Error("918 down 不得用 DROP INDEX CONCURRENTLY（down 通道默认 --single-transaction ⇒ 事务块内直接报错；916 down 同款取舍）")
	}
}

// ---- 920-D：登记面齐全 ------------------------------------------------------

func TestMigration919And918RegisteredEverywhere(t *testing.T) {
	runner := readRepoR176(t, "../../../installer/internal/dbinit/runner.go")
	seq := readRepoR176(t, "../../../scripts/apply-db-revision-sequence.sh")
	for _, mig := range []string{mig919R176File, mig920R176File} {
		if !strings.Contains(runner, mig) {
			t.Errorf("★ %s 必须登记进 installer 的 StartupFiles（否则全新安装缺该对象）", mig)
		}
		if !strings.Contains(seq, mig) {
			t.Errorf("★ %s 必须登记进 scripts/apply-db-revision-sequence.sh（否则 211/252 存量库部署不到）", mig)
		}
		// 双目录逐字一致。
		embed := readRepoR176(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig)
		src := readRepoR176(t, mig)
		if embed != src {
			t.Errorf("★ %s 在 sql/migrations/startup 与 embeddata 不一致（fresh-install 与存量库会拿到不同内容）", mig)
		}
		downEmbed := readRepoR176(t, "../../../installer/cmd/llm-gw-installer/embeddata/startup/"+mig919R176DownFile)
		_ = downEmbed
	}

	// db-changelog.md 必须带当前文件的 sha256。
	for _, mig := range []string{mig919R176File, mig920R176File} {
		raw, err := os.ReadFile(mig)
		if err != nil {
			t.Fatalf("read %s: %v", mig, err)
		}
		sum := sha256.Sum256(raw)
		want := hex.EncodeToString(sum[:])
		changelog := readRepoR176(t, "../../../docs/db-changelog.md")
		if !strings.Contains(changelog, want) {
			t.Errorf("★ docs/db-changelog.md 里没有 %s 当前的 sha256（%s）⇒ 登记册与磁盘不一致", mig, want)
		}
	}

	// concurrency gate：918 必须以「已知的非事务通道文件」身份在册。
	gate := readRepoR176(t, "../../../installer/cmd/llm-gw-installer/startup_concurrency_gate_test.go")
	if !strings.Contains(gate, `"920_node_probe_runs_cred_completed_idx.sql": 1,`) {
		t.Errorf("★ startup_concurrency_gate_test.go 缺 918 的 CONCURRENTLY 计数登记（⇒ 该文件会被判为「未登记的通道违规」）")
	}
}
