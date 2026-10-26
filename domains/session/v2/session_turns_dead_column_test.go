//go:build !integration

package v2

// session_turns_dead_column_test.go —— G-6「`session_turns` 无死列」的**全集差集门**。
//
// # 这道门在补哪个缺口
//
// 审计方案 §3.3 的 G-6 判据原文是：「`session_turns` 写方列清单与实际用途一致（**无死列**）」。
// 上一轮把它判成 ✅，依据是两道门：
//
//	· domains/session/v2/turn_tools_test.go:68（钉 `tools` 一列被写入）
//	· sql/migrations/startup/session_turns_tools_population_test.go:112,138（钉迁移 889）
//
// ⚠ 但那两道门**只覆盖 `tools` 一列**。「无死列」是**全称量词**：
// 它说的是「108 列里没有一列是死的」，而门只回答了「`tools` 不是死的」。
// ⇒ 下一列变死（加了列忘了写、或把某个写路径删了）**没有任何门会发现**。
//
// 本文件补的就是这个**全集**差集：
//
//	库面列集合（真库 information_schema）
//	  ∖ （主写路径的 INSERT 列清单 ∪ 已登记的 UPDATE 路径列 ∪ 已登记的已知死列）
//	  = ∅
//
// # 三条纪律（沿用 AGENTS.md 判据纪律）
//
// 1. **「死列」的定义必须可复核**：无 DEFAULT ∧ 非生成列 ∧ 非 identity 列 ∧
//    不在任何写方集合里。有 DEFAULT 的列由数据库自己填 ⇒ 不是死列；
//    生成列由数据库自己算 ⇒ 不是死列。
// 2. **前提自证**：表在不在、两面列集合同不同、有没有触发器、锚点是否唯一
//    —— 前提不成立 ⇒ t.Fatalf，**不是** t.Skip、不是「安静地通过」。
//    「没被检查」和「检查通过」必须长得不一样。
// 3. **登记册必须自证**：每条 UPDATE 路径登记都带 file:line，
//    门会当场核对该行确实含该列名、且该文件确实提到这两张表之一
//    ⇒ 「随手登记一个不存在的写方」骗不过这道门。
//
// # 为什么不用「填充率」当判据
//
// 直觉做法是「库里这一列全是 NULL ⇒ 它是死列」。**在开发库上这条判据不可用**：
// 本机库 `session_turns` 有 1540 行，其中 **24 列**既无默认值又全为 NULL
// —— 那些是**近期新增、还没有数据**的列（tools / host / tags / client_protocol …），
// 不是死列。按填充率判据，这道门会一上来就红 24 条 ⇒ 变成「恒红门」⇒ 没人看。
//
// ⇒ 因此判据改用**写方集合**（静态可复核），不用填充率（数据相关）。
// 填充率只能当**旁证**（生产 252 实测 `upstream_protocol` 非空 0 行）。
//
// # 怎么跑
//
// 需要一个**已迁移**的 PG 库。仓内脚本（克隆生产形态库到一次性 itgate_*，退出即删）：
//
//	bash scripts/audit/verify-session-family-p0-realdb.sh
//
// 也可手工跑：
//
//	TEST_DATABASE_URL='postgres://llm_gateway@127.0.0.1:5432/itgate_x' \
//	  go test ./domains/session/v2/ -run 'DeadColumn' -v
//
// 另外两条腿（登记册自证 + 未登记写路径扫描）**不需要库** ⇒ 普通 CI 形态可跑。

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 判据来源（一）：库面
// ---------------------------------------------------------------------------

const (
	sessionTurnsParentTable = "public.session_turns"
	sessionTurnsHotTable    = "public.session_turns_hot"
)

// ---------------------------------------------------------------------------
// 判据来源（二）：写方
// ---------------------------------------------------------------------------

// sessionTurnsWritePath 是「**非 INSERT 写路径**」的登记条目。
//
// 为什么需要登记：主写路径（turn_writer.go 的 INSERT）能**解析**出来，
// 但其余写路径是散落在多个包里的 UPDATE（两张表各一条、有的表名走变量）。
// 静态扫描器看不见「表名是变量」的写法 ⇒ 只能登记。
// ⇒ 登记的代价是「可能腐坏」⇒ 门会自证每条登记（见 TestSessionTurnsWritePathRegistryHonest）。
type sessionTurnsWritePath struct {
	Column string
	File   string
	Line   int
	Note   string
}

