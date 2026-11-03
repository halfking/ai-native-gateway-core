package schema

// fk_target_unique_constraint_gate_test.go —— ★ 「外键的引用列必须被目标表的主键/唯一约束覆盖」门。
//
// # 它守的不变量
//
//	全新安装基线（01-schema.sql 三份副本）里的**每一条** FOREIGN KEY，
//	它的**引用列集合**必须被**目标表**上的某条 PRIMARY KEY / UNIQUE 约束**恰好覆盖**
//	（PostgreSQL 要求：引用列上存在一个**恰好**覆盖这些列的唯一索引/约束）。
//
// ★★ 为什么值得做成门（★ 2026-11-04 12h 审计轮**实测**出来的，不是推演）：
//
//	上一轮提交 `2b0731675`（运营计费 P0/P1）**手工**把迁移 936 的新表
//	`subscription_plan_entitlements` 和它的外键
//	`FOREIGN KEY (plan_id) REFERENCES public.subscription_plans(id)`
//	抄进了基线，**但没有**把迁移 936 自带的「幂等补主键」DO 块的效果一起抄进来
//	⇒ 基线里 `subscription_plans` 只有 `UNIQUE (code)`、**`id` 上没有唯一约束**
//	⇒ 而基线是**整体作为一个单元**被应用的（`init-local-db.sh` 步骤 1、
//	  `deploy-local.sh` 的 `apply_schema_if_empty`），**排在任何迁移之前**
//	⇒ ⇒ PostgreSQL 拒绝这条外键：SQLSTATE 42803
//	    「there is no unique or exclusion constraint matching given keys」
//	⇒ ⇒ **全新安装当场中止**（`die "schema snapshot 01-schema.sql failed"`）。
//
//	★ **为什么既有门全都看不见**（★ 这一段是本门存在的理由）：
//	  · `baseline_drift_test.go`：只比三份副本的对象集合，**不看约束语义**；
//	  · `baseline_ordering_test.go`：只钉「定义先于引用」的**行序**，不判「引用是否可满足」；
//	  · `baseline_view_refs_test.go`：只看视图的引用；
//	  · `sql/migrations/startup/` 下的迁移门：跑的是**迁移**，不是基线。
//	⇒ ⇒ 三份副本在缺陷存在期间**逐字节相同**、且**全部门绿**
//	⇒ ⇒ 这是一条**假绿**：门全绿 ≠ 门覆盖了「全新安装能不能装完」这件事。
//
// ★★ **迁移 936 的 DO 块救不了这个场景**（★ 这是本轮最关键的一处推理）：
//	「基线缺一个对象 ⇒ 只要有在册迁移会建它 ⇒ 合法」——这是
//	`baseline_drift_test.go` 写明的**单向**规则，它成立的前提是
//	**基线自身不引用那个对象**。
//	而这里基线**自己就带着这条外键** ⇒ 外键必须在**基线被应用的瞬间**可满足
//	⇒ 排在后面的迁移再补主键也来不及 ⇒ ⇒ 基线必须**自带**那个唯一约束。
//
// # 三条判定
//
//	A（覆盖）：每条 FK 的引用列集合，必须被目标表某条 PK/UNIQUE **恰好**覆盖。
//	B（可达）：每条 FK 的目标表，必须**在本基线里被建出来**（否则 FK 同样无法应用）。
//	C（量具自检）：解析出的 FK 数 / 约束数必须 > 0，否则本门**恒绿 = 形同虚设**。
//
// ★ 自检（C）不可省：★ 解析器一旦遇到没见过的书写形态而静默跳过，
//	本门会变成「零检查」而依然全绿 ⇒ 那正是本门要防的那类失效本身。

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fkConstraintRe 匹配 pg_dump 形态的三类约束：
//
//	ALTER TABLE ONLY public.T
//	    ADD CONSTRAINT c PRIMARY KEY (cols);
//	ALTER TABLE ONLY public.T
//	    ADD CONSTRAINT c UNIQUE (cols);
//	ALTER TABLE ONLY public.T
//	    ADD CONSTRAINT c FOREIGN KEY (cols) REFERENCES public.R (cols);
var fkConstraintRe = regexp.MustCompile(
	`ALTER TABLE ONLY public\.(\w+)\s*\n\s*ADD CONSTRAINT (\w+)\s+` +
		`(PRIMARY KEY|UNIQUE|FOREIGN KEY)\s*\(([^)]*)\)` +
		`(?: REFERENCES public\.(\w+)\s*\(([^)]*)\))?`)

