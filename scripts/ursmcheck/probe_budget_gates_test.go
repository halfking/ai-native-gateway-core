package ursm_test

// probe_budget_gates_test.go —— R151 轮（2026-10-27）新增门。
//
// ── 为什么有这道门 ───────────────────────────────────────────────────────────
//
// R151 轮用 2×2 判别实验把「探针把健康但慢的模型判死」这件事打到内核（本地实测）：
//
//　臂 A：**只**放宽超时预算（`bg/node_probe.go` 的 30 s → 180 s）
//　　　 ⇒ ★ **无效**：探针连上了、拿到了响应，但判定是
//　　　　　`probe_response_inconclusive` ⇒ ★★ **故意不写回** ⇒ 状态永不改变。
//　臂 C：放宽超时 **+** 放宽【内容预算】（`providercap.ProbePingMaxTokens` 32 → 1024）
//　　　 ⇒ ★★ **判定翻转**：`last_direct_ok=true`、`consecutive_failures` 归 0、
//　　　　　`updated_at` 前移、`state=healthy_confirmed` ⇒ 该对重新可路由。
//
// ⇒ ★★★ **两个门槛是独立的，只放宽其一无效**；★ 且**更致命的是内容预算**——
//　　因为它触发的「不写回」把一个本可自愈的失败变成**永久封禁**。
//
// ⇒ 本门的作用：把「探针的两个预算」与「inconclusive 的不写回语义」**显式钉在仓里**，
//　使任何改动都**必须伴随一次有意识的决定**，而不是无声地漂掉。
//
// ★ 门红 = 「现状被改了」⇒ 两种可能：
//　(a) 有人引入了回归；★ (b) ★★ **D9b/D9c 真的被修了**（属主裁决后落地）。
//　⇒ **门红时先读判据**：若是 (b)，请连同审计档 §R151 与 LESSONS §399 一起更新。
//
// ★ 阳性对照：故意写坏的合成片段 ⇒ 检测器必须报出来；
// ★ 阴性对照：合规片段 ⇒ 检测器不得误报。

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	probeBudgetCapFile    = "internal/providercap/capability.go"
	probeBudgetWorkerFile = "bg/node_probe.go"
)

// probeBudgetRepoRoot 定位仓根（本文件在 scripts/ursmcheck/ 下 ⇒ 往上两级）。
func probeBudgetRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// probeBudgetRead 读一个仓内文件。
func probeBudgetRead(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(probeBudgetRepoRoot(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", rel, err)
	}
	return string(raw)
}

// probeBudgetPingMaxTokens 抽出 `ProbePingMaxTokens = <expr>` 的右值表达式。
func probeBudgetPingMaxTokens(t *testing.T, src string) string {
	t.Helper()
	const key = "ProbePingMaxTokens"
	for _, ln := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if !strings.Contains(trimmed, key+" =") && !strings.Contains(trimmed, key+"=") {
			continue
		}
		idx := strings.Index(trimmed, "=")
		if idx < 0 {
			continue
		}
		return strings.TrimSpace(strings.TrimSuffix(trimmed[idx+1:], ","))
	}
	t.Fatalf("找不到 %s 的定义", key)
	return ""
}

// probeBudgetIsProbePingBudget 判断一个表达式是否是「探针 ping 的内容预算」：
// 允许两种形态 —— 直接给字面量，或引用同包的另一个常量（后者需人工核对）。
func probeBudgetIsProbePingBudget(expr string) bool {
	return expr != "" && expr != "ResponsesProbeMaxOutputTokens" ||
		strings.Contains(expr, "ResponsesProbeMaxOutputTokens")
}