// sessionTurnsWritePathRegistry 是会话族两条面上**所有非 INSERT 写路径**的登记册。
//
// ⚠ 登记 = 「这一列有活着的写方」。删掉一条登记 = 声称「这一列没人写」。
// ⇒ 门会核对 file:line ⇒ 编造条目会被当场抓到。
var sessionTurnsWritePathRegistry = []sessionTurnsWritePath{
	{
		Column: "aggregate_applied_at",
		File:   "domains/session/v2/session_aggregator.go",
		Line:   166,
		Note:   "聚合 claim：父面 UPDATE ... SET aggregate_applied_at = NOW()；同文件 :199 是 hot 面同列同写。",
	},
	{
		Column: "is_abandoned",
		File:   "internal/sessionv2mirror/abandoned_turn.go",
		Line:   213,
		Note: "废弃轮标记：UPDATE <表名变量> SET is_abandoned = TRUE，两面各一条；" +
			"表名是运行时变量（tbl）⇒ 静态扫描器看不见 ⇒ 必须登记。",
	},
	{
		Column: "trace_events",
		File:   "internal/trace/trace.go",
		Line:   494,
		Note: "trace 落库：两面各一条 UPDATE；该列同时也在主写路径的 INSERT 清单里（turn_writer.go:552）" +
			"⇒ 登记它是为了留痕「这条 UPDATE 是活的」，不是它靠登记才活。",
	},
}

// sessionTurnsKnownDeadColumn 是「**已知且已接受**的死列」登记（带原因 + 处置归属）。
//
// ⚠ 语义：登记 ≠ 「没问题」⇒ 登记 = 「**已知没人写**，且**处置需要属主拍板**」。
// 门的作用是让**新的**死列变红；已登记的那一条由审计文档与属主处置。
type sessionTurnsKnownDeadColumn struct {
	Column string
	Reason string
	// Disposition 说明「谁、怎么处置」。空 ⇒ 门红（登记必须写明处置归属）。
	Disposition string
}

// sessionTurnsKnownDeadColumns 当前已登记的已知死列。
//
// ★ 2026-10-21 实测发现（本轮 G-6 全集差集算出来的唯一一条）：
//
//	session_turns.upstream_protocol —— 迁移 640 加的列（父表 + hot 各一），**全仓无任何写方**：
//	  · Go 侧只有 telemetry 的 JSON 字段（domains/hooks/observability/telemetry/client.go:315）
//	    与一条注释（:1724，说的是 v1 宽表）；
//	  · v1 族的 scripts/audit/mirror_outbox_backfill.sql:68,104（v1 已随 890 退役）；
//	  · 3 份 v1 baseline/embeddata schema（deploy/sql/schemas/baseline/01-schema.sql:7360 等）；
//	  · admin/view_source_columns_contract.go:64 的列名清单（不是写方）。
//	· 迁移 **877 已经把它的索引 idx_session_turns_hot_upstream_protocol 当「死索引」删了**
//	  （sql/migrations/startup/migration_877_integration_test.go:112 原文：「复刻 640 建的那条
//	  **死索引**：upstream_protocol 恒 NULL ⇒ 谓词永不成立」）
//	  ⇒ **索引按死列处理过了，列本身没人处置** —— 这正是「只审索引、不审列」的漏法。
//	· 生产 252 实测：session_turns 1,061,207 行 / session_turns_hot 3,896 行，该列非空 **0**。
var sessionTurnsKnownDeadColumns = []sessionTurnsKnownDeadColumn{
	{
		Column: "upstream_protocol",
		Reason: "迁移 640 加的列（父表 + hot 各一），全仓无任何写方（只有 telemetry 的 JSON 字段、" +
			"v1 族已退役的 mirror 脚本、v1 baseline、admin 列名清单）；迁移 877 已把它的索引当「死索引」删除" +
			"⇒ 索引按死列处理过，列本身没人处置。",
		Disposition: "待属主拍板（二选一）：① 填充（写方补上游侧协议，与 client_protocol 对称）" +
			"② 删列（连带 640 的列注释与 877 的索引结论）；本门只登记，不代决。",
	},
}

// ---------------------------------------------------------------------------
// 腿 ①：真库差集（全集）
// ---------------------------------------------------------------------------

