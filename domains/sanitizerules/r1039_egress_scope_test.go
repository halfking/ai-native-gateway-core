package sanitizerules

// r1039（D5，2026-10-20）：egress scope 只接受**逐条的内建规则 ID**。
//
// ## 为什么（实测，不是推演）
//
// `LLM_GATEWAY_HARD_BLOCK_RULES="egress:*"` 会把 PII/secret 分类器的 .egress
// 副本一并变成**硬阻断**规则，而那些规则的语义是「分类与遮蔽」，不是
// 「外传判定」。实测被拦的全是**正常操作**：
//
//	ssh deploy@prod.example.com 'kubectl get pods'  → 拦
//	scp file.txt backup@files.example.com:/tmp/x    → 拦
//	echo contact ops@example.com                    → 拦
//	一个手机号 / 一个身份证 / `发货地址 建国路88号`    → 拦
//
// 这正是 owner 说的「不要放大范围，导致正常的工具执行或脚本执行失败」。
//
// ## 契约
//
//	egress:*                  → 拒绝（通配会把分类器变成阻断规则）
//	egress:secret.*           → 拒绝（egress 不接受前缀族）
//	egress:pii.email.egress   → 拒绝（分类器规则不是外传判定）
//	egress:builtin.egress_sink → 接受
//	input:* / output:*        → 不变（出厂默认就是 input:*）
//
// ## 判据纪律
//
//   - 限制的**执行点是解析层**（运维唯一能碰的那张表）。
//     程序内 SetHardBlockAllows 不受限：security/sanitize 的测试基线用
//     `egress:*` 表达「全部可阻断」，r1035 的夹具用 PII 形状的规则 ID
//     （pii.server_ip）钉住「本地执行工具上的公网 IP 仍要拦」——
//     把限制下沉到匹配层会把这两条既有判据一起打死。
//   - 每条「放行」断言配一条「前提仍在场」断言：放行有两种成因——
//     (a) 解析层拒了它，(b) 解析层压根不认识它。(b) 是假绿。

import (
	"os"
	"strings"
	"testing"
)

// r1039InitialAllowlist 记录**进程启动时**的出厂清单。
//
// 为什么要在 TestMain 里抓：本包的既有判据会直接改进程级清单且不恢复
// （TestR1037HardBlockAllowedPrefixDoesNotOverreach 就是这样），所以
// 「出厂默认是什么」只能在任何判据跑之前取。
var r1039InitialAllowlist []HardBlockAllow

func TestMain(m *testing.M) {
	r1039InitialAllowlist = DefaultHardBlockAllows()
	os.Exit(m.Run())
}

// TestR1039EgressScopeRejectsWildcardAndClassifierRules 是本文件的主契约：
// egress scope 的通配、前缀族、分类器规则**一律拒绝**。
func TestR1039EgressScopeRejectsWildcardAndClassifierRules(t *testing.T) {
	for _, raw := range []string{
		"egress:*",
		"egress:secret.*",
		"egress:pii.email.egress",
		"egress:pii.server_ip",
		"egress:secret.password",
		"egress:builtin.egress_sink,egress:*",
	} {
		if _, err := ParseHardBlockAllows(raw); err == nil {
			t.Errorf("ParseHardBlockAllows(%q) 应报错（egress scope 不接受该项），实际通过", raw)
		} else if !strings.Contains(err.Error(), "egress") {
			t.Errorf("ParseHardBlockAllows(%q) 的错误信息应点名 egress scope，实际 %v", raw, err)
		}
	}
}

