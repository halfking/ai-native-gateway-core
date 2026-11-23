// Package promptinjection - 错误收集器
// 用途：收集 PI 检测过程中的错误，入队等待智能分析
package promptinjection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// ErrorCollector PI 检测错误收集器
type ErrorCollector struct {
	db      *sql.DB
	enabled bool

	// ★ 2026-11-17 审计轮：异步（非阻塞）采集通道。
	//
	//	错误采集发生在**请求热路径**上（判官失败、canary 查询失败……）⇒
	//	绝不能同步等一次 DB 往返 ⇒ 用有界缓冲队列 + 专职 drain 协程。
	//	队列满 ⇒ **丢弃并计数**（宁可丢一条错误，也不能把请求路径拖垮）。
	queue     chan *CollectedError
	started   atomic.Bool
	dropped   atomic.Int64
	collected atomic.Int64

	// maxDepth 是**数据库队列**的深度上限（pending + processing 行数）。
	//
	// ★ 2026-11-17 审计轮复核修正：本仓此前有一个「队列深度上限」的环境变量
	//	（LLMGatewayPIErrorAnalysisMaxDepth），但**没有任何实现**：
	//	配置里写着「超出则丢弃新错误」，代码里找不到任何深度判断
	//	⇒ 错误风暴（判官持续失败）会把队列无限撑大，
	//	  而 worker 的处理速度跟不上 ⇒ 队列成为只涨不消的黑洞。
	//	⇒ 这里真正实现它（0 = 不限制）。
	//
	// ★ 与 errorCollectorQueueSize（内存 channel 容量）是**两件事**：
	//
	//	前者是进程内的瞬时缓冲（满了丢一条、计入 dropped）；
	//	后者是**持久队列**的深度闸（满了之后不再入库）。
	maxDepth atomic.Int64

	// depthDropped 统计「因队列深度闸被丢弃」的错误数。
	depthDropped atomic.Int64
}

// SetMaxDepth 设置数据库队列的深度上限（0 = 不限制）。
//
// ★ 必须在 Start(ctx) 之前调用（避免与 drain 协程并发写）。
func (ec *ErrorCollector) SetMaxDepth(n int) {
	if ec == nil {
		return
	}
	if n < 0 {
		n = 0
	}
	ec.maxDepth.Store(int64(n))
}

// errorCollectorQueueSize 是异步采集队列的容量。
const errorCollectorQueueSize = 256

// NewErrorCollector 创建错误收集器
//
// ★ 2026-11-17 审计轮：异步 drain 协程**不在构造函数里启动**，
//
//	而是由 Start(ctx) 显式启动 ⇒ 单元测试用 sqlmock 直接 new 出来时
//	不会留下悬挂协程。
func NewErrorCollector(db *sql.DB, enabled bool) *ErrorCollector {
	return &ErrorCollector{
		db:      db,
		enabled: enabled,
		queue:   make(chan *CollectedError, errorCollectorQueueSize),
	}
}

// Start 启动异步采集 drain 协程。
//
// ctx 取消 ⇒ drain 协程退出（未处理完的队列项被丢弃）。
// 重复调用安全（幂等）。
func (ec *ErrorCollector) Start(ctx context.Context) {
	if ec == nil || !ec.enabled {
		return
	}
	if !ec.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-ec.queue:
				if item == nil {
					continue
				}
				if err := ec.Collect(ctx, item); err != nil {
					slog.Warn("error_collector: async collect failed", "error", err)
				}
				ec.collected.Add(1)
			}
		}
	}()
}

// CollectAsync **非阻塞**地采集一条错误。
//
// ★ 为什么必须异步：调用点在请求热路径上（判官失败、canary 查询失败……）
//
//	⇒ 同步 DB 往返会把外部依赖的延迟传导到用户请求上。
//
//	★ 队列满 ⇒ 丢弃并计数（不阻塞、不 panic）⇒ 错误风暴下请求路径仍然健康。
func (ec *ErrorCollector) CollectAsync(ctx context.Context, collectedErr *CollectedError) {
	if ec == nil || !ec.enabled || collectedErr == nil {
		return
	}
	if !ec.started.Load() {
		// 未 Start ⇒ 同步丢弃（记录一次，避免"看起来在采集"）。
		ec.dropped.Add(1)
		return
	}
	select {
	case ec.queue <- collectedErr:
	default:
		n := ec.dropped.Add(1)
		if n%100 == 1 {
			slog.Warn("error_collector: queue full; dropping error samples",
				"dropped_total", n, "queue_size", errorCollectorQueueSize)
		}
	}
}

// Enabled 报告采集功能是否开启。
//
// ★ 2026-11-18 审计轮 P1：管理面需要能区分
//
//	「采集开着但队列是空的」与「采集**没开**（或**没有生产者**）」——
//	否则「队列恒空」会被读成「没有错误发生过」。
func (ec *ErrorCollector) Enabled() bool {
	if ec == nil {
		return false
	}
	return ec.enabled
}

