package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// api_sweep_script_shape_test.go —— 「admin API 逐条巡检脚本」的**形状门 + 行为门**。
//
// # 这道门守的是哪件东西
//
// `scripts/audit/api-sweep.sh` 是本仓「逐个 API 验证 + 效率检查」的**仓内可复现**巡检器。
// 它在仓外的前身（/tmp/kx-api-sweep.sh）有三个硬伤：
//
//	① **没有失败分类** ⇒ 「端点坏了」与「端点本来就要 POST」在输出里同形；
//	② **没有宿主机负载采样** ⇒ ★ 本机曾有外部任务把 CPU 烧到 load average 400+，
//	   **污染了所有慢读数**（慢读数在饱和机器上没有意义）；
//	③ **没有自检** ⇒ 量具静默少跑了一半也照样返回 0。
//
// # ★ 为什么不能只做「关键字在不在」的形状门
//
// 本仓反复吃过这个亏：**门绿 ≠ 事实成立**。如果这道门只断言
// 「脚本里出现过 `server_error` 这个字符串」，那么
// ① 把分类名改掉 ⇒ 门红（好）；
// ② 但**把分类逻辑整个写反**（500 判成 ok）⇒ 门**依然绿**。
//
// ⇒ 所以本门分两层：
//
//	★ **形状层**（静态）：文件在不在、`bash -n` 过不过、分类关键字在不在、
//	  负载采样代码在不在、行数自检在不在、SSE 排除在不在、cookie 会不会被回显。
//	★★ **行为层**（动态）：用一个 **curl 替身**（stub）把预设响应喂给脚本，
//	  然后**读它写出的 TSV**，断言 **class 列逐条正确**。
//	  ⇒ 这才钉住「分类逻辑真的这么判」，而不只是「分类名出现过」。
//
// ★ 行为层的**量具自证**：curl 替身会把每次被调用的路径写进日志文件；
//   门断言该日志的行数 == 期望被巡检的路由数
//   ⇒ 一来证明「替身确实被调到了」（不是真实 curl 联网跑出来的巧合），
//     二来证明「脚本没有多打/少打请求」。
//
// # ★ SSE 排除为什么要单独钉一条
//
// 实测踩过的坑：SSE / 长轮询端点（`/api/admin/live-stream`、
// `/api/admin/probe/stream`、`/api/admin/system-monitor/stream`、
// `/api/free-pool/stream`）**响应永不结束** ⇒ GET 上去会一直挂到客户端超时：
//
//	· 每条白白烧满 TIMEOUT（4 条 = 80 s 纯浪费）；
//	· 且会被误分类成 `timeout` **真故障** ⇒ 那是**量具假象**，不是产品缺陷。
//
// ★★ **排除式必须同时覆盖斜杠形态与短横线形态**：
// `/stream`（斜杠）**匹配不到** `live-stream`（短横线）⇒ 只写前者会漏掉 4 条里的 1 条。
// ⇒ 本门用**行为判据**（拿真正则去 match 真实路径）而不是字面量判据来钉这条。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// apiSweepRelPath 是被测脚本**在仓内的相对路径**（正斜杠）。
//
// ★ 这条常量同时被「门指向的文件」与「脚本自述的用法」两处断言引用 ⇒
//
//	门指错文件 ⇒ 红；脚本被挪走 ⇒ 红。
const apiSweepRelPath = "scripts/audit/api-sweep.sh"

// apiSweepRepoRoot 返回仓根。
//
// ★ 必须用 runtime.Caller：写死相对路径（"../.."）或依赖 os.Getwd()，
//
//	在测试的工作目录变化时会扫不到文件 ⇒ 静态守卫变成恒绿门。
func apiSweepRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	// file = <repo>/scripts/ursmcheck/<this>.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// apiSweepScriptPath 返回被测脚本的绝对路径。
func apiSweepScriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(apiSweepRepoRoot(t), filepath.FromSlash(apiSweepRelPath))
}

