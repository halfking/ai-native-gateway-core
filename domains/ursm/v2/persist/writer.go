package persist

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

type Writer struct {
	rdb    *redis.Client
	prefix string
	db     *pgxpool.Pool
	every  time.Duration
}

func New(rdb *redis.Client, prefix string, db *pgxpool.Pool) *Writer {
	return &Writer{rdb: rdb, prefix: prefix, db: db, every: time.Minute}
}

type Row struct {
	SnapshotTS     time.Time
	RecoveryEpoch  int64
	ProviderID     int
	CredentialID   int
	RawModel       string
	CanonicalName  string
	TenantID       string
	Available      bool
	HealthStatus   string
	FailStreak     int
	CoolUntil      *time.Time // nullable: NULL when not in cooldown
	SR1m           float64
	SR5m           float64
	SR30m          float64
	Samples1m      int
	Samples5m      int
	Samples30m     int
	LatP50Ms       int
	Score          float64
	SourcePriority int
	Generation     int64
	Payload        []byte
}

func (w *Writer) Collect(ctx context.Context) ([]Row, error) {
	if w == nil || w.rdb == nil {
		return nil, fmt.Errorf("ursm.v2.persist: nil redis")
	}

	// 1. Read recovery_epoch from the hash written by recovery.Manager.
	epochKey := store.EpochKey(w.prefix)
	epochStr, err := w.rdb.HGet(ctx, epochKey, "counter").Result()
	var recoveryEpoch int64
	if err == nil {
		recoveryEpoch = atoi64(epochStr)
	} else if err != redis.Nil {
		return nil, fmt.Errorf("ursm.v2.persist: read recovery epoch: %w", err)
	}

	// 2. 扫描所有 node keys
	pattern := fmt.Sprintf("%snode:*", w.prefix)
	iter := w.rdb.Scan(ctx, 0, pattern, 500).Iterator()

	// 3. 所有行使用同一个快照时间戳，保证原子性
	snapshotTS := time.Now()
	var out []Row

	for iter.Next(ctx) {
		k := iter.Val()

		// 4. Decode the tenant-aware Redis key without losing colons in raw model.
		parsed, ok := store.ParseNodeKey(w.prefix, k)
		if !ok {
			slog.Warn("ursm.v2: persist skipped invalid node key", "key", k)
			continue
		}

		// 5. Read hash fields.
		hash, err := w.rdb.HGetAll(ctx, k).Result()
		if err != nil || len(hash) == 0 {
			continue
		}
		tenantID := parsed.TenantID
		if hashTenant := hash["tenant_id"]; hashTenant != "" {
			if tenantID != "" && hashTenant != tenantID {
				slog.Warn("ursm.v2: persist skipped tenant mismatch", "key", k, "key_tenant", tenantID, "hash_tenant", hashTenant)
				continue
			}
			tenantID = hashTenant
		}

		// 6. Construct the audit row from the authoritative key and hash.
		row := Row{
			SnapshotTS:     snapshotTS,
			RecoveryEpoch:  recoveryEpoch,
			CredentialID:   parsed.CredentialID,
			RawModel:       parsed.RawModel,
			ProviderID:     atoi(hash["provider_id"]),
			CanonicalName:  hash["canonical"],
			TenantID:       tenantID,
			Available:      hash["available"] == "1",
			HealthStatus:   hash["health"],
			FailStreak:     atoi(hash["fail_streak"]),
			SR1m:           parseFloat(hash["sr_1m"]),
			SR5m:           parseFloat(hash["sr_5m"]),
			SR30m:          parseFloat(hash["sr_30m"]),
			Samples1m:      atoi(hash["samples_1m"]),
			Samples5m:      atoi(hash["samples_5m"]),
			Samples30m:     atoi(hash["samples_30m"]),
			LatP50Ms:       atoi(hash["lat_p50_ms"]),
			Score:          parseFloat(hash["score"]),
			SourcePriority: atoi(hash["source_priority"]),
			Generation:     atoi64(hash["generation"]),
		}

		// Parse cool_until if present (nullable)
		if coolStr := hash["cool_until_ms"]; coolStr != "" {
			if coolMs := atoi64(coolStr); coolMs > 0 {
				coolTime := time.UnixMilli(coolMs)
				row.CoolUntil = &coolTime
			}
		}

		// 7. 将完整的 hash 序列化为 Payload（用于完整恢复）
		// 即使我们已经提取了关键字段，Payload 包含所有原始数据
		// 以防未来需要恢复其他字段（pricing, concurrency, etc.）
		if payloadBytes, err := json.Marshal(hash); err == nil {
			row.Payload = payloadBytes
		} else {
			slog.Warn("ursm.v2: persist failed to marshal payload",
				"key", k,
				"error", err)
			// 继续处理，只是 Payload 为空
		}

		out = append(out, row)
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("redis scan failed: %w", err)
	}

	// 7. 诊断日志：帮助定位为何没有数据
	if len(out) == 0 {
		slog.Warn("ursm.v2: persist collect empty",
			"pattern", pattern,
			"recovery_epoch", recoveryEpoch,
		)
	}

	return out, nil
}

func (w *Writer) Flush(ctx context.Context, rows []Row) error {
	if w == nil || w.db == nil || len(rows) == 0 {
		return nil
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, r := range rows {
		// Payload 应该在 Collect() 中已经填充；如果为空则用 placeholder
		// （正常情况下不应该为空，除非 json.Marshal 失败）
		if len(r.Payload) == 0 {
			r.Payload, _ = json.Marshal(map[string]any{
				"_note": "payload empty, hash marshal failed in Collect()",
			})
		}

		// 2026-07-22: 使用 ::text::jsonb cast 避免 22P02 错误
		// pgx 的二进制协议传输 []byte 到 JSONB 列时，若含非 UTF-8 字符、
		// NaN/Inf 或其他非法 JSON 片段会触发 "invalid input syntax for type json"。
		// 转为 string 并使用 text→jsonb 类型转换可强制 PG 的 JSON 解析器处理。
		// 匹配 domains/analysis/bus/publisher.go:86 和 telemetry/client.go:572 的模式。
		payloadStr := string(r.Payload)

		_, err := tx.Exec(ctx, `
INSERT INTO ursm_node_snapshot_min
  (snapshot_ts, recovery_epoch, provider_id, credential_id, raw_model_name, canonical_name, tenant_id,
   available, health_status, fail_streak, cool_until,
   sr_1m, sr_5m, sr_30m, samples_1m, samples_5m, samples_30m,
   lat_p50_ms, score, source_priority, generation, payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22::text::jsonb)
ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING`,
			r.SnapshotTS, r.RecoveryEpoch, r.ProviderID, r.CredentialID, r.RawModel,
			r.CanonicalName, r.TenantID, r.Available, r.HealthStatus, r.FailStreak, r.CoolUntil,
			r.SR1m, r.SR5m, r.SR30m, r.Samples1m, r.Samples5m, r.Samples30m,
			r.LatP50Ms, r.Score, r.SourcePriority, r.Generation,
			payloadStr,
		)
		if err != nil {
			return fmt.Errorf("insert row cid=%d model=%s: %w", r.CredentialID, r.RawModel, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// 记录成功写入的行数和第一行样本，便于审计
	slog.Info("ursm.v2: persist committed",
		"rows", len(rows),
		"first_cid", rows[0].CredentialID,
		"first_model", rows[0].RawModel,
		"snapshot_ts", rows[0].SnapshotTS.Format(time.RFC3339),
	)

	return nil
}