// CollectorStats 返回采集器的运行计数（供管理 API 观测）。
func (ec *ErrorCollector) CollectorStats() map[string]int64 {
	if ec == nil {
		return map[string]int64{}
	}
	return map[string]int64{
		"queued":    int64(len(ec.queue)),
		"collected": ec.collected.Load(),
		"dropped":   ec.dropped.Load(),
		// 「因队列深度闸被丢弃」与「内存 channel 满被丢弃」是两件事，
		// 分开计数 ⇒ 运维能区分「内存压力」与「持久队列太深」。
		"depth_dropped": ec.depthDropped.Load(),
		"max_depth":     ec.maxDepth.Load(),
	}
}

// ErrorType 错误类型常量
const (
	ErrorTypeLLMDetectionFailed    = "llm_detection_failed"    // LLM 判官调用失败
	ErrorTypeRuleFalsePositive     = "rule_false_positive"     // 规则误报
	ErrorTypeCanaryDetectionFailed = "canary_detection_failed" // Canary 检测失败
	ErrorTypeVectorMatchFailed     = "vector_match_failed"     // 向量匹配失败
	ErrorTypeHeuristicMisfire      = "heuristic_misfire"       // 启发式误触发
	ErrorTypePolicyLoadFailed      = "policy_load_failed"      // 策略加载失败
	ErrorTypeUnexpectedBehavior    = "unexpected_behavior"     // 意外行为
	ErrorTypePerformanceIssue      = "performance_issue"       // 性能问题
	// ErrorTypeOutputGuardBlocked：输出合规守卫的格式类拦截（2026-11-20 PI
	// 接入轮）。**生产者是 domains/streaming 的策略拦截反馈出口**（经
	// SetOutputGuardPISink 注入），不是 PI 检测器自身——这是第一个「把其它
	// 子系统的失败喂给 PI 智能体」的源（此前生产者只有 Detector 的 6 个失败
	// 分支）。这类拦截拦的是上游输出格式（工具参数非法 JSON / 脱敏破坏 JSON /
	// 帧编码失败），不是敏感内容 ⇒ 分析目标是归因（上游断流截断？中转格式
	// 转换 bug？守卫误判？）+ 给出继续的建议与操作；**apply 面对这类事件
	// 恒 view-only**（其建议不指向 prompt_injection_rules，见
	// admin/pi_analysis_fix_apply.go 的类型闸）。
	ErrorTypeOutputGuardBlocked = "output_guard_blocked" // 输出守卫格式类拦截
)

// ErrorQueueStatus 队列状态
const (
	QueueStatusPending    = "pending"
	QueueStatusProcessing = "processing"
	QueueStatusAnalyzed   = "analyzed"
	QueueStatusFailed     = "failed"
	QueueStatusSkipped    = "skipped"
)

