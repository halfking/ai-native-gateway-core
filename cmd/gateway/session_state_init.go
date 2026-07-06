// Package main — session_state_init.go
//
// Session State Management 依赖注入辅助函数。
// 在 main.go 中调用 InitializeSessionState 完成全部组件初始化与注入。
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/redis/go-redis/v9"
)

// SessionStateComponents 会话状态管理组件集合
type SessionStateComponents struct {
	Manager       *session.Manager
	DBWriter      *session.DBWriter
	CleanupWorker *session.CleanupWorker
	RotationHook  *session.RotationHook
	SessionPref   *session.SessionPreference
}

// InitializeSessionState 初始化会话状态管理组件并注入到 adminHandler。
//
// 参数：
//   - ctx: 根上下文（用于启动后台 workers）
//   - pgPool: PostgreSQL 连接池
//   - redisClient: Redis 客户端（复用已有连接，不新建）
//   - adminHandler: Admin Handler（注入 Manager/DBWriter/CleanupWorker）
//
// 返回：
//   - *SessionStateComponents: 组件集合（需在 main 函数末尾 defer Shutdown）
//   - error: 初始化错误（非 nil 时应记录日志，不阻塞启动）
func InitializeSessionState(
	ctx context.Context,
	pgPool *pgxpool.Pool,
	redisClient *redis.Client,
	adminHandler *admin.Handler,
) (*SessionStateComponents, error) {
	if pgPool == nil || redisClient == nil {
		slog.Warn("session state init skipped: missing dependencies")
		return nil, nil
	}

	// 1. 复用已有 Redis 连接（不新建）
	sessionRedis := session.NewRedisClientFromClient(redisClient)

	// 2. 创建 Manager
	sessionManager := session.NewManager(sessionRedis, 7*24*time.Hour)
	slog.Info("session manager created")

	// 3. 创建 SessionPreference（凭据偏好缓存）
	sessionPref := session.NewSessionPreference(sessionRedis)
	slog.Info("session preference created")

	// 4. 创建 DBWriter（批量异步持久化）
	batchSize := 10
	flushInterval := 60 * time.Second
	sessionDBWriter := session.NewDBWriter(pgPool, batchSize, flushInterval)
	sessionDBWriter.Start(ctx)
	slog.Info("session db writer started", "batch_size", batchSize, "flush_interval", flushInterval)

	// 5. 创建 CleanupWorker（清理过期 stopped session）
	stoppedTTL := 30 * time.Minute
	scanInterval := 5 * time.Minute
	cleanupWorker := session.NewCleanupWorker(redisClient, stoppedTTL, scanInterval)
	cleanupWorker.Start(ctx)
	slog.Info("session cleanup worker started", "stopped_ttl", stoppedTTL, "scan_interval", scanInterval)

	// 6. 创建 RotationHook（凭据轮换检测）
	rotationHook := session.NewRotationHook(sessionManager, sessionPref)
	slog.Info("session rotation hook created")

	// 7. 注入到 adminHandler
	if adminHandler != nil {
		adminHandler.SetSessionManager(sessionManager)
		adminHandler.SetSessionDBWriter(sessionDBWriter)
		adminHandler.SetSessionCleanupWorker(cleanupWorker)
		slog.Info("session components wired into admin handler")
	}

	return &SessionStateComponents{
		Manager:       sessionManager,
		DBWriter:      sessionDBWriter,
		CleanupWorker: cleanupWorker,
		RotationHook:  rotationHook,
		SessionPref:   sessionPref,
	}, nil
}

// Shutdown 优雅停止所有 session state 后台 workers。
// 应在 main 函数中 defer sessionComponents.Shutdown()。
func (c *SessionStateComponents) Shutdown() {
	if c == nil {
		return
	}
	if c.DBWriter != nil {
		c.DBWriter.Stop()
		slog.Info("session db writer stopped")
	}
	if c.CleanupWorker != nil {
		c.CleanupWorker.Stop()
		slog.Info("session cleanup worker stopped")
	}
}
