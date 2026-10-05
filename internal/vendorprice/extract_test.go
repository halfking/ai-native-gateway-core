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
	"strconv"
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

// ---------------------------------------------------------------------------
// 实抓夹具：live-openai-pricing.md
//
// 这条夹具同时钉住两个东西：
//
//  1. **出处**。仓里原有的 openai.md 的 URL Source 是 docs/models，抓下来是
//     一整页导航栏，零张表——它压根不是定价页，而 vendorPage map 里写的却是
//     docs/pricing。快照与它声称的 URL 对不上，出处这一栏是假的。
//     这里钉的是真正抓自 docs/pricing 的那份（2026-10-04）。
//  2. **一张表里同一个模型有多行价**。见下面的两条判据。
// ---------------------------------------------------------------------------

// 同一个模型在一张表里按模态分行，价格差一个数量级。
//
// 实测（live-openai-pricing.md:1108-1115）：
//
//	| Model | Modality | Input | Cached input | Output / cost |
//	| gpt-realtime-2.1        | Audio | $32.00 | $0.40 | $64.00 |
//	| Text  | $4.00  | $0.40 | $24.00 |
//	| Image | $5.00  | $0.50 | -        |
//
// 第一行取到 $32/$64 并挂上模型名 gpt-realtime-2.1——而那其实是**音频**价，
// 文本调用真实价格是 $4/$24。写进 SSOT 就是把最贵的那一档当成标价。
// 后两行更糟：首列是 "Text"/"Image"，会被当成模型名，于是提案里出现一个
// 叫 Text 的模型。
//
// 正确的处理是**整张表拒绝**：表头带 Modality 列就意味着「这个模型的价按模态
// 分档」，而「gpt-realtime-2.1 的标准价」在跨模态时根本不是一个数。取哪一档
// 是运营决策，不是解析器该替人做的选择。
func TestExtract_PerModalityPriceTableIsRefused(t *testing.T) {
	cands := Extract("openai", "https://platform.openai.com/docs/pricing",
		readLive(t, "live-openai-pricing.md"))

	// 旗舰表按上下文分档、realtime 表按模态分档 ⇒ 两张都不得产出可用候选。
	// 这两条不能只写成「整页 0 候选」：OpenAI 另有一张 Standard 档的平价表
	// （见下一个测试），整页一刀切会把对的那张也一起钉死。
	for _, c := range cands {
		if c.Confidence != ConfidenceTableRow {
			continue
		}
		switch c.LineNo {
		case 1032, 1033, 1034, // Flagship / Standard —— 上下文分档
			1043, 1044, 1045, // Batch 档
			1108, 1109, 1110, 1111, 1112, 1113: // Realtime —— 按模态分档
			t.Errorf("line %d graded table_row but its price is conditional, not a flat list price: %.90s",
				c.LineNo, c.Row)
		}
	}
	// 首列是模态词的行不得被当成模型名。
	for _, modality := range []string{"Text", "Image", "Audio"} {
		for _, c := range cands {
			if strings.EqualFold(c.Model, modality) {
				t.Errorf("a grouped sub-row was attributed to a model named %q — "+
					"it belongs to the model named on the row above", modality)
			}
		}
	}
	// 至少要有一条说清「模态分档」这个理由。
	var sawModality bool
	for _, c := range cands {
		if hasWarning(c, "Modality") {
			sawModality = true
		}
	}
	if !sawModality {
		t.Error("no candidate named the per-modality dimension; a reviewer would see only column-count noise")
	}
}

