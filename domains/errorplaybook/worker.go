package errorplaybook

// 后台 worker：错误摄取窗口 → 匹配手册 → 标准执行 → 未知分析 → 自动提炼。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/lib/pq"
)

// ErrorSource 是错误事件的摄取面（生产实现查 candidate_failure_logs_hot）。
type ErrorSource interface {
	// FetchWindow 返回 (since, until] 窗口内按 error_kind 聚合的事件
	// 与新的水位（窗口内最大 ts；无行时返回 since）。
	FetchWindow(ctx context.Context, since, until time.Time) ([]Event, time.Time, error)
}

// Worker 是手册自动化循环。
//
//	★ 复用 bg.SpawnLoop（panic 收口 + 指数退避监督）——PI 分析 worker 同款。
//	★ 配置每轮从 cfgFn 重读（settings 热加载即时生效）。
type Worker struct {
	engine      *Engine
	source      ErrorSource
	cfgFn       func() Config
	interval    time.Duration
	remediation *Remediation

	// watermark 初始 = 启动前 15 分钟（重启后有一个小回看窗口，不丢事件）。
	watermark time.Time
	// lastPrune 是上次留痕清理时刻（24h 节流；零值 ⇒ 首轮即清一次）。
	lastPrune time.Time
	// warnedNotProvisioned 防止表未建时每轮刷 WARN。
	warnedNotProvisioned bool
	// warnedDisabled 防止关闭态每轮刷 Info。
	warnedDisabled bool
}

// NewWorker 创建 worker。cfgFn 每轮调用（热加载）。
func NewWorker(engine *Engine, source ErrorSource, cfgFn func() Config, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Worker{
		engine:    engine,
		source:    source,
		cfgFn:     cfgFn,
		interval:  interval,
		watermark: time.Now().Add(-15 * time.Minute),
	}
}

// WithRemediation configures the optional templates before the worker starts.
func (w *Worker) WithRemediation(pilot *Remediation) *Worker {
	w.remediation = pilot
	return w
}

// Start 启动循环（幂等：worker 由装配侧调用一次）。
//
//	★ 2026-11-22 审计轮修正（P1-3）：此前 `w.runOnce(ctx)` 的**返回值被丢弃**，
//	而 bg.SpawnLoop 的监督只在 panic 时接管错误 ⇒ 摄取 SQL 每轮失败
//	（22P02 / 数组 Scan）时**日志里一行都没有**，`/stats` 的 last_run_at
//	也停在旧值 ⇒ 「灰度已开启却看不出来」。
//	⇒ 现在每轮失败都记 Error 级日志。
//
//	★ 2026-11-23 审计轮修正（自功能诞生即存在）：runFn 必须**自带循环** ——
//	bg.SpawnLoop 的监督器对 runFn 的「正常返回」语义是「有意退出、不重启」
//	（bg/base_worker.go supervise：只有 panic 才退避重启）。此前的函数体是
//	「一轮 → 睡一个间隔 → 返回」⇒ **每个进程生命周期只跑一轮**，只有部署或
//	重启时才会再跑。生产上（211）它被高频部署掩盖成「每 7–15 分钟一轮」
//	（轮次时间逐一对应镜像构建后 3–5 分钟）；部署稀疏时就静默数小时
//	（2026-11-23 09:35→14:26 CST 空了 4 小时 51 分）。
//	既有测试只直接调 RunOnce、从不经过 Start，所以一直没被发现。
func (w *Worker) Start(ctx context.Context) {
	bg.SpawnLoop(ctx, "error_playbook.worker", func(ctx context.Context) {
		for {
			if err := w.runOnce(ctx); err != nil {
				slog.Error("error_playbook: worker round failed", "error", err)
			}
			// sleep 尊重 ctx 取消（P3-16）：停机时最多滞留一个 interval（上限 1 小时）。
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.interval):
			}
		}
	})
}

// RunOnce 单轮处理（测试与手动触发入口）。
func (w *Worker) RunOnce(ctx context.Context) error {
	return w.runOnce(ctx)
}

