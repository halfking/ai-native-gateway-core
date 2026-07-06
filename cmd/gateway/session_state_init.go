// Package main — session_state_init.go
//
// Session State Management 依赖注入辅助函数
// 用于在 main.go 中初始化 session 相关的 workers 和 hooks
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

// wrapRedisClient 从已有的 redis.Client 创建 session.RedisClient
// 注意：这是临时方案，理想情况应该在 session 包中添加 NewRedisClientFromExisting
func wrapRedisClient(client *redis.Client) *session.RedisClient {
	if client == nil {
		return nil
	}
	// 获取配置信息
	opts := client.Options()
	// 使用相同配置创建新的 RedisClient
	return session.NewRedisClient(opts.Addr, opts.Password, opts.DB)
}

// SessionStateComponents 会话状态管理组件集合
type SessionStateComponents struct {
	Manager       *session.Manager
	DBWriter      *session.DBWriter
	CleanupWorker *session.CleanupWorker
	RotationHook  *session.RotationHook
	SessionPref   *session.SessionPreference
}

// InitializeSessionState 初始化会话状态管理组件
//
// 参数：
//   - ctx: 根上下文（用于启动 workers）
//   - pgPool: PostgreSQL 连接池
//   - redisClient: Redis 客户端（用于 session 和 session_pref）
//   - adminHandler: Admin Handler（用于注入依赖）
//
// 返回：
//   - *SessionStateComponents: 组件集合（需要在 shutdown 时清理）
//   - error: 初始化错误
//
// 使用示例（在 main.go 中）：
//
//	if dbConn != nil && fpSlotRedis != nil && adminHandler != nil {
//	    sessionComponents, err := InitializeSessionState(
//	        context.Background(),
//	        dbConn.Pool(),
//	        fpSlotRedis,
//	        adminHandler,
//	    )
//	    if err != nil {
//	        slog.Error("session state init failed", "error", err)
//	    } else {
//	        slog.Info("session state management initialized")
//	        // 注册 shutdown hook
//	        defer sessionComponents.Shutdown()
//	    }
//	}
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

	// 1. 创建 RedisClient wrapper
	sessionRedis := &session.RedisClient{}
	// 注意：RedisClient.client 是私有字段，需要通过构造函数或在 session 包中添加 setter
	// 这里我们直接用已有的 redis.Client 创建一个新的连接（如果配置相同）
	// 或者可以修改 session.RedisClient 添加 SetClient 方法
	// 临时方案：使用相同的配置重新创建（假设 fpSlotRedis 的配置可用）
	// 理想方案：添加 session.NewRedisClientFromExisting(redisClient)

	// 由于 RedisClient 结构不支持外部注入，这里创建一个包装函数
	sessionRedis = wrapRedisClient(redisClient)

	// 2. 创建 Manager
	sessionManager := session.NewManager(sessionRedis, 7*24*time.Hour)
	slog.Info("session manager created")

	// 3. 创建 SessionPreference
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

// Shutdown 清理所有 session state 组件
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

// 在 main.go 中的集成位置：
//
// 插入到 line 1699 之后（SetRedisClient 块结束后）：
//
//		}
//		slog.Info("CHECKPOINT: after SetRedisClient")
//
//		// 2026-07-06: Session State Management (Phase 1-5)
//		// 初始化会话状态管理组件（Manager, DBWriter, CleanupWorker, RotationHook）
//		if fpSlotRedis != nil && adminHandler != nil {
//			sessionComponents, err := InitializeSessionState(
//				context.Background(),
//				dbConn.Pool(),
//				fpSlotRedis,
//				adminHandler,
//			)
//			if err != nil {
//				slog.Error("session state init failed", "error", err)
//			} else if sessionComponents != nil {
//				slog.Info("session state management initialized")
//				// 注册到全局 shutdown（如果有的话）
//				// 或者在 main 函数末尾 defer sessionComponents.Shutdown()
//			}
//		}
//	}
//
//	slog.Info("CHECKPOINT: before memoraClient check")
