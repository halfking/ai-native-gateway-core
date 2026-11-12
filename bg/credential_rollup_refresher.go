// Package bg — credential rollup refresher（迁移 963，2026-11-13 24h 审计轮路径③）。
//
// CredentialRollupRefresher 周期性把「全凭据 × 两臂计数」一次性聚合 UPSERT 进
// public.credential_total_requests_rollup（迁移 963），供
// /api/credentials/monitor-summary 的 total_requests 读面走 O(1) 预物化。
//
// # 为什么需要（第五轮主档 §八第 12 条实测留痕）
//
// 端点对每个凭据实时跑 session_turns_hot ∪ session_turns 两臂 COUNT——
// 252 库母表 1.4M 行，热凭据单次 ~12 s（Bitmap Heap Scan 回表十几万行：
// 「要数完」，不是「找不到」）⇒ 74 凭据 × 12 s 必越 15 s context ⇒
// 245/154 恒 500。任何同口径实时改法都过不了（GROUP BY 形态 23.4 s）。
// 唯一不触碰口径裁决面的路＝异步物化（口径零变化，新鲜度受刷新间隔约束）。
//
// # 契约
//
//   - 单语句 UPSERT：空表 → 全量行**一次可见**（无半刷新态；读面的
//     EXISTS 判定因此只有「全空=回落」与「非空=全量可信」两态）。
//   - credential_id 表达式复用 db.SessionFamilyCredentialIDExpr 单点常量
//     （与按凭据 source / 索引表达式 / 962 函数体同源 ⇒ 口径等价前提①
//     由单点承担）。
//   - 失败处理：transient DB 错误 log WARN + 下个间隔重试（读面回落
//     实时计数，端点行为与 963 之前一致）；表缺席（down 迁移过）同族。
//     不停 worker——963 的设计就是「回落语义自愈」，无需 operator 干预。
//   - 首跑在 goroutine 内异步执行（**不阻塞 boot**）：boot 后到首跑落地
//     之间读面走回落分支（252 真库实测单次聚合 17.3 s）。
//   - 间隔 LLM_GATEWAY_CRED_ROLLUP_INTERVAL（默认 30m）；
//     单次超时 LLM_GATEWAY_CRED_ROLLUP_TIMEOUT（默认 2m，实测 17.3 s 有余量）。
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/db"
)

const (
	defaultCredRollupInterval = 30 * time.Minute
	defaultCredRollupTimeout  = 2 * time.Minute
)

// CredentialRollupRefresher 周期刷新凭据请求数预物化表（迁移 963）。
type CredentialRollupRefresher struct {
	pool *pgxpool.Pool

	// Interval 是两次刷新的间隔。默认 30m（LLM_GATEWAY_CRED_ROLLUP_INTERVAL）。
	Interval time.Duration
	// Timeout 是单次 UPSERT 的 statement_timeout 上限。默认 2m。
	// （252 真库实测全凭据聚合 17.3 s @1.4M 行；上限留数据增长余量。）
	Timeout time.Duration

	// OnRefreshComplete 可选回调：每次成功刷新后带 (rows, duration) 调用。
	// 供测试与观测面使用。
	OnRefreshComplete func(rows int64, took time.Duration)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewCredentialRollupRefresher 构造刷新器。pool 为 nil 时 Start 直接跳过
// （lite/无 DB 形态没有预物化面，读面回落也只在 DB 读面上有意义）。
func NewCredentialRollupRefresher(pool *pgxpool.Pool) *CredentialRollupRefresher {
	return &CredentialRollupRefresher{
		pool:     pool,
		Interval: credRollupIntervalFromEnv(),
		Timeout:  credRollupTimeoutFromEnv(),
		done:     make(chan struct{}),
	}
}

func credRollupIntervalFromEnv() time.Duration {
	return credRollupDurationFromEnv("LLM_GATEWAY_CRED_ROLLUP_INTERVAL", defaultCredRollupInterval)
}

func credRollupTimeoutFromEnv() time.Duration {
	return credRollupDurationFromEnv("LLM_GATEWAY_CRED_ROLLUP_TIMEOUT", defaultCredRollupTimeout)
}

func credRollupDurationFromEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	slog.Warn("credential rollup refresher: invalid env value, using default",
		"key", key, "value", raw, "default", def.String())
	return def
}

