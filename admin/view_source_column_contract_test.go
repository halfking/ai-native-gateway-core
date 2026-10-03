// 本文件**必须不带 build tag**（2026-10-02，审计 §9.74）。
//
// 它原先带 `//go:build !integration`。该 tag 把整份文件排除出
// `-tags=integration` 构建，于是两件事同时坏掉：
//
//  1. 同包无 tag 的 admin/request_logs_indirect_readers_test.go 引用本文件
//     定义的 v1DirectTables ⇒ `-tags=integration` 下 admin 包**编译不过**
//     （2026-10-02 实测：vet: admin/request_logs_indirect_readers_test.go:87:
//     undefined: v1DirectTables）。
//  2. 本文件同时是 goFilesUnder 等共享 helper 的**定义处**，而 helper 被
//     sql/、tests/、domains/ 等多个包的测试引用 ⇒ 一旦加回 tag，那些包
//     在该配置下一起断。
//
// 为什么这个 tag 当初「看起来无害」：门与测试都是**纯静态分析**（go/ast +
// 文件系统遍历），不连真库，而 goFilesUnder 遍历时排除 _test.go 且**不读
// build tag**，所以扫描集与 tag 无关 —— tag 对门的行为毫无影响，纯粹只是
// 把符号从一种构建里拿掉。也就是说：加 tag 不会让门更准，只会让门在
// 另一种配置下不存在。
//
// 回归护栏：scripts/check-build-tags.sh 对**每个** tag 配置编译全部含约束
// 的包；把本行加回去，那道门会立刻红。
package admin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 会话存储解耦 v3 审计（2026-10-02）第二个类级网：视图源 + 物理表独有列。
//
// 真实故障（2026-10-02 真库复现，线上 500 已存在 ≥2 周）：
//
//	admin/credential_monitor_heatmap.go  buildHeatmapSQL
//	  ERROR: column rl.origin_stage does not exist   (SQLSTATE 42703)
//
// `request_logs_with_current_month` 在 migration 710 之后是「session 家族拼装体」，
// 冻结 113 列契约。真库 information_schema 查得：该视图 origin_stage 列数 = 0，
// 而 request_logs / request_logs_hot / session_turns 各 = 1。所以把物理表谓词
// 贴到以该视图为源的查询上，**每次调用都 500**，不是偶发。
//
// 加重它的是 `excludeSelf_test` 缺省 true（credential_monitor_heatmap.go 的
// 参数解析 + web/src/api/credential-monitor.ts 显式下发该参数），所以裸调用和
// 前端调用走的是同一条必错路径。
//
// 根因不是「有人写错了一个列名」，而是**读面对象与谓词变体没有绑定**：R49
// （2026-09-20）已经识别出「物理表变体对视图必 42703」并造了视图变体谓词，
// R50（2026-09-21）给 admin 热图补臂时把**物理表**那条内联了回去，而 R50 的
// 调用面守卫 `bg.TestProbeExclusionPredicateCallSitesR50` 的文件清单只含 bg 包
// 7 个文件 —— admin 不在扫描范围内，所以整套测试全绿。
//
// 这道门的作用不是重复 R50，而是补上「按读面对象选谓词」这个不变量的**全仓**
// 覆盖面。
//
// 粒度说明（这里第一版想过做错）：不能逐字符串字面量查。原缺陷里
// 「FROM request_logs_with_current_month」和越列谓词 `COALESCE(rl.origin_stage…)`
// 分属**两条不同的字面量**（一条 raw string、一条双引号串），逐字面量判定会
// 漏掉正在修的这一个 case。粒度取**顶层声明**（func / const / var 块）内的
// 字符串字面量并集，视图源与谓词才必然落在同一个判定域里。
//
// 用 go/ast 取字面量而不是正则扫源码：热图那条谓词是 Go 双引号串，正则容易只
// 盯着反引号 raw string 而整类漏网。

