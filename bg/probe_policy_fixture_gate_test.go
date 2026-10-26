//go:build integration

package bg

// 探针排除谓词 × **测试夹具**的列一致性门（2026-10-21，R59 轮）
//
// # 为什么这道门必须存在（它补的是一个**判据层错位**）
//
// 既有守卫 `TestProbeTrafficExclusionPredicateColumnsExistOnTheirFamily`
// （bg/probe_policy_family_gate_test.go）把「每条谓词引用的列」与
// 「**仓内迁移声明的列**」求差集：
//
//	v1 族     → sql/objects/tables/request_logs_hot.sql
//	会话族    → sql/migrations/startup/526_session_turns_hot.sql + 707 的加列
//
// 那份列集合**与生产一致**，所以那道门是绿的 —— 而 R59 轮在真库 sweep 里实测到
// 5 条结算腿全红在同一个错误上：
//
//	loadTaskBaselines: ERROR: column rl.origin_stage does not exist (SQLSTATE 42703)
//	settleBaselinesSQL(v1): ERROR: column rl.quality_flags does not exist (SQLSTATE 42703)
//
// 原因：这些腿跑在**测试自己手写的夹具**上（bg/auto_route_settle_worker_integration_test.go
// 的 `settleWorkerSchema`），那份夹具的列集**早于**产品 SQL 的需求：
// `_request_logs_hot` 没有 quality_flags / origin_stage，
// `session_turns_hot` 没有 origin_stage。
//
// ⇒ ★ **判据与被测对象不在同一层**：门查的是「迁移声明」，失败发生在「夹具」。
//   这是 AGENTS.md 判据纪律第 1 条的教科书形态 ——
//   门绿不是因为没缺陷，而是因为它没看缺陷所在的那一层。
//
// # 本门做什么
//
// 把同一套谓词、同一套列，**再对夹具求一次差集**：
// ① 谓词用到的列必须都在**该族那张夹具表**上（否则那条腿必然 42703）；
// ② 反向：夹具表**不得**出现别族的专属列（会话族夹具若有了 quality_flags，
//    「族谓词不许引用别族列」这条性质在夹具上就失去意义 —— 缺列断言会变廉价）。
//
// # 为什么放在 integration build tag 下
//
// 夹具常量 `settleWorkerSchema` 住在带 `//go:build integration` 的文件里
// （它要起真库）⇒ 无 tag 的门引用不到它（编译期就炸）。
// 而**失败也只发生在 integration 腿** ⇒ 门与失败同层 ⇒ tag 一致。
//
// # 变异验证
//
//	M1 从夹具 `_request_logs_hot` 里删掉 `origin_stage`   ⇒ ① 红
//	M2 从夹具 `session_turns_hot` 里删掉 `origin_stage`    ⇒ ① 红
//	M3 给夹具 `session_turns_hot` 加一列 `quality_flags`   ⇒ ② 红
//	M4 把夹具解析器锚点删掉（让解析不出列）              ⇒ 自指断言红（不是恒绿）

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fixtureTableBlockRE 抓夹具 DDL 里 `CREATE TABLE <name> ( ... );` 的列块。
// 夹具的缩进是**一个制表符**，而既有门的 tableColumnRE 要求「两个以上空白字符」
// ⇒ 这里单独用「一个以上」的正则（两者不能互换，见文件头 M4）。
var fixtureTableBlockRE = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+([a-z_][a-z0-9_]*)\s*\((.*?)\n\)`)

// fixtureColumnRE 抓夹具列块里的一行一列。
var fixtureColumnRE = regexp.MustCompile(`(?m)^\s*([a-z_][a-z0-9_]*)\s+[a-zA-Z]`)

// fixtureNonColumnWords 是「看着像列、其实不是列」的 DDL 关键字。
// 漏掉它们 ⇒ 解析出的列集里混进 "constraint"/"primary" ⇒ 差集断言变廉价。
var fixtureNonColumnWords = map[string]bool{
	"constraint": true, "primary": true, "unique": true, "check": true,
	"foreign": true, "exclude": true, "like": true, "partition": true,
}

// fixtureColumns 返回夹具里某张表的列集合。
//
// ★ 自指断言：解析结果必须含锚点列，否则直接判红 ——
// 「自己解析不出来却静默通过」与「没有检查」在输出里长得一样。
func fixtureColumns(t *testing.T, ddl, table string, anchors []string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	block := fixtureTableBlockRE.FindStringSubmatchIndex(ddl)
	if block == nil {
		t.Fatalf("夹具 DDL 里找不到任何 `CREATE TABLE ... ( ... );` 块。\n" +
			"  解析不出来却继续跑 ⇒ 本门拿着空列集合判绿 ⇒ 等于没有门。\n" +
			"  若夹具改了建表语句形态，请改这里的正则并同步更新本注释。")
	}
	// 找出目标表的那一块（夹具里多张表，逐块扫）。
	found := false
	for _, m := range fixtureTableBlockRE.FindAllStringSubmatchIndex(ddl, -1) {
		if ddl[m[2]:m[3]] != table {
			continue
		}
		found = true
		for _, c := range fixtureColumnRE.FindAllStringSubmatch(ddl[m[4]:m[5]], -1) {
			if fixtureNonColumnWords[c[1]] {
				continue
			}
			cols[c[1]] = true
		}
	}
	if !found {
		t.Fatalf("夹具 DDL 里找不到表 %q 的建表块。\n"+
			"  找不到 ⇒ 本门对它没有任何结论（这比判红更糟：看起来是「检查通过」）。", table)
	}
	for _, a := range anchors {
		if !cols[a] {
			t.Fatalf("从夹具的 %s 建表块里解析出 %d 列，但缺锚点列 %q。\n"+
				"  解析器抓到的东西不对 ⇒ 后面所有差集都是无意义的。", table, len(cols), a)
		}
	}
	return cols
}

// TestProbePredicateColumnsExistOnTheIntegrationFixture 是本门的主体。
func TestProbePredicateColumnsExistOnTheIntegrationFixture(t *testing.T) {
	const alias = "rl"

	v1Fixture := fixtureColumns(t, settleWorkerSchema, "_request_logs_hot",
		[]string{"request_id", "task_type", "ts"})
	sessionFixture := fixtureColumns(t, settleWorkerSchema, "session_turns_hot",
		[]string{"session_id", "turn_no", "task_type", "ts"})

	cases := []struct {
		label     string
		family    string
		fixture   map[string]bool
		mustHave  []string
		forbidAny []string
	}{
		{
			label:  "v1",
			family: settleFamilyV1,
			// v1 族谓词两条臂：'probe' = ANY(quality_flags) 与 COALESCE(origin_stage,'business')。
			fixture:  v1Fixture,
			mustHave: []string{"origin_stage", "quality_flags"},
		},
		{
			label:  "session",
			family: settleFamilySession,
			// 会话族谓词三条臂：origin_stage / task_type / origin_actor。
			fixture:   sessionFixture,
			mustHave:  []string{"origin_stage", "task_type", "origin_actor"},
			forbidAny: []string{"quality_flags"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rendered, ok := probeTrafficExclusionPredicateFor(tc.family, alias)
			if !ok {
				t.Fatalf("族 %q 在 probeTrafficExclusionPredicateFor 里没有对应谓词。\n"+
					"  没有谓词 ⇒ settleBaselinesSQL 会 panic。", tc.family)
			}
			used := predicateColumns(t, tc.label, rendered)

			var missing []string
			for col := range used {
				if !tc.fixture[col] {
					missing = append(missing, col)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("族 %q 的探针谓词引用了**夹具表**上不存在的列：%v\n"+
					"  渲染结果：%s\n"+
					"  ⇒ 这些腿跑在夹具上时**每一次结算轮询都会 42703**（unknown column）。\n"+
					"  ★ 这不是产品缺陷：生产表有这些列（迁移 428/707 补的）。\n"+
					"    是**夹具的列集比产品 SQL 的需求旧** ⇒ 修夹具，不是改产品。\n"+
					"  ★ 而既有守卫查的是「仓内迁移声明的列」，不是夹具 ⇒ 它绿着，\n"+
					"    因为**判据与被测对象不在同一层**（本门补的就是这一层）。",
					tc.family, missing, rendered)
			}

			var notUsed []string
			for _, col := range tc.mustHave {
				if tc.fixture[col] && !used[col] {
					notUsed = append(notUsed, col)
				}
			}
			if len(notUsed) > 0 {
				sort.Strings(notUsed)
				t.Errorf("族 %q 的探针谓词**没有用到**夹具表上真实存在的探针标记列：%v\n"+
					"  渲染结果：%s\n"+
					"  这些列在族表上存在却被排除在谓词之外 ⇒ 对应的探针形态会静默进入 cohort。",
					tc.family, notUsed, rendered)
			}

			for _, col := range tc.forbidAny {
				if tc.fixture[col] {
					t.Errorf("族 %q 的夹具表**不该有** %q 这一列。\n"+
						"  它是**别族**的列：会话族的真实表（迁移 707）就没有它。\n"+
						"  夹具补上它 ⇒ 「族谓词不许引用别族列」这条性质在夹具上就失去意义 ⇒ 缺列断言变廉价。",
						tc.family, col)
				}
			}
		})
	}
}

// TestSettleLegsPinTheirFamily 钉住「结算腿运行时读到的族 = v1」。
//
// ★ 为什么这道门必须存在（2026-10-21，R59 轮）：
//
//	结算读哪一族 = `settleSourceFor(settings.RequestLogsWriteEnabled())`，
//	而 `RequestLogsWriteEnabled()` 的**缺省值**在 2026-10-20（audit D0）被从 true 改成 false
//	⇒ 缺省时族 = session ⇒ 按 v1 写的结算腿**测不到自己**：cohort 为空
//	⇒ 失败消息是「baseline for task_type 'code' missing」，**完全不指向真正的原因（族选错）**。
//	⇒ 这道门只断言一件事：**钉住之后**读到的是 v1 族
//	（它证明「钉住」这个动作有效，而不是把「缺省是哪个族」冻成规格 —— 后者是待属主决策项）。
func TestSettleLegsPinTheirFamily(t *testing.T) {
	withSettleSourceV1(t)
	if got := currentSettleSource().TurnsTable; got != "_request_logs_hot" {
		t.Errorf("钉住 S4 写门之后，结算源族应是 v1（_request_logs_hot），实得 %q。\n"+
			"  ⇒ 辅助函数 withSettleSourceV1 没生效 ⇒ 结算腿会静默读到别的族，\n"+
			"    而症状只会是「cohort 为空」，看不出原因。", got)
	}
}

// TestSettleFixtureCoversEveryPredicateColumnOnBothFamilies 补一条更宽的断言：
// 两条族的谓词列集合，在**各自那张夹具表**上必须全部存在。
//
// 为什么要两条门：上一条按族分述（v1 一条、session 一条），
// 这条把「每个族的谓词 ⇒ 只在它自己的夹具表上找列」这件事独立出来钉住 ——
// 万一将来有人把两族的表名写反，这条会红，而上一条可能仍然绿。
func TestSettleFixtureCoversEveryPredicateColumnOnBothFamilies(t *testing.T) {
	tables := map[string]string{
		"_request_logs_hot": settleFamilyV1,
		"session_turns_hot": settleFamilySession,
	}
	for table, family := range tables {
		cols := fixtureColumns(t, settleWorkerSchema, table, []string{"ts"})
		rendered, ok := probeTrafficExclusionPredicateFor(family, "rl")
		if !ok {
			t.Fatalf("族 %q 没有对应谓词。", family)
		}
		used := predicateColumns(t, family, rendered)
		var missing []string
		for c := range used {
			if !cols[c] {
				missing = append(missing, c)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("族 %q 的谓词列 %v 不在夹具表 %q 上。", family, missing, table)
		}
	}
}

// fixtureTableNames 列出夹具里建的表（给「表名写反」那条断言用）。
func fixtureTableNames(ddl string) []string {
	var out []string
	for _, m := range fixtureTableBlockRE.FindAllStringSubmatch(ddl, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// TestSettleFixtureHasExactlyTheTwoFamilyTables 钉住「夹具里只有这两张族表」。
//
// 理由：上一条按族找表，若有人把族名映射到**同一张**表上，两条断言会同时变绿
// ⇒ 少了一张表也可能没人发现。这条把表的**数量与名字**钉死。
func TestSettleFixtureHasExactlyTheTwoFamilyTables(t *testing.T) {
	got := strings.Join(fixtureTableNames(settleWorkerSchema), ",")
	want := "_request_logs_hot,auto_route_selections_hot,session_summaries,session_turns_hot"
	if got != want {
		t.Errorf("夹具里的表清单变了：\n  实得：%s\n  期望：%s\n"+
			"  族表增删会让「每族一张夹具表」的映射悄悄失配 ⇒ 差集断言会指向错误的表。",
			got, want)
	}
}