// fkStripSQLComments 去掉 `--` 行注释，但**不碰**单引号字符串字面量里的内容。
//
// ★ 为什么要引号感知：naive 的 `strings.Split(line, "--")` 会把
// `'a--b'` 这样的默认值截断，进而让后续解析**静默少读一条约束**
// ⇒ ⇒ 那会让本门变成假绿 ⇒ ⇒ 与本门要防的失效同型。
// ★ 同包的 `stripSQLComments`（storage_reclaim_guard_test.go）**不是**引号感知的，
//
//	且它的语义是「逐行替换、保留行数」⇒ 不适合本门（本门要的是「内容正确」）。
func fkStripSQLComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inQuote := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inQuote {
			b.WriteByte(c)
			if c == '\'' {
				// 连续两个单引号是转义 ('')，不闭合字符串
				if i+1 < len(src) && src[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				inQuote = false
			}
			continue
		}
		if c == '\'' {
			inQuote = true
			b.WriteByte(c)
			continue
		}
		if c == '-' && i+1 < len(src) && src[i+1] == '-' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// parseColumnSet 把 "(a, b)" 形态的列清单解析成列名集合。
// 只接受**裸标识符**；出现表达式（如 `lower(x)`）时返回 nil ——
// 因为表达式唯一索引**不满足**外键对「该列集合恰有唯一约束」的要求。
func parseColumnSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !identRe.MatchString(part) {
			return nil
		}
		out[part] = true
	}
	return out
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// attachPartitionRe 匹配基线里的分区挂载语句：
//
//	ALTER TABLE ONLY public.credit_ledger ATTACH PARTITION public.credit_ledger_2026_07 FOR VALUES ...
var attachPartitionRe = regexp.MustCompile(
	`ALTER TABLE ONLY public\.(\w+)\s+ATTACH PARTITION public\.(\w+)`)

// tableColumns 解析一份基线里每张表的列名集合。
//
// ★★ 为什么要这一层（★ 2026-11-04 12h 审计轮 D-5 的教训）：
//
//	★ 本门第一版**只查外键** ⇒ 对「分区缺列」**完全隐形** ⇒
//	  门全绿，而基线在真库上**仍然装不上**（实测：ATTACH PARTITION 报
//	  「在子表中没有列 "owner_user"」⇒ 基线整体中止）。
//	⇒ ⇒ **「外键可满足」不等于「基线可加载」**：分区必须在 ATTACH 时刻
//	  带上父表的**每一个列** ⇒ 列集合也要查。
func tableColumns(src string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, m := range createTableRe.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		// 从 "(" 起按括号深度扫描到配平的 ")"（列清单里有 varchar(32) 这类
		// 修饰，不能按第一个 ")" 截断）。
		start := m[1] - 1
		depth := 0
		end := -1
		for i := start; i < len(src); i++ {
			switch src[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		body := src[start+1 : end]
		cols := map[string]bool{}
		for _, ln := range strings.Split(body, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" {
				continue
			}
			// 表级约束不是列
			if strings.HasPrefix(t, "CONSTRAINT ") || strings.HasPrefix(t, "PRIMARY KEY") ||
				strings.HasPrefix(t, "UNIQUE ") || strings.HasPrefix(t, "CHECK ") ||
				strings.HasPrefix(t, "FOREIGN KEY") {
				continue
			}
			f := strings.Fields(t)
			if len(f) == 0 {
				continue
			}
			cols[strings.Trim(f[0], "\"")] = true
		}
		out[name] = cols
	}
	return out
}

// checkPartitionColumnCoverage 校验每条 ATTACH PARTITION：子表的列集合必须
// **覆盖**父表的列集合（PostgreSQL 的硬要求）。
func checkPartitionColumnCoverage(src string) []string {
	clean := fkStripSQLComments(src)
	cols := tableColumns(clean)
	var problems []string
	for _, m := range attachPartitionRe.FindAllStringSubmatch(clean, -1) {
		parent, child := m[1], m[2]
		pcols, ok := cols[parent]
		if !ok {
			problems = append(problems, fmt.Sprintf("父表 %s 不在基线内（分区挂载无处可指）", parent))
			continue
		}
		ccols, ok := cols[child]
		if !ok {
			problems = append(problems, fmt.Sprintf("子表 %s 不在基线内（分区无处可挂）", child))
			continue
		}
		var missing []string
		for c := range pcols {
			if !ccols[c] {
				missing = append(missing, c)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"分区 %s 缺少父表 %s 的列 %v ⇒ PostgreSQL 在 ATTACH PARTITION 时报"+
					"「在子表中没有列 %q」⇒ 基线整体应用中止",
				child, parent, missing, missing[0]))
		}
	}
	return problems
}

