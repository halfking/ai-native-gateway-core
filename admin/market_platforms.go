package admin

// market_platforms.go — 定稿 M1-T3：市场平台注册表（迁移 958）的 admin 读写面。
//
//	GET /api/admin/market/platforms             全量清单（region, platform_id 排序）
//	PUT /api/admin/market/platforms/{platformId} 单行部分更新（指针字段 = 只改提交的字段）
//
// 权限 = h.superAdmin（平台注册表是 A-17 双币制的区域/币种裁定权威 + A-5
// 我方平台排除清单：改 region/native_currency 会移动 959 视图的市场地板、
// 改 is_ours/enabled 会改变同步器与 C2 地板的平台集合 ⇒ 内部运维裁决面）。
//
// ★ 红线锚点：本表只承载「平台元数据」（区域/币种/我方标记），不承载任何
// 价格数字 ⇒ CRUD 面不存在「行情价回流路由/计费」的通路（价格面在
// market_price_reference / supplier_market_position，只读对账）。
//
// ★ PUT 语义：
//   - platform_id 是主键且与 provider_catalog/syncer 对齐 ⇒ 不可变（无改名/无
//     INSERT：补录新平台的口径归迁移种子 + 运营裁决，见
//     platform_region_unmapped 告警的处置文案）；
//   - 校验与 DB CHECK（market_platform_registry_region_chk / _currency_chk /
//     _pair_chk）同形：region ∈ {overseas,domestic}、native_currency ∈
//     {USD,CNY}、overseas↔USD / domestic↔CNY 配对；只提交一半时按库内现值
//     配对校验（先 SELECT 后 UPDATE，两步同一 handler 内完成）；
//   - 审计：每次成功 UPDATE 落一行 routing_audit_log（actor=requestActor，
//     before/after 都进 after_json），失败 best-effort（与 users.go 同纪律）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
)

// marketPlatformDB 市场平台注册表读写面（Query/QueryRow/Exec）。pgxmock 与
// 真实连接池都实现 ⇒ handler 单测可注入（topologyStatsDB 同款范式）。
type marketPlatformDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// marketPlatformsQuerier 解析注入口：显式注入优先，否则回 h.db。
// ★ h.db 是具体类型 *pgxpool.Pool：nil 时装进接口就是非 nil 接口值
// （typed-nil 陷阱）⇒ 必须显式判 nil 再返回，否则 nil pool 直穿到 Query。
func (h *Handler) marketPlatformsQuerier() marketPlatformDB {
	if h.marketPlatformDB != nil {
		return h.marketPlatformDB
	}
	if h.db == nil {
		return nil
	}
	return h.db
}

// marketPlatformRow 与 958 表列一一对应（GET/PUT 的 JSON 形状）。
type marketPlatformRow struct {
	PlatformID     string `json:"platform_id"`
	PlatformName   string `json:"platform_name"`
	Region         string `json:"region"`
	NativeCurrency string `json:"native_currency"`
	IsOurs         bool   `json:"is_ours"`
	Enabled        bool   `json:"enabled"`
	Notes          string `json:"notes"`
	UpdatedAt      string `json:"updated_at"`
}

const marketPlatformCols = `platform_id, platform_name, region, native_currency,
is_ours, enabled, notes, updated_at`

