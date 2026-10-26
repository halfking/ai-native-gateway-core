// request_logs_indirect_readers_test.go — 「v1 读法是间接的」读点登记与门
// （2026-10-02 审计 §9.49）。
//
// # 这张表为什么存在
//
// §9.45 已经查清：S4 退役规划里有一批**集中式切换层**（maas.requestLogsSource、
// admin.requestLogsFromClause、…）把表名收进 Go 标识符再拼进 SQL，于是
//
//	`SELECT … FROM ` + logsTable + ` WHERE …`
//
// 在任何「扫文件找 v1 表名」的器眼里，关系名**不存在**。
//
// §9.49 是在自己的回归上撞见这件事的：§9.43 把 settle worker 的
// `LEFT JOIN _request_logs_hot rl` 改成 `LEFT JOIN " + src.TurnsTable + " rl`，
// 于是一夜之间**三道独立的门同时变红**，而它们全都是绿的：
//
//	requestLogsReadInventory        按行正则 `from\s+_request_logs` 扫
//	paddedColumnsReadFromV1Direct  从 SQL 字面量里抽关系名
//	sourceFamilyOf                 剥注释后匹配族正则
//
// 三个器眼，三种实现，同一个前提：**关系名是字面量**。
//
// # 为什么不能用「加条注释让它看见」
//
// 那会让计数变对而**理由是错的**：扫描器扫的是原始行文本，注释里的
// `from _request_logs` 会被计成一个读点。这是**伪造测量**——门绿了，测的却不是
// 代码里真实存在的东西。§9.45 记过同族的事：子串门被注释喂饱。
// ⇒ 唯一诚实的做法是让器眼**知道**有间接读点，而不是骗它。
//
// # 这张表**不**能发现新的间接读点
//
// 必须说清楚，否则它会变成又一张「看起来在防、实际测不出」的表：机器无法判定
// 「一个没有 v1 字面量的文件是不是通过某种 Go 表达式在读 v1」。
// 本表覆盖的是**已知**的间接读点，加上一道失效自检（下面的
// TestIndirectRequestLogsReadersAreStillIndirect）。
// 新的间接读点仍需靠 cmd/tools/sql_source_indirection_audit 人工发现后登记。
// 这也是本表不给 CI 用的理由。

package admin

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// indirectReader 描述一个「v1 读法是间接的」文件。
type indirectReader struct {
	// Family 是它应归入的族。与 sourceFamilyOf 的返回值同一套取值，
	// 但**必须显式登记**——器眼看不见它，只能由人判定。
	Family string

	// ResolvesTo 是它间接解析到的 v1 关系名。必填，且必须是 v1DirectTables
	// 里的真表名，这样「它读的是 v1」这句话本身是可核的，而不是断言。
	ResolvesTo string

	// Reason 说明间接机制（哪一层切换、表名怎么进来）。必填非空。
	Reason string

	// SwitchFunc 是**返回关系名的那个 Go 函数名**（如 `sessionBodiesFromSQL`）。
	// 非空时，本文件被认作一个「切换层」，其**调用点**也是 v1 读方。
	//
	// # 为什么需要它（§9.232）
	//
	// §9.230 只有一个切换层（bodies 腿），2 个消费点。那时「把消费点列进
	// 登记���」还负担得起。§9.231 判定 A 类 13 个读方可换之后，
	// 消费点变成 15 个 —— 而它们**每一个**都会从
	// `scanV1BodiesReaders`（bodies 退役门总体）里消失。
	//
	// ⇒ 必须让「消费点」成为**机器可算**的量，而不是每加一个手填一行。
	// 逐个手填的失败形态已经实测过一次（§9.230.3）：忘了填 ⇒ 总体静缩
	// 15 次，而那道 bodies 门**本来就故意红**，所以 15 次都没有信号。
	//
	// ⚠ 消费点**不是**各自登记的间接读方 —— 它们是**已登记切换层的下游**。
	// 混进 indirectRequestLogsReaders 会让那张表同时装「切换层」和「调用方」
	// 两种东西，ResolvesTo 也会开始出现重复登记。
	SwitchFunc string
}

