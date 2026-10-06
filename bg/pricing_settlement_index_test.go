package bg

// 内部结算价指数的判据。
//
// # 这一层为什么要自己一整套判据，而不是复用 pricing_baseline_sync 的
//
// 基准价那层验的是「这个价对不对」（出处、币种、对账判词）；这一层验的是
// 「这个**相对数**算得对不对，且不可算的时候说得清为什么」。两者的失败形态
// 完全不同：价错了台账里是 drift，而指数错了是**结算单上印了一个看着正常的
// 倍率** —— 后者没有任何对账机制会发现它。
//
// ★ 判据设计的核心（据 2026-10-06 采集到的真实 SSOT 形状）：
//
//   - 实测三个模型 input 同为 0.30（minimax-m2 / m2.1 / m2.5）。**同价**是
//     这一层最常遇到的形态，所以「基数模型自己 = 精确 100」必须被单独钉住：
//     浮点除法会给出 99.99999999999999，而结算单上的 100 与 99.99999999999999
//     差的是「口径」与「待排查」的分别。
//   - 实测各家 output/input 挂牌倍率不统一（MiniMax 4×、Claude 5×、codex 8×），
//     所以两档**必须**各自成指数、各自取基数，合成一个数需要引入配比假设。
//   - 免费（0）与未定价（NULL）在迁移 826 里是分开的两个事实，这一层不得合并。

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

// realCatalog 是按 2026-10-06 真跑 raw/ + models.dev 互证后落进 SSOT 的
// 那 13 条的**子集**，数值逐字照抄，不是编的：
//
//	minimax-m2{0.3, 1.2}  minimax-m2.1{0.3, 1.2}  minimax-m2.5{0.3, 1.2}
//	minimax-m2.5-highspeed{0.6, 2.4}
//	claude-haiku-4-5{1, 5}  claude-sonnet-4-5{3, 15}
//	claude-opus-4-5{5, 25} claude-fable-5{10, 50}
//
// 收录标准是「形状上覆盖每一条判据」：同价三连、input 与 output 的最低价
// **不是同一个模型**（0.30 vs 1.20 都是 minimax，但下面对照会刻意造一个
// input 最低与 output 最低分属不同厂商的样本）、以及 2×/4×/10× 的跨度。
func realCatalog() map[string]BaselinePrice {
	return map[string]BaselinePrice{
		"minimax-m2":             {InputPer1M: f(0.3), OutputPer1M: f(1.2), Currency: "USD"},
		"minimax-m2.1":           {InputPer1M: f(0.3), OutputPer1M: f(1.2), Currency: "USD"},
		"minimax-m2.5":           {InputPer1M: f(0.3), OutputPer1M: f(1.2), Currency: "USD"},
		"minimax-m2.5-highspeed": {InputPer1M: f(0.6), OutputPer1M: f(2.4), Currency: "USD"},
		"claude-haiku-4-5":       {InputPer1M: f(1), OutputPer1M: f(5), Currency: "USD"},
		"claude-sonnet-4-5":      {InputPer1M: f(3), OutputPer1M: f(15), Currency: "USD"},
		"claude-opus-4-5":        {InputPer1M: f(5), OutputPer1M: f(25), Currency: "USD"},
		"claude-fable-5":         {InputPer1M: f(10), OutputPer1M: f(50), Currency: "USD"},
	}
}

// TestTheCheapestModelIsExactlyOneHundred 是这一层的**承重**判据。
//
// 它钉住的是「最便宜的模型 = 100」这条口径本身，而且是**逐位**钉：同价的
// 三个 minimax 都是基数候选，任何一个都必须落在精确的 100.00。
//
// ★ 修正过一次判据本身（2026-10-06）：原文写「浮点除法会给出
// 99.99999999999999」。**那是错的** —— 实测 Go 1.x / IEEE754 下
// `100*a/a` 对 a ∈ {0.1, 0.3, 1.2, 7, 1e-7} 逐位等于 100（先乘后除精确），
// 所以对 0.30 那种真实价位，写不写归一分支都一样。
// ⇒ 那条注释是一次**没跑就下的结论**，而它恰好是唯一一处让判据看起来
// 有牙、实际抓不到任何变异的地方。
//
// 现在触发条件换成实测真会发生的那个：极小价位 1e-300 → 100.00000000000001。
// 下面 TestTheExtremePriceStillLandsOnExactlyOneHundred 钉它。
func TestTheCheapestModelIsExactlyOneHundred(t *testing.T) {
	idx := BuildSettlementIndex(realCatalog())

	// 三个同价模型都必须是基数候选，也都必须精确 100。
	for _, name := range []string{"minimax-m2", "minimax-m2.1", "minimax-m2.5"} {
		si, ok := idx[name]
		if !ok {
			t.Fatalf("%s missing from the index", name)
		}
		if si.Input.Index == nil {
			t.Fatalf("%s: input index is nil (verdict=%s reason=%s)", name, si.Input.Verdict, si.Input.Reason)
		}
		if *si.Input.Index != SettlementIndexBase {
			t.Errorf("%s: input index = %.17g, want exactly %v — the cheapest model IS the "+
				"definition of the base, and a base that prints as 100.00000000000001 on a "+
				"settlement sheet is a defect, not a rounding curiosity",
				name, *si.Input.Index, SettlementIndexBase)
		}
	}
}

