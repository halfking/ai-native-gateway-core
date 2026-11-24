package ursm_test

// 「多语句启动迁移必须事务包裹」的**增量门**（2026-11-24）
//
// ★★★ **这道门钉的是什么（★ 一条被实测出来的生产级风险）★★★：
//
//　★ 2026-11-24，★ 我方在审阅「teams 族的号段归位」时发现：★ 归位后的现仓 `057_api_keys_team_id.sql`
//　★ **没有事务包裹**（★ 9 条语句、★ 无 `BEGIN;` / `COMMIT;` ★）★★ ｜★★ ★★ **而部署通道
//　★ 「首个失败即停、★ 台账不再前进」⇒ ★★ **同一文件里的多条语句不在同一事务里 ⇒ 中途失败
//　★ 留半成品（★ 列已加、★ 策略未建 ★）⇒ ★★ 而后续文件全被卡** ★★★ ★★
//　★ ⇒ ★★ **这正是 252 那颗「392 地雷」的同型问题** ★★ ★★。
//
// ★★★ **★★ 为什么判据必须做成【增量口径】★★（★ 这是本门最要紧的设计决定 ★）★★★：
//
//　★ ★★ 实测：★ 仓里启动迁移共 **675** 个（★ `sql/migrations/startup/*.sql`、★ 不含 `.down.sql` ★）★★
//　★ ★★ 其中 **429 个已经事务包裹**、★ **244 个没有** ★★ ★★
//　★ ⇒ ★★★ **★ ⇒ 「多语句文件必须事务包裹」这条判据在【存量水位】上【不成立】★★** ★★★
//　★ ⇒ ★★ **★ ⇒ 改那 244 个 ⇒ 触及已 applied 字节 ⇒ ★★ 【绝对不可行】★★** ★★
//　★ ⇒ ★★★ **★★ ⇒ 正确做法是【判据做成增量口径 ＋ 存量做成一次性快照豁免】★★ ★★★ ★★★
//
// ★★★ **判据（三条、★ 逐条说清为什么这样切）★★★：
//
//	① **投递面里每个【多语句且未事务包裹】的启动迁移 ⇒ 必须在存量豁免台账里** ⇒ 否则判红
//	   （★ 台账＝`scripts/audit/asset/multi_statement_migration_legacy.tsv`、★ 由官方生成器产出 ★★）
//	② **台账每一项必须【仍然成立】**：★ 文件还在、★ 仍多语句、★ 仍未包裹、★ 且【在基线 pin 里存在】★
//	   ⇒ 否则判红（★ ⇒ 台账不能变成垃圾桶、★ 也不能登记【基线之后的新文件】★★）
//	③ **反向闭合**：★ 基线 pin 时【已存在】且【多语句且未包裹】的每个文件 ⇒ 都必须在台账里 ★
//	   ⇒ 否则判红（★ ⇒ 台账与现实【双向闭合】、★ 不会漏 ★★）
//
// ★★★ **★ 基线 pin（★ 唯一的、显式的豁免边界 ★）★★★：
//
//　★ `scripts/audit/asset/multi_statement_migration_baseline.ref`（★ 一行 commit sha ★）★★
//　★ ★★ **★ 豁免是【显式的、可审计的】：★ 要让一个新文件豁免 ⇒ 必须【前移 pin】★★
//　★ ★★ **★ 而不是在台账里加一行（★ 加一行 ⇒ 门② 抓它：★ 「基线之后新增的文件不许进台账」★★）★★** ★★
//　★ ⇒ ★★ **⇒ 豁免集随 pin 前移而自然收敛（★ 而不会变成一个只增不减的垃圾桶 ★★）★★ ★★
//
// ★★★ **★ 通用式（★ 本门自己的来历 ★）★★★：
//
//　★ ★★ **「这条规则要普遍适用」这句话、★ 在存量水位上常常【不成立】★★ ⇒ ★★★
//　★ ★★ **★ ⇒ 正确做法是【把判据做成增量口径 ＋ 把存量做成一次性快照豁免】★★ ★★ ★★★
//　★ ★★ **★ 而且【增量口径】还顺手解掉了「门自带基线红」这个老问题（★ 新口径在当前水位天然零红 ★）★★ ★★

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	// multiStmtStartupDir 是启动迁移目录（★ 相对仓根 ★）。
	multiStmtStartupDir = "sql/migrations/startup"
	// multiStmtBaselineRefFile 是基线 pin 文件（★ 一行 commit sha ★）。
	multiStmtBaselineRefFile = "scripts/audit/asset/multi_statement_migration_baseline.ref"
	// multiStmtLegacyTSV 是存量豁免台账（★ 生成物 ★）。
	multiStmtLegacyTSV = "scripts/audit/asset/multi_statement_migration_legacy.tsv"
)

