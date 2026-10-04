package ursm_test

// ursm-snapshot-payload-bloat.sh 的行为门（2026-10-04）
//
// 这道门存在的理由和脚本本身一样：**退出码路径不跑到就等于没写**。
// 尤其「量具跑不通 ⇒ 记成跳过 ⇒ exit 0 报成功」这个形态历史上真的坑过
// 一次（go-cache-guard 的 launchd 环境里 go 不在 PATH，脚本静默跳过主缓存
// 检查，天天报绿，而主缓存实际 89 GiB）。所以这里对**每一条**分支都断言
// 退出码，尤其是「没有结论 ⇒ 3」与「健康 ⇒ 0」必须可区分。
//
// 注入方式是脚本的 PSQL_CMD 覆盖点，不改动生产形态。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const bloatScript = "252-monitor/ursm-snapshot-payload-bloat.sh"

// runBloat 用一个假的 psql 顶替，按行返回预设输出。
func runBloat(t *testing.T, stub string, env ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+stub+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	cmd := exec.Command("bash", bloatScript)
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(),
		"PSQL_CMD="+fake,
		"SAMPLE_ROWS=1000", // 与测试里的 0 行/少行阈值配套
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

func TestBloatScriptHealthyExitsZero(t *testing.T) {
	// n=1000, age=120s, avg=24, p95=25
	code, out, _ := runBloat(t, `echo "1000|120|24|25"`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (healthy)\n%s", code, out)
	}
	if !strings.Contains(out, "未检出膨胀") {
		t.Fatalf("healthy run must say so explicitly:\n%s", out)
	}
}

func TestBloatScriptZeroRowsIsNoConclusionNotHealthy(t *testing.T) {
	// ★ 这条是本文件的核心：样本 0 行必须 exit 3，绝不能 exit 0。
	// 「查不到」≠「没问题」——记成跳过并报成功是反复出现的事故形态。
	code, out, errOut := runBloat(t, `echo "0||0|0"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (no conclusion), got:\n%s", code, out)
	}
	if !strings.Contains(out+errOut, "参照系") && !strings.Contains(out+errOut, "没有结论") {
		t.Fatalf("must explain that it is inconclusive:\nstdout=%s\nstderr=%s", out, errOut)
	}
}

func TestBloatScriptStaleSampleIsNoConclusion(t *testing.T) {
	// 快照写入停了 → 最新行越来越旧 → 此时报告的是历史值，不能当现状。
	code, out, _ := runBloat(t, `echo "1000|999999|24|25"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (stale sample), got:\n%s", code, out)
	}
}

func TestBloatScriptThinSampleIsNoConclusion(t *testing.T) {
	// 只取到 3/1000 行：样本不具代表性。
	code, out, _ := runBloat(t, `echo "3|120|24|25"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (thin sample), got:\n%s", code, out)
	}
}

func TestBloatScriptBloatExitsOne(t *testing.T) {
	// p95 超过阈值 ⇒ exit 1，且必须给出键级诊断。
	code, out, _ := runBloat(t, `
if [ "${2#*LIMIT}" = "" ]; then :; fi
case "$1" in
  *percentile_cont*) echo "1000|120|180|205" ;;
  *jsonb_each*)      echo "updated_at_ms | 800 | 15 | 9.8 kB" ;;
  *)                echo "1000|120|180|205" ;;
esac`)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (bloat detected), got:\n%s", code, out)
	}
	if !strings.Contains(out, "updated_at_ms") {
		t.Fatalf("bloat report must include the per-key diagnosis:\n%s", out)
	}
}

func TestBloatScriptWrongFieldCountAborts(t *testing.T) {
	// 字段数不对必须 abort：下游解析会把缺失字段当 0，于是「全部报 0 ⇒ 报 OK」。
	code, out, _ := runBloat(t, `echo "1000|120|24"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (field count mismatch), got:\n%s", code, out)
	}
}

func TestBloatScriptQueryFailureAborts(t *testing.T) {
	code, _, errOut := runBloat(t, `echo "psql: FATAL:  terminating connection" >&2; exit 1`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (instrument unavailable)", code)
	}
	if !strings.Contains(errOut, "量具不可用") && !strings.Contains(errOut, "ABORT") {
		t.Fatalf("instrument failure must be labelled, got stderr=%s", errOut)
	}
}

