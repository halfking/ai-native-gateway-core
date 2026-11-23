package dbinit

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ============================================================================
// migration_auto_enum_order_test.go
//
// 「自动枚举通道」的依赖序守卫 —— 2026-11-24 12h 修订审计轮**第二轮**新增。
//
// ── 为什么要有这道门（A1 的实测复现）────────────────────────────────────
//
// ★★★ 上一轮把补列迁移从 994 改成 **973**（撞号 994_session_analytics_owner_key_and_close_path
// ★★★ ⇒ 迁移号唯一性门红），**改完之后**这道门不存在 ⇒ **新缺陷静默落地**：
// ★★★ 973 声明 `Dependencies: 983 (request_metrics表)` ⇒ **依赖号 983 > 自身号 973**
// ★★★ ⇒ 而仓内**全部 9xx 迁移的依赖号都小于自身号**（997→547、998→997/606、955→922
// ★★★ 全是降序）⇒ 973 是**全仓唯一**的逆序依赖。
//
// ★★★ 危害面与「两条显式通道的顺序写错」**完全不同**：
// ★★★  · installer/internal/dbinit/runner.go 的 StartupFiles（显式清单）
// ★★★  · scripts/apply-db-revision-sequence.sh 的 files=()（显式清单）
// ★★★ 这两条是**人手写的顺序** ⇒ 写错时，改号的人通常会顺手把它排对。
// ★★★ 而下面两条是**自动枚举**（只按文件名/数字前缀排序，**不读 Dependencies 头**）：
// ★★★  · installer/internal/upgrader/offline_apply.go 的 forwardMigrationFiles
// ★★★    （★ 数字前缀升序 ⇒ 973 < 983 ⇒ 补列件排在建表件之前
// ★★★      ⇒ 且 executeSQLFile 用 ON_ERROR_STOP=1 ⇒ **离线升级整体中止**）
// ★★★  · scripts/init-local-db.sh 的 `[0-9]*.sql` glob（★ 字典序 ⇒ 同上
// ★★★      ⇒ 失败只计数 ⇒ 建表但无该列 ⇒ 落库必 42703）
//
// ★★★ ⇒ **既有门（runner_990_order_test.go 等）只覆盖两条显式通道** ⇒
// ★★★   **检不出自动枚举通道的逆序** ⇒ 这就是「改号引入了新缺陷却没人发现」的原因。
//
// ── 这道门断言什么 ───────────────────────────────────────────────────────
//
// ★ ① **不变量**：任何 startup 迁移声明的依赖号都 **小于自身号**。
// ★   （★ 这条是自动枚举通道能工作的**前提** ⇒ 违反它 ⇒ 自动枚举通道必然乱序。）
// ★ ② **实测两种自动枚举排序**（字典序 + 数字前缀升序）里，
// ★   每个「消费者」都排在它的「生产者」之后。
//
// ★ ① 与 ② 的分工：① 是**根因门**（一眼看出谁逆序）；② 是**行为门**
// ★（即便有人用字母后缀绕过了 ①，② 仍按实际排序口径复核）。
//
// ── 为什么 ① 不能靠「手写依赖清单」代替 ─────────────────────────────────
//
// ★ 现有门（runner_990_order_test.go）用的是**手写** startupDependency 清单：
// ★ 每加一条迁移就要记得往清单里补一行 ⇒ ★ **漏补就是漏检** ⇒
// ★ ★★ 本门改为**从磁盘扫描每个迁移文件头的 `-- Dependencies:` 行** ★★
// ★ ⇒ 新迁移只要写了 Dependencies 头，就自动进入检查面 ⇒ 无人需要记得。
//
// ── 依赖头的解析口径 ─────────────────────────────────────────────────────
//
// ★ 迁移头里依赖号的写法有多种形态（实测）：
// ★   `-- Dependencies: 983 (request_metrics表)`
// ★   `-- Dependencies: 997 (tasks 表), 606 (agent_type/expert_type/tags 基线)`
// ★   `-- Dependencies: 547 (project_dim 表)`
// ★   `-- Dependencies: 无 (独立表)` / `-- Dependencies:`（空）
// ★ ⇒ 口径：**只取 Dependencies 行里出现的、且在 startup 目录里真实存在的三位数号**
// ★   （★ 这样既拿到了号，又天然排除了 "00-prereqs"、日期、以及**非迁移号**的散文数字）。
//
// ── 与字母后缀记法的关系 ─────────────────────────────────────────────────
//
// ★ 仓内既有「主号+字母后缀」记法（361b / 487b / 478p2 …）：它的 versionPrefix
// ★ 解析成**主号** ⇒ 排序时与主号同组、按文件名先后 ⇒ ★★ 「字母后缀」天然
// ★★ 排在「主号」之后（'983_' < '983b_'）★★ ⇒ ★ 所以 983b 这类补列件
// ★ 天然满足 ② 的实测排序门 ⇒ 不需要为它开后门。
// ★ ★★ 但 ①（依赖号 < 自身号）对字母后缀**不适用**：983b 的自身号是 983b，
// ★ ★★ 而它的依赖 983 < 983b（按主号比）★★ ⇒ 成立 ⇒ ★ 所以 ① 要按
// ★ ★★ **主号**（数字部分）来比，不能按字面字符串比 ★★（否则 '983b' > '983'
// ★ ★★ 的字面比较会把 983 当成比 983b 大 ⇒ 误报）。
//
// ============================================================================

