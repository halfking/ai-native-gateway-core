package startup

// q_usage_script_contract_test.go — `scripts/audit/q-usage.sh` 的契约门。
//
// 这道门守的是 **877 决策的触发条件取数工具**。877（search_text 的 GIN trigram
// 索引）的最终建议是「不回滚，但把 `?q=` 降级为显式启用」，并写死一条触发条件：
//
//	任一 7 天窗口内，生产出现 ≥ 1 次带 `q=` 的 `GET /api/logs` ⇒ 保留索引
//
// 这条触发条件**只有一个取数口径**：`scripts/audit/q-usage.sh`。
// 因此这个脚本一旦失准（匹配写错、漏枚举文件族、恒返回 0），
// 决策就会在**没人察觉**的情况下被翻转——而它恰恰是"0 次"这个读数，
// 最容易被写成"计数器坏了"而没人怀疑。
//
// ── 这道门守什么 ────────────────────────────────────────────────────────
// 1) 脚本自带的自检必须通过（`--self-test`）——它验证「计数器能数出非 0」。
// 2) ★ **门本身要能变红**：对脚本做两处**真实变异**（改坏匹配器 / 删掉一个文件族），
//    自检必须**失败**。否则这道门就是恒绿的装饰。
// 3) 脚本必须覆盖 logrotate 的三族文件（当前 / 未压缩轮转 / 压缩轮转）——
//    漏掉 `delaycompress` 产生的未压缩轮转文件，会漏掉**当天**的流量
//    （154 上实测少计 47 次、211 上少计 14 次）。
// 4) 匹配口径必须是 token 精确的 `[?&]q=`，**不许**回退成裸 `q=`
//    （裸 `q=` 会误配 `seq=` / `req=` / `limit=` / `mode=` / `omit_body=`）。
//
// 运行：go test ./sql/migrations/startup/ -run 'QUsage' -v
// （不需要数据库、不需要任何生产访问）

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const qUsageScript = "../../../scripts/audit/q-usage.sh"

// readQUsageScript 读取脚本源码；不存在时直接判红（不要静默跳过）。
func readQUsageScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(qUsageScript)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", qUsageScript, err)
	}
	return string(raw)
}

// runQUsageScript 在指定目录下跑一次脚本，返回 combined output 与退出码。
func runQUsageScript(t *testing.T, dir, scriptPath string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("执行 %s %v 失败: %v", scriptPath, args, err)
		}
	}
	return string(out), code
}

// TestQUsageScriptSelfTestPasses —— 脚本自带的离线自检必须通过。
//
// 这条断言看着弱，实则是整道门的地基：自检若恒绿，上面两条变异门就无从谈起。
func TestQUsageScriptSelfTestPasses(t *testing.T) {
	src := readQUsageScript(t)
	if !strings.Contains(src, "self-test: ALL PASS") {
		t.Fatalf("%s 里找不到自检的收尾标记 `self-test: ALL PASS`："+
			"自检要么被删了，要么改了输出格式（门已失去锚点）", qUsageScript)
	}
	out, code := runQUsageScript(t, ".", qUsageScript, "--self-test")
	if code != 0 {
		t.Fatalf("q-usage.sh --self-test 退出码 = %d，期望 0。输出：\n%s", code, out)
	}
	for _, want := range []string{
		"self-test: ALL PASS",
		"计数能数出非 0",
		"三族都枚举到",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("自检输出里缺少 %q，输出：\n%s", want, out)
		}
	}
}

// TestQUsageScriptDetectsBrokenMatcher —— ★ 变异验证 1：改坏匹配器，自检必须变红。
//
// 变异：`index($0, "?q=") || index($0, "&q=")` → `?zz=` / `&zz=`。
// 注意只改**匹配器**、不改自检夹具（夹具里的 `?q=hello` 保持原样），
// 这样计数结果必然从 3 掉到 0，自检的「计数能数出非 0」就会失败。
func TestQUsageScriptDetectsBrokenMatcher(t *testing.T) {
	const orig = `index($0, "?q=") || index($0, "&q=")`
	const mutated = `index($0, "?zz=") || index($0, "&zz=")`

	src := readQUsageScript(t)
	if strings.Count(src, orig) != 1 {
		t.Fatalf("匹配器 %q 在脚本里出现 %d 次（期望恰好 1 次）："+
			"变异点必须唯一，否则这道变异门测的不是它以为的东西", orig, strings.Count(src, orig))
	}

	tmp := t.TempDir()
	mutatedPath := filepath.Join(tmp, "q-usage-mutated.sh")
	if err := os.WriteFile(mutatedPath, []byte(strings.Replace(src, orig, mutated, 1)), 0o755); err != nil {
		t.Fatalf("写变异脚本失败: %v", err)
	}
	out, code := runQUsageScript(t, ".", mutatedPath, "--self-test")
	if code == 0 {
		t.Fatalf("★ 变异后自检仍然通过 ⇒ 这道门是恒绿的装饰。"+
			"（把匹配器改成 ?zz= 之后，计数器不可能再数出非 0，自检必须失败）输出：\n%s", out)
	}
	if !strings.Contains(out, "计数能数出非 0") {
		t.Errorf("变异后的失败原因不是「计数能数出非 0」，说明变异点变了：\n%s", out)
	}
}

