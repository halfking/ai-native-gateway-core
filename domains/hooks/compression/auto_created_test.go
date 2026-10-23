package compression

import (
	"context"
	"testing"
)

// ★★ 同包测试（★ 必须同包：★ 要能覆盖包级开关变量 `skipAutoCreatedEnabled`）。
// ★★ 行为断言放在这里（★ 而不是 cmd/gateway 的门里）★ ⇒ ⇒ cmd/gateway 那个门
// ★★ 只能看见别的包 ⇒ ⇒ 它的职责是「钉住【实现存在 + 形态正确】」，
// ★★ 【行为正确性】由本文件负责。★★

// TestSkipAutoCreatedDefaultIsOff 断言【默认关】。
//
// ★ ★★ 这是本轮最要紧的一条：★ §143.85 的设计原本写的是
// `settings.IsEnabled("session_cache_skip_auto_created")` ⇒ ⇒ ★★ 但那个 API 是
// 【kill-switch】语义（★ 未注册的名字【fail-open ⇒ 默认「开」】）⇒ ⇒ ★★ 用它实现
// 「默认关」【根本实现不了】⇒ ⇒ 这里钉住【独立的 opt-in env】的默认行为。
func TestSkipAutoCreatedDefaultIsOff(t *testing.T) {
	t.Setenv(EnvSkipAutoCreated, "")
	if skipAutoCreatedEnabledDefault() {
		t.Fatalf("★ %s 未设置时 skipAutoCreatedEnabledDefault() 必须为 false —— "+
			"★ ★★ 「默认关」是本设计的安全底线：★ 代码已部署但开关没打开 ⇒ ⇒ "+
			"★ ⇒ ⇒ 线上行为必须与改动前逐字一致",
			EnvSkipAutoCreated)
	}
}

// TestSkipAutoCreatedDefaultReadsEnv 断言 opt-in env 的取值语义。
func TestSkipAutoCreatedDefaultReadsEnv(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"false", false},
		{"0", false},
	} {
		t.Setenv(EnvSkipAutoCreated, tc.val)
		if got := skipAutoCreatedEnabledDefault(); got != tc.want {
			t.Errorf("★ %s=%q ⇒ skipAutoCreatedEnabledDefault() = %v（期望 %v）",
				EnvSkipAutoCreated, tc.val, got, tc.want)
		}
	}
}

// TestShouldSkipLegacyLoadRequiresEqualSessionID 断言【零误伤护栏】：
//
// ★ ctx 里的自动新建会话 ID 必须与【正在查的】 gwSessionID 相等才短路。
func TestShouldSkipLegacyLoadRequiresEqualSessionID(t *testing.T) {
	orig := skipAutoCreatedEnabled
	t.Cleanup(func() { skipAutoCreatedEnabled = orig })
	skipAutoCreatedEnabled = func() bool { return true }

	ctx := WithAutoCreatedSession(context.Background(), "sess-A")
	if !ShouldSkipLegacyLoad(ctx, "sess-A") {
		t.Error("★ 同一个会话 ID ⇒ 必须短路（★ 这才是 ⓪ 的目标）")
	}
	if ShouldSkipLegacyLoad(ctx, "sess-B") {
		t.Error("★ ★★ 会话 ID 不同 ⇒ ★ 必须【不短路】—— ★ 恢复/重试路径的 gwSessionID " +
			"★ 来自请求头 ⇒ ⇒ 它【可能】与本次会话不同 ⇒ ⇒ 只传 bool 会误伤别的会话")
	}
	if ShouldSkipLegacyLoad(ctx, "") {
		t.Error("★ gwSessionID 为空 ⇒ 必须【不短路】")
	}
}

// TestShouldSkipLegacyLoadRequiresSwitchOn 断言【开关关 ⇒ 一律不短路】。
func TestShouldSkipLegacyLoadRequiresSwitchOn(t *testing.T) {
	orig := skipAutoCreatedEnabled
	t.Cleanup(func() { skipAutoCreatedEnabled = orig })
	skipAutoCreatedEnabled = func() bool { return false }

	ctx := WithAutoCreatedSession(context.Background(), "sess-A")
	if ShouldSkipLegacyLoad(ctx, "sess-A") {
		t.Error("★ ★★ 开关关闭时必须【一律不短路】—— ★ 默认关 ⇒ ⇒ 部署后行为不变")
	}
}

// TestShouldSkipLegacyLoadRejectsUnmarkedContext 断言【没有标记 ⇒ 不短路】。
func TestShouldSkipLegacyLoadRejectsUnmarkedContext(t *testing.T) {
	orig := skipAutoCreatedEnabled
	t.Cleanup(func() { skipAutoCreatedEnabled = orig })
	skipAutoCreatedEnabled = func() bool { return true }

	if ShouldSkipLegacyLoad(context.Background(), "sess-A") {
		t.Error("★ ctx 里没有自动新建标记 ⇒ 必须【不短路】")
	}
	if ShouldSkipLegacyLoad(nil, "sess-A") {
		t.Error("★ ctx 为 nil ⇒ 必须【不短路】且【不 panic】")
	}
}

// TestWithAutoCreatedSessionEdgeCases 断言 ctx 载体的边界行为。
func TestWithAutoCreatedSessionEdgeCases(t *testing.T) {
	if got := AutoCreatedSessionID(WithAutoCreatedSession(nil, "sess-A")); got != "sess-A" {
		t.Errorf("★ nil ctx ⇒ WithAutoCreatedSession 仍应带上 sessionID（★ got=%q）", got)
	}
	if got := AutoCreatedSessionID(WithAutoCreatedSession(context.Background(), "")); got != "" {
		t.Errorf("★ 空 sessionID ⇒ 不应制造标记（★ got=%q）", got)
	}
	if got := AutoCreatedSessionID(context.Background()); got != "" {
		t.Errorf("★ 未标记的 ctx ⇒ AutoCreatedSessionID 必须返回空（★ got=%q）", got)
	}
	if got := AutoCreatedSessionID(nil); got != "" {
		t.Errorf("★ nil ctx ⇒ AutoCreatedSessionID 必须返回空（★ got=%q）", got)
	}
}
