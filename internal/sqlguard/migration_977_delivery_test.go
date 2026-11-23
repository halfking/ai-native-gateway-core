package sqlguard

// migration_977_delivery_test.go —— 迁移 977（pi_error_analysis_semantics）
// 的**投递六点齐备性 + 幂等性 + 四语义修复**的门。
//
// # 背景（2026-11-17 PI 错误分析语义订正轮）
//
//	971（pi_error_analysis_system）把 PI 智能体错误自动分析系统 Phase 1 落地了
//	（两张表 + 统计视图 + 触发器 + 保留期清理函数），但**留下四个语义缺陷**：
//
//	① **F1 cleanup_old_error_analysis 会炸 ＋ 恒 no-op**
//	   971 的 `DELETE ... RETURNING * INTO v_deleted_results` 把 **21 列**塞进
//	   一个**标量** ⇒ 删到 ≥2 行时 PL/pgSQL 抛
//	   「query returned more than one row」⇒ 整个清理事务回滚
//	   （**隔离 PG 17.10 实测复现**：同形状函数删 3 行即抛该错）；
//	   而删除谓词依赖 `auto_applied` / `human_verified` —— **全仓 Go 代码从不写
//	   这两列**（写面 0 处命中；DEFAULT 为 FALSE/NULL）⇒ 即使不报错也是永久 no-op。
//	② **F2 v_error_analysis_stats JOIN 膨胀**
//	   旧视图 `COUNT(*)` 数的是 **(队列行 × 结果行) 的连接行** ⇒ 1 条队列 + 2 条
//	   结果报 total_errors=2、total_tokens_used 翻倍（实测同数据：971 形态报 2 /
//	   977 形态报 1）⇒ 监控面系统性虚高。
//	③ **F3 error_analysis_results(queue_id) 无唯一性**
//	   971 只给了普通索引，而写面 INSERT 没有 queue_id 冲突处理 ⇒ 同一队列可挂
//	   多条结果 ⇒ F2 的膨胀有了真实来源。
//	④ **F4 L2（advanced）规则 pattern 的分组缺陷**
//	   `privilege_escalation`（sev 9）的 `(?i)(grant|give).*admin|sudo|root`
//	   顶层 `|` 让 `sudo`/`root` **裸词成支** ⇒ "root cause"、"/root" severity 9
//	   命中；`command_injection` / `tool_abuse_exec`（sev 10）的 `system` + 无界
//	   `.*` ⇒ "the operating system as a whole (see below)" severity 10 命中。
//
// # 本门钉住什么
//
//	① 977 的**六点位**逐点在位（canonical / embeddata 副本 / go:embed + var /
//	   embeddedSQLFiles map / dbinit StartupFiles / deploy 通道 files 数组 +
//	   ledger_artifact_probe / tsv 台账 / changelog 台账）；
//	② 977 的 SQL **幂等**（可被内容指纹通道重放）；
//	③ **四个修复各自真的落地**（不是「文件里写了句话」）；
//	④ **F4 的语义用 RE2 实测**（RE2 ＝ 检测器编译规则的同款引擎）：普通文本
//	   **不**命中、真实攻击形态**仍**命中；且**负控**证明旧 pattern 确实命中过
//	   这些普通文本（否则「不命中」可能是断言写空了）。
//
// ★ 为什么 F4 的判据要用 **Go 的 regexp** 而不是字符串比较：
//   检测器（domains/promptinjection）用 Go 的 regexp（RE2）编译 pattern ⇒
//   语义判据必须用**同一个引擎** ⇒ 否则「在 SQL 里看着对」不等于「运行时对」。
//   ★ 同理，本门**不在** 977 的 pattern 里加 `(?s)`：跨行绕过是**检测器编译
//   侧**的修法（并行工程师在 Go 侧统一补）⇒ 本门断言 977 的 pattern **不含**
//   `(?s)` ⇒ 两边职责不重叠、不会互相覆盖。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	pi977UpFile   = "977_pi_error_analysis_semantics.sql"
	pi977DownFile = "977_pi_error_analysis_semantics.down.sql"

	pi977CanonicalDir = "../../sql/migrations/startup"
	pi977EmbedDir     = "../../installer/cmd/llm-gw-installer/embeddata/startup"
	pi977InstallerGo  = "../../installer/cmd/llm-gw-installer/main.go"
	pi977DbinitRunner = "../../installer/internal/dbinit/runner.go"
	pi977Tsv          = "../../sql/schema/installed_startup_migrations.tsv"
	pi977Changelog    = "../../docs/db-changelog.md"

	// pi977Seed315 / pi977Seed364 是 L2 规则的**种子**来源。
	//
	// ★ 它们同时是本门 F4 负控的**取材处**（旧 pattern 的真身）⇒
	//   「旧 pattern 确实命中过普通文本」这条断言不是照抄的，是从磁盘取的。
	pi977Seed315 = "../../sql/migrations/startup/315_prompt_injection_detection.sql"
	pi977Seed364 = "../../sql/migrations/startup/364_prompt_injection_enhanced.sql"
)

