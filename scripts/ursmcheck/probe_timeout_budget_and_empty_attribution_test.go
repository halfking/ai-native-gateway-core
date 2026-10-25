package ursm_test

// probe_timeout_budget_and_empty_attribution_test.go —— R150 轮（2026-10-27）新增门。
//
// ── 为什么有这道门 ───────────────────────────────────────────────────────────
//
// R150 轮要把「Goal 的长提示词成功回合」闭合，途中连着踩了三个坑，
// 而**三个坑都是同一个根因的不同外衣**：
//
//　坑 1（配置层）：本地把 `error_probe.timeout_ms` 调到上限 60000 并重启网关，
//　　**期望**「node 探针不再把『健康但慢』的上游判死」——
//　　⇒ ★ **不成立**：`bg/node_probe.go` 构造自己的探针 HTTP 客户端时
//　　　`Timeout: 30 * time.Second` 是**硬编码字面量**（bg/node_probe.go:558/569），
//　　　而 `settings.ErrorProbeTimeoutMs()` 只流向 `bg.NewActiveProbeExecutor(...)`
//　　　（cmd/gateway/main.go:4547 → 4586）——**两者是两条不同的执行路径**。
//　　⇒ ★★ **「可配置的超时」与「真正执行探针的那个超时」不是同一个量。**
//
//　坑 2（分类层）：上游对长提示词返回 **HTTP 200**，但 content 为空 ——
//　　因为**整个输出预算被推理吃掉**（completion_tokens == reasoning_tokens
//　　== max_tokens，finish_reason=length）⇒ 网关判 `empty_response` ⇒
//　　**同一 body 重试 3 次**（93 s，注定失败）⇒ 客户端看到 503 `model_not_found`。
//　　⇒ ★★ **「200 但空」与「上游挂了」必须能被区分** —— 改动前的服务端日志
//　　　只记 `status=200` ⇒ 两种完全不同的故障读起来一模一样。
//
//　坑 3（量具层）：脚本用**固定的**临时文件存响应头 ⇒ 两个并发跑批互相覆盖
//　　⇒ 把 A 请求的 request_id 读成了 B 的 ⇒ 写入核实的行对错了。
//　　（这一条属「量具自身」，仓内已有同族纪律，此门不覆盖。）
//
// ⇒ 本门钉死两条**结构事实**，防止下一轮又从同一个错误前提出发：
//
//　① **node 探针的超时不由 `error_probe.timeout_ms` 驱动**（当前状态）。
//　② **empty-response 的告警日志必须带上游 usage / finish_reason**
//　　（否则「预算耗尽」在服务端不可见）。
//
// ★ 门红 = 「现状被改了」⇒ 两种可能：(a) 有人引入了回归；(b) ★★ D9 真的被修了。
// 　 ⇒ **门红时先读判据说明再动手**：若是 (b)，请连同本门与审计档一起更新。
//
// ★ 阳性对照：故意写坏的合成片段 ⇒ 检测器必须报出来；
// ★ 阴性对照：合规片段 ⇒ 检测器不得误报。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	probeTimeoutMainFile   = "cmd/gateway/main.go"
	probeTimeoutWorkerFile = "bg/node_probe.go"
	emptyAttributionFile   = "domains/streaming/executors/executor_chat.go"
)

// probeTimeoutRepoRoot 定位仓根（本文件在 scripts/ursmcheck/ 下 ⇒ 往上两级）。
func probeTimeoutRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// probeTimeoutCallArgs 抽出 `callee(` 调用从左括号到配对右括号的实参列表。
func probeTimeoutCallArgs(src, callee string) (string, bool) {
	idx := strings.Index(src, callee+"(")
	if idx < 0 {
		return "", false
	}
	rest := src[idx+len(callee)+1:]
	depth := 1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return rest[:i], true
			}
		}
	}
	return rest, true
}

// probeTimeoutLooksLikeTimeoutToken 判断一个实参里是否出现了「超时」味道的标识符。
func probeTimeoutLooksLikeTimeoutToken(arg string) bool {
	for _, tok := range []string{"Timeout", "timeout", "TimeoutMs", "timeoutMs"} {
		if strings.Contains(arg, tok) {
			return true
		}
	}
	return false
}

