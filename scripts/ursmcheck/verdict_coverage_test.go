package ursm_test

// R95 轮 · 段 C 的门：**verdict 覆盖自检器的行为契约**。
//
// 被测对象：scripts/audit/check-verdict-coverage.py
//
// 为什么需要它：任何「跑一批包再统计」的脚本，都会在某包【没跑全】时把该包
// 记成「-」（未观测），汇总表再把「- → FAIL」写成「判定变化」⇒ ⇒ 假变化。
// R93 轮实测：矩阵 A1 档有 4 个包没有留下 verdict 行，汇总表因此报出 12 条
// 「判定变化」，真实只有 8 条 ⇒ ⇒ 根因就是缺这道自检。
//
// 这道门用夹具验证六件事：
//   1. 全部覆盖 ⇒ 退出码 0（可以给结论）
//   2. 部分覆盖 ⇒ 退出码 2（拒绝给结论），且状态标成 partial
//   3. 日志在但没有 verdict ⇒ 退出码 2，状态标成 no_verdict
//   4. 日志不在 ⇒ 退出码 2，状态标成 absent
//   5. 期望清单本身不合法（空 / 行格式错）⇒ 退出码 3
//   6. ★ 计数口径：子测试名（TestParent/sub）必须归并到顶层函数名，
//      不得被计入 out-of-scope 计数；真正 out-of-scope 的函数仍须被计入。
//
// ★ 门自身的教训（R95 段三 · 变异 M2 实证）：
//   第一版的「子测试归并」断言只看【退出码 + 输出措辞】，而变异前后这两者
//   完全相同（missing 计算对子测试天然鲁棒）⇒ ⇒ 恒绿门。
//   ⇒ 修法：**把断言锚到两个实现真正给出不同读数的量上**（out-of-scope 计数）。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const vccChecker = "../audit/check-verdict-coverage.py"

// vccReport 是检查器 --json 输出的结构（字段名以检查器为准）。
type vccReport struct {
	Arch               string `json:"arch"`
	Packages           int    `json:"packages"`
	ExpectedFunctions  int    `json:"expected_functions"`
	CoveredFunctions   int    `json:"covered_functions"`
	OutOfScopeVerdicts int    `json:"out_of_scope_verdicts"`
	Incomplete         []any  `json:"incomplete"`
	OK                 bool   `json:"ok"`
}

// vccRun 在临时目录里造夹具并跑检查器，返回 (退出码, 输出)。
func vccRun(t *testing.T, scope string, logs map[string]string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	scopePath := filepath.Join(dir, "scope.tsv")
	if err := os.WriteFile(scopePath, []byte(scope), 0o600); err != nil {
		t.Fatalf("写 scope 失败: %v", err)
	}
	for name, body := range logs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("写日志失败: %v", err)
		}
	}
	cmd := exec.Command("python3", vccChecker, "--scope", scopePath, "--logs", dir, "--arch", "T")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑检查器失败: %v（输出: %s）", err, out)
	}
	return code, string(out)
}

// vccRunJSON 同 vccRun，但要求检查器以 --json 输出并解析之。
func vccRunJSON(t *testing.T, scope string, logs map[string]string) (int, vccReport) {
	t.Helper()
	dir := t.TempDir()
	scopePath := filepath.Join(dir, "scope.tsv")
	if err := os.WriteFile(scopePath, []byte(scope), 0o600); err != nil {
		t.Fatalf("写 scope 失败: %v", err)
	}
	for name, body := range logs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("写日志失败: %v", err)
		}
	}
	cmd := exec.Command("python3", vccChecker, "--scope", scopePath, "--logs", dir, "--arch", "T", "--json")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("跑检查器失败: %v（输出: %s）", err, out)
	}
	var rep vccReport
	if uerr := json.Unmarshal(out, &rep); uerr != nil {
		t.Fatalf("解析 --json 输出失败: %v（输出: %s）", uerr, out)
	}
	return code, rep
}

const vccScopeOne = "pkg/a\t^(TestAlpha|TestBeta)$\n"