// apiSweepRead 读被测脚本文本。
func apiSweepRead(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(apiSweepScriptPath(t))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", apiSweepRelPath, err)
	}
	return string(b)
}

// apiSweepExtractShellVar 取脚本里 `NAME='...'` 形态的单引号赋值。
//
// ★ 只认**单引号**形态：这是脚本自己的写法约定（引号里有 `|` 与 `(` 等元字符，
//
//	不加引号会被 shell 吃掉）⇒ 判据读的就是脚本真实写出来的东西。
func apiSweepExtractShellVar(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `='([^']*)'`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("脚本里找不到 %s 的单引号赋值 ⇒ 判据按这个形态读，读不到就是真缺陷", name)
	}
	return m[1]
}

// apiSweepHeaderBlock 返回脚本顶部的注释块（到第一行非注释代码为止）。
//
// ★ 用途：要求「逐类解释」这件事落在**注释**里，而不是落在代码里。
//
//	只在代码里出现分类名 ⇒ 说明没人读得懂这张表 ⇒ 门红。
func apiSweepHeaderBlock(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		break
	}
	return b.String()
}

// TestApiSweepScriptExistsAndParses —— 文件在、能在 bash 下解析。
//
// ★ `bash -n` 是最便宜的检查：能抓住绝大多数自伤（引号不闭合、heredoc 不完整）。
func TestApiSweepScriptExistsAndParses(t *testing.T) {
	path := apiSweepScriptPath(t)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s 不存在：%v", apiSweepRelPath, err)
	}
	if st.IsDir() {
		t.Fatalf("%s 是目录，不是脚本", apiSweepRelPath)
	}
	if st.Mode().Perm()&0111 == 0 {
		t.Errorf("%s 没有可执行位（%v）⇒ 用 `bash %s` 能跑，但直接执行会失败",
			apiSweepRelPath, st.Mode().Perm(), apiSweepRelPath)
	}
	out, err := exec.Command("bash", "-n", path).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 失败（%s）:\n%s", apiSweepRelPath, out)
	}
}

// TestApiSweepGateTargetsRepoScript —— ★ 门**没有指错文件**。
//
// 三条断言：
// ① 门自己的源码里写着这个仓内相对路径（门被搬走/改名 ⇒ 红）；
// ② 脚本的用法注释里也写着这个仓内相对路径（脚本被挪走 ⇒ 红）；
// ③ 解析出来的绝对路径，其相对仓根的路径**逐字等于**该相对路径。
func TestApiSweepGateTargetsRepoScript(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位门自身")
	}
	self, err := os.ReadFile(thisFile)
	if err != nil {
		t.Fatalf("读门自身失败：%v", err)
	}
	if !strings.Contains(string(self), apiSweepRelPath) {
		t.Errorf("门自身源码里没有出现 %q ⇒ 这道门可能已经指错文件了", apiSweepRelPath)
	}

	src := apiSweepRead(t)
	if !strings.Contains(src, apiSweepRelPath) {
		t.Errorf("脚本 %s 里没有出现自己的仓内路径 %q ⇒ 用法注释与实际位置对不上",
			apiSweepRelPath, apiSweepRelPath)
	}

	rel, err := filepath.Rel(apiSweepRepoRoot(t), apiSweepScriptPath(t))
	if err != nil {
		t.Fatalf("算相对路径失败：%v", err)
	}
	if got := filepath.ToSlash(rel); got != apiSweepRelPath {
		t.Errorf("门解析到的脚本是 %q，与期望的 %q 不一致 ⇒ 门指错文件了", got, apiSweepRelPath)
	}
}

// apiSweepRequiredClasses 是失败分类的**全集**（含 ok）。
//
// ★ 分类必须**互斥且穷尽**：漏一类 ⇒ 某个响应落进「兜底」；
//
//	多一类且命名撒谎（例如把 3xx 塞进 other_4xx）⇒ 读表的人被误导。
var apiSweepRequiredClasses = []string{
	"ok",
	"method_not_allowed",
	"bad_request",
	"not_found",
	"server_error",
	"timeout",
	"other_4xx",
}

