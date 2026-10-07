package bg

// baseline_price_ssot_parity_realdb_test.go —— 「真库里的基准价 == SSOT 文件」
//
// 这是 2026-10-06 数据填充审计的第二层防线。第一层
// （baseline_price_single_writer_test.go）防「有人另开写价路径」，
// 本条防「库里的值已经不是 SSOT 了」——两者的失效方向不同，都要有。
//
// ★ 本条最要紧的不是比对逻辑，是**非恒真守卫**。
// 集成门那套一次性新库是从迁移+种子长出来的，种子**不带**基准价
// （实测：sql/schema/ 下无任何文件写 baseline_input_price_per_1m），
// 所以在那里跑本条会「0 行可比」。若那时报 PASS，读数就与
// 「逐条比对过且一致」完全同形 —— 这是最坏的一种绿。
// 因此可比集为空时**只 Skip 并说明没比对任何东西**，绝不报 PASS。
//
// 用法：对**已跑过同步**的库运行。
//
//	export TEST_DATABASE_URL="postgres://…/llm_gateway?sslmode=disable"
//	go test ./bg/ -run TestBaselinePricesInDatabaseMatchTheSSOT -v
//
// 2026-10-06 对真库实测：补齐前 cache_write 14/16 缺失、fetched_at 12/16 不符；
// 跑 RunBaselinePriceSync 补齐后本条 PASS，且第二遍同步指纹不变（幂等）。

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dbBaselineRow 是一个模型在真库里的基准价快照。
type dbBaselineRow struct {
	Currency    string
	Input       *float64
	Output      *float64
	CacheRead   *float64
	CacheWrite  *float64
	Vendor      string
	SourceURL   string
	FetchedAt   *time.Time
	InputIsNull bool
}

func TestBaselinePricesInDatabaseMatchTheSSOT(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本条需要**已跑过同步**的库；" +
			"在未同步的库上它会比对 0 行，那不是「通过」而是「没比对」")
	}

	catalog, err := LoadEmbeddedBaselineCatalog()
	if err != nil {
		t.Fatalf("加载 SSOT 失败: %v", err)
	}
	if len(catalog) == 0 {
		t.Fatalf("SSOT 是空的（%d 条），本条无从比对；"+
			"空清单是合法状态，但那意味着「没有基准价可校验」", len(catalog))
	}

	ctx := context.Background()
	pool := newBaselineParityPool(t, dsn)

	// 表本身可能不存在（夹具库/未跑迁移的库）。那种情况**不是失败** ——
	// 本条在这个库上无法运行。t.Fatalf 在这里只会造出「因环境而红」的噪声，
	// 而噪声会训练人忽略红色。具名 SKIP。
	var tbl *string
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.models_canonical')::text`).Scan(&tbl); err != nil {
		t.Fatalf("探测 models_canonical: %v", err)
	}
	if tbl == nil {
		t.Skip("该库没有 public.models_canonical（未跑迁移）—— 本条在此库无法运行，" +
			"这是「跑不了」不是「通过」")
	}

	rows := queryBaselineRows(ctx, t, pool)
	if len(rows) == 0 {
		t.Skipf("库里 0 个模型带基准价 ⇒ 本条**没有比对任何东西**。"+
			"这不是通过。要让它有意义，先对同一个库跑 bg.RunBaselinePriceSync "+
			"（或 bg.SyncBaselinePricesToDB），再回来跑本条。SSOT 有 %d 条。", len(catalog))
	}

	// 方向一：SSOT 里有、库里也带基准价的，逐字段比。
	var mismatches []string
	for name, want := range catalog {
		got, ok := rows[name]
		if !ok {
			// 库里有 SSOT 没有的行 → 方向二报；这里只关心 SSOT→库
			continue
		}
		mismatches = append(mismatches, compareOne(name, want, got)...)
	}
	// 方向二：库里有基准价、但 SSOT 里没有 —— 那是「来源不明」的价，最该报。
	for name := range rows {
		if _, ok := catalog[name]; !ok {
			mismatches = append(mismatches,
				name+": 库里有基准价但 SSOT 里没有这一条（来源不明的价）")
		}
	}
	// 方向三：SSOT 有、库里压根没有这个模型的基准价。
	for name := range catalog {
		if _, ok := rows[name]; !ok {
			mismatches = append(mismatches,
				name+": SSOT 有但库里没有基准价（未同步或模型未入库）")
		}
	}

	t.Logf("比对了 %d 个库内模型 × %d 个字段（SSOT %d 条）",
		len(rows), len(baselinePriceColumns), len(catalog))
	if len(mismatches) > 0 {
		t.Fatalf("真库基准价与 SSOT 不一致，共 %d 处：\n  %s\n"+
			"修法是跑官方入口 bg.RunBaselinePriceSync / bg.SyncBaselinePricesToDB，"+
			"**不要手工 UPDATE** —— 手工路径正是 2026-10-06 丢掉 cache_write 的原因。",
			len(mismatches), joinLines(mismatches))
	}
}

