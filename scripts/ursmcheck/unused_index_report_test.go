package ursm_test

// unused-index-report.sh 的行为门（2026-10-19，§143.9）
//
// # 这个工具要拦的退化
//
// 它是「删索引」这条生产写路径的**唯一前置证据**。三条性质一旦悄悄没了，
// 它的输出就会从「证据」退化成「看着像证据的猜测」：
//
// ① **只读** —— 工具里一旦出现写语句，它就可能在自己跑的时候改生产。
//    而它的输出会被直接拿去支撑 DROP INDEX。
// ② **必须报统计窗口** —— 「14 天没人用过」这句话**离开窗口就是没有意义的**。
//    窗口由 PG 重启决定（idx_scan 会被清零），工具若不打印它，
//    读数就变成了「不知道覆盖了多久的零」。
// ③ **必须按「删了会不会复发」分类** —— 2026-10-19 实测：
//    零扫描索引里 **1,233,600,512 字节（34 个）是叶分区索引**，
//    删掉它们**会随下一个分区创建而长回来**（§79 的教训）；
//    真正「删了就没了」的只有 **405,692,416 字节（18 个）**。
//    不分类 ⇒ 清单会把 1.2GB 的无效动作混进「可回收」里。
//
// # 为什么用假 psql 而不是文本断言
//
// 与 emergency_cleanup / vacuum_bloat / disk_watch 同款：注入假 psql，
// 让脚本真的把 SQL 发出来，再断言**它实际发了什么**。
// 「把判定词写进注释」骗不过它。

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const unusedIndexReportScript = "../252-monitor/unused-index-report.sh"

// fakePsql 记录脚本实际下发的 SQL，并返回 canned 结果。
//
// ⚠ 分类（C/D/E）是在 **SQL 的 CASE 里**算出来的，桩不会执行 SQL ⇒
//
//	桩若对分类查询也返回通用行，输出里就永远不会出现那三个标签。
//
// ⇒ 这里对**分类那条查询**返回 canned 的三行，使「脚本确实把分类打印出来」可验；
//
//	而「分类是怎么算出来的」由另一条门在 **SQL 文本**上钉（见 …ClassifiesRegrowthAndConstraint）。
func fakePsql(t *testing.T, dir string) string {
	t.Helper()
	script := `#!/bin/sh
all="$*"
{
  echo "--- sql ---"
  for a in "$@"; do echo "$a"; done
} >> "$TRACE_FILE"

case "$all" in
  *"C_叶分区索引"*)
      printf 'C_叶分区索引(删了会长回来,须动父表声明)|34|1233600512\n'
      printf 'D_非分区表的约束附带索引(删索引=删约束)|5|93937664\n'
      printf 'E_非分区表的独立索引(删了就没了)|18|405692416\n' ;;
  *"pg_postmaster_start_time"*)
      printf 'window|postmaster_start|2026-10-05 15:49:43+08\n'
      printf 'window|uptime|14 days 05:39:46\n'
      printf 'window|pg_stat_statements_rows|3437\n'
      printf 'window|pg_stat_statements_reset|2026-10-18 22:03:37+08\n'
      printf 'window|total_idx_scan|6715962614\n'
      printf 'window|indexes_total|2376\n'
      printf 'window|indexes_used|615\n' ;;
  *)
      printf 'stub|0|0\n' ;;
esac
exit 0
`
	p := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 psql: %v", err)
	}
	return p
}

func runUnusedIndexReport(t *testing.T, args ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	psql := fakePsql(t, dir)
	trace := filepath.Join(dir, "trace.txt")

	cmdArgs := append([]string{unusedIndexReportScript}, args...)
	cmd := exec.Command(bashForScriptFor(t, unusedIndexReportScript), cmdArgs...)
	cmd.Env = append(os.Environ(),
		"PSQL_CMD="+psql,
		"TRACE_FILE="+trace,
		"LLMGW_NO_SINGLE_INSTANCE_LOCK=1",
	)
	out, _ := cmd.CombinedOutput()

	traceBody, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("读调用轨迹失败（脚本可能没发出任何查询）: %v", err)
	}
	return string(traceBody), string(out)
}

func skipUnusedIndexWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 基线（既有，非回归）：守卫脚本与 psql 桩是 bash 轨迹")
	}
}

// TestUnusedIndexReportIsReadOnly 钉住①。
//
// 断言的是**脚本实际下发给 psql 的 SQL** 里没有写语句 —— 不是文件里没有。
// 因为工具可能把 SQL 拼在变量里再下发，文件级文本断言拦不住。
func TestUnusedIndexReportIsReadOnly(t *testing.T) {
	skipUnusedIndexWindows(t)

	trace, _ := runUnusedIndexReport(t)

	if !strings.Contains(trace, "--- sql ---") {
		t.Fatalf("脚本一次都没发出查询 —— 门禁空转（注入点失效？）\ntrace:\n%s", trace)
	}
	// ★ 大小写不敏感地扫，因为 PG 的 DDL 关键字大小写随人变。
	upper := strings.ToUpper(trace)
	for _, kw := range []string{
		"DROP ", "DELETE ", "TRUNCATE", "ALTER ", "UPDATE ", "INSERT ",
		"CREATE ", "REINDEX", "VACUUM", "ANALYZE", "REFRESH", "GRANT", "REVOKE",
	} {
		if strings.Contains(upper, kw) {
			t.Fatalf("★ 报告工具下发了写语句 %q —— 它是「删索引」路径的前置证据工具，"+
				"一旦能自己改生产，它的输出就不能再被当作只读证据\ntrace:\n%s", kw, trace)
		}
	}
}

