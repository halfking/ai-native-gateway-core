package ursm_test

// ursm-snapshot-health.sh 的运行时基线跨度检查（2026-10-18，§86 第八节）
//
// 这道门覆盖的是**仓库侧静态门（baseline_retention_window_test.go）完全够不着**的那一半：
// 静态门只比对两个**默认值**，防不住生产 env 被调小。
// 本门测的是脚本读到「有数据的分区数 < BASELINE_DAYS」时会不会告警。
//
// ★ 本脚本此前**没有任何行为测试**，只有 cron_registration_test.go 钉住
//   「它被注册了」。本轮为此加了 PSQL_CMD 注入点（默认形态逐字不变）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const healthPath = "252-monitor/ursm-snapshot-health.sh"

// runHealth 用假 psql 按「问题特征」分派返回值，按脚本原有的 de() 调用顺序响应。
func runHealth(t *testing.T, stub string, env ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+stub+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	cmd := exec.Command("bash", healthPath)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(),
		"PSQL_CMD="+fake,
		// 关掉可能被 $HOME 下真实配置影响的开关（脚本第 142 行会 source $CONF）。
		"CONF=/nonexistent/health-test.conf",
	)
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run script: %v", err)
	}
	return code, stdout.String(), stderr.String()
}

// 好数据：10 个判据字段全齐、无降级、基线 8 天（有 8 个非空分区）。
const okSummary = "3|0|0|0|0|3|318520|158498|321269|100"

func TestHealthWarnsWhenBaselineHasTooFewDays(t *testing.T) {
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "2026-10-18|2026-10-11|3|p|8" ;;   # ← 只剩 3 天（relkind=p, 分区总数 8）
  *ref_act*|*"WITHIN GROUP"*) echo '` + okSummary + `' ;;
  *)                        echo '` + okSummary + `' ;;
esac`
	_, _, stderr := runHealth(t, stub)
	if !strings.Contains(stderr, "基线样本不足") {
		t.Fatalf("表里只有 3 天而 BASELINE_DAYS=7 时必须告警，stderr 却没有：\n%s", stderr)
	}
	if !strings.Contains(stderr, "偏宽松") {
		t.Fatalf("告警必须说清「本次 OK 是偏宽松的」，否则运维会当成真 OK：\n%s", stderr)
	}
}

func TestHealthStaysQuietWhenBaselineIsAdequate(t *testing.T) {
	// 生产实测形态：8 个非空分区（+1 个预建的 0 字节空分区，已被 size>0 排除）。
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "2026-10-18|2026-10-11|8|p|9" ;;   # relkind=p, 9 个分区（8 有数据 + 1 预建空分区）
  *)                        echo '` + okSummary + `' ;;
esac`
	_, stdout, stderr := runHealth(t, stub)
	if strings.Contains(stderr, "基线样本不足") {
		t.Fatalf("8 天 ≥ BASELINE_DAYS=7 时不应告警：\n%s", stderr)
	}
	if !strings.Contains(stdout, "baseline span: 8 day(s) with data") {
		t.Fatalf("体检读数必须进日志（否则事后无法追溯）：\n%s", stdout)
	}
}

func TestHealthSpanProbeFailureDoesNotAbortHealthyRun(t *testing.T) {
	// ★ 探针只是告警探针。它挂掉时绝不能把一台健康的机器判成 exit 3
	//   —— 那会把「体检不可用」渲染成「巡检失败」，与本脚本文件头的
	//   「量具不可用与真没问题必须可区分」是同一条纪律，只是方向相反。
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "ERROR:  probe exploded" ;;
  *)                        echo '` + okSummary + `' ;;
esac`
	code, stdout, stderr := runHealth(t, stub)
	if code != 0 {
		t.Fatalf("探针失败不得改变退出码，got %d（应仍为 0）\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "baseline-span probe failed") {
		t.Fatalf("探针失败必须留痕：\n%s", stdout)
	}
	if strings.Contains(stdout, "OK: 无真空洞") == false {
		t.Fatalf("主判据仍应正常出结论：\n%s", stdout)
	}
}

func TestHealthSummaryCarriesBaselineThinSuffix(t *testing.T) {
	// 薄基线必须**留在 summary 行**上：那是运维事后 grep 的唯一入口。
	// ★ 不能混进 10 个判据字段 —— 下游按位置取 awk 字段，混进去会错位。
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "2026-10-18|2026-10-11|3|p|8" ;;   # relkind=p, 分区总数 8
  *)                        echo '` + okSummary + `' ;;
esac`
	_, stdout, _ := runHealth(t, stub)
	line := ""
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "summary:") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("没找到 summary 行：\n%s", stdout)
	}
	if !strings.Contains(line, "baseline_thin=thin:3d/7d") {
		t.Fatalf("summary 行必须带 baseline_thin 后缀：\n%s", line)
	}
	// 10 个判据字段本身必须逐字不变 —— 后缀只能是追加。
	if !strings.Contains(line, okSummary) {
		t.Fatalf("判据字段被改动/错位了：\n%s", line)
	}
}

