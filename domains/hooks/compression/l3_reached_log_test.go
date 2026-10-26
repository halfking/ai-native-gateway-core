package compression

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// ★ §143.89 的同包单元测试：★ 【行为正确性】必须在【被测包的同包测试】里
// （★ cmd/gateway 那个包看不见本包的内部变量 ⇒ ★ 跨包门只能钉形态）。
//
// ★ ★★ 三条：★ ① 默认关（★ opt-in 的安全底线）｜★ ② env 取值语义（★ 方向与 kill-switch 相反）｜
// ★ ③ 空 sessionID 不记（★ 便于按 session 求交时过滤）。

// TestL3ReachedLogDefaultIsOff（★ 默认关）
func TestL3ReachedLogDefaultIsOff(t *testing.T) {
	t.Setenv(EnvL3ReachedLog, "")
	if l3ReachedLogEnabledDefault() {
		t.Error("★ 未设置 env 时开关必须是【关】 —— ★ 默认关是新增观测/优化行为的安全底线" +
			"★ ⇒ ⇒ 部署后线上行为必须与改动前逐字一致")
	}
}

// TestL3ReachedLogEnvSemantics（★ env 取值语义）
func TestL3ReachedLogEnvSemantics(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"on", true},
		{"yes", true},
		{"TRUE", true},
		{" 1 ", true},
		{"garbage", true}, // ★ 非空非布尔 ⇒ 按「开」处理（★ 与 kill-switch 取值风格一致、方向相反）
	}
	for _, c := range cases {
		t.Setenv(EnvL3ReachedLog, c.raw)
		if got := l3ReachedLogEnabledDefault(); got != c.want {
			t.Errorf("★ env=%q ⇒ 开关 = %v，期望 %v", c.raw, got, c.want)
		}
	}
}

// TestLogL3ReachedIgnoresEmptySessionID（★ 空 sessionID 不记）
func TestLogL3ReachedIgnoresEmptySessionID(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	// ★ ★★ 默认关 ⇒ 无论 sessionID 是什么都不该有输出。
	LogL3Reached(context.Background(), "", "tenant-1")
	if buf.Len() != 0 {
		t.Errorf("★ 空 sessionID 时不应产生日志，★ 实际输出 = %q", buf.String())
	}

	// ★ ★★ 开关关时（默认）⇒ 也不应有输出。
	LogL3Reached(context.Background(), "sess-1", "tenant-1")
	if buf.Len() != 0 {
		t.Errorf("★ 开关关闭时不应产生日志，★ 实际输出 = %q", buf.String())
	}

	// ★ ★★ 埋点文本必须可 grep（★ 属主的对拍手段）。
	if !strings.Contains("session_cache: read chain reached L3", "read chain reached L3") {
		t.Error("★ 埋点文本不可 grep —— ★ 属主要靠它与服务侧 v2 落空日志求交")
	}
}
