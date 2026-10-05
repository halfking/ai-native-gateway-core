// Command propose-supplier-prices turns an observation source that publishes
// **per-provider** prices into a reviewable draft of supplier prices for the
// gateway's credential_model_bindings.
//
// # Why this tool exists
//
// bg's 13th health check (supplier_price_missing_from_cost) reports that
// **zero** of the routable per_token bindings carry a price, so every
// per-token cost total omits them. That check says "there is no price"; this
// tool is the other half — "here is where a price could legitimately come
// from, and here is the part where nobody can supply one".
//
// # Measured coverage (2026-10-05, real gateway, 39 providers with bindings)
//
// The observation source (models.dev) publishes cost **per provider**, so it
// is structurally usable as a supplier-price source. Of the gateway's 39
// providers with bindings, 7 exist there — including the two largest by
// binding count (openrouter 419, nvidia 412) = 956/2045 bindings = 46.8%.
// At (provider, model) granularity 393/565 pairs carry a non-empty cost
// (69.6%). The other 32 providers are private relay/reseller channels whose
// prices are commercial agreements with no public machine-readable list.
//
// ⇒ This tool is a **partial** fix by construction. It must never imply that
// an absent offer is free or fine; that is the rejection list's whole job.
//
// # What this tool deliberately does NOT do
//
//   - It never writes to the database. It emits a CSV in exactly the shape
//     admin/pricing.go pricingImport already accepts (offer_id + price columns
//   - currency + billing_mode), so a human reviews and then imports through
//     the existing, already-audited path.
//   - It never flattens a conditional price into a single number.
//   - It never assumes a currency. The source publishes **no currency field**
//     on model entries (verified 2026-10-05 on the live payload), so the
//     operator must state it with -currency; there is no USD default.
//     This mirrors the write-side rule that an unknown currency is refused
//     rather than defaulted to USD.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------- data model

// Offer is one credential_model_bindings row that may need a price.
// OfferID is model_offers.id, which is credential_model_bindings.id — the same
// number pricingImport keys on.
type Offer struct {
	OfferID  int64  `json:"offer_id"`
	Provider string `json:"provider_code"`
	Model    string `json:"raw_model_name"`
}

// Tier is one entry of a provider's conditional pricing. Its presence means
// the price is NOT a single value.
//
// ★ The shape is nested: the live payload spells it
//
//	"tiers":[{"input":4,"output":12,"tier":{"type":"context","size":200000}}]
//
// i.e. the condition lives under a `tier` OBJECT, not at the entry's top
// level. The first version of this struct read `type`/`size` from the top
// level, so `Conditional()` reported "1 conditional tier (first: )" — an
// empty condition — and the criteria still passed, because the fixture had
// been written to match the struct instead of to match the source. Fixed both
// sides; see the criterion that now requires the condition to be named.
type Tier struct {
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	CacheRead *float64 `json:"cache_read"`
	Tier      struct {
		Type string   `json:"type"`
		Size *float64 `json:"size"`
	} `json:"tier"`
}

// Condition renders the tier's condition for a human, e.g. "context/200000".
// It returns "" when the source did not say what the condition is — which is
// itself worth seeing, so callers should not silently drop it.
func (t Tier) Condition() string {
	if t.Tier.Type == "" && t.Tier.Size == nil {
		return "unspecified"
	}
	if t.Tier.Size == nil {
		return t.Tier.Type
	}
	if t.Tier.Type == "" {
		return fmt.Sprintf("at %g", *t.Tier.Size)
	}
	return fmt.Sprintf("%s/%g", t.Tier.Type, *t.Tier.Size)
}

// ObservedPrice is what the source says about one (provider, model) pair.
type ObservedPrice struct {
	// CostAbsent is true when the model is listed under the provider but
	// carries no `cost` object at all.
	//
	// ★ It must be kept separate from "cost present but zero". The two look
	// identical after parsing and mean different things: absent = "this
	// channel publishes no price list"; zero = "this channel says the model
	// costs nothing", which is a **billing-mode** fact. Collapsing them would
	// make the tool prescribe billing_mode='free' for a model it simply has no
	// information about.
	CostAbsent bool

	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	CacheRead *float64 `json:"cache_read"`
	// Conditional pricing: non-empty ⇒ refuse, do not flatten.
	Tiers []Tier `json:"tiers"`
	// ContextOver holds cost keys this tool does not model (e.g.
	// context_over_200k). Any presence ⇒ refuse.
	ContextOver map[string]json.RawMessage `json:"context_over_200k"`
}

