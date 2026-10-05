// Package apihub — PostgreSQL-backed implementation of Store.
//
// This is the production Store. It writes to the `assets` and
// `asset_relationships` tables created by migrations 047 and 048.
// All queries are scoped by tenant via the `app.current_tenant` GUC,
// which is the defense-in-depth companion to the per-query `WHERE
// tenant_id = $1` clause. RLS policies on both tables enforce that
// the GUC matches the row's tenant_id, so a forgotten `WHERE` clause
// still cannot leak cross-tenant data.
//
// Wiring: pass the result of `db.DB.Pool()` into NewPGStore; the
// apihub.Service is then created via `apihub.New(NewPGStore(pool))`.
//
// Multi-tenancy contract (mirrors docs/multi-tenant-standards.md):
//   - Every method MUST issue SET LOCAL app.current_tenant before the
//     actual query, so RLS is in force for the duration of the tx.
//   - The GUC value is always the tenant_id from the Asset (Upsert/Link)
//     or from the explicit argument (Get/List/Neighbors).
//   - Pool connections are reused; SET LOCAL is transaction-scoped so
//     the GUC auto-clears when the tx ends.
package apihub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxQuerier is the minimal subset of pgxpool.Pool used by PGStore.
// Defined as an interface so tests can inject a fake without spinning up
// PostgreSQL.
type pgxQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// pgStore is the production Store. Construct via NewPGStore.
type pgStore struct {
	pool *pgxpool.Pool
	q    pgxQuerier // optional injection seam for tests
}

// NewPGStore returns a Store backed by the given pgxpool.Pool. Pass the
// result of `db.DB.Pool()` from this service's db package.
//
// Returns a non-nil Store even when pool is nil; methods will fail with
// ErrNoDB. This lets the caller wire apihub.Service unconditionally and
// only error at first query time.
func NewPGStore(pool *pgxpool.Pool) Store {
	return &pgStore{pool: pool}
}

// newPGStoreWithQuerier is the test seam. Production code MUST use
// NewPGStore; tests inject a fakeQuerier via this constructor.
func newPGStoreWithQuerier(q pgxQuerier) *pgStore { //nolint:unused
	return &pgStore{q: q}
}

// --- Upsert ---

// assetHeartbeatRefreshInterval 是 last_seen_at 的最小刷新间隔。
//
// 为什么需要它（2026-10-04 实测，runbook §10.2）：
//
//	upsertAssetSQL 的 ON CONFLICT 分支带 `last_seen_at = now()`，而 PostgreSQL
//	的 DO UPDATE **不比较新旧值**，无条件写新行版本。实测生产上 3 个实例、
//	每 60 秒把全部 2141 个资产全量同步一遍 ⇒ 每天 924.9 万次行重写 × 187 B
//	≈ 1.73 GB/天，而这张表总共只有 2141 行、66 MB（其中 98.39% 已是空洞）。
//
//	而 last_seen_at 的唯一用途是 listStaleSQL 的「超过 6 小时未见」
//	（staleThreshold 默认 6h）⇒ **60 秒的心跳精度对 6 小时的判据过剩 360 倍**。
//
//	5 分钟的刷新窗口把这 360 倍压到 72 倍，stale 判定的最大误差 5 分钟
//	（相对 6 小时阈值 1.4%），写量降 12 倍。
//	★ 选 5 分钟而不是更长的理由：这是个**判定精度**而不是纯性能旋钮，
//	  在没有实测「stale 误判的代价」之前，不该把窗口开得比必要的大。
const assetHeartbeatRefreshInterval = 5 * time.Minute

const upsertAssetSQL = `
INSERT INTO public.assets (
    kind, ref_id, tenant_id, name, owner, team, cost_center,
    tags, health_state, version, registered_at, last_seen_at, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    COALESCE($8::text::jsonb, '{}'::jsonb), $9, $10, now(), now(), COALESCE($11::text::jsonb, '{}'::jsonb)
)
ON CONFLICT (kind, ref_id) DO UPDATE SET
    tenant_id    = EXCLUDED.tenant_id,
    name         = EXCLUDED.name,
    owner        = EXCLUDED.owner,
    team         = EXCLUDED.team,
    cost_center  = EXCLUDED.cost_center,
    tags         = EXCLUDED.tags,
    health_state = EXCLUDED.health_state,
    version      = EXCLUDED.version,
    last_seen_at = now(),
    metadata     = EXCLUDED.metadata
WHERE COALESCE(public.assets.last_seen_at, '-infinity'::timestamptz)
         < now() - ($12 * interval '1 second')
   OR (public.assets.tenant_id,    public.assets.name,        public.assets.owner,
       public.assets.team,         public.assets.cost_center, public.assets.tags,
       public.assets.health_state, public.assets.version,     public.assets.metadata)
      IS DISTINCT FROM
       (EXCLUDED.tenant_id,       EXCLUDED.name,        EXCLUDED.owner,
        EXCLUDED.team,            EXCLUDED.cost_center, EXCLUDED.tags,
        EXCLUDED.health_state,    EXCLUDED.version,     EXCLUDED.metadata)
`