// 模型列由**表头**决定，不由位置决定。
//
// 实测（live-openai-pricing.md:1190）：`| Category | Model | Input | Cached input | Output |`
// —— 第一列是产品分类。按位置取名会得到一个叫 "ChatGPT" 的模型，而 $5/$30
// 其实是 chat-latest 的价。这类错误在提案里完全看不出来：模型名、单价、
// 出处、行号全都齐备，只有名字是错的。
func TestExtract_ModelColumnComesFromTheHeaderNotThePosition(t *testing.T) {
	cands := Extract("openai", "https://platform.openai.com/docs/pricing",
		readLive(t, "live-openai-pricing.md"))

	chat := findCandidate(t, cands, "chat-latest")
	if chat.Confidence != ConfidenceTableRow {
		t.Fatalf("chat-latest confidence = %q want %q (warnings=%v)", chat.Confidence, ConfidenceTableRow, chat.Warnings)
	}
	if chat.Input == nil || *chat.Input != 5.00 {
		t.Errorf("chat-latest input = %v want 5.00", chat.Input)
	}
	if chat.Output == nil || *chat.Output != 30.00 {
		t.Errorf("chat-latest output = %v want 30.00", chat.Output)
	}
	// 第一列是产品分类，它绝不能被当成模型名。
	for _, product := range []string{"ChatGPT", "Codex", "Life Sciences"} {
		for _, c := range cands {
			if strings.EqualFold(c.Model, product) {
				t.Errorf("a candidate is named %q — that is the Category column, not the model",
					product)
			}
		}
	}
}

// Fast / Batch / Flex 档与 Standard 档形状一模一样、价不同，标签也写着。
// 标签只被**陈述**不够：不定级的话，Fast 档的 Codex 会以 table_row 进提案，
// 而 SSOT 的键只有 canonical 名一个——它会和 Standard 档那一条互相覆盖。
func TestExtract_PremiumTierLabelDowngradesTheTable(t *testing.T) {
	cands := Extract("openai", "https://platform.openai.com/docs/pricing",
		readLive(t, "live-openai-pricing.md"))

	// :1207 是 Fast 档下 `gpt-5.3-codex` 的 $3.50 / $28.00 —— 比 Standard 档贵。
	for _, c := range cands {
		if c.LineNo != 1207 {
			continue
		}
		if c.Confidence == ConfidenceTableRow {
			t.Errorf("the Fast-tier row graded table_row: %.90s", c.Row)
		}
		if !hasWarning(c, "Fast") {
			t.Errorf("the Fast tier must be named in the warnings; got %v", c.Warnings)
		}
	}
	// Standard 档的那一条**不该**被警告「你得是标准档」——它就是。
	for _, c := range cands {
		if c.LineNo != 1194 {
			continue
		}
		if hasWarning(c, "must come from the vendor") {
			t.Errorf("a Standard-tier row was warned about not being standard — "+
				"a warning that fires on a good row teaches the reader to ignore warnings: %v", c.Warnings)
		}
	}
}

// 一页四张形状完全一样的表，原厂把档位写成紧贴表格上方的裸文本。
// 不报出来，人在提案里分不清哪张是哪张。
func TestExtract_ProductTierLabelsAreReported(t *testing.T) {
	cands := Extract("openai", "https://platform.openai.com/docs/pricing",
		readLive(t, "live-openai-pricing.md"))

	found := map[string]int{}
	for _, c := range cands {
		for _, w := range c.Warnings {
			i := strings.Index(w, `labelled "`)
			j := strings.Index(w, `" directly`)
			if i < 0 || j < 0 || j <= i {
				continue
			}
			found[w[i+len(`labelled "`):j]]++
		}
	}
	for _, want := range []string{"Standard", "Batch", "Flex"} {
		if found[want] == 0 {
			t.Errorf("product tier %q was never reported; found labels: %v", want, found)
		}
	}
}

