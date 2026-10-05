// bg/pricing_baseline_sync.go — 模型基准价（原厂标准价）的入库与价差对账
// （迁移 826）
//
// # 这一层为什么必须存在
//
// 实盘成本完全由人工填的绝对价决定：provider/client.go:1643 取
// COALESCE(mo.unit_price_in_per_1m, pp_fb.plan_in)，两处都是人填的，兜底还带
// 一刀钝斧（迁移 480 把零价 offer 一律写成 0.1/0.1）。所以「价填错了」与
// 「价没填」在账面上分不开，成本核算没有可对账的参照物。
//
// 本文件给出「参照物」：一份**带出处**的原厂标准价清单（SSOT，权威面）
// + 一个把外部机读源当**对账面**的定时任务。
//
// # 权威面与对账面必须分开
//
// 这是本设计里最要紧的一条纪律：**对账只记账，不改价。**
//
// 观察源（models.dev 那类）是第三方社区维护的数据，把它当权威写进账本，等于
// 让机器替运营做了「我们按这个价卖」的决定。正确形态是：漂移被看见、被记账、
// 可以按模型逐条处理。反过来，把 SSOT 也做成自动抓取，就退回成现状那个
// 「手工维护 + 失效的刷新」形态，只是把手写换成了易碎的解析。
package bg

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// baselinePriceCatalog 是随二进制内嵌的 SSOT 清单。内嵌而不是运行时读文件：
// 清单与读它的代码同版本，部署时不会出现「代码更新了但清单还是三个月前的」。
//
//go:embed data/model_baseline_prices.json
var baselinePriceCatalog embed.FS

// baselinePriceStaleAfter 是基准价出处的最大可接受年龄。新鲜度先于正确性。
const baselinePriceStaleAfter = 30 * 24 * time.Hour

// baselineDriftTolerancePct 是判定「对上了」的相对偏差容限。
//
// 为什么不是 0：观察源与原厂页面的四舍五入、缓存读价是否单独计价、上下文
// 分档，都会产生 1~2% 的表观差。容限取 2% 时，真漂移（调价通常是 25%+
// 的整数倍）仍然全部被抓住。
const baselineDriftTolerancePct = 2.0

// 对账判词。与迁移 826 的 CHECK 约束一一对应。
const (
	PriceVerdictMatch         = "match"
	PriceVerdictDrift         = "drift"
	PriceVerdictStaleSource   = "stale_source"
	PriceVerdictMissing       = "missing"
	PriceVerdictNotComparable = "not_comparable"
)

// BaselinePrice 是清单里的一个条目。
type BaselinePrice struct {
	InputPer1M      *float64 `json:"input_per_1m"`
	OutputPer1M     *float64 `json:"output_per_1m"`
	CacheReadPer1M  *float64 `json:"cache_read_per_1m"`
	CacheWritePer1M *float64 `json:"cache_write_per_1m"`
	Currency        string   `json:"currency"`
	Vendor          string   `json:"vendor"`
	Source          string   `json:"source"`
	SourceURL       string   `json:"source_url"`
	FetchedAt       string   `json:"fetched_at"`
}

// baselineCatalogFile 是内嵌清单的顶层形状。
type baselineCatalogFile struct {
	Models map[string]BaselinePrice `json:"models"`
}

// loadBaselineCatalog 解析清单。返回按模型名排序的条目，便于输出稳定。
func loadBaselineCatalog(raw []byte) (map[string]BaselinePrice, error) {
	var file baselineCatalogFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse baseline price catalog: %w", err)
	}
	if file.Models == nil {
		file.Models = map[string]BaselinePrice{}
	}
	for name, p := range file.Models {
		if err := p.validate(name); err != nil {
			return nil, err
		}
	}
	return file.Models, nil
}

// validate 是清单的入口校验。**缺 source_url 一律拒绝**——没有出处的价格
// 不可审计，而不可审计的价格正是现状那张表漂移到没人知道的原因。
// Validate 是 validate 的导出包装，供**库外**消费者在写权威面之前自检。
//
// 为什么需要：SSOT 的唯一写入口 SyncBaselinePricesToDB 内部会调 validate，
// 而提案工具导出的草稿（cmd/tools/propose-baseline-prices 的 -emit-ssot）在
// 被人合入 bg/data/model_baseline_prices.json 之前**还没有走到那个函数**。
// 没有这个包装，「草稿能不能过权威闸门」只能在真库上、而且要等有人真的
// 合入并跑同步才知道 —— 那是事故发现，不是验证。
//
// 语义与 validate 完全一致；它不做任何事，只是让它可以被调用。
func (p BaselinePrice) Validate(model string) error {
	return p.validate(model)
}