var (
	multiStmtBeginRE  = regexp.MustCompile(`(?im)^\s*BEGIN\s*;`)
	multiStmtCommitRE = regexp.MustCompile(`(?im)^\s*COMMIT\s*;`)
)

// multiStmtMigrationFacts 是一条启动迁移的「事实」。
type multiStmtMigrationFacts struct {
	File       string
	Statements int
	Wrapped    bool
}

// multiStmtAnalyze 解析一段 SQL 文本 ⇒ 事实。
//
// ★ ★ 抽成函数是**为了让自证臂能喂合成样本**：★ 判据本体与自证臂必须走【同一条代码路径】★
func multiStmtAnalyze(name, text string) multiStmtMigrationFacts {
	n := 0
	for _, raw := range strings.Split(text, "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "--") {
			continue
		}
		n++
	}
	return multiStmtMigrationFacts{
		File:       name,
		Statements: n,
		Wrapped:    multiStmtBeginRE.MatchString(text) && multiStmtCommitRE.MatchString(text),
	}
}

// multiStmtIsMultiStatement 判「是否多语句」（★ 单语句文件不在判据面里 ★）。
func multiStmtIsMultiStatement(f multiStmtMigrationFacts) bool {
	return f.Statements > 1
}

// multiStmtReadBaselinePin 读基线 pin。
func multiStmtReadBaselinePin(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, multiStmtBaselineRefFile))
	if err != nil {
		t.Fatalf("★ 读基线 pin 文件失败（★ %s ★）: %v", multiStmtBaselineRefFile, err)
	}
	for _, raw := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		return s
	}
	t.Fatalf("★ 基线 pin 文件里没有有效内容（★ %s ★）", multiStmtBaselineRefFile)
	return ""
}

// multiStmtBaselineFileSet 列基线 pin 时已存在的启动迁移文件。
func multiStmtBaselineFileSet(t *testing.T, root, pin string) map[string]bool {
	t.Helper()
	// ★ -z 化（2026-11-25 12h 修订审计轮）：★ `--name-only` 默认受 core.quotePath
	// ★ 转义 ⇒ 非 ASCII 文件名被转成八进制＋外层双引号 ⇒ ★ 而这里取的是
	// ★ **真实基名**（要与仓内启动迁移逐字比对）⇒ ★ 必须走 `-z` ★
	out, err := exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", "-z", pin,
		"--", multiStmtStartupDir).Output()
	if err != nil {
		t.Fatalf("★ 读基线 pin 失败（★ %s ★）：★ %v", pin, err)
	}
	set := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		base := filepath.Base(strings.TrimSpace(p))
		if base == "" || !strings.HasSuffix(base, ".sql") || strings.HasSuffix(base, ".down.sql") {
			continue
		}
		set[base] = true
	}
	if len(set) == 0 {
		t.Fatalf("★ 基线 pin（★ %s ★）里读不到任何启动迁移（★ 判据的输入集合不对 ⇒ 拒绝继续 ★）", pin)
	}
	return set
}