// TestTheExtremePriceStillLandsOnExactlyOneHundred 是上面那条归一分支的
// **实际**承重判据（变异实测：本条是唯一能让「摘掉归一」变红的用例）。
//
// 为什么用 1e-300 这种现实里不存在的价位：它不是造出来的刁难，而是**实测
// 出来唯一会触发浮点偏差的形状**。真实 LLM 价位到不了那么小，可一旦有人
// 填进一个极小的价（某个按次计费的模型折算到每 1M token），结算单上就会出现
// 一个不是 100 的「基准指数」，而没有任何告警会响。
//
// ★ 阴性对照：这条判据必须在**归一分支存在**时绿、摘掉时红。若两边都绿，
// 说明它测不到那段代码，那它就是一条装饰。
func TestTheExtremePriceStillLandsOnExactlyOneHundred(t *testing.T) {
	// 先自证量具：这个价位在裸浮点下**确实**偏离 100，否则本判据是恒真的。
	tiny := 1e-300
	if raw := SettlementIndexBase * tiny / tiny; raw == SettlementIndexBase {
		t.Skipf("100*%g/%g happens to be exactly %v on this platform — the float tail this "+
			"judgement guards against does not occur here, so it cannot be verified", tiny, tiny, SettlementIndexBase)
	}

	cat := map[string]BaselinePrice{
		"tiny-base": {InputPer1M: f(tiny), OutputPer1M: f(tiny), Currency: "USD"},
		"normal":    {InputPer1M: f(1), OutputPer1M: f(1), Currency: "USD"},
	}
	idx := BuildSettlementIndex(cat)

	si := idx["tiny-base"]
	if si.Input.Index == nil {
		t.Fatalf("tiny-base input index is nil (verdict=%s)", si.Input.Verdict)
	}
	if *si.Input.Index != SettlementIndexBase {
		t.Errorf("tiny-base input index = %.17g, want exactly %v — the base model's index is a "+
			"definition, not a measurement, so a float tail here is a reporting defect",
			*si.Input.Index, SettlementIndexBase)
	}
	// 对照：非基数模型仍按真倍率算，不被归一波及。
	if got, want := *idx["normal"].Input.Index, SettlementIndexBase*(1/tiny); math.Abs(got-want) > 1 {
		t.Errorf("normal index = %.4f, want ~%.4f — the base snap must not touch other models",
			got, want)
	}
}

// TestTheIndexIsTheVendorPriceRatio 钉住指数的定义式：100 × 价 / 最低价。
//
// 这一条不钉公式，别的都白写：把 base 从 100 改成 1000、或把除法写成乘法，
// 上面那条（基数 = 100）**仍然全绿**。
func TestTheIndexIsTheVendorPriceRatio(t *testing.T) {
	idx := BuildSettlementIndex(realCatalog())

	// 最低 input 0.30、最低 output 1.20。
	// claude-opus-4-5 = {5, 25} ⇒ input 100×5/0.3 = 1666.666…，output 100×25/1.2 = 2083.33…
	cases := []struct {
		model   string
		side    string
		wantIn  float64
		wantOut float64
	}{
		{"minimax-m2", "input", 100, 100},
		{"claude-haiku-4-5", "input", 100 * 1 / 0.3, 100 * 5 / 1.2},
		{"claude-sonnet-4-5", "input", 100 * 3 / 0.3, 100 * 15 / 1.2},
		{"claude-opus-4-5", "input", 100 * 5 / 0.3, 100 * 25 / 1.2},
		{"claude-fable-5", "input", 100 * 10 / 0.3, 100 * 50 / 1.2},
	}
	for _, c := range cases {
		si := idx[c.model]
		gotIn, gotOut := *si.Input.Index, *si.Output.Index
		if math.Abs(gotIn-c.wantIn) > 1e-6 {
			t.Errorf("%s input index = %.6f, want %.6f", c.model, gotIn, c.wantIn)
		}
		if math.Abs(gotOut-c.wantOut) > 1e-6 {
			t.Errorf("%s output index = %.6f, want %.6f", c.model, gotOut, c.wantOut)
		}
	}
}

