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
	engine   *Engine
	source   ErrorSource
	cfgFn    func() Config
	interval time.Duration

	// watermark 初始 = 启动前 15 分钟（重启后有一个小回看窗口，不丢事件）。
	watermark time.Time
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

// Start 启动循环（幂等：worker 由装配侧调用一次）。
func (w *Worker) Start(ctx context.Context) {
	bg.SpawnLoop(ctx, "error_playbook.worker", func(ctx context.Context) {
		w.runOnce(ctx)
		time.Sleep(w.interval)
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
	events, watermark, err := w.source.FetchWindow(ctx, w.watermark, until)
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
	for _, ev := range events {
		if processed >= limit {
			slog.Info("error_playbook: round event cap reached", "cap", limit, "total", len(events))
			break
		}
		res := w.engine.HandleEvent(ctx, cfg, ev)
		processed++
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
	if cfg.Enabled && cfg.AutoAnalyze {
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

	if watermark.After(w.watermark) {
		w.watermark = watermark
	}
	w.engine.MarkRun(now)
	return nil
}

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
			(array_agg(error_message ORDER BY ts DESC))[1:3] AS samples,
			(array_agg(DISTINCT COALESCE(provider_id, '')))[1:3] AS providers,
			(array_agg(DISTINCT COALESCE(raw_model_name, '')))[1:3] AS models
		FROM candidate_failure_logs_hot
		WHERE ts > $1 AND ts <= $2
		  AND error_kind IS NOT NULL AND error_kind <> ''
		GROUP BY error_kind
		ORDER BY cnt DESC
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
		if err := rows.Scan(&kind, &cnt, &firstTS, &lastTS, &samples, &providers, &models); err != nil {
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
