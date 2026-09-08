package freediscovery

import "testing"

func TestToSChecker_FreeMarkingIsOK(t *testing.T) {
	c := NewToSChecker()
	tpl := &ProviderTemplate{ProviderCode: "openrouter"}
	verdict, notes := c.Check(tpl, "gemma-4-31b-it:free")
	if verdict != "ok" {
		t.Fatalf(":free marking should be ok, got %q (%s)", verdict, notes)
	}
}

func TestToSChecker_NoRuleProviderIsUnknown(t *testing.T) {
	c := NewToSChecker()
	verdict, _ := c.Check(&ProviderTemplate{ProviderCode: "no-such-provider"}, "some-model")
	if verdict != "unknown" {
		t.Fatalf("provider without ToS rule must be unknown (not reviewed), got %q", verdict)
	}
}

func TestToSChecker_CautionKeyword(t *testing.T) {
	c := NewToSChecker()
	verdict, notes := c.Check(&ProviderTemplate{ProviderCode: "openrouter"}, "vision-preview:free")
	if verdict != "caution" {
		t.Fatalf("caution keyword must downgrade, got %q (%s)", verdict, notes)
	}
}

func TestToSChecker_AvoidKeywordWins(t *testing.T) {
	c := NewToSChecker()
	// avoid 关键词优先级最高, 即便模板是 ok
	verdict, _ := c.Check(&ProviderTemplate{ProviderCode: "groq", TosVerdict: "ok"}, "deprecated-model:free")
	if verdict != "avoid" {
		t.Fatalf("avoid keyword must win, got %q", verdict)
	}
}

func TestToSChecker_TemplateVerdictPriority(t *testing.T) {
	c := NewToSChecker()
	// 模板级 caution 覆盖提供商预设
	verdict, notes := c.Check(&ProviderTemplate{
		ProviderCode: "openrouter", TosVerdict: "caution", TosNotes: "reviewed by admin",
	}, "unmarked-model-x")
	if verdict != "caution" || notes != "reviewed by admin" {
		t.Fatalf("template verdict must win: %q / %q", verdict, notes)
	}
}

func TestToSChecker_ProviderVerdictFallback(t *testing.T) {
	c := NewToSChecker()
	// groq 预设是 caution: 无关键词命中时走提供商级兜底
	verdict, _ := c.Check(&ProviderTemplate{ProviderCode: "groq"}, "llama-3.1-8b-instant")
	if verdict != "caution" {
		t.Fatalf("provider-level fallback verdict expected, got %q", verdict)
	}
}

func TestToSChecker_FreeMarkingDowngradedByProviderCaution(t *testing.T) {
	c := NewToSChecker()
	// 提供商级 avoid 时, 即使模型带 :free 也不能给 ok
	c.rules["evil-provider"] = &ToSRule{
		ProviderCode:    "evil-provider",
		AllowKeywords:   []string{":free"},
		ProviderVerdict: "avoid",
		Notes:           "provider bans proxying",
	}
	verdict, notes := c.Check(&ProviderTemplate{ProviderCode: "evil-provider"}, "model:free")
	if verdict != "avoid" {
		t.Fatalf("provider avoid must dominate :free marking, got %q (%s)", verdict, notes)
	}
}

func TestToSChecker_TemplateOKUpgradedToCautionByKeyword(t *testing.T) {
	c := NewToSChecker()
	verdict, notes := c.Check(&ProviderTemplate{
		ProviderCode: "openrouter", TosVerdict: "ok", TosNotes: "provider reviewed",
	}, "beta-experimental-model")
	if verdict != "caution" {
		t.Fatalf("caution keyword must downgrade template ok, got %q (%s)", verdict, notes)
	}
}

func TestToSChecker_NilTemplate(t *testing.T) {
	c := NewToSChecker()
	verdict, _ := c.Check(nil, "model:free")
	if verdict == "" {
		t.Fatal("nil template must not panic and must return a verdict")
	}
}