func (p BaselinePrice) validate(model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("baseline price entry has an empty model name")
	}
	if p.InputPer1M == nil {
		return fmt.Errorf("baseline price %q: input_per_1m is required", model)
	}
	if p.OutputPer1M == nil {
		return fmt.Errorf("baseline price %q: output_per_1m is required", model)
	}
	if *p.InputPer1M < 0 || *p.OutputPer1M < 0 {
		return fmt.Errorf("baseline price %q: negative price", model)
	}
	if strings.TrimSpace(p.SourceURL) == "" {
		return fmt.Errorf("baseline price %q: source_url is required — a price without provenance is not auditable", model)
	}
	// ★ currency 同样必填（2026-10-05 补）。
	//
	// 原先这里不校验，于是 `SyncBaselinePricesToDB` 里的 `currency == "" → "USD"`
	// 会给一条**币种未知**的价格凭空贴上 USD：那不是缺省，而是一个关于钱的
	// **断言**。后果有两处，且都静默：
	//   · 826 视图按 `COALESCE(baseline_price_currency,'USD')` 比币种 ⇒ 真值是
	//     EUR 却被当成 USD，要么 `currency_comparable=false` 永远算不出偏差，
	//     要么与同为 "USD" 的供应商价算出**看起来正常的错倍率**；
	//   · 台账记下的 `baseline_price_currency` 是错的，而它就是权威面。
	//
	// 提取器 `currencyOf` 在价格行里找不到 $/€/£/¥ 时会返回 ""，所以这条路径
	// 真实可达：提案会带空币种，人照抄进 SSOT 就中招。正确做法是**拒绝**——
	// 与 source_url 同一个理由：无法核实的价不是价。
	if strings.TrimSpace(p.Currency) == "" {
		return fmt.Errorf("baseline price %q: currency is required — a price whose currency is unknown "+
			"cannot be compared against a supplier price, and defaulting it to USD would state a fact "+
			"the source never said", model)
	}
	if _, err := p.FetchedAtTime(); err != nil {
		return fmt.Errorf("baseline price %q: fetched_at: %w", model, err)
	}
	return nil
}

// FetchedAtTime 解析出处读取时刻。
func (p BaselinePrice) FetchedAtTime() (time.Time, error) {
	raw := strings.TrimSpace(p.FetchedAt)
	if raw == "" {
		return time.Time{}, fmt.Errorf("fetched_at is empty")
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not RFC3339: %w", raw, err)
	}
	return t, nil
}

// LoadEmbeddedBaselineCatalog 读内嵌清单。
func LoadEmbeddedBaselineCatalog() (map[string]BaselinePrice, error) {
	raw, err := baselinePriceCatalog.ReadFile("data/model_baseline_prices.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded baseline catalog: %w", err)
	}
	return loadBaselineCatalog(raw)
}

// PriceObservation 是从一个外部机读源观察到的价。
type PriceObservation struct {
	InputPer1M  *float64
	OutputPer1M *float64
	Currency    string
	Source      string
	SourceURL   string
	ObservedAt  time.Time
}

// Reconciliation 是逐模型对账的结论。
type Reconciliation struct {
	Model          string
	Verdict        string
	InputDriftPct  *float64
	OutputDriftPct *float64
	SSOTPrice      *BaselinePrice
	Observation    *PriceObservation
	SSOTFetchedAt  *time.Time
	Detail         map[string]any
}