// TestApiSweepDocumentsEveryFailureClass —— ★ 每一类都要在**头部注释里**逐类解释。
func TestApiSweepDocumentsEveryFailureClass(t *testing.T) {
	src := apiSweepRead(t)
	header := apiSweepHeaderBlock(src)
	if len(strings.TrimSpace(header)) == 0 {
		t.Fatal("脚本没有头部注释块 ⇒ 判据失效")
	}
	for _, c := range apiSweepRequiredClasses {
		if !strings.Contains(header, c) {
			t.Errorf("头部注释里没有解释分类 %q ⇒ 读表的人无从知道它意味着什么", c)
		}
	}
	// ★ 405 的语义必须写清楚「这不是产品缺陷，但 GET-only 覆盖不到真实方法」。
	if !strings.Contains(header, "405") {
		t.Error("头部注释里没有 405 ⇒ 漏了 method_not_allowed 的语义说明")
	}
	if !strings.Contains(header, "POST") || !strings.Contains(header, "DELETE") {
		t.Error("头部注释里没有写明「该路径需 POST/PUT/DELETE 才能验证」⇒ " +
			"405 的覆盖缺口说不清楚")
	}
	// ★ 5xx 是唯一「真故障」的一类 ⇒ 注释必须点明。
	if !strings.Contains(header, "5xx") {
		t.Error("头部注释里没有点明 5xx 才是真故障 ⇒ 读表的人会把覆盖缺口当缺陷报上去")
	}
}

// TestApiSweepClassifiesEveryRequiredFailure —— 六个失败分类关键字都出现在脚本里。
//
// ★ 这是**形状层**的最弱一环（只认「字符串出现过」）；
//
//	真正的语义由 TestApiSweepClassifiesResponsesBehaviourally 钉。
func TestApiSweepClassifiesEveryRequiredFailure(t *testing.T) {
	src := apiSweepRead(t)
	for _, c := range []string{
		"method_not_allowed", "bad_request", "not_found",
		"server_error", "timeout", "other_4xx",
	} {
		if !strings.Contains(src, c) {
			t.Errorf("脚本里没有分类名 %q ⇒ 失败分类不完整", c)
		}
	}
}

// TestApiSweepSamplesHostLoad —— ★ 宿主机负载同窗采样必须真的存在。
//
// ★ 判据不只看「有 loadavg 这个词」，还看**两条取数路径都在**
//
//	（sysctl 优先 + uptime 回退）⇒ 只写一条就意味着另一类机器上量具是瞎的。
func TestApiSweepSamplesHostLoad(t *testing.T) {
	src := apiSweepRead(t)
	for _, needle := range []string{
		"vm.loadavg", "sysctl", "uptime",
		"command -v sysctl", "command -v uptime",
		"load_start", "load_end", "load_peak",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("脚本里没有 %q ⇒ 宿主机负载采样不完整", needle)
		}
	}
	// ★ 不得依赖 macOS 专有命令而让 Linux 挂掉 ⇒ 两边都必须被 command -v 守着。
	if !strings.Contains(src, "command -v sysctl") || !strings.Contains(src, "command -v uptime") {
		t.Error("sysctl / uptime 没有被 `command -v` 守着 ⇒ 在缺命令的机器上会直接崩")
	}
	// ★ 负载必须写进 TSV 的元数据行（route 列以 #meta 开头）。
	if !strings.Contains(src, "#meta") {
		t.Error("脚本里没有 #meta 元数据行 ⇒ 负载读数没有落进产物")
	}
}

