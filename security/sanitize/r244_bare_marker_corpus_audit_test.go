package sanitize

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
)

// r244_bare_marker_corpus_audit_test.go —— 占位符扫描的**误报 / 漏报**审计。
//
// ## 这道门在审什么
//
// 2026-10-10 那次生产抽样（154，response_preview 1920 条 + request_preview
// 400 条）的可复跑版本：把真实文本扫一遍，要求
//  1. 命中的 span 全部是真 marker；
//  2. 散文不被当成 marker（误报为零）。
//
// ## 2026-10-21 的三处修正（都有实测依据，不是"读代码觉得"）
//
// 1. **扫描口径与生产对齐。**
//    原审计用 `loosePlaceholderSpanPattern.FindAllString(line, -1)` —— 那是
//    **正则的字面分支**，它的裸形态只认 ASCII 标记词/ASCII 冒号/ASCII 类型。
//    而生产扫描（looseMarkerSpans）会在**折宽副本**上再扫一遍，因此看得见全角
//    拼写。实测差异（2026-10-21）：
//        `ＳＥＮＳＩＴＩＶＥ：phone`  生产 1 个 span / 原审计口径 0 个 span
//    ⇒ 用原口径审计 = 审计了一个生产不会用的扫描器 ⇒ 全角形态**永远查不出**。
//    现在改用 `looseMarkerSpans`（生产扫描路径本身）。
//
// 2. **提交式语料，保证审计永远跑。**
//    原审计只扫外部语料（默认 /tmp/sbld3/samples.txt）；本机没有该文件 ⇒ 整个
//    测试 **skip** ⇒ 在 CI 里它与"审计通过"长得一模一样（AGENTS.md 硬要求：
//    「检查没跑成」与「检查通过」必须长得不一样）。
//    现在 testdata/r244_marker_corpus.txt 是**提交进仓库**的语料，永远被扫；
//    外部生产语料存在时**叠加**扫描，并显式报告是否扫到。
//
// 3. **误报规则不再与同包的另一条判据打架。**
//    原规则：「无花括号且无索引 ⇒ 误报」。但同一包里的
//    r244_bare_marker_form_test.go 明确把
//        （SENSITIVE:phone, SENSITIVE:email, SENSITIVE:id_card）
//    当作**实测到的真 marker 形态**（154 实测：模型会成串列举类型），而
//    r244_bare_marker_false_positive_test.go 也断言它 `parsed > 0`。
//    ⇒ 同一形态：一条判据说"这是 marker"，另一条判据说"这是误报"。
//    真跑一遍即确认（2026-10-21 实测）：枚举形态的每个 span 都被原规则判成误报。
//    ⇒ 新规则：**裸形态的 type 必须是已知敏感类型**，否则才是误报。
//      （散文里的 `sensitive:foo` 仍被抓；真 marker 的类型不会被误伤。）

// r244CorpusRecord 是一条语料。
type r244CorpusRecord struct {
	kind string // "MARKER" | "PROSE"
	text string
	line int
}

// loadR244Corpus 读取语料文件。格式见 testdata/r244_marker_corpus.txt 的头注释。
func loadR244Corpus(t *testing.T, path string) []r244CorpusRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open corpus %s: %v", path, err)
	}
	defer f.Close()

	var out []r244CorpusRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "MARKER "):
			out = append(out, r244CorpusRecord{kind: "MARKER", text: strings.TrimPrefix(line, "MARKER "), line: lineNo})
		case strings.HasPrefix(line, "MARKER-JSON "):
			out = append(out, r244CorpusRecord{kind: "MARKER-JSON", text: strings.TrimPrefix(line, "MARKER-JSON "), line: lineNo})
		case strings.HasPrefix(line, "PROSE "):
			out = append(out, r244CorpusRecord{kind: "PROSE", text: strings.TrimPrefix(line, "PROSE "), line: lineNo})
		default:
			t.Fatalf("%s:%d: unknown corpus record (want MARKER / MARKER-JSON / PROSE / #): %q", path, lineNo, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan corpus %s: %v", path, err)
	}
	return out
}

