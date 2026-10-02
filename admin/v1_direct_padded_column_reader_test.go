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
const s4PaddedReaderWhy = `S4 退出工作项（§9.26 重分类后的**真补位**阻塞项）：绕过 canonical 视图直读 v1 宽族，且读到了 session 臂**恒为 NULL**的补位列。恒 NULL 集现网只有 6 列（db.RequestLogsViewPaddedSessionColumns()），本条命中的正是其中的 ` + "`" + `id` + "`" + ` —— 它**证明不可投影**（真库 1,515,984 组同 request_id 配对里 r.id = t.id 命中 0 次：v1 是请求行 id、session 侧是 turn id），所以只能改读法：去掉对 id 的依赖，或改用 request_id 回查。v1 退役前必须完成。`

// v1DirectPaddedColumnReader 是登记表的条目：cols 是该读方今天读到的补位列集合。
type v1DirectPaddedColumnReader struct {
	cols []string
	why  string
}

var v1DirectPaddedColumnReaders = map[string]v1DirectPaddedColumnReader{
	"admin/logs.go":                           {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"admin/probe_history.go":                  {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"admin/providers.go":                      {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"admin/routing.go":                        {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"admin/swim_lane_init.go":                 {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"bg/auto_index_refresher.go":              {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"bg/auto_route_settle_worker.go":          {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"bg/credential_recovery.go":               {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"bg/credential_selfcheck.go":              {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"bg/model_probe.go":                       {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"bg/today_success_probe.go":               {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"cmd/compression-bench/main.go":           {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"db/db.go":                                {cols: []string{"id"}, why: s4PaddedReaderWhy},
	"domains/streaming/model_alternatives.go": {cols: []string{"id"}, why: s4PaddedReaderWhy},
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
	for _, f := range files {
		rel := relToRepoRoot(f)
		for col := range paddedColumnsReadFromV1Direct(f) {
			if fired[rel] == nil {
				fired[rel] = map[string]bool{}
			}
			fired[rel][col] = true
		}
	}

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
// canonical 视图」的补位列引用。
func paddedColumnsReadFromV1Direct(path string) map[string]bool {
	out := map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return out // 构建会报解析错误，不是本门的事
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
		for _, m := range fromJoinRE.FindAllStringSubmatch(body, -1) {
			rel := strings.ToLower(m[1])
			last := rel[strings.LastIndexByte(rel, '.')+1:]
			if v1DirectTables[last] {
				hasV1 = true
			}
			if last == canonicalView {
				hasView = true
			}
		}
		if !hasV1 || hasView {
			return true
		}
		for col := range sessionArmNullPaddedColumns {
			if len(findColumnRefs(body, col)) > 0 {
				out[col] = true
			}
		}
		return true
	})
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
