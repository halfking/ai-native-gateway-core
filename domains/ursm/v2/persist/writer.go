package persist

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

	// readHashes reads every candidate key in ONE call. §10.110.56 replaced a
	// per-key SafeHGetAll loop (two serial round trips each, ~3,600 total at
	// ~1,800 keys) with a chunked pipeline; this seam exists so that fact is
	// observable in a test rather than only in production logs. Nil means the
	// real chunked pipeline path.
	readHashes func(ctx context.Context, keys []string) (map[string]map[string]string, error)
}

// WithHashReader overrides how Collect reads node hashes. Intended for tests
// and benchmarks; production wiring uses the default chunked pipeline.
func (w *Writer) WithHashReader(fn func(ctx context.Context, keys []string) (map[string]map[string]string, error)) *Writer {
	if w != nil {
		w.readHashes = fn
	}
	return w
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
	// 875：`_5m` 原先只在 payload 里（两个兄弟已是列）。
	// 生产实测它由同一批 270 行携带，store/pipeline.go:132 读它。
	EmptyResponseRate5m  *float64
	EmptyResponseRate30m *float64
	LastErr              *string
	ManualReason         *string
	ManualActor          *string
	DisabledReason       *string
	CoolReason           *string

	// Payload 818 起语义收窄：只保留 typed 列覆盖不到的 hash 键，
	// 保住 hash schema 演进时的前向兼容（见 818 迁移头注释）。
	//
	// 2026-10-18（§10.110.63）：残渣为空时**保持 nil**，不再写 `{}`。
	// 生产实测 85.0% 的行携带空对象，而 `pg_column_size('{}'::jsonb)` = 8 B
	// —— 空 jsonb 并不免费，1531 行 × 8 B ≈ 34 MB/天 ≈ 12.6 GB/年。
	// 该列在迁移 453、四份 baseline 副本与生产上**均可空**，
	// 故这不是约束变更，只是不再存一个没有信息量的值。
	Payload []byte

	// PayloadMarshalFailed 标记「本该有残渣、但 json.Marshal 失败」。
	//
	// ⚠ 它存在的唯一理由是把两种「Payload 为空」区分开：
	//   ① 残渣本来就空（85% 的行）—— 正常，什么都不该写；
	//   ② marshal 失败 —— 异常，必须留下 `_note` 占位痕迹。
	// 没有这个标志，Flush 里那句「Payload 为空就写 _note」会把
	// ① 也一并打上占位（每行约 50 B），比原来的 8 B 空对象**差 6 倍**。
	PayloadMarshalFailed bool
}