// CredRollupUpsertSQL 返回单语句 UPSERT（迁移 963 读数源）。
// 聚合 SQL 来自 db.SessionFamilyCredentialCountAggregateSQL（口径单点）。
func CredRollupUpsertSQL() string {
	return "INSERT INTO public.credential_total_requests_rollup (credential_id, total_requests, refreshed_at) " +
		"SELECT agg.credential_id, agg.total_requests, now() FROM (" +
		db.SessionFamilyCredentialCountAggregateSQL() +
		") agg ON CONFLICT (credential_id) DO UPDATE SET " +
		"total_requests = EXCLUDED.total_requests, refreshed_at = EXCLUDED.refreshed_at"
}

// Start 启动后台刷新循环。goroutine 内先立刻首跑（异步，不阻塞 boot），
// 之后按 Interval 触发；Stop/ctx 取消即退出。
func (r *CredentialRollupRefresher) Start(ctx context.Context) {
	if r == nil || r.pool == nil {
		slog.Info("credential rollup refresher: nil pool, stays off (read face falls back to live counts)")
		if r != nil {
			close(r.done)
		}
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	Go("credential_rollup_refresher.run", func() { r.run(cctx) })
	slog.Info("credential rollup refresher started",
		"interval", r.Interval.String(),
		"timeout", r.Timeout.String(),
	)
}

// Stop 停止刷新循环（幂等）。
func (r *CredentialRollupRefresher) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-r.done
}

func (r *CredentialRollupRefresher) run(ctx context.Context) {
	defer close(r.done)
	// 首跑：立即（异步 goroutine 内）。boot 不等它；落地前读面回落。
	r.refreshOnce(ctx)
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshOnce(ctx)
		}
	}
}

// RefreshOnce 执行一次 UPSERT（带自管 statement_timeout 的事务）。
// 导出以供 realdb 测试与 admin 手动触发复用。
func (r *CredentialRollupRefresher) RefreshOnce(ctx context.Context) error {
	return r.refreshOnce(ctx)
}

func (r *CredentialRollupRefresher) refreshOnce(ctx context.Context) error {
	started := time.Now()
	rctx, cancel := context.WithTimeout(ctx, r.Timeout+10*time.Second)
	defer cancel()
	tx, err := r.pool.Begin(rctx)
	if err != nil {
		r.logFailure(err, started)
		return fmt.Errorf("credential rollup refresher: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(rctx) }() // 提交后再 Rollup 是 pgx 的 no-op
	// 单语句聚合对角色级 statement_timeout（30s）不设防会被生产数据量增长
	// 撞死（252 实测 17.3 s @1.4M 行）⇒ 事务内 SET LOCAL 自管上限。
	if _, err := tx.Exec(rctx, "SET LOCAL statement_timeout = '"+strconv.Itoa(int(r.Timeout/time.Millisecond))+"ms'"); err != nil {
		r.logFailure(err, started)
		return fmt.Errorf("credential rollup refresher: set timeout: %w", err)
	}
	ct, err := tx.Exec(rctx, CredRollupUpsertSQL())
	if err != nil {
		r.logFailure(err, started)
		return fmt.Errorf("credential rollup refresher: upsert: %w", err)
	}
	if err := tx.Commit(rctx); err != nil {
		r.logFailure(err, started)
		return fmt.Errorf("credential rollup refresher: commit: %w", err)
	}
	took := time.Since(started)
	rows := ct.RowsAffected()
	slog.Info("credential rollup refreshed",
		"rows", rows, "took", took.String(), "interval", r.Interval.String())
	if r.OnRefreshComplete != nil {
		r.OnRefreshComplete(rows, took)
	}
	return nil
}

func (r *CredentialRollupRefresher) logFailure(err error, started time.Time) {
	slog.Warn("credential rollup refresh failed (read face keeps falling back to live counts)",
		"error", err, "elapsed", time.Since(started).String(),
		"next_retry", r.Interval.String())
}
