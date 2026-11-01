package startup

// migration_929_error_type_closure_test.go ——
//
// 「errorsx.ErrorKind 闭集 × self_check_runs.error_type 的 DB CHECK 闭集」交叉门。
//
// ── 为什么需要这道门（2026-11-02 12h 审计轮 P1 的根因面）────────────────
//
//	缺陷本体：上游 9 提交给 errorsx 加了 6 个新 kind
//	（request_rejected / circuit_open / fp_slot_saturated /
//	  first_byte_timeout / reply_loop / account_restricted），
//	但 `self_check_runs_error_type_check` 的字面量清单**没跟着长**。
//
//	★ 后果不是「多了一个新错误类型」，而是**写入被 DB 拒绝**：
//	  写侧 `bg/credential_selfcheck.go:844` 写的正是 kind 名
//	  （r.ErrType = string(errorsx.ClassifyErrorWithBody(...))），
//	  而 `finalizeRun`（:896）那条 UPDATE 被 23514 拒绝 ⇒ **整条回滚**
//	  ⇒ run 行停在 status='running' / error_type=NULL / completed_at=NULL
//	  的**半写态** ⇒ `auditToSystemProbeRuns`（:433）与 `publishSelfcheck`
//	  都不执行 ⇒ 这次自检在系统里**根本没发生过**，但调度器以为发生过。
//
// ── 为什么「挑几个补」不够（★ 这道门要根治的那一面）─────────────────────
//
//	补齐那 6 个之后，**下一个**新 kind 会重犯同一个错：写侧自动跟随
//	errorsx 闭集走，DB 约束却是一份手抄的静态清单 ⇒ 两份清单必然漂移。
//
//	⇒ 所以判据必须是**闭集对拍**，而不是「补齐清单」：
//	  `errorsx.AllErrorKinds()` ⊆ DB CHECK 字面量集。
//	  ⇒ 新增 kind 时这道门当场红 ⇒ 逼着人补一条 startup 迁移
//	    （★ 不许改已应用迁移的字节：那是 content replay 的连锁 break 源）。
//
// ── 量具纪律（★ 这道门自己也可能是坏的）────────────────────────────────
//
//	① **注释不许参与判定**：迁移文件的说明段里会写 `'request_rejected'`
//	   这类字面量 ⇒ 若不涂白 ⇒ 判定器会把**散文**当成**声明**
//	   ⇒ ⇒ ⇒ 假绿（门绿、约束窄）。⇒ 复用同包 `stripSQLComments`。
//	② **提取范围要钉死**：不能「扫全文里的引号字面量」——守卫数组
//	   （v_required）本身就含全集 ⇒ 全文扫 ⇒ 哪怕 CHECK 窄了也判绿。
//	   ⇒ 只从 `ADD CONSTRAINT self_check_runs_error_type_check CHECK (...)`
//	   之后的那个 `ARRAY[...]` 里取，并**同时**单独取守卫数组
//	   ⇒ 两份必须**逐项相等**（防「守卫放宽、约束没放宽」）。
//	③ **判红要点名**：变异自检断言「转红」且「输出点名被拆的字面量」，
//	   而不是断言「进程非零」（rc=127 之类「脚本没跑成」也非零）。
//	④ **判据按安装顺序取最后一个**：已应用的迁移字节冻结 ⇒ 真正生效的
//	   是**安装顺序里最后一个**重建该 CHECK 的迁移 ⇒ 判据跟着它走，
//	   而不是钉死在某个文件号上。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const (
	// errorTypeConstraintName 是被钉住的那条 CHECK。
	errorTypeConstraintName = "self_check_runs_error_type_check"

	// errorTypeCanonicalObject 是正典目录里的表定义（SSOT 文档形态）。
	errorTypeCanonicalObject = "../../objects/tables/self_check_runs.sql"

	// startupManifestPath 是 installer 的**有序** startup 迁移清单。
	// ★ 用它（而不是文件名排序）确定「安装顺序里最后一个重建该 CHECK 的迁移」
	//   —— 清单里的顺序**不是**数字序（bootstrap 在 index 3、715 在 index 149）。
	startupManifestPath = "../../schema/installed_startup_migrations.tsv"
)

