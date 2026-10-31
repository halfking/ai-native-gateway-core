package ursm_test

// db_object_census_gate_test.go
//
// 守的是 R161 轮立起来的那条底账：**各环境【真实对象】× 仓内迁移【期望集】**。
//
// # 这条底账为什么必须存在（R160 轮撞出来的）
//
// ★ R160 轮把「登记册 × 各环境账本」变成了机器可判，但 R160 自己撞到一件事：
//
//	local 账本覆盖 1..909（277 行）｜252 账本只有 49 行（560..890）｜211 账本到 895（264 行）
//
// ⇒ ⇒ **「某个迁移在 252/211 上到底建没建出对象」这个问题，账本回答不了**
// ⇒ ⇒ ★★ 于是 R161 把事实源**再往下一层**挪：★★ **不看账本，看数据库里真实存在的对象**。
//
// # R161 轮的头号发现（★ 本门守着它的可复算性）
//
// ★★ **211 缺 14 个【只有它缺】的性能索引**（★ 迁移 900/901/903/904/905/906/909）：
//
//	909  idx_session_summaries_{session_key,title,primary_model}_trgm
//	    ⇒ ★ 252/本地走 BitmapOr + Bitmap Index Scan；★★ 211 走 **Parallel Seq Scan**
//	    ⇒ ★★ R154 实测该查询在 252 上是 40.4 秒；★ ctx 超时 2 秒 ⇒ 静默返回「没搜到」
//	905  idx_session_turns_model_session(+hot)
//	    ⇒ ★★ 211 上 `?model=` 是【逐分区 Seq Scan】；★ 仓内注释记着实测 **205,708 ms**
//	906  idx_session_turns_api_key_numeric(+hot)
//	    ⇒ ★ 211 上 api_key 回填路径无索引
//
// ★★ 本门**不**断言「211 必须一直缺这些索引」（★ 那是世界状态，★ 修好了就该翻绿）
// ★★ ⇒ 本门断言的是【**台账 ≡ 底账 + 期望集**】⇒ ★★ 台账永远可复算、不会自己腐坏。
//
// # ★★ 六道门与它们的牙
//
//	G1 底账形状 + 每环境 sha256 自洽       ⇒ 采集被截断/被污染 ⇒ 红
//	G2 期望集 ≡ Go 独立反解                 ⇒ 反解器漂了 ⇒ 红
//	G3 缺口台账 ≡ Go 独立重算（★ 全量 852 行）⇒ 台账与底账脱节 ⇒ 红
//	G4 坏行必被拒（★ 本轮真踩过）            ⇒ 读数被吞 ⇒ 红
//	G5 四态判定 + 反向对照                  ⇒ 「表在不在」判错 ⇒ 红
//	G6 生产缺口棘轮（★ 只降不升）             ⇒ 缺口变大 ⇒ 红
//
// ★★ G2/G3 是【第二份独立实现】：★ Python 工具（scripts/audit/db-object-census.py）
// ★★ 与本文件（Go）**各写一遍**期望集反解与状态判定 ⇒ ★★ 两份实现互证
// ★★ ⇒ 工具的「自证」不能自证（★ R160 轮的教训）。

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	dbObjCensusRel     = "scripts/audit/asset/db-object-census.tsv"
	dbObjExpectRel     = "scripts/audit/asset/db-object-expect.tsv"
	dbObjLedgerRel     = "scripts/audit/asset/db-object-gap-ledger.tsv"
	dbObjRetiredRel    = "scripts/audit/asset/db-object-retired.tsv"
	dbObjColumnsRel    = "scripts/audit/asset/db-object-columns.tsv"
	dbObjReachRel      = "scripts/audit/asset/installer-reachability.tsv"
	dbObjManifestRel   = "sql/schema/installed_startup_migrations.tsv"
	dbObjPackageRel    = "installer/cmd/llm-gw-installer/embeddata/startup"
	dbObjMigrationsRel = "sql/migrations/startup"
	dbObjAuditRel      = "docs/audit/2026-10-27-R161-三环境真实对象底账与迁移落点缺口.md"

	dbObjCensusCols  = 17 // env + 16（★ R162：从 15 列扩到 17 列，★ 多了 index_columns / index_predicate）
	dbObjLedgerCols  = 9
	dbObjExpectCols  = 6 // ★ R163：★ 加了 declared_columns（★ 列级联退役判据的输入）
	dbObjRetiredCols = 10 // ★ R163：★ 加了 retire_kind / retired_column（★ 第二类退役证据）
	dbObjColumnsCols = 4 // ★ R163：★ env + schema + table + column
	dbObjReachCols   = 10
)

// ★★ 生产缺口棘轮基线（★ R161 轮实测值，★ 2026-10-27）。
// ★★ 只许下降（★ 属主把迁移补到 211/252 之后 ⇒ 数字变小 ⇒ 仍绿）。
// ★★ 变大 ⇒ 红（★ 说明有人在别处挖了坑）。
var dbObjGapRatchet = map[string]int{
	"local": 56,
	"fresh": 49,
	"252":   56,
	// ★★ 211：62 → 49（★ R178 轮：★ 211 的底账原来是 10-26 的旧快照 ⇒ ★★
	// ★★ 刷新后 211 实际多部署了 13 条索引 ⇒ ★ 真缺口 62 → 49）★★
	// ★★ ⇒ 按 R161 审计档 §棘轮 的纪律「每次修复后手工下调基线」★ ★★
	"211": 49,
}

// ★★★ R178：★「待部署豁免」的【结算边界】★ —— ★★ 棘轮从「计 missing」改成「只计真缺口」。
//
// ★★★ 为什么要改（★ 本轮真踩到的一个结构性缺陷）：
// ★★★ 新增一条迁移 ⇒ ★ 它声明的对象在【还没部署到】的环境里**必然**是 missing
// ★★★ ⇒ ★ 计数型棘轮把它当成「有人在别处挖了坑」⇒ ★★ 红。
// ★★★ ⇒ ⇒ 于是这道门**结构性地**挡住了「加一条迁移」这件事 ⇒ ⇒ 唯一的出路是抬基线
// ★★★ ⇒ ⇒ 而抬基线正是仓内明令禁止的（R161 审计档 §「棘轮」行：
// ★★★ 「★★ 调高基线的理由必须是『检测器变准』而不是『新增了未修的缺口』」）。
//
// ★★★ 口径（★ 三条【同时】成立才算「待部署」，★ 否则一律当真缺口）：
//
//	① 迁移号 > dbObjGapRatchetEpoch（★ 在途：★ 还没结算）
//	② 该迁移在安装器清单里【可达】（★ 不可达 ⇒ 它永远不会被应用 ⇒ 那是【永久缺口】）
//	③ 该迁移声明的对象在目标环境里【一个都不存在】（★ ⇒ 该环境【还没应用过】它）
//
// ★★★ 判据 ③ 为什么够用：★ 一条迁移的对象要么全在、要么全不在
// ★★★ （★ 迁移在事务里跑，★ 失败即回滚；★ CONCURRENTLY 迁移则要么建完、要么没建）
// ★★★ ⇒ ⇒ 「全不在」= 「没应用过」⇒ ⇒ 缺口是【待部署】而不是【被谁删了】。
//
// ★★★ epoch 的作用是【给在途的东西一个期限】：★ epoch 之上的迁移暂时豁免
// ★★★ ⇒ ★ 下一轮把 epoch 抬上去（★ 即「它该落地了」）⇒ ★ 它的缺口若还在 ⇒ ★★ 门红。
var dbObjGapRatchetEpoch = 914

