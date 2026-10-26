package startup

// audit_sql_idempotence_contract_test.go — `sql/audit/` 下 877 基准 SQL 的
// **幂等性 / 相互隔离**契约门。
//
// ── 为什么需要这道门（2026-10-20 复现时暴露的真缺陷）─────────────────────
// 877 决策文档里写着「可复现」，并给出两条交叉校验 SQL 的执行命令。
// 真按那条命令在同一个容器上跑**第二遍**时炸了：
//
//   ERROR:  function xc_time(regclass, integer, integer) is not unique
//   HINT:  Could not choose the best candidate function.
//
// 根因：两个脚本的辅助函数**同名**（`xc_time`）但**签名不同** ——
// 交叉校验是 3 参，因子分解是 4 参且第 4 参（`id_off`）**带默认值**。
// PostgreSQL 解析函数调用时，带默认值的那个**也能接 3 参调用**，
// 于是同一次调用出现两个同优候选 ⇒ 报 "not unique"。
// ⇒ 「跑第一次对」≠「可复现」：**可复现脚本必须能重复执行**。
//
// 修法（两个都做了，缺一不可）：
//   ① 交叉校验脚本开头按**签名**清空本族（`xc_%`）辅助函数再重建；
//   ② 因子分解脚本改用**独立前缀 `xf_`**，两个脚本彻底隔离。
//
// 这道门守的就是这两条不许被回退（并对门本身做变异验证）。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	crossCheckSQLPath = "../../audit/2026-10-19-877-write-amplification-cross-check.sql"
	factorsSQLPath    = "../../audit/2026-10-19-877-write-amplification-factors.sql"
	decisionDocPath   = "../../../docs/audit/2026-10-19-877-写放大定量决策.md"
)

// readAuditFile 读取文件；不存在即判红（不静默跳过）。
func readAuditFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(raw)
}

// dropSweepRe 匹配「按签名清空某一族辅助函数」的 DO 块。
// 形如： p.proname LIKE 'xc\_%'  +  DROP FUNCTION IF EXISTS %s CASCADE
var dropSweepRe = regexp.MustCompile(`(?s)proname LIKE '(xc|xf)\\_%'.{0,600}?DROP FUNCTION IF EXISTS %s CASCADE`)

// createFuncRe 匹配脚本里第一次创建辅助函数的位置。
var createFuncRe = regexp.MustCompile(`CREATE OR REPLACE FUNCTION (xc|xf)_[a-z_]+`)

// stripSQLComments 复用同包 migration_602_test.go 里的实现（语义相同）：
// 注释里提到别的前缀是**解释性文字**，不是函数标识符，
// 不能算作「跨脚本污染」。

// checkAuditSQLIdempotence 返回**违规列表**（空 = 合格）。
// 抽成纯函数是为了让下面两条变异验证能喂进**改坏的副本**。
func checkAuditSQLIdempotence(crossCheck, factors string) []string {
	var bad []string

	// ① 交叉校验：必须先按签名清空 xc_% 家族，且这段必须**在**创建函数之前。
	sweep := dropSweepRe.FindStringIndex(crossCheck)
	create := createFuncRe.FindStringIndex(crossCheck)
	if sweep == nil {
		bad = append(bad, "交叉校验脚本缺少「按签名清空 xc_% 辅助函数」的 DO 块 ⇒ 同一库上跑第二遍会报 "+
			"function xc_time(regclass, integer, integer) is not unique")
	}
	if create == nil {
		bad = append(bad, "交叉校验脚本里找不到 CREATE OR REPLACE FUNCTION xc_*")
	}
	if sweep != nil && create != nil && sweep[0] > create[0] {
		bad = append(bad, "交叉校验脚本的清理块写在建函数**之后** ⇒ 清理不生效")
	}

	// ② 因子分解：必须清空 xf_% 家族，且**不得**再出现 xc_ 前缀（跨脚本隔离）。
	if !dropSweepRe.MatchString(factors) || !strings.Contains(factors, "xf\\_%") {
		bad = append(bad, "因子分解脚本缺少「按签名清空 xf_% 辅助函数」的 DO 块 ⇒ 不可重复执行")
	}
	if strings.Contains(stripSQLComments(factors), "xc_") {
		bad = append(bad, "因子分解脚本里仍出现 xc_ 前缀 ⇒ 与交叉校验脚本共用函数族，会互相污染")
	}
	if create := createFuncRe.FindStringIndex(factors); create == nil ||
		!strings.HasPrefix(factors[create[0]:], "CREATE OR REPLACE FUNCTION xf_") {
		bad = append(bad, "因子分解脚本的辅助函数应统一用 xf_ 前缀")
	}

	// ③ 两个脚本的函数族必须不重叠。
	if strings.Contains(stripSQLComments(crossCheck), "xf_") {
		bad = append(bad, "交叉校验脚本里出现 xf_ ⇒ 与因子分解脚本共用函数族")
	}
	return bad
}

// TestAuditSQLIsIdempotentAndIsolated —— 两个 877 基准 SQL 必须能重复执行、且互不污染。
func TestAuditSQLIsIdempotentAndIsolated(t *testing.T) {
	crossCheck := readAuditFile(t, crossCheckSQLPath)
	factors := readAuditFile(t, factorsSQLPath)

	if bad := checkAuditSQLIdempotence(crossCheck, factors); len(bad) > 0 {
		for _, b := range bad {
			t.Errorf("877 基准 SQL 的幂等性/隔离性不合格：%s", b)
		}
	}
}

