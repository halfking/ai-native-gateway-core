package admin

// 会话存储解耦 v3 审计（2026-10-02）：request_logs_with_current_month 契约之外、
// 只在物理 request_logs 上存在的列。
//
// 放在非 test 文件里是因为两道门都要用同一份清单，build tag 不能把它们隔开：
//   - TestNoPhysicalOnlyColumnsInViewSourcedSQL（!integration，静态、逐字面量）
//   - TestPhysicalOnlyColumnsListMatchesRealDatabase（integration，真库反向校验）
//
// physicalOnlyRequestLogColumns 是在 request_logs 上存在、但在视图契约上
// **不存在**的列。清单由 integration 那道门从 information_schema 重新推导并
// 逐列核对，所以视图一旦新增其中任一列、或 request_logs 删掉其中任一列，
// 那道门就会红并要求更新。
//
// 815（2026-10-02，审计 §9.22）把 origin_stage / token_band /
// client_forwarded_for 移出本清单：它们进了视图契约 ⇒ 视图读方读这三列不再是
// 越列，compression_stats 的 token_band 聚合也随之从「每调 42703」恢复。
//
// ── 一条不能跟着清单一起想当然的推论 ────────────────────────────────────
// 「某列被移出清单」只说明它不再是 42703，**不**说明视图读方可以改用物理表版
// 谓词 probeTrafficExclusionPredicate。该谓词两臂是
// quality_flags + origin_stage：origin_stage 现在在契约内（不报错），而
// quality_flags 在 session 分臂由 details 层供值、缺行时为 NULL ⇒
// `NOT COALESCE('probe' = ANY(NULL), FALSE)` 求值为 TRUE，于是 session 分臂的
// 探测流量**不再被排除**，INV-3 静默泄漏（探测被当成用量，扫描器反复重探）。
// 这是「修好了 500，却换了一个更安静的洞」。禁令由
// TestNoPhysicalPredicateOnViewSource 单独守，不依赖本清单。
var physicalOnlyRequestLogColumns = []string{
	"api_key_fingerprint", "audio_tokens", "billed_despite_cancellation",
	"cache_hit", "cache_tokens_saved", "cached_response_id",
	"compression_end_index", "compression_ratio",
	"compression_start_index", "content_safety_score", "context_size_tokens",
	"continuation_keywords", "discard_events", "dlp_violations",
	"effective_timeout_seconds", "image_tokens", "ir_extensions",
	"is_continuation", "is_terminal", "keepalive_sent_count",
	"node_switch_count", "outbound_body",
	"protocol_conversion", "provider_tokens", "rate_limit_status",
	"reasoning_tokens", "request_depth", "sanitizer_mutations",
	"sensitive_keywords", "session_summary", "session_title", "task_id",
	"task_title", "timeout_mode",
	// trace_events 留在清单里，且**不是**因为「有 session 侧同名列」就天然安全：
	// 它在 session_turns 上存在，但镜像从不写它（近 1/7/30 天非空率恒为 0），
	// 而 v1 侧有 691,883 行带值、且这些行已被反连接丢弃 ⇒ 把它投影进视图是
	// **净数据损失**（691,883 行真实值变成 NULL，而接口照样 200）。
	// 正解是先让镜像写该列再投影（§9.22 遗留项），不是把它塞进契约。
	"trace_events",
	"upstream_endpoint", "upstream_protocol", "vendor_metadata", "video_tokens",
}
