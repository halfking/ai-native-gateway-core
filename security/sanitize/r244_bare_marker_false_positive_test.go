package sanitize

import (
	"context"
	"strings"
	"testing"
)

// 2026-10-10 — 新增的裸形态规则（loosePlaceholderSpanPattern 的无花括号
// 分支、grammar 的可选索引）在真实生产流量上的误伤审计。
//
// 这些形态看起来像散文但结构上和 marker 一致，是最可能误伤的地方：
//   - 代码片段：grep -n 'sensitive: 12345' file.txt
//   - CSS/选择器：a.sensitive-phone
//   - 自然语言：the sensitive data was returned
//   - 工具参数 JSON：走 .arguments lane，重写时还要做 JSON 转义
//
// 生产抽样审计（154，response_preview 1920 条 + request_preview 400 条）
// 结果为零误伤：命中的 span 全部是真 marker。以下用例把该结论固化为
// 可复跑的回归门。

func TestBareFormRulesAgainstRealisticNonMarkers(t *testing.T) {
	// 这些必须在任何 lane 上都不被解析成 marker。
	nonMarkers := []struct {
		why  string
		text string
	}{
		{"shell grep over prose", `grep -n 'sensitive: 12345' file.txt`},
		{"css class selector", `a.sensitive-phone`},
		{"prose noun phrase", "the sensitive data was returned"},
		{"prose adjective", "this is not sensitive"},
		{"prose before colon", "most sensitive: phone number"},
		{"digits after colon only", "sensitive:12345"},
		{"type name alone", "server_ip is a type name"},
		{"tool arg json", `{"path":"/tmp/a.txt","count":3}`},
		{"nested tool arg json", `{"cfg":{"retries":5},"tags":["a","b"]}`},
	}
	for _, c := range nonMarkers {
		t.Run(c.why, func(t *testing.T) {
			for _, sp := range loosePlaceholderSpanPattern.FindAllString(c.text, -1) {
				if typ, idx, ok := parseLoosePlaceholder(sp); ok {
					t.Errorf("%q parsed as marker %q index=%d from %q",
						sp, typ, idx, c.text)
				}
			}
		})
	}
}

// TestBareFormRulesCoverFullWidthSpellings 是 2026-10-21 轮补的口径扩展。
//
// ## 为什么必须单独一条
//
// 本文件上面的用例全部用**裸正则**（loosePlaceholderSpanPattern）扫描，而那个
// 正则的裸形态分支只认 ASCII 标记词、ASCII 冒号、ASCII 类型 ⇒ **全角拼写
// 在它眼里不存在**。
//
// 而生产扫描（looseMarkerSpans）会在**折宽副本**上再扫一遍，因此看得见全角。
// 2026-10-21 实测差异：
//
//	`ＳＥＮＳＩＴＩＶＥ：phone`   裸正则 0 个 span   生产扫描 1 个 span
//
// ⇒ 「用裸正则写判据」会**看不见生产看得见的东西** ⇒ 这道门看着绿，线上漏。
// 本条改用**生产扫描路径**来断言，并同时覆盖两个方向：
//   - 全角 marker 形态必须被认出来（正向）；
//   - 全角散文不得被认出来（反向，防止把普通中文拖进遮蔽）。
func TestBareFormRulesCoverFullWidthSpellings(t *testing.T) {
	markers := []struct {
		why  string
		text string
	}{
		{"full width word + full width colon", "ＳＥＮＳＩＴＩＶＥ：phone"},
		{"full width word + ascii colon", "ＳＥＮＳＩＴＩＶＥ:phone"},
		{"ascii word + full width colon", "SENSITIVE：phone"},
		{"full width braces", "｛SENSITIVE：phone：1｝"},
		{"full width type and index", "ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１"},
	}
	for _, c := range markers {
		t.Run(c.why, func(t *testing.T) {
			parsed := 0
			for _, sp := range looseMarkerSpans(c.text) {
				candidate := c.text[sp[0]:sp[1]]
				if _, _, ok := parseLoosePlaceholder(candidate); ok {
					parsed++
				}
			}
			if parsed == 0 {
				t.Errorf("生产扫描路径没认出这个全角 marker 形态: %q", c.text)
			}
			// 反向：它必须是**遮蔽**而不是原样穿过（端到端）。
			s, err := NewSanitizer(NewPatternDetector())
			if err != nil {
				t.Fatalf("NewSanitizer: %v", err)
			}
			out := s.restoreText(context.Background(), c.text, SanitizeMap{}.resolverFor(), looseMaskUnknown)
			if out == c.text {
				t.Errorf("全角 marker 原样穿过到客户端: %q", c.text)
			}
		})
	}

	prose := []struct {
		why  string
		text string
	}{
		{"full width chinese prose", "这是敏感数据，请妥善保管"},
		{"full width field label", "敏感字段：手机号"},
		{"full width punctuation", "全角标点，：（）；！？"},
		{"full width latin not a marker", "ＡＢＣ：ＡＢＣ"},
		{"full width digits", "联系电话：１３８００１３８００"},
	}
	for _, c := range prose {
		t.Run(c.why, func(t *testing.T) {
			if spans := looseMarkerSpans(c.text); len(spans) != 0 {
				t.Errorf("全角散文被当成 marker: %q -> %q", c.text, c.text[spans[0][0]:spans[0][1]])
			}
			s, err := NewSanitizer(NewPatternDetector())
			if err != nil {
				t.Fatalf("NewSanitizer: %v", err)
			}
			out := s.restoreText(context.Background(), c.text, SanitizeMap{}.resolverFor(), looseMaskUnknown)
			if out != c.text {
				t.Errorf("全角散文被误遮蔽: %q -> %q", c.text, out)
			}
		})
	}
}

