// Package bg/systemmonitor — types.go
//
// 系统监测模块（System Monitor）的核心类型定义。
//
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3 / §4
// Phase 1 — 仅落地 6 种 task_type、automaticity、5min 跳过规则的最小骨架。
//
// KEEP: 5min 自动跳过规则 — 保留至 Phase 2 完成、监控指标验证跳过率 >= 30% 后再讨论是否保留 [@monitoring] [review 2026-Q3]
// FUTURE: 多租户隔离 — 当前所有任务归 'default' tenant, 待 multi-tenant 改造时按 tenant_id 路由 [@platform] [trigger 2027-Q1]
// FUTURE: chat_stream 探测 — 当前 SSE 流式上游尚未普适，先不实现 [trigger 2027-Q1]
package systemmonitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TaskType 六种原子探测类型。
//
// 命名规则：与 docs/会话优化v2/32-系统监测模块设计.md §4.1 严格一致。
// 任何不在该枚举内的 task_type 必须走 ErrInvalidTaskType 拒绝（rule 38 §4.4）。
type TaskType string

const (
	TaskTypeDirectPing  TaskType = "direct_ping"  // 直连上游 chat ping
	TaskTypeGatewayPing TaskType = "gateway_ping" // 通过本网关 chat ping
	TaskTypeChatMinimal TaskType = "chat_minimal" // 最小 chat（max_tokens=1）
	TaskTypeChatTool    TaskType = "chat_tool"    // chat + 单 tool_call
	TaskTypeChatStream  TaskType = "chat_stream"  // 流式 chat（SSE 上游）
	TaskTypeHTTPPing    TaskType = "http_ping"    // HTTP 层网络探测，不消耗 quota
)

// Valid reports whether t is one of the six enumerated task types.
func (t TaskType) Valid() bool {
	switch t {
	case TaskTypeDirectPing, TaskTypeGatewayPing, TaskTypeChatMinimal,
		TaskTypeChatTool, TaskTypeChatStream, TaskTypeHTTPPing:
		return true
	}
	return false
}

// Priority 任务优先级（数值越大越优先）。
//
// 设计: rule 38 §4.4 + design §4.1
//
//	http_ping     = 90  (网络层预检，不消耗 quota)
//	chat_minimal  = 60  (最快 chat 探测)
//	direct_ping   = 50  (直连上游)
//	gateway_ping  = 40  (通过网关)
//	chat_tool     = 30  (tool 调用)
//	chat_stream   = 20  (SSE 流式)
func (t TaskType) Priority() int16 {
	switch t {
	case TaskTypeHTTPPing:
		return 90
	case TaskTypeChatMinimal:
		return 60
	case TaskTypeDirectPing:
		return 50
	case TaskTypeGatewayPing:
		return 40
	case TaskTypeChatTool:
		return 30
	case TaskTypeChatStream:
		return 20
	}
	return 50 // 未识别类型走 default
}

// Automaticity 自动性：mandatory（强制）vs automatic（自动）。
//
// mandatory:  手工触发（仪表盘按钮）或错误触发（NodeProbeWorker.Submit）
// automatic:  周期触发（credential_selfcheck 24h、asset_health 1h、watchdog 6h）
//
// 行为差异：automatic 任务执行前会查 5min 内最近请求成功标记，决定跳过
// 还是继续；mandatory 任务永远执行（design §4.3）。
type Automaticity string

const (
	AutomaticityMandatory Automaticity = "mandatory"
	AutomaticityAutomatic Automaticity = "automatic"
)

// Valid reports whether a is one of the two enumerated automaticities.
func (a Automaticity) Valid() bool {
	return a == AutomaticityMandatory || a == AutomaticityAutomatic
}

// Source 任务来源（仪表盘 / 周期 / 错误触发 / fallback 等）。
//
// 命名规则：snake_case，长度 ≤ 32，唯一来源由 Submit() 调用方传入。
type Source string

