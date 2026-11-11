package startup

// migration_957_error_type_mirror_faces_test.go ——
//
// 「self_check_runs.error_type 的 CHECK 闭集」在**仓内每一个声明面**上的镜像门。
//
// ── 这道门补的是哪个洞（2026-11-11 12h 审计 C-1 / C-2）────────────────────
//
// 929/957 那道门（migration_929_error_type_closure_test.go）只扫**两条腿**：
//
//	① 安装顺序里**最后一个**重建该 CHECK 的 startup 迁移（= 957）＋ 它的 embeddata 副本；
//	② 正典目录 `sql/objects/tables/self_check_runs.sql`。
//
// ⇒ 而仓里**还有 4 个面**在声明同一张约束（本轮实测确认）:
//
//	· `sql/schema/01-schema.sql`（基线 SSOT）
//	· `installer/cmd/llm-gw-installer/embeddata/01-schema.sql`（installer //go:embed 基线）
//	· `deploy/sql/schemas/baseline/01-schema.sql`（**随一键包分发**的基线）
//	· `deploy/sql/migrations/V361__self_check_runs_canonical_taxonomy.sql`（**会重建**该约束）
//
// ⇒ 这 4 个面当时都是 **31 项**（缺 8 个 kind）⇒ ⇒ **门绿而镜像面仍缺**。
//
// ★ 失败场景（**本轮已取证，触发条件成立**）：
//
//	· `deploy/one-click/lib/bootstrap.sh:71-76` 的 `sync_sql_baseline()` 把
//	  `deploy/sql/schemas/baseline/*.sql` 打进**生产一键包**；
//	· `deploy/one-click/DEPLOYMENT-GUIDE.md:138` 明确让用户手跑
//	  `psql … -f sql/baseline/01-schema.sql`（**只跑基线、不跑 startup 链**）。
//
//	⇒ 只跑基线的通道 ⇒ 约束停在 31 项 ⇒ 上游 4xx 落 `rate_limit_observed`
//	⇒ **23514** ⇒ `finalizeRun` 的 UPDATE 回滚 ⇒ run 行停在
//	  `status='running'` / `error_type=NULL` 的**半写态**（与 957 修的故障同形）。
//
// ── 为什么这道门要**按面**判，而不是「补齐清单」 ─────────────────────────
//
// 教训（LESSONS §684 / §685 与本轮第 35 处审计）：
//
//	① **手写的清单**与**代码的实际输出**必然漂移 ⇒ 必须有机器核对两份；
//	② **只同步被点名的几个副本 = 假绿** ⇒ **覆盖面本身要被审计**。
//
// ⇒ 所以本门分三层：
//
//	① **结构化发现**：遍历**全仓** `.sql`，找出**每一个**声明该约束的面
//	  （**不**靠人手写路径清单去找面 ⇒ 新增副本逃不掉）；
//	② **双向对账**：发现的面集合 ⇄ 登记表（`testdata/error_type_closure_faces.txt`）
//	  —— 登记了却不存在 = 红；存在却没登记 = 红；
//	③ **逐系列判终态**：每个「系列」（= 目录 = 一条安装链）按安装顺序走一遍，
//	  判它的**终态**闭集是否 ⊇ 必需闭集（errorsx 闭集 ∪ worker 专用字面量）。
//
// ── 「终态」为什么是唯一该判的口径 ───────────────────────────────────────
//
// 一条安装链里**早先**的迁移声明窄集是**合法的**：它随后会被更宽的迁移覆盖。
// ★ 所以判据不能是「每个文件都得是全集」（那会把历史迁移也判红），
// ★ 也不能是「只看最后一个」（那正是 929 门的漏洞：它只看**一条链**的最后一条）。
//
// ⇒正确口径 = **每条链的终态**。而「链」在仓里的对应物是**目录**：
//
//	· `sql/migrations/startup/`            —— installer 启动链（终态 = 957 的 39 项）
//	· `installer/…/embeddata/startup/`     —— 同一链的 embeddata 副本（39 项）
//	· `sql/schema/`                          —— 基线 SSOT（01-schema.sql）
//	· `installer/…/embeddata/`               —— installer 内嵌基线（01-schema.sql）
//	· `deploy/sql/schemas/baseline/`         —— **随包分发**的基线（01-schema.sql）
//	· `deploy/sql/migrations/` —— deploy 迁移系列（V361 会重建约束）
//	· `sql/objects/tables/`                  —— 正典对象（self_check_runs.sql）
//	· `sql/migrations/domain/`               —— domain 链（339 把约束**删掉**⇒ 终态「无约束」
//	                                        ⇒ 允许（deferred）⇒ 约束由 startup 链提供）
//
// ── 量具纪律（★ 这道门自己也可能是坏的）────────────────────────────────
//
//	① **注释不许参与判定**：面文件里的散文会写 `'rate_limit_observed'`
//	  这类字面量 ⇒ 若不涂白 ⇒ 判定器会把**散文**当成**声明** ⇒ 假绿。
//	  ⇒ 复用同包 `stripSQLComments`（本轮实测：面文件里 `/* */` 计数 = 0）。
//	② **提取要锚定约束名 + 括号配平**：不能「取文件里第一个 ARRAY」
//	  （★ 本轮实测踩过：基线文件里第一个 ARRAY 属于同表的 status 检查约束
//	  ⇒ 每次都恰好抽出一组相同的 9 个垃圾值 ⇒ 「缺失/多余」结论全错）。
//	③ **两种声明形态都要认**：
//	   `= ANY (ARRAY[...])`（基线 /迁移）与 `IN (...)`（domain 338 / 339 的历史写法）。
//	④ **判红要点名**：变异自检断言「转红」且「输出点名被改的那个面 + 被拆的字面量」，
//	  而不是断言「进程非零」。
//	⑤ **登记表读不到 ⇒ fail-closed**（不可判 ≠ 没发现问题）。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// mirrorRepoRoot 是仓根（本文件在 `sql/migrations/startup/`）。
	mirrorRepoRoot = "../../.."

	// mirrorFacesFixture 是**面的登记表**（路径清单）。
	//
	// ★ 它与被守卫的内容**不同源**：登记表只写「哪些文件声明该约束」，
	// ★ 而「声明了什么」在 SQL 文件里 ⇒ ⇒ 同源变异（改 SQL 而不改登记表）
	// ★ 打不中这道门 ⇒ 这正是 LESSONS §680 的要求。
	//
	// ★ 双���对账（发现 ⇄ 登记）⇒ 登记表既不能漏（漏 ⇒ 新副本逃掉），
	// ★ 也不能多（多 ⇒ 文件被删了却没人知道）。
	mirrorFacesFixture = "testdata/error_type_closure_faces.txt"
)

