package ursm_test

// §143.35 的仓内门：**P11 的仓内落地** —— 每日 slot 汇总表（迁移 892）
// + 巡检脚本的基线来源二选一。
//
// 背景（runbook §143.35）：
// ① P11 是巡检成本 #1 的落点（每次 2.9 GB / 190 s、累计 74 GB，§143.29）；
// ② 迁移 892 建 `ursm_snapshot_slot_daily` + 幂等填充函数；
// ③ 巡检脚本的 `base_buckets` 改成【源表路 / 汇总表路】二选一；
// ④ ★★ 三层等价性已在本地真库实测通过：
//    `ab_identical` / `p50_identical` / `verdict_identical`；
// ⑤ ★★ 「两路必须写成【两段独立 SQL】」是被端到端实测逼出来的
//    （PG 在规划期解析表名 ⇒ 同一段 SQL 里写两路 ⇒ 表不存在时整条语句崩
//    ⇒ 回退机制是假的）。
//
// # 为什么这次必须钉门
//
// ① ★★★★★ **「回退必须是真回退」** —— 表不存在时脚本若直接崩，
//    那不是回退而是「巡检失明」⇒ 门钉住「两段独立 SQL」。
// ② ★★★★★ **判定等价是「零语义风险」的唯一硬证据** ⇒ 门钉住三层断言
//    与它们的可重跑脚本。
// ③ ★★★★ **★ 判定输入的一处真实改动（窗口改桶对齐）必须被登记** ——
//    否则后人以为这是「零成本改动」。
// ④ ★★★★ **迁移号与注册点**：892 是现场取的号 ⇒ 门钉住注册点齐全。
// ⑤ ★★★ **部署台账**：改了脚本必须更新台账（漂移门会拦）。

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
)

const (
	mig892      = "../../sql/migrations/startup/892_ursm_snapshot_slot_daily.sql"
	mig892Down  = "../../sql/migrations/startup/892_ursm_snapshot_slot_daily.down.sql"
	patrolSh    = "../../scripts/252-monitor/ursm-snapshot-health.sh"
	ledger252   = "../../scripts/252-monitor/deployed-252.tsv"
	verify892   = "../../scripts/audit/verify_892_local.sh"
	verifyAB    = "../../scripts/audit/verify_892_patrol_ab.sh"
	verifyE2E   = "../../scripts/audit/verify_892_patrol_e2e.sh"
	runnerGo    = "../../installer/internal/dbinit/runner.go"
	installerGo = "../../installer/cmd/llm-gw-installer/main.go"

	// ★★ 收尾时补上的三处「台账/通道」—— 起初 892 只登记了 runner.go 与 main.go，
	// 而这三处全漏 ⇒ 其中【投递通道】当时没有任何门能抓到（迁移在升级库上会被静默跳过）。
	seqSh       = "../../scripts/apply-db-revision-sequence.sh"
	tsvManifest = "../../sql/schema/installed_startup_migrations.tsv"
	dbChangelog = "../../docs/db-changelog.md"

	embedMig892     = "../../installer/cmd/llm-gw-installer/embeddata/startup/892_ursm_snapshot_slot_daily.sql"
	embedMig892Down = "../../installer/cmd/llm-gw-installer/embeddata/startup/892_ursm_snapshot_slot_daily.down.sql"
)

// md5Of14335 返回文件的 md5（十六进制）。
func md5Of14335(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return fmt.Sprintf("%x", md5.Sum(raw))
}

