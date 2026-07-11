// Package moduleexec 实现会话模块执行记录系统
//
// 核心功能：
//   - Check-Execute-Record 模式：执行前检查是否已有有效结果
//   - 自动记录每个模块的执行情况
//   - 支持 Redis L1 缓存 + 数据库 L2 缓存
//   - 批量查询优化
//
// 数据流：
//  1. 调用 CheckAndExecute(sessionID, moduleName, inputParams, ttl, executeFn)
//  2. 系统计算 cache_key，查询是否有有效缓存
//  3. 有缓存：直接返回（FromCache=true）
//  4. 无缓存：执行 executeFn，记录开始（status=running）
//  5. 执行完成后：记录结果（status=completed/skipped）
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
	"golang.org/x/sync/singleflight"
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
	inflight     singleflight.Group
}

// Config 执行器配置
type Config struct {
	DB          DBTX
	Redis       *redis.Client // 可选
	Logger      *slog.Logger
	EnableRedis bool
	MemCacheTTL time.Duration // 内存缓存 TTL，默认 30 秒
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
		db:           cfg.DB,
		redis:        cfg.Redis,
		logger:       cfg.Logger,
		enableRedis:  cfg.EnableRedis && cfg.Redis != nil,
		memCache:     make(map[string]*ExecuteResult),
		memCacheTime: make(map[string]time.Time),
		memCacheTTL:  cfg.MemCacheTTL,
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

	if cached := e.getCached(ctx, tenantID, sessionID, moduleName, cacheKey, ttlSeconds); cached != nil {
		return e.returnCached(moduleName, cached)
	}

	value, err, _ := e.inflight.Do(memCacheKey(tenantID, sessionID, moduleName, cacheKey), func() (interface{}, error) {
		if cached := e.getCached(ctx, tenantID, sessionID, moduleName, cacheKey, ttlSeconds); cached != nil {
			return cached, nil
		}

		recordCacheMiss(moduleName)
		return e.executeAfterCacheMiss(ctx, sessionID, tenantID, moduleName, cacheKey, ttlSeconds, executeFn)
	})
	if err != nil {
		return nil, err
	}

	result, ok := value.(*ExecuteResult)
	if !ok || result == nil {
		return nil, fmt.Errorf("module %s returned an invalid execution result", moduleName)
	}
	clone, err := cloneExecuteResult(result)
	if err != nil {
		return nil, fmt.Errorf("clone module execution result: %w", err)
	}
	return clone, nil
}

func (e *Executor) returnCached(moduleName string, cached *ExecuteResult) (*ExecuteResult, error) {
	cached.FromCache = true
	recordCacheHit(moduleName, "cache")
	recordExecution(moduleName, string(cached.Status), true, 0)
	return cached, nil
}

func (e *Executor) getCached(ctx context.Context, tenantID, sessionID, moduleName, cacheKey string, ttlSeconds int) *ExecuteResult {
	if cached := e.getFromMemCache(tenantID, sessionID, moduleName, cacheKey); cached != nil {
		return cached
	}

	if e.enableRedis {
		if cached := e.getFromRedis(ctx, tenantID, sessionID, moduleName, cacheKey); cached != nil {
			if err := e.setToMemCache(tenantID, sessionID, moduleName, cacheKey, cached); err != nil {
				e.logger.Warn("clone Redis module execution cache result failed", "error", err, "module", moduleName)
			}
			return cached
		}
	}

	if e.db == nil {
		return nil
	}

	if cached, err := e.getFromDB(ctx, tenantID, sessionID, moduleName, cacheKey); err == nil && cached != nil {
		if e.enableRedis {
			e.setToRedis(ctx, tenantID, sessionID, moduleName, cacheKey, cached, ttlSeconds)
		}
		if err := e.setToMemCache(tenantID, sessionID, moduleName, cacheKey, cached); err != nil {
			e.logger.Warn("clone database module execution cache result failed", "error", err, "module", moduleName)
		}
		return cached
	}
	return nil
}