func (w *Worker) runOnce(ctx context.Context) error {
	cfg := DefaultConfig()
	if w.cfgFn != nil {
		cfg = w.cfgFn()
	}
	if !cfg.Enabled {
		if !w.warnedDisabled {
			w.warnedDisabled = true
			slog.Info("error_playbook: worker disabled by configuration (error_playbook.enabled=false)")
		}
		return nil
	}
	w.warnedDisabled = false

	now := w.engine.nowFn()
	until := now.Add(-2 * time.Second) // 避开在途写入的边界行
	if w.remediation != nil {
		defer w.remediation.run(ctx, until, w.interval)
	}
	//	★ 2026-11-22 审计轮修正（P1-4）：水位线改由**已处理**分组自行推进，
	//	故这里不再采用 FetchWindow 返回的 maxTS（它覆盖全部 20 个分组 ⇒
	//	会把「本轮被 cap 挡下、未处理」的分组也一并消费掉 ⇒ 静默永久丢弃）。
	events, _, err := w.source.FetchWindow(ctx, w.watermark, until)
	if err != nil {
		if errors.Is(err, ErrNotProvisioned) {
			if !w.warnedNotProvisioned {
				w.warnedNotProvisioned = true
				slog.Warn("error_playbook: playbook tables not provisioned (migration 995); skipping rounds until deployed")
			}
			return nil
		}
		return fmt.Errorf("fetch error window failed: %w", err)
	}
	w.warnedNotProvisioned = false

	limit := maxInt(cfg.MaxEventsPerRound, 1)
	processed := 0
	// ★ 2026-11-22 审计轮修正（P1-4）：水位线**只按已处理分组推进**。
	//	此前水位线取的是 FetchWindow 对**全部** 20 个分组的 maxTS，而本轮只处理
	//	前 `limit`（默认 10）个 ⇒ 第 11~20 名分组的事件落在新水位线之前，
	//	**永不重读、无人处理、无留痕**（错误类型被静默永久丢弃）。
	//	⇒ 水位线改为「已处理分组的 lastTS 最大值」。
	processedWatermark := w.watermark
	for _, ev := range events {
		if processed >= limit {
			slog.Info("error_playbook: round event cap reached",
				"cap", limit, "total", len(events), "deferred", len(events)-processed)
			break
		}
		res := w.engine.HandleEvent(ctx, cfg, ev)
		processed++
		if ev.LastSeen.After(processedWatermark) {
			processedWatermark = ev.LastSeen
		}
		if res.Err != nil {
			if errors.Is(res.Err, ErrNotProvisioned) {
				w.warnedNotProvisioned = true
				return nil
			}
			slog.Warn("error_playbook: handle event failed",
				"error_type", ev.ErrorType, "matched", res.Matched, "error", res.Err)
		}
	}

	// ── 自动完善：标准执行失败的条目带失败上下文重新提炼 ──────────
	//	★ 审计轮（2026-11-21）补 HasLLM 守卫：RefineEntry 的内存 dedup 只在
	//	提炼**成功**后才置位 ⇒ LLM 未配置时（提炼必失败、dedup 永不置位）
	//	每个失败条目每轮都会空转一次并刷 WARN。提炼前提是有 LLM。
	if cfg.Enabled && cfg.AutoAnalyze && w.engine.HasLLM() {
		candidates, err := w.engine.store.ListRefineCandidates(ctx, cfg.RefineCooldown, 5)
		if err != nil && !errors.Is(err, ErrNotProvisioned) {
			slog.Warn("error_playbook: list refine candidates failed", "error", err)
		}
		for i := range candidates {
			entry := &candidates[i]
			if err := w.engine.RefineEntry(ctx, cfg, entry); err != nil {
				slog.Warn("error_playbook: refine entry failed",
					"entry", entry.ID, "error_type", entry.ErrorType, "error", err)
			}
		}
	}

	// ── 执行留痕保留期清理（审计轮 2026-11-21 新增）────────────────
	//	命中/跳过/待确认都会落一行留痕，错误风暴下表会持续增长 ⇒ 每 24h
	//	清一次超过保留期（默认 30 天）的旧行。清理失败只 WARN（下轮再试），
	//	绝不影响本轮的错误处理。
	if cfg.Enabled && time.Since(w.lastPrune) >= 24*time.Hour {
		if n, err := w.engine.store.PruneExecutions(ctx, playbookRetention); err != nil {
			if !errors.Is(err, ErrNotProvisioned) {
				slog.Warn("error_playbook: prune executions failed", "error", err)
			}
		} else if n > 0 {
			slog.Info("error_playbook: pruned old executions", "rows", n, "retention", playbookRetention.String())
		}
		w.lastPrune = time.Now()
	}

	// ── 水位线推进 ───────────────────────────────────────────────────
	//	★ 2026-11-22 审计轮修正（P1-4）：推进量取**已处理**分组的最晚 ts，
	//	不再取 FetchWindow 对全部 20 个分组算出的 maxTS。
	//	「被 cap 挡在门外、因此没处理」的分组其事件因此仍落在水位线之前
	//	⇒ 下一轮会被重新读到（配合 SQL 侧按 lastTS 升序取 ⇒ 被推迟的
	//	分组总是「更新」的那些 ⇒ 不会被水位线越过 ⇒ 不再被静默永久丢弃）。
	//	窗口内无事件时推进到 until：否则窗口只会单调变宽、每轮重复全量扫描。
	nextWatermark := processedWatermark
	if processed == 0 && len(events) == 0 {
		nextWatermark = until
	}
	if nextWatermark.After(w.watermark) {
		w.watermark = nextWatermark
	}
	w.engine.MarkRun(now)
	return nil
}

// playbookRetention 是执行留痕的保留期（审计轮 2026-11-21：30 天，
// 覆盖「事故复盘窗口」；错误风暴下 awaiting_approval/skipped 每轮各一行，
// 无保留期 ⇒ 表无限增长）。
const playbookRetention = 30 * 24 * time.Hour

// SQLSource 是 ErrorSource 的 SQL 实现（candidate_failure_logs_hot）。
//
//	★ 该表由基础 schema 建出；不在场的部署（罕见）⇒ ErrNotProvisioned
//	⇒ worker 跳过而不是崩。
type SQLSource struct {
	db *sql.DB
}

// NewSQLSource 创建摄取源。
func NewSQLSource(db *sql.DB) *SQLSource {
	return &SQLSource{db: db}
}

// FetchWindow implements ErrorSource.
//
//	按 error_kind 聚合：次数、首末时间、最多 3 条消息样本、供应商/模型
//	样本（DISTINCT 数组切片）。LIMIT 20 防单轮把极端错误风暴全部吞进来。
//
//	★ 2026-11-22 审计轮修正两处**致命**缺陷（此前摄取链在生产上从未跑通一帧）：
//	① `COALESCE(provider_id, '')` —— candidate_failure_logs_hot.provider_id 是
//	   **INT NOT NULL**（sql/migrations/startup/037_candidate_failure_logs.sql:28），
//	   PG 把 '' 强转 integer ⇒ 运行期 `22P02 invalid input syntax for type integer: ""`。
//	   ⇒ 改为 `provider_id::text`（该列 NOT NULL，COALESCE 本就无意义）。
//	② 三个数组列（text[]，OID 1009）用 `rows.Scan(&[]string)` 承装 ——
//	   生产用 pgx v5 stdlib 驱动（cmd/gateway/main.go 的 stdlib.OpenDB），
//	   该驱动对未特判的 OID 走 `default: var d string` 分支，把数组当**字符串**返回，
//	   database/sql 再塞进 *[]string ⇒ `unsupported Scan, storing driver.Value
//	   type string into type *[]string`。
//	   ⇒ 改用 `pq.Array(&x)`（仓内既有依赖与用法，见 domains/hooks/goal/store.go）。
//	   ⚠ 两个缺陷必须**一起**修：单修 ① 仍会在 ② 上失败。
func (s *SQLSource) FetchWindow(ctx context.Context, since, until time.Time) ([]Event, time.Time, error) {
	if s.db == nil {
		return nil, since, fmt.Errorf("error source: database not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			error_kind,
			COUNT(*) AS cnt,
			MIN(ts),
			MAX(ts),
			(array_agg(COALESCE(error_message, '') ORDER BY ts DESC))[1:3] AS samples,
			(array_agg(DISTINCT provider_id::text))[1:3] AS providers,
			(array_agg(DISTINCT COALESCE(raw_model_name, '')))[1:3] AS models
		FROM candidate_failure_logs_hot
		WHERE ts > $1 AND ts <= $2
		  AND error_kind IS NOT NULL AND error_kind <> ''
		GROUP BY error_kind
		ORDER BY MAX(ts) ASC
		LIMIT 20`, since, until)
	if err != nil {
		if isNotProvisioned(err) {
			return nil, since, ErrNotProvisioned
		}
		return nil, since, fmt.Errorf("query candidate failure window failed: %w", err)
	}
	defer rows.Close()

	out := []Event{}
	maxTS := since
	for rows.Next() {
		var (
			kind      string
			cnt       int64
			firstTS   time.Time
			lastTS    time.Time
			samples   []string
			providers []string
			models    []string
		)
		if err := rows.Scan(&kind, &cnt, &firstTS, &lastTS, pq.Array(&samples), pq.Array(&providers), pq.Array(&models)); err != nil {
			return nil, since, fmt.Errorf("scan failure window row failed: %w", err)
		}
		ev := Event{
			ErrorType:   kind,
			Occurrences: int(cnt),
			FirstSeen:   firstTS,
			LastSeen:    lastTS,
			Samples:     nonEmpty(samples),
		}
		if len(ev.Samples) > 0 {
			ev.Message = ev.Samples[0]
		}
		ev.Provider = strings.Join(nonEmpty(providers), ",")
		ev.Model = strings.Join(nonEmpty(models), ",")
		out = append(out, ev)
		if lastTS.After(maxTS) {
			maxTS = lastTS
		}
	}
	if err := rows.Err(); err != nil {
		return nil, since, fmt.Errorf("iterate failure window failed: %w", err)
	}
	return out, maxTS, nil
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, truncateRunes(s, 500))
		}
	}
	return out
}