var indirectRequestLogsReaders = map[string]indirectReader{
	// 890 之后：间接读 v1 的文件为零。
	//
	// 三条曾经在这里的登记为什么都走了（2026-10-20 实测）：
	//
	//	① db/request_logs_view_schema.go —— `SessionBodiesSourceSQL()` 的
	//	   v1 臂已被删掉，函数体恒等于 `SessionFamilyBodiesSourceSQL()`
	//	   （db/request_logs_view_schema.go:807-809）⇒ 它不再能解析到 v1。
	//	② bg/auto_route_settle_sql.go —— `settleSourceFor()` 恒返回
	//	   `session_turns_hot`（bg/auto_route_settle_source.go:80-87），
	//	   写门不再决定读哪一族 ⇒ 不再是「条件性读 v1」。
	//	③ admin/auto_route_outcome_freshness.go —— 与 ② 同一个
	//	   `bg.ActiveSettleTurnsTable()`，同一条推理。
	//
	// ⚠ 这张表**保留**（不删）：它守的是「不许出现新的间接读 v1」这条不变式，
	// 而间接读点恰恰是**行扫描看不见**的那一类（§9.49）。
	// 清空它不是「这张表没用了」，是「间接读 v1 的总体归零」。
	//
	// ⚠ 新增登记的规矩不变：ResolvesTo 必须是**真表名**、Reason 必填非空、
	// Family 必须是已声明的族（见 TestIndirectRequestLogsReadersAreDeclaredWell）。
}

// TestIndirectRequestLogsReadersAreDeclaredWell 挡住「登记了但说不清」。
//
// # 890 之后（2026-10-20）：本表为空 ⇒ 循环体空转 ⇒ 判据必须有牙
//
// 空表 + 循环 = **恒绿**。⇒ 本门拆成两半：
//
//	① **表必须为空**（间接读 v1 的总体归零）；
//	② 登记表的自检逻辑本身必须有效 —— 用**合成条目**做阳性对照：
//	   一个缺 Reason / 缺 ResolvesTo / ResolvesTo 不在 v1DirectTables 里 /
//	   Family 不在已声明族里的条目，**必须**被自检逻辑判出问题；
//	   一个各项都合规的条目，**必须**不被判出问题。
//
// ⇒ ② 保证「将来有人往表里填一条，它会立刻被拦下 / 放行」这件事是真的，
// 而不是「表空着所以什么都没发生」。
func TestIndirectRequestLogsReadersAreDeclaredWell(t *testing.T) {
	families := map[string]bool{
		familyBodies: true, familyBodiesMixed: true,
		familyView: true, familyBase: true,
	}

	// ① 间接读 v1 的总体必须为空。
	if len(indirectRequestLogsReaders) > 0 {
		var files []string
		for f := range indirectRequestLogsReaders {
			files = append(files, f)
		}
		sort.Strings(files)
		t.Errorf("indirectRequestLogsReaders 仍有 %d 条登记 —— 890 之后 v1 族已物理删除，"+
			"不再存在间接读 v1 的文件：\n  %s\n"+
			"若某条登记其实已改指会话族，请删掉它；若它仍能解析到 v1，那是生产缺陷，"+
			"请登记进 retiredV1ReferenceExceptions 而不是留在这里。",
			len(files), strings.Join(files, "\n  "))
	}

	// ② 自检逻辑的阳性/阴性对照（合成条目，不碰真实表）。
	ok := indirectReader{
		Family:     familyBase,
		ResolvesTo: "_request_logs_hot",
		Reason:     "合成条目：各项合规",
	}
	if problems := indirectReaderProblems("synthetic/ok.go", ok, families); len(problems) != 0 {
		t.Errorf("一个各项都合规的合成条目被判出问题 %v —— 自检逻辑本身坏了", problems)
	}
	bad := map[string]indirectReader{
		"缺 Reason":     {Family: familyBase, ResolvesTo: "_request_logs_hot"},
		"缺 ResolvesTo": {Family: familyBase, Reason: "合成条目"},
		"ResolvesTo 不在 v1DirectTables": {
			Family: familyBase, ResolvesTo: "not_a_v1_table", Reason: "合成条目",
		},
		"Family 不在已声明族": {
			Family: "family_unknown", ResolvesTo: "_request_logs_hot", Reason: "合成条目",
		},
	}
	for name, e := range bad {
		if problems := indirectReaderProblems("synthetic/"+name, e, families); len(problems) == 0 {
			t.Errorf("合成条目「%s」没有被自检逻辑判出问题 —— 自检逻辑没有牙（表空着的时候，"+
				"这是唯一还能证明它有效的手段）", name)
		}
	}
}

