package admin

// `/api/admin/market/compare` 的**真库行为门**（F13，2026-11-17）。
//
// # 它证明什么
//
// 生产事故：`GET /api/admin/market/compare` 对**任何调用方**都 500，
// 容器日志逐字：
//   "caller":"market_compare.go:187",
//   "err":"can't scan into dest[24] (col: overseas_min_platform_id): cannot scan NULL into *string"
//
// 根因：`mpr` 是 `LEFT JOIN LATERAL`（按 canonical 名去 940 行情面取双地板）
// ⇒ **未命中时整组 mpr.* 全 NULL** ⇒ 其中 overseas/domestic 的
// platform_id（string）与 platform_count（int）扫进裸类型 ⇒ 炸。
// 211 生产实测：前 200 行里 **194 行**没有 mpr 匹配；全量 4176 行里
// **2396 行（57%）**没有 ⇒ 「没有行情行」是**常态**，不是异常。
//
// # 为什么必须是真库门（★ 量具纪律）
//
// 仓里的 pgxmock 单测**测不出这个缺陷族**：pgxmock 的 Scan 语义与真 pgx 不同
// ① NULL 时它直接把目标置空、**不报错**（真 pgx 会报 NULL→*string）；
// ② 对 `**T` 目的地与真 pgx 行为也不同。
// ⇒ 单测能守住「响应语义」，守不住「不再 500」；**只有真 pgx + 真数据**能。
// 本门跑**生产 handler 本体**（`Handler{marketPlatformDB: 真实连接池}`）⇒ 复现生产路径。
//
// ★ 只读：本门只发 SELECT（to_regclass 探针 + 主查询 + 汇总查询）⇒ 可在生产库上跑。
// ★ 未设 TEST_DATABASE_URL 时**静默 Skip** —— Skip ≠ 通过。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketCompareRealDBToleratesMissingMarketPriceLateral(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — 需要真库（只读 SELECT）；Skip ≠ 通过")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连真库失败：%v", err)
	}
	defer pool.Close()

	h := &Handler{marketPlatformDB: pool}
	rr := httptest.NewRecorder()
	h.handleMarketCompare(rr, httptest.NewRequest(http.MethodGet, "/api/admin/market/compare?limit=200", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("compare 端点应 200（生产 500 的复现路径），得到 %d：%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Available bool               `json:"available"`
		Items     []marketCompareRow `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("非法 JSON：%v\n%s", err, rr.Body.String())
	}
	if !out.Available {
		t.Fatal("available=false ⇒ 评分表缺席？真库形状变了？")
	}
	if len(out.Items) == 0 {
		t.Fatal("items 为空 ⇒ 门空跑（真库里没有评分行？）")
	}

	// ★ 量具不许腐化：必须有行真的走到「LATERAL 未命中」这条路，
	//   否则本门退化成「端点没炸」的弱断言。
	noLateral := 0
	for _, r := range out.Items {
		if r.OverseasID == "" && r.DomesticID == "" {
			noLateral++
		}
	}
	if noLateral == 0 {
		t.Fatalf("%d 行里没有一行「LATERAL 未命中」⇒ 该路径未被覆盖 ⇒ 门腐化", len(out.Items))
	}
	t.Logf("真库读数：%d 行，其中 LATERAL 未命中 %d 行", len(out.Items), noLateral)
}