func (e *Executor) executeAfterCacheMiss(
	ctx context.Context,
	sessionID, tenantID, moduleName, cacheKey string,
	ttlSeconds int,
	executeFn func(context.Context) (*ExecuteResult, error),
) (*ExecuteResult, error) {
	if e.db == nil {
		return e.executeDirectly(ctx, sessionID, tenantID, moduleName, executeFn)
	}

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
		if err := e.recordExecutionFailure(ctx, execID, execErr.Error(), durationMs); err != nil {
			e.logger.Warn("record execution failure failed", "error", err, "execution_id", execID)
		}
		recordExecution(moduleName, "failed", false, execDuration)
		return nil, execErr
	}
	if result == nil {
		err := fmt.Errorf("module %s returned a nil result", moduleName)
		if recordErr := e.recordExecutionFailure(ctx, execID, err.Error(), durationMs); recordErr != nil {
			e.logger.Warn("record nil execution result failed", "error", recordErr, "execution_id", execID)
		}
		recordExecution(moduleName, "failed", false, execDuration)
		return nil, err
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
		e.setToRedis(ctx, tenantID, sessionID, moduleName, cacheKey, result, ttlSeconds)
	}
	if err := e.setToMemCache(tenantID, sessionID, moduleName, cacheKey, result); err != nil {
		e.logger.Warn("clone module execution cache result failed", "error", err, "module", moduleName)
	}

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
	if result == nil {
		recordExecution(moduleName, "failed", false, execDuration)
		return nil, fmt.Errorf("module %s returned a nil result", moduleName)
	}

	result.DurationMs = durationMs
	result.Status = StatusCompleted
	recordExecution(moduleName, "completed", false, execDuration)
	return result, nil
}

// ────────────────────────────────────────────────────────────────
// L0 内存缓存
// ────────────────────────────────────────────────────────────────

func (e *Executor) getFromMemCache(tenantID, sessionID, moduleName, cacheKey string) *ExecuteResult {
	e.memCacheMu.RLock()
	defer e.memCacheMu.RUnlock()

	key := memCacheKey(tenantID, sessionID, moduleName, cacheKey)
	cachedTime, exists := e.memCacheTime[key]
	if !exists {
		return nil
	}
	if time.Since(cachedTime) > e.memCacheTTL {
		return nil
	}
	result, err := cloneExecuteResult(e.memCache[key])
	if err != nil {
		e.logger.Warn("clone memory module execution cache result failed", "error", err, "module", moduleName)
		return nil
	}
	return result
}

func (e *Executor) setToMemCache(tenantID, sessionID, moduleName, cacheKey string, result *ExecuteResult) error {
	clone, err := cloneExecuteResult(result)
	if err != nil {
		return err
	}

	e.memCacheMu.Lock()
	defer e.memCacheMu.Unlock()

	key := memCacheKey(tenantID, sessionID, moduleName, cacheKey)
	e.memCache[key] = clone
	e.memCacheTime[key] = time.Now()
	return nil
}

// ────────────────────────────────────────────────────────────────
// L1 Redis 缓存
// ────────────────────────────────────────────────────────────────

func (e *Executor) redisKey(tenantID, sessionID, moduleName, cacheKey string) string {
	return "module:exec:" + compositeKey(tenantID, sessionID, moduleName, cacheKey)
}

func (e *Executor) getFromRedis(ctx context.Context, tenantID, sessionID, moduleName, cacheKey string) *ExecuteResult {
	if e.redis == nil {
		return nil
	}

	data, err := e.redis.Get(ctx, e.redisKey(tenantID, sessionID, moduleName, cacheKey)).Bytes()
	if err != nil {
		if err != redis.Nil {
			e.logger.Warn("read module execution cache failed", "error", err, "tenant_id", tenantID, "session_id", sessionID, "module", moduleName)
		}
		return nil
	}

	var result ExecuteResult
	if err := json.Unmarshal(data, &result); err != nil {
		e.logger.Warn("decode module execution cache failed", "error", err, "tenant_id", tenantID, "session_id", sessionID, "module", moduleName)
		return nil
	}

	return &result
}

func (e *Executor) setToRedis(ctx context.Context, tenantID, sessionID, moduleName, cacheKey string, result *ExecuteResult, ttlSeconds int) {
	if e.redis == nil {
		return
	}

	data, err := json.Marshal(result)
	if err != nil {
		e.logger.Warn("encode module execution cache failed", "error", err, "tenant_id", tenantID, "session_id", sessionID, "module", moduleName)
		return
	}

	if err := e.redis.Set(ctx, e.redisKey(tenantID, sessionID, moduleName, cacheKey), data, time.Duration(ttlSeconds)*time.Second).Err(); err != nil {
		e.logger.Warn("persist module execution cache failed", "error", err, "tenant_id", tenantID, "session_id", sessionID, "module", moduleName)
	}
}

// ────────────────────────────────────────────────────────────────
// L2 数据库查询
// ────────────────────────────────────────────────────────────────

