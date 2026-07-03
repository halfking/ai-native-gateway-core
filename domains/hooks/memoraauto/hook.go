package memoraauto

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// MemoraAutoHook Memora 自动沉淀 Hook
//
// 功能：
//   - 在 PhasePostResponse 阶段执行（不阻塞响应）
//   - 检测会话空闲状态
//   - 异步调用 kxmemory 进行数据沉淀
//   - 支持重试机制
type MemoraAutoHook struct {
	config         *Config
	idleDetector   *IdleDetector
	kxmemoryClient *KxmemoryClient
	retryManager   *RetryManager
	logger         *slog.Logger
}

// NewMemoraAutoHook 创建 Memora 自动沉淀 Hook
func NewMemoraAutoHook(config *Config, logger *slog.Logger) *MemoraAutoHook {
	if config == nil {
		config = DefaultConfig()
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &MemoraAutoHook{
		config:         config,
		idleDetector:   NewIdleDetector(config.IdleThreshold, config.MinRequestCount),
		kxmemoryClient: NewKxmemoryClient(config.KxmemoryURL, config.Timeout),
		retryManager:   NewRetryManager(config.MaxRetries, config.RetryBackoff),
		logger:         logger,
	}
}

// Name 返回 Hook 名称
func (h *MemoraAutoHook) Name() string {
	return "memora.auto"
}

// Priority 返回 Hook 优先级
// 在 PostResponse 阶段，优先级较低（200+），确保在其他关键 Hook 之后执行
func (h *MemoraAutoHook) Priority() int {
	return 200
}

// Enabled 判断 Hook 是否启用
func (h *MemoraAutoHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if !h.config.Enabled {
		return false
	}

	// 必须有会话信息
	if env == nil || env.SessionID == "" {
		return false
	}

	// 只在 PostResponse 阶段执行（由 Pipeline 控制）
	return true
}

// Execute 执行 Hook 逻辑
func (h *MemoraAutoHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 提取会话信息
	sessionKey := env.SessionID
	// TaskID 从 Metadata 中获取，如果没有则使用空字符串
	taskID := ""
	if env.Metadata != nil {
		if tid, ok := env.Metadata["task_id"].(string); ok {
			taskID = tid
		}
	}
	tenantID := env.TenantID

	if sessionKey == "" {
		return fmt.Errorf("session_key is required")
	}

	// 1. 先检查是否空闲（在 Track 之前，避免更新 LastActive）
	isIdle, stats, err := h.idleDetector.CheckIdle(ctx, sessionKey)
	if err != nil {
		// 会话不存在，说明是第一次，需要先 Track
		if err := h.idleDetector.Track(ctx, sessionKey, taskID, tenantID); err != nil {
			h.logger.Error("failed to track session",
				"session_key", sessionKey,
				"error", err)
			return err
		}
		// 第一次肯定不会空闲
		return nil
	}

	if isIdle {
		// 会话空闲，异步发送到 kxmemory
		h.logger.Info("session idle detected, starting ingest",
			"session_key", sessionKey,
			"task_id", taskID,
			"request_count", stats.RequestCount)

		// 在 goroutine 中异步执行，不阻塞响应
		go h.ingestSessionAsync(context.Background(), stats)

		// 空闲后不再跟踪新请求（已经开始沉淀流程）
		return nil
	}

	// 2. 未空闲，继续跟踪会话活动
	if err := h.idleDetector.Track(ctx, sessionKey, taskID, tenantID); err != nil {
		h.logger.Error("failed to track session",
			"session_key", sessionKey,
			"error", err)
		return err
	}

	h.logger.Debug("session not idle yet",
		"session_key", sessionKey,
		"request_count", stats.RequestCount,
		"last_active", stats.LastActive)

	return nil
}

// OnError 错误处理
// Memora 自动沉淀失败不应该影响主流程，因此吞掉错误
func (h *MemoraAutoHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	h.logger.Error("memora auto hook error",
		"session_id", env.SessionID,
		"error", err)

	// 记录到 metadata（可选）
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata["memora_auto_error"] = err.Error()

	// 吞掉错误，不影响主流程
	return nil
}

// ingestSessionAsync 异步发送会话数据到 kxmemory（带重试）
func (h *MemoraAutoHook) ingestSessionAsync(ctx context.Context, stats *SessionStats) {
	// 构造请求
	req := &SessionIngestRequest{
		SessionKey: stats.SessionKey,
		TaskID:     stats.TaskID,
		TenantID:   stats.TenantID,
		Metadata: map[string]interface{}{
			"request_count": stats.RequestCount,
			"last_active":   stats.LastActive,
			"created_at":    stats.CreatedAt,
		},
	}

	// 使用重试管理器执行
	err := h.retryManager.Execute(ctx, func(ctx context.Context, attempt int) error {
		h.logger.Debug("attempting to ingest session",
			"session_key", stats.SessionKey,
			"attempt", attempt+1)

		resp, err := h.kxmemoryClient.IngestSession(ctx, req)
		if err != nil {
			h.logger.Warn("ingest attempt failed",
				"session_key", stats.SessionKey,
				"attempt", attempt+1,
				"error", err)
			return err
		}

		h.logger.Info("session ingested successfully",
			"session_key", stats.SessionKey,
			"job_id", resp.JobID,
			"attempt", attempt+1)

		return nil
	})

	if err != nil {
		h.logger.Error("failed to ingest session after all retries",
			"session_key", stats.SessionKey,
			"error", err)
		return
	}

	// 成功后，标记为已处理
	if err := h.idleDetector.MarkProcessed(ctx, stats.SessionKey); err != nil {
		h.logger.Warn("failed to mark session as processed",
			"session_key", stats.SessionKey,
			"error", err)
	}
}

// GetIdleDetector 返回空闲检测器（用于测试）
func (h *MemoraAutoHook) GetIdleDetector() *IdleDetector {
	return h.idleDetector
}

// 编译期接口断言
var _ pipeline.Hook = (*MemoraAutoHook)(nil)