// TestSessionTurnsDeadColumnRealDB 是 G-6 全集差集的主腿。
//
// 判据：库面「无默认值且非生成」的列 ⊆ （主写路径 INSERT 列清单 ∪ 登记的 UPDATE 路径列
// ∪ 已登记的已知死列）。
//
// 任何**未登记**的列落在差集里 ⇒ 红。
func TestSessionTurnsDeadColumnRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx := context.Background()

	// ── 前提自证 ①：两张表都在，且列集合相同 ────────────────────────────
	// 形状相同是 promote_session_turns_hot_to_partition 的运行时契约
	// （迁移 707 起的列形状守卫 / 869 起的目录派生列清单）。
	// 两面不同形 ⇒ 父面的列集合不代表 hot 面 ⇒ 下面的差集只对一面成立
	// ⇒ **前提不成立 ⇒ Fatal**，不是「只算父面」。
	cols := map[string]map[string]columnMeta{}
	for _, tbl := range []string{sessionTurnsParentTable, sessionTurnsHotTable} {
		rows, err := pool.Query(ctx, `
			SELECT c.column_name,
			       c.column_default IS NULL,
			       c.is_generated,
			       c.is_identity
			  FROM information_schema.columns c
			  JOIN information_schema.tables t
			    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
			 WHERE c.table_schema = 'public'
			   AND c.table_name = $1
			   AND t.table_type = 'BASE TABLE'
			 ORDER BY c.ordinal_position
		`, strings.TrimPrefix(tbl, "public."))
		if err != nil {
			t.Fatalf("读 %s 的列集合失败：%v", tbl, err)
		}
		m := map[string]columnMeta{}
		for rows.Next() {
			var name string
			var noDefault bool
			// is_generated / is_identity 在 information_schema 里是 **varchar**
			// （'NEVER' / 'NO'），不是 boolean ⇒ 按字符串读再比较。
			var generated, identity string
			if err := rows.Scan(&name, &noDefault, &generated, &identity); err != nil {
				rows.Close()
				t.Fatalf("扫描 %s 的列集合失败：%v", tbl, err)
			}
			m[name] = columnMeta{
				noDefault:    noDefault,
				notGenerated: strings.EqualFold(generated, "NEVER"),
				notIdentity:  strings.EqualFold(identity, "NO"),
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("遍历 %s 的列集合失败：%v", tbl, err)
		}
		if len(m) == 0 {
			t.Fatalf("前提不成立：%s 在库里不存在（或不是基表）⇒ 差集无意义", tbl)
		}
		cols[tbl] = m
	}
	for name := range cols[sessionTurnsParentTable] {
		if _, ok := cols[sessionTurnsHotTable][name]; !ok {
			t.Fatalf("前提不成立：列 %q 只在 %s 上、不在 %s 上 ⇒ 两面不同形，差集口径失效",
				name, sessionTurnsParentTable, sessionTurnsHotTable)
		}
	}
	for name := range cols[sessionTurnsHotTable] {
		if _, ok := cols[sessionTurnsParentTable][name]; !ok {
			t.Fatalf("前提不成立：列 %q 只在 %s 上、不在 %s 上 ⇒ 两面不同形，差集口径失效",
				name, sessionTurnsHotTable, sessionTurnsParentTable)
		}
	}

	// ── 前提自证 ②：两张表上没有触发器 ─────────────────────────────────
	// 触发器会**隐式**写列（典型：BEFORE INSERT 填一列）⇒ 静态写方集合不完整
	// ⇒ 「差集 = 死列」这个推断的前提被破坏 ⇒ 必须 Fatal 并让人重新评估，
	// 而不是继续算出一份**漏了触发器写方**的差集。
	var triggerRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM pg_trigger t
		  JOIN pg_class c ON c.oid = t.tgrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE NOT t.tgisinternal
		   AND n.nspname = 'public'
		   AND c.relname IN ('session_turns', 'session_turns_hot')
	`).Scan(&triggerRows); err != nil {
		t.Fatalf("读触发器失败：%v", err)
	}
	if triggerRows != 0 {
		t.Fatalf("前提不成立：%s / %s 上有 %d 个非内部触发器 ⇒ 触发器会隐式写列，"+
			"静态写方集合不再完整 ⇒ 必须先评估触发器再谈死列",
			sessionTurnsParentTable, sessionTurnsHotTable, triggerRows)
	}

	// ── 前提自证 ③：写方 INSERT 清单解析成功（锚点恰好一次）────────────
	insertCols := sessionTurnsInsertColumns(t)
	if len(insertCols) == 0 {
		t.Fatalf("前提不成立：写方 INSERT 列清单解析出 0 列 ⇒ 差集会把全部列判成死列")
	}

	// ── 前提自证 ④：写方列清单 ⊆ 库面列集合 ────────────────────────────
	// 清单里的列在库里不存在 ⇒ 写路径本身会 42P01/42703 ⇒ 那是更严重的问题，
	// 不能混进「死列」里。
	var missing []string
	for _, c := range insertCols {
		if _, ok := cols[sessionTurnsParentTable][c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("写方 INSERT 列清单里的 %d 列在库里不存在 ⇒ 写路径会直接报错（不是死列，是更严重的问题）：%v",
			len(missing), missing)
	}

	// ── 差集 ────────────────────────────────────────────────────────────
	// 「死」的定义：无默认值（数据库不自己填）∧ 非生成列 ∧ 非 identity 列。
	dead := []string{}
	for name, meta := range cols[sessionTurnsParentTable] {
		if !meta.noDefault || !meta.notGenerated || !meta.notIdentity {
			continue // 有默认值 / 生成列 / identity ⇒ 数据库自己填 ⇒ 不是死列
		}
		if sessionTurnsInList(name, insertCols) {
			continue // 主写路径的 INSERT 列清单
		}
		if sessionTurnsInRegistry(name) {
			continue // 登记的 UPDATE 路径列
		}
		dead = append(dead, name)
	}
	sort.Strings(dead)

	known := map[string]sessionTurnsKnownDeadColumn{}
	for _, k := range sessionTurnsKnownDeadColumns {
		known[k.Column] = k
	}

	// 断言 ①：未登记的差集必须为空。
	var unregistered []string
	for _, c := range dead {
		if _, ok := known[c]; !ok {
			unregistered = append(unregistered, c)
		}
	}
	if len(unregistered) > 0 {
		t.Errorf("G-6 全集差集：%d 列没有写方且未登记为已知死列 ⇒ 「无死列」判据被违反：%v\n"+
			"  处置二选一：① 给它接上写方（写方 INSERT 列清单或一条 UPDATE 路径，并登记）\n"+
			"                ② 认定它是死列 ⇒ 加进 sessionTurnsKnownDeadColumns（带原因 + 处置归属）",
			len(unregistered), unregistered)
	}

	// 断言 ②：登记的已知死列必须**确实还是死列** ⇒ 否则登记册腐坏。
	for _, k := range sessionTurnsKnownDeadColumns {
		if _, ok := cols[sessionTurnsParentTable][k.Column]; !ok {
			t.Errorf("登记的已知死列 %q 在库里已经不存在 ⇒ 登记册腐坏（该列可能已被删）⇒ 请删掉这条登记",
				k.Column)
			continue
		}
		if !sessionTurnsInList(k.Column, dead) {
			t.Errorf("登记的已知死列 %q 现在**不是**死列了 ⇒ 登记册腐坏（它已有写方或已有默认值）\n"+
				"  ⇒ 请删掉这条登记，或把对应的写方登记进 sessionTurnsWritePathRegistry", k.Column)
		}
	}

	t.Logf("G-6 差集：库面 %d 列（其中无默认 %d 列）/ 写方 INSERT %d 列 / 登记 UPDATE %d 列 / "+
		"死列 %d 条（已登记 %d，未登记 %d）",
		len(cols[sessionTurnsParentTable]), sessionTurnsCountNoDefault(cols[sessionTurnsParentTable]),
		len(insertCols), len(sessionTurnsWritePathRegistry), len(dead), len(dead)-len(unregistered),
		len(unregistered))
}

// columnMeta 是 information_schema 里一列的三个判据位。
type columnMeta struct {
	noDefault    bool
	notGenerated bool
	notIdentity  bool
}

func sessionTurnsCountNoDefault(m map[string]columnMeta) int {
	n := 0
	for _, meta := range m {
		if meta.noDefault {
			n++
		}
	}
	return n
}

func sessionTurnsInList(name string, list []string) bool {
	for _, c := range list {
		if c == name {
			return true
		}
	}
	return false
}

func sessionTurnsInRegistry(name string) bool {
	for _, w := range sessionTurnsWritePathRegistry {
		if w.Column == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 腿 ②：登记册自证（不需要库）
// ---------------------------------------------------------------------------

// TestSessionTurnsWritePathRegistryHonest 核对登记册**没有腐坏**。
//
// 三条：
//  1. 主写路径的 INSERT 列清单解析锚点**恰好命中一次**（0 次 = 写方改了形状；
//     ≥2 次 = 解析器有歧义 ⇒ 差集不可信）。
//  2. 每条 UPDATE 路径登记的 file:line **确实存在**，且**那一行确实含该列名**，
//     且该文件**确实提到**两张表之一 ⇒ 编造的登记骗不过这道门。
//  3. 每条已知死列登记都写了原因与处置归属 ⇒ 「登记 = 已知且有人管」。
func TestSessionTurnsWritePathRegistryHonest(t *testing.T) {
	root := sessionTurnsRepoRoot(t)

	// ① 锚点唯一性（在 sessionTurnsInsertColumns 内部已 Fatal，这里只补一句可读性）。
	insertCols := sessionTurnsInsertColumns(t)
	if len(insertCols) != 102 {
		t.Logf("提示：写方 INSERT 列清单当前 %d 列（历史值 102）⇒ 写方形状变了，"+
			"请同步核对本文件的注释与审计文档", len(insertCols))
	}
	seen := map[string]bool{}
	for _, c := range insertCols {
		if seen[c] {
			t.Errorf("写方 INSERT 列清单里有重复列 %q ⇒ 解析器有歧义 ⇒ 差集不可信", c)
		}
		seen[c] = true
	}

	// ② 登记册逐条自证。
	for _, w := range sessionTurnsWritePathRegistry {
		if w.Column == "" || w.File == "" || w.Line <= 0 {
			t.Errorf("登记条目不完整（列/文件/行号必填）：%+v", w)
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(w.File))
		text, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("登记条目 %q 指向的文件读不到：%s（%v）", w.Column, w.File, err)
			continue
		}
		lines := strings.Split(string(text), "\n")
		if w.Line > len(lines) {
			t.Errorf("登记条目 %q 的行号 %d 超出文件 %s 的行数 %d", w.Column, w.Line, w.File, len(lines))
			continue
		}
		line := lines[w.Line-1]
		if !strings.Contains(line, w.Column) {
			t.Errorf("登记条目 %q 指向 %s:%d，但那一行不含该列名：\n  %s",
				w.Column, w.File, w.Line, strings.TrimSpace(line))
		}
		if !strings.Contains(string(text), "session_turns") {
			t.Errorf("登记条目 %q 指向的 %s 里根本没提到 session_turns ⇒ 它不是这两张表的写方",
				w.Column, w.File)
		}
	}

	// ③ 已知死列登记必须有原因与处置归属。
	for _, k := range sessionTurnsKnownDeadColumns {
		if strings.TrimSpace(k.Reason) == "" {
			t.Errorf("已知死列 %q 没写原因 ⇒ 「登记」必须说明为什么它没有写方", k.Column)
		}
		if strings.TrimSpace(k.Disposition) == "" {
			t.Errorf("已知死列 %q 没写处置归属 ⇒ 「登记」必须说明谁、怎么处置", k.Column)
		}
	}
}

// ---------------------------------------------------------------------------
// 腿 ③：未登记写路径扫描（不需要库）
// ---------------------------------------------------------------------------

// TestSessionTurnsNoUnregisteredWritePath 扫仓内 .go 文件，找出「针对这两张表、
// 但没被登记」的写路径。
//
// 为什么需要这道腿：登记册是人写的 ⇒ 人可能**忘了登记**新写的 UPDATE 路径。
// 忘了 ⇒ 该列会被差集判成死列（**假红**）⇒ 开发者会随手把它加进 allowlist
// ⇒ 于是 allowlist 变成「让门变绿的地方」⇒ **门失去牙**。
//
// ⇒ 这道腿反过来守：**只允许通过「写方集合」或「已知死列登记」两条路解释一列**。
// 扫描器看不懂的写法（表名是变量）会漏 —— 那是登记册要补的洞（见
// sessionTurnsWritePathRegistry 里 is_abandoned 那条）。
func TestSessionTurnsNoUnregisteredWritePath(t *testing.T) {
	root := sessionTurnsRepoRoot(t)

	covered := map[string]string{}
	for _, c := range sessionTurnsInsertColumns(t) {
		covered[c] = "主写路径 INSERT 列清单"
	}
	for _, w := range sessionTurnsWritePathRegistry {
		covered[w.Column] = fmt.Sprintf("登记的 UPDATE 路径 %s:%d", w.File, w.Line)
	}
	for _, k := range sessionTurnsKnownDeadColumns {
		covered[k.Column] = "已登记的已知死列"
	}

	found := map[string][]string{} // 列 -> 来源
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		for col, line := range sessionTurnsWrittenColumnsInGoFile(t, path) {
			if _, ok := covered[col]; ok {
				continue
			}
			found[col] = append(found[col], fmt.Sprintf("%s:%d", filepath.ToSlash(rel), line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓内 .go 文件失败：%v", err)
	}

	if len(found) > 0 {
		cols := make([]string, 0, len(found))
		for c := range found {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		var b strings.Builder
		for _, c := range cols {
			fmt.Fprintf(&b, "\n  %s ← %s", c, strings.Join(found[c], ", "))
		}
		t.Errorf("扫到 %d 个「针对 session_turns / session_turns_hot、但既不在写方集合、"+
			"也不在已知死列登记里」的列：%s\n"+
			"  ⇒ 这说明写方集合腐坏了：要么把它登记进 sessionTurnsWritePathRegistry（带 file:line），"+
			"要么它其实是死列 ⇒ 加进 sessionTurnsKnownDeadColumns（带原因 + 处置归属）。",
			len(cols), b.String())
	}
}

// ---------------------------------------------------------------------------
// 解析器
// ---------------------------------------------------------------------------

// sessionTurnsInsertRe 匹配 INSERT INTO <两张表>（。
var sessionTurnsInsertRe = regexp.MustCompile(`(?i)INSERT\s+INTO\s+(?:public\.)?(session_turns_hot|session_turns)\s*\(`)

// sessionTurnsUpdateRe 匹配 UPDATE <两张表>（表名必须是字面量 ⇒ 变量表名扫不到，
// 那类写法登记在 sessionTurnsWritePathRegistry 里）。
var sessionTurnsUpdateRe = regexp.MustCompile(`(?i)UPDATE\s+(?:public\.)?(session_turns_hot|session_turns)\b`)

// sessionTurnsAssignRe 匹配 SET 子句里的「列 =」。
var sessionTurnsAssignRe = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s*=`)

// sessionTurnsDynamicUpdateRe 匹配「表名是运行时变量」的 UPDATE。
//
// ★ 为什么必须单独处理：`internal/sessionv2mirror/abandoned_turn.go` 写的是
// `UPDATE `+tbl+` SET is_abandoned = TRUE`（tbl ∈ {hot, parent}）。
// 严格扫描器看不见它 ⇒ 它的写方**只能靠登记册**覆盖 ⇒
// 一旦登记册漏了它，差集门会**假红**，开发者就会把这一列塞进 allowlist
// ⇒ allowlist 变成「让门变绿的地方」⇒ **门失去牙**。
//
// ⇒ 这里补一条**保守归属**：动态表名的 UPDATE，若**同一文件里、上下 20 行内**
// 出现这两张表的字面量，就认定它写的是这两张表。
// 判定仍然保守（必须邻近出现字面量），宁可漏也不误报。
var sessionTurnsDynamicUpdateRe = regexp.MustCompile("(?i)UPDATE\\s+(?:public\\.)?(?:<dynamic>|`)")

// sessionTurnsDynamicInsertRe 匹配「表名是运行时变量」的 INSERT。
var sessionTurnsDynamicInsertRe = regexp.MustCompile("(?i)INSERT\\s+INTO\\s+(?:public\\.)?<dynamic>\\s*\\(")

// sessionTurnsFormatVerbRe 匹配 fmt 的 %s/%v 占位符。
var sessionTurnsFormatVerbRe = regexp.MustCompile(`%[sv]`)

// sessionTurnsIdentRe 判定一个标识符是不是列名。
var sessionTurnsIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// sessionTurnsInsertColumns 解析主写路径的 INSERT 列清单。
//
// 为什么不把清单**硬编码**在门里：清单会漂移，而漂移是静默的
// （多一列少一列都编译得过）。⇒ 门必须**从源里解析**，
// 源一改，门立刻跟着变 ⇒ 这才是「写方列清单」的真实定义。
func sessionTurnsInsertColumns(t *testing.T) []string {
	t.Helper()
	root := sessionTurnsRepoRoot(t)
	path := filepath.Join(root, "domains", "session", "v2", "turn_writer.go")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读写方源文件失败：%s（%v）", path, err)
	}
	src := string(text)
	const anchor = "INSERT INTO public.session_turns_hot ("
	if n := strings.Count(src, anchor); n != 1 {
		t.Fatalf("锚点 %q 在 %s 里命中 %d 次（要求恰好 1 次）⇒ 解析口径失效，"+
			"门既不能证明也不能证伪", anchor, path, n)
	}
	i := strings.Index(src, anchor)
	rest := src[i+len(anchor):]
	j := strings.Index(rest, ")")
	if j < 0 {
		t.Fatalf("锚点后面找不到 ')' ⇒ 列清单边界失效")
	}
	list := rest[:j]
	// 列清单里不该有括号，但保险起见按逗号切并逐个校验标识符。
	cols := []string{}
	for _, part := range strings.Split(list, ",") {
		col := strings.TrimSpace(part)
		if col == "" {
			continue
		}
		if !sessionTurnsIdentRe.MatchString(col) {
			t.Fatalf("列清单里有非标识符的片段 %q ⇒ 解析口径失效", col)
		}
		cols = append(cols, col)
	}
	return cols
}