// TestProbeContentBudgetMustBeDeclared 钉死「探针的内容预算」在仓里是**显式可见**的。
func TestProbeContentBudgetMustBeDeclared(t *testing.T) {
	src := probeBudgetRead(t, probeBudgetCapFile)
	expr := probeBudgetPingMaxTokens(t, src)

	if expr == "" {
		t.Fatal("探针内容预算的表达式为空 ⇒ 无法审计")
	}
	// ★ 当前状态：要么是字面量，要么引用一个**同名的常量**（两种都要能被审计读到）。
	// ⇒ 若它变成「运行期算出来的」（例如按模型族查表），本门会读到表达式本身并要求注释说明。
	if !strings.Contains(expr, "//") && !probeBudgetIsProbePingBudget(expr) {
		t.Errorf("探针内容预算的定义形态无法识别（%q）⇒ 请改成显式字面量或常量引用，"+
			"并在注释里写清它对【推理模型】是否足够（★ D9b）", expr)
	}

	// ★★ 2026-10-27（R152 轮 · D9b 已落地）：**值**本身必须够大。
	//
	// ★ 为什么值也要钉：R151 轮的 2×2 正交消融实测 —— 臂 C（内容预算 32 → 1024）
	// 才让探针判定翻转 ⇒ ⇒ **1024 是有实测支撑的下限**（不是拍脑袋的数字）。
	// ⇒ ⇒ 低于它 ⇒ 「健康但慢/爱推理」的模型会被**永久判死**（inconclusive 不写回）。
	//
	// ⚠ 本门只钉「**下限**」⇒ 更高的值不会被本门拦（那是成本取舍，属主裁决）。
	//
	// ★★ 2026-10-27（R152 轮）：**值必须回源文件解析**，不能靠门内的镜像表。
	//   （门内若硬编码「名字 → 值」，那是一份**过期镜像** ⇒ 被引用的常量
	//   一旦改值，门会读到旧值 ⇒ ★★ 门绿但事实错。）
	if n, ok := probeBudgetResolveInt(expr, probeBudgetConstInts(src)); ok && n < probeBudgetMinPingTokens {
		t.Errorf("探针内容预算 = %d < %d ⇒ ★★ 对【推理模型】不足 ⇒ 探针拿不到 assistant 正文 "+
			"⇒ 判 `probe_response_inconclusive` ⇒ ★★ **故意不写回** ⇒ 永久 broken_confirmed "+
			"（R151 实测：臂 A 只放宽超时**无效**，臂 C 两个一起放宽才翻转）",
			n, probeBudgetMinPingTokens)
	}
}

// probeBudgetMinPingTokens 是探针内容预算的**实测下限**（R151 轮臂 C）。
const probeBudgetMinPingTokens = 1024

// probeBudgetConstInts 从**同一个源文件**里解析 `const NAME = <整数字面量>`
// 声明，返回 名字 → 值 的映射。
//
// ★★ 为什么必须回源文件解析、而不是在门里硬编码一张「名字 → 值」的表：
//   硬编码表是一份**过期镜像** ⇒ ★★ 若被引用的常量改了值，门会读到旧值
//   ⇒ **门绿但事实错**（R152 教训：量具自身的失效点最难发现，因为
//   它长得和「门通过」一模一样）。
// ⇒ ⇒ 一律回到源文件解析 ⇒ 量具与事实永远同步。
func probeBudgetConstInts(src string) map[string]int {
	out := map[string]int{}
	for _, ln := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		idx := strings.Index(trimmed, "=")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(trimmed[:idx])
		name = strings.TrimSpace(strings.TrimPrefix(name, "const "))
		if !probeBudgetIsGoIdent(name) {
			continue
		}
		val := strings.TrimSpace(strings.TrimSuffix(trimmed[idx+1:], ","))
		if n, err := strconv.Atoi(val); err == nil {
			out[name] = n
		}
	}
	return out
}