func (e *Executor) getFromDB(ctx context.Context, tenantID, sessionID, moduleName, cacheKey string) (*ExecuteResult, error) {
	query := `
			SELECT execution_id, status, result_summary, result_detail,
			       duration_ms, expires_at
			FROM session_module_executions_hot
			WHERE gw_session_id = $1
			  AND tenant_id = $2
			  AND module_name = $3
			  AND cache_key = $4
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

	err := e.db.QueryRow(ctx, query, sessionID, tenantID, moduleName, cacheKey).
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
		if err := json.Unmarshal(summaryJSON, &result.ResultSummary); err != nil {
			return nil, fmt.Errorf("decode result summary: %w", err)
		}
	}
	if len(detailJSON) > 0 {
		if err := json.Unmarshal(detailJSON, &result.ResultDetail); err != nil {
			return nil, fmt.Errorf("decode result detail: %w", err)
		}
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
	tenantID string,
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
			  AND tenant_id = $2
			  AND module_name = ANY($3)
		  AND status = 'completed'
		  AND expires_at > NOW()
		ORDER BY created_at DESC
	`

	rows, err := e.db.Query(ctx, query, sessionID, tenantID, moduleNames)
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
			if err := json.Unmarshal(summaryJSON, &result.ResultSummary); err != nil {
				return nil, fmt.Errorf("decode %s result summary: %w", moduleName, err)
			}
		}
		if len(detailJSON) > 0 {
			if err := json.Unmarshal(detailJSON, &result.ResultDetail); err != nil {
				return nil, fmt.Errorf("decode %s result detail: %w", moduleName, err)
			}
		}

		results[moduleName] = result
	}

	return results, nil
}

// InvalidateCache 使指定租户会话的指定模块缓存失效
func (e *Executor) InvalidateCache(ctx context.Context, tenantID, sessionID, moduleName string) error {
	query := `
			UPDATE session_module_executions_hot
			SET expires_at = NOW(),
			    updated_at = NOW()
			WHERE gw_session_id = $1
			  AND tenant_id = $2
			  AND module_name = $3
			  AND status = 'completed'
		`

	var err error
	if e.db != nil {
		_, err = e.db.Exec(ctx, query, sessionID, tenantID, moduleName)
	}

	// 清理 Redis 缓存（如果有）
	if e.enableRedis {
		pattern := "module:exec:" + compositeKeyPrefix(tenantID, sessionID, moduleName)
		keys, redisErr := e.redis.Keys(ctx, pattern).Result()
		if redisErr != nil {
			e.logger.Warn("list module execution cache keys failed", "error", redisErr, "tenant_id", tenantID, "session_id", sessionID, "module", moduleName)
		}
		if len(keys) > 0 {
			if redisErr := e.redis.Del(ctx, keys...).Err(); redisErr != nil {
				e.logger.Warn("invalidate module execution cache failed", "error", redisErr, "tenant_id", tenantID, "session_id", sessionID, "module", moduleName)
			}
		}
	}

	// 清理内存缓存
	prefix := compositeKeyPrefix(tenantID, sessionID, moduleName)
	e.memCacheMu.Lock()
	for key := range e.memCache {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
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

func memCacheKey(tenantID, sessionID, moduleName, cacheKey string) string {
	return compositeKey(tenantID, sessionID, moduleName, cacheKey)
}

func compositeKey(parts ...string) string {
	key := ""
	for _, part := range parts {
		key += fmt.Sprintf("%d:%s", len(part), part)
	}
	return key
}

func compositeKeyPrefix(tenantID, sessionID, moduleName string) string {
	return compositeKey(tenantID, sessionID, moduleName)
}

func cloneExecuteResult(result *ExecuteResult) (*ExecuteResult, error) {
	if result == nil {
		return nil, nil
	}
	clone := *result
	var err error
	clone.ResultSummary, err = cloneMap(result.ResultSummary)
	if err != nil {
		return nil, fmt.Errorf("clone result summary: %w", err)
	}
	clone.ResultDetail, err = cloneMap(result.ResultDetail)
	if err != nil {
		return nil, fmt.Errorf("clone result detail: %w", err)
	}
	if result.ExpiresAt != nil {
		expiresAt := *result.ExpiresAt
		clone.ExpiresAt = &expiresAt
	}
	return &clone, nil
}

func cloneMap(src map[string]interface{}) (map[string]interface{}, error) {
	if src == nil {
		return nil, nil
	}
	data, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("encode map: %w", err)
	}
	var clone map[string]interface{}
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, fmt.Errorf("decode map: %w", err)
	}
	return clone, nil
}

// ComputeCacheKey 计算缓存键
func ComputeCacheKey(moduleName string, params map[string]interface{}) string {
	data, _ := json.Marshal(params)
	hash := sha256.Sum256(data)
	return moduleName + ":" + hex.EncodeToString(hash[:])[:16]
}

// RecordSkip 记录跳过（用于已有结果但希望显式记录的情况）
func (e *Executor) RecordSkip(ctx context.Context, sessionID, tenantID, moduleName, cacheKey string) error {
	if e.db == nil {
		return nil
	}
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