// TestNoPhysicalOnlyColumnsInViewSourcedSQL flags any SINGLE SQL literal that
// both takes request_logs_with_current_month as a source and names a column
// that exists only on the physical request_logs tables.
//
// Scope is deliberately per-literal, not per-declaration. Per-declaration was
// tried first and false-positived 13 times on correct code, because a handler
// routinely carries two independent queries: admin/session_turns_unified.go's
// serveSessionTurnsUnifiedDB reads session_turns_with_current_month (with a
// legal b.outbound_body against session_bodies_unified) at :68 and the canonical
// view at :159, and the declaration-level union conflated the two. A guard that
// flags correct code gets disabled, and a disabled guard is worse than none.
//
// The known cost of per-literal scope: it is blind to the shape that actually
// bit us. In buildHeatmapSQL the view reference and the offending predicate were
// two separate literals joined at runtime by strings.Join(whereClauses, " AND ")
// — no single literal contained both, so a per-literal rule could never have
// caught it. That shape is covered by the complementary real-database gate
// TestCredentialHeatmapSQL_ExecutesOnRealDatabase (integration tag), which
// assembles and executes the production SQL instead of pattern-matching it.
// Neither gate subsumes the other: this one is fast and needs no database but
// only sees co-located references; the other sees everything PostgreSQL rejects.
// viewSourcePhysicalOnlyColumnExemptions 是**具名豁免**：已确认的越列引用，逐条写明
// 为什么放行、跟踪在哪。
//
// 放行条件不是「门宽了」，而是「有人写下为什么，且会被 diff 审到」——与
// request_logs_stop_write_classification_test.go 的 bodiesUnaffectedJustification
// 同一范式。键是 `<相对仓库根的路径>:<行号>`，值为必须非空的理由；理由会随豁免
// 一起出现在失败输出里，所以它不能是一句空话。
// 815（2026-10-02）之后这张表是**空的**，而且这是本轮的正确终态，不是待办：
//   - admin/compression_stats.go:212 的 token_band 读端不再是越列（815 把
//     token_band 投影进视图契约）⇒ 那条具名豁免的前提消失，按本表自身的
//     规则（豁免失效必须删）必须删掉，否则它变成一个永不消失的洞。
//   - 「表为空」本身由 TestViewSourceExemptionsTableIsEmptyOrJustified 之外的
//     既有逻辑保证：checkLiteralsInFile 命中的位置若不在表里就报错，所以
//     真有新的越列出现时这道门照样会红。
//
// 留这张表（而不是删掉机制）是因为「具名豁免 + 理由必填 + 失效自检」这套
// 范式本身是对的：下一次真的出现一条确认过的越列时，写下为什么的成本远低于
// 重新发明一遍「门宽了怎么办」的讨论。
var viewSourcePhysicalOnlyColumnExemptions = map[string]string{}

func TestNoPhysicalOnlyColumnsInViewSourcedSQL(t *testing.T) {
	files, err := goFilesUnder("..")
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}
	fired := map[string]bool{}
	for _, f := range files {
		checkLiteralsInFile(t, f, fired)
	}
	// 豁免自身也要被审：行号漂移或缺陷已修之后，豁免必须跟着失效，
	// 否则它会变成一个永不消失的洞。
	for key := range viewSourcePhysicalOnlyColumnExemptions {
		if !fired[key] {
			t.Errorf("豁免 %q 已失效：该位置不再触发本门（缺陷可能已修，或代码已移动）。\n"+
				"请删除这条豁免，别让它继续占位。", key)
		}
	}
}

