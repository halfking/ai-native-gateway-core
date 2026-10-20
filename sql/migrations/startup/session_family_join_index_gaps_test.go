package startup

// session_family_join_index_gaps_test.go —— 迁移 888 的静态契约门（审计 G-7）。
//
// # 缺陷本体（2026-10-20 会话族审计 G-7）
//
// 轮次列表 / 详情两处 join（admin/session_turns_v2.go:132-136 与 :304-310）：
//
//	FROM public.session_turns_with_current_month t
//	LEFT JOIN public.session_bodies_unified b
//	  ON b.tenant_id = t.tenant_id AND b.session_id = t.session_id
//	 AND b.turn_no = t.turn_no AND b.request_id = t.request_id
//
// 四个等值条件里 **request_id 是唯一没有索引承重的那一个**（父表与 hot 表的
// 索引都不含它）⇒ 每次 join 都要回堆做一次 recheck；而且 hot 臂那条索引的
// **列序与父表不同**（hot 是 (session_id, turn_no, tenant_id)）。
//
// 另外 sessions 表缺 (tenant_id, session_id, partition_date DESC)，而
// 「按 (tenant_id, session_id) 找会话」的读方有四处。
//
// # 这道门钉什么
//
// ★ **每条新索引都必须挂在一条具体的查询上** —— 一条没有查询承重的索引
//   是缺陷（本仓自己的先例：877 删掉了一条没人用的部分索引，而不是留着）。
//   ⇒ 每条索引都断言两件事：
//     (i) 迁移里的**索引列清单与列序**（逐字解析，不是 grep 关键词）；
//     (ii) **那条具体查询的谓词**真的引用了这些列，而且**顺序一致**。
//   只断言 (i) 会让「索引建在没人查的列上」全绿；只断言 (ii) 会让
//   「索引列序与查询不匹配」全绿（列序错了 planner 就用不上）。
//
// ★ 迁移必须写明「为什么是建新 + 删旧」—— PostgreSQL **不支持**
//   ALTER INDEX … ADD COLUMN，没有这句理由，下一个人会以为可以就地改。
//
// ★ 迁移必须写明「删掉的那两条旧索引各自的代价口径」—— 476 那条是新索引
//   的严格超集（零代价），614 那条**列序不同**（不是前缀关系，删除依赖
//   「无 session_id 单列读方」的普查结论）⇒ 两者不能被同一句话打发。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const migration888 = "888_session_family_join_index_gaps.sql"

