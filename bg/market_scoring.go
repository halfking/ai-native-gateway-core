package bg

// market_scoring.go — 定稿 M2-T3：每日评分 job（C1/C2/C3 双币落
// supplier_market_position）。
//
// 设计依据：《运营管理与价格计费体系-总体设计定稿》§3.4（C1/C2/C3 双币口径）、
// §4.2（③ 每日评分 job）与 959 迁移（双地板列 + 双读数列的落库面）。
//
// # 调度时刻（对定稿 01:30 的有意偏离，留痕）
//
// 定稿写「每日评分 job（01:30，错开采集）」时假设同步链在 01:30 前完成；
// 实际在役形态是采集 01:00（salesdecorate）→ 同步 02:30（211 宿主 cron，
// market-price-sync.py）→ 评分。评分若按 01:30 跑，读到的是**前一天** 02:30
// 的地板（行情管道整整滞后一天），「错开采集」的本意（读到新地板）落空。
// ⇒ 默认 03:10（同步后 + 40min 缓冲），env LLM_GATEWAY_MARKET_SCORING_AT
// （"HH:MM" 本地时）可覆盖。幂等重放 ⇒ 提前/重复跑都收敛到同一结果。
//
// # 读面（与 959 视图逐字同源，契约门钉死）
//
// 读 SQL 复刻 v_supplier_price_vs_baseline 的三个承重片段（mc LATERAL /
// mpr LATERAL / C3 置信度 CASE），因为 C1 需要的 vendor_list_* 与 fx_rate
// 在视图里没有（视图 append-only 冻结，959 纪律），而 mpr 的 join 语义必须
// 与视图完全一致 —— 特别是「mpr.canonical_id 非空但不等 ⇒ 不 join」这一
// 排除分支，按名字裸 join 会把它错误地接上。防漂移：契约门
// TestMarketScoringSQLFragmentsMatch959 把三个片段与 959 文件做归一化文本
// 相等断言（同 TestMigration955ITTBucketWidthCrossPin 的跨钉形态）。
//
// # 评分口径（纯函数 scoreSupplierPosition，零 IO 可单测）
//
//   C1  discount_vs_vendor = 1 − supplier_blended / vendor_list_blended
//       绑定币种 == vendor_list_currency ⇒ 原币直除（零 FX）；
//       不同 ⇒ 厂商价按行快照 fx 折到绑定币种，fx_used 记录；
//       fx 快照缺失 ⇒ 折算 NULL（绝不引入第二把汇率，FX 单源红线）；
//       无原厂价（vendor_list 两轴全 NULL/0）⇒ C1 NULL —— NULL（未知）≠
//       0（免费）红线：vendor 免费（blend=0）同样除不出来 ⇒ NULL。
//       vendor_ref_in/out 落**原币**事实源，币种见 vendor_ref_currency；
//       折算只发生在比值里，不落库（§3.1「折算过的数不能替代原币的数」）。
//   C2  主列 market_position = 绑定同区地板（USD→overseas / CNY→domestic，
//       原生比较零 FX）；market_ref_used 记录用的是哪个；
//       双读数 vs_overseas / vs_domestic：海外地板（USD）/国内地板（CNY）
//       各自折到绑定币种再比（同币时原生零折算）；
//       platform_count <3 时读数照写（降级为参考是消费方判据 +
//       market_ref_thin_coverage 告警的事，评分不吞读数）。
//   C3  price_confidence 四档门：trusted / suspect_symmetric / empty /
//       unknown_currency —— 直接透传读 SQL 里与 959 视图逐字同源的 CASE。
//   MarketAdvantage 候选信号 market_advantage_score =
//       100 × clamp(1 − supplier_blended / market_ref_blended, 0, 1)；
//       market_ref = 绑定同区地板（与 C2 主列同源）；同区地板缺失 ⇒
//       用综合地板（market_min_*，CNY）按快照 fx 折算；再无 ⇒ NULL
//       （无信号即中性，消费方按 0 处理，绝不按免费/最便宜处理）。
//
// # 写面（单事务，可重放）
//
//   pg_advisory_xact_lock 串行化多实例（245/154 共库）；staging 临时表
//   COPY + INSERT..ON CONFLICT DO UPDATE + 反连接 DELETE 过期行；
//   重放（同一输入）写出同一值集 ⇒ 收敛，computed_at 随之刷新。
//
// 生命周期照 bg/consistency_worker.go：Start(ctx) 阻塞式（调用方
// `go w.Start(ctx)`），ctx 取消后优雅退出。

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── 调度参数 ─────────────────────────────────────────────────────────────

