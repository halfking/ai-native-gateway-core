package licensing

import (
	"context"
	"log/slog"
	"time"
)

// StartTokenRefreshDaemon 启动 token 自动续期守护进程。
// 首次延迟 1 小时（避免启动风暴），之后每 interval 执行一次刷新。
// 守护进程可通过 context 取消。
func StartTokenRefreshDaemon(
	ctx context.Context,
	masterURL string,
	refreshTokenPath string,
	instanceTokenPath string,
	interval time.Duration,
) {
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
	runRefresh(ctx, masterURL, refreshTokenPath, instanceTokenPath)

	for {
		select {
		case <-ctx.Done():
			slog.Info("token_refresh_daemon: stopped")
			return
		case <-ticker.C:
			runRefresh(ctx, masterURL, refreshTokenPath, instanceTokenPath)
		}
	}
}

// runRefresh 执行一次刷新，失败时记录错误但不中断守护进程
func runRefresh(ctx context.Context, masterURL, refreshTokenPath, instanceTokenPath string) {
	slog.Info("token_refresh_daemon: starting refresh cycle")

	if err := AutoRefreshToken(ctx, masterURL, refreshTokenPath, instanceTokenPath); err != nil {
		slog.Error("token_refresh_daemon: refresh failed",
			"error", err,
			"hint", "current instance_token remains valid for up to 7 days")
		return
	}

	slog.Info("token_refresh_daemon: refresh cycle completed successfully")
}
