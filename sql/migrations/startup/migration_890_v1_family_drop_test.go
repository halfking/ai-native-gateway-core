package startup

// migration_890_v1_family_drop_test.go —— 890（v1 request_logs 族最终删除）
// 的**契约门**。
//
// # 这道门防什么
//
// 890 是本仓**第一条真正删表的迁移**。它同时动了三样必须彼此等价的东西：
//
//	① Go 侧的自愈重建体（db.canonicalSessionOnlyDDL）
//	② 迁移文件里的视图体（890 的 $proj$ 块）
//	③ 运行时 SSOT（db.recentSuccessRateEnsureSQL —— 与 890 里的
//	   recent_success_rate 函数体同文）
//
// 这三样只要有一处漂移，后果**不是**编译错、也不是 DDL 错，而是：
//
//   · ① 与 ② 不一致 ⇒ 迁移装上去的视图与网关启动时自愈重建的视图**不是同一个**
//     ⇒ 某次「视图被带外删掉」的自愈会把**两臂**换成**三臂**（或反之）
//     ⇒ 而三臂形态引用已被 890 删掉的表 ⇒ 42P01 ⇒ 整个日志面板 500。
//   · ③ 与 890 不一致 ⇒ 迁移把函数换成了会话族形态，而网关启动时又把它换回
//     带 v1 腿的版本 ⇒ **每一次重启**都把 890 的成果抹掉 ⇒ 函数在第一次被
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

const mig885File = "890_drop_request_logs_v1_family.sql"

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

// TestMigration890HasDownCompanion —— 新增迁移必须带同名 .down.sql。
//
// 判据的判据（scripts/pre-commit-check.sh:check_migration_has_down）只对
// **未跟踪**文件生效 ⇒ 已提交之后就没有第二道门了 ⇒ 这里补一道常驻的。
func TestMigration890HasDownCompanion(t *testing.T) {
	if _, err := os.Stat(filepath.Join(".", "890_drop_request_logs_v1_family.down.sql")); err != nil {
		t.Fatalf("890 must ship a .down.sql companion: %v", err)
	}
}

// TestMigration890CarriesLockGuard —— >870 的启动迁移必须自带 lock_timeout。
//
// 890 要 DROP 一族**分区大表** ⇒ AccessExclusiveLock ⇒ 等锁可能堵住全库。
// （与 installer/cmd/llm-gw-installer/startup_ddl_lock_guard_gate_test.go
// 同一要求；本门是 canonical 目录侧的常驻替身。）
func TestMigration890CarriesLockGuard(t *testing.T) {
	src := readMig885(t)
	if !strings.Contains(strings.ToLower(src), "lock_timeout") {
		t.Fatal("890 must set lock_timeout: it DROPs a partitioned table family " +
			"(AccessExclusiveLock) and would otherwise queue behind DML indefinitely")
	}
}

// TestMigration890RefusesNonEmptyV1Family —— **非空闸**必须在。
//
// 这道闸是 890 唯一的"数据不丢"保证：v1 族里的行是会话族镜像建立**之前**的
// 历史，删表即删历史，而 down 只能重建**骨架**（救不回数据）。
//
// ⚠ 判据不只看"有没有非空闸"这三个字，还要看它**真的会在非空时拒绝**：
// 下面用一段**合成**的 v1 族（非空）跑迁移主体，断言它 RAISE EXCEPTION。
// 合成夹具在**临时 schema**里建，不碰真库。
func TestMigration890RefusesNonEmptyV1Family(t *testing.T) {
	src := readMig885(t)
	if !strings.Contains(src, "refuses to drop a NON-EMPTY v1 family") {
		t.Fatal("890 must refuse to drop a non-empty v1 family " +
			"(those rows predate the session mirror and are not recoverable)")
	}
	// 闸必须**逐表**统计，而不是只看母表：hot 表与 bodies 表各自独立。
	for _, rel := range []string{"request_logs", "request_logs_hot", "request_logs_bodies"} {
		if !strings.Contains(src, "'"+rel+"'") && !strings.Contains(src, "'_"+rel+"'") {
			t.Errorf("890's non-empty guard must cover %q (and its renamed spelling)", rel)
		}
	}
}