// 产品档位必须**结构化**落字段，而且必须与列内维度（Tier）分开。
//
// 起因是一个实测出来的、会直接错算成本的结构缺陷。OpenAI 定价页上有五张
// 形状一模一样的表，档位写在紧贴表格上方的裸文本里：Standard / Batch /
// Flex / Fast / Ultrafast。它们的**列内维度全都是 "Short context"**。
// 于是 gpt-6-astra 在提案里长这样：
//
//	10 / 50   tier="Short context"   ← Standard
//	 5 / 25   tier="Short context"   ← Batch
//	 5 / 25   tier="Short context"   ← Flex
//	20 /100   tier="Short context"   ← Fast
//	60 /300   tier="Short context"   ← Ultrafast
//
// 五行在结构化字段上**完全无法区分**，跨度 12 倍。档位不是没识别——识别了，
// 只以一句 warning 字符串存在。照 tier 字段挑「基准价」的人，挑中 60/300
// 就是 6 倍高估、挑中 5/25 就是 2 倍低估，而这正是本项目要防的那类错。
//
// 判据钉三件事：
//  1. product_tier 真的被填上（**不是**空串）；
//  2. product_tier 的**分辨力严格高于** tier —— 这条是承重的，它把
//     「Tier 单独不够用」这个事实写进断言，删掉 product_tier 立刻红；
//  3. (product_tier, tier) 必须是 (模型, 价格) 的**函数**：同一组条件下
//     不允许出现两个价。那是散文窗口串台时会发生的形状。
func TestExtract_ProductTierIsAStructuredFieldAndOutresolvesTheColumnDimension(t *testing.T) {
	cands := Extract("openai", "https://platform.openai.com/docs/pricing",
		readLive(t, "live-openai-pricing.md"))

	// 只看有数字的行：没数字的行本来也不参与任何裁决。
	type keyed struct {
		product, tier string
		in, out       float64
	}
	var rows []keyed
	for _, c := range cands {
		if c.Model == "gpt-6-astra" && c.Input != nil && c.Output != nil {
			rows = append(rows, keyed{c.ProductTier, c.Tier, *c.Input, *c.Output})
		}
	}

	// **量具自证**：夹具退化时（页面改版 / 提取器不再识别档位）这里会是 0，
	// 而下面所有断言在空集合上都「成立」。先证明数据真的到位。
	if len(rows) < 4 {
		t.Fatalf("found %d priced gpt-6-astra candidates, want at least 4 — the fixture or the "+
			"extractor changed, and every assertion below would pass vacuously on an empty set",
			len(rows))
	}

	for _, r := range rows {
		if r.product == "" {
			t.Errorf("priced candidate %v/%v has an empty product_tier — the product tier was "+
				"recognised (it reaches the warnings) but never reached a structured field, so "+
				"these rows are indistinguishable to anything reading the proposal",
				r.in, r.out)
		}
	}

	distinctTier, distinctProduct := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		distinctTier[r.tier] = true
		distinctProduct[r.product] = true
	}

	// 承重的那一条。OpenAI 五张表共用一个列内维度，所以 tier 必然只有 1 个值；
	// 断言写成「product 的分辨力必须严格更高」而不是写死 5，是为了让将来
	// 原厂增删档位时这条判据不必跟着改，却仍然抓住「product_tier 没填」这件事。
	if len(distinctProduct) <= len(distinctTier) {
		t.Errorf("product_tier resolves %d distinct tiers where the column dimension resolves only "+
			"%d (%v) — the product tier is the only thing separating prices that differ by up to 12x, "+
			"so it must out-resolve the column dimension",
			len(distinctProduct), len(distinctTier), distinctTier)
	}
	if len(distinctProduct) < 4 {
		t.Errorf("only %d distinct product tiers among %d priced rows (%v) — the five same-shaped "+
			"OpenAI tables (Standard/Batch/Flex/Fast/Ultrafast) are not all being attributed",
			len(distinctProduct), len(rows), distinctProduct)
	}

	// (product_tier, tier) 必须是价格的函数。反例的形状是散文窗口串台：
	// 两张不同的表挂着同一个档位标签，于是同一个键下冒出两个价。
	seen := map[keyed]string{}
	for _, r := range rows {
		k := keyed{r.product, r.tier, 0, 0}
		price := formatPrice(r.in) + "/" + formatPrice(r.out)
		if prev, ok := seen[k]; ok && prev != price {
			t.Errorf("product_tier=%q tier=%q maps to two different prices (%s and %s) — a "+
				"condition key must determine exactly one price", r.product, r.tier, prev, price)
		}
		seen[k] = price
	}
}