// TestVerdictCoverageCheckerContract 检查器的退出码与状态标注契约。
func TestVerdictCoverageCheckerContract(t *testing.T) {
	cases := []struct {
		name      string
		scope     string
		logs      map[string]string
		wantCode  int
		wantWords []string
	}{
		{
			name:  "全部覆盖 ⇒ 0",
			scope: vccScopeOne,
			logs: map[string]string{
				"T__pkg_a.log": "--- PASS: TestAlpha (0.01s)\n--- FAIL: TestBeta (0.02s)\n",
			},
			wantCode:  0,
			wantWords: []string{"可以给结论"},
		},
		{
			name:  "部分覆盖 ⇒ 2 且标 partial",
			scope: vccScopeOne,
			logs: map[string]string{
				"T__pkg_a.log": "--- PASS: TestAlpha (0.01s)\n",
			},
			wantCode:  2,
			wantWords: []string{"partial", "拒绝给结论", "TestBeta"},
		},
		{
			name:  "日志在但无 verdict ⇒ 2 且标 no_verdict",
			scope: vccScopeOne,
			logs: map[string]string{
				"T__pkg_a.log": "panic: something went wrong\n",
			},
			wantCode:  2,
			wantWords: []string{"no_verdict", "拒绝给结论"},
		},
		{
			name:      "日志不在 ⇒ 2 且标 absent",
			scope:     vccScopeOne,
			logs:      map[string]string{},
			wantCode:  2,
			wantWords: []string{"absent", "拒绝给结论"},
		},
		{
			name:      "期望清单为空 ⇒ 3",
			scope:     "# 只有注释\n",
			logs:      map[string]string{},
			wantCode:  3,
			wantWords: []string{"分母为 0"},
		},
		{
			name:      "scope 行格式错 ⇒ 3",
			scope:     "pkg/a 没有制表符\n",
			logs:      map[string]string{},
			wantCode:  3,
			wantWords: []string{"格式错"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := vccRun(t, tc.scope, tc.logs)
			if code != tc.wantCode {
				t.Errorf("退出码 = %d，期望 %d\n输出:\n%s", code, tc.wantCode, out)
			}
			for _, w := range tc.wantWords {
				if !strings.Contains(out, w) {
					t.Errorf("输出缺少 %q\n输出:\n%s", w, out)
				}
			}
		})
	}
}

// TestVerdictCoverageSubtestAccounting 量具自检：子测试名必须归并到顶层函数名，
// 且【不得】被计入 out-of-scope 计数；真正 out-of-scope 的函数仍须被计入。
//
// `go test` 的子测试 verdict 形如 `--- PASS: TestParent/sub_name (0.01s)`。
// Go 顶层测试函数名不可能含 `/` ⇒ `/` 一律是子测试分隔符 ⇒ 必须归并。
//
// ★ 为什么断言锚在【out-of-scope 计数】而不是【退出码】：
//
//	期望集合 ⊆ 实测集合 的判据对子测试天然鲁棒（父函数 verdict 在，子测试在不在
//	都不改变 missing）⇒ ⇒ 任何只看退出码的「子测试归并」断言必然恒绿
//	（变异 M2 已实证）。两个实现真正给出不同读数的量是 out-of-scope 计数：
//	归并 ⇒ 子测试不进 extra；不归并 ⇒ 每个子测试各进一次 extra。
func TestVerdictCoverageSubtestAccounting(t *testing.T) {
	scope := "pkg/a\t^(TestParent)$\n"
	cases := []struct {
		name      string
		logs      map[string]string
		wantExtra int
		wantCover int
	}{
		{
			name: "只有父函数与它的子测试 ⇒ out-of-scope 必须是 0",
			logs: map[string]string{
				"T__pkg_a.log": "--- PASS: TestParent (0.01s)\n" +
					"--- PASS: TestParent/sub_one (0.00s)\n" +
					"--- PASS: TestParent/sub_two (0.00s)\n",
			},
			wantExtra: 0,
			wantCover: 1,
		},
		{
			name: "多一个真正 out-of-scope 的函数 ⇒ 只计 1（子测试仍不计入）",
			logs: map[string]string{
				"T__pkg_a.log": "--- PASS: TestParent (0.01s)\n" +
					"--- PASS: TestParent/sub_one (0.00s)\n" +
					"--- PASS: TestParent/sub_two (0.00s)\n" +
					"--- PASS: TestZebra (0.00s)\n",
			},
			wantExtra: 1,
			wantCover: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, rep := vccRunJSON(t, scope, tc.logs)
			if code != 0 {
				t.Errorf("退出码 = %d，期望 0（期望函数已覆盖 ⇒ 可以给结论）", code)
			}
			if !rep.OK {
				t.Errorf("ok = false，期望 true（incomplete: %v）", rep.Incomplete)
			}
			if rep.OutOfScopeVerdicts != tc.wantExtra {
				t.Errorf("out_of_scope_verdicts = %d，期望 %d\n"+
					"（子测试名必须归并到顶层函数名，不得计入 out-of-scope）",
					rep.OutOfScopeVerdicts, tc.wantExtra)
			}
			if rep.CoveredFunctions != tc.wantCover {
				t.Errorf("covered_functions = %d，期望 %d", rep.CoveredFunctions, tc.wantCover)
			}
			if rep.ExpectedFunctions != 1 {
				t.Errorf("expected_functions = %d，期望 1", rep.ExpectedFunctions)
			}
		})
	}
}
