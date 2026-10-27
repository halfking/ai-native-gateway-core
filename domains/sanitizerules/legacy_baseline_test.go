package sanitizerules

// legacy_baseline_test.go — legacy `pii_patterns` 导入形状门禁。
//
// r1038（2026-10-20）：851 迁移把 `pii_patterns.ip_address` 那行**停用**了，
// 但那是**数据**修复：
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
//
// r1038b（2026-10-27）：本文件随函数一起从 package main 下沉到
// domains/sanitizerules —— 让**判据与生产调同一个函数**，不再手抄映射。

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
			rule := LegacyPIIRule(11, "legacy", tc.patternTyp, `x`, true)
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

// TestR1038bLegacyDeclaredAddressTypesAreNotDowngradedToCustom 钉住
// 「声明出来的类型不许被丢掉」。
//
// 背景（r1038b 取证）：legacy 映射里的 `default: typ = "custom"` 会把**所有
// 未知类型**降级成 `custom`。而脱敏面的地址族豁免是**按类型**判定的
// （security/sanitize 的 scopedType → 本包的 IsAddressFamilyType），
// `custom` 不在名单里 ⇒ 一条 `pattern_type='domain'` 的行会被降级成 custom
// ⇒ 在脱敏面**仍然遮蔽域名**，与用户口径「脱敏的范围应该不包含域名、地址、
// ip」相违。
//
// 生产上当前没有 domain 类型的行（`pii_patterns` 实测 12 行），所以这在
// r1038b 之前是**潜在**缺口。但缺口的位置很危险：它由一行 `default` 兜底，
// 而 `default` 的语义是「不认识的一律当最普通的自定义规则」——于是**运维声明
// 的类型被静默丢弃**，且没有任何判据会报警。
//
// 这条判据要求：声明为地址族（domain / hostname / address）的行**不得**被
// 降级成 custom ⇒ 必须映射进地址族名单。
//
// ★ 变异验证：把 `case "domain", "hostname", "address":` 这一支删掉
//
//	（落回 default → custom）后本判据转红。
func TestR1038bLegacyDeclaredAddressTypesAreNotDowngradedToCustom(t *testing.T) {
	for _, typ := range []string{"domain", "hostname", "address"} {
		t.Run(typ, func(t *testing.T) {
			rule := LegacyPIIRule(99, "declared-"+typ, typ, `x`, true)
			if rule.Type == "custom" {
				t.Fatalf("声明类型 %q 被降级成 custom ⇒ 脱敏面会遮蔽域名/URL（用户口径：脱敏范围不含域名、地址、ip）", typ)
			}
			if !IsAddressFamilyType(rule.Type) {
				t.Fatalf("映射后的类型 %q 不在地址族名单里 ⇒ 脱敏面不会放行", rule.Type)
			}
			if rule.Action != "log" {
				t.Errorf("action = %q，期望 log（地址族只记录不遮蔽）", rule.Action)
			}
		})
	}
}

// TestR1038bLegacyURLTypeStaysMaskable 是上面那条的**反向对照**。
//
// `url` 故意**不在**地址族名单里：URL 能携带凭据（`?token=…` /
// `/user/<secret>`）。把「URL」整类从遮蔽面豁免，等于给查询串里的令牌
// 开后门 —— 那比「域名被遮蔽」严重得多。
//
// 这条判据的作用是**防止有人把 url 也加进豁免名单**（那看起来像是在
// 「兑现用户口径」，实际是开安全后门）。改名单之前必须先答这条判据。
func TestR1038bLegacyURLTypeStaysMaskable(t *testing.T) {
	rule := LegacyPIIRule(98, "url_pattern", "url", `x`, true)
	if IsAddressFamilyType(rule.Type) {
		t.Fatalf("url 被当成地址族（%q）⇒ URL 里的凭据（?token=…）会被放行", rule.Type)
	}
	if rule.Action != "mask" {
		t.Errorf("action = %q，期望 mask（URL 必须仍可遮蔽）", rule.Action)
	}
}

// TestR1038bLegacyProducedRulesMustCompile 是「**产出的规则必须装载得上**」
// 这道闸。
//
// 为什么需要它：r1038b 的审计里，我把「声明为 domain 的行」映射成了一个
// **新造的 `domain` 类型** —— 而规则类型词表是**封闭的**（`Compile` 只接受
// phone/id_card/email/credit_card/secret/internal_ip/server_ip/account/
// name/custom 这 10 个）⇒ `NewManager` 直接拒绝**整份配置**
// （`rules[0].type must be a restorable sensitive type`）。
//
// 也就是说：那个修法**会让整个脱敏规则集装载不起来** —— 一个比原缺陷严重得
// 多的故障。它是被 r1038b 的新判据当场抓住的（而不是上线后才发现）。
//
// 这条判据把「装载得上」钉成契约：legacy 映射对**任何**输入类型产出的规则，
// 都必须能被 `Compile` 接受。
//
// ★ 变异验证：把映射改成产出 `"domain"`（或任何词表外的类型）⇒ 本判据转红。
func TestR1038bLegacyProducedRulesMustCompile(t *testing.T) {
	types := []string{
		"email", "phone", "id_card", "credit_card", "secret", "account", "name",
		"internal_ip", "server_ip", "custom",
		"ip", "ip_address", "ipv6", "bank_card",
		"domain", "hostname", "address",
		"url", "ssn", "some_type_nobody_declared",
	}
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			rule := LegacyPIIRule(1, "produced", typ, `\d+`, true)
			cfg := Config{Rules: []Rule{rule}}
			if _, err := Compile(cfg, 0); err != nil {
				t.Fatalf("legacy 映射对 %q 产出的规则装载不上（会让整份规则集失效）: %v", typ, err)
			}
		})
	}
}

// TestR1038bAddressFamilyListIsSharedByBothFaces 钉住「一份名单」。
//
// r1038b 之前有两份名单（security/sanitize 的 scopedType 与
// outputcompliance 的 isAddressPIIType）且**已经漂移**。现在两面都读
// domains/sanitizerules 的 IsAddressFamilyType。
//
// 这条判据的作用不是「再抄一遍名单」（那会重新引入漂移），而是钉住
// **名单里不该有的东西**：url / custom 一旦混进去，就是安全后门。
func TestR1038bAddressFamilyListIsSharedByBothFaces(t *testing.T) {
	for _, forbidden := range []string{"url", "custom", "secret", "account", "email", "phone"} {
		if IsAddressFamilyType(forbidden) {
			t.Errorf("%q 被列进地址族名单 ⇒ 会被脱敏面放行（安全后门）", forbidden)
		}
	}
	for _, required := range []string{"ip", "ip_address", "ipv6", "server_ip", "server_ipv6", "internal_ip", "domain", "hostname", "address"} {
		if !IsAddressFamilyType(required) {
			t.Errorf("%q 不在地址族名单里 ⇒ 脱敏面会遮蔽它（与用户口径相违）", required)
		}
	}
}