// TestTheTwoSidesMayHaveDifferentBases 是「两档各自取基数」的判据。
//
// 构造 input 最便宜与 output 最便宜**分属不同厂商**的样本。合成单一指数
// 的实现在这里必然露馅：它只有一个 base，而这份清单的 input 基数与 output
// 基数是两个不同的模型。
func TestTheTwoSidesMayHaveDifferentBases(t *testing.T) {
	cat := map[string]BaselinePrice{
		"cheap-in":  {InputPer1M: f(0.5), OutputPer1M: f(20), Currency: "USD"},
		"cheap-out": {InputPer1M: f(4), OutputPer1M: f(2), Currency: "USD"},
	}
	idx := BuildSettlementIndex(cat)

	if got := idx["cheap-in"].Input.BaselineModel; got != "cheap-in" {
		t.Errorf("input base model = %q, want %q — input 0.5 is the cheapest input", got, "cheap-in")
	}
	if got := idx["cheap-out"].Output.BaselineModel; got != "cheap-out" {
		t.Errorf("output base model = %q, want %q — output 2.0 is the cheapest output", got, "cheap-out")
	}
	// ★ 「两个基数各自 100」的准确说法是：每个模型在**它自己是基数的那个
	// 方向**上等于 100。跨方向不等于 100 —— cheap-in 的 output 20 是最贵的
	// output（基价 2），所以它是 1000 而不是 100。
	//   （第一版判据把这里写成「两个模型两档都 100」，红的是判据不是实现：
	//   那样写会要求 cheap-in 的 output 也是 100，等于要求 20/2 = 1。）
	for _, c := range []struct{ model, side string }{
		{"cheap-in", "input"}, {"cheap-out", "output"},
	} {
		si := idx[c.model]
		ax := si.Input
		if c.side == "output" {
			ax = si.Output
		}
		if ax.Verdict != IndexOK || ax.Index == nil || *ax.Index != 100 {
			t.Errorf("%s %s: want verdict ok and index exactly 100, got %s / %v",
				c.model, c.side, ax.Verdict, ax.Index)
		}
	}
	// 跨方向：cheap-in 的 output 20/2 = 10×；cheap-out 的 input 4/0.5 = 8×。
	if got, want := *idx["cheap-in"].Output.Index, 1000.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("cheap-in output index = %.4f, want %.4f (100×20/2)", got, want)
	}
	if got, want := *idx["cheap-out"].Input.Index, 800.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("cheap-out input index = %.4f, want %.4f (100×4/0.5)", got, want)
	}
}

// TestFreeAndMissingAreRefusedNotGuessed 是用户 2026-10-06 明确指定的那一条。
//
// 免费（基准价 = 0）与未定价（NULL）**都不给指数**，且两者判词必须不同。
//
// 为什么判词必须不同：把它们折叠成同一个值，那份结算表上「这家白给」与
// 「这家我们还没问过价」就长得一样，而要动的地方完全不同（一个不用动，
// 一个要去原厂页核实）。这与迁移 826 把 NULL 和 0 分成两列是同一个理由。
func TestFreeAndMissingAreRefusedNotGuessed(t *testing.T) {
	cat := map[string]BaselinePrice{
		"baseline-cheap": {InputPer1M: f(1), OutputPer1M: f(4), Currency: "USD"},
		"free-model":     {InputPer1M: f(0), OutputPer1M: f(0), Currency: "USD"},
		"unpriced-model": {InputPer1M: nil, OutputPer1M: nil, Currency: "USD"},
		"half-priced":    {InputPer1M: f(0), OutputPer1M: f(8), Currency: "USD"},
	}
	idx := BuildSettlementIndex(cat)

	free := idx["free-model"]
	if free.Input.Index != nil {
		t.Errorf("free-model input index = %v, want nil — 0 is an undefined divisor, and "+
			"recording a number here merges \"free\" with \"not priced yet\"", *free.Input.Index)
	}
	if free.Input.Verdict != IndexFreeBaseline {
		t.Errorf("free-model input verdict = %q, want %q", free.Input.Verdict, IndexFreeBaseline)
	}

	unpriced := idx["unpriced-model"]
	if unpriced.Input.Index != nil || unpriced.Input.Verdict != IndexNoBaseline {
		t.Errorf("unpriced-model input = %v / %q, want nil / %q",
			unpriced.Input.Index, unpriced.Input.Verdict, IndexNoBaseline)
	}

	// ★ 判词必须可区分 —— 这是本判据最容易被「顺手简化」掉的一半。
	if free.Input.Verdict == unpriced.Input.Verdict {
		t.Errorf("free and unpriced share the verdict %q; they are different facts with "+
			"different remedies and must not collapse into one", free.Input.Verdict)
	}

	// ★ 一档免费、另一档有价：两个方向**各自**判，不是一条结论。
	half := idx["half-priced"]
	if half.Input.Verdict != IndexFreeBaseline {
		t.Errorf("half-priced input verdict = %q, want %q", half.Input.Verdict, IndexFreeBaseline)
	}
	if half.Output.Verdict != IndexOK {
		t.Errorf("half-priced output verdict = %q, want %q — a free input side does not make "+
			"the output side unpriceable", half.Output.Verdict, IndexOK)
	}
	if half.Settled {
		t.Error("half-priced reports Settled=true while one side is free — the flag must be " +
			"all-sides, or settlement will price a token direction that has no price")
	}
}

