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
func newPGStoreWithQuerier(q pgxQuerier) *pgStore {
	return &pgStore{q: q}
}

// --- Upsert ---

const upsertAssetSQL = `
INSERT INTO public.assets (
    kind, ref_id, tenant_id, name, owner, team, cost_center,
    tags, health_state, version, registered_at, last_seen_at, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    COALESCE($8, '{}'::jsonb), $9, $10, now(), now(), COALESCE($11, '{}'::jsonb)
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
`

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

	return s.withTenantTx(ctx, a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, upsertAssetSQL,
			string(a.Kind),         // 1
			a.RefID,                // 2
			a.TenantID,             // 3
			a.Name,                 // 4
			nullable(a.Owner),      // 5
			nullable(a.Team),       // 6
			nullable(a.CostCenter), // 7
			tagsJSON,               // 8
			string(health),         // 9
			nullable(a.Version),    // 10
			metadataJSON,           // 11
		)
		return err
	})
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
	if limit > 500 {
		limit = 500
	}
	kindFilter := string(f.Kind)
	healthFilter := string(f.Health)

	var assets []Asset
	err := s.withTenantReadOnlyTx(ctx, f.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listAssetsSQL,
			f.TenantID, kindFilter, healthFilter, limit,
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
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

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
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)

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

func marshalStringMap(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func marshalAny(v map[string]any) ([]byte, error) {
	if len(v) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
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