// autoEnumStartupDir 是 canonical startup 迁移目录（相对本包）。
const autoEnumStartupDir = "../../../sql/migrations/startup"

// migrationDependencyLine 匹配迁移头里的 Dependencies 声明行。
var migrationDependencyLine = regexp.MustCompile(`(?im)^\s*--\s*Dependencies\s*:`)

// dependencyNumberPattern 从 Dependencies 行里抽三位数号。
var dependencyNumberPattern = regexp.MustCompile(`\b[0-9]{3}\b`)

// autoEnumMigration 是一个 startup 迁移的编号信息。
type autoEnumMigration struct {
	base string   // 文件名（含 .sql）
	num  int      // 数字主号（★ 字母后缀折叠回主号 ★）
	suf  string   // 后缀（"983b" 的 "b"；纯数字号为空）
	deps []int    // 声明的依赖主号
}

// autoEnumParseNumber 把文件名前缀解析成（主号, 后缀）。
//
// ★ 与 installer/internal/upgrader/offline_apply.go 的 versionPrefix **同口径**：
// ★ 取第一个 "_" 之前的部分做 strconv.Atoi ⇒ 失败即「无数字前缀」。
// ★ 字母后缀（"983b"）⇒ Atoi 失败 ⇒ ★★ 但本门要的是**主号** ⇒
// ★ 所以这里额外做一次「剥掉尾部字母」的解析（★ 与仓内 versionPrefix 的
// ★ **排序**行为一致：它把 983b 排到 983 之后，靠的正是同号按文件名比 ★★）。
func autoEnumParseNumber(base string) (int, string, bool) {
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, "", false
	}
	prefix := base[:idx]
	if v, err := strconv.Atoi(prefix); err == nil {
		return v, "", true
	}
	// 字母后缀记法：剥掉尾部的小写字母 ⇒ "983b" → (983, "b")。
	trimmed := strings.TrimRight(prefix, "abcdefghijklmnopqrstuvwxyz")
	if trimmed == prefix || trimmed == "" {
		return 0, "", false
	}
	v, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, "", false
	}
	return v, prefix[len(trimmed):], true
}

// autoEnumMainMigrationOf 给出某个主号下的**主迁移**文件名。
//
// ★ 为什么需要它：★ 一个主号下可能有多个文件（★ 983_request_metrics.sql +
// ★ 983b_add_raw_model_name.sql + 983c_add_occurred_cursor_index.sql ——
// ★ 主迁移 + 同族序篇/补列件）⇒ ★ 依赖头里写的「983」指的是**主迁移**
// ★ （建表件），**不是**同族的补列件 ★
// ★ ⇒ ���则「983b 与 983c 互为生产者」⇒ 门会误报（★ 本门第一版就踩了这个坑 ★）。
//
// ★ 主迁移的判定：文件名以 "<号>_" 开头（★ 注意主号后是**下划线** ⇒
// ★ '983b_' 不匹配 '^983_' ⇒ 自动排除同族后缀件 ★）。
func autoEnumMainMigrationOf(byNum map[int][]string, num int) (string, bool) {
	prefix := strconv.Itoa(num) + "_"
	for _, b := range byNum[num] {
		if strings.HasPrefix(b, prefix) {
			return b, true
		}
	}
	return "", false
}