// TestHealthSpanSQLExcludesEmptyPartitions —— 覆盖**上面四条测试够不着**的那一半。
//
// ★ 为什么需要它：上面四条用的是固定返回值的假 psql，
//   **无论脚本发出什么 SQL，它都吐同一串** ⇒ SQL 语句被改坏而 shell 逻辑不变时，
//   四条全绿。变异验证实测：把 `pg_relation_size(c.oid) > 0` 去掉，四条依然全过。
//   ⇒ 这是「桩测试与真库验证不能互相替代」的第三例（§86 第七节是前两例的静态/运行时版）。
//
// ★ 它的边界要写清：这是**源码文本断言**，只能钉住「排除空分区」这个属性，
//   钉不住 SQL 的语法正确性（那个只能真跑一次，§86 已真跑过：
//   返回 `2026-10-18|2026-10-11|8`，8 而非 9）。
//   所以它与真库验证是**互补**的：前者防回归，后者保语法。
func TestHealthSpanSQLExcludesEmptyPartitions(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", healthPath))
	if err != nil {
		t.Fatalf("read %s: %v", healthPath, err)
	}
	src := stripFullLineComments(string(b))

	if !strings.Contains(src, "pg_relation_size(c.oid) > 0") {
		t.Fatalf("%s 的基线跨度查询必须用 pg_relation_size(c.oid) > 0 过滤空分区。\n"+
			"partition_manager 会预建明天的分区而它是 0 字节（实测 20261019 = 0 bytes /\n"+
			"reltuples = -1）；按分区名计数会把空占位算成「一天有数据」\n"+
			"（实测：不过滤返回 9，过滤返回 8）。", healthPath)
	}
}

// TestHealthSpanProbeStaysQuietOnNonPartitionedTable —— §143.32 的 P18。
//
// ★ 背景（211 实测）：`ursm_node_snapshot_min` 在 211 上是**普通表**（relkind='r'），
//   而这条探针只数「带日期的分区」⇒ 在普通表上**恒返回 0 天**
//   ⇒ 于是它每轮报「0 day(s) with data」+「基线样本不足 / OK 判定偏宽松」
//   ⇒ ★ **那是假告警**：211 的表里实测有 7 天数据（5,933,760 行）。
//
// ★ 修法：探针对「不适用」的形态必须说 **n/a**，不能把「数不到」说成「没有」；
//   而「基线真薄」的形态（分区表 + 天数不足）**必须继续告警**（见上面两条）。
func TestHealthSpanProbeStaysQuietOnNonPartitionedTable(t *testing.T) {
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "0|0|0|r|0" ;;   # relkind='r' ⇒ 非分区表
  *)                        echo '` + okSummary + `' ;;
esac`
	_, stdout, stderr := runHealth(t, stub)
	if strings.Contains(stderr, "基线样本不足") {
		t.Fatalf("★ 假告警：非分区表上探针数不到分区，**不能**报「基线样本不足」\n"+
			"（211 实测：表里其实有 7 天数据）\n%s", stderr)
	}
	if !strings.Contains(stdout, "baseline span: n/a") {
		t.Fatalf("非分区表上探针必须明确说 n/a（不能把「数不到」说成「0 天」）\n%s", stdout)
	}
	// ★ 主判据仍然必须出结论 —— 探针不适用不等于巡检不可用。
	if !strings.Contains(stdout, "OK: 无真空洞") {
		t.Fatalf("探针不适用时主判据仍应正常出结论：\n%s", stdout)
	}
}

// TestHealthSpanProbeStaysQuietWhenPartitionNamesDoNotMatch —— §143.32 的 P18 另一半。
//
// ★ 是分区父表、但没有一个分区名匹配 `_YYYYMMDD` ⇒ 命名约定不适用 ⇒ 同样不能报「0 天」。
func TestHealthSpanProbeStaysQuietWhenPartitionNamesDoNotMatch(t *testing.T) {
	stub := `
case "$1" in
  *"last_10minutes"*)      echo 30010 ;;
  *"_([0-9]{8})"*)         echo "0|0|0|p|4" ;;   # relkind='p'，4 个分区但没有一个名字匹配
  *)                        echo '` + okSummary + `' ;;
esac`
	_, stdout, stderr := runHealth(t, stub)
	if strings.Contains(stderr, "基线样本不足") {
		t.Fatalf("分区名不匹配时不能报「0 天数据」：\n%s", stderr)
	}
	if !strings.Contains(stdout, "baseline span: n/a") {
		t.Fatalf("分区名不匹配时探针必须说 n/a：\n%s", stdout)
	}
}

// TestHealthSpanProbeSQLCarriesShapeFields —— 钉住「探针必须带上形态判据」。
//
// ★ 为什么要有这条（源码文本断言）：上面的用例用的是**假 psql**，
//   它按 SQL 文本分派返回固定值 ⇒ ★ **SQL 少返回了形态字段，桩照样吐 5 段**，
//   ⇒ 脚本读到空的 relkind ⇒ 就会走进 n/a 分支 ⇒ ★ **「真薄基线要告警」那条会被静默跳过**。
//   ⇒ 所以「探针到底返回了什么」必须被源码文本钉住。
func TestHealthSpanProbeSQLCarriesShapeFields(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", healthPath))
	if err != nil {
		t.Fatalf("read %s: %v", healthPath, err)
	}
	src := stripFullLineComments(string(b))

	for _, n := range []string{
		"relkind::text",           // 表的形态
		"FROM pg_inherits WHERE inhparent", // 分区总数
		`!= "p"`,                  // 非分区父表 ⇒ n/a
		"baseline span: n/a",      // 「不适用」必须有明确措辞
	} {
		if !strings.Contains(src, n) {
			t.Fatalf("%s 的基线跨度探针必须包含 %q —— 探针要能区分「数不到」与「没有」。\n"+
				"§143.32 P18：211 上该表是普通表，探针恒返回 0 天 ⇒ 假告警。", healthPath, n)
		}
	}
}

// stripFullLineComments 去掉整行注释（以 # 开头，允许前导空白）。
// 注释里会写「为什么用 size>0」，若不剥掉，断言会把注释当成代码命中 ⇒ 恒绿。
func stripFullLineComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
