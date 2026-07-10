// Package moduleexec 实现会话模块执行记录系统
//
// 核心功能：
//   - Check-Execute-Record 模式：执行前检查是否已有有效结果
//   - 自动记录每个模块的执行情况
//   - 支持 Redis L1 缓存 + 数据库 L2 缓存
//   - 批量查询优化
//
// 数据流：
//   1. 调用 CheckAndExecute(sessionID, moduleName, inputParams, ttl, executeFn)
//   2. 系统计算 cache_key，查询是否有有效缓存
//   3. 有缓存：直接返回（FromCache=true）
//   4. 无缓存：执行 executeFn，记录开始（status=running）
//   5. 执行完成后：记录结果（status=completed/skipped）
//
// 使用示例：
//
//	executor := moduleexec.NewExecutor(dbPool, redisClient)
//	result, err := executor.CheckAndExecute(
//	    ctx, sessionID, tenantID,
//	    moduleregistry.ModuleSessionAudit,
//	    map[string]interface{}{"content_hash": hash},
//	    3600, // TTL
//	    func(ctx context.Context) (*moduleexec.ExecuteResult, error) {
//	        // 实际业务逻辑
//	        return &moduleexec.ExecuteResult{...}, nil
//	    },
//	)
package moduleexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/moduleregistry"
	"github.com/redis/go-redis/v9"
)

// ExecuteStatus 执行状态
type ExecuteStatus string

const (
	StatusPending   ExecuteStatus = "pending"   // 待执行
	StatusRunning   ExecuteStatus = "running"   // 执行中
	StatusCompleted ExecuteStatus = "completed" // 执行成功
	StatusFailed    ExecuteStatus = "failed"    // 执行失败
	StatusSkipped   ExecuteStatus = "skipped"   // 已跳过（有有效缓存）
)

// ExecuteResult 模块执行结果
type ExecuteResult struct {
	ExecutionID   int64                  `json:"execution_id"`
	Status        ExecuteStatus          `json:"status"`
	ResultSummary map[string]interface{} `json:"result_summary,omitempty"`
	ResultDetail  map[string]interface{} `json:"result_detail,omitempty"`
	ErrorMessage  string                 `json:"error_message,omitempty"`
	FromCache     bool                   `json:"from_cache"`
	DurationMs    int                    `json:"duration_ms"`
	ExpiresAt     *time.Time             `json:"expires_at,omitempty"`
}

// Executor 模块执行器
type Executor struct {
	db          DBTX
	redis       *redis.Client
	logger      *slog.Logger
	enableRedis bool

	// 内存 L0 缓存（极短 TTL，用于超高频请求）
	memCache     map[string]*ExecuteResult
	memCacheTTL  time.Duration
	memCacheTime map[string]time.Time
	memCacheMu   sync.RWMutex
}

// Config 执行器配置
type Config struct {
	DB              DBTX
	Redis           *redis.Client // 可选
	Logger          *slog.Logger
	EnableRedis     bool
	MemCacheTTL     time.Duration // 内存缓存 TTL，默认 30 秒
}

// NewExecutor 创建执行器
func NewExecutor(cfg Config) *Executor {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MemCacheTTL == 0 {
		cfg.MemCacheTTL = 30 * time.Second
	}

	return &Executor{
		db:            cfg.DB,
		redis:         cfg.Redis,
		logger:        cfg.Logger,
		enableRedis:   cfg.EnableRedis && cfg.Redis != nil,
		memCache:      make(map[string]*ExecuteResult),
		memCacheTime:  make(map[string]time.Time),
		memCacheTTL:   cfg.MemCacheTTL,
	}
}