// ── 以下辅助 ──

// newBaselineParityPool 只建连接。
//
// ★ 刻意**不**复用同包的 orphanPool：那个是破坏性夹具 —— 它要求那五张表在库里
// 不存在，存在就 Skip，清理时 DROP 它们。对真库（models_canonical 有 960 行）
// 它会 Skip，也就是「碰巧安全」；但本条要读的正是真 models_canonical，语义完全
// 不同。所以这里另开一个**只读**连接：不建表、不删表、不接管任何表。
func newBaselineParityPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// queryBaselineRows 读出库里**所有带基准价**的模型。
func queryBaselineRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]dbBaselineRow {
	t.Helper()
	q := `SELECT canonical_name,
	             baseline_price_currency,
	             baseline_input_price_per_1m, baseline_output_price_per_1m,
	             baseline_cache_read_price_per_1m, baseline_cache_write_price_per_1m,
	             baseline_price_vendor, baseline_price_source_url, baseline_price_fetched_at
	        FROM public.models_canonical
	       WHERE baseline_input_price_per_1m IS NOT NULL
	       ORDER BY canonical_name`
	rs, err := pool.Query(ctx, q)
	if err != nil {
		t.Fatalf("query baseline rows: %v", err)
	}
	defer rs.Close()

	out := map[string]dbBaselineRow{}
	for rs.Next() {
		var (
			name                     string
			r                        dbBaselineRow
			in, out_, cr, cw         *float64
			cur, ven, url            *string
			ts                       *time.Time
		)
		if err := rs.Scan(&name, &cur, &in, &out_, &cr, &cw, &ven, &url, &ts); err != nil {
			t.Fatalf("scan baseline row: %v", err)
		}
		if cur != nil {
			r.Currency = *cur
		}
		if ven != nil {
			r.Vendor = *ven
		}
		if url != nil {
			r.SourceURL = *url
		}
		r.Input, r.Output, r.CacheRead, r.CacheWrite = in, out_, cr, cw
		r.FetchedAt = ts
		out[name] = r
	}
	if err := rs.Err(); err != nil {
		t.Fatalf("iterate baseline rows: %v", err)
	}
	return out
}

// compareOne 逐字段比一个模型，返回不一致处的描述（空切片 = 一致）。
func compareOne(name string, want BaselinePrice, got dbBaselineRow) []string {
	var bad []string
	// 数值：两边都空算一致；一边空算「库缺值」。
	num := func(label string, w, g *float64) {
		switch {
		case w == nil && g == nil:
		case w == nil && g != nil:
			bad = append(bad, fmt.Sprintf("%s: SSOT 无值但库里有 %g", label, *g))
		case w != nil && g == nil:
			bad = append(bad, fmt.Sprintf("%s: SSOT=%g 但库里是 NULL", label, *w))
		case math.Abs(*w-*g) > 1e-9:
			bad = append(bad, fmt.Sprintf("%s: SSOT=%g 库=%g", label, *w, *g))
		}
	}
	num("input", want.InputPer1M, got.Input)
	num("output", want.OutputPer1M, got.Output)
	num("cache_read", want.CacheReadPer1M, got.CacheRead)
	num("cache_write", want.CacheWritePer1M, got.CacheWrite)

	str := func(label, w, g string) {
		if strings.TrimSpace(w) != strings.TrimSpace(g) {
			bad = append(bad, fmt.Sprintf("%s: SSOT=%q 库=%q", label, w, g))
		}
	}
	str("currency", want.Currency, got.Currency)
	str("vendor", want.Vendor, got.Vendor)
	str("source_url", want.SourceURL, got.SourceURL)

	// fetched_at：SSOT 是 RFC3339 字符串，库里是 timestamptz，比**时刻**不比字面。
	if wft, err := want.FetchedAtTime(); err == nil {
		switch {
		case got.FetchedAt == nil:
			bad = append(bad, fmt.Sprintf("fetched_at: SSOT=%s 但库里是 NULL", wft))
		case !wft.Equal(*got.FetchedAt):
			bad = append(bad, fmt.Sprintf("fetched_at: SSOT=%s 库=%s",
				wft.Format(time.RFC3339), got.FetchedAt.Format(time.RFC3339)))
		}
	}
	return bad
}

func joinLines(items []string) string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, "    "+s)
	}
	return strings.Join(out, "\n")
}