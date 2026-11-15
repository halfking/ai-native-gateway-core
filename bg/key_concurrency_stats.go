// Package bg — per-key 并发分钟统计写入器（迁移 965，2026-11-15 三点审计轮）。
//
// KeyConcurrencyStatsWriter 每 ~60s 从 streaming.KeyConcurrentGate 取一个
// 观测窗口的 per-key 样本（在飞/峰值/被拒/生效帽），UPSERT 进
// public.api_key_concurrency_minute——「每个 key 的实时并发数据按分钟落库」。
//
// # 设计要点
//
//   - 空窗口零写：无活动 key ⇒ 无行。零活动分钟不产生噪声行，表保持极小
//     （有显式帽的 key 全库目前仅个位数，常态窗口样本数 < 活跃 key 数）。
//   - 幂等 UPSERT：同分钟重复采样（重启追赶/tick 漂移）peak 取 GREATEST、
//     rejected 累加——两次窗口语义 = 覆盖区间的并集，不会丢峰值也不会丢拒绝。
//   - instance_id 进主键：蓝绿双槽并行期两实例各写各的行（进程内计数本就
//     per-instance；聚合读面按 (api_key_id, minute_ts) SUM 即得全域视图）。
//   - 失败语义：WARN + 下个窗口重试，不停 worker——观测面缺失一分钟不是
//     故障（并发闸执法完全不受影响）；表缺席（down 迁移）同族自愈。
//   - retention：每小时清一次超期行（默认 7 天，env 可调）——分钟级观测
//     数据没有长期价值，租户用量归 request_stats_minute 族管。
//
// 间隔 env：LLM_GATEWAY_KEY_CONCURRENCY_STATS_INTERVAL（默认 60s）；
// 保留期 env：LLM_GATEWAY_KEY_CONCURRENCY_RETENTION（默认 168h=7d）。
package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	defaultKeyConcurrencyStatsInterval = 60 * time.Second
	defaultKeyConcurrencyRetention     = 7 * 24 * time.Hour
	keyConcurrencyWriteTimeout         = 10 * time.Second
	// keyConcurrencyTrimEvery：每 N 个 tick 执行一次 retention 清理
	//（60s tick × 60 = 每小时一次）。
	keyConcurrencyTrimEvery = 60
)

// KeyConcurrencyDB 是写入器的最小 DB 依赖（*pgxpool.Pool 与
// pgxmock.PgxPoolIface 都满足——测试不依赖真池）。
type KeyConcurrencyDB interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// KeyConcurrencySample 是写入单元（streaming.KeyConcurrencySample 的 bg 侧
// 镜像——bg 不 import streaming，主装配在 cmd/gateway 做适配）。
type KeyConcurrencySample struct {
	KeyID        int
	InFlight     int
	PeakInFlight int
	Rejected     int
	Limit        int
}

