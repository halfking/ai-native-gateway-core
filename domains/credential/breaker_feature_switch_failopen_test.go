package credential

// ★★ circuit_degradation 开关关闭时的 fail-open 语义（2026-11-18 · Worker
// 交付包第 ⑫ 条）
//
// ── 这条测试为什么长成「父进程起子进程」的样子 ────────────────────────
//
// 开关的真实来源是 `settings.IsEnabled("circuit_degradation")`
// （settings/feature_switches.go:37-44），它读的是环境变量
// `KILL_CIRCUIT_DEGRADATION`（:76-88 的 readKillSwitch），且**进程内只读
// 一次**（:29-38 的 sync.Once + featureCache）⇒ 同一进程里改环境变量不会
// 让它翻转，settings 包也没有导出重置钩子。
// ⇒ 想在测试里拿到「关」这一真实态，只能让**带着该环境变量的子进程**去
//   读它。本文件因此用「父进程起子进程（跑同一个测试二进制）」的形态。
//
// ── 消费方与本工作包的边界 ────────────────────────────────────────────
//
// 真正的消费方在 `domains/streaming/executors/executor_dispatch.go:951-956`：
//
//	circuitOpen := !settings.IsEnabled("circuit_degradation")
//	if !circuitOpen {
//		probeConsumed = e.Circuit.Allow(cand.ProviderID, cand.CredentialID)
//		circuitOpen = !probeConsumed
//	}
//
// 开关关掉时**整段跳过 breaker**（连 Allow() 都不调）⇒ fail-open。
// ★ 那个包由另一条工作线在改，本工作包**不读改**它的生产代码 ⇒ 这里用
//   settings 的**既有 API**（真实开关读取）+ 一段显式标注为「契约镜像」的
//   判定式复现该语义，并用**负控**证明镜像不是恒真。
//   镜像与生产式的差别只有一处：这里传的是 *Breaker 而不是 e.Circuit。

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// breakerSwitchSubprocessMarker 标记「我是被父进程拉起来的子进程」。
	breakerSwitchSubprocessMarker = "LLM_GATEWAY_BREAKER_SWITCH_SUBPROCESS"
	// breakerSwitchExpectEnv 告诉子进程它该看到「开」还是「关」。
	breakerSwitchExpectEnv = "LLM_GATEWAY_BREAKER_SWITCH_EXPECT"
)

// circuitAllows mirrors the production decision in
// domains/streaming/executors/executor_dispatch.go:951-956:
//
//	circuitOpen := !settings.IsEnabled("circuit_degradation")
//	if !circuitOpen {
//		probeConsumed = e.Circuit.Allow(cand.ProviderID, cand.CredentialID)
//		circuitOpen = !probeConsumed
//	}
//	if circuitOpen && settings.IsEnabled("circuit_degradation") {
//	    …拒绝请求…
//	}
//
// ★ 这段有个**命名陷阱**（也是本条最值得钉的点）：:951 把 `circuitOpen`
// 初始化成 `!IsEnabled(...)`——开关**关**时它是 true，但那个 true 的含义
// 不是「熔断开路」而是「熔断模块被关掉了」。真正的拒绝条件是 :956 的
// **双重条件** `circuitOpen && IsEnabled(...)`：开关关 ⇒ 第二项为 false ⇒
// 即使 circuitOpen 为 true 也不拒绝 ⇒ fail-open。
// ⇒ 镜像必须照抄这个双重条件，只看 circuitOpen 会把 fail-open 读反。
//
// ★ 这是**契约镜像**而不是生产路径本身（生产消费方在 domains/streaming 包
// 内，本工作包不碰）。镜像存在的意义：把「开关关 ⇒ 不调 Allow ⇒ 不拒绝」
// 这条语义钉在 credential 包里，让开关的语义在本地有测试可依；再用负控
// 证明它不是恒真。
func circuitAllows(switchOn bool, b *Breaker) bool {
	circuitOpen := !switchOn
	if !circuitOpen {
		circuitOpen = !b.Allow()
	}
	// 生产式的拒绝条件（:956）是双重条件，不是单看 circuitOpen。
	return !(circuitOpen && switchOn)
}