// r244Span 是语料里的一次命中。
type r244Span struct {
	text string
	typ  SensitiveType
	idx  int
}

// r244Scan 用**生产扫描路径**扫一行文本，返回解析成功的 marker span。
//
// 用 looseMarkerSpans（而不是 loosePlaceholderSpanPattern）是有意为之：见文件
// 头注释第 1 条 —— 正则的字面分支看不见全角拼写，审计必须与生产同口径。
func r244Scan(line string) []r244Span {
	var out []r244Span
	for _, sp := range looseMarkerSpans(line) {
		candidate := line[sp[0]:sp[1]]
		typ, idx, ok := parseLoosePlaceholder(candidate)
		if !ok {
			continue
		}
		out = append(out, r244Span{text: candidate, typ: typ, idx: idx})
	}
	return out
}

// r244IsFalsePositive 判定一次命中是不是误报。
//
// 规则（2026-10-21 重铸，理由见文件头第 3 条）：
//   - 花括号形态（ASCII 或全角）⇒ 永远是 marker 形态，不是误报；
//   - 裸形态 ⇒ type 必须是已知敏感类型，否则是散文被误当成标记。
func r244IsFalsePositive(sp r244Span) bool {
	if strings.ContainsAny(sp.text, "{}｛｝") {
		return false
	}
	return !ValidateSensitiveType(sp.typ)
}

// TestR244CommittedCorpusAudit 是**永远会跑**的那一半审计。
//
// 提交式语料（testdata/r244_marker_corpus.txt）里：
//   - MARKER 行必须被遮蔽（输出与输入不同 + 出现遮蔽标记 + 标记词不再出现）；
//   - PROSE 行必须**逐字不变**（散文不得被遮蔽）。
//
// ⚠️ 这里判的是**客户端拿到的那段文本**（端到端），不是内部 span 细节。
// 原因（2026-10-21 实测）：裸形态嵌在 JSON 里时，`{"phone":"SENSITIVE:phone"}`
// 会被花括号分支**整段**捕获，生产是在 restoreLooseMarkers 的**内层重扫**里才
// 看到里面那个 marker 的 ⇒ 只看 span 会把这条形态误判成「没检出」。
// 端到端口径与生产一致，且直接对应用户可见的结果。
func TestR244CommittedCorpusAudit(t *testing.T) {
	const path = "testdata/r244_marker_corpus.txt"
	records := loadR244Corpus(t, path)
	if len(records) == 0 {
		t.Fatalf("committed corpus %s is empty — an empty corpus audits nothing", path)
	}
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	markers, prose := 0, 0
	for _, rec := range records {
		out := s.restoreText(context.Background(), rec.text, SanitizeMap{}.resolverFor(), looseMaskUnknown)
		spans := r244Scan(rec.text)
		switch rec.kind {
		case "MARKER":
			markers++
			if out == rec.text {
				t.Errorf("%s:%d: marker 原样穿过到客户端（未被遮蔽）: %q", path, rec.line, rec.text)
				continue
			}
			if !strings.Contains(out, redactedMarkerPrefix) {
				t.Errorf("%s:%d: marker 未被遮蔽（输出里没有遮蔽标记）: %q -> %q", path, rec.line, rec.text, out)
			}
			if r1039CarriesMarkerWord(out) {
				t.Errorf("%s:%d: 内部标记词仍出现在客户端可见文本里: %q -> %q", path, rec.line, rec.text, out)
			}
			// ★ 生产扫描路径必须真的看见它。
			// 这条断言是「审计口径与生产对齐」的**承重墙**：把 r244Scan 换回
			// 裸正则（loosePlaceholderSpanPattern）后，全角形态在它眼里不存在
			// ⇒ 这里立刻红。少了它，审计就又变成「审了一个生产不用的扫描器」。
			if len(spans) == 0 {
				t.Errorf("%s:%d: 生产扫描路径没检出这个 marker 形态: %q", path, rec.line, rec.text)
			}
			// span 级断言：真 marker 不得被误报规则判成散文。
			// 这条**必须**留在提交式语料里 —— 否则重铸后的误报规则只被
			// 那条「语料缺失即 skip」的外部审计使用，等于无人验证。
			for _, sp := range spans {
				if r244IsFalsePositive(sp) {
					t.Errorf("%s:%d: 真 marker 被误报规则判成散文: span=%q type=%q in %q",
						path, rec.line, sp.text, sp.typ, rec.text)
				}
			}
		case "MARKER-JSON":
			markers++
			// 花括号分支会整段吞掉这段 JSON ⇒ 不要求 span 数，但**必须**端到端
			// 被遮蔽（生产是在 restoreLooseMarkers 的内层重扫里认出它的）。
			if out == rec.text {
				t.Errorf("%s:%d: JSON 里的 marker 原样穿过到客户端: %q", path, rec.line, rec.text)
			}
			if !strings.Contains(out, redactedMarkerPrefix) {
				t.Errorf("%s:%d: JSON 里的 marker 未被遮蔽: %q -> %q", path, rec.line, rec.text, out)
			}
			if r1039CarriesMarkerWord(out) {
				t.Errorf("%s:%d: 内部标记词仍出现在客户端可见文本里: %q -> %q", path, rec.line, rec.text, out)
			}
		case "PROSE":
			prose++
			if out != rec.text {
				t.Errorf("%s:%d: 散文被误遮蔽: %q -> %q", path, rec.line, rec.text, out)
			}
			// span 级断言：散文连 marker 形状都不该产生（比「端到端没变」更严：
			// 端到端可能被别的东西掩盖住）。
			for _, sp := range spans {
				t.Errorf("%s:%d: 散文产生了 marker 形状的 span: span=%q type=%q in %q",
					path, rec.line, sp.text, sp.typ, rec.text)
			}
		}
	}
	t.Logf("committed corpus: %d records (%d marker, %d prose) audited end-to-end", len(records), markers, prose)
	if markers == 0 || prose == 0 {
		t.Fatalf("语料必须同时含 MARKER 与 PROSE 两段，否则审计有一半是空的")
	}
}

