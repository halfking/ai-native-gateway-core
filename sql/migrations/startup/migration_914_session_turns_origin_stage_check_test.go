// AUDIT-GUARD: sql/migrations/startup 已登记进 Makefile 的 GUARD_PACKAGES ⇒ 本门在 CI 的 `make guards-fast` 上真跑。
package startup

// migration_914_session_turns_origin_stage_check_test.go —— 914
// （会话族 origin_stage 的 DB 级 CHECK 补回）的**契约门**。
//
// # 这道门防什么
//
// v1 族的 `request_logs.origin_stage` 上有一条 DB 级 CHECK
// （`request_logs_origin_stage_check`，允许集 = 8 个探针 stage + `business`）。
// 它随 890 的 v1 族 DROP 一起消失，而**会话族从未重建**：
// `session_turns.origin_stage` / `session_turns_hot.origin_stage` 都是 `text`
// 且**无任何 CHECK**（PG 上 `pg_constraint` 实测 0 条）。
//
// ⇒ 落库前的闸门只剩 `middleware/origin_mw.go` 的 `isValidOriginStage`
// （Go 侧唯一守卫）⇒ **DB 完全不拒**。
// ⇒ 而消费方（`admin/routing.go`、`admin/analytics.go`、`bg/mv_consistency.go`、
// `bg/probe_policy.go`）一律用
// `COALESCE(origin_stage,'') NOT IN (8 个探针 stage)` **排除探针流量**
// ⇒ 一个越界值落进去 ⇒ 探针流量被算成业务流量 + 对账被污染，
// 而且**没有任何一层会报错**。
//
// # 为什么这道门不是「查文件里有没有某个字符串」
//
// 真正的风险不是「914 这个文件没了」，而是**允许集与 Go 侧真值源漂了**：
// `internal/probekind.ValidStages()` 是「DB CHECK 允许的全部取值」的唯一真值源
// （该函数自己的注释就是这么写的）。
// ⇒ 只要有人往 `probekind.ProbeStages` 加了一个新 stage 而没同步 914，
//   就会出现「中间件认、DB 拒」的行 —— 正是 `probekind.IsValidStage`
//   注释里明写要避免的那种状态。
// ⇒ 所以本门的正题是**两边集合必须逐项相等**（不是「包含」，不是「大致相同」）。
//
// # 判据纪律（本门自身）
//
//   · **阳性对照**：每个检测器先证明自己能命中（否则一个恒返回空的
//     提取器也能让本门全绿 —— §171 的「尺子不许复用尺子的返回值」）。
//   · **变异可证**：把 914 的允许集改坏 ⇒ 本门必须红（见文件末的变异记录）。
//   · **作用域与契约一致**：静态腿只管仓内产物；真库腿单独一跳，
//     没配 DSN 时**显式 skip 并打印**，不冒充「通过」。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

const (
	mig914File     = "914_session_turns_origin_stage_check.sql"
	mig914DownFile = "914_session_turns_origin_stage_check.down.sql"

	// 会话族两侧的约束名（父表 / hot）。
	// ★ 命名沿用会话族自己的约定（706b 把父表的 CHECK 克隆到 hot 并加
	//   `_hot_` 后缀；hot 的 bootstrap 也自带 8 个 `_hot_` 命名的 CHECK）。
	mig914ParentConstraint = "session_turns_origin_stage_check"
	mig914HotConstraint    = "session_turns_hot_origin_stage_check"
)

// readMig914 读 914 的 canonical 文件。
func readMig914(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".", mig914File))
	if err != nil {
		t.Fatalf("读 %s 失败: %v", mig914File, err)
	}
	return string(raw)
}

// stripSQLComments914 去行注释与块注释。
//
// ★ 为什么必须剥注释再抽字面量：914 的注释里**也**出现了 stage 名
// （例如解释「8 个探针 stage + business」的那几段）⇒ 不剥注释就会
// 把注释里的字面量一起抽进来 ⇒ 集合比对失真。
// ⇒ 而且注释是**人写的散文**，它的字面量集合会随措辞变 ⇒ 门会变成噪音。
func stripSQLComments914(s string) string {
	blockRe := regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineRe := regexp.MustCompile(`--[^\n]*`)
	out := blockRe.ReplaceAllString(s, " ")
	return lineRe.ReplaceAllString(out, " ")
}