func formatPrice(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// 非档位标签的小标题**不得**变成 product_tier。
//
// 这条判据是被一次变异逼出来的。把 markUnusable 里的 isProductTierLabel(label)
// 守卫整个删掉（即「表上方 whatever 一行都当档位」），**整个包的测试仍然全绿**
// —— 因为真页面上恰好没有一张表上方挂着会被 productTierRE 误认的普通标题。
//
// 守卫本身没被任何判据看着，这跟没有守卫是一回事：产物仍是对的，但**靠的是
// 页面恰好长这样**，不是靠代码。于是原厂哪天加一个 "Model overview" 或
// "Base models" 小标题，proposal 里的 product_tier 就开始说谎，而没有任何
// 测试会红——直到有人照着它选错基准价。
//
// 直接打 markUnusable，不走真页面：这条要验的是「守卫本身」，真页面只能
// 提供恰好正确的样本，提供不了**恰好不匹配**的样本。
func TestProductTierIgnoresNonTierHeadings(t *testing.T) {
	for _, label := range []string{
		"",
		"Model overview",
		"Embeddings",
		"Audio",
		"Prices per 1M tokens.",
		"Long context pricing explained",
		strings.Repeat("x", 41),
	} {
		c := Candidate{Confidence: ConfidenceTableRow}
		markUnusable(&c, "| gpt-5.1 | $1.25 | $10.00 |", UnitPer1M, 10, "", "", label)
		if c.ProductTier != "" {
			t.Errorf("heading %q became product_tier=%q — only an actual product-tier label "+
				"(Standard / Batch / Flex / Fast / Ultrafast / Priority …) may be recorded as one",
				truncate(label, 40), c.ProductTier)
		}
	}

	// 反向：真档位必须被记下来，且**归一**成关键词。
	//
	// 归一这条是实测逼出来的：productTierRE 是前缀匹配，所以 "Batch API
	// reference" 这种小标题也算档位标签。存原文的话，同一档位会占两个键
	// （"batch" 与 "Batch API reference"），按键分组与去重全部失准。
	// 原文仍保留在 warnings 里。
	for label, want := range map[string]string{
		"Standard":             "standard",
		"Batch":                "batch",
		"Flex":                 "flex",
		"Fast":                 "fast",
		"Ultrafast":            "ultrafast",
		"Priority":             "priority",
		"Batch API reference":  "batch",
		"standard processing":  "standard",
		"Pay-as-you-go":        "payasyougo",
		"PayGo":                "paygo",
		"Standard pricing >$5": "standard",
	} {
		c := Candidate{Confidence: ConfidenceTableRow}
		markUnusable(&c, "| gpt-5.1 | $1.25 | $10.00 |", UnitPer1M, 10, "", "", label)
		if c.ProductTier != want {
			t.Errorf("heading %q recorded product_tier=%q, want the normalized keyword %q",
				label, c.ProductTier, want)
		}
	}

	// **边界：regional 不进 product_tier。**
	//
	// "Regional" 出现在 premiumTierRE 里，却**不在** productTierRE 里，所以
	// isProductTierLabel("Regional") 为假 ⇒ product_tier 为空。这是刻意的：
	// 区域定价是**计费维度**（由 dimensionRE 那一路在管，出的是
	// "names a non-standard billing dimension (regional)"），不是「这张表属于
	// 哪个产品档」。两者混进同一个字段，「维度」与「档位」就再也分不开，
	// 而取哪一档当基准价恰好要靠这个区分。
	c := Candidate{Confidence: ConfidenceTableRow}
	markUnusable(&c, "| gpt-5.1 | $1.25 | $10.00 |", UnitPer1M, 10, "", "", "Regional")
	if c.ProductTier != "" {
		t.Errorf(`"Regional" recorded as product_tier=%q — it is a billing dimension `+
			`(handled by dimensionRE), not a product tier`, c.ProductTier)
	}
}

// 非档位词的长散文不得被误认成档位标签。
func TestTierLabelWarningIgnoresProseAndMoney(t *testing.T) {
	for _, bad := range []string{
		"",
		"Prices per 1M tokens.",
		"All models and their prices are listed below for reference.",
		"$10.00 and $50.00 per MTok",
		strings.Repeat("x", 41),
	} {
		if w := tierLabelWarning(bad); len(w) != 0 {
			t.Errorf("tierLabelWarning(%q) = %v, want none", truncate(bad, 40), w)
		}
	}
	if w := tierLabelWarning("Standard"); len(w) == 0 {
		t.Error(`tierLabelWarning("Standard") returned nothing`)
	}
	if w := tierLabelWarning("standard processing"); len(w) == 0 {
		t.Error(`tierLabelWarning("standard processing") returned nothing`)
	}
}

// ---------------------------------------------------------------------------
// 分档表的四条判据
//
// 上一组测试只钉住了「哪些行**不得**成为可用候选」。这一组钉住另一半：
// **被拒绝的行必须仍然带着数字和它所属的档位**。两者缺一不可——
//
//	只钉「拒绝」⇒ 解析器退化成「整页列数对不上」，人既看不到数、也看不出
//	                    这张表其实有货，退化在测试里是绿的；
//	只钉「有数」⇒ 忘了定价必须被拒，一条分档价会以挂牌价身份进 SSOT。
// ---------------------------------------------------------------------------

// 分档表必须**读得出数**，并标明它属于哪一档。
//
// 变异实测：把两层表头的拼装直接关掉（buildEffectiveHeader 不再被调用），
// 这 3 个模型全部退回「列数 9 对不上表头的 3」，数字一个都没有。
func TestExtract_TieredTableStillYieldsItsNumbers(t *testing.T) {
	cands := Extract("openai", "https://platform.openai.com/docs/pricing",
		readLive(t, "live-openai-pricing.md"))

	for _, tc := range []struct {
		model    string
		in, out  float64
		tier     string
		wantLine int
	}{
		{"gpt-6-astra", 10, 50, "Short context", 1032}, // Flagship / Standard
		{"gpt-6.1-sol", 2, 10, "Short context", 1033},  // Flagship / Standard
		{"gpt-4.7", 0, 0, "", 0},                       // 占位，下面单独处理
	} {
		if tc.wantLine == 0 {
			continue
		}
		t.Run(tc.model, func(t *testing.T) {
			c := findCandidate(t, cands, tc.model)
			if c.LineNo != tc.wantLine {
				t.Fatalf("line = %d want %d (row=%.80s)", c.LineNo, tc.wantLine, c.Row)
			}
			if c.Input == nil || *c.Input != tc.in {
				t.Errorf("input = %v want %v — a refused row must still show its numbers", c.Input, tc.in)
			}
			if c.Output == nil || *c.Output != tc.out {
				t.Errorf("output = %v want %v", c.Output, tc.out)
			}
			if c.Tier != tc.tier {
				t.Errorf("tier = %q want %q", c.Tier, tc.tier)
			}
			if c.Confidence == ConfidenceTableRow {
				t.Error("a tiered price must never be graded table_row")
			}
		})
	}

	// xAI 的 8 个 grok 文本模型同样必须读得出数。
	xai := Extract("xai", "https://docs.x.ai/developers/models", readLive(t, "live-xai-pricing.md"))
	grok := findCandidate(t, xai, "grok-4.7")
	if grok.Input == nil || *grok.Input != 2.00 {
		t.Errorf("grok-4.7 input = %v want 2.00", grok.Input)
	}
	if grok.Output == nil || *grok.Output != 6.00 {
		t.Errorf("grok-4.7 output = %v want 6.00", grok.Output)
	}
	if grok.Confidence == ConfidenceTableRow {
		t.Error("grok-4.7 is context-tiered on this page and must not be table_row")
	}
}

// Tier 必须只记**被取走的那几列**所属的档位。
//
// 若把整行出现过的档位都记上，Tier 会写成 "Long context | Short context"
// 而数字其实只来自短上下文那一组——标签比数字宽，正是最容易被读成
// 「两档价一样」的地方。
func TestTierIsTheTierTheTakenValuesCameFrom(t *testing.T) {
	cands := Extract("xai", "https://docs.x.ai/developers/models", readLive(t, "live-xai-pricing.md"))
	c := findCandidate(t, cands, "grok-4.7")
	if c.Tier != "Short context" {
		t.Fatalf("tier = %q want %q — the label must not be wider than the numbers it describes", c.Tier, "Short context")
	}
	if strings.Contains(c.Tier, "Long context") {
		t.Error("tier names a context window whose prices were not taken")
	}
}

// 两层表头拼不上时必须**放弃**，而不是硬分。
//
// 变异实测：去掉「档位列数必须整除」这条后，一个 3 档 × 8 列的假表会被
// 静默对齐，8 个价格列被切成 3/3/2——前两档对、第三档跨了半格。
func TestEffectiveHeaderRefusesAnAmbiguousTierLayout(t *testing.T) {
	// 三个档位（三个词都必须在 headerDimensionRE 里，否则第一层根本认不出档位，
	// 拼装直接放弃，这条判据就变成恒假）。
	tier3 := []string{"", "Batch", "Flex", "Regional"}
	// 8 个价格列，3 个档位 ⇒ 8 % 3 != 0，对齐不唯一。
	sub := []string{"Model", "Input", "Cached input", "Output", "Input", "Cached input", "Output", "Input", "Output"}
	if _, ok := buildEffectiveHeader(tier3, sub, 9); ok {
		t.Error("buildEffectiveHeader accepted a 3-way split of 8 price columns — the spans are not unique")
	}
	// 整除时必须拼得出来，否则这条判据就是恒假。
	tier2 := []string{"", "Batch", "Flex"}
	ok2 := []string{"Model", "Input", "Cached input", "Output", "Input", "Cached input", "Output"}
	h, ok := buildEffectiveHeader(tier2, ok2, 7)
	if !ok {
		t.Fatal("buildEffectiveHeader rejected a cleanly divisible layout (2 tiers x 3 columns)")
	}
	want := []string{"", "Batch", "Batch", "Batch", "Flex", "Flex", "Flex"}
	for i := range want {
		if h.tiers[i] != want[i] {
			t.Errorf("tiers[%d] = %q want %q (full=%v)", i, h.tiers[i], want[i], h.tiers)
		}
	}
	// 没有档位词的一层表头根本不该走这条路。
	if _, ok := buildEffectiveHeader([]string{"Model", "Input", "Output"}, ok2, 7); ok {
		t.Error("buildEffectiveHeader aligned a header that names no billing tier at all")
	}
}

// 「表前散文」必须有界：导航侧边栏里的一个链接词不得染上整页的表。
//
// 实测 OpenAI 定价页第一张表在第 1029 行，而它前面一行表格都没有 ⇒ 旧实现
// 累积了整整 1028 行导航，其中 `[Fast mode](…/fast-mode)` 成了 Standard 档那张
// 好表被判不可用的理由之一。
//
// 变异实测：把 proseWindow 的截断去掉，`chat-latest` 立刻退回 unusable。
func TestProseAboveATableIsBounded(t *testing.T) {
	b := readLive(t, "live-openai-pricing.md")

	// 量具自证：夹具里真的存在「远处导航里的维度词」。
	if !strings.Contains(string(b), "guides/fast-mode") {
		t.Skip("live OpenAI fixture no longer contains the nav link this test pins")
	}
	navLine := 0
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "guides/fast-mode") {
			navLine = i + 1
			break
		}
	}

	cands := Extract("openai", "https://platform.openai.com/docs/pricing", b)
	chat := findCandidate(t, cands, "chat-latest")
	if chat.Confidence != ConfidenceTableRow {
		t.Fatalf("chat-latest confidence = %q want %q — a nav link %d lines above the table "+
			"must not reach it (warnings=%v)", chat.Confidence, ConfidenceTableRow, navLine, chat.Warnings)
	}
	// 反向钉：它确实**没有**被「Fast mode」这条理由染过。
	for _, w := range chat.Warnings {
		if strings.Contains(w, "Fast mode") {
			t.Errorf("chat-latest was tainted by a navigation link %d lines above its table: %s", navLine, w)
		}
	}

	// 内联形态：同页同结构的小样本，不依赖 61KB 的夹具。
	var b2 strings.Builder
	b2.WriteString("*   [Fast mode](https://example.invalid/fast-mode)\n")
	for i := 0; i < 40; i++ {
		b2.WriteString("navigation filler line\n")
	}
	b2.WriteString("## Standard models\n\nPrices per 1M tokens.\n\n")
	b2.WriteString("| Model | Input | Output |\n| --- | --- | --- |\n| a-model | $1.00 | $2.00 |\n")
	c2 := Extract("v", "https://example.invalid/p", []byte(b2.String()))
	if len(c2) != 1 || c2[0].Confidence != ConfidenceTableRow {
		t.Fatalf("a clean table 42 lines below a nav link must stay usable; got %d candidates: %+v", len(c2), c2)
	}
}