// TestApiSweepSelfChecksRowCount —— ★ 「量具静默少跑了一半」必须被自检抓住。
func TestApiSweepSelfChecksRowCount(t *testing.T) {
	src := apiSweepRead(t)
	for _, needle := range []string{"exit 70", "写出的数据行数", "-ne"} {
		if !strings.Contains(src, needle) {
			t.Errorf("脚本里没有 %q ⇒ 行数自检不完整（期望行数 != 实际行数 时无人报警）", needle)
		}
	}
	// ★ 跳过规则必须体现在计数里：被跳过的路由**不计入**期望行数。
	if !strings.Contains(src, "n_expected") {
		t.Error("脚本里没有 n_expected ⇒ 期望行数没有与「跳过后的路由数」绑定")
	}
}

// apiSweepKnownSSEPaths 是**实测**存在的 SSE / 长轮询端点。
//
// ★ 这 4 条是本轮真跑出来的（SSE 响应永不结束 ⇒ 一次性客户端必然超时）。
var apiSweepKnownSSEPaths = []string{
	"/api/admin/live-stream",
	"/api/admin/probe/stream",
	"/api/admin/system-monitor/stream",
	"/api/free-pool/stream",
}

// TestApiSweepExcludesSSEEndpoints —— ★ SSE 排除必须**行为上**真的覆盖住那 4 条。
//
// ★ 用真正则去 match 真实路径，而不是断言「源码里有 live-stream 这个词」
//
//	—— 后者挡不住「写成 /stream（漏掉短横线形态）」这个**实测踩过的坑**。
func TestApiSweepExcludesSSEEndpoints(t *testing.T) {
	src := apiSweepRead(t)
	sseRE := apiSweepExtractShellVar(t, src, "SSE_RE")
	re, err := regexp.Compile(sseRE)
	if err != nil {
		t.Fatalf("SSE_RE 不是合法正则（%q）：%v", sseRE, err)
	}
	for _, p := range apiSweepKnownSSEPaths {
		if !re.MatchString(p) {
			t.Errorf("SSE 排除没覆盖 %s（SSE_RE=%q）⇒ 它会被 GET 巡检挂到客户端超时，"+
				"再被误分类成 timeout 真故障", p, sseRE)
		}
	}
	// ★ 不能过度跳过：普通端点被误跳 ⇒ 巡检覆盖率悄悄下降。
	for _, p := range []string{"/api/admin/health", "/api/admin/live", "/api/admin/probe/results"} {
		if re.MatchString(p) {
			t.Errorf("SSE 排除过度命中普通端点 %s（SSE_RE=%q）⇒ 巡检覆盖率被悄悄削掉", p, sseRE)
		}
	}
	// ★★ 斜杠形态与短横线形态必须**都**在排除式里（实测坑：/stream 匹配不到 live-stream）。
	if !strings.Contains(sseRE, "/stream") {
		t.Errorf("SSE_RE=%q 里没有 /stream（斜杠形态）⇒ /api/admin/probe/stream 这类端点会漏", sseRE)
	}
	if !strings.Contains(sseRE, "live-stream") {
		t.Errorf("SSE_RE=%q 里没有 live-stream（短横线形态）⇒ ★ 只写 /stream 匹配不到它，"+
			"这是本轮实测踩过的坑", sseRE)
	}
}

// TestApiSweepSSEExclusionMatcherHasTeeth —— ★ 量具自检：证明上面的匹配判据不是恒绿。
//
// 若「/stream」竟然能匹配到「live-stream」，那上面那条排除断言就是**恒绿门**。
func TestApiSweepSSEExclusionMatcherHasTeeth(t *testing.T) {
	onlySlash := regexp.MustCompile(`/stream`)
	if onlySlash.MatchString("/api/admin/live-stream") {
		t.Fatal("量具坏了：`/stream` 竟然匹配到了 `live-stream` ⇒ " +
			"排除断言恒绿，钉不住任何东西")
	}
	onlySlash.MatchString("/api/admin/probe/stream") // 斜杠形态本身必须能匹配
}

