// Command extract-product-referenced-relations 找出「产品按名引用、但仓内 DDL 没有建出」的关系
// （**悬空引用**），并对仓内每个关系给出产品依赖强度的三档桶。
//
// # 它要回答的问题
//
//	产品代码里写着 `INSERT INTO prompt_injection_approvals …`，而仓内没有任何 DDL 建出它
//	⇒ ⇒ 那张表要么来自别的产品共用的库、要么是拼错的、要么是**漏建的**。
//	⇒ 这三件事的处理方式完全不同 ⇒ ⇒ ★ 所以【缺的是【一个把悬空引用摆到台面上的工具】】。
//
// # 两条腿（★ 实现在 internal/relrefs，本目录只是薄壳）
//
//	腿 A（ddl.go）= 枚举仓内**全部 DDL 形态**建出的关系名。
//	  ★ 只扫 `CREATE TABLE` 必然漏 ⇒ 视图 / 物化视图 / 函数返回表 / 分区子表 /
//	    RENAME TO 后的新名 / TEMP 表全部看不见 ⇒ ⇒ 「悬空引用」会被严重高估。
//	  ★★ ★ 而且 DDL **不只存在于 `.sql` 文件** ⇒ 它也在 **Go 字符串**里
//	    （`ensure*Schema` 链、`admin/session_export.go` 的 CREATE TABLE）
//	    ⇒ ⇒ **腿 A 必须同时扫 `.sql` 与 Go 侧字面量** ⇒ 否则那批表被误报成悬空。
//	腿 B（product.go）= 从**产品代码**里提取按名引用的关系。
//	  ★ 用 `go/parser` + `go/ast` 取字符串字面量 ⇒ 天然分开注释与字面量。
//
// # 三档桶 + 一个前提守卫
//
//	强命中 = 出现在 SQL 关系位置（FROM/JOIN/UPDATE/TRUNCATE/REFERENCES/COPY/INSERT INTO 之后）
//	弱命中 = 出现在 SQL 字面量的非关系位置 ⇒ ★★ 可能误报 ⇒ 需人验
//	零命中 = 产品代码里完全查不到
//	★★★ 拼接嫌疑 = 存在「以该名字开头且更长」的字面量
//	  ⇒ 「按名零命中 ⇒ 可判定不依赖」这条推论**被它推翻**
//	  ⇒ ⇒ 凡有拼接嫌疑的名字，**不得**判为「不依赖」
//
// # 用法
//
//	go run ./cmd/tools/extract-product-referenced-relations
//	go run ./cmd/tools/extract-product-referenced-relations --tsv > /tmp/dangling.tsv
//	go run ./cmd/tools/extract-product-referenced-relations --self-test
//
// # 它【不是】判定的权威
//
//	本工具给的是**候选集**⇒ ⇒ 终值必须逐条人工/实测。
//	★ 「按名命中」几乎总是假阳性；★ 「按名零命中」才是可信证据（R63 §4）。
//	★ 判定在 scripts/ursmcheck/dangling_relation_gate_test.go。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/relrefs"
)

func main() {
	tsv := flag.Bool("tsv", false, "输出机器可读清单（TSV）")
	selfTest := flag.Bool("self-test", false, "跑自检并退出")
	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	if *selfTest {
		if err := relrefs.RunSelfTest(root); err != nil {
			fmt.Fprintln(os.Stderr, "SELF-TEST FAIL:", err)
			os.Exit(1)
		}
		fmt.Println("SELF-TEST PASS")
		return
	}

	sqlFiles, err := relrefs.RepoFiles(root, "*.sql")
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(2)
	}
	goFiles, err := relrefs.RepoFiles(root, "*.go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(2)
	}

	ddl, err := relrefs.ScanDDLRepo(root, sqlFiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(2)
	}
	ps, err := relrefs.ScanProductRepo(root, goFiles, ddl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(2)
	}

	// ★ 悬空引用 = 强命中名字 − 腿 A 建出的关系
	dangling := map[string][]relrefs.RelationRef{}
	for _, r := range ps.Refs() {
		if !r.Strong {
			continue
		}
		if ddl.Has(r.Name) {
			continue
		}
		dangling[r.Name] = append(dangling[r.Name], r)
	}

	if *tsv {
		w := bufio.NewWriter(os.Stdout)
		defer w.Flush()
		fmt.Fprintln(w, "# name\tfile\tline\tbucket\tsql")
		for _, n := range sortedKeys(dangling) {
			for _, r := range dangling[n] {
				fmt.Fprintf(w, "%s\t%s\t%d\tstrong\t%s\n", n, r.File, r.Line, oneLine(r.SQL))
			}
		}
		return
	}

	// ── 人读摘要 ──
	fmt.Printf("== 腿 A：仓内 DDL 建出的关系 ==\n")
	fmt.Printf("  .sql 文件 %d 个；关系名（全名口径）%d 个、裸名 %d 个\n",
		ddl.SQLFiles(), len(ddl.FullNames()), len(ddl.BareNames()))
	fmt.Printf("  各形态计数：")
	kinds := make([]string, 0, len(ddl.PerKind()))
	for k := range ddl.PerKind() {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("%s=%d ", k, ddl.PerKind()[k])
	}
	fmt.Println()

	fmt.Printf("\n== 腿 B：产品侧引用 ==\n")
	fmt.Printf("  .go 文件 %d 个（已按排除清单过滤）；字符串字面量 %d 处，其中 SQL 上下文 %d 处；全仓 CTE 名 %d 个\n",
		ps.Files(), ps.StringLits(), ps.SQLLits(), ps.GlobalCTECount())
	fmt.Printf("  强命中名字 %d 个；弱命中名字 %d 个；拼接嫌疑名字 %d 个\n",
		len(ps.StrongNames()), len(ps.WeakNames()), len(ps.PrefixSuspects()))

	fmt.Printf("\n== ★ 悬空引用（产品按名引用、但仓内 DDL 没建出）: %d 个名字 / %d 处 ==\n",
		len(dangling), countRefs(dangling))
	for _, n := range sortedKeys(dangling) {
		fmt.Printf("  %-40s %d 处\n", n, len(dangling[n]))
		for _, r := range dangling[n] {
			fmt.Printf("      %s:%d  %s\n", r.File, r.Line, oneLine(r.SQL))
		}
	}

	fmt.Println()
	fmt.Println("★ 本工具给的是【候选集】⇒ 终值必须逐条人工/实测（R63 §4：按名命中几乎总是假阳性；按名零命中才是可信证据）。")
	fmt.Println("★ 判定在 scripts/ursmcheck/dangling_relation_gate_test.go。")
}

func countRefs(m map[string][]relrefs.RelationRef) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

func sortedKeys(m map[string][]relrefs.RelationRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return s
}
