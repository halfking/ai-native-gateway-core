package bg

// baseline_price_single_writer_test.go —— 「原厂基准价只有一个写入方，且它必须写全」
// 这条判据是 2026-10-06 数据填充审计的直接产物。
//
// 背景（真库读数，不是推演）：SSOT 落 16 条后，真库 models_canonical 的
// baseline_cache_write_price_per_1m **16 行全 NULL**，而 SSOT 里 14 条有值；
// baseline_price_fetched_at 另有 12 条与 SSOT 不符。根因不是「同步跑失败」，
// 而是**有人绕过了官方写价入口手工 UPDATE**，那条语句没带 cache_write。
//
// 为什么此前没被发现：本仓唯一的写价入口 SyncBaselinePricesToDB **确实**写
// 全 9 列（pricing_baseline_sync.go:383-397），所以「跑过同步的库」永远是对的；
// 出错的永远是绕过同步的第二条路径。而第 18 条健康检查
// （canonical_row_discovered_but_never_referenced）只判
// baseline_input_price_per_1m IS NOT NULL —— **存在性**，不是**数值**。
// ⇒ 全仓没有任何判据会因为少写一列而变红。
//
// 所以这里钉两件事，且两件都必须钉：
//   1. 写入方**唯一** —— 出现第二处写价语法就红（阻止「再开一条路」）。
//   2. 那个唯一写入方**写全 9 列** —— 少写一列就红（阻止「唯一的那条路也漏了」）。
// 只钉第 1 条不够：把现有 SET 子句里的 cache_write 删掉，第 1 条仍然绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// baselinePriceColumns 是 models_canonical 上「人确认过的原厂基准价」那一族列。
// 顺序即迁移 826 的建列顺序，便于人读。
var baselinePriceColumns = []string{
	"baseline_price_currency",
	"baseline_input_price_per_1m",
	"baseline_output_price_per_1m",
	"baseline_cache_read_price_per_1m",
	"baseline_cache_write_price_per_1m",
	"baseline_price_vendor",
	"baseline_price_source",
	"baseline_price_source_url",
	"baseline_price_fetched_at",
}

// canonicalBaselineWriter 是全仓**唯一**允许写这族列的生产代码文件。
const canonicalBaselineWriter = "bg/pricing_baseline_sync.go"