// ---------------------------------------------------------------------------
// prose 收窄的**三道机制互相冗余** —— 必须逐条单独钉
//
// 下面三条判据用的是同一段「导航污染」的失效，但每条只验**一道**机制。
// 合在一起写会互相掩盖：P3（行数上限）被 P8（标题清空）顶着，P8 又被
// P9（档位标签清空）顶着，实测三条单独回退时全绿——三道机制一起失效才会红。
// ⇒ **冗余的防御要逐条验，否则等价于只有一道。**
// ---------------------------------------------------------------------------

// P3：行数上限。导航词在表前**很远**、且中途没有标题。
func TestProseWindowAloneBoundsHowFarBackWeLook(t *testing.T) {
	var b strings.Builder
	b.WriteString("*   [Fast mode](https://example.invalid/fast-mode)\n")
	// 超过 proseWindow 行且**没有 markdown 标题**——把「标题清空」这条机制摘掉。
	for i := 0; i < proseWindow*4; i++ {
		b.WriteString("navigation filler line\n")
	}
	b.WriteString("Prices per 1M tokens.\n\n")
	b.WriteString("| Model | Input | Output |\n| --- | --- | --- |\n| a-model | $1.00 | $2.00 |\n")
	c := Extract("v", "https://example.invalid/p", []byte(b.String()))
	if len(c) != 1 {
		t.Fatalf("candidates = %d want 1", len(c))
	}
	if c[0].Confidence != ConfidenceTableRow {
		t.Errorf("confidence = %q want %q — a dimension word %d lines above must not reach the table (warnings=%v)",
			c[0].Confidence, ConfidenceTableRow, proseWindow*4, c[0].Warnings)
	}
}

