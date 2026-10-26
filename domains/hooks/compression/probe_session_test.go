package compression

import (
	"testing"
)

// ★★ 同包测试（必须同包：要能覆盖包级开关变量 skipProbeSessionsEnabled 的
// ★★ Default 纯函数 + 断言行为）。行为断言放在这里；cmd/gateway 的
// ★★ section_143103 门只负责「钉住实现存在 + 形态正确 + 跨包一致」。

// TestSkipProbeSessionsDefaultIsOff 断言【默认关】—— 本设计的安全底线：
// 代码已部署但开关没打开 ⇒ 线上行为与改动前逐字一致。
func TestSkipProbeSessionsDefaultIsOff(t *testing.T) {
	t.Setenv(EnvSkipProbeSessions, "")
	if skipProbeSessionsEnabledDefault() {
		t.Fatalf("★ %s 未设置时 skipProbeSessionsEnabledDefault() 必须为 false", EnvSkipProbeSessions)
	}
}

// TestSkipProbeSessionsReadsEnv 断言 opt-in env 的取值语义。
func TestSkipProbeSessionsReadsEnv(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"whatever", true}, // 非空且非 bool ⇒ 开（与 §143.86 取值风格一致）
	} {
		t.Setenv(EnvSkipProbeSessions, tc.val)
		if got := skipProbeSessionsEnabledDefault(); got != tc.want {
			t.Errorf("EnvSkipProbeSessions=%q ⇒ %v, want %v", tc.val, got, tc.want)
		}
	}
}

// TestIsProbeSyntheticSessionID 断言判据形态：认得生成器的 probe 形态、
// 不误伤真实用户会话与其它 kind 的合成会话。
func TestIsProbeSyntheticSessionID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		// 生成器形态（sessionv2mirror.SyntheticSessionID probe kind）：
		{"sys:probe:cred35:20260923", true},   // §143.100 实测的那个 4 万轮会话
		{"sys:probe:cred126:20261024", true},  // 凭据 scope
		{"sys:probe:prov7:20260101", true},    // 供应商 scope
		{"sys:probe:gw:20260101", true},       // gw 兜底 scope
		// 其它 kind 的合成会话（§143.103 刻意不覆盖：无独立证据）：
		{"sys:internal:cred3:20261024", false},
		{"sys:anon:gw:20261024", false},
		// 真实用户会话（来自会话头，命名空间分离）：
		{"", false},
		{"abc123", false},
		{"session-xyz", false},
		{"probe:session", false},
		{"sys:probe", false},            // 前缀不完整（缺尾冒号）
		{"SYS:PROBE:cred1:20260101", false}, // 大小写敏感（生成器只产小写）
	} {
		if got := IsProbeSyntheticSessionID(tc.id); got != tc.want {
			t.Errorf("IsProbeSyntheticSessionID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestShouldSkipProbeLoad 断言双条件：开关关 ⇒ 恒 false（含探测形态）；
// 开关开 ⇒ 只对探测形态 true。
//
// ★ 直接换掉包级 var（auto_created_test.go 同款）：OnceValue 在进程内缓存
// ⇒ 靠 Setenv 驱动它【不可复现】（同包其它测试可能已经触发过缓存）。
func TestShouldSkipProbeLoad(t *testing.T) {
	orig := skipProbeSessionsEnabled
	t.Cleanup(func() { skipProbeSessionsEnabled = orig })

	skipProbeSessionsEnabled = func() bool { return false }
	if ShouldSkipProbeLoad("sys:probe:cred35:20260923") {
		t.Fatal("开关关 ⇒ 即使探测形态也必须 false（默认关底线）")
	}

	skipProbeSessionsEnabled = func() bool { return true }
	if !ShouldSkipProbeLoad("sys:probe:cred35:20260923") {
		t.Fatal("开关开 + 探测形态 ⇒ 必须 true")
	}
	if ShouldSkipProbeLoad("user-session-42") {
		t.Fatal("开关开 + 非探测形态 ⇒ 必须 false（零误伤）")
	}
	if ShouldSkipProbeLoad("") {
		t.Fatal("空 session ID ⇒ 必须 false")
	}
}