// CheckAndExecute 检查并执行模块逻辑
//
// 参数：
//   - ctx: 上下文
//   - sessionID: 会话 ID
//   - tenantID: 租户 ID
//   - moduleName: 模块名称（必须使用 moduleregistry 中的常量）
//   - inputParams: 输入参数（用于计算 cache_key）
//   - ttlSeconds: 结果有效期（秒），0 表示使用模块默认值
//   - executeFn: 实际执行函数
//
// 返回：
//   - *ExecuteResult: 执行结果（包含 FromCache 标识）
//   - error: 错误
func (e *Executor) CheckAndExecute(
	ctx context.Context,
	sessionID string,
	tenantID string,
	moduleName string,
	inputParams map[string]interface{},
	ttlSeconds int,
	executeFn func(context.Context) (*ExecuteResult, error),
) (*ExecuteResult, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("sessionID cannot be empty")
	}
	if moduleName == "" {
		return nil, fmt.Errorf("moduleName cannot be empty")
	}

	// 获取模块信息
	moduleInfo, ok := moduleregistry.GetModuleInfo(moduleName)
	if !ok {
		return nil, fmt.Errorf("unknown module: %s", moduleName)
	}

	// 使用默认 TTL
	if ttlSeconds <= 0 {
		ttlSeconds = moduleInfo.TTLSeconds
	}

	// 计算 cache_key
	cacheKey := ComputeCacheKey(moduleName, inputParams)

	// L0: 内存缓存
	if cached := e.getFromMemCache(sessionID, moduleName, cacheKey); cached != nil {
		cached.FromCache = true
		recordCacheHit(moduleName, "L0")
		recordExecution(moduleName, string(cached.Status), true, 0)
		return cached, nil
	}

	// L1: Redis 缓存
	if e.enableRedis {
		if cached := e.getFromRedis(ctx, sessionID, moduleName, cacheKey); cached != nil {
			cached.FromCache = true
			e.setToMemCache(sessionID, moduleName, cacheKey, cached)
			recordCacheHit(moduleName, "L1")
			recordExecution(moduleName, string(cached.Status), true, 0)
			return cached, nil
		}
	}

	// L2: 数据库查询
	if cached, err := e.getFromDB(ctx, sessionID, moduleName, cacheKey); err == nil && cached != nil {
		cached.FromCache = true
		if e.enableRedis {
			e.setToRedis(ctx, sessionID, moduleName, cacheKey, cached, ttlSeconds)
		}
		e.setToMemCache(sessionID, moduleName, cacheKey, cached)
		recordCacheHit(moduleName, "L2")
		recordExecution(moduleName, string(cached.Status), true, 0)
		return cached, nil
	}

	// 没有有效缓存
	recordCacheMiss(moduleName)

	// 没有有效缓存，记录执行开始并执行
	execID, err := e.recordExecutionStart(ctx, sessionID, tenantID, moduleName, cacheKey, ttlSeconds)
	if err != nil {
		e.logger.Warn("record execution start failed", "error", err, "module", moduleName, "session_id", sessionID)
		// 记录失败不影响主流程，直接执行
		return e.executeDirectly(ctx, sessionID, tenantID, moduleName, executeFn)
	}

	// 执行实际逻辑
	startTime := time.Now()
	result, execErr := executeFn(ctx)
	durationMs := int(time.Since(startTime).Milliseconds())
	execDuration := time.Since(startTime)

	if execErr != nil {
		// 记录失败
		_ = e.recordExecutionFailure(ctx, execID, execErr.Error(), durationMs)
		recordExecution(moduleName, "failed", false, execDuration)
		return nil, execErr
	}

	// 记录成功
	result.ExecutionID = execID
	result.DurationMs = durationMs
	result.Status = StatusCompleted
	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	result.ExpiresAt = &expiresAt

	if err := e.recordExecutionSuccess(ctx, execID, result, durationMs); err != nil {
		e.logger.Warn("record execution success failed", "error", err, "execution_id", execID)
	}

	recordExecution(moduleName, "completed", false, execDuration)

	// 写入缓存
	if e.enableRedis {
		e.setToRedis(ctx, sessionID, moduleName, cacheKey, result, ttlSeconds)
	}
	e.setToMemCache(sessionID, moduleName, cacheKey, result)

	return result, nil
}

// executeDirectly 直接执行（不记录到数据库，用于记录失败时的降级）
func (e *Executor) executeDirectly(
	ctx context.Context,
	sessionID, tenantID, moduleName string,
	executeFn func(context.Context) (*ExecuteResult, error),
) (*ExecuteResult, error) {
	startTime := time.Now()
	result, err := executeFn(ctx)
	execDuration := time.Since(startTime)
	durationMs := int(execDuration.Milliseconds())

	if err != nil {
		recordExecution(moduleName, "failed", false, execDuration)
		return nil, err
	}

	result.DurationMs = durationMs
	result.Status = StatusCompleted
	recordExecution(moduleName, "completed", false, execDuration)
	return result, nil
}

// ────────────────────────────────────────────────────────────────
// L0 内存缓存
// ────────────────────────────────────────────────────────────────

func (e *Executor) getFromMemCache(sessionID, moduleName, cacheKey string) *ExecuteResult {
	e.memCacheMu.RLock()
	defer e.memCacheMu.RUnlock()

	key := fmt.Sprintf("%s:%s:%s", sessionID, moduleName, cacheKey)
	cachedTime, exists := e.memCacheTime[key]
	if !exists {
		return nil
	}
	if time.Since(cachedTime) > e.memCacheTTL {
		return nil
	}
	return e.memCache[key]
}