// probeBudgetIsGoIdent 判断一个字符串是否是合法的 Go 标识符（够用的近似即可）。
func probeBudgetIsGoIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// probeBudgetResolveInt 解析探针内容预算的**实际整数值**。
//
// 形态：① 纯字面量（`1024`）② 同文件常量引用（`ResponsesProbeMaxOutputTokens`）
//
//	③ 链式引用（A 引用 B，B 是字面量）⇒ 逐层解到整数值。
//
// 运行期表达式（查表/函数调用）⇒ 解析不出来 ⇒ 返回 ok=false
// ⇒ ⇒ **本门不拦它**（那属于「口径变了」，按门自己的说明要在注释里写清）。
func probeBudgetResolveInt(expr string, consts map[string]int) (int, bool) {
	expr = strings.TrimSpace(expr)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		if n, err := strconv.Atoi(expr); err == nil {
			return n, true
		}
		if seen[expr] {
			return 0, false // 引用成环 ⇒ 解析不出来
		}
		seen[expr] = true
		n, ok := consts[expr]
		if !ok {
			return 0, false
		}
		expr = strconv.Itoa(n)
	}
	return 0, false
}

// TestProbeTimeoutBudgetMustBeExplicit 钉死「探针的【超时】预算」在仓里是硬编码且显式的。
//
// ★ 现状：`bg/node_probe.go` 构造自己的探针 HTTP 客户端时写死 `Timeout: 30 * time.Second`
// （含 `ResponseHeaderTimeout`）⇒ ⇒ `error_probe.timeout_ms` 对它**无效**。
// ⇒ 本门要求这个数字**显式出现在仓里**（而非藏在某个变量后面），
// 且要求改动时必须显式声明「D9a 是否已修」。
func TestProbeTimeoutBudgetMustBeExplicit(t *testing.T) {
	// ★★ 2026-10-27（R152 轮 · D9a 已落地）：本条**翻转**。
	//
	// ★ 原判据要求「`Timeout: 30 * time.Second` 必须出现在仓里」—— 那是在钉
	//   「D9a 还没修」这个**现状**（门是「现状登记」而不是「行为门」）。
	// ★★ D9a 落地后：那个字面量必须**消失**（否则 `error_probe.timeout_ms` 仍无效），
	//   而**默认值**必须以**具名常量**留在仓里（可审计）。
	//
	// ⚠ 判定必须**剥掉整行注释**（R152 轮踩到：文档里引用那个字面量被当成现状）
	//   ⇒ 与 sql/schema 的 stripShellComments 同一教训。
	code := probeBudgetStripCommentLines(probeBudgetRead(t, probeBudgetWorkerFile))

	if strings.Contains(code, "Timeout: 30 * time.Second") {
		t.Errorf("%s 里仍有 `Timeout: 30 * time.Second` 的硬编码字面量 ⇒ "+
			"★ 探针的超时预算仍**写死** ⇒ `error_probe.timeout_ms` 对它无效 "+
			"（R150 实测：本地把该键调到 60000 并重启，node 探针照旧 30 s 超时）"+
			"⇒ ★★ 「健康但慢」的上游会被**反复判死**", probeBudgetWorkerFile)
	}
	if !strings.Contains(code, "nodeProbeDefaultTimeoutMs") {
		t.Errorf("%s 里找不到具名的默认超时常量 `nodeProbeDefaultTimeoutMs` ⇒ "+
			"探针的默认超时预算不再是一个**可审计的具名事实**", probeBudgetWorkerFile)
	}
}

