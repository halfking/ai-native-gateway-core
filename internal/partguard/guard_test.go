package partguard

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// allowedParentDML 是**当前已知**的「对分区父表直接写」集合。
//
// 为什么要一张这么长的清单：objective 的红线是「更新、删除只能在 hot 表
// 中进行」，而现状是这条红线在 Go 侧被违反了 18 处 / 10 个文件
// （R78 复核，比子代理初报多了 5 个文件）。这些站点今天能跑，只因为
// 涉及的父表恰好还是 heap；按 objective 列存化后 citus-columnar 引擎
// 会直接拒绝 UPDATE/DELETE。改造它们是架构决策（须产品裁决，见 49 号
// 报告），本门不做改造，只保证**清单不再变长**。
//
// 门有三条独立判据，任一不满足即红：
//  1. 集合必须恰好相等：多一条 = 新违规；少一条 = 登记项已失效（代码改了
//     而白名单没跟），后者同样危险——它会让下一个人以为该文件已合规。
//  2. 每条的**条数**必须相等：同文件同表再加一条 UPDATE 会被计数抓到。
//     只比对集合的话，第二条会被第一条的登记项吸收掉。
//  3. Reason 必填且有实质长度：写"同上""已知问题"这类占位理由等于没有
//     理由——本门开发时在 metricguard 上已因同类占位理由被自己的门抓过一次。
var allowedParentDML = []struct {
	File   string
	Table  string
	Count  int
	Reason string
}{
	{"admin/session_turns_v2.go", "sessions", 1,
		"会话摘要/标题回写。代码自带分区 pin 绕行（partition_date = MAX(...))，注释自认「Partitioned sessions table: UPDATE ... ORDER BY/LIMIT is invalid in PostgreSQL」；改 hot 需要先有 v2 摘要投影列，R78 登记为待裁决。"},
	{"bg/opslog_trimmer.go", "candidate_failure_logs", 1,
		"7 天 TTL 行级清扫。文件头注释记录该 TTL 长期失效的真实形态（id 全 NULL 致 `id IN` 一行删不掉），换成 ctid 形态后才生效；是既有清扫通道，不是新增写路径。"},
	{"cmd/gateway/turn_logs_aggregator.go", "sessions", 2,
		"会话 turn-logs 摘要 flush（upsert 分支与 flush 分支各一处）。经 pg_advisory_xact_lock 串行，同 admin/session_turns_v2.go 属同一族「会话元数据直写父表」。"},
	{"cmd/tools/validate_sessions_v2/repair.go", "session_bodies", 1,
		"一次性校验修复工具（cmd/tools，手工触发，非请求路径）。按 session_id 删除孤儿 body；工具形态与在线链路分离，R78 登记为待裁决。"},
	{"cmd/tools/validate_sessions_v2/repair.go", "session_turns", 1,
		"同上，一次性修复工具按 session_id 删除孤儿 turn 元数据。"},
	{"cmd/tools/validate_sessions_v2/repair.go", "sessions", 1,
		"同上，一次性修复工具删除目标会话行本身。"},
	{"cmd/tools/validate_sessions_v2/repair.go", "session_turn_details", 1,
		"同上族，但**这条是 R43 轮 L4 补的第三族表腿**：重建只写 turns（partition_date=CURRENT_DATE），而 817 视图的 join 键含 partition_date ⇒ 上一轮遗留的存活旧行永不再匹配、同时留下孤儿行。只删 turns 那一面关不掉这个闭环，故必须扫 details 父表。一次性手工工具、R78 同族待裁决。"},
	{"db/db.go", "session_bodies", 1,
		"promote 函数内的 reconciled CTE：父表 UNIQUE(tenant_id,request_id,partition_date) 与 hot 的 ON CONFLICT (id,partition_date) 冲突，毒丸行会卡死 hot 窗口，故 promote 期间必须回写父表。与 R75 的 columns 冲突同源，是「promote 期间写父表」被引入不变量的根因。"},
	{"domains/session/v2/session_aggregator.go", "session_turns", 1,
		"聚合 claim（claimAggregateTurn）：把 aggregate_applied_at 置 NOW 以抢占该轮聚合。unified 视图中分区副本为权威，故必须先 claim 父表。"},
	{"domains/session/v2/session_aggregator.go", "sessions", 2,
		"会话元数据状态机：CloseSession 置 status/closed_at，以及带 COALESCE 的字段合并更新（经 sessionAdvisoryLockSQL 串行）。"},
	{"domains/session/v2/session_request_status_backfill.go", "session_turns", 1,
		"request_status 回填的 UPDATE ... FROM (VALUES …) 批量回写（:667）。与同族 claim/状态机不同：它的**分母口径就是全量 turn**（候选集按 request_id join request_logs 母表统计，见 sqlreadguard 同批登记），按 hot 单窗回写会让「已回填比例」的分母与写入面错位 ⇒ 统计恒为 0。带 WHERE request_status IS NULL 幂等守卫。"},
	{"domains/stats/event_writer.go", "stats_event_inbox", 1,
		"inbox 行状态机：pending→processed 回写。注释记录本条曾留下 119 万行「processed_at 已置但状态仍 pending」的脏数据。"},
	{"domains/stats/inbox_consumer.go", "stats_event_inbox", 4,
		"inbox 消费端状态机：claim 置 processing、markProcessed、失败重试置 pending、租约过期回收，四处同一状态机。带 fencing_token + lease_until 的租约语义，不能改成只写 hot。"},
	{"domains/ursm/v2/persist/retention.go", "ursm_node_snapshot_min", 1,
		"**兜底腿，不是主腿**：`CleanupWithStats`（retention.go:215-240）完全按 `pg_class.relkind` 分派——分区父表走 `cleanupPartitioned`（整分区 DROP，立即归还磁盘），只有两种情况才落到这里的行级批 DELETE：① 表还是普通 heap（825 分区迁移执行前的形态）；② DROP 本轮失败，降级重试。文件注释自陈理由「宁可慢也不能让留存静默停摆——停摆的表现是磁盘单调增长，而没有任何告警」，且降级路径会打 slog.Warn 并把 `Degraded: true` 写进结果。0b80d07b7 的「形态自适应」在此成立：主腿是 DROP，行删是它明确登记的退路。"},
	{"internal/titlestore/store.go", "sessions", 2,
		"标题投影的条件提交（WITH changed AS (UPDATE ...) 取 RETURNING）与清除标题；两处均带 partition_date = MAX(...) pin，注释指向与 admin/session_turns_v2.go 同一手写绕行模式。"},
}

