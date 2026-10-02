//go:build !integration

package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// S4 v1 退役的读面门（2026-10-02 建，§9.26 重分类）。
//
// 门的作用不变：把「绕过 canonical 视图直读 v1 宽族、且读到 session 臂恒 NULL
// 补位列」的读方变成**显式登记表**。新增一个必须登记并写明理由；登记项失效
// （文件改完、被删、或不再读那些列）时门反过来报红。
//
// # §9.26 把 39 改成 14，以及为什么原来的 39 是错的
//
// 建门时的口径是「读了 710 那 30 列补位集里的任何一列」。但 710 的 $proj$ 是
// **734 之前**的形态：734 已经把其中 27 列换成了 session_turn_details 特征层的
// 真实值（真库覆盖 99.9996%：hot 1,344/1,344 无缺失，parent 1,683,104/1,683,098）。
// 那 27 列在 session 分臂**不是空的**，所以按旧口径判成「恒 NULL」是错的，
// 而错的代价不只是多列 25 个文件——
//
//   - 族分类器（request_logs_stop_write_classification_test.go）会因此把「行级
//     有值」判成「谓词级空」，从而**拒绝正确的判定**；
//   - S4 退出判据第 2 条「39 个读方逐个改视图读法」把 39 当成分母，会让人以为
//     逐个重写是必要成本，而实际上 25 个现在就能改指视图。
//
// 现口径：补位集取自 db.RequestLogsViewPaddedSessionColumns()（**生效投影**
// withDetails=true 里形如 `NULL::` 的列），现网 6 列。同一批文件按新口径重算：
// **14 个**，且 14 个全部命中同一列 —— `id`。
//
// # 为什么 14 个不能靠补投影消解
//
// `id` 是本项目里少见的「session 侧有同名列但不是同一个东西」：v1 的
// request_logs.id 是**请求行 id**，session_turns.id 是 **turn id**，真库
// 1,515,984 组同 request_id 配对里 `r.id = t.id` 命中 **0** 次。补投影等于给读方
// 一个语义已变的同名列，比 NULL 更坏（NULL 至少看得见）。⇒ 这 14 个只能改读法。
//
// # 为什么不是「禁止」
//
// 这些读方在 v1 还在的时候是**正确**的代码，全面禁止会把 S4 之前的正常迭代
// 也堵死。正确的形状是「默认未登记 = 需要有人拍板」——与
// request_logs_stop_write_classification_test.go 的具名论证同一范式。
//
// # 口径精度（必须随登记表一起读）
//
// 本门只看 SQL 字面量的 FROM/JOIN 关系集合，分不出 SELECT 与 UPDATE...FROM /
// ON CONFLICT，所以写路径也会被计入；登记表里的多数条目是读方，但**不应**把
// 14 读成 14 条 SELECT 语句。

// s4PaddedReaderWhy 是本表所有条目的统一理由（§9.26 起）。逐条不同的部分只有
// cols —— 也就是「这个读方多读了几列补位列」这件唯一影响重写工作量的事。
//
// 为什么共用一条而不是逐条写：39 条逐条文案里 39 条都在复述同一句「读了 session
// 臂恒 NULL 的补位列」，而那句话在 §9.21 里**对 27 列是错的**（它们由 734 的
// details 层供值，非空率 99.9996%）。把错误复述 39 遍，等于给错误结论盖了 39 个
// 戳。共用一条之后，理由只有一份，改对一处就够。
//
// **本表当前为空（§9.42）**。最后一条（`cmd/compression-bench/main.go` 读 `id`）
// 已改完：去掉对 id 的依赖，行身份改用 request_id。所以这条理由现在只作为
// 「将来新增登记项时该写什么」的模板保留，**不是**任何一条现存登记的依据。
const s4PaddedReaderWhy = `S4 退出工作项（§9.26 重分类后的**真补位**阻塞项）：绕过 canonical 视图直读 v1 宽族，且读到了 session 臂**恒为 NULL**的补位列。恒 NULL 集现网只有 6 列（db.RequestLogsViewPaddedSessionColumns()），本条命中的正是其中的 ` + "`" + `id` + "`" + ` —— 它**证明不可投影**（真库 1,515,984 组同 request_id 配对里 r.id = t.id 命中 0 次：v1 是请求行 id、session 侧是 turn id），所以只能改读法：去掉对 id 的依赖，或改用 request_id 回查。v1 退役前必须完成。`