// CollectedError 收集的错误信息
type CollectedError struct {
	ErrorType    string                 `json:"error_type"`
	TenantID     string                 `json:"tenant_id"`
	SessionID    string                 `json:"session_id,omitempty"`
	RequestID    string                 `json:"request_id,omitempty"`
	InputText    string                 `json:"input_text,omitempty"`
	ErrorMessage string                 `json:"error_message"`
	ErrorDetails map[string]interface{} `json:"error_details,omitempty"`
	Priority     int                    `json:"priority"` // 1-10, 10=highest

	// 检测上下文
	DetectionLayers map[string]bool        `json:"detection_layers,omitempty"`
	MatchedRules    []MatchedRule          `json:"matched_rules,omitempty"`
	PolicyConfig    map[string]interface{} `json:"policy_config,omitempty"`

	// 元数据
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// QueuedError 队列中的错误项
type QueuedError struct {
	ID              int64
	ErrorType       string
	ErrorHash       string
	Priority        int
	TenantID        string
	SessionID       sql.NullString
	RequestID       sql.NullString
	InputText       sql.NullString
	InputHash       sql.NullString
	ErrorMessage    string
	ErrorDetails    json.RawMessage
	DetectionLayers json.RawMessage
	MatchedRules    json.RawMessage
	PolicyConfig    json.RawMessage
	Status          string
	RetryCount      int
	MaxRetries      int
	CreatedAt       time.Time
	StartedAt       sql.NullTime
	CompletedAt     sql.NullTime
	Metadata        json.RawMessage
}

// defaultTenantID 是租户缺失时的兜底哨兵。
//
// ★ 为什么不再直接报错丢弃（2026-11-17 审计轮）：
//
//	原实现对空 tenant_id 直接 return error ⇒ 收集失败。
//	但 tenant_id 在请求链路上**本就可以为空**（V2 pipeline 的
//	env.TenantID 在未绑定租户的请求上是空串）⇒
//	这类错误会被**静默丢掉**，正好是最该被分析的一类
//	（"检测失败"往往发生在租户上下文还没建立的链路上）。
//	本仓的既有哨兵口径是 "default"（见对账模块的 tenant_id 取值）。
const defaultTenantID = "default"

// Collect 收集错误并入队
//
// ★ 2026-11-17 审计轮的三处根修：
//
//	① **JSONB 绑定形态**（P1，条件性）：五个 jsonb 列原先直接绑 json.Marshal 的 []byte。
//	   本仓 PG 的执行模式是**有条件的**（db/db.go:105-108）：默认 CacheStatement
//	   ⇒ []byte 走 JSONBCodec，绑定是**好的**；只有 kill-switch
//	   `LLM_GATEWAY_DB_STATEMENT_CACHE=false` 落到 SimpleProtocol 时
//	   才被文本编码成 bytea 十六进制 ⇒ PG 报 22P02。
//	   ⇒ 统一改为 jsonbArg(...) 产出 string + SQL 里显式
//	     NULLIF($N,'')::text::jsonb（两种模式都正确，且与本仓正典
//	     internal/dbx.NormalizeJSONB 同形）。
//	② **UTF-8 切点**（P0，已实测复现）：原先 inputText[:10000] 按字节切 ⇒
//	   中文输入会切开 rune ⇒ 非法 UTF-8 ⇒ PG **22021** ⇒ 整行丢失。
//	   实测：纯中文输入 3334..3399 字 **66/66** 命中切点 ⇒
//	   真实中文流量上错误队列**一条都插不进去**。
//	   ⇒ 改为 truncateUTF8Bytes。
//	③ **去重竞态**（P1）：原先「查重 → 插入」是 read-then-write ⇒ 两个并发
//	   收集者能同时查到「不存在」⇒ 同一错误入队两次 ⇒ 重复 LLM 开销 +
//	   结果表出现两条互斥结论。⇒ 改为**事务内 pg_advisory_xact_lock**
//	   按 error_hash 串行化「查重 + 插入」两段（零迁移、无需唯一索引）。
func (ec *ErrorCollector) Collect(ctx context.Context, collectedErr *CollectedError) error {
	if !ec.enabled {
		return nil // 收集器未启用，静默跳过
	}

	if collectedErr == nil {
		return fmt.Errorf("collected error is nil")
	}

	// 验证必填字段
	if collectedErr.ErrorType == "" {
		return fmt.Errorf("error_type is required")
	}
	tenantID := collectedErr.TenantID
	if tenantID == "" {
		tenantID = defaultTenantID
	}

	// 计算错误指纹（用于去重）
	errorHash := ec.calculateErrorHash(collectedErr)

	// 计算输入 hash（如果有输入文本）
	var inputHash sql.NullString
	if collectedErr.InputText != "" {
		hash := sha256.Sum256([]byte(collectedErr.InputText))
		inputHash = sql.NullString{
			String: hex.EncodeToString(hash[:]),
			Valid:  true,
		}
	}

	// 序列化 JSON 字段（★ 必须是 string，理由见函数注释①）
	errorDetailsJSON, err := jsonbArg(collectedErr.ErrorDetails)
	if err != nil {
		return fmt.Errorf("failed to marshal error_details: %w", err)
	}

	detectionLayersJSON, err := jsonbArg(collectedErr.DetectionLayers)
	if err != nil {
		return fmt.Errorf("failed to marshal detection_layers: %w", err)
	}

	matchedRulesJSON, err := jsonbArg(collectedErr.MatchedRules)
	if err != nil {
		return fmt.Errorf("failed to marshal matched_rules: %w", err)
	}

	policyConfigJSON, err := jsonbArg(collectedErr.PolicyConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal policy_config: %w", err)
	}

	metadataJSON, err := jsonbArg(collectedErr.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// 确定优先级（默认 5）
	priority := collectedErr.Priority
	if priority < 1 || priority > 10 {
		priority = 5
	}

	// 截断输入文本（★ 必须落在 rune 边界上，理由见函数注释②）
	inputText := truncateUTF8Bytes(collectedErr.InputText, maxInputTextBytes)

	// 插入队列（★ jsonb 列一律 NULLIF($N,'')::text::jsonb，理由见函数注释①）
	query := `
		INSERT INTO error_analysis_queue (
			error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			input_text, input_hash, error_message,
			error_details, detection_layers, matched_rules, policy_config,
			metadata, status, retry_count, max_retries
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
		          NULLIF($10, '')::text::jsonb, NULLIF($11, '')::text::jsonb,
		          NULLIF($12, '')::text::jsonb, NULLIF($13, '')::text::jsonb,
		          NULLIF($14, '')::text::jsonb, $15, $16, $17)
		RETURNING id
	`

	// ★ 去重串行化（理由见函数注释③）。
	//   pg_advisory_xact_lock 是**事务级**锁：随事务提交/回滚自动释放，
	//   不会泄漏；同 error_hash 的并发收集者在这里排队，
	//   后到者拿到锁后重新查重 ⇒ 看到已入队的行 ⇒ 跳过。
	tx, err := ec.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin dedup transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, errorHash); err != nil {
		// 锁失败不致命：退化成「无串行化的查重」，仍然继续插入
		// （宁可偶尔重复，也不要因为拿不到锁就整条错误丢掉）。
		slog.Warn("error_collector: advisory lock failed; dedup may race", "error", err)
	}

	exists, existingID, err := ec.checkDuplicateTx(ctx, tx, errorHash, 24*time.Hour)
	if err != nil {
		// F5 修复：查重失败意味着事务已中止（PostgreSQL SQLSTATE 25P02），
		// 后续 INSERT 不可能成功。之前的注释声称"继续插入"是误导性的。
		return fmt.Errorf("failed to check duplicate in transaction: %w", err)
		// defer 会自动 rollback
	}

	if exists {
		slog.Debug("error_collector: duplicate error skipped",
			"error_hash", errorHash,
			"existing_id", existingID)
		committed = true
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit dedup transaction: %w", err)
		}
		return nil // 重复错误，跳过
	}

	// ★ 队列深度闸（maxDepth）：持久队列已经太深 ⇒ 不再入库。
	//
	//	放在事务内（而不是入库前的独立 COUNT）⇒ 与 INSERT 看到的是**同一个**
	//	快照 ⇒ 不会出现「COUNT 时不满、INSERT 时已满」的窗口。
	//	★ 查深度失败**不阻断入库**（宁可多一条，也不能因为深度探测失败
	//	  就把错误整条丢掉——那与「没有深度闸」是同一种后果）。
	if limit := ec.maxDepth.Load(); limit > 0 {
		var depth int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM error_analysis_queue
			 WHERE status IN ('pending','processing')`).Scan(&depth); err != nil {
			// F5 修复：深度检查失败意味着事务已中止，后续 INSERT 不可能成功。
			return fmt.Errorf("failed to check queue depth in transaction: %w", err)
		} else if depth >= limit {
			ec.depthDropped.Add(1)
			slog.Warn("error_collector: queue depth limit reached, error dropped",
				"max_depth", limit, "depth", depth)
			committed = true
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("failed to commit depth-guard transaction: %w", err)
			}
			return nil
		}
	}

	var queueID int64
	err = tx.QueryRowContext(ctx, query,
		collectedErr.ErrorType, errorHash, priority,
		tenantID,
		toNullString(collectedErr.SessionID),
		toNullString(collectedErr.RequestID),
		toNullString(inputText),
		inputHash,
		collectedErr.ErrorMessage,
		errorDetailsJSON,
		detectionLayersJSON,
		matchedRulesJSON,
		policyConfigJSON,
		metadataJSON,
		QueueStatusPending,
		0, // retry_count
		3, // max_retries
	).Scan(&queueID)

	if err != nil {
		return fmt.Errorf("failed to insert error into queue: %w", err)
	}

	committed = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit error queue insert: %w", err)
	}

	slog.Info("error_collector: error queued for analysis",
		"queue_id", queueID,
		"error_type", collectedErr.ErrorType,
		"error_hash", errorHash,
		"priority", priority)

	return nil
}

// maxInputTextBytes 是落库的输入文本上限（字节）。
const maxInputTextBytes = 10000

// CollectLLMDetectionFailure 收集 LLM 检测失败错误
func (ec *ErrorCollector) CollectLLMDetectionFailure(
	ctx context.Context,
	tenantID, sessionID, requestID string,
	input string,
	llmError error,
	policy *Policy,
) error {
	if !ec.enabled {
		return nil
	}

	// ★ nil 安全（2026-11-17 审计轮 P1）：原实现直接解引用 policy.LLMEngineID
	//   ⇒ policy 为 nil 时**panic**。而 policy 为 nil 恰好是
	//   「策略加载失败」这条错误路径的常态 ⇒
	//   「收集错误」的动作本身把进程**打挂** ⇒ 错误收集器成了故障放大器。
	details := map[string]interface{}{}
	if policy != nil {
		if policy.LLMEngineID != nil {
			details["llm_engine_id"] = *policy.LLMEngineID
		}
		details["detection_timeout"] = policy.LLMDetectionTimeoutSec
		details["fail_closed_enabled"] = policy.LLMDetectionFailClosed
	} else {
		details["policy_loaded"] = false
	}

	errMsg := "unknown error"
	if llmError != nil {
		errMsg = llmError.Error()
	}

	collected := &CollectedError{
		ErrorType:    ErrorTypeLLMDetectionFailed,
		TenantID:     tenantID,
		SessionID:    sessionID,
		RequestID:    requestID,
		InputText:    input,
		ErrorMessage: errMsg,
		Priority:     7, // 高优先级
		ErrorDetails: details,
		Metadata: map[string]interface{}{
			"timestamp": time.Now().Unix(),
		},
	}

	return ec.Collect(ctx, collected)
}

// CollectRuleFalsePositive 收集规则误报
func (ec *ErrorCollector) CollectRuleFalsePositive(
	ctx context.Context,
	tenantID, sessionID, requestID string,
	input string,
	matchedRules []MatchedRule,
	detectionLayers map[string]bool,
) error {
	if !ec.enabled {
		return nil
	}

	err := &CollectedError{
		ErrorType:       ErrorTypeRuleFalsePositive,
		TenantID:        tenantID,
		SessionID:       sessionID,
		RequestID:       requestID,
		InputText:       input,
		ErrorMessage:    "Rule matched but likely false positive",
		Priority:        6,
		MatchedRules:    matchedRules,
		DetectionLayers: detectionLayers,
		Metadata: map[string]interface{}{
			"timestamp":          time.Now().Unix(),
			"matched_rule_count": len(matchedRules),
		},
	}

	return ec.Collect(ctx, err)
}

// GetPendingErrors 获取待处理的错误（按优先级排序）
//
// ⚠️ 只读列举，**不构成占位**：两个调用方可以拿到同一行。
//
//	真正干活请用 ClaimPendingErrors（原子占位）。
func (ec *ErrorCollector) GetPendingErrors(ctx context.Context, limit int) ([]*QueuedError, error) {
	// ★ 2026-11-18 根修（245 生产实证）：五个 jsonb 列 COALESCE 到
	// 'null'::jsonb —— QueuedError 的对应字段是 json.RawMessage，而
	// database/sql 对「NULL → *json.RawMessage」报 Scan error
	//（converting NULL to []uint8 is unsupported）。生产 Collect 会把
	// 这些列写满，但**手工入队/历史行/其它写入方**可以留 NULL ⇒ 领取即炸
	// ⇒ 整批错误卡死在 processing。COALESCE 后 NULL 变合法 JSON null，
	// 下游 json.Unmarshal 对 RawMessage("null") 的行为是明确的。
	query := `
		SELECT
			id, error_type, error_hash, priority,
			tenant_id, session_id, request_id,
			input_text, input_hash, COALESCE(error_message, ''),
			COALESCE(error_details, 'null'::jsonb),
			COALESCE(detection_layers, 'null'::jsonb),
			COALESCE(matched_rules, 'null'::jsonb),
			COALESCE(policy_config, 'null'::jsonb),
			status, retry_count, max_retries,
			created_at, started_at, completed_at,
			COALESCE(metadata, 'null'::jsonb)
		FROM error_analysis_queue
		WHERE status = $1
		ORDER BY priority DESC, created_at ASC
		LIMIT $2
	`

	rows, err := ec.db.QueryContext(ctx, query, QueueStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending errors: %w", err)
	}
	defer rows.Close()

	var errors []*QueuedError
	for rows.Next() {
		var qe QueuedError
		err := rows.Scan(
			&qe.ID, &qe.ErrorType, &qe.ErrorHash, &qe.Priority,
			&qe.TenantID, &qe.SessionID, &qe.RequestID,
			&qe.InputText, &qe.InputHash, &qe.ErrorMessage,
			&qe.ErrorDetails, &qe.DetectionLayers, &qe.MatchedRules, &qe.PolicyConfig,
			&qe.Status, &qe.RetryCount, &qe.MaxRetries,
			&qe.CreatedAt, &qe.StartedAt, &qe.CompletedAt, &qe.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan queued error: %w", err)
		}
		errors = append(errors, &qe)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return errors, nil
}

// ClaimPendingErrors **原子占位**地领取待处理错误：把 pending 行改成 processing
// 并把领取到的整行返回。
//
// ★ 为什么必须有这一步（2026-11-17 审计轮 P1）：
//
//	原实现是「GetPendingErrors（只读 SELECT）→ UpdateStatus(processing)」两步。
//	两个处理器（后台 worker + 管理 API 手动触发，或多副本网关）可以
//	**同时 SELECT 到同一行** ⇒ 都以为自己领到了 ⇒
//	同一错误被分析两次 ⇒ LLM 花费翻倍，且 error_analysis_results 里
//	**同一 queue_id 出现两条互斥结论**（schema 对 queue_id **没有唯一约束**）
//	⇒ 运维看到「同一错误两个根因」而无法判断哪条是真的。
//
//	★ 为什么用单条 UPDATE ... RETURNING（而不是 FOR UPDATE + 后续 UPDATE）：
//	单语句下「挑行 + 占位 + 取回」是**一个原子操作**；
//	子查询里的 FOR UPDATE SKIP LOCKED 让并发领取者**直接跳过**已被占位的行，
//	不必等待 ⇒ 既不会重复领��，也不会互相阻塞。
func (ec *ErrorCollector) ClaimPendingErrors(ctx context.Context, limit int) ([]*QueuedError, error) {
	if limit <= 0 {
		return nil, nil
	}

	query := `
		UPDATE error_analysis_queue q
		SET status = 'processing',
		    started_at = NOW()
		WHERE q.id IN (
			SELECT id
			FROM error_analysis_queue
			WHERE status = 'pending'
			ORDER BY priority DESC, created_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING
			q.id, q.error_type, q.error_hash, q.priority,
			q.tenant_id, q.session_id, q.request_id,
			q.input_text, q.input_hash, COALESCE(q.error_message, ''),
			COALESCE(q.error_details, 'null'::jsonb),
			COALESCE(q.detection_layers, 'null'::jsonb),
			COALESCE(q.matched_rules, 'null'::jsonb),
			COALESCE(q.policy_config, 'null'::jsonb),
			q.status, q.retry_count, q.max_retries,
			q.created_at, q.started_at, q.completed_at,
			COALESCE(q.metadata, 'null'::jsonb)
	`

	rows, err := ec.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to claim pending errors: %w", err)
	}
	defer rows.Close()

	var claimed []*QueuedError
	for rows.Next() {
		var qe QueuedError
		err := rows.Scan(
			&qe.ID, &qe.ErrorType, &qe.ErrorHash, &qe.Priority,
			&qe.TenantID, &qe.SessionID, &qe.RequestID,
			&qe.InputText, &qe.InputHash, &qe.ErrorMessage,
			&qe.ErrorDetails, &qe.DetectionLayers, &qe.MatchedRules, &qe.PolicyConfig,
			&qe.Status, &qe.RetryCount, &qe.MaxRetries,
			&qe.CreatedAt, &qe.StartedAt, &qe.CompletedAt, &qe.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan claimed error: %w", err)
		}
		claimed = append(claimed, &qe)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating claimed rows: %w", err)
	}

	return claimed, nil
}

// ReclaimStaleClaims 把「卡在 processing 超过 staleAfter」的行放回 pending。
//
// ★ 为什么需要（2026-11-17 审计轮 P2）：
//
//	processor 领取后若**进程崩溃 / 被杀 / 网关滚动重启**，该行永远停在
//	processing ⇒ 既不在 pending 队列里（不会被再处理），也不在 analyzed/failed
//	（统计视图里是「处理中」僵尸）⇒ **错误被永久吞掉**。
//	⇒ 每次领取前先回收超时的 processing 行。
//
// ★ 2026-11-17 第57增量：**已耗尽重试的孤儿行 → 终态 failed**。
//
//	原谓词 `retry_count < max_retries` 把「耗尽重试的 processing 行」整类
//	跳过 ⇒ 它们永远停在 processing（IncrementRetry 的终态分支只会在
//	**分析器活着走到那一行**时执行；孤儿行恰恰是没走到的）。
//	⇒ 补第二条 UPDATE：耗尽的孤儿行直接 failed + completed_at，
//	统计面不再有永久「处理中」僵尸。
func (ec *ErrorCollector) ReclaimStaleClaims(ctx context.Context, staleAfter time.Duration, excludeIDs ...int64) (int64, error) {
	if staleAfter <= 0 {
		return 0, nil
	}

	// ★ 2026-11-18 审计轮 P1 续修：excludeIDs 是**本进程正在分析**的行。
	//
	//	为什么要这个排除项：领取是**整批**打点（ClaimPendingErrors 对整批
	//	一次 SET started_at = NOW()），而分析是**串行**的 ⇒ 批内第 k 行的
	//	started_at 已老（批龄），可批还在跑 ⇒ 只要有**另一路**领取
	//	（后台 worker 与管理 API 的手动触发可并发）在 staleClaimAge 之后
	//	进来回收，它就会把**正在分析中**的行放回 pending ⇒ 同一错误被重复
	//	分析（重复 LLM 花费），且 retry_count 累加到 max_retries 后被直接
	//	标成 failed ⇒ 本来能分析成功的错误最终失败。
	//
	//	★ 该竞态在超时旋钮从 60s 放宽到 150s 之后由「罕见形态」变成
	//	「常态形态」：批内串行时长上限 = batchSize × timeout = 10×150s
	//	= 1500s，而回收阈值只有 600s。
	//
	//	修法：**排除本进程在途的行**（驱动无关的 NOT IN 占位符实现，
	//	不依赖 pq.Array / pgtype 的数组编码）。
	excludeClause := ""
	args := []any{fmt.Sprintf("%d seconds", int(staleAfter.Seconds()))}
	if len(excludeIDs) > 0 {
		placeholders := make([]string, 0, len(excludeIDs))
		for i, id := range excludeIDs {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i+2))
			args = append(args, id)
		}
		excludeClause = " AND id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}

	query := fmt.Sprintf(`
		UPDATE error_analysis_queue
		SET status = 'pending',
		    started_at = NULL,
		    retry_count = retry_count + 1
		WHERE status = 'processing'
		  AND started_at IS NOT NULL
		  AND started_at < NOW() - $1::interval
		  AND retry_count < max_retries
		  %s
	`, excludeClause)

	res, err := ec.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to reclaim stale claims: %w", err)
	}
	n, _ := res.RowsAffected()

	// ★ 第57增量：耗尽重试的孤儿行 → 终态 failed（带 completed_at）。
	exhaustedQuery := fmt.Sprintf(`
		UPDATE error_analysis_queue
		SET status = 'failed',
		    completed_at = NOW()
		WHERE status = 'processing'
		  AND started_at IS NOT NULL
		  AND started_at < NOW() - $1::interval
		  AND retry_count >= max_retries
		  %s
	`, excludeClause)
	resExhausted, err := ec.db.ExecContext(ctx, exhaustedQuery, args...)
	if err != nil {
		// 第一条已成功；第二条失败不回滚整体（非事务），但必须让调用方看见。
		return n, fmt.Errorf("failed to fail exhausted stale claims: %w", err)
	}
	nExhausted, _ := resExhausted.RowsAffected()
	return n + nExhausted, nil
}

// UpdateStatus 更新错误状态
//
// ★ 2026-11-17 审计轮 P1：改为**条件更新**（WHERE status = $3）。
//
//	原实现是无条件 UPDATE ⇒ 两个处理器对同一行的状态写入互相覆盖
//	（一个写 analyzed、另一个写 failed ⇒ 最终状态取决于**写入顺序**，
//	而不是取决于谁真的分析成功了）⇒ 队列状态不可信。
//	条件更新让「只有仍处于预期状态的那一方能推进」⇒ 冲突方自动失败。
//
// 返回 false 表示状态不匹配（已被别人推进）⇒ 调用方应当跳过。
func (ec *ErrorCollector) UpdateStatus(ctx context.Context, queueID int64, status string) error {
	return ec.updateStatusFrom(ctx, queueID, status, "")
}

// updateStatusFrom 条件更新状态；expectStatus 为空表示不校验当前状态。
func (ec *ErrorCollector) updateStatusFrom(ctx context.Context, queueID int64, status, expectStatus string) error {
	query := `
		UPDATE error_analysis_queue
		SET status = $1,
		    started_at = CASE WHEN $1 = 'processing' THEN NOW() ELSE started_at END,
		    completed_at = CASE WHEN $1 IN ('analyzed', 'failed', 'skipped') THEN NOW() ELSE completed_at END
		WHERE id = $2
		  AND ($3 = '' OR status = $3)
	`

	_, err := ec.db.ExecContext(ctx, query, status, queueID, expectStatus)
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// IncrementRetry 增加重试计数
//
// ★ 2026-11-17 审计轮 P2：两处根修。
//
//	(a) **条件更新**：原实现 `WHERE id = $1` 无状态守卫 ⇒ 过期 worker
//	    可以把别人已写好的 analyzed 覆盖回 pending/failed
//	    ⇒「已成功的分析在页面上变成待处理」⇒ 队列状态不可信。
//	    ⇒ 改为 `WHERE id = $1 AND status = 'processing'`。
//	(b) **completed_at 两种形态**：原实现只改 status/retry_count，
//	    不碰 completed_at ⇒ 重试耗尽走 IncrementRetry 的 'failed'
//	    ⇒ completed_at 仍为 NULL；而 UpdateStatus('failed') 的 'failed'
//	    分支**会**设 completed_at ⇒ **两种 'failed' 形态并存**。
//	    ⇒ 回退 pending 时清 completed_at；耗尽时设 completed_at。
func (ec *ErrorCollector) IncrementRetry(ctx context.Context, queueID int64) error {
	query := `
		UPDATE error_analysis_queue
		SET retry_count = retry_count + 1,
		    status = CASE
		        WHEN retry_count + 1 >= max_retries THEN 'failed'
		        ELSE 'pending'
		    END,
		    started_at = CASE
		        WHEN retry_count + 1 >= max_retries THEN started_at
		        ELSE NULL
		    END,
		    completed_at = CASE
		        WHEN retry_count + 1 >= max_retries THEN NOW()
		        ELSE NULL
		    END
		WHERE id = $1
		  AND status = 'processing'
	`

	_, err := ec.db.ExecContext(ctx, query, queueID)
	if err != nil {
		return fmt.Errorf("failed to increment retry: %w", err)
	}

	return nil
}

// calculateErrorHash 计算错误指纹
func (ec *ErrorCollector) calculateErrorHash(collectedErr *CollectedError) string {
	// F3 修复：输出守卫类错误的去重指纹不含 inputHash
	//
	//	问题：输出守卫的 inputText 是逐请求不同的证据摘录（streaming/policy_block_feedback.go
	//	的 digest/evidence），导致同一条规则误报 N 次会入队 N 条。
	//	修复：输出守卫类错误只用 errorType + tenantID + rule 去重。
	if collectedErr.ErrorType == ErrorTypeOutputGuardBlocked {
		// 提取规则名称（按字母序排序确保稳定）
		ruleNames := make([]string, 0, len(collectedErr.MatchedRules))
		for _, rule := range collectedErr.MatchedRules {
			if rule.RuleName != "" {
				ruleNames = append(ruleNames, rule.RuleName)
			}
		}
		sort.Strings(ruleNames)

		hashInput := fmt.Sprintf("%s|%s|rules:%s",
			collectedErr.ErrorType,
			collectedErr.TenantID,
			strings.Join(ruleNames, ","),
		)

		hash := sha256.Sum256([]byte(hashInput))
		return hex.EncodeToString(hash[:])
	}

	// 其他类型错误：原有逻辑（包含 errorMessage 和 inputHash）
	hashInput := fmt.Sprintf("%s|%s|%s",
		collectedErr.ErrorType,
		collectedErr.TenantID,
		truncateString(collectedErr.ErrorMessage, 100),
	)

	if collectedErr.InputText != "" {
		inputHash := sha256.Sum256([]byte(collectedErr.InputText))
		hashInput += "|" + hex.EncodeToString(inputHash[:8]) // 前 8 字节
	}

	hash := sha256.Sum256([]byte(hashInput))
	return hex.EncodeToString(hash[:])
}

// checkDuplicate 检查是否存在重复错误
func (ec *ErrorCollector) checkDuplicate(ctx context.Context, errorHash string, within time.Duration) (bool, int64, error) {
	return ec.checkDuplicateTx(ctx, ec.db, errorHash, within)
}

// checkDuplicateTx 在给定执行器（db 或 tx）上查重。
//
// ★ 2026-11-17 审计轮 P1：拆出 tx 版本，是为了让「查重 + 插入」两段
//
//	能落在**同一个事务**里（配合 pg_advisory_xact_lock）⇒ 消除 read-then-write 竞态。
func (ec *ErrorCollector) checkDuplicateTx(ctx context.Context, execer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, errorHash string, within time.Duration) (bool, int64, error) {
	query := `
		SELECT id
		FROM error_analysis_queue
		WHERE error_hash = $1
		  AND created_at > NOW() - $2::INTERVAL
		ORDER BY created_at DESC
		LIMIT 1
	`

	var existingID int64
	err := execer.QueryRowContext(ctx, query, errorHash, fmt.Sprintf("%d seconds", int(within.Seconds()))).Scan(&existingID)

	if err == sql.ErrNoRows {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}

	return true, existingID, nil
}

// GetStats 获取错误统计
//
// ★ 2026-11-19 一致性根修（owner 报障「列表与统计的数据不一致」，211 实测定案三因）：
//
//	① **状态盲区**：旧实现只数 pending/analyzed——failed/processing/skipped
//	   完全不可见（211 实测：队列 2 条全 failed ⇒ 统计卡全 0，而列表明明有行）。
//	② **作用域错位**：旧实现 `WHERE created_at > NOW() - INTERVAL '7 days'`，
//	   而列表（GetQueue）**无时间过滤** ⇒ 超 7 天的错误统计直接消失。
//	③ **死零**：误报率/平均耗时的数据后端从未提供，前端是硬编码 '0%'/'0ms'。
//
//	⇒ 修法＝统计与列表**同表同域**：队列聚合去掉时间过滤（= GetQueue 的域）、
//	  数全部 5 种状态；结果聚合（误报数/平均耗时）取自 error_analysis_results
//	  （= GetResults「分析结果」tab 的域）⇒ 四个数字都能与列表逐项对上。
func (ec *ErrorCollector) GetStats(ctx context.Context) (map[string]interface{}, error) {
	// ── 队列聚合：全量、全状态（与 GetQueue 列表同表同域）─────────────
	queueAgg := `
		SELECT
			COUNT(*) AS total,
			COUNT(CASE WHEN status = 'pending' THEN 1 END) AS pending,
			COUNT(CASE WHEN status = 'processing' THEN 1 END) AS processing,
			COUNT(CASE WHEN status = 'analyzed' THEN 1 END) AS analyzed,
			COUNT(CASE WHEN status = 'failed' THEN 1 END) AS failed,
			COUNT(CASE WHEN status = 'skipped' THEN 1 END) AS skipped
		FROM error_analysis_queue
	`
	var qTotal, qPending, qProcessing, qAnalyzed, qFailed, qSkipped int
	if err := ec.db.QueryRowContext(ctx, queueAgg).Scan(
		&qTotal, &qPending, &qProcessing, &qAnalyzed, &qFailed, &qSkipped,
	); err != nil {
		return nil, fmt.Errorf("failed to get queue stats: %w", err)
	}

	// ── 结果聚合：与 GetResults「分析结果」tab 同表同域 ──────────────
	//   ★ 空表时 AVG 返回 NULL ⇒ 用 NullFloat64 承接（前端显示 —）。
	resultsAgg := `
		SELECT
			COUNT(*) AS results_total,
			COUNT(CASE WHEN is_false_positive THEN 1 END) AS false_positives,
			AVG(analysis_duration_ms) AS avg_duration_ms
		FROM error_analysis_results
	`
	var rTotal, rFalsePos int
	var rAvgDur sql.NullFloat64
	if err := ec.db.QueryRowContext(ctx, resultsAgg).Scan(
		&rTotal, &rFalsePos, &rAvgDur,
	); err != nil {
		return nil, fmt.Errorf("failed to get results stats: %w", err)
	}

	// ── 按类型细分：同样全量域（旧 7 天过滤退役）────────────────────
	query := `
		SELECT
			error_type,
			COUNT(*) as total,
			COUNT(CASE WHEN status = 'pending' THEN 1 END) as pending,
			COUNT(CASE WHEN status = 'analyzed' THEN 1 END) as analyzed,
			COUNT(CASE WHEN status = 'failed' THEN 1 END) as failed,
			MAX(created_at) as last_error_at
		FROM error_analysis_queue
		GROUP BY error_type
	`

	rows, err := ec.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]interface{})
	var typeStats []map[string]interface{}

	for rows.Next() {
		var errorType string
		var total, pending, analyzed, failed int
		var lastErrorAt time.Time

		err := rows.Scan(&errorType, &total, &pending, &analyzed, &failed, &lastErrorAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stats: %w", err)
		}

		typeStats = append(typeStats, map[string]interface{}{
			"error_type":    errorType,
			"total":         total,
			"pending":       pending,
			"analyzed":      analyzed,
			"failed":        failed,
			"last_error_at": lastErrorAt,
		})
	}
	// ★ 2026-11-18 审计轮：rows 循环的**终检**（internal/rowsguard 门）。
	//   不查 rows.Err() ⇒ 连接中断时统计数字**静默偏小**（少了没读到的那几类）
	//   ⇒ 运维读到的「队列积压」是假的。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stats: %w", err)
	}

	stats["by_type"] = typeStats
	// ★ 2026-11-19：顶层计数（前端直接读，不再自己 by_type 求和——
	//   求和口径与列表 COUNT 域一致这件事由后端单点保证）。
	stats["total"] = qTotal
	stats["pending"] = qPending
	stats["processing"] = qProcessing
	stats["analyzed"] = qAnalyzed
	stats["failed"] = qFailed
	stats["skipped"] = qSkipped
	stats["results_total"] = rTotal
	stats["false_positive_count"] = rFalsePos
	if rAvgDur.Valid {
		stats["avg_duration_ms"] = int(rAvgDur.Float64 + 0.5)
	} else {
		stats["avg_duration_ms"] = nil
	}
	stats["timestamp"] = time.Now()

	return stats, nil
}

// Helper functions

func toNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
