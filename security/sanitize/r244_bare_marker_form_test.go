package sanitize

import "testing"

// 2026-10-10 — 模型剥掉占位符花括号后的裸形态泄漏。
//
// 实测（154 生产，minimax-m3）：非流式响应里模型把占位符改写成
// "SENSITIVE:phone"（无花括号、无索引），有时还成串列举
// "（SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card）"。
// loosePlaceholderSpanPattern 的花括号分支匹配不到这种形态，于是内部
// token 原样到达客户端。
//
// 这是结构改写而非语义改写，与本包已支持的 "{sensitive:…}" 小写化同类，
// 所以在容错阶段的职责范围内。两条改动共同生效：
//   - span pattern 增加裸形态分支；
//   - grammar 的索引改为可选，缺失时 index = -1（既有语义：无法解析成
//     映射行，mask 模式下被遮蔽而不是放行）。
//
// 散文不被误伤同样是要钉住的边界 —— "sensitive data"、"sensitive:12345"、
// "the sensitive field" 都不含 "冒号 + 类型标识符"，因此不匹配。

func TestLoosePlaceholderSpanMatchesBareForm(t *testing.T) {
	// 实测形态
	bare := []string{
		"SENSITIVE:phone",
		"SENSITIVE:email",
		"SENSITIVE:id_card",
		"SENSITIVE:server_ip:1",
		"tags: SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card",
		"标注（SENSITIVE:phone）后继续",
	}
	for _, text := range bare {
		t.Run(text, func(t *testing.T) {
			spans := loosePlaceholderSpanPattern.FindAllString(text, -1)
			if len(spans) == 0 {
				t.Fatalf("no span extracted from %q", text)
			}
			for _, sp := range spans {
				if _, _, ok := parseLoosePlaceholder(sp); !ok {
					t.Errorf("span %q from %q did not parse as marker syntax", sp, text)
				}
			}
		})
	}
}

func TestLoosePlaceholderSpanIgnoresProse(t *testing.T) {
	prose := []string{
		"the sensitive data was returned",
		"sensitive:12345",
		"this is not sensitive",
		"user data is sensitive here",
		"most sensitive: phone number",
		"server_ip is a type name",
		`JSON output like {"a": 1} is fine`,
		"code: for i in range(10): pass",
	}
	for _, text := range prose {
		t.Run(text, func(t *testing.T) {
			for _, sp := range loosePlaceholderSpanPattern.FindAllString(text, -1) {
				if _, _, ok := parseLoosePlaceholder(sp); ok {
					t.Errorf("prose %q produced marker span %q", text, sp)
				}
			}
		})
	}
}

// TestLoosePlaceholderBareFormResolvesIndexMinusOne pins the meaning of a bare
// form with no index: it is marker syntax, it can never resolve to a mapping
// row, and mask-mode callers therefore redact it. Callers rely on ok==true
// with index<0 to distinguish "corrupt marker, still must not leak" from
// "ordinary text, leave alone".
func TestLoosePlaceholderBareFormResolvesIndexMinusOne(t *testing.T) {
	typ, idx, ok := parseLoosePlaceholder("SENSITIVE:phone")
	if !ok {
		t.Fatal("bare form must parse as marker syntax")
	}
	if typ != TypePhone {
		t.Errorf("type = %q, want %q", typ, TypePhone)
	}
	if idx != -1 {
		t.Errorf("index = %d, want -1 (no index present)", idx)
	}
}

// TestLoosePlaceholderOptionalIndexKeepsSeparatorlessSplit guards the
// interaction the optional index introduced.
//
// Making the index optional means an empty index group always matches, so the
// type group no longer has to backtrack to give the digits up. Without the
// type group being forbidden to end on a digit, "{SENSITIVEsecret1}" — a case
// this package has always split into type=secret index=1 — came back as
// type="secret1" index=-1, silently turning a resolvable marker into an
// unresolvable one (the value would have been masked instead of restored).
func TestLoosePlaceholderOptionalIndexKeepsSeparatorlessSplit(t *testing.T) {
	cases := []struct {
		in        string
		wantType  SensitiveType
		wantIndex int
	}{
		{"{SENSITIVEsecret1}", TypeSecret, 1},
		{"SENSITIVEphone1", TypePhone, 1},
		{"{SENSITIVE:server_ip:1}", TypeServerIP, 1},
		{"{SENSITIVE:server-ip:1}", TypeServerIP, 1},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			typ, idx, ok := parseLoosePlaceholder(c.in)
			if !ok {
				t.Fatalf("parseLoosePlaceholder(%q) failed", c.in)
			}
			if typ != c.wantType || idx != c.wantIndex {
				t.Errorf("got (%q, %d), want (%q, %d)", typ, idx, c.wantType, c.wantIndex)
			}
		})
	}
}

// TestLoosePlaceholderSpacedFormUnchanged guards the pre-existing behaviour
// that the optional-index change must not disturb: a model re-spacing the
// marker ("{SENSITIVE: server_ip : 1}") still resolves to a real index.
func TestLoosePlaceholderSpacedFormUnchanged(t *testing.T) {
	cases := []struct {
		in        string
		wantType  SensitiveType
		wantIndex int
	}{
		{"{SENSITIVE: server_ip : 1}", TypeServerIP, 1},
		{"{SENSITIVE:secret : 1}", TypeSecret, 1},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			typ, idx, ok := parseLoosePlaceholder(c.in)
			if !ok {
				t.Fatalf("parseLoosePlaceholder(%q) failed", c.in)
			}
			if typ != c.wantType || idx != c.wantIndex {
				t.Errorf("got (%q, %d), want (%q, %d)", typ, idx, c.wantType, c.wantIndex)
			}
		})
	}
}
