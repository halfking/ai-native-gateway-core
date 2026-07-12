package sessionforensics

import (
	"time"
)

// SessionMeta 公共会话元信息（覆盖 admin/session_export.go 的 SessionExportMeta，
// 但是 export schema 完全对齐以便兼容）。
type SessionMeta struct {
	ID         string `json:"id"`                  // gw_session_id (gateway-prefixed UUID)
	Title      string `json:"title,omitempty"`     // 高精度 LLM 标题
	Directory  string `json:"directory,omitempty"` // 工作目录
	Instance   string `json:"instance,omitempty"`  // 来源 host:port
	TaskID     string `json:"taskId,omitempty"`
	TenantID   string `json:"tenant_id,omitempty"` // 显式包含 tenant_id（与 export 区分）
	Source     string `json:"source,omitempty"`    // "prod" / "local" / "ci" 等
	ExportedAt string `json:"exported_at,omitempty"`
}

// ResumeBrief 语义层会话简报（与 Pocket model.SessionResumeBrief + 现有
// admin/session_export.go SessionResumeBrief 对齐）。
type ResumeBrief struct {
	CurrentState  string   `json:"currentState,omitempty"`
	LastObjective string   `json:"lastObjective,omitempty"`
	Decisions     []string `json:"decisions,omitempty"`
	ChangedFiles  []string `json:"changedFiles,omitempty"`
	Blockers      []string `json:"blockers,omitempty"`
	NextAction    string   `json:"nextAction,omitempty"`
}

// ExportMessage 单条消息（对话累积后的视图）。
//
// Content 是 message 的可读内容；当从 admin 导出时是 response_body 或
// request_body 的反序列化 JSON（json.RawMessage），从 extract.py 导出时
// 是完整 request_body 的 JSON 对象（即 Content 本身就是一个 chat completions
// 请求体）。
type ExportMessage struct {
	Turn                int            `json:"turn"`
	Role                string         `json:"role"`
	Content             string         `json:"content"`
	ParentRequestID     string         `json:"parent_request_id,omitempty"`
	CompressionReason   string         `json:"compression_reason,omitempty"`
	CompressionStrategy string         `json:"compression_strategy,omitempty"`
	CompressionMeta     map[string]any `json:"compression_meta,omitempty"`
	CreatedAt           string         `json:"created_at,omitempty"`
}

// ExportAttachment 附件元数据（与 admin 对齐）。
type ExportAttachment struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
	Size int64  `json:"size,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// SessionPack 一个完整会话的迁移包（含 messages + summary + title）。
// 与 admin/session_export.go 的 SessionExport 结构字段命名一致，方便
// `json.Marshal` 后直接 POST 到 admin 的 /import 端点。
type SessionPack struct {
	SessionMeta SessionMeta        `json:"session_meta"`
	ResumeBrief ResumeBrief        `json:"resume_brief"`
	Messages    []ExportMessage    `json:"messages"`
	Attachments []ExportAttachment `json:"attachments"`
	Summary     string             `json:"summary,omitempty"`
	ExportedAt  string             `json:"exported_at,omitempty"`
}

// ReplayStep 单轮回放的指标（与 tests/session_replay.ReplayStep 对齐，但放
// 在正式的 sessionforensics 包以便 admin 后端能直接复用）。
type ReplayStep struct {
	Turn                 int    `json:"turn"`
	RequestID            string `json:"request_id"`
	Ts                   string `json:"ts"`
	Model                string `json:"model"`
	MsgCountIn           int    `json:"msg_count_in"`
	MsgCountOut          int    `json:"msg_count_out"`
	BytesBefore          int    `json:"bytes_before"`
	BytesAfter           int    `json:"bytes_after"`
	CompressionStrategy  string `json:"compression_strategy"`
	WindowTriggered      string `json:"window_triggered,omitempty"`
	SummaryMarker        string `json:"summary_marker,omitempty"`
	Degraded             bool   `json:"degraded"`
	Lossiness            string `json:"lossiness"`
	ToolsCachedHit       bool   `json:"tools_cached_hit"`
	CacheTier            string `json:"cache_tier"`
	CacheLatencyMicrosec int64  `json:"cache_latency_us"`
	OutboundBodySize     int    `json:"outbound_body_bytes"`
}

// ReplayAggregate 多轮回放后的聚合。
type ReplayAggregate struct {
	TotalTurns           int            `json:"total_turns"`
	StrategyCounts       map[string]int `json:"strategy_counts"`
	LossinessCounts      map[string]int `json:"lossiness_counts"`
	CacheTierCounts      map[string]int `json:"cache_tier_counts"`
	MaxBytesBefore       int            `json:"max_bytes_before"`
	MaxBytesAfter        int            `json:"max_bytes_after"`
	MaxCompressionRatio  float64        `json:"max_compression_ratio"`
	AvgCompressionRatio  float64        `json:"avg_compression_ratio"`
	WindowTriggeredCount int            `json:"window_triggered_count"`
	SummaryMarkerCount   int            `json:"summary_marker_count"`
	ToolsCachedHitCount  int            `json:"tools_cached_hit_count"`
	ReplayedAt           string         `json:"replayed_at,omitempty"`
}

// ReplayReport 整个会话的回放报告。
type ReplayReport struct {
	SessionID  string          `json:"session_id"`
	Label      string          `json:"label,omitempty"`
	TenantID   string          `json:"tenant_id"`
	Mode       string          `json:"mode,omitempty"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"finished_at"`
	Steps      []ReplayStep    `json:"steps"`
	Aggregate  ReplayAggregate `json:"aggregate"`
}

// SummaryResult 标题/摘要生成的结果。
type SummaryResult struct {
	SessionID   string    `json:"session_id"`
	Title       string    `json:"title"`   // 高精度 LLM 标题（可能为 "" 表示 fallback）
	Summary     string    `json:"summary"` // 完整摘要
	KeyTopics   []string  `json:"key_topics,omitempty"`
	UserIntent  string    `json:"user_intent,omitempty"`
	Source      string    `json:"source"` // "llm" | "fallback" | "preview" | "existing"
	GeneratedAt time.Time `json:"generated_at"`
	TokensUsed  int       `json:"tokens_used,omitempty"`
	Error       string    `json:"error,omitempty"` // 失败原因（fallback 模式下为空）
}

// SessionAudit 是会话质量评估报告（每个 session 一份）。
// 由 audit.go 计算；用途：在 admin web 上快速看到"哪些会话的压缩没触发"，
// "哪些会话缺 session_id" 等运维指标。
type SessionAudit struct {
	SessionID         string    `json:"session_id"`
	HasCompression    bool      `json:"has_compression"`  // 任何 turn 触发了 compression
	CompressionHits   int       `json:"compression_hits"` // 触发 compression 的轮次
	TotalTurns        int       `json:"total_turns"`
	HasSessionID      bool      `json:"has_session_id"`      // false 表示 request_logs.gw_session_id 为 NULL
	MissingSessionIDs int       `json:"missing_session_ids"` // NULL 计数
	HasTitle          bool      `json:"has_title"`
	HasSummary        bool      `json:"has_summary"`
	ModelsUsed        []string  `json:"models_used"`
	TotalPromptTokens int       `json:"total_prompt_tokens"`
	TotalRespTokens   int       `json:"total_resp_tokens"`
	TotalCostUSD      float64   `json:"total_cost_usd"`
	EarliestAt        string    `json:"earliest_at,omitempty"`
	LatestAt          string    `json:"latest_at,omitempty"`
	AuditAt           time.Time `json:"audit_at"`
}