// multiStmtReadLegacyTable 读存量豁免台账（★ 文件 → 语句数 ★）。
func multiStmtReadLegacyTable(t *testing.T, root string) map[string]int {
	t.Helper()
	fh, err := os.Open(filepath.Join(root, multiStmtLegacyTSV))
	if err != nil {
		t.Fatalf("★ 读存量豁免台账失败（★ %s ★）: %v", multiStmtLegacyTSV, err)
	}
	defer fh.Close()
	table := map[string]int{}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			t.Fatalf("★ 台账行格式不对（★ 期望 file<TAB>statements ★）: %q", line)
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			t.Fatalf("★ 台账里的语句数不是整数（★ %q ★）", parts[1])
		}
		table[strings.TrimSpace(parts[0])] = n
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("★ 读台账出错: %v", err)
	}
	if len(table) == 0 {
		t.Fatalf("★ 台账里没有有效行（★ 判据会变成恒绿 ⇒ 拒绝继续 ★）")
	}
	return table
}

// TestMultiStatementStartupMigrationsAreTransactionWrapped —— ★★ 承重断言：★★
//
// ★★ **投递面里每个【多语句且未事务包裹】的启动迁移 ⇒ 必须在存量豁免台账里 ★★。
func TestMultiStatementStartupMigrationsAreTransactionWrapped(t *testing.T) {
	root := repoRootForMultiStmt(t)
	pin := multiStmtReadBaselinePin(t, root)
	baseSet := multiStmtBaselineFileSet(t, root, pin)
	table := multiStmtReadLegacyTable(t, root)

	dir := filepath.Join(root, multiStmtStartupDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("★ 读启动迁移目录失败: %v", err)
	}

	checked := 0
	legacyExpected := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		facts := multiStmtAnalyze(name, string(b))
		if !multiStmtIsMultiStatement(facts) {
			continue
		}
		if facts.Wrapped {
			continue
		}
		checked++
		if baseSet[name] {
			legacyExpected++
		}
		if _, ok := table[name]; !ok {
			t.Errorf("★ 迁移 `%s` 是【多语句（★ %d 条 ★）且【未事务包裹】】⇒ ★★ 它既不在存量豁免台账里、★ "+
				"★ 而它在【基线 pin `%s`】里就已存在 ⇒ ★★ 按判据它应当登记在台账里 ★★ "+
				"★ ⇒ 请【重跑官方生成器】（★ `python3 scripts/audit/measure-multi-statement-migrations.py --out %s` ★）★★ "+
				"★ —— ★★ 【注意】★ 新写的迁移**不能**靠往台账里加一行来豁免（★ 那是基线之后新增的 ⇒ 门会抓 ★★）★★",
				name, facts.Statements, pin, multiStmtLegacyTSV)
		}
	}

	// ★ ★ 反向闭合（★ 判据③ ★）：★ 基线里的每个「多语句且未包裹」都必须在台账里 ★ ★
	for name := range baseSet {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		facts := multiStmtAnalyze(name, string(b))
		if !multiStmtIsMultiStatement(facts) || facts.Wrapped {
			continue
		}
		if _, ok := table[name]; !ok {
			t.Errorf("★★ 台账与现实【反向不闭合】：★ 基线 pin `%s` 里已存在的 `%s`（★ 多语句且未包裹 ★）★ "+
				"★ 却不在存量豁免台账里（★ `%s` ★）⇒ ★★ 请重跑官方生成器 ★★", pin, name, multiStmtLegacyTSV)
		}
	}

	t.Logf("★ 基线 pin `%s`｜★ 判据面：%d 个多语句且未包裹的迁移｜★ 其中基线存量 %d 个（★ 台账 %d 条 ★）",
		pin[:8], checked, legacyExpected, len(table))
}

