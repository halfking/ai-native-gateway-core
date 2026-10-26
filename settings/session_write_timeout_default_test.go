package settings

// session_write_timeout_default_test.go — 2026-10-20。
//
// 钉住「sessions_v2.write_timeout_ms 的 spec.Default」与
// 「internal/sessionv2mirror 的 defaultShadowWriteTimeoutMs」**必须一致**。
//
// 为什么必须钉（这是一次被静默撤销的修复）：
//
//	internal/sessionv2mirror/hook.go 的注释写着「raise the default budget to
//	2000ms」，它把常量 defaultShadowWriteTimeoutMs 从 500 提到了 2000。
//	但 settings.GetPlatformInt(key, fallback) 的解析顺序是
//	**DB → env → spec.Default → 代码里的 fallback**：
//	spec.Default 命中时，代码里那个常量**永远用不上**。
//	而 spec.Default 一直是 500 ⇒ 那次「提到 2000」的修复从来没有生效过，
//	实际预算一直是 500ms。
//
// 代价（本地实测）：一次 shadow write 要串行做 session_dim 维度写 +
// session_turns + session_turn_details + session_bodies + session_summaries，
// 500ms 撑不住 ⇒ 细节层写超时 ⇒ 正文一行不落、整笔写走 outbox 异步重放
// ⇒ turn_no 与请求顺序错位、trace 丢失。**全是静默失效。**
//
// 本门的作用不是「把值钉死在 2000」，而是**钉住「两边不许各写各的」**：
// 谁把常量改成别的值，门会红，并要求同步改 spec。

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// mirrorWriteTimeoutConst 是 internal/sessionv2mirror 里的常量在源码中的字面量。
// 跨包拿不到未导出常量（settings 不能 import internal/sessionv2mirror，
// 那会造成依赖倒置），所以读源码取字面量 —— 读的是**同一个常量**，
// 不是复制品：常量改名/改值都会让这道门红。
func mirrorWriteTimeoutConst(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile("../internal/sessionv2mirror/hook.go")
	if err != nil {
		t.Fatalf("read mirror hook source: %v", err)
	}
	src := string(raw)
	const marker = "defaultShadowWriteTimeoutMs = "
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatal("defaultShadowWriteTimeoutMs 常量在 internal/sessionv2mirror/hook.go 里找不到了 —— " +
			"要么被改名，要么被挪走。两边一致这道门就失去意义，必须一起改。")
	}
	rest := src[idx+len(marker):]
	end := strings.IndexAny(rest, "\n")
	if end < 0 {
		t.Fatal("defaultShadowWriteTimeoutMs 的定义没有换行，源码形态异常")
	}
	v, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		t.Fatalf("defaultShadowWriteTimeoutMs 不是整数字面量（%q）：%v", strings.TrimSpace(rest[:end]), err)
	}
	return v
}

func TestSessionWriteTimeoutSpecDefaultMatchesMirrorConstant(t *testing.T) {
	sp := lookupSessionWriteTimeoutSpec(t)
	if sp == nil {
		t.Fatal("sessions_v2.write_timeout_ms 不在 spec 表里 —— spec.Default 消失 ⇒ " +
			"解析会掉到代码里的 fallback，而代码里那个值又被 spec.Default 覆盖过，语义再次漂移")
	}
	got, ok := sp.Default.(int)
	if !ok {
		t.Fatalf("sessions_v2.write_timeout_ms 的 Default 不是 int，而是 %T —— "+
			"GetPlatformInt 走 json.Unmarshal，类型不对会整条回落 fallback", sp.Default)
	}
	want := mirrorWriteTimeoutConst(t)
	if got != want {
		t.Fatalf("sessions_v2.write_timeout_ms 的 spec.Default = %d，但 internal/sessionv2mirror "+
			"的 defaultShadowWriteTimeoutMs = %d —— **两边不一致就等于悄悄改了写预算**。"+
			"解析顺序是 DB → env → spec.Default → 代码 fallback，spec.Default 命中时那个常量不生效。",
			got, want)
	}
}

// TestSessionWriteTimeoutSpecDefaultIsNotTighterThanTheMirrorConstant 是一道
// **方向性**的补充门：即便有人把两边改成同一个更小的值（两边一致了），
// 这个值也必须够撑完一次 shadow write 的串行步骤。
//
// 依据（本地实测）：一次 shadow write 串行做 session_dim 维度写（其触发器
// 还要 UPDATE session_summaries）+ session_turns + session_turn_details +
// session_bodies + session_summaries。500ms 实测撑不住（正文整段丢失）。
// 2000ms 是仓内注释明确写下的目标值。
func TestSessionWriteTimeoutSpecDefaultIsNotTighterThanTheMirrorConstant(t *testing.T) {
	sp := lookupSessionWriteTimeoutSpec(t)
	if sp == nil {
		t.Fatal("sessions_v2.write_timeout_ms 不在 spec 表里")
	}
	got, ok := sp.Default.(int)
	if !ok {
		t.Fatalf("Default 不是 int（%T）", sp.Default)
	}
	const floorMs = 2000
	if got < floorMs {
		t.Fatalf("sessions_v2.write_timeout_ms 的 spec.Default = %d ms < %d ms —— "+
			"这个预算撑不住一次 shadow write 的串行步骤（实测：正文整段丢失、"+
			"turn_no 错位、trace 丢失）。要么给足，要么把写路径拆成多段各自带预算。",
			got, floorMs)
	}
}

// lookupSessionWriteTimeoutSpec 从已注册的 spec 表里取这条设置。
func lookupSessionWriteTimeoutSpec(t *testing.T) *Spec {
	t.Helper()
	for _, sp := range SessionsV2Specs() {
		if sp != nil && sp.Key == "sessions_v2.write_timeout_ms" {
			return sp
		}
	}
	return nil
}