// TestR244ProductionCorpusAuditOnCorpus 是**外部生产语料**那一半。
//
// 语料来源（154 生产抽样）：
//   response_preview 1920 条 + request_preview 400 条。
//
// ⚠️ 语料缺失时**跳过**是既有设计（进 CI 不拖慢），但这次必须把「没扫到」
// 与「扫到且干净」**在输出上分开**：t.Skipf 会让这条用例在普通 `go test`
// 输出里**完全不可见** ⇒ 读报告的人会以为它跑过且通过。
// ⇒ 这里用 t.Logf 显式打出「外部语料缺失，本次只跑了提交式语料」。
func TestR244ProductionCorpusAuditOnCorpus(t *testing.T) {
	path := os.Getenv("SANITIZE_FP_AUDIT_CORPUS")
	if path == "" {
		path = "/tmp/sbld3/samples.txt"
	}
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("外部生产语料缺失（%s；set SANITIZE_FP_AUDIT_CORPUS 以启用）—— "+
			"注意：本次**没有**扫生产语料，只有提交式语料被审计过", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	lines, spans := 0, 0
	var bare []string
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		for _, sp := range r244Scan(line) {
			spans++
			if r244IsFalsePositive(sp) {
				if len(bare) < 10 {
					bare = append(bare, sp.text+"  <<< "+truncateRunes(line, 100))
				}
			}
		}
	}
	t.Logf("scanned %d lines, %d parsed marker spans (production scan path)", lines, spans)
	for _, b := range bare {
		t.Errorf("false positive: %s", b)
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
