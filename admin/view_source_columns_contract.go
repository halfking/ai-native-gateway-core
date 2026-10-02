package admin

// 会话存储解耦 v3 审计（2026-10-02）：request_logs_with_current_month 的冻结
// 113 列契约之外、只在物理 request_logs 上存在的列。
//
// 放在非 test 文件里是因为两道门都要用同一份清单，build tag 不能把它们隔开：
//   - TestNoPhysicalOnlyColumnsInViewSourcedSQL（!integration，静态、逐字面量）
//   - TestPhysicalOnlyColumnsListMatchesRealDatabase（integration，真库反向校验）
//
// 真值由 integration 那道门从 information_schema 重新推导并逐列核对，所以这份
// 清单不会悄悄过期：视图一旦新增其中任一列，或 request_logs 删掉其中任一列，
// 那道门就会红并要求更新。
// physicalOnlyRequestLogColumns 是在 request_logs 上存在、但在
// request_logs_with_current_month 冻结契约上**不存在**的列（真库
// information_schema 差集，2026-10-02 实测 41 列）。
//
// 它是硬编码的，所以由 TestPhysicalOnlyColumnsListMatchesRealDatabase
// （integration tag）在真库上反向校验：每一列都必须在 request_logs 上存在、
// 且在视图上不存在；视图若新增了其中任一列，这道门会逼人来更新清单。
var physicalOnlyRequestLogColumns = []string{
	"api_key_fingerprint", "audio_tokens", "billed_despite_cancellation",
	"cache_hit", "cache_tokens_saved", "cached_response_id",
	"client_forwarded_for", "compression_end_index", "compression_ratio",
	"compression_start_index", "content_safety_score", "context_size_tokens",
	"continuation_keywords", "discard_events", "dlp_violations",
	"effective_timeout_seconds", "image_tokens", "ir_extensions",
	"is_continuation", "is_terminal", "keepalive_sent_count",
	"node_switch_count", "origin_stage", "outbound_body",
	"protocol_conversion", "provider_tokens", "rate_limit_status",
	"reasoning_tokens", "request_depth", "sanitizer_mutations",
	"sensitive_keywords", "session_summary", "session_title", "task_id",
	"task_title", "timeout_mode", "token_band", "trace_events",
	"upstream_endpoint", "upstream_protocol", "vendor_metadata", "video_tokens",
}