const (
	SourceButtonAll          Source = "button_all"           // 仪表盘"开始全部任务"
	SourceButtonByCredential Source = "button_by_credential" // 按凭据
	SourceButtonByProvider   Source = "button_by_provider"   // 按供应商
	SourceButtonByModel      Source = "button_by_model"      // 按模型
	SourceNodeProbe          Source = "node_probe"           // NodeProbeWorker 错误触发
	SourceActiveProbe        Source = "active_probe"         // ActiveProbeWorker 连续失败
	SourceSelfcheckTick      Source = "selfcheck_tick"       // CredentialSelfcheck 周期
	SourceAssetHealth        Source = "asset_health"         // AssetHealthProbe 周期
	SourceWatchdog           Source = "watchdog"             // 6h 无请求 watchdog
	SourceErrorFallback      Source = "error_fallback"       // direct 失败 → http_ping
)

// TaskStatus 任务执行状态（与 system_probe_runs.status 严格一致）。
type TaskStatus string

const (
	TaskStatusReady        TaskStatus = "ready"
	TaskStatusRunning      TaskStatus = "running"
	TaskStatusSuccess      TaskStatus = "success"
	TaskStatusFailed       TaskStatus = "failed"
	TaskStatusExpired      TaskStatus = "expired"
	TaskStatusSkipped      TaskStatus = "skipped"
	TaskStatusTimeout      TaskStatus = "timeout"
	TaskStatusNetworkError TaskStatus = "network_error"
)

// IsTerminal reports whether the status is a terminal state (worker loop should not re-enqueue).
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case TaskStatusSuccess, TaskStatusFailed, TaskStatusExpired,
		TaskStatusSkipped, TaskStatusTimeout, TaskStatusNetworkError:
		return true
	}
	return false
}

// SkipReason 自动任务被跳过的原因。
type SkipReason string

const (
	SkipReasonRecentRequestSuccess SkipReason = "recent_request_success" // 5min 内同节点真实请求成功
	SkipReasonInflightDedup        SkipReason = "inflight_dedup"         // 30s 内同节点探测中
	SkipReasonMaxAttemptsReached   SkipReason = "max_attempts_reached"   // 超过最大重试次数
)

// Task 系统监测模块的核心任务结构。
//
// 该结构在 Redis 中以 Hash 形式存储于 llmgw:monitor:tasks:{id}，
// 在 PG 中以审计行写入 system_probe_runs。
//
// 字段选择遵循 rule 38 §4.4 (每个字段必有 Purpose)，命名沿用
// 现有 credential_probe_queue + node_probe_runs 的 snake_case 习惯，
// 以便未来合并查询（双写兼容）。
type Task struct {
	// 身份
	ID           int64        `json:"id"`           // Redis INCR 生成
	TaskType     TaskType     `json:"task_type"`    // 6 种 task_type
	Automaticity Automaticity `json:"automaticity"` // mandatory/automatic
	Source       Source       `json:"source"`       // 来源标识

	// 目标
	CredentialID int64  `json:"credential_id"`
	ProviderID   int64  `json:"provider_id"`
	RawModel     string `json:"raw_model"`

	// 时间控制
	EnqueuedAt  time.Time  `json:"enqueued_at"`           // 入队时间
	ScheduledAt time.Time  `json:"scheduled_at"`          // 计划执行时间（即时任务 = EnqueuedAt）
	NextRunAt   time.Time  `json:"next_run_at"`           // 下次执行（回退任务）
	StartedAt   *time.Time `json:"started_at,omitempty"`  // 当前轮次开始时间
	FinishedAt  *time.Time `json:"finished_at,omitempty"` // 当前轮次结束时间
	Attempt     int        `json:"attempt"`               // 已尝试次数
	MaxAttempts int        `json:"max_attempts"`          // 上限

	// 关联
	ParentRequestID string `json:"parent_request_id,omitempty"` // 父请求 ID（错误触发时携带）

	// 执行上下文（worker 写入）
	WorkerID  string     `json:"worker_id,omitempty"`  // 抢占该任务的 worker 标识
	Status    TaskStatus `json:"status"`               // 当前状态
	ClaimedAt *time.Time `json:"claimed_at,omitempty"` // worker 抢占时间（用于心跳恢复）

	// 上次执行结果（用于 SSE 广播 + 审计）
	HTTPStatus      *int       `json:"http_status,omitempty"`
	LatencyMs       *int       `json:"latency_ms,omitempty"`
	ErrCode         string     `json:"err_code,omitempty"`
	ErrDetail       string     `json:"err_detail,omitempty"`
	TokenCount      int        `json:"token_count,omitempty"`
	SkipReason      SkipReason `json:"skip_reason,omitempty"`
	RecentRequestID string     `json:"recent_request_id,omitempty"`
	RecentRequestAt *time.Time `json:"recent_request_at,omitempty"`
}