// dbObjGapFacts 判定「真缺口 / 待部署」所需的三份事实（★ 全部来自已有资产，★ 不新增台账）。
type dbObjGapFacts struct {
	// migObjects：★ 迁移号 → 该迁移声明的全部索引名。
	migObjects map[string][]string
	// present：★ 环境 → 该环境【现存的索引名集合】。
	present map[string]map[string]bool
	// reachable：★ 索引名 → 它的创建迁移是否登记进安装器清单。
	reachable map[string]bool
}

// dbObjGapKind 判定某环境里某条期望索引的缺口属于「真缺口」还是「待部署」。
//
// ★ 返回 "hole"（★ 计入棘轮）或 "pending"（★ 豁免，★ 但仍留在台账里）。
func (f dbObjGapFacts) dbObjGapKind(migNo, index, env string) string {
	n, err := strconv.Atoi(migNo)
	if err != nil || n <= dbObjGapRatchetEpoch {
		return "hole" // ★ 迁移号不认得、或已结算 ⇒ 一律当真缺口（★ 保守）
	}
	if !f.reachable[index] {
		return "hole" // ★ 创建迁移不可达 ⇒ 它永远不会被应用 ⇒ 永久缺口
	}
	for _, sib := range f.migObjects[migNo] {
		if f.present[env][sib] {
			// ★★ 该迁移在这个环境【已经应用过】⇒ ⇒ 缺的那些是真缺口（★ 迁移建了、谁删了）
			return "hole"
		}
	}
	return "pending"
}

// dbObjGapFactsLoad 读三份仓内事实。
func dbObjGapFactsLoad(t *testing.T) dbObjGapFacts {
	t.Helper()
	f := dbObjGapFacts{
		migObjects: map[string][]string{},
		present:    map[string]map[string]bool{},
		reachable:  map[string]bool{},
	}
	// ① 缺口台账：★ 迁移号 → 该迁移声明的全部索引。
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjLedgerRel)) {
		if len(r) != dbObjLedgerCols {
			t.Fatalf("ledger row malformed: %v", r)
		}
		f.migObjects[r[0]] = append(f.migObjects[r[0]], r[1])
	}
	// ② 对象底账：★ 环境 → 现存索引名。
	for env, st := range dbObjParseCensus(t, dbObjRepoPath(t, dbObjCensusRel)) {
		set := map[string]bool{}
		for name := range st.idx {
			set[name] = true
		}
		f.present[env] = set
	}
	// ③ 可达性台账：★ 索引 → 它的创建迁移是否登记进安装器。
	for _, r := range dbObjDataLines(t, dbObjRepoPath(t, dbObjReachRel)) {
		if len(r) != dbObjReachCols {
			t.Fatalf("reachability row malformed: %v", r)
		}
		f.reachable[r[0]] = (r[4] == "yes")
	}
	return f
}

// ─────────────────────────── 读文件 ───────────────────────────

func dbObjRepoPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join("..", "..", filepath.FromSlash(rel))
}

// dbObjDataLines 读 TSV 的数据行（★ 跳过空行与 # 注释行）。
func dbObjDataLines(t *testing.T, path string) [][]string {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer fh.Close()
	var out [][]string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.Split(line, "\t"))
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func dbObjHeaderLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// ─────────────────────────── 底账 ───────────────────────────

type dbObjEnvState struct {
	idx      map[string]bool
	children map[string][]string
	rels     map[string]string // name -> relkind
}

// dbObjExpect 一条期望索引（★ 编号 / 表 / 声明键列 / 来源文件）。
type dbObjExpect struct {
	no    string
	table string
	cols  []string
	file  string
}

// dbObjColState 列底账：★ env -> table -> 列名集合。
type dbObjColState map[string]map[string]map[string]bool

// dbObjParseColumns 读列底账（★ 「这个键列在本环境还在不在」的事实源）。
//
// ★★★ R163 新增：★ 这层事实此前**完全不存在**⇒ ⇒ 「列没了 ⇒ 索引被 PG 级联删掉」
// ★★★ 在仓内不可见 ⇒ ⇒ 假缺口的第三种来源一直躺着。
func dbObjParseColumns(t *testing.T, path string) dbObjColState {
	t.Helper()
	out := dbObjColState{}
	for _, r := range dbObjDataLines(t, path) {
		if len(r) != dbObjColumnsCols {
			t.Fatalf("columns row has %d fields, expected %d: %v", len(r), dbObjColumnsCols, r)
		}
		env, table, col := r[0], r[2], r[3]
		if out[env] == nil {
			out[env] = map[string]map[string]bool{}
		}
		if out[env][table] == nil {
			out[env][table] = map[string]bool{}
		}
		out[env][table][col] = true
	}
	if len(out) == 0 {
		t.Fatal("columns artifact has no data rows ⇒ the column-retirement verdict asserts nothing")
	}
	return out
}

