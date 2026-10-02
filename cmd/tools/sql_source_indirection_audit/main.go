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
// # 它不是门，刻意不进 CI
//
// 三个理由，逐条都可证：
//
//  1. 它的结论依赖 **Go 层的表达式解析**，不是文本扫描。把它的输出冻结成一张
//     登记表，就等于给 30 个文件各写一条「手验过」——而 §9.37 记录的正是这种
//     登记表在代码演进后静默腐烂的过程。
//  2. 空库 / 一次性库上它同样证明不了任何事（§9.34）。
//  3. 它的正确用法是**按需运行**、把输出当证据读，而不是让 CI 每周报一次同样的
//     48 行然后被人加进豁免表。
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
	fmt.Println("本工具不是门、不进 CI；输出是证据，不是待维护的登记表。")

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
