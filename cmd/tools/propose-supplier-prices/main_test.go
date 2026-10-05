package main

// 判据：propose-supplier-prices。
//
// # 这份判据钉的是什么
//
// 工具的价值不在「能输出几个价」，而在**它对不能定价的那些行说了什么**。
// 实测：可路由 per_token 绑定里有价的是 0 条，而观察源只覆盖 7/39 家 provider
// （占绑定量 46.8%）。⇒ 多数行注定进不了 accepted[]。
//
// ★ 因此**「没进 accepted[]」与「成本是 0」必须能区分开**，否则这份草稿就是把
// 「没量到」说成「不要钱」。下面每个用例都为此存在。
//
// # 六条 teeth 分别回退
//
//	V1 去掉「重复 offer_id 全拒」        ⇒ 同一行被写两次
//	V2 去掉条件价判定（tiers / 阈值）     ⇒ 阶梯价被压平成基础价（**最危险**）
//	V3 去掉 provider 键的大小写/空白归一   ⇒ 真实 provider 名一个都匹配不上
//	V4 去掉「全零价单独成族」            ⇒ 免费模型被当成有价写进价格列
//	V5 去掉 parseModelPrice 里的「未知 cost 键」扫描 ⇒ 未建模的条件价被静默丢弃
//	V6 把 -currency 校验去掉             ⇒ 币种缺失被放行（回到兜底 USD 的老毛病）

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func fl(v float64) *float64 { return &v }

// nestedTier builds a tier entry in the shape the live payload actually uses:
// the condition lives under a nested "tier" object. Writing the fixture by
// hand to match the struct is how the empty-condition bug slipped through, so
// the shape now has exactly one constructor and it mirrors the source.
func nestedTier(kind string, size float64) Tier {
	var t Tier
	t.Input, t.Output = fl(4), fl(12)
	t.Tier.Type = kind
	t.Tier.Size = &size
	return t
}

// obsFixture 建一个观察源，形态与实抓 payload 同构。
func obsFixture() Observation {
	return Observation{
		SourceURL: "https://models.dev/api.json",
		Providers: map[string]map[string]ObservedPrice{
			"openrouter": {
				// 可接受
				"vendor/model-a": {Input: fl(1.5), Output: fl(7.5), CacheRead: fl(0.15)},
				// 只有缓存价 ⇒ 计费方式事实，不是价格
				"vendor/only-cache": {CacheRead: fl(0.2)},
				// 阶梯价 ⇒ 定价决策
				"vendor/tiered": {Input: fl(2), Output: fl(6), Tiers: []Tier{
					nestedTier("context", 200000),
				}},
				// 超过阈值另一个价
				"vendor/long-ctx": {Input: fl(2), Output: fl(6), ContextOver: map[string]json.RawMessage{
					"context_over_200k": json.RawMessage(`{"input":4,"output":12}`),
				}},
				// 三个价全 0，但 cost 对象存在 ⇒ 计费方式事实
				"vendor/free-ish": {Input: fl(0), Output: fl(0)},
				// 有条目但根本没有 cost 对象 ⇒ 源不公开它的价
				"vendor/no-cost": {CostAbsent: true},
			},
			// 大小写不同的 provider 键（真实源里都有小写 ASCII 键）
			"nvidia": {"nvidia/some-model": {Input: fl(0.3), Output: fl(0.6)}},
		},
	}
}

func allOffers() []Offer {
	return []Offer{
		{OfferID: 1, Provider: "openrouter", Model: "vendor/model-a"},    // accepted
		{OfferID: 2, Provider: "openrouter", Model: "vendor/only-cache"}, // published_price_is_zero
		{OfferID: 3, Provider: "openrouter", Model: "vendor/tiered"},     // price_is_conditional
		{OfferID: 4, Provider: "openrouter", Model: "vendor/long-ctx"},   // price_is_conditional
		{OfferID: 5, Provider: "openrouter", Model: "vendor/free-ish"},   // published_price_is_zero
		{OfferID: 6, Provider: "NVIDIA", Model: "nvidia/some-model"},     // accepted (normKey)
		{OfferID: 7, Provider: "apiclaude", Model: "claude-x"},           // provider_not_in_source
		{OfferID: 8, Provider: "openrouter", Model: "vendor/absent"},     // model_not_listed_for_provider
		{OfferID: 10, Provider: "openrouter", Model: "vendor/no-cost"},   // no_price_published
		{OfferID: 9, Provider: "openrouter", Model: "vendor/dup"},        // duplicate_offer_id_in_input
		{OfferID: 9, Provider: "openrouter", Model: "vendor/dup"},
	}
}