// sha256Of14335 返回文件的 sha256（十六进制）。
func sha256Of14335(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// readFile14335 读文件并在失败时给出可读信息。
func readFile14335(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	return string(raw)
}

// TestSection14335Migration892Exists 钉住迁移 892 存在（源文件 + down）。
func TestSection14335Migration892Exists(t *testing.T) {
	for _, p := range []string{mig892, mig892Down} {
		body := readFile14335(t, p)
		if body == "" {
			t.Fatalf("%s 是空文件", p)
		}
	}
}

// TestSection14335MigrationShape 钉住迁移 892 的关键形态。
func TestSection14335MigrationShape(t *testing.T) {
	body := readFile14335(t, mig892)

	for _, n := range []string{
		"CREATE TABLE IF NOT EXISTS public.ursm_snapshot_slot_daily",
		"day         date        NOT NULL",
		"granularity text        NOT NULL",
		"slot        int         NOT NULL",
		"bucket_ts   timestamptz NOT NULL",
		"rows        bigint      NOT NULL",
		"PRIMARY KEY (day, granularity, slot, bucket_ts)",
		"CREATE OR REPLACE FUNCTION public.refresh_ursm_snapshot_slot_daily",
		"p_granularity text DEFAULT 'hour'",
		"ON CONFLICT (day, granularity, slot, bucket_ts) DO UPDATE",
		"CREATE OR REPLACE FUNCTION public.refresh_ursm_snapshot_slot_daily(",
		"IF p_granularity IS NULL OR p_granularity NOT IN ('hour', 'minute') THEN",
		"RAISE EXCEPTION 'refresh_ursm_snapshot_slot_daily: p_days_back must be >= 1",
		"to_regclass('public.ursm_node_snapshot_min') IS NULL",
		"DELETE FROM public.ursm_snapshot_slot_daily d",
	} {
		if !strings.Contains(body, n) {
			t.Fatalf("迁移 892 里缺关键片段 %q —— 表/函数形态被改动了", n)
		}
	}
}

// TestSection14335MigrationHasBackfill 钉住「迁移内做首次回填」。
//
// ★★★ 为什么必须钉住这条：若表是空的，巡检读到「没有基线」
//   ⇒ 所有桶判成 NO-BASELINE ⇒ **巡检会安静地失明**（比慢更坏）。
func TestSection14335MigrationHasBackfill(t *testing.T) {
	body := readFile14335(t, mig892)

	for _, n := range []string{
		"DO $backfill$",
		"public.refresh_ursm_snapshot_slot_daily(8, 'hour')",
		"public.refresh_ursm_snapshot_slot_daily(8, 'minute')",
		"backfill produced ZERO rows",
		"source table public.ursm_node_snapshot_min absent — backfill skipped",
	} {
		if !strings.Contains(body, n) {
			t.Fatalf("迁移 892 里缺回填片段 %q —— 首次部署时表会是空的 ⇒ 巡检会失明", n)
		}
	}
}

// TestSection14335NoOverloadAmbiguity 钉住「只有【一个】重载」。
//
// ★★★ 本地真库实测过的两条坑：
//   ① `COMMENT ON FUNCTION` **不会创建函数** ⇒ 调单参数版报 does not exist；
//   ② ★ 只要存在【可省略参数】的重载，`f(1)` 就会同时匹配多个候选
//      ⇒ `ERROR: function ... is not unique`（实测：给单参数版去掉默认值也救不了）。
//   ⇒ ⇒ ★★ 唯一可行形态：**只保留一个函数**，第二个参数带默认值
//      （`f(1)` 落到 'hour'，`f(8, 'minute')` 显式指定粒度）。
func TestSection14335NoOverloadAmbiguity(t *testing.T) {
	body := readFile14335(t, mig892)

	// ★★ 全文只能有【一处】CREATE OR REPLACE FUNCTION（针对这个函数名）。
	n := strings.Count(body, "CREATE OR REPLACE FUNCTION public.refresh_ursm_snapshot_slot_daily(")
	if n != 1 {
		t.Fatalf("★ 迁移 892 里有 %d 个 refresh_ursm_snapshot_slot_daily 的重载 ⇒ "+
			"本地真库实测：`f(1)` 会报 function ... is not unique（PG 的重载判定看"+
			"「参数类型 + 哪些参数可省略」）", n)
	}

	// ★ 且必须是【双参数、第二个带默认值】的形态。
	if !strings.Contains(body, "p_days_back integer DEFAULT 1,\n    p_granularity text DEFAULT 'hour'") {
		t.Fatalf("迁移 892 的函数签名不是「双参数 + 第二个带默认值」⇒ "+
			"要么 f(1) 匹配不到、要么 f(8,'minute') 匹配不到")
	}

	// ★ 不得用 COMMENT 冒充函数（实测：不会创建函数）。
	if strings.Contains(body, "COMMENT ON FUNCTION public.refresh_ursm_snapshot_slot_daily(integer)") {
		t.Fatalf("迁移 892 里有一参签名的 COMMENT ⇒ 那是个不存在的函数（实测）")
	}
}

// TestSection14335PatrolHasTwoIndependentSQL 钉住「两路是两段独立 SQL」。
//
// ★★★ 这是本节最重要的判据：PG 在规划期解析表名 ⇒ 同一段 SQL 里写两路
//   ⇒ 汇总表不存在时**整条语句崩** ⇒ 回退机制是假的。
func TestSection14335PatrolHasTwoIndependentSQL(t *testing.T) {
	body := readFile14335(t, patrolSh)

	for _, n := range []string{
		`BUCKETS_SQL_SOURCE="`,
		`BUCKETS_SQL_DAILY="`,
		`if [ "$BASE_SRC" = "daily" ]; then`,
		`BASE_SRC="source"`,
		"to_regclass('public.ursm_snapshot_slot_daily')",
	} {
		if !strings.Contains(body, n) {
			t.Fatalf("巡检脚本里缺片段 %q —— 基线来源二选一被改动了", n)
		}
	}

	// ★★★ 汇总表路【不得】出现在源表路的 SQL 里（否则表不存在时源表路也崩）。
	srcStart := strings.Index(body, `BUCKETS_SQL_SOURCE="`)
	dailyStart := strings.Index(body, `BUCKETS_SQL_DAILY="`)
	if srcStart < 0 || dailyStart < 0 || srcStart > dailyStart {
		t.Fatalf("两段 SQL 的位置异常（source=%d daily=%d）", srcStart, dailyStart)
	}
	srcBody := body[srcStart:dailyStart]
	if strings.Contains(srcBody, "ursm_snapshot_slot_daily") {
		t.Fatalf("★ 源表路的 SQL 里出现了汇总表名 ⇒ 表不存在时源表路也会崩 ⇒ 回退机制是假的")
	}

	// ★ 汇总表路必须同时引用汇总表与源表（今天那一段）。
	dailyEnd := strings.Index(body[dailyStart:], "\n\"")
	if dailyEnd < 0 {
		t.Fatalf("汇总表路的 SQL 段没有正常结束")
	}
	dailyBody := body[dailyStart : dailyStart+dailyEnd]
	if !strings.Contains(dailyBody, "ursm_snapshot_slot_daily") {
		t.Fatalf("汇总表路里没有引用汇总表")
	}
	if !strings.Contains(dailyBody, "FROM ursm_node_snapshot_min") {
		t.Fatalf("汇总表路里没有「今天仍读源表」那一段 ⇒ 今天的数据会整段消失")
	}
}

// TestSection14335PatrolHasNoBacktickInSQLStrings 钉住「SQL 字符串里没有反引号」。
//
// ★★★ 本地真库实测踩过：反引号在双引号字符串里会被 bash 当【命令替换】执行
//   ⇒ 运行时报「syntax: 未找到命令」，而 `bash -n` **发现不了**。
func TestSection14335PatrolHasNoBacktickInSQLStrings(t *testing.T) {
	body := readFile14335(t, patrolSh)

	for _, marker := range []string{`BUCKETS_SQL_SOURCE="`, `BUCKETS_SQL_DAILY="`} {
		start := strings.Index(body, marker)
		if start < 0 {
			t.Fatalf("找不到 %s", marker)
		}
		rest := body[start:]
		end := strings.Index(rest, "\n\"")
		if end < 0 {
			t.Fatalf("%s 的 SQL 段没有正常结束", marker)
		}
		seg := rest[:end]
		if strings.Contains(seg, "`") {
			t.Fatalf("★ %s 的 SQL 段里有反引号 ⇒ bash 会当命令替换执行 ⇒ "+
				"运行时报「未找到命令」而 bash -n 查不出来", marker)
		}
	}
}

// TestSection14335PatrolUsesBucketAlignedWindow 钉住「窗口桶对齐」。
//
// ★★ 这是【判定输入的一处真实改动】（旧路原本用行时间下界）
//   ⇒ 必须被门钉住，否则后人会以为「零成本改动」。
func TestSection14335PatrolUsesBucketAlignedWindow(t *testing.T) {
	body := readFile14335(t, patrolSh)

	// ★ 两段 SQL 都必须用桶对齐的下界。
	for _, marker := range []string{`BUCKETS_SQL_SOURCE="`, `BUCKETS_SQL_DAILY="`} {
		start := strings.Index(body, marker)
		rest := body[start:]
		end := strings.Index(rest, "\n\"")
		seg := rest[:end]
		if !strings.Contains(seg, "date_trunc('$G_UNIT', $NOW_EXPR - make_interval(days => $BASELINE_DAYS))") {
			t.Fatalf("★ %s 里没有桶对齐的窗口下界 ⇒ 两路的边界桶会不一致", marker)
		}
	}
}

// TestSection14335VerificationScriptsExist 钉住三道可重跑验证脚本都在仓里。
//
// ★ 分别是：迁移单测 / 逻辑 A/B / 端到端脚本 A/B。
func TestSection14335VerificationScriptsExist(t *testing.T) {
	for _, p := range []string{verify892, verifyAB, verifyE2E} {
		body := readFile14335(t, p)
		if body == "" {
			t.Fatalf("%s 是空文件", p)
		}
		if !strings.Contains(body, "itgate") && !strings.Contains(body, "DB=") {
			t.Fatalf("%s 看起来不像一次性库实测脚本", p)
		}
	}

	// ★ 三层断言必须写在仓里（否则「实测过」只是一句话）。
	ab := readFile14335(t, "../../scripts/audit/sql/verify_892_patrol_ab.sql")
	for _, n := range []string{
		"ab_identical",
		"p50_identical",
		"verdict_identical",
	} {
		if !strings.Contains(ab, n) {
			t.Fatalf("A/B 脚本里缺断言 %q", n)
		}
	}
}

// TestSection14335RegistrationPoints 钉住迁移 892 的【全部】登记点齐全。
//
// ★★★★ 本门是**收尾时补写的**，而且它补的是一个**真缺口**：
// 892 起初只登记了 runner.go 与 main.go（**5 处注册点里的 2 处**），
// 而 TSV / 投递通道 `files=()` / db-changelog **三处全漏** ⇒
// ★★★ 其中【投递通道 files=()】当时**没有任何门能抓到**
// ⇒ 迁移在升级库上会被**静默跳过**（脚本 warn-only + 部署侧只查文件存在）。
// ⇒ 所以本门覆盖【5 处注册点 + 2 处台账】，而不是「看起来够了的 2 处」。
//
// ★ 纪律：**「只改文件」≠「生效」** —— 登记点必须逐处对齐，
// 且每处都要有门钉住，否则缺口只能靠「恰好有人跑了那个包的测试」才暴露。
func TestSection14335RegistrationPoints(t *testing.T) {
	checks := []struct {
		path   string
		needle string
		why    string
	}{
		{runnerGo, `"892_ursm_snapshot_slot_daily.sql"`,
			"runner.go 的 StartupFiles 缺它 —— 安装器（新装库）不会执行它"},
		{installerGo, "//go:embed embeddata/startup/892_ursm_snapshot_slot_daily.sql",
			"main.go 缺 //go:embed —— 全新装机编不出二进制"},
		{installerGo, `"startup/892_ursm_snapshot_slot_daily.sql"`,
			"main.go 缺 map 条目 —— 投递映射找不到文件"},
		{seqSh, `"$ROOT_DIR/sql/migrations/startup/892_ursm_snapshot_slot_daily.sql"`,
			"部署通道 files=() 缺它 —— 升级库上会被静默跳过"},
		{tsvManifest, "892_ursm_snapshot_slot_daily.sql",
			"TSV 缺它 —— 位置记录断档（installer 包的 manifest 门会红，但那个包不在 ursmcheck 回归范围内）"},
		{dbChangelog, "| 892 | `892_ursm_snapshot_slot_daily.sql` |",
			"台账缺登记 —— sqlguard 的连续性门判红且运维看不见"},
	}
	for _, c := range checks {
		body := readFile14335(t, c.path)
		// ★★ 断言 needle **恰好出现一次**：
		//   Contains 型门只保护「至少有一处」⇒ needle 不唯一时会假绿
		//   （本轮已实测踩过：台账里「迁移 892」出现两次 ⇒ 改一处门仍绿）。
		if n := strings.Count(body, c.needle); n != 1 {
			t.Errorf("%s 里 %s 出现 %d 次（要求恰好 1 次）—— %s",
				c.path, c.needle, n, c.why)
		}
	}
}

// TestSection14335EmbeddataCopyIsByteIdentical 钉住「canonical 与 embeddata 副本逐字相同」。
//
// ★ 迁移正文改一个字而副本没跟上 ⇒ 两条通道执行的**不是同一份字节**，
//   而「两份 sha 现场一致」是台账行里的承诺 ⇒ 门钉住它。
func TestSection14335EmbeddataCopyIsByteIdentical(t *testing.T) {
	for _, pair := range [][2]string{
		{mig892, embedMig892},
		{mig892Down, embedMig892Down},
	} {
		if a, b := readFile14335(t, pair[0]), readFile14335(t, pair[1]); a != b {
			t.Errorf("embeddata 副本与 canonical 不一致：%s vs %s", pair[0], pair[1])
		}
	}
}

// TestSection14335ChangelogShaIsMeasured 钉住「台账里的 sha 是文件实测值」。
//
// ★ 历史上出现过手抄 sha 导致对账门判红（见 885 那一行的订正记录）⇒
//   本门要求台账里出现的 sha **等于**当场对文件算出来的 sha。
func TestSection14335ChangelogShaIsMeasured(t *testing.T) {
	want := sha256Of14335(t, mig892)
	ch := readFile14335(t, dbChangelog)
	if !strings.Contains(ch, want) {
		t.Errorf("★ 台账里的 sha 与文件实测值不一致 —— 实测应为 %s（台账的 sha 必须是实测值，不是从别处抄的）", want)
	}
}

// TestSection14335LedgerIsUpdated 钉住「改了脚本 ⇒ 台账已更新」。
//
// ★ 这是仓内漂移门的另一半：本门钉「台账里的仓内 md5 == 脚本实际 md5」。
func TestSection14335LedgerIsUpdated(t *testing.T) {
	ledger := readFile14335(t, ledger252)

	// ★ 台账里这一行的仓内 md5 必须与脚本实际 md5 一致。
	var repoMD5 string
	for _, ln := range strings.Split(ledger, "\n") {
		if strings.HasPrefix(ln, "ursm-snapshot-health.sh\t") {
			parts := strings.Split(ln, "\t")
			if len(parts) >= 4 {
				repoMD5 = parts[2]
			}
		}
	}
	if repoMD5 == "" {
		t.Fatalf("台账里没有 ursm-snapshot-health.sh 那一行")
	}
	if actual := md5Of14335(t, patrolSh); repoMD5 != actual {
		t.Fatalf("★ 台账里的仓内 md5 (%s) 与脚本实际 md5 (%s) 不一致 ⇒ 必须更新台账",
			repoMD5, actual)
	}
}

// TestSection14335LedgerRecordsDrift 钉住台账对 ursm-snapshot-health.sh 部署状态的记录。
//
// ★ 历史判据（2026-10-22 写）：「线上未部署 ⇒ 台账必须记 drift 且写清原因」。
// ★ 2026-10-24 llmgo 六页面修复轮：P11 两件套（巡检新版本 + slot-refresh）与
//   /etc/cron.d/pg17 的刷新行已实际部署到 252（手动实测 base_source=daily、
//   64.7s 完成、coverage 7/7）⇒ 事实翻转 ⇒ 本门同步翻转到新事实：
//   「已部署 ⇒ 台账必须 in-sync 且写清部署证据」。
//   （门的意图不变：台账不许对部署状态说谎——状态必须与线上实测一致。）
func TestSection14335LedgerRecordsDrift(t *testing.T) {
	ledger := readFile14335(t, ledger252)

	for _, ln := range strings.Split(ledger, "\n") {
		if !strings.HasPrefix(ln, "ursm-snapshot-health.sh\t") {
			continue
		}
		parts := strings.Split(ln, "\t")
		if len(parts) < 5 {
			t.Fatalf("台账行字段不足：%q", ln)
		}
		if parts[3] != "in-sync" {
			t.Fatalf("★ 台账状态是 %q —— 线上已部署 10efc586（2026-10-24 实测 base_source=daily）⇒ 必须是 in-sync", parts[3])
		}
		reason := parts[4]
		// ★ needle 用【唯一】的表述 —— 部署证据必须落在台账里，
		//   让「部署过、验证过」可从台账本身复算（不依赖会话记忆）。
		for _, n := range []string{"§143.35", "P11", "base_source=daily", "迁移 892"} {
			if !strings.Contains(reason, n) {
				t.Fatalf("台账原因里缺 %q —— 必须写清部署证据（快路实测、依赖迁移 892）", n)
			}
		}
		return
	}
	t.Fatalf("台账里没有 ursm-snapshot-health.sh 那一行")
}

// ═══════════════════════════════════════════════════════════════════════
// 文档层判据（runbook §143.35 + 订正横幅十六）
// ═══════════════════════════════════════════════════════════════════════

const runbook14335 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14335 取出指定小节的正文。
func section14335(t *testing.T, prefix string) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14335)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## "+prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到小节 %q", prefix)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// mustContain14335 断言给定文本里至少含 needle 之一。
func mustContain14335(t *testing.T, what string, body string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(body, n) {
			t.Fatalf("%s 里缺关键读数 %q", what, n)
		}
	}
}