// mirrorFace 是**一个声明面**（= 一个声明了该 CHECK 约束的文件）。
type mirrorFace struct {
	// Path 是相对仓根的路径（斜杠分隔）。
	Path string
	// Series 是所在目录（相对仓根）= 它所属的安装链。
	Series string
	// Order 是文件名里的数字前缀（无数字 = 0）⇒ 链内安装顺序。
	Order int
	// IsDown 表示这是 `.down.sql`（回滚脚本）。
	IsDown bool
	// Defines 表示该文件**声明**（CREATE / ADD）了该约束。
	Defines bool
	// Drops 表示该文件**删除**了该约束（且没有重新声明）。
	Drops bool
	// Literals 是它声明的字面量集（Defines 时有效）。
	Literals map[string]bool
}

// mirrorOrderOf 取文件名的数字前缀（`V361__x.sql` → 361；`01-schema.sql` → 1；
// 无数字 → 0）。
func mirrorOrderOf(name string) int {
	s := strings.TrimPrefix(name, "V")
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// mirrorConstraintRegion 返回 `CONSTRAINT <name> CHECK` 之后的**配对**括号区间。
//
// ★ 锚定约束名是必需的：同一个 CREATE TABLE 里还有别的 CHECK（status /
// ★ selection_strategy），取「第一个 CHECK」会抽到别人的字面量集。
func mirrorConstraintRegion(clean, name string) (int, int, bool) {
	anchor := indexOfFold(clean, "CONSTRAINT "+name+" CHECK")
	if anchor < 0 {
		return 0, 0, false
	}
	rel := strings.Index(clean[anchor:], "(")
	if rel < 0 {
		return 0, 0, false
	}
	open := anchor + rel
	depth := 0
	for i := open; i < len(clean); i++ {
		c := clean[i]
		if c == '\'' {
			// 跳过字符串字面量（`''` 是转义的单引号，不是结束符）。
			j := i + 1
			for j < len(clean) {
				if clean[j] == '\'' {
					if j+1 < len(clean) && clean[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return open, i + 1, true
			}
		}
	}
	return 0, 0, false
}

// mirrorLiteralsInRegion 取 [start,end) 区间内的全部单引号字面量。
func mirrorLiteralsInRegion(text string, start, end int) map[string]bool {
	out := map[string]bool{}
	for i := start; i < end && i < len(text); i++ {
		if text[i] != '\'' {
			continue
		}
		j := i + 1
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
			break
		}
		out[text[i+1:j]] = true
		i = j
	}
	return out
}

// mirrorValueLiterals 取 CHECK 表达式里的**值集合**。
//
// ★ 两种声明形态：
//
//	① `= ANY (ARRAY[...])` —— 基线 / 929/930/957 / V361 / 正典对象
//	② `IN (...)`          —— domain 338 / 339 的历史写法（没有 ARRAY）
//
// ★ 优先取 ARRAY：同一份 CHECK 表达式里 `LIKE 'http_%'` 也是个字面量，
// ★ 但它是**模式**不是值 ⇒ 取 ARRAY 正好把它排除掉。
func mirrorValueLiterals(expr string) (map[string]bool, bool) {
	if _, _, lits, ok := scanSQLArrayRegion(expr, 0); ok {
		return lits, true
	}
	rel := indexOfFold(expr, " IN (")
	if rel < 0 {
		return nil, false
	}
	open := rel + len(" IN (")
	depth := 1
	for i := open; i < len(expr); i++ {
		switch expr[i] {
		case '\'':
			j := i + 1
			for j < len(expr) {
				if expr[j] == '\'' {
					if j+1 < len(expr) && expr[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return mirrorLiteralsInRegion(expr, open, i), true
			}
		}
	}
	return nil, false
}

// mirrorDiscoverFaces 遍历 root 下**所有** `.sql`，找出每一个提到该约束的面。
//
// ★ 遍历的是**工作树**（不是 git 索引）⇒ 未提交的手改当场被抓。
// ★ 跳过以 `.` 开头的目录（`.git` / `.audit*/` 之类沙箱）与 `node_modules` / `vendor`。
func mirrorDiscoverFaces(t *testing.T, root string) []mirrorFace {
	t.Helper()
	var faces []mirrorFace
	upperName := strings.ToUpper(errorTypeConstraintName)

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if name == "node_modules" || name == "vendor" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".sql") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		clean := stripSQLComments(string(raw))
		if !strings.Contains(strings.ToUpper(clean), upperName) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		f := mirrorFace{
			Path:   filepath.ToSlash(rel),
			Series: filepath.ToSlash(filepath.Dir(rel)),
			Order:  mirrorOrderOf(d.Name()),
			IsDown: strings.HasSuffix(d.Name(), ".down.sql"),
		}
		if s, e, ok := mirrorConstraintRegion(clean, errorTypeConstraintName); ok {
			lits, ok := mirrorValueLiterals(clean[s:e])
			if !ok {
				t.Fatalf("%s：约束表达式里既没有 ARRAY[...] 也没有 IN (...) ⇒ 提取器失效"+
					"（不是「约束消失了」）", f.Path)
			}
			f.Defines = true
			f.Literals = lits
		}
		if strings.Contains(strings.ToUpper(clean), "DROP CONSTRAINT IF EXISTS "+upperName) {
			f.Drops = true
		}
		faces = append(faces, f)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	if len(faces) == 0 {
		t.Fatalf("%s 下没发现任何声明面 ⇒ 门会空转全绿（提取器/遍历面坏了）", root)
	}
	sort.Slice(faces, func(i, j int) bool {
		if faces[i].Series != faces[j].Series {
			return faces[i].Series < faces[j].Series
		}
		if faces[i].Order != faces[j].Order {
			return faces[i].Order < faces[j].Order
		}
		if faces[i].IsDown != faces[j].IsDown {
			return !faces[i].IsDown // up 在 down 之前
		}
		return faces[i].Path < faces[j].Path
	})
	return faces
}

// mirrorIsSubset 判 a ⊆ b。
func mirrorIsSubset(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// mirrorMissing 返回 required 里 have 没有的项（已排序）。
func mirrorMissing(have map[string]bool, required []string) []string {
	var out []string
	for _, r := range required {
		if !have[r] {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// mirrorSeriesState 是「一条链跑完之后的终态」。
type mirrorSeriesState struct {
	// Closure 非 nil ⇒链终态存在该约束，其字面量集为 Closure。
	// Closure == nil ⇒ 链终态**没有**该约束（被删掉了 ⇒ deferred）。
	Closure map[string]bool
	// Deferred 为 true ⇒ 终态无该约束（允许：由别的链提供）。
	Deferred bool
}

// mirrorRunSeries 走一遍一条链（目录），返回终态与违规描述。
//
// ★ 三条判据：
//
//	① **杂物**：任何声明面的字面量都必须 ⊆ 必需闭集 ∪ {http_*}（历史诊断值）。
//	② **up 链单调**：后一条 up 迁移的字面量集必须 ⊇ 前一条 ⇒ 不许收窄。
//	③ **终态闭集**：链终态的闭集必须 ⊇ 必需闭集（终态无约束 ⇒ deferred，允许）。
//
// ★ down 链另判（mirrorCheckDownChain）：`X.down.sql` 必须**精确**恢复
// ★ 「X 的 up 之前那一条 up 迁移」留下的闭集 ⇒ 回滚只退一步。
func mirrorRunSeries(files []mirrorFace, required []string) (mirrorSeriesState, []string) {
	var violations []string
	allowed := map[string]bool{}
	for _, r := range required {
		allowed[r] = true
	}

	// ① 杂物字面量。
	for _, f := range files {
		if !f.Defines {
			continue
		}
		var junk []string
		for lit := range f.Literals {
			if allowed[lit] || strings.HasPrefix(lit, "http_") {
				continue
			}
			junk = append(junk, lit)
		}
		if len(junk) > 0 {
			sort.Strings(junk)
			violations = append(violations, fmt.Sprintf(
				"%s：声明了必需闭集之外的字面量 %v —— 要么是拼写错误，要么是手抄清单漂移",
				f.Path, junk))
		}
	}

	// ② + ③ 走 up 链（回滚脚本不参与终态推进）。
	var (
		prevUpFace *mirrorFace
		// lastStateFace 是**决定终态的那个文件**（最后一个 up 声明者；
		// 若终态被某个 DROP 清空，则指向那个 DROP 文件）。
		lastStateFace *mirrorFace
		state         map[string]bool
	)
	for i := range files {
		f := files[i]
		if f.IsDown {
			continue
		}
		switch {
		case f.Defines:
			if prevUpFace != nil && !mirrorIsSubset(prevUpFace.Literals, f.Literals) {
				violations = append(violations, fmt.Sprintf(
					"%s：up 链在此**收窄**（上一条 %s 声明的字面量不是本条的子集）⇒ "+
						"先跑的迁移会被后来的覆盖成更窄的约束 ⇒ 写入被 23514 拒",
					f.Path, prevUpFace.Path))
			}
			prev := f
			prevUpFace = &prev
			state = f.Literals
			last := f
			lastStateFace = &last
		case f.Drops:
			state = nil
			last := f
			lastStateFace = &last
		}
	}

	st := mirrorSeriesState{Closure: state}
	if state == nil {
		st.Deferred = true
	} else if miss := mirrorMissing(state, required); len(miss) > 0 {
		// ★ 必须点名**决定终态的那个文件**（★ 只点名系列 ⇒ 读数指向不明）。
		where := files[0].Series
		if lastStateFace != nil {
			where = fmt.Sprintf("%s（终态由 %s 决定）", files[0].Series, lastStateFace.Path)
		}
		violations = append(violations, fmt.Sprintf(
			"系列 %s：终态闭集缺 %v ⇒ 该通道建出来的库会用**更窄**的约束拒写新 kind "+
				"（23514 ⇒ run 行半写态）",
			where, miss))
	}
	return st, violations
}

// mirrorCheckDownChain 判回滚链：`X.down.sql` 必须精确恢复「X 的 up 之前那条
// up 迁移」留下的闭集。
//
// ★ 为什么单列一条判据：up 链判的是**终态**（宽），而回滚判的是**中间态**
// ★ （必须能被 up 链里的某一步精确复现）⇒ 两者不是同一件事。
// ★ 实测（改前）：929.down(31) ≡ 644.up(31)、930.down(37) ≡ 929.up(37)、
// ★ 957.down(38) ≡ 930.up(38) ⇒ 「只退一步」成立。
func mirrorCheckDownChain(files []mirrorFace) []string {
	var violations []string
	// up 链上的声明者（按顺序）。
	var upDefiners []mirrorFace
	for _, f := range files {
		if !f.IsDown && f.Defines {
			upDefiners = append(upDefiners, f)
		}
	}
	for _, f := range files {
		if !f.IsDown || !f.Defines {
			continue
		}
		var prev *mirrorFace
		for i := range upDefiners {
			if upDefiners[i].Order < f.Order {
				prev = &upDefiners[i]
			}
		}
		if prev == nil {
			violations = append(violations, fmt.Sprintf(
				"%s：回滚脚本声明了闭集，但它前面**没有任何 up 迁移**声明过 ⇒ 回滚链断了", f.Path))
			continue
		}
		if len(prev.Literals) != len(f.Literals) {
			violations = append(violations, fmt.Sprintf(
				"%s：回滚闭集（%d 项）与前一条 up 迁移 %s 的闭集（%d 项）**不是同一份** ⇒ "+
					"回滚不是「只退一步」⇒ 回滚会造出一个 up 链里不存在的约束态",
				f.Path, len(f.Literals), prev.Path, len(prev.Literals)))
			continue
		}
		for lit := range prev.Literals {
			if !f.Literals[lit] {
				violations = append(violations, fmt.Sprintf(
					"%s：回滚闭集缺 %q（前一条 up 迁移 %s 有）⇒ 回滚链与 up 链漂移",
					f.Path, lit, prev.Path))
			}
		}
	}
	return violations
}

// mirrorLoadDeclaredFaces 读登记表（面路径清单）。
//
// ★ 读不到 ⇒ fail-closed（不可判 ≠ 没发现问题）。
func mirrorLoadDeclaredFaces(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读面登记表 %s 失败: %v ——登记表不可读 ⇒ 门必须 fail-closed", path, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if len(out) == 0 {
		t.Fatalf("面登记表 %s 解析出 0 条 ⇒ 门会空转全绿", path)
	}
	return out
}

// mirrorCheckAll 是判据主体：给定一组面，返回全部违规描述。
//
// ★ 分工：
//   - 发现 ⇄ 登记 双向对账（少一个 / 多一个都红）；
//   - 每个系列跑 mirrorRunSeries（杂物 / up 链单调 / 终态闭集）；
//   - 每个系列跑 mirrorCheckDownChain（回滚链）。
func mirrorCheckAll(declared map[string]bool, faces []mirrorFace, required []string) []string {
	var violations []string

	discovered := map[string]bool{}
	for _, f := range faces {
		discovered[f.Path] = true
	}

	var missing, undeclared []string
	for p := range declared {
		if !discovered[p] {
			missing = append(missing, p)
		}
	}
	for p := range discovered {
		if !declared[p] {
			undeclared = append(undeclared, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(undeclared)
	for _, p := range missing {
		violations = append(violations, fmt.Sprintf(
			"登记表里的面 %s 不存在（或已不再声明该约束）⇒ 有人删了/改了一个声明面而没人发现", p))
	}
	for _, p := range undeclared {
		violations = append(violations, fmt.Sprintf(
			"面 %s 声明了该约束但**没登记** ⇒ 新增的镜像面逃出了登记表 ⇒ 请登记后再判", p))
	}

	// 按系列分组。
	bySeries := map[string][]mirrorFace{}
	var order []string
	for _, f := range faces {
		if _, ok := bySeries[f.Series]; !ok {
			order = append(order, f.Series)
		}
		bySeries[f.Series] = append(bySeries[f.Series], f)
	}
	sort.Strings(order)
	for _, s := range order {
		files := bySeries[s]
		_, v := mirrorRunSeries(files, required)
		violations = append(violations, v...)
		violations = append(violations, mirrorCheckDownChain(files)...)
	}
	return violations
}

// ── 主判据 ────────────────────────────────────────────────────────────────

// TestSelfCheckErrorTypeClosureCoversEveryDeclaredFace 是主判据。
//
// ★ 覆盖的是**全仓**每一个声明该约束的面 ⇒ 门绿 ⇒没有任何通道会用更窄的
// ★ 约束拒写新 kind。
func TestSelfCheckErrorTypeClosureCoversEveryDeclaredFace(t *testing.T) {
	root := filepath.Clean(mirrorRepoRoot)
	declared := mirrorLoadDeclaredFaces(t, mirrorFacesFixture)
	faces := mirrorDiscoverFaces(t, root)
	required := requiredErrorTypeLiterals()

	t.Logf("发现 %d 个面 /登记 %d 个面 / 必需闭集 %d 项",
		len(faces), len(declared), len(required))

	if v := mirrorCheckAll(declared, faces, required); len(v) > 0 {
		t.Fatalf("error_type CHECK 闭集在部分面上不合格（%d 条）：\n  · %s",
			len(v), strings.Join(v, "\n  · "))
	}
}

// ── 变异自检（★ 这道门自己也要被审计）──────────────────────────────────

// mirrorCopyFaceTree 把**发现到的全部面**按相对路径复制进临时目录。
//
// ★ 变异的落点是**真实文件内容**（不是内存里的字符串）⇒ 变异臂走的是
// ★ 与主判据**同一条**代码路径（读文件 → 发现 → 判据）。
func mirrorCopyFaceTree(t *testing.T, root string, faces []mirrorFace) string {
	t.Helper()
	dst := t.TempDir()
	for _, f := range faces {
		src := filepath.Join(root, filepath.FromSlash(f.Path))
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("复制面 %s 失败: %v", f.Path, err)
		}
		target := filepath.Join(dst, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("建目录 %s 失败: %v", target, err)
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			t.Fatalf("写 %s 失败: %v", target, err)
		}
	}
	return dst
}

// mirrorMaskSQL 把**注释**替换成等长空格（**长度不变**）⇒ 掩码文本的偏移
// 与原文**一一对应**。
//
// ★ 为什么需要它：注释里也会出现 `'rate_limit_observed'` 这类字面量
// ★ （★ 930/957 的 ARRAY 里就有 `--    'none'  （bg/…）` 这种注释）
// ★ ⇒ 变异时若在**原文**里找字面量，会先命中**注释里的那一份**
// ★ ⇒ 变异「看起来生效」实则改的是注释 ⇒ 门当然不红。
//
// ★ 而 `stripSQLComments`（本包共用）会**截短行** ⇒ 偏移对不上
// ★ ⇒ 不能拿它的偏移去改原文 ⇒ 故本函数**保长**。
func mirrorMaskSQL(text string) string {
	b := []byte(text)
	// 逐字符扫描，跳过字符串字面量（字符串里的 `--` 不是注释）。
	for i := 0; i < len(b); i++ {
		if b[i] == '\'' {
			j := i + 1
			for j < len(b) {
				if b[j] == '\'' {
					if j+1 < len(b) && b[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(b) {
				break
			}
			i = j
			continue
		}
		if b[i] == '-' && i+1 < len(b) && b[i+1] == '-' {
			j := i
			for j < len(b) && b[j] != '\n' {
				b[j] = ' '
				j++
			}
			i = j - 1
			continue
		}
		if b[i] == '/' && i+1 < len(b) && b[i+1] == '*' {
			j := i
			for j < len(b) {
				if b[j] == '*' && j+1 < len(b) && b[j+1] == '/' {
					b[j] = ' '
					b[j+1] = ' '
					j += 2
					break
				}
				if b[j] != '\n' {
					b[j] = ' '
				}
				j++
			}
			i = j - 1
			continue
		}
	}
	return string(b)
}

// mirrorCheckArrayRegion 在**原始文本**里定位「该约束的 CHECK 表达式里的
// ARRAY[...]」的区间（绝对下标）。
//
// ★ 先做**保长掩码**再定位 ⇒ 得到的下标可直接用于改原文。
func mirrorCheckArrayRegion(text, name string) (int, int, bool) {
	masked := mirrorMaskSQL(text)
	cs, ce, ok := mirrorConstraintRegion(masked, name)
	if !ok {
		return 0, 0, false
	}
	as, ae, _, ok := scanSQLArrayRegion(masked[cs:ce], 0)
	if !ok {
		return 0, 0, false
	}
	return cs + as, cs + ae, true
}

// mirrorRemoveLiteralInCheck 在**该约束的 CHECK 数组区间内**删掉一个字面量。
//
// ★ 必须钉区间：同一个字面量在「守卫数组 v_required」与「真正的 CHECK 数组」
// ★ 里各出现一次（★ 930/957 都有）⇒ 全文替换会改到**守卫数组**
// ★ ⇒ 变异「看起来生效」实则测的是另一条腿。
//
// ★ 顺带吃掉可选的 `::text` 转型后缀与尾随逗号（基线的 pg_dump 形态带转型）。
func mirrorRemoveLiteralInCheck(t *testing.T, body, literal string) (string, bool) {
	t.Helper()
	masked := mirrorMaskSQL(body)
	s, e, ok := mirrorCheckArrayRegion(body, errorTypeConstraintName)
	if !ok {
		return body, false
	}
	region := masked[s:e]
	token := "'" + literal + "'"
	rel := strings.Index(region, token)
	if rel < 0 {
		return body, false
	}
	abs := s + rel + len(token)
	// 吃掉可选的 `::type` 后缀。
	for abs+1 < len(body) && body[abs] == ':' && abs+2 < len(body) && body[abs+1] == ':' {
		k := abs + 2
		for k < len(body) && (body[k] == '_' || (body[k] >= 'a' && body[k] <= 'z') ||
			(body[k] >= 'A' && body[k] <= 'Z') || (body[k] >= '0' && body[k] <= '9')) {
			k++
		}
		if k == abs+2 {
			break
		}
		abs = k
	}
	// 吃掉尾随逗号与空白（数组里最后一个元素没有逗号 ⇒ 只吃掉空白）。
	j := abs
	for j < len(body) && (body[j] == ' ' || body[j] == '\t') {
		j++
	}
	if j < len(body) && body[j] == ',' {
		j++
		for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n') {
			j++
		}
	}
	return body[:s+rel] + body[j:], true
}

// mirrorMutateFace 在临时树里对某个面做一次**区间锚定**的字面量删除。
func mirrorMutateFace(t *testing.T, root, rel, literal string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	mutated, ok := mirrorRemoveLiteralInCheck(t, string(raw), literal)
	if !ok {
		t.Fatalf("变异没生效：%s 的 CHECK 数组里找不到 %q（★ 区间锚定失败 ⇒ "+
			"多半是提取器坏了，不是门没牙）", rel, literal)
	}
	if err := os.WriteFile(p, []byte(mutated), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", rel, err)
	}
}

// TestSelfCheckErrorTypeMirrorFaceGateHasTeeth 是**变异自检**。
//
// ★ 纪律：断言「判据转红」+「转红时点名被改的那个面」+「点名被拆的那个字面量」，
// ★ 而不是断言「进程非零」。
// ★ 并且先断言「变异真的改变了读数」—— 否则「变异没生效」会被误当成「门有牙」。
func TestSelfCheckErrorTypeMirrorFaceGateHasTeeth(t *testing.T) {
	root := filepath.Clean(mirrorRepoRoot)
	declared := mirrorLoadDeclaredFaces(t, mirrorFacesFixture)
	faces := mirrorDiscoverFaces(t, root)
	required := requiredErrorTypeLiterals()

	// 反向对照：未变异时必须绿（否则「门恒红」也满足下面的断言）。
	if v := mirrorCheckAll(declared, faces, required); len(v) > 0 {
		t.Fatalf("未变异时门却判红 ⇒ 门判错了：%v", v)
	}

	const victim = "rate_limit_observed"

	t.Run("窄化基线副本", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		mirrorMutateFace(t, tree, "sql/schema/01-schema.sql", victim)
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「基线副本收窄」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "sql/schema/01-schema.sql") || !strings.Contains(joined, victim) {
			t.Fatalf("门转红了但没有点名被改的面与被拆的字面量 ⇒ 报错指向不明。读数: %s", joined)
		}
	})

	t.Run("窄化 deploy 迁移", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		mirrorMutateFace(t, tree,
			"deploy/sql/migrations/V361__self_check_runs_canonical_taxonomy.sql", victim)
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「deploy 迁移收窄」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "V361__self_check_runs_canonical_taxonomy.sql") ||
			!strings.Contains(joined, victim) {
			t.Fatalf("门转红了但没有点名被改的面与被拆的字面量 ⇒ 报错指向不明。读数: %s", joined)
		}
	})

	t.Run("加杂物字面量", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		mirrorMutateFace(t, tree, "sql/objects/tables/self_check_runs.sql", victim)
		// 再塞一个不在闭集里的字面量。
		p := filepath.Join(tree, filepath.FromSlash("sql/objects/tables/self_check_runs.sql"))
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读失败: %v", err)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(raw),
			"'account_restricted'::text,", "'account_restricted'::text, 'not_a_kind'::text,", 1)), 0o644); err != nil {
			t.Fatalf("写失败: %v", err)
		}
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「杂物字面量」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "not_a_kind") {
			t.Fatalf("门转红了但没有点名杂物字面量 ⇒ 报错指向不明。读数: %s", joined)
		}
	})

	t.Run("破坏回滚链", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		// 929.down 必须精确恢复 644.up 的闭集（31 项）⇒ 拆掉其中一项即漂移。
		mirrorMutateFace(t, tree,
			"sql/migrations/startup/929_self_check_error_type_kind_closure.down.sql", "canceled")
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「回滚链漂移」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "929_self_check_error_type_kind_closure.down.sql") {
			t.Fatalf("门转红了但没有点名被改的回滚面 ⇒ 报错指向不明。读数: %s", joined)
		}
	})

	t.Run("破坏 up 链单调性", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		// 930 砍掉 929 已经有的 `circuit_open` ⇒ up 链在此收窄。
		mirrorMutateFace(t, tree,
			"sql/migrations/startup/930_self_check_error_type_kind_closure_model_unavailable.sql",
			"circuit_open")
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「up 链收窄」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "930_self_check_error_type_kind_closure_model_unavailable.sql") {
			t.Fatalf("门转红了但没有点名被改的 up 面 ⇒ 报错指向不明。读数: %s", joined)
		}
	})

	t.Run("删掉一个声明面", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		if err := os.Remove(filepath.Join(tree,
			filepath.FromSlash("deploy/sql/schemas/baseline/01-schema.sql"))); err != nil {
			t.Fatalf("删面失败: %v", err)
		}
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「声明面被删」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "deploy/sql/schemas/baseline/01-schema.sql") {
			t.Fatalf("门转红了但没有点名被删的面 ⇒ 报错指向不明。读数: %s", joined)
		}
	})

	t.Run("新增一个未登记的镜像面", func(t *testing.T) {
		tree := mirrorCopyFaceTree(t, root, faces)
		newFace := filepath.Join(tree, filepath.FromSlash("deploy/sql/schemas/stray_copy.sql"))
		if err := os.MkdirAll(filepath.Dir(newFace), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		body := "ALTER TABLE public.self_check_runs\n" +
			"    ADD CONSTRAINT " + errorTypeConstraintName + " CHECK (\n" +
			"        error_type = ANY (ARRAY['none'])\n" +
			"    ) NOT VALID;\n"
		if err := os.WriteFile(newFace, []byte(body), 0o644); err != nil {
			t.Fatalf("写新面失败: %v", err)
		}
		got := mirrorCheckAll(declared, mirrorDiscoverFaces(t, tree), required)
		if len(got) == 0 {
			t.Fatal("变异后门仍判绿 ⇒ 这道门对「未登记的新镜像面」毫无反应（没有牙）")
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "stray_copy.sql") {
			t.Fatalf("门转红了但没有点名新面 ⇒ 报错指向不明。读数: %s", joined)
		}
	})
}

