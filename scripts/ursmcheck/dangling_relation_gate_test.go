// dangling_relation_gate_test.go —— ★ 悬空引用门（★ 工具与门【同源】：同一份 `internal/relrefs`）。
//
// # 它守的不变量
//
//	「产品按名引用的关系，仓内必须有 DDL 建出它」
//	⇒ 差集 = **悬空引用** = (产品强命中的关系名) \ (仓内 DDL 建出的关系名)
//
// ★★★ 为什么值得做成门（★ 而不是一次性脚本）：
//
//	悬空引用有两种成因，处理方式完全相反：
//	  ① 那张表**由别的产品同库提供** ⇒ ⇒ **不是缺口**（R63 §6：98 条候选里 93 条是这种）
//	  ② 那张表**该建而没建**      ⇒ ⇒ **是真缺口**
//	⇒ ⇒ ★★ 两者在【一次性输出】里长得一模一样 ⇒ ⇒ ★★ 只有【把清单钉死】才能
//	       ① 让 ① 不再被反复重新讨论 ② 让 ② 一旦新增就立刻变红。
//	⇒ ⇒ ★★ **⇒ 门不负责判断「是不是缺口」，它只负责「缺口清单不许悄悄变」**。
//
// # 三条判据
//
//	① 悬空引用 ⊆ 豁免表（豁免表 = 已逐条定性的清单，每条带理由）
//	② 豁免表 ⊆ 悬空引用（★ 豁免表不许【提前豁免不存在的问题】⇒ 防止豁免表被做大）
//	③ 每条豁免都必须有非空理由（★ 「不写理由就豁免」这条路必须堵死）
//
// ★★ 附带：工具自身的 7 项自检（搜索面/无截断/清单可用性/解析器有牙/拼接守卫/
//
//	剥注释/幂等）也在这道门里跑 ⇒ ⇒ **量具坏了 ⇒ 门直接红**（★ 而不是「门绿、量具瞎了」）。
//
// ★★ 「为什么工具与门同源」（★ 抽 internal/relrefs 的理由）：
//
//	若门靠 `go run` 调工具 ⇒ ⇒ ① 慢 ② **口径可能漂移**（工具改了、门没改 ⇒ 门还在测旧口径）
//	⇒ ⇒ 抽成可导入包 ⇒ ⇒ 门与工具【同一次编译】⇒ ⇒ 口径不可能漂移。
package ursm_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/relrefs"
)

// repoRoot = 仓根（★ 本门在 scripts/ursmcheck/ 下 ⇒ 往上两级）。
const repoRoot = "../.."