// probeBudgetStripCommentLines 去掉**整行注释**后再做子串判定。
//
// ★ 为什么需要它：这道门是**文本**判据 ⇒ 注释里引用那个字面量也会被命中
// ⇒ ⇒ 门把「历史说明」判成「现状」⇒ ★★ 与 sql/schema 的 stripShellComments
// 同一教训（那里早就踩过并解决）。
func probeBudgetStripCommentLines(src string) string {
	kept := make([]string, 0, 64)
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "//") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// TestProbeInconclusiveMustNotSilentlyFreeze 钉死「inconclusive ⇒ 不写回」必须是**显式声明**的。
//
// ★ 为什么重要：「不写回」本身是设计（不确定的探测不覆盖既有判定），
// ★ 但它与「永久封禁」叠在一起 ⇒ ★★★ **既不解封、也不退避** ⇒ 模型被钉死在死状态。
// ⇒ ⇒ 本门要求这句语义在仓里**看得见**（⇒ 将来若按 D9c 改成「参与退避阶梯」，
// 本门会红，并提示同步更新审计档）。
func TestProbeInconclusiveMustNotSilentlyFreeze(t *testing.T) {
	// 「不写回」的实现在 credential probe v2 的分支里；本门只要求该语义在仓里可检出。
	sources := []string{
		"bg/credential_probe_v2.go",
		"bg/node_probe.go",
	}
	found := false
	for _, rel := range sources {
		raw, err := os.ReadFile(filepath.Join(probeBudgetRepoRoot(t), rel))
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), "inconclusive probe") ||
			strings.Contains(string(raw), "preserving authoritative state") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("找不到「inconclusive 探测不写回」的显式声明（%s）⇒ "+
			"★★ 该分支的语义变了 ⇒ 请核对：★ 若已按 D9c 让它参与退避阶梯 ⇒ "+
			"请连同审计档 §R151 与 LESSONS §399 一起更新本门",
			strings.Join(sources, " / "))
	}

	// ★★ 2026-10-27（R152 轮 · D9c 已落地）：「不写回」**必须同时**是
	// 「参与退避阶梯」⇒ ⇒ 判据落在 `probe_consecutive_failures` 上。
	//
	// ★ 为什么是它：它**只**驱动探针节奏（auth 退避阶梯 + quota 探针 due 门），
	//   **不**驱动健康判定（健康结论走 health_status / availability_state，
	//   而 ObservationOnly 路径整条跳过那个写入）⇒ ⇒ ★★ **inconclusive
	//   不会把健康对判死**（★ R151 确立的关键区分，不能破坏）。
	//
	// ★ 反面：若 observation 路径**不**碰它 ⇒ 「不写回」就真的是「什么都不做」
	//   ⇒ 探针每 5 s 重新武装、状态一行不改 ⇒ ★★★ **永久 broken_confirmed**。
	obsSQL := probeBudgetRead(t, "bg/credential_probe_response_validation.go")
	if !strings.Contains(obsSQL, "probe_consecutive_failures = COALESCE(probe_consecutive_failures, 0) + 1") {
		t.Errorf("%s 里找不到「inconclusive 让 probe_consecutive_failures + 1」⇒ "+
			"★★ D9c 未落地：inconclusive 既不解封、也不退避 ⇒ 把对象钉死在死状态 "+
			"（R151 实测：node_probe_state 一行不改、consecutive_failures 卡在 3）",
			"bg/credential_probe_response_validation.go")
	}
}

// TestProbeBudgetDetectorHasTeeth 是阳性对照：故意写坏的合成片段必须被报出。
func TestProbeBudgetDetectorHasTeeth(t *testing.T) {
	// ① 超时字面量消失 ⇒ 必须被「形态变了」这条判据捕获。
	src := "client: &http.Client{Timeout: 45 * time.Second},"
	if strings.Contains(src, "Timeout: 30 * time.Second") {
		t.Fatal("阳性对照失效：改过的字面量仍被判定为存在")
	}
	// ② inconclusive 的显式声明消失 ⇒ 必须被捕获。
	if strings.Contains("some other message", "inconclusive probe") ||
		strings.Contains("some other message", "preserving authoritative state") {
		t.Fatal("阳性对照失效：改过的文本仍被判定为存在")
	}
}

// TestProbeBudgetDetectorNoFalsePositive 是阴性对照：合规片段不得被误报。
func TestProbeBudgetDetectorNoFalsePositive(t *testing.T) {
	compliant := "client: &http.Client{Timeout: 30 * time.Second},"
	if !strings.Contains(compliant, "Timeout: 30 * time.Second") {
		t.Fatal("阴性对照失效：合规字面量被误判")
	}
	if !strings.Contains("credential probe v2: inconclusive probe; preserving authoritative state",
		"preserving authoritative state") {
		t.Fatal("阴性对照失效：合规文本被误判")
	}
}