// pi977ExpectedTSVIndex 是 977 在 installed_startup_migrations.tsv 里的
// **应用序**下标（0 起）——该文件是 installer StartupFiles 的机器可读镜像，
// 两边必须逐条一致（installer/internal/dbinit 的门在比）。
const pi977ExpectedTSVIndex = 377

// pi977UpdateRe 抽取「UPDATE public.prompt_injection_rules SET pattern = '…'
// WHERE rule_name = '…'」⇒ (pattern, rule_name)。
//
// ★ 977 的 pattern 里**含逗号**（语境词表）⇒ 不能按逗号切分 ⇒ 这里用
//
//	「引号内的原文」整体取出。
var pi977UpdateRe = regexp.MustCompile(
	`(?s)UPDATE\s+public\.prompt_injection_rules\s+SET\s+pattern\s*=\s*'([^']*)'\s*WHERE\s+rule_name\s*=\s*'([^']*)'`)

// pi977RuleCase 是一条 L2 规则的语义判据。
type pi977RuleCase struct {
	rule string   // rule_name
	seed string   // 种子文件（负控取材处）
	fp   []string // 普通文本：必须**不**命中
	tp   []string // 真实攻击形态：必须**仍**命中
}

// pi977RuleCases 是 F4 的语义判据表。
//
// ★ fp（假阳性样本）逐条取自**缺陷的真实形态**（不是随手编的句子）：
//
//	· "root cause" / "/root" —— 971 的 `|root` 裸词分支的经典误报；
//	· "the operating system as a whole (see below)" —— `system` + 无界 `.*`
//	  的经典误报（英文里 "system" 后面跟括号极常见）。
//
// ★ tp（真阳性样本）逐条取自**必须保住的攻击形态**：收紧 pattern 时最容易
//
//	顺手把这些也收紧掉 ⇒ 本门把它们钉住（⇒ 「不削弱真正危险的模式」）。
var pi977RuleCases = []pi977RuleCase{
	{
		rule: "privilege_escalation",
		seed: pi977Seed315,
		fp: []string{
			"the root cause of the failure",
			"look under /root for the config",
			"the root directory of the filesystem",
		},
		tp: []string{
			"grant admin access",
			"give me admin rights",
			"sudo rm -rf /",
			"sudo -u root -i",
			"root access",
			"root shell",
			"run as root",
			"escalate to root",
			"become root",
		},
	},
	{
		rule: "command_injection",
		seed: pi977Seed315,
		fp: []string{
			"the operating system as a whole (see below)",
			"the filesystem layout (read the docs)",
		},
		tp: []string{
			"os.system('rm -rf /')",
			"os.system ('rm -rf /')",
			"eval(input())",
			"exec('ls')",
			"shell_exec('ls')",
		},
	},
	{
		rule: "tool_abuse_exec",
		seed: pi977Seed364,
		fp: []string{
			"the system will call you back",
			"system call overhead is high",
		},
		tp: []string{
			"subprocess.call('ls')",
			"child_process.exec('ls')",
			"os.system('rm -rf /')",
			"invoke subprocess",
			"eval('1+1')",
		},
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// ① 投递六点齐备性
// ─────────────────────────────────────────────────────────────────────────────

func TestMigration977DeliverySixPointsAreInPlace(t *testing.T) {
	// ── canonical 正典件在位 ───────────────────────────────────────────
	if _, err := os.ReadFile(filepath.Join(pi977CanonicalDir, pi977UpFile)); err != nil {
		t.Fatalf("读 canonical %s：%v", pi977UpFile, err)
	}
	if _, err := os.Stat(filepath.Join(pi977CanonicalDir, pi977DownFile)); err != nil {
		t.Errorf("canonical down 件 %s 不存在：%v", pi977DownFile, err)
	}

	// ── embeddata 副本逐字节相同（849/850/851 那一族漂移）────────────
	for _, name := range []string{pi977UpFile, pi977DownFile} {
		embed, err := os.ReadFile(filepath.Join(pi977EmbedDir, name))
		if err != nil {
			t.Errorf("embeddata 副本 %s 不存在：%v", name, err)
			continue
		}
		src, err := os.ReadFile(filepath.Join(pi977CanonicalDir, name))
		if err != nil {
			t.Errorf("读 canonical %s：%v", name, err)
			continue
		}
		if string(embed) != string(src) {
			t.Errorf("embeddata 漂移 %s：embeddata 与 sql/migrations/startup 逐字节不一致"+
				"（installer 会带着非正典 SQL 上门）", name)
		}
	}

	// ── installer main.go：go:embed ×2 + embeddedSQLFiles map ×2 ─────
	installerRaw, err := os.ReadFile(pi977InstallerGo)
	if err != nil {
		t.Fatalf("读 %s：%v", pi977InstallerGo, err)
	}
	installerText := string(installerRaw)
	for _, name := range []string{pi977UpFile, pi977DownFile} {
		if !strings.Contains(installerText, "//go:embed embeddata/startup/"+name) {
			t.Errorf("installer main.go 缺 go:embed 指令：embeddata/startup/%s", name)
		}
		if !strings.Contains(installerText, `"startup/`+name+`"`) {
			t.Errorf("installer main.go 的 embeddedSQLFiles map 缺条目：%q", "startup/"+name)
		}
	}

	// ── dbinit runner 的 StartupFiles ──────────────────────────────────
	runnerRaw, err := os.ReadFile(pi977DbinitRunner)
	if err != nil {
		t.Fatalf("读 %s：%v", pi977DbinitRunner, err)
	}
	runnerText := string(runnerRaw)
	if !strings.Contains(runnerText, `"`+pi977UpFile+`"`) {
		t.Errorf("dbinit runner 的 StartupFiles 缺 %q ⇒ 全新安装/升级不会应用这条迁移", pi977UpFile)
	}
	// ★ 投递**顺序**也是契约：977 OR REPLACE 971 建出来的函数与视图 ⇒
	//   977 必须排在 971 **之后**（顺序反了 ⇒ 977 先跑、971 后跑 ⇒ 缺陷回来）。
	if idx971, idx977 := strings.Index(runnerText, `"971_pi_error_analysis_system.sql"`),
		strings.Index(runnerText, `"`+pi977UpFile+`"`); idx971 < 0 || idx977 < 0 || idx977 < idx971 {
		t.Errorf("dbinit runner 的 StartupFiles 里 977 必须排在 971 **之后**" +
			"（971 建函数/视图、977 订正它们；顺序反了 ⇒ 971 的缺陷被重新覆盖回来）")
	}

	// ── deploy 通道 files=(...) 数组 + ledger_artifact_probe ───────────
	seqRaw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读 %s：%v", revisionSequenceScriptPath, err)
	}
	seqText := string(seqRaw)
	arrBlock := revisionSequenceArrayBlock(t, seqText)
	if !revisionSequenceEntryRe.MatchString(arrBlock) ||
		!strings.Contains(arrBlock, pi977UpFile) {
		t.Errorf("deploy 通道 files=(...) 数组缺 %q ⇒ 211 的 seamless 部署通道不会应用这条迁移"+
			"（962 那次断层的同型）", pi977UpFile)
	}
	if !strings.Contains(seqText, `ledger_artifact_probe "`+pi977UpFile+`"`) {
		t.Errorf("deploy 通道缺 ledger_artifact_probe %q ⇒ 「台账说已应用但产物不在场」"+
			"这一形态无人对账（2026-10-29 211 实测：912/913 台账有行、产物缺席）", pi977UpFile)
	}

	// ── tsv 台账行（应用序下标必须与 StartupFiles 一致）────────────────
	tsvRaw, err := os.ReadFile(pi977Tsv)
	if err != nil {
		t.Fatalf("读 %s：%v", pi977Tsv, err)
	}
	wantLine := strconv.Itoa(pi977ExpectedTSVIndex) + "\t" + pi977UpFile
	found := false
	for _, line := range strings.Split(string(tsvRaw), "\n") {
		if strings.TrimSpace(line) == wantLine {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s 缺 %q 行 ⇒ 启动链的机器可读镜像与实际投递不同步"+
			"（该 tsv 是 installer StartupFiles 的派生镜像，两边必须逐条一致）",
			pi977Tsv, wantLine)
	}

	// ── changelog 台账行 ×2（.sql 与 .down.sql），sha 必须是文件实测值 ───
	changelogRaw, err := os.ReadFile(pi977Changelog)
	if err != nil {
		t.Fatalf("读 %s：%v", pi977Changelog, err)
	}
	changelogText := string(changelogRaw)
	for _, name := range []string{pi977UpFile, pi977DownFile} {
		body, err := os.ReadFile(filepath.Join(pi977CanonicalDir, name))
		if err != nil {
			t.Errorf("读 canonical %s：%v", name, err)
			continue
		}
		sum := sha256.Sum256(body)
		want := hex.EncodeToString(sum[:])
		if !strings.Contains(changelogText, "| `"+name+"` | `"+want+"` |") {
			t.Errorf("%s 缺 977 的登记行，或登记的 sha 与文件实测不符（实测 %s）"+
				"⇒ verify-migration-checksums.sh 会判 MISMATCH ⇒ CI 失败",
				pi977Changelog, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ② 幂等性（内容指纹通道会重放已应用文件 ⇒ 非幂等 DDL 会 42P07/42710）
// ─────────────────────────────────────────────────────────────────────────────

func TestMigration977MigrationIsIdempotent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi977CanonicalDir, pi977UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi977UpFile, err)
	}
	text := string(raw)
	exec := pi977StripComments(text)

	// ── 事务块：SET LOCAL 必须**在事务块内**才生效（PG 只在事务块里认它）──
	beginIdx := strings.Index(exec, "BEGIN;")
	if beginIdx < 0 {
		t.Fatalf("%s 没有 BEGIN; ⇒ SET LOCAL 的守卫不会生效（PG 在事务块外只发一条"+
			"WARNING、值不设置）⇒ lock_timeout / statement_timeout 是空文", pi977UpFile)
	}
	if !strings.Contains(exec, "COMMIT;") {
		t.Errorf("%s 没有 COMMIT; ⇒ 事务边界不完整", pi977UpFile)
	}
	for _, want := range []string{"SET LOCAL lock_timeout", "SET LOCAL statement_timeout"} {
		idx := strings.Index(exec, want)
		if idx < 0 {
			t.Errorf("%s 缺 %q ⇒ 等锁/超长 DDL 没有守卫", pi977UpFile, want)
			continue
		}
		if idx < beginIdx {
			t.Errorf("%s 的 %q 出现在 BEGIN; **之前** ⇒ 它在事务块外，不生效",
				pi977UpFile, want)
		}
	}

	// ── 索引必须 IF NOT EXISTS（裸 CREATE INDEX 重放即 42P07）──────────
	for i, line := range strings.Split(text, "\n") {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if strings.HasPrefix(upper, "CREATE INDEX") ||
			(strings.HasPrefix(upper, "CREATE UNIQUE INDEX") && !strings.Contains(upper, "IF NOT EXISTS")) {
			t.Errorf("第 %d 行的 CREATE INDEX 缺 IF NOT EXISTS：%s\n"+
				"后果：通道按内容指纹重放本件时 42P07（relation already exists）⇒ 部署 die",
				i+1, strings.TrimSpace(line))
		}
	}

	// ── 台账自登记（≥952 纪律）：缺了 ⇒ 「账本说没做」⇒ 后续按账本判断的人踩空
	if !strings.Contains(text, "schema_migrations") {
		t.Errorf("%s 没有 schema_migrations 自登记行 ⇒ 211 的记账链在这里断层", pi977UpFile)
	}
	if !strings.Contains(text, "VALUES ('977'") {
		t.Errorf("%s 的自登记行里没有 '977' 这个版本号", pi977UpFile)
	}
	if !strings.Contains(strings.ToUpper(text), "ON CONFLICT") {
		t.Errorf("%s 的自登记行没有 ON CONFLICT ⇒ 重放时唯一键冲突", pi977UpFile)
	}

	// ── 「零建表 / 零列变更」是本轮对外承诺的形态（与并行 Go 侧零冲突面）──
	for _, forbidden := range []string{"CREATE TABLE", "ALTER TABLE", "DROP TABLE", "DROP COLUMN"} {
		if strings.Contains(strings.ToUpper(exec), forbidden) {
			t.Errorf("%s 的可执行面含 %q ⇒ 本轮承诺的「零建表 / 零列变更」被打破"+
				"（977 是**语义订正**，结构变更属于另一次迁移）", pi977UpFile, forbidden)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ③ 四语义修复各自落地
// ─────────────────────────────────────────────────────────────────────────────

// TestPi977FixesCleanupFunctionSemantics —— F1。
func TestMigration977FixesCleanupFunctionSemantics(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi977CanonicalDir, pi977UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi977UpFile, err)
	}
	exec := pi977StripComments(string(raw))

	if !strings.Contains(exec, "CREATE OR REPLACE FUNCTION public.cleanup_old_error_analysis") {
		t.Fatalf("%s 没有重写 cleanup_old_error_analysis ⇒ F1 未修", pi977UpFile)
	}
	// ★ 判据落在**函数体**上，而不是整份文件：
	//   文件里的 `COMMENT ON FUNCTION ... IS '…'` 是一段**字符串字面量**，
	//   它为了登记缺陷而提到了 `auto_applied` / `RETURNING`
	//   ⇒ 拿整份文件当判据 ⇒ 会把「说明文字」当成「实现」（本仓反复踩到的
	//   「扫的是代码还是散文」）；函数体才是这段缺陷真正所在的地方。
	body := pi977FunctionBody(t, exec, "public.cleanup_old_error_analysis")
	if !strings.Contains(body, "GET DIAGNOSTICS") || !strings.Contains(body, "ROW_COUNT") {
		t.Errorf("%s 的清理函数体没有 GET DIAGNOSTICS ... = ROW_COUNT ⇒ 行数取不到", pi977UpFile)
	}
	// ★ 缺陷本体：`RETURNING * INTO` 一个标量 ⇒ 删到 ≥2 行就抛
	//   「query returned more than one row」⇒ 整个清理事务回滚。
	if strings.Contains(strings.ToUpper(body), "RETURNING") {
		t.Errorf("%s 的清理函数体还有 RETURNING ⇒ 971 那个「多行即抛错」的缺陷没修掉"+
			"（隔离 PG 17.10 实测：同形状函数删 3 行即抛 query returned more than one row）",
			pi977UpFile)
	}
	// ★ 缺陷本体之二：判据依赖没人写的标志位 ⇒ 清理器恒 no-op。
	for _, never := range []string{"auto_applied", "human_verified"} {
		if strings.Contains(body, never) {
			t.Errorf("%s 的清理函数体还有 %q ⇒ 清理谓词仍依赖**全仓 Go 代码从不写**的列"+
				"（DEFAULT 为 FALSE/NULL）⇒ 清理器恒 no-op", pi977UpFile, never)
		}
	}
	// ★ 修法本体：时间口径 + 两张表同一保留窗口。
	if !strings.Contains(body, "created_at < v_cutoff") {
		t.Errorf("%s 的清理函数体没有时间口径的保留窗口（created_at < v_cutoff）⇒ F1 未修",
			pi977UpFile)
	}
}

// TestPi977FixesStatsViewInflation —— F2。
func TestMigration977FixesStatsViewInflation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi977CanonicalDir, pi977UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi977UpFile, err)
	}
	exec := pi977StripComments(string(raw))

	if !strings.Contains(exec, "CREATE OR REPLACE VIEW public.v_error_analysis_stats") {
		t.Fatalf("%s 没有重写 v_error_analysis_stats ⇒ F2 未修", pi977UpFile)
	}
	if !strings.Contains(exec, "COUNT(DISTINCT queue_id)") {
		t.Errorf("%s 的统计视图没有 COUNT(DISTINCT queue_id) ⇒ total_errors 仍在数"+
			"(队列 × 结果) 的连接行 ⇒ JOIN 膨胀没修掉", pi977UpFile)
	}
	// ★ 旧视图的形态（LEFT JOIN 直接接结果表）必须不再出现。
	if strings.Contains(exec, "LEFT JOIN public.error_analysis_results r ON q.id = r.queue_id") {
		t.Errorf("%s 的统计视图还在用 971 那个会膨胀的 LEFT JOIN 形态 ⇒ F2 未修", pi977UpFile)
	}
	// ★ 语义保持：analyzed_count / pending_count 仍是「每条队列计一次」。
	for _, col := range []string{"analyzed_count", "pending_count", "false_positive_count"} {
		if !strings.Contains(exec, col) {
			t.Errorf("%s 的统计视图缺列 %s ⇒ 读面的列集合被改了（下游会 42703）",
				pi977UpFile, col)
		}
	}
}