type pair struct {
	file  string
	table string
}

func TestNoParentTableDMLBeyondKnown(t *testing.T) {
	root := repoRoot(t)
	vs, err := CollectViolations(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	got := map[pair]int{}
	for _, v := range vs {
		got[pair{v.File, v.Table}]++
	}

	want := map[pair]int{}
	for _, a := range allowedParentDML {
		p := pair{a.File, a.Table}
		if _, dup := want[p]; dup {
			t.Errorf("白名单重复登记 %s / %s", a.File, a.Table)
		}
		want[p] = a.Count
		if len([]rune(a.Reason)) < 20 {
			t.Errorf("%s / %s 的登记理由过短（%d 字），等于没写：%q",
				a.File, a.Table, len([]rune(a.Reason)), a.Reason)
		}
	}

	var extra, stale []string
	for p, n := range got {
		w, ok := want[p]
		switch {
		case !ok:
			extra = append(extra, p.file+" / "+p.table)
		case n != w:
			extra = append(extra, fmt.Sprintf("%s / %s 条数 %d≠登记的 %d", p.file, p.table, n, w))
		}
	}
	for p := range want {
		if _, ok := got[p]; !ok {
			stale = append(stale, p.file+" / "+p.table)
		}
	}
	sort.Strings(extra)
	sort.Strings(stale)

	if len(extra) > 0 {
		t.Errorf("发现未登记的分区父表写操作（%d 项）——违反「更新/删除只在 hot 表」红线：\n  %s",
			len(extra), strings.Join(extra, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("白名单登记项已失效（%d 项）——代码已改而登记未跟，下一个人会误以为该文件已合规：\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}

// sqliteBacked 是**排除**项。若它变成死代码（SQLite 侧不再有同名表
// DELETE），这条排除就会静默地不再需要解释——本门必须知道它还在承重。
func TestSQLiteExclusionIsLoadBearing(t *testing.T) {
	root := repoRoot(t)
	vs, err := collectViolations(root, true)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var fromSQLite int
	for _, v := range vs {
		if isSQLitePath(v.File) {
			fromSQLite++
		}
	}
	if fromSQLite == 0 {
		t.Fatal("sqliteBacked 排除项已失效：SQLite 侧没有任何同名表写操作，" +
			"应当把该排除项删掉，而不是留一条不再承重的规则")
	}
	t.Logf("SQLite 侧同名表写操作 %d 处已按路径排除（非 PostgreSQL 分区父表）", fromSQLite)
}

// TestParentsAreDeclaredInDDL 防止父表清单漂移：列表里的每个名字必须在
// 仓内 DDL 中确实以 PARTITION BY 声明。硬编码列表的固有风险是「拼错」
// 与「把不存在的表写进来」，后者会让门对该表彻底失效却毫无征兆。
func TestParentsAreDeclaredInDDL(t *testing.T) {
	root := repoRoot(t)
	var stmts []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", "bin", ".build-local":
				return filepath.SkipDir
			}
			return nil
		}
		// ★ 2026-10-06（R47）：`.sql.skip` 必须一并收进来。
		//
		// `404050630` 把 830 改标成
		// `830_ursm_node_snapshot_min_partitioned.sql.skip`（它是一条
		// manual-by-design 迁移：无人值守升级会 RENAME 一张 10GB 在线表）。
		// 该扩展名以 `.skip` 结尾 ⇒ 被下面这条 `.sql` 后缀判断整个排除
		// ⇒ 本门扫不到它的 `PARTITION BY` 声明 ⇒
		// `TestParentsAreDeclaredInDDL` 把**仍然存在**的父表
		// `ursm_node_snapshot_min` 报成「拼错或已下线」。
		//
		// 为什么必须收：`.sql.skip` 是本仓既有的「不投递、但保留 DDL」
		// 形态（见 bg/partition_manager.go:39 对 336 的记述），
		// DDL 仍是仓内关于该表形状的**唯一声明来源**。
		// 排除它 ⇒ 任何改标为 skip 的分区表都会让本门失效，
		// 且失效方向是「悄悄少扫」而不是「报错」。
		//
		// ⚠ 只改这一处，不改下面 :255 那处：那是 DDL 文件**计数**，
		// 语义上只数真正会投递的 .sql；把 skip 计进去会漂移它的基线。
		//
		// 负控（去掉 `.sql.skip` 那一半）⇒ 精确复现原红：
		//   guard_test.go:223: 父表清单里的 "ursm_node_snapshot_min" … 找不到
		if !strings.HasSuffix(p, ".sql") && !strings.HasSuffix(p, ".sql.skip") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		txt := commentStripRE.ReplaceAllString(string(b), "")
		for _, s := range strings.Split(txt, ";") {
			if strings.Contains(strings.ToUpper(s), "PARTITION BY") {
				stmts = append(stmts, strings.ToUpper(s))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(stmts) < 50 {
		t.Fatalf("只扫到 %d 条 PARTITION BY 语句，扫描范围疑似失效（仓内应有近百条）", len(stmts))
	}
	for _, name := range partitionParents {
		re := regexp.MustCompile(`\b` + strings.ToUpper(name) + `\b`)
		found := false
		for _, s := range stmts {
			if re.MatchString(s) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("父表清单里的 %q 在仓内 DDL 中找不到 PARTITION BY 声明："+
				"要么拼错，要么该表已改名/已下线——两种都要立刻改清单", name)
		}
	}
}

// TestPartitionParentsAreExhaustive 补 TestParentsAreDeclaredInDDL 缺的**反向**判据。
//
// 既有那道门只做正向：「清单里的每个名字在 DDL 里确有 PARTITION BY」。它挡得住
// 拼错与「把不存在的表写进白名单」，但**挡不住新迁移加一个分区父表而没人登记** ——
// 而 objective 的红线是「**所有**的大数据表是 hot+分区（columnar）表，
// 更新、删除只能在 hot 表中进行」。一张新分区表若没进清单，它上面的
// Go 写操作会被本门**完全放过**，且没有任何征兆。
//
// 201 号实测：当前树**没有**这个缺口（DDL 侧发现的父表与 32 条清单恰好一致，
// 多一张少一张都没有）。所以这道门现在守的是**将来**，不是现状 —— 但
// 「今天恰好没有」和「明天也不会有」之间，差的正是这道判据。
//
// 为什么必须自带覆盖下限：本门判据的形式是「集合相等」，而**空集合也相等**。
// 若抽取逻辑某天坏掉（DDL 形态变了、路径过滤写错、换行解析退化），它会抽出
// 0 个父表、比对通过、安静下来。所以下限取**贴近实测值的下界**，且把实测值
// 一起打出来，便于日后校准 —— 一个只会 Logf 的覆盖数字是装饰，不是守卫。
func TestPartitionParentsAreExhaustive(t *testing.T) {
	root := repoRoot(t)

	// 锚在 CREATE TABLE 上取名，并**取点分路径的最后一段**。
	//
	// 只认 `public.` 前缀是错的：641_local_shared_platform_schema_fixup.sql:41 写的是
	// `CREATE TABLE IF NOT EXISTS platform.platform_outbox (`，naive 正则会把 **schema 名**
	// platform 当成表名，于是凭空多出一张「清单里没有的父表」。
	// 这与基线 :164 的 PL/pgSQL 格式串让 ALTER 变体误抓 schema 名 public 是同一类假阳性，
	// 两次都是「先把抓到的名字当表名，再怀疑清单」——顺序反了。
	//
	// 同时**不要**改用 `ALTER TABLE … ATTACH PARTITION`：实测那个形态贡献 0 个独有父表
	//（涉及的表都已被 PARTITION BY 收全），只贡献假阳性 —— 零收益的模式要拿掉，不是修补。
	nameRe := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*(?:\s*\.\s*[a-z_][a-z0-9_]*)*)`)

	found := map[string]string{} // 小写表名 -> 声明它的第一个 .sql
	sqlFiles := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", "bin", ".build-local":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".sql") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		sqlFiles++
		rel := filepath.ToSlash(p)
		if abs, e := filepath.Rel(root, p); e == nil {
			rel = filepath.ToSlash(abs)
		}
		for _, s := range strings.Split(commentStripRE.ReplaceAllString(string(b), ""), ";") {
			up := strings.ToUpper(s)
			if !strings.Contains(up, "PARTITION BY") {
				continue
			}
			m := nameRe.FindStringSubmatch(s)
			if m == nil {
				continue
			}
			// 取最后一段：`platform.platform_outbox` → `platform_outbox`。
			// 清单里是**不带 schema** 的表名，跨 schema 的同名表也据此归一。
			path := m[1]
			if i := strings.LastIndex(path, "."); i >= 0 {
				path = path[i+1:]
			}
			name := strings.ToLower(strings.TrimSpace(path))
			if name == "" {
				continue
			}
			if _, dup := found[name]; !dup {
				found[name] = rel
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// 覆盖面下限：先证「扫到东西了」，再谈集合比较。
	// 199 号教训：下限只能证明「我数到了」，不能证明「我数对了」—— 所以
	// 阈值贴着实测值取（实测 32 个父表），并在失败时把实际抽到的名字打出来。
	const minParents = 25 // 实测 33；留 8 的余量给「合法新增父表时同步登记」
	if len(found) < minParents {
		t.Fatalf("只从 %d 个 .sql 里抽出 %d 个分区父表（下限 %d）：抽取逻辑很可能已失效，"+
			"此时「集合相等」会与空集合同步为真而安静通过。抽到的：%v",
			sqlFiles, len(found), minParents, keysOf(found))
	}

	known := map[string]bool{}
	for _, n := range partitionParents {
		known[n] = true
	}
	var missing []string
	for name, src := range found {
		if !known[name] {
			missing = append(missing, fmt.Sprintf("%s <- %s", name, src))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("DDL 里有 %d 个分区父表不在 partitionParents 清单里（守卫看不见它们上面的写操作）：\n  %s\n"+
			"  修法：把它们加进 parents.go 清单，并给每张表补 TestNoParentTableDMLBeyondKnown 的登记理由"+
			"（若该表确无 Go 侧写操作，加进清单即可，登记项可不加）。\n"+
			"  这道门与 TestParentsAreDeclaredInDDL 是互补的两向：那边查「清单里的都在」，\n"+
			"  这边查「DDL 里的都在」——只有单向时，父表清单会静默腐化。",
			len(missing), strings.Join(missing, "\n  "))
	}
	t.Logf("从 %d 个 .sql 抽出 %d 个分区父表，清单 %d 条，缺 %d 条", sqlFiles, len(found), len(partitionParents), len(missing))
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 扫描器自身的编译健全性：仓内 Go 源必须能解析。若解析被静默跳过
// （CollectViolations 里 `parse 失败就 return nil`），扫描范围会在无人
// 察觉的情况下缩水，门变成恒绿。
func TestScannerParsesRepoGo(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var parsed, failed int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if err == nil && info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", "node_modules", "vendor", "web", "bin", ".build-local":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if _, perr := parser.ParseFile(fset, p, nil, 0); perr != nil {
			failed++
			if failed <= 3 {
				t.Errorf("解析失败 %s: %v", p, perr)
			}
			return nil
		}
		parsed++
		return nil
	})
	if failed > 0 {
		t.Errorf("共 %d 个 .go 文件解析失败", failed)
	}
	if parsed < 500 {
		t.Errorf("只解析到 %d 个 .go 文件，扫描范围疑似缩水", parsed)
	}
	t.Logf("解析 %d 个非测试 .go 文件", parsed)
}

var commentStripRE = regexp.MustCompile(`--[^\n]*`)