// handleMarketPlatformsList GET /api/admin/market/platforms
func (h *Handler) handleMarketPlatformsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := h.marketPlatformsQuerier()
	if q == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := q.Query(ctx, `
SELECT `+marketPlatformCols+`
  FROM public.market_platform_registry
 ORDER BY region, platform_id`)
	if err != nil {
		writeInternalErr(w, "market platforms list", err)
		return
	}
	defer rows.Close()
	items := make([]marketPlatformRow, 0, 40)
	for rows.Next() {
		var it marketPlatformRow
		var updatedAt time.Time
		if err := rows.Scan(&it.PlatformID, &it.PlatformName, &it.Region,
			&it.NativeCurrency, &it.IsOurs, &it.Enabled, &it.Notes, &updatedAt); err != nil {
			writeInternalErr(w, "market platforms scan", err)
			return
		}
		it.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, "market platforms rows", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// marketPlatformUpdate 是 PUT body：指针 = 「提交了才改」的部分更新。
type marketPlatformUpdate struct {
	PlatformName   *string `json:"platform_name"`
	Region         *string `json:"region"`
	NativeCurrency *string `json:"native_currency"`
	IsOurs         *bool   `json:"is_ours"`
	Enabled        *bool   `json:"enabled"`
	Notes          *string `json:"notes"`
}

// marketRegistryValidatePair 与 958 的 _pair_chk CHECK 同形
// （overseas↔USD / domestic↔CNY）。region/currency 各自的取值域也在
// _region_chk/_currency_chk 里，这里一并前置校验（fail-closed，不依赖
// DB 报错回 400 的兜底路径）。
func marketRegistryValidatePair(region, currency string) error {
	switch region {
	case "overseas", "domestic":
	default:
		return fmt.Errorf("region 必须是 overseas|domestic（得到 %q）", region)
	}
	switch currency {
	case "USD", "CNY":
	default:
		return fmt.Errorf("native_currency 必须是 USD|CNY（得到 %q）", currency)
	}
	if (region == "overseas") != (currency == "USD") {
		return fmt.Errorf("region 与 native_currency 必须配对：overseas↔USD、domestic↔CNY（得到 %s/%s）", region, currency)
	}
	return nil
}

// handleMarketPlatformUpdate PUT /api/admin/market/platforms/{platformId}
func (h *Handler) handleMarketPlatformUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := h.marketPlatformsQuerier()
	if q == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	platformID := r.PathValue("platformId")
	if platformID == "" {
		writeError(w, http.StatusBadRequest, "missing platform id")
		return
	}
	var body marketPlatformUpdate
	if ok, _ := jsonbody.ReadRequired(w, r, &body); !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// ① 现值（404 判定 + 半提交时的配对基准 + 审计 before）。
	var before marketPlatformRow
	var beforeAt time.Time
	err := q.QueryRow(ctx, `
SELECT `+marketPlatformCols+`
  FROM public.market_platform_registry
 WHERE platform_id = $1`, platformID).
		Scan(&before.PlatformID, &before.PlatformName, &before.Region,
			&before.NativeCurrency, &before.IsOurs, &before.Enabled, &before.Notes, &beforeAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "platform not found: "+platformID)
		return
	}
	if err != nil {
		writeInternalErr(w, "market platform read", err)
		return
	}
	before.UpdatedAt = beforeAt.UTC().Format(time.RFC3339)

	// ② 应用部分更新（nil = 不改）。
	after := before
	if body.PlatformName != nil {
		after.PlatformName = *body.PlatformName
	}
	if body.Region != nil {
		after.Region = *body.Region
	}
	if body.NativeCurrency != nil {
		after.NativeCurrency = *body.NativeCurrency
	}
	if body.IsOurs != nil {
		after.IsOurs = *body.IsOurs
	}
	if body.Enabled != nil {
		after.Enabled = *body.Enabled
	}
	if body.Notes != nil {
		after.Notes = *body.Notes
	}
	if err := marketRegistryValidatePair(after.Region, after.NativeCurrency); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// ③ 落库（RETURNING updated_at 让响应携带真实落库时刻）。
	var updatedAt time.Time
	err = q.QueryRow(ctx, `
UPDATE public.market_platform_registry
   SET platform_name = $2, region = $3, native_currency = $4,
       is_ours = $5, enabled = $6, notes = $7, updated_at = now()
 WHERE platform_id = $1
 RETURNING updated_at`, platformID,
		after.PlatformName, after.Region, after.NativeCurrency,
		after.IsOurs, after.Enabled, after.Notes).Scan(&updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// SELECT 与 UPDATE 之间被删 ⇒ 958 无 DELETE 面，这属于并发窗口的
		// 兜底；按 404 报，不改静默吞。
		writeError(w, http.StatusNotFound, "platform not found: "+platformID)
		return
	}
	if err != nil {
		// CHECK 约束直穿（理论上前置校验已拦）⇒ 按 400 把 DB 的话带出来。
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	after.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)

	// ④ 审计（best-effort：审计失败不回滚运营操作，但必须留痕）。
	h.auditMarketPlatformUpdate(r, before, after)

	writeJSON(w, http.StatusOK, map[string]any{"item": after})
}

func (h *Handler) auditMarketPlatformUpdate(r *http.Request, before, after marketPlatformRow) {
	q := h.marketPlatformsQuerier()
	if q == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := q.Exec(ctx, `INSERT INTO routing_audit_log
(actor, action, target_type, target_id, after_json)
VALUES ($1, $2, $3, $4, $5::text::jsonb)`,
		requestActor(r), "market_platform_update", "market_platform_registry",
		after.PlatformID, string(sanitizeJSONBPayload(marketPlatformAuditPayload(before, after)))); err != nil {
		slog.Warn("market platform audit insert failed",
			"platform_id", after.PlatformID, "actor", requestActor(r), "error", err)
	}
}

// marketPlatformAuditPayload 审计载荷（before/after 全行）—— 纯函数以便
// 测试直接钉形状（audit 失败 best-effort，不 fail 请求）。
func marketPlatformAuditPayload(before, after marketPlatformRow) []byte {
	details, err := json.Marshal(map[string]any{"before": before, "after": after})
	if err != nil {
		return []byte("null")
	}
	return details
}