// TestBareFormFalsePositiveRuleAcceptsEnumeratedMarkerForms 钉住误报规则的重铸。
//
// 2026-10-21 实测：原规则「无花括号且无索引 ⇒ 误报」会把**实测存在的**枚举
// 形态（154：模型成串列举类型）判成误报，而同一包里
// r244_bare_marker_form_test.go / r244_bare_marker_false_positive_test.go 都
// 把它当作真 marker ⇒ 同一形态、两条判据、相反结论。
//
// 新规则：裸形态的 type 必须是已知敏感类型。新规则比旧规则**更弱**（判得更
// 少），因此不可能让既有生产语料变红；同时它仍然抓得住散文里的
// `sensitive:foo`（未知类型）。
func TestBareFormFalsePositiveRuleAcceptsEnumeratedMarkerForms(t *testing.T) {
	enumerated := "（SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card）"
	for _, sp := range looseMarkerSpans(enumerated) {
		candidate := enumerated[sp[0]:sp[1]]
		typ, idx, ok := parseLoosePlaceholder(candidate)
		if !ok {
			t.Fatalf("枚举形态里的 %q 应当解析成 marker", candidate)
		}
		if r244IsFalsePositive(r244Span{text: candidate, typ: typ, idx: idx}) {
			t.Errorf("枚举形态里的真 marker 被误报规则判成散文: %q (type=%q idx=%d)", candidate, typ, idx)
		}
	}

	// 反向对照：未知类型的裸形态仍是误报（散文被抓得住）。
	prose := "the sensitive:foo value was returned"
	caught := false
	for _, sp := range looseMarkerSpans(prose) {
		candidate := prose[sp[0]:sp[1]]
		typ, idx, ok := parseLoosePlaceholder(candidate)
		if !ok {
			continue
		}
		if r244IsFalsePositive(r244Span{text: candidate, typ: typ, idx: idx}) {
			caught = true
		}
	}
	if !caught {
		t.Errorf("未知类型的裸形态（散文）必须仍被判成误报: %q", prose)
	}
}

func TestBareFormRulesStillCatchRealMarkers(t *testing.T) {
	// 工具参数 lane：裸形态嵌在 JSON 里。此时 span pattern 的花括号分支会
	// 整段捕获该 JSON，restoreLooseMarkers 的内部重扫（专用裸形态 pattern）
	// 才会看到里面的 marker。测的是真正生效的那条路径。
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	out := s.restoreText(context.Background(), `{"phone":"SENSITIVE:phone"}`,
		SanitizeMap{}.resolverFor(), looseMaskUnknown)
	if strings.Contains(out, "SENSITIVE:phone") {
		t.Errorf("bare marker inside tool-argument JSON survived: %s", out)
	}
	if !strings.Contains(out, "[REDACTED") {
		t.Errorf("expected a redaction, got: %s", out)
	}
	if !strings.Contains(out, `"phone"`) {
		t.Errorf("surrounding JSON must survive the re-scan: %s", out)
	}

	// 同样要盖住带花括号的形态，确保重扫没有破坏原有能力。
	braced := s.restoreText(context.Background(), `{"phone":"{SENSITIVE:phone:1}"}`,
		SanitizeMap{}.resolverFor(), looseMaskUnknown)
	if strings.Contains(braced, "SENSITIVE:phone") {
		t.Errorf("braced marker inside JSON survived: %s", braced)
	}

	// 反向：真实观测到的形态必须仍然命中，否则上一组全是绿的也没意义。
	markers := []struct {
		why  string
		text string
	}{
		{"enumerated types", "（SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card）"},
		{"bare with index", "SENSITIVE:server_ip:1"},
	}
	for _, c := range markers {
		t.Run(c.why, func(t *testing.T) {
			parsed := 0
			for _, sp := range loosePlaceholderSpanPattern.FindAllString(c.text, -1) {
				if _, _, ok := parseLoosePlaceholder(sp); ok {
					parsed++
				}
			}
			if parsed == 0 {
				t.Errorf("no marker parsed from %q", c.text)
			}
		})
	}
}