// sessionTurnsRepoRoot 从当前工作目录往上找 go.mod 找到仓库根。
func sessionTurnsRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败：%v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("从工作目录往上找不到 go.mod ⇒ 定位不到仓库根")
	return ""
}

// sessionTurnsWrittenColumnsInGoFile 返回该 .go 文件里「针对两张表」的语句写到的列
// （列 -> 行号）。
//
// ★ 只扫**字符串字面量**（用 go/ast 走一遍）：注释里的 SQL 不是写路径，
// 扫注释 ⇒ 假红。这条纪律来自 §13.10 轮的「断言 SQL 文本前必须剥注释」。
func sessionTurnsWrittenColumnsInGoFile(t *testing.T, path string) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败（门自己读不懂源 ⇒ 扫描面不可信）：%v", path, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", path, err)
	}
	fileLines := strings.Split(string(raw), "\n")

	out := map[string]int{}
	collect := func(sql string, baseLine int) {
		strict, dynamic := sessionTurnsColumnsInSQL(sql)
		for col, off := range strict {
			line := baseLine + strings.Count(sql[:off], "\n")
			if prev, ok := out[col]; !ok || line < prev {
				out[col] = line
			}
		}
		// 动态表名（`UPDATE `+tbl+` …`）：只有在**同文件邻近**出现这两张表的
		// 字面量时才归属 —— 保守归属，宁可漏也不误报。
		for col, off := range dynamic {
			line := baseLine + strings.Count(sql[:off], "\n")
			if !sessionTurnsNearTableLiteral(fileLines, line) {
				continue
			}
			if prev, ok := out[col]; !ok || line < prev {
				out[col] = line
			}
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BinaryExpr:
			// ★ Go 字符串拼接：一条 SQL 会被拆成多个字面量
			//   （`UPDATE ` + tbl + ` SET x = 1`）。**只扫单个字面量会漏掉它**
			//   ⇒ 这就是「变量表名」盲区的真正成因（已实测：漏）。
			if v.Op != token.ADD {
				return true
			}
			s, ok := sessionTurnsConcatString(v)
			if !ok {
				return true
			}
			// 消费条件：文本里出现目标表，**或**出现「动态表名」标记。
			// ⚠ 只判「出现目标表」会漏掉 `UPDATE `+tbl+` …`：
			//    那条拼接文本里**没有**表名字面量（表名就是那个变量）⇒
			//    不消费子树 ⇒ 子字面量里的 SET 子句又被单独扫一遍（而单独一个字面量
			//    里没有 UPDATE 关键字 ⇒ 什么也抽不到）⇒ **整条路径静默丢失**。
			lower := strings.ToLower(s)
			if !strings.Contains(lower, "session_turns") &&
				!sessionTurnsDynamicUpdateRe.MatchString(s) &&
				!sessionTurnsDynamicInsertRe.MatchString(s) {
				return true // 与本门无关 ⇒ 不消费子树，继续往下走
			}
			collect(s, fset.Position(v.Pos()).Line)
			return false // 已消费整条拼接 ⇒ 不再单独扫子字面量
		case *ast.CallExpr:
			// fmt.Sprintf("UPDATE public.%s SET x = 1", tbl) ⇒ 占位符换成 <dynamic>。
			if s, ok := sessionTurnsFormatString(v); ok &&
				(strings.Contains(strings.ToLower(s), "session_turns") ||
					sessionTurnsDynamicUpdateRe.MatchString(s) ||
					sessionTurnsDynamicInsertRe.MatchString(s)) {
				collect(s, fset.Position(v.Pos()).Line)
			}
			return true
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(v.Value)
			if err != nil {
				return true
			}
			collect(s, fset.Position(v.Pos()).Line)
			return true
		}
		return true
	})
	return out
}