// dbObjParseCensus 解析底账 ⇒ {env: state}。
// ★★ 字段数不对 ⇒ **直接失败**（★ 本轮真踩过：★ PG17 pretty-print 往 def 里塞换行
// ★★ ⇒ 行式 TSV 被静默读错 ⇒ ⇒ 这里必须是硬失败，不是"跳过这行"）。
func dbObjParseCensus(t *testing.T, path string) map[string]*dbObjEnvState {
	t.Helper()
	out := map[string]*dbObjEnvState{}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lineno := 0
	for _, line := range strings.Split(string(raw), "\n") {
		lineno++
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != dbObjCensusCols {
			t.Fatalf("%s:%d: census row has %d fields, expected %d",
				path, lineno, len(parts), dbObjCensusCols)
		}
		env := parts[0]
		st, ok := out[env]
		if !ok {
			st = &dbObjEnvState{
				idx:      map[string]bool{},
				children: map[string][]string{},
				rels:     map[string]string{},
			}
			out[env] = st
		}
		switch parts[1] {
		case "idx":
			st.idx[parts[4]] = true
			if parent := parts[14]; parent != "" {
				st.children[parent] = append(st.children[parent], parts[4])
			}
		case "rel":
			st.rels[parts[4]] = parts[3]
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s has no data rows", path)
	}
	return out
}

// dbObjClassify 是四态判定（★ 与 Python 工具同规格、★ 独立实现）。
// dbObjClassify 判定一个索引在某环境的状态（★ 五态）。
//
// ★★★ R163 加了第 5 态 `retired-by-column-drop`：★ 索引的【直接键列】在本环境
// ★★★ 已经不存在 ⇒ ⇒ PG 在 DROP COLUMN 时会【自动删掉】依赖它的索引
// ★★★ ⇒ ⇒ 它【不是】缺口 ⇒ ⇒ **这是假缺口的第三种来源**（★ 与 R162 的「假缺口」相反，
// ★★★ R163 修的「ON ONLY」是**假退役**⇒ 掩盖真缺口 ⇒ ★★ 两个方向都要修）。
//
//	★ 实测命中：★ idx_sessions_last_full_at（★ 迁移 708 删了 last_full_payload_at）
//	★ 而 211 上那列还在（★ 456 跑在 708 之后）⇒ ⇒ ★★ 顺序倒置的机器可读指纹
func dbObjClassify(st *dbObjEnvState, indexName, table string, keycols []string, colEnv map[string]map[string]bool) string {
	if st.idx[indexName] {
		return "present"
	}
	if len(st.children[indexName]) > 0 {
		return "present-via-child"
	}
	kind, ok := st.rels[table]
	if !ok {
		return "retired"
	}
	if kind != "r" && kind != "p" {
		return "retired"
	}
	if len(keycols) > 0 && colEnv != nil {
		have, known := colEnv[table]
		if known {
			for _, c := range keycols {
				if !have[c] {
					return "retired-by-column-drop"
				}
			}
		}
	}
	return "missing"
}

// ─────────────────────────── 期望集（Go 独立反解） ───────────────────────────

var (
	// ★★★ R163 修掉的老 bug：★ `CREATE INDEX x ON ONLY <table>` ⇒ 老正则把表名吃成 "ONLY"
	// ★★★ ⇒ ⇒ 那 17 条索引在缺口台账里被判成 `retired`（★ 表不存在）⇒ ⇒ ★★ **真缺口被掩盖**
	// ★★★ ⇒ ⇒ ★★ Python 工具与这份 Go 门【两侧同源同错】⇒ ⇒ 互证对那批是零信息
	// ★★★ ⇒ ⇒ 修法：★ 两侧都显式容忍 `ONLY`（★ 并用下面的自证钉死）
	dbObjReCreate = regexp.MustCompile(`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_"]+)\s+ON\s+(?:ONLY\s+)?([a-z0-9_."]+)`)
	// ★★ 括号配对扫描的头（★ 允许 `USING <method>` 夹在表名与列清单之间）
	dbObjReCreateHead = regexp.MustCompile(`(?is)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_"]+)\s+ON\s+(?:ONLY\s+)?([a-z0-9_."]+)\s*(?:USING\s+[a-z0-9_]+\s*)?\(`)
	dbObjReDrop        = regexp.MustCompile(`(?is)DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?([a-z0-9_."]+)`)
	dbObjReUsing       = regexp.MustCompile(`(?is)ADD\s+CONSTRAINT[^;]*?USING\s+INDEX\s+([a-z0-9_."]+)`)
	dbObjReLineCm      = regexp.MustCompile(`(?m)--[^\n]*`)
	dbObjReAscDesc     = regexp.MustCompile(`(?i)\s+(ASC|DESC)\s+NULLS\s+(FIRST|LAST)$`)
	dbObjReAscDescOnly = regexp.MustCompile(`(?i)\s+(ASC|DESC)$`)
	dbObjReIdent       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
)

// dbObjCreate 一条 CREATE INDEX 的结构化结果（★ 名字 / 表 / 直接键列）。
type dbObjCreate struct {
	name  string
	table string
	cols  []string
}

// dbObjDeclaredKeyColumns 取索引的【直接键列】（★ 保守口径：★ 表达式索引不取列名）。
//
// ★★ 为什么保守：★ 取错列名 ⇒ 把活索引误判成「被列级联退役」
// ★★ ⇒ 假缺口从「缺」翻成「不该有」⇒ ⇒ **两个方向都会骗人** ⇒ ⇒ 只在证据明确时退役。
func dbObjDeclaredKeyColumns(colsText string) []string {
	out := []string{}
	depth := 0
	cur := strings.Builder{}
	flush := func() {
		e := strings.TrimSpace(cur.String())
		cur.Reset()
		if e == "" {
			return
		}
		e = dbObjReAscDesc.ReplaceAllString(e, "")
		e = dbObjReAscDescOnly.ReplaceAllString(e, "")
		if strings.Contains(e, "(") {
			return
		}
		e = strings.Trim(e, "\"")
		if !dbObjReIdent.MatchString(e) {
			return
		}
		out = append(out, strings.ToLower(e))
	}
	for _, ch := range colsText {
		switch ch {
		case '(':
			depth++
			cur.WriteRune(ch)
		case ')':
			depth--
			cur.WriteRune(ch)
		case ',':
			if depth == 0 {
				flush()
			} else {
				cur.WriteRune(ch)
			}
		default:
			cur.WriteRune(ch)
		}
	}
	flush()
	return out
}

// dbObjScanCreateIndexes 括号配对扫描 CREATE INDEX（★ 取列清单用）。
//
// ★★ 为什么不用正则的 `\(([^)]*)\)`：★ 表达式索引（lower(x) / (a->>'b')）
// ★★ 会被截断在第一个 `)` ⇒ 列清单不完整 ⇒ 「列级联退役」判据会失真。
func dbObjScanCreateIndexes(body string) []dbObjCreate {
	out := []dbObjCreate{}
	locs := dbObjReCreateHead.FindAllStringSubmatchIndex(body, -1)
	for _, loc := range locs {
		name := dbObjStripName(body[loc[2]:loc[3]])
		table := dbObjStripName(body[loc[4]:loc[5]])
		if i := strings.Index(table, "."); i >= 0 {
			table = table[i+1:]
		}
		open := loc[1] - 1
		depth := 0
		end := -1
	loop:
		for j := open; j < len(body); j++ {
			switch body[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = j
					break loop
				}
			}
		}
		colsText := ""
		if end > open {
			colsText = body[open+1 : end]
		}
		out = append(out, dbObjCreate{name: name, table: table, cols: dbObjDeclaredKeyColumns(colsText)})
	}
	return out
}

func dbObjStripName(raw string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(raw), `"`))
}

// dbObjBareName 剥掉 schema 前缀后的【裸名】。
//
// ★★ R162 轮定位到的 R161 真缺陷（★ 差集失效 ⇒ 假缺口）：
//
//	`DROP INDEX IF EXISTS public.foo` 抓到的是 `public.foo`，
//	而 CREATE 侧存的是裸名 `foo` ⇒ `created - dropped` 永远匹配不上
//	⇒ ★★ 被后续迁移【明确退役】的索引仍然留在期望集里
//	⇒ ★★ 缺口台账于是把它们记成「三环境都没建」
//	⇒ ★★ 而它们在三个环境确实都不存在 ⇒ ⇒ ★★ **43 条全是工具造的假缺口**。
//
// ★★ ★★ 教训：**「两份独立实现互证」也可能同源同错** —— ★★ R161 的 Python 工具与
// ★★ 这份 Go 门当时**都没剥前缀** ⇒ ★★ 互证对这批零信息 ⇒ ★★ 断言「互证通过」是假的。
func dbObjBareName(raw string) string {
	return dbObjStripName(raw[strings.LastIndex(raw, ".")+1:])
}

