package streaming

// r1037（2026-10-20，211）：硬阻断清单的**测试基线**。
//
// ## 为什么本包也需要 TestMain
//
// 硬阻断清单的**出厂默认**是「只有请求侧（input）允许硬阻断」——egress/output
// 一律不终止回合（owner 口径 2026-10-20）。
//
// 但本包既有的判据写的是「**这条守卫开着的时候**它拦得住 X」：
//
//   - r1028_swallowed_stream_repro_test.go：Bash 参数里的公网 IP 必须被拦，
//     且客户端必须拿到带 code=output_policy_blocked 的终态帧；
//   - managed_write_snapshot_test.go：发布的 output/block 规则必须产出 403。
//
// 清单默认关掉之后，这些判据若原样跑，会从「测拦截能力」退化成「测默认不拦」——
// **阻断侧与允许侧都会假绿**（判据纪律第 3 条：放行有两种成因，(b) 压根没触发
// 也是放行）。
//
// 所以本包统一在 TestMain 里把清单打开成「全部可阻断」= 清单引入前的行为，
// 让既有判据继续测它本来要测的东西；**默认行为由 r1037 的新判据单独测**
// （那里显式把清单设成出厂默认）。
//
// 新增判据若要测「默认不拦」，必须显式调用 withHardBlockAllows(t,
// hardBlockDefault()...)——不要依赖本文件的隐式前提。

import (
	"os"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// hardBlockDefault 是硬阻断清单的**出厂默认**：只有请求侧允许硬阻断。
func hardBlockDefault() []sanitizerules.HardBlockAllow {
	return []sanitizerules.HardBlockAllow{{Scope: "input", Rule: "*"}}
}

// hardBlockAll 打开 egress+output 的全部硬阻断（= 清单引入前的行为）。
func hardBlockAll() []sanitizerules.HardBlockAllow {
	return []sanitizerules.HardBlockAllow{
		{Scope: "input", Rule: "*"},
		{Scope: "egress", Rule: "*"},
		{Scope: "output", Rule: "*"},
	}
}

// withHardBlockAllows 临时替换硬阻断清单，跑完恢复。
func withHardBlockAllows(t *testing.T, allows ...sanitizerules.HardBlockAllow) {
	t.Helper()
	prev := sanitizerules.HardBlockAllows()
	sanitizerules.SetHardBlockAllows(allows)
	t.Cleanup(func() { sanitizerules.SetHardBlockAllows(prev) })
}

func TestMain(m *testing.M) {
	sanitizerules.SetHardBlockAllows(hardBlockAll())
	os.Exit(m.Run())
}