// btreeIndexRe 抓 `CREATE INDEX [IF NOT EXISTS] <name> ON [ONLY] <table>
// USING btree (<cols>)`。
var btreeIndexRe = regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(?:([a-z0-9_]+)|%I)\s+ON\s+(?:ONLY\s+)?public\.([a-z0-9_]+)\s+USING\s+btree\s*\(([^)]*)\)`)

// btreeIndexFormatRe 抓**生成器形态**的 btree 索引：
//
//	format('CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.<table>
//	        USING btree (<cols>)', '<name>')
//
// 索引名在 format 的**参数**里（不在 SQL 串里）⇒ 正则 A 抓不到 ⇒ 必须单列
// 一条形态。两条形态合起来才覆盖迁移里两种写法（父表壳是内联的、逐分区
// 索引是生成器的）。
var btreeIndexFormatRe = regexp.MustCompile(`(?is)format\(\s*'CREATE\s+INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+%I\s+ON\s+public\.([a-z0-9_]+)\s+USING\s+btree\s*\(([^)]*)\)'\s*,\s*'([a-z0-9_]+)'`)

// joinEqRe 抓 join 谓词里的 `b.<col> = t.<col>`（按出现顺序）。
var joinEqRe = regexp.MustCompile(`b\.([a-z_]+)\s*=\s*t\.([a-z_]+)`)

// indexColumns 解析索引定义里的列名（去掉 DESC/ASC 与表达式包装）。
func indexColumns(def string) []string {
	cols := []string{}
	for _, raw := range strings.Split(def, ",") {
		c := strings.TrimSpace(raw)
		c = strings.TrimSuffix(strings.TrimSuffix(c, " DESC"), " ASC")
		c = strings.Trim(c, "\"'")
		if c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// readRepoFile 读仓库内相对本包（sql/migrations/startup）的文件。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// migration888Indexes 返回迁移里定义的全部 btree 索引：name → (table, cols)。
func migration888Indexes(t *testing.T) map[string]struct {
	Table string
	Cols  []string
} {
	t.Helper()
	body := stripSQLComments(readRepoFile(t, migration888))
	out := map[string]struct {
		Table string
		Cols  []string
	}{}
	for _, m := range btreeIndexRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = struct {
			Table string
			Cols  []string
		}{m[2], indexColumns(m[3])}
	}
	// 生成器形态：索引名在 format 的参数里。
	for _, m := range btreeIndexFormatRe.FindAllStringSubmatch(body, -1) {
		out[m[3]] = struct {
			Table string
			Cols  []string
		}{m[1], indexColumns(m[2])}
	}
	if len(out) == 0 {
		t.Fatalf("%s 里一条 btree CREATE INDEX 都没解析到 —— 判据的解析器失效了（迁移形态漂移？）", migration888)
	}
	return out
}

// TestMigration888BodiesJoinIndexMatchesTheJoinPredicate 是 (i)+(ii)：
// 父表与 hot 表两条索引的列清单必须**与 join 谓词逐列同序**。
func TestMigration888BodiesJoinIndexMatchesTheJoinPredicate(t *testing.T) {
	idx := migration888Indexes(t)

	// join 谓词的真实形态取自 admin/session_turns_v2.go（列表 + 详情两处）。
	joinSrc := readRepoFile(t, "../../../admin/session_turns_v2.go")
	matches := joinEqRe.FindAllStringSubmatch(joinSrc, -1)
	if len(matches) == 0 {
		t.Fatalf("admin/session_turns_v2.go 里没解析到 join 等值条件 —— " +
			"判据的尺子坏了（join 写法漂移？）")
	}
	// 取第一处 join 的列序（两处应同序；下面的「两处同序」单独断言）。
	var wantCols []string
	for _, m := range matches {
		if m[1] != m[2] {
			continue
		}
		if wantCols == nil {
			wantCols = []string{m[1]}
			continue
		}
		if wantCols[len(wantCols)-1] != m[1] {
			wantCols = append(wantCols, m[1])
		}
	}
	if len(wantCols) != 4 {
		t.Fatalf("join 谓词解析出 %d 个等值列（%v）—— 期望 4 个 "+
			"(tenant_id, session_id, turn_no, request_id)", len(wantCols), wantCols)
	}
	for _, c := range []string{"tenant_id", "session_id", "turn_no", "request_id"} {
		found := false
		for _, w := range wantCols {
			if w == c {
				found = true
			}
		}
		if !found {
			t.Errorf("join 谓词里没有 %s 等值条件（解析出 %v）—— "+
				"索引的列清单必须与 join 实际用到的列一致", c, wantCols)
		}
	}

	for _, name := range []string{
		"idx_session_bodies_tenant_session_turn_request",
		"idx_session_bodies_hot_tenant_session_turn_request",
	} {
		got, ok := idx[name]
		if !ok {
			t.Errorf("%s 里没有索引 %s —— 这条 join 没有索引承重（%v）", migration888, name, keys(idx))
			continue
		}
		if len(got.Cols) != len(wantCols) {
			t.Errorf("索引 %s 的列数 %d（%v）与 join 的等值列数 %d（%v）不符",
				name, len(got.Cols), got.Cols, len(wantCols), wantCols)
			continue
		}
		for i := range wantCols {
			if got.Cols[i] != wantCols[i] {
				t.Errorf("索引 %s 的第 %d 列是 %q，join 谓词的第 %d 个等值列是 %q —— "+
					"列序不一致 ⇒ planner 用不上这条索引（它要求等值列与索引前缀同序）",
					name, i+1, got.Cols[i], i+1, wantCols[i])
			}
		}
	}
}

// TestMigration888HotAndParentColumnOrderAreIdentical 钉住缺口②：
// hot 臂与父表的索引列清单必须**逐字相同**。
//
// 这不是审美：列序不同的两条索引 ⇒ 同一个 join 在两条臂上走不同路径，
// 也就是 888 要修的那个缺口本身。
func TestMigration888HotAndParentColumnOrderAreIdentical(t *testing.T) {
	idx := migration888Indexes(t)
	parent, okP := idx["idx_session_bodies_tenant_session_turn_request"]
	hot, okH := idx["idx_session_bodies_hot_tenant_session_turn_request"]
	if !okP || !okH {
		t.Fatalf("迁移里缺父表或 hot 侧的 join 索引（parent=%v hot=%v）", okP, okH)
	}
	if parent.Table != "session_bodies" || hot.Table != "session_bodies_hot" {
		t.Fatalf("索引建错了表：parent=%s hot=%s", parent.Table, hot.Table)
	}
	if strings.Join(parent.Cols, ",") != strings.Join(hot.Cols, ",") {
		t.Errorf("父表与 hot 的索引列序不一致：\n父表：%v\nhot ：%v\n"+
			"888 的缺口②就是「两条臂列序不同 ⇒ 同一个 join 走不同路径」",
			parent.Cols, hot.Cols)
	}
}

// TestMigration888SessionsIndexHasFourQuerySites 钉住 sessions 索引的承重查询。
//
// 四处读方（都带 tenant_id + session_id 两个等值条件）：
//
//	· domains/session/v2/session_aggregator.go（两处）
//	· internal/titlestore/store.go（最新分区投影）
//	· admin/turns_sessions.go（最新分区去重谓词）
func TestMigration888SessionsIndexHasFourQuerySites(t *testing.T) {
	idx := migration888Indexes(t)
	got, ok := idx["idx_sessions_tenant_session_partition_date"]
	if !ok {
		t.Fatalf("%s 里没有索引 idx_sessions_tenant_session_partition_date（%v）", migration888, keys(idx))
	}
	want := []string{"tenant_id", "session_id", "partition_date"}
	if strings.Join(got.Cols, ",") != strings.Join(want, ",") {
		t.Errorf("sessions 索引的列清单 = %v，期望 %v", got.Cols, want)
	}

	sites := []struct {
		path string
		what string
	}{
		{"../../../domains/session/v2/session_aggregator.go", "按 (tenant_id, session_id) 读会话摘要"},
		{"../../../internal/titlestore/store.go", "写侧投影最新分区行"},
		{"../../../admin/turns_sessions.go", "读侧「一会话一行」去重谓词"},
	}
	for _, s := range sites {
		body := readRepoFile(t, s.path)
		// ★ 允许别名形态（admin/turns_sessions.go 写的是
		//   x.tenant_id = s.tenant_id / x.session_id = s.session_id）——
		//   早先只认 `tenant_id = $n` 形态，把这一处读方误判成「没有承重」。
		hasTenant := regexp.MustCompile(`tenant_id\s*=\s*([a-z_]+\.)?(\$\d+|[a-z_]+)`).MatchString(body)
		hasSession := regexp.MustCompile(`session_id\s*=\s*([a-z_]+\.)?(\$\d+|[a-z_]+)`).MatchString(body)
		if !hasTenant || !hasSession {
			t.Errorf("%s（%s）里没有同时按 tenant_id 与 session_id 等值过滤的查询 —— "+
				"索引 idx_sessions_tenant_session_partition_date 在这个文件里没有承重读方",
				s.path, s.what)
		}
	}
	// 聚合器那两处读方必须**都**在（少一处就少一个承重点）。
	agg := readRepoFile(t, "../../../domains/session/v2/session_aggregator.go")
	if n := len(regexp.MustCompile(`tenant_id\s*=\s*\$\d+\s+AND\s+session_id\s*=\s*\$\d+`).FindAllString(agg, -1)); n < 2 {
		t.Errorf("domains/session/v2/session_aggregator.go 里只找到 %d 处 "+
			"(tenant_id=$n AND session_id=$n) 形态的读方（期望 ≥2）", n)
	}
}

// TestMigration888DropsTheReplacedIndexes 钉住「建新 + 删旧」的两端。
//
// 删的那两条各有不同口径：476 那条是新索引的**严格超集**（零代价），
// 614 那条**列序不同**（删除依赖「无 session_id 单列读方」的普查结论）。
// ⇒ 迁移必须点名它们。
func TestMigration888DropsTheReplacedIndexes(t *testing.T) {
	body := stripSQLComments(readRepoFile(t, migration888))
	for _, name := range []string{
		"idx_session_bodies_tenant_session_turn",
		"idx_session_bodies_hot_lookup",
	} {
		re := regexp.MustCompile(`(?i)DROP\s+INDEX(?:\s+CONCURRENTLY)?\s+(?:IF\s+EXISTS\s+)?public\.` + regexp.QuoteMeta(name) + `\b`)
		if !re.MatchString(body) {
			t.Errorf("%s 没有点名要删的旧索引 %s（可执行语句里没有对应的 DROP INDEX）—— "+
				"「建新 + 删旧」只建不删等于索引翻倍", migration888, name)
		}
	}
	// ⚠ 不能把 614 那条的名字误当作新索引的一部分（它是新名字的前缀子串）。
	if regexp.MustCompile(`CREATE\s+INDEX[^;]*idx_session_bodies_hot_lookup`).MatchString(body) {
		t.Errorf("%s 里又建了一条 idx_session_bodies_hot_lookup —— "+
			"被替换的旧索引不该被重建", migration888)
	}
}

// TestMigration888StatesTheAlterIndexLimitation 钉住「为什么建新 + 删旧」。
//
// PostgreSQL 不支持 ALTER INDEX … ADD COLUMN（键列集合在建索引时固定）。
// 没有这句理由，下一个人会以为可以就地改列。
func TestMigration888StatesTheAlterIndexLimitation(t *testing.T) {
	body := readRepoFile(t, migration888)
	if !strings.Contains(body, "ALTER INDEX") {
		t.Errorf("%s 没提 ALTER INDEX —— 「为什么是建新 + 删旧」的理由没写下来", migration888)
	}
	// 诚实口径：PG 11 之后 ADD COLUMN 只支持 INCLUDE，**不支持**把已有列
	// 塞进键列 ⇒ 迁移的理由必须落到「键列不可后加」上。
	if !strings.Contains(body, "ADD COLUMN") {
		t.Errorf("%s 没说明「PostgreSQL 不支持 ALTER INDEX … ADD COLUMN」这条硬约束 —— "+
			"理由没写下来，下一个人会以为可以就地改列", migration888)
	}
}

// keys 便于报错时列出实际解析到的索引名。
func keys(m map[string]struct {
	Table string
	Cols  []string
}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