// TestSelfCheckErrorTypeMirrorFaceExtractorIgnoresCommentedLiterals 是量具自证：
// 只在注释里出现的字面量**不得**被提取器当成声明。
func TestSelfCheckErrorTypeMirrorFaceExtractorIgnoresCommentedLiterals(t *testing.T) {
	const victim = "rate_limit_observed"
	sql := "-- 说明段里提到 '" + victim + "' 这个字面量（散文，不是声明）\n" +
		"ALTER TABLE public.self_check_runs\n" +
		"    ADD CONSTRAINT " + errorTypeConstraintName + " CHECK (\n" +
		"        error_type = ANY (ARRAY['none'])\n" +
		"    ) NOT VALID;\n"

	clean := stripSQLComments(sql)
	s, e, ok := mirrorConstraintRegion(clean, errorTypeConstraintName)
	if !ok {
		t.Fatal("提取器没找到约束表达式 ⇒ 提取器坏了")
	}
	lits, ok := mirrorValueLiterals(clean[s:e])
	if !ok {
		t.Fatal("提取器没抽出值集合 ⇒ 提取器坏了")
	}
	if lits[victim] {
		t.Fatal("提取器把注释里的字面量当成了声明 ⇒ 门会假绿（注释不许参与判定）")
	}
	if !lits["none"] {
		t.Fatalf("提取器漏掉了真正的声明字面量 'none' ⇒ 提取器坏了（读数: %v)", lits)
	}
}