// TestQUsageScriptDetectsMissingPlainRotatedFamily —— ★ 变异验证 2：删掉一个文件族，自检必须变红。
//
// 变异：把枚举「未压缩轮转」那一族的 pattern 改成一个永不匹配的名字。
// 这一族正是 logrotate `delaycompress` 的产物（`<name>.log-<YYYYMMDD>`，没有 .gz），
// 漏掉它 ⇒ 漏掉**当天**的流量（154 实测少计 47 次）。
func TestQUsageScriptDetectsMissingPlainRotatedFamily(t *testing.T) {
	const orig = `-name "${PREFIX}.log-*"`
	const mutated = `-name "${PREFIX}.log-NEVERMATCH-*"`

	src := readQUsageScript(t)
	if strings.Count(src, orig) != 1 {
		t.Fatalf("未压缩轮转族的 pattern %q 在脚本里出现 %d 次（期望恰好 1 次）："+
			"变异点必须唯一", orig, strings.Count(src, orig))
	}

	tmp := t.TempDir()
	mutatedPath := filepath.Join(tmp, "q-usage-mutated.sh")
	if err := os.WriteFile(mutatedPath, []byte(strings.Replace(src, orig, mutated, 1)), 0o755); err != nil {
		t.Fatalf("写变异脚本失败: %v", err)
	}
	out, code := runQUsageScript(t, ".", mutatedPath, "--self-test")
	if code == 0 {
		t.Fatalf("★ 变异后自检仍然通过 ⇒ 门测不到「漏枚举文件族」。输出：\n%s", out)
	}
	if !strings.Contains(out, "文件枚举") {
		t.Errorf("变异后的失败原因不是「文件枚举」，说明变异点变了：\n%s", out)
	}
}

// TestQUsageScriptCoversThreeLogFamilies —— 源码层钉住三族文件与两个工具坑。
//
// 这条是**文本契约**（不是行为契约）：行为由上面的变异门兜住，
// 这里守的是「注释与实现不会各走各的」——
// 哪天有人把 `delaycompress` 那族删了却留着注释，或反过来。
func TestQUsageScriptCoversThreeLogFamilies(t *testing.T) {
	src := readQUsageScript(t)
	for _, want := range []string{
		"delaycompress", // 未压缩轮转文件这一族
		"zcat -f",       // awk 读不了 .gz，必须先解压
		"?q=",           // token 精确的匹配口径
		"&q=",
		"轮转", // 轮转未命中时必须告警
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%s 里找不到 %q —— 脚本要么退化，要么注释没跟上", qUsageScript, want)
		}
	}
	// 裸 `q=` 匹配是本仓踩过的坑（会匹配 seq=/req=/limit=），不许出现。
	if strings.Contains(src, `index($0, "q=")`) {
		t.Errorf("%s 出现了裸 `q=` 匹配：裸 q= 会把 seq=/req=/limit=/mode= 全匹配进来",
			qUsageScript)
	}
}

// TestQUsageScriptIsExecutableBash —— 脚本要能直接跑（它是被 ssh 过去用的）。
func TestQUsageScriptIsExecutableBash(t *testing.T) {
	info, err := os.Stat(qUsageScript)
	if err != nil {
		t.Fatalf("stat %s 失败: %v", qUsageScript, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s 没有可执行位（mode=%s）", qUsageScript, info.Mode())
	}
	raw, err := os.ReadFile(qUsageScript)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", qUsageScript, err)
	}
	if !strings.HasPrefix(string(raw), "#!/bin/bash\n") {
		t.Errorf("%s 的 shebang 不是 `#!/bin/bash`（脚本用到 bash 3.2 兼容写法）", qUsageScript)
	}
}
