package licensing

import (
	"context"
	"log/slog"
	"time"
)

// StartTokenRefreshDaemon 启动 token 自动续期守护进程。
// 首次延迟 1 小时（避免启动风暴），之后每 interval 执行一次刷新。
// 守护进程可通过 context 取消。
//
// The daemon maintains a singleton DaemonHealth snapshot (see
// daemon_health.go) that ops can read via GetDaemonHealth() to
// observe cycle outcomes without coupling to this goroutine.
func StartTokenRefreshDaemon(
	ctx context.Context,
	masterURL string,
	refreshTokenPath string,
	instanceTokenPath string,
	interval time.Duration,
) {
	StartTokenRefreshDaemonWithConfig(
		ctx, masterURL, refreshTokenPath, instanceTokenPath, interval,
		DefaultBackoffConfig,
	)
}

// StartTokenRefreshDaemonWithConfig is the policy-aware variant. It
// lets callers (and tests) supply a tuned BackoffConfig without
// affecting the default production curve.
func StartTokenRefreshDaemonWithConfig(
	ctx context.Context,
	masterURL string,
	refreshTokenPath string,
	instanceTokenPath string,
	interval time.Duration,
	cfg BackoffConfig,
) {
	health := ensureHealthInit()
	health.StartedAt = time.Now().UTC()

	slog.Info("token_refresh_daemon: starting",
		"interval", interval.String(),
		"initial_delay", "1h")

	// 首次延迟 1 小时
	select {
	case <-ctx.Done():
		slog.Info("token_refresh_daemon: cancelled before first run")
		return
	case <-time.After(1 * time.Hour):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 立即执行第一次刷新（在 1 小时延迟后）
	runRefreshWithConfig(ctx, masterURL, refreshTokenPath, instanceTokenPath, cfg)

	for {
		select {
		case <-ctx.Done():
			slog.Info("token_refresh_daemon: stopped")
			return
		case <-ticker.C:
			runRefreshWithConfig(ctx, masterURL, refreshTokenPath, instanceTokenPath, cfg)
		}
	}
}

// runRefresh 执行一次刷新，失败时记录错误但不中断守护进程。
// Kept as a thin shim for backward compat with callers that don't
// pass a config.
func runRefresh(ctx context.Context, masterURL, refreshTokenPath, instanceTokenPath string) {
	runRefreshWithConfig(ctx, masterURL, refreshTokenPath, instanceTokenPath, DefaultBackoffConfig)
}

// runRefreshWithConfig is the policy-aware variant. After every
// cycle it updates the global DaemonHealth snapshot, so an ops
// dashboard scraping /metrics can surface per-cycle outcomes.
func runRefreshWithConfig(ctx context.Context, masterURL, refreshTokenPath, instanceTokenPath string, cfg BackoffConfig) {
	slog.Info("token_refresh_daemon: starting refresh cycle")

	if err := AutoRefreshTokenWithConfig(ctx, masterURL, refreshTokenPath, instanceTokenPath, cfg); err != nil {
		slog.Error("token_refresh_daemon: refresh failed",
			"error", err,
			"hint", "current instance_token remains valid for up to 7 days")
		GetDaemonHealth() // ensure singleton exists
		// Reach into the same singleton to update it.
		if h := getHealthSingleton(); h != nil {
			h.RecordFailure(err)
		}
		return
	}

	slog.Info("token_refresh_daemon: refresh cycle completed successfully")
	if h := getHealthSingleton(); h != nil {
		h.RecordSuccess()
	}
}

// getHealthSingleton exposes the package-level singleton without
// pulling its direct mutation into the public API. Used by
// runRefreshWithConfig to update the snapshot.
func getHealthSingleton() *DaemonHealth {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	return singleton
}