// workerOnlyErrorTypeLiterals 是 self-check worker 直接写、但**不属于**
// errorsx.ErrorKind 闭集的字面量。写点：
//
//	'none'          bg/self_check_worker.go:550 / bg/credential_selfcheck.go:388
//	'internal'      bg/self_check_worker.go:634 / bg/credential_selfcheck.go:813
//	'parse_error'   bg/self_check_worker.go:690
//	'unattributed'  bg/credential_selfcheck.go:808
//	'upstream_fail' 历史值（644 的 canonical 清单里的遗留项）
//
// ★ 这五个必须同时进 DB CHECK —— 否则它们会撞 23514，症状与本轮 P1 同形。
var workerOnlyErrorTypeLiterals = []string{
	"none", "internal", "parse_error", "unattributed", "upstream_fail",
}

// requiredErrorTypeLiterals = errorsx kind 闭集 ∪ worker 专用字面量。
func requiredErrorTypeLiterals() []string {
	out := make([]string, 0, len(errorsx.AllErrorKinds())+len(workerOnlyErrorTypeLiterals))
	for _, k := range errorsx.AllErrorKinds() {
		out = append(out, string(k))
	}
	out = append(out, workerOnlyErrorTypeLiterals...)
	return out
}

// ── SQL 字面量提取 ─────────────────────────────────────────────────────────

