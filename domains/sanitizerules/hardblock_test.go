package sanitizerules

// r1037（2026-10-20，211）：硬阻断清单的解析与匹配语义。
//
// 这份清单是 owner 口径落地的**唯一开关**：
//
//	「Bash 参数里出现公网主机，默认不要拦截」+「最好有一个列表可控制」
//
// ⇒ 清单本身的行为（解析、匹配、未设置时的默认）必须有独立判据，
// 否则「配错了却静默不生效」这类事故无从查起。

import (
	"os"
	"testing"
)

func TestR1037ParseHardBlockAllowsForms(t *testing.T) {
	// r1039（D5）：egress scope 只接受逐条的内建规则 ID —— 前缀族与通配都不再
	// 接受（`egress:secret.*` 会把 secret 分类器变成阻断规则）。
	// output scope 的三种写法不受影响。
	got, err := ParseHardBlockAllows("egress:builtin.network_destination, output:builtin.pii.email , output:secret.*")
	if err != nil {
		t.Fatalf("ParseHardBlockAllows: %v", err)
	}
	want := []HardBlockAllow{
		{Scope: "egress", Rule: "builtin.network_destination"},
		{Scope: "output", Rule: "builtin.pii.email"},
		{Scope: "output", Rule: "secret.*"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d allows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allow[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestR1037ParseHardBlockAllowsRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"builtin.network_destination", // 缺 scope
		"weird:rule",                  // 未知 scope
		"egress:",                     // 空规则
		"egress:builtin.x,broken",     // 列表里有一项非法 → 整体报错
	} {
		if _, err := ParseHardBlockAllows(raw); err == nil {
			t.Errorf("ParseHardBlockAllows(%q) 应报错，实际通过", raw)
		}
	}
}

func TestR1037HardBlockAllowedMatching(t *testing.T) {
	SetHardBlockAllows([]HardBlockAllow{
		{Scope: "egress", Rule: "builtin.network_destination"},
		{Scope: "egress", Rule: "secret.*"},
		{Scope: "output", Rule: "*"},
	})
	cases := []struct {
		scope, id string
		want      bool
		why       string
	}{
		{"egress", "builtin.network_destination", true, "精确 ID 命中"},
		{"egress", "secret.password.assignment", true, "前缀族命中"},
		{"egress", "builtin.secret_field.egress", false, "前缀 secret.* 不覆盖 builtin. 前缀"},
		{"egress", "pii.server_ip", false, "未列入"},
		{"output", "anything.at.all", true, "output scope 是通配"},
		{"input", "pii.server_ip", false, "scope 隔离：output 的通配不串到 input"},
		{"egress", "", false, "空规则 ID = 不允许（说不清是哪条规则就不许拦）"},
	}
	for _, tc := range cases {
		if got := HardBlockAllowed(tc.scope, tc.id); got != tc.want {
			t.Errorf("HardBlockAllowed(%q, %q) = %v, want %v（%s）", tc.scope, tc.id, got, tc.want, tc.why)
		}
	}
}

// TestR1037HardBlockAllowedPrefixDoesNotOverreach 钉住前缀匹配的边界：
// `secret.*` 只匹 `secret.` 开头的 ID，不能把 `secretive.*` 也放进来。
func TestR1037HardBlockAllowedPrefixDoesNotOverreach(t *testing.T) {
	SetHardBlockAllows([]HardBlockAllow{{Scope: "egress", Rule: "secret.*"}})
	if HardBlockAllowed("egress", "secretive.thing") {
		t.Error("前缀匹配越界：secret.* 放行了 secretive.*")
	}
	if !HardBlockAllowed("egress", "secret.thing") {
		t.Error("前缀匹配漏放：secret.* 没放行 secret.thing")
	}
}

// TestR1037LoadFromEnvKeepsDefaultWhenUnset 是运维最关心的一条：
// **没配** ≠ **配成全放开**。未设置环境变量时必须保持出厂默认
// （只有 input 允许硬阻断），否则「忘了配」= 「全网不再拦外传」。
func TestR1037LoadFromEnvKeepsDefaultWhenUnset(t *testing.T) {
	t.Setenv(HardBlockAllowEnv, "")
	os.Unsetenv(HardBlockAllowEnv)
	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })

	SetHardBlockAllows([]HardBlockAllow{{Scope: "input", Rule: "*"}})
	allows, err := LoadHardBlockAllowsFromEnv()
	if err != nil {
		t.Fatalf("LoadHardBlockAllowsFromEnv: %v", err)
	}
	if len(allows) != 1 || allows[0].Scope != "input" || allows[0].Rule != "*" {
		t.Fatalf("未设置环境变量时应保持出厂默认，实际 %+v", allows)
	}
	if HardBlockAllowed("egress", "builtin.network_destination") {
		t.Error("未设置环境变量时 egress 不该被允许硬阻断")
	}
	if !HardBlockAllowed("input", "pii.server_ip") {
		t.Error("未设置环境变量时 input 侧应仍然 fail-closed")
	}
}