// ★ 这个 WHERE 条件有两半，缺一不可，且方向相反：
//
//	① 业务字段真的变了 ⇒ 必须立刻落库（否则改动要等到心跳窗口才可见）
//	② 心跳已超过刷新窗口 ⇒ 需要刷新（否则 last_seen_at 永远停在旧值，
//	   stale 判定会在 6h + 5min 处误判为「消失」）
//
//	两半都假时**不重写**——这正是省掉那 924.9 万次/天中绝大部分的关键。
//
//	用 IS DISTINCT FROM 而不是 `<>`：owner/team/cost_center 都可能为 NULL，
//	而 `NULL <> 'x'` 求值为 NULL（不是 true）⇒ 会让本该落库的变更被吞掉。
//
//	不命中时 PostgreSQL **不报错**，只是不更新；Upsert 用的是 tx.Exec
//	（不看 RETURNING），所以 Go 侧不需要任何改动。
//
// ★ R44 修正：② 这一半原先写作 `last_seen_at < now() - …`，而该列在
//
//	deploy/sql/schemas/baseline/01-schema.sql:5673 是**可空、无 DEFAULT、
//	无 NOT NULL**（`timestamp with time zone`，裸声明）。于是对任何
//	last_seen_at IS NULL 的行：
//	   · 半② `NULL < x` 求值为 NULL（不是 true）⇒ 不成立；
//	   · 半① 在业务字段未变时为 false ⇒ 不成立；
//	两半都不成立 ⇒ **这行永远不会被更新**，last_seen_at 永远停在 NULL。
//	而读方（:746）用 `COALESCE(last_seen_at, registered_at)` 判 stale
//	⇒ 一个活着的资产会因 registered_at 已超 6h 被 bg/asset_health_probe
//	判成 degraded。
//	这就是「省掉无效重写」被省过头的样子：NULL 行不是「无需重写」，
//	而是「永远写不进去」。COALESCE 到 '-infinity' 让半②对 NULL 恒真，
//	第一跳就把它从 NULL 救出来；此后走正常的 5 分钟窗口。
//	（生产是否真有 NULL 行本轮未能核验——无库访问；本行是防御性收口，
//	  代价为每行一次 COALESCE，恒定开销。）

// Upsert writes an asset row, replacing any existing row with the same
// (kind, ref_id) composite key. RLS is enforced via SET LOCAL.
func (s *pgStore) Upsert(ctx context.Context, a Asset) error {
	if !a.Kind.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, a.Kind)
	}
	if a.TenantID == "" {
		return errors.New("apihub: tenant_id is required")
	}

	tagsJSON, err := marshalStringMap(a.Tags)
	if err != nil {
		return fmt.Errorf("apihub: marshal tags: %w", err)
	}
	metadataJSON, err := marshalAny(a.Metadata)
	if err != nil {
		return fmt.Errorf("apihub: marshal metadata: %w", err)
	}
	health := a.HealthState
	if health == "" {
		health = HealthUnknown
	}
	version := a.Version
	if version == "" {
		version = "0.0.0"
	}

	return s.withTenantTx(ctx, a.TenantID, func(tx pgx.Tx) error {
		// 2026-07-16: Force text protocol for metadata JSONB to avoid
		// pgx binary encoding issues. Cast string → text → jsonb in SQL.
		_, err := tx.Exec(ctx, upsertAssetSQL,
			string(a.Kind),                          // 1
			a.RefID,                                 // 2
			a.TenantID,                              // 3
			a.Name,                                  // 4
			nullable(a.Owner),                       // 5
			nullable(a.Team),                        // 6
			nullable(a.CostCenter),                  // 7
			string(tagsJSON),                        // 8 — text protocol
			string(health),                          // 9
			version,                                 // 10
			string(metadataJSON),                    // 11 — text protocol
			assetHeartbeatRefreshInterval.Seconds(), // 12
		)
		return err
	})
}