// exemptedDanglingRefs = 已定性的悬空引用清单（★ 名字 → 理由）。
//
// ★★ 【每条都必须有理由】—— ★ 理由不是注释，是判据（判据 ③ 会检查它非空）。
//
// ★★ 【不许把表做大】—— ★ 判据 ② 会检查「豁免表里的每一项都仍然真的存在」。
//
//	⇒ ⇒ 所以「提前豁免一个还不存在的问题」会在下一次运行时被抓出来。
var exemptedDanglingRefs = map[string]string{
	// ── PostgreSQL 系统目录（pg_catalog）—— 由 PG 自身提供，仓内当然不建 ──
	// ★ 这 11 个不是缺口 ⇒ 它们的「不在 DDL 集合里」是**正确**的。
	"pg_am":               "PG 系统目录（pg_catalog）：由 PostgreSQL 自身提供 ⇒ 仓内不建是正确形态 ⇒ 不是缺口",
	"pg_attribute":        "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_class":            "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_constraint":       "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_index":            "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_indexes":          "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_inherits":         "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_namespace":        "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_policies":         "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_stat_user_tables": "PG 系统目录（pg_catalog）⇒ 不是缺口",
	"pg_trigger":          "PG 系统目录（pg_catalog）⇒ 不是缺口",

	// ── ★★★ 真悬空引用（★ 全仓唯一一条）──
	//
	// ★ 证据（★ 机器复核，非推断）：
	//   · baseline `sql/schema/01-schema.sql`：`prompt_injection_rules` 有 20 处，
	//     而 `prompt_injection_approvals` **0 处**
	//   · startup 迁移 `sql/migrations/startup/315_prompt_injection_detection.sql`：
	//     建 `prompt_injection_policies` + `prompt_injection_rules`，**没有** approvals
	//   · 全仓 `.sql`：**0 命中**
	//   ⇒ ⇒ **全新安装的库里这张表必然不存在**
	//   · 而 `domains/promptinjection/detector.go` 有写路径：
	//     `INSERT INTO prompt_injection_approvals (…)`
	//     ⇒ 触发条件 = `result.RequireApproval && result.ActionTaken == "approve"`
	//     ⇒ 失败处理 = **只 `slog.Warn`**（不 panic、不阻断）
	//     ⇒ ⇒ **后果 = 审批请求静默丢失**（不是崩溃）
	//
	// ★ 为什么【不】在这里直接判它是缺陷：★ 生产库上这张表**可能存在**
	//   （历史迁移 / 手工建 / 别的产品同库提供）⇒ ⇒ 「新装缺表」与「生产有表」
	//   **可以同时成立** ⇒ ⇒ ⇒ 需属主裁决（是补建、还是同库共用、还是审批流该退役）。
	//   ⇒ ⇒ 本门只负责把它【钉在台面上】，不替属主拍板。
	"prompt_injection_approvals": "★★★ 真悬空引用：全仓无任何 DDL 建它（baseline 0 处 / 迁移链 0 处 / 全仓 .sql 0 命中）" +
		"⇒ 全新安装的库缺这张表；而 domains/promptinjection/detector.go 有写路径且失败只记 Warn ⇒ 审批请求静默丢失" +
		"⇒ ★ 生产库上可能存在 ⇒ 需属主裁决（补建 / 同库共用 / 审批流退役）⇒ 本门只负责让它可见",
}

// ★★ 三条判据共用【同一份】扫描结果（sync.Once）⇒
//
//	⇒ 全仓扫描只跑一次（★ 否则三条判据各跑一遍 ⇒ 门要多花 30s）
var (
	danglingOnce  sync.Once
	danglingCache map[string]int
	danglingErr   error
)

// danglingRefsNow = 当前的悬空引用名字集合（★ 只扫一次 ⇒ 三条判据共用）。
func danglingRefsNow(t *testing.T) map[string]int {
	t.Helper()
	danglingOnce.Do(func() {
		danglingCache, danglingErr = computeDanglingRefs()
	})
	if danglingErr != nil {
		if errors.Is(danglingErr, errNotInRepo) {
			t.Skipf("不在仓内 ⇒ 跳过")
		}
		t.Fatalf("提取悬空引用失败：%v", danglingErr)
	}
	return danglingCache
}

// computeDanglingRefs 真正跑一遍提取器。
func computeDanglingRefs() (map[string]int, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil, errNotInRepo
	}
	sqlFiles, err := relrefs.RepoFiles(root, "*.sql")
	if err != nil {
		return nil, err
	}
	goFiles, err := relrefs.RepoFiles(root, "*.go")
	if err != nil {
		return nil, err
	}
	ddl, err := relrefs.ScanDDLRepo(root, sqlFiles)
	if err != nil {
		return nil, err
	}
	ps, err := relrefs.ScanProductRepo(root, goFiles, ddl)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range ps.Refs() {
		if !r.Strong {
			continue
		}
		if ddl.Has(r.Name) {
			continue
		}
		out[r.Name]++
	}
	return out, nil
}

// errNotInRepo = 「不在仓内」的哨兵错误（★ ⇒ 门跳过而不是红）。
var errNotInRepo = errors.New("不在仓内 ⇒ 跳过")

