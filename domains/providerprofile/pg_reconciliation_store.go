package providerprofile

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGReconciliationStore 是 ReconciliationStore 的 PostgreSQL 实现，
// 读写 provider_cost_reconciliation 表（2026-07-26 供应商画像 migration）。
//
// 表的 id 列在不同部署形态下不一致：migration 形态（BIGSERIAL）有默认值，
// dump-objects 形态没有默认值也没有序列。为保证两种形态都能写入，插入
// 显式分配 id：在事务级咨询锁内取 max(id)+n。该表写入方只有对账定时任务
// 与管理端账单导入，低频且都走本实现，锁内分配足以避免冲突。
type PGReconciliationStore struct {
	db *pgxpool.Pool
}

// NewPGReconciliationStore 创建。
func NewPGReconciliationStore(db *pgxpool.Pool) *PGReconciliationStore {
	return &PGReconciliationStore{db: db}
}

// reconAdvisoryKey 是分配 provider_cost_reconciliation.id 时使用的
// 事务级咨询锁 key（任意固定 int64 常量）。
const reconAdvisoryKey = int64(8262026081501)

// UpsertGatewayUsage 写入网关侧聚合值；只更新 gateway_* 列与 updated_at，
// 不触碰账单列与 diff 列（账单列由 SaveRecord 维护）。
func (s *PGReconciliationStore) UpsertGatewayUsage(ctx context.Context, month time.Time, usage []GatewayMonthlyUsage) error {
	if len(usage) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 事务已提交时 Rollback 是 no-op

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, reconAdvisoryKey); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	// 批量 upsert：id 从 max(id) 递增分配；冲突时仅刷新 gateway_* 列。
	// 月份对整批相同，作为标量参数传入而非进入 unnest。
	_, err = tx.Exec(ctx, `
		INSERT INTO provider_cost_reconciliation (
			id, provider_id, reconciliation_month,
			gateway_total_tokens, gateway_input_tokens, gateway_output_tokens, gateway_total_cost)
		SELECT (SELECT COALESCE(max(id), 0) FROM provider_cost_reconciliation)
		       + row_number() OVER (ORDER BY u.provider_id),
		       u.provider_id, $2,
		       u.total_tokens, u.input_tokens, u.output_tokens, u.total_cost
		FROM unnest($1::bigint[], $3::bigint[], $4::bigint[], $5::bigint[], $6::numeric[]) AS u(provider_id, input_tokens, output_tokens, total_tokens, total_cost)
		ON CONFLICT (provider_id, reconciliation_month) DO UPDATE SET
			gateway_total_tokens   = EXCLUDED.gateway_total_tokens,
			gateway_input_tokens   = EXCLUDED.gateway_input_tokens,
			gateway_output_tokens  = EXCLUDED.gateway_output_tokens,
			gateway_total_cost     = EXCLUDED.gateway_total_cost,
			updated_at             = now()`,
		providerIDs(usage), month, mapFunc(usage, func(u GatewayMonthlyUsage) int64 { return u.InputTokens }),
		mapFunc(usage, func(u GatewayMonthlyUsage) int64 { return u.OutputTokens }),
		mapFunc(usage, func(u GatewayMonthlyUsage) int64 { return u.TotalTokens }),
		mapFunc(usage, func(u GatewayMonthlyUsage) float64 { return u.TotalCost }))
	if err != nil {
		return fmt.Errorf("upsert gateway usage: %w", err)
	}
	return tx.Commit(ctx)
}