// marketScoringDefaultAt 默认每日 03:10（本地时）跑：同步 cron 02:30 之后
// （见文件头「调度时刻」），env LLM_GATEWAY_MARKET_SCORING_AT 可覆盖。
const marketScoringDefaultAt = "03:10"

// marketScoringCatchUpDelay 启动后 catch-up 检查的延迟：表为空或最近一次
// 评分超过 24h ⇒ 先补一轮（幂等重放 ⇒ 提前跑无副作用；部署后的首次数据
// 不必等到下一个 03:10）。
const marketScoringCatchUpDelay = 15 * time.Minute

// marketScoringStaleAfter catch-up 判据：computed_at 早于该窗 ⇒ 视为过期。
const marketScoringStaleAfter = 24 * time.Hour

// ParseMarketScoringAt 解析 "HH:MM"（本地时）。
func ParseMarketScoringAt(v string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid LLM_GATEWAY_MARKET_SCORING_AT %q: want HH:MM", v)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid LLM_GATEWAY_MARKET_SCORING_AT %q: hour %v", v, parts[0])
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid LLM_GATEWAY_MARKET_SCORING_AT %q: minute %v", v, parts[1])
	}
	return hour, minute, nil
}

// ── 读 SQL（承重片段与 959 视图逐字同源；契约门 TestMarketScoringSQLFragmentsMatch959 钉死） ──

// marketScoringReadSQL：每绑定一行，给出评分所需的全部事实。
// 三个承重片段（mc LATERAL 的 WHERE..LIMIT / mpr LATERAL 的 WHERE..LIMIT /
// C3 CASE）与 sql/migrations/startup/959_market_dual_floor_a17.sql 里
// v_supplier_price_vs_baseline 的对应片段逐字一致 —— 改任一侧必须过契约门。
const marketScoringReadSQL = `
SELECT
    cmb.credential_id,
    cmb.provider_model_id,
    COALESCE(pm.canonical_raw_name, mc.canonical_name) AS canonical_name,
    COALESCE(cmb.currency, 'USD')                      AS supplier_currency,
    cmb.unit_price_in_per_1m                           AS supplier_in_per_1m,
    cmb.unit_price_out_per_1m                          AS supplier_out_per_1m,
    CASE
      WHEN COALESCE(cmb.currency, 'USD') NOT IN ('USD', 'CNY')
        THEN 'unknown_currency'
      WHEN COALESCE(cmb.unit_price_in_per_1m, 0) = 0
       AND COALESCE(cmb.unit_price_out_per_1m, 0) = 0
        THEN 'empty'
      WHEN cmb.unit_price_in_per_1m IS NOT NULL
       AND cmb.unit_price_in_per_1m = cmb.unit_price_out_per_1m
        THEN 'suspect_symmetric'
      ELSE 'trusted'
    END                                                AS price_confidence,
    mpr.vendor_list_in_per_1m                          AS vendor_list_in_per_1m,
    mpr.vendor_list_out_per_1m                         AS vendor_list_out_per_1m,
    mpr.vendor_list_currency                           AS vendor_list_currency,
    mpr.fx_rate                                        AS fx_rate,
    mpr.overseas_min_in_per_1m                         AS overseas_min_in_per_1m,
    mpr.overseas_min_out_per_1m                        AS overseas_min_out_per_1m,
    mpr.overseas_min_platform_id                       AS overseas_min_platform_id,
    mpr.overseas_min_platform_count                    AS overseas_min_platform_count,
    mpr.domestic_min_in_per_1m                         AS domestic_min_in_per_1m,
    mpr.domestic_min_out_per_1m                        AS domestic_min_out_per_1m,
    mpr.domestic_min_platform_id                       AS domestic_min_platform_id,
    mpr.domestic_min_platform_count                    AS domestic_min_platform_count,
    mpr.market_min_in_per_1m                           AS market_min_in_per_1m,
    mpr.market_min_out_per_1m                          AS market_min_out_per_1m
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN LATERAL (
    SELECT mc.id, mc.canonical_name
      FROM public.models_canonical mc
     WHERE mc.id = pm.canonical_id
        OR (pm.canonical_id IS NULL
            AND lower(mc.canonical_name) = lower(pm.canonical_raw_name))
     ORDER BY (pm.canonical_id IS NOT NULL AND mc.id = pm.canonical_id) DESC
     LIMIT 1
) mc ON true
LEFT JOIN LATERAL (
    SELECT mpr.vendor_list_in_per_1m, mpr.vendor_list_out_per_1m,
           mpr.vendor_list_currency, mpr.fx_rate,
           mpr.overseas_min_in_per_1m, mpr.overseas_min_out_per_1m,
           mpr.overseas_min_platform_id, mpr.overseas_min_platform_count,
           mpr.domestic_min_in_per_1m, mpr.domestic_min_out_per_1m,
           mpr.domestic_min_platform_id, mpr.domestic_min_platform_count,
           mpr.market_min_in_per_1m, mpr.market_min_out_per_1m
      FROM public.market_price_reference mpr
     WHERE mpr.dimension = 'standard'
       AND (mpr.canonical_id = mc.id
            OR (mpr.canonical_id IS NULL
                AND lower(mpr.canonical_name) = lower(mc.canonical_name)))
     ORDER BY (mpr.canonical_id IS NOT NULL) DESC
     LIMIT 1
) mpr ON true`

