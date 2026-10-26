package main

// r1038_legacy_pii_shape_test.go — 2026-10-20 legacy pii_patterns 导入形状门禁。
//
// 851 迁移把 `pii_patterns.ip_address` 那行**停用**了，但那是**数据**修复：
//
//   - 基线快照不可变。851 之前发布过基线的部署，拿到的快照里那行仍然是
//     enabled —— 遮蔽会一直存在，直到有人手工重发一版规则；
//   - 运维随时可以把它改回 enabled。
//
// 所以代码侧必须自己站住：legacy 导入造出来的地址族规则必须是 **log**
// （只记录、不遮蔽），而不是 mask。
//
// 这条门禁存在的意义是「数据修复失效时唯一的兜底」——它一旦被改回 mask，
// 没有任何其它判据会报警（已发布快照的形状是运行期数据，测试看不见）。

import "testing"

func TestR1038LegacyPIIRuleMarksAddressFamilyAsLog(t *testing.T) {
	for _, tc := range []struct {
		name       string
		patternTyp string
		wantType   string
		wantAction string
	}{
		{"legacy ip_address 行", "ip", "server_ip", "log"},
		{"legacy ip 别名", "ip_address", "server_ip", "log"},
		{"legacy ipv6", "ipv6", "server_ip", "log"},
		{"legacy internal_ip", "internal_ip", "internal_ip", "log"},
		{"legacy email 仍遮蔽", "email", "email", "mask"},
		{"legacy phone 仍遮蔽", "phone", "phone", "mask"},
		{"legacy bank_card 仍遮蔽", "bank_card", "credit_card", "mask"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := legacyPIIRule(11, "legacy", tc.patternTyp, `x`, true)
			if rule.Type != tc.wantType {
				t.Errorf("类型 = %q，期望 %q", rule.Type, tc.wantType)
			}
			if rule.Action != tc.wantAction {
				t.Errorf("action = %q，期望 %q（地址族必须只记录不遮蔽）", rule.Action, tc.wantAction)
			}
			if rule.Enabled != true {
				t.Error("enabled 被改写")
			}
			if rule.Scopes == nil || len(rule.Scopes) != 1 || rule.Scopes[0] != "output" {
				t.Errorf("scopes = %v，期望 [output]", rule.Scopes)
			}
		})
	}
}