func resolveFixture() Draft {
	return Resolve(allOffers(), obsFixture(), "2026-10-05T00:00:00Z", "USD")
}

func reasonsOf(d Draft) map[int64]string {
	m := map[int64]string{}
	for _, l := range d.Rejected {
		m[l.OfferID] = l.Reason
	}
	return m
}

// TestResolve_每族各有归属且后果不同 —— 主判据：逐行断言族键。
func TestResolve_每族各有归属且后果不同(t *testing.T) {
	d := resolveFixture()
	got := reasonsOf(d)

	want := map[int64]string{
		2:  "published_price_is_zero",
		3:  "price_is_conditional",
		4:  "price_is_conditional",
		5:  "published_price_is_zero",
		7:  "provider_not_in_source",
		8:  "model_not_listed_for_provider",
		9:  "duplicate_offer_id_in_input",
		10: "no_price_published",
	}
	for id, fam := range want {
		if got[id] != fam {
			t.Errorf("offer %d 应当归入 %q，实得 %q", id, fam, got[id])
		}
	}
	if len(d.Accepted) != 2 {
		t.Fatalf("应当只接受 2 条（offer 1 与 6），实得 %d 条：%+v", len(d.Accepted), d.Accepted)
	}
	for _, l := range d.Accepted {
		if l.OfferID == 1 && (positive(l.UnitPriceInPer1M) != 1.5 || positive(l.UnitPriceOutPer1M) != 7.5) {
			t.Errorf("offer 1 的价应原样透传 1.5/7.5，实得 %v/%v",
				f64(l.UnitPriceInPer1M), f64(l.UnitPriceOutPer1M))
		}
	}
	// 重复的 offer_id 必须**两条都被拒**，不是只拒第二条 ——
	// 挑一个赢家会让输出依赖文件顺序。
	dupRows := 0
	for _, l := range d.Rejected {
		if l.OfferID == 9 {
			dupRows++
		}
	}
	if dupRows != 2 {
		t.Errorf("重复 offer_id 应当两行都进 rejected[]，实得 %d 行", dupRows)
	}
}

// TestResolve_每条被拒的行都带可行动的族解释 —— 「为什么没定价」必须可查。
func TestResolve_每条被拒的行都带可行动的族解释(t *testing.T) {
	d := resolveFixture()
	for k, why := range d.FamilyWhy {
		if strings.TrimSpace(why) == "" {
			t.Errorf("族 %q 的解释为空", k)
		}
	}
	for _, l := range d.Rejected {
		if d.FamilyWhy[l.Reason] == "" {
			t.Errorf("offer %d 的族 %q 在 FamilyWhy 里没有解释", l.OfferID, l.Reason)
		}
	}
}

// TestResolve_每条被拒的行都带**逐行**证据 —— 族键之外还要有「源到底说了什么」。
//
// ★ 这条是首跑真数据后加的：只有族键时，评审者拿到 "price_is_conditional" 却不知道
// 源发布了几个档、按什么条件 —— 于是必须重跑工具才能行动。判据钉住「族键 + 证据」
// 两样都在。
func TestResolve_每条被拒的行都带逐行证据(t *testing.T) {
	d := resolveFixture()
	for _, l := range d.Rejected {
		if strings.TrimSpace(l.Detail) == "" {
			t.Errorf("offer %d（族 %q）没有逐行证据，评审者无法据此行动", l.OfferID, l.Reason)
		}
	}
	// 条件价那一族的证据必须说出**几个档**与条件，否则等于没说。
	// 每种条件形态各自要说出**自己**的条件：tiers 形态给类型+阈值，
	// threshold 形态给它命中的那个键。两种形态用同一个断言会逼着其中一种
	// 写出不属于它的措辞 —— 那正是「措辞不可核实」的来源。
	wantDetail := map[string]string{
		"vendor/tiered":   "context/200000",
		"vendor/long-ctx": "context_over_200k",
	}
	for _, l := range d.Rejected {
		if l.Reason != "price_is_conditional" {
			continue
		}
		want, ok := wantDetail[l.Model]
		if !ok {
			t.Errorf("未预期的条件价样本 %q —— 补上它的期望措辞，否则这条判据对它无效", l.Model)
			continue
		}
		if !strings.Contains(l.Detail, want) {
			// ★ 这条在第一版是 `Contains(detail, "context")`，而夹具的 tiers 是我
			// 照着**自己的 struct** 写的扁平形状 —— 于是真载荷里嵌套在
			// `tier` 对象下的 type/size 一个都读不到，detail 变成
			// "1 conditional tier(s) (first: )"，判据照样绿。
			// 现在要求它说出**确切的**条件。
			t.Errorf("模型 %s 的逐行证据必须说出条件 %q，实得 %q", l.Model, want, l.Detail)
		}
	}
}