// sessionTurnsConcatString 把 Go 的 `+` 拼接表达式还原成一段文本。
//
// 非字符串的部分（变量、函数调用）替换成 <dynamic> —— 它是「表名在运行时决定」
// 的标记，扫描器据此把这条语句归到「动态表名」一类。
func sessionTurnsConcatString(n ast.Node) (string, bool) {
	switch v := n.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "<dynamic>", true
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, ok := sessionTurnsConcatString(v.X)
		if !ok {
			return "", false
		}
		r, ok := sessionTurnsConcatString(v.Y)
		if !ok {
			return "", false
		}
		return l + r, true
	case *ast.Ident, *ast.CallExpr, *ast.SelectorExpr, *ast.IndexExpr:
		return "<dynamic>", true
	}
	return "", false
}

// sessionTurnsFormatString 取 fmt.Sprintf 之类的调用的格式串，并把 %s/%v 换成 <dynamic>。
func sessionTurnsFormatString(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return sessionTurnsFormatVerbRe.ReplaceAllString(s, "<dynamic>"), true
}

// sessionTurnsNearTableLiteral 判断 fileLines 的第 line 行上下 20 行内是否出现
// 这两张表的**字面量**。
func sessionTurnsNearTableLiteral(fileLines []string, line int) bool {
	lo := line - 21
	if lo < 0 {
		lo = 0
	}
	hi := line + 20
	if hi > len(fileLines) {
		hi = len(fileLines)
	}
	window := strings.Join(fileLines[lo:hi], "\n")
	return strings.Contains(window, "session_turns")
}