// cCanonDeclRe 定位 914 里**唯一**的规范允许集声明。
//
// ★ 刻意锚在这个声明上，而不是「把文件里所有字面量都抽出来」：
//
//	文件里还有 `format('public.%I', …)`、`'origin_stage'` 等无关字面量，
//	全抽会让集合比对失去意义。
//
// ★ 锚点变了 ⇒ 提取器漂移 ⇒ 本门**报错**（而不是静默通过）。
var cCanonDeclRe = regexp.MustCompile(`(?s)c_canon\s+text\[\]\s*:=\s*ARRAY\[(.*?)\]`)

// literalRe 抽 SQL 单引号字面量。
var literalRe = regexp.MustCompile(`'([^']*)'`)

// extractMig914AllowedSet 从 914 的规范声明里抽出允许集。
func extractMig914AllowedSet(t *testing.T, body string) []string {
	t.Helper()
	clean := stripSQLComments914(body)
	m := cCanonDeclRe.FindAllStringSubmatch(clean, -1)
	if len(m) != 1 {
		t.Fatalf("914 里规范允许集的声明必须**恰好一处**（`c_canon text[] := ARRAY[…]`），实测 %d 处 ⇒ "+
			"提取器漂移或文件被改写；本门无法判定 914 与 Go 侧真值源是否一致（不要改本门的提取器去迁就文件，"+
			"要么把声明改回唯一一处，要么同步改本门并写明理由）。", len(m))
	}
	set := map[string]bool{}
	for _, lit := range literalRe.FindAllStringSubmatch(m[0][1], -1) {
		set[lit[1]] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedCopy 返回排序后的副本。
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestMigration914HasDownCompanion —— down 脚本必须在场（仓库纪律：每条迁移带同名 .down.sql）。
func TestMigration914HasDownCompanion(t *testing.T) {
	for _, f := range []string{mig914File, mig914DownFile} {
		if _, err := os.Stat(filepath.Join(".", f)); err != nil {
			t.Errorf("914 缺少文件 %s: %v", f, err)
		}
	}
}

// TestMigration914AllowedSetMatchesProbekindSSOT —— ★★ 本门的正题。
//
// 914 里声明的允许集必须与 `internal/probekind.ValidStages()` **逐项相等**。
// 允许集多一个少一个都判红（不是包含关系）：
//
//	· 少一个 ⇒ 该 stage 能落库但 DB 拒 ⇒ 「中间件认、DB 拒」；
//	· 多一个 ⇒ DB 认了一个 Go 侧不认的值 ⇒ 与「DB CHECK 允许集 == ValidStages」
//	  的设计意图相反，且探针排除逻辑会漏掉它。
func TestMigration914AllowedSetMatchesProbekindSSOT(t *testing.T) {
	body := readMig914(t)
	got := extractMig914AllowedSet(t, body)
	want := sortedCopy(probekind.ValidStages())

	// ★ 阳性对照：提取器必须真的读到了东西（否则「空集 == 空集」会让本门恒绿）。
	if len(want) == 0 {
		t.Fatalf("probekind.ValidStages() 返回空集 ⇒ 判据的基准侧坏了，本门无法判定任何东西")
	}
	if len(got) != len(want) {
		t.Errorf("914 的允许集与 internal/probekind.ValidStages() **项数不等**：\n"+
			"  914        (%d): %v\n  ValidStages (%d): %v\n"+
			"★ 两者必须**逐项相等**：少一个 ⇒ 「中间件认、DB 拒」；多一个 ⇒ DB 认了 Go 侧不认的值。",
			len(got), got, len(want), want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("914 的允许集与 internal/probekind.ValidStages() 不一致：\n"+
				"  914        (%d): %v\n  ValidStages (%d): %v\n"+
				"★ 改 probekind.ProbeStages 时必须同步改 914（或改 914 的守卫），"+
				"否则会出现「中间件认、DB 拒」的行。",
				len(got), got, len(want), want)
			return
		}
	}
	t.Logf("允许集逐项相等（%d 项）：%v", len(got), got)
}

// TestMigration914CoversBothSessionFamilyTables —— 父表与 hot 表**都要有**约束。
//
// ★ 为什么两侧都要：会话族的 hot 表是「近 8 小时的写入暂存区」，
// 冷行由 promote_session_turns_hot_to_partition 搬进父表的月分区。
// v1 族的 CHECK 只加在父表上（341 的原文）⇒ 越界值能先进 hot、
// 在 promote 时才炸 ⇒ 表现为「promote 任务反复失败」而不是「写入被拒」。
// 会话族的约定（706b）把父表的 CHECK 克隆到 hot 并改名 ⇒ 914 照此办理：
// **越界值在写入那一刻就被拒**，而不是等到 promote。
//
// ★★ 判据用**结构化解析**而不是 `strings.Contains`：
//
//	子串包含会把 `session_turns_hot_origin_stage_check_disabled` 判成
//	「提到了 session_turns_hot_origin_stage_check」⇒ 门变成摆设。
//	⇒ 本门解析 c_targets 里的 (表, 约束名) 对，**逐对精确比对**。
func TestMigration914CoversBothSessionFamilyTables(t *testing.T) {
	body := stripSQLComments914(readMig914(t))

	pairs := extractMig914TargetPairs(t, body)
	want := map[string]bool{
		"session_turns|" + mig914ParentConstraint:  true,
		"session_turns_hot|" + mig914HotConstraint: true,
	}
	if len(pairs) != len(want) {
		t.Errorf("914 的目标清单有 %d 项，期望 %d 项：%v\n"+
			"★ 会话族两侧（父表 + hot）都必须被覆盖", len(pairs), len(want), pairs)
	}
	for _, p := range pairs {
		if !want[p] {
			t.Errorf("914 的目标清单里有**不该有**的一项 %q", p)
			continue
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("914 的目标清单里**缺** %q ⇒ 会话族有一侧没有约束", p)
	}
}

// cTargetsDeclRe 定位 914 的目标清单声明。
var cTargetsDeclRe = regexp.MustCompile(`(?s)c_targets\s+text\[\]\[\]\s*:=\s*ARRAY\[(.*?)\n\s*\];`)

// arrayPairRe 抽出 `ARRAY['表', '约束名']` 形态的一对。
var arrayPairRe = regexp.MustCompile(`ARRAY\[\s*'([^']*)'\s*,\s*'([^']*)'\s*\]`)

// extractMig914TargetPairs 抽出 914 的 (表, 约束名) 对。
func extractMig914TargetPairs(t *testing.T, body string) []string {
	t.Helper()
	m := cTargetsDeclRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("914 里找不到目标清单声明（`c_targets text[][] := ARRAY[…];`）⇒ 提取器漂移或文件被改写")
	}
	ms := arrayPairRe.FindAllStringSubmatch(m[1], -1)
	if len(ms) == 0 {
		t.Fatalf("914 的目标清单里解析不出 (表, 约束名) 对 ⇒ 提取器漂移")
	}
	out := make([]string, 0, len(ms))
	for _, p := range ms {
		out = append(out, p[1]+"|"+p[2])
	}
	sort.Strings(out)
	return out
}

// TestMigration914UsesDefinitionAwareIdempotentGuard —— 幂等必须是**定义感知**的。
//
// ★ 两种幂等写法只有一种是对的：
//
//	· `ADD CONSTRAINT`（无 IF NOT EXISTS）⇒ 重放会报 "already exists" ⇒ 部署第二次就挂；
//	· 先 DROP 再 ADD ⇒ 重放会**再扫一遍全表、再抢一次强锁** ⇒ 每次重放都付全款。
//
// ⇒ 正解：读现状定义 → 已是规范形态就跳过 → 否则才重建。
// ⇒ 本门钉住这套标记，缺一即判红。
//
// ★★ 判据用**词边界**（`\b`）而不是裸子串：
//
//	`VALIDATE CONSTRAINT_DISABLED` 里仍然**含有** `VALIDATE CONSTRAINT`
//	⇒ 裸子串判据会把「把 VALIDATE 改坏」这个回归判成绿（实测踩过）。
//	⇒ `\b` 要求标记后面是非单词字符 ⇒ `_DISABLED` 这种后缀骗不过去。
func TestMigration914UsesDefinitionAwareIdempotentGuard(t *testing.T) {
	body := stripSQLComments914(readMig914(t))
	required := []struct {
		marker string
		why    string
	}{
		{`pg_get_constraintdef\b`, "读现状定义 ⇒ 判定「已是规范形态」"},
		{`regexp_matches\b`, "抽出定义里的字面量集合 ⇒ 与规范集对拍"},
		{`DROP\s+CONSTRAINT\s+IF\s+EXISTS\b`, "定义不对时才重建"},
		{`NOT\s+VALID\b`, "第一步只加约束不扫存量 ⇒ 新行立刻受约束、写入不被堵"},
		{`VALIDATE\s+CONSTRAINT\b`, "第二步校验存量 ⇒ 越界行必须让迁移失败而不是被放过"},
	}
	for _, r := range required {
		re, err := regexp.Compile(r.marker)
		if err != nil {
			t.Fatalf("编译判据正则 %q 失败: %v", r.marker, err)
		}
		if !re.MatchString(body) {
			t.Errorf("914 缺少 %s（%s）⇒ 幂等/守卫形态不对", r.marker, r.why)
		}
	}

	// ★ 阳性对照：判据必须能识别「正确形态」，也必须**认得出被改坏的形态**。
	//   一个恒真的正则（哪怕在坏样本上也匹配）会让上面那条断言变成摆设。
	for _, bad := range []string{
		"VALIDATE CONSTRAINT_DISABLED",
		"NOT VALID_DISABLED",
		"DROP CONSTRAINT IF EXISTS_DISABLED",
	} {
		re := regexp.MustCompile(`VALIDATE\s+CONSTRAINT\b`)
		if bad == "NOT VALID_DISABLED" {
			re = regexp.MustCompile(`NOT\s+VALID\b`)
		}
		if bad == "DROP CONSTRAINT IF EXISTS_DISABLED" {
			re = regexp.MustCompile(`DROP\s+CONSTRAINT\s+IF\s+EXISTS\b`)
		}
		if re.MatchString(bad) {
			t.Errorf("★ 检测器阳性对照失败：%q 被误判成合规形态 ⇒ 本门的形态断言不可信", bad)
		}
	}
}

// TestMigration914CarriesEffectiveLockGuard —— ★★ 锁守卫必须**真的生效**。
//
// ★★ 本轮实测发现（本文件的核心动机之一）：
//
//	`SET LOCAL` 只在事务块内有效。
//	部署通道 scripts/apply-db-revision-sequence.sh 走 `psql -f -`，
//	**不加 --single-transaction**、也不注入 BEGIN ⇒ 每个语句各自成事务
//	⇒ `SET LOCAL lock_timeout` 只打一条 WARNING 后**完全失效**
//	（PG 17.6 实测：`SET LOCAL lock_timeout='123ms'` 之后
//	  `current_setting('lock_timeout')` 仍是 `0`）。
//	（installer 通道走 `--single-transaction` ⇒ 那条路径上 SET LOCAL 才有效。）
//
// ⇒ 914 因此用**会话级** `SET lock_timeout` ⇒ 两条通道都真的生效。
// ⇒ 本门把这条形态钉住，防止后人「照着邻近迁移的样子」改回 `SET LOCAL`。
//
// ★ 为什么锁守卫要紧：PG 的锁队列里，**等锁期间新事务会排在它后面**
// ⇒ 一条迟迟拿不到 AccessExclusive 的 ALTER 会把整张表的写入一起堵住
// （§44 的生产事故：一条 CREATE TABLE 堵了 14 个网关会话 1.5 分钟）。
func TestMigration914CarriesEffectiveLockGuard(t *testing.T) {
	body := stripSQLComments914(readMig914(t))

	if !strings.Contains(strings.ToLower(body), "lock_timeout") {
		t.Errorf("914 没有 lock_timeout ⇒ 它会在等锁期间堵住全库（§44）")
	}
	// ★ 会话级形态：`SET lock_timeout` 且**不是** `SET LOCAL lock_timeout`。
	sessionLevel := regexp.MustCompile(`(?im)^\s*SET\s+lock_timeout\s*=`)
	localOnly := regexp.MustCompile(`(?im)^\s*SET\s+LOCAL\s+lock_timeout\s*=`)
	if !sessionLevel.MatchString(body) {
		t.Errorf("914 没有**会话级** `SET lock_timeout` ⇒ 锁守卫在部署通道上是装饰品\n" +
			"（部署通道 psql -f - 不加 --single-transaction ⇒ SET LOCAL 会失效）")
	}
	if localOnly.MatchString(body) {
		t.Errorf("914 用了 `SET LOCAL lock_timeout` ⇒ 它在部署通道（psql -f -，无 --single-transaction）上失效\n" +
			"★ 本轮在 PG 17.6 实测：SET LOCAL 之后 current_setting('lock_timeout') 仍是 0")
	}

	// ★ 阳性对照：形态检测器本身必须能区分两种写法。
	//   一个恒假的正则会让上面两条断言永远通过 ⇒ 门变成装饰品。
	if !sessionLevel.MatchString("SET lock_timeout = '2s';") {
		t.Errorf("★ 检测器阳性对照失败：会话级写法没有被识别 ⇒ 本门的形态断言恒假（门是装饰品）")
	}
	if localOnly.MatchString("SET lock_timeout = '2s';") {
		t.Errorf("★ 检测器阳性对照失败：会话级写法被误判成 SET LOCAL ⇒ 本门的形态断言不可信")
	}
	if !localOnly.MatchString("SET LOCAL lock_timeout = '2s';") {
		t.Errorf("★ 检测器阳性对照失败：SET LOCAL 写法没有被识别 ⇒ 本门抓不到「改回 SET LOCAL」这个回归")
	}
}

// TestMigration914EmbeddataCopiesAreByteIdentical —— installer 的 embeddata 副本必须逐字相同。
func TestMigration914EmbeddataCopiesAreByteIdentical(t *testing.T) {
	repoRoot := "../../.."
	for _, f := range []string{mig914File, mig914DownFile} {
		canonical, err := os.ReadFile(filepath.Join(".", f))
		if err != nil {
			t.Fatalf("读 canonical %s 失败: %v", f, err)
		}
		embedded, err := os.ReadFile(filepath.Join(repoRoot,
			"installer", "cmd", "llm-gw-installer", "embeddata", "startup", f))
		if err != nil {
			t.Fatalf("读 embeddata 副本 %s 失败: %v", f, err)
		}
		if string(canonical) != string(embedded) {
			t.Errorf("%s 的 canonical 与 embeddata 副本**不是逐字相同** ⇒ 装出来的库与升级库会不一致", f)
		}
	}
}

// TestMigration914IsDeliveredByEverySurface —— 两条**安装器门管不到**的投递面。
//
// ★ 为什么单独钉：installer 的门（`TestCanonicalStartupMigrationsAtOrAbove704AreRegistered`
//
//	/ `TestStartupManifestMatchesStartupFiles` / `TestInstallerEmbedStartupMigrationsByteMatchCanonical`）
//	只覆盖 embeddata + StartupFiles + 启动清单 TSV；
//	而**升级通道**（scripts/apply-db-revision-sequence.sh 的 files=()）
//	与**台账**（docs/db-changelog.md）各有各的门，但那些门是「扫全体」
//	的形态（bash 门只看**号最大的**那条迁移在不在通道里；
//	台账门只看「磁盘上最大编号的近邻窗口」）。
//
// ⇒ 一条**中间的**迁移漏投递，两道门都可能看不见 ⇒ 这里显式钉住。
func TestMigration914IsDeliveredByEverySurface(t *testing.T) {
	// ① 升级通道 files=()
	seq, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	if !strings.Contains(string(seq), "sql/migrations/startup/"+mig914File) {
		t.Errorf("升级通道 scripts/apply-db-revision-sequence.sh 的 files=() 里没有 %s ⇒ 升级库拿不到这条迁移", mig914File)
	}

	// ② 台账 docs/db-changelog.md（sha 行）
	reg, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "db-changelog.md"))
	if err != nil {
		t.Fatalf("读 db-changelog.md 失败: %v", err)
	}
	rowRe := regexp.MustCompile(`(?m)^\|\s+914\s+\|\s+\x60` + regexp.QuoteMeta(mig914File) + `\x60\s+\|\s+\x60([0-9a-f]{64})\x60\s+\|`)
	if !rowRe.MatchString(string(reg)) {
		t.Errorf("docs/db-changelog.md 里没有 %s 的登记行（| 914 | `文件` | `sha` | … |）⇒ 台账门会报「未登记」", mig914File)
	}

	// ③ 启动清单 TSV（0 基序号）
	manifest, err := os.ReadFile(filepath.Join("..", "..", "..", "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	// ★ 要求**精确**的 `<序号>\t<文件名>` 形态（不是"行尾含这个文件名"）。
	tsvRowRe := regexp.MustCompile(`(?m)^\d+\t` + regexp.QuoteMeta(mig914File) + `\s*$`)
	if !tsvRowRe.MatchString(string(manifest)) {
		t.Errorf("sql/schema/installed_startup_migrations.tsv 里没有 %s 的精确登记行（`<序号>\\t<文件名>`）⇒ 安装器的启动清单与实际不一致", mig914File)
	}
}

// retiredOriginStageAllowlist 是「允许引用**退役形态**的名字」的显式豁免表。
//
// ★ 为什么要有豁免表：R175 轮修的那几处注释，**必须逐字引用旧文案**
// （否则读者看不出改了什么、也不知道旧文案错在哪）⇒ 豁免。
// ★ 仓内既有惯例同款（见 installer/cmd/llm-gw-installer/
//
//	startup_ddl_lock_guard_gate_test.go 的 lockGuardAllowlist）：
//	**显式 map + 每条都写清理由**，而不是"没命中就跳过"。
//
// ★ 豁免的**附加条件**：被豁免的文件必须同时提到**现役**约束名
//
//	（session_turns_origin_stage_check）或现役迁移文件名
//	⇒ 「引用旧名」必须与「指出现役形态」成对出现。
var retiredOriginStageAllowlist = map[string]string{
	"middleware/origin_mw.go":         "R175 轮的订正说明本身要逐字引用旧文案（「matches the additive CHECK on _request_logs.origin_stage (see migration 341)」），否则读者看不出改的是什么。",
	"internal/probekind/probekind.go": "同上：IsValidStage 的订正说明要逐字引用旧文案（「能落进 _request_logs.origin_stage」「DB CHECK（migration 341）用的就是这份集合」）。",
}

// TestNoGoSourceStillClaimsTheRetiredOriginStageCheck —— ★ 注释也是事实源。
//
// v1 族退役之后，Go 源码里有**十几处注释**仍在描述那个**已经不存在的**约束：
//
//	· `middleware/origin_mw.go` 的 isValidOriginStage 注释写着
//	  「matches the additive CHECK on _request_logs.origin_stage (see migration 341)」
//	  —— 表不存在、CHECK 不存在、迁移号还指错了文件（今天的 startup 链里
//	  `341_` 是 `341_hot_table_independence.sql`）。
//	· `internal/probekind/probekind.go` 的 IsValidStage 注释写着
//	  「能落进 _request_logs.origin_stage」「DB CHECK（migration 341）用的就是这份集合」
//	  —— 同上三处皆错。
//
// ★★ 这类注释的伤害不是"过时"，而是**把后来人引向错误的修改方向**：
//
//	本仓自己记过这条形态（「注释说一套、代码做另一套」是第 6 条已知形态）。
//	比如有人照着「DB CHECK 在 _request_logs 上」去 v1 族里找约束，
//	或者照着「migration 341」去 startup 链里找文件 —— 两者都不存在。
//
// ⇒ 本门钉住两条**精确**的判据（不做「提到 origin_stage + CHECK 就怎样」那种
//
//	粗判据 —— 实测会把 `node_probe_runs` / `credential_probe_queue` 的
//	**别的**表的 CHECK 一并误报，5 条假阳性）：
//	① **表限定形态** `request_logs.origin_stage`（含 `_request_logs.origin_stage`）
//	   不许出现在任何非测试 Go 文件里 —— v1 族已真删除，这个限定名指向一张
//	   不存在的表上的列；豁免表里的文件必须**同时**提到现役形态。
//	② ★★ **hot 表的限定名** `request_logs_hot.origin_stage`（★★ 本轮补上的真洞：
//	   `_hot.` 插在中间 ⇒ 它**不是** ① 的子串 ⇒ 旧版门管不到它）。
//	③ **退役的约束名** `request_logs_origin_stage_check` 同理。
//
// ★★ 另有一条**阴性对照**：★ 现役形态（`session_turns*.origin_stage`）**不许**
// ★ 被这些标记命中 ⇒ ★ 判据必须只认【已退役】的限定名 ⇒ 门要能【分得清】。
func TestNoGoSourceStillClaimsTheRetiredOriginStageCheck(t *testing.T) {
	root := "../../.."

	// ★ 阳性对照：豁免表不许为空（否则「一条都不命中」会伪装成通过）。
	if len(retiredOriginStageAllowlist) == 0 {
		t.Fatalf("★ 豁免表是空的 ⇒ 本门在本仓状态下会恒绿（没有任何退役形态被检出）")
	}
	for path, why := range retiredOriginStageAllowlist {
		if strings.TrimSpace(why) == "" {
			t.Errorf("豁免表条目 %q 没有写理由 ⇒ 豁免必须说明为什么", path)
		}
	}

	// 退役形态的精确标记。
	//
	// ★★ 本轮自审订正之五：★ 旧版只有两条标记 ⇒ ★★ **漏掉了 hot 表的限定名**
	// `request_logs_hot.origin_stage`（★ `_hot.` 插在中间 ⇒ 它**不是**
	// `request_logs.origin_stage` 的子串 ⇒ ★★ 旧版门**管不到**它 ⇒ ★★ 那是一个**真洞**）。
	// ★ 实测全仓 `request_logs_hot.origin_stage` 命中 **0** ⇒ ★★ 补这条标记的**误报面为 0**。
	//
	// ★★ 另注：★ `_request_logs.origin_stage` 之所以被旧版覆盖，是**子串偶然**命中
	// （★ `_request_logs.origin_stage` 里**含有** `request_logs.origin_stage`）⇒ ★★ 那不是判据，是巧合。
	markers := []string{
		"request_logs.origin_stage",       // 表限定形态
		"request_logs_hot.origin_stage",   // ★★ hot 表的限定名（旧版真洞，本轮补上）
		"request_logs_origin_stage_check", // 退役的约束名
	}

	// ★★ 阴性对照：★ 现役形态**不许**被这条门误伤（★ 门要能【分得清】）。
	// ★ 这条是「反过来的变异臂」：★ 把标记写宽到能命中现役形态 ⇒ 这里必须红。
	for _, live := range []string{
		"session_turns.origin_stage",
		"session_turns_hot.origin_stage",
	} {
		for _, m := range markers {
			if strings.Contains(live, m) {
				t.Errorf("★ 标记 %q **误伤现役形态** %q\n"+
					"⇒ ⇒ 门会把正确的注释判红 ⇒ 判据太宽 ⇒ 请收窄到【已退役】的限定名。", m, live)
			}
		}
	}

	for _, path := range collectGoFiles(t, root) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", path, err)
		}
		s := string(body)
		hit := ""
		for _, m := range markers {
			if strings.Contains(s, m) {
				hit = m
				break
			}
		}
		if hit == "" {
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if why, ok := retiredOriginStageAllowlist[rel]; ok {
			// ★ 豁免的附加条件：必须同时提到现役形态（否则这不是「订正说明」，
			//   而是一句还在生效的错误断言）。
			if !strings.Contains(s, "session_turns_origin_stage_check") &&
				!strings.Contains(s, "914_session_turns_origin_stage_check") {
				t.Errorf("%s 被豁免（%s），但它**没有**提到现役约束名/现役迁移文件名\n"+
					"⇒ 豁免的前提不成立：只引用旧形态而不指出现役形态，仍是误导。", rel, why)
			}
			continue
		}
		t.Errorf("%s 里出现**已退役**的形态 %q\n"+
			"⇒ v1 族已随 890 真删除（表不存在、CHECK 不存在、迁移号 341 在 startup 链里\n"+
			"   指的是 341_hot_table_independence.sql）⇒ 这句注释描述的是一个不存在的对象。\n"+
			"★ 修法：指向现役约束 session_turns_origin_stage_check（迁移 914）；\n"+
			"   若本文件就是要做这件事的**订正说明**，请把文件路径登记进\n"+
			"   retiredOriginStageAllowlist 并写明理由。", rel, hit)
	}
}

// TestCorrectedCommentsPointAtTheLiveConstraint —— ★ 订正后的注释必须**指出现役形态**。
//
// ★ 只查「不许说错」不够：注释被清空成「（已修正）」也算过门，
// 而下一个读代码的人拿不到任何指针。⇒ 这条门要求那两个真正改过的文件
// **点名现役约束/现役迁移**。
func TestCorrectedCommentsPointAtTheLiveConstraint(t *testing.T) {
	root := "../../.."
	for _, rel := range []string{
		"middleware/origin_mw.go",
		"internal/probekind/probekind.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		s := string(body)
		if !strings.Contains(s, "session_turns_origin_stage_check") &&
			!strings.Contains(s, "914_session_turns_origin_stage_check") {
			t.Errorf("%s 里没有提到现役约束名/现役迁移文件名 ⇒ 订正后的注释没有给读者任何指针", rel)
		}
	}
}

// collectGoFiles 枚举仓内所有非测试 Go 文件（跳过 vendor / node_modules / 第三方目录）。
func collectGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "node_modules", ".git", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("在 %s 下一个 Go 文件都没枚举到 ⇒ 提取器漂移，本门会恒绿", root)
	}
	sort.Strings(out)
	return out
}