// dbObjDeriveExpected 从仓内 startup 迁移反解期望索引集（★ 独立于 Python 工具）。
//
// ★ 三个规格（★ 与 Python 侧同规格、★ 但代码路径不同 ⇒ ★ 互证）：
//
//	① 剥 `--` 行注释后再解析（★ 注释里也有 CREATE INDEX）
//	② 中间产物索引 = 同一个文件里被 `ADD CONSTRAINT … USING INDEX <它>` 消费掉的
//	   ⇒ ★★ 判据是【结构】不是【名字带 _tmp】
//	③ 后来的迁移 DROP 掉的 ⇒ 不进期望集
func dbObjDeriveExpected(t *testing.T) (map[string]dbObjExpect, map[string]int) {
	t.Helper()
	dir := dbObjRepoPath(t, dbObjMigrationsRel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	created := map[string]dbObjExpect{} // index -> {migrationNo, table, key columns}
	dropped := map[string]bool{}
	transient := 0
	files := 0
	// ★★ R163：★ 两个解析路径的【名字集合】必须逐字相同（★ 互为交叉校验）
	// ★★ 理由：★ R162 教训「两份独立实现互证也可能同源同错」⇒ ⇒ 光有一份解析不够
	// ★★ ⇒ ⇒ 这里让【老正则】与【括号配对扫描】互相钉死：★ 任一侧单独漂移都会红。
	// ★★ ★★ 交叉校验必须比【表名】⇒ ★ 只比名字是不够的
	// ★★ ⇒ ★ 「ON ONLY」型缺陷【只改表名、不改名字】⇒ ★★ 只比名字 ⇒ ★★ 永远发现不了
	regexNames := map[string]string{}
	consumedNames := map[string]bool{}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		files++
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := dbObjReLineCm.ReplaceAllString(string(raw), "")
		head := strings.SplitN(name, "_", 2)[0]
		mig := head
		if n, err := strconv.Atoi(head); err == nil {
			mig = strconv.Itoa(n)
		} else {
			mig = "-1"
		}

		consumed := map[string]bool{}
		for _, m := range dbObjReUsing.FindAllStringSubmatch(body, -1) {
			consumed[dbObjStripName(m[1])] = true
			consumedNames[dbObjStripName(m[1])] = true
		}
		for _, m := range dbObjReCreate.FindAllStringSubmatch(body, -1) {
			tbl := dbObjStripName(m[2])
			if i := strings.Index(tbl, "."); i >= 0 {
				tbl = tbl[i+1:]
			}
			regexNames[dbObjStripName(m[1])] = tbl
		}
		for _, c := range dbObjScanCreateIndexes(body) {
			name := c.name
			if consumed[name] {
				transient++
				continue
			}
			created[name] = dbObjExpect{no: mig, table: c.table, cols: c.cols, file: name}
		}
		for _, m := range dbObjReDrop.FindAllStringSubmatch(body, -1) {
			dropped[dbObjBareName(m[1])] = true
		}
	}

	// ★★ R163：★ 两套解析的名字集合必须逐字相同（★ 任一侧单独漂移 ⇒ 这里红）
	for k, v := range created {
		rt, ok := regexNames[k]
		if !ok {
			t.Errorf("★ 括号配对扫描多出了索引 %q（★ 老正则没抓到）⇒ 期望集会漂", k)
			continue
		}
		if rt != v.table {
			t.Errorf("★ 索引 %q 的表名：★ 老正则=%q 配对扫描=%q ⇒ ★ 两侧口径不一致（★ 「ON ONLY」型缺陷【只改表名、不改名字】⇒ ★★ 只比名字就永远发现不了）", k, rt, v.table)
		}
	}
	for k := range regexNames {
		if _, ok := created[k]; !ok && !consumedNames[k] {
			t.Errorf("★ 老正则抓到了索引 %q，★ 但括号配对扫描没抓到 ⇒ 期望集会缩", k)
		}
	}

	out := map[string]dbObjExpect{}
	for k, v := range created {
		if !dropped[k] {
			out[k] = v
		}
	}
	stats := map[string]int{
		"files":             files,
		"created":           len(created),
		"dropped":           len(dropped),
		"transient":         transient,
		"expected_final":    len(out),
		"created_dropped":   len(created) - len(out),
		"non_numeric_files": 0,
	}
	return out, stats
}

// dbObjSeverity 算 severity（★ 与 Python 侧 `_severity` 同一口径，★ 但独立实现）。
//
// ★★ R161 第一版写的是「生产环境有缺口 ⇒ production-gap；否则 ⇒ all-env-gap」
// ⇒ ⇒ ★★ 于是「**只有 local 缺**」（★ 生产其实齐备）被判成 `all-env-gap`
// ⇒ ⇒ ★★ 而「**三环境全缺**」（★ 一致退役）被判成 `production-gap`
// ⇒ ⇒ ★★ **词表名与判据完全相反** ⇒ ★★ 谁读都会读反
// ⇒ ⇒ ★★ 而 R161 的门只校验计数 ⇒ ★★ **门绿掩盖了这个缺陷**
//
// ★★ 现在词表是【每个格子组合唯一确定】的 4 值：
//
//	none              —— 任何环境都不缺
//	local-only-gap    —— 只有 local 缺（★ 生产 252/211 都齐备）
//	prod-partial-gap  —— 生产环境里缺一部分
//	prod-full-gap     —— 生产环境【全都】缺（★ 252 与 211 都缺）
func dbObjSeverity(cells []string, envs []string) string {
	missing := map[string]bool{}
	for i, c := range cells {
		if c == "missing" {
			missing[envs[i]] = true
		}
	}
	if len(missing) == 0 {
		return "none"
	}
	// ★★ 生产环境 = 252 与 211（★ fresh 是新装环境，★ local 是开发环境 ⇒ 都不是生产）
	prodEnvs := []string{"252", "211"}
	prodMissing := 0
	for _, e := range prodEnvs {
		if missing[e] {
			prodMissing++
		}
	}
	// ★★ 先判生产侧（★ 它决定「用户可见的劣化」）
	if prodMissing == len(prodEnvs) {
		return "prod-full-gap"
	}
	if prodMissing > 0 {
		return "prod-partial-gap"
	}
	// ★★ 生产齐备 ⇒ 只剩 local / fresh 缺 ⇒ ★★ 按【缺的是哪一个】分三档
	if len(missing) == 1 && missing["local"] {
		return "local-only-gap"
	}
	if len(missing) == 1 && missing["fresh"] {
		return "fresh-only-gap"
	}
	if missing["fresh"] {
		// ★★ local 与 fresh 都缺、★ 而生产齐备 ⇒ ★★★ **新装环境【必然】缺**
		return "new-install-gap"
	}
	return "local-only-gap"
}