func checkLiteralsInFile(t *testing.T, path string, fired map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// 解析失败不是本门的事（构建会报），跳过而不是制造噪声。
		return
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		body := stripSQLLineComments(v)
		scope := analyzeViewSource(body)
		if scope == nil {
			return true
		}
		for _, col := range physicalOnlyRequestLogColumns {
			for _, ref := range findColumnRefs(body, col) {
				if !scope.bindsToView(ref) {
					continue
				}
				pos := fset.Position(lit.Pos())
				rel := relToRepoRoot(path)
				key := fmt.Sprintf("%s:%d", rel, pos.Line)
				fired[key] = true
				if why, ok := viewSourcePhysicalOnlyColumnExemptions[key]; ok {
					if strings.TrimSpace(why) == "" {
						t.Errorf("豁免 %q 的理由是空的——空理由的豁免等于没有豁免", key)
					}
					t.Logf("具名豁免放行 %s（列 %q）：%s", key, col, why)
					continue
				}
				t.Errorf("%s: 同一条 SQL 字面量里，%q 绑定到 request_logs_with_current_month，却引用了物理表独有列 %q\n"+
					"该列不在视图的冻结 113 列契约内 → SQLSTATE 42703，这条查询必然失败。\n"+
					"改用 bg.ProbeTrafficExclusionPredicateView 一类的视图变体谓词，或把数据源换成 request_logs_hot。\n"+
					"若这是已知的待修缺陷，请加进 viewSourcePhysicalOnlyColumnExemptions 并写明理由与跟踪位置。",
					key, ref.text, col)
			}
		}
		return true
	})
}

// relToRepoRoot 把 admin/xxx.go 形式的路径归一到仓库根相对路径，让豁免键与
// 运行时工作目录无关。
func relToRepoRoot(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if i := strings.Index(clean, "llm-gateway-go/"); i >= 0 {
		clean = clean[i+len("llm-gateway-go/"):]
	}
	for strings.HasPrefix(clean, "../") {
		clean = strings.TrimPrefix(clean, "../")
	}
	return clean
}

// fromJoinRE 抓 FROM / JOIN 后面绑定的 (relation, alias)。
// 末段与 canonicalViewName 相同的那一条就是视图本身。
var fromJoinRE = regexp.MustCompile(`(?i)\b(?:from|join)\s+([a-z_][\w.]*)\s*(?:as\s+)?([a-z_]\w*)?`)

const canonicalViewName = "request_logs_with_current_month"

// viewSourceScope 描述一条字面量里视图的绑定情况。
type viewSourceScope struct {
	viewAlias    string // 视图自身的别名；无别名时等于表名末段
	relCount     int    // 该字面量绑定的 FROM/JOIN 关系总数
	viewAliasSet map[string]bool
}

// bindsToView 判定一次列引用是否绑定到视图。
//
// 这一步不是可有可无的收紧，而是这道门能不能用的问题。同一条字面量里同时存在
// 多张表时，`<别的表别名>.<列>` 是完全合法的，而同一个列名恰好也存在于物理
// request_logs 上——不判别名就会把正确代码判成违规。实测两种成因：
//
//	rb.outbound_body  绑 LEFT JOIN 的 request_logs_bodies_with_current_month
//	                  （admin/session_sanitize_matches.go、data_lifecycle_blobs.go）
//	AS task_id        输出别名，源列是视图自己的 gw_task_id
//	                  （admin/memora_handlers.go buildMemoraSessionsSQL）
func (s *viewSourceScope) bindsToView(ref columnRef) bool {
	if ref.qualifier == "" {
		// 无限定列绑定到唯一的关系才可判定；多表字面量里无限定列的归属是
		// PostgreSQL 的解析问题，不是本门能判定的，跳过而不是猜。
		return s.relCount == 1
	}
	return s.viewAliasSet[ref.qualifier]
}

func analyzeViewSource(body string) *viewSourceScope {
	matches := fromJoinRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	scope := &viewSourceScope{relCount: len(matches), viewAliasSet: map[string]bool{}}
	for _, m := range matches {
		rel := strings.ToLower(m[1])
		last := rel[strings.LastIndexByte(rel, '.')+1:]
		alias := last
		if m[2] != "" {
			alias = strings.ToLower(m[2])
		}
		// 只收视图自身的别名。别名绑定到 CTE / 别的表时一律不收：
		// admin/memora_handlers.go 的 buildMemoraSessionsSQL 里
		// `FROM base b2` 的 b2 是 CTE base 的别名，而 base 的 task_id 是
		// `COALESCE(NULLIF(TRIM(gw_task_id),''),NULL) AS task_id` —— 派生自
		// 视图自己的 gw_task_id，合法。
		if last != canonicalViewName {
			continue
		}
		scope.viewAlias = alias
		scope.viewAliasSet[alias] = true
	}
	if scope.viewAlias == "" {
		return nil
	}
	return scope
}