// v1DirectPaddedColumnReader 是登记表的条目：cols 是该读方今天读到的补位列集合。
type v1DirectPaddedColumnReader struct {
	cols []string
	why  string
}

// v1DirectPaddedColumnReaders 现在是**空表**（§9.42，2026-10-02）。
//
// 「空」在这里是一个**被验证过的结论**，不是「没人看过」。三条支撑：
//
//  1. 历史轨迹 39 → 14 → **1** → 0。1 那一条（cmd/compression-bench 读 `id`）
//     已改完并由 TestNoVPaddedColumnReaderRemains 把这个终态钉住。
//  2. 口径钉在**生效投影**（withDetails=true 的 `NULL::` 形态）而不是 710 的 $proj$，
//     权威源是 db.RequestLogsViewPaddedSessionColumns()（§9.29.1）。早先的
//     「30 列」口径是钉在 710 上算出来的，本表曾据此报出 14 个假读方——
//     门拿错误源与错误派生式互相校验，所以它一直绿着。
//  3. 判定按**限定符归属**（列绑到哪张表），不按「这个词在字面量里出现过」
//     （§9.29.4）。列名撞车会让登记表凭空膨胀。
//
// 为什么登记表要能是空的：本门的形状是「默认未登记 = 需要有人拍板」。
// 若把已改完的读方留着，门会因「登记项失效」报红（这正是它 2026-10-02 的行为，
// 是对的）；而若为了让它绿而把失效项改成豁免，表就退化成永不更新的占位。
var v1DirectPaddedColumnReaders = map[string]v1DirectPaddedColumnReader{}

// TestNoVPaddedColumnReaderRemains 把「读面已收口」这个终态钉成一道门。
//
// 为什么要单独一道：§9.32.3 记着，本项目里最贵的两个决定都是**不做**
// （不补 `id` 投影、不补 `trace_events` 投影），而「不做」没有任何编译期或
// 运行时信号——半年后有人「顺手把洞补齐」，两个洞同时打开且无人察觉。
// 空表本身不会被任何人看见，只有「它必须保持空」这件事需要有人守。
//
// 断言的是**计数为零**而不是「某些具体文件不在表里」：本门要守的是
// 「全仓没有任何读方直读 v1 且读到补位列」这个不变量。逐个点名文件会在有人
// 新增第 2 个读方时给出误导性的通过。
func TestNoVPaddedColumnReaderRemains(t *testing.T) {
	if len(v1DirectPaddedColumnReaders) != 0 {
		t.Errorf("v1DirectPaddedColumnReaders 有 %d 条登记，但 S4 读面判据第 2 条已收口（应为 0 条）。\n"+
			"若确有新的真补位读方，那它说明 S4 读面**重新**被阻塞了，请先在审计文档里记一条再登记；\n"+
			"若只是想让它变绿而登记一条已改完的读方，请删掉该条——本门的设计是「失效即报红」。",
			len(v1DirectPaddedColumnReaders))
	}
}

// v1DirectTables 是「绕过视图直读」判定里的 v1 宽族关系名。
var v1DirectTables = map[string]bool{
	"request_logs": true, "request_logs_hot": true,
	"request_logs_bodies": true, "request_logs_bodies_hot": true,
}

const canonicalView = "request_logs_with_current_month"