// TestRunbook14335SectionPresent 钉住 §143.35 已在 runbook 里。
func TestRunbook14335SectionPresent(t *testing.T) {
	s := section14335(t, "§143.35 ")
	for _, h := range []string{
		"### §143.35.0 结论先行",
		"### §143.35.1 交付物（全部仓内）",
		"### §143.35.2 迁移 892 的形态要点",
		"### §143.35.3 复核方法（可原样重跑）",
	} {
		if !strings.Contains(s, h) {
			t.Fatalf("§143.35 缺少子节 %q", h)
		}
	}
}

// TestRunbook14335ThreeLayersAreMeasured 钉住「三层等价性已实测」。
func TestRunbook14335ThreeLayersAreMeasured(t *testing.T) {
	s := section14335(t, "§143.35 ")
	mustContain14335(t, "§143.35 正文", s,
		"ab_identical",
		"p50_identical",
		"verdict_identical",
		"E2E_VERDICT: PASS",
		"verify_892_local.sh",
		"verify_892_patrol_ab.sh",
		"verify_892_patrol_e2e.sh",
		"10 项全 PASS",
	)
}

// TestRunbook14335RecordsTheFailedFirstRun 钉住「第一次 A/B 是 FAIL 的」。
//
// ★★★ 这条最重要：只写「验证通过」而不写「先失败后改设计」，
//   后人就看不到「零语义风险」这句话被修正过一次 ⇒ 也会低估下一次的风险。
func TestRunbook14335RecordsTheFailedFirstRun(t *testing.T) {
	s := section14335(t, "§143.35 ")
	mustContain14335(t, "§143.35 正文", s,
		"第一次 A/B 实测 FAIL",
		"16 行 `OLD_ONLY`，全部是【今天】的桶",
		"[today - N, today)",
		"方案 B",
	)
}