// TestMigration890ProjectionMatchesComposer —— ★ 迁移的视图体 ⇔ Go 自愈体。
//
// 这是本门最重要的一条：①（Go 自愈）与 ②（迁移）必须是**同一份文本**。
// 漂移的后果见文件头（"自愈把两臂换成三臂 ⇒ 42P01 ⇒ 面板全 500"）。
//
// 取法：迁移里的 $proj$ 块（列投影）+ 与 Go 侧同款的 DDL 模板 ⇒ 与
// db.canonicalSessionOnlyDDL(true) 的**投影部分**逐字比较（归一化后）。
func TestMigration890ProjectionMatchesComposer(t *testing.T) {
	src := readMig885(t)

	// ── 阳性对照：块必须存在，且非空 ──
	projStart := strings.Index(src, "proj := $proj$")
	projEnd := strings.Index(src, "$proj$;")
	if projStart < 0 || projEnd < 0 || projEnd <= projStart {
		t.Fatal("890 no longer carries the $proj$ block; update this contract test")
	}
	sqlProj := src[projStart+len("proj := $proj$") : projEnd]
	if strings.TrimSpace(sqlProj) == "" {
		t.Fatal("890's $proj$ block is empty — the detector would be vacuously green")
	}

	// ── Go 侧的自愈体：直接取它的投影（不从 DDL 里切——见导出口径的注释）──
	goProj := db.CanonicalSessionOnlyProjectionForTest()
	if strings.TrimSpace(goProj) == "" {
		t.Fatal("db.canonicalSessionOnlyDDL returned an empty projection — detector is broken")
	}

	if normalizeSQL885(sqlProj) != normalizeSQL885(goProj) {
		t.Fatalf("890's $proj$ block and db.canonicalSessionOnlyDDL have drifted.\n"+
			"Consequence: the view the migration installs and the view the gateway "+
			"self-heals on boot are NOT the same — a boot-time self-heal would swap "+
			"one body for the other, and the three-arm body references tables 890 "+
			"just dropped (42P01 across the whole logs panel).\n"+
			"--- migration ---\n%s\n--- go ---\n%s", sqlProj, goProj)
	}
}