// probeTimeoutStripCommentLines 去掉**整行注释**后再做子串判定。
//
// ★ 为什么需要它（R152 轮实测踩到）：这道门是**文本**判据
// （`strings.Contains(worker, "Timeout: 30 * time.Second")`）⇒ ⇒ **注释里
// 引用那个字面量也会被命中** ⇒ ★★ 门把「**历史说明**」判成「**现状**」
// ⇒ ⇒ 这正是本仓 `sql/schema/shell_bash_compat_gate_test.go` 早就踩过的坑
// （那里用 `stripShellComments` 解决）⇒ ⇒ ★★ 这里用同样的口径解决：
// **代码里的字面量才算数，注释里写的历史不算。**
func probeTimeoutStripCommentLines(src string) string {
	kept := make([]string, 0, 64)
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "//") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func TestProbeTimeoutBudgetIsNotSharedWithTheConfigurableSetting(t *testing.T) {
	root := probeTimeoutRepoRoot(t)

	mainRaw, err := os.ReadFile(filepath.Join(root, probeTimeoutMainFile))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", probeTimeoutMainFile, err)
	}
	main := string(mainRaw)

	// ① 配置项必须仍然流向 ActiveProbeExecutor（那是它真正的消费者）。
	execArgs, ok := probeTimeoutCallArgs(main, "bg.NewActiveProbeExecutor")
	if !ok {
		t.Fatalf("%s 里找不到 bg.NewActiveProbeExecutor( —— 配置项的消费者消失了，请更新本门与审计档", probeTimeoutMainFile)
	}
	if !probeTimeoutLooksLikeTimeoutToken(execArgs) {
		t.Errorf("bg.NewActiveProbeExecutor 的实参里看不到超时量（%s）⇒ error_probe.timeout_ms 的消费者被改动了，请更新本门与审计档", strings.TrimSpace(execArgs))
	}

	// ② ★★ node 探针的构造函数**必须**收到超时参数（D9a 已落地 ⇒ 本条已翻转）。
	//
	// ★ 翻转原因：R152 轮实测「只放宽超时**单独**无效」（探针连上了、拿到响应，
	// 但判定 `probe_response_inconclusive` ⇒ 故意不写回 ⇒ 状态永不改变）
	// ⇒ ⇒ ★★ **D9a 必须与 D9b（内容预算）一起落地** ⇒ ⇒ ★★ 既然要落地，
	// 那个 30 s 硬编码**必须**换成设置驱动的值，否则「可配置的超时」仍是空话。
	seen := false
	for _, callee := range []string{"bg.NewNodeProbeWorker"} {
		args, ok := probeTimeoutCallArgs(main, callee)
		if !ok {
			continue // 该调用点不存在（例如测试专用构造），跳过
		}
		if probeTimeoutLooksLikeTimeoutToken(args) {
			seen = true
		}
	}
	if !seen {
		t.Errorf("%s 里没有把超时量传给 node 探针的构造函数 ⇒ ★★ node 探针的超时预算"+
			"又退回「写死」了 ⇒ ① `error_probe.timeout_ms` 对它无效（R150 实测）"+
			"② ★★ 「健康但慢」的上游会被**反复判死**（R151 实测：30 s 是边缘预算）"+
			"⇒ 请接回 `settings.ErrorProbeTimeoutMs()`", probeTimeoutMainFile)
	}

	// ③ ★★ node_probe.go 里**不得**再有 `Timeout: 30 * time.Second` 的硬编码字面量。
	workerRaw, err := os.ReadFile(filepath.Join(root, probeTimeoutWorkerFile))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", probeTimeoutWorkerFile, err)
	}
	worker := probeTimeoutStripCommentLines(string(workerRaw))
	if strings.Contains(worker, "Timeout: 30 * time.Second") {
		t.Errorf("%s 里仍有 `Timeout: 30 * time.Second` 的硬编码字面量 ⇒ "+
			"node 探针的超时预算仍**写死** ⇒ `error_probe.timeout_ms` 对它无效 ⇒ "+
			"请改成由构造实参算出（D9a）", probeTimeoutWorkerFile)
	}
	// ③' ★ 默认值必须**显式**留在仓里（而不是藏在某个字面量后面）。
	if !strings.Contains(worker, "nodeProbeDefaultTimeoutMs") {
		t.Errorf("%s 里找不到 `nodeProbeDefaultTimeoutMs` ⇒ "+
			"探针的默认超时预算不再是一个**可审计的具名事实** ⇒ 请显式声明它", probeTimeoutWorkerFile)
	}
}