// 判据只按字节，不按键名 ⇒ 键名清单漂移不会让判据失效。
// 反过来也要钉住：判据**必须**用字节。用「payload 里有没有某个键」当判据
// 会在有人给那个键改名时静默变成恒绿。
//
// ★ 键名清单**从 writer.go 的权威定义解析**，不在本测试里重抄一份：
//
//	重抄就是「7 vs 31 就是这么来的」那类漂移的成因。
func TestBloatScriptVerdictDoesNotDependOnKeyNames(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", bloatScript))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	s := string(src)
	verdictStart := strings.Index(s, "if [ \"$p95_b\" -le \"$BLOAT_BYTES\" ]")
	if verdictStart < 0 {
		t.Fatal("verdict block not found — the script changed shape; re-check this gate")
	}
	verdictEnd := strings.Index(s[verdictStart:], "exit 0")
	if verdictEnd < 0 {
		t.Fatal("healthy exit not found after the verdict block")
	}
	verdict := s[verdictStart : verdictStart+verdictEnd]

	if strings.Contains(verdict, "jsonb_each") {
		t.Fatalf("verdict must key off bytes only; it inspects the payload structure:\n%s", verdict)
	}

	// 从权威定义解析键名表：var payloadDuplicateKeys = [...]string{ "k1", ... }
	writer, err := os.ReadFile(filepath.Join("..", "..", "domains", "ursm", "v2", "persist", "writer.go"))
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	w := string(writer)
	m := regexp.MustCompile(`(?s)var payloadDuplicateKeys = \[\.\.\.\]string\{(.*?)\n\}`).FindStringSubmatch(w)
	if m == nil {
		t.Fatal("payloadDuplicateKeys not found in writer.go — re-check this gate")
	}
	re := regexp.MustCompile(`"([a-z0-9_]+)"`)
	var keys []string
	for _, k := range re.FindAllStringSubmatch(m[1], -1) {
		keys = append(keys, k[1])
	}
	if len(keys) < 30 {
		t.Fatalf("parsed only %d keys from payloadDuplicateKeys — the gate's own input "+
			"looks stale; a partial list would let real key-based verdicts slip through", len(keys))
	}
	for _, k := range keys {
		if strings.Contains(verdict, k) {
			t.Fatalf("verdict references payload key %q — key-based verdicts go silently "+
				"green the day someone renames that key:\n%s", k, verdict)
		}
	}
}

// ---------------------------------------------------------------------------
// --footprint 模式（设计稿 §12.6 那条「待实测」的可执行形态）
// ---------------------------------------------------------------------------

func TestFootprintModeInToleranceExitsZero(t *testing.T) {
	// n=19000000, bytes/row=250, heap, idx, 非分区, 0 分区
	code, out, _ := runBloatFootprint(t, `echo "19000000|250|8500 MB|1502 MB|0|0|18900000"`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (within tolerance), got:\n%s", code, out)
	}
	if !strings.Contains(out, "825 能回收的字节确实很少") {
		t.Fatalf("in-tolerance run must state the interpretation:\n%s", out)
	}
}

func TestFootprintModeEmptyTableIsNoConclusion(t *testing.T) {
	// ★ 0 行必须 exit 3。堆占用核验在空表上算出来的「每行字节」是除零产物。
	code, out, _ := runBloatFootprint(t, `echo "0|0|0 kB|0 kB|0|0|0"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (no reference), got:\n%s", code, out)
	}
}

// ★ 这条是 M49 变异暴露出来的形状缺口，原始那条输入不够。
//
//	脚本有两道守卫：① `fr_n > 0`（表非空）② `fr_bpr/live > 0`（分母可信）。
//	原始输入 `0|0|0 kB|0 kB|0|0|0` 让**两道同时**命中，所以只删掉①时②还在顶着
//	⇒ 门照样绿 ⇒ 看起来「①无用」，其实是「①和②在这个输入上重叠」。
//	等价于只有一道防御：只要有一道在，永远绿。
//
//	本条构造一个只让①能拦住的输入：**行数 0，但下游数字看着完全正常**
//	（每行字节 500、n_live_tup 9999）。真库里这组数不会出现（count=0 ⇒ 表空），
//	但它正是量具该守的形状：「下游数值看起来对」不能替代「有参照系」。
func TestFootprintModeEmptyTableNotOutrankedByPlausibleDownstreamNumbers(t *testing.T) {
	code, out, errOut := runBloatFootprint(t, `echo "0|500|8500 MB|1502 MB|0|0|9999"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 — 行数为 0 就没有参照系，下游数字再正常也不能出结论:\n%s\n%s",
			code, out, errOut)
	}
	combined := out + errOut
	if strings.Contains(combined, "偏高") || strings.Contains(combined, "偏低") {
		t.Fatalf("must not report a direction for a zero-row table:\n%s", combined)
	}
}