// TestMigration890ViewBodyMatchesComposer —— ★ 迁移装出的**整段视图体** ⇔ Go 自愈体。
//
// # 为什么这道门必需（R76 轮实测，2026-10-21）
//
// 这道门是**上一道门的补缺**，不是重复：
//
//   - 上一道门（TestMigration890ProjectionMatchesComposer）**只比 projection**
//     （迁移的 $proj$ 块 vs Go 的投影常量）⇒ ⇒ **FROM/JOIN 结构没人守**；
//   - `db.CanonicalSessionOnlyDDLForTest()`（**整段 DDL** 的导出入口）**没有任何门在用**
//     —— 它的文档注释写着「契约门比 $proj$」，可导出摆在那里没人调。
//
// ⇒ ⇒ **结构漂移会静悄悄通过静态层**，而它的后果恰恰是本文件头写的那一类：
// Go 侧若把 `session_turn_details_hot` 写成 `session_turn_details`（hot 臂 join 到
// parent 特征层）、或漏掉一条 details 腿、或把两臂顺序颠倒 ⇒ 迁移装上去的视图与
// 网关启动时自愈重建的视图**不是同一个** ⇒ ⇒ 启动自愈会把视图换歪 ⇒
// 而「两臂同名同序 118 列」的读方零改动承诺当场破掉。
//
// ★ 为什么是「整段组装后比」而不是「逐条 grep 关键字」：
//
//	逐条 grep（找 FROM / LEFT JOIN / ON 的字样）只能证明**出现过**，
//	证不了**结构相同**（顺序、别名、腿的配对都测不到）⇒ 而且 grep 会被注释里的
//	示例骗过 ⇒ ★ 本门把迁移里的 **$ddl$ 模板 + $proj$ 投影组装成迁移实际装出的
//	整段语句**，与 Go 的整段 DDL **归一化后逐字比** ⇒ ⇒ projection 与结构**一起**被守住。
//
// ★ 阳性对照：组装结果必须**非空**且**含两臂与两条 details 腿**，
//
//	否则一个「抽不出来就返回空」的检测器也能让本门全绿。
func TestMigration890ViewBodyMatchesComposer(t *testing.T) {
	src := readMig885(t)

	// ── 取 $proj$ 块（与上一道门同款取法）──
	projStart := strings.Index(src, "proj := $proj$")
	projEnd := strings.Index(src, "$proj$;")
	if projStart < 0 || projEnd < 0 || projEnd <= projStart {
		t.Fatal("890 no longer carries the $proj$ block; update this contract test")
	}
	proj := src[projStart+len("proj := $proj$") : projEnd]

	// ── 取装视图的 $ddl$ 模板（整段 CREATE OR REPLACE VIEW …）──
	ddlStart := strings.Index(src, "CREATE OR REPLACE VIEW public.request_logs_with_current_month AS")
	if ddlStart < 0 {
		t.Fatal("890 no longer carries the canonical view DDL template; update this contract test")
	}
	ddlEnd := strings.Index(src[ddlStart:], "$ddl$")
	if ddlEnd < 0 {
		t.Fatal("890's canonical view DDL template is no longer terminated by $ddl$; update this contract test")
	}
	template := src[ddlStart : ddlStart+ddlEnd]

	// ── 组装：把投影填进模板（与迁移运行时同一动作）──
	assembled := strings.ReplaceAll(template, "%s", proj)
	if strings.TrimSpace(assembled) == "" {
		t.Fatal("assembled 890 view body is empty — the detector would be vacuously green")
	}

	// ── 阳性对照：两臂 + 两条 details 腿必须在（否则「抽错了范围」会伪装成「一致」）──
	for _, needle := range []string{
		"FROM public.session_turns_hot t",
		"FROM public.session_turns t",
		"LEFT JOIN public.session_turn_details_hot d",
		"LEFT JOIN public.session_turn_details d",
		"UNION ALL",
	} {
		if !strings.Contains(assembled, needle) {
			t.Fatalf("assembled 890 view body lacks %q — the extraction range is wrong "+
				"(a wrong range reads like 'the two agree')", needle)
		}
	}

	goBody := db.CanonicalSessionOnlyDDLForTest()
	if strings.TrimSpace(goBody) == "" {
		t.Fatal("db.CanonicalSessionOnlyDDLForTest returned an empty body — detector is broken")
	}

	if normalizeSQL885(assembled) != normalizeSQL885(goBody) {
		t.Fatalf("890's installed view body and db.canonicalSessionOnlyDDL have drifted.\n"+
			"Consequence: the view the migration installs and the view the gateway "+
			"self-heals on boot are NOT the same body — a boot-time self-heal would "+
			"rebuild the canonical view with a different arm/join structure "+
			"(e.g. the hot arm joined to the parent details layer, or a missing leg), "+
			"which breaks the 'two arms, same names, same order' promise every reader "+
			"relies on.\n"+
			"--- migration (assembled) ---\n%s\n--- go ---\n%s", assembled, goBody)
	}
}

// TestMigration890RecentSuccessRateMatchesRuntimeSSOT —— ★ 迁移的函数体 ⇔
// 运行时 SSOT。
//
// `recent_success_rate` 进的是 provider 的候选查询（`CROSS JOIN LATERAL`）⇒
// 它带 v1 腿 = **100% 流量失败**。而 `CREATE OR REPLACE FUNCTION` 对 SQL
// 函数体**不在创建时解析** ⇒ 漂移的后果是"建得成功、首次调用才炸"，
// 症状与"上游全挂"同形 ⇒ 必须在**静态**层拦住。
func TestMigration890RecentSuccessRateMatchesRuntimeSSOT(t *testing.T) {
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
		t.Fatal("890 must replace recent_success_rate (it is called on the provider " +
			"candidate path — a v1 leg there is a 100% traffic failure)")
	}
	seg := src[i:]
	o2 := strings.Index(seg, "AS $$")
	if o2 < 0 {
		t.Fatalf("cannot locate the function body opener inside 890:\n%s", seg[:min(len(seg), 800)])
	}
	c2 := strings.Index(seg[o2+len("AS $$"):], "$$")
	if c2 < 0 {
		t.Fatalf("cannot locate the function body closer inside 890:\n%s", seg[:min(len(seg), 800)])
	}
	migBody := seg[o2+len("AS $$") : o2+len("AS $$")+c2]

	if normalizeSQL885(migBody) != normalizeSQL885(ssotBody) {
		t.Fatalf("890's recent_success_rate body and db.recentSuccessRateEnsureSQL have drifted.\n"+
			"Consequence: the gateway's boot-time CREATE OR REPLACE would RESTORE the "+
			"v1 leg that 890 just removed ⇒ every restart re-breaks the provider "+
			"candidate query ⇒ 100%% traffic failure (and the symptom looks like "+
			"'all upstreams down', not like a migration mismatch).\n"+
			"--- migration ---\n%s\n--- runtime SSOT ---\n%s", migBody, ssotBody)
	}
}