// TestFamilies_不许有人偷偷用模糊匹配把名字对上 —— 纯文本 ratchet。
//
// 实测 2026-10-05：115 条 `model_not_listed_for_provider` 里 114 条是 nvidia，
// **去掉 vendor 前缀后 0 个近似名** ⇒ 源的目录覆盖不足，归一化救不了。
// 但其中确有两组**看起来**像改名/换版：
// `nvidia/llama-3.1-nemoguard-8b-content-safety` vs 源里的
// `nvidia/llama-3.1-nemotron-safety-guard-8b-v3`，`riva-translate-4b-instruct-v2`
// vs `…-v1.1`。仓自己的身份表也不知道（model_aliases 无这两行，
// model_name_mapping 只把每个名字各自映射到去掉前缀的自己）。
//
// ⇒ 族的解释里**必须**带着这条禁令。若哪天有人「顺手」加一层模糊匹配，
// 这条 ratchet 会挡住：那等于把没核实过的数字写进计费路径。
func TestFamilies_不许有人偷偷用模糊匹配把名字对上(t *testing.T) {
	why := families["model_not_listed_for_provider"].Why
	for _, must := range []string{"不要模糊匹配", "model_aliases", "身份判定"} {
		if !strings.Contains(why, must) {
			t.Errorf("model_not_listed_for_provider 的解释必须包含 %q，实得：%s", must, why)
		}
	}
	// 族解释也不许给出「自动改名对齐」这种路径。
	for _, banned := range []string{"自动匹配", "模糊匹配后填价"} {
		if strings.Contains(why, banned) && !strings.Contains(why, "不要模糊匹配") {
			t.Errorf("族解释不得建议自动对齐：%s", why)
		}
	}
}

// TestFamilies_每族都可达 —— 族表守卫。
//
// 为什么要有：新增一族却没有任何代码路径能产生它，它就会成为一行永远不会出现的
// 说明文字，而「族表与代码一致」正是别的族表守卫已经证明过的可测形状。
func TestFamilies_每族都可达(t *testing.T) {
	d := resolveFixture()
	produced := map[string]bool{}
	for _, l := range d.Rejected {
		produced[l.Reason] = true
	}
	for _, k := range familyKeys() {
		if !produced[k] {
			t.Errorf("族 %q 在夹具里从未被产生 —— 要么代码路径不存在，要么夹具缺样本", k)
		}
		if _, ok := families[k]; !ok {
			t.Errorf("族 %q 出现在产物里但不在族表里", k)
		}
	}
}

// ★ TestResolve_条件价绝不被压平 —— 本工具最危险的一条。
//
// 若把阶梯价的基础值当成价格写进价格列，长上下文流量会被**系统性少计费**，
// 而且草稿里看不出任何异常：它有一行、有数、看起来完全正常。
func TestResolve_条件价绝不被压平(t *testing.T) {
	d := resolveFixture()
	for _, l := range d.Accepted {
		switch l.Model {
		case "vendor/tiered", "vendor/long-ctx":
			t.Fatalf("模型 %s 的价是有条件的，绝不能进 accepted[]（实得 %v/%v）",
				l.Model, f64(l.UnitPriceInPer1M), f64(l.UnitPriceOutPer1M))
		}
	}
	// 而且两者的 reason 都必须指向同一族。
	for _, l := range d.Rejected {
		if (l.Model == "vendor/tiered" || l.Model == "vendor/long-ctx") && l.Reason != "price_is_conditional" {
			t.Errorf("模型 %s 应归 price_is_conditional，实得 %q", l.Model, l.Reason)
		}
	}
}