// SaveRecord 全量 upsert 一条对账记录（按 provider_id + 月份）。
func (s *PGReconciliationStore) SaveRecord(ctx context.Context, rec *ReconciliationRecord) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, reconAdvisoryKey); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO provider_cost_reconciliation (
			id, provider_id, reconciliation_month,
			gateway_total_tokens, gateway_input_tokens, gateway_output_tokens, gateway_total_cost,
			provider_total_tokens, provider_input_tokens, provider_output_tokens, provider_total_cost,
			token_diff_rate, cost_diff_rate, data_source, notes, updated_at)
		VALUES (
			(SELECT COALESCE(max(id), 0) + 1 FROM provider_cost_reconciliation),
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now())
		ON CONFLICT (provider_id, reconciliation_month) DO UPDATE SET
			gateway_total_tokens   = EXCLUDED.gateway_total_tokens,
			gateway_input_tokens   = EXCLUDED.gateway_input_tokens,
			gateway_output_tokens  = EXCLUDED.gateway_output_tokens,
			gateway_total_cost     = EXCLUDED.gateway_total_cost,
			provider_total_tokens  = EXCLUDED.provider_total_tokens,
			provider_input_tokens  = EXCLUDED.provider_input_tokens,
			provider_output_tokens = EXCLUDED.provider_output_tokens,
			provider_total_cost    = EXCLUDED.provider_total_cost,
			token_diff_rate        = EXCLUDED.token_diff_rate,
			cost_diff_rate         = EXCLUDED.cost_diff_rate,
			data_source            = EXCLUDED.data_source,
			notes                  = EXCLUDED.notes,
			updated_at             = now()`,
		rec.ProviderID, rec.Month,
		rec.GatewayTotalTokens, rec.GatewayInputTokens, rec.GatewayOutputTokens, rec.GatewayTotalCost,
		rec.ProviderTotalTokens, rec.ProviderInputTokens, rec.ProviderOutputTokens, rec.ProviderTotalCost,
		nullDiffRate(rec.TokenDiffRate), nullDiffRate(rec.CostDiffRate),
		nullableText(rec.DataSource), nullableText(rec.Notes))
	if err != nil {
		return fmt.Errorf("save reconciliation record: %w", err)
	}
	return tx.Commit(ctx)
}

// Get 读取指定供应商+月份的记录；不存在返回 (nil, nil)。
func (s *PGReconciliationStore) Get(ctx context.Context, providerID int64, month time.Time) (*ReconciliationRecord, error) {
	rows, err := s.db.Query(ctx, reconSelectSQL+` WHERE provider_id = $1 AND reconciliation_month = $2`, providerID, month)
	if err != nil {
		return nil, fmt.Errorf("query reconciliation record: %w", err)
	}
	defer rows.Close()
	recs, err := scanReconciliationRows(rows)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return &recs[0], nil
}

// ListByMonth 列出某月全部对账记录（按 provider_id 升序）。
func (s *PGReconciliationStore) ListByMonth(ctx context.Context, month time.Time) ([]ReconciliationRecord, error) {
	rows, err := s.db.Query(ctx, reconSelectSQL+` WHERE reconciliation_month = $1 ORDER BY provider_id`, month)
	if err != nil {
		return nil, fmt.Errorf("query reconciliation records: %w", err)
	}
	defer rows.Close()
	return scanReconciliationRows(rows)
}

const reconSelectSQL = `
	SELECT id, provider_id, reconciliation_month,
	       COALESCE(gateway_total_tokens, 0), COALESCE(gateway_input_tokens, 0),
	       COALESCE(gateway_output_tokens, 0), COALESCE(gateway_total_cost, 0),
	       COALESCE(provider_total_tokens, 0), COALESCE(provider_input_tokens, 0),
	       COALESCE(provider_output_tokens, 0), COALESCE(provider_total_cost, 0),
	       COALESCE(token_diff_rate, 0), COALESCE(cost_diff_rate, 0),
	       COALESCE(data_source, ''), COALESCE(notes, ''), updated_at
	FROM provider_cost_reconciliation`

func scanReconciliationRows(rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}) ([]ReconciliationRecord, error) {
	var out []ReconciliationRecord
	for rows.Next() {
		var rec ReconciliationRecord
		if err := rows.Scan(
			&rec.ID, &rec.ProviderID, &rec.Month,
			&rec.GatewayTotalTokens, &rec.GatewayInputTokens, &rec.GatewayOutputTokens, &rec.GatewayTotalCost,
			&rec.ProviderTotalTokens, &rec.ProviderInputTokens, &rec.ProviderOutputTokens, &rec.ProviderTotalCost,
			&rec.TokenDiffRate, &rec.CostDiffRate,
			&rec.DataSource, &rec.Notes, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan reconciliation row: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PGGatewayMonthlyUsageSource 从 request_logs_hot（热表）与 request_logs
// （冷表）按 provider + 月份聚合网关侧用量。热冷两表在归档窗口内可能
// 同时持有同一 request_id，先按 request_id 去重再聚合，避免双计。
type PGGatewayMonthlyUsageSource struct {
	db *pgxpool.Pool
}

// NewPGGatewayMonthlyUsageSource 创建。
func NewPGGatewayMonthlyUsageSource(db *pgxpool.Pool) *PGGatewayMonthlyUsageSource {
	return &PGGatewayMonthlyUsageSource{db: db}
}

// MonthlyUsageByProvider 见 GatewayUsageSource 接口文档。
func (s *PGGatewayMonthlyUsageSource) MonthlyUsageByProvider(ctx context.Context, month time.Time) ([]GatewayMonthlyUsage, error) {
	rows, err := s.db.Query(ctx, `
		WITH combined AS (
			SELECT request_id, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd, ts
			FROM request_logs_hot
			WHERE ts >= $1 AND ts < $2 AND provider_id IS NOT NULL
			UNION ALL
			SELECT request_id, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd, ts
			FROM request_logs
			WHERE ts >= $1 AND ts < $2 AND provider_id IS NOT NULL
		), deduped AS (
			SELECT DISTINCT ON (request_id) provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd
			FROM combined
			ORDER BY request_id, ts DESC
		)
		SELECT provider_id,
		       COALESCE(sum(prompt_tokens), 0),
		       COALESCE(sum(completion_tokens), 0),
		       COALESCE(sum(total_tokens), 0),
		       COALESCE(sum(cost_usd), 0)
		FROM deduped
		GROUP BY provider_id
		ORDER BY provider_id`,
		month, month.AddDate(0, 1, 0))
	if err != nil {
		return nil, fmt.Errorf("query monthly usage: %w", err)
	}
	defer rows.Close()

	var out []GatewayMonthlyUsage
	for rows.Next() {
		var u GatewayMonthlyUsage
		if err := rows.Scan(&u.ProviderID, &u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.TotalCost); err != nil {
			return nil, fmt.Errorf("scan monthly usage row: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PGReconciliationEventSink 把对账差异告警写入 provider_events 事件流
// （与 profile_auto_disabled 等画像事件同一张表，event_kind 前缀
// cost_reconciliation_diff），按 (provider_id, month) 幂等去重。
//
// provider_events.credential_id 对账场景为 NULL（供应商级事件）。
type PGReconciliationEventSink struct {
	db *pgxpool.Pool
}

// NewPGReconciliationEventSink 创建。
func NewPGReconciliationEventSink(db *pgxpool.Pool) *PGReconciliationEventSink {
	return &PGReconciliationEventSink{db: db}
}

// eventAdvisoryKey 是分配 provider_events.id 用的咨询锁 key。
// provider_events.id 在 dump 形态库上同样没有默认值/序列。
const eventAdvisoryKey = int64(8262026081502)

// EmitCostDiffAlert 见 ReconciliationAlertSink 接口文档。
func (s *PGReconciliationEventSink) EmitCostDiffAlert(ctx context.Context, rec *ReconciliationRecord) error {
	month := rec.Month.Format("2006-01")
	payload := map[string]interface{}{
		"provider_id":           rec.ProviderID,
		"month":                 month,
		"gateway_total_cost":    rec.GatewayTotalCost,
		"provider_total_cost":   rec.ProviderTotalCost,
		"gateway_total_tokens":  rec.GatewayTotalTokens,
		"provider_total_tokens": rec.ProviderTotalTokens,
		"cost_diff_rate":        rec.CostDiffRate,
		"token_diff_rate":       rec.TokenDiffRate,
		"message":               diffAlertMessage(rec),
	}
	payloadJSON, err := marshalJSON(payload)
	if err != nil {
		return fmt.Errorf("marshal alert payload: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, eventAdvisoryKey); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	// 幂等去重：同一 (provider, month) 已有告警事件则跳过。
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM provider_events
		              WHERE event_kind = 'cost_reconciliation_diff'
		                AND payload_json->>'provider_id' = $1
		                AND payload_json->>'month' = $2)`,
		fmt.Sprintf("%d", rec.ProviderID), month).Scan(&exists); err != nil {
		return fmt.Errorf("check existing alert event: %w", err)
	}
	if !exists {
		if _, err := tx.Exec(ctx, `
			INSERT INTO provider_events (id, credential_id, event_kind, payload_json, ts)
			VALUES ((SELECT COALESCE(max(id), 0) + 1 FROM provider_events), NULL,
			        'cost_reconciliation_diff', $1, now())`, payloadJSON); err != nil {
			return fmt.Errorf("insert alert event: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// diffAlertMessage 生成人类可读的告警摘要（进入 payload.message）。
func diffAlertMessage(rec *ReconciliationRecord) string {
	if breached, reason := DiffAlertReason(rec, DefaultDiffThresholds()); breached {
		return reason
	}
	return fmt.Sprintf("provider %d month %s cost diff %.4f token diff %.4f",
		rec.ProviderID, rec.Month.Format("2006-01"), rec.CostDiffRate, rec.TokenDiffRate)
}

// ---- 小工具 ----

func providerIDs(usage []GatewayMonthlyUsage) []int64 {
	return mapFunc(usage, func(u GatewayMonthlyUsage) int64 { return u.ProviderID })
}

func mapFunc[T, R any](in []T, f func(T) R) []R {
	out := make([]R, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// nullDiffRate 差异率为 0 时存 NULL，与表的 nullable 语义一致。
func nullDiffRate(f float64) interface{} {
	if f == 0 {
		return nil
	}
	return f
}

func nullableText(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
