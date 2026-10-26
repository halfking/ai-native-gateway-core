// probe_policy_family_gate_test.go — 审计 §9.73.4/§9.73.5 的分族谓词门。
//
// # 这道门在防什么
//
// settle 基线 cohort 的读点表随 S4 写门在 `_request_logs_hot` ⇄ `session_turns_hot`
// 之间切换，而**两个表可用的探针标记列不同**：
//
//	_request_logs_hot   有 origin_stage / task_type / origin_actor / quality_flags
//	session_turns_hot  有 origin_stage / task_type / origin_actor，**没有 quality_flags**
//
// 迁移 707 给会话族补了 55 列（origin_stage:87、origin_actor:88、is_auto_request:55），
// 但**没有**补 `quality_flags`；252 生产实测双向吻合（session_turns 104 列无
// quality_flags，_request_logs_hot 156 列四列俱全）。
//
// ⇒ 对会话族误用 v1 那条谓词，**每一次结算轮询都会 42703**。
// 这不是理论风险：R50 记录的 admin 回归就是同一个坑（对视图用了物理谓词）。
//
// # 为什么这道门不能只做字符串检查
//
// 「会话族那条谓词里没有 quality_flags」是**字面量**断言，它只能挡住一种写法。
// 真正的失效形态是「某条谓词引用了会话族没有的列」——可以是 quality_flags，
// 也可以是任何将来被加进谓词的列。所以本门做的是**列存在性**：把谓词里引用的
// 每个列名，与该族表在**仓内迁移**里声明的列集合求差集。
//
// ★ 这条差集之所以有意义，是因为**族列的来源必须选对**：
// `sql/objects/tables/session_turns.sql` 只有 **41 列**，且**不含** origin_stage /
// origin_actor / is_auto_request —— 它早于迁移 707，是过期快照。
// 拿它当会话族的列 SSOT，会让 origin_stage / origin_actor 三条臂**全部**被报成
// 缺列（假红），而更糟的是：若有人为了「让门变绿」而把谓词改窄，门就再也拦不住
// 真正的 42703 了。⇒ 会话族取**迁移**（526 建表 + 707 加列），不取 objects 文件。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：**v1 族整族被 DROP** ⇒
// `sql/objects/tables/request_logs_hot.sql` 随 canonical 对象库一起被删除。
// ⇒ 本门对 v1 族的那一半**重铸**（见下方「退役族的闭包门」）：
// 「v1 谓词的列在 v1 表上」这条断言已经**无对象可查**（那张表不存在了），
// 而它在今天的**活**含义是另一条更要紧的不变量：**v1 族不许再被任何活路径
// 选中**——一旦被选中，那条 SQL 会引用一张已 DROP 的表 ⇒ 整条语句 42P01。
// 闭包门（settleSourceFor 恒返回会话族）比列存在性检查更强，且永远有效。
package bg

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	// 迁移里建 hot 表的文件（列的基线集合）。
	migrationSessionTurnsHotDDL = "sql/migrations/startup/526_session_turns_hot.sql"
	// 迁移里给会话族补 55 列的文件。
	migrationSessionTurnsS1A = "sql/migrations/startup/707_session_turns_s1a.sql"
)

// aliasedColumnRE 抓渲染后谓词里的 `<alias>.<col>` 引用。
//
// 三条谓词的每一条臂都带别名（形如 `rl.quality_flags`），所以这个模式覆盖
// 谓词的**全部**列引用；不抓裸列名是有意的——裸列名会与 SQL 关键字
// （NOT / AND / ANY）混淆，而带别名的写法正是本仓的既有约定。
var aliasedColumnRE = regexp.MustCompile(`\b\w+\.(\w+)\b`)

// addColumnRE 抓迁移里的 `ADD COLUMN IF NOT EXISTS <col> <type>`。
var addColumnRE = regexp.MustCompile(`(?i)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+\w`)