func TestFootprintModeHighBytesIsDeviation(t *testing.T) {
	// 400 B/行 vs 期望 248 ⇒ 偏离 61% ⇒ exit 1
	code, out, _ := runBloatFootprint(t, `echo "19000000|400|8500 MB|1502 MB|0|0|18900000"`)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (deviation), got:\n%s", code, out)
	}
	// ★ 偏离方向必须被说出来：偏高=有空闲可回收，偏低=行宽更小不是坏事。
	//   压成一个布尔会让人分不清该做什么。
	for _, want := range []string{"偏高", "偏低"} {
		if !strings.Contains(out, want) {
			t.Fatalf("deviation report must explain both directions, missing %q:\n%s", want, out)
		}
	}
}

func TestFootprintModeLowBytesIsAlsoDeviationButNotBad(t *testing.T) {
	// 100 B/行 ⇒ 偏离 60%，仍是 exit 1（偏离就是偏离），但解读必须说「不是坏事」
	code, out, _ := runBloatFootprint(t, `echo "19000000|100|8500 MB|1502 MB|0|0|18900000"`)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "不是坏事") {
		t.Fatalf("a below-expectation reading must be explained as benign:\n%s", out)
	}
}

func TestFootprintModeQueryFailureAborts(t *testing.T) {
	code, _, errOut := runBloatFootprint(t, `echo "psql: FATAL: boom" >&2; exit 1`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if !strings.Contains(errOut, "ABORT") {
		t.Fatalf("instrument failure must be labelled: %s", errOut)
	}
}

func TestFootprintModeReportsPartitionedShape(t *testing.T) {
	// 825 落地后 relkind='p'，父表 pg_total_relation_size 近乎 0 —— 形态必须被读出来
	code, out, _ := runBloatFootprint(t, `echo "19000000|250|8500 MB|1502 MB|1|8|18900000"`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0, got:\n%s", code, out)
	}
	if !strings.Contains(out, "分区父表") || !strings.Contains(out, "8 个分区") {
		t.Fatalf("partitioned shape must be reported:\n%s", out)
	}
}

func runBloatFootprint(t *testing.T, stub string, env ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-psql")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+stub+"\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	cmd := exec.Command("bash", bloatScript, "--footprint")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "PSQL_CMD="+fake, "SAMPLE_ROWS=1000")
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

// ★ 真库实跑抓到的量具缺陷：分母 n_live_tup 可能为 0（统计采集器没跟上）。
//
//	用 greatest(...,1) 兜底会把「统计没跟上」放大成「每行字节巨大」，
//	然后被报成「偏高 ⇒ 有空闲可回收」—— 一个会误导人的结论。
//	本条要求：分母不可信时判「没有结论」，不得给出偏高/偏低判定。
func TestFootprintModeUnreliableDenominatorIsNoConclusion(t *testing.T) {
	// n_live_tup=0（统计没跟上），reltuples 推出的每行字节巨大
	code, out, errOut := runBloatFootprint(t, `echo "19000000|999999|8500 MB|1502 MB|0|0|0"`)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (denominator untrustworthy), got:\n%s", code, out+errOut)
	}
	// ★ 断言必须查 **stdout + stderr 的合并**。
	//   脚本把 ABORT 与解释写进 stderr（>&2），stdout 在这条路径上是空的。
	//   只查 stdout 会造成两件事同时发生：
	//     · 「必须解释没有结论」那条 ⇒ 永远查不到 ⇒ 假红；
	//     · 「不得出现偏高/偏低」那条 ⇒ 查的是空串 ⇒ **恒过、无牙**，
	//       脚本哪天真的打出「偏高」也照样绿。
	// ★ 断言 1「不得给出方向判定」**必须**查 stdout+stderr 合并流：
	//   `Contains(combined, "偏高")` 在改查 stdout 后落在空串上 ⇒ 恒假 ⇒
	//   这条断言变成恒过、**无牙**（M57 实测确认）。
	//   而断言 2「必须解释」不一样：`!Contains(out, "ABORT")` 在空串上是 true
	//   ⇒ 它退化成**恒红**，天生有牙，不依赖合并流（M58 实测推翻了
	//   我最初写的「两条的牙都来自合并流」——只有断言 1 是）。
	//   同一条断言各自的来源必须分别验，不能整块换掉后只看红绿。
	combined := out + errOut
	if strings.Contains(combined, "偏高") || strings.Contains(combined, "偏低") {
		t.Fatalf("must NOT report a deviation direction when the denominator is broken:\n%s", combined)
	}
	if !strings.Contains(combined, "ABORT") && !strings.Contains(combined, "行数估算") {
		t.Fatalf("must explain why there is no conclusion:\n%s", combined)
	}
	// 自证：这次真的拿到了非空的 stderr。若脚本哪天把解释挪到 stdout，
	// 或干脆什么都不打，上面两条都会退化成空检查 —— 这里先把「确实有文本」钉住。
	if strings.TrimSpace(errOut) == "" {
		t.Fatalf("expected the reason to be reported on stderr, got nothing:\nstdout=%q", out)
	}
}