// TestUnusedIndexReportAlwaysReportsStatsWindow 钉住②。
//
// 「idx_scan = 0」这句话离开窗口就没有意义：idx_scan 在 PG 重启时被清零，
// 而 pg_stat_statements 有**自己独立**的重置时间（本机实测 23 小时 vs 14 天）。
// ⇒ 工具必须把两个窗口都打出来，否则读数会显得比实际更有说服力。
func TestUnusedIndexReportAlwaysReportsStatsWindow(t *testing.T) {
	skipUnusedIndexWindows(t)

	trace, out := runUnusedIndexReport(t)

	if !strings.Contains(trace, "pg_postmaster_start_time") {
		t.Fatalf("工具没有查询 postmaster 启动时间 ⇒ idx_scan 的覆盖窗口不可知，"+
			"「零扫描」这个结论无法解释\ntrace:\n%s", trace)
	}
	if !strings.Contains(trace, "pg_stat_statements") {
		t.Fatalf("工具没有查询 pg_stat_statements 的容量/重置时间 ⇒ "+
			"读方普查的窗口不可知（同上）\ntrace:\n%s", trace)
	}
	// 输出里必须真的印出窗口行，而不只是查了它。
	if !strings.Contains(out, "window|") {
		t.Fatalf("输出里没有 window| 行 —— 窗口没有落到人眼可见的地方\nout:\n%s", out)
	}
	if !strings.Contains(out, "uptime") {
		t.Fatalf("输出里没有 uptime —— idx_scan 覆盖了多久仍不可知\nout:\n%s", out)
	}
}

// TestUnusedIndexReportClassifiesRegrowthAndConstraint 钉住③。
//
// 三类必须都在输出里出现，且分类依据必须体现在**实际下发的 SQL** 里：
//
//	C_叶分区索引(删了会长回来)   —— 34 个 / 1,233,600,512 B
//	D_约束附带(删索引=删约束)     —— 5 个 / 93,937,664 B
//	E_独立索引(删了就没了)         —— 18 个 / 405,692,416 B
//
// ⚠ 不分类的后果是**量化**的：E 类只有 405MB，而 C 类有 1.2GB。
//
//	混在一起的清单会让人以为「删索引能回收 1.6GB」，实际只有 405MB 且
//	另外 1.2GB 删了会长回来（§79：CREATE TABLE … PARTITION OF 会克隆父表索引）。
func TestUnusedIndexReportClassifiesRegrowthAndConstraint(t *testing.T) {
	skipUnusedIndexWindows(t)

	trace, out := runUnusedIndexReport(t)

	for _, want := range []string{"C_叶分区索引", "D_非分区表的约束附带索引", "E_非分区表的独立索引"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出里缺少分类 %q —— 三类处置方式完全不同，混在一起会误导\nout:\n%s", want, out)
		}
	}
	// 输出层：脚本确实把 DB 给的分类**打印出来**（桩按分类查询返回 canned 三行）。
	for _, want := range []string{"C_叶分区索引", "D_非分区表的约束附带索引", "E_非分区表的独立索引"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出里缺少分类 %q —— 三类处置方式完全不同，混在一起会误导\nout:\n%s", want, out)
		}
	}
	// SQL 层：分类必须**在 SQL 里算出来**（桩不执行 SQL，输出层验不了这个）。
	for _, want := range []string{"C_叶分区索引", "D_非分区表的约束附带索引", "E_非分区表的独立索引"} {
		if !strings.Contains(trace, want) {
			t.Fatalf("分类 SQL 里缺少标签 %q —— 分类没有在查询里算出来，"+
				"那它就只是输出里的三个字\ntrace:\n%s", want, trace)
		}
	}
	// 分类依据必须体现在**实际下发的 SQL** 里。
	if !strings.Contains(trace, "relispartition") || !strings.Contains(trace, "pg_inherits") {
		t.Fatalf("分类 SQL 里没有 relispartition / pg_inherits —— "+
			"「是不是叶分区」与「父表是谁」必须从目录里读出来\ntrace:\n%s", trace)
	}
	if !strings.Contains(trace, "pg_constraint") || !strings.Contains(trace, "conindid") {
		t.Fatalf("分类 SQL 里没有按 conindid 判断「是否约束附带」—— "+
			"「删索引 = 删约束」是最容易踩的坑\ntrace:\n%s", trace)
	}
}

// TestUnusedIndexReportPointsToReaderCensus 钉住「给证据不给结论」的边界。
//
// 工具必须明确把「谁在读」这件事指给 index-reader-census.sh ——
// 因为本工具只看**索引**的扫描数，看不到**表**的读方形态。
func TestUnusedIndexReportPointsToReaderCensus(t *testing.T) {
	skipUnusedIndexWindows(t)

	_, out := runUnusedIndexReport(t)

	if !strings.Contains(out, "index-reader-census.sh") {
		t.Fatalf("输出没有指向 index-reader-census.sh —— "+
			"本工具只回答「这条索引没人用过」，读方证据必须由普查工具取\nout:\n%s", out)
	}
}