// autoEnumLoadMigrations 扫描 canonical startup 目录，读出每个迁移的编号与依赖声明。
func autoEnumLoadMigrations(t *testing.T) []autoEnumMigration {
	t.Helper()
	entries, err := os.ReadDir(autoEnumStartupDir)
	if err != nil {
		t.Fatalf("读 startup 迁移目录失败（%s）：%v", autoEnumStartupDir, err)
	}
	// 先收一遍全部文件名 ⇒ 后面解析依赖时要判「这个号真实存在吗」。
	exists := map[int]bool{}
	var bases []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		bases = append(bases, name)
		if v, _, ok := autoEnumParseNumber(name); ok {
			exists[v] = true
		}
	}
	sort.Strings(bases)

	var out []autoEnumMigration
	for _, name := range bases {
		num, suf, ok := autoEnumParseNumber(name)
		if !ok {
			// 无数字前缀的文件（bootstrap 类）不参与本门。
			continue
		}
		m := autoEnumMigration{base: name, num: num, suf: suf}

		raw, err := os.ReadFile(filepath.Join(autoEnumStartupDir, name))
		if err != nil {
			t.Fatalf("读迁移文件失败（%s）：%v", name, err)
		}
		text := string(raw)
		if loc := migrationDependencyLine.FindStringIndex(text); loc != nil {
			// 只取该行（到行尾）★ 避免把文件正文里的数字误当依赖号。
			line := text[loc[0]:]
			if nl := strings.IndexByte(line, '\n'); nl >= 0 {
				line = line[:nl]
			}
			for _, tok := range dependencyNumberPattern.FindAllString(line, -1) {
				v, err := strconv.Atoi(tok)
				if err != nil {
					continue
				}
				// ★ 只认「真实存在的迁移号」⇒ 排除散文里的数字。
				if !exists[v] {
					continue
				}
				m.deps = append(m.deps, v)
			}
		}
		out = append(out, m)
	}
	return out
}

// ── 门 ①：依赖号必须小于自身号（自动枚举通道能工作的前提）─────────────────

func TestStartupMigrationsDeclareOnlyLowerDependencyNumbers(t *testing.T) {
	migrations := autoEnumLoadMigrations(t)
	if len(migrations) == 0 {
		t.Fatalf("没扫到任何编号迁移 ⇒ 枚举面失效 ⇒ 本门会恒绿")
	}
	byNum := map[int][]string{}
	for _, m := range migrations {
		byNum[m.num] = append(byNum[m.num], m.base)
	}

	for _, m := range migrations {
		for _, dep := range m.deps {
			if dep < m.num {
				continue
			}
			// dep == m.num：同号（同族序篇）⇒ 合法（如 983b 依赖 983 ⇒
			// ★ 本门按**主号**比，983b 的主号是 983 ⇒ dep(983) == num(983)
			// ★ ⇒ 会被这条判红 ⇒ 所以**必须**按字面 token 比才区分得开。
			//
			// ★ 精确口径：依赖号 == 自身主号时，只有当依赖的那个文件**名不同**
			// ★ 且**字典序更早**才是合法的（同族序篇补列件：'983_' < '983b_'）。
			if dep == m.num {
				producers := byNum[dep]
				ok := false
				for _, p := range producers {
					if p != m.base && p < m.base {
						ok = true
						break
					}
				}
				if ok {
					continue
				}
			}
			t.Errorf("迁移 %s 声明依赖 %d ⇒ **依赖号 ≥ 自身号**（自身主号 %d）\n"+
				"  ★ 这在「自动枚举通道」里是致命的：\n"+
				"    · installer/internal/upgrader/offline_apply.go 按数字前缀升序\n"+
				"    · scripts/init-local-db.sh 按字典序 glob\n"+
				"    ⇒ 两者都**不读 Dependencies 头** ⇒ 消费者会排在生产者之前\n"+
				"    ⇒ ALTER/CREATE 落空（建表但无列 ⇒ 落库 42703）或离线升级整体中止。\n"+
				"  ★ 修法：改号到**大于**依赖号且仓内已有合法形态的位置\n"+
				"    （★ 字母后缀补列件如 983b_…：versionPrefix 折叠回主号 ⇒\n"+
				"      排序与 983_ 同组、按文件名 ⇒ '983_' < '983b_' ⇒ 排在生产者之后）。",
				m.base, dep, m.num)
		}
	}
}

// ── 门 ②：按两种自动枚举排序实测，consumer 必须晚于 producer ────────────

// autoEnumSortLexical 复刻 scripts/init-local-db.sh 的字典序 glob 展开顺序。
func autoEnumSortLexical(files []string) []string {
	out := append([]string(nil), files...)
	sort.Strings(out)
	return out
}

// autoEnumSortNumericPrefix 复刻 installer/internal/upgrader/offline_apply.go 的
// migrationPathLess：**数字前缀升序**；同号时**按文件名**先后。
func autoEnumSortNumericPrefix(files []string) []string {
	out := append([]string(nil), files...)
	sort.SliceStable(out, func(i, j int) bool {
		vi, _, oki := autoEnumParseNumber(out[i])
		vj, _, okj := autoEnumParseNumber(out[j])
		switch {
		case oki && okj:
			if vi != vj {
				return vi < vj
			}
			return out[i] < out[j]
		case oki:
			return false
		case okj:
			return true
		default:
			return out[i] < out[j]
		}
	})
	return out
}