// scanSQLArrayRegion 从 text 的 start 处开始找第一个 `ARRAY[`，返回其
// **配对**方括号区间 [regionStart, regionEnd) 及其中的全部单引号字面量。
//
// ★ 为什么不能用「正则扫全文引号」：守卫数组本身就含全集 ⇒ 全文扫会让
//   窄约束也判绿。⇒ 范围必须钉在具体语句的 ARRAY 里。
// ★ 单引号按 SQL 规则处理：`''` 是转义的单引号，不是结束符。
// ★ 返回**区间**而不只是字面量集：变异自检必须能在**指定区间内**做替换
//   —— 同一份 SQL 里同一个字面量出现两次（守卫数组 + CHECK 数组），
//   `strings.Replace(..., 1)` 取第一处 ⇒ 若不钉区间就会改到另一个数组
//   ⇒ 变异「看起来生效」实则测的是另一条腿（★ 本轮第一次写变异自检
//   就是被自己的自检这么抓住的）。
func scanSQLArrayRegion(text string, start int) (int, int, map[string]bool, bool) {
	upper := strings.ToUpper(text)
	if start < 0 || start > len(text) {
		return 0, 0, nil, false
	}
	rel := strings.Index(upper[start:], "ARRAY[")
	if rel < 0 {
		return 0, 0, nil, false
	}
	arrStart := start + rel
	i := arrStart + len("ARRAY[")
	out := map[string]bool{}
	depth := 1
	for i < len(text) {
		c := text[i]
		if c == '\'' {
			litStart := i + 1
			j := litStart
			for j < len(text) {
				if text[j] == '\'' {
					if j+1 < len(text) && text[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(text) {
				return 0, 0, out, false // 未闭合的字符串字面量
			}
			out[text[litStart:j]] = true
			i = j + 1
			continue
		}
		switch c {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return arrStart, i + 1, out, true
			}
		}
		i++
	}
	return 0, 0, out, false
}

// scanSQLArrayLiterals 是 scanSQLArrayRegion 的「只要字面量集」简写。
func scanSQLArrayLiterals(text string, start int) (map[string]bool, bool) {
	_, _, lits, ok := scanSQLArrayRegion(text, start)
	return lits, ok
}

// indexOfFold 大小写不敏感地找子串，返回绝对下标（找不到返回 -1）。
func indexOfFold(text, needle string) int {
	return strings.Index(strings.ToUpper(text), strings.ToUpper(needle))
}

// checkConstraintLiterals 从「ADD CONSTRAINT <name> CHECK」之后的 ARRAY 里取字面量。
func checkConstraintLiterals(t *testing.T, clean, constraintName string) map[string]bool {
	t.Helper()
	anchor := indexOfFold(clean, "ADD CONSTRAINT "+constraintName+" CHECK")
	if anchor < 0 {
		t.Fatalf("SQL 里找不到 ADD CONSTRAINT %s CHECK —— 提取器失效（不是「约束已消失」）", constraintName)
	}
	lits, ok := scanSQLArrayLiterals(clean, anchor)
	if !ok {
		t.Fatalf("SQL 里 %s 的 ARRAY[...] 提取失败 —— 提取器失效", constraintName)
	}
	return lits
}

// guardArrayLiterals 从守卫数组 `v_required text[] := ARRAY[...]` 里取字面量。
func guardArrayLiterals(t *testing.T, clean string) map[string]bool {
	t.Helper()
	_, _, lits := guardArrayRegion(t, clean)
	return lits
}

// checkConstraintArrayRegion 返回 CHECK 语句里那个 ARRAY[...] 的区间。
func checkConstraintArrayRegion(t *testing.T, clean, constraintName string) (int, int, map[string]bool) {
	t.Helper()
	anchor := indexOfFold(clean, "ADD CONSTRAINT "+constraintName+" CHECK")
	if anchor < 0 {
		t.Fatalf("SQL 里找不到 ADD CONSTRAINT %s CHECK —— 提取器失效（不是「约束已消失」）", constraintName)
	}
	s, e, lits, ok := scanSQLArrayRegion(clean, anchor)
	if !ok {
		t.Fatalf("SQL 里 %s 的 ARRAY[...] 提取失败 —— 提取器失效", constraintName)
	}
	return s, e, lits
}

// guardArrayRegion 返回守卫数组 ARRAY[...] 的区间。
func guardArrayRegion(t *testing.T, clean string) (int, int, map[string]bool) {
	t.Helper()
	anchor := indexOfFold(clean, "v_required")
	if anchor < 0 {
		t.Fatalf("SQL 里找不到 v_required 守卫数组 —— 提取器失效")
	}
	s, e, lits, ok := scanSQLArrayRegion(clean, anchor)
	if !ok {
		t.Fatalf("SQL 里 v_required 的 ARRAY[...] 提取失败 —— 提取器失效")
	}
	return s, e, lits
}

// removeLiteralInRegion 在 text 的 [regionStart, regionEnd) 区间内删掉一个字面量。
//
// ★ 区间是必需的：同一个字面量在守卫数组与 CHECK 数组里各出现一次，
//   全文 `Replace(..., 1)` 取第一处 ⇒ 改到的是**另一个数组**。
func removeLiteralInRegion(text string, regionStart, regionEnd int, literal string) (string, bool) {
	region := text[regionStart:regionEnd]
	target := "'" + literal + "',"
	mutated := strings.Replace(region, target, "", 1)
	if mutated == region {
		return text, false
	}
	return text[:regionStart] + mutated + text[regionEnd:], true
}

// ── 找「安装顺序里最后一个重建该 CHECK 的迁移」 ──────────────────────────

// registeredStartupMigrations 返回安装顺序里的 startup 迁移文件名。
func registeredStartupMigrations(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(startupManifestPath)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", startupManifestPath, err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		out = append(out, strings.TrimSpace(parts[1]))
	}
	if len(out) == 0 {
		t.Fatalf("%s 解析出 0 条迁移 —— 判据会空转全绿", startupManifestPath)
	}
	return out
}

// lastConstraintDefiner 返回安装顺序里**最后一个**重建该 CHECK 的迁移的文件名。
//
// ★ 扫描时正文优先读 **embeddata**（installer 真正执行的那份），canonical 为备：
//   清单里有两份只存在于 embeddata 的文件（session_turns_hot_bootstrap.sql /
//   600_outbound_body_to_bodies_hot.sql）⇒ 只读 canonical 会漏掉它们
//   ⇒ 「最后一个 definer」的判定就不完整。
func lastConstraintDefiner(t *testing.T) string {
	t.Helper()
	var (
		lastName string
		found    int
	)
	for _, name := range registeredStartupMigrations(t) {
		if strings.HasSuffix(name, ".down.sql") {
			continue
		}
		clean := stripSQLComments(readMigrationAnyCopy(t, name))
		if indexOfFold(clean, "ADD CONSTRAINT "+errorTypeConstraintName+" CHECK") < 0 {
			continue
		}
		found++
		lastName = name
	}
	if found == 0 {
		t.Fatalf("没有任何已注册的 startup 迁移重建 %s —— "+
			"要么该约束已经没人维护，要么安装清单与文件脱节", errorTypeConstraintName)
	}
	return lastName
}

// embedStartupDir 是 installer 实际执行的 embeddata 目录。
const embedStartupDir = "../../../installer/cmd/llm-gw-installer/embeddata/startup"

// readMigrationAnyCopy 读一份迁移：embeddata 优先，canonical 为备。
func readMigrationAnyCopy(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(embedStartupDir, name))
	if err != nil {
		raw, err = os.ReadFile(name)
	}
	if err != nil {
		t.Logf("跳过 %s: %v", name, err)
		return ""
	}
	return string(raw)
}