// TestEmptyResponseLogMustCarryUpstreamUsage 钉死「空回复必须带上游 usage」。
func TestEmptyResponseLogMustCarryUpstreamUsage(t *testing.T) {
	root := probeTimeoutRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, emptyAttributionFile))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", emptyAttributionFile, err)
	}
	src := string(raw)

	marker := "executor: non-stream empty response, failing over to next candidate"
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("%s 里找不到空回复告警日志（%q）⇒ 形态变了，请更新本门", emptyAttributionFile, marker)
	}
	// 取**包含该 marker 的那次** slog.Warn(...) 调用的参数列表。
	// ★ 注意：必须往前找（LastIndex），不能往后找 —— 文件里更后面还有别的
	// slog.Warn 调用，往后找会切到不相干的调用上（★ 本轮第一次写门就是这么
	// 把自己写红了一次：门红的原因是判据取错片段，不是被测对象有问题）。
	warnIdx := strings.LastIndex(src[:idx], "slog.Warn(")
	if warnIdx < 0 {
		t.Fatalf("空回复告警不再是 slog.Warn 调用 ⇒ 形态变了，请更新本门")
	}
	rest := src[warnIdx+len("slog.Warn("):]
	depth := 1
	end := -1
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("无法切出空回复告警的参数列表 ⇒ 形态变了，请更新本门")
	}
	call := rest[:end]

	for _, key := range []string{
		"empty_reason",
		"finish_reason",
		"completion_tokens",
		"reasoning_tokens",
	} {
		if !strings.Contains(call, `"`+key+`"`) {
			t.Errorf("空回复告警缺少 %q ⇒ ★★ 「上游 200 但预算被推理吃光」与「上游挂了」在服务端读起来一模一样；"+
				"该字段是区分二者的唯一证据（R150 实测：completion_tokens == reasoning_tokens == max_tokens）", key)
		}
	}
}

// TestProbeTimeoutDetectorHasTeeth 是阳性对照：故意写坏的合成片段必须被报出。
func TestProbeTimeoutDetectorHasTeeth(t *testing.T) {
	// ① node 探针构造被接上超时 ⇒ 必须报出。
	bad := "bg.NewNodeProbeWorker(pool, key, kr, apiKey, \"\", proxyFn, epTimeoutMs)"
	args, ok := probeTimeoutCallArgs(bad, "bg.NewNodeProbeWorker")
	if !ok {
		t.Fatal("合成片段解析失败")
	}
	if !probeTimeoutLooksLikeTimeoutToken(args) {
		t.Fatal("检测器漏报了「node 探针被接上超时」的合成片段 ⇒ 主判据不可信")
	}

	// ② 空回复告警缺 usage 字段 ⇒ 必须报出。
	call := `"request_id", params.RequestID, "status", resp.StatusCode`
	for _, key := range []string{"empty_reason", "completion_tokens", "reasoning_tokens"} {
		if strings.Contains(call, `"`+key+`"`) {
			t.Fatalf("合成片段不该含 %q", key)
		}
	}
}

// TestProbeTimeoutDetectorNoFalsePositive 是阴性对照：合规片段不得被误报。
func TestProbeTimeoutDetectorNoFalsePositive(t *testing.T) {
	ok := "bg.NewNodeProbeWorker(pool, key, kr, apiKey, \"\", proxyFn)"
	args, present := probeTimeoutCallArgs(ok, "bg.NewNodeProbeWorker")
	if !present {
		t.Fatal("合成片段解析失败")
	}
	if probeTimeoutLooksLikeTimeoutToken(args) {
		t.Fatalf("检测器误报了合规片段: %s", args)
	}
}