func TestNoUnregisteredVPaddedColumnReader(t *testing.T) {
	files, err := goFilesUnder("..")
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}
	fired := map[string]map[string]bool{} // file -> 读到的补位列
	var unattributed []string
	for _, f := range files {
		rel := relToRepoRoot(f)
		cols, unattr := paddedColumnsReadFromV1Direct(f)
		for _, c := range unattr {
			unattributed = append(unattributed, rel+":"+c)
		}
		for col := range cols {
			if fired[rel] == nil {
				fired[rel] = map[string]bool{}
			}
			fired[rel][col] = true
		}
	}
	// 「不可归属」必须可见。判归属的口径宁可保守也不猜，但**不猜**不等于
	// **静默丢弃**：多关系字面量里的裸补位列既不计入登记表，也必须在输出里
	// 点名，否则这道门就多了一个没人看得见的失明区。
	sort.Strings(unattributed)
	checkUnattributablePaddedRefs(t, unattributed)

	// 未登记 = 需要有人拍板。
	var unreg []string
	for f := range fired {
		if _, ok := v1DirectPaddedColumnReaders[f]; !ok {
			unreg = append(unreg, f)
		}
	}
	sort.Strings(unreg)
	for _, f := range unreg {
		cols := sortedKeys(fired[f])
		t.Errorf("%s: 新增了「绕过 %s 直读 v1 宽族、且读了补位列 %s」的读方。\n"+
			"它在 v1 存活期间是正确代码，但改指视图后 session 分臂会从这些列拿到 NULL 而接口返回 200。\n"+
			"若已评估过，请登记进 v1DirectPaddedColumnReaders 并写明理由（审计文档 §9.21）。",
			f, canonicalView, strings.Join(cols, ", "))
	}

	// 已登记的读方**多读一列**补位列，文件级判定看不见，但重写工作量会变。
	// 只钉「文件在不在表里」的门会漏掉这一类（变异验证实测：给 admin/analytics.go
	// 追加一个 client_model 过滤条件，门不响）。
	for f, e := range v1DirectPaddedColumnReaders {
		recorded := map[string]bool{}
		for _, c := range e.cols {
			recorded[c] = true
		}
		var grown []string
		for c := range fired[f] {
			if !recorded[c] {
				grown = append(grown, c)
			}
		}
		sort.Strings(grown)
		if len(grown) > 0 {
			t.Errorf("%s: 已登记读方新读了补位列 %s（登记集合为 %s）。\n"+
				"该文件不会出现在「新增读方」清单里，但 v1 退役前要重写的列又多了一列。\n"+
				"请同步更新 v1DirectPaddedColumnReaders 里的 cols。",
				f, strings.Join(grown, ", "), strings.Join(e.cols, ", "))
		}
	}

	// 反向：登记项失效也要报红，否则表会变成永不更新的占位。
	//
	// 注意 (路径, 原因) 必须成对收集再排序：早先的写法把原因串收进一个 slice、
	// 排序后拿 registryOrder()[i] 去配对——排的是**原因**不是**路径**，多条失效时
	// 会把原因报到别的文件头上。单条失效时恰好对，所以是靠运气过的。
	type staleEntry struct{ path, why string }
	var stale []staleEntry
	for f, e := range v1DirectPaddedColumnReaders {
		if strings.TrimSpace(e.why) == "" {
			t.Errorf("登记 %q 的理由是空的——空理由的登记等于没有登记", f)
		}
		if _, still := fired[f]; !still {
			stale = append(stale, staleEntry{f, "该位置不再触发本门（读方已改完、被删或不再读补位列）"})
			continue
		}
		var lost []string
		for _, c := range e.cols {
			if !fired[f][c] {
				lost = append(lost, c)
			}
		}
		if len(lost) > 0 {
			sort.Strings(lost)
			stale = append(stale, staleEntry{f, "登记了补位列 " + strings.Join(lost, ", ") + "，但该读方已不再读它们"})
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].path < stale[j].path })
	for _, e := range stale {
		t.Errorf("登记 %q 已失效：%s。\n"+
			"请同步更新 v1DirectPaddedColumnReaders，别让它继续占位。", e.path, e.why)
	}

	t.Logf("v1 直读 + 读补位列的读方：%d 个（其中已登记 %d，未登记 %d）",
		len(fired), len(v1DirectPaddedColumnReaders), len(unreg))
}