// TestPi977AddsQueueIdUniqueness —— F3。
func TestMigration977FixesQueueIdUniqueness(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi977CanonicalDir, pi977UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi977UpFile, err)
	}
	exec := pi977StripComments(string(raw))

	if !strings.Contains(exec, "CREATE UNIQUE INDEX IF NOT EXISTS idx_error_analysis_results_queue_uniq") {
		t.Errorf("%s 没有给 error_analysis_results(queue_id) 建唯一索引 ⇒ F3 未修"+
			"（同一队列仍可挂多条分析结果）", pi977UpFile)
	}
	if !strings.Contains(exec, "ON public.error_analysis_results(queue_id)") {
		t.Errorf("%s 的唯一索引不是建在 queue_id 上 ⇒ F3 未修", pi977UpFile)
	}
	// ★ 去重必须**在前**：否则存量库建唯一索引会 23505 ⇒ 迁移失败。
	dedupeIdx := strings.Index(exec, "HAVING COUNT(*) > 1")
	indexIdx := strings.Index(exec, "CREATE UNIQUE INDEX")
	if dedupeIdx < 0 {
		t.Errorf("%s 没有去重块（HAVING COUNT(*) > 1）⇒ 存量库上建唯一索引会 23505"+
			"（duplicate key）⇒ 迁移失败", pi977UpFile)
	} else if indexIdx >= 0 && dedupeIdx > indexIdx {
		t.Errorf("%s 的去重块写在唯一索引**之后** ⇒ 建索引时存量重复行还在 ⇒ 23505 ⇒ 迁移失败",
			pi977UpFile)
	}
	// ★ 去重判定必须是**确定**的：同一 queue_id 保留 id 最大的一行。
	if !strings.Contains(exec, "dup.id < keep.id") {
		t.Errorf("%s 的去重判定不是「保留 id 最大的一行」⇒ 去重结果不确定、不可复算",
			pi977UpFile)
	}
}

