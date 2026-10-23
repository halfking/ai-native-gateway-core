// selftest.go —— 工具自检。
//
// ★★★ 为什么工具自己也要有自检：
//
//	一个「扫全仓」的量具如果搜索面被悄悄截断（`head -n`、glob 写错、目录漏了），
//	它的输出会**看起来很正常**——只是少了几条 ⇒ ⇒ 假绿。
//	⇒ ⇒ 工具必须自证：① 两侧非空 ② 计数与独立口径一致 ③ 没有截断。
package relrefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunSelfTest 跑全部自检（★ 供 cmd/tools 的 --self-test 与门共用）。
func RunSelfTest(root string) error {
	// ── ① 搜索面完整性：两侧非空 ──
	sqlFiles, err := RepoFiles(root, "*.sql")
	if err != nil {
		return fmt.Errorf("① 取 .sql 清单失败：%w", err)
	}
	if len(sqlFiles) == 0 {
		return fmt.Errorf("① 一个 .sql 文件都没扫到 ⇒ 搜索面不完整 ⇒ 量具不可信")
	}
	goFiles, err := RepoFiles(root, "*.go")
	if err != nil {
		return fmt.Errorf("① 取 .go 清单失败：%w", err)
	}
	if len(goFiles) == 0 {
		return fmt.Errorf("① 一个 .go 文件都没扫到 ⇒ 搜索面不完整 ⇒ 量具不可信")
	}

	// ── ② 无截断自检：★ 独立口径数一遍文件数（★ 不复用 repoFiles 的结果）──
	// ★ 判据 = 独立 `git ls-files` 计数 与 工具实际扫到的计数必须一致。
	if err := checkNoTruncation(root); err != nil {
		return err
	}

	// ── ③ 解析器必须有牙：反向夹具 ──
	if err := checkParserHasTeeth(); err != nil {
		return err
	}

	// ── ④ 动态拼名前缀探测：★「按名零命中 ⇒ 不依赖」的前提守卫 ──
	if err := checkPrefixGuard(); err != nil {
		return err
	}

	// ── ⑤ 剥离注释不得吃掉普通字符串 ──
	if err := checkStripComments(); err != nil {
		return err
	}

	// ── ⑥ 幂等：跑两遍结果必须一致 ──
	if err := checkIdempotent(root); err != nil {
		return err
	}

	return nil
}