// ─────────────────────── G8：退役证据台账（★ 防止假缺口复活） ───────────────────────
//
// ★★★ 这道门是 R162 轮加的，★ 存在的理由是 R161 轮的一次真事故：
//
// ★ R161 的期望集推导里，★ 【DROP 名的 schema 前缀】让差集失效
// ⇒ ⇒ 被后续迁移【明确退役】的索引仍然留在期望集里
// ⇒ ⇒ 缺口台账把它们记成「三个环境都没建」⇒ ★★ **43 条假缺口**
// ⇒ ⇒ ★★ **任何人照台账去「补建」，就会复活被刻意退役的索引**
//
//	（★ 例：876 退役 idx_sessions_session_id 是因为它已被前缀覆盖，★ 复活它是纯浪费写入）
//
// ★★ 所以这里钉死两件事：
//
//	① ★★ 退役台账里列出的索引，★ **一个都不许出现在期望集里**
//	② ★★ 退役台账必须【真的非空】且【真的含 876 的那一条】⇒ ★★ 否则这道门是空转的
func TestDbObjectRetiredEvidenceLedger(t *testing.T) {
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjRetiredRel))
	if len(rows) == 0 {
		t.Fatal("★ retired ledger has zero rows ⇒ the gate asserts nothing")
	}
	for _, r := range rows {
		if len(r) != dbObjRetiredCols {
			t.Fatalf("retired row malformed (want %d cols, got %d): %v", dbObjRetiredCols, len(r), r)
		}
	}
	// ★★ R163：★ 两类退役证据都要在（★ 显式 DROP INDEX / ★ 删列 ⇒ PG 级联删索引）
	kinds := map[string]int{}
	for _, r := range rows {
		kinds[r[8]]++
	}
	if kinds["drop-index"] == 0 {
		t.Error("retired ledger has zero drop-index events ⇒ the explicit-DROP evidence class is missing")
	}
	if kinds["drop-column"] == 0 {
		t.Error("retired ledger has zero drop-column events ⇒ the column-cascade evidence class is missing")
	}

	// ① 期望集里不得有已退役的索引
	//
	// ★★★ R163 修正：★ 这条规则**只对 `drop-index` 类生效**。
	// ★★★ 理由：★★ 显式 `DROP INDEX` 是【顺序无关】的 ⇒ ⇒ 那个索引在任何环境都不该有
	// ★★★ ⇒ ⇒ 留在期望集里就是假缺口（★ R162 修的正是这个）。
	// ★★★ 而 `drop-column` 类（★ 删列 ⇒ PG 级联删索引）是【顺序相关】的
	// ★★★ ⇒ ⇒ 「那个列在不在」取决于**各环境的执行顺序**
	// ★★★ ⇒ ⇒ 实测：★ 211 上 708 先跑、456 后跑 ⇒ 死列复活 ⇒ 索引也复活
	// ★★★ ⇒ ⇒ 所以它【必须】留在期望集里 ⇒ ⇒ 由**每个环境的状态**判定（★ retired-by-column-drop）
	expected, _ := dbObjDeriveExpected(t)
	leaked := []string{}
	for _, r := range rows {
		if r[8] != "drop-index" {
			continue
		}
		name := r[1]
		if _, ok := expected[name]; ok {
			leaked = append(leaked, name)
		}
	}
	if len(leaked) > 0 {
		sort.Strings(leaked)
		t.Errorf("★ %d retired indexes leaked into the expected set: %v", len(leaked), trunc(leaked))
	}

	// ② 阳性对照：★ 必须含 876 退役的那一条（★ 否则台账没抓到真退役）
	found876 := false
	for _, r := range rows {
		if r[1] == "idx_sessions_session_id" && r[4] == "876" {
			found876 = true
			if r[6] != "yes" {
				t.Errorf("★ 876 的 DROP 在 DO 块内（有覆盖证明才退役）⇒ conditional 应为 yes，★ 实为 %q", r[6])
			}
		}
	}
	if !found876 {
		t.Error("★ retired ledger lost the 876 event (idx_sessions_session_id) ⇒ the gate asserts nothing")
	}
	// ★★★ R163：★ drop-column 类必须【真的】在期望集里（★ 它是按环境判定的，★ 不是全局退役）
	// ★★★ 且它必须在缺口台账里【至少一个环境】判成 `retired-by-column-drop`（★ 否则判据失灵）
	foundCol := false
	for _, r := range rows {
		if r[8] != "drop-column" {
			continue
		}
		foundCol = true
		if _, ok := expected[r[1]]; !ok {
			t.Errorf("★ drop-column 退役的 %q 不在期望集里（★ 顺序相关的退役必须留在期望集里）", r[1])
		}
		if r[9] == "" {
			t.Errorf("★ drop-column 退役的 %q 没点名被删的列", r[1])
		}
	}
	if !foundCol {
		t.Error("★ retired ledger has no drop-column event ⇒ the column-cascade evidence class is missing")
	}
	t.Logf("retired ledger: %d events", len(rows))
}

// ─────────────────────────── G1：底账形状 + sha256 自洽 ───────────────────────────

func TestDbObjectCensusSnapshotIsWellFormed(t *testing.T) {
	path := dbObjRepoPath(t, dbObjCensusRel)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// ① 每个数据行必须恰好 17 列（★ 换行污染 ⇒ 这里必须炸）
	perEnv := map[string][]string{}
	order := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != dbObjCensusCols {
			t.Fatalf("census row has %d fields, expected %d", len(parts), dbObjCensusCols)
		}
		env := parts[0]
		if _, seen := perEnv[env]; !seen {
			order = append(order, env)
		}
		perEnv[env] = append(perEnv[env], line)
	}

	// ② 三个环境必须都在
	wantEnvs := []string{"211", "252", "fresh", "local"}
	for _, e := range wantEnvs {
		if _, ok := perEnv[e]; !ok {
			t.Errorf("census missing environment %q", e)
		}
	}
	if len(perEnv) != len(wantEnvs) {
		t.Errorf("census has %d envs, want %d (%v)", len(perEnv), len(wantEnvs), order)
	}

	// ③ 头部声明的每环境 rows/sha256 必须与重算一致
	for _, e := range wantEnvs {
		rows := perEnv[e]
		if len(rows) == 0 {
			t.Errorf("env %q has no rows", e)
			continue
		}
		sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
		got := hex.EncodeToString(sum[:])
		want := ""
		for _, h := range dbObjHeaderLines(t, path) {
			if strings.Contains(h, fmt.Sprintf("env=%s rows=", e)) {
				m := regexp.MustCompile(`env=` + regexp.QuoteMeta(e) + ` rows=(\d+) sha256=([0-9a-f]+)`).FindStringSubmatch(h)
				if m == nil {
					t.Fatalf("cannot parse env header line: %s", h)
				}
				wantRows, _ := strconv.Atoi(m[1])
				if wantRows != len(rows) {
					t.Errorf("env %q header says %d rows, file has %d", e, wantRows, len(rows))
				}
				want = m[2]
			}
		}
		if want == "" {
			t.Errorf("no header line for env %q", e)
			continue
		}
		if want != got {
			t.Errorf("env %q sha256 mismatch: header=%s recomputed=%s", e, want, got)
		}
	}

	// ④ 阳性对照：★ 底账里必须真的有【分区子索引 ⇒ 父索引】的父子链
	//    （★ 若这条不成立 ⇒ 采集端没采到 pg_inherits ⇒ 「present-via-child」永远判不出来）
	states := dbObjParseCensus(t, path)
	links := 0
	for _, st := range states {
		links += len(st.children)
	}
	if links == 0 {
		t.Error("census has zero parent_index links ⇒ pg_inherits was not captured")
	}
	if len(states["211"].children) == 0 {
		t.Error("env 211 has zero partition-child links (★ 211 IS a partitioned instance)")
	}
}

// ─────────────────────────── G2：期望集 ≡ Go 独立反解 ───────────────────────────

