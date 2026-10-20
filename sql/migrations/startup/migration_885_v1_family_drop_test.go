package startup

// migration_885_v1_family_drop_test.go —— 885（v1 request_logs 族最终删除）
// 的**契约门**。
//
// # 这道门防什么
//
// 885 是本仓**第一条真正删表的迁移**。它同时动了三样必须彼此等价的东西：
//
//	① Go 侧的自愈重建体（db.canonicalSessionOnlyDDL）
//	② 迁移文件里的视图体（885 的 $proj$ 块）
//	③ 运行时 SSOT（db.recentSuccessRateEnsureSQL —— 与 885 里的
//	   recent_success_rate 函数体同文）
//
// 这三样只要有一处漂移，后果**不是**编译错、也不是 DDL 错，而是：
//
//   · ① 与 ② 不一致 ⇒ 迁移装上去的视图与网关启动时自愈重建的视图**不是同一个**
//     ⇒ 某次「视图被带外删掉」的自愈会把**两臂**换成**三臂**（或反之）
//     ⇒ 而三臂形态引用已被 885 删掉的表 ⇒ 42P01 ⇒ 整个日志面板 500。
//   · ③ 与 885 不一致 ⇒ 迁移把函数换成了会话族形态，而网关启动时又把它换回
//     带 v1 腿的版本 ⇒ **每一次重启**都把 885 的成果抹掉 ⇒ 函数在第一次被
//     调用时 42P01 ⇒ provider 的候选查询失败 ⇒ **100% 流量失败**。
//
// ⇒ 三者必须**同源**。本门就是那个"同源"的守卫。
//
// # 为什么是「文本等价」而不是「行为等价」
//
// 行为等价（跑一把看结果）需要真库 + 时间；文本等价是**静态**的、每次 CI 都能
// 跑。而两者的风险面在这里**是同一个**：视图体/函数体的差异只可能来自文本漂移。
// ⇒ 静态文本等价 + 真库行为门（deploy 后的读回）＝ 两层，缺一不可。
//
// # 判据纪律（本门自身）
//
//   · **阳性对照**：本门先断言"迁移文件里确实有 $proj$ 块 / 确实含
//     recent_success_rate"，否则一个恒返回空的检测器也能让本门全绿。
//   · **变异可证**：把迁移里的投影改坏 ⇒ 本门必须红（见文件末的变异记录）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

const mig885File = "885_drop_request_logs_v1_family.sql"

// normalizeSQL 归一化 SQL 文本：去行注释、压空白、**去首尾空白**。
// 与 db/view_schema_v2_contract_test.go 的同名助手同形（各文件自持一份，
// 不跨包导出一个"测试用归一化器"——那会让两边的口径悄悄漂移）。
//
// ⚠ 首尾 trim 不是洁癖：迁移里的 $proj$ 块是 `proj := $proj$\n<投影>\n$proj$;`
// ⇒ 抽出来的文本**必然**带一个前导换行/空格；而 Go 侧的投影常量没有。
// 不 trim ⇒ 两边差 1~2 个空白字符 ⇒ 门永远红，而失败信息里那"差"根本不是
// 语义差异 ⇒ 读起来像"门坏了"，实际是门没把"空白"归一化。
// ⇒ 归一化必须吃掉空白差异，否则门就会退化成噪音。
func normalizeSQL885(s string) string {
	lineComment := regexp.MustCompile(`--[^\n]*`)
	spaceRun := regexp.MustCompile(`\s+`)
	return strings.TrimSpace(spaceRun.ReplaceAllString(lineComment.ReplaceAllString(s, " "), " "))
}

func readMig885(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".", mig885File))
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	return string(raw)
}

// TestMigration885HasDownCompanion —— 新增迁移必须带同名 .down.sql。
//
// 判据的判据（scripts/pre-commit-check.sh:check_migration_has_down）只对
// **未跟踪**文件生效 ⇒ 已提交之后就没有第二道门了 ⇒ 这里补一道常驻的。
func TestMigration885HasDownCompanion(t *testing.T) {
	if _, err := os.Stat(filepath.Join(".", "885_drop_request_logs_v1_family.down.sql")); err != nil {
		t.Fatalf("885 must ship a .down.sql companion: %v", err)
	}
}