// TestSelfCheckErrorTypeMirrorFaceExtractorHandlesBothDeclarationForms 是量具自证：
// `= ANY (ARRAY[...])` 与 `IN (...)` 两种声明形态都要能抽出同一个值集合。
func TestSelfCheckErrorTypeMirrorFaceExtractorHandlesBothDeclarationForms(t *testing.T) {
	arrayForm := "ALTER TABLE public.self_check_runs\n" +
		"    ADD CONSTRAINT " + errorTypeConstraintName + " CHECK (\n" +
		"        error_type = ANY (ARRAY['none', 'timeout'])\n" +
		"    ) NOT VALID;\n"
	inForm := "ALTER TABLE public.self_check_runs\n" +
		"    ADD CONSTRAINT " + errorTypeConstraintName + " CHECK (\n" +
		"        error_type IN ('none', 'timeout')\n" +
		"    ) NOT VALID;\n"

	for label, sql := range map[string]string{"ARRAY 形态": arrayForm, "IN 形态": inForm} {
		clean := stripSQLComments(sql)
		s, e, ok := mirrorConstraintRegion(clean, errorTypeConstraintName)
		if !ok {
			t.Fatalf("%s：提取器没找到约束表达式", label)
		}
		lits, ok := mirrorValueLiterals(clean[s:e])
		if !ok {
			t.Fatalf("%s：提取器没抽出值集合", label)
		}
		if len(lits) != 2 || !lits["none"] || !lits["timeout"] {
			t.Fatalf("%s：抽出值集合 %v ≠期望 {none, timeout}", label, lits)
		}
	}
}