// TestParseModelPrice_未知的 cost 键不许被静默丢掉。
//
// 实测活载荷里 grok-4.7 带 `context_over_200k`（2026-10-05）。用定长 struct 解析
// cost 会把这类键悄悄扔掉，工具随后把基础价当成价格写出去 —— 这一条就是防它。
func TestParseModelPrice_未知的_cost_键不许被静默丢掉(t *testing.T) {
	raw := json.RawMessage(`{
		"name":"x-ai/grok-4.7",
		"cost":{"input":2,"output":6,"cache_read":0.5,
		        "context_over_200k":{"input":4,"output":12}}}`)
	p, ok := parseModelPrice(raw)
	if !ok {
		t.Fatal("应当解析出一条价格")
	}
	if why := p.Conditional(); why == "" {
		t.Fatal("context_over_200k 属于未建模的条件价，必须被 Conditional() 看见")
	}
	// 基础价仍在，但 Conditional() 非空 ⇒ Resolve 会拒。
	if positive(p.Input) != 2 {
		t.Errorf("基础价应被保留（供判词引用），实得 %v", f64(p.Input))
	}

	// tiers 形态
	raw2 := json.RawMessage(`{"cost":{"input":2,"output":6,
		"tiers":[{"input":4,"output":12,"tier":{"type":"context","size":200000}}]}}`)
	_ = raw2
	p2, ok := parseModelPrice(raw2)
	if !ok {
		t.Fatal("应当解析出一条价格")
	}
	if len(p2.Tiers) != 1 || p2.Conditional() == "" {
		t.Errorf("tiers 形态必须被 Conditional() 看见，实得 %d 阶", len(p2.Tiers))
	}

	// 纯标量：必须**不**被判成条件价，否则每一条都会被无理由拒掉。
	raw3 := json.RawMessage(`{"cost":{"input":1,"output":2}}`)
	p3, _ := parseModelPrice(raw3)
	if p3.Conditional() != "" {
		t.Errorf("纯标量价不该被判成条件价，实得 %q", p3.Conditional())
	}
}

// TestParseModelPrice_有条目但无 cost 的模型不许被丢掉。
//
// ★ 这是一条真缺陷的判据：初版在这里 `continue` 掉了没有 cost 对象的模型，
// 于是它在 Resolve 里被报成「该 provider 下没有这个模型」—— 把「价没公开」
// 说成了「模型不存在」，而这两件事的下一步完全不同。
func TestParseModelPrice_有条目但无_cost_的模型不许被丢掉(t *testing.T) {
	raw := json.RawMessage(`{"name":"vendor/quiet-model","id":"quiet"}`)
	p, ok := parseModelPrice(raw)
	if !ok {
		t.Fatal("没有 cost 对象的模型仍应被保留（ok=false 会让它被误报成「没有这个模型」）")
	}
	if !p.CostAbsent {
		t.Error("CostAbsent 必须为 true")
	}
	if p.Conditional() != "" {
		t.Errorf("没有 cost 不等于条件价，实得 %q", p.Conditional())
	}
}

// TestResolve_空草稿必须能自辩 —— 「没量到」不等于「不要钱」。
func TestResolve_空草稿必须能自辩(t *testing.T) {
	// 一个 provider 都不在源里
	offers := []Offer{{OfferID: 1, Provider: "apiclaude", Model: "claude-x"}}
	d := Resolve(offers, obsFixture(), "2026-10-05T00:00:00Z", "USD")
	if d.AcceptedCount != 0 {
		t.Fatalf("本用例的前提是接受 0 条，实得 %d", d.AcceptedCount)
	}
	if !strings.Contains(d.Why, "NOT evidence") {
		t.Errorf("空草稿的 Why 必须明说「这不证明成本是 0」，实得 %q", d.Why)
	}
	if d.FamilyCounts["provider_not_in_source"] != 1 {
		t.Errorf("family_counts 应记录 1 条 provider_not_in_source，实得 %v", d.FamilyCounts)
	}

	// 完全没有 offer
	empty := Resolve(nil, obsFixture(), "2026-10-05T00:00:00Z", "USD")
	if !strings.Contains(empty.Why, "input problem") {
		t.Errorf("offer 数为 0 时 Why 必须指向输入问题，实得 %q", empty.Why)
	}

	// 部分成功时也必须说清「缺席 ≠ 免费」
	part := Resolve([]Offer{
		{OfferID: 1, Provider: "openrouter", Model: "vendor/model-a"},
		{OfferID: 2, Provider: "apiclaude", Model: "claude-x"},
	}, obsFixture(), "2026-10-05T00:00:00Z", "USD")
	if !strings.Contains(part.Why, "NOT because its cost is zero") {
		t.Errorf("部分成功时 Why 仍须区分「没量到」与「成本是 0」，实得 %q", part.Why)
	}
}

