// Command sql_source_indirection_audit 找出「SQL 里的关系名不是字面量」的读点，
// 并尽可能把它们解析回具体的表名（审计 §9.45）。
//
// # 它要回答的问题
//
// 退役规划里有一批「S4 读面门」是靠**扫描 SQL 字面量文本**发现 v1 直读读方的
// （代表：admin/v1_direct_padded_column_reader_test.go 的
// paddedColumnsReadFromV1Direct，用 fromJoinRE 从字面量里抽 FROM/JOIN 后的关系名）。
// 那个口径有一个结构性前提：关系名必须是**字面量**。而本项目为了「改一处完成
// 整体端口」，大量读点把表名放进 Go 标识符再拼进 SQL——
//
//	`SELECT … FROM ` + logsTable + ` WHERE …`
//
// 对这类读点，扫描器看到的是一段**以 FROM 结尾的碎片**，里面没有关系名 ⇒ 该文件
// 整个不进入判定。§9.45 实测：全仓 48 处这样的拼接点，跨 30 个文件（R33 复跑
// 2026-10-02 HEAD：61 处 / 37 个文件）。
//
// ⚠ 已知盲区（R33，2026-10-02 12h 审计登记，检测臂待下一轮补）：本工具只认
// `+` 拼接（BinaryExpr ADD）。`fmt.Sprintf("... FROM %s ...", t)` 形态完全
// 不可见——例如 admin/dashboard_board_fallback.go:238 的
// fallbackErrorDrill（logsTable 在 days≤7 时取 request_logs_hot，v1 基表）只经
// Sprintf 进 SQL，本文件无任何 `+` 形 FROM 拼接，因此不出现在输出里。
// §9.45 的「盲区当前无活漏网」建立在这个缺一半的枚举上；引用本工具的计数
// 时要连这个例外一起说。
//
// 本工具不替代那道门（它判定的是别的东西），它回答的是：
// **「那些门看不见的读点，实际读的是哪张表？」**
//
// # 三种结果分别意味着什么
//
//	reads-v1        解析到 v1 宽族（request_logs*）⇒ 退役后会拿到 session 臂的
//	                NULL 补位列。**这是需要有人拍板的那一类。**
//	reads-canonical 解析到 canonical 视图或会话族 ⇒ 退役安全，仅作信息。
//	unresolved      跨包调用 / struct 字段 / 运行时决定 ⇒ 机械判定不了，需要手验。
//	                **本工具不猜**：宁可标记为不可判定，也不把它算进 reads-v1。
//
// # 它曾经**刻意**不是门：那三个理由仍然成立，但已被一一回答
//
// §9.45 当初写下「本工具不是门、不进 CI」，三个理由逐条都可证：
//
//  1. 结论依赖 **Go 层的表达式解析**，不是文本扫描。把输出**冻结**成一张
//     登记表，等于给每个文件各写一条「手验过」——§9.37 记录的正是这种
//     登记表在代码演进后静默腐烂的过程。
//  2. 空库 / 一次性库上它证明不了任何事（§9.34）。
//  3. 正确用法是按需运行、把输出当证据读；进 CI 只会每周重印同样的几十行，
//     然后被人加进豁免表。
//
// **2026-10-05（§9.227）：第 1 条被回答，第 2、3 条本来就不适用于这道门。**
//
//   - 对 1：现在有 `manifest_test.go`，但它**不是把输出冻结成表**。
//     清单是人读源码写的，与工具的分类**双向交叉核对**：
//     工具说 v1 而清单说不是 ⇒ 红；清单说 v1 而工具说 canonical-only ⇒ 红；
//     清单里出现 still-unknown ⇒ 红（ratchet，不许「先填上以后再说」）。
//     **一张能和自己量具吵起来的表不会静默腐烂**——它会当场变红。
//     反面教材就在同一份历史里：§9.226.3 的 `admin/tenants.go` 被工具自信地
//     判成「退役安全」；若当时有这张表且是单向抄表，两边会一起绿。
//   - 对 2：这道门**不碰数据库**。它走源码树，空库/一次性库上照样成立。
//   - 对 3：清单补齐后，通过时只打 3 行汇总，不重印站点清单；
//     它只在**变化**时红（新增未登记 / 判定与实测矛盾 / 字段为空 / 总体为 0）。
//
// ⇒ `go run` 仍然是它的正常用法；只是现在**有人欠了账会被 CI 叫住**。
// 下面这三行收尾是**给人看**的输出摘要，不是门的判定——门在 `manifest_test.go`。
//
// 用法：
//
//	go run ./cmd/tools/sql_source_indirection_audit
//	go run ./cmd/tools/sql_source_indirection_audit -v1-only
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	v1Only := flag.Bool("v1-only", false, "只打印解析到 v1 宽族的读点")
	flag.Parse()

	repo := "."
	if flag.NArg() > 0 {
		repo = flag.Arg(0)
	}

	sites, err := AuditRepo(repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(2)
	}

	var v1s, other, unresolved []Site
	for _, s := range sites {
		switch s.Classification() {
		case ClassReadsV1:
			v1s = append(v1s, s)
		case ClassUnresolved:
			unresolved = append(unresolved, s)
		default:
			other = append(other, s)
		}
	}

	if !*v1Only {
		fmt.Printf("== 解析到 canonical 视图 / 会话族（退役安全）: %d 处 ==\n", len(other))
		for _, s := range other {
			fmt.Printf("  %s\n", s)
		}
		fmt.Println()
	}

	fmt.Printf("== 解析到 v1 宽族（退役后拿 NULL 补位列）: %d 处 ==\n", len(v1s))
	for _, s := range v1s {
		fmt.Printf("  %s\n", s)
	}

	fmt.Printf("\n== 不可静态解析（需手验）: %d 处 ==\n", len(unresolved))
	for _, s := range unresolved {
		fmt.Printf("  %s\n", s)
	}

	fmt.Printf("\n合计 %d 处拼接点 / %d 个文件；其中解析到 v1 的 %d 处分布在 %d 个文件。\n",
		len(sites), uniqueFiles(sites), len(v1s), uniqueFiles(v1s))
	fmt.Println("本工具的输出是证据；判定在 manifest_test.go（§9.227 起它**是**门）。")
	fmt.Println("若上面出现 reads-v1 却不在 indirectSiteAssessments 里，那道门会红。")

	if len(v1s) > 0 && !*v1Only {
		fmt.Fprintln(os.Stderr, "\n注意：存在解析到 v1 宽族的间接读点，请读 §9.45 的处置口径。")
	}
}

func uniqueFiles(sites []Site) int {
	seen := map[string]bool{}
	for _, s := range sites {
		seen[s.File] = true
	}
	return len(seen)
}