// checkNoTruncation = 无截断自检。
//
// ★ ★★ 判据必须用【独立口径】：
//
//	「`git ls-files '*.sql' | wc -l`」与「`git ls-files '*.sql' | head -n 50 | wc -l`」
//	都是同一个命令 ⇒ ⇒ 同源 ⇒ 恒等 ⇒ 恒真。
//	⇒ ★ 这里用【文件系统侧独立枚举】作对照：walk 目录数出 .sql/.go 文件数，
//	  与 git 清单数比对 ⇒ 两个口径的【失效方式不同】。
func checkNoTruncation(root string) error {
	gitSQL, err := RepoFiles(root, "*.sql")
	if err != nil {
		return err
	}
	gitGO, err := RepoFiles(root, "*.go")
	if err != nil {
		return err
	}

	// ★ 文件系统侧独立枚举（★ 刻意【不】用 git ⇒ 两个口径不同源）
	//
	// ★★★ ⚠ 关键：★ **两侧的排除规则必须一致**，否则会误判「一方被截断」。
	//   本例第一版在 Walk 里跳过了 `vendor/`（★ 因为 vendor 有 4994 个 .go），
	//   而 `git ls-files '*.go'` **包含** vendor ⇒ ⇒ 两侧边界不对齐
	//   ⇒ ⇒ 报出「文件系统枚举 5774 < git 清单 10764 ⇒ 清单被截断」
	//   ⇒ ⇒ ★★ **这是假红**：真因是【口径边界不对齐】，不是截断。
	//   ⇒ ⇒ ★★ 通用式：「双口径互证」⇒ **先把两边的排除规则对齐**，否则互证本身会误报。
	//   ⇒ ⇒ 现版：**两侧都不排除 vendor** ⇒ 边界一致 ⇒ 差值只可能来自「未跟踪文件」。
	var fsSQL, fsGO int
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".sql":
			fsSQL++
		case ".go":
			fsGO++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("② 文件系统枚举失败：%w", err)
	}

	// ★ 判定：文件系统枚举的数目必须【不少于】git 清单的数目。
	//   （多于 ⇒ 有未跟踪文件，那不是截断；少于 ⇒ ★ 清单被截断了！）
	if fsSQL < len(gitSQL) {
		return fmt.Errorf("② 无截断自检失败：文件系统枚举到 %d 个 .sql，git 清单只有 %d 个 ⇒ 清单被截断",
			fsSQL, len(gitSQL))
	}
	if fsGO < len(gitGO) {
		return fmt.Errorf("② 无截断自检失败：文件系统枚举到 %d 个 .go，git 清单只有 %d 个 ⇒ 清单被截断",
			fsGO, len(gitGO))
	}

	// ★★★ ★★★ 「数目相等」不等于「清单可用」—— ★ 这是本轮真踩到的第二个坑。
	//
	// ★ `git ls-files` 默认 `core.quotePath=true` ⇒ **非 ASCII 路径被转义成
	//   `\345\205\250…` 形态** ⇒ ⇒ 清单【数目完全正常】（转义后仍是一行）
	//   ⇒ ⇒ 但【每一个中文路径都打不开】⇒ ⇒ 那批文件里的 DDL **静默缺席**
	//   ⇒ ⇒ 「悬空引用」被高估 ⇒ ⇒ **而量具一声不吭**。
	// ⇒ ⇒ 判据必须是【逐个可打开】，不能只是【数目相等】。
	for _, rel := range gitSQL {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			return fmt.Errorf("② 清单可用性自检失败：清单里的 %q 打不开 ⇒ ★ 路径被转义或文件缺失 ⇒ "+
				"★ ⇒ 那批文件里的 DDL 会【静默缺席】⇒ 悬空引用被高估", rel)
		}
	}
	for _, rel := range gitGO {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			return fmt.Errorf("② 清单可用性自检失败：清单里的 %q 打不开 ⇒ ★ 路径被转义或文件缺失",
				rel)
		}
	}
	return nil
}