// ── 行/结果结构 ──────────────────────────────────────────────────────────

// marketScoringBinding 是读 SQL 的一行（评分输入事实）。
type marketScoringBinding struct {
	CredentialID     int64
	ProviderModelID  int64
	CanonicalName    string
	SupplierCur      string // 已 COALESCE 'USD'
	SupplierIn       *float64
	SupplierOut      *float64
	PriceConfidence  string // 视图同源 CASE 的四档输出
	HasRef           bool   // mpr 行是否命中（false = 无行情行）
	VendorListIn     *float64
	VendorListOut    *float64
	VendorListCur    string // mpr.vendor_list_currency（NOT NULL，可能为 ''）
	FXRate           *float64
	OverseasIn       *float64
	OverseasOut      *float64
	OverseasID       string
	OverseasCount    int
	DomesticIn       *float64
	DomesticOut      *float64
	DomesticID       string
	DomesticCount    int
	ComprehensiveIn  *float64 // market_min_*（CNY 语义，959 钉死）
	ComprehensiveOut *float64
}

// MarketScoringResult 是一行评分输出（与 supplier_market_position 的写入列
// 一一对应；指针 nil = NULL）。
type MarketScoringResult struct {
	DiscountVsVendor *float64
	VendorRefIn      *float64
	VendorRefOut     *float64
	VendorRefCur     string
	MarketPosition   *float64
	MarketRefIn      *float64
	MarketRefOut     *float64
	MarketRefID      string
	MarketRefCount   int
	AdvantageScore   *float64
	VsOverseas       *float64
	VsDomestic       *float64
	MarketRefUsed    string
	FXUsed           *float64
}

// marketScoringStats 记录纯函数层的口径计数（单测断言 + 运行汇总）。
type marketScoringStats struct {
	FXFoldedRows    int // fx_used 非空的行数
	FXMissingFolds  int // 需要 fx 而快照缺失 ⇒ 该分量 fail-closed NULL 的次数
	VendorAbsent    int // C1 因无原厂价而 NULL 的行数
	UnknownCurrency int // unknown_currency 行数
}

// blend 是 941 视图同款的混合口径：COALESCE(in,0)+COALESCE(out,0)。
func blend(in, out *float64) float64 {
	var s float64
	if in != nil {
		s += *in
	}
	if out != nil {
		s += *out
	}
	return s
}

// roundTo 对齐 PG numeric round：四舍五入（半数远离零）到 n 位。
func roundTo(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}

// foldPrice 把 v 从 src 币种折到 dst 币种（fx = USD→CNY 快照，单源）。
// 同币零折算；跨币而 fx 缺失 ⇒ ok=false（fail-closed，绝不猜第二把汇率）。
func foldPrice(v float64, src, dst string, fx *float64) (out float64, used bool, ok bool) {
	if src == dst {
		return v, false, true
	}
	if fx == nil || *fx == 0 {
		return 0, false, false
	}
	switch {
	case src == "USD" && dst == "CNY":
		return v * *fx, true, true
	case src == "CNY" && dst == "USD":
		return v / *fx, true, true
	default:
		// 非 USD/CNY 币种不可折（C3 门已把它标 unknown_currency）。
		return 0, false, false
	}
}