// TestMigration890EmbeddataCopyIsByteIdentical —— canonical 与 installer
// embeddata 两份必须逐字节相同。
//
// ⚠ 不是"内容等价"：**逐字节**。因为 embeddata 那份是**逐字执行**的
// （安装器不做部署通道的词法改写），差一个字符就是两个行为。
func TestMigration890EmbeddataCopyIsByteIdentical(t *testing.T) {
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
		t.Fatalf("890's embeddata copy has drifted from the canonical file "+
			"(canonical %d bytes, embeddata %d bytes)", len(canonical), len(embed))
	}
}

// TestMigration890DownRestoresEveryDroppedFunction —— ★ down 必须把 up 删掉的
// **每一个**函数/过程都装回去。
//
// 这道门是 2026-10-20 补的：890 的 down 此前**只恢复了 4 个**函数
// （ensure_request_logs_partition / ensure_request_logs_bodies_partition /
// create_next_month_partitions / ensure_next_month_archive_partition），
// 而 up 删掉的是 **20 个**⇒ 回滚后 v1 族「表回来了、promote / TTL / 归档
// 函数没回来」= **半活**：表在、分区不产、正文不清理；而且旧代码
// （回滚通常连代码一起回滚）会调用那些函数 ⇒ 每 tick 42P01。
//
// 判据是**集合比对**（up 的 DROP 清单 ⇔ down 的 CREATE 清单），不是数个数：
// 数个数挡不住「补了 4 个、漏了 4 个、总数不变」。
func TestMigration890DownRestoresEveryDroppedFunction(t *testing.T) {
	up := readMig885(t)
	downBytes, err := os.ReadFile(filepath.Join(".", "890_drop_request_logs_v1_family.down.sql"))
	if err != nil {
		t.Fatalf("read 890 down: %v", err)
	}
	down := string(downBytes)

	dropRe := regexp.MustCompile(`(?m)^DROP (FUNCTION|PROCEDURE) IF EXISTS public\.([a-z_0-9]+)\(`)
	dropMatches := dropRe.FindAllStringSubmatch(up, -1)
	// ── 阳性对照：检测器必须真的抓得到东西（否则下面的比对是空集比空集）──
	if len(dropMatches) != 20 {
		t.Fatalf("890 drops %d functions/procedures, expected 20 — "+
			"the detector (or 890 itself) changed; update this gate together with it",
			len(dropMatches))
	}
	dropped := map[string]bool{}
	for _, m := range dropMatches {
		dropped[m[1]+":"+m[2]] = true
	}

	createRe := regexp.MustCompile(`(?m)^CREATE OR REPLACE (FUNCTION|PROCEDURE) public\.([a-z_0-9]+)\(`)
	createMatches := createRe.FindAllStringSubmatch(down, -1)
	created := map[string]bool{}
	for _, m := range createMatches {
		created[m[1]+":"+m[2]] = true
	}
	// 语句条数必须相等（**重载也算数**）：`credential_most_used_model` 有
	// (bigint,int) 与 (int,int) 两个重载 ⇒ 20 个对象只有 19 个**名字**。
	// 只比名字集合会漏掉「少复原一个重载」⇒ 这里比**条数**，比集合。
	if len(createMatches) != len(dropMatches) {
		t.Errorf("890's down creates %d functions/procedures but the up drops %d "+
			"(overloads count separately: credential_most_used_model has two)",
			len(createMatches), len(dropMatches))
	}

	for key := range dropped {
		if !created[key] {
			t.Errorf("890's down must restore %s — restoring the tables without the "+
				"functions leaves the v1 family half-alive (tables back, partitions "+
				"never created, bodies never cleaned) and the pre-890 code hits 42P01 "+
				"on every tick", key)
		}
	}
	// 反向：down 复原的对象必须是 up **删掉**的那些（防止 down 里塞进不属于
	// v1 族的函数，或把「被替换」的函数当成「被删除」的函数混进来）。
	for key := range created {
		if !dropped[key] {
			t.Errorf("890's down restores %s, but 890 never dropped it — "+
				"the down and the up must operate on the same object set", key)
		}
	}
}