// ── 批量 upsert（runbook §10.30）────────────────────────────────────────
//
// 为什么需要它：AssetWatcher 每 60 秒把源表**全量**灌进资产中心，对每个
// 资产各发一次 Upsert。门控（上面那个 WHERE）只让其中少数真正改行，
// **语句数却一分没少** —— 实测 5,562,432 次/天里 93.7% 什么都不做。
//
// 三个设计约束，都是从现有代码读出来的而不是猜的：
//
//  ① **必须按租户分组**。单行 Upsert 走 withTenantTx(a.TenantID) 设 RLS，
//     是**每行一个事务**；而 watcher 同步的资产横跨 4 个租户
//     （default/acme/hansi/kevin）。跨租户塞进一个事务会被 RLS 挡掉。
//     ⇒ 分组 → 每 (租户, 分片) 一个事务。
//
//  ② **参数上限**。VALUES 里每行 11 个占位符（$11 之后是 metadata，
//     registered_at/last_seen_at 是字面 now()）。500 行 = 5,500 个参数，
//     远低于 PostgreSQL 的 65,535 上限。窗口参数**全批共用一个**
//     （Go 侧常量，所有行同值），所以不是每行一个。
//
//  ③ **单行坏数据的韧性不能丢**。现在 watcher 是逐行 log+continue，
//     一个坏资产不会中断整轮同步。批量后一行坏会炸掉整个分片，
//     ⇒ 分片执行失败时**退回逐行执行**隔离坏行，行为与现在一致。

const (
	// upsertAssetsBatchRowParams 是 VALUES 里每行的占位符个数。
	upsertAssetsBatchRowParams = 11
	// maxUpsertBatchRows 是单条语句的行数上限。
	// 11 × 500 = 5,500 参数，离 65,535 上限有 12 倍余量。
	// 再往上调收益递减（语句数已从 1306 降到 3），而单条语句变长会
	// 拉长事务持有时间 —— 那是拿写放大换锁持有，不划算。
	maxUpsertBatchRows = 500
)

// buildUpsertAssetsBatchSQL 生成 rows 行的多值 INSERT。
// 窗口参数放在**所有行之后**（索引 rows*11+1），因为它对整批同值。
func buildUpsertAssetsBatchSQL(rows int) string {
	if rows <= 0 {
		panic("apihub: buildUpsertAssetsBatchSQL called with rows=" + strconv.Itoa(rows))
	}
	windowIdx := rows*upsertAssetsBatchRowParams + 1

	var sb strings.Builder
	sb.Grow(256 + rows*160)
	sb.WriteString(`INSERT INTO public.assets (
    kind, ref_id, tenant_id, name, owner, team, cost_center,
    tags, health_state, version, registered_at, last_seen_at, metadata
) VALUES `)
	for i := 0; i < rows; i++ {
		b := i * upsertAssetsBatchRowParams
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `
    ($%d, $%d, $%d, $%d, $%d, $%d, $%d,
     COALESCE($%d::text::jsonb, '{}'::jsonb), $%d, $%d, now(), now(),
     COALESCE($%d::text::jsonb, '{}'::jsonb))`,
			b+1, b+2, b+3, b+4, b+5, b+6, b+7, b+8, b+9, b+10, b+11)
	}
	// ★ DO UPDATE / WHERE 两段必须与 upsertAssetSQL **逐字一致**。
	//   这是同一份契约的两个实现；只改一处就会让单行与批量路径
	//   对「什么时候该写」产生分歧（而那种分歧在数据上不可见 ——
	//   一边多写一边少写，n_tup_upd 只会显示一个「差不多」的数）。
	fmt.Fprintf(&sb, `
ON CONFLICT (kind, ref_id) DO UPDATE SET
    tenant_id    = EXCLUDED.tenant_id,
    name         = EXCLUDED.name,
    owner        = EXCLUDED.owner,
    team         = EXCLUDED.team,
    cost_center  = EXCLUDED.cost_center,
    tags         = EXCLUDED.tags,
    health_state = EXCLUDED.health_state,
    version      = EXCLUDED.version,
    last_seen_at = now(),
    metadata     = EXCLUDED.metadata
WHERE COALESCE(public.assets.last_seen_at, '-infinity'::timestamptz)
         < now() - ($%d * interval '1 second')
   OR (public.assets.tenant_id,    public.assets.name,        public.assets.owner,
       public.assets.team,         public.assets.cost_center, public.assets.tags,
       public.assets.health_state, public.assets.version,     public.assets.metadata)
      IS DISTINCT FROM
       (EXCLUDED.tenant_id,       EXCLUDED.name,        EXCLUDED.owner,
        EXCLUDED.team,            EXCLUDED.cost_center, EXCLUDED.tags,
        EXCLUDED.health_state,    EXCLUDED.version,     EXCLUDED.metadata)`, windowIdx)
	return sb.String()
}

// upsertRow 是把 Asset 拍平成绑定参数后的中间形态。
type upsertRow struct {
	asset    Asset
	tagsJSON string
	metaJSON string
	health   HealthState
	version  string
}

