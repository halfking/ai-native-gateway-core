package sanitizerules

import "testing"

// ★ 规则说明目录（2026-10-29 输出合规误报轮 · 「规则说明太简单」）。
//
// ★ 判据：★ ① 登记的说明能被取到；★ ② 补齐只补**缺省**的（★ 快照里人工写
//   过的说明不许被覆盖）；★ ③ 没有说明的规则**保持空**（★ 不许编一句假说明）★★

func TestRuleDescriptionCatalog(t *testing.T) {
	const ruleID = "test.catalog.rule"
	const desc = "这是测试用的一条规则说明。"
	RegisterRuleDescription(ruleID, desc)

	got, ok := RuleDescription(ruleID)
	if !ok || got != desc {
		t.Fatalf("取说明失败: %q ok=%v", got, ok)
	}
	if _, ok := RuleDescription("test.catalog.missing"); ok {
		t.Fatal("没登记过的规则不该返回说明")
	}
}

func TestApplyRuleDescriptionsFillsMissingOnly(t *testing.T) {
	const ruleID = "test.apply.rule"
	RegisterRuleDescription(ruleID, "目录里的说明")

	cfg := Config{Rules: []Rule{
		{ID: ruleID}, // 没有说明 ⇒ 补
		{ID: ruleID, Description: "快照里的说明"}, // 已有 ⇒ 保持
		{ID: "test.apply.unknown"},          // 没有 ⇒ 保持空
	}}
	out := ApplyRuleDescriptions(cfg)
	if out.Rules[0].Description != "目录里的说明" {
		t.Fatalf("缺省的说明没补上: %q", out.Rules[0].Description)
	}
	if out.Rules[1].Description != "快照里的说明" {
		t.Fatalf("已有的说明被覆盖了: %q", out.Rules[1].Description)
	}
	if out.Rules[2].Description != "" {
		t.Fatalf("没有说明的规则不该被编一句: %q", out.Rules[2].Description)
	}
	// ★ 入参不被就地改写（★ 调用方的配置保持原样）。
	if cfg.Rules[0].Description != "" {
		t.Fatalf("入参被就地改写了: %q", cfg.Rules[0].Description)
	}
}

// ★ 内建规则目录的说明必须能被取到（★ 合成规则的说明在目录里）。
func TestBuiltinRuleDescriptionsAvailable(t *testing.T) {
	for _, id := range []string{"builtin.egress_sink", "builtin.network_destination", "builtin.secret_field.egress"} {
		desc, ok := RuleDescription(id)
		if !ok || desc == "" {
			t.Fatalf("内建规则 %s 没有说明", id)
		}
	}
}
