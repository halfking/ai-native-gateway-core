//go:build !integration

package admin

// retirement_exposure_attribution_gap_probe_test.go — 2026-10-04（审计 §9.194）。
//
// # 这是一道**探针**，不是一道门
//
// 它**刻意永不判红**。它量一个数：现有暴露分析
// （`extractV1ReadingLiterals` + `columnAttribution`）在读方清单上
// **漏归因了多少个「列 × 文件」**。
//
// 为什么不做成门：
//
//  1. 漏归因的**正确值是 0**，而它现在不是 0。把一个已知的坏值写成期望值，
//     等于**把缺陷冻进断言**——将来有人修好提取器，这道门会红，
//     而它的红原因是「变好了」。这类门会教会所有人忽略它。
//  2. 我试过做成「默认拒绝 + 具名登记」的门，判据三次放宽/收紧后
//     命中 **12 → 276 → 171** 个文件。**171 条登记是负资产**：
//     没有人会维护，而没人维护的清单等于没有清单。
//     真正该做的是 D29-a（把别名表改成文件级并集）——**修掉整类，而不是枚举它**。
//
// # 探针唯一的红条件：它自己坏了
//
// 扫描到 0 个读方文件时直接 Fatal。本项目已经栽过一次同族错误
// （审计 §9.184：普查脚本的 `PGPASSWORD` 从未赋值，12 次连接失败全被
// 默认判 OK，整张矩阵作废）。**探针必须能说出「我这次真的量到了东西」。**
//
// # 双口径对比（2026-10-04 §9.195 追加）
//
// 提取器已被参数化为 `extractV1ReadingLiteralsScoped(t, path, fileScope)`：
// **默认口径（false）与改动前逐字等价**，文件级口径（true）是 D29-a 的候选。
// 本探针的分子现在取**两个口径都没归因**的列 ⇒
// **修完之后它应当降到 0**，届时这道探针就是 D29-a 的验证器。
//
// # 口径
//
// 分母 = `allKnownRequestLogsReaderFiles()`（现有分析的覆盖范围）。
// 分子 = 「出现在无 FROM 的 SQL 片段里、但 `extractV1ReadingLiterals`
// 没有归因」的退役清单列。`id` 单列——提取器自己的注释写着
// 「This is the case that makes `id` a false positive」，它常以派生名
// （`AS id`）出现，不是源列名，计进去会让数字虚高。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	attrGapFromRe   = regexp.MustCompile(`(?i)\bFROM\b`)
	attrGapSignalRe = regexp.MustCompile(`(?i)(\sAS\s|\.\w|::|\()`)
)

func TestProbeRetirementExposureAttributionGap(t *testing.T) {
	root := repoRootFromCaller(t)
	cols := retirementListedColumns()
	anchored := make([]*regexp.Regexp, len(cols))
	for i, c := range cols {
		anchored[i] = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(c) + `\b`)
	}

	type row struct {
		file   string
		missed []string
	}
	var rows []row
	scanned := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "web":
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		inInventory := false
		for _, f := range allKnownRequestLogsReaderFiles() {
			if f == rel {
				inInventory = true
				break
			}
		}
		if !inInventory {
			return nil
		}
		scanned++

		attributed := map[string]bool{}
		for _, lit := range extractV1ReadingLiterals(t, path) {
			for c := range lit.allColumns {
				attributed[c] = true
			}
		}
		attributedScoped := map[string]bool{}
		for _, lit := range extractV1ReadingLiteralsScoped(t, path, true) {
			for c := range lit.allColumns {
				attributedScoped[c] = true
			}
		}
		// 收窄的只在「文件级口径找到了、默认口径没找到」时才记，
		// 并显式排除**反向**（默认有、文件级没有）——那不该发生，
		// 发生了说明并集实现写反了，必须看得见。
		for c := range attributed {
			if !attributedScoped[c] {
				t.Errorf("%s：列 %s 在默认口径被归因、在文件级口径却没有。"+
					"文件级作用域是**更宽**的并集，不可能更窄 —— 除非实现写反了。", rel, c)
			}
		}
		missed := map[string]bool{}
		hasSelectFrom := false
		for _, lit := range goStringLiterals(t, path) {
			lit = sqlBlockCommentRe.ReplaceAllString(lit, " ")
			lit = sqlLineCommentRe.ReplaceAllString(lit, " ")
			if selectRe.MatchString(lit) && attrGapFromRe.MatchString(lit) {
				hasSelectFrom = true
				continue
			}
			// 「像查询片段」：含逗号 + ` AS `/点号/`::`/括号。
			// 这一条是排噪关键——放宽到「只要含列名」时命中 276 个文件，
			// 其中绝大多数是结构体 tag 与单个裸列名常量，不是拼接失效形状。
			if !strings.Contains(lit, ",") || !attrGapSignalRe.MatchString(lit) {
				continue
			}
			for i, re := range anchored {
				if re.MatchString(lit) && !attributed[cols[i]] && !attributedScoped[cols[i]] {
					missed[cols[i]] = true
				}
			}
		}
		if !hasSelectFrom || len(missed) == 0 {
			return nil
		}
		var ms []string
		for c := range missed {
			ms = append(ms, c)
		}
		sort.Strings(ms)
		rows = append(rows, row{file: rel, missed: ms})
		return nil
	})
	if err != nil {
		t.Fatalf("扫描生产源码失败: %v", err)
	}

	// ★ 探针自检：扫描到 0 个读方文件 ⇒ 我这个探针坏了自己，不是「测出 0」。
	if scanned == 0 {
		t.Fatalf("探针自检失败：分母（读方清单）里一个文件都没扫到。" +
			"这**不是**「漏归因为 0」的结论，是探针读错了对象。" +
			"请先查 allKnownRequestLogsReaderFiles() / repoRootFromCaller。")
	}
	if len(allKnownRequestLogsReaderFiles()) < 50 {
		t.Fatalf("探针自检失败：读方清单只有 %d 个文件，低于 50 的地板 —— "+
			"分母被悄悄换掉了", len(allKnownRequestLogsReaderFiles()))
	}

	filesA, pairsA, uniqA := 0, 0, map[string]bool{}
	filesB, pairsB, uniqB := 0, 0, map[string]bool{}
	for _, r := range rows {
		filesA++
		hitB := false
		for _, c := range r.missed {
			pairsA++
			uniqA[c] = true
			if c == "id" {
				continue // 派生名磁铁，单列
			}
			pairsB++
			uniqB[c] = true
			hitB = true
		}
		if hitB {
			filesB++
		}
		var keep []string
		for _, c := range r.missed {
			if c != "id" {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			t.Logf("  %-52s %s", r.file, strings.Join(keep, " "))
		}
	}
	names := func(m map[string]bool) string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	t.Logf("=== 汇总A（含 id）：%d 个文件 / %d 个「列×文件」对 / %d 个不同列 ===",
		filesA, pairsA, len(uniqA))
	t.Logf("=== 汇总B（剔除 id）：%d 个文件 / %d 对 / %d 个不同列 ===",
		filesB, pairsB, len(uniqB))
	t.Logf("汇总B 涉及列: %s", names(uniqB))
	t.Logf("读方清单分母 = %d 个文件（本探针已扫）", scanned)
}

