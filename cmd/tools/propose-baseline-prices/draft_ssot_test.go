package main

// 提案 → SSOT 草稿这条接缝的判据。
//
// # 缺口（2026-10-05 实测出来的，不是推理）
//
// 目标第二半是「按原厂拿到标准价 → 设为基准价 → 控实际成本」。这条链在仓里
// 分成两段，中间**没有代码**：
//
//	抓取 → 提案（cmd/tools/propose-baseline-prices，产出 proposal JSON）
//	                                          ↕ ← 这里全靠人手抄
//	SSOT（bg/data/model_baseline_prices.json）→ 写库（SyncBaselinePricesToDB）
//
// 两段各自都有测试，而**中间那一段没有任何东西守着** —— 与本会话早前反复
// 撞到的「已实现 ≠ 已接线」同一族。
//
// 具体的洞：`proposal` 里**唯一**带 canonical 名的一节是 `corroborated`
// （`ready_to_review` 只有厂商展示名），也就是唯一能当 SSOT 键的那一节。
// 而它此前只带 input/output —— **没有币种、没有缓存读写价**。⇒ 证据最强的
// 那一节，恰好**写不出**一条能通过 `BaselinePrice.validate` 的条目
// （2026-10-05 起 currency 必填，而 validate 会因它为空而拒）。
//
// # 这条判据钉的承重
//
//  1. **草稿能被权威闸门接受**：`buildDraft` 产出的每一条，marshal 成 JSON 后
//     用 **bg 自己的 SSOT 结构**反序列化，再逐条过 **bg 自己的 Validate**。
//     —— 这就是「两份形状一致」被**测出来**的方式，而不是靠注释声明。
//  2. **每一处不确定都是拒收并点名**，不是省略、不是编造：
//     缺 fetched_at / 币种空 / 两源不一致 / 缺价 / 键重复，各自必须出现在
//     `Refused` 里**并带上原因**。
//  3. 尤其：**没有 --fetched-at 时不许拿文件 mtime 顶上**。快照文件名是
//     `{vendor}.md`（无日期），mtime 记的是「文件什么时候被复制过」，不是
//     「什么时候从原厂页取的」。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

func f(v float64) *float64 { return &v }

// proposalWith 造一份最小的 proposal：n 条互证条目，全部可用。
func proposalWith(n int) *proposal {
	p := &proposal{GeneratedAt: "2026-10-05T00:00:00Z"}
	for i := 0; i < n; i++ {
		p.Corroborated = append(p.Corroborated, corroborated{
			Vendor:      "v" + string(rune('a'+i)),
			DisplayName: "display-" + string(rune('a'+i)),
			Canonical:   "canonical-" + string(rune('a'+i)),
			MatchScore:  1,
			VendorPage:  &baselineSide{Input: f(3), Output: f(15)},
			Observed:    &baselineSide{Input: f(3), Output: f(15)},
			SourceURL:   "https://vendor.example/pricing",
			Currency:    "USD",
			CacheRead:   f(0.3),
			CacheWrit:   f(3.75),
			Row:         "| model | 3 | 15 |",
			LineNo:      10 + i,
			Verdict:     "corroborated",
		})
	}
	return p
}