// P8：markdown 标题清空散文。维度词离表**很近**（窗口够不着），只能靠标题断开。
func TestHeadingAloneClearsThePreviousSectionsProse(t *testing.T) {
	md := []byte("Batch API pricing, a 50% discount on both input and output.\n" +
		"\n## Standard models\n\nPrices per 1M tokens.\n\n" +
		"| Model | Input | Output |\n| --- | --- | --- |\n| a-model | $1.00 | $2.00 |\n")
	c := Extract("v", "https://example.invalid/p", md)
	if len(c) != 1 {
		t.Fatalf("candidates = %d want 1", len(c))
	}
	if c[0].Confidence != ConfidenceTableRow {
		t.Errorf("confidence = %q want %q — a heading must cut the previous section's prose off "+
			"from this table (warnings=%v)", c[0].Confidence, ConfidenceTableRow, c[0].Warnings)
	}
}

// P9：档位标签清空标签之前的 UI 残留。维度词在标签**之前**、但离表很近。
func TestProductTierLabelAloneClearsStaleUIProse(t *testing.T) {
	md := []byte("Standard Batch Flex Fast Ultrafast\n" + // 标签条：只是导航
		"Standard\n" + // 当前选中项
		"Prices per 1M tokens.\n\n" +
		"| Model | Input | Output |\n| --- | --- | --- |\n| a-model | $1.00 | $2.00 |\n")
	c := Extract("v", "https://example.invalid/p", md)
	if len(c) != 1 {
		t.Fatalf("candidates = %d want 1", len(c))
	}
	if c[0].Confidence != ConfidenceTableRow {
		t.Errorf("confidence = %q want %q — the words Batch/Flex in the tab strip are navigation, "+
			"not a statement about this table (warnings=%v)", c[0].Confidence, ConfidenceTableRow, c[0].Warnings)
	}
	// 量具自证：这条表确实带着 "Standard" 标签，说明机制被触发了。
	if len(c[0].Warnings) == 0 || !strings.Contains(strings.Join(c[0].Warnings, " "), "Standard") {
		t.Errorf("the tier label must be reported even for a usable row; warnings=%v", c[0].Warnings)
	}
}