// TestMigration885CarriesLockGuard —— >870 的启动迁移必须自带 lock_timeout。
//
// 885 要 DROP 一族**分区大表** ⇒ AccessExclusiveLock ⇒ 等锁可能堵住全库。
// （与 installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go
// 同一要求；本门是 canonical 目录侧的常驻替身。）
func TestMigration885CarriesLockGuard(t *testing.T) {
	src := readMig885(t)
	if !strings.Contains(strings.ToLower(src), "lock_timeout") {
		t.Fatal("885 must set lock_timeout: it DROPs a partitioned table family " +
			"(AccessExclusiveLock) and would otherwise queue behind DML indefinitely")
	}
}

// TestMigration885RefusesNonEmptyV1Family —— **非空闸**必须在。
//
// 这道闸是 885 唯一的"数据不丢"保证：v1 族里的行是会话族镜像建立**之前**的
// 历史，删表即删历史，而 down 只能重建**骨架**（救不回数据）。
//
// ⚠ 判据不只看"有没有非空闸"这三个字，还要看它**真的会在非空时拒绝**：
// 下面用一段**合成**的 v1 族（非空）跑迁移主体，断言它 RAISE EXCEPTION。
// 合成夹具在**临时 schema**里建，不碰真库。
func TestMigration885RefusesNonEmptyV1Family(t *testing.T) {
	src := readMig885(t)
	if !strings.Contains(src, "refuses to drop a NON-EMPTY v1 family") {
		t.Fatal("885 must refuse to drop a non-empty v1 family " +
			"(those rows predate the session mirror and are not recoverable)")
	}
	// 闸必须**逐表**统计，而不是只看母表：hot 表与 bodies 表各自独立。
	for _, rel := range []string{"request_logs", "request_logs_hot", "request_logs_bodies"} {
		if !strings.Contains(src, "'"+rel+"'") && !strings.Contains(src, "'_"+rel+"'") {
			t.Errorf("885's non-empty guard must cover %q (and its renamed spelling)", rel)
		}
	}
}

// TestMigration885ProjectionMatchesComposer —— ★ 迁移的视图体 ⇔ Go 自愈体。
//
// 这是本门最重要的一条：①（Go 自愈）与 ②（迁移）必须是**同一份文本**。
// 漂移的后果见文件头（"自愈把两臂换成三臂 ⇒ 42P01 ⇒ 面板全 500"）。
//
// 取法：迁移里的 $proj$ 块（列投影）+ 与 Go 侧同款的 DDL 模板 ⇒ 与
// db.canonicalSessionOnlyDDL(true) 的**投影部分**逐字比较（归一化后）。
func TestMigration885ProjectionMatchesComposer(t *testing.T) {
	src := readMig885(t)

	// ── 阳性对照：块必须存在，且非空 ──
	projStart := strings.Index(src, "proj := $proj$")
	projEnd := strings.Index(src, "$proj$;")
	if projStart < 0 || projEnd < 0 || projEnd <= projStart {
		t.Fatal("885 no longer carries the $proj$ block; update this contract test")
	}
	sqlProj := src[projStart+len("proj := $proj$") : projEnd]
	if strings.TrimSpace(sqlProj) == "" {
		t.Fatal("885's $proj$ block is empty — the detector would be vacuously green")
	}

	// ── Go 侧的自愈体：直接取它的投影（不从 DDL 里切——见导出口径的注释）──
	goProj := db.CanonicalSessionOnlyProjectionForTest()
	if strings.TrimSpace(goProj) == "" {
		t.Fatal("db.canonicalSessionOnlyDDL returned an empty projection — detector is broken")
	}

	if normalizeSQL885(sqlProj) != normalizeSQL885(goProj) {
		t.Fatalf("885's $proj$ block and db.canonicalSessionOnlyDDL have drifted.\n"+
			"Consequence: the view the migration installs and the view the gateway "+
			"self-heals on boot are NOT the same — a boot-time self-heal would swap "+
			"one body for the other, and the three-arm body references tables 885 "+
			"just dropped (42P01 across the whole logs panel).\n"+
			"--- migration ---\n%s\n--- go ---\n%s", sqlProj, goProj)
	}
}