// TestValidateFlags_币种无默认值 —— 源里没有币种字段这件事不许被兜底掉。
func TestValidateFlags_币种无默认值(t *testing.T) {
	if err := validateFlags("f.csv", "2026-10-05T00:00:00Z", ""); err == nil {
		t.Error("缺 -currency 必须被拒：源里没有币种字段，兜底 USD 就是把没核实的东西当核实过的")
	} else if !strings.Contains(err.Error(), "no USD default") {
		t.Errorf("错误文案要点明「没有 USD 默认值」，实得 %q", err.Error())
	}
	if err := validateFlags("f.csv", "", "USD"); err == nil {
		t.Error("缺 -fetched-at 必须被拒")
	}
	if err := validateFlags("", "2026-10-05T00:00:00Z", "USD"); err == nil {
		t.Error("缺 -offers 必须被拒")
	}
	if err := validateFlags("f.csv", "2026-10-05T00:00:00Z", "CNY"); err != nil {
		t.Errorf("三者齐备时不应被拒：%v", err)
	}
}

// TestImportCSV_形状必须与 admin 的 pricingImport 对得上。
func TestImportCSV_形状必须与_admin_的_pricingImport_对得上(t *testing.T) {
	d := resolveFixture()
	path := filepath.Join(t.TempDir(), "import.csv")
	if err := writeImportCSV(path, d); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(b)
	// pricingImport 要求 offer_id 列，其余列它按名字可选读取。
	for _, col := range []string{"offer_id", "unit_price_in_per_1m", "unit_price_out_per_1m",
		"cache_read_price_per_1m", "currency"} {
		if !strings.Contains(got, col) {
			t.Errorf("CSV 表头缺 %q 列：admin/pricing.go pricingImport 按列名读取", col)
		}
	}
	// 只有被接受的行进 CSV：被拒的**绝不能**出现在可导入文件里。
	// CSV 里是 offer_id，所以按 id 查而不是按模型名。
	for _, banned := range []string{"2", "3", "4", "5", "7", "8", "10"} {
		if strings.Contains(got, "\n"+banned+",") {
			t.Errorf("offer %s 被拒却出现在导入 CSV 里 —— 那是把定价决策偷偷变成了默认值", banned)
		}
	}
	if !strings.Contains(got, "\n1,") || !strings.Contains(got, "\n6,") {
		t.Errorf("接受的两条（offer 1 与 6）应出现在 CSV 里：\n%s", got)
	}
	if strings.Contains(got, "vendor/tiered") {
		t.Error("模型名不该出现在以 offer_id 为键的 CSV 里")
	}
	if !strings.Contains(got, "USD") {
		t.Error("CSV 必须带上 operator 声明的币种")
	}
	// 行数 = 表头 + 2 条接受
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Errorf("CSV 应为 表头+2 行，实得 %d 行：\n%s", len(lines), got)
	}
}