// checkParserHasTeeth = 反向夹具：★ 解析器必须报出【故意造出来的】东西。
func checkParserHasTeeth() error {
	// ★ ① 关系位置必须抓到表名
	//   ★★ 关系名一律用【中性夹具名】（同本文件 testDDL 里的 real_table /
	//     keepme / some_fixture_table 同一约定）⇒ ★ 理由：自检夹具里如果写
	//     真实族名（曾写 public.session_turns / usage_ledger），本文件就会
	//     被 admin 侧三条「读法门」当成**生产读方**⇒ 报假红
	//     ⇒ ★★ 而「夹具用真名」换不到任何额外覆盖：这些断言测的是
	//     【解析器机制】（关系位置 / EXTRACT / CTE / 函数名 / 前缀门槛），
	//     与具体族名无关 ⇒ ⇒ 与 commit e91ff07e5（v1 族名换中性名）同因同解。
	sql := "SELECT a.id FROM public.fixture_parent a JOIN fixture_child u ON u.id = a.id"
	got := map[string]bool{}
	for _, p := range extractRelationPositions(sql, testDDL(), testCTEs()) {
		got[p.Name] = true
	}
	for _, want := range []string{"public.fixture_parent", "fixture_child"} {
		if !got[want] {
			return fmt.Errorf("③ 反向夹具失败：关系位置没抓到 %q（抓到 %v）⇒ 解析器没牙", want, got)
		}
	}

	// ★ ② 函数用法 `EXTRACT(x FROM y)` 里的 FROM **不是**关系位置
	fnSQL := "SELECT EXTRACT(day FROM ts) FROM fixture_parent"
	got2 := map[string]bool{}
	for _, p := range extractRelationPositions(fnSQL, testDDL(), testCTEs()) {
		got2[p.Name] = true
	}
	if got2["ts"] {
		return fmt.Errorf("③ 反向夹具失败：把 EXTRACT(day FROM ts) 里的 ts 当成了关系名 ⇒ 会有假阳性")
	}
	if !got2["fixture_parent"] {
		return fmt.Errorf("③ 反向夹具失败：EXTRACT 之后的真关系 fixture_parent 没被抓到 ⇒ 括号深度过滤过头了")
	}

	// ★ ③ SQL 上下文判据：中文里嵌 SQL 片段也算
	if !sqlContextRE.MatchString("中文里嵌一句 SELECT id FROM users 也会被认出来") {
		return fmt.Errorf("③ 反向夹具失败：SQL 上下文判据对全角相邻不可靠")
	}
	// ★ ④ 反过来：非 SQL 文本不该被认成 SQL 上下文
	if sqlContextRE.MatchString("just a plain english sentence about updates") {
		return fmt.Errorf("③ 反向夹具失败：普通英文被误判成 SQL 上下文 ⇒ 弱命中桶会被污染")
	}
	// ★★★★★ ⑤ 第一版假阳性的最大来源：英文错误消息里的 update/from
	//   ⇒ ★★ 「含一个 SQL 关键字」不构成 SQL 上下文 ⇒ 必须要求【成对结构】。
	for _, phrase := range []string{
		"update action log",
		"update annotation: %w",
		"no models found from any candidate URL",
		"failed to update approver",
		"copy backup: %w",
	} {
		if sqlContextRE.MatchString(phrase) {
			return fmt.Errorf("③ 反向夹具失败：英文短语 %q 被误判成 SQL 上下文 ⇒ "+
				"★ ⇒ 第一版那种「309 个悬空引用」的假阳性会回来", phrase)
		}
	}

	// ★ ⑥ CTE 名不是关系
	cteSQL := "WITH base AS (SELECT 1) SELECT * FROM base JOIN real_table t ON true"
	gotCTE := map[string]bool{}
	for _, p := range extractRelationPositions(cteSQL, testDDL(), testCTEs()) {
		gotCTE[p.Name] = true
	}
	if gotCTE["base"] {
		return fmt.Errorf("③ 反向夹具失败：CTE 名 `base` 被当成了关系名 ⇒ 假阳性")
	}
	if !gotCTE["real_table"] {
		return fmt.Errorf("③ 反向夹具失败：CTE 排除过头了，真关系 real_table 没抓到")
	}

	// ★ ⑦ 函数名不是关系
	fnSQL2 := "SELECT * FROM generate_series(1, 10) g JOIN keepme k ON true"
	gotFn := map[string]bool{}
	for _, p := range extractRelationPositions(fnSQL2, testDDL(), testCTEs()) {
		gotFn[p.Name] = true
	}
	if gotFn["generate_series"] {
		return fmt.Errorf("③ 反向夹具失败：函数名 `generate_series` 被当成了关系名 ⇒ 假阳性")
	}
	if !gotFn["keepme"] {
		return fmt.Errorf("③ 反向夹具失败：函数排除过头了，真关系 keepme 没抓到")
	}
	return nil
}

// checkPrefixGuard = 动态拼名前缀探测（★「按名零命中 ⇒ 不依赖」的前提守卫）。
func checkPrefixGuard() error {
	ddl := newDDLRelations()
	ddl.add("table", "prefix_guard_tbl")
	ddl.add("table", "ab") // ★ 太短的名字（< 4 字符）⇒ 不参与嫌疑探测

	ps := &productScan{
		strongNames:    map[string]bool{},
		weakNames:      map[string]bool{},
		prefixSuspects: map[string]bool{},
	}
	// ★ 夹具：拼接点 `"prefix_guard_tbl" + suffix` ⇒ 字面量是 `prefix_guard_tbl_2026`
	detectPrefixSuspects(ps, ddl, "prefix_guard_tbl_2026")

	if !ps.prefixSuspects["prefix_guard_tbl"] {
		return fmt.Errorf("④ 拼接守卫失败：`prefix_guard_tbl_2026` 没被认成 `prefix_guard_tbl` 的拼接嫌疑 ⇒ " +
			"★ ⇒ 「按名零命中 ⇒ 不依赖」这条推论会被推翻而没人知道")
	}
	// ★ 反向：太短的名字不应产生嫌疑（噪音会淹没信号）
	//   ★ 注意：★ 不能用「是另一个名字的前缀」来测这条 —— 那种情况**本来就该**判嫌疑
	//     （前缀关系是真的）⇒ 用「短到被长度门槛挡掉」来测。
	if ps.prefixSuspects["ab"] {
		return fmt.Errorf("④ 拼接守卫失败：`ab` 产生了嫌疑 ⇒ 噪音太多")
	}
	return nil
}