// fkViolation 描述一条「引用列没被唯一约束覆盖」的外键。
type fkViolation struct {
	Constraint string
	FromTable  string
	RefTable   string
	RefColumns []string
	Reason     string
}

// checkFKCoverage 扫一份基线文本，返回违规列表。
//
// ★ 这段逻辑**同时**是判据和量具：门与自检共用同一份实现
//
//	⇒ 不会出现「门在测 A、自检在测 B」的漂移。
func checkFKCoverage(src string) (violations []fkViolation, fkCount, uniqueCount int, createdTables map[string]bool) {
	clean := fkStripSQLComments(src)

	createdTables = map[string]bool{}
	for _, m := range createTableRe.FindAllStringSubmatch(clean, -1) {
		// createTableRe 捕获的是带 schema 的名字（如 public.foo）⇒ 取最后一段
		name := m[1]
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		createdTables[name] = true
	}

	// target -> 该表上「恰好覆盖某组列」的唯一约束集合
	uniqueSets := map[string][]map[string]bool{}
	for _, m := range fkConstraintRe.FindAllStringSubmatch(clean, -1) {
		tbl, cname, kind, cols, refTbl, refCols := m[1], m[2], m[3], m[4], m[5], m[6]
		switch kind {
		case "PRIMARY KEY", "UNIQUE":
			set := parseColumnSet(cols)
			if set == nil {
				// 表达式唯一索引：不是 FK 可用的唯一约束，忽略（但不计入 uniqueCount）
				continue
			}
			uniqueSets[tbl] = append(uniqueSets[tbl], set)
			uniqueCount++
		case "FOREIGN KEY":
			fkCount++
			refSet := parseColumnSet(refCols)
			if refSet == nil {
				violations = append(violations, fkViolation{
					Constraint: cname, FromTable: tbl, RefTable: refTbl,
					Reason: "引用列清单不是裸标识符集合，判据无法判定 ⇒ 按不可满足处理",
				})
				continue
			}
			if !createdTables[refTbl] {
				violations = append(violations, fkViolation{
					Constraint: cname, FromTable: tbl, RefTable: refTbl,
					RefColumns: sortedKeys(refSet),
					Reason:     "目标表不在本基线内 ⇒ 基线整体应用时该外键无处可指",
				})
				continue
			}
			covered := false
			for _, u := range uniqueSets[refTbl] {
				if sameSet(u, refSet) {
					covered = true
					break
				}
			}
			if !covered {
				violations = append(violations, fkViolation{
					Constraint: cname, FromTable: tbl, RefTable: refTbl,
					RefColumns: sortedKeys(refSet),
					Reason:     "引用列没有被目标表上任何 PRIMARY KEY / UNIQUE 约束恰好覆盖 ⇒ PostgreSQL 报 SQLSTATE 42803",
				})
			}
		}
	}
	return violations, fkCount, uniqueCount, createdTables
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestBaselineForeignKeysAreCoveredByUniqueConstraint —— 判定 A + B + C。
func TestBaselineForeignKeysAreCoveredByUniqueConstraint(t *testing.T) {
	for _, c := range copies {
		t.Run(c.name, func(t *testing.T) {
			b, err := os.ReadFile(c.path)
			if err != nil {
				t.Fatalf("read %s: %v", c.path, err)
			}
			violations, fkCount, uniqueCount, tables := checkFKCoverage(string(b))

			// ── 判定 C：量具自检。零 FK / 零唯一约束 ⇒ 本门恒绿 = 形同虚设。
			if fkCount == 0 {
				t.Fatalf("%s：解析出 0 条 FOREIGN KEY ⇒ 解析器没认出来 ⇒ 本门恒绿 ⇒ 判据失效", c.path)
			}
			if uniqueCount == 0 {
				t.Fatalf("%s：解析出 0 条 PRIMARY KEY / UNIQUE 约束 ⇒ 解析器没认出来 ⇒ 本门恒绿 ⇒ 判据失效", c.path)
			}
			t.Logf("%s：解析出 %d 条 FOREIGN KEY / %d 条唯一约束 / %d 张表", c.path, fkCount, uniqueCount, len(tables))

			for _, v := range violations {
				t.Errorf("%s：外键 %s（%s → %s，引用列 %v）不满足：%s\n"+
					"  ★ 后果：全新安装应用基线时 PostgreSQL 报 SQLSTATE 42803 ⇒ 安装中止。\n"+
					"  ★ 修法：把目标表上覆盖该列集合的 PRIMARY KEY / UNIQUE 约束**补进本基线**\n"+
					"    （★ 排在引用它的外键之前；★ 三份副本要同步改，否则 baseline_drift 会红）。",
					c.path, v.Constraint, v.FromTable, v.RefTable, v.RefColumns, v.Reason)
			}

			// ── 判定 D：分区挂载的列集合必须覆盖父表的列集合。
			//   ★ 这条**不在**第一版里 ⇒ 第一版对「分区缺列」完全隐形 ⇒
			//   门全绿而基线在真库上装不上（2026-11-04 12h 审计轮 D-5 实测）。
			partProblems := checkPartitionColumnCoverage(string(b))
			// 量具自检：必须真的认出了分区挂载语句，否则这条判据恒绿。
			if n := len(attachPartitionRe.FindAllStringSubmatch(fkStripSQLComments(string(b)), -1)); n == 0 {
				t.Errorf("%s：一条 ATTACH PARTITION 都没解析出来 ⇒ 分区列覆盖判据恒绿 ⇒ 判据失效", c.path)
			}
			for _, p := range partProblems {
				t.Errorf("%s：%s\n"+
					"  ★ 后果：全新安装应用基线时 PostgreSQL 报「在子表中没有列 ...」⇒ 安装中止。\n"+
					"  ★ 修法：把父表新增的列**补进每个子分区**（★ 分区的列集合必须覆盖父表；\n"+
					"    ★ 活库里 ADD COLUMN 会自动传播到已挂载的分区，★ 但基线是静态 dump ⇒ 必须逐个写明）。",
					c.path, p)
			}
		})
	}
}

// TestFKCoverageCheckerHasTeeth —— ★ 变异臂：证明判据本身能咬住。
//
// ★ 为什么必须有这一条：★ 本门是「**静态**判据」，而静态门最大的失效形态是
//
//	「判据写错了 ⇒ 永远绿」。⇒ 这里用**构造出来的坏样例**驱动同一个
//	`checkFKCoverage`，断言它**确实**报出违规。
//	⇒ ⇒ 门红不只依赖真实基线，还依赖「判据会红」这件事本身被钉住。
func TestFKCoverageCheckerHasTeeth(t *testing.T) {
	const good = `
CREATE TABLE public.parent (
    id integer NOT NULL
);
ALTER TABLE ONLY public.parent
    ADD CONSTRAINT parent_pkey PRIMARY KEY (id);
CREATE TABLE public.child (
    pid integer NOT NULL
);
ALTER TABLE ONLY public.child
    ADD CONSTRAINT child_pid_fkey FOREIGN KEY (pid) REFERENCES public.parent (id);
`
	// 坏样例①：目标表只有 UNIQUE(别的列) ⇒ 引用列没有唯一约束覆盖
	const badNoCover = `
CREATE TABLE public.parent (
    id integer NOT NULL,
    code text NOT NULL
);
ALTER TABLE ONLY public.parent
    ADD CONSTRAINT parent_code_key UNIQUE (code);
CREATE TABLE public.child (
    pid integer NOT NULL
);
ALTER TABLE ONLY public.child
    ADD CONSTRAINT child_pid_fkey FOREIGN KEY (pid) REFERENCES public.parent (id);
`
	// 坏样例②：目标表压根不在基线里 ⇒ 外键无处可指
	const badDangling = `
CREATE TABLE public.child (
    pid integer NOT NULL
);
ALTER TABLE ONLY public.child
    ADD CONSTRAINT child_pid_fkey FOREIGN KEY (pid) REFERENCES public.parent (id);
`

	cases := []struct {
		name      string
		src       string
		wantFKs   int
		wantViole int
	}{
		{"好样例：有 PK 覆盖 ⇒ 无违规", good, 1, 0},
		{"坏样例①：只有 UNIQUE(别的列) ⇒ 必须报违规", badNoCover, 1, 1},
		{"坏样例②：目标表不在基线 ⇒ 必须报违规", badDangling, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violations, fkCount, _, _ := checkFKCoverage(tc.src)
			if fkCount != tc.wantFKs {
				t.Fatalf("解析出 %d 条 FK，期望 %d 条 ⇒ 解析器行为与预期不符 ⇒ 本自检失去意义", fkCount, tc.wantFKs)
			}
			if len(violations) != tc.wantViole {
				t.Fatalf("报出 %d 条违规，期望 %d 条；违规明细：%+v", len(violations), tc.wantViole, violations)
			}
		})
	}
}