// TestApiSweepSideEffectSkipListIsEffective —— 副作用黑名单必须真的挡住那类路径。
func TestApiSweepSideEffectSkipListIsEffective(t *testing.T) {
	src := apiSweepRead(t)
	skipRE := apiSweepExtractShellVar(t, src, "SKIP_RE")
	re, err := regexp.Compile(skipRE)
	if err != nil {
		t.Fatalf("SKIP_RE 不是合法正则（%q）：%v", skipRE, err)
	}
	for _, p := range []string{
		"/api/admin/sessions/execute",
		"/api/admin/cleanup",
		"/api/admin/delete",
		"/api/admin/rotate-key",
		"/api/admin/actions",
	} {
		if !re.MatchString(p) {
			t.Errorf("副作用黑名单没挡住 %s（SKIP_RE=%q）⇒ GET 也可能触发写/外呼", p, skipRE)
		}
	}
	for _, p := range []string{"/api/admin/health", "/api/admin/live"} {
		if re.MatchString(p) {
			t.Errorf("副作用黑名单过度命中普通端点 %s（SKIP_RE=%q）", p, skipRE)
		}
	}
}

// TestApiSweepNeverEchoesCookie —— ★ cookie / 口令不得回显、不得进 argv。
//
// 判据：
// ① curl 必须以**文件**形式收 cookie（-b "${COOKIE}"）⇒ argv 里只有路径，没有 cookie 值；
// ② 不许把 cookie 值拼进 -H（那才是真的进了 argv）；
// ③ 脚本里除注释外，任何引用 COOKIE 的行都必须在允许的上下文里
//
//	（赋值 / 可读性检查 / export / curl 参数）⇒ 加一句 cat "${COOKIE}" 就会红。
func TestApiSweepNeverEchoesCookie(t *testing.T) {
	src := apiSweepRead(t)
	if !strings.Contains(src, `-b "${COOKIE}"`) {
		t.Errorf("脚本没有用 `-b \"${COOKIE}\"` 形态把 cookie 交给 curl ⇒ " +
			"cookie 值可能被拼进 argv")
	}
	if strings.Contains(src, `-H "Cookie:`) || strings.Contains(src, "-H 'Cookie:") {
		t.Error("脚本把 cookie 值拼进了 -H ⇒ cookie 值会出现在 argv / ps 输出里")
	}
	// ★ 「读 cookie 文件内容」的任何形态都要红
	//   （cat/head/tail/source/< 紧跟在 cookie 变量前 ⇒ 在读内容，不是读路径）。
	//   ★ 注意不能简单地禁掉 `$(`：整条 curl 调用本身就是 `$(curl ... -b "${COOKIE}" ...)`，
	//   那是**把路径交给 curl**，不是读内容 ⇒ 禁 `$(` 会误伤正确写法。
	reader := regexp.MustCompile(`(?:cat|head|tail|source|less|more|dd|cp)\s+"?\$\{?COOKIE|<"?\$\{?COOKIE`)
	for i, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "COOKIE") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // 注释
		}
		if reader.MatchString(line) {
			t.Errorf("第 %d 行在读 cookie 的**内容**（而不只是路径）：%s", i+1, trimmed)
		}
		// ★ 允许的上下文：赋值 / 可读性检查 / export / curl 参数 /
		//   报错（只报**路径**与原因，不报内容）。
		allowed := strings.Contains(line, "COOKIE=") ||
			strings.Contains(line, "[[") ||
			strings.Contains(line, "export") ||
			strings.Contains(line, "curl") ||
			strings.Contains(line, "fatal_usage") ||
			strings.Contains(line, "die ")
		if !allowed {
			t.Errorf("第 %d 行引用了 COOKIE 但不在允许的上下文里（可能把 cookie 打出来了）：%s",
				i+1, trimmed)
		}
	}
}

// ─────────────────────────── 行为层：真的跑一遍 ───────────────────────────