// TestR1037LoadFromEnvDashClearsList 固化「显式清空」的写法：
// 用 "-"（而不是空串）表达「清空清单」，与「未设置」区分开。
func TestR1037LoadFromEnvDashClearsList(t *testing.T) {
	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })
	t.Setenv(HardBlockAllowEnv, "-")

	allows, err := LoadHardBlockAllowsFromEnv()
	if err != nil {
		t.Fatalf("LoadHardBlockAllowsFromEnv: %v", err)
	}
	if len(allows) != 0 {
		t.Fatalf("\"-\" 应清空清单，实际 %+v", allows)
	}
	if HardBlockAllowed("input", "pii.server_ip") {
		t.Error("清单清空后 input 侧也不该允许硬阻断")
	}
}

func TestR1037LoadFromEnvAppliesConfiguredList(t *testing.T) {
	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })
	t.Setenv(HardBlockAllowEnv, "egress:builtin.network_destination")

	if _, err := LoadHardBlockAllowsFromEnv(); err != nil {
		t.Fatalf("LoadHardBlockAllowsFromEnv: %v", err)
	}
	if !HardBlockAllowed("egress", "builtin.network_destination") {
		t.Error("配置的规则未生效")
	}
	if HardBlockAllowed("output", "builtin.pii.email") {
		t.Error("只配了 egress，output 不该被放开")
	}
}

// TestR1037LoadFromEnvIsAdditive 钉住清单的**累加**语义。
//
// 211 首轮部署实测踩到的坑：把 `egress:builtin.egress_sink` 配上去后，
// 启动日志的 allows 里 **input 没了** ⇒ 只想定一条 egress 规则的运维，
// 顺手把请求侧的明文凭据 fail-closed 也关了 —— 那是**静默放宽已有收紧**。
//
// ⇒ 配置项只能**加**规则，不能顶掉出厂默认；关请求侧底线必须显式写 "-"。
func TestR1037LoadFromEnvIsAdditive(t *testing.T) {
	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })
	SetHardBlockAllows([]HardBlockAllow{{Scope: "input", Rule: "*"}})
	t.Setenv(HardBlockAllowEnv, "egress:builtin.egress_sink")

	allows, err := LoadHardBlockAllowsFromEnv()
	if err != nil {
		t.Fatalf("LoadHardBlockAllowsFromEnv: %v", err)
	}
	if !HardBlockAllowed("input", "pii.server_ip") {
		t.Error("配置 egress 规则后，请求侧的 fail-closed 底线被顶掉了（清单语义应是累加）")
	}
	if !HardBlockAllowed("egress", "builtin.egress_sink") {
		t.Error("配置的 egress 规则未生效")
	}
	// 重复项去重：配置里显式再写一遍 input:*，启动日志里也不该出现两条。
	t.Setenv(HardBlockAllowEnv, "egress:builtin.egress_sink,input:*")
	allows, err = LoadHardBlockAllowsFromEnv()
	if err != nil {
		t.Fatalf("LoadHardBlockAllowsFromEnv: %v", err)
	}
	seen := map[HardBlockAllow]int{}
	for _, a := range allows {
		seen[a]++
	}
	for a, n := range seen {
		if n > 1 {
			t.Errorf("清单出现重复项 %+v（%d 次）", a, n)
		}
	}
}

// TestR1037LoadFromEnvInvalidKeepsPrevious 固化「配错不静默生效」：
// 解析失败时**保持原清单**并报错——不能因为一个错别字就悄悄把守门全撤了。
//
// 注：规则 ID 里**可以**含冒号（解析按第一个冒号切分 scope:rule），
// 所以「少一个冒号」与「scope 拼错」才是真的非法值。
func TestR1037LoadFromEnvInvalidKeepsPrevious(t *testing.T) {
	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })
	SetHardBlockAllows([]HardBlockAllow{{Scope: "egress", Rule: "*"}})
	t.Setenv(HardBlockAllowEnv, "egress-no-scope-separator")

	if _, err := LoadHardBlockAllowsFromEnv(); err == nil {
		t.Fatal("非法值应报错")
	}
	if !HardBlockAllowed("egress", "anything") {
		t.Error("解析失败后原清单被改动")
	}
}