// ── 判据 ──────────────────────────────────────────────────────────────────

// closureViolations 返回「CHECK 字面量集」相对「必需集」的违规描述。
func closureViolations(have map[string]bool, required []string) []string {
	var missing []string
	for _, want := range required {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	sort.Strings(missing)
	out := make([]string, 0, len(missing))
	for _, m := range missing {
		out = append(out, "CHECK 字面量集缺少 errorsx kind: "+m)
	}
	return out
}

// junkLiterals 返回「CHECK 里多出来的、既不是 kind 也不是 worker 专用」的字面量。
func junkLiterals(have map[string]bool, required []string) []string {
	allowed := map[string]bool{}
	for _, r := range required {
		allowed[r] = true
	}
	var junk []string
	for lit := range have {
		if !allowed[lit] {
			junk = append(junk, lit)
		}
	}
	sort.Strings(junk)
	return junk
}

// TestSelfCheckErrorTypeConstraintCoversErrorKindClosure 是主判据。
func TestSelfCheckErrorTypeConstraintCoversErrorKindClosure(t *testing.T) {
	required := requiredErrorTypeLiterals()
	name := lastConstraintDefiner(t)
	t.Logf("安装顺序里最后一个重建 %s 的迁移: %s", errorTypeConstraintName, name)

	// ★ 两份副本都要判：embeddata（installer 真正执行的那份）与 canonical
	//   （人读/文档那份）。只判一份 ⇒ 「改了一份、另一份没改」会被放过。
	for _, copyLabel := range []string{"embeddata", "canonical"} {
		t.Run(copyLabel, func(t *testing.T) {
			var clean string
			if copyLabel == "embeddata" {
				raw, err := os.ReadFile(filepath.Join(embedStartupDir, name))
				if err != nil {
					t.Fatalf("读 embeddata 副本失败: %v", err)
				}
				clean = stripSQLComments(string(raw))
			} else {
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatalf("读 canonical 副本失败: %v", err)
				}
				clean = stripSQLComments(string(raw))
			}

			// ① 生效的 CHECK 字面量集必须 ⊇ errorsx 闭集 ∪ worker 专用字面量。
			checkLits := checkConstraintLiterals(t, clean, errorTypeConstraintName)
			if v := closureViolations(checkLits, required); len(v) > 0 {
				t.Fatalf("%s（%s 副本）的 CHECK 字面量集没有覆盖 errorsx.ErrorKind 全闭集 ⇒ "+
					"运行时写入新 kind 时会被 DB 以 23514 拒绝 ⇒ self_check run 行半写。"+
					"修法 = 新增一条 startup 迁移重建该 CHECK（★ 不要改已应用迁移的字节）。缺失：%v",
					name, copyLabel, v)
			}

			// ② CHECK 里不许出现既不是 kind、也不是 worker 专用字面量的杂物。
			if junk := junkLiterals(checkLits, required); len(junk) > 0 {
				t.Fatalf("%s（%s 副本）的 CHECK 字面量集含杂物字面量 %v —— "+
					"要么是拼写错误，要么是手抄清单漂移", name, copyLabel, junk)
			}

			// ③ 守卫数组（v_required）与真正落库的 CHECK 数组必须**逐项相等**
			//    —— 防「守卫放宽了、约束没放宽」这种最阴的半修。
			guardLits := guardArrayLiterals(t, clean)
			if diff := closureViolations(guardLits, required); len(diff) > 0 {
				t.Fatalf("%s（%s 副本）的守卫数组 v_required 没有覆盖 errorsx 闭集：%v",
					name, copyLabel, diff)
			}
			if len(guardLits) != len(checkLits) {
				t.Fatalf("%s（%s 副本）的守卫数组（%d 项）与 CHECK 数组（%d 项）不是同一份 —— "+
					"守卫与约束漂移了（守卫会判定「已覆盖」而实际没覆盖）",
					name, copyLabel, len(guardLits), len(checkLits))
			}
			for lit := range checkLits {
				if !guardLits[lit] {
					t.Fatalf("%s（%s 副本）的 CHECK 有字面量 %q 而守卫数组没有 —— 守卫与约束漂移了",
						name, copyLabel, lit)
				}
			}
		})
	}
}

