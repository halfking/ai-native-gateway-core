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