// UpsertBatch writes many assets in as few statements as possible.
//
// 分组规则：先按 tenant_id 分组（RLS 要求），组内按 maxUpsertBatchRows 切片。
// 返回的 error 只在**整批都失败**时非 nil；单行坏数据会被隔离并记日志，
// 与逐行 Upsert 时代的「log+continue」语义一致（见本节约束③）。
func (s *pgStore) UpsertBatch(ctx context.Context, assets []Asset) error {
	if len(assets) == 0 {
		return nil
	}

	// ── 1. 先全部拍平 + 校验。序列化失败在这里就被拦下，
	//       不会带着半好的行去开事务。
	byTenant := make(map[string][]upsertRow, 4)
	for _, a := range assets {
		if !a.Kind.IsValid() {
			slog.Warn("apihub: batch upsert 跳过非法 kind", "kind", string(a.Kind), "ref_id", a.RefID)
			continue
		}
		if a.TenantID == "" {
			slog.Warn("apihub: batch upsert 跳过空 tenant", "kind", string(a.Kind), "ref_id", a.RefID)
			continue
		}
		tagsJSON, err := marshalStringMap(a.Tags)
		if err != nil {
			slog.Warn("apihub: batch upsert 跳过 tags 序列化失败", "ref_id", a.RefID, "error", err)
			continue
		}
		metaJSON, err := marshalAny(a.Metadata)
		if err != nil {
			slog.Warn("apihub: batch upsert 跳过 metadata 序列化失败", "ref_id", a.RefID, "error", err)
			continue
		}
		health := a.HealthState
		if health == "" {
			health = HealthUnknown
		}
		version := a.Version
		if version == "" {
			version = "0.0.0"
		}
		byTenant[a.TenantID] = append(byTenant[a.TenantID], upsertRow{
			asset: a, tagsJSON: string(tagsJSON), metaJSON: string(metaJSON),
			health: health, version: version,
		})
	}

	var firstErr error
	for tenant, rows := range byTenant {
		for start := 0; start < len(rows); start += maxUpsertBatchRows {
			end := start + maxUpsertBatchRows
			if end > len(rows) {
				end = len(rows)
			}
			chunk := rows[start:end]
			if err := s.execUpsertChunk(ctx, tenant, chunk); err != nil {
				// 整片失败 ⇒ 退回逐行，隔离坏行，行为退回改动前。
				if firstErr == nil {
					firstErr = err
				}
				s.upsertRowsIndividually(ctx, tenant, chunk, err)
			}
		}
	}
	return firstErr
}

// execUpsertChunk 用一条多值 INSERT 写入整个分片。
func (s *pgStore) execUpsertChunk(ctx context.Context, tenant string, rows []upsertRow) error {
	sqlText := buildUpsertAssetsBatchSQL(len(rows))
	args := make([]any, 0, len(rows)*upsertAssetsBatchRowParams+1)
	for _, r := range rows {
		args = append(args,
			string(r.asset.Kind), r.asset.RefID, r.asset.TenantID, r.asset.Name,
			nullable(r.asset.Owner), nullable(r.asset.Team), nullable(r.asset.CostCenter),
			r.tagsJSON, string(r.health), r.version, r.metaJSON,
		)
	}
	args = append(args, assetHeartbeatRefreshInterval.Seconds())

	return s.withTenantTx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sqlText, args...)
		return err
	})
}

// upsertRowsIndividually 是分片失败后的退路：逐行执行并把失败行记下来。
// ★ 必须真的隔离出坏行 —— 否则一个坏资产会让同租户其余 499 个
//
//	在这一轮里全部同步失败，而这是**静默**的（watcher 只看总成功数）。
func (s *pgStore) upsertRowsIndividually(ctx context.Context, tenant string, rows []upsertRow, chunkErr error) {
	okN := 0
	for _, r := range rows {
		if err := s.Upsert(ctx, r.asset); err != nil {
			slog.Warn("apihub: 批量回退后单行仍失败",
				"tenant", tenant, "kind", string(r.asset.Kind), "ref_id", r.asset.RefID, "error", err)
			continue
		}
		okN++
	}
	slog.Warn("apihub: 批量分片失败，已退回逐行",
		"tenant", tenant, "rows", len(rows), "ok", okN, "chunk_error", chunkErr)
}

// --- Get ---

const getAssetSQL = `
SELECT kind, ref_id, tenant_id, name,
       COALESCE(owner, ''), COALESCE(team, ''), COALESCE(cost_center, ''),
       tags, health_state, COALESCE(version, ''),
       registered_at, last_seen_at, metadata
FROM public.assets
WHERE tenant_id = $1 AND kind = $2 AND ref_id = $3
LIMIT 1
`