// TestSelfCheckErrorTypeCanonicalObjectCoversErrorKindClosure 钉正典目录。
//
// ★ 正典目录（sql/objects/tables/）是这张表 DDL 的 SSOT 文档形态。
//   本轮实测：它此前记的是**窄集 7 值**（http_000/502/503/504/timeout/
//   upstream_fail/none），而迁移与线上实际生效的是**宽集** ⇒ 两份文档
//   互相矛盾 ⇒ 后人照正典目录推演会得出「只允许 7 种错误」的错结论。
func TestSelfCheckErrorTypeCanonicalObjectCoversErrorKindClosure(t *testing.T) {
	raw, err := os.ReadFile(errorTypeCanonicalObject)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", errorTypeCanonicalObject, err)
	}
	clean := stripSQLComments(string(raw))
	required := requiredErrorTypeLiterals()

	lits, ok := scanSQLArrayLiterals(clean, 0)
	if !ok {
		t.Fatalf("%s 里找不到 ARRAY[...] —— 提取器失效", errorTypeCanonicalObject)
	}
	if v := closureViolations(lits, required); len(v) > 0 {
		t.Fatalf("%s 的 error_type 字面量集没有覆盖 errorsx.ErrorKind 全闭集 ⇒ "+
			"正典目录与迁移/线上实际不一致 ⇒ 后人会照它推演出错的结论。缺失：%v",
			errorTypeCanonicalObject, v)
	}
	if junk := junkLiterals(lits, required); len(junk) > 0 {
		t.Fatalf("%s 的 error_type 字面量集含杂物字面量 %v", errorTypeCanonicalObject, junk)
	}
}

// TestMigration929EmbeddedCopyMatchesCanonical 钉 canonical / embeddata 双副本。
func TestMigration929EmbeddedCopyMatchesCanonical(t *testing.T) {
	const name = "929_self_check_error_type_kind_closure.sql"
	source, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	embeddedPath := filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name)
	embedded, err := os.ReadFile(embeddedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != string(embedded) {
		t.Fatal("startup migration 929 differs from installer embedded copy")
	}
}

// TestSelfCheckErrorTypeLiteralExtractorIgnoresCommentedLiterals 是量具自证：
// 只在注释里出现的字面量**不得**被提取器当成声明。
func TestSelfCheckErrorTypeLiteralExtractorIgnoresCommentedLiterals(t *testing.T) {
	const victim = "request_rejected"
	sql := "-- 说明段里提到 '" + victim + "' 这个字面量（散文，不是声明）\n" +
		"ALTER TABLE public.self_check_runs\n" +
		"    ADD CONSTRAINT " + errorTypeConstraintName + " CHECK (\n" +
		"        error_type = ANY (ARRAY['none'])\n" +
		"    ) NOT VALID;\n"

	clean := stripSQLComments(sql)
	lits := checkConstraintLiterals(t, clean, errorTypeConstraintName)

	if lits[victim] {
		t.Fatal("提取器把注释里的字面量当成了声明 ⇒ 门会假绿（注释不许参与判定）")
	}
	if !lits["none"] {
		t.Fatalf("提取器漏掉了真正的声明字面量 'none' ⇒ 提取器坏了（读数: %v)", lits)
	}
}

