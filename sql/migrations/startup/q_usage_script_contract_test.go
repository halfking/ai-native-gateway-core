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
	return runQUsageScriptWithPath(t, dir, scriptPath, "", args...)
}

// runQUsageScriptWithPath 在指定目录、指定 PATH 前缀下跑一次脚本。
// ★ PATH 前缀用于**注入坏掉的同名工具**（下面两条 2026-11-08 新增门要用）。
func runQUsageScriptWithPath(t *testing.T, dir, scriptPath, pathPrefix string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
	cmd.Dir = dir
	if pathPrefix != "" {
		cmd.Env = append(os.Environ(), "PATH="+pathPrefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
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

// writeStubTool 在 dir 下写一个「永远失败」的可执行桩，模拟同名工具的坏掉形态。
func writeStubTool(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("写桩 %s 失败: %v", path, err)
	}
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
		"gzip -cd",      // awk 读不了 .gz，必须先解压（★ 2026-11-08：由 `zcat -f` 改为 POSIX 的 gzip -cd）
		"?q=",           // token 精确的匹配口径
		"&q=",
		"轮转", // 轮转未命中时必须告警
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%s 里找不到 %q —— 脚本要么退化，要么注释没跟上", qUsageScript, want)
		}
	}
	// ★★ 2026-11-08（12h 审计 F-1）：**不许再用 `zcat -f --` 这个 GNU 专有调用**。
	//   `zcat -f`（force：非 gzip 直通）是 GNU gzip 扩展；busybox/Alpine 的 zcat
	//   没有这个语义 ⇒ 三族混读时**整体失败**（rc=1、stdout 空）⇒ 计数器恒为 0，
	//   而「计数器恒为 0」正是脚本自己声明的**最危险失效模式**。
	//   ⇒ 注释里可以提这段历史（"曾用 zcat -f"），但**不许**再作为调用出现。
	if strings.Contains(src, "zcat -f --") {
		t.Errorf("%s 仍在调用 `zcat -f --`：那是 GNU gzip 专有扩展，"+
			"busybox/Alpine 上混读三族会整体失败 ⇒ 计数器恒为 0（脚本自己声明的最危险失效模式）", qUsageScript)
	}
	// 裸 `q=` 匹配是本仓踩过的坑（会匹配 seq=/req=/limit=），不许出现。
	if strings.Contains(src, `index($0, "q=")`) {
		t.Errorf("%s 出现了裸 `q=` 匹配：裸 q= 会把 seq=/req=/limit=/mode= 全匹配进来",
			qUsageScript)
	}
}

// TestQUsageScriptDoesNotDependOnGNUSpecificZcat —— ★ 2026-11-08（12h 审计 F-1）新增行为门。
//
// ★ 判据：**PATH 里放一个「永远失败」的 `zcat` 桩，自检必须照样通过**。
//
//	⇒ 证明脚本已经**不再依赖 `zcat`**（更不依赖它的 GNU 专有 `-f` 语义）。
//	变异验证：把 `read_logs` 改回 `zcat -f --` ⇒ 桩生效 ⇒ 自检红 ⇒ 本门变红。
func TestQUsageScriptDoesNotDependOnGNUSpecificZcat(t *testing.T) {
	binDir := t.TempDir()
	// 模拟 busybox zcat 遇到非 gzip 输入时的形态：整体失败、stdout 空、rc=1。
	writeStubTool(t, binDir, "zcat", "echo 'zcat: no gzip/bzip2/xz magic' >&2\nexit 1\n")

	out, code := runQUsageScriptWithPath(t, ".", qUsageScript, binDir, "--self-test")
	if code != 0 {
		t.Fatalf("★ PATH 里有坏 zcat 时自检失败 ⇒ 脚本仍然依赖 `zcat`（及其 GNU 专有 `-f` 语义）⇒ "+
			"在 busybox/Alpine 上会「计数器恒为 0」。输出：\n%s", out)
	}
	if !strings.Contains(out, "self-test: ALL PASS") {
		t.Errorf("坏 zcat 桩下自检输出缺少收尾标记，输出：\n%s", out)
	}
}

// TestQUsageScriptFailsClosedWhenLogReadFails —— ★ 2026-11-08（12h 审计 F-1）新增行为门。
//
// ★ 判据：**日志读不出来 ⇒ 必须拒绝打印判定**。
//
//	旧实现：读失败只打一条 WARN，然后照样打印
//	「verdict: 窗口内 0 次带 q= ⇒ 未触发「保留 877」的触发条件」
//	⇒ 一次**读失败**被渲染成一条**看起来很确定的负面基础设施结论**
//	⇒ 877 决策会被一次环境差异静默翻转。
//	⇒ 新口径：**读失败 ≠ 「0 次」**，读失败时退出码非 0 且**不出现 verdict**。
func TestQUsageScriptFailsClosedWhenLogReadFails(t *testing.T) {
	work := t.TempDir()
	logDir := filepath.Join(work, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("建日志目录失败: %v", err)
	}
	// 一个普通（未压缩）日志文件 + 一个 .gz 文件 ⇒ 触发 read_logs 的两条分派路径。
	if err := os.WriteFile(filepath.Join(logDir, "access.log"),
		[]byte("1.1.1.1 - - [19/Oct/2026:10:00:00 +0800] \"GET /api/logs?q=hello HTTP/1.1\" 200 1 \"-\" \"ua\"\n"), 0o644); err != nil {
		t.Fatalf("写日志失败: %v", err)
	}
	gzBody := "1.1.1.1 - - [19/Oct/2026:10:00:01 +0800] \"GET /api/logs?q=rotated HTTP/1.1\" 200 1 \"-\" \"ua\"\n"
	if err := os.WriteFile(filepath.Join(work, "gzsrc"), []byte(gzBody), 0o644); err != nil {
		t.Fatalf("写 gz 源失败: %v", err)
	}
	// 用系统 gzip 生成真正的 .gz（必须在注入坏 gzip 之前完成）。
	gzCmd := exec.Command("gzip", "-c", filepath.Join(work, "gzsrc"))
	gzOut, err := gzCmd.Output()
	if err != nil {
		t.Skipf("本机没有可用的 gzip，跳过本门: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "access.log-20261018.gz"), gzOut, 0o644); err != nil {
		t.Fatalf("写 .gz 失败: %v", err)
	}

	binDir := t.TempDir()
	// 让 gzip -cd 失败（模拟 .gz 损坏 / gzip 不可用）⇒ read_logs 返回非 0。
	writeStubTool(t, binDir, "gzip", "echo 'gzip: broken stub' >&2\nexit 1\n")

	out, code := runQUsageScriptWithPath(t, ".", qUsageScript, binDir, "--log-dir", logDir)
	if code == 0 {
		t.Fatalf("★ 日志读取失败时脚本仍然退出 0 ⇒ 「读不出来」被当成「0 次」。输出：\n%s", out)
	}
	if strings.Contains(out, "verdict:") {
		t.Errorf("★ 日志读取失败时仍然打印了 verdict ⇒ 一次读失败被渲染成了确定的负面结论"+
			"（877 决策会被静默翻转）。输出：\n%s", out)
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("读取失败时没有明确的 ERROR 说明，输出：\n%s", out)
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