// notExempted = 判定函数：返回「当前悬空引用里【没被豁免】的名字」。
//
// ★ ★ 判定逻辑单独成函数 ⇒ ⇒ 反向夹具才能直接喂它（★ 门自己有牙的证据）。
func notExempted(current map[string]int, exempt map[string]string) []string {
	var out []string
	for name := range current {
		if _, ok := exempt[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestDanglingRelationReferencesAreExempted：★ 主判据 —— 悬空引用必须都在豁免表里。
func TestDanglingRelationReferencesAreExempted(t *testing.T) {
	current := danglingRefsNow(t)
	if len(current) == 0 {
		t.Fatalf("当前悬空引用集合为空 ⇒ ★ 量具瞎了（★ 「一个都没有」通常意味着提取器坏了，不是仓库干净）")
	}
	got := notExempted(current, exemptedDanglingRefs)
	if len(got) > 0 {
		var detail []string
		for _, n := range got {
			detail = append(detail, "  "+n+"（"+strconv.Itoa(current[n])+" 处）")
		}
		t.Fatalf("悬空引用清单变了：%d 个新名字【没有】被豁免 ⇒\n%s\n"+
			"  ★ 新增悬空引用 ⇒ 请逐条定性后加进 exemptedDanglingRefs（★ 必须写理由）\n"+
			"  ★ 若判定为「PG 系统目录 / 别的产品同库提供 / 解析器假阳性」⇒ 也请写进豁免表并说明依据",
			len(got), strings.Join(detail, "\n"))
	}
}

// TestDanglingRelationExemptionTableIsNotInflated：★ 豁免表不许【提前豁免不存在的问题】。
//
// ★ 为什么这条必须有：★ 若豁免表可以随便写名字 ⇒ ⇒ 「先豁免掉，省得以后再讨论」
//
//	就会把**未来**的真缺口也一起掩盖掉 ⇒ ⇒ 门就白建了。
func TestDanglingRelationExemptionTableIsNotInflated(t *testing.T) {
	current := danglingRefsNow(t)
	var stale []string
	for name := range exemptedDanglingRefs {
		if _, ok := current[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("豁免表里有 %d 项【已经不在当前悬空引用集合里】：%v\n"+
			"  ★ ⇒ 说明它们要么已被修掉、要么是误登记 ⇒ 请删掉这些条目\n"+
			"  ★ ⇒ 豁免表必须与现盘【逐项对齐】⇒ 不许留空条目",
			len(stale), stale)
	}
}

// TestDanglingRelationExemptionsMustHaveReason：★ 每条豁免都必须有非空理由。
func TestDanglingRelationExemptionsMustHaveReason(t *testing.T) {
	var bad []string
	for name, reason := range exemptedDanglingRefs {
		if strings.TrimSpace(reason) == "" {
			bad = append(bad, name)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("豁免表里有 %d 项没有写理由：%v\n"+
			"  ★ 「不写理由就豁免」这条路必须堵死 ⇒ 理由是判据的一部分，不是注释",
			len(bad), bad)
	}
}

// TestDanglingRelationGateHasTeeth：★ 反向夹具 —— 判定函数必须能判红。
//
// ★ ★ 若这条红了 ⇒ 说明 notExempted 恒返回空 ⇒ ⇒ 门是【恒绿门】。
func TestDanglingRelationGateHasTeeth(t *testing.T) {
	// ★ 夹具：造一个【新的】悬空引用，看判定函数会不会放过它。
	cur := map[string]int{
		"pg_class":               48, // 已豁免
		"brand_new_dangling_tbl": 3,  // ★ 未豁免 ⇒ 必须被抓出来
	}
	got := notExempted(cur, exemptedDanglingRefs)
	if len(got) != 1 || got[0] != "brand_new_dangling_tbl" {
		t.Fatalf("反向夹具失败：判定函数返回 %v，期望恰好 [brand_new_dangling_tbl] ⇒ 门没牙", got)
	}
}

// TestDanglingRelationToolSelfCheck：★ 工具自身的 7 项自检 ⇒ ★ 量具坏了 ⇒ 门直接红。
//
// ★★★ 这条是本门最重要的设计：★
//
//	「门绿、量具瞎了」是本仓反复出现的失败模式 ⇒ ⇒ ★ 所以门里必须**跑量具的自检**。
//	⇒ ⇒ 量具的搜索面被截断 / 解析器失灵 / 拼接守卫失效 ⇒ ⇒ **门当场红**。
func TestDanglingRelationToolSelfCheck(t *testing.T) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("解析仓根失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skipf("不在仓内（%v）⇒ 跳过", err)
	}
	if err := relrefs.RunSelfTest(root); err != nil {
		t.Fatalf("提取器自检失败 ⇒ ★ 量具坏了 ⇒ 本门的所有读数都不可信：%v", err)
	}
}