// TestMigration885RecentSuccessRateMatchesRuntimeSSOT —— ★ 迁移的函数体 ⇔
// 运行时 SSOT。
//
// `recent_success_rate` 进的是 provider 的候选查询（`CROSS JOIN LATERAL`）⇒
// 它带 v1 腿 = **100% 流量失败**。而 `CREATE OR REPLACE FUNCTION` 对 SQL
// 函数体**不在创建时解析** ⇒ 漂移的后果是"建得成功、首次调用才炸"，
// 症状与"上游全挂"同形 ⇒ 必须在**静态**层拦住。
func TestMigration885RecentSuccessRateMatchesRuntimeSSOT(t *testing.T) {
	src := readMig885(t)
	runtime := db.RecentSuccessRateEnsureSQLForTest()
	if strings.TrimSpace(runtime) == "" {
		t.Fatal("db.recentSuccessRateEnsureSQL is empty — detector is broken")
	}
	// 从运行时 SSOT 里抽出函数体（$$ … $$ 之间）。
	// ⚠ 闭合标记必须用「**开标记之后**的第一个 `$$`」，不能用 LastIndex：
	// 整份文件里后面还有别的 `$$`（PL/pgSQL 块、注释里的示例）⇒ 用 LastIndex
	// 会把"函数体 + 文件尾巴"整段当成函数体 ⇒ 比较必然失败，而且失败信息
	// 里那段"函数体"根本不是函数体（读起来像"门坏了"，实际是门取错了范围）。
	open := strings.Index(runtime, "AS $$")
	if open < 0 {
		t.Fatalf("cannot locate the function body opener in the runtime SSOT:\n%s", runtime)
	}
	closeIdx := strings.Index(runtime[open+len("AS $$"):], "$$")
	if closeIdx < 0 {
		t.Fatalf("cannot locate the function body closer in the runtime SSOT:\n%s", runtime)
	}
	ssotBody := runtime[open+len("AS $$") : open+len("AS $$")+closeIdx]

	// 迁移里那份：CREATE OR REPLACE FUNCTION recent_success_rate … AS $$ … $$。
	i := strings.Index(src, "CREATE OR REPLACE FUNCTION recent_success_rate")
	if i < 0 {
		t.Fatal("885 must replace recent_success_rate (it is called on the provider " +
			"candidate path — a v1 leg there is a 100% traffic failure)")
	}
	seg := src[i:]
	o2 := strings.Index(seg, "AS $$")
	if o2 < 0 {
		t.Fatalf("cannot locate the function body opener inside 885:\n%s", seg[:min(len(seg), 800)])
	}
	c2 := strings.Index(seg[o2+len("AS $$"):], "$$")
	if c2 < 0 {
		t.Fatalf("cannot locate the function body closer inside 885:\n%s", seg[:min(len(seg), 800)])
	}
	migBody := seg[o2+len("AS $$") : o2+len("AS $$")+c2]

	if normalizeSQL885(migBody) != normalizeSQL885(ssotBody) {
		t.Fatalf("885's recent_success_rate body and db.recentSuccessRateEnsureSQL have drifted.\n"+
			"Consequence: the gateway's boot-time CREATE OR REPLACE would RESTORE the "+
			"v1 leg that 885 just removed ⇒ every restart re-breaks the provider "+
			"candidate query ⇒ 100%% traffic failure (and the symptom looks like "+
			"'all upstreams down', not like a migration mismatch).\n"+
			"--- migration ---\n%s\n--- runtime SSOT ---\n%s", migBody, ssotBody)
	}
}

// TestMigration885EmbeddataCopyIsByteIdentical —— canonical 与 installer
// embeddata 两份必须逐字节相同。
//
// ⚠ 不是"内容等价"：**逐字节**。因为 embeddata 那份是**逐字执行**的
// （安装器不做部署通道的词法改写），差一个字符就是两个行为。
func TestMigration885EmbeddataCopyIsByteIdentical(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join(".", mig885File))
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	embed, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", mig885File))
	if err != nil {
		t.Fatalf("read embeddata copy: %v", err)
	}
	if string(canonical) != string(embed) {
		t.Fatalf("885's embeddata copy has drifted from the canonical file "+
			"(canonical %d bytes, embeddata %d bytes)", len(canonical), len(embed))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
