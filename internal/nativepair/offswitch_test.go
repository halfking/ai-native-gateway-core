package nativepair

import (
	"os"
	"os/exec"
	"testing"
)

// TestEnabledOffSwitch（环境级验证，2026-10-28 三巡）：
// LLM_GATEWAY_NATIVE_PASSTHROUGH=off 必须让 Match 整体判否（紧急回退开关）。
//
// Enabled 用 sync.OnceValue 进程内缓存 ⇒ 同一测试进程无法翻转它 ⇒ 这里用
// 子进程重置环境验证。断言方式：子进程以 off 跑 TestMatch_BuiltinPairs，
// 期望**失败**（正例全部 want true，off 下必然 false）—— 开关有效 ⇔ 子进程红。
// 外层再跑一遍不带 off 的子进程作为对照（必须绿），排除「子进程本来就红」
// 的假信号。
func TestEnabledOffSwitch(t *testing.T) {
	if os.Getenv("NATIVEPAIR_OFFSWITCH_CHILD") == "1" {
		// 子进程：只跑正例表，off 下应全红 ⇒ 本函数在子进程里必然失败，
		// 外层以「失败」为期望。
		for _, tc := range []struct{ client, catalog, model string }{
			{"zcode", "zhipu", "glm-5.3"},
			{"minimax-code", "minimax", "minimax-m2"},
			{"deepseek-code", "deepseek", "deepseek-chat"},
		} {
			if Match(tc.client, tc.catalog, tc.model) {
				t.Errorf("off switch: Match(%q,%q,%q) = true, want false", tc.client, tc.catalog, tc.model)
			}
		}
		return
	}

	goExe, _ := exec.LookPath("go")

	run := func(extraEnv ...string) (childPassed bool, out string) {
		cmd := exec.Command(goExe, "test", "-count=1", "-run", "TestEnabledOffSwitch", ".")
		cmd.Env = append(os.Environ(), "NATIVEPAIR_OFFSWITCH_CHILD=1")
		cmd.Env = append(cmd.Env, extraEnv...)
		outBytes, err := cmd.CombinedOutput()
		return err == nil, string(outBytes)
	}

	// 对照臂：不带 off 的子进程必须红（正例在开关开启时全真 ⇒ 子测试的
	// want-false 断言失败）—— 红了才说明子进程真的在执行断言。
	if childPassed, out := run(); childPassed {
		t.Fatalf("control arm passed (child executed nothing?): expected child failure without off:\n%s", out)
	}
	// off 臂：子进程必须绿 —— 绿了才证明开关真的穿透到 Match（正例全部判否）。
	if childPassed, out := run("LLM_GATEWAY_NATIVE_PASSTHROUGH=off"); !childPassed {
		t.Fatalf("off arm failed (switch is NOT wired to Match):\n%s", out)
	}
}