// payloadDuplicateKeys 是已在 typed 列中存在、不应再进 payload 的 hash 键。
//
// 2026-10-02 初版只列了 7 个键，而同一次 818 迁移实际把 **31 个** hash 键
// 提升为 typed 列 —— 漏掉的 24 个既进了 typed 列、又被原样抄进 payload，
// 同一个值存了两份。2026-10-03 生产实测：payload 35 个键里 27 个重复；
// 逐值比对 3797 行样本 8 个字段 3797/3797 完全一致。
//
// 实测收益（ursm_node_snapshot_min，19,877,268 行 / 10 GB）：
//
//	payload 215 -> 34 B/行，整行 402 -> 220 B（-45.1%），全表约省 3436 MB。
//
// 这是一张**白名单式的排除表**：hash 里新出现的键默认**保留**在 payload 中，
// 只有明确列在此处的键才被剔除。方向不能反 —— 反了会在 hash 演进时静默丢字段。
//
// ★ 清单靠人维护就会漂（7 vs 31 就是这么来的）。TestPayloadDuplicateKeysCoverAllTypedHashKeys
//
//	会把 writer.go 里每一处 hash["..."] 读取都与本表比对，漏一个就红。
//	新增 typed 列时若忘了加进来，那条测试就是提醒。
//
// ⚠⚠ 但那道门**只覆盖一个方向**（2026-10-18 §10.110.62 查实）：
//
//	它的全集是 **writer.go 自己的 hash["..."] 读取**，
//	所以「store 层读了、而 writer 压根没读」的键对它**结构性不可见**。
//	本表补的正是那个方向：TestStoreReadKeysAreTypedOrDeclaredResident。
var payloadDuplicateKeys = [...]string{
	// —— 818 之前就已存在的 7 个 ——
	"available",       // -> Row.Available      (boolean 列)
	"generation",      // -> Row.Generation     (bigint  列)
	"source_priority", // -> Row.SourcePriority (int     列)
	"fail_streak",     // -> Row.FailStreak     (int     列)
	"sr_1m",           // -> Row.SR1m           (real    列)
	"sr_5m",           // -> Row.SR5m           (real    列)
	"sr_30m",          // -> Row.SR30m          (real    列)

	// —— 818 提升但当初漏排的 24 个 ——
	"canonical",                // -> Row.CanonicalName  (text)
	"tenant_id",                // -> Row.TenantID       (text)
	"provider_id",              // -> Row.ProviderID     (integer)
	"health",                   // -> Row.HealthStatus   (text)
	"score",                    // -> Row.Score          (real)
	"lat_p50_ms",               // -> Row.LatP50Ms       (integer)
	"updated_at_ms",            // -> Row.UpdatedAtMS    (bigint)
	"last_probe_at_ms",         // -> Row.LastProbeAtMS  (bigint)
	"last_probe_latency_ms",    // -> Row.LastProbeLatencyMS
	"last_attempt_ms",          // -> Row.LastAttemptMS  (bigint)
	"last_ok_ms",               // -> Row.LastOKMS       (bigint)
	"last_request_at_ms",       // -> Row.LastRequestAtMS
	"last_request_error_at_ms", // -> Row.LastRequestErrorAtMS
	"manual_at_ms",             // -> Row.ManualAtMS     (bigint)
	"cool_until_ms",            // -> Row.CoolUntilMS    (bigint)
	"event_seq",                // -> Row.EventSeq       (bigint)
	"disabled",                 // -> Row.Disabled       (boolean)
	"last_direct_ok",           // -> Row.LastDirectOK   (boolean)
	"manual_hold",              // -> Row.ManualHold     (boolean)
	"success_count",            // -> Row.SuccessCount   (integer)
	"failure_count",            // -> Row.FailureCount   (integer)
	"disable_count",            // -> Row.DisableCount   (integer)
	"lat_ewma_ms",              // -> Row.LatEWMAMS      (real)
	"empty_response_rate_1m",   // -> Row.EmptyResponseRate1m
	"empty_response_rate_5m",   // -> Row.EmptyResponseRate5m（875：原只在 payload）
	"empty_response_rate_30m",  // -> Row.EmptyResponseRate30m
	"last_err",                 // -> Row.LastErr        (text)
	"manual_reason",            // -> Row.ManualReason   (text)
	"manual_actor",             // -> Row.ManualActor    (text)
	"disabled_reason",          // -> Row.DisabledReason (text)
	"cool_reason",              // -> Row.CoolReason     (text)

	// —— 窗口采样字段，writer 在更早处映射，同样是 typed 列 ——
	"samples_1m",  // -> Row.Samples1m
	"samples_5m",  // -> Row.Samples5m
	"samples_30m", // -> Row.Samples30m
}