// TestAFreeModelCannotBecomeTheBase 是上面那条的反向承重。
//
// 把免费模型放进样本，看它会不会被选成基数。若 cheapestBaseline 忘了跳过 0，
// 免费模型当 100 基准 ⇒ 其余所有模型的指数全部变成 0，而**每个判词都还是
// "ok"** —— 那是最像成功的一种错。
func TestAFreeModelCannotBecomeTheBase(t *testing.T) {
	cat := map[string]BaselinePrice{
		"aaa-free":       {InputPer1M: f(0), OutputPer1M: f(0), Currency: "USD"},
		"real-cheap":     {InputPer1M: f(2), OutputPer1M: f(6), Currency: "USD"},
		"real-expensive": {InputPer1M: f(10), OutputPer1M: f(30), Currency: "USD"},
	}
	idx := BuildSettlementIndex(cat)

	if got := idx["real-cheap"].Input.BaselineModel; got != "real-cheap" {
		t.Fatalf("input base = %q, want %q — a free model must never be the 100 base "+
			"(it would drive every other index to 0 while every verdict still says ok)", got, "real-cheap")
	}
	exp := idx["real-expensive"]
	if exp.Input.Verdict != IndexOK || exp.Input.Index == nil || *exp.Input.Index <= 0 {
		t.Fatalf("real-expensive input = %v / %q, want a positive ok index", exp.Input.Index, exp.Input.Verdict)
	}
}

// TestAnEmptyDirectionIsItsOwnVerdict 覆盖「这个方向一个价都没有」。
//
// 它与「就它没价」是不同的形状，要动的地方也不同：前者是采集器整体失灵
// （该方向 0 条），后者是单个模型待补。折叠成同一个判词会让一次采集失败
// 看起来像十几条待办。
func TestAnEmptyDirectionIsItsOwnVerdict(t *testing.T) {
	cat := map[string]BaselinePrice{
		// 所有人都只有 input 价 ⇒ output 方向没有任何基数。
		"a": {InputPer1M: f(1), OutputPer1M: nil, Currency: "USD"},
		"b": {InputPer1M: f(3), OutputPer1M: nil, Currency: "USD"},
	}
	idx := BuildSettlementIndex(cat)

	if got := idx["a"].Output.Verdict; got != IndexNoUniverse {
		t.Errorf("output verdict = %q, want %q — the whole direction is empty, which is a "+
			"collection failure, not a per-model gap", got, IndexNoUniverse)
	}
	// input 侧仍应正常可结算。
	if idx["a"].Input.Verdict != IndexOK || *idx["a"].Input.Index != 100 {
		t.Errorf("a input = %v / %q, want 100 / ok — an empty output side must not poison input",
			idx["a"].Input.Index, idx["a"].Input.Verdict)
	}
	// 而「就它没有 output 价」在有基数时是另一条判词。
	cat2 := map[string]BaselinePrice{
		"has-output": {InputPer1M: f(1), OutputPer1M: f(4), Currency: "USD"},
		"no-output":  {InputPer1M: f(2), OutputPer1M: nil, Currency: "USD"},
	}
	idx2 := BuildSettlementIndex(cat2)
	if got := idx2["no-output"].Output.Verdict; got != IndexNoBaseline {
		t.Errorf("no-output verdict = %q, want %q — with a base present this is a per-model gap",
			got, IndexNoBaseline)
	}
	if idx2["no-output"].Output.Verdict == idx2["a"].Output.Verdict {
		t.Error("a per-model gap and an empty universe produced the same verdict")
	}
}

