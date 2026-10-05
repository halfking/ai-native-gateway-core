package admin

// `pricingImport` 的真库判据 —— 目标第二半「根据供应商的实际计费方式与价格
// 进行设置」的**唯一落地口**。
//
// # 为什么需要这一批（2026-10-06）
//
// 端点是 `POST /api/pricing/import`（admin/pricing.go:pricingImport），而
// `cmd/tools/propose-supplier-prices` 产出的 CSV **唯一的去处就是这里**。
// 真库实测那批提案是 **630 offer / 14 接受 / 616 拒收** —— 也就是说，
// 「准确控制实际成本」这条目标的**写侧终点**就是这个 handler。
//
// 而它此前**零真库判据**（`grep -rn pricingImport --include=*_test.go` = 4，
// 全是 cmd/tools 侧对自己 CSV 形状的断言，没有一条真的走过这个 handler）。
//
// # 缺陷是怎么被发现的（读代码 + 实测确认，不是推理）
//
// 原来的列循环是「**先** append SET 子句（占掉 `$N`）、**再** ParseFloat，
// 失败就 continue」：
//
//	499  setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
//	500  if typ == "float" {
//	501      if f, err := strconv.ParseFloat(val, 64); err == nil {
//	502          args = append(args, f)
//	503      } else {
//	504          continue          ← 子句已经进了 setClauses，实参没进 args
//	505      }
//
// ⇒ 拼出来的 SQL 占位符数与实参数对不上，bind 报
// `expected N arguments, got M`，而紧接着的 `if err != nil { continue }`
// 把**整行静默丢弃**。且 `argIdx` 也没 ++，后续列复用同一个 `$N`。
//
// 后果：**CSV 里只要一个价格格是脏值，同一行其它正确的价格列也一起丢掉**，
// 而响应只有 `{"updated": N}` —— 被丢的行与「本来就不该改的行」长得一模一样。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具对象已存在时也跳过（它要建表）。

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pricingImportFixture 搭出 model_offers 的最小生产形态。
//
// ★ 为什么不用对象 SSOT：`sql/objects/tables/` 里**没有** model_offers
// （只有 model_offer_events.sql），而这个 handler 只 UPDATE 8 列 ⇒ 手抄这
// 8 列是可控的，且没有 FK 牵连。
func pricingImportFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = 'model_offers'`).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skip("model_offers already exists — this test drops it")
	}

	// ★ 注册在**建表之前**。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DROP TABLE IF EXISTS public.model_offers;`)
	})

	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.model_offers (
		    id                       bigserial PRIMARY KEY,
		    unit_price_in_per_1m     numeric,
		    unit_price_out_per_1m    numeric,
		    cache_read_price_per_1m  numeric,
		    cache_write_price_per_1m numeric,
		    currency                 text,
		    billing_mode             text,
		    pricing_source           text,
		    pricing_updated_at       timestamptz);`); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// 量具自证。
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
		JOIN pg_namespace ns ON ns.oid = c.relnamespace
		WHERE ns.nspname='public' AND c.relname='model_offers'`).Scan(&n); err != nil {
		t.Fatalf("self-check: %v", err)
	}
	if n != 1 {
		t.Fatal("fixture self-check: public.model_offers is absent — assertions would fail for the wrong reason")
	}
}

// postImport 把 CSV 文本走 multipart 打给 pricingImport，返回解码后的响应。
func postImport(t *testing.T, h *Handler, csv string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "prices.csv")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte(csv)); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pricing/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.pricingImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func pricingImportRealDB(t *testing.T) (*pgxpool.Pool, context.Context, *Handler) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	pricingImportFixture(t, ctx, pool)
	return pool, ctx, &Handler{db: pool}
}