// TestMigration890DownRestoresTheTwoTriggers —— 触发器随表消失，
// 触发器**函数**由上一道门覆盖，但两个触发器本身也要复原。
//
// 实测：这两个触发器**只**挂在 v1 族上（真库 pg_trigger 清点：
// `trg_session_tenant_binding` → `_request_logs` 及其分区，
// `trg_update_session_summary` → `_request_logs_hot`）；
// 它们写的 `session_tenant_binding` / `session_summaries` 是**会话族**的表。
func TestMigration890DownRestoresTheTwoTriggers(t *testing.T) {
	downBytes, err := os.ReadFile(filepath.Join(".", "890_drop_request_logs_v1_family.down.sql"))
	if err != nil {
		t.Fatalf("read 890 down: %v", err)
	}
	down := string(downBytes)
	for _, want := range []string{
		"CREATE TRIGGER trg_session_tenant_binding AFTER INSERT ON public._request_logs",
		"CREATE TRIGGER trg_update_session_summary AFTER INSERT ON public._request_logs_hot",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("890's down must re-create the v1 triggers; missing: %q "+
				"(triggers die with their table, so only the down can bring them back)", want)
		}
	}
}

// TestMigration890DownEmbeddataCopyIsByteIdentical —— down 的两份副本也必须
// 逐字节相同（up 的那道门只覆盖 up）。
func TestMigration890DownEmbeddataCopyIsByteIdentical(t *testing.T) {
	const downName = "890_drop_request_logs_v1_family.down.sql"
	canonical, err := os.ReadFile(filepath.Join(".", downName))
	if err != nil {
		t.Fatalf("read canonical down: %v", err)
	}
	embed, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", downName))
	if err != nil {
		t.Fatalf("read embeddata down copy: %v", err)
	}
	if string(canonical) != string(embed) {
		t.Fatalf("890's down embeddata copy has drifted from the canonical file "+
			"(canonical %d bytes, embeddata %d bytes)", len(canonical), len(embed))
	}
}

// TestMigration890CutsV1DependentEdgesBeforeDropping —— ★ 依赖边必须在**删表之前**切断。
//
// 这道门是 2026-10-20 新装链实测逼出来的：890 在**新装路径**上被 PostgreSQL
// 直接拒绝：
//
//	cannot drop table _request_logs_hot because other objects depend on it
//	DETAIL: policy session_turns_owner_filter on table session_turns depends on table _request_logs_hot
//
// 根因是**两条**还在指向 v1 族的依赖边（**升级路径上看不见，因为生产上它们
// 早就不存在/早已是 canonical 形态**）：
//
//	① `routing_analytics_source`：由迁移 649 建，body 直读 `_request_logs_hot`；
//	   生产上 2026-10-13 被**应用启动期**的 CREATE OR REPLACE 换成 canonical 形态
//	   ⇒ 新装路径上应用**从没启动过** ⇒ 仍是 v1 直读形态。
//	② 两条 RESTRICTIVE `*_owner_filter` 策略（`session_turns_owner_filter` /
//
// //	   `session_turns_hot_owner_filter`）：策略表达式**内部** UNION ALL 读
//
//	`_request_logs_hot` ∪ `_request_logs` 取最早那行的 `owner_user`。
//
// ⚠ 仓库里早有权威决策（不是本门临时发明的）：`docs/audit/2026-10-04-session-migration-decision-sheet.md`
// 的 **D9**（并援引审计 §9.137 / §9.139 的真库实测）写明整改路径三步、**顺序不能换**：
// ① `sessions.owner_user` 已存在 ⇒ ② 把 `*_owner_filter` 策略改写成读
// `sessions.owner_user`、不再引用 `request_logs` ⇒ ③ **最后才** DROP TABLE。
//
// 本门钉的是**顺序**：两条切边的语句必须在删表步骤**之前**。顺序反了 ⇒
// DROP 照样被拒（而且失败信息只说「有依赖」，不说是哪条边）。
func TestMigration890CutsV1DependentEdgesBeforeDropping(t *testing.T) {
	src := readMig885(t)

	// ── 阳性对照：删表步骤本身必须在（否则这道门没有对象）──
	dropIdx := strings.Index(src, "DROP TABLE IF EXISTS public.%I")
	if dropIdx < 0 {
		t.Fatal("890 no longer drops the v1 tables (DROP TABLE step missing) — " +
			"update this gate together with the migration")
	}

	// ── ① routing_analytics_source 必须被换成 canonical 形态，且在删表之前 ──
	rasIdx := strings.Index(src, "CREATE OR REPLACE VIEW public.routing_analytics_source")
	if rasIdx < 0 {
		t.Errorf("890 must re-point public.routing_analytics_source at the canonical view " +
			"(the fresh-install path keeps 649's v1-direct body because the app never boots " +
			"during an install) — otherwise DROP TABLE _request_logs_hot is refused by PostgreSQL")
	} else if rasIdx > dropIdx {
		t.Errorf("890 must re-point public.routing_analytics_source BEFORE dropping the v1 "+
			"tables (re-point at offset %d, DROP TABLE at %d) — the dependency edge has to be "+
			"cut first", rasIdx, dropIdx)
	}

	// ── ② 两条 owner_filter 策略必须被改写成读 sessions.owner_user，且在删表之前 ──
	for _, pol := range []string{"session_turns_owner_filter", "session_turns_hot_owner_filter"} {
		idx := strings.Index(src, "CREATE POLICY "+pol)
		if idx < 0 {
			t.Errorf("890 must rewrite the RESTRICTIVE policy %q so it resolves owner from "+
				"public.sessions.owner_user instead of the retired v1 family "+
				"(decision sheet D9 step 2; PostgreSQL refuses DROP TABLE otherwise)", pol)
			continue
		}
		if idx > dropIdx {
			t.Errorf("890 must rewrite the policy %q BEFORE dropping the v1 tables "+
				"(rewrite at offset %d, DROP TABLE at %d)", pol, idx, dropIdx)
		}
	}
	// 改写后的策略必须读会话族自己的 owner 列（而不是把 owner 过滤**删掉**）：
	// 决策清单明写「只做 2+3 步会把 owner 过滤直接删掉（不是迁移，是丢失）」。
	//
	// ⚠ 判据必须挑**完整表达式**，不能只挑字段名：写 `s.owner_user` 会被注释里的
	// `sessions.owner_user` 顶住（子串）⇒ 变异「把 owner 来源换成别的列」照样全绿
	// ⇒ 那是**假绿**。这里钉的是「`s.owner_user = current_setting('app.current_user', true)`」
	// 这条**完整**比较式。
	if !strings.Contains(src, "s.owner_user = current_setting('app.current_user', true)") {
		t.Errorf("890's rewritten owner_filter policies must resolve owner from " +
			"public.sessions.owner_user (s.owner_user = current_setting('app.current_user', true)) — " +
			"dropping the owner filter is NOT a migration")
	}
}