// scoreSupplierPosition 是评分纯函数（定稿 §3.4 双币口径）。
// 绑定币种非 USD/CNY 时 C3 门已判 unknown_currency；C1/C2 全部 NULL
// （不猜币种、不做隐式折算）。
func scoreSupplierPosition(b *marketScoringBinding) (MarketScoringResult, marketScoringStats) {
	var st marketScoringStats
	var r MarketScoringResult

	supplierBlend := blend(b.SupplierIn, b.SupplierOut)

	// unknown_currency：读 SQL 的 C3 CASE 已判；这里对不可折币种 fail-closed。
	if b.SupplierCur != "USD" && b.SupplierCur != "CNY" {
		st.UnknownCurrency = 1
		return r, st
	}

	// ── C1 采购折扣率 ────────────────────────────────────────────────────
	if b.HasRef && blend(b.VendorListIn, b.VendorListOut) > 0 && supplierBlend > 0 {
		vendorBlend := blend(b.VendorListIn, b.VendorListOut)
		refCur := b.VendorListCur
		folded, used, ok := foldPrice(vendorBlend, refCur, b.SupplierCur, b.FXRate)
		if ok {
			d := roundTo(1-supplierBlend/folded, 4)
			r.DiscountVsVendor = &d
			r.VendorRefIn = b.VendorListIn
			r.VendorRefOut = b.VendorListOut
			r.VendorRefCur = refCur
			if used {
				rate := *b.FXRate
				r.FXUsed = &rate
			}
		} else {
			// 跨币而 fx 缺失（或币种不可折）⇒ C1 fail-closed NULL。
			st.FXMissingFolds++
		}
	} else {
		st.VendorAbsent = 1
	}

	// ── C2 主列：绑定同区地板（原生零 FX） ───────────────────────────────
	var regionFloorIn, regionFloorOut *float64
	var regionFloorID string
	var regionFloorCount int
	switch b.SupplierCur {
	case "USD":
		regionFloorIn, regionFloorOut, regionFloorID, regionFloorCount =
			b.OverseasIn, b.OverseasOut, b.OverseasID, b.OverseasCount
	case "CNY":
		regionFloorIn, regionFloorOut, regionFloorID, regionFloorCount =
			b.DomesticIn, b.DomesticOut, b.DomesticID, b.DomesticCount
	}
	if regionFloorIn != nil || regionFloorOut != nil {
		r.MarketRefIn = regionFloorIn
		r.MarketRefOut = regionFloorOut
		r.MarketRefID = regionFloorID
		r.MarketRefCount = regionFloorCount
		// market_ref_used = 区域名（overseas|domestic），不是币种 —— 959 列注释
		// 与定稿 §3.4（「主列 = 绑定同区地板，market_ref_used 记录用的是哪个」）。
		switch b.SupplierCur {
		case "USD":
			r.MarketRefUsed = "overseas"
		case "CNY":
			r.MarketRefUsed = "domestic"
		}
		if supplierBlend > 0 && blend(regionFloorIn, regionFloorOut) > 0 {
			p := roundTo(blend(regionFloorIn, regionFloorOut)/supplierBlend, 4)
			r.MarketPosition = &p
		}
	}

	// ── C2 双读数：双地板各自折到绑定币种 ────────────────────────────────
	foldFloor := func(fin, fout *float64, nativeCur string) *float64 {
		if supplierBlend <= 0 {
			return nil
		}
		fb := blend(fin, fout)
		if fb <= 0 {
			return nil
		}
		folded, used, ok := foldPrice(fb, nativeCur, b.SupplierCur, b.FXRate)
		if !ok {
			st.FXMissingFolds++
			return nil
		}
		if used && r.FXUsed == nil {
			rate := *b.FXRate
			r.FXUsed = &rate
		}
		p := roundTo(folded/supplierBlend, 4)
		return &p
	}
	r.VsOverseas = foldFloor(b.OverseasIn, b.OverseasOut, "USD")
	r.VsDomestic = foldFloor(b.DomesticIn, b.DomesticOut, "CNY")

	// ── MarketAdvantage 候选信号：同区地板 → 综合地板 → 无信号 ──────────
	advRef := func() (refBlend float64, used bool, ok bool) {
		if regionFloorIn != nil || regionFloorOut != nil {
			if fb := blend(regionFloorIn, regionFloorOut); fb > 0 {
				return fb, false, true
			}
		}
		// 同区地板缺失（count=0 / 未映射 / 无价）⇒ 综合地板（CNY）折算。
		if b.ComprehensiveIn != nil || b.ComprehensiveOut != nil {
			if fb := blend(b.ComprehensiveIn, b.ComprehensiveOut); fb > 0 {
				folded, used, ok := foldPrice(fb, "CNY", b.SupplierCur, b.FXRate)
				if !ok {
					st.FXMissingFolds++
					return 0, false, false
				}
				return folded, used, true
			}
		}
		return 0, false, false
	}
	if refBlend, used, ok := advRef(); ok && supplierBlend > 0 {
		if used && r.FXUsed == nil {
			rate := *b.FXRate
			r.FXUsed = &rate
		}
		s := roundTo(100*math.Max(0, math.Min(1, 1-supplierBlend/refBlend)), 2)
		r.AdvantageScore = &s
	}

	if r.FXUsed != nil {
		st.FXFoldedRows = 1
	}
	return r, st
}

