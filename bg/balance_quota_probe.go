package bg

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BalanceQuotaProbe 专门针对需要充值才能恢复的配额类型（balance_exhausted、permanently_exhausted）
// 进行更频繁的探测，以便及时感知用户充值后的恢复。
//
// 与 PeriodicQuotaProbe 的区别：
//   - PeriodicQuotaProbe: 针对周期性配额用尽（periodic_exhausted），默认 5 分钟探测一次
//   - BalanceQuotaProbe: 针对余额/永久配额用尽（balance_exhausted、permanently_exhausted），
//     默认 2 分钟探测一次，更快感知充值恢复
//
// 探测策略：
//  1. 选择 quota_state IN ('balance_exhausted', 'permanently_exhausted') 的活跃凭据
//  2. 通过 credential_probe_v2 进行实际探测（调用上游 API）
//  3. 如果探测成功，probe-v2 会自动清除 quota_state 并恢复 availability_state
//  4. 如果探测失败且仍然是配额错误，保持当前状态不变
type BalanceQuotaProbe struct {
	db             *pgxpool.Pool
	interval       time.Duration
	probeSubmitter func(credID int)
	stopCh         chan struct{}
	stopOnce       sync.Once
}

func NewBalanceQuotaProbe(db *pgxpool.Pool) *BalanceQuotaProbe {
	// 默认 2 分钟，比周期性配额探测（5 分钟）更频繁
	// 原因：余额充值是用户主动操作，需要更快感知恢复
	interval := 2 * time.Minute
	if v := os.Getenv("LLM_GATEWAY_BALANCE_QUOTA_PROBE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Minute
		}
	}
	return &BalanceQuotaProbe{
		db:       db,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

func (p *BalanceQuotaProbe) SetProbeSubmitter(fn func(credID int)) {
	p.probeSubmitter = fn
}

func (p *BalanceQuotaProbe) Start(ctx context.Context) {
	slog.Info("balance_quota_probe started",
		"interval", p.interval,
		"target_states", []string{"balance_exhausted", "permanently_exhausted"},
	)
	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("balance_quota_probe stopping")
				return
			case <-p.stopCh:
				slog.Info("balance_quota_probe stopped")
				return
			case <-ticker.C:
				if err := p.probeBalanceExhausted(ctx); err != nil {
					slog.Error("balance_quota_probe failed", "error", err)
				}
			}
		}
	}()
}

func (p *BalanceQuotaProbe) Stop() {
	p.stopOnce.Do(func() { close(p.stopCh) })
}

// probeBalanceExhausted 探测 balance_exhausted 和 permanently_exhausted 状态的凭据。
//
// 选择条件：
//   - quota_state IN ('balance_exhausted', 'permanently_exhausted')
//   - lifecycle_status = 'active'
//   - 凭据和 provider 都未被手动禁用
//   - provider 已启用
//   - 有配置 default_probe_model（探测需要指定模型）
//
// 探测结果：
//   - 成功：credential_probe_v2 会清除 quota_state='ok'，恢复 availability_state='ready'
//   - 失败（仍然配额错误）：保持当前状态
//   - 失败（其他错误）：可能转为其他错误状态（由 probe-v2 的分类逻辑决定）
func (p *BalanceQuotaProbe) probeBalanceExhausted(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rows, err := p.db.Query(timeoutCtx, `
		SELECT c.id
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.quota_state IN ('balance_exhausted', 'permanently_exhausted')
		  AND c.lifecycle_status = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND COALESCE(c.default_probe_model, '') <> ''
		ORDER BY c.id
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var credID int
		if err := rows.Scan(&credID); err != nil {
			slog.Warn("balance_quota_probe: scan failed", "error", err)
			continue
		}
		if p.probeSubmitter != nil {
			p.probeSubmitter(credID)
		}
		count++
	}
	if count > 0 {
		slog.Info("balance_quota_probe: submitted probes for balance/permanently exhausted credentials",
			"count", count,
			"interval", p.interval,
			"reason", "detect recharge recovery faster",
		)
	}
	return nil
}
