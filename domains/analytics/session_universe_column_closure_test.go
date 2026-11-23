package analytics

// session_universe_column_closure_test.go —— 会话宇宙的**列闭合守卫**
// （2026-11-23 D2 去重修复部署踩坑后补）。
//
// # 这道门守什么
//
// D2 去重把 `JOIN sessions s ON …` 换成
//
//	JOIN (
//	  SELECT DISTINCT ON (session_id, tenant_id)
//	    <列清单>
//	  FROM sessions
//	  ORDER BY session_id, tenant_id, partition_date DESC
//	) s ON …
//
// —— `sessions` 从「表」变成了「投影」：**下游引用了、但子查询没选的列，
// 会在建视图/跑查询那一刻直接 42P03（column … does not exist）**。
//
// 第一次落地就踩了：`agent_usage_aggregates` 视图体引用 `s.agent_name`，
// 而三处去重子查询都只选了 6 列 ⇒ 211 启动重放 994 时报
// `column s.agent_name does not exist`，部署 FATAL。
//
// ★ 为什么既有契约门没抓到：`TestSessionUniverseJoinMatches994` 只对拍
// 「Go 侧产物 == 迁移 SQL 里的规范片段」——**两边同样漏掉一列时依然全绿**。
// 对拍守卫只能防「两边不一致」，防不住「两边一致地漏」。
//
// ⇒ 本守卫改从**消费面反推生产面**：抽出每段 SQL 里所有 `s.<col>` 引用，
// 断言它们**全部**落在该段去重子查询的 SELECT 列表里。漏一列必红。
//
// # 为什么用「消费面」而不是手工维护列清单
//
// 手工清单（dedup 测试里的 requiredFields）只会抄「此刻用到的列」，
// 下游新增一个 `s.xxx` 引用时它不会变红 ⇒ 守卫会随代码演进失效。
// 从引用面反推则天然跟随下游演进。

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// stripSQLComments 去掉 `--` 行注释（注释里的 `s.xxx` 不是真引用）。
func stripSQLComments(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// dedupSubqueryColumns 抽去重子查询的列清单。
// 形态：SELECT DISTINCT ON (<键>) <列清单> FROM sessions
// 返回 (列集合, 是否找到)。
var dedupSubqueryRe = regexp.MustCompile(
	`(?is)SELECT\s+DISTINCT\s+ON\s*\([^)]*\)\s*(.*?)\s*FROM\s+sessions\b`)

func dedupSubqueryColumns(sql string) (map[string]bool, bool) {
	m := dedupSubqueryRe.FindStringSubmatch(sql)
	if m == nil {
		return nil, false
	}
	cols := map[string]bool{}
	for _, c := range strings.Split(m[1], ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" {
			cols[c] = true
		}
	}
	return cols, true
}

// sessionColumnRefs 抽所有 `s.<col>` 引用（`ss.`/`sd.` 不匹配——`\b` 边界
// 保证：ss. 的第二个 s 前面是词字符，无词边界）。
var sessionColumnRefRe = regexp.MustCompile(`\bs\.([a-z_][a-z0-9_]*)`)