// ── Worker ───────────────────────────────────────────────────────────────

// MarketScoringRunStats 是一轮评分的汇总（ScoreNow 返回值 / LastRun 快照）。
type MarketScoringRunStats struct {
	RunAt           time.Time
	BindingsRead    int
	RowsWritten     int
	RowsDeleted     int
	FXFoldedRows    int
	FXMissingFolds  int
	VendorAbsent    int
	UnknownCurrency int
}

// MarketScoringWorker 每日重算 C1/C2/C3 双币评分落 supplier_market_position。
type MarketScoringWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	atHour   int
	atMinute int

	mu       sync.Mutex
	lastRun  MarketScoringRunStats
	hasStats bool

	stopOnce sync.Once
	started  bool
}

// NewMarketScoringWorker 构造 worker（默认每日 03:10 本地时）。
func NewMarketScoringWorker(db *pgxpool.Pool) *MarketScoringWorker {
	h, m := 3, 10
	if v := os.Getenv("LLM_GATEWAY_MARKET_SCORING_AT"); v != "" {
		if hh, mm, err := ParseMarketScoringAt(v); err == nil {
			h, m = hh, mm
		} else {
			slog.Warn("market scoring: bad LLM_GATEWAY_MARKET_SCORING_AT, using default",
				"value", v, "default", marketScoringDefaultAt, "error", err)
		}
	}
	return &MarketScoringWorker{db: db, done: make(chan struct{}), atHour: h, atMinute: m}
}

// Start 启动调度循环（阻塞式；调用方 `go w.Start(ctx)`）。
func (w *MarketScoringWorker) Start(ctx context.Context) {
	if w.started {
		return
	}
	w.started = true
	cctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	Go("market_scoring.run", func() { w.run(cctx) })
	slog.Info("market scoring worker started",
		"daily_at", fmt.Sprintf("%02d:%02d", w.atHour, w.atMinute),
		"catch_up_delay", marketScoringCatchUpDelay.String())
}

// Stop 取消循环并等待退出；对未 Start 的 worker 安全（no-op），可重入。
func (w *MarketScoringWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if !w.started {
		return
	}
	<-w.done
}

// LastRunStats 返回最近一轮评分汇总（未跑过 ⇒ false）。
func (w *MarketScoringWorker) LastRunStats() (MarketScoringRunStats, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hasStats {
		return MarketScoringRunStats{}, false
	}
	return w.lastRun, true
}