// paddedColumnsReadFromV1Direct 返回该文件里「以 v1 宽族为源、且同一字面量里没有
// canonical 视图」的补位列引用，**按列绑定到哪张表判定**，不按「这个词出现过」。
//
// 为什么必须判归属（2026-10-02 修正，§9.28.6）：第一版只问
// `len(findColumnRefs(body, col)) > 0`，把**整个字面量里任何位置**的该列名都算进来。
// 于是一条 `FROM request_logs_hot rl JOIN providers p ON … WHERE p.provider_id = $1`
// 会因为 `provider_id` 出现过而命中——可那一列绑的是 `providers` 表，v1 根本没被读它。
// 后果是登记表从 1 膨胀到 14，S4 判据第 2 条的分母被抬高 14 倍。
//
// 判定规则（与 §9.18 的 AST 门同款，但这里只需要别名归属，不需要完整的绑定图）：
//
//	限定列 `<qual>.<col>`  → qual ∈ 本字面量的 v1 别名集合 ⇒ 命中
//	无限定列 `<col>`        → 仅当本字面量**只有 1 个关系**且它是 v1 ⇒ 命中
//	其余                    → 不可归属，不计入（但由调用方登记到 unattributable，
//	                          以便「不计入」这件事在 -v 输出里可见，而不是静默）
//
// 不可归属的形状（多关系字面量里的裸列）无法在不引入完整 SQL 绑定分析的前提下
// 判对。§9.18 的教训在这里同样适用：宁可让不可归属的形状在日志里点名，也不
// 把它猜成命中或猜成不命中。
func paddedColumnsReadFromV1Direct(path string) (cols map[string]bool, unattributable []string) {
	cols = map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return cols, nil // 构建会报解析错误，不是本门的事
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

		hasV1, hasView := false, false
		v1Aliases := map[string]bool{}
		relCount := 0
		for _, m := range fromJoinRE.FindAllStringSubmatch(body, -1) {
			rel := strings.ToLower(m[1])
			last := rel[strings.LastIndexByte(rel, '.')+1:]
			relCount++
			alias := strings.ToLower(m[2])
			if v1DirectTables[last] {
				hasV1 = true
				// 无 AS 别名时，SQL 允许用表自身名引用（FROM request_logs_hot
				// 后写 request_logs_hot.id 合法）。有别名时表名不可再引用。
				if alias != "" {
					v1Aliases[alias] = true
				} else {
					v1Aliases[last] = true
				}
			}
			if last == canonicalView {
				hasView = true
			}
		}
		if !hasV1 || hasView {
			return true
		}
		singleV1Relation := relCount == 1
		for col := range sessionArmNullPaddedColumns {
			for _, ref := range findColumnRefs(body, col) {
				switch {
				case ref.qualifier != "":
					if v1Aliases[ref.qualifier] {
						cols[col] = true
					}
					// 限定到别的关系 ⇒ 不是 v1 读方读这一列，不计。
				case singleV1Relation:
					cols[col] = true
				default:
					unattributable = append(unattributable, col)
				}
			}
		}
		return true
	})
	return cols, dedupeSorted(unattributable)
}

func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// unattributablePaddedRefExemptions 是「不可归属」形状的**具名豁免**，
// 键为 `<仓库根相对路径>:<列名>`，值为必须非空的手验理由。
//
// 为什么需要它：多关系字面量里的裸补位列无法在不引入完整 SQL 绑定分析的前提下
// 判对。选项只有三个——猜命中（把 1 膨胀成 14，已犯过）、猜不命中（静默失明）、
// 或者**点名 + 具名手验**。这里选第三个。
//
// 与 v1DirectPaddedColumnReaders 的区别：那份表说「这个读方读 v1 的某个补位列，
// 改指视图会拿到 NULL」；这份表说「这处引用语法上不可归属，我手验过它绑的不是
// v1」。后者一旦 SQL 变了就自动失效（下面的自检会报红），所以它不会变成
// 一个永不更新的占位。
var unattributablePaddedRefExemptions = map[string]string{
	"domains/streaming/model_alternatives.go:id": "手验（2026-10-02）：该文件里读 v1 的那条 " +
		"字面量（`FROM request_logs_hot WHERE ts > … AND success AND canonical_model IS NOT NULL " +
		"GROUP BY canonical_model`）只读 canonical_model / ts / success，**不读任何补位列**。" +
		"被点名的裸 `id` 在另一条字面量里（`ORDER BY id` 与 models_canonical 侧），那条字面量" +
		"虽因内嵌 CTE 而同时含 request_logs_hot，但 id 绑的是 models_canonical。",
}

