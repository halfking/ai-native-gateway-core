package caveman

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAllEmbeddedRulesCompile 是 GW-07 最核心门禁：证明嵌入的全部 8 语言包
// 规则在 Go RE2 下编译通过（无残留 lookbehind / backreference）。
// 任何 pattern 编译失败 → 立即失败，阻塞合入。
func TestAllEmbeddedRulesCompile(t *testing.T) {
	t.Parallel()
	langs := availableLanguages()
	if len(langs) != 8 {
		t.Fatalf("expected 8 language packs, got %d: %v", len(langs), langs)
	}
	total := 0
	for _, lang := range langs {
		files := languageFiles(lang)
		if len(files) == 0 {
			t.Errorf("language %s has no rule files", lang)
			continue
		}
		for fname, raw := range files {
			var pack rulePack
			if err := json.Unmarshal(raw, &pack); err != nil {
				t.Errorf("%s/%s: unmarshal: %v", lang, fname, err)
				continue
			}
			if err := validateRulePack(pack); err != nil {
				t.Errorf("%s/%s: validate: %v", lang, fname, err)
				continue
			}
			for _, fr := range pack.Rules {
				r, err := compileRule(fr, lang+"/"+fname)
				if err != nil {
					t.Errorf("%s/%s:%s compile: %v", lang, fname, fr.Name, err)
					continue
				}
				if r.Pattern == nil {
					t.Errorf("%s/%s:%s: nil pattern", lang, fname, fr.Name)
				}
				total++
			}
		}
	}
	if total < 300 {
		t.Errorf("expected >=300 rules total, got %d", total)
	}
	t.Logf("compiled %d rules across %d languages", total, len(langs))
}

// TestNoLookbehindInEmbeddedPatterns 断言嵌入的 JSON pattern 里无 lookbehind。
// 这是 RE2 兼容性的直接检查（除 _note 注释外的 pattern 字段）。
func TestNoLookbehindInEmbeddedPatterns(t *testing.T) {
	t.Parallel()
	for _, lang := range availableLanguages() {
		for fname, raw := range languageFiles(lang) {
			var pack rulePack
			if err := json.Unmarshal(raw, &pack); err != nil {
				continue
			}
			for _, fr := range pack.Rules {
				if strings.Contains(fr.Pattern, "(?<") {
					t.Errorf("%s/%s:%s pattern still has lookbehind/lookahead-zero-width(?<: %s",
						lang, fname, fr.Name, fr.Pattern)
				}
			}
		}
	}
}

// TestEnPleasantriesRewrite 验证 en/filler.json pleasantries 的 RE2 改写正确。
// TS 语义：(?<!make\s)(?<!be\s) 前面不是 make/be 才删除。
// 改写后：(make\s+|be\s+)? 捕获组1 + guardGroup，前有 make/be 时保留。
func TestEnPleasantriesRewrite(t *testing.T) {
	files := languageFiles("en")
	raw, ok := files["filler.json"]
	if !ok {
		t.Fatal("en/filler.json not found")
	}
	var pack rulePack
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	var pleasantries *fileRule
	for i := range pack.Rules {
		if pack.Rules[i].Name == "pleasantries" {
			pleasantries = &pack.Rules[i]
			break
		}
	}
	if pleasantries == nil {
		t.Fatal("pleasantries rule not found")
	}
	if pleasantries.GuardGroup != 1 {
		t.Errorf("pleasantries guardGroup = %d, want 1", pleasantries.GuardGroup)
	}
	r, err := compileRule(*pleasantries, "en/filler")
	if err != nil {
		t.Fatal(err)
	}
	// "make sure you" → 前有 make，保留（不删 sure）。
	out := r.Pattern.ReplaceAllStringFunc("make sure you", r.Replace)
	if !strings.Contains(strings.ToLower(out), "make") {
		t.Errorf("pleasantries should preserve 'make sure' (lookbehind guard); got %q", out)
	}
	// 单独 "sure" → 删除。
	out2 := r.Pattern.ReplaceAllStringFunc("yes sure thing", r.Replace)
	if strings.Contains(strings.ToLower(out2), "sure") {
		t.Errorf("pleasantries should drop standalone 'sure'; got %q", out2)
	}
}