// TestCircuitDegradationSwitchDrivesFailOpen 钉住 ⑫：circuit_degradation
// 关闭 ⇒ 熔断被**完全跳过** ⇒ 请求不被熔断拒绝（fail-open）。
//
// 父进程只负责拉起子进程（断言都在子进程里做，因为开关是进程级一次性读取）。
// 两个子进程分别覆盖「关」（KILL_CIRCUIT_DEGRADATION=1）与「开」（不设该
// 变量 ⇒ 默认开启，:20 的默认值声明）。
func TestCircuitDegradationSwitchDrivesFailOpen(t *testing.T) {
	t.Run("the switch off makes the breaker fail open", func(t *testing.T) {
		runBreakerSwitchSubprocess(t, "1", "off")
	})
	t.Run("the switch on makes the breaker reject", func(t *testing.T) {
		runBreakerSwitchSubprocess(t, "", "on")
	})
}

// TestBreakerSwitchSubprocessBody 是子进程里真正跑断言的用例。
// 父进程直接跑到它时（没有 marker 环境变量）跳过。
func TestBreakerSwitchSubprocessBody(t *testing.T) {
	if os.Getenv(breakerSwitchSubprocessMarker) != "1" {
		t.Skip("subprocess-only helper: the feature switch is read once per process")
	}
	expect := os.Getenv(breakerSwitchExpectEnv)
	if expect != "on" && expect != "off" {
		t.Fatalf("unexpected expectation %q", expect)
	}

	// 真实开关读取（settings 的既有 API，不是本地复制品）。
	switchOn := settings.IsEnabled("circuit_degradation")
	wantOn := expect == "on"
	if switchOn != wantOn {
		t.Fatalf("settings.IsEnabled(circuit_degradation) = %v, want %v (KILL_CIRCUIT_DEGRADATION=%q)",
			switchOn, wantOn, os.Getenv("KILL_CIRCUIT_DEGRADATION"))
	}

	// 前提在场：造一个**开路**的 breaker（熔断本来会拒绝它）。
	m := NewManager()
	m.RecordFailurePerModel(20, 20, "", KindTransient)
	m.RecordFailurePerModel(20, 20, "", KindTransient)
	b := m.GetPerModel(20, 20, "")
	if got := b.State(); got != StateOpen {
		t.Fatalf("premise broken: breaker should be open, got %s", got)
	}

	allowed := circuitAllows(switchOn, b)
	if expect == "off" {
		if !allowed {
			t.Fatal("with circuit_degradation off an open breaker must not reject the request (fail-open)")
		}
		// 负控：开关开着时，同一个开路 breaker **必须**拒绝 ⇒ 上面那句
		// fail-open 不是恒真的镜像。
		if circuitAllows(true, b) {
			t.Fatal("negative control: with circuit_degradation on an open breaker must reject the request")
		}
	} else {
		if allowed {
			t.Fatal("with circuit_degradation on an open breaker must reject the request")
		}
	}

	t.Logf("subprocess-ok:%s", expect)
}

// runBreakerSwitchSubprocess 拉起一个带着指定 kill switch 的子进程，跑
// TestBreakerSwitchSubprocessBody 并断言它真的跑过（哨兵日志）。
func runBreakerSwitchSubprocess(t *testing.T, killSwitch, expect string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBreakerSwitchSubprocessBody$", "-test.v")
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		// 先清掉继承来的同名变量，再按本用例的意图设置一份 ⇒ 子进程读到的
		// 一定是本用例指定的值（不依赖调用者的环境）。
		if strings.HasPrefix(kv, "KILL_CIRCUIT_DEGRADATION=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		breakerSwitchSubprocessMarker+"=1",
		breakerSwitchExpectEnv+"="+expect,
	)
	if killSwitch != "" {
		env = append(env, "KILL_CIRCUIT_DEGRADATION="+killSwitch)
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess (%s) failed: %v\n%s", expect, err, out)
	}
	if !strings.Contains(string(out), "subprocess-ok:"+expect) {
		t.Fatalf("subprocess (%s) did not reach the assertions:\n%s", expect, out)
	}
}