// writePatterns 匹配「在写这族列」的语法。SELECT / 只出现在 WHERE 里不算。
//
// ★ 只用这一条形态，且列名里必须含 `price`，因为第一版写成 `\bSET\s+baseline_`
// 时实测抓到两个**假阳性**（都已订正，记录在此因为这是本判据最容易重犯的错）：
//
//	- bg/routing_health_checks.go 的告警英文散文
//	  '…not being monitored. **Set baseline_price_currency from** the vendor
//	  pricing page.' —— 列名后面跟的是 from 不是 =，所以要求 `\s*=` 就排除了它。
//	- bg/integrity_fingerprint_drift.go 的 `DO UPDATE SET
//	  baseline_fingerprint = EXCLUDED.baseline_fingerprint` —— 那是**另一张表的
//	  另一列**，名字里没有 price，所以列名必须含 price 才算命中。
//
// 要求 `SET <含 price 的列> =` 而不是任意 `SET baseline_`：前者只匹配真写入。
var writePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)\bSET\s+baseline_[a-z0-9_]*price[a-z0-9_]*\s*=`),
}

// repoGoFiles 返回仓库内所有生产 .go 文件（排除 _test.go）。
//
// 每项是「读它用的路径」。go test 的工作目录是包目录（bg/），所以 Walk("..")
// 产出的 "../bg/x.go" 才是可读路径；判据比对时要的是 "bg/x.go"。
// ★ 这两个形态不能混：曾经把返回值 TrimPrefix("../") 之后拿同一个串既读又比，
// 结果 ReadFile 全部 "no such file or directory"（解析到 bg/ 底下去了），
// 而报错长相与「文件不在仓库里」一模一样。
func repoGoFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// 跳过 vendor/node_modules 这类依赖树，以及**全部点开头的目录**：
			// .git 是版本库，.db-audit/ 之类是工具临时产物（实测会碰到
			// .db-audit/instdiff/main.go 这种 Walk 列得到、ReadFile 读不到
			// 的条目）。点目录不是生产代码，放它进来只会让判据因为与「写入方
			// 唯一」无关的原因红。注意 node_modules 在点目录之外，单列。
			//
			// ★ path != ".." 这半个条件不能省：Walk 的起点名字就是 ".."，
			// 它自己以点开头，省掉就会 SkipDir 掉整棵子树 —— 实测过一次，
			// 表现为「写入方实测 0 个」，与「写入方不止一个」长得完全不一样，
			// 很容易被读成另一个故事。
			if path != ".." && (info.Name() == "node_modules" ||
				strings.HasPrefix(info.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 原样保留 "../" 前缀 —— 这是从包目录（bg/）读它用的路径。
		out = append(out, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestBaselinePriceHasExactlyOneWriter 钉「写入方唯一」。
//
// 变异预期：从 pricing_baseline_sync.go 之外新增/改动任一文件使其含写价语法 ⇒ 🔴。
func TestBaselinePriceHasExactlyOneWriter(t *testing.T) {
	var writers []string
	for _, f := range repoGoFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, re := range writePatterns {
			if re.Match(b) {
				// 比对用去掉 "../" 的仓库相对名，读文件用原路径。
				writers = append(writers, strings.TrimPrefix(f, "../"))
				break
			}
		}
	}

	want := []string{canonicalBaselineWriter}
	if len(writers) != len(want) || (len(writers) == 1 && writers[0] != want[0]) {
		t.Fatalf("基准价的写入方必须唯一，实测 %d 个：\n  got  %v\n  want %v\n"+
			"新增的写价路径会绕开 SyncBaselinePricesToDB 的 validate 与「写全 9 列」保证 —— "+
			"2026-10-06 真库 cache_write 16 行全 NULL 就是这么来的。\n"+
			"要写价就调 bg.SyncBaselinePricesToDB，不要另开 UPDATE。",
			len(writers), writers, want)
	}
}

// TestCanonicalBaselineWriterSetsEveryColumn 钉「唯一写入方写全 9 列」。
//
// 这条才是「少写一列会红」的那颗牙：把 SET 子句里的 cache_write 删掉，
// TestBaselinePriceHasExactlyOneWriter 仍然是绿的，只有本条会红。
func TestCanonicalBaselineWriterSetsEveryColumn(t *testing.T) {
	b, err := os.ReadFile("../" + canonicalBaselineWriter)
	if err != nil {
		t.Fatalf("read %s: %v", canonicalBaselineWriter, err)
	}
	src := string(b)

	// 只取 UPDATE models_canonical ... WHERE canonical_name 这一段，
	// 免得函数别处的同名列（注释、参数说明）把判定撑成恒真。
	start := strings.Index(src, "UPDATE models_canonical")
	if start < 0 {
		t.Fatalf("%s 里找不到 `UPDATE models_canonical` —— 写价入口被挪走了？",
			canonicalBaselineWriter)
	}
	end := strings.Index(src[start:], "WHERE canonical_name")
	if end < 0 {
		t.Fatalf("UPDATE 段里找不到 `WHERE canonical_name`，无法界定 SET 子句范围")
	}
	setClause := src[start : start+end]

	var missing []string
	for _, col := range baselinePriceColumns {
		if !regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(col) + `\s*=\s*\$`).MatchString(setClause) {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("唯一写入方的 SET 子句漏了 %d 列：%v\n"+
			"漏一列的后果不是测试红，而是真库那一列全 NULL 而没有任何判据会响 —— "+
			"2026-10-06 的 cache_write 就是这样丢的。",
			len(missing), missing)
	}
	t.Logf("唯一写入方 %s 的 SET 子句覆盖全部 %d 列", canonicalBaselineWriter, len(baselinePriceColumns))
}