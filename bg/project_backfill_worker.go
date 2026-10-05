package bg

// project_backfill_worker.go — 2026-09-30 (审计三十三轮 Track C / R32-P-3):
// session_summaries.gw_project_id 存量回填 worker（迁移 762 的批量半边）。
//
// 链路分工（口径单一：全部经 SQL 函数 sync_session_project_attr，762 定义）：
//
//   - 新会话：session_dim UPSERT 触发 trg_session_dim_project_attr_ins/_upd
//     （762），请求终态自动同步，本 worker 不参与。
//   - 存量：本 worker 扫 session_summaries.gw_project_id IS NULL 且能 join 上
//     session_dim 的行（本地实测 330,423 行 NULL、96% 可 join），分批调用
//     sync_session_project_attr 完成回填 + 维度 upsert + attribution 留痕。
//     join 不上的行（~13k，无 session_dim 记录）保持 NULL，随窗口滚动
//     自然衰减，不阻塞批循环。
//
// 节奏与守护（对齐 session_summaries_trimmer）：
//   - 单批 ≤2000 行、单 tick ≤ maxBatchesPerTick 批（settings
//     lifecycle.session_project_backfill_batches，默认 50，热加载）——
//     10 万行/tick 上限，33 万存量约 4 个 tick 排空，无长锁。
//   - tick 10m；启动先跑一轮（drain 存量）。
//   - 幂等：UPDATE 谓词本身幂等（NULL 填充 / app:* 只被权威值升级），
//     多实例并发双跑无害。
//
// Pattern follows bg/session_summaries_trimmer.go (sync.Once idempotent Stop,
// chan struct{} lifecycle). 挂载点：cmd/gateway/main.go（与其它 bg worker
// 同区块，见移交项）。

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const (
	// projectBackfillBatchSize bounds each batch (same ≤5000-row audit
	// mandate family; 2000 keeps each tick's lock footprint small).
	projectBackfillBatchSize = 2000

	// defaultProjectBackfillMaxBatches caps one run; settings key
	// lifecycle.session_project_backfill_batches overrides (≥1 floor).
	defaultProjectBackfillMaxBatches = 50

	// defaultProjectBackfillTick is the cadence. Short enough that the
	// 330k-row backlog drains in hours, long enough to be idle-cheap.
	defaultProjectBackfillTick = 10 * time.Minute
)

// sessionProjectBackfillSQL — one batch: pick NULL-project rows joinable to
// session_dim, run the shared sync function per row inside a single
// statement. ORDER BY session_key keeps batch boundaries deterministic.
//
// ★ 2026-10-06 增补可解析性谓词（实测停摆，见 runbook §10.38）：
// 「能 join 上 session_dim」不等于「能回填」。project_id 与 application_code
// 双 NULL 的行照样进窗口，而 sync_session_project_attr 对它们返回 0（ref 解不出
// 来，行保持 NULL）。这类行不会被消费掉，下一批又会被 ORDER BY session_key
// 选回来 —— 窗口被永久占死。
// 生产实测：排序集前 5,745 行全部双 NULL，而 LIMIT 是 2,000，即
// first_resolvable_rank(5746) > LIMIT(2000)；154 日志连续 10 次
// backfilled=0（每次空烧 6~28s CPU），而窗口外还压着 141,797 行可回填。
// 谓词把「注定改变不了自己的行」挡在窗口外，n==0 才重新等价于「已排空」。
const sessionProjectBackfillSQL = `
WITH targets AS (
    SELECT ss.session_key, ss.tenant_id, sd.project_id, sd.application_code
    FROM session_summaries ss
    JOIN session_dim sd
      ON sd.gw_session_id = ss.session_key
     AND sd.tenant_id IS NOT DISTINCT FROM ss.tenant_id
    WHERE ss.gw_project_id IS NULL
      AND public.gw_resolve_project_ref(sd.project_id, sd.application_code) IS NOT NULL
    ORDER BY ss.session_key
    LIMIT $1
)
SELECT public.sync_session_project_attr(
           t.tenant_id, t.session_key, t.project_id, t.application_code)
FROM targets t
`

// SessionProjectBackfillWorker periodically backfills
// session_summaries.gw_project_id for pre-762 sessions.
type SessionProjectBackfillWorker struct {
	pool     *pgxpool.Pool
	tick     time.Duration
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// NewSessionProjectBackfillWorker constructs the worker with the default
// 10-minute tick. A nil pool yields a no-op worker (装配端免 nil 分支).
func NewSessionProjectBackfillWorker(pool *pgxpool.Pool) *SessionProjectBackfillWorker {
	return &SessionProjectBackfillWorker{
		pool: pool,
		tick: defaultProjectBackfillTick,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Start spawns the background goroutine. Returns immediately. An initial
// pass runs on startup so a fresh deploy drains the 762-era backlog without
// waiting a full tick.
func (w *SessionProjectBackfillWorker) Start(ctx context.Context) {
	Go("project_backfill_worker.run", func() { w.run(ctx) })
	slog.Info("session project backfill worker started",
		"interval", w.tick.String(),
		"batch_size", projectBackfillBatchSize)
}

// Stop terminates the goroutine. Safe on a never-Started worker and safe to
// call multiple times (sync.Once), mirroring the trimmer lifecycle.
func (w *SessionProjectBackfillWorker) Stop() {
	if w == nil || w.stop == nil || w.done == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stop)
	})
	select {
	case <-w.done:
	default:
		// goroutine never started
	}
}

// BackfillOnce drains up to maxBatches batches. Returns the number of
// session_summaries rows that gained a project ref. Errors abort the current
// run (next tick retries) — best-effort, non-critical.
func (w *SessionProjectBackfillWorker) BackfillOnce(ctx context.Context) (int64, error) {
	if w == nil || w.pool == nil {
		return 0, nil
	}
	maxBatches := settings.GetPlatformInt(
		"lifecycle.session_project_backfill_batches", defaultProjectBackfillMaxBatches)
	if maxBatches < 1 {
		maxBatches = defaultProjectBackfillMaxBatches // safety floor — never 0
	}

	start := time.Now()
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		rows, err := w.pool.Query(ctx, sessionProjectBackfillSQL, projectBackfillBatchSize)
		if err != nil {
			slog.Error("session project backfill: batch query failed",
				"error", err, "backfilled_before_failure", total)
			return total, err
		}
		n := int64(0)
		for rows.Next() {
			var affected int
			if err := rows.Scan(&affected); err != nil {
				rows.Close()
				return total, err
			}
			n += int64(affected)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			slog.Error("session project backfill: batch rows error",
				"error", err, "backfilled_before_failure", total)
			return total, err
		}
		total += n
		// A batch that synced nothing means either the NULL set is drained
		// or the remainder is unjoinable (no session_dim row) — stop either
		// way; unjoinable rows are not an error condition.
		if n == 0 {
			break
		}
	}
	slog.Info("session project backfill: pass complete",
		"backfilled", total, "duration_ms", time.Since(start).Milliseconds())
	return total, nil
}

func (w *SessionProjectBackfillWorker) run(ctx context.Context) {
	defer close(w.done)

	// Initial pass on startup (drain pre-762 backlog).
	//nolint:errcheck // best-effort backfill, non-critical
	w.BackfillOnce(ctx)

	tk := time.NewTicker(w.tick)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-tk.C:
			//nolint:errcheck // best-effort backfill, non-critical
			w.BackfillOnce(ctx)
		}
	}
}