// TestAuditSQLIdempotenceGateDetectsRegression —— ★ 变异验证：门必须能变红。
//
// 变异 1：把交叉校验脚本的清理 DO 块删掉（= 回到 2026-10-20 修之前的版本）。
// 变异 2：把因子分解脚本的 xf_ 前缀改回 xc_（= 回到跨脚本互相污染的版本）。
func TestAuditSQLIdempotenceGateDetectsRegression(t *testing.T) {
	crossCheck := readAuditFile(t, crossCheckSQLPath)
	factors := readAuditFile(t, factorsSQLPath)

	// 变异 1：删掉交叉校验的清理块。
	mutated := regexp.MustCompile(`(?s)DO \$\$.*?END\n\$\$;\n`).ReplaceAllString(crossCheck, "")
	if mutated == crossCheck {
		t.Fatalf("★ 变异 1 没生效：交叉校验脚本里找不到那段清理 DO 块（脚本结构变了？）")
	}
	if bad := checkAuditSQLIdempotence(mutated, factors); len(bad) == 0 {
		t.Errorf("★ 变异 1（删掉交叉校验的清理块）后门仍然全绿 ⇒ 这道门测不到它")
	}

	// 变异 2：因子分解的 xf_ 改回 xc_。
	mutatedFactors := strings.ReplaceAll(factors, "xf_", "xc_")
	if mutatedFactors == factors {
		t.Fatalf("★ 变异 2 没生效：因子分解脚本里找不到 xf_ 前缀")
	}
	if bad := checkAuditSQLIdempotence(crossCheck, mutatedFactors); len(bad) == 0 {
		t.Errorf("★ 变异 2（因子分解改回 xc_ 前缀）后门仍然全绿 ⇒ 这道门测不到跨脚本污染")
	}
}

// checkDecisionDocReproduction 返回**违规列表**（空 = 合格）。
// 抽成纯函数，便于下面做变异验证。
func checkDecisionDocReproduction(doc string) []string {
	var bad []string
	for _, name := range []string{
		"2026-10-19-877-write-amplification-cross-check.sql",
		"2026-10-19-877-write-amplification-factors.sql",
	} {
		// 文档里 `psql ... -f /tmp/<name>` 用的文件名，必须与仓库里的文件名逐字一致。
		re := regexp.MustCompile(`-f\s+/tmp/([^\s'"]+\.sql)`)
		matches := re.FindAllStringSubmatch(doc, -1)
		if len(matches) == 0 {
			bad = append(bad, "决策文档的复现段里找不到 `psql -f /tmp/xxx.sql`（`"+name+"` 无从执行）")
			continue
		}
		found := false
		for _, m := range matches {
			if m[1] == name {
				found = true
			}
		}
		if !found {
			bad = append(bad, "决策文档的复现段里没有 `-f /tmp/"+name+"`（实际写的是别的文件名）")
		}
	}
	// 必须出现「把文件拷到 PG 所在机」的步骤（docker cp / scp）。
	if !strings.Contains(doc, "docker cp") || !strings.Contains(doc, "scp") {
		bad = append(bad, "决策文档的复现段缺少「把 SQL 拷到 PG 所在机」的步骤（scp + docker cp）")
	}
	return bad
}

// TestDecisionDocReproductionCommandsAreExecutable —— 决策文档的复现段必须**照抄能跑**。
//
// 2026-10-20 实测发现该段两处对不上：
//
//	① `docker cp` 拷过去的文件名与 `psql -f` 引用的文件名不一致
//	   （连字符 vs 下划线）—— 在**脏容器**上因为有旧文件而侥幸跑通，
//	   在干净容器上必然 "No such file or directory"；
//	② 没说清「交叉校验必须在 PG 所在机上执行」（psql 在容器内，文件要先拷进去）。
func TestDecisionDocReproductionCommandsAreExecutable(t *testing.T) {
	doc := readAuditFile(t, decisionDocPath)
	for _, b := range checkDecisionDocReproduction(doc) {
		t.Errorf("决策文档的复现段照抄不能跑：%s", b)
	}
}

// TestDecisionDocGateDetectsWrongFileName —— ★ 变异验证：文档门必须能变红。
//
// 变异：把复现段里的 `-f /tmp/<仓库文件名>` 改回 2026-10-20 之前那个
// 对不上的名字（连字符 vs 下划线）⇒ 门必须报红。
func TestDecisionDocGateDetectsWrongFileName(t *testing.T) {
	doc := readAuditFile(t, decisionDocPath)
	mutated := strings.ReplaceAll(doc,
		"-f /tmp/2026-10-19-877-write-amplification-cross-check.sql",
		"-f /tmp/877_write_amplification_cross_check.sql")
	if mutated == doc {
		t.Fatalf("★ 变异没生效：复现段里找不到 `-f /tmp/2026-10-19-877-write-amplification-cross-check.sql`")
	}
	if bad := checkDecisionDocReproduction(mutated); len(bad) == 0 {
		t.Errorf("★ 变异后门仍然全绿 ⇒ 这道门测不到「文件名对不上」")
	}
}
