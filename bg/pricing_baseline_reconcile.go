// bg/pricing_baseline_reconcile.go — 基准价对账面：抓外部机读源 + 逐模型判词
// （迁移 826）
//
// # 为什么是「对账面」而不是「权威面」
//
// 本仓的 SSOT（bg/data/model_baseline_prices.json）是权威面：人确认过的
// 价 + 出处。外部机读源（models.dev）是**对账面**：它覆盖广、更新快，但
// 是社区维护的第三方数据。把它当权威写进账本，等于让机器替运营做了
// 「我们按这个价卖」的决定。
//
// 所以本文件只做两件事：把观察值取回来、按顺序判词、记台账。
// **它一条写价格的路径都没有**——写价只在 SyncBaselinePricesToDB 里，而那
// 里的输入是 SSOT。
//
// # 为什么只对账 SSOT 里有的模型
//
// 反过来（对所有出现在外部源里的模型建基准价）会得到一个巨大的、社区维护
// 的价格库直接进账本——那正是「不可审计的价格」的定义。SSOT 缺席的模型
// 判词是 missing，它需要人决定要不要纳入权威面。
package bg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MachineReadablePricingURL 是外部机读源。环境变量可覆盖，因为这类源
// 会换地址，而换地址不该要求改代码。
const MachineReadablePricingURL = "https://models.dev/api.json"

const machineReadablePricingURLEnv = "LLM_GATEWAY_PRICE_OBSERVATION_URL"

// observedPrices 是「供应商 id + 模型 id」下的观察价。
type observedPrices map[string]map[string]PriceObservation

// observationSourceURL 解析本次观察的来源地址。
func observationSourceURL() string {
	if v := strings.TrimSpace(os.Getenv(machineReadablePricingURLEnv)); v != "" {
		return v
	}
	return MachineReadablePricingURL
}