// ① 正常导入：每列都落库，且盖章 pricing_source / pricing_updated_at。
func TestPricingImport_writesEveryPriceColumn(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	csv := "offer_id,unit_price_in_per_1m,unit_price_out_per_1m,cache_read_price_per_1m,currency,billing_mode\n" +
		offerIDStr(id) + ",3.0,15.0,0.3,USD,per_token\n"
	out := postImport(t, h, csv)
	if u, _ := out["updated"].(float64); u != 1 {
		t.Errorf("updated=%v, want 1 (response=%v)", out["updated"], out)
	}

	var pIn, pOut, pCache, src string
	var updatedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT COALESCE(unit_price_in_per_1m,0)::text,
		COALESCE(unit_price_out_per_1m,0)::text, COALESCE(cache_read_price_per_1m,0)::text,
		COALESCE(pricing_source,''), pricing_updated_at FROM public.model_offers WHERE id=$1`, id).
		Scan(&pIn, &pOut, &pCache, &src, &updatedAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if pIn != "3" || pOut != "15" || pCache != "0.3" {
		t.Errorf("prices not written: in=%s out=%s cache=%s", pIn, pOut, pCache)
	}
	if src != "imported" {
		t.Errorf("pricing_source=%q, want %q — without the stamp these rows look hand-maintained", src, "imported")
	}
	if updatedAt == nil {
		t.Error("pricing_updated_at must be stamped; the staleness checks read this column")
	}
}

// ② ★ 承重：脏值只让**那一列**不参与更新，同行其它列必须仍然落库。
//
//	这是 2026-10-06 修的那个缺陷（占位符/实参错位 → 整行静默丢弃）。
func TestPricingImport_badCellDoesNotDropTheWholeRow(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	// 第二列是脏值（人手工编辑 CSV 时最常见的形态），第三列是好的。
	csv := "offer_id,unit_price_in_per_1m,unit_price_out_per_1m,currency\n" +
		offerIDStr(id) + ",N/A,15.0,USD\n"
	out := postImport(t, h, csv)
	if u, _ := out["updated"].(float64); u != 1 {
		t.Fatalf("updated=%v, want 1 — one bad cell must not discard the whole row (response=%v)",
			out["updated"], out)
	}

	var pIn, pOut, cur string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(unit_price_in_per_1m,0)::text,
		COALESCE(unit_price_out_per_1m,0)::text, COALESCE(currency,'')
		FROM public.model_offers WHERE id=$1`, id).Scan(&pIn, &pOut, &cur); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if pIn != "0" {
		t.Errorf("the bad cell must be left untouched, got unit_price_in_per_1m=%s (want 0/NULL)", pIn)
	}
	if pOut != "15" {
		t.Errorf("the GOOD column next to the bad one was lost: unit_price_out_per_1m=%s, want 15", pOut)
	}
	if cur != "USD" {
		t.Errorf("currency=%q, want USD — string columns are not affected by a float failure", cur)
	}
}

// ③ ★ 丢弃必须**在响应里看得见**。原来只有 updated 计数，被丢的行与
//
//	「本来就不该改的行」在响应里长得一模一样。
func TestPricingImport_reportsRejectedRows(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	// 三行：正常 / offer_id 不是数字 / offer_id 指向不存在的行
	csv := "offer_id,unit_price_in_per_1m\n" +
		offerIDStr(id) + ",3.0\n" +
		"not-a-number,4.0\n" +
		",5.0\n" +
		"999999,6.0\n"
	out := postImport(t, h, csv)
	if u, _ := out["updated"].(float64); u != 1 {
		t.Errorf("updated=%v, want 1 (only the existing offer_id matches)", out["updated"])
	}
	rej, ok := out["rejected_rows"].(map[string]any)
	if !ok {
		t.Fatalf("response has no rejected_rows — dropped rows are invisible to the operator: %v", out)
	}
	if v, _ := rej["bad_or_missing_offer_id"].(float64); v != 2 {
		t.Errorf("bad_or_missing_offer_id=%v, want 2 (one unparsable + one empty)", rej["bad_or_missing_offer_id"])
	}
	if _, has := out["message"]; !has {
		t.Error("response must carry a message saying the import is incomplete — " +
			"`updated` alone reads as \"the whole file landed\"")
	}
}

// ④ 对照组：全部干净时，响应里**不出现** rejected_rows / message。
//
//	少了它，一个「永远带着 rejected_rows」的响应也会让 ③ 变绿。
func TestPricingImport_cleanFileReportsNothingRejected(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}
	out := postImport(t, h, "offer_id,unit_price_in_per_1m\n"+offerIDStr(id)+",3.0\n")
	if u, _ := out["updated"].(float64); u != 1 {
		t.Errorf("updated=%v want 1", out["updated"])
	}
	if _, ok := out["rejected_rows"]; ok {
		t.Errorf("a clean file must not claim rejections were counted: %v", out)
	}
	if _, ok := out["message"]; ok {
		t.Errorf("a clean file must not carry the incomplete-import message: %v", out)
	}
}

// offerIDStr 不叫 itoa：admin 包里已有一个 itoa(int)（memora_handlers_test.go），
// 同名会在整包编译时冲突。
func offerIDStr(v int64) string { return strconv.FormatInt(v, 10) }