// ReconcileBaselinePrice 逐模型判词。纯函数，所以每条分支都能被测到。
//
// 判词优先级是有意的：
//
//	stale_source > missing > not_comparable > drift > match
//
// 「出处过期」排在「漂移」前面，因为出处过期时算出来的偏差本身就是过期
// 数据的偏差——先修新鲜度，再谈数字对不对。顺序反过来会让人去追一个由
// 旧数据造出来的假漂移。
func ReconcileBaselinePrice(model string, ssot *BaselinePrice, obs *PriceObservation, now time.Time) Reconciliation {
	r := Reconciliation{Model: model, Detail: map[string]any{}}

	if ssot == nil {
		r.Verdict = PriceVerdictMissing
		r.Detail["reason"] = "no baseline price in the catalog"
		return r
	}
	r.SSOTPrice = ssot
	if fetched, err := ssot.FetchedAtTime(); err == nil {
		r.SSOTFetchedAt = &fetched
		if now.Sub(fetched) > baselinePriceStaleAfter {
			r.Verdict = PriceVerdictStaleSource
			r.Detail["reason"] = "baseline source is older than the freshness window"
			r.Detail["fetched_at"] = fetched.Format(time.RFC3339)
			r.Detail["stale_after"] = baselinePriceStaleAfter.String()
			// 观察仍在下面算，但**不改判词**——过期来源先修新鲜度。
			if obs != nil {
				r.Observation = obs
				r.InputDriftPct, r.OutputDriftPct = driftPcts(*ssot, obs)
			}
			return r
		}
	}

	if obs == nil {
		r.Verdict = PriceVerdictMissing
		r.Detail["reason"] = "no observation from the machine-readable source"
		return r
	}
	r.Observation = obs
	r.InputDriftPct, r.OutputDriftPct = driftPcts(*ssot, obs)

	// 币种不同：算出来的倍率是纯噪声，但它在报表里长得和真偏差一样，
	// 所以必须显式判成不可比，而不是给一个数字了事。
	if obs.Currency != "" && ssot.Currency != "" && obs.Currency != ssot.Currency {
		r.Verdict = PriceVerdictNotComparable
		r.Detail["reason"] = "currency mismatch"
		r.Detail["ssot_currency"] = ssot.Currency
		r.Detail["observed_currency"] = obs.Currency
		return r
	}

	// ★ 基准侧是 0（厂商把这一档列为免费）而观察侧非 0：
	// 「免费 → 收费」。百分比在这时候无定义（driftPct 返回 nil），但它**不是**
	// 「两边不可比」—— 它恰恰是成本核算里最可行动的一类：本该白给的东西在收钱。
	//
	// 不显式判出来的话，它会落进下面那条泛泛的 not_comparable，与「两边都是 0
	// （真免费、价一致）」**判词和 reason 完全一样**（2026-10-05 实测：两种都输出
	// verdict=not_comparable / reason="neither side has a comparable price"）。
	// 运营从台账里分不出这两者，而后者才是该立刻去谈价的那一条。
	//
	// 判成 drift 而不是新增一个枚举值：它确实是对基准价的偏离，且 drift 已被
	// 对账侧的告警路径统计（counts[drift] > 0 ⇒ 健康面报），不新增迁移即生效。
	if baselineFreeButCharged(*ssot, obs) {
		r.Verdict = PriceVerdictDrift
		r.Detail["reason"] = "the original vendor lists this model as free (baseline 0) while the " +
			"observation is non-zero — a percentage is undefined here, so this is a free-to-paid " +
			"change rather than a rounding disagreement"
		return r
	}

	// ★ 币种**未知**是第三件事，与上面两条都不同（2026-10-05 扫面补的）。
	//
	// 「币种不同」那条之所以成立，靠的是 `obs.Currency != "" && ssot.Currency != ""
	// && … != …` —— **两个非空条件都在守卫里**。任何一侧为空，这条检查被整个
	// 跳过，于是**从未发生在同一种货币里**的比较照样给出判词。实测（修前）：
	//   · 观测侧币种空、价格一致   ⇒ verdict=match    —— 台账谎称「与原厂价一致」
	//   · 观测侧币种空、99 vs 2.50 ⇒ verdict=drift，detail 只剩 tolerance_pct=2
	//   · 基准侧币种空             ⇒ verdict=match
	// 「match」那一类最坏：源页面从没说过两个价一致，台账却替它说了，而且
	// detail 是空的，运营无从分辨它与真正的 match。
	//
	// 真实可达：观测源抽不出 $/€/£/¥ 就返回 ""（抽取失败是常态不是异常）；
	// 基准侧现在被 validate 挡住入库，但这个函数收的是**内存里的**清单，
	// 绕得过 validate。
	//
	// 位置在 free-to-paid **之后**是刻意的：0 在任何币种下都是 0，「本该免费
	// 却在收钱」与币种无关，判成不可比会把最可行动的一类降级掉。
	if obs.Currency == "" || ssot.Currency == "" {
		side := "the observation"
		if ssot.Currency == "" {
			side = "the baseline"
		}
		if obs.Currency == "" && ssot.Currency == "" {
			side = "neither side"
		}
		r.Verdict = PriceVerdictNotComparable
		r.Detail["reason"] = side + " has no currency, so these two numbers were never in the same " +
			"currency — a percentage between them would be an artefact of the exchange rate, not a drift"
		r.Detail["ssot_currency"] = ssot.Currency
		r.Detail["observed_currency"] = obs.Currency
		return r
	}

	if r.InputDriftPct == nil && r.OutputDriftPct == nil {
		r.Verdict = PriceVerdictNotComparable
		r.Detail["reason"] = "neither side has a comparable price"
		return r
	}
	if exceedsTolerance(r.InputDriftPct) || exceedsTolerance(r.OutputDriftPct) {
		r.Verdict = PriceVerdictDrift
		r.Detail["tolerance_pct"] = baselineDriftTolerancePct
		return r
	}
	r.Verdict = PriceVerdictMatch
	return r
}