// TestMigration890DownRestoresTheOwnerFilterPolicies —— down 必须把 2c 步改写的
// 策略**还原成 v1 形态**（否则回滚后的库与回滚前不等价）。
//
// ⚠ 判据要点：down 只在「策略存在 **且** 其表达式**不再**引用 v1 族」时才还原 ⇒
// 生产形态（本来就没有这两条策略）下是 no-op ⇒ 不会凭空造出生产从来没有的策略。
func TestMigration890DownRestoresTheOwnerFilterPolicies(t *testing.T) {
	downBytes, err := os.ReadFile(filepath.Join(".", "890_drop_request_logs_v1_family.down.sql"))
	if err != nil {
		t.Fatalf("read 890 down: %v", err)
	}
	down := string(downBytes)
	// ⚠ 判据必须挑**整行**，不能只挑子串：策略名在 down 的注释与
	// `DROP POLICY IF EXISTS` 行里都出现 ⇒ 只查「文件里有没有这个名字」
	// ⇒ 变异「把复原语句注释掉」照样全绿 ⇒ 那是**假绿**。
	for _, want := range []string{
		"CREATE POLICY session_turns_owner_filter ON public.session_turns",
		"CREATE POLICY session_turns_hot_owner_filter ON public.session_turns_hot",
	} {
		found := false
		for _, line := range strings.Split(down, "\n") {
			if strings.TrimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("890's down must re-create the owner_filter policies (missing the whole line %q) — "+
				"the rollback must leave the same owner-isolation semantics it found", want)
		}
	}
	// 还原必须**有守卫**：只在「策略存在且不再引用 v1」时才还原 ⇒ 生产形态是 no-op。
	if !strings.Contains(down, "strpos(v_expr, '_request_logs') = 0") {
		t.Errorf("890's down must guard the policy restoration (strpos(v_expr, '_request_logs') = 0) — " +
			"without the guard it would CREATE policies on environments that never had them")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