func (e *Executor) setToMemCache(sessionID, moduleName, cacheKey string, result *ExecuteResult) {
	e.memCacheMu.Lock()
	defer e.memCacheMu.Unlock()

	key := fmt.Sprintf("%s:%s:%s", sessionID, moduleName, cacheKey)
	e.memCache[key] = result
	e.memCacheTime[key] = time.Now()
}

// ────────────────────────────────────────────────────────────────
// L1 Redis 缓存
// ────────────────────────────────────────────────────────────────

func (e *Executor) redisKey(sessionID, moduleName, cacheKey string) string {
	return fmt.Sprintf("module:exec:%s:%s:%s", sessionID, moduleName, cacheKey)
}

func (e *Executor) getFromRedis(ctx context.Context, sessionID, moduleName, cacheKey string) *ExecuteResult {
	if e.redis == nil {
		return nil
	}

	data, err := e.redis.Get(ctx, e.redisKey(sessionID, moduleName, cacheKey)).Bytes()
	if err != nil {
		return nil
	}

	var result ExecuteResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}

	return &result
}

func (e *Executor) setToRedis(ctx context.Context, sessionID, moduleName, cacheKey string, result *ExecuteResult, ttlSeconds int) {
	if e.redis == nil {
		return
	}

	data, err := json.Marshal(result)
	if err != nil {
		return
	}

	_ = e.redis.Set(ctx, e.redisKey(sessionID, moduleName, cacheKey), data, time.Duration(ttlSeconds)*time.Second).Err()
}

// ────────────────────────────────────────────────────────────────
// L2 数据库查询
// ────────────────────────────────────────────────────────────────

func (e *Executor) getFromDB(ctx context.Context, sessionID, moduleName, cacheKey string) (*ExecuteResult, error) {
	query := `
		SELECT execution_id, status, result_summary, result_detail, 
		       duration_ms, expires_at
		FROM session_module_executions_hot
		WHERE gw_session_id = $1
		  AND module_name = $2
		  AND cache_key = $3
		  AND status = 'completed'
		  AND expires_at > NOW()
		ORDER BY created_at DESC
		LIMIT 1
	`

	var (
		execID      int64
		status      string
		summaryJSON []byte
		detailJSON  []byte
		durationMs  int
		expiresAt   time.Time
	)

	err := e.db.QueryRow(ctx, query, sessionID, moduleName, cacheKey).
		Scan(&execID, &status, &summaryJSON, &detailJSON, &durationMs, &expiresAt)
	if err != nil {
		return nil, err
	}

	result := &ExecuteResult{
		ExecutionID: execID,
		Status:      ExecuteStatus(status),
		DurationMs:  durationMs,
		ExpiresAt:   &expiresAt,
	}

	if len(summaryJSON) > 0 {
		json.Unmarshal(summaryJSON, &result.ResultSummary)
	}
	if len(detailJSON) > 0 {
		json.Unmarshal(detailJSON, &result.ResultDetail)
	}

	return result, nil
}

func (e *Executor) recordExecutionStart(
	ctx context.Context,
	sessionID, tenantID, moduleName, cacheKey string,
	ttlSeconds int,
) (int64, error) {
	moduleInfo, _ := moduleregistry.GetModuleInfo(moduleName)

	query := `
		INSERT INTO session_module_executions_hot (
			gw_session_id, tenant_id, module_name, module_version,
			status, cache_key, ttl_seconds, expires_at
		) VALUES ($1, $2, $3, $4, 'running', $5, $6, NOW() + ($6 || ' seconds')::INTERVAL)
		RETURNING execution_id
	`

	var execID int64
	err := e.db.QueryRow(ctx, query,
		sessionID, tenantID, moduleName, moduleInfo.Version,
		cacheKey, ttlSeconds,
	).Scan(&execID)

	return execID, err
}

func (e *Executor) recordExecutionSuccess(ctx context.Context, execID int64, result *ExecuteResult, durationMs int) error {
	summaryJSON, _ := json.Marshal(result.ResultSummary)
	detailJSON, _ := json.Marshal(result.ResultDetail)

	query := `
		UPDATE session_module_executions_hot
		SET status = 'completed',
		    completed_at = NOW(),
		    duration_ms = $2,
		    result_summary = $3,
		    result_detail = $4,
		    updated_at = NOW()
		WHERE execution_id = $1
	`

	_, err := e.db.Exec(ctx, query, execID, durationMs, summaryJSON, detailJSON)
	return err
}