func TestDbObjectExpectSetMatchesIndependentDerivation(t *testing.T) {
	goExpected, stats := dbObjDeriveExpected(t)
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjExpectRel))

	pyExpected := map[string]dbObjExpect{}
	for _, r := range rows {
		if len(r) != dbObjExpectCols {
			t.Fatalf("expect row has %d fields, expected %d: %v", len(r), dbObjExpectCols, r)
		}
		cols := []string{}
		for _, c := range strings.Split(r[4], ",") {
			if c != "" {
				cols = append(cols, c)
			}
		}
		pyExpected[r[1]] = dbObjExpect{no: r[0], table: r[2], cols: cols, file: r[3]}
	}

	if len(pyExpected) != len(goExpected) {
		t.Errorf("expect set size: tool=%d go=%d", len(pyExpected), len(goExpected))
	}
	var onlyTool, onlyGo []string
	for k := range pyExpected {
		if _, ok := goExpected[k]; !ok {
			onlyTool = append(onlyTool, k)
		}
	}
	for k := range goExpected {
		if _, ok := pyExpected[k]; !ok {
			onlyGo = append(onlyGo, k)
		}
	}
	sort.Strings(onlyTool)
	sort.Strings(onlyGo)
	if len(onlyTool) > 0 {
		t.Errorf("%d indexes in the tool's set but not in Go's: %v", len(onlyTool), trunc(onlyTool))
	}
	if len(onlyGo) > 0 {
		t.Errorf("%d indexes in Go's set but not in the tool's: %v", len(onlyGo), trunc(onlyGo))
	}
	for k, v := range goExpected {
		pv, ok := pyExpected[k]
		if !ok {
			continue
		}
		if pv.no != v.no || pv.table != v.table {
			t.Errorf("index %q: tool=(%s,%s) go=(%s,%s)", k, pv.no, pv.table, v.no, v.table)
			continue
		}
		// ★★ R163：★ 声明列必须逐条一致（★ 它是「列级联退役」判据的输入）
		if strings.Join(pv.cols, ",") != strings.Join(v.cols, ",") {
			t.Errorf("index %q: tool cols=%v go cols=%v", k, pv.cols, v.cols)
		}
	}

	// ★ 反向对照：★ 迁移 902 的中间产物索引**必须**不在期望集里
	//    （★ 它建完就被 ADD CONSTRAINT … USING INDEX 消费并改名）
	const transient = "stats_event_dedup_pkey_include_tmp"
	if _, ok := pyExpected[transient]; ok {
		t.Errorf("transient index %q leaked into the expected set", transient)
	}
	if _, ok := goExpected[transient]; ok {
		t.Errorf("transient index %q leaked into Go's expected set", transient)
	}
	if stats["transient"] != 1 {
		t.Errorf("Go detected %d transient indexes, want exactly 1", stats["transient"])
	}
	if stats["expected_final"] >= stats["created"] {
		t.Errorf("expected set (%d) must be smaller than created (%d) ⇒ DROP subtraction not applied",
			stats["expected_final"], stats["created"])
	}
	t.Logf("expected=%d created=%d dropped=%d transient=%d",
		stats["expected_final"], stats["created"], stats["dropped"], stats["transient"])
}

// ─────────────────────────── G3：缺口台账 ≡ Go 独立重算（★ 全量） ───────────────────────────

func TestDbObjectGapLedgerIsFullyRecomputable(t *testing.T) {
	states := dbObjParseCensus(t, dbObjRepoPath(t, dbObjCensusRel))
	colStates := dbObjParseColumns(t, dbObjRepoPath(t, dbObjColumnsRel))
	goExpected, _ := dbObjDeriveExpected(t)

	envs := []string{"local", "fresh", "252", "211"}
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjLedgerRel))
	if len(rows) != len(goExpected) {
		t.Fatalf("ledger has %d rows, expected %d", len(rows), len(goExpected))
	}

	ledger := map[string][]string{}
	for _, r := range rows {
		if len(r) != dbObjLedgerCols {
			t.Fatalf("ledger row has %d fields, expected %d: %v", len(r), dbObjLedgerCols, r)
		}
		ledger[r[1]] = r
	}

	// ★★ 全量逐条重算（★ 不是抽样 —— ★ R160 轮的教训：抽样互证有盲区）
	mismatches := 0
	for name, want := range goExpected {
		got, ok := ledger[name]
		if !ok {
			t.Errorf("ledger missing index %q", name)
			mismatches++
			continue
		}
		if got[0] != want.no || got[2] != want.table {
			t.Errorf("index %q: ledger=(%s,%s) go=(%s,%s)", name, got[0], got[2], want.no, want.table)
			mismatches++
			continue
		}
		for i, env := range envs {
			wantState := dbObjClassify(states[env], name, want.table, want.cols, colStates[env])
			if got[3+i] != wantState {
				t.Errorf("index %q env %s: ledger=%s recomputed=%s", name, env, got[3+i], wantState)
				mismatches++
			}
		}
		cells := got[3 : 3+len(envs)]
		verdict, severity := "complete", dbObjSeverity(cells, envs)
		for _, c := range cells {
			if c == "missing" {
				verdict = "gap"
				break
			}
		}
		// ★★ 列位随环境数走：★ 3 + len(envs) = verdict 列，★ +1 = severity 列
		verdictCol := 3 + len(envs)
		severityCol := verdictCol + 1
		if got[verdictCol] != verdict {
			t.Errorf("index %q: verdict=%s want %s", name, got[verdictCol], verdict)
			mismatches++
		}
		if got[severityCol] != severity {
			t.Errorf("index %q: severity=%s want %s", name, got[severityCol], severity)
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Errorf("%d ledger cells disagree with the independent recomputation", mismatches)
	}

	// 头部声明的 gaps_by_env 必须与重算一致（★ 头部不许腐坏）
	gapsByEnv := map[string]int{}
	for _, r := range rows {
		for i, env := range envs {
			if r[3+i] == "missing" {
				gapsByEnv[env]++
			}
		}
	}
	for _, h := range dbObjHeaderLines(t, dbObjRepoPath(t, dbObjLedgerRel)) {
		if !strings.Contains(h, "gaps_by_env=") {
			continue
		}
		m := regexp.MustCompile(`gaps_by_env=(\{.*\})`).FindStringSubmatch(h)
		if m == nil {
			t.Fatalf("cannot parse gaps_by_env from: %s", h)
		}
		var declared map[string]int
		if err := json.Unmarshal([]byte(m[1]), &declared); err != nil {
			t.Fatalf("parse declared gaps_by_env: %v", err)
		}
		for env, v := range gapsByEnv {
			if declared[env] != v {
				t.Errorf("header gaps_by_env[%s]=%d, recomputed %d", env, declared[env], v)
			}
		}
	}
}

// ─────────────────────────── G1b：列底账形状 + 每环境 sha256 自洽 ───────────────────────────