// ⑤ ★ 接缝：空串格必须意味着「这一列不参与更新」，**绝不能**变成 0。
//
// 这一条与 cmd/tools/propose-supplier-prices 的 `f64(nil) == ""` 是一对：
// 那侧把「未知价格」渲染成空串，这侧把空串当「跳过」。任何一侧改动都会破。
//
// 为什么是承重的：`cmd/tools/propose-supplier-prices` 产出的 CSV 里，
// nil 价格写的是**空串**（不是 0）。若这一侧把空串当成 0 落库，库里就会出现
// 「per_token 有价但等于 0」的行 —— 正是第 13 条健康检查
// supplier_price_missing_from_cost 专门盯的那种状态（真库实测当前
// 「可路由 + per_token + 有价」是 0 条）。一次渲染改动就能凭空造出那批告警，
// 而且看不出来源。
func TestPricingImport_emptyCellIsSkippedNotZeroed(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	// 形状照 cmd/tools/propose-supplier-prices 的 writeImportCSV：
	// 三个价格列里只填了 in，out 与 cache 是空串（= 未知价格）。
	csv := "offer_id,unit_price_in_per_1m,unit_price_out_per_1m,cache_read_price_per_1m,currency\n" +
		offerIDStr(id) + ",3.0,,,USD\n"
	out := postImport(t, h, csv)
	if u, _ := out["updated"].(float64); u != 1 {
		t.Fatalf("updated=%v want 1 (response=%v)", out["updated"], out)
	}

	var pIn, pOut, pCache *string
	if err := pool.QueryRow(ctx, `SELECT
		unit_price_in_per_1m::text, unit_price_out_per_1m::text, cache_read_price_per_1m::text
		FROM public.model_offers WHERE id=$1`, id).Scan(&pIn, &pOut, &pCache); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if pIn == nil || *pIn != "3" {
		t.Errorf("unit_price_in_per_1m=%v, want 3", pIn)
	}
	if pOut != nil {
		t.Errorf("unit_price_out_per_1m=%v, want NULL — an empty cell means \"price unknown\", "+
			"writing 0 would fabricate a free price (and trip supplier_price_missing_from_cost)", *pOut)
	}
	if pCache != nil {
		t.Errorf("cache_read_price_per_1m=%v, want NULL", *pCache)
	}
}

// ⑥ ★ 空串与脏值必须**可区分** —— 否则日志就在说谎。
//
// 上面 ⑤ 只钉了结果（那一列不写），但它在两种实现下都成立：
//
//	(a) 空串 → 显式跳过（正确）；
//	(b) 空串 → 落进 ParseFloat 失败那条路（teeth T5 变异出来的形态）。
//
// 两者**落库结果完全一样**，于是 ⑤ 对这个差异毫无判别力（实测 T5 全绿）。
//
// 而两者的差别是**运维可见的**：走 (b) 时，每个「价格未知」的行都会打出一条
// `some price cells in this row were not importable` 的 Warn —— 而价格未知是
// **条件价场景下的常态**（propose-supplier-prices 产出的 CSV 里，nil 价格
// 本来就是空串）。⇒ 每次正常导入都刷一批「有格不可导入」的告警，
// 而真正该喊的脏值被淹没。**告警一旦 routinely 触发就等于没有告警。**
//
// 所以这里钉日志：干净的一行（空串 + 无脏值）**不得**出现任何 Warn。
func TestPricingImport_cleanRowWithEmptyCellsLogsNothing(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	// 捕获 slog：判据关心的是「有没有对着正常行喊脏值」。
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	postImport(t, h, "offer_id,unit_price_in_per_1m,unit_price_out_per_1m,currency\n"+
		offerIDStr(id)+",3.0,,USD\n")

	got := logs.String()
	if strings.Contains(got, "not importable") {
		t.Errorf("a clean row (empty cells = price unknown) must not be reported as "+
			"\"not importable\" — that warning fires on every conditional-pricing row and "+
			"would bury the real bad cells. Log:\n%s", got)
	}
}

// ⑦ 对照组：真脏值**必须**留下日志（否则 ⑥ 变成「永远不喊」）。
func TestPricingImport_badCellStillLogs(t *testing.T) {
	pool, ctx, h := pricingImportRealDB(t)
	if _, err := pool.Exec(ctx, `INSERT INTO public.model_offers DEFAULT VALUES`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM public.model_offers LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("read id: %v", err)
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	postImport(t, h, "offer_id,unit_price_in_per_1m,unit_price_out_per_1m\n"+
		offerIDStr(id)+",N/A,15.0\n")

	if !strings.Contains(logs.String(), "not importable") {
		t.Errorf("a genuinely unparsable cell must be logged — otherwise ⑥'s rule "+
			"degenerates into never warning at all. Log:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "unit_price_in_per_1m") {
		t.Errorf("the log must name the offending column. Log:\n%s", logs.String())
	}
}