// baselineFreeButCharged 报告「原厂基准 0 而观察非 0」。
//
// 逐侧判断：只要**任一侧**是「基准 0、观察非 0」就成立 —— 免的是一档而收的是
// 另一档，运营照样需要知道。
func baselineFreeButCharged(ssot BaselinePrice, obs *PriceObservation) bool {
	if ssot.InputPer1M != nil && *ssot.InputPer1M == 0 && obs.InputPer1M != nil && *obs.InputPer1M != 0 {
		return true
	}
	if ssot.OutputPer1M != nil && *ssot.OutputPer1M == 0 && obs.OutputPer1M != nil && *obs.OutputPer1M != 0 {
		return true
	}
	return false
}

// driftPct 返回 (观察 - 清单)/清单 * 100。
//
// 分母用清单侧：它是权威面，偏差要表达「权威值与观察值差多少」。
// 清单侧为 0 时返回 nil（不可比）而不是 0——「两边都是 0」与「算不出比值」
// 在成本核算里不是一回事。
func driftPct(ssot, observed float64) *float64 {
	if ssot == 0 {
		return nil
	}
	pct := (observed - ssot) / ssot * 100
	return &pct
}

func driftPcts(ssot BaselinePrice, obs *PriceObservation) (*float64, *float64) {
	var in, out *float64
	if ssot.InputPer1M != nil && obs.InputPer1M != nil {
		in = driftPct(*ssot.InputPer1M, *obs.InputPer1M)
	}
	if ssot.OutputPer1M != nil && obs.OutputPer1M != nil {
		out = driftPct(*ssot.OutputPer1M, *obs.OutputPer1M)
	}
	return in, out
}

func exceedsTolerance(pct *float64) bool {
	return pct != nil && math.Abs(*pct) > baselineDriftTolerancePct
}

// SyncBaselinePricesToDB 把清单写进 models_canonical。
//
// 只在出处完整时写（validate 已保证）。写的是「人确认过的价」，所以这一步
// 覆盖既有值是对的——清单变了就该跟着变。
func SyncBaselinePricesToDB(ctx context.Context, db *pgxpool.Pool, catalog map[string]BaselinePrice) (int, error) {
	names := make([]string, 0, len(catalog))
	for n := range catalog {
		names = append(names, n)
	}
	sort.Strings(names)

	written := 0
	for _, name := range names {
		p := catalog[name]
		if err := p.validate(name); err != nil {
			return written, err
		}
		fetched, err := p.FetchedAtTime()
		if err != nil {
			return written, err
		}
		// 非空由 validate 保证（上面那条 currency 校验）；刻意**不再**兜底成
		// "USD" —— 那个回退正是把「未知」变成「断言」的动作。
		currency := p.Currency
		tag, err := db.Exec(ctx, `
			UPDATE models_canonical
			   SET baseline_price_currency          = $2,
			       baseline_input_price_per_1m      = $3,
			       baseline_output_price_per_1m     = $4,
			       baseline_cache_read_price_per_1m = $5,
			       baseline_cache_write_price_per_1m = $6,
			       baseline_price_vendor            = $7,
			       baseline_price_source            = $8,
			       baseline_price_source_url        = $9,
			       baseline_price_fetched_at        = $10,
			       updated_at                       = now()
			 WHERE canonical_name = $1
		`, name, currency, p.InputPer1M, p.OutputPer1M, p.CacheReadPer1M,
			p.CacheWritePer1M, p.Vendor, p.Source, p.SourceURL, fetched)
		if err != nil {
			return written, fmt.Errorf("sync baseline price %q: %w", name, err)
		}
		if tag.RowsAffected() == 0 {
			// 清单里的模型在库里不存在。这不是错误——清单覆盖的原厂
			// 模型可能一个都没被任何供应商接入。记日志，不中断。
			slog.Info("baseline price: catalog model not present in models_canonical",
				"canonical_name", name)
			continue
		}
		written++
	}
	return written, nil
}