// TestDbObjectColumnsSnapshotIsWellFormed 钉住列底账的完整性（★ 与底账同形态）。
//
// ★★ 为什么单列一道门（★ 而不是并进缺口台账那门）：★
// ★★ 「列底账被污染」与「缺口台账与重算不一致」是【两件事】
// ⇒ ⇒ 前者查【快照完整性】，★ 后者查【判定一致性】
// ⇒ ⇒ 分开才能让变异臂各自红在【指名的那条腿】上（★ 红了 ≠ 验到了你想验的那条腿）。
func TestDbObjectColumnsSnapshotIsWellFormed(t *testing.T) {
	path := dbObjRepoPath(t, dbObjColumnsRel)
	rows := dbObjDataLines(t, path)
	if len(rows) == 0 {
		t.Fatal("★ columns artifact has zero rows ⇒ the column-retirement verdict asserts nothing")
	}
	perEnv := map[string][]string{}
	for _, r := range rows {
		if len(r) != dbObjColumnsCols {
			t.Fatalf("columns row has %d fields, expected %d: %v", len(r), dbObjColumnsCols, r)
		}
		perEnv[r[0]] = append(perEnv[r[0]], strings.Join(r, "\t"))
	}
	for _, e := range []string{"211", "252", "fresh", "local"} {
		envRows := perEnv[e]
		if len(envRows) == 0 {
			t.Errorf("columns artifact missing environment %q", e)
			continue
		}
		sum := sha256.Sum256([]byte(strings.Join(envRows, "\n")))
		got := hex.EncodeToString(sum[:])
		want := ""
		for _, h := range dbObjHeaderLines(t, path) {
			if !strings.Contains(h, e+" rows=") {
				continue
			}
			m := regexp.MustCompile(regexp.QuoteMeta(e) + ` rows=(\d+) sha256=([0-9a-f]+)`).FindStringSubmatch(h)
			if m == nil {
				continue
			}
			wantRows, _ := strconv.Atoi(m[1])
			if wantRows != len(envRows) {
				t.Errorf("env %q header says %d rows, file has %d", e, wantRows, len(envRows))
			}
			want = m[2]
		}
		if want == "" {
			t.Errorf("no header line for env %q in the columns artifact", e)
			continue
		}
		if want != got {
			t.Errorf("env %q columns sha256 mismatch: header=%s recomputed=%s", e, want, got)
		}
	}
	t.Logf("columns artifact: %d rows across %d envs", len(rows), len(perEnv))
}

// ─────────────────────────── G4：坏行必被拒 ───────────────────────────

func TestDbObjectCensusRejectsMalformedRows(t *testing.T) {
	dir := t.TempDir()

	// ★★ 夹具用切片拼（★ 不要手数 tab —— ★ 本轮手数 tab 错了三次）
	mkRow := func(cols ...string) string {
		out := make([]string, dbObjCensusCols)
		copy(out, cols)
		return strings.Join(out, "\t")
	}

	// ★ 阳性对照：★ 17 列的行必须被接受
	good := filepath.Join(dir, "good.tsv")
	writeFile(t, good, mkRow("local", "idx", "public", "i", "idx_a", "owner",
		"", "", "", "", "", "", "", "CREATE INDEX x", "true,true,true")+"\n")
	states := dbObjParseCensus(t, good)
	if len(states["local"].idx) != 1 {
		t.Errorf("well-formed census not parsed: %v", states)
	}

	// ★ 反向对照：★ 16 列（★ 少一列 ⇒ 典型的换行截断）必须被拒
	bad := filepath.Join(dir, "bad.tsv")
	short := mkRow("local", "idx", "public", "i", "idx_a", "owner",
		"", "", "", "", "", "", "", "CREATE INDEX x", "true,true,true")
	short = strings.Join(strings.Split(short, "\t")[:dbObjCensusCols-1], "\t")
	writeFile(t, bad, short+"\n")
	if _, ok := dbObjParseCensusQuiet(bad); ok {
		t.Error("a short census row was accepted ⇒ truncated rows would be read silently")
	}
}

func dbObjParseCensusQuiet(path string) (map[string]*dbObjEnvState, bool) {
	// ★ 复刻 dbObjParseCensus 的校验，但把 t.Fatalf 换成返回值（★ 不炸测试进程）
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(strings.Split(line, "\t")) != dbObjCensusCols {
			return nil, false
		}
	}
	return map[string]*dbObjEnvState{}, true
}

// ─────────────────────────── G5：四态判定 + 反向对照 ───────────────────────────

func TestDbObjectClassifierStates(t *testing.T) {
	st := &dbObjEnvState{
		idx:      map[string]bool{"idx_present": true},
		children: map[string][]string{"idx_parent": {"part_child"}},
		rels:     map[string]string{"tbl": "r", "part": "p", "viewish": "v"},
	}
	cases := []struct {
		name  string
		idx   string
		table string
		want  string
	}{
		{"present", "idx_present", "tbl", "present"},
		{"present-via-child", "idx_parent", "part", "present-via-child"},
		{"retired-absent-table", "idx_x", "gone", "retired"},
		{"retired-not-a-table", "idx_x", "viewish", "retired"},
		{"missing", "idx_x", "tbl", "missing"},
	}
	for _, c := range cases {
		if got := dbObjClassify(st, c.idx, c.table, nil, nil); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	// ★★★ R163：★ 五态的第 5 态「列级联退役」（★ 键列没了 ⇒ PG 自动删索引 ⇒ 不是缺口）
	colEnv := map[string]map[string]bool{"tbl": {"tenant_id": true, "session_id": true}}
	if got := dbObjClassify(st, "idx_x", "tbl", []string{"tenant_id", "last_full_payload_at"}, colEnv); got != "retired-by-column-drop" {
		t.Errorf("retired-by-column-drop: got %q", got)
	}
	if got := dbObjClassify(st, "idx_x", "tbl", []string{"tenant_id"}, colEnv); got != "missing" {
		t.Errorf("column present but index absent must stay missing, got %q", got)
	}
	if got := dbObjClassify(st, "idx_x", "tbl", []string{"tenant_id"}, nil); got != "missing" {
		t.Errorf("no column facts must conservatively be missing, got %q", got)
	}

	// ★★★ 反向对照（R161 第一版工具的实测漏洞）：
	// ★★ 一个【名字带前缀的视图】绝不能被当成「表存在」
	// ★★（★ `request_logs_with_current_month` 是视图，★ 而 `request_logs` 已退役
	// ★★ ⇒ ★ 靠「名字前缀」判表存在 ⇒ ★ 会把 51 个已退役索引误判成真缺口）。
	viewState := &dbObjEnvState{
		idx:      map[string]bool{},
		children: map[string][]string{},
		rels:     map[string]string{"request_logs_with_current_month": "v", "real_table": "r"},
	}
	if got := dbObjClassify(viewState, "idx_on_real_table", "real_table", nil, nil); got != "missing" {
		t.Errorf("a real table without the index must be 'missing', got %q", got)
	}
	if got := dbObjClassify(viewState, "idx_on_viewish", "request_logs_with_current_month", nil, nil); got != "retired" {
		t.Errorf("a view must not satisfy the table guard, got %q", got)
	}
}

// ─────────────────────────── G6：生产缺口棘轮（★ 只降不升） ───────────────────────────

func TestDbObjectProductionGapRatchet(t *testing.T) {
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjLedgerRel))
	envs := []string{"local", "fresh", "252", "211"}
	facts := dbObjGapFactsLoad(t)
	// ★★★ R178：★ 棘轮只数【真缺口】⇒ ★ 「还没部署到该环境」的缺口单独记为 pending。
	gaps := map[string]int{}
	pending := map[string]int{}
	for _, r := range rows {
		if len(r) != dbObjLedgerCols {
			t.Fatalf("ledger row malformed: %v", r)
		}
		for i, env := range envs {
			if r[3+i] != "missing" {
				continue
			}
			if facts.dbObjGapKind(r[0], r[1], env) == "pending" {
				pending[env]++
				continue
			}
			gaps[env]++
		}
	}
	for env, baseline := range dbObjGapRatchet {
		if gaps[env] > baseline {
			t.Errorf("★ gap ratchet broken: env %s has %d real gaps, baseline %d (★ 只许下降)",
				env, gaps[env], baseline)
		}
	}

	// ★ 阳性对照：★ 台账里必须【确实】有生产缺口行（★ 否则棘轮是空转的）
	// ★ ★ R162：★ severity 词表改成【每个格子组合唯一确定】的 4 值 ⇒ 阳性对照跟着换
	prodRows := 0
	severityCol := 3 + len(envs) + 1
	for _, r := range rows {
		if r[severityCol] == "prod-partial-gap" || r[severityCol] == "prod-full-gap" {
			prodRows++
		}
	}
	if prodRows == 0 {
		t.Error("ledger has zero production-gap rows ⇒ the ratchet asserts nothing")
	}
	// ★★ R162 新增：★ severity 词表本身必须【与格子状态一致】⇒ ★★ 词表名与语义不许再错位
	for _, r := range rows {
		want := dbObjSeverity(r[3:3+len(envs)], envs)
		if r[severityCol] != want {
			t.Errorf("index %q: severity=%s but cells ⇒ %s (★ 词表名与语义必须一致)",
				r[1], r[severityCol], want)
		}
	}
	// ★★★ R178：★ 豁免必须【可证伪】⇒ ★ 每一格 pending 都要说得出理由（★ 否则它就是白名单）。
	// ★★★ 且 pending 只能出现在【在途迁移】上 ⇒ ★ 已结算的迁移一律是真缺口。
	for _, r := range rows {
		for i, env := range envs {
			if r[3+i] != "missing" {
				continue
			}
			if facts.dbObjGapKind(r[0], r[1], env) != "pending" {
				continue
			}
			n, err := strconv.Atoi(r[0])
			if err != nil || n <= dbObjGapRatchetEpoch {
				t.Errorf("index %q is pending in %s but migration %s is not newer than epoch %d",
					r[1], env, r[0], dbObjGapRatchetEpoch)
			}
			if !facts.reachable[r[1]] {
				t.Errorf("index %q is pending in %s but its creator migration is unreachable", r[1], env)
			}
		}
	}
	t.Logf("real gaps by env: %v (baseline %v); pending by env: %v",
		gaps, dbObjGapRatchet, pending)
}

