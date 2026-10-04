// internal/vendorprice/extract_test.go — 对**真实原厂页面快照**的提取
//
// 这组测试不喂夹具，喂的是仓里 docs/02-resources/research/pricing/raw/
// 下 2026-06-12 抓下来的原厂定价页 markdown。理由：夹具只能证明提取器对
// 我想象的形状成立，而「自动提取原厂价格」这件事的难点恰恰在于各厂页面
// 形状互不兼容——只有真页面能把那些形状逼出来。
//
// 判据的方向性要求与本包契约一致：**宁可少提，不可提错**。所以每条真页面
// 都同时钉两件事——该提出来的提对了，以及**不该被用的行确实没被当成可用**。
package vendorprice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rawDir = "../../docs/02-resources/research/pricing/raw"

func readRaw(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rawDir, name))
	if err != nil {
		t.Skipf("real vendor snapshot %s unavailable: %v", name, err)
	}
	return b
}

// findCandidate 按模型名取一条候选。
func findCandidate(t *testing.T, cands []Candidate, model string) Candidate {
	t.Helper()
	for _, c := range cands {
		if strings.EqualFold(c.Model, model) {
			return c
		}
	}
	t.Fatalf("no candidate for model %q (extracted: %d candidates)", model, len(cands))
	return Candidate{}
}

// ---------------------------------------------------------------------------
// anthropic：模型在行内 + 表头给列义（可靠形态）
// ---------------------------------------------------------------------------
func TestExtract_AnthropicTableShape(t *testing.T) {
	cands := Extract("anthropic", "https://docs.anthropic.com/en/docs/about-claude/models/overview", readRaw(t, "anthropic.md"))

	opus := findCandidate(t, cands, "Claude Opus 4.8")
	if opus.Confidence != ConfidenceTableRow {
		t.Fatalf("Opus 4.8 confidence = %q want %q (warnings=%v)", opus.Confidence, ConfidenceTableRow, opus.Warnings)
	}
	// 表头是：Model | Base Input Tokens | 5m Cache Writes | 1h Cache Writes
	//        | Cache Hits & Refreshes | Output Tokens
	if opus.Input == nil || *opus.Input != 5.00 {
		t.Errorf("input = %v want 5.00 (Base Input Tokens column)", opus.Input)
	}
	if opus.Output == nil || *opus.Output != 25.00 {
		t.Errorf("output = %v want 25.00 (Output Tokens column)", opus.Output)
	}
	if opus.CacheRead == nil || *opus.CacheRead != 0.50 {
		t.Errorf("cache read = %v want 0.50 (Cache Hits & Refreshes column)", opus.CacheRead)
	}
	// 5m 与 1h 两个写缓存列只取第一个，另一个必须有警告而不是被静默吞掉。
	if opus.CacheWrit == nil || *opus.CacheWrit != 6.25 {
		t.Errorf("cache write = %v want 6.25 (first of the two write columns)", opus.CacheWrit)
	}
	if opus.Currency != "USD" {
		t.Errorf("currency = %q want USD", opus.Currency)
	}
	if opus.SourceURL == "" || opus.Row == "" || opus.LineNo == 0 {
		t.Error("candidate must carry source_url, the raw row and a line number so a human can check it")
	}
}

// ---------------------------------------------------------------------------
// xai：同类形态，但表头是 Input / Cached input / Output，且多一个 Context 列
// ---------------------------------------------------------------------------
func TestExtract_XaiTableShape(t *testing.T) {
	cands := Extract("xai", "https://docs.x.ai/docs/pricing", readRaw(t, "xai.md"))

	grok := findCandidate(t, cands, "grok-4.3")
	if grok.Confidence != ConfidenceTableRow {
		t.Fatalf("grok-4.3 confidence = %q want %q (warnings=%v)", grok.Confidence, ConfidenceTableRow, grok.Warnings)
	}
	if grok.Input == nil || *grok.Input != 1.25 {
		t.Errorf("input = %v want 1.25", grok.Input)
	}
	if grok.Output == nil || *grok.Output != 2.50 {
		t.Errorf("output = %v want 2.50", grok.Output)
	}
	// "Cached input" 必须判成 cache_read 而不是 input —— 判错就是
	// 把缓存读价当输入价，比提取失败更糟。
	if grok.CacheRead == nil || *grok.CacheRead != 0.20 {
		t.Errorf("cache read = %v want 0.20", grok.CacheRead)
	}
	if grok.Input != nil && *grok.Input == 0.20 {
		t.Error("cached-input column was taken as the input price")
	}
}