// RecordReconciliation 把一次对账写进台账。
func RecordReconciliation(ctx context.Context, db *pgxpool.Pool, r Reconciliation) error {
	detail, err := json.Marshal(r.Detail)
	if err != nil {
		return fmt.Errorf("marshal reconciliation detail: %w", err)
	}
	var (
		ssotIn, ssotOut     *float64
		ssotCurrency        *string
		ssotURL             *string
		obsIn, obsOut       *float64
		obsCurrency, obsSrc *string
		obsURL              *string
	)
	if p := r.SSOTPrice; p != nil {
		ssotIn, ssotOut = p.InputPer1M, p.OutputPer1M
		if p.Currency != "" {
			ssotCurrency = &p.Currency
		}
		if p.SourceURL != "" {
			ssotURL = &p.SourceURL
		}
	}
	if o := r.Observation; o != nil {
		obsIn, obsOut = o.InputPer1M, o.OutputPer1M
		if o.Currency != "" {
			obsCurrency = &o.Currency
		}
		if o.Source != "" {
			obsSrc = &o.Source
		}
		if o.SourceURL != "" {
			obsURL = &o.SourceURL
		}
	}
	_, err = db.Exec(ctx, `
		INSERT INTO model_baseline_price_reconciliation
		       (canonical_name, ssot_input_price_per_1m, ssot_output_price_per_1m,
		        ssot_currency, ssot_source_url, ssot_source_fetched_at,
		        observed_input_price_per_1m, observed_output_price_per_1m,
		        observed_currency, observed_source, observed_source_url,
		        verdict, input_drift_pct, output_drift_pct, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb)
	`, r.Model, ssotIn, ssotOut, ssotCurrency, ssotURL, r.SSOTFetchedAt,
		obsIn, obsOut, obsCurrency, obsSrc, obsURL,
		r.Verdict, r.InputDriftPct, r.OutputDriftPct, string(detail))
	if err != nil {
		return fmt.Errorf("record reconciliation %q: %w", r.Model, err)
	}
	return nil
}

// baselineSyncEnabled 读关闭开关。价格入库会覆盖既有基准价，所以它必须能
// 不重新发版就掐掉。
const baselineSyncEnvKillSwitch = "LLM_GATEWAY_BASELINE_PRICE_SYNC"

func baselineSyncEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(baselineSyncEnvKillSwitch)))
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// fmtPct 是给日志用的可空百分比格式化。
func fmtPct(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return strconv.FormatFloat(math.Round(*p*100)/100, 'f', 2, 64) + "%"
}

// BaselinePriceSyncInterval 是基准价入库 + 价差对账的周期。
//
// 出网只有一次（拉外部机读源），所以周期可以长；真正的时效压力来自出处
// 的 fetched_at —— 过期由判词里的 stale_source 报，不是靠跑得勤来掩盖。
const BaselinePriceSyncInterval = 6 * time.Hour

// RunBaselinePriceSync 周期性地把 SSOT 清单写进 models_canonical。
//
// 它**不**拉外部源、不做对账：对账需要 observations，由调用方注入（见
// ReconcileBaselinePrice）。把「权威面入库」与「对账」分成两个动作，是为了
// 让入库这条路径保持零出网——它一旦出错，改的是钱。
func RunBaselinePriceSync(ctx context.Context, db *pgxpool.Pool) {
	if db == nil {
		return
	}
	if !baselineSyncEnabled() {
		slog.Info("baseline_price_sync: disabled by env kill switch",
			"env", baselineSyncEnvKillSwitch)
		return
	}
	run := func() {
		catalog, err := LoadEmbeddedBaselineCatalog()
		if err != nil {
			slog.Error("baseline_price_sync: catalog load failed", "error", err)
			return
		}
		written, err := SyncBaselinePricesToDB(ctx, db, catalog)
		if err != nil {
			slog.Error("baseline_price_sync: sync failed", "error", err)
			return
		}
		if len(catalog) == 0 {
			// 显式说出来：空清单是合法状态，但运营需要知道「基准价一条
			// 都没有，所以供应商偏差当前无从算起」，否则成本核算缺了
			// 参照物这件事是隐形的。
			slog.Warn("baseline_price_sync: catalog is empty — no model has a vendor " +
				"baseline price yet, so supplier-vs-baseline deviation cannot be computed. " +
				"Populate bg/data/model_baseline_prices.json (source_url + fetched_at required).")
			return
		}
		slog.Info("baseline_price_sync: done", "catalog", len(catalog), "written", written)
	}
	run()
	ticker := time.NewTicker(BaselinePriceSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