func TestDraftIsAcceptedByTheAuthoritativeGate(t *testing.T) {
	p := proposalWith(2)
	d := buildDraft(p, "2026-10-04T00:00:00Z")
	if len(d.Models) != 2 {
		t.Fatalf("draft has %d entries, want 2", len(d.Models))
	}
	if len(d.Refused) != 0 {
		t.Fatalf("nothing should be refused when every fact is present, got %+v", d.Refused)
	}
	if !d.Draft {
		t.Error("draft flag is false — this file must never be mistaken for the authoritative catalog")
	}

	// ★ 承重之一：用 **bg 自己的 SSOT 结构**反序列化，再用 **bg 自己的
	// Validate** 逐条过。这就是「工具产出的草稿能被权威写入口接受」这句话的
	// 全部内容 —— 两侧各有一份形状，只有真跑一遍才知道它们是不是同一份。
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal draft: %v", err)
	}
	// SSOT 的顶层形状是 {"models": {名字: 条目}}。所以要证明的不是「整个草稿能
	// 反序列化成 SSOT」，而是**草稿的 models 子对象就是 SSOT 的 models 子对象**
	// —— 那才是那条接缝。顶层多出来的 generated_at/draft/refused 是给人看的，
	// encoding/json 会忽略它们，所以这个文件是人确认后可以直接落位的形状。
	var wrapper struct {
		Models map[string]bg.BaselinePrice `json:"models"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatalf("the draft's models object does not fit bg's SSOT entry shape: %v", err)
	}
	asSSOT := wrapper.Models
	// 量具自证：顶层键必须**真的**叫 models，否则「是 SSOT 形状」这句话是空的。
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("unmarshal top level: %v", err)
	}
	if _, ok := top["models"]; !ok {
		t.Fatalf("the draft has no top-level models key (keys: %v) — it could not replace "+
			"bg/data/model_baseline_prices.json even by hand", keysOf(top))
	}
	if len(asSSOT) != len(d.Models) {
		t.Fatalf("SSOT-shaped map has %d entries, draft has %d — the two shapes disagree",
			len(asSSOT), len(d.Models))
	}
	for name, line := range asSSOT {
		if err := line.Validate(name); err != nil {
			t.Errorf("entry %q would be REJECTED by the authoritative gate: %v", name, err)
		}
	}
	// 承重的另一半：值必须**原样**传过去，不能被加工（倍率、四舍五入、单位换算）。
	got, ok := asSSOT["canonical-a"]
	if !ok {
		t.Fatalf("canonical-a missing from the SSOT-shaped map: %v", asSSOT)
	}
	if got.InputPer1M == nil || *got.InputPer1M != 3 ||
		got.OutputPer1M == nil || *got.OutputPer1M != 15 {
		t.Errorf("prices moved in conversion: in=%v out=%v, want 3/15", got.InputPer1M, got.OutputPer1M)
	}
	if got.CacheReadPer1M == nil || *got.CacheReadPer1M != 0.3 {
		t.Errorf("cache read price was dropped or changed: %v", got.CacheReadPer1M)
	}
	if got.CacheWritePer1M == nil || *got.CacheWritePer1M != 3.75 {
		t.Errorf("cache write price was dropped or changed: %v", got.CacheWritePer1M)
	}
	if got.FetchedAt != "2026-10-04T00:00:00Z" {
		t.Errorf("fetched_at = %q, want the value the human supplied", got.FetchedAt)
	}
}

func TestDraftRefusesInsteadOfInventing(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(p *proposal)
		fetchedAt   string
		wantRefuse  string
		wantNoModel bool
	}{
		{
			name:        "no fetched_at must refuse everything, not fall back to file mtime",
			mutate:      func(p *proposal) {},
			fetchedAt:   "",
			wantRefuse:  "no --fetched-at given",
			wantNoModel: true,
		},
		{
			name: "unknown currency must refuse",
			mutate: func(p *proposal) {
				p.Corroborated[0].Currency = ""
			},
			fetchedAt:   "2026-10-04T00:00:00Z",
			wantRefuse:  "no currency",
			wantNoModel: false, // 只拒这一条，另一条仍在
		},
		{
			name: "sources that disagree must refuse",
			mutate: func(p *proposal) {
				p.Corroborated[0].Verdict = "sources_disagree"
			},
			fetchedAt:   "2026-10-04T00:00:00Z",
			wantRefuse:  "sources_disagree",
			wantNoModel: false,
		},
		{
			name: "a missing price must refuse",
			mutate: func(p *proposal) {
				p.Corroborated[0].VendorPage = &baselineSide{Input: f(3), Output: nil}
			},
			fetchedAt:   "2026-10-04T00:00:00Z",
			wantRefuse:  "no input/output price",
			wantNoModel: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := proposalWith(2)
			tc.mutate(p)
			d := buildDraft(p, tc.fetchedAt)

			hit := false
			for _, r := range d.Refused {
				if strings.Contains(r.Reason, tc.wantRefuse) {
					hit = true
					if r.Canonical == "" {
						t.Errorf("refusal has no canonical name, so the operator cannot tell "+
							"which row on the vendor page it refers to: %+v", r)
					}
				}
			}
			if !hit {
				t.Fatalf("no refusal mentions %q; refusals = %+v", tc.wantRefuse, d.Refused)
			}
			if tc.wantNoModel && len(d.Models) != 0 {
				t.Errorf("draft has %d entries while every entry should be refused — a price with "+
					"no stated fetch time is not a price", len(d.Models))
			}
		})
	}
}

func TestDraftRefusesASecondClaimOnTheSameCanonicalName(t *testing.T) {
	p := proposalWith(1)
	dup := p.Corroborated[0]
	dup.DisplayName = "another spelling"
	p.Corroborated = append(p.Corroborated, dup)

	d := buildDraft(p, "2026-10-04T00:00:00Z")
	if len(d.Models) != 1 {
		t.Fatalf("draft has %d entries, want 1 — the SSOT is one price per model", len(d.Models))
	}
	if len(d.Refused) != 1 || !strings.Contains(d.Refused[0].Reason, "overwrite") {
		t.Errorf("the second claim must be refused by name, got %+v", d.Refused)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCorroboratedCarriesEveryFieldTheSotEntryNeeds 是上一条判据**没能**覆盖的
// 那一段，由变异验证揪出来的（teeth O）。
//
// 形态：TestDraftIsAcceptedByTheAuthoritativeGate 自己手搓 `proposal`，所以它只
// 测了 buildDraft；**填充 corroborated 的那个构造点**（`Currency: c.Currency`
// 那一行）它一次都没走过。把那行删掉，判据**全绿**。
//
// ⇒ 「已实现 ≠ 已接线」在我自己新写的判据里又发生了一次。所以这条走**真实
// crossCheck**（用仓里现成的 httptest 观察源夹具），断言：
//  1. corroborated 条目带着候选的币种与缓存读写价；
//  2. 它直接喂进 buildDraft 后产出的条目能过 bg 的 Validate。
//
// 这样「候选 → corroborated → 草稿 → 权威闸门」是一整条真的路。
func TestCorroboratedCarriesEveryFieldTheSotEntryNeeds(t *testing.T) {
	srv := observationServer(t, modelsDevShape("openai", "gpt-5", 1.25, 10))
	t.Setenv("LLM_GATEWAY_PRICE_OBSERVATION_URL", srv)

	p := &proposal{
		ReadyToReview: []vendorprice.Candidate{{
			Vendor: "openai", Model: "GPT-5",
			Input: f64(1.25), Output: f64(10),
			CacheRead: f64(0.125), CacheWrit: f64(12.5),
			Currency:  "USD",
			SourceURL: "https://platform.openai.com/docs/pricing",
			Row:       "| GPT-5 | 1.25 | 10 | 0.125 | 12.5 |", LineNo: 42,
			Confidence: vendorprice.ConfidenceTableRow,
			Warnings:   []string{`display name "GPT-5" resolved to canonical "gpt-5" (score 0.95)`},
		}},
	}
	if err := crossCheck(p); err != nil {
		t.Fatalf("crossCheck: %v", err)
	}
	if len(p.Corroborated) != 1 {
		t.Fatalf("corroborated has %d entries, want 1", len(p.Corroborated))
	}
	e := p.Corroborated[0]
	if e.Canonical != "gpt-5" {
		t.Fatalf("canonical = %q, want \"gpt-5\"", e.Canonical)
	}
	if e.Currency != "USD" {
		t.Errorf("corroborated.Currency = %q, want \"USD\" — the corroborated section is the "+
			"only one carrying canonical names, and without currency it cannot produce a valid "+
			"SSOT entry at all (validate requires it)", e.Currency)
	}
	if e.CacheRead == nil || *e.CacheRead != 0.125 {
		t.Errorf("corroborated.CacheRead = %v, want 0.125 — the cache prices are dropped on "+
			"the way to the only section that can be keyed by canonical name", e.CacheRead)
	}
	if e.CacheWrit == nil || *e.CacheWrit != 12.5 {
		t.Errorf("corroborated.CacheWrit = %v, want 12.5", e.CacheWrit)
	}
	if e.LineNo != 42 || !strings.Contains(e.Row, "GPT-5") {
		t.Errorf("corroborated lost the page location (row=%q line=%d) — a human checking this "+
			"price would have to search the whole vendor page for the model name", e.Row, e.LineNo)
	}

	// 一整条：真实 crossCheck 的产物 → 草稿 → bg 的权威闸门。
	d := buildDraft(p, "2026-10-04T00:00:00Z")
	if len(d.Refused) != 0 {
		t.Fatalf("nothing should be refused for a fully specified candidate: %+v", d.Refused)
	}
	line, ok := d.Models["gpt-5"]
	if !ok {
		t.Fatalf("draft has no gpt-5 entry (keys=%v)", keysOfModels(d.Models))
	}
	// draftLine 是工具侧的形状，闸门在 bg 侧 ⇒ 必须**过一遍 JSON** 才算真的
	// 验过。少这一步就是在拿自己家的类型问自己家的问题。
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal draft line: %v", err)
	}
	var asSOT bg.BaselinePrice
	if err := json.Unmarshal(raw, &asSOT); err != nil {
		t.Fatalf("draft line does not fit bg's SSOT entry shape: %v", err)
	}
	if err := asSOT.Validate("gpt-5"); err != nil {
		t.Errorf("the entry produced from a real cross-check would be rejected by the "+
			"authoritative gate: %v", err)
	}
}

func keysOfModels(m map[string]draftLine) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestAnEmptyDraftMustSayWhyItIsEmpty 钉「空草稿必须能自辩」。
//
// 这条是**真跑一次整条链之后**才发现的洞（2026-10-05）：`-emit-ssot` 写出来的
// 草稿里 `models` 为空时，`refused` 也是空的，于是「原厂页上一条可用价都没有」
// 与「互证根本没跑成」在文件里**长得一模一样**。
//
// 危害不是"不好看"：人打开一个空草稿会读成「这一家没有价」，于是去把 SSOT
// 填成别的，或者干脆放弃这一家 —— 而真相是观察源没连上，价其实在那儿。
func TestAnEmptyDraftMustSayWhyItIsEmpty(t *testing.T) {
	cases := []struct {
		name   string
		p      *proposal
		wantIn string
	}{
		{
			name: "corroboration did not run",
			p: &proposal{
				CorroborationRefused: "the observation source could not be read: dial tcp: refused",
			},
			wantIn: "corroboration did not run",
		},
		{
			name:   "no canonical list at all",
			p:      &proposal{},
			wantIn: "no canonical list was supplied",
		},
		{
			name:   "candidates existed but none resolved",
			p:      &proposal{CanonicalList: "models_canonical @ prod (exported …, 960 names)"},
			wantIn: "none was corroborated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := buildDraft(tc.p, "2026-10-04T00:00:00Z")
			if len(d.Models) != 0 {
				t.Fatalf("fixture is not an empty-draft case: %d entries", len(d.Models))
			}
			if !strings.Contains(d.Why, tc.wantIn) {
				t.Errorf("Why=%q does not say %q — an empty draft that does not explain "+
					"itself is indistinguishable from \"this vendor has no price\"",
					d.Why, tc.wantIn)
			}
			// 还要能**反序列化**出去：解释存在文件里，不在内存里。
			body, err := json.Marshal(d)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back draftCatalog
			if err := json.Unmarshal(body, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if back.Why == "" {
				t.Error("the explanation is lost on the way to disk — the operator reads the file")
			}
		})
	}
	// 非空草稿也要有 Why（说明这些条目凭什么可信），且**不能**说成空的。
	p := proposalWith(1)
	if got := buildDraft(p, "2026-10-04T00:00:00Z").Why; strings.Contains(got, "EMPTY") {
		t.Errorf("a non-empty draft claims to be EMPTY: %q", got)
	}
}

// TestPerModelBlockDiagnosticIsBidirectional 钉「这条新诊断只对它该命中的页形说话」。
//
// 方向很关键：2026-10-05 在 google-gemini.md 上加了「这张表没有 model 列、列是
// 计费档位 ⇒ 逐模型价块，模型名在抓取时丢了」这条诊断（真跑一次命中 329 行）。
// 它是**告警**，所以「什么时候不说」和「什么时候说」一样承重：一条对所有表都说的
// 告警等于噪声，运营会开始无视整块 rejection_reasons。
//
// 承重是**双向**的，跑**真实夹具**过**真实提取器**：
//
//	· live-google-gemini-permodel-block.md ⇒ 必须命中（页形夹具，含一张对照的
//	  标准「一行一模型」表，证明这个夹具里两种页形同时存在）
//	· live-anthropic-models-overview.md    ⇒ 必须**一条都不命中**
func TestPerModelBlockDiagnosticIsBidirectional(t *testing.T) {
	const dir = "../../../internal/vendorprice/testdata"
	count := func(name string) int {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		cands := vendorprice.Extract("unknown-vendor", "https://example.invalid/"+name, body)
		n := 0
		for _, c := range cands {
			for _, w := range c.Warnings {
				if strings.Contains(w, "per-model price block") {
					n++
					break
				}
			}
		}
		return n
	}
	if got := count("live-google-gemini-permodel-block.md"); got == 0 {
		t.Error("the per-model-block fixture matched 0 rows — the diagnostic no longer fires on " +
			"the page shape that motivated it, and that whole class of rows goes back to being " +
			"reported as a misleading column-mapping problem")
	}
	if got := count("live-anthropic-models-overview.md"); got != 0 {
		t.Errorf("the anthropic fixture matched %d row(s) — a standard one-row-per-model table "+
			"must not be told its model name was lost in the fetch; a warning that fires on "+
			"correct tables is how a whole report stops being read", got)
	}
}