// indirectReaderProblems 是一条间接读方登记的**自检**（返回问题列表，空 = 合格）。
//
// 为什么抽成函数：表清空之后，循环体不再执行 ⇒ 「自检逻辑有没有牙」就没人证明了。
// 抽出来才能在合成条目上直接驱动它。
func indirectReaderProblems(file string, e indirectReader, families map[string]bool) []string {
	var problems []string
	if strings.TrimSpace(e.Reason) == "" {
		problems = append(problems, "Reason 为空——空理由的登记等于没有登记")
	}
	if strings.TrimSpace(e.ResolvesTo) == "" {
		problems = append(problems, "ResolvesTo 为空")
	} else if !v1DirectTables[e.ResolvesTo] {
		problems = append(problems, "ResolvesTo 不在 v1DirectTables 里")
	}
	if !families[e.Family] {
		problems = append(problems, "Family 不是已声明的族")
	}
	return problems
}

// TestIndirectRequestLogsReadersAreStillIndirect 是这张表的失效自检。
//
// # 890 之后（2026-10-20）：从「登记不许腐烂」翻成「切换层不许再能选到 v1 臂」
//
// 原两道检查（① 登记的文件必须仍然读不到直接字面量；② 文件必须仍然存在）
// 在表清空之后都变成空转 ⇒ 本门改守**它们当初要防的那个东西本身**：
//
//	**间接读点的机制还在（切换层还在），但切换层已经选不到 v1 了。**
//
// ⇒ 断言两个切换层当前解析到的关系**不是**已退役的 v1 族：
//
//   - db.SessionBodiesSourceSQL()：bodies 读端切换层（v1 臂已删，恒等于会话族）
//   - bg.ActiveSettleTurnsTable()：settle worker 的 turns 源（写门不再决定读哪一族）
//
// 这两条是**正向**断言（说清楚它们现在读的是什么），不是「没找到坏东西」式的空断言。
//
// ⚠ 顺带守住的次级不变式：同一文件不得同时出现在直接表与间接表（否则算两次）。
func TestIndirectRequestLogsReadersAreStillIndirect(t *testing.T) {
	root := repoRootFromCaller(t)

	// ① 表必须为空（总体归零）。逐条检查见上一道门。
	if len(indirectRequestLogsReaders) > 0 {
		t.Errorf("indirectRequestLogsReaders 仍有 %d 条登记 —— 890 之后不存在间接读 v1 的文件", len(indirectRequestLogsReaders))
	}

	// ② 切换层当前解析到的关系必须不是已退役的 v1 族。
	//
	// ⚠ 这里判的是**返回值**，不是源码文本：helper 内部怎么写的（写门、开关、
	// 常量折叠）都不影响调用点实际会发出什么 SQL ⇒ 判返回值才是不受实现细节
	// 影响的判据。
	bodiesSrc := dbpkg.SessionBodiesSourceSQL()
	if hits := retiredV1RelationsInSQL(bodiesSrc); len(hits) > 0 {
		t.Errorf("db.SessionBodiesSourceSQL() 解析到了已退役的 v1 族关系 %v —— "+
			"它仍能选到 v1 臂 ⇒ 间接读 v1 的通道还开着", hits)
	}
	if bodiesSrc != dbpkg.SessionFamilyBodiesSourceSQL() {
		t.Errorf("db.SessionBodiesSourceSQL() ≠ db.SessionFamilyBodiesSourceSQL() —— " +
			"bodies 读端切换层不再是「恒等于会话族」，它仍可能选到别的源")
	}
	settleTable := bg.ActiveSettleTurnsTable()
	if hits := retiredV1RelationsInSQL(settleTable); len(hits) > 0 {
		t.Errorf("bg.ActiveSettleTurnsTable() = %q，解析到了已退役的 v1 族关系 %v —— "+
			"settle worker 的 turns 源仍能选到 v1", settleTable, hits)
	}
	if settleTable != "session_turns_hot" {
		t.Errorf("bg.ActiveSettleTurnsTable() = %q，期望 session_turns_hot —— "+
			"写门不再决定读哪一族（890 之后恒为会话族热表）", settleTable)
	}

	// ③ 同一文件不得同时出现在直接表与间接表。
	direct := scanRequestLogsReaders(t, root)
	for file := range indirectRequestLogsReaders {
		if n := direct[file]; n != 0 {
			t.Errorf("间接读点登记 %q：现在能被直接扫到 %d 处 —— 间接性已经消失，请从本表删掉", file, n)
		}
		if _, dup := requestLogsReadInventory[file]; dup {
			t.Errorf("%s 同时出现在 indirectRequestLogsReaders 与 requestLogsReadInventory —— 只能算一次", file)
		}
	}
}