// TestPi977FixesL2RulePatternGrouping —— F4（语义用 RE2 实测 + 负控）。
func TestMigration977FixesL2RulePatternGrouping(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pi977CanonicalDir, pi977UpFile))
	if err != nil {
		t.Fatalf("读 canonical %s：%v", pi977UpFile, err)
	}
	exec := pi977StripComments(string(raw))

	updates := pi977UpdateRe.FindAllStringSubmatch(exec, -1)
	got := map[string]string{}
	for _, m := range updates {
		if _, dup := got[m[2]]; dup {
			t.Errorf("%s 对规则 %q 有多条 UPDATE ⇒ 语义面多写", pi977UpFile, m[2])
		}
		got[m[2]] = m[1]
	}
	if len(got) != len(pi977RuleCases) {
		t.Errorf("%s 订正了 %d 条规则（期望 %d 条：%v）⇒ 多改或漏改",
			pi977UpFile, len(got), len(pi977RuleCases), pi977RuleRuleNames())
	}

	for _, tc := range pi977RuleCases {
		newPat, ok := got[tc.rule]
		if !ok {
			t.Errorf("%s 没有订正规则 %q 的 pattern ⇒ F4 未修", pi977UpFile, tc.rule)
			continue
		}
		// ── 新 pattern 必须能被**检测器同款引擎**（Go regexp / RE2）编译 ──
		re, err := regexp.Compile(newPat)
		if err != nil {
			t.Errorf("规则 %q 的新 pattern 编译失败（Go regexp / RE2）：%v\n"+
				"⇒ 检测器加载这条规则时会直接失败 ⇒ 那条规则等于不存在", tc.rule, err)
			continue
		}
		// ── 假阳性样本必须**不**命中 ──
		for _, s := range tc.fp {
			if re.MatchString(s) {
				t.Errorf("规则 %q 的新 pattern 仍命中普通文本 %q ⇒ 分组/词界缺陷没修掉",
					tc.rule, s)
			}
		}
		// ── 真阳性样本必须**仍**命中（不削弱真正危险的模式）────────────
		for _, s := range tc.tp {
			if !re.MatchString(s) {
				t.Errorf("规则 %q 的新 pattern 不再命中攻击形态 %q ⇒ 收紧过头、削弱了检测",
					tc.rule, s)
			}
		}
		// ── 负控：种子里的**旧** pattern 必须命中过这些普通文本 ──
		//   （否则上面的「不命中」可能只是断言写空了 ⇒ 恒绿门）
		oldPat := pi977SeedPattern(t, tc.seed, tc.rule)
		oldRe, err := regexp.Compile(oldPat)
		if err != nil {
			t.Errorf("规则 %q 的种子 pattern 编译失败：%v", tc.rule, err)
			continue
		}
		hit := ""
		for _, s := range tc.fp {
			if oldRe.MatchString(s) {
				hit = s
				break
			}
		}
		if hit == "" {
			t.Errorf("规则 %q 的**种子** pattern（%s）一个普通文本样本都没命中"+
				"⇒ 负控失效：本门的「不命中」断言可能是空的（种子可能已被改过）",
				tc.rule, oldPat)
		}
		// ── `(?s)` 归检测器编译侧管：本件不得塞进去（否则与并行 Go 侧重复）──
		if strings.Contains(newPat, "(?s)") {
			t.Errorf("规则 %q 的新 pattern 含 `(?s)` ⇒ 跨行绕过的修法属于**检测器编译侧**"+
				"（Go 侧统一补）⇒ 本件塞进来会与并行改动重复、且两边职责混乱", tc.rule)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 工具
// ─────────────────────────────────────────────────────────────────────────────

// pi977RuleRuleNames 返回判据表里的规则名（报错用）。
func pi977RuleRuleNames() []string {
	out := make([]string, 0, len(pi977RuleCases))
	for _, tc := range pi977RuleCases {
		out = append(out, tc.rule)
	}
	return out
}

// pi977StripComments 剥掉 SQL 注释，得到**可执行面**。
//
// ★ 为什么必须有它：本文件通篇在解释缺陷（注释里必然出现
//
//	`RETURNING * INTO` / `auto_applied` 这类**被禁字符串**）⇒ 不剥注释的话
//	判据会把**说明文字**当成**实现**（这是本仓反复踩到的「扫的是代码还是散文」）。
func pi977StripComments(sql string) string {
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inQuote {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					b.WriteByte(sql[i+1])
					i++
					continue
				}
				inQuote = false
			}
			continue
		}
		if c == '\'' {
			inQuote = true
			b.WriteByte(c)
			continue
		}
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				break
			}
			i += 2 + j + 1
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// pi977FunctionBody 抽出 `CREATE OR REPLACE FUNCTION <name>` 的**函数体**
// （两个 `$$` 之间的原文）。
//
// ★ 为什么单独抽：文件里的 `COMMENT ON FUNCTION … IS '…'` 是一段**字符串
//
//	字面量**，它为了登记缺陷而提到了被禁的词 ⇒ 判据必须落在**函数体**上，
//	否则「说明文字」会被当成「实现」。
func pi977FunctionBody(t *testing.T, exec, funcName string) string {
	t.Helper()
	anchor := "CREATE OR REPLACE FUNCTION " + funcName
	idx := strings.Index(exec, anchor)
	if idx < 0 {
		t.Fatalf("可执行面里找不到 %s", anchor)
	}
	rest := exec[idx:]
	start := strings.Index(rest, "$$")
	if start < 0 {
		t.Fatalf("%s 的函数体没有 $$ 定界", funcName)
	}
	rest = rest[start+2:]
	end := strings.Index(rest, "$$")
	if end < 0 {
		t.Fatalf("%s 的函数体没有闭合的 $$", funcName)
	}
	return rest[:end]
}