func autoEnumIndexOf(order []string, name string) int {
	for i, n := range order {
		if n == name {
			return i
		}
	}
	return -1
}

func TestAutoEnumChannelsRespectDeclaredDependencies(t *testing.T) {
	migrations := autoEnumLoadMigrations(t)
	if len(migrations) == 0 {
		t.Fatalf("没扫到任何编号迁移 ⇒ 枚举面失效 ⇒ 本门会恒绿")
	}

	var files []string
	producerOf := map[string][]string{} // consumer base → 依赖生产者文件
	for _, m := range migrations {
		files = append(files, m.base)
	}
	// 建「主号 → 文件名」索引。
	byNum := map[int][]string{}
	for _, m := range migrations {
		byNum[m.num] = append(byNum[m.num], m.base)
	}
	for _, m := range migrations {
		for _, dep := range m.deps {
			// ★ 依赖指向该号的**主迁移**（★ 不是同族序篇/补列件 ★）。
			p, ok := autoEnumMainMigrationOf(byNum, dep)
			if !ok || p == m.base {
				continue
			}
			producerOf[m.base] = append(producerOf[m.base], p)
		}
	}

	orders := []struct {
		name  string
		order []string
	}{
		{"字典序（scripts/init-local-db.sh 的 [0-9]*.sql glob）", autoEnumSortLexical(files)},
		{"数字前缀升序（installer/internal/upgrader/offline_apply.go 的 forwardMigrationFiles）", autoEnumSortNumericPrefix(files)},
	}

	for _, o := range orders {
		for consumer, producers := range producerOf {
			ci := autoEnumIndexOf(o.order, consumer)
			if ci < 0 {
				t.Fatalf("排序结果里找不到 %s ⇒ 枚举面失效", consumer)
			}
			for _, producer := range producers {
				pi := autoEnumIndexOf(o.order, producer)
				if pi < 0 {
					t.Fatalf("排序结果里找不到 %s ⇒ 枚举面失效", producer)
				}
				if pi > ci {
					t.Errorf("自动枚举通道【%s】里依赖顺序错：生产者 %s 在 %d，"+
						"但消费者 %s 在 %d ⇒ 消费者先跑 ⇒ 它的 ALTER/CREATE 会落空\n"+
						"  ★ 这条通道**不读** Dependencies 头、只按文件名/数字前缀排序 ⇒\n"+
						"    ⇒ 唯一可靠的保证是「消费者在文件名排序上晚于生产者」。",
						o.name, producer, pi, consumer, ci)
				}
			}
		}
	}
}

// ── 门 ③：量具自检 —— 门 ② 必须真的能检出乱序（变异臂）───────────────────
//
// ★ 没有变异臂的门可能在「枚举面退化成空集」时恒绿 ⇒ 这里用一组
// ★ 人工构造的乱序数据证明排序比较本身有牙。
func TestAutoEnumOrderComparisonDetectsReversal(t *testing.T) {
	lexical := autoEnumSortLexical([]string{"983_request_metrics.sql", "983b_add_raw_model_name.sql"})
	if autoEnumIndexOf(lexical, "983b_add_raw_model_name.sql") <= autoEnumIndexOf(lexical, "983_request_metrics.sql") {
		t.Fatalf("变异臂失效：字典序里 '983b_' 应当晚于 '983_'，实际 %v", lexical)
	}
	numeric := autoEnumSortNumericPrefix([]string{"983b_add_raw_model_name.sql", "983_request_metrics.sql"})
	if autoEnumIndexOf(numeric, "983b_add_raw_model_name.sql") <= autoEnumIndexOf(numeric, "983_request_metrics.sql") {
		t.Fatalf("变异臂失效：数字前缀升序里 '983b_' 应当晚于 '983_'，实际 %v", numeric)
	}
	// ★ 反向：把文件名换成「补列件用小号」⇒ 必须被检出为乱序。
	bad := autoEnumSortLexical([]string{"973_add_raw_model_name.sql", "983_request_metrics.sql"})
	if autoEnumIndexOf(bad, "973_add_raw_model_name.sql") > autoEnumIndexOf(bad, "983_request_metrics.sql") {
		t.Fatalf("变异臂失效：小号补列件（973）本应排在 983 之前（这正是 973 的缺陷），"+
			"实际 %v ⇒ 比较逻辑不可信", bad)
	}
}
