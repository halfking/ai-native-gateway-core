// Package bootstrap initializes the URSM v2 Redis state from legacy probe
// state before an authoritative gateway starts serving traffic.
package bootstrap

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	"github.com/redis/go-redis/v9"
)

const failStreakLimit = 3

type Options struct {
	Pool           *pgxpool.Pool
	Redis          *redis.Client
	KeyPrefix      string
	TenantID       string
	CoolSeconds    int
	ForceOverwrite bool
	Now            func() time.Time
	// SchemaMode selects the bootstrap write target (doc 14 §5): legacy
	// writes the frozen legacy bytes, dual writes both grammars, canonical
	// writes canonical only. Zero value legacy keeps historical behavior.
	SchemaMode store.KeySchemaMode
}

type Result struct {
	Total   int
	Written int
	Skipped int
}

type probeRow struct {
	TenantID            string
	CredentialID        int64
	RawModel            string
	ConsecutiveFailures int
	LastAttemptAt       pgtype.Timestamptz
	Paused              bool
	LastDirectOK        pgtype.Bool
	LastErrCode         pgtype.Text
}

type mappedNode struct {
	Key    string
	Fields map[string]string
}

// Apply reads every tenant-aware legacy probe state row, safely initializes
// missing Redis nodes, and publishes the complete coverage manifest. Existing
// admin overrides and live v2 records are never overwritten unless explicitly
// requested by the standalone migration command.
func Apply(ctx context.Context, opts Options) (Result, error) {
	if opts.Pool == nil {
		return Result{}, fmt.Errorf("legacy probe database is unavailable")
	}
	if opts.Redis == nil {
		return Result{}, fmt.Errorf("redis is unavailable")
	}
	if opts.KeyPrefix == "" {
		return Result{}, fmt.Errorf("URSM v2 key prefix is empty")
	}
	if opts.CoolSeconds <= 0 {
		return Result{}, fmt.Errorf("URSM v2 cool seconds must be positive")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	rows, err := readProbeRows(ctx, opts.Pool, opts.TenantID)
	if err != nil {
		return Result{}, err
	}
	if len(rows) == 0 {
		return Result{}, fmt.Errorf("legacy node_probe_state has no rows")
	}

	nodes := make([]mappedNode, 0, len(rows))
	coverage := make([]string, 0, len(rows))
	for _, row := range rows {
		mapped, err := mapRowNodes(row, opts.KeyPrefix, opts.CoolSeconds, opts.Now(), opts.SchemaMode)
		if err != nil {
			return Result{}, err
		}
		nodes = append(nodes, mapped...)
		// The coverage manifest carries the authoritative grammar's key:
		// canonical in dual/canonical modes, legacy bytes otherwise
		// (doc 14 §5.3).
		coverageKey := mapped[0].Key
		if opts.SchemaMode != store.KeySchemaModeLegacy {
			if k2, k2err := store.K2NodeKeyForTenant(opts.KeyPrefix, row.TenantID, int(row.CredentialID), row.RawModel); k2err == nil {
				coverageKey = k2
			}
		}
		coverage = append(coverage, coverageKey)
	}

	toWrite, skipped, err := selectWrites(ctx, opts.Redis, nodes, opts.ForceOverwrite)
	if err != nil {
		return Result{}, err
	}
	for start := 0; start < len(toWrite); start += 500 {
		end := start + 500
		if end > len(toWrite) {
			end = len(toWrite)
		}
		pipe := opts.Redis.Pipeline()
		for _, node := range toWrite[start:end] {
			pipe.HSet(ctx, node.Key, node.Fields)
			pipe.Expire(ctx, node.Key, 90*time.Minute)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return Result{}, fmt.Errorf("write URSM v2 nodes: %w", err)
		}
	}

	coverageKey := store.CoverageKey(opts.KeyPrefix)
	if opts.TenantID != "" {
		coverageKey += ":tenant:" + opts.TenantID
	}
	if err := replaceCoverageManifest(ctx, opts.Redis, coverageKey, coverage); err != nil {
		return Result{}, fmt.Errorf("publish URSM v2 coverage manifest: %w", err)
	}
	return Result{Total: len(nodes), Written: len(toWrite), Skipped: skipped}, nil
}

func readProbeRows(ctx context.Context, pool *pgxpool.Pool, tenantID string) ([]probeRow, error) {
	query := `SELECT c.tenant_id, nps.credential_id, nps.raw_model_name,
		nps.consecutive_failures, nps.last_attempt_at, nps.paused,
		nps.last_direct_ok, nps.last_err_code
		FROM public.node_probe_state nps
		JOIN public.credentials c ON c.id = nps.credential_id`
	var args []interface{}
	if tenantID != "" {
		query += " WHERE c.tenant_id = $1"
		args = append(args, tenantID)
	}
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read legacy node_probe_state: %w", err)
	}
	defer rows.Close()

	var out []probeRow
	for rows.Next() {
		var row probeRow
		if err := rows.Scan(&row.TenantID, &row.CredentialID, &row.RawModel,
			&row.ConsecutiveFailures, &row.LastAttemptAt, &row.Paused,
			&row.LastDirectOK, &row.LastErrCode); err != nil {
			return nil, fmt.Errorf("scan legacy node_probe_state: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy node_probe_state: %w", err)
	}
	return out, nil
}

func mapRow(row probeRow, prefix string, coolSeconds int, now time.Time) mappedNode {
	return mappedNode{
		Key:    store.NodeKeyForTenant(prefix, row.TenantID, int(row.CredentialID), row.RawModel),
		Fields: rowFields(row, coolSeconds, now),
	}
}

// mapRowNodes builds the node writes for one probe row under the schema
// mode: legacy emits the frozen legacy key, dual emits the legacy and the
// canonical node, canonical emits the canonical node only and refuses a
// tuple the canonical grammar cannot represent (empty tenant, doc 14 §2).
func mapRowNodes(row probeRow, prefix string, coolSeconds int, now time.Time, mode store.KeySchemaMode) ([]mappedNode, error) {
	fields := rowFields(row, coolSeconds, now)
	legacyKey := store.NodeKeyForTenant(prefix, row.TenantID, int(row.CredentialID), row.RawModel)
	switch mode {
	case store.KeySchemaModeCanonical:
		k2Key, err := store.K2NodeKeyForTenant(prefix, row.TenantID, int(row.CredentialID), row.RawModel)
		if err != nil {
			return nil, fmt.Errorf("canonical bootstrap cannot map credential %d: %w", row.CredentialID, err)
		}
		return []mappedNode{{Key: k2Key, Fields: fields}}, nil
	case store.KeySchemaModeDual:
		if k2Key, err := store.K2NodeKeyForTenant(prefix, row.TenantID, int(row.CredentialID), row.RawModel); err == nil {
			return []mappedNode{{Key: legacyKey, Fields: fields}, {Key: k2Key, Fields: fields}}, nil
		}
		// Empty tenant stays on legacy bytes only.
		return []mappedNode{{Key: legacyKey, Fields: fields}}, nil
	default:
		return []mappedNode{{Key: legacyKey, Fields: fields}}, nil
	}
}

func rowFields(row probeRow, coolSeconds int, now time.Time) map[string]string {
	fields := map[string]string{
		"generation":      "1",
		"source_priority": "10",
		"fail_streak":     strconv.Itoa(row.ConsecutiveFailures),
	}
	if row.LastAttemptAt.Valid {
		fields["last_attempt_ms"] = strconv.FormatInt(row.LastAttemptAt.Time.UnixMilli(), 10)
	}
	if row.LastDirectOK.Valid {
		fields["last_direct_ok"] = boolString(row.LastDirectOK.Bool)
	}
	if row.LastErrCode.Valid && row.LastErrCode.String != "" {
		fields["last_err"] = row.LastErrCode.String
	}
	if row.Paused {
		fields["available"] = "0"
		fields["disabled"] = "1"
		fields["manual_hold"] = "1"
		fields["manual_hold_reason"] = "migrated_from_legacy_paused"
		return fields
	}
	if row.ConsecutiveFailures >= failStreakLimit {
		fields["available"] = "0"
		fields["disabled"] = "1"
		fields["cool_until_ms"] = strconv.FormatInt(now.Add(time.Duration(coolSeconds)*time.Second).UnixMilli(), 10)
		fields["cool_reason"] = "migrated_from_legacy_fail_streak"
		return fields
	}
	fields["available"] = "1"
	fields["disabled"] = "0"
	if row.LastAttemptAt.Valid {
		fields["last_ok_ms"] = strconv.FormatInt(row.LastAttemptAt.Time.UnixMilli(), 10)
	}
	fields["updated_at_ms"] = strconv.FormatInt(now.UnixMilli(), 10)
	return fields
}

func selectWrites(ctx context.Context, rdb *redis.Client, nodes []mappedNode, force bool) ([]mappedNode, int, error) {
	if force {
		return nodes, 0, nil
	}
	pipe := rdb.Pipeline()
	exists := make([]*redis.IntCmd, len(nodes))
	for i, node := range nodes {
		exists[i] = pipe.Exists(ctx, node.Key)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, 0, err
	}
	statePipe := rdb.Pipeline()
	state := make([]*redis.SliceCmd, len(nodes))
	for i, node := range nodes {
		if exists[i].Val() != 0 {
			state[i] = statePipe.HMGet(ctx, node.Key, "manual_hold", "generation")
		}
	}
	if _, err := statePipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, 0, err
	}

	toWrite := make([]mappedNode, 0, len(nodes))
	skipped := 0
	for i, node := range nodes {
		if state[i] == nil {
			toWrite = append(toWrite, node)
			continue
		}
		values, err := state[i].Result()
		if err != nil && err != redis.Nil {
			return nil, 0, err
		}
		manualHold, generation := "", ""
		if len(values) > 0 && values[0] != nil {
			manualHold = fmt.Sprint(values[0])
		}
		if len(values) > 1 && values[1] != nil {
			generation = fmt.Sprint(values[1])
		}
		if manualHold == "1" || (generation != "" && generation != "1") {
			skipped++
			continue
		}
		toWrite = append(toWrite, node)
	}
	return toWrite, skipped, nil
}

func replaceCoverageManifest(ctx context.Context, rdb *redis.Client, key string, members []string) error {
	pendingKey := key + ":pending"
	if err := rdb.Set(ctx, pendingKey, time.Now().UTC().Format(time.RFC3339Nano), 0).Err(); err != nil {
		return err
	}
	defer func() { _ = rdb.Del(context.Background(), pendingKey).Err() }()
	tmp := key + ":staging"
	if err := rdb.Del(ctx, tmp).Err(); err != nil {
		return err
	}
	args := make([]interface{}, 0, len(members))
	for _, member := range members {
		args = append(args, member)
	}
	if len(args) > 0 {
		if err := rdb.SAdd(ctx, tmp, args...).Err(); err != nil {
			return err
		}
	}
	stored, err := rdb.SMembers(ctx, tmp).Result()
	if err != nil {
		return err
	}
	if len(stored) != len(members) {
		return fmt.Errorf("coverage manifest count mismatch: expected=%d got=%d", len(members), len(stored))
	}
	return rdb.Rename(ctx, tmp, key).Err()
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