func sessionColumnRefs(sql string) map[string]bool {
	refs := map[string]bool{}
	for _, m := range sessionColumnRefRe.FindAllStringSubmatch(sql, -1) {
		refs[strings.ToLower(m[1])] = true
	}
	return refs
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assertColumnClosure 断言「引用 ⊆ 投影列」。缺列时给出可操作的诊断。
func assertColumnClosure(t *testing.T, label string, sql string, cols map[string]bool) {
	t.Helper()
	refs := sessionColumnRefs(sql)
	if len(refs) == 0 {
		t.Errorf("%s: 没有抽到任何 s.<col> 引用——量具本身失效（别名可能变了）", label)
		return
	}
	for _, ref := range sortedKeys(refs) {
		if !cols[ref] {
			t.Errorf("%s: 引用了 s.%s，但去重子查询的 SELECT 列表里没有这一列"+
				"（投影缺列 ⇒ 建视图/查询时 42P03）\n  子查询列: %v\n  引用列: %v",
				label, ref, sortedKeys(cols), sortedKeys(refs))
		}
	}
}

// TestSessionUniverseColumnClosureIn984 —— 迁移侧列闭合。
//
// 逐个 CREATE MATERIALIZED VIEW 块检查：
//   - 含 FROM sessions 的块 ⇒ 必须走去重子查询（不得回退成直接 JOIN）；
//   - 块内所有 `s.<col>` 引用 ⊆ 该块去重子查询的 SELECT 列表。
//
// ★ 2026-11-24 P1：真源从 994 换成 **984**（它重建四个物化视图 ⇒ 现行定义
//   在 984）。判据本身逐字未变。
func TestSessionUniverseColumnClosureIn984(t *testing.T) {
	mig := stripSQLComments(readSessionUniverseMigration(t))

	const marker = "CREATE MATERIALIZED VIEW"
	idxs := []int{}
	for i := 0; ; {
		j := strings.Index(mig[i:], marker)
		if j < 0 {
			break
		}
		idxs = append(idxs, i+j)
		i = i + j + len(marker)
	}
	if len(idxs) == 0 {
		t.Fatal("984 里没找到 CREATE MATERIALIZED VIEW 块")
	}

	checked := 0
	for i, start := range idxs {
		end := len(mig)
		if i+1 < len(idxs) {
			end = idxs[i+1]
		}
		block := mig[start:end]

		// 请求级视图（model_usage_aggregates）不 JOIN sessions ⇒ 不适用。
		if !strings.Contains(block, "FROM sessions") {
			continue
		}
		checked++
		name := blockName(block)

		cols, ok := dedupSubqueryColumns(block)
		if !ok {
			t.Errorf("%s: JOIN sessions 没有走去重子查询（缺 DISTINCT ON）——"+
				"直接 JOIN 会因 sessions 按天分区而一对多放大（D2 根因）", name)
			continue
		}
		assertColumnClosure(t, name, block, cols)
	}
	if checked == 0 {
		t.Fatal("984 里没有任何 JOIN sessions 的视图块——守卫失效")
	}
	if checked != 3 {
		t.Errorf("984 里 JOIN sessions 的视图块数 = %d，期望 3（user/project/agent）", checked)
	}
}

// blockName 从块首行取视图名，用于失败信息。
func blockName(block string) string {
	ln := strings.SplitN(block, "\n", 2)[0]
	if i := strings.Index(ln, "public."); i >= 0 {
		rest := ln[i+len("public."):]
		if j := strings.IndexAny(rest, " \t"); j > 0 {
			return "public." + rest[:j]
		}
	}
	return strings.TrimSpace(ln)
}

// TestSessionUniverseColumnClosureInGo —— Go 侧列闭合。
//
// Go 侧产物 = SessionUniverseJoin（子查询列清单）+ SessionUniverseWhere
// （引用面）。两者合起来必须闭合。
func TestSessionUniverseColumnClosureInGo(t *testing.T) {
	join := SessionUniverseJoin("ss", "s", "sd")
	where := SessionUniverseWhere("s", "sd", "ss")

	cols, ok := dedupSubqueryColumns(join)
	if !ok {
		t.Fatalf("SessionUniverseJoin 没有去重子查询:\n%s", join)
	}
	assertColumnClosure(t, "Go 侧会话宇宙", join+"\n"+where, cols)
}

// TestSessionUniverseClosureGuardCanFail —— 量具自证。
//
// 守卫的价值取决于「它能不能判红」。这里用一段**故意漏列**的 SQL 喂给
// 同一套判定逻辑：漏掉 s.owner_user ⇒ 必须被判红。若哪天正则或解析写钝了、
// 变成永远绿，本测试会先红。
func TestSessionUniverseClosureGuardCanFail(t *testing.T) {
	cols, ok := dedupSubqueryColumns("SELECT DISTINCT ON (session_id, tenant_id) session_id, tenant_id, is_probe FROM sessions")
	if !ok {
		t.Fatal("去重子查询解析失败")
	}
	refs := sessionColumnRefs("WHERE NULLIF(COALESCE(s.owner_user, sd.api_key_owner_user), '') IS NOT NULL")
	if len(refs) == 0 {
		t.Fatal("引用抽取失效")
	}
	missing := false
	for ref := range refs {
		if !cols[ref] {
			missing = true
		}
	}
	if !missing {
		t.Fatal("漏列样例没被判红——闭合守卫的判定逻辑已失效")
	}
}

// TestSessionUniverse994FileExists —— 守卫依赖 994/984 迁移文件存在；
// 文件被改名/移走时立刻红，而不是静默跳过。
func TestSessionUniverse994FileExists(t *testing.T) {
	for _, p := range []string{sessionUniverseBaselineFile, sessionUniverseMigrationFile} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("会话宇宙迁移文件不在预期路径 %s: %v", p, err)
		}
	}
}