// TestReadOffers_表头缺列要报错 —— 输入契约也是承重的。
func TestReadOffers_表头缺列要报错(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.csv")
	if err := os.WriteFile(bad, []byte("offer_id,provider_code\n1,openrouter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOffers(bad); err == nil {
		t.Error("缺 raw_model_name 列必须报错，否则每行都会被当成没有模型名")
	}
	good := filepath.Join(dir, "good.csv")
	if err := os.WriteFile(good, []byte("offer_id,provider_code,raw_model_name\n1,openrouter,vendor/model-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offers, err := readOffers(good)
	if err != nil {
		t.Fatalf("合法输入被拒：%v", err)
	}
	if len(offers) != 1 || offers[0].OfferID != 1 || offers[0].Model != "vendor/model-a" {
		t.Errorf("解析结果不对：%+v", offers)
	}
}

// ── 接缝契约（2026-10-06）──────────────────────────────────────────────
//
// 上面那条「形状必须与 pricingImport 对得上」只查了**表头名字**，而真正的
// 接缝风险有两个更细的形态，都没有被任何判据覆盖：
//
// ① **未知列被静默忽略**。pricingImport 按**列名**挑它认识的 6 列。
//    工具多写一列（哪怕拼错、或者以后给它加了 `cache_write_price_per_1m`
//    之外的什么）⇒ 那一列在落地时**不报错、不生效**，价格就这么消失了。
//    「产出格式的工具有判据」≠「消费这个格式的端点认识它」。
// ② **nil 价格渲染成什么**。`f64(nil)` 必须渲染成**空串**。若渲染成 `0`，
//    落地端会把它当成一个**真的 0 价**写进库里 —— 而「per_token 有价但
//    等于 0」正是第 13 条健康检查 supplier_price_missing_from_cost 专门
//    盯的那种状态（真库实测过 0 条「可路由 + per_token + 有价」）。
//    也就是说：一次渲染改动就能凭空造出那批告警，且看不出来源。

// TestImportCSV_列名必须被落地端认识 钉 ①：工具的表头 ⊆ 落地端认识的列。
func TestImportCSV_列名必须被落地端认识(t *testing.T) {
	d := resolveFixture()
	path := filepath.Join(t.TempDir(), "import.csv")
	if err := writeImportCSV(path, d); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	header, err := csv.NewReader(f).Read()
	if err != nil {
		t.Fatalf("read header: %v", err)
	}

	// 从落地端源码里抽出它认识的列（那个 fields map 的键）。
	// ★ 路径要用 <包目录>/../../admin/pricing.go，不是 ../../admin/pricing.go。
	//   `go test` 的工作目录是**包目录**（cmd/tools/propose-supplier-prices），
	//   从那里往上两级是 cmd/，所以要三级。2026-10-06 实测踩到：
	//   两级写出来的路径不存在，而报错是「no such file」—— 看着像文件搬走了，
	//   实际是相对层级算错了一级。
	src, err := os.ReadFile("../../../admin/pricing.go")
	if err != nil {
		t.Fatalf("read admin/pricing.go: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (h *Handler) pricingImport")
	if i < 0 {
		t.Fatal("pricingImport not found — the consumer of this CSV moved or was renamed")
	}
	rest := body[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}
	known := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*"([a-z0-9_]+)":\s*"(float|string)",\s*$`).FindAllStringSubmatch(rest, -1) {
		known[m[1]] = true
	}
	if len(known) == 0 {
		t.Fatalf("could not extract pricingImport's known columns from the source — " +
			"this judgment is self-referential, fix the extraction (or the handler) first")
	}
	for _, col := range header {
		if col == "offer_id" {
			continue // 唯一必需列，由 handler 单独校验
		}
		if !known[col] {
			t.Errorf("工具写出了落地端不认识的列 %q —— pricingImport 按列名挑选，"+
				"这一列会在导入时**静默丢失**，不报错也不生效。known=%v", col, keysOf(known))
		}
	}
	// 反向：落地端认识的列里，工具至少要用上价格三列（否则改了落地端也不会被发现）。
	for _, mustUse := range []string{"unit_price_in_per_1m", "unit_price_out_per_1m"} {
		used := false
		for _, col := range header {
			if col == mustUse {
				used = true
			}
		}
		if !used {
			t.Errorf("工具不再输出 %q，而落地端仍然认识它 —— 两侧漂移了", mustUse)
		}
	}
}

// TestF64_nil必须渲染成空串 钉 ②。
func TestF64_nil必须渲染成空串(t *testing.T) {
	if got := f64(nil); got != "" {
		t.Errorf("f64(nil)=%q, want \"\" —— 落地端把空串当「这一列不参与更新」，"+
			"而 0 会被当成**真的 0 价**写进库（那正是 supplier_price_missing_from_cost 盯的状态）", got)
	}
	if got := f64(ptr(3.5)); got != "3.5" {
		t.Errorf("f64(3.5)=%q want 3.5", got)
	}
	// 零是**有意义的**价格（供应商真的免费），不能和 nil 混同。
	if got := f64(ptr(0.0)); got != "0" {
		t.Errorf("f64(0)=%q want \"0\" —— 供应商真的免费时，0 必须能被表达出来", got)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func ptr(f float64) *float64 { return &f }