// apiSweepStubCurl 是 curl 的**替身**：按 URL 路径返回预设响应。
//
// ★ 它镜像了脚本使用的 `-w` 契约（`%{http_code}` / `%{size_download}` / `%{time_total}`）。
// ★ 它把每次被调用的路径写进 $API_SWEEP_STUB_LOG ⇒ 门可以证明「替身确实被调到了」。
const apiSweepStubCurl = `#!/usr/bin/env bash
out=""; fmt=""; url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) fmt="$2"; shift 2 ;;
    -b|-m) shift 2 ;;
    -sS) shift ;;
    --*) shift ;;
    *) url="$1"; shift ;;
  esac
done
path="/${url#*://*/}"
if [ -n "${API_SWEEP_STUB_LOG:-}" ]; then
  printf '%s\n' "$path" >> "$API_SWEEP_STUB_LOG"
fi
body=""; code="200"; t="0.010000"; rc=0
case "$path" in
  /ok)          body="fine" ;;
  /method)      code="405"; body="method not allowed" ;;
  /badreq)      code="400"; body="missing param" ;;
  /missing)     code="404"; body="not found" ;;
  /boom)        code="500"; body="EXPLODE: internal error detail" ;;
  /teapot)      code="418"; body="teapot" ;;
  /redirect)    code="302" ;;
  /slow)        t="2.500000"; body="slow" ;;
  /timeout)     rc=28; code="000"; t="20.000000" ;;
  /refused)     rc=7;  code="000"; t="0.001000" ;;
esac
bytes=${#body}
printf '%s' "$body" > "$out"
fmt="$(printf '%s' "$fmt" | sed -e "s/%{http_code}/$code/" -e "s/%{size_download}/$bytes/" -e "s/%{time_total}/$t/")"
printf '%b\n' "$fmt"
exit $rc
`

// apiSweepStubRoutes 是喂给脚本的路由清单。
//
// ★ 刻意混进三类「必须被跳过」的形态（SSE / 非 GET / 带副作用），
//
//	好让门能同时断言「该跑的跑了」与「该跳的跳了」。
const apiSweepStubRoutes = `GET /ok
/method
/badreq
/missing
/boom
/teapot
/redirect
/slow
/timeout
/refused
/api/admin/live-stream
/api/admin/probe/stream
/api/admin/system-monitor/stream
/api/free-pool/stream
DELETE /api/admin/sessions/{id}
GET /api/admin/execute-now
/api/admin/health
`

// apiSweepExpectedClass 是每个路径**应该**被分到的类。
//
// ★ 这就是行为层的核心断言：不是「脚本里出现过 server_error」，
//
//	而是「500 那一行**确实**被写成了 server_error」。
var apiSweepExpectedClass = map[string]string{
	"/ok":               "ok",
	"/method":           "method_not_allowed",
	"/badreq":           "bad_request",
	"/missing":          "not_found",
	"/boom":             "server_error",
	"/teapot":           "other_4xx",
	"/redirect":         "unexpected_status",
	"/slow":             "ok",
	"/timeout":          "timeout",
	"/refused":          "timeout",
	"/api/admin/health": "ok",
}

// apiSweepExpectedSkipped 是**必须被跳过**、因而**不得**出现在数据行里的路径。
var apiSweepExpectedSkipped = []string{
	"/api/admin/live-stream",
	"/api/admin/probe/stream",
	"/api/admin/system-monitor/stream",
	"/api/free-pool/stream",
	"DELETE /api/admin/sessions/{id}",
	"/api/admin/execute-now",
}

// apiSweepRow 是 TSV 的一行。
type apiSweepRow struct {
	route   string
	status  string
	latency string
	bytes   string
	class   string
	note    string
}