// payloadResidentKeys 是「store 层在读、但快照表**没有**对应 typed 列」的 hash 键。
//
// 它们因此只能从 payload 里取 —— 而 payload 是**兜底仓**，不是查询面：
// 一次分析查询要拿到它们必须写 `payload->>'x'`，走不了索引、也不会被
// 类型检查或列清单校验覆盖到。
//
// ★ 本表存在的唯一理由：让「这个键读不到 typed 列」变成一个**显式决定**
//
//	而不是沉默。TestStoreReadKeysAreTypedOrDeclaredResident 强制每个
//	store 读取的键都落进「typed 列」或「本表」二者之一。
//
// —— 2026-10-18 生产实测（§10.110.62）——
//
//	实测这 8 个键只由 **同一批 270 行（15%）** 携带，其余 1531 行（85%）
//	携带的是空 `{}`。每轮 payload 合计 65,274 B，占整行 324 B 的 11.7%。
//
//	`empty_response_rate_5m` 尤其刺眼：它的两个兄弟 `..._1m` / `..._30m`
//	**都已经**是 typed 列（见上表），唯独中间这个留在 jsonb 里。
var payloadResidentKeys = [...]string{
	"empty_responses_1m",  // -> store/pipeline.go:128  v.EmptyResponses1m
	"empty_responses_5m",  // -> store/pipeline.go:129  v.EmptyResponses5m
	"empty_responses_30m", // -> store/pipeline.go:130  v.EmptyResponses30m
	// ★ 这一条是被新门**自己抓出来的**，不是人工翻出来的：
	//   probe_evidence.go:140 用 `boolField(raw, "last_request_failed")` 读它
	//   —— 函数式访问器，下标正则看不见它。生产 payload 实测 270 行携带。
	"last_request_failed", // -> store/probe_evidence.go:140  boolField(raw, ...)
	// ⚠ lat_p95_ms 目前是**三重死字段**（§10.110.62 实测）：
	//   store/pipeline.go:135 读它、api/types.go:114 声明它，
	//   但全仓**没有任何地方往 Redis 写它**，persist 也不写 ——
	//   快照表里 lat_p95_ms 列最新一轮 1816 行全 NULL，最近 200 轮累计 0 次非 NULL。
	//   快照里它是 integer 列（存在），但恒为 NULL；payload 里也从不出现。
	//   ⇒ 保留在表里是因为「读得到」比「查得到」重要：一旦上游开始写，
	//   它会立刻出现在 payload 里而不会被静默丢弃。
	"lat_p95_ms",
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

	// ── §10.110.56：把「扫一个 key 就往返读一次」拆成三段 ──
	//
	// 改动前这个循环体里做的是 SafeHGetAll(ctx, w.rdb, k)，而它每次调用是
	// TYPE + HGETALL **两次串行往返**（internal/redis/safe_operations.go）。
	// 每轮 ~1,800 个 key ⇒ ~3,600~3,800 次往返挤在同一个 30s 预算里：
	//
	//	30,000 ms ÷ 3,800 ≈ 7.9 ms/往返  ← 临界线
	//
	// 实测往返 8.8ms（154 视角）/ 26.1ms（245 视角）已经越过这条线（§54），
	// 于是 Collect 必然吃满预算，Flush 只剩几百毫秒，单事务回滚 → 整轮全丢。
	//
	// 现在：阶段 1 扫齐 key 并按原顺序解析 → 阶段 2 分块批量读
	// → 阶段 3 按扫描顺序建行。读哪些 key、怎么解析、哪些行被丢弃，全部不变。

	// 阶段 1：扫齐 key 并解析。
	type nodeCandidate struct {
		key    string
		parsed store.ParsedNodeKeyWithSchema
	}
	candidates := make([]nodeCandidate, 0, 2048)
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

		// Decode the node key under either grammar. A key neither
		// grammar can decode is ambiguous: it is excluded from the
		// snapshot and left to the migration preflight's NO-GO
		// classification — never guessed into a tuple (doc 14 §4).
		parsed, ok := store.ParseNodeKeyAny(w.prefix, k)
		if !ok {
			slog.Warn("ursm.v2: persist excluded ambiguous node key", "key", k)
			continue
		}
		candidates = append(candidates, nodeCandidate{key: k, parsed: parsed})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("redis scan failed: %w", err)
	}

	// 阶段 2：分块批量读。批大小让单次 HGETALL 回复有界，
	// 往返次数 = 2 × ⌈N/批⌉（每块两次 pipeline：TYPE 一轮、HGETALL 一轮）。
	keys := make([]string, len(candidates))
	for i, c := range candidates {
		keys[i] = c.key
	}
	readHashes := w.readHashes
	if readHashes == nil {
		readHashes = func(ctx context.Context, keys []string) (map[string]map[string]string, error) {
			return redissafe.SafeHGetAllBatch(ctx, w.rdb, keys, collectHashBatchSize())
		}
	}
	hashes, err := readHashes(ctx, keys)
	if err != nil {
		// 与改动前同一语义：任何一个 key 读失败就让整轮返回错误（0 行）。
		slog.Warn("ursm.v2: persist failed to read node hashes",
			"operation", "hgetall_pipeline", "keys", len(keys), "error", err)
		return nil, fmt.Errorf("ursm.v2.persist: read node hash: %w", err)
	}

	// 阶段 3：按扫描顺序建行（first_cid 语义与改动前一致）。
	//
	// ★ skippedSeedRows 统计「命中种子签名」而被跳过的行（§143.44 选项 A）。
	//   它必须**可观测**：若某天它从几十万掉到 0，说明种子源停了（或判据被改坏）
	//   ⇒ 只有日志能让人立刻发现。
	skippedSeedRows := 0
	for _, c := range candidates {
		parsed := c.parsed
		hash, present := hashes[c.key]
		if !present {
			// SafeHGetAll 对缺席键返回 ErrKeyNotFound，改动前是 continue。
			continue
		}
		if len(hash) == 0 {
			continue
		}
		tenantID := parsed.TenantID
		if hashTenant := hash["tenant_id"]; hashTenant != "" {
			if tenantID != "" && hashTenant != tenantID {
				slog.Warn("ursm.v2: persist skipped tenant mismatch", "key", c.key, "key_tenant", tenantID, "hash_tenant", hashTenant)
				continue
			}
			tenantID = hashTenant
		}

		// ★★ §143.44 选项 A（**按实证修订后的判据**）：跳过「**种子签名**」的行。
		//
		// 背景（§143.42 实测）：这些行是 `MigrateFpSlotsNodeStates`
		//（domains/ursm/v2/cache/migrate_fpslots.go:88 —— 全仓**唯一**
		// `NodeKeyForTenant(prefix, "", …)` 的非测试调用点）播下的**退化种子**：
		// 它把 legacy fp 状态铺成一个 node hash，但**不携带任何观测信息**。
		//
		// ★★★ 判据为什么是「generation==1 ∧ updated_at_ms==0」而不是「租户为空」
		// （2026-10-21 两实例实测推翻了字面版本）：
		//
		//	211（分区化后的全量 6,070,457 行）：租户分布**只有 `default`**，
		//	  空租户行 = **0**；gen1 = 1,166,352（19.2%）但 **updated_at_ms=0 的行 = 0**
		//	  ⇒ ⇒ 211 的那些 gen1 行**已经被真实观测覆盖过**（updated_at_ms ≠ 0）
		//	  ⇒ ⇒ **它们携带观测，不是垃圾** ⇒ 「跳过空租户」在 211 上收益 = 0，
		//	  而且「跳过空租户」会误伤「无租户部署」形态下的真实观测。
		//	252（20261020 分区）：空租户组 897,036 行 **100%** 命中
		//	  `generation=1 ∧ updated_at_ms=0`（且 payload IS NULL、source_priority=10）；
		//	  而 `default` 组里 updated_at_ms=0 的行 = **0**
		//	  ⇒ ⇒ **这个签名在 252 上精确命中退化行、在真实观测上零命中**。
		//
		// ⇒ ⇒ **种子签名 = 「generation==1 ∧ updated_at_ms==0」**：
		//   252 命中 24~25%（要砍的那 25.2%），211 命中 0（无收益也无损害），
		//   两实例的真实观测**零误伤**（真实观测的 updated_at_ms 恒 > 0）。
		//
		// ⚠ 判据落在**最终行**上（不是 key 的租户）⇒ 「无租户部署」形态免疫。
		if atoi64(hash["generation"]) == 1 && atoi64(hash["updated_at_ms"]) == 0 {
			skippedSeedRows++
			continue
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
		if len(residual) == 0 {
			// 2026-10-18（§10.110.63）：残渣为空 ⇒ 写 NULL，不写 {}。
			// 该列可空（迁移 453 / baseline / 生产三处一致），所以这不是约束变更。
			// `payload->>'x'` 对 NULL 与对 {} 返回同样的 NULL，
			// 唯一会变的是 `payload ? 'x'`（NULL vs false）与 jsonb_object_keys 的计数。
			slog.Debug("ursm.v2: persist payload residual empty, storing NULL",
				"key", c.key)
		} else if payloadBytes, err := json.Marshal(residual); err == nil {
			row.Payload = payloadBytes
		} else {
			row.PayloadMarshalFailed = true
			slog.Warn("ursm.v2: persist failed to marshal payload",
				"key", c.key,
				"error", err)
			// 继续处理，Flush 会写 _note 占位痕迹
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
		assignFloat(&row.EmptyResponseRate5m, hash["empty_response_rate_5m"])
		assignFloat(&row.EmptyResponseRate30m, hash["empty_response_rate_30m"])
		assignStr(&row.LastErr, hash["last_err"])
		assignStr(&row.ManualReason, hash["manual_reason"])
		assignStr(&row.ManualActor, hash["manual_actor"])
		assignStr(&row.DisabledReason, hash["disabled_reason"])
		assignStr(&row.CoolReason, hash["cool_reason"])

		out = append(out, row)
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

	// ★ §143.44 选项 A 的可观测点：命中种子签名（generation=1 ∧ updated_at_ms=0）
	//   而被跳过的行数。实测基线：252 ≈ 占扫描键数 24~25%；211 = 0
	//   （211 的种子早已被真实观测覆盖 ⇒ 那里没有无信息行 ⇒ 这是正常值，不是故障）。
	if skippedSeedRows > 0 {
		slog.Info("ursm.v2: persist skipped legacy-seed rows",
			"skipped", skippedSeedRows,
			"scanned", len(candidates),
			"rows", len(out))
	}

	return out, nil
}

// payloadArgFor 决定 INSERT 给 payload 列绑什么。
//
// ★ 返回 any 而不是 string：**空 payload 必须绑未类型化的 nil**。
// `string(nil)` 得到 ""，而 `”::jsonb` 在 PG 里是**语法错误**（不是 NULL）
// ⇒ 一行出错就会让整条 INSERT 失败、整轮事务回滚 —— 1,802 行全丢。
//
// 三态：
//
//	payload 非空          → 原样绑定
//	marshal 失败（罕见）    → 写 _note 占位，留下痕迹
//	残渣本来就空（85%）    → nil ⇒ SQL NULL
//
// ⚠ 抽成纯函数是为了能**不连数据库**就单测这个决定 ——
//
//	真库门是 opt-in 的，被静默跳过时（见 2026-10-17 审计 §七.6）
//	最需要承重的恰恰是「空值别绑成空串」这条。
func payloadArgFor(r Row) any {
	switch {
	case len(r.Payload) > 0:
		return string(r.Payload)
	case r.PayloadMarshalFailed:
		placeholder, err := json.Marshal(map[string]any{
			"_note": "payload empty, hash marshal failed in Collect()",
		})
		if err != nil {
			return nil
		}
		return string(placeholder)
	default:
		return nil
	}
}

// snapshotInsertSQL 是快照行的 INSERT 语句。
//
// ★ 提到包级常量有两个理由（§143.11）：
//
//	① **排序列必须从同一份 SQL 派生**（见 snapshotConflictColumns）。
//	   两份各写各的常量必然漂移，而漂移的后果是**静默的**：
//	   排序列少一维 ⇒ btree 叶页密度从 ~90% 掉回 ~70% ⇒ 索引凭空多占 ~22%。
//	② 门（writer_index_order_test.go）直接读这份文本，
//	   断言「排序键集合 == ON CONFLICT 列集合」—— 改约束的人一定会撞到它。
//
// ⚠ 语句本身**逐字未改**，只是从 Flush 的函数体搬到这里。
const snapshotInsertSQL = `
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
   lat_ewma_ms, empty_response_rate_1m, empty_response_rate_5m, empty_response_rate_30m,
   last_err, manual_reason, manual_actor, disabled_reason, cool_reason)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22::text::jsonb,
        $23,$24,$25,$26,$27,$28,$29,$30,$31,$32,
        $33,$34,$35,
        $36,$37,$38,
        $39,$40,$41,$42,
        $43,$44,$45,$46,$47)
ON CONFLICT (snapshot_ts, tenant_id, credential_id, raw_model_name) DO NOTHING`

// snapshotConflictColumnsRE 从 snapshotInsertSQL 里抽 ON CONFLICT 的推断列。
var snapshotConflictColumnsRE = regexp.MustCompile(`(?is)ON\s+CONFLICT\s*\(([^)]*)\)`)

// snapshotConflictColumns 返回 ON CONFLICT 的推断列（去空白，保持出现顺序）。
//
// ★ 它是**排序列的唯一来源**：排序键 = 这份列表 ⇒ 改唯一约束时排序自动跟上，
//
//	不存在「约束改了、排序没改」这种漂移（§10.59 家族：ON CONFLICT 列与
//	唯一约束不一致 ⇒ 写入 100% 失败；那次是 701 次写入失败、62 小时无告警）。
func snapshotConflictColumns() []string {
	m := snapshotConflictColumnsRE.FindStringSubmatch(snapshotInsertSQL)
	if m == nil {
		return nil
	}
	parts := strings.Split(m[1], ",")
	cols := make([]string, 0, len(parts))
	for _, p := range parts {
		if c := strings.TrimSpace(p); c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

// snapshotIndexColumnCmp 是「列名 → 比较函数」的**穷举表**。
//
// ★ 为什么必须是穷举，而不是「取不到就跳过」：
//
//	漏一个列名 ⇒ 排序少一维 ⇒ 索引密度悄悄退化，**而没有任何报错**。
//	⇒ 门断言「ON CONFLICT 的列集合 == 本表的键集合」：
//	  新增一列而忘了加比较器 ⇒ 门红（而不是等到线上索引胖了才发现）。
//
// ★ 运行时再退一步：真的取不到比较器时**跳过该列并告警一次**，而不是报错。
//
//	报错会让整轮快照写失败 —— 那比「索引胖 22%」严重得多（丢数据 > 索引胖）。
var snapshotIndexColumnCmp = map[string]func(a, b Row) int{
	"snapshot_ts":    func(a, b Row) int { return a.SnapshotTS.Compare(b.SnapshotTS) },
	"tenant_id":      func(a, b Row) int { return strings.Compare(a.TenantID, b.TenantID) },
	"credential_id":  func(a, b Row) int { return cmp.Compare(a.CredentialID, b.CredentialID) },
	"raw_model_name": func(a, b Row) int { return strings.Compare(a.RawModel, b.RawModel) },
}

// snapshotIndexSortWarnOnce 让「缺比较器」只告警一次（每进程），
// 免得每轮刷屏把真正的告警淹掉。门是硬约束，这里只是运行时的可见性。
var snapshotIndexSortWarnOnce sync.Once

// sortRowsForIndexKey 按唯一约束的列序排序，返回**新切片**（不改调用方的）。
//
// ★ 为什么必须排序（§143.11 的全部依据）：
//
//	采集顺序 = Redis SCAN 迭代顺序（Collect 里 `w.rdb.Scan(...).Iterator()`），
//	而**同一批的所有行共享同一个 snapshot_ts**（Collect 注释：「所有行使用同一个
//	快照时间戳，保证原子性」）⇒ 整批行落进 btree 的**同一个窄键区间**，
//	而区间内部是随机的。
//	实测（252 生产）：每 20~40 秒一批、每批 1,881 行，
//	pkey 的 `avg_leaf_density` 只有 **70.25%**（叶页空着 30%）。
//	本地沙箱判定实验：同样数据**随机序**插入 ⇒ 密度 **70.20%**；
//	**键序**插入 ⇒ 密度 **89.82%**，索引体积少 **21.98%**。
//	⇒ 而工具基线里实测的「自然填充率」是 **90.20%**（REINDEX = 排序建）
//	⇒ 也就是说：**这 30% 是可回收的，不是自然填充率**。
//
// ★ 为什么不改调用方的切片：Flush 的日志会打 `rows[0]` 作为「第一行样本」，
//
//	那是**采集顺序**的样本，排序不该改变它。
func sortRowsForIndexKey(rows []Row) []Row {
	cols := snapshotConflictColumns()
	out := make([]Row, len(rows))
	copy(out, rows)
	if len(cols) == 0 {
		snapshotIndexSortWarnOnce.Do(func() {
			slog.Warn("ursm.v2.persist: 解析不出 ON CONFLICT 列，索引排序退化（密度会掉回随机序）")
		})
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		for _, c := range cols {
			cmpFn, ok := snapshotIndexColumnCmp[c]
			if !ok {
				snapshotIndexSortWarnOnce.Do(func() {
					slog.Warn("ursm.v2.persist: ON CONFLICT 列缺比较器，索引排序退化",
						"column", c)
				})
				continue
			}
			if v := cmpFn(out[i], out[j]); v != 0 {
				return v < 0
			}
		}
		return false
	})
	return out
}

// snapshotRowExecutor 是逐行 INSERT 需要的最小接口。
//
// ★ 抽它不是为了「好测」，而是让**插入顺序**变成可观测的事实 ——
//
//	§143.11 的全部收益都来自「批内行序 = 键序」，而它此前**没有任何观测点**。
//	与 Writer.readHashes 那条 seam 同一个理由（仓里已有的范式）。
type snapshotRowExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertSnapshotRows 按**唯一约束的列序**把这些行插进去。
//
// ★ 排序放在这里（而不是调用方），是为了让「插入顺序」这个性质
//
//	落在同一个函数里 ⇒ 判据直接盯它，变异（去掉排序）也会被门抓到。
//
// ⚠ 2026-07-22: 使用 ::text::jsonb cast 避免 22P02 错误
//
//	pgx 的二进制协议传输 []byte 到 JSONB 列时，若含非 UTF-8 字符、
//	NaN/Inf 或其他非法 JSON 片段会触发 "invalid input syntax for type json"。
//	转为 string 并使用 text→jsonb 类型转换可强制 PG 的 JSON 解析器处理。
//	匹配 domains/analysis/bus/publisher.go:86 和 telemetry/client.go:572 的模式。
//	★ NULL 经该 cast 仍是 NULL，不会变成 ''。
//
// ★ 参数名刻意叫 `tx`（生产路径传的是 pgx.Tx，测试传的是假执行器）：
//
//	仓里既有的门 writer_argorder_test.go 是**源码文本**判据，
//	它按 `tx.Exec(ctx, …)` 抽取实参清单 ⇒ 参数改名会让那道门失配。
//	⇒ 保持这个名字，是为了让既有门继续盯着这段实参（它守的是
//	「实参顺序错位 ⇒ PG 静默把值写进错误的列」）。
func insertSnapshotRows(ctx context.Context, tx snapshotRowExecutor, rows []Row) error {
	ordered := sortRowsForIndexKey(rows)
	for _, r := range ordered {
		payloadArg := payloadArgFor(r)
		_, err := tx.Exec(ctx, snapshotInsertSQL,
			r.SnapshotTS, r.RecoveryEpoch, r.ProviderID, r.CredentialID, r.RawModel,
			r.CanonicalName, r.TenantID, r.Available, r.HealthStatus, r.FailStreak, r.CoolUntil,
			r.SR1m, r.SR5m, r.SR30m, r.Samples1m, r.Samples5m, r.Samples30m,
			r.LatP50Ms, r.Score, r.SourcePriority, r.Generation,
			payloadArg,
			r.UpdatedAtMS, r.LastProbeAtMS, r.LastProbeLatencyMS, r.LastAttemptMS,
			r.LastOKMS, r.LastRequestAtMS, r.LastRequestErrorAtMS, r.ManualAtMS,
			r.CoolUntilMS, r.EventSeq,
			r.Disabled, r.LastDirectOK, r.ManualHold,
			r.SuccessCount, r.FailureCount, r.DisableCount,
			r.LatEWMAMS, r.EmptyResponseRate1m, r.EmptyResponseRate5m, r.EmptyResponseRate30m,
			r.LastErr, r.ManualReason, r.ManualActor, r.DisabledReason, r.CoolReason,
		)
		if err != nil {
			return fmt.Errorf("insert row cid=%d model=%s: %w", r.CredentialID, r.RawModel, err)
		}
	}
	return nil
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

	if err := insertSnapshotRows(ctx, tx, rows); err != nil {
		return err
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

// collectHashBatchSize is the number of node keys read per pipeline in
// Collect. Two round trips per chunk (one TYPE pipeline, one HGETALL
// pipeline), so at ~1,800 keys and the default 256 this is ~14 round trips
// instead of ~3,600.
//
// Overridable for operators who want to trade reply size against round trips;
// a non-positive or unparseable value keeps the default so a typo can never
// turn into an unbounded batch (SafeHGetAllBatch normalises it anyway, but
// the default is what the round-trip arithmetic above assumes).
func collectHashBatchSize() int {
	if v, ok := redissafe.BatchSizeFromEnv("URSM_V2_PERSIST_HASH_BATCH_SIZE"); ok {
		return v
	}
	return redissafe.DefaultHashBatchSize
}