func (w *MarketScoringWorker) run(ctx context.Context) {
	defer close(w.done)

	// catch-up：表空或最近评分已过期 ⇒ 启动后先补一轮（幂等）。
	if w.catchUpNeeded(ctx) {
		timer := time.NewTimer(marketScoringCatchUpDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if _, err := w.ScoreNow(ctx); err != nil {
			slog.Warn("market scoring catch-up failed", "error", err)
		}
	}

	for {
		next := w.nextRunTime(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if _, err := w.ScoreNow(ctx); err != nil {
			slog.Warn("market scoring run failed", "error", err)
		}
	}
}

func (w *MarketScoringWorker) nextRunTime(now time.Time) time.Time {
	n := time.Date(now.Year(), now.Month(), now.Day(), w.atHour, w.atMinute, 0, 0, time.Local)
	if !n.After(now) {
		n = n.AddDate(0, 0, 1)
	}
	return n
}

func (w *MarketScoringWorker) catchUpNeeded(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var last time.Time
	err := w.db.QueryRow(cctx,
		`SELECT COALESCE(max(computed_at),'0001-01-01'::timestamptz) FROM public.supplier_market_position`).Scan(&last)
	if err != nil {
		// 表不存在等 ⇒ 交给 ScoreNow 的显式错误；catch-up 按「需要」处理。
		return true
	}
	return last.Before(time.Now().Add(-marketScoringStaleAfter))
}

// ScoreNow 跑一轮评分（读 → 纯函数 → 单事务写）。幂等重放：同一输入收敛。
func (w *MarketScoringWorker) ScoreNow(ctx context.Context) (MarketScoringRunStats, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// 读与写同事务（单快照）：评分读到的行情/绑定与写回的行在同一 MVCC
	// 快照上，不会把「A 时刻的行情」配「B 时刻的绑定」写进同一行。
	tx, err := w.db.Begin(cctx)
	if err != nil {
		return MarketScoringRunStats{}, fmt.Errorf("market scoring begin: %w", err)
	}
	defer func() { _ = tx.Rollback(cctx) }()

	rows, err := tx.Query(cctx, marketScoringReadSQL)
	if err != nil {
		return MarketScoringRunStats{}, fmt.Errorf("market scoring read: %w", err)
	}

	var bindings []*marketScoringBinding
	seen := map[[2]int64]struct{}{}
	dupes := 0
	for rows.Next() {
		b := &marketScoringBinding{}
		var vin, vout, fin, fout, din, dout, min_, mout *float64
		var vidCur, ovID, dmID *string // LATERAL 未命中 ⇒ 全列 NULL ⇒ 可空扫描
		var ovCnt, dmCnt *int
		if err := rows.Scan(&b.CredentialID, &b.ProviderModelID, &b.CanonicalName,
			&b.SupplierCur, &b.SupplierIn, &b.SupplierOut, &b.PriceConfidence,
			&vin, &vout, &vidCur, &b.FXRate,
			&fin, &fout, &ovID, &ovCnt,
			&din, &dout, &dmID, &dmCnt,
			&min_, &mout); err != nil {
			return MarketScoringRunStats{}, fmt.Errorf("market scoring scan: %w", err)
		}
		b.VendorListIn, b.VendorListOut = vin, vout
		if vidCur != nil {
			b.VendorListCur = *vidCur
		}
		if ovID != nil {
			b.OverseasID = *ovID
		}
		if dmID != nil {
			b.DomesticID = *dmID
		}
		if ovCnt != nil {
			b.OverseasCount = *ovCnt
		}
		if dmCnt != nil {
			b.DomesticCount = *dmCnt
		}
		b.OverseasIn, b.OverseasOut = fin, fout
		b.DomesticIn, b.DomesticOut = din, dout
		b.ComprehensiveIn, b.ComprehensiveOut = min_, mout
		// mpr 行命中判定：vendor_list_currency 列非 NULL ⇒ 行在（NOT NULL 列）。
		b.HasRef = vidCur != nil
		key := [2]int64{b.CredentialID, b.ProviderModelID}
		if _, dup := seen[key]; dup {
			// 防御：cmb UNIQUE 保证一绑定一行（940 PK 语义 + 视图行数门）；
			// 若被违反，ON CONFLICT DO UPDATE 会 21005 —— 丢弃重复并计数。
			dupes++
			continue
		}
		seen[key] = struct{}{}
		bindings = append(bindings, b)
	}
	if err := rows.Err(); err != nil {
		return MarketScoringRunStats{}, fmt.Errorf("market scoring rows: %w", err)
	}
	rows.Close()
	if dupes > 0 {
		slog.Warn("market scoring: duplicate binding rows dropped",
			"dupes", dupes)
	}

	stats := MarketScoringRunStats{RunAt: time.Now(), BindingsRead: len(bindings)}
	trustedCount := 0
	if len(bindings) == 0 {
		w.recordRun(stats)
		return stats, nil
	}

	// 单事务写（读已在事务内）：advisory 锁串行化多实例；staging COPY + upsert + 反连接删。
	if _, err := tx.Exec(cctx,
		`SELECT pg_advisory_xact_lock(hashtext('market_scoring:daily'))`); err != nil {
		return stats, fmt.Errorf("market scoring lock: %w", err)
	}

	if _, err := tx.Exec(cctx, `
CREATE TEMP TABLE smp_stage (
    credential_id             bigint,
    provider_model_id         bigint,
    canonical_name            text,
    currency                  text,
    supplier_in_per_1m        numeric(14,6),
    supplier_out_per_1m       numeric(14,6),
    discount_vs_vendor        numeric(10,4),
    vendor_ref_in_per_1m      numeric(14,6),
    vendor_ref_out_per_1m     numeric(14,6),
    market_position           numeric(10,4),
    market_ref_in_per_1m      numeric(14,6),
    market_ref_out_per_1m     numeric(14,6),
    market_ref_platform_id    text,
    market_ref_platform_count int,
    price_confidence          text,
    market_advantage_score    numeric(6,2),
    market_position_vs_overseas numeric(10,4),
    market_position_vs_domestic numeric(10,4),
    market_ref_used           text,
    fx_used                   numeric(12,6),
    vendor_ref_currency       text
) ON COMMIT DROP`); err != nil {
		return stats, fmt.Errorf("market scoring stage: %w", err)
	}

	stage := make([][]any, 0, len(bindings))
	for _, b := range bindings {
		if b.PriceConfidence == "trusted" {
			trustedCount++
		}
		r, sst := scoreSupplierPosition(b)
		stats.FXFoldedRows += sst.FXFoldedRows
		stats.FXMissingFolds += sst.FXMissingFolds
		stats.VendorAbsent += sst.VendorAbsent
		stats.UnknownCurrency += sst.UnknownCurrency
		stage = append(stage, []any{
			b.CredentialID, b.ProviderModelID, b.CanonicalName, b.SupplierCur,
			b.SupplierIn, b.SupplierOut,
			r.DiscountVsVendor, r.VendorRefIn, r.VendorRefOut,
			r.MarketPosition, r.MarketRefIn, r.MarketRefOut,
			r.MarketRefID, int32(r.MarketRefCount),
			b.PriceConfidence, r.AdvantageScore,
			r.VsOverseas, r.VsDomestic, r.MarketRefUsed,
			r.FXUsed, r.VendorRefCur,
		})
	}

	if _, err := tx.CopyFrom(cctx, pgx.Identifier{"smp_stage"},
		[]string{"credential_id", "provider_model_id", "canonical_name", "currency",
			"supplier_in_per_1m", "supplier_out_per_1m",
			"discount_vs_vendor", "vendor_ref_in_per_1m", "vendor_ref_out_per_1m",
			"market_position", "market_ref_in_per_1m", "market_ref_out_per_1m",
			"market_ref_platform_id", "market_ref_platform_count",
			"price_confidence", "market_advantage_score",
			"market_position_vs_overseas", "market_position_vs_domestic",
			"market_ref_used", "fx_used", "vendor_ref_currency"},
		pgx.CopyFromRows(stage)); err != nil {
		return stats, fmt.Errorf("market scoring copy: %w", err)
	}

	utag, err := tx.Exec(cctx, `
INSERT INTO public.supplier_market_position
    (credential_id, provider_model_id, canonical_name, currency,
     supplier_in_per_1m, supplier_out_per_1m,
     discount_vs_vendor, vendor_ref_in_per_1m, vendor_ref_out_per_1m,
     market_position, market_ref_in_per_1m, market_ref_out_per_1m,
     market_ref_platform_id, market_ref_platform_count,
     price_confidence, market_advantage_score,
     market_position_vs_overseas, market_position_vs_domestic,
     market_ref_used, fx_used, vendor_ref_currency)
SELECT credential_id, provider_model_id, canonical_name, currency,
     supplier_in_per_1m, supplier_out_per_1m,
     discount_vs_vendor, vendor_ref_in_per_1m, vendor_ref_out_per_1m,
     market_position, market_ref_in_per_1m, market_ref_out_per_1m,
     market_ref_platform_id, market_ref_platform_count,
     price_confidence, market_advantage_score,
     market_position_vs_overseas, market_position_vs_domestic,
     market_ref_used, fx_used, vendor_ref_currency
FROM smp_stage
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
    canonical_name              = EXCLUDED.canonical_name,
    currency                    = EXCLUDED.currency,
    supplier_in_per_1m          = EXCLUDED.supplier_in_per_1m,
    supplier_out_per_1m         = EXCLUDED.supplier_out_per_1m,
    discount_vs_vendor          = EXCLUDED.discount_vs_vendor,
    vendor_ref_in_per_1m        = EXCLUDED.vendor_ref_in_per_1m,
    vendor_ref_out_per_1m       = EXCLUDED.vendor_ref_out_per_1m,
    market_position             = EXCLUDED.market_position,
    market_ref_in_per_1m        = EXCLUDED.market_ref_in_per_1m,
    market_ref_out_per_1m       = EXCLUDED.market_ref_out_per_1m,
    market_ref_platform_id      = EXCLUDED.market_ref_platform_id,
    market_ref_platform_count   = EXCLUDED.market_ref_platform_count,
    price_confidence            = EXCLUDED.price_confidence,
    market_advantage_score      = EXCLUDED.market_advantage_score,
    market_position_vs_overseas = EXCLUDED.market_position_vs_overseas,
    market_position_vs_domestic = EXCLUDED.market_position_vs_domestic,
    market_ref_used             = EXCLUDED.market_ref_used,
    fx_used                     = EXCLUDED.fx_used,
    vendor_ref_currency         = EXCLUDED.vendor_ref_currency,
    computed_at                 = now()`)
	if err != nil {
		return stats, fmt.Errorf("market scoring upsert: %w", err)
	}
	stats.RowsWritten = int(utag.RowsAffected())

	dtag, err := tx.Exec(cctx, `
DELETE FROM public.supplier_market_position s
 WHERE NOT EXISTS (SELECT 1 FROM smp_stage st
                    WHERE st.credential_id = s.credential_id
                      AND st.provider_model_id = s.provider_model_id)`)
	if err != nil {
		return stats, fmt.Errorf("market scoring delete stale: %w", err)
	}
	stats.RowsDeleted = int(dtag.RowsAffected())

	// 961 运行台账（price_confidence_degraded 环比的读数源）：与评分同事务，
	// 幂等重放（ON CONFLICT (run_date)）。★ 台账缺席的检查必须在**事务外**：
	// PG 里任何语句失败都会中止事务（in-tx 容错再提交只会得到
	// 「commit unexpectedly resulted in rollback」）——先探 to_regclass，
	// 缺席 ⇒ 显式留痕后跳过（该环境下告警面 Optional 也会跳过，
	// 但「告警无源」必须可见，不许安静吞掉）。
	var ledgerPresent bool
	if err := w.db.QueryRow(cctx,
		`SELECT to_regclass('public.market_scoring_runs') IS NOT NULL`).Scan(&ledgerPresent); err != nil {
		ledgerPresent = false
	}
	if ledgerPresent {
		if _, err := tx.Exec(cctx, `
INSERT INTO public.market_scoring_runs
    (run_date, bindings, trusted_count, non_trusted_count,
     fx_folded_rows, fx_missing_folds, vendor_absent, unknown_currency)
VALUES (current_date, $1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (run_date) DO UPDATE SET
    bindings          = EXCLUDED.bindings,
    trusted_count     = EXCLUDED.trusted_count,
    non_trusted_count = EXCLUDED.non_trusted_count,
    fx_folded_rows    = EXCLUDED.fx_folded_rows,
    fx_missing_folds  = EXCLUDED.fx_missing_folds,
    vendor_absent     = EXCLUDED.vendor_absent,
    unknown_currency  = EXCLUDED.unknown_currency,
    computed_at       = now()`,
			stats.BindingsRead, trustedCount, stats.BindingsRead-trustedCount,
			stats.FXFoldedRows, stats.FXMissingFolds, stats.VendorAbsent, stats.UnknownCurrency); err != nil {
			return stats, fmt.Errorf("market scoring ledger: %w", err)
		}
	} else {
		slog.Warn("market scoring: ledger table missing (961 not applied) — " +
			"price_confidence_degraded alert will have no history source")
	}

	if err := tx.Commit(cctx); err != nil {
		return stats, fmt.Errorf("market scoring commit: %w", err)
	}

	w.recordRun(stats)
	slog.Info("market scoring run complete",
		"bindings", stats.BindingsRead, "written", stats.RowsWritten,
		"deleted_stale", stats.RowsDeleted,
		"fx_folded_rows", stats.FXFoldedRows,
		"fx_missing_folds", stats.FXMissingFolds,
		"vendor_absent", stats.VendorAbsent,
		"unknown_currency", stats.UnknownCurrency)
	return stats, nil
}

func (w *MarketScoringWorker) recordRun(stats MarketScoringRunStats) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastRun = stats
	w.hasStats = true
}
