package persist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
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
	// Schema records which key grammar the row was collected from
	// (store.KeySchemaLegacy / store.KeySchemaK2). When one logical tuple
	// exists in both grammars the canonical row replaces its legacy twin,
	// so a snapshot never double-counts a node (doc 14 §2).
	Schema         store.KeySchema
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

	// 818 起：以下 24 个字段由 payload 提升为 typed 列（实测 payload 占
	// heap 66%，其中 31 个键里有 7 个与本结构已有字段纯重复）。
	// 指针类型 = hash 中该键可能不存在，落库为 SQL NULL 而不是零值
	// ——「没采集到」与「采集到 0」在排障时是两种不同的结论。
	UpdatedAtMS          *int64
	LastProbeAtMS        *int64
	LastProbeLatencyMS   *int64
	LastAttemptMS        *int64
	LastOKMS             *int64
	LastRequestAtMS      *int64
	LastRequestErrorAtMS *int64
	ManualAtMS           *int64
	CoolUntilMS          *int64
	EventSeq             *int64
	Disabled             *bool
	LastDirectOK         *bool
	ManualHold           *bool
	SuccessCount         *int
	FailureCount         *int
	DisableCount         *int
	LatEWMAMS            *float64
	EmptyResponseRate1m  *float64
	EmptyResponseRate30m *float64
	LastErr              *string
	ManualReason         *string
	ManualActor          *string
	DisabledReason       *string
	CoolReason           *string

	// Payload 818 起语义收窄：只保留 typed 列覆盖不到的 hash 键，
	// 保住 hash schema 演进时的前向兼容（见 818 迁移头注释）。
	Payload []byte
}

// payloadDuplicateKeys 是已在 typed 列中存在、不应再进 payload 的 hash 键。
// 2026-10-02 实测：7 个键合计 70.8 B/行 = payload 的 24%，且全部是
// 已有同名列以 JSON 字符串再存一遍（"generation":"43" 而列是 bigint）。
//
// 这是一张**白名单式的排除表**：hash 里新出现的键默认**保留**在 payload 中，
// 只有明确列在此处的键才被剔除。方向不能反 —— 反了会在 hash 演进时静默丢字段。
var payloadDuplicateKeys = [...]string{
	"available",       // -> Row.Available      (boolean 列)
	"generation",      // -> Row.Generation     (bigint  列)
	"source_priority", // -> Row.SourcePriority (int     列)
	"fail_streak",     // -> Row.FailStreak     (int     列)
	"sr_1m",           // -> Row.SR1m           (real    列)
	"sr_5m",           // -> Row.SR5m           (real    列)
	"sr_30m",          // -> Row.SR30m          (real    列)
}

