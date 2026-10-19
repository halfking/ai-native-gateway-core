package outputcompliance

// r1037（2026-10-20，211）：硬阻断清单的**测试基线**（同 security/sanitize 的
// hardblock_testmain_test.go，理由见那里的注释）。
//
// 本包既有的判据写的是「这条守卫开着的时候它拦得住 X」；清单默认关掉之后，
// 它们必须显式把守卫打开，否则会退化成假绿。默认行为由 r1037 的新判据单独测。

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