type columnRef struct {
	qualifier string // 前导的 `alias.`，无则空
	text      string // 原始文本，用于报错信息
}

// findColumnRefs 找出字面量里对该列的**引用**，并跳过两处同形非引用：
// 输出别名定义（`... AS task_id`）与字符串字面量内部的内容。
func findColumnRefs(body, col string) []columnRef {
	var out []columnRef
	for _, loc := range columnWordRE(col).FindAllStringIndex(body, -1) {
		start, end := loc[0], loc[1]
		// `AS <col>` / `as <col>` 是别名定义，不是列引用。
		if pre := strings.TrimRight(body[:start], " \t\n"); len(pre) >= 2 {
			p := strings.ToLower(pre[len(pre)-2:])
			if p == "as" && (len(pre) == 2 || !isIdentByte(pre[len(pre)-3])) {
				continue
			}
		}
		// 反引号/单引号内部的内容不是列引用。
		if inQuote(body, start) {
			continue
		}
		ref := columnRef{text: body[start:end]}
		// 向前找 `qualifier.`
		j := start - 1
		for j >= 0 && (body[j] == ' ' || body[j] == '\t') {
			j--
		}
		if j >= 0 && body[j] == '.' {
			k := j - 1
			for k >= 0 && isIdentByte(body[k]) {
				k--
			}
			qual := strings.ToLower(body[k+1 : j])
			if qual != "" {
				ref.qualifier = qual
				ref.text = qual + "." + ref.text
			}
		}
		out = append(out, ref)
	}
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// inQuote 判断偏移是否落在单引号或反引号包裹的字符串内部。
func inQuote(s string, off int) bool {
	var q byte
	for i := 0; i < off; i++ {
		switch s[i] {
		case '\'', '"', '`':
			if q == 0 {
				q = s[i]
			} else if q == s[i] {
				q = 0
			}
		}
	}
	return q != 0
}

// stripSQLLineComments 去掉 SQL 的 `--` 行注释。
//
// 这不是洁癖，是被本门自己的第一次运行打脸打出来的：bg/shared_pick.go 的
// PickProbeModelForCredential 里，`origin_stage` **只**出现在 SQL 字面量内部的一行
// `-- R50 dual-arm exclusion, frozen-view variant (R49 自纠 S1：origin_stage 不在
// 113 列契约…)` 注释中——那句话正是在解释「origin_stage 不能用」。剥掉 Go 注释
// 不够，SQL 自带注释，而字面量被整体 unquote 后注释就变成了可匹配文本。
//
// 已知不完美：`--` 出现在 SQL 字符串字面量内部时会被多剥一点。对本门而言这是安全
// 方向（宁可少报不可误报），因为真缺陷的列引用必然在可执行文本里，不在字符串里。
var sqlLineCommentRE = regexp.MustCompile(`--[^\n]*`)

func stripSQLLineComments(s string) string {
	return sqlLineCommentRE.ReplaceAllString(s, " ")
}

// columnWordRE 给列名加词边界，避免 `task_id` 命中 `task_id_list`、
// `origin_stage` 命中 `origin_stages`。列名本身是合法标识符，逐个预编译。
func columnWordRE(col string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(col) + `\b`)
}

// goFilesUnder 走目录树收集非测试 .go 文件。跳过 .git / vendor / node_modules /
// 测试产物目录 —— 门要覆盖的是我们自己写的 SQL，不是依赖树里的。
func goFilesUnder(root string) ([]string, error) {
	var out []string
	skipDir := map[string]bool{
		".git": true, "vendor": true, "node_modules": true,
		"testdata": true, "dist": true, "build": true,
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 不可达的目录不阻断整道门
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out, err
}