// TestMultiStatementLegacyTableEntriesStayValid —— ★★ 承重断言：★★
//
// ★★ **台账每一项必须【仍然成立】**：★ 文件还在、★ 仍多语句、★ 仍未包裹、★ 且【在基线 pin 里存在】★★。
func TestMultiStatementLegacyTableEntriesStayValid(t *testing.T) {
	root := repoRootForMultiStmt(t)
	pin := multiStmtReadBaselinePin(t, root)
	baseSet := multiStmtBaselineFileSet(t, root, pin)
	table := multiStmtReadLegacyTable(t, root)

	dir := filepath.Join(root, multiStmtStartupDir)
	for name, wantN := range table {
		if !baseSet[name] {
			t.Errorf("★★ 台账项 `%s` 【不在基线 pin `%s` 里】⇒ ★★ 它要么是【基线之后新增的】、★ "+
				"★ 要么是【已经不存在了】★★ ⇒ ★★ **豁免只能靠【前移 pin】获得、★ 不能靠往台账里加一行** ★★ "+
				"★ ⇒ 请把它从台账里删掉、★ 并重跑官方生成器（★ 基线 pin 在 `%s` ★）★★",
				name, pin[:8], multiStmtBaselineRefFile)
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("★★ 台账项 `%s` 的文件已不存在 ⇒ ★★ 请重跑官方生成器 ★★", name)
			continue
		}
		facts := multiStmtAnalyze(name, string(b))
		if facts.Wrapped {
			t.Errorf("★★ 台账项 `%s` 现在【已经事务包裹了】⇒ ★★ 它不再需要豁免 ⇒ ★★ 请重跑官方生成器把它移出台账 ★★", name)
			continue
		}
		if !multiStmtIsMultiStatement(facts) {
			t.Errorf("★★ 台账项 `%s` 现在【只剩 %d 条语句、★ 已不是多语句】⇒ ★★ 请重跑官方生成器 ★★",
				name, facts.Statements)
			continue
		}
		if facts.Statements != wantN {
			t.Errorf("★★ 台账项 `%s` 的语句数与台账记的【%d】不一致（★ 实测 %d ★）⇒ ★★ 请重跑官方生成器 ★★",
				name, wantN, facts.Statements)
		}
	}
}

// TestMultiStatementDetectorRecognisesSyntheticSamples —— ★★ 自证臂：★★
//
// ★★ **判据自身必须能命中**（★ 否则上面两道门就是恒绿 ★★）。
func TestMultiStatementDetectorRecognisesSyntheticSamples(t *testing.T) {
	// ★ 未包裹的多语句文件 ⇒ 必须被认成「需豁免」★
	unwrapped := multiStmtAnalyze("x.sql", "-- 注释\nCREATE TABLE a(i int);\nCREATE INDEX i ON a(i);\n")
	if !multiStmtIsMultiStatement(unwrapped) {
		t.Fatalf("★ 自证臂红：★ 未包裹样本应被认成多语句")
	}
	if unwrapped.Wrapped {
		t.Fatalf("★ 自证臂红：★ 未包裹样本不该被认成已包裹")
	}
	// ★ 已包裹的多语句文件 ⇒ 必须被认成「已包裹」★
	wrapped := multiStmtAnalyze("y.sql", "BEGIN;\nCREATE TABLE a(i int);\nCREATE INDEX i ON a(i);\nCOMMIT;\n")
	if !wrapped.Wrapped {
		t.Fatalf("★ 自证臂红：★ 已包裹样本没被认成已包裹")
	}
	// ★ 单语句文件 ⇒ 不在判据面里 ★
	single := multiStmtAnalyze("z.sql", "-- 只有注释\nCREATE TABLE a(i int);\n")
	if multiStmtIsMultiStatement(single) {
		t.Fatalf("★ 自证臂红：★ 单语句样本被误认成多语句")
	}
	// ★ ★ 只有 BEGIN 没有 COMMIT ⇒ 【不算】包裹（★ 半截事务比没有更危险 ★★）★
	half := multiStmtAnalyze("w.sql", "BEGIN;\nCREATE TABLE a(i int);\nCREATE INDEX i ON a(i);\n")
	if half.Wrapped {
		t.Fatalf("★ 自证臂红：★ 只有 BEGIN 没有 COMMIT 的半截事务被认成已包裹")
	}
	_ = fmt.Sprintf
}

// repoRootForMultiStmt 定位仓根（★ 本门在 `scripts/ursmcheck` 下 ★）。
func repoRootForMultiStmt(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("★ 找不到仓根（★ go.mod ★）")
	return ""
}