// Validate runs invariant checks before Submit(). Required fields:
//
//	TaskType, Automaticity, CredentialID > 0, RawModel != "".
func (t *Task) Validate() error {
	if t == nil {
		return errors.New("task is nil")
	}
	if !t.TaskType.Valid() {
		return fmt.Errorf("invalid task_type: %q", t.TaskType)
	}
	if !t.Automaticity.Valid() {
		return fmt.Errorf("invalid automaticity: %q", t.Automaticity)
	}
	if t.CredentialID <= 0 {
		return fmt.Errorf("credential_id must be > 0, got %d", t.CredentialID)
	}
	if t.RawModel == "" {
		return errors.New("raw_model must not be empty")
	}
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = defaultMaxAttempts(t.Automaticity)
	}
	if t.Attempt < 0 {
		t.Attempt = 0
	}
	return nil
}

// defaultMaxAttempts 按 automaticity 返回默认上限。
//
//	mandatory  → 3 次（错误恢复需要多次尝试）
//	automatic  → 1 次（避免 watchdog 永远自循环）
//
// 设计: docs/会话优化v2/32-系统监测模块设计.md §4.5
func defaultMaxAttempts(a Automaticity) int {
	switch a {
	case AutomaticityMandatory:
		return 3
	case AutomaticityAutomatic:
		return 1
	}
	return 1
}

// DefaultMaxAttempts returns the default max_attempts for the task's automaticity.
// Exposed so callers (e.g. background schedulers) can align their retry policy
// without re-implementing the rule.
func (t *Task) DefaultMaxAttempts() int { return defaultMaxAttempts(t.Automaticity) }

// HashKey returns the Redis Hash key for storing this task.
//
// Format: llmgw:monitor:tasks:{id}
//
// Keep in sync with design §3.1.
func (t *Task) HashKey() string {
	return fmt.Sprintf("llmgw:monitor:tasks:%d", t.ID)
}

// InflightKey returns the per-node dedup token key.
//
// Format: llmgw:monitor:inflight:{credential_id}:{raw_model}
//
// TTL: 30s (design §3.1). Key collision with raw_model containing colons
// is acceptable because the value is always the worker_id / 'skipped',
// never interpreted by downstream tools.
func (t *Task) InflightKey() string {
	return fmt.Sprintf("llmgw:monitor:inflight:%d:%s", t.CredentialID, t.RawModel)
}

// RecentSuccessKey returns the recent-success marker key for the 5-min skip rule.
//
// Format: llmgw:monitor:node:recent_success:{credential_id}:{raw_model}
//
// TTL: 300s (design §3.1).
func (t *Task) RecentSuccessKey() string {
	return fmt.Sprintf("llmgw:monitor:node:recent_success:%d:%s", t.CredentialID, t.RawModel)
}

// MarshalJSON validates before serializing. Used by the Redis Hash codec.
//
// We avoid storing the InflightKey / HashKey inside the JSON body because
// they are derived from the identity and would just bloat the wire size.
func (t Task) MarshalJSON() ([]byte, error) {
	type alias Task
	return json.Marshal(alias(t))
}