// TestCurrencyMismatchIsRefused 是「币种不同 ⇒ 不可结算」。
//
// 拿 CNY 的价除以 USD 的最低价，得到的指数长得极像真数字，而它是汇率的
// 产物。必须显式判成不可比，而不是给一个数了事。
func TestCurrencyMismatchIsRefused(t *testing.T) {
	cat := map[string]BaselinePrice{
		"usd-cheap": {InputPer1M: f(0.3), OutputPer1M: f(1.2), Currency: "USD"},
		"cny-model": {InputPer1M: f(2), OutputPer1M: f(8), Currency: "CNY"},
	}
	idx := BuildSettlementIndex(cat)

	si := idx["cny-model"]
	if si.Input.Index != nil {
		t.Errorf("CNY model got input index %v against a USD base — that number is an "+
			"exchange-rate artefact, not an index", *si.Input.Index)
	}
	if si.Input.Verdict != IndexCurrencyMismatch {
		t.Errorf("verdict = %q, want %q", si.Input.Verdict, IndexCurrencyMismatch)
	}
	// 同币种那条不受影响。
	if idx["usd-cheap"].Input.Verdict != IndexOK {
		t.Errorf("the USD base model itself went non-ok: %q", idx["usd-cheap"].Input.Verdict)
	}
}

// TestTheIndexIsRecomputedNotStored 钉住「指数是派生量」这条纪律。
//
// 往清单里加一个更便宜的模型，所有既有模型的指数**必须全部改变**。
// 若实现是「算一次存进表」，这条判据会红 —— 而那正是我们要防的形态：
// 新上一个更便宜的模型时，结算单上其余模型的相对关系悄无声息地错了。
func TestTheIndexIsRecomputedNotStored(t *testing.T) {
	before := BuildSettlementIndex(realCatalog())
	opusBefore := *before["claude-opus-4-5"].Input.Index

	cat := realCatalog()
	cat["brand-new-cheap"] = BaselinePrice{InputPer1M: f(0.1), OutputPer1M: f(0.4), Currency: "USD"}
	after := BuildSettlementIndex(cat)

	opusAfter := *after["claude-opus-4-5"].Input.Index
	if math.Abs(opusBefore-opusAfter) < 1e-9 {
		t.Fatalf("opus index unchanged (%v) after a cheaper model entered the catalog — the "+
			"index must be derived from the catalog every time, never snapshotted",
			opusBefore)
	}
	// 100×5/0.1 = 5000
	if math.Abs(opusAfter-5000) > 1e-6 {
		t.Errorf("opus index after = %.4f, want 5000 (100×5/0.1)", opusAfter)
	}
	// 原基数模型不再是 100，而是 300。
	if got := *after["minimax-m2"].Input.Index; math.Abs(got-300) > 1e-6 {
		t.Errorf("former base index = %.4f, want 300 (100×0.3/0.1)", got)
	}
}

// TestSamePriceTiesBreakDeterministically 让输出稳定这件事可测。
//
// 同价即同指数，所以挑谁当基数不影响任何**数字**；它只影响台账里写哪个名字。
// 但那个名字会进结算单，所以必须确定：同样的清单跑两次必须逐字相同。
func TestSamePriceTiesBreakDeterministically(t *testing.T) {
	first := BuildSettlementIndex(realCatalog())
	for i := 0; i < 20; i++ {
		next := BuildSettlementIndex(realCatalog())
		for name, a := range first {
			b, ok := next[name]
			if !ok {
				t.Fatalf("%s vanished on run %d — Go map iteration order leaked into the output", name, i)
			}
			if a.Input.BaselineModel != b.Input.BaselineModel {
				t.Fatalf("run %d: %s input base model flipped from %q to %q among equal prices",
					i, name, a.Input.BaselineModel, b.Input.BaselineModel)
			}
		}
	}
}

// TestEmptyCatalogIsEmptyNotAFullTable 覆盖空输入。
//
// 空清单必须产出**空**索引，而不是「每个模型都 NoUniverse」或一条都不产出
// 却报 ok。前者会让人以为表已填满。
func TestEmptyCatalogIsEmptyNotAFullTable(t *testing.T) {
	idx := BuildSettlementIndex(map[string]BaselinePrice{})
	if len(idx) != 0 {
		t.Errorf("empty catalog produced %d entries, want 0", len(idx))
	}
}