// productionGoFiles 列出仓库里全部非 _test.go 的生产 .go 文件（仓库根相对路径）。
//
// ⚠ 与 scanRequestLogsReaders / scanV1BodiesReaders 各自那份 WalkDir **共用**
// 同一套跳过目录（.git/docs/node_modules/vendor）。三份各写一遍的代价不是
// 「重复」，而是**将来某一份加了 vendor 跳过、另一份没加**，
// 于是「某个目录里的读方在一道门里存在、在另一道门里不存在」——
// 又是一次无声的总体漂移。
var nonSourceDirs = map[string]bool{
	".git": true, "docs": true, "node_modules": true, "vendor": true,
	".audit": true, ".audit-validation": true, ".audit-workspace": true,
}

func productionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if nonSourceDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), "_test.go") {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	sort.Strings(out)
	return out
}

// indirectSourceConsumers 返回「已登记切换层的调用点」文件集合（仓库根相对）。
//
// ⚠ 判据是**函数调用**（`fn(`），不是函数**定义**。
// 定义所在的那个文件自己有 `func fn(`，必须排除，否则切换层会把自己
// 也算成自己的消费点 —— 而它已经被 indirectRequestLogsReaders 登记过一次，
// 算两次会让「切��层 vs 消费点」这个区分彻底失效。
// ⚠ 这个函数**很贵**：一次调用 = 一次全仓 WalkDir + 重读全部生产 .go。
// 而 `isSwitchConsumerFile` 是**逐文件**调它的（族分类器 6 个调用点 ×
// ~110 个已分类文件 ⇒ ~660 次全仓 walk）。实测没有缓存时全量 admin 门
// 从 509s 涨到 25 分钟以上。⇒ 每进程算一次。
//
// 判据的**结论不变**（同一批文件），只是不再重复算。
// 缓存的失效条件是「本轮测试期间源码被改」—— `go test` 不会在运行中改源码，
// 所以按进程缓存是安全的。
var (
	consumersOnce  sync.Once
	consumersCache map[string]bool
)

func indirectSourceConsumers(t *testing.T, root string) map[string]bool {
	t.Helper()
	consumersOnce.Do(func() { consumersCache = computeIndirectSourceConsumers(t, root) })
	return consumersCache
}