// TestEnFillerAdverbsRewrite 验证 filler_adverbs 的 RE2 改写。
// TS 语义：(?<![a-z]) 前面不是小写字母才删。
func TestEnFillerAdverbsRewrite(t *testing.T) {
	files := languageFiles("en")
	var pack rulePack
	if err := json.Unmarshal(files["filler.json"], &pack); err != nil {
		t.Fatal(err)
	}
	var fr *fileRule
	for i := range pack.Rules {
		if pack.Rules[i].Name == "filler_adverbs" {
			fr = &pack.Rules[i]
			break
		}
	}
	if fr == nil {
		t.Fatal("filler_adverbs not found")
	}
	if fr.GuardGroup != 1 {
		t.Errorf("filler_adverbs guardGroup = %d, want 1", fr.GuardGroup)
	}
	r, err := compileRule(*fr, "en/filler")
	if err != nil {
		t.Fatal(err)
	}
	// "basically" 句首 → 删除。
	out := r.Pattern.ReplaceAllStringFunc("Basically it works", r.Replace)
	if strings.Contains(strings.ToLower(out), "basically") {
		t.Errorf("filler_adverbs should drop sentence-start 'Basically'; got %q", out)
	}
}

// TestLoadAllRulesForLanguage_AllLangs 验证 8 语言都能加载且规则数合理。
func TestLoadAllRulesForLanguage_AllLangs(t *testing.T) {
	t.Parallel()
	minExpected := map[string]int{
		"en": 40, "es": 40, "pt-BR": 40, "id": 45,
		"de": 25, "fr": 25, "ja": 25, "zh": 15,
	}
	for _, lang := range availableLanguages() {
		rules := LoadAllRulesForLanguage(lang)
		if len(rules) < minExpected[lang] {
			t.Errorf("language %s: got %d rules, want >= %d", lang, len(rules), minExpected[lang])
		}
	}
}

// TestGetRulesForContext 验证 role/intensity/skip 过滤。
func TestGetRulesForContext(t *testing.T) {
	enRules := LoadAllRulesForLanguage("en")
	skip := map[string]bool{"pleasantries": true}
	userLite := getRulesForContext("user", IntensityLite, "en", enRules, skip)
	for _, r := range userLite {
		if r.Name == "pleasantries" {
			t.Error("pleasantries should be skipped")
		}
		if r.Context != CtxAll && r.Context != CtxUser {
			t.Errorf("rule %s has context %s, should be all/user", r.Name, r.Context)
		}
		if intensityRank(r.MinIntensity) > intensityRank(IntensityLite) {
			t.Errorf("rule %s minIntensity %s > lite", r.Name, r.MinIntensity)
		}
	}
	// ultra 强度应比 lite 多包含规则。
	userUltra := getRulesForContext("user", IntensityUltra, "en", enRules, nil)
	if len(userUltra) <= len(userLite) {
		t.Errorf("ultra (%d) should include more rules than lite (%d)", len(userUltra), len(userLite))
	}
}

// TestReplacementMapNormalization 验证 replacementMap key 归一化。
func TestReplacementMapNormalization(t *testing.T) {
	fn := mapReplace(map[string]string{"  Hello   World  ": "hi"}, "")
	// match "  Hello   World  " 归一化后 = "hello world"，应命中。
	if got := fn("Hello World"); got != "hi" {
		t.Errorf("normalized lookup failed: got %q, want %q", got, "hi")
	}
	// miss → 返回原 match（fallback 空）。
	if got := fn("unknown"); got != "unknown" {
		t.Errorf("miss should return original match: got %q", got)
	}
}

// TestValidateRulePack 验证 schema 校验的负向 case。
func TestValidateRulePack(t *testing.T) {
	cases := []struct {
		name string
		pack rulePack
	}{
		{"empty language", rulePack{Language: "", Category: "x", Rules: []fileRule{}}},
		{"empty category", rulePack{Language: "en", Category: "", Rules: []fileRule{}}},
		{"nil rules", rulePack{Language: "en", Category: "x"}},
		{"empty name", rulePack{Language: "en", Category: "x", Rules: []fileRule{{Name: "", Pattern: "x"}}}},
		{"empty pattern", rulePack{Language: "en", Category: "x", Rules: []fileRule{{Name: "r", Pattern: ""}}}},
		{"invalid pattern", rulePack{Language: "en", Category: "x", Rules: []fileRule{{Name: "r", Pattern: "("}}}},
		{"replacementMap empty key", rulePack{Language: "en", Category: "x", Rules: []fileRule{{Name: "r", Pattern: "x", ReplacementMap: map[string]string{"": "v"}}}}},
		{"bad context", rulePack{Language: "en", Category: "x", Rules: []fileRule{{Name: "r", Pattern: "x", Context: "bogus"}}}},
		{"bad intensity", rulePack{Language: "en", Category: "x", Rules: []fileRule{{Name: "r", Pattern: "x", MinIntensity: "bogus"}}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRulePack(tc.pack); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}
