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
	{"db/db.go", "session_bodies", 1,
		"promote 函数内的 reconciled CTE：父表 UNIQUE(tenant_id,request_id,partition_date) 与 hot 的 ON CONFLICT (id,partition_date) 冲突，毒丸行会卡死 hot 窗口，故 promote 期间必须回写父表。与 R75 的 columns 冲突同源，是「promote 期间写父表」被引入不变量的根因。"},
	{"domains/session/v2/session_aggregator.go", "session_turns", 1,
		"聚合 claim（claimAggregateTurn）：把 aggregate_applied_at 置 NOW 以抢占该轮聚合。unified 视图中分区副本为权威，故必须先 claim 父表。"},
	{"domains/session/v2/session_aggregator.go", "sessions", 2,
		"会话元数据状态机：CloseSession 置 status/closed_at，以及带 COALESCE 的字段合并更新（经 sessionAdvisoryLockSQL 串行）。"},
	{"domains/stats/event_writer.go", "stats_event_inbox", 1,
		"inbox 行状态机：pending→processed 回写。注释记录本条曾留下 119 万行「processed_at 已置但状态仍 pending」的脏数据。"},
	{"domains/stats/inbox_consumer.go", "stats_event_inbox", 4,
		"inbox 消费端状态机：claim 置 processing、markProcessed、失败重试置 pending、租约过期回收，四处同一状态机。带 fencing_token + lease_until 的租约语义，不能改成只写 hot。"},
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
		if !strings.HasSuffix(p, ".sql") {
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