// TestSessionTurnsOriginStageCheckOnLiveDB —— 真库腿：约束真的在库里吗？
// ★ 口径与 Go 侧真值源对拍：从 `pg_get_constraintdef` 抽字面量集合，
//
//	与 `probekind.ValidStages()` 逐项相等；且 convalidated 必须为 true
//	（NOT VALID 的约束**拦得住新行**，但存量没被校验过 ⇒ 不能算「已就位」）。
func TestSessionTurnsOriginStageCheckOnLiveDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿（本腿纯只读，可指向任何库）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	defer pool.Close()

	want := sortedCopy(probekind.ValidStages())
	litRe := regexp.MustCompile(`'([^']*)'`)

	for _, c := range []struct {
		table      string
		constraint string
	}{
		{"session_turns", mig914ParentConstraint},
		{"session_turns_hot", mig914HotConstraint},
	} {
		var def string
		var validated bool
		err := pool.QueryRow(ctx,
			`SELECT pg_get_constraintdef(oid), convalidated
			   FROM pg_constraint
			  WHERE conrelid = to_regclass($1) AND conname = $2 AND contype = 'c'`,
			"public."+c.table, c.constraint).Scan(&def, &validated)
		if err != nil {
			t.Errorf("%s 上没有约束 %s（%v）⇒ 914 未落到这个库 ⇒ origin_stage 在 DB 层完全不被拒", c.table, c.constraint, err)
			continue
		}
		if !validated {
			t.Errorf("%s 的约束 %s 的 convalidated = false ⇒ 它是 NOT VALID 形态：存量行没被校验过 ⇒ 不算「已就位」", c.table, c.constraint)
		}
		set := map[string]bool{}
		for _, m := range litRe.FindAllStringSubmatch(def, -1) {
			set[m[1]] = true
		}
		got := make([]string, 0, len(set))
		for k := range set {
			got = append(got, k)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s 的约束 %s 的允许集与 internal/probekind.ValidStages() 不一致：\n"+
				"  库上 (%d): %v\n  Go 侧 (%d): %v\n"+
				"★ 落库前的闸门与落库后的约束必须一致。",
				c.table, c.constraint, len(got), got, len(want), want)
		}
	}
}