// sessionTurnsColumnsInSQL 从一段 SQL 文本里抽出语句写到的列。
//
// 返回两张表：strict = 表名字面量就是这两张表的语句；dynamic = 表名是运行时
// 变量（`UPDATE `+tbl+` …`）的语句 —— 后者由调用方按「同文件邻近字面量」归属。
func sessionTurnsColumnsInSQL(sql string) (strict map[string]int, dynamic map[string]int) {
	strict = map[string]int{}
	dynamic = map[string]int{}

	// INSERT INTO <表>（列清单）
	for _, m := range sessionTurnsInsertRe.FindAllStringSubmatchIndex(sql, -1) {
		open := m[1] - 1 // '(' 的位置
		content, _ := sessionTurnsBalanced(sql, open)
		for _, part := range strings.Split(content, ",") {
			col := strings.TrimSpace(part)
			if !sessionTurnsIdentRe.MatchString(col) {
				continue // 占位符（<cols> 之类）不是列
			}
			if _, ok := strict[col]; !ok {
				strict[col] = open
			}
		}
	}

	// INSERT INTO <运行时表名>（列清单）
	for _, m := range sessionTurnsDynamicInsertRe.FindAllStringSubmatchIndex(sql, -1) {
		open := m[1] - 1
		content, _ := sessionTurnsBalanced(sql, open)
		for _, part := range strings.Split(content, ",") {
			col := strings.TrimSpace(part)
			if !sessionTurnsIdentRe.MatchString(col) {
				continue
			}
			if _, ok := dynamic[col]; !ok {
				dynamic[col] = open
			}
		}
	}

	// UPDATE <表> SET a = ..., b = ...
	for _, m := range sessionTurnsUpdateRe.FindAllStringSubmatchIndex(sql, -1) {
		for col, off := range sessionTurnsSetColumns(sql[m[1]:]) {
			if _, ok := strict[col]; !ok {
				strict[col] = m[1] + off
			}
		}
	}

	// UPDATE <运行时表名> SET a = ...
	for _, m := range sessionTurnsDynamicUpdateRe.FindAllStringSubmatchIndex(sql, -1) {
		for col, off := range sessionTurnsSetColumns(sql[m[1]:]) {
			if _, ok := dynamic[col]; !ok {
				dynamic[col] = m[1] + off
			}
		}
	}
	return strict, dynamic
}