// ---------------------------------------------------------------------------
// MiniMax：删除线原价 + 上下文分档 —— 本包最要紧的一条
// ---------------------------------------------------------------------------
func TestExtract_MiniMaxStrikethroughAndTiersAreUnusable(t *testing.T) {
	raw := readRaw(t, "MiniMax-paygo.md")
	cands := Extract("minimax", "https://platform.minimax.io/docs/guides/pricing-paygo", raw)

	var sawUnusable bool
	for _, c := range cands {
		if !strings.Contains(c.Row, "~~") && !tierQualifierRE.MatchString(c.Row) {
			continue
		}
		sawUnusable = true
		if c.Confidence != ConfidenceUnusable {
			t.Errorf("row %q: confidence = %q want %q —— 划线原价/分档价进账本就是错价",
				truncate(c.Row, 70), c.Confidence, ConfidenceUnusable)
		}
	}
	if !sawUnusable {
		t.Fatal("no struck-through or tiered row was found in the real snapshot — " +
			"either the page changed shape or the extraction no longer sees those rows")
	}

	// 反向钉：整份 MiniMax 页里**不得**出现任何 confidence=table_row 的行。
	// 这一页全是「≤512k / >512k」分档 + 永久折扣，朴素价不存在。
	for _, c := range cands {
		if c.Confidence == ConfidenceTableRow {
			t.Errorf("row %q was graded table_row, but this page only publishes "+
				"tiered/discounted prices — there is no flat list price to take",
				truncate(c.Row, 70))
		}
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// zhipu / mistral：页面里没有 $ —— 必须「提不出」而不是「提错」
// ---------------------------------------------------------------------------
func TestExtract_NoDollarPagesYieldNoTableRowCandidates(t *testing.T) {
	for _, tc := range []struct{ vendor, file, url string }{
		{"zhipu", "zhipu.md", "https://open.bigmodel.cn/pricing"},
		{"mistral", "mistral.md", "https://docs.mistral.ai/getting-started/models/models_overview"},
	} {
		t.Run(tc.vendor, func(t *testing.T) {
			cands := Extract(tc.vendor, tc.url, readRaw(t, tc.file))
			for _, c := range cands {
				if c.Confidence == ConfidenceTableRow {
					t.Errorf("%s: row %q graded table_row but this page publishes no $ prices — "+
						"an invented USD number here would silently corrupt billing",
						tc.vendor, truncate(c.Row, 70))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 列义与失效原因的结构性判据（用内联小样本，不依赖真页面）
// ---------------------------------------------------------------------------
func TestRoleFromHeader(t *testing.T) {
	cases := []struct {
		header string
		want   ColumnRole
	}{
		{"Model", RoleModel},
		{"Base Input Tokens", RoleInput},
		{"Input", RoleInput},
		{"Output Tokens", RoleOutput},
		{"Output", RoleOutput},
		{"Cached input", RoleCacheRead},
		{"Cache Hits & Refreshes", RoleCacheRead},
		{"5m Cache Writes", RoleCacheWrite},
		{"1h Cache Writes", RoleCacheWrite},
		{"Context", RoleOther},
		{"", RoleOther},
	}
	for _, tc := range cases {
		if got := RoleFromHeader(tc.header); got != tc.want {
			t.Errorf("RoleFromHeader(%q) = %q want %q", tc.header, got, tc.want)
		}
	}
}

// 一张没有 output 列的表，任何行都不得被当成可用的定价行。
func TestHeaderWithoutOutputColumnIsUnusable(t *testing.T) {
	md := []byte("| Model | Input | Context |\n| --- | --- | --- |\n" +
		"| some-model | $5 / MTok | 1M |\n")
	cands := Extract("v", "https://example.invalid/p", md)
	if len(cands) != 1 {
		t.Fatalf("candidates = %d want 1", len(cands))
	}
	if cands[0].Confidence != ConfidenceUnusable {
		t.Errorf("confidence = %q want %q", cands[0].Confidence, ConfidenceUnusable)
	}
}

// 表头必须**紧邻**数据行；段落之后旧表头不得继续生效，否则上一个厂商的
// 列义会被套到下一个厂商的行上。
func TestHeaderDoesNotLeakAcrossParagraphs(t *testing.T) {
	md := []byte("| Model | Input | Output |\n| --- | --- | --- |\n" +
		"| a | $1 / MTok | $2 / MTok |\n" +
		"\nSome prose paragraph.\n\n" +
		"| b | $9 | $19 |\n")
	cands := Extract("v", "https://example.invalid/p", md)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d want 2", len(cands))
	}
	if cands[0].Confidence != ConfidenceTableRow {
		t.Errorf("first row confidence = %q want %q", cands[0].Confidence, ConfidenceTableRow)
	}
	if cands[1].Confidence != ConfidenceUnusable {
		t.Errorf("row after a paragraph must not inherit the old header; got %q (warnings=%v)",
			cands[1].Confidence, cands[1].Warnings)
	}
}

// ---------------------------------------------------------------------------
// 同一模型在多张表里以**不同计费维度**出现 —— 三个洞里最难堵的一个
//
// 删改线与分档这两类失效在行内自带信号；维度不同则没有：下面三行都长得
// 像一张干净的定价表。判据必须看表头与**表前的散文**。
// ---------------------------------------------------------------------------
func TestExtract_SameModelAcrossBillingDimensionsIsUnusable(t *testing.T) {
	md := []byte(`# Pricing

| Model | Base Input Tokens | Output Tokens |
| --- | --- | --- |
| Claude Opus 4.8 | $5 / MTok | $25 / MTok |

Fast mode pricing, in research preview, provides significantly faster output at premium pricing.

| Model | Input | Output |
| --- | --- | --- |
| Claude Opus 4.8 | $10 / MTok | $50 / MTok |

The Batch API allows asynchronous processing with a 50% discount on both input and output.

| Model | Batch input | Batch output |
| --- | --- | --- |
| Claude Opus 4.8 | $2.50 / MTok | $12.50 / MTok |
`)

	cands := Extract("anthropic", "https://example.invalid/p", md)
	var prices []string
	for _, c := range cands {
		if c.Input == nil {
			continue
		}
		switch c.Confidence {
		case ConfidenceTableRow:
			prices = append(prices, "usable")
		default:
			prices = append(prices, "unusable")
		}
	}
	if len(cands) != 3 {
		t.Fatalf("candidates = %d want 3 (one per table)", len(cands))
	}
	// 只有第一张（标准表）可用。
	if prices[0] != "usable" {
		t.Errorf("row 1 (the standard table) graded %q, want %q", prices[0], ConfidenceTableRow)
	}
	for i, p := range prices[1:] {
		if p != "unusable" {
			t.Errorf("row %d (a premium/batch dimension) graded usable — "+
				"taking it as the list price is exactly the cross-wiring this catches", i+2)
		}
	}

	// 每一张不可用的都必须说清是哪个维度。
	for _, c := range cands[1:] {
		found := false
		for _, w := range c.Warnings {
			if strings.Contains(w, "billing dimension") {
				found = true
			}
		}
		if !found {
			t.Errorf("row %q was graded unusable without naming the dimension: %v", truncate(c.Row, 60), c.Warnings)
		}
	}
}

// ---------------------------------------------------------------------------
// 实抓夹具：internal/vendorprice/testdata/live-*.md
//
// raw/ 下那批是 2026-06-12 的快照。原厂页面改版是常态，把真实快照钉成
// 长期夹具后，页面一变就会立刻看到差异，而不是某天突然「提取不出价了」。
// 这三个文件是 2026-10-04 实抓的原样内容，未做任何编辑。
// ---------------------------------------------------------------------------

const liveDir = "testdata"

func readLive(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(liveDir, name))
	if err != nil {
		t.Fatalf("live fixture %s missing: %v", name, err)
	}
	return b
}

func hasWarning(c Candidate, substr string) bool {
	for _, w := range c.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 结构性判据一：表里的钱一个都不许消失
//
// 这条判据的由来是一次真实的静默丢失：anthropic 新页把四个模型的价格挤在
// `|  | $10 / input MTok$50 / output MTok | … |` 这一行，而这一行同时含有
// "input" 和 "output" 两个词，旧实现把它登记成了「候选表头」；紧随其后的
// 分隔行并不存在，于是它既没被确认成表头，也再没被当成数据行报出来。
//
// 后果是整个页面**零候选**，而且看不出是丢了——提案里其他行都好好地列着。
// 「某个模型的价格凭空不见了」是这类工具最坏的失效形态，因为人只会以为
// 「原厂没公布」。
//
// 判据形如「每个含货币符号的表格行都至少对应一条候选」，与置信度无关：
// 不可用的行也**必须出现**，只是带着原因。
// ---------------------------------------------------------------------------
func TestNoMoneyEverDisappears(t *testing.T) {
	var files []string
	for _, dir := range []string{rawDir, liveDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(files) < 10 {
		t.Fatalf("only %d fixtures found — the corpus shrank, this test would pass vacuously", len(files))
	}

	totalMoneyLines := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		cands := Extract("v", "https://example.invalid/"+f, raw)

		covered := map[int]int{}
		for _, c := range cands {
			covered[c.LineNo]++
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "|") {
				continue
			}
			if !strings.ContainsAny(line, "$€£¥") {
				continue
			}
			totalMoneyLines++
			if covered[i+1] == 0 {
				t.Errorf("%s:%d carries money but produced no candidate at all — "+
					"this is the silent-loss failure mode (row=%.90s)", f, i+1, line)
			}
		}
	}
	if totalMoneyLines < 100 {
		t.Fatalf("only %d money-bearing lines scanned across %d fixtures — "+
			"the corpus shrank and this invariant proves nothing", totalMoneyLines, len(files))
	}
}

// ---------------------------------------------------------------------------
// 结构性判据二：单位必须由单元格自己声明，而不是全页假定
//
// 实测 xAI 一页之内有两种单位：chat 段是 per 1M tokens，图像段是
// `$0.002 / img`。旧实现把 `Unit` 硬编码成 "per_1m"，于是每张图的 0.002
// 美元顶着「每 1M token」的标签一路走下去。它当时没进账本，**纯属侥幸**：
// 挡下来的是另一条不相干的规则（页面散文里恰好出现了 "long context"）。
// 一次页面改版就能让它变成一条 table_row 候选，而一个按图计费的数字
// 填进每 1M token 的价格列，偏差是四个数量级。
// ---------------------------------------------------------------------------
func TestPerImagePriceIsNotTaggedAsPerMillionTokens(t *testing.T) {
	cands := Extract("xai", "https://docs.x.ai/developers/models", readLive(t, "live-xai-pricing.md"))

	var sawImage bool
	for _, c := range cands {
		if !strings.Contains(c.Row, "$0.01 / img") && !strings.Contains(c.Row, "$0.002 / img") {
			continue
		}
		sawImage = true
		if c.Unit == UnitPer1M {
			t.Errorf("line %d prices per image but is tagged per_1m: %q", c.LineNo, truncate(c.Row, 80))
		}
		if c.Confidence == ConfidenceTableRow {
			t.Errorf("line %d is a per-image price but graded table_row — it would be "+
				"stored as a per-1M token price (row=%.80s)", c.LineNo, c.Row)
		}
		if !hasWarning(c, "billed per") {
			t.Errorf("line %d must say which unit it is actually billed in; warnings=%v", c.LineNo, c.Warnings)
		}
	}
	if !sawImage {
		t.Fatal("no per-image price found in the live xAI fixture — the page changed shape " +
			"or the extractor stopped seeing those rows")
	}
}

// 同一个判据的单元级形态：cellUnit 对每种写法都必须给对单位。
func TestCellUnitClassification(t *testing.T) {
	cases := []struct{ cell, want string }{
		{"$5 / MTok", UnitPer1M},
		{"$5 / 1M", UnitPer1M},
		{"$5 / M tokens", UnitPer1M},
		{"$0.002 / img", UnitPerImage},
		{"$0.08 / sec", UnitPerSecond},
		{"$0.05 / min", UnitPerMinute},
		{"$3.00 / hr", UnitPerHour},
		{"$5 / 1k calls", UnitPer1KCalls},
		{"$15.00 / 1M chars", UnitPerChar},
		{"$0.004 / message", UnitPerMessage},
		{"$1.25", UnitUnknown},
		{"500k", UnitUnknown},
	}
	for _, tc := range cases {
		if got := cellUnit(tc.cell); got != tc.want {
			t.Errorf("cellUnit(%q) = %q want %q", tc.cell, got, tc.want)
		}
	}
}

// 「1M chars」必须先被 per_char 吃掉，而不是笼统的「1M」把它记成 token。
// 一个字符与一个 token 差三个数量级，这一条顺序错了就是错价。
func TestCharUnitIsNotSwallowedByTheGenericMegaRule(t *testing.T) {
	if got := cellUnit("$15.00 / 1M chars"); got != UnitPerChar {
		t.Fatalf("cellUnit(%q) = %q want %q — the specific pattern must be tried before the generic one",
			"$15.00 / 1M chars", got, UnitPerChar)
	}
}

// 判据一的**直接形态**：一个带钱的表格行落在任何已确认表块之外时，必须被
// 报出来。
//
// 为什么这条要单独写死而不只靠跨夹具扫描：修复「静默吞行」的方式是改成
// 表块解析，于是真实页面上的丢钱行**全部落进了块内**——跨夹具扫描那条
// 判据就再也碰不到孤儿路径了，于是「把孤儿行直接丢掉」这个变异仍然全绿。
// 判据守着自己最该守的那条路径，却被自己的修复方式绕开，是很典型的
// 「测试通过但什么都没证明」。变异实测：把 orphanCandidate 那行删掉，
// 跨夹具判据纹丝不动，本条判据转红。
func TestMoneyRowOutsideAnyConfirmedTableIsStillReported(t *testing.T) {
	md := []byte(`Some prose before the table.

|  | $10 / input MTok$50 / output MTok | $4 / input MTok$20 / output MTok |

More prose after it.
`)
	cands := Extract("anthropic", "https://example.invalid/p", md)
	if len(cands) == 0 {
		t.Fatal("a money-bearing table row outside any confirmed table produced no candidate — " +
			"this is exactly how anthropic's whole price row used to vanish")
	}
	found := false
	for _, c := range cands {
		if !strings.Contains(c.Row, "$10 / input MTok") {
			continue
		}
		found = true
		if c.Confidence == ConfidenceTableRow {
			t.Errorf("row graded table_row without a confirmed header: %.80s", c.Row)
		}
		if !hasWarning(c, "carries money") {
			t.Errorf("the orphan money row must say its amounts are unaccounted for; warnings=%v", c.Warnings)
		}
	}
	if !found {
		t.Errorf("the money row itself was not reported; candidates=%d", len(cands))
	}
}

// ---------------------------------------------------------------------------
// 列向表格：模型在表头，价格挤在一个单元格里
// ---------------------------------------------------------------------------
func TestExtract_ColumnOrientedTable(t *testing.T) {
	cands := Extract("anthropic", "https://docs.anthropic.com/en/docs/about-claude/models/overview",
		readLive(t, "live-anthropic-models-overview.md"))

	// 页面原文（live-anthropic-models-overview.md:17）：
	//   |  | $10 / input MTok$50 / output MTok | $4 / input MTok$20 / output MTok
	//     | $2 / input MTok$10 / output MTok | $1 / input MTok$5 / output MTok |
	// 表头（:14）依次是 Fable 5.1 / Opus 5.5 / Sonnet 5.5 / Haiku 4.5。
	for _, tc := range []struct {
		model   string
		in, out float64
	}{
		{"Claude Fable 5.1", 10, 50},
		{"Claude Opus 5.5", 4, 20},
		{"Claude Sonnet 5.5", 2, 10},
		{"Claude Haiku 4.5", 1, 5},
	} {
		t.Run(tc.model, func(t *testing.T) {
			c := findCandidate(t, cands, tc.model)
			if c.Confidence != ConfidenceTableRow {
				t.Fatalf("confidence = %q want %q (warnings=%v)", c.Confidence, ConfidenceTableRow, c.Warnings)
			}
			if c.Input == nil || *c.Input != tc.in {
				t.Errorf("input = %v want %v", c.Input, tc.in)
			}
			if c.Output == nil || *c.Output != tc.out {
				t.Errorf("output = %v want %v", c.Output, tc.out)
			}
			if c.Unit != UnitPer1M {
				t.Errorf("unit = %q want %q", c.Unit, UnitPer1M)
			}
		})
	}
}

// 模型名后面粘的说明（「The fastest model with near-frontier intelligence」）
// 既不能进名字，也不能参与列义判定——里面的 "model" 一词曾经把整张表判成
// 行向表，四个价格一个都没提出来。
func TestDescriptionAfterALinkIsSplitOffTheIdentifier(t *testing.T) {
	ident, desc := splitIdent("[Claude Haiku 4.5](https://x.invalid/h) The fastest model with near-frontier intelligence")
	if ident != "Claude Haiku 4.5" {
		t.Errorf("ident = %q want %q", ident, "Claude Haiku 4.5")
	}
	if !strings.Contains(desc, "fastest model") {
		t.Errorf("description = %q, want it to keep the trailing prose", desc)
	}
	if got := headerRole("[Claude Haiku 4.5](https://x.invalid/h) The fastest model with near-frontier intelligence"); got != RoleOther {
		t.Errorf("headerRole = %q want %q — the description must not decide the column's semantics", got, RoleOther)
	}
}

// 短词必须带词边界：`tier` 曾匹配 "fronTIER"，把 anthropic 整页判死；
// `flex` 曾匹配 "fleXible"。这类假阳性比漏判更贵——它让一整页本来正确的数据
// 变成不可用，而提案里的人看不出是哪里错了。
func TestShortDimensionWordsNeedWordBoundaries(t *testing.T) {
	benign := []string{
		"The fastest model with near-frontier intelligence",
		"A flexible model for everyday work",
		"prioritised support and regional availability", // regional 是真的，见下
	}
	for _, b := range benign[:2] {
		if m := headerDimensionRE.FindString(b); m != "" {
			t.Errorf("headerDimensionRE matched %q inside benign prose %q", m, b)
		}
	}
	for _, real := range []string{
		"| Model | Batch input | Batch output |",
		"| Model | Context | Short context | Long context |",
		"| Model | Standard processing | Priority processing |",
		"Fast mode pricing applies across the full context window",
	} {
		if dimensionRE.FindString(real) == "" && headerDimensionRE.FindString(real) == "" {
			t.Errorf("neither dimension regex matched a real billing dimension in %q", real)
		}
	}
}

// 「计费对象」不是「处理模式」：正文里的 tool use 不是定价维度。
// 保留它会让 anthropic models overview 唯一正确的四个价格整表不可用。
func TestToolUseIsNotAPricingDimension(t *testing.T) {
	prose := "All current models support text and image input, text output, multilingual " +
		"capabilities, vision, and tool use."
	if m := dimensionRE.FindString(prose); m != "" {
		t.Errorf("dimensionRE matched %q in a capabilities sentence — a capability list is not a billing dimension", m)
	}
}

// ---------------------------------------------------------------------------
// 双层表头：必须拒绝，而且必须说清为什么
// ---------------------------------------------------------------------------
func TestExtract_MultiLevelHeaderIsRefusedWithADiagnosis(t *testing.T) {
	cands := Extract("xai", "https://docs.x.ai/developers/models", readLive(t, "live-xai-pricing.md"))

	// 整页**不得**出现任何可用候选：那张表的每一行都同时给出短上下文与长
	// 上下文两套价，而 SSOT 的键只有 canonical 名一个。
	for _, c := range cands {
		if c.Confidence == ConfidenceTableRow {
			t.Errorf("line %d graded table_row but this page publishes context-tiered prices: %.90s",
				c.LineNo, c.Row)
		}
	}

	// 至少要有一条把「这是双层表头 + 表头按上下文分档」说清楚，
	// 而不是只丢一句「列数对不上」。
	var sawSubHeader, sawTier bool
	for _, c := range cands {
		if hasWarning(c, "second header row") {
			sawSubHeader = true
		}
		if hasWarning(c, "billing dimension") {
			sawTier = true
		}
	}
	if !sawSubHeader {
		t.Error("no candidate named the second header row — a reviewer cannot act on \"column count 8 does not match the header's 4\"")
	}
	if !sawTier {
		t.Error("no candidate named the context-tier billing dimension")
	}
}

// ---------------------------------------------------------------------------
// 诚实失败：deepseek 当前页面没有 markdown 表格
// ---------------------------------------------------------------------------
// 这一条钉的是「提不出」而不是「提错」。deepseek 的价在纯文本版面里，而且
// 是 OFF-PEAK/PEAK 分档——即使能提出来，分档价也不是挂牌价。
func TestExtract_DeepSeekFlatLayoutYieldsNothing(t *testing.T) {
	raw := readLive(t, "live-deepseek-pricing.md")
	if strings.Contains(string(raw), "\n|") {
		t.Skip("the deepseek fixture now contains a markdown table — re-check what it should yield")
	}
	cands := Extract("deepseek", "https://api-docs.deepseek.com/quick_start/pricing", raw)
	for _, c := range cands {
		if c.Confidence == ConfidenceTableRow {
			t.Errorf("row %.80s graded table_row from a page with no markdown table", c.Row)
		}
	}
}

// 描述字段要留给人看：它不进计算，但人靠它分清「这个价是哪个产品的」。
func TestDescriptionIsCarriedOnCandidates(t *testing.T) {
	cands := Extract("xai", "https://docs.x.ai/docs/pricing", readRaw(t, "xai.md"))
	for _, c := range cands {
		if strings.HasPrefix(c.Model, "grok-imagine") && c.Description == "" {
			t.Errorf("candidate %q has no description; the cell carried %q",
				c.Model, truncate(c.Row, 90))
		}
	}
}