// 关于 `bg/auto_route_settle_worker.go:id` 这条豁免为什么**被删掉**而不是被改写
// （§9.45）：
//
// 它的形状是「一条同时含 v1 关系与派生表的字面量里，裸 `id` 不可归属」。§9.44 把
// settleBatch 的 SQL 从 worker 体内搬进 `bg/auto_route_settle_sql.go` 的两个纯函数，
// 于是那条字面量被拆成若干片段，**每一片都不含任何关系名**（关系名是拼进去的
// `src.TurnsTable`）⇒ 本门在那个文件里不再产生任何命中，豁免按「失效即报红」的
// 设计报了出来。
//
// 正确处置是**删掉**，理由有两条，缺一不可：
//
//  1. 豁免的语义是「这处不可归属的裸列，我手验过它绑的不是 v1」。删掉后形状确实
//     不再存在，这句话对**今天**仍然成立。
//  2. 但删掉会让 `bg/auto_route_settle_sql.go` 对本门**完全隐形**——不是「判定为
//     干净」，而是「看不见」。这一点由 §9.45 记录：全仓共 48 处 SQL 关系名不是字面量，
//     其中 `requestLogsSource` / `logsSourceFromSQL` / `requestLogsFromClause` /
//     `boardRequestLogsFromClause` 这四个**切换层**在 `days <= 7` 时直接返回
//     `request_logs_hot`，6 个调用点对本门不可见。
//
// 也就是说：本门今天的「0 个读方」结论**方向正确但不完整**——它是在一个漏掉
// 拼接式 SQL 的测量面上得到的。settle worker 那两个读点另有更强的覆盖
// （bg/auto_route_settle_source_test.go 的 TestSettleLegsAllUseTheSameSource 与
// TestSettleWorkerDelegatesToTheSQLBuilders 断言 SQL 里只能出现 src.TurnsTable、
// 不得出现任何字面量表名），所以删掉这条豁免**不会**降低该读点的守门强度。
//
// 四个切换层的 6 个调用点经手验**当前没有读补位列**（admin/usage_credits.go 与
// maas/usage.go、maas/credit_buckets.go 对 6 个补位列零匹配；
// maas/consumption_detail.go 的 `id` 全部限定在 maas_settings / providers /
// credentials / models_canonical，不是 request_logs 的别名），所以这是**潜在**盲区
// 而非现网漏网。复现工具见 cmd/tools/sql_source_indirection_audit。

// checkUnattributablePaddedRefs 要求每个「不可归属」命中都在具名豁免表里且理由非空，
// 并反过来要求每条豁免**仍然命中**（SQL 改了就失效，逼人复核而不是让它躺平）。
func checkUnattributablePaddedRefs(t *testing.T, hits []string) {
	t.Helper()
	got := map[string]bool{}
	for _, h := range hits {
		got[h] = true
		why, ok := unattributablePaddedRefExemptions[h]
		if !ok {
			t.Errorf("不可归属的裸补位列引用 %q 未登记。\n"+
				"多关系字面量里的裸列无法机械判定归属，请**手验**它绑的是哪张表：\n"+
				"  绑 v1  → 加进 v1DirectPaddedColumnReaders（改指视图会拿到 NULL）；\n"+
				"  绑别的 → 加进 unattributablePaddedRefExemptions 并写明机制。\n"+
				"不要靠「看起来像」决定——本表历史上就是靠「出现过」判的，"+
				"把 1 膨胀成了 14。", h)
			continue
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("不可归属豁免 %q 的理由是空的——空理由的豁免等于没有豁免。", h)
		}
	}
	for key := range unattributablePaddedRefExemptions {
		if !got[key] {
			t.Errorf("不可归属豁免 %q 已失效：该形状不再出现（SQL 改过或列不再被读）。\n"+
				"请删掉这条豁免，别让它继续占位。", key)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