// TestR1039EgressScopeAcceptsExplicitBuiltinRules 是正负对照的另一半：
// 逐条的内建规则**照常接受**——限制不能把 egress 档位整个关掉。
func TestR1039EgressScopeAcceptsExplicitBuiltinRules(t *testing.T) {
	got, err := ParseHardBlockAllows("egress:builtin.egress_sink,egress:builtin.network_destination,egress:builtin.secret_field.egress")
	if err != nil {
		t.Fatalf("ParseHardBlockAllows: %v", err)
	}
	want := []HardBlockAllow{
		{Scope: "egress", Rule: "builtin.egress_sink"},
		{Scope: "egress", Rule: "builtin.network_destination"},
		{Scope: "egress", Rule: "builtin.secret_field.egress"},
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

// TestR1039InputScopeKeepsWildcard 钉住「限制只作用于 egress scope」。
//
// 出厂默认就是 {input:"*"}（请求侧 fail-closed 底线），它必须照常解析。
func TestR1039InputScopeKeepsWildcard(t *testing.T) {
	got, err := ParseHardBlockAllows("input:*")
	if err != nil {
		t.Fatalf("ParseHardBlockAllows(input:*): %v", err)
	}
	if len(got) != 1 || got[0].Scope != "input" || got[0].Rule != "*" {
		t.Fatalf("input:* 应照常接受，实际 %+v", got)
	}
	// 前提仍在场：output scope 的三种写法不受 egress 限制影响。
	if _, err := ParseHardBlockAllows("output:*,output:secret.*,output:builtin.pii.email"); err != nil {
		t.Fatalf("output scope 不应受 egress 限制影响: %v", err)
	}
}

// TestR1039InvalidEgressEntryKeepsDefaultAllowlist 钉住「配错不静默放开」。
//
// `egress:*` 被拒 ⇒ 整张表不装载 ⇒ 进程保持出厂默认（只有 input 可阻断）。
// 这正是本次要的效果：分类器规则不会因为运维写了一个通配就变成阻断规则，
// 同时请求侧的 fail-closed 底线**不受影响**。
func TestR1039InvalidEgressEntryKeepsDefaultAllowlist(t *testing.T) {
	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })
	SetHardBlockAllows([]HardBlockAllow{{Scope: "input", Rule: "*"}})
	t.Setenv(HardBlockAllowEnv, "egress:*")

	if _, err := LoadHardBlockAllowsFromEnv(); err == nil {
		t.Fatal("egress:* 应被解析层拒绝")
	}
	// 前提仍在场：清单保持出厂默认，egress 仍不可阻断，input 仍 fail-closed。
	if HardBlockAllowed("egress", "pii.email.egress") {
		t.Error("被拒的清单生效了：egress scope 不该允许分类器规则阻断")
	}
	if !HardBlockAllowed("input", "pii.server_ip") {
		t.Error("请求侧的 fail-closed 底线被这次拒绝连带关掉了")
	}
}

// TestR1039ValidateHardBlockAllowMatchesParser 钉住「校验只有一个入口」。
//
// 解析层与将来的管理面都走 ValidateHardBlockAllow；两处口径漂移就会出现
// 「配置说不行、运行时却行」的缝。
func TestR1039ValidateHardBlockAllowMatchesParser(t *testing.T) {
	cases := []struct {
		scope, rule string
		wantErr     bool
	}{
		{"egress", "*", true},
		{"egress", "secret.*", true},
		{"egress", "pii.email.egress", true},
		{"egress", "builtin.egress_sink", false},
		{"input", "*", false},
		{"output", "*", false},
		{"output", "secret.*", false},
	}
	for _, tc := range cases {
		err := ValidateHardBlockAllow(tc.scope, tc.rule)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateHardBlockAllow(%q, %q) err = %v, wantErr = %v", tc.scope, tc.rule, err, tc.wantErr)
		}
	}
	// 前提仍在场：解析层确实会调用同一个校验（而不是各写一套）。
	if _, err := ParseHardBlockAllows("egress:builtin.egress_sink"); err != nil {
		t.Fatalf("解析层应接受合法项: %v", err)
	}
}

// TestR1039DefaultAllowlistIsUnchanged 钉住「出厂默认没被动过」。
//
// D5 是纯收窄：默认档位（只有 input 可阻断）必须原样。
func TestR1039DefaultAllowlistIsUnchanged(t *testing.T) {
	if len(r1039InitialAllowlist) != 1 ||
		r1039InitialAllowlist[0].Scope != "input" ||
		r1039InitialAllowlist[0].Rule != "*" {
		t.Fatalf("出厂默认应仍是 {input:*}，实际 %+v", r1039InitialAllowlist)
	}

	prev := HardBlockAllows()
	t.Cleanup(func() { SetHardBlockAllows(prev) })
	SetHardBlockAllows([]HardBlockAllow{{Scope: "input", Rule: "*"}})
	os.Unsetenv(HardBlockAllowEnv)

	allows, err := LoadHardBlockAllowsFromEnv()
	if err != nil {
		t.Fatalf("LoadHardBlockAllowsFromEnv: %v", err)
	}
	if len(allows) != 1 || allows[0].Scope != "input" || allows[0].Rule != "*" {
		t.Fatalf("出厂默认应仍是 {input:*}，实际 %+v", allows)
	}
}