// Get fetches a single asset by composite key. Returns ErrNotFound if
// the row does not exist OR belongs to another tenant (RLS hides it).
func (s *pgStore) Get(ctx context.Context, tenantID string, k Kind, refID int64) (Asset, error) {
	if tenantID == "" {
		return Asset{}, errors.New("apihub: tenant_id is required")
	}

	var a Asset
	var tagsRaw, metadataRaw []byte
	var lastSeen *interface{}

	err := s.withTenantReadOnlyTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, getAssetSQL, tenantID, string(k), refID)
		return row.Scan(
			&a.Kind, &a.RefID, &a.TenantID, &a.Name,
			&a.Owner, &a.Team, &a.CostCenter,
			&tagsRaw, &a.HealthState, &a.Version,
			&a.RegisteredAt, &lastSeen, &metadataRaw,
		)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}

	if err := unmarshalStringMap(tagsRaw, &a.Tags); err != nil {
		return Asset{}, fmt.Errorf("apihub: unmarshal tags: %w", err)
	}
	if err := unmarshalAny(metadataRaw, &a.Metadata); err != nil {
		return Asset{}, fmt.Errorf("apihub: unmarshal metadata: %w", err)
	}
	return a, nil
}

// --- List ---

const listAssetsSQL = `
SELECT kind, ref_id, tenant_id, name,
       COALESCE(owner, ''), COALESCE(team, ''), COALESCE(cost_center, ''),
       tags, health_state, COALESCE(version, ''),
       registered_at, last_seen_at, metadata
FROM public.assets
WHERE tenant_id = $1
  AND ($2 = '' OR kind = $2)
  AND ($3 = '' OR health_state = $3)
ORDER BY kind, ref_id
LIMIT $4
OFFSET $5
`