func (w *Writer) Collect(ctx context.Context) ([]Row, error) {
	if w == nil || w.rdb == nil {
		return nil, fmt.Errorf("ursm.v2.persist: nil redis")
	}

	// 1. Read recovery_epoch from the hash written by recovery.Manager.
	epochKey := store.EpochKey(w.prefix)
	epochHash, err := redissafe.SafeHGetAll(ctx, w.rdb, epochKey)
	var recoveryEpoch int64
	if errors.Is(err, redissafe.ErrKeyNotFound) {
		// An uninitialized epoch is equivalent to zero.
	} else if err != nil {
		return nil, fmt.Errorf("ursm.v2.persist: read recovery epoch: %w", err)
	} else {
		recoveryEpoch = atoi64(epochHash["counter"])
	}

	// 2. 扫描所有 node keys
	pattern := fmt.Sprintf("%snode:*", w.prefix)
	iter := w.rdb.Scan(ctx, 0, pattern, 500).Iterator()

	// 3. 所有行使用同一个快照时间戳，保证原子性
	snapshotTS := time.Now()
	var out []Row

	for iter.Next(ctx) {
		k := iter.Val()

		// request_dedup markers (<nodeKey>:request_dedup:<sha256hex>,
		// record_request.go requestDedupKey) are STRING flags written with
		// SET NX EX, not node state hashes. The legacy grammar's trailing
		// rejoin would "decode" them into a node tuple and SafeHGetAll's
		// wrong-type error would abort the whole snapshot batch every tick
		// (P4, docs/audit/2026-09-12-r14-observation-p5p4-readonly.md §C).
		// They are not snapshot state: skip before parse — same exemption
		// the migration preflight already applies. Unknown wrong-type keys
		// must still abort below.
		if strings.Contains(k, ":request_dedup:") {
			continue
		}

		// 4. Decode the node key under either grammar. A key neither
		// grammar can decode is ambiguous: it is excluded from the
		// snapshot and left to the migration preflight's NO-GO
		// classification — never guessed into a tuple (doc 14 §4).
		parsed, ok := store.ParseNodeKeyAny(w.prefix, k)
		if !ok {
			slog.Warn("ursm.v2: persist excluded ambiguous node key", "key", k)
			continue
		}

		// 5. Read hash fields through the type-checking wrapper. A missing key
		// is normal during TTL expiry; any other read error must be visible so
		// a corrupted node key cannot silently disappear from the snapshot.
		hash, err := redissafe.SafeHGetAll(ctx, w.rdb, k)
		if errors.Is(err, redissafe.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			slog.Warn("ursm.v2: persist failed to read node hash", "operation", "hgetall", "error", err)
			return nil, fmt.Errorf("ursm.v2.persist: read node hash: %w", err)
		}
		if len(hash) == 0 {
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
			Schema:         parsed.Schema,
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

		// 7. 将 hash 序列化为 Payload。
		//
		// 818 起语义收窄：**剔除 7 个与 typed 列重复的键**（零信息损失，
		// 实测省 70.8 B/行），其余键原样保留 —— hash 来自 HGETALL，
		// schema 会演进，payload 是新字段唯一的兜底仓（见 818 迁移头注释）。
		//
		// 排除方向是「白名单剔除」而非「黑名单保留」：hash 新增的键默认留在
		// payload 里。若反过来（只保留已知键），hash 加字段时会静默丢数据。
		residual := make(map[string]string, len(hash))
		for k, v := range hash {
			if isPayloadDuplicateKey(k) {
				continue
			}
			residual[k] = v
		}
		if payloadBytes, err := json.Marshal(residual); err == nil {
			row.Payload = payloadBytes
		} else {
			slog.Warn("ursm.v2: persist failed to marshal payload",
				"key", k,
				"error", err)
			// 继续处理，只是 Payload 为空
		}

		// 8. 818：24 个 hash 键提升为 typed 列。
		assignInt64(&row.UpdatedAtMS, hash["updated_at_ms"])
		assignInt64(&row.LastProbeAtMS, hash["last_probe_at_ms"])
		assignInt64(&row.LastProbeLatencyMS, hash["last_probe_latency_ms"])
		assignInt64(&row.LastAttemptMS, hash["last_attempt_ms"])
		assignInt64(&row.LastOKMS, hash["last_ok_ms"])
		assignInt64(&row.LastRequestAtMS, hash["last_request_at_ms"])
		assignInt64(&row.LastRequestErrorAtMS, hash["last_request_error_at_ms"])
		assignInt64(&row.ManualAtMS, hash["manual_at_ms"])
		assignInt64(&row.CoolUntilMS, hash["cool_until_ms"])
		assignInt64(&row.EventSeq, hash["event_seq"])
		assignBool(&row.Disabled, hash["disabled"])
		assignBool(&row.LastDirectOK, hash["last_direct_ok"])
		assignBool(&row.ManualHold, hash["manual_hold"])
		assignInt(&row.SuccessCount, hash["success_count"])
		assignInt(&row.FailureCount, hash["failure_count"])
		assignInt(&row.DisableCount, hash["disable_count"])
		assignFloat(&row.LatEWMAMS, hash["lat_ewma_ms"])
		assignFloat(&row.EmptyResponseRate1m, hash["empty_response_rate_1m"])
		assignFloat(&row.EmptyResponseRate30m, hash["empty_response_rate_30m"])
		assignStr(&row.LastErr, hash["last_err"])
		assignStr(&row.ManualReason, hash["manual_reason"])
		assignStr(&row.ManualActor, hash["manual_actor"])
		assignStr(&row.DisabledReason, hash["disabled_reason"])
		assignStr(&row.CoolReason, hash["cool_reason"])

		out = append(out, row)
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("redis scan failed: %w", err)
	}

	// During dual mode one logical tuple exists in both grammars; the
	// canonical row is authoritative and replaces its legacy twin so a
	// snapshot never double-counts a node (doc 14 §5.2.1).
	type tupleKey struct {
		tenant string
		cid    int
		raw    string
	}
	idxByTuple := make(map[tupleKey]int, len(out))
	uniq := make([]Row, 0, len(out))
	for _, r := range out {
		key := tupleKey{r.TenantID, r.CredentialID, r.RawModel}
		if idx, seen := idxByTuple[key]; seen {
			if r.Schema == store.KeySchemaK2 {
				uniq[idx] = r
			}
			continue
		}
		idxByTuple[key] = len(uniq)
		uniq = append(uniq, r)
	}
	out = uniq

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
   lat_p50_ms, score, source_priority, generation, payload,
   -- 818：24 个由 payload 提升出来的 typed 列
   updated_at_ms, last_probe_at_ms, last_probe_latency_ms, last_attempt_ms,
   last_ok_ms, last_request_at_ms, last_request_error_at_ms, manual_at_ms,
   cool_until_ms, event_seq,
   disabled, last_direct_ok, manual_hold,
   success_count, failure_count, disable_count,
   lat_ewma_ms, empty_response_rate_1m, empty_response_rate_30m,
   last_err, manual_reason, manual_actor, disabled_reason, cool_reason)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22::text::jsonb,
        $23,$24,$25,$26,$27,$28,$29,$30,$31,$32,
        $33,$34,$35,
        $36,$37,$38,
        $39,$40,$41,
        $42,$43,$44,$45,$46)
ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING`,
			r.SnapshotTS, r.RecoveryEpoch, r.ProviderID, r.CredentialID, r.RawModel,
			r.CanonicalName, r.TenantID, r.Available, r.HealthStatus, r.FailStreak, r.CoolUntil,
			r.SR1m, r.SR5m, r.SR30m, r.Samples1m, r.Samples5m, r.Samples30m,
			r.LatP50Ms, r.Score, r.SourcePriority, r.Generation,
			payloadStr,
			r.UpdatedAtMS, r.LastProbeAtMS, r.LastProbeLatencyMS, r.LastAttemptMS,
			r.LastOKMS, r.LastRequestAtMS, r.LastRequestErrorAtMS, r.ManualAtMS,
			r.CoolUntilMS, r.EventSeq,
			r.Disabled, r.LastDirectOK, r.ManualHold,
			r.SuccessCount, r.FailureCount, r.DisableCount,
			r.LatEWMAMS, r.EmptyResponseRate1m, r.EmptyResponseRate30m,
			r.LastErr, r.ManualReason, r.ManualActor, r.DisabledReason, r.CoolReason,
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