// KeyConcurrencyStatsWriter 周期把 per-key 并发观测样本写进
// api_key_concurrency_minute（迁移 965）。
type KeyConcurrencyStatsWriter struct {
	pool       KeyConcurrencyDB
	snapshot   func() []KeyConcurrencySample
	instanceID string

	// Interval 是两次采样间隔（默认 60s）。
	Interval time.Duration
	// Retention 是行保留期（默认 7d），超期行由周期 trim 清除。
	Retention time.Duration

	// OnWriteComplete 可选回调：每次成功写入后带 (rows, duration) 调用。
	OnWriteComplete func(rows int64, took time.Duration)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewKeyConcurrencyStatsWriter 构造写入器。pool/snapshot 为 nil 时 Start
// 直接跳过（lite/无 DB 形态没有落库面）。instanceID 为空时写空串（单实例
// 形态仍唯一）。
func NewKeyConcurrencyStatsWriter(pool KeyConcurrencyDB, snapshot func() []KeyConcurrencySample, instanceID string) *KeyConcurrencyStatsWriter {
	return &KeyConcurrencyStatsWriter{
		pool:       pool,
		snapshot:   snapshot,
		instanceID: instanceID,
		Interval:   keyConcurrencyStatsIntervalFromEnv(),
		Retention:  keyConcurrencyRetentionFromEnv(),
		done:       make(chan struct{}),
	}
}

func keyConcurrencyStatsIntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_KEY_CONCURRENCY_STATS_INTERVAL"))
	if raw == "" {
		return defaultKeyConcurrencyStatsInterval
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	slog.Warn("key concurrency stats writer: invalid interval env, using default",
		"key", "LLM_GATEWAY_KEY_CONCURRENCY_STATS_INTERVAL", "value", raw,
		"default", defaultKeyConcurrencyStatsInterval.String())
	return defaultKeyConcurrencyStatsInterval
}

func keyConcurrencyRetentionFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_KEY_CONCURRENCY_RETENTION"))
	if raw == "" {
		return defaultKeyConcurrencyRetention
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	slog.Warn("key concurrency stats writer: invalid retention env, using default",
		"key", "LLM_GATEWAY_KEY_CONCURRENCY_RETENTION", "value", raw,
		"default", defaultKeyConcurrencyRetention.String())
	return defaultKeyConcurrencyRetention
}

// KeyConcurrencyUpsertSQL 单行 UPSERT（迁移 965 读数源）。
// peak 取 GREATEST、rejected 累加：同分钟两次窗口 = 覆盖区间并集。
func KeyConcurrencyUpsertSQL() string {
	return `INSERT INTO public.api_key_concurrency_minute
  (minute_ts, api_key_id, instance_id, in_flight, peak_in_flight, rejections, concurrent_limit, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (minute_ts, api_key_id, instance_id) DO UPDATE SET
  in_flight = EXCLUDED.in_flight,
  peak_in_flight = GREATEST(public.api_key_concurrency_minute.peak_in_flight, EXCLUDED.peak_in_flight),
  rejections = public.api_key_concurrency_minute.rejections + EXCLUDED.rejections,
  concurrent_limit = EXCLUDED.concurrent_limit,
  updated_at = now()`
}

// KeyConcurrencyTrimSQL retention 清理（每小时一次）。
func KeyConcurrencyTrimSQL() string {
	return `DELETE FROM public.api_key_concurrency_minute WHERE minute_ts < now() - $1::interval`
}

// Start 启动写入循环（goroutine 内首跑延迟一个 Interval——首个窗口需要先
// 积累观测数据，立即跑只会写空集）。重复 Start 由调用方避免（与
// CredentialRollupRefresher 同款装配纪律）。
func (w *KeyConcurrencyStatsWriter) Start(ctx context.Context) {
	if w == nil || w.pool == nil || w.snapshot == nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	if w.cancel != nil {
		// 已在运行：保持唯一循环（防御性，装配期一次 Start 是纪律）。
		w.mu.Unlock()
		cancel()
		return
	}
	w.cancel = cancel
	w.mu.Unlock()

	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.Interval)
		defer ticker.Stop()
		tick := 0
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			w.writeOnce(runCtx)
			tick++
			if tick%keyConcurrencyTrimEvery == 0 {
				w.trimOnce(runCtx)
			}
		}
	}()
}

// Stop 停止写入循环并等待退出（幂等）。
func (w *KeyConcurrencyStatsWriter) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-w.done
	}
}

func (w *KeyConcurrencyStatsWriter) writeOnce(ctx context.Context) {
	samples := w.snapshot()
	if len(samples) == 0 {
		return // 空窗口零写（设计要点①）
	}
	started := time.Now()
	wctx, cancel := context.WithTimeout(ctx, keyConcurrencyWriteTimeout)
	defer cancel()

	minuteTS := started.UTC().Truncate(time.Minute)
	batch := &pgx.Batch{}
	for _, s := range samples {
		if s.KeyID <= 0 {
			continue // 闸侧已守卫；写入侧再挡一道（脏样本绝不落库）
		}
		batch.Queue(KeyConcurrencyUpsertSQL(),
			minuteTS, s.KeyID, w.instanceID, s.InFlight, s.PeakInFlight, s.Rejected, s.Limit)
	}
	if batch.Len() == 0 {
		return
	}
	br := w.pool.SendBatch(wctx, batch)
	var written int64
	var firstErr error
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	_ = br.Close()
	if firstErr != nil {
		// 空缺一分钟观测数据不是故障（执法不受影响）；表缺席（down 迁移）
		// 同族——WARN 后自愈（下个窗口继续）。部分行成功也如实计数。
		slog.Warn("key concurrency stats: write failed",
			"error", firstErr, "rows_attempted", batch.Len(), "rows_written", written)
		return
	}
	if w.OnWriteComplete != nil {
		w.OnWriteComplete(written, time.Since(started))
	}
}

func (w *KeyConcurrencyStatsWriter) trimOnce(ctx context.Context) {
	wctx, cancel := context.WithTimeout(ctx, keyConcurrencyWriteTimeout)
	defer cancel()
	if _, err := w.pool.Exec(wctx, KeyConcurrencyTrimSQL(), w.Retention.String()); err != nil {
		slog.Warn("key concurrency stats: trim failed", "error", err)
	}
}