// List returns assets matching the filter, always scoped by tenantID.
// The caller (Service.List) already overrides Filter.TenantID with the
// context tenant; we still re-assert in the SQL for defense-in-depth.
func (s *pgStore) List(ctx context.Context, f Filter) ([]Asset, error) {
	if f.TenantID == "" {
		return nil, errors.New("apihub: tenant_id is required")
	}
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	// 2026-10-05（runbook §10.27）：这个 500 上限**保留**。它是
	// apihub.Filter.Limit 的文档化契约（types.go「default 100, max 500」），
	// 作用是**页大小**；过去出错是因为没有 OFFSET，使它同时成了**总量上限**。
	// 现在两件事分开了：要取全量就翻页，而不是把页放大。
	// ★ 静默截断在这里本身就是 bug 源：AssetHealthProbe 要 1000 行、拿到 500，
	//   既不报错也不留痕（admin/agents.go 要 1000 也一样）。
	if limit > 500 {
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	kindFilter := string(f.Kind)
	healthFilter := string(f.Health)

	var assets []Asset
	err := s.withTenantReadOnlyTx(ctx, f.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listAssetsSQL,
			f.TenantID, kindFilter, healthFilter, limit, offset,
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var a Asset
			var tagsRaw, metadataRaw []byte
			var lastSeen *interface{}
			if err := rows.Scan(
				&a.Kind, &a.RefID, &a.TenantID, &a.Name,
				&a.Owner, &a.Team, &a.CostCenter,
				&tagsRaw, &a.HealthState, &a.Version,
				&a.RegisteredAt, &lastSeen, &metadataRaw,
			); err != nil {
				return err
			}
			if err := unmarshalStringMap(tagsRaw, &a.Tags); err != nil {
				return err
			}
			if err := unmarshalAny(metadataRaw, &a.Metadata); err != nil {
				return err
			}
			assets = append(assets, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

// --- Link ---

const linkRelationshipSQL = `
INSERT INTO public.asset_relationships (
    src_kind, src_ref_id, dst_kind, dst_ref_id, rel, weight
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (src_kind, src_ref_id, dst_kind, dst_ref_id, rel) DO NOTHING
`

// Link inserts a directed edge. Both endpoints must already exist as
// assets under tenantID — the FK constraint on asset_relationships
// enforces this at the DB layer (ON DELETE CASCADE also removes edges
// when an asset disappears).
func (s *pgStore) Link(ctx context.Context, tenantID string, rel Relationship) error {
	if tenantID == "" {
		return errors.New("apihub: tenant_id is required")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, linkRelationshipSQL,
			string(rel.SrcKind.Kind), rel.SrcKind.RefID,
			string(rel.DstKind.Kind), rel.DstKind.RefID,
			string(rel.Type), rel.Weight,
		)
		return err
	})
}

// --- Neighbors ---

const neighborsRecursiveSQL = `
WITH RECURSIVE walk AS (
    SELECT src_kind, src_ref_id, dst_kind, dst_ref_id, rel, weight, 1 AS depth
    FROM public.asset_relationships
    WHERE src_kind = $1 AND src_ref_id = $2
    UNION ALL
    SELECT e.src_kind, e.src_ref_id, e.dst_kind, e.dst_ref_id, e.rel, e.weight, w.depth + 1
    FROM public.asset_relationships e
    JOIN walk w ON e.src_kind = w.dst_kind AND e.src_ref_id = w.dst_ref_id
    WHERE w.depth < $3
)
SELECT a.kind, a.ref_id, a.tenant_id, a.name,
       COALESCE(a.owner, ''), COALESCE(a.team, ''), COALESCE(a.cost_center, ''),
       a.tags, a.health_state, COALESCE(a.version, ''),
       a.registered_at, a.last_seen_at, a.metadata,
       w.dst_kind, w.dst_ref_id, w.rel, w.weight
FROM walk w
JOIN public.assets a
  ON a.kind = w.dst_kind AND a.ref_id = w.dst_ref_id
WHERE a.tenant_id = $4
ORDER BY w.depth
`

// Neighbors performs a BFS traversal of the topology graph up to the
// given depth (1 = direct neighbors). RLS scoping applies to the asset
// rows joined in at the end.
func (s *pgStore) Neighbors(ctx context.Context, tenantID string, k Kind, refID int64, depth int) ([]Asset, []Relationship, error) {
	if tenantID == "" {
		return nil, nil, errors.New("apihub: tenant_id is required")
	}
	if depth < 1 {
		depth = 1
	}

	var assets []Asset
	var rels []Relationship
	err := s.withTenantReadOnlyTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, neighborsRecursiveSQL,
			string(k), refID, depth, tenantID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		seen := make(map[string]bool)
		for rows.Next() {
			var a Asset
			var tagsRaw, metadataRaw []byte
			var lastSeen *interface{}
			var dstKind, relType string
			var dstRefID int64
			var weight float64

			if err := rows.Scan(
				&a.Kind, &a.RefID, &a.TenantID, &a.Name,
				&a.Owner, &a.Team, &a.CostCenter,
				&tagsRaw, &a.HealthState, &a.Version,
				&a.RegisteredAt, &lastSeen, &metadataRaw,
				&dstKind, &dstRefID, &relType, &weight,
			); err != nil {
				return err
			}

			ak := string(a.Kind) + "|" + itoa64(a.RefID)
			if !seen[ak] {
				seen[ak] = true
				if err := unmarshalStringMap(tagsRaw, &a.Tags); err != nil {
					return err
				}
				if err := unmarshalAny(metadataRaw, &a.Metadata); err != nil {
					return err
				}
				assets = append(assets, a)
			}

			rels = append(rels, Relationship{
				SrcKind: RelationEndpoint{Kind: k, RefID: refID},
				DstKind: RelationEndpoint{Kind: Kind(dstKind), RefID: dstRefID},
				Type:    RelationType(relType),
				Weight:  weight,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	return assets, rels, nil
}

// --- tenant-scoped transaction helpers ---

// withTenantTx opens a write transaction, sets the app.current_tenant
// GUC so RLS is in force, and invokes fn. Commits on nil error.
//
// Routes through s.q when present (test seam); otherwise uses s.pool.
// In production NewPGStore sets only pool, so s.q is always nil.
//
// 2026-07-16 曾在此处加 `RESET app.current_tenant`，理由是「防止 GUC 跨事务
// 污染连接池」。2026-10-03 移除：该理由基于一个**不成立的前提** —— 它假定
// 写入用的是 `SET LOCAL`（会话级残留）。但 setTenantGUC 用的是
// `set_config('app.current_tenant', $1, true)`，第三个参数 true 即事务级作用域，
// PostgreSQL 在提交/回滚时自动撤销，**根本不会残留在连接上**。
//
// 保留它反而有害：defer 在 `return tx.Commit(ctx)` 之后执行，事务已关闭，
// `tx.Exec` 必返 ErrTxClosed，于是每次成功调用都打一条 WARN。
// 252 实测 3 小时 200,897 条（≈18.5 行/秒），/var/log/messages 1.2G、约 6 MB/h。
func (s *pgStore) withTenantTx(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if s.pool == nil && s.q == nil {
		return ErrNoDB
	}
	q := s.q
	if q == nil {
		q = s.pool
	}
	tx, err := q.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("apihub: begin tx: %w", err)
	}
	defer func() {
		// 2026-10-03：删掉这里的 `tx.Exec(ctx, "RESET app.current_tenant")`。
		//
		// 它有两个问题，第二个更严重：
		//  1) 本来就多余 —— setTenantGUC 用的是
		//     `set_config('app.current_tenant', $1, true)`，第三个参数 true 即
		//     **事务级作用域**，提交/回滚时该设置自动消失，不可能泄漏到连接池。
		//     下方 rollback 本就足以覆盖全部路径。
		//  2) 成功路径上必然报错 —— defer 在 `return tx.Commit(ctx)` 之后执行，
		//     此时事务已关闭，`tx.Exec` 返回 pgx.ErrTxClosed（"tx is closed"）。
		//     于是**每次成功调用都打一条 WARN**：252 实测 3 小时 200,897 条
		//     （≈18.5 行/秒），/var/log/messages 涨到 1.2G、约 6 MB/h。
		//
		// 这不是偶发错误而是恒定噪声，却以 WARN 形态出现，看起来像持续故障。
		// 原注释「RESET 失败时 rollback 兜底」其实已经承认了 rollback 才是兜底。
		_ = tx.Rollback(ctx) // rollback is idempotent after commit
	}()

	if err := setTenantGUC(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// withTenantReadOnlyTx is the read-only variant. We use READ ONLY for
// planner hints and to make accidental writes fail loudly.
//
// 2026-10-03: 同 withTenantTx —— 事务级 GUC 不残留，无需 RESET（理由见上）。
func (s *pgStore) withTenantReadOnlyTx(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if s.pool == nil && s.q == nil {
		return ErrNoDB
	}
	q := s.q
	if q == nil {
		q = s.pool
	}
	tx, err := q.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("apihub: begin read tx: %w", err)
	}
	defer func() {
		// 2026-10-03：同 withTenantTx —— 删掉事务级 GUC 的冗余 RESET。
		// 理由与实测见上；此处原本是第二份同款拷贝。
		_ = tx.Rollback(ctx)
	}()

	if err := setTenantGUC(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// setTenantGUC sets the app.current_tenant GUC scoped to the current
// transaction. Uses set_config(...) so the tenant id is passed as a
// parameter (defense against any quoting accidents; SET LOCAL would
// require client-side escaping).
func setTenantGUC(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return fmt.Errorf("apihub: set tenant GUC: %w", err)
	}
	return nil
}

// --- marshaling helpers ---

// marshalStringMap JSON-encodes a string→string map for storage in a
// JSONB column. Empty maps serialize to "{}". Values are scrubbed of
// invalid UTF-8 / control bytes so PostgreSQL never rejects the row
// with SQLSTATE 22P02. json.Marshal itself rejects NaN/±Inf, which
// can leak in via config sources, so we strip those keys before
// marshaling.
func marshalStringMap(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	clean := make(map[string]string, len(m))
	for k, v := range m {
		clean[k] = scrubUTF8ForJSONB(v)
	}
	return json.Marshal(clean)
}

// marshalAny JSON-encodes a map[string]any for storage in a JSONB
// column. Empty maps serialize to "{}". Values are scrubbed of:
//
//  1. NaN / +Inf / -Inf floats (json.Marshal errors on these; without
//     pre-cleaning PG gets a Go-side error and the row is dropped).
//  2. Non-finite numerics in nested maps/slices.
//  3. Invalid UTF-8 / control bytes in string leaves.
//
// All scrubbing is best-effort: on any unexpected value type we fall
// back to a stable representation (0 / "" / {}) rather than fail the
// entire upsert. This is what fixed the 2026-07-16 incident where the
// apihub watcher logged 170k+ "invalid input syntax for type json"
// errors for three stuck ref_ids (1139198-200).
func marshalAny(v map[string]any) ([]byte, error) {
	if len(v) == 0 {
		return []byte("{}"), nil
	}
	clean := sanitizeForJSONB(v)
	out, err := json.Marshal(clean)
	if err != nil {
		// Last-resort fallback: emit an empty object so the upsert
		// still succeeds. The caller logs err separately.
		return []byte("{}"), err
	}
	if !json.Valid(out) {
		// Should not happen after sanitizeForJSONB, but defend in
		// depth: never feed PG invalid JSON.
		return []byte("{}"), nil
	}
	return out, nil
}

// sanitizeForJSONB recursively walks v and returns a deep copy where
// every value is JSON-safe: NaN/Inf replaced by 0, invalid UTF-8
// replaced by U+FFFD, control bytes (\x00) stripped.
func sanitizeForJSONB(v map[string]any) map[string]any {
	out := make(map[string]any, len(v))
	for k, val := range v {
		out[k] = sanitizeValueForJSONB(val)
	}
	return out
}

func sanitizeValueForJSONB(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return sanitizeForJSONB(x)
	case []any:
		arr := make([]any, len(x))
		for i, e := range x {
			arr[i] = sanitizeValueForJSONB(e)
		}
		return arr
	case string:
		return scrubUTF8ForJSONB(x)
	case float64:
		// NaN, +Inf, -Inf → 0. finite values pass through unchanged.
		if x != x || x > 1e308 || x < -1e308 {
			return 0.0
		}
		return x
	case float32:
		f := float64(x)
		if f != f || f > 1e308 || f < -1e308 {
			return 0.0
		}
		return f
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return v
	default:
		// json.Number, custom Marshalers, etc. — hand to json.Marshal
		// via fmt to avoid reflection surprises. Use a string form.
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		if !json.Valid(b) {
			return nil
		}
		return json.RawMessage(b)
	}
}

// scrubUTF8ForJSONB replaces invalid UTF-8 byte sequences with U+FFFD
// and strips C0 control bytes except \\t \\n \\r (which PG JSONB
// accepts). Null bytes (\x00) are stripped because PostgreSQL TEXT /
// JSONB columns reject them with SQLSTATE 22021.
func scrubUTF8ForJSONB(s string) string {
	if utf8.ValidString(s) && !strings.ContainsAny(s, "\x00") && !hasC0ControlExceptWS(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/10)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString("\uFFFD")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			// strip control bytes other than tab/lf/cr
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// hasC0ControlExceptWS returns true if s contains any C0 control byte
// other than \t \n \r. Used as a fast-path check so common cases
// (already-clean UTF-8) skip the per-byte loop.
func hasC0ControlExceptWS(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}

func unmarshalStringMap(raw []byte, dst *map[string]string) error {
	if len(raw) == 0 {
		*dst = nil
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func unmarshalAny(raw []byte, dst *map[string]any) error {
	if len(raw) == 0 {
		*dst = nil
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func itoa64(i int64) string {
	// Avoid pulling strconv just for this — simple path is fine here.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ErrNoDB is returned by PGStore methods when no connection pool is
// available. Wiring code should check this and disable the hub rather
// than crashing the gateway.
var ErrNoDB = errors.New("apihub: no database connection")

// ── MarkHealth + ListStale (Phase 7) ─────────────────────────────────────

const markHealthSQL = `
UPDATE public.assets
   SET health_state = $4
 WHERE tenant_id = $1 AND kind = $2 AND ref_id = $3
RETURNING 1
`

func (s *pgStore) MarkHealth(ctx context.Context, tenantID string, k Kind, refID int64, state HealthState) error {
	if s.pool == nil && s.q == nil {
		return ErrNoDB
	}
	if tenantID == "" {
		return errors.New("apihub: tenant_id required")
	}
	stateStr := string(state)
	if stateStr == "" {
		stateStr = string(HealthUnknown)
	}
	var found int
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, markHealthSQL, tenantID, string(k), refID, stateStr).Scan(&found)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("apihub: mark health: %w", err)
	}
	return nil
}

const listStaleSQL = `
SELECT kind, ref_id, tenant_id, name,
       COALESCE(owner, ''), COALESCE(team, ''), COALESCE(cost_center, ''),
       tags, health_state, COALESCE(version, ''),
       registered_at, last_seen_at, metadata
FROM public.assets
WHERE tenant_id = $1
  AND COALESCE(last_seen_at, registered_at) < now() - make_interval(secs => $2)
ORDER BY COALESCE(last_seen_at, registered_at) ASC
LIMIT 1000
`

func (s *pgStore) ListStale(ctx context.Context, tenantID string, threshold time.Duration) ([]Asset, error) {
	if s.pool == nil && s.q == nil {
		return []Asset{}, nil
	}
	if tenantID == "" {
		return nil, errors.New("apihub: tenant_id required")
	}
	if threshold < 0 {
		threshold = 0
	}
	seconds := int64(threshold.Seconds())
	if seconds < 1 {
		seconds = 1
	}

	var stale []Asset
	err := s.withTenantReadOnlyTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listStaleSQL, tenantID, seconds)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Asset
			var tagsRaw, metadataRaw []byte
			if err := rows.Scan(
				&a.Kind, &a.RefID, &a.TenantID, &a.Name,
				&a.Owner, &a.Team, &a.CostCenter,
				&tagsRaw, &a.HealthState, &a.Version,
				&a.RegisteredAt, &a.LastSeenAt, &metadataRaw,
			); err != nil {
				return err
			}
			if err := unmarshalStringMap(tagsRaw, &a.Tags); err != nil {
				return err
			}
			if err := unmarshalAny(metadataRaw, &a.Metadata); err != nil {
				return err
			}
			stale = append(stale, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return stale, nil
}

// ListTenants returns all distinct tenant_id values in the assets table.
// Used by AssetHealthProbe to iterate over all tenants.
func (s *pgStore) ListTenants(ctx context.Context) ([]string, error) {
	if s.pool == nil && s.q == nil {
		return nil, ErrNoDB
	}
	q := s.q
	if q == nil {
		q = s.pool
	}
	rows, err := q.Query(ctx, "SELECT DISTINCT tenant_id FROM public.assets ORDER BY tenant_id")
	if err != nil {
		return nil, fmt.Errorf("apihub: list tenants: %w", err)
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tenants, nil
}