// probeV1BaseTableRe 是 4 张 v1 裸表的名字，**独立抄写**，
// **刻意不复用** `request_logs_reader_population_test.go` 里的 `v1BaseTableRe`。
//
// 理由有两层，都值得记：
//
//  1. 同名撞车（`v1BaseTableRe redeclared`）——我又一次先动手后 grep。
//     本轮第三次。
//  2. 更要紧的：**被检验对象本身就是错的**，量具就不能与它同源。
//     「量具与被检验对象必须同源」是对的，但那句话的前提是两者都对；
//     当要量的正是「它哪里错了」，共用常量会让量具跟着一起错。
//     本探针要断言的恰恰是
//     「`request_logs_retirement_exposure_test.go` 的 `v1TableRe`
//     **不认** `_with_current_month` 视图」——若共用，那条断言就是恒真的。
var probeV1BaseTableRe = regexp.MustCompile(
	`\b(request_logs|request_logs_hot|request_logs_bodies|request_logs_bodies_hot)\b`)

// currentMonthViewRe 认 `*_with_current_month` 视图族。
var currentMonthViewRe = regexp.MustCompile(`\b[a-z_]*_with_current_month\b`)

// TestProbeRetirementExposureBlindSpotFiles 量**另一个**、更大的盲区：
// 有多少文件只经由 `_with_current_month` 视图读 v1，
// 而 `extractV1ReadingLiterals` 对它们**一个字面量都产不出来**
// （它要求字面量里出现 4 张裸表之一，视图名不在名单里）。
//
// ⇒ 这些文件在 `TestRequestLogsRetirementExposure` 的报告里落在
// **「clean」**桶、且 `literals=0` —— 它们不是「查过了没问题」，
// 而是**「从来没被看过」**。两者在报告里长得一模一样。
//
// 同样是探针（不判红），同样带自检：扫到 0 个文件 ⇒ Fatal。
func TestProbeRetirementExposureBlindSpotFiles(t *testing.T) {
	root := repoRootFromCaller(t)
	scanned := 0
	blind := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		scanned++
		// 逐字面量判（而不是整文件 grep）：注释里提到视图名不算读它。
		viaView, viaBase := false, false
		for _, lit := range goStringLiterals(t, path) {
			lit = sqlBlockCommentRe.ReplaceAllString(lit, " ")
			lit = sqlLineCommentRe.ReplaceAllString(lit, " ")
			if !selectRe.MatchString(lit) {
				continue
			}
			if currentMonthViewRe.MatchString(lit) {
				viaView = true
			}
			if probeV1BaseTableRe.MatchString(lit) {
				viaBase = true
			}
		}
		if viaView && !viaBase {
			blind = append(blind, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描生产源码失败: %v", err)
	}
	if scanned == 0 {
		t.Fatalf("探针自检失败：扫到 0 个生产文件 —— 探针读错了对象，不是「盲区为 0」。")
	}
	sort.Strings(blind)
	t.Logf("扫描 %d 个生产文件", scanned)
	t.Logf("=== 只经由 *_with_current_month 视图读 v1、分析一个字面量都产不出的文件：%d 个 ===",
		len(blind))
	for _, b := range blind {
		t.Logf("  %s", b)
	}
}