// TestSelfCheckErrorTypeClosureGateHasTeeth 是**变异自检**。
//
// ★ 纪律：断言「判据转红」+「转红时点名被拆的那一项」，
//   而不是断言「进程非零」（rc=127 之类「脚本没跑成」也非零）。
// ★ 并且先断言「变异真的改变了提取结果」——
//   否则「变异没生效」会被误当成「门有牙」。
func TestSelfCheckErrorTypeClosureGateHasTeeth(t *testing.T) {
	const victim = "request_rejected"

	name := lastConstraintDefiner(t)
	clean := stripSQLComments(readMigrationAnyCopy(t, name))
	required := requiredErrorTypeLiterals()

	// 反向对照：未变异时必须绿（否则「门恒红」也满足下面的断言）。
	baselineCheck := checkConstraintLiterals(t, clean, errorTypeConstraintName)
	if v := closureViolations(baselineCheck, required); len(v) > 0 {
		t.Fatalf("未变异时门却判红 ⇒ 门判错了：%v", v)
	}
	if !baselineCheck[victim] {
		t.Fatalf("基准迁移的 CHECK 里本就没有 %q —— 变异自检的前提不成立", victim)
	}

	// ── 臂 1：把 CHECK 数组里的一个 kind 删掉 ⇒ 门必须转红并点名它 ──────
	// ★ 变异必须**钉在 CHECK 数组的区间内**：同一个字面量在守卫数组里
	//   也出现一次 ⇒ 全文 Replace(...,1) 会改到守卫数组 ⇒ 变异「看起来
	//   生效」实则测的是另一条腿（★ 本轮第一版就是这么写的，
	//   被本自检当场抓住：改后 CHECK 里该字面量仍在）。
	t.Run("CHECK 数组窄化", func(t *testing.T) {
		s, e, _ := checkConstraintArrayRegion(t, clean, errorTypeConstraintName)
		mutated, ok := removeLiteralInRegion(clean, s, e, victim)
		if !ok {
			t.Fatalf("变异没生效：CHECK 数组区间里找不到 %q", victim)
		}

		after := checkConstraintLiterals(t, mutated, errorTypeConstraintName)
		if after[victim] {
			t.Fatalf("变异没改变提取结果：%q 仍在 CHECK 提取集里 ⇒ 变异自检没测到东西", victim)
		}

		violations := closureViolations(after, required)
		if len(violations) == 0 {
			t.Fatalf("变异后门仍判绿 ⇒ 这道门对 CHECK 窄化毫无反应（没有牙）")
		}
		joined := strings.Join(violations, " | ")
		if !strings.Contains(joined, victim) {
			t.Fatalf("门转红了但没有点名被拆的 %q ⇒ 报错指向不明。读数: %s", victim, joined)
		}
	})

	// ── 臂 2：把守卫数组里的一个 kind 删掉（CHECK 不动）⇒ 门必须转红 ─────
	// ★ 这一条防的是「守卫放宽、约束没放宽」这种最阴的半修：
	//   守卫说「已覆盖」⇒ 迁移直接跳过 ⇒ 而真正的 CHECK 仍是窄的 ⇒
	//   运行时照样被 23514 拒。
	t.Run("守卫数组窄化", func(t *testing.T) {
		s, e, _ := guardArrayRegion(t, clean)
		mutated, ok := removeLiteralInRegion(clean, s, e, victim)
		if !ok {
			t.Fatalf("变异没生效：守卫数组区间里找不到 %q", victim)
		}

		after := guardArrayLiterals(t, mutated)
		if after[victim] {
			t.Fatalf("变异没改变守卫提取结果：%q 仍在守卫提取集里 ⇒ 变异自检没测到东西", victim)
		}

		violations := closureViolations(after, required)
		if len(violations) == 0 {
			t.Fatalf("变异后门仍判绿 ⇒ 这道门对守卫数组窄化毫无反应（没有牙）")
		}
		joined := strings.Join(violations, " | ")
		if !strings.Contains(joined, victim) {
			t.Fatalf("门转红了但没有点名被拆的 %q ⇒ 报错指向不明。读数: %s", victim, joined)
		}
	})
}