// ─────────────────────── G6b：★ 缺口分类器的自证（★ 合成用例） ───────────────────────

// TestDbObjGapKindSynthetic —— ★★ 判据是本轮新立的 ⇒ ⇒ 必须有【不依赖线上数据】的自证。
//
// ★ 四条判据各配一个用例：★ epoch 边界 · 不可达 · 已应用过 · 全新在途。
func TestDbObjGapKindSynthetic(t *testing.T) {
	f := dbObjGapFacts{
		migObjects: map[string][]string{
			"915": {"idx_new_a", "idx_new_b"},
			"913": {"idx_old_a"},
		},
		present: map[string]map[string]bool{
			"252": {"idx_old_a": true},
			"211": {"idx_old_a": true},
		},
		reachable: map[string]bool{
			"idx_new_a": true,
			"idx_new_b": true,
			"idx_old_a": true,
		},
	}
	cases := []struct {
		name string
		mig  string
		idx  string
		env  string
		want string
	}{
		{"在途迁移 + 全缺 + 可达 ⇒ 待部署", "915", "idx_new_a", "252", "pending"},
		{"同迁移的兄弟对象也全缺 ⇒ 仍是待部署", "915", "idx_new_b", "211", "pending"},
		{"已结算迁移 ⇒ 真缺口", "913", "idx_old_a", "252", "hole"},
		{"迁移号不可解析 ⇒ 保守当真缺口", "abc", "idx_old_a", "252", "hole"},
	}
	for _, c := range cases {
		if got := f.dbObjGapKind(c.mig, c.idx, c.env); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	// ★★ 反向对照（★ 变异臂的合成版）：★ 把创建迁移改成【不可达】
	// ★★ ⇒ ⇒ 在途迁移的缺口【不再豁免】⇒ ⇒ 变成真缺口。
	f2 := f
	f2.reachable = map[string]bool{"idx_new_a": false, "idx_new_b": false, "idx_old_a": true}
	if got := f2.dbObjGapKind("915", "idx_new_a", "252"); got != "hole" {
		t.Errorf("★ unreachable creator must not be exempt: got %q want hole", got)
	}
	// ★★ 反向对照二：★ 同迁移的【兄弟对象已存在】⇒ ⇒ 该环境【应用过】这条迁移
	// ★★ ⇒ ⇒ 缺的那些是真缺口（★ 迁移建了、谁删了）。
	f3 := f
	f3.present = map[string]map[string]bool{
		"252": {"idx_new_b": true},
		"211": {"idx_old_a": true},
	}
	if got := f3.dbObjGapKind("915", "idx_new_a", "252"); got != "hole" {
		t.Errorf("★ partially applied migration must count as a real gap: got %q want hole", got)
	}
}


// ─────────────────────────── 审计档与台账的一致性 ───────────────────────────

func TestDbObjectAuditDocQuotesTheLedger(t *testing.T) {
	doc := dbObjRepoPath(t, dbObjAuditRel)
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	text := string(raw)
	rows := dbObjDataLines(t, dbObjRepoPath(t, dbObjLedgerRel))
	envs := []string{"local", "fresh", "252", "211"}
	// ★★★ R178：★ 文档引用的缺口数必须与【棘轮用的同一个口径】⇒ ★ 真缺口（★ 不含待部署）。
	// ★★★ ⇒ ★ 否则同一份台账会同时有两种数（★ raw missing 与 real gaps）
	// ★★★ ⇒ ★ 审计档与门各说各的 ⇒ ★★ 那比没有还糟。
	facts := dbObjGapFactsLoad(t)
	gaps := map[string]int{}
	for _, r := range rows {
		for i, env := range envs {
			if r[3+i] != "missing" {
				continue
			}
			if facts.dbObjGapKind(r[0], r[1], env) == "pending" {
				continue
			}
			gaps[env]++
		}
	}
	// ★ 审计档必须写出每环境的缺口数（★ 数字与台账一致 ⇒ 文档不会悄悄过期）
	for _, env := range envs {
		needle := fmt.Sprintf("%s=%d", env, gaps[env])
		if !strings.Contains(text, needle) {
			t.Errorf("audit doc does not quote %q (★ numbers must match the ledger)", needle)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func trunc(s []string) []string {
	if len(s) > 8 {
		return append(s[:8:8], "...")
	}
	return s
}