// sessionTurnsSetColumns 抽出「UPDATE … SET …」里的被赋列（列 -> 相对偏移）。
func sessionTurnsSetColumns(rest string) map[string]int {
	out := map[string]int{}
	upper := strings.ToUpper(rest)
	setIdx := strings.Index(upper, "SET ")
	if setIdx < 0 {
		return out
	}
	section := rest[setIdx:]
	cut := len(section)
	for _, kw := range []string{"WHERE", "RETURNING", "FROM"} {
		if i := sessionTurnsIndexWord(section, kw); i >= 0 && i < cut {
			cut = i
		}
	}
	section = section[:cut]
	for _, a := range sessionTurnsAssignRe.FindAllStringSubmatchIndex(section, -1) {
		col := strings.ToLower(section[a[2]:a[3]])
		if _, ok := out[col]; !ok {
			out[col] = setIdx + a[0]
		}
	}
	return out
}

// sessionTurnsBalanced 返回 s[open] 处的 '(' 到其配对 ')' 之间的内容。
func sessionTurnsBalanced(s string, open int) (string, int) {
	if open < 0 || open >= len(s) || s[open] != '(' {
		return "", len(s)
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i
			}
		}
	}
	return "", len(s)
}

// sessionTurnsIndexWord 找 keyword 的**整词**首次出现位置（大小写不敏感）。
func sessionTurnsIndexWord(s, keyword string) int {
	up := strings.ToUpper(s)
	kw := strings.ToUpper(keyword)
	from := 0
	for {
		i := strings.Index(up[from:], kw)
		if i < 0 {
			return -1
		}
		i += from
		beforeOK := i == 0 || !sessionTurnsIdentByte(up[i-1])
		afterIdx := i + len(kw)
		afterOK := afterIdx >= len(up) || !sessionTurnsIdentByte(up[afterIdx])
		if beforeOK && afterOK {
			return i
		}
		from = i + len(kw)
	}
}

func sessionTurnsIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