// checkStripComments = 剥注释不得吃掉普通字符串。
func checkStripComments() error {
	in := "-- 注释里的 SELECT FROM x\nSELECT 'a -- b' FROM y /* 块注释 SELECT FROM z */"
	out := stripSQLComments(in)
	if strings.Contains(out, "块注释") {
		return fmt.Errorf("⑤ 剥注释失败：块注释没被剥掉")
	}
	if !strings.Contains(out, "'a -- b'") {
		return fmt.Errorf("⑤ 剥注释失败：字符串字面量里的 `--` 被当成注释 ⇒ 正则会把内容吃空")
	}
	if !strings.Contains(out, "FROM y") {
		return fmt.Errorf("⑤ 剥注释失败：正常 SQL 被误删")
	}
	return nil
}

// checkIdempotent = 幂等：两遍提取必须给出同一组关系名。
//
// ★★ 复用同一批已收集的字面量（★ 不重新解析 2000+ 个文件）⇒ ⇒ 门的时间从 56s 降到 ~20s。
// ★ ⇒ ⇒ 幂等检查的**范围**没变（仍是全仓字面量），只是**采集**只做一次。
func checkIdempotent(root string) error {
	sqlFiles, err := RepoFiles(root, "*.sql")
	if err != nil {
		return err
	}
	goFiles, err := RepoFiles(root, "*.go")
	if err != nil {
		return err
	}
	ps := &productScan{
		strongNames:    map[string]bool{},
		weakNames:      map[string]bool{},
		prefixSuspects: map[string]bool{},
	}
	lits, err := collectProductLiterals(root, goFiles, ps)
	if err != nil {
		return err
	}
	ddl1 := newDDLRelations()
	for _, rel := range sqlFiles {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		scanDDLFile(ddl1, string(b))
	}
	buildDDLFromLiterals(ddl1, lits)
	ctes := globalCTEFromLiterals(lits)

	names1 := extractStrongNames(lits, ddl1, ctes)
	// ★ 第二遍：同样的输入 ⇒ 同样的输出（★ 中间不复用任何可变状态）
	ddl2 := newDDLRelations()
	for _, rel := range sqlFiles {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		scanDDLFile(ddl2, string(b))
	}
	buildDDLFromLiterals(ddl2, lits)
	ctes2 := globalCTEFromLiterals(lits)
	names2 := extractStrongNames(lits, ddl2, ctes2)

	if len(names1) != len(names2) {
		return fmt.Errorf("⑥ 幂等自检失败：两遍的强命中名字数不同（%d vs %d）", len(names1), len(names2))
	}
	a := sortedKeysOf(names1)
	b := sortedKeysOf(names2)
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("⑥ 幂等自检失败：第 %d 个名字不同（%q vs %q）", i, a[i], b[i])
		}
	}
	if len(a) == 0 {
		return fmt.Errorf("⑥ 幂等自检失败：强命中名字集合为空 ⇒ 量具瞎了")
	}
	return nil
}

// sortedKeysOf 把 map 的键排好序取出。
func sortedKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// testDDL = 自检用的最小 schema 事实源。
//
// ★ 只含 `public` ⇒ 因为绝大多数真表都是 `public.xxx` 形态，
// ★ 而 `c.credential_id` 这类别名形态的第一段不是 public ⇒ ⇒ 被排除。
func testDDL() *ddlRelations {
	d := newDDLRelations()
	d.schemas["public"] = true
	d.add("table", "public.fixture_parent")
	d.add("table", "public.fixture_child")
	d.add("table", "public.credentials")
	d.add("table", "public.providers")
	d.add("table", "public.real_table")
	d.add("table", "public.keepme")
	d.add("table", "public.some_fixture_table")
	return d
}

// testCTEs = 自检用的全局 CTE 名集合。
//
// ★ 夹具里 `base` 是 CTE ⇒ 必须被排除 ⇒ ⇒ 所以要放进全局集合。
func testCTEs() globalCTENames {
	return globalCTENames{"base": true}
}
