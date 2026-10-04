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
// `LEFT JOIN request_logs_hot rl` 改成 `LEFT JOIN " + src.TurnsTable + " rl`，
// 于是一夜之间**三道独立的门同时变红**，而它们全都是绿的：
//
//	requestLogsReadInventory        按行正则 `from\s+request_logs` 扫
//	paddedColumnsReadFromV1Direct  从 SQL 字面量里抽关系名
//	sourceFamilyOf                 剥注释后匹配族正则
//
// 三个器眼，三种实现，同一个前提：**关系名是字面量**。
//
// # 为什么不能用「加条注释让它看见」
//
// 那会让计数变对而**理由是错的**：扫描器扫的是原始行文本，注释里的
// `from request_logs` 会被计成一个读点。这是**伪造测量**——门绿了，测的却不是
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
	"strings"
	"testing"
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
}

var indirectRequestLogsReaders = map[string]indirectReader{
	"admin/session_bodies_source.go": {
		Family:     familyBodies,
		ResolvesTo: "request_logs_bodies",
		Reason: "会话导出/对比的 bodies 腿灰度开关（审计 §9.230）。" +
			"`sessionBodiesFromSQL()` 有两臂：默认返回字面量 " +
			"`request_logs_bodies_with_current_month rb`（v1 bodies 视图，" +
			"挂在 request_logs_bodies 的 monthly 视图上），" +
			"开关打开才返回 `db.SessionFamilyBodiesSourceSQL() + \" rb\"`（会话族）。" +
			"⇒ **默认支是 v1**，与 bg/auto_route_settle_sql.go 同形：条件性读 v1。" +
			"ResolvesTo 写基表名 request_logs_bodies 而不是视图名，" +
			"因为本表用 v1DirectTables 核对，而那个集合只收基表。" +
			"消费点是 admin/session_export.go:227 与 admin/session_compare.go:903 —— " +
			"它们源码里**不再有** v1 关系名字面量，所以也不在 requestLogsReadInventory 里。",
	},
	"bg/auto_route_settle_sql.go": {
		Family:     familyBase,
		ResolvesTo: "request_logs_hot",
		Reason: "settleBatch / loadTaskBaselines 的 SQL 由 settleBaselinesSQL(src) 与 " +
			"settlePendingSQL(src) 构造，关系名是 src.TurnsTable（§9.43 引入）。" +
			"src 来自 settleSourceFor(logsWriteEnabled)：写门开着 ⇒ request_logs_hot（当前族），" +
			"关掉 ⇒ session_turns_hot（§9.44）。⇒ 同一段 SQL 在两个族之间切换，" +
			"族不能由文本判定，必须显式登记。",
	},
}

// TestIndirectRequestLogsReadersAreDeclaredWell 挡住「登记了但说不清」。
func TestIndirectRequestLogsReadersAreDeclaredWell(t *testing.T) {
	families := map[string]bool{
		familyBodies: true, familyBodiesMixed: true,
		familyView: true, familyBase: true,
	}
	for file, e := range indirectRequestLogsReaders {
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("%s：间接读点登记的 Reason 为空——空理由的登记等于没有登记", file)
		}
		if strings.TrimSpace(e.ResolvesTo) == "" {
			t.Errorf("%s：ResolvesTo 为空。不写它，「它读的是 v1」就是一句断言；\n"+
				"  写上表名之后，这句话可以被 v1DirectTables 核对。", file)
		} else if !v1DirectTables[e.ResolvesTo] {
			t.Errorf("%s：ResolvesTo = %q，不在 v1DirectTables 里。\n"+
				"  这张表只登记「间接读 v1 宽族」的文件；若它其实指向别的表，"+
				"应该从本表删掉而不是改成一个不存在的表名。", file, e.ResolvesTo)
		}
		if !families[e.Family] {
			t.Errorf("%s：Family = %q，不是已声明的族。\n  可选：%s",
				file, e.Family, strings.Join([]string{familyBodies, familyBodiesMixed, familyView, familyBase}, " / "))
		}
	}
}

// TestIndirectRequestLogsReadersAreStillIndirect 是这张表的失效自检。
//
// 两条，缺一不可：
//
//	① 登记的文件必须**仍然读不到直接字面量**（直接计数为 0）。若某天有人把
//	   settleSourceFor 的表名内联回 SQL，间接性消失 ⇒ 本条目应删掉、文件回到
//	   requestLogsReadInventory 的直接表里。留着会让「间接」这个分类永久占位。
//	② 文件必须仍然存在。文件被删/改名而登记项还在 = 一条指向空气的记录。
//
// ⚠ 这道门**发现不了新的间接读点**（见文件头注释）。它只保证已知的那几条不腐烂。
func TestIndirectRequestLogsReadersAreStillIndirect(t *testing.T) {
	root := repoRootFromCaller(t)
	direct := scanRequestLogsReaders(t, root)
	for file := range indirectRequestLogsReaders {
		if _, err := os.Stat(filepath.Join(root, file)); err != nil {
			t.Errorf("间接读点登记 %q：文件不存在（%v）。请删掉这条登记。", file, err)
			continue
		}
		if n := direct[file]; n != 0 {
			t.Errorf("间接读点登记 %q：现在能被直接扫到 %d 处（`from request_logs` 匹配上了）。\n"+
				"  间接性已经消失——请把它从本表删掉、并登记进 requestLogsReadInventory，\n"+
				"  然后重新评估它的族与停写后果。留着会让「间接」变成永不更新的占位。", file, n)
		}
	}
	// 反向：直接表里不许出现本表已登记的文件，否则它被算两次。
	for file := range indirectRequestLogsReaders {
		if _, dup := requestLogsReadInventory[file]; dup {
			t.Errorf("%s 同时出现在 indirectRequestLogsReaders 与 requestLogsReadInventory。\n"+
				"  它只能算一次：要么是直接读点，要么是间接读点。", file)
		}
	}
}

// allKnownRequestLogsReaderFiles 是「直接表 ∪ 间接表」的全集。
//
// 族分类器与任何需要「v1 读点全集」的消费者都应遍历它，而不是只遍历直接表——
// 只遍历直接表正是 §9.49 那三道门变红的直接原因（它们从未看到过这个文件）。
func allKnownRequestLogsReaderFiles() []string {
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
	return out
}
