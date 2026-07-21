package persist

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
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
	SnapshotTS    time.Time
	RecoveryEpoch int64
	ProviderID    int
	CredentialID  int
	RawModel      string
	CanonicalName string
	TenantID      string
	Available     bool
	HealthStatus  string
	FailStreak    int
	SR1m          float64
	SR5m          float64
	SR30m         float64
	Samples1m     int
	LatP50Ms      int
	Score         float64
	Generation    int64
	Payload       []byte
}

func (w *Writer) Collect(ctx context.Context) ([]Row, error) {
	if w == nil || w.rdb == nil {
		return nil, fmt.Errorf("ursm.v2.persist: nil redis")
	}
	pattern := fmt.Sprintf("%snode:*", w.prefix)
	iter := w.rdb.Scan(ctx, 0, pattern, 500).Iterator()
	var out []Row
	for iter.Next(ctx) {
		k := iter.Val()
		hash, err := w.rdb.HGetAll(ctx, k).Result()
		if err != nil {
			continue
		}
		row := Row{
			Available:    hash["available"] == "1",
			HealthStatus: hash["health"],
			FailStreak:   atoi(hash["fail_streak"]),
			SR1m:         parseFloat(hash["sr_1m"]),
			SR5m:         parseFloat(hash["sr_5m"]),
			SR30m:        parseFloat(hash["sr_30m"]),
			Samples1m:    atoi(hash["samples_1m"]),
			LatP50Ms:     atoi(hash["lat_p50_ms"]),
			Score:        parseFloat(hash["score"]),
			Generation:   atoi64(hash["generation"]),
		}
		out = append(out, row)
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
		if len(r.Payload) == 0 {
			r.Payload, _ = json.Marshal(map[string]any{"placeholder": true})
		}
		_, err := tx.Exec(ctx, `
INSERT INTO ursm_node_snapshot_min
  (snapshot_ts, recovery_epoch, provider_id, credential_id, raw_model_name, canonical_name, tenant_id,
   available, health_status, fail_streak, sr_1m, sr_5m, sr_30m, samples_1m, lat_p50_ms, score, generation, payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (snapshot_ts, credential_id, raw_model_name) DO NOTHING`, r.SnapshotTS, r.RecoveryEpoch, r.ProviderID, r.CredentialID, r.RawModel, r.CanonicalName, r.TenantID, r.Available, r.HealthStatus, r.FailStreak, r.SR1m, r.SR5m, r.SR30m, r.Samples1m, r.LatP50Ms, r.Score, r.Generation, r.Payload)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