// FetchMachineReadablePrices 拉一次外部机读源并摊平成观察价表。
//
// 摊平的键是 "<provider>:<model>"，因为**同一个模型在不同 provider 下价格
// 不同**（中转商加价），只有原厂那一档才是基准价的对家。
func FetchMachineReadablePrices(ctx context.Context, httpClient *http.Client) (observedPrices, string, time.Time, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	url := observationSourceURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, url, time.Time{}, fmt.Errorf("build observation request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, url, time.Time{}, fmt.Errorf("fetch observation source: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close
	if resp.StatusCode != http.StatusOK {
		return nil, url, time.Time{}, fmt.Errorf("observation source returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, url, time.Time{}, fmt.Errorf("read observation source: %w", err)
	}

	var doc map[string]struct {
		Name   string `json:"name"`
		Doc    string `json:"doc"`
		Models map[string]struct {
			ID   string `json:"id"`
			Cost *struct {
				Input     *float64 `json:"input"`
				Output    *float64 `json:"output"`
				CacheRead *float64 `json:"cache_read"`
				Tiers     []struct {
					Input  *float64 `json:"input"`
					Output *float64 `json:"output"`
					Tier   struct {
						Type string `json:"type"`
						Size int    `json:"size"`
					} `json:"tier"`
				} `json:"tiers"`
			} `json:"cost"`
			Modalities *struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, url, time.Time{}, fmt.Errorf("parse observation source: %w", err)
	}

	observedAt := time.Now().UTC()
	out := observedPrices{}
	for providerID, p := range doc {
		perModel := map[string]PriceObservation{}
		for modelID, m := range p.Models {
			if m.Cost == nil || (m.Cost.Input == nil && m.Cost.Output == nil) {
				continue
			}
			obs := PriceObservation{
				InputPer1M:  m.Cost.Input,
				OutputPer1M: m.Cost.Output,
				Currency:    "USD",
				Source:      "machine_readable_catalog",
				SourceURL:   url,
				ObservedAt:  observedAt,
			}
			perModel[modelID] = obs
		}
		if len(perModel) > 0 {
			out[providerID] = perModel
		}
	}
	if len(out) == 0 {
		return nil, url, observedAt, fmt.Errorf("observation source yielded no priced models")
	}
	return out, url, observedAt, nil
}

// LookupObservation 取某个 (原厂, 模型) 的观察价。
func (o observedPrices) LookupObservation(vendor, model string) (PriceObservation, bool) {
	if vendor == "" {
		return PriceObservation{}, false
	}
	perModel, ok := o[strings.ToLower(strings.TrimSpace(vendor))]
	if !ok {
		return PriceObservation{}, false
	}
	obs, ok := perModel[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		// 观察源的模型 id 常带 provider 前缀（"openai/gpt-4o" 形态的
		// base_model 引用），所以再试一次带前缀的键。这只放宽查找，
		// 不放宽判据。
		if obs, ok = perModel[strings.ToLower(strings.TrimSpace(vendor)+"/"+strings.TrimSpace(model))]; ok {
			return obs, true
		}
	}
	return obs, ok
}

// ReconcileCatalog 对 SSOT 里的每个模型判一次词并记台账。
//
// 只对 SSOT 里有的模型判词：SSOT 缺席的模型说明「还没决定要不要纳入权威
// 面」，那是运营的决策，不是对账能替它做的。
func ReconcileCatalog(ctx context.Context, record func(Reconciliation) error, catalog map[string]BaselinePrice, observed observedPrices, now time.Time) (counts map[string]int, err error) {
	counts = map[string]int{}
	names := make([]string, 0, len(catalog))
	for n := range catalog {
		names = append(names, n)
	}
	sortStrings(names)

	for _, name := range names {
		p := catalog[name]
		var obs *PriceObservation
		if got, ok := observed.LookupObservation(p.Vendor, name); ok {
			obs = &got
		}
		r := ReconcileBaselinePrice(name, &p, obs, now)
		if err := record(r); err != nil {
			return counts, err
		}
		counts[r.Verdict]++
	}
	return counts, nil
}

// sortStrings 是升序字符串排序的本地别名（避免为一个调用点引 sort 的
// 泛化用法，也让这里的意图显式）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ReconcileInterval 是对账周期。它是出网项，所以比入库周期短、比探测短：
// 12h。
// recordObservationFailure 把一次失败的抓取落到 830 的健康表。
//
// 三条不可让步：
//
//  1. **失败要计数，但不吞**：consecutive_failures 累加，last_error 记原始错误。
//  2. **它自己失败绝不能连带把对账弄挂**：这张表是「让人知道」的仪表，不是
//     判据。所以这里只 slog，绝不返回错误——一张仪表坏了不该让对账停摆。
//  3. 记不上也要**说出来**（ERROR 级），否则「仪表坏了」又变回静默。
func recordObservationFailure(ctx context.Context, db *pgxpool.Pool, url string, cause error) {
	if db == nil {
		return
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO public.model_baseline_price_observation_health
			(source_url, last_attempt_at, consecutive_failures, last_error, updated_at)
		VALUES ($1, now(), 1, $2, now())
		ON CONFLICT (source_url) DO UPDATE
		   SET last_attempt_at      = now(),
		       consecutive_failures = public.model_baseline_price_observation_health
		                                  .consecutive_failures + 1,
		       last_error            = EXCLUDED.last_error,
		       updated_at            = now()`,
		url, cause.Error()); err != nil {
		// 表可能还没被 830 应用（账本落后）。这正是仪表本身要报告的状态。
		slog.Error("baseline_reconciliation: could not record observation failure "+
			"(migration 830 applied?) — the fetch failure is now invisible to the health surface",
			"source_url", url, "fetch_error", cause, "record_error", err)
	}
}

// recordObservationSuccess 记一次成功，并把连续失败归零。
//
// observed_models == 0 记成**失败**而不是成功：见函数调用处的说明——源答了
// 200 但内容不再是能解析的形状时，这是最像成功的失败，而条数是它唯一的信号。
func recordObservationSuccess(ctx context.Context, db *pgxpool.Pool, url string, models int) {
	if db == nil {
		return
	}
	if models == 0 {
		recordObservationFailure(ctx, db, url,
			errors.New("source returned no parsable model prices (HTTP success but the payload "+
				"no longer has the shape we parse — the source changed, or it is serving a stub)"))
		return
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO public.model_baseline_price_observation_health
			(source_url, last_attempt_at, last_success_at, consecutive_failures,
			 last_error, observed_models, updated_at)
		VALUES ($1, now(), now(), 0, NULL, $2, now())
		ON CONFLICT (source_url) DO UPDATE
		   SET last_attempt_at      = now(),
		       last_success_at      = now(),
		       consecutive_failures = 0,
		       last_error            = NULL,
		       observed_models       = EXCLUDED.observed_models,
		       updated_at            = now()`,
		url, models); err != nil {
		slog.Error("baseline_reconciliation: could not record observation success "+
			"(migration 830 applied?) — successful cross-checks are now invisible to the health surface",
			"source_url", url, "observed_models", models, "record_error", err)
	}
}

const ReconcileInterval = 12 * time.Hour

// RunBaselineReconciliation 周期性把 SSOT 与外部机读源对一遍。
//
// 它不写价格。判词为 drift 的行留在台账里等人处理；日志把计数抬到 Warn
// 是因为「一批模型的价格对不上原厂」是一个需要人知道的账单事实。
func RunBaselineReconciliation(ctx context.Context, db *pgxpool.Pool, client *http.Client) {
	if db == nil {
		return
	}
	if !baselineSyncEnabled() {
		slog.Info("baseline_reconciliation: disabled by env kill switch",
			"env", baselineSyncEnvKillSwitch)
		return
	}
	run := func() {
		catalog, err := LoadEmbeddedBaselineCatalog()
		if err != nil {
			slog.Error("baseline_reconciliation: catalog load failed", "error", err)
			return
		}
		if len(catalog) == 0 {
			slog.Warn("baseline_reconciliation: catalog is empty — nothing to reconcile, " +
				"and no model has a vendor baseline price (so supplier-vs-baseline " +
				"deviation is not computable at all)")
			return
		}
		observed, url, at, err := FetchMachineReadablePrices(ctx, client)
		if err != nil {
			// 抓不到观察源**不写任何判词**：没有观察就没有对账结论，
			// 往台账里写一堆 missing 会把「源挂了」与「价格真的不对」
			// 混成一类信号。判词口径这一步必须守住。
			//
			// ★ 但「不写判词」不等于「不留痕」（迁移 830）。
			//
			// 原来这里只有一行 Warn 然后 return，于是「每 12h 抓一次、每次
			// 都失败、每次都不写任何东西」可以连续几周不被任何人发现：报表
			// 照常出数（用的是几个月前的基准价），台账里没有一行「本轮没
			// 做成」，健康面也没有任何检查会响。这比压根没有对账更坏——
			// 后者让人知道自己在裸奔，前者让人以为自己在看仪表盘。
			//
			// 2026-10-04 实测这类失败真的会发生：同一时刻 curl 与三种 UA 的
			// Go 客户端全部 HTTP 200 / 5,317,334 字节，只有这一次 EOF。
			// ⇒ 它是零星反复的，不是「一次就不会再有」。
			slog.Warn("baseline_reconciliation: observation fetch failed, no verdicts written",
				"source_url", url, "error", err)
			recordObservationFailure(ctx, db, url, err)
			return
		}
		// 成功侧也要记，且**条数为 0 算失败**：HTTP 200 但内容不再是能解析的
		// 形状时，错误路径一个都不会触发，而所有价格都「对不上」——那是最像
		// 成功的一种失败。见 observed_models 的列注释。
		recordObservationSuccess(ctx, db, url, len(observed))
		counts, err := ReconcileCatalog(ctx, func(r Reconciliation) error {
			return RecordReconciliation(ctx, db, r)
		}, catalog, observed, at)
		if err != nil {
			slog.Error("baseline_reconciliation: recording verdicts failed", "error", err)
			return
		}
		attrs := []any{
			"catalog", len(catalog), "providers_seen", len(observed),
			"source_url", url, "observed_at", at.Format(time.RFC3339),
		}
		for _, v := range []string{PriceVerdictDrift, PriceVerdictStaleSource,
			PriceVerdictMissing, PriceVerdictNotComparable, PriceVerdictMatch} {
			attrs = append(attrs, v, counts[v])
		}
		if counts[PriceVerdictDrift] > 0 || counts[PriceVerdictStaleSource] > 0 {
			slog.Warn("baseline_reconciliation: done — prices need attention (no price was changed)", attrs...)
			return
		}
		slog.Info("baseline_reconciliation: done", attrs...)
	}
	run()
	ticker := time.NewTicker(ReconcileInterval)
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
