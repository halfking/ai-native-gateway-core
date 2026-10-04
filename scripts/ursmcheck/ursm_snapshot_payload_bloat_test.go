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