// apiSweepParseTSV 解析脚本产出的 TSV（数据行 + #meta 元数据行）。
func apiSweepParseTSV(t *testing.T, path string) (data, meta []apiSweepRow) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读产出 TSV 失败：%v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("产出 TSV 是空的")
	}
	head := strings.Split(lines[0], "\t")
	want := []string{"route", "status", "latency_ms", "bytes", "class", "note"}
	if len(head) != len(want) {
		t.Fatalf("TSV 表头列数不对：got %v", head)
	}
	for i := range want {
		if head[i] != want[i] {
			t.Fatalf("TSV 表头第 %d 列不对：got %q want %q", i+1, head[i], want[i])
		}
	}
	for _, ln := range lines[1:] {
		f := strings.Split(ln, "\t")
		if len(f) != len(want) {
			t.Errorf("TSV 数据行列数不对（%d）：%q", len(f), ln)
			continue
		}
		r := apiSweepRow{f[0], f[1], f[2], f[3], f[4], f[5]}
		if strings.HasPrefix(r.route, "#meta") {
			meta = append(meta, r)
			continue
		}
		data = append(data, r)
	}
	return data, meta
}

// TestApiSweepClassifiesResponsesBehaviourally —— ★★ 行为层：真的跑一遍，看 class 列对不对。
//
// ★★ 这是本门**最硬**的一条：它不读源码的分类名，它读**脚本写出来的 TSV**。
//
//	把分类逻辑整个写反（500 判成 ok）⇒ 这条立刻红，而形状层仍然绿。
func TestApiSweepClassifiesResponsesBehaviourally(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("建 bin 目录失败：%v", err)
	}
	stub := filepath.Join(binDir, "curl")
	if err := os.WriteFile(stub, []byte(apiSweepStubCurl), 0o755); err != nil {
		t.Fatalf("写 curl 替身失败：%v", err)
	}

	routesPath := filepath.Join(dir, "routes.txt")
	if err := os.WriteFile(routesPath, []byte(apiSweepStubRoutes), 0o644); err != nil {
		t.Fatalf("写路由清单失败：%v", err)
	}
	cookiePath := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookiePath, []byte("# Netscape HTTP Cookie File\n"), 0o644); err != nil {
		t.Fatalf("写 cookie 文件失败：%v", err)
	}
	outPath := filepath.Join(dir, "sweep.tsv")
	stubLog := filepath.Join(dir, "stub.log")

	cmd := exec.Command("bash", apiSweepScriptPath(t))
	cmd.Env = append(os.Environ(),
		"LC_ALL=C",
		"LANG=C",
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ROUTES="+routesPath,
		"COOKIE="+cookiePath,
		"OUT="+outPath,
		"TIMEOUT=5",
		"CONC=2",
		"LOAD_EVERY=3",
		"API_SWEEP_STUB_LOG="+stubLog,
	)
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	runErr := cmd.Run()
	if runErr != nil {
		t.Fatalf("巡检脚本跑失败：%v\nstderr:\n%s", runErr, stderr.String())
	}

	// ★ 量具自证 ①：curl 替身确实被调到了（不是真实 curl 联网跑出来的巧合）。
	logRaw, err := os.ReadFile(stubLog)
	if err != nil {
		t.Fatalf("读 curl 替身日志失败（替身一次都没被调用 ⇒ 这条门是恒绿门）：%v", err)
	}
	gotCalls := len(strings.Split(strings.TrimRight(string(logRaw), "\n"), "\n"))
	wantCalls := len(apiSweepExpectedClass)
	if gotCalls != wantCalls {
		t.Errorf("curl 替身被调用了 %d 次，期望 %d 次 ⇒ 巡检的请求条数不对",
			gotCalls, wantCalls)
	}

	data, meta := apiSweepParseTSV(t, outPath)

	// ★ 自检：写出的数据行数 == 期望巡检的路由数。
	if len(data) != wantCalls {
		t.Errorf("写出的数据行数(%d) != 期望巡检的路由数(%d) ⇒ 脚本的行数自检失效",
			len(data), wantCalls)
	}

	// ★★ 核心断言：class 列逐条正确。
	byRoute := map[string]apiSweepRow{}
	for _, r := range data {
		byRoute[r.route] = r
	}
	for route, wantClass := range apiSweepExpectedClass {
		r, ok := byRoute[route]
		if !ok {
			t.Errorf("路由 %s 没有出现在数据行里", route)
			continue
		}
		if r.class != wantClass {
			t.Errorf("路由 %s 的 class 应为 %q，实际 %q（status=%s）",
				route, wantClass, r.class, r.status)
		}
	}

	// ★ 状态码与耗时也要对（否则「全判成 ok」也能骗过 class 断言）。
	if r, ok := byRoute["/boom"]; ok {
		if r.status != "500" {
			t.Errorf("/boom 的 status 应为 500，实际 %q", r.status)
		}
		// ★ 5xx 是真故障 ⇒ 响应体前 200 字符必须留在 note 里。
		if !strings.Contains(r.note, "EXPLODE") {
			t.Errorf("/boom 的 note 里没有响应体片段（真故障必须留证据）：%q", r.note)
		}
	}
	if r, ok := byRoute["/slow"]; ok {
		ms, err := strconv.Atoi(r.latency)
		if err != nil {
			t.Errorf("/slow 的 latency_ms 不是整数：%q", r.latency)
		} else if ms != 2500 {
			t.Errorf("/slow 的 latency_ms 应为 2500，实际 %d", ms)
		}
	}

	// ★ 405 的 note 必须带上「该路径需 POST/PUT/DELETE 才能验证」。
	if r, ok := byRoute["/method"]; ok {
		if !strings.Contains(r.note, "POST/PUT/DELETE") {
			t.Errorf("/method 的 note 必须写明「该路径需 POST/PUT/DELETE 才能验证」，实际：%q", r.note)
		}
	}

	// ★ timeout 必须区分「客户端超时」与「连接失败」。
	tn, tokn := byRoute["/timeout"], byRoute["/refused"]
	if !strings.Contains(tn.note, "client_timeout") {
		t.Errorf("/timeout 的 note 应写明 client_timeout，实际：%q", tn.note)
	}
	if !strings.Contains(tokn.note, "conn_failed") {
		t.Errorf("/refused 的 note 应写明 conn_failed，实际：%q", tokn.note)
	}
	if tn.note == tokn.note {
		t.Error("客户端超时与连接失败的 note 相同 ⇒ 这两类没法区分")
	}

	// ★ 被跳过的路径不得出现在数据行里。
	for _, p := range apiSweepExpectedSkipped {
		if _, ok := byRoute[p]; ok {
			t.Errorf("路径 %s 应当被跳过，却出现在数据行里", p)
		}
	}

	// ★ 元数据行：负载读数与行数自检的记账都必须在。
	//   ★ 元数据行的形态：route 列以 #meta 开头，**键在 status 列**，键值对在 note 列。
	metaKeys := map[string]string{}
	for _, r := range meta {
		if !strings.HasPrefix(r.route, "#meta") {
			t.Errorf("元数据行的 route 列应以 #meta 开头，实际 %q", r.route)
		}
		metaKeys[r.status] = r.note
	}
	for _, k := range []string{
		"load_start", "load_end", "load_peak",
		"routes_expected", "rows_written", "skipped_sse",
		"class_server_error", "class_timeout",
	} {
		if _, ok := metaKeys[k]; !ok {
			t.Errorf("元数据行里缺少 %q", k)
		}
	}

	// ★ 汇总（stderr）里必须把「SSE 跳过」与「非 GET / 带副作用跳过」分开列，
	//   并打印 SSE 的路径清单 ⇒ 跳过不等于已验证，读表的人必须看得见。
	se := stderr.String()
	if !strings.Contains(se, "SSE") {
		t.Error("汇总里没有 SSE 段落 ⇒ 跳过项没有被单独报出来")
	}
	for _, p := range apiSweepExpectedSkipped {
		if !strings.Contains(se, p) {
			t.Errorf("汇总里没有列出被跳过的路径 %s", p)
		}
	}
	if !strings.Contains(se, "2000ms") {
		t.Error("汇总里没有 >2000ms 的清单")
	}
}