func computeIndirectSourceConsumers(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for file, e := range indirectRequestLogsReaders {
		if e.SwitchFunc == "" {
			continue
		}
		// ⚠ **§9.233 修：第一版只在切换层所在包内解析，于是跨包消费点全部丢失。**
		//
		// 实测：把 4 个 A 类读方（domains/sessionforensics、
		// domains/sessionsummary ×2、bg）改走 `db.SessionBodiesSourceSQL()` 之后，
		// bodies 总体从 **27 掉到 23** —— 4 个文件**静默消失**。
		// 而 bodies 退役门**本来就故意红** ⇒ 少 4 个不改退出码。
		// 这就是 §9.230.3 那个失效模式的**跨包形态**。
		//
		// ⇒ 判据按「符号是否导出」分流：
		//   首字母大写（导出）⇒ **全仓**解析 `pkg.Func(` 与裸 `Func(`；
		//   首字母小写（包内）⇒ 只在本包内解析裸名。
		// 分流的依据不是「严不严」，是**它本来能被谁看见**：未导出符号
		// 跨包不可见，全仓扫它只会把同名的别的函数算进来。
		exported := e.SwitchFunc[0] >= 'A' && e.SwitchFunc[0] <= 'Z'
		dir := filepath.Dir(filepath.Join(root, file))
		needle := e.SwitchFunc + "("
		qualified := "." + needle
		for _, rel := range productionGoFiles(t, root) {
			if !exported && filepath.Dir(filepath.Join(root, rel)) != dir {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			// ⚠ **必须先剥注释。** 实测：我自己写的一句注释
			// 「全部调用点直接调 `dbpkg.SessionBodiesSourceSQL()`」里含有
			// `SessionBodiesSourceSQL(`，于是**定义文件自己被判成自己的消费点** ——
			// 报错是 `总体里 "db/request_logs_view_schema.go" 出现两次`。
			//
			// ⇒ **注释不是调用。** 这与 §9.233 里 Evidence 门被迫改判据是同一件事
			// （那里是「注释里提到旧 key 被当成残留」），两次都是同一个错误形状：
			// 在源码文本里找标识符，却不先排除注释。
			src := stripGoCommentsKeepLines(string(raw))
			if rel == file {
				// 去掉 func 定义那一行再找调用点。
				src = strings.Replace(src, "func "+e.SwitchFunc+"(", "func __def__(", 1)
			}
			if strings.Contains(src, needle) ||
				(exported && strings.Contains(src, qualified)) {
				out[rel] = true
			}
		}
	}
	return out
}

// allKnownRequestLogsReaderFiles 是「直接表 ∪ 间接表 ∪ 切换层消费点」的全集。
//
// 族分类器与任何需要「v1 读点全集」的消费者都应遍历它，而不是只遍历直接表——
// 只遍历直接表正是 §9.49 那三道门变红的直接原因（它们从未看到过这个文件）。
// isSwitchConsumerFile 报告 rel 是否是某个已登记切换层的消费点。
//
// 它只是 `indirectSourceConsumers` 的单文件版本 —— 两边**必须**同源：
// 若这里另写一份「看起来像」的判定，消费点总体与族分类就会在**某一轮**分叉，
// 而分叉的方向恰好是「少算 bodies 读方」（§9.232 记的同一失效方向）。
func isSwitchConsumerFile(t *testing.T, root, rel string) bool {
	t.Helper()
	return indirectSourceConsumers(t, root)[rel]
}

func allKnownRequestLogsReaderFiles(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for f := range requestLogsReadInventory {
		seen[f] = true
		out = append(out, f)
	}
	for f := range indirectRequestLogsReaders {
		if !seen[f] {
			out = append(out, f)
		}
	}
	// ⚠ 消费点必须并进来（§9.232）。它们是 v1 读方（切换层默认臂=v1），
	// 但自己**不**在两张表里的任何一张 —— 漏掉它们 = 退役清单少 15 行。
	for f := range indirectSourceConsumers(t, repoRootFromCaller(t)) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