// createTableColumnsRE 抓 `CREATE TABLE [IF NOT EXISTS] <schema>.<table> ( ... );` 块里的列。
// 块尾按「行首 `)`」收尾而不是 `\n);`：`sql/objects/tables/request_logs_hot.sql` 收在
// `)\nWITH (autovacuum_...)`，而 526 收在 `);` —— 只认其中一种会让另一个族解析失败，
// 而解析失败在有自指断言时是**响亮的红**（这是本门能安全存在的前提）。
var createTableColumnsRE = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[\w.]*\b(\w+)\s*\((.*?)\n\)`)

// tableColumnRE 抓 DDL 块里的一行一列（`    <col> <type>...`）。
var tableColumnRE = regexp.MustCompile(`(?m)^\s{2,}([a-z_][a-z0-9_]*)\s+[a-zA-Z]`)

func repoRootFromBg(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("解析仓库根失败 %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根不像样（%s/go.mod 读不到：%v）", root, err)
	}
	return root
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromBg(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败 %v。\n"+
			"  这道门把**仓内迁移**当作族列的事实来源；文件被改名/移动时必须同步改门里的路径，\n"+
			"  不能把解析结果当成空集继续跑（空集会让本门静默通过——见自指断言）。", rel, err)
	}
	return string(b)
}

// sessionFamilyColumns 汇总会话族两张表的列集合（建表 + 后续加列）。
//
// ★ **自指断言**：返回的集合若小于 migrationSessionTurnsHotDDL 的建表列数，
// 说明建表块没被解析出来 ⇒ 直接判红。理由见 §9.72.4 记的那一类：
// 「自己解析不出来就静默跳过」与「没有检查」在输出里长得一样。
func sessionFamilyColumns(t *testing.T) map[string]bool {
	t.Helper()
	cols := map[string]bool{}

	ddl := readRepoFile(t, migrationSessionTurnsHotDDL)
	block := createTableColumnsRE.FindStringSubmatchIndex(ddl)
	if block == nil {
		t.Fatalf("%s 里找不到 `CREATE TABLE ... session_turns_hot ( ... );` 块。\n"+
			"  解析不出来却继续跑会让本门拿着空列集合判绿——那等于没有门。\n"+
			"  若迁移改写了建表语句形态，请改门的正则并同步更新本注释。", migrationSessionTurnsHotDDL)
	}
	ddlCols := map[string]bool{}
	for _, m := range tableColumnRE.FindAllStringSubmatch(ddl[block[4]:block[5]], -1) {
		ddlCols[m[1]] = true
		cols[m[1]] = true
	}
	// 锚点：建表块至少要含这几个，否则说明抓到的是别的表或块被截断。
	for _, anchor := range []string{"id", "session_id", "request_id", "task_type", "ts"} {
		if !ddlCols[anchor] {
			t.Fatalf("从 %s 的 session_turns_hot 建表块里解析出 %d 列，但缺锚点列 %q。\n"+
				"  解析器抓到的东西不对 ⇒ 后面所有差集都是无意义的。", migrationSessionTurnsHotDDL, len(ddlCols), anchor)
		}
	}

	// 707 的加列（两个 ALTER 目标：session_turns 与 session_turns_hot，列集相同）。
	s1a := readRepoFile(t, migrationSessionTurnsS1A)
	added := map[string]bool{}
	for _, m := range addColumnRE.FindAllStringSubmatch(s1a, -1) {
		added[strings.ToLower(m[1])] = true
		cols[strings.ToLower(m[1])] = true
	}
	for _, anchor := range []string{"is_auto_request", "origin_stage", "origin_actor"} {
		if !added[anchor] {
			t.Fatalf("从 %s 里没解析出加列 %q。\n"+
				"  会话族的列集合必须包含迁移 707 补的这几列——它们正是会话族探针谓词要用的。\n"+
				"  解析不到 ⇒ 本门会把这三列报成缺列，或（更糟）让缺列断言变得没有意义。",
				migrationSessionTurnsS1A, anchor)
		}
	}
	return cols
}

// v1FamilyColumns（解析 v1 族 hot 表列集合）已随迁移 890 **退役**：
// v1 族连表带分区一起被 DROP，canonical 对象库里的
// `sql/objects/tables/request_logs_hot.sql` 也随之被删除 ⇒ 「v1 谓词引用的列
// 是否都在 v1 表上」已经没有可查的对象（那张表不会再被查询）。
//
// 它被**替换**为 TestRetiredV1FamilyIsUnreachable（退役族闭包门）：v1 族在活
// 路径上不可达 ⇒ 那条 v1 谓词永远不会被执行 ⇒ 「它引用了哪几列」不再是活
// 风险；而「有人把 v1 族选成读源」才是（那会让整条 SQL 42P01）。
//
// 保留这一段的**教训**：族列的来源必须选对（见文件头）——换成会话族之后，
// 同一个坑会以另一个形式回来（拿过期的 objects 快照当列 SSOT）。

// TestRetiredV1FamilyIsUnreachable 是 v1 族退役后的**闭包门**（替代上面那条
// 已无对象可查的 v1 列存在性检查）。
//
// 为什么这条更强：v1 族被 DROP 后，「v1 谓词引用了哪几列」不再是活风险
// ——**没有任何活路径会执行它**。活风险是反过来的：**有人把 v1 族选成读源**
// ⇒ 那条 SQL 引用一张不存在的表 ⇒ 42P01 ⇒ 整个 worker 每轮失败。
//
// ⇒ 门钉住「v1 族标签不得再被产出」：写门开 / 关两种取值都必须落到会话族。
// （这条与 bg/auto_route_settle_source_test.go 的读源门同源；两处都留是因为
// 一处钉的是**结算规格**，一处钉的是**族闭包**。）
func TestRetiredV1FamilyIsUnreachable(t *testing.T) {
	for _, logsWriteEnabled := range []bool{true, false} {
		src := settleSourceFor(logsWriteEnabled)
		if src.Family == settleFamilyV1 {
			t.Errorf("写门=%v 时读源族仍是 %q：v1 族已随迁移 890 被 DROP，"+
				"选中它 ⇒ 整条结算 SQL 42P01。规格：%+v", logsWriteEnabled, src.Family, src)
		}
		if src.TurnsTable == "_request_logs_hot" {
			t.Errorf("写门=%v 时读源表仍是 %q（已随迁移 890 被 DROP）", logsWriteEnabled, src.TurnsTable)
		}
	}
	// 正向对照：活路径确实落在会话族上（否则上面两条可能只是「都没选」）。
	src := settleSourceFor(true)
	if src.Family != settleFamilySession || src.TurnsTable != "session_turns_hot" {
		t.Fatalf("活路径必须落在会话族热腿上；实际：%+v", src)
	}
}

// predicateColumns 返回渲染后谓词引用的列名集合。
func predicateColumns(t *testing.T, label, rendered string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	for _, m := range aliasedColumnRE.FindAllStringSubmatch(rendered, -1) {
		cols[m[1]] = true
	}
	// ★ 自指断言：谓词必须真的带别名列引用。
	//
	// 若将来有人把谓词改写成全裸列名（`origin_stage = 'business'`），
	// 提取结果会是空集，于是下面每一条差集断言都自动为真 ⇒ **门绿着而它
	// 什么都不再检查**。这与 §9.72.4 的 M4 是同一族：解析失败伪装成通过。
	if len(cols) < 2 {
		t.Fatalf("谓词 %q 只解析出 %d 个带别名的列（渲染结果：%s）。\n"+
			"  本门靠 `<alias>.<col>` 提取列引用；谓词若不再带别名，本门将**静默失去全部检查能力**。\n"+
			"  请保持带别名的写法，或同步改提取逻辑（并重跑变异）。", label, len(cols), rendered)
	}
	return cols
}

// TestProbeTrafficExclusionPredicateColumnsExistOnTheirFamily 是本门的主体。
//
// 它把「每条谓词引用的列」与「该族表在仓内迁移里声明的列」求差集。
// 差集非空 ⇒ 红，并直接打印会让 PG 42703 的那个列名。
func TestProbeTrafficExclusionPredicateColumnsExistOnTheirFamily(t *testing.T) {
	const alias = "rl"

	sessionCols := sessionFamilyColumns(t)

	// ★ 2026-10-20（迁移 890，v1 族退役）：**v1 那一格被移除**。
	// v1 族连表带分区一起被 DROP ⇒ 「v1 谓词引用的列是否都在 v1 表上」已经
	// 没有可查的对象（那张表不会再被查询，canonical 对象库里的
	// request_logs_hot.sql 也随之删除）。
	// ⇒ 它被 TestRetiredV1FamilyIsUnreachable（退役族闭包门）替代：v1 族在
	// 活路径上不可达，那条 v1 谓词永远不会被执行。
	// ⚠ 这里**不是**把 v1 的覆盖删掉了：v1 谓词常量仍在（闭集分派函数还认
	// 它），删掉的只是「对它做列存在性检查」这一件已无对象可查的事。
	cases := []struct {
		label    string
		family   string
		cols     map[string]bool
		mustHave []string // 该族谓词**必须**用到的探针标记（少一条 = 少挡一类探针）
	}{
		{label: "session", family: settleFamilySession, cols: sessionCols,
			mustHave: []string{"origin_stage", "task_type", "origin_actor"}},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rendered, ok := probeTrafficExclusionPredicateFor(tc.family, alias)
			if !ok {
				t.Fatalf("族 %q 在 probeTrafficExclusionPredicateFor 里没有对应谓词。\n"+
					"  族集合是闭集：新增族必须同时配一条该族**列上真实存在**的谓词，\n"+
					"  否则 settleBaselinesSQL 会 panic（见该函数的注释）。", tc.family)
			}
			used := predicateColumns(t, tc.label, rendered)

			var missing []string
			for col := range used {
				if !tc.cols[col] {
					missing = append(missing, col)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("族 %q 的探针谓词引用了该族表上**不存在**的列：%v\n"+
					"  渲染结果：%s\n"+
					"  ⇒ 线上每次结算轮询都会 42703（unknown column）。\n"+
					"  这不是新表少列那么简单：会话族是迁移 707 补列的产物，而 707 **没有**补 "+
					"`quality_flags`，所以照抄 v1 那条谓词必然炸。\n"+
					"  修法是给该族配一条只用它真实拥有的列的谓词，"+
					"而不是给表加列（表是事实，不是可调参数）。",
					tc.family, missing, rendered)
			}

			// 反向：该族谓词**必须**覆盖的探针标记。
			//
			// 只有「不缺列」是不够的：把谓词写窄成恒真也能通过上面的差集断言。
			// 这一条盯的是「该族真实存在的探针标记有没有被逐条用上」——
			// 少用一条 = 一类探针静默进入 cohort，而 cohort 是基线，
			// 静默污染它等于静默改变 reward（§9.73.4 实测：99.99% 的 cohort
			// 原本就是探针，而 `probe_triggered` 根本不在 selection 的词表里）。
			var notUsed []string
			for _, col := range tc.mustHave {
				if tc.cols[col] && !used[col] {
					notUsed = append(notUsed, col)
				}
			}
			if len(notUsed) > 0 {
				sort.Strings(notUsed)
				t.Errorf("族 %q 的探针谓词**没有用到**该族表上真实存在的探针标记列：%v\n"+
					"  渲染结果：%s\n"+
					"  这些列在族表上存在，却被排除在谓词之外 ⇒ 对应的探针形态会静默进入 cohort。\n"+
					"  「不缺列」与「用全了」是两件事：把谓词写窄成恒真同样能通过缺列检查。",
					tc.family, notUsed, rendered)
			}
		})
	}
}

// TestProbeTrafficExclusionPredicateForRejectsUnknownFamily 钉住「未知族不静默回落」。
//
// 没有这道门时，把 `default:` 分支改成 `return probeTrafficExclusionPredicate, true`
// （即静默回落到 v1）不会有任何测试反应——而那意味着：新增一个族却没配谓词时，
// 会话族会用上引用 quality_flags 的物理谓词并每次 42703，或者（更隐蔽的变体）
// 回落成空谓词让 cohort 静默变空、每条 reward 塌成中性 0.5。
func TestProbeTrafficExclusionPredicateForRejectsUnknownFamily(t *testing.T) {
	for _, family := range []string{"", "V1", "Session", "session ", "v2", "unknown", "SESSION"} {
		got, ok := probeTrafficExclusionPredicateFor(family, "rl")
		if ok {
			t.Errorf("族 %q 返回了谓词 %q（ok=true）。\n"+
				"  未知族必须**拒绝**：静默回落到 v1 会让会话族用上引用 quality_flags 的谓词（每次 42703），\n"+
				"  静默回落到空串则让 cohort 变空、每条 reward 的延迟/成本项塌成中性 0.5。\n"+
				"  族标签是闭集且大小写敏感（settleSourceFor 只产出 %q / %q）。", family, got, settleFamilyV1, settleFamilySession)
		}
		if got != "" {
			t.Errorf("族 %q 被拒绝时却返回了非空谓词 %q —— 调用方若忽略 ok 就会照样用上它。", family, got)
		}
	}
}

// TestSettleBaselinesSQLPanicsOnUnknownFamily 钉住响亮失败这条选择。
//
// 判据是「**必须** panic」而不是「不许静默」：本轮刻意选 panic 而不是回落，
// 因为空 cohort 是这套系统里最坏的一种静默失败（reward 全塌成 0.5 且无 error）。
// 若将来有人改成返回别的，必须同时改这道门并写下新的理由。
func TestSettleBaselinesSQLPanicsOnUnknownFamily(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("settleBaselinesSQL 对未知族没有 panic。\n" +
				"  未知族按构造不可达（settleSourceFor 是同包两分支闭集），所以这里要的是响亮失败：\n" +
				"  回落成空谓词 = cohort 变空 = 每条 reward 的延迟/成本项静默塌成 0.5，且不是 error。")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "没有配探针排除谓词") {
			t.Errorf("panic 消息没有点明「没有配探针排除谓词」，实际是：%v", r)
		}
	}()
	_ = settleBaselinesSQL(settleSourceSpec{TurnsTable: "t", SessionKeyCol: "c", Family: "v2"})
}