// TestRunbook14335RecordsFakeFallback 钉住「回退机制原本是假的」。
func TestRunbook14335RecordsFakeFallback(t *testing.T) {
	s := section14335(t, "§143.35 ")
	mustContain14335(t, "§143.35 正文", s,
		"回退机制」原本是【假的】",
		`relation "public.ursm_snapshot_slot_daily" does not exist`,
		"PG 在【规划期】就解析表名",
		"两段各自完整的 SQL",
		"巡检失明",
	)
}

// TestRunbook14335RecordsSemanticChange 钉住「窗口桶对齐 = 一处真实语义改动」。
func TestRunbook14335RecordsSemanticChange(t *testing.T) {
	s := section14335(t, "§143.35 ")
	mustContain14335(t, "§143.35 正文", s,
		"判定输入的真实改动",
		"桶对齐",
		"边界桶只算半截",
		"属主必须知情并拍板",
	)
}

// TestRunbook14335DoesNotPromiseSavings 钉住「不因等价性已证就承诺收益」。
//
// ★★ 这是 §143.28 的纪律：等价性 ≠ 收益已量。
// ★ 这两句话写在【订正横幅十六】里（§143.31.2.3）⇒ 判据钉它的「老家」。
func TestRunbook14335DoesNotPromiseSavings(t *testing.T) {
	s31 := section14335(t, "§143.31 ")
	mustContain14335(t, "§143.31 正文", s31,
		"而「省多少」仍然是【估】",
		"不因等价性已证就承诺收益数字",
	)
}

// TestRunbook14335HonestBoundaries 钉住本节的诚实边界。
func TestRunbook14335HonestBoundaries(t *testing.T) {
	s := section14335(t, "§143.35 ")
	mustContain14335(t, "§143.35 正文", s,
		"对生产的写入 = 0",
		"未部署",
		"本地一次性库",
		"退出即 DROP",
	)
}

// TestRunbook14335BannerLivesIn14331 钉住订正横幅就地钉在 §143.31.2.3。
func TestRunbook14335BannerLivesIn14331(t *testing.T) {
	s31 := section14335(t, "§143.31 ")
	s35 := section14335(t, "§143.35 ")

	mustContain14335(t, "§143.31 正文", s31,
		"订正横幅十六",
		"订正横幅十六（2026-10-20，§143.35 就地订正本节 §143.31.2.3）",
		// ★ 被订正的原文必须逐字还在。
		"26M 行/小时 → 3.75M 行/天 ⇒ −96%",
	)
	// ★ 横幅的本体不能在 §143.35 里。
	if strings.Contains(s35, "> **订正横幅十六（") {
		t.Fatalf("订正横幅十六的【本体】出现在 §143.35 里 ⇒ 它被搬走了 ⇒ 「就地订正」不成立")
	}
}