func (e *Executor) recordExecutionFailure(ctx context.Context, execID int64, errMsg string, durationMs int) error {
	query := `
		UPDATE session_module_executions_hot
		SET status = 'failed',
		    completed_at = NOW(),
		    duration_ms = $2,
		    error_message = $3,
		    updated_at = NOW()
		WHERE execution_id = $1
	`

	_, err := e.db.Exec(ctx, query, execID, durationMs, errMsg)
	return err
}

// ────────────────────────────────────────────────────────────────
// 批量查询接口
// ────────────────────────────────────────────────────────────────

// BatchCheck 批量检查多个模块的执行状态
func (e *Executor) BatchCheck(
	ctx context.Context,
	sessionID string,
	moduleNames []string,
) (map[string]*ExecuteResult, error) {
	if len(moduleNames) == 0 {
		return make(map[string]*ExecuteResult), nil
	}

	query := `
		SELECT module_name, execution_id, status, result_summary, result_detail, duration_ms, expires_at
		FROM session_module_executions_hot
		WHERE gw_session_id = $1
		  AND module_name = ANY($2)
		  AND status = 'completed'
		  AND expires_at > NOW()
		ORDER BY created_at DESC
	`

	rows, err := e.db.Query(ctx, query, sessionID, moduleNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make(map[string]*ExecuteResult)
	for rows.Next() {
		var (
			moduleName  string
			execID      int64
			status      string
			summaryJSON []byte
			detailJSON  []byte
			durationMs  int
			expiresAt   time.Time
		)

		if err := rows.Scan(&moduleName, &execID, &status, &summaryJSON, &detailJSON, &durationMs, &expiresAt); err != nil {
			continue
		}

		result := &ExecuteResult{
			ExecutionID: execID,
			Status:      ExecuteStatus(status),
			DurationMs:  durationMs,
			ExpiresAt:   &expiresAt,
			FromCache:   true,
		}

		if len(summaryJSON) > 0 {
			json.Unmarshal(summaryJSON, &result.ResultSummary)
		}
		if len(detailJSON) > 0 {
			json.Unmarshal(detailJSON, &result.ResultDetail)
		}

		results[moduleName] = result
	}

	return results, nil
}

// InvalidateCache 使指定会话的指定模块缓存失效
func (e *Executor) InvalidateCache(ctx context.Context, sessionID, moduleName string) error {
	query := `
		UPDATE session_module_executions_hot
		SET expires_at = NOW(),
		    updated_at = NOW()
		WHERE gw_session_id = $1
		  AND module_name = $2
		  AND status = 'completed'
	`

	_, err := e.db.Exec(ctx, query, sessionID, moduleName)

	// 清理 Redis 缓存（如果有）
	if e.enableRedis {
		pattern := fmt.Sprintf("module:exec:%s:%s:*", sessionID, moduleName)
		keys, _ := e.redis.Keys(ctx, pattern).Result()
		if len(keys) > 0 {
			e.redis.Del(ctx, keys...)
		}
	}

	// 清理内存缓存
	e.memCacheMu.Lock()
	for key := range e.memCache {
		if len(key) > len(sessionID)+len(moduleName)+2 &&
			key[:len(sessionID)] == sessionID &&
			key[len(sessionID)+1:len(sessionID)+1+len(moduleName)] == moduleName {
			delete(e.memCache, key)
			delete(e.memCacheTime, key)
		}
	}
	e.memCacheMu.Unlock()

	return err
}

// ────────────────────────────────────────────────────────────────
// 工具函数
// ────────────────────────────────────────────────────────────────

// ComputeCacheKey 计算缓存键
func ComputeCacheKey(moduleName string, params map[string]interface{}) string {
	data, _ := json.Marshal(params)
	hash := sha256.Sum256(data)
	return moduleName + ":" + hex.EncodeToString(hash[:])[:16]
}

// RecordSkip 记录跳过（用于已有结果但希望显式记录的情况）
func (e *Executor) RecordSkip(ctx context.Context, sessionID, tenantID, moduleName, cacheKey string) error {
	moduleInfo, _ := moduleregistry.GetModuleInfo(moduleName)

	query := `
		INSERT INTO session_module_executions_hot (
			gw_session_id, tenant_id, module_name, module_version,
			status, cache_key, ttl_seconds, expires_at,
			started_at, completed_at, duration_ms
		) VALUES ($1, $2, $3, $4, 'skipped', $5, 60, NOW() + INTERVAL '60 seconds',
		          NOW(), NOW(), 0)
	`

	_, err := e.db.Exec(ctx, query,
		sessionID, tenantID, moduleName, moduleInfo.Version, cacheKey)

	return err
}