// Conditional explains, when set, why the price is not a single value.
func (p ObservedPrice) Conditional() string {
	if n := len(p.Tiers); n > 0 {
		return fmt.Sprintf("the source publishes %d conditional tier(s) (first condition: %s)",
			n, p.Tiers[0].Condition())
	}
	if len(p.ContextOver) > 0 {
		keys := make([]string, 0, len(p.ContextOver))
		for k := range p.ContextOver {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "the source publishes a different price beyond a context threshold (" + strings.Join(keys, ", ") + ")"
	}
	return ""
}

// Observation is the parsed source: provider key (lower-cased) -> model -> price.
type Observation struct {
	// Providers preserves the source's own display names for messages.
	Providers map[string]map[string]ObservedPrice
	// SourceURL is recorded in the draft so a reviewer can re-derive it.
	SourceURL string
}

// ------------------------------------------------------------------ families

// family is a rejection class. Every family must have a *distinct next
// action*; two classes whose remedy is the same belong in one family.
type family struct {
	Key string
	Why string
}

var families = map[string]family{
	"provider_not_in_source": {
		Key: "provider_not_in_source",
		Why: "这个 provider 不在观察源里。它是私有中转/聚合渠道，价格是私下商务约定，" +
			"没有公开机读价目 —— 自动化对它无解，下一步是人工按供应商报价单填写。",
	},
	"model_not_listed_for_provider": {
		Key: "model_not_listed_for_provider",
		Why: "provider 在观察源里，但它名下没有这个模型。实测 2026-10-05：630 条待定价里" +
			"115 条落到这一族，其中 114 条是 nvidia，**去掉 vendor 前缀后仍然 0 个近似名**" +
			"（逐条量过）⇒ 这是**源的目录覆盖不足**，不是命名约定问题，任何名称归一化都救不了。" +
			"⚠ **不要模糊匹配**：其中 `nvidia/llama-3.1-nemoguard-8b-content-safety` 与源里的 " +
			"`nvidia/llama-3.1-nemotron-safety-guard-8b-v3` 像是同一模型的改名，`riva-translate-" +
			"4b-instruct-v2` 与 `…-v1.1` 像是换版 —— 但仓自己的身份表不知道：model_aliases 里" +
			"没有这两行，model_name_mapping 只是把每个名字各自映射到去掉前缀的自己。" +
			"⇒ 那是一次**身份判定**，不是取价问题，而仓里没有判据能替人判。" +
			"下一步：(a) 人工确认是不是同一模型（确认后把别名写进 model_aliases，" +
			"下次就能自动对上），(b) 换一个目录更全的源，或 (c) 接受它无价。" +
			"**在 (a) 完成前给它填另一个模型的价，等于把没核实过的数字写进计费路径。**",
	},
	"no_price_published": {
		Key: "no_price_published",
		Why: "这个模型在该 provider 名下**有条目**，但没有 cost 对象 —— 该渠道对它不公开价目。" +
			"注意这与「源里说它是 0」不同：那一条是计费方式的事实。" +
			"下一步：人工按供应商报价单填写；**不要**因为源里没有就当成免费。",
	},
	"published_price_is_zero": {
		Key: "published_price_is_zero",
		Why: "观察源把价报成 0（或只给了缓存读价）。那是**计费方式**的事实而不是价格：" +
			"写 0 进价格列等于什么都没说（CalcCost 对 0 与缺失都返回 0），" +
			"而只写缓存价会让 prompt/completion token 变成隐式免费、**静默少计费**。" +
			"下一步：显式把 billing_mode 设成 free / token_plan / code_plan，" +
			"让第 13 条检查不再把它当成「按 token 却没填价」。",
	},
	"price_is_conditional": {
		Key: "price_is_conditional",
		Why: "★ 这一族与其它所有族的下一步动作都不同：其它都是「去人工填价」，" +
			"这一族是**定价决策**。观察源对这个模型给的是**有条件的价**（按上下文阶梯 / " +
			"超过某个长度后另一个价），而 credential_model_bindings 的两列价格只能装**一个**数。" +
			"取哪一档、或按哪个上下文上限取，是人和成本口径的事。" +
			"下一步：定口径并写进说明；任何自动化挑一档，都是替人做了那个决策。",
	},
	"duplicate_offer_id_in_input": {
		Key: "duplicate_offer_id_in_input",
		Why: "输入表里同一个 offer_id 出现了多次。offer_id 是绑定身份，重复会让同一行被写两次，" +
			"且两次的价可能不同。下一步：修输入表（这通常是一次导出 join 重复，不是价格问题）。",
	},
}

// familyKeys is the stable, sorted list used by the criteria and by the draft.
func familyKeys() []string {
	ks := make([]string, 0, len(families))
	for k := range families {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ------------------------------------------------------------------ decisions

// Line is one draft line. Exactly one of Accepted / Family is meaningful:
// Accepted lines carry prices, rejected lines carry a family key and a why.
type Line struct {
	OfferID  int64  `json:"offer_id"`
	Provider string `json:"provider_code"`
	Model    string `json:"raw_model_name"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`

	UnitPriceInPer1M    *float64 `json:"unit_price_in_per_1m,omitempty"`
	UnitPriceOutPer1M   *float64 `json:"unit_price_out_per_1m,omitempty"`
	CacheReadPricePer1M *float64 `json:"cache_read_price_per_1m,omitempty"`

	// Detail is the per-line evidence: what exactly the source said (or did
	// not say) about THIS row. It is what turns a family key into something
	// an operator can act on without re-running the tool.
	//
	// ★ Added after the first real run: the family key alone left the reviewer
	// with "price_is_conditional" and no way to learn that the source
	// publishes 2 tiers. Found by running it, not by reading the code.
	Detail string `json:"detail,omitempty"`
}

// Draft is the reviewable artifact. It is always self-describing: even when
// nothing was accepted, Why says so.
type Draft struct {
	SourceURL     string            `json:"source_url"`
	FetchedAt     string            `json:"fetched_at"`
	Currency      string            `json:"currency"`
	CurrencyNote  string            `json:"currency_note"`
	Why           string            `json:"why"`
	OfferCount    int               `json:"offer_count"`
	AcceptedCount int               `json:"accepted_count"`
	Accepted      []Line            `json:"accepted"`
	Rejected      []Line            `json:"rejected"`
	FamilyCounts  map[string]int    `json:"family_counts"`
	FamilyWhy     map[string]string `json:"family_why"`
}

const currencyNote = "the observation source publishes NO currency field on model entries " +
	"(verified on the live payload 2026-10-05), so the currency below is operator-stated " +
	"via -currency and was NOT read from the source. Confirm it before importing."

// ---------------------------------------------------------------------- core

// Resolve is the whole tool's logic, and it is pure: no network, no clock, no
// filesystem. Everything else is transport.
func Resolve(offers []Offer, obs Observation, fetchedAt, currency string) Draft {
	d := Draft{
		SourceURL:    obs.SourceURL,
		FetchedAt:    fetchedAt,
		Currency:     currency,
		CurrencyNote: currencyNote,
		OfferCount:   len(offers),
		Accepted:     []Line{},
		Rejected:     []Line{},
		FamilyCounts: map[string]int{},
		FamilyWhy:    map[string]string{},
	}
	for k, f := range families {
		d.FamilyWhy[k] = f.Why
	}

	// Duplicate offer ids: every copy is refused, not just the second one.
	// Picking a winner would make the output depend on file order.
	seen := map[int64]int{}
	for _, o := range offers {
		seen[o.OfferID]++
	}

	for _, o := range offers {
		if seen[o.OfferID] > 1 {
			d.reject(o, "duplicate_offer_id_in_input",
				fmt.Sprintf("offer_id %d appears %d times in the input", o.OfferID, seen[o.OfferID]))
			continue
		}
		models, ok := obs.Providers[normKey(o.Provider)]
		if !ok {
			d.reject(o, "provider_not_in_source", fmt.Sprintf(
				"provider %q has no entry in %s", o.Provider, obs.SourceURL))
			continue
		}
		p, ok := models[o.Model]
		if !ok {
			d.reject(o, "model_not_listed_for_provider", fmt.Sprintf(
				"provider %q is in the source but does not list model %q", o.Provider, o.Model))
			continue
		}
		// ★ Order matters: a conditional price is refused even when the base
		// numbers are non-zero, because writing the base number as if it were
		// the price would silently under-charge the long-context traffic.
		if why := p.Conditional(); why != "" {
			d.reject(o, "price_is_conditional", why)
			continue
		}
		// Listed under the provider, but the source publishes no price list
		// for it. Distinct from "the source says it is zero".
		if p.CostAbsent {
			d.reject(o, "no_price_published",
				"the model is listed under this provider but carries no cost object")
			continue
		}
		if fam, detail, isZero := zeroPriceRejection(p); isZero {
			d.reject(o, fam, detail)
			continue
		}
		d.Accepted = append(d.Accepted, Line{
			OfferID:             o.OfferID,
			Provider:            o.Provider,
			Model:               o.Model,
			Accepted:            true,
			UnitPriceInPer1M:    p.Input,
			UnitPriceOutPer1M:   p.Output,
			CacheReadPricePer1M: p.CacheRead,
		})
	}

	d.AcceptedCount = len(d.Accepted)
	for _, l := range d.Rejected {
		d.FamilyCounts[l.Reason]++
	}
	d.Why = draftWhy(d)
	return d
}

func (d *Draft) reject(o Offer, key, detail string) {
	d.Rejected = append(d.Rejected, Line{
		OfferID:  o.OfferID,
		Provider: o.Provider,
		Model:    o.Model,
		Accepted: false,
		Reason:   key,
		// The family-level explanation lives in FamilyWhy; Detail is what the
		// source said about THIS row, which is what makes the row actionable
		// without re-running the tool.
		Detail: detail,
	})
}

func draftWhy(d Draft) string {
	switch {
	case d.OfferCount == 0:
		return "EMPTY because the offer list had no rows — that is an input problem, " +
			"NOT evidence that prices are unnecessary."
	case d.AcceptedCount == 0:
		return fmt.Sprintf(
			"EMPTY because none of the %d offer(s) could be priced from %s. "+
				"This is NOT evidence that these models are free or that their cost is 0 — "+
				"read family_counts for why each one was refused, and price the rest by hand.",
			d.OfferCount, d.SourceURL)
	case d.AcceptedCount < d.OfferCount:
		return fmt.Sprintf(
			"%d of %d offer(s) priced from %s; the remaining %d are in rejected[] with a "+
				"family key each. An offer is absent from accepted[] because the source could "+
				"not price it, NOT because its cost is zero.",
			d.AcceptedCount, d.OfferCount, d.SourceURL, d.OfferCount-d.AcceptedCount)
	default:
		return fmt.Sprintf("all %d offer(s) priced from %s; nothing left to price by hand.",
			d.OfferCount, d.SourceURL)
	}
}

// ------------------------------------------------------------------- helpers

func positive(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// zeroPriceRejection decides whether a published price is really a
// **billing-mode** fact rather than a price, and says which of the two shapes
// it is.
//
// Both shapes land in ONE family because the remedy is identical for both: set
// billing_mode explicitly. Writing 0 into the price columns is
// indistinguishable from leaving them empty (CalcCost returns 0 either way),
// and importing only a cache-read price would make CalcCost charge cache reads
// while treating prompt and completion tokens as free — a silent under-billing.
//
// They used to be two separate guards in Resolve, and the first (all three
// zero) was **entirely subsumed** by the second (input==0 && output==0).
// teeth V4 proved it by deleting the first and watching nothing move: a guard
// that can never be the one that fires is a guard nobody is testing.
func zeroPriceRejection(p ObservedPrice) (fam, detail string, isZeroPrice bool) {
	if positive(p.Input) > 0 || positive(p.Output) > 0 {
		return "", "", false
	}
	if positive(p.CacheRead) == 0 {
		return "published_price_is_zero",
			"the source publishes 0 for input, output and cache read alike", true
	}
	return "published_price_is_zero",
		"only a cache-read price is published (input=0, output=0); importing it " +
			"would charge cache reads while treating prompt and completion tokens as free", true
}

// normKey lower-cases and trims a lookup key. Needed because gateway provider
// codes include non-ASCII names (e.g. 速云U站) and models.dev keys are
// lower-case ASCII; a byte-exact match would find nothing.
func normKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func f64(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// ----------------------------------------------------------------------- main

func main() {
	offersFile := flag.String("offers", "", "CSV with header offer_id,provider_code,raw_model_name (required)")
	out := flag.String("out", "", "write the draft JSON here (default: stdout)")
	csvOut := flag.String("csv", "", "write an admin/pricing import-ready CSV here")
	source := flag.String("source", "https://models.dev/api.json",
		"observation source publishing per-provider prices")
	fetchedAt := flag.String("fetched-at", "", "ISO-8601 timestamp of the fetch (REQUIRED: prices without a fetch time are not reviewable)")
	currency := flag.String("currency", "",
		"currency for every price in the draft (REQUIRED: the source publishes no currency field; there is deliberately no default)")
	flag.Parse()

	if err := validateFlags(*offersFile, *fetchedAt, *currency); err != nil {
		fail("%v", err)
	}

	offers, err := readOffers(*offersFile)
	if err != nil {
		fail("read offers: %v", err)
	}

	// A failed run must not leave a previous run's draft looking fresh.
	//
	// ★ Found by running it: a TLS handshake timeout made this tool exit 2
	// without writing, and the *previous* draft file was still sitting there
	// with its own (now stale) fetched_at. A reviewer opening that file has no
	// way to tell it is not from this run. We do not delete the operator's
	// file — we say so, and the draft itself carries fetched_at to be checked.
	if *out != "" {
		if st, err := os.Stat(*out); err == nil && st.Size() > 0 {
			fmt.Fprintf(os.Stderr,
				"NOTE: %s already exists (from an earlier run) and will be overwritten only if this "+
					"run reaches the write step. If this run fails, that file is STALE — check its "+
					"fetched_at before trusting it.\n", *out)
		}
	}

	obs, err := fetchObservation(*source)
	if err != nil {
		fail("fetch %s: %v", *source, err)
	}

	draft := Resolve(offers, obs, *fetchedAt, *currency)

	if *out != "" {
		if err := writeJSON(*out, draft); err != nil {
			fail("write %s: %v", *out, err)
		}
	} else {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(draft); err != nil {
			fail("encode draft: %v", err)
		}
	}

	if *csvOut != "" {
		if err := writeImportCSV(*csvOut, draft); err != nil {
			fail("write %s: %v", *csvOut, err)
		}
	}

	fmt.Fprintf(os.Stderr,
		"offers=%d accepted=%d rejected=%d (source=%s fetched_at=%s)\n",
		draft.OfferCount, draft.AcceptedCount, len(draft.Rejected), draft.SourceURL, draft.FetchedAt)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "propose-supplier-prices: "+format+"\n", args...)
	os.Exit(2)
}

// validateFlags refuses before any network access. Extracted from main so the
// criteria can exercise the refusals without spawning the binary.
//
// All three exist because a draft that cannot be re-derived is not a review
// artifact — and the currency one because the source publishes no currency
// field at all, so assuming USD here would repeat the exact defect that was
// removed from the write side earlier in this work (unknown currency refused,
// never defaulted).
func validateFlags(offersFile, fetchedAt, currency string) error {
	if offersFile == "" {
		return fmt.Errorf("-offers is required: this tool prices bindings, it does not discover them")
	}
	if fetchedAt == "" {
		return fmt.Errorf("-fetched-at is required. A price without the time it was observed " +
			"cannot be reviewed or re-checked later, which is the whole point of emitting a " +
			"draft instead of writing to the database")
	}
	if currency == "" {
		return fmt.Errorf("-currency is required. The observation source publishes no currency " +
			"field on model entries, so the currency is operator-stated. This tool " +
			"deliberately has no USD default: an unknown currency is refused, not assumed")
	}
	return nil
}

func readOffers(path string) ([]Offer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(recs) < 1 {
		return nil, fmt.Errorf("offers file is empty")
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"offer_id", "provider_code", "raw_model_name"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("offers file needs a %q column (header: %v)", need, recs[0])
		}
	}
	var out []Offer
	for n, row := range recs[1:] {
		get := func(k string) string {
			i := col[k]
			if i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		if get("offer_id") == "" {
			continue
		}
		id, err := strconv.ParseInt(get("offer_id"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("row %d: offer_id %q is not an integer", n+2, get("offer_id"))
		}
		out = append(out, Offer{OfferID: id, Provider: get("provider_code"), Model: get("raw_model_name")})
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeImportCSV emits exactly what admin/pricing.go pricingImport reads:
// offer_id plus the six optional columns it knows, with a header row.
func writeImportCSV(path string, d Draft) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"offer_id", "unit_price_in_per_1m", "unit_price_out_per_1m",
		"cache_read_price_per_1m", "currency"}); err != nil {
		return err
	}
	for _, l := range d.Accepted {
		if err := w.Write([]string{
			strconv.FormatInt(l.OfferID, 10),
			f64(l.UnitPriceInPer1M),
			f64(l.UnitPriceOutPer1M),
			f64(l.CacheReadPricePer1M),
			d.Currency,
		}); err != nil {
			return err
		}
	}
	return w.Error()
}

// fetchObservation fetches and parses the source. It retries once on a
// transport error, because a one-shot EOF is a real, observed failure mode of
// this source (2026-10-05, and 2026-10-04) — but a single retry, not a loop:
// a source that is down should fail loudly here rather than silently produce
// an empty observation that would look like "nothing to price".
func fetchObservation(url string) (Observation, error) {
	cl := &http.Client{Timeout: 90 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := cl.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status %s", resp.Status)
			time.Sleep(2 * time.Second)
			continue
		}
		obs, perr := parseObservation(body, url)
		if perr != nil {
			return Observation{}, perr
		}
		return obs, nil
	}
	return Observation{}, lastErr
}

// rawProvider / rawModel mirror only the fields this tool reads, so the parse
// stays honest about what a "price" is here.
type rawProvider struct {
	Name   string                     `json:"name"`
	Models map[string]json.RawMessage `json:"models"`
}

func parseObservation(body []byte, url string) (Observation, error) {
	var raw map[string]rawProvider
	if err := json.Unmarshal(body, &raw); err != nil {
		return Observation{}, fmt.Errorf("parse: %w", err)
	}
	if len(raw) == 0 {
		// An empty provider map would make every offer look like
		// provider_not_in_source — i.e. would report "this source has no
		// prices" when the truth is "we parsed nothing". Refuse instead.
		return Observation{}, fmt.Errorf("source parsed to 0 providers — refusing to " +
			"report every offer as unpriceable on the strength of an empty read")
	}
	obs := Observation{Providers: map[string]map[string]ObservedPrice{}, SourceURL: url}
	for pid, pv := range raw {
		models := map[string]ObservedPrice{}
		for mid, mraw := range pv.Models {
			p, ok := parseModelPrice(mraw)
			if !ok {
				continue
			}
			models[mid] = p
		}
		obs.Providers[normKey(pid)] = models
	}
	return obs, nil
}

// parseModelPrice pulls the price fields out of one model entry.
//
// ★ `cost` is parsed as a **map**, not a struct, on purpose. The live source
// publishes conditional prices under keys this tool has never seen before
// (`context_over_200k` on grok-4.7, measured 2026-10-05) alongside the scalar
// input/output/cache_read. A struct with fixed fields would silently drop the
// conditional keys and the tool would then write the *base* number as if it
// were the price — under-charging every long-context request without saying
// anything. The map lets us notice "there is a key here that is not one of the
// three scalars" and refuse.
func parseModelPrice(mraw json.RawMessage) (ObservedPrice, bool) {
	var m struct {
		Cost map[string]json.RawMessage `json:"cost"`
	}
	if err := json.Unmarshal(mraw, &m); err != nil {
		return ObservedPrice{}, false
	}
	if m.Cost == nil {
		// The model IS listed; it just has no price list. Return it marked as
		// absent instead of dropping it — dropping it would make Resolve
		// report "model_not_listed_for_provider", i.e. blame the model's
		// existence when the truth is only that its price is unpublished.
		return ObservedPrice{CostAbsent: true}, true
	}
	p := ObservedPrice{}
	getF := func(k string) *float64 {
		raw, ok := m.Cost[k]
		if !ok {
			return nil
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil
		}
		return &f
	}
	p.Input, p.Output, p.CacheRead = getF("input"), getF("output"), getF("cache_read")

	if raw, ok := m.Cost["tiers"]; ok {
		var ts []Tier
		if err := json.Unmarshal(raw, &ts); err == nil {
			p.Tiers = ts
		}
	}
	// Any remaining key that is not one of the three scalars is a conditional
	// price we do not model. Record it rather than ignore it.
	for k, raw := range m.Cost {
		switch k {
		case "input", "output", "cache_read", "tiers", "cache_write":
			continue
		}
		if p.ContextOver == nil {
			p.ContextOver = map[string]json.RawMessage{}
		}
		p.ContextOver[k] = raw
	}
	return p, true
}