// pi977SeedPattern 从种子迁移里取出某条规则的**原始 pattern**。
//
// ★ 种子行的字段数在 315（6 列）与 364（9 列，含 category_new / is_system /
//
//	tags）之间不同 ⇒ 不按固定下标取，而是取「第一个以 `(?` 开头的字段」
//	⇒ 那就是 pattern（两条种子的 L2 pattern 都以 `(?i)` 开头）。
func pi977SeedPattern(t *testing.T, path, ruleName string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读种子文件 %s：%v", path, err)
	}
	prefix := "('" + ruleName + "',"
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		fields := pi977SplitFields(trimmed)
		for _, f := range fields {
			if strings.HasPrefix(f, "(?") {
				return f
			}
		}
		t.Fatalf("种子文件 %s 里规则 %q 的行没有以 `(?` 开头的 pattern 字段：%q",
			path, ruleName, trimmed)
	}
	t.Fatalf("种子文件 %s 里找不到规则 %q 的种子行", path, ruleName)
	return ""
}

// pi977SplitFields 按**引号感知**的方式切分种子行。
//
// ★ 必须引号感知：977 的新 pattern 里含逗号（语境词表）⇒ 裸 strings.Split(",")
//
//	会把一条 pattern 切成碎片 ⇒ 断言会变成「碎片对碎片」的空比对。
func pi977SplitFields(line string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inQuote {
			if c == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					cur.WriteByte('\'')
					i++
					continue
				}
				inQuote = false
				continue
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\'':
			inQuote = true
		case ',':
			fields = append(fields, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if last := strings.TrimSpace(cur.String()); last != "" {
		fields = append(fields, last)
	}
	// 首字段带行首的 `(` ⇒ 去掉。
	if len(fields) > 0 {
		fields[0] = strings.TrimSpace(strings.TrimPrefix(fields[0], "("))
	}
	return fields
}
