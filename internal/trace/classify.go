package trace

// ClassifyFailureToStage 把已知的 error_kind 字符串映射到 trace 阶段。
//
// 这是个 best-effort 映射: 不在表内的 code 落回 request_complete 阶段
// (前端在 request_complete 行的 snapshot.failure_hint 看到原始 kind)。
//
// 保持与 domains/streaming/handler.go 既有 classifyFailureStage (gateway/upstream)
// 解耦: trace 关心"发生在哪一步", 而非"是否经过上游"。
func ClassifyFailureToStage(errCode string) Stage {
	switch errCode {
	// Auth / pre-routing
	case "missing_key", "invalid_key", "auth_unavailable", "auth_error":
		return StageAuthenticate
	case "rate_limit", "rate_limit_exceeded", "concurrent_limit_exceeded",
		"tpm_limit_exceeded", "key_throttled", "budget_exhausted",
		"insufficient_credits":
		return StageRateLimit
	// Body
	case "body_read_error", "body_too_large", "json_parse_error",
		"conversion_error", "missing_model", "missing_max_tokens",
		"chat_to_anthropic_conversion_error":
		return StageBodyParse
	// Session
	case "session_forbidden", "session_crosstalk":
		return StageSessionLookup
	// Routing
	case "no_candidate", "executor_unavailable", "auto_route_decider_failed":
		return StageRouteResolve
	// Upstream
	case "upstream_error", "upstream_5xx", "upstream_4xx",
		"connection_reset", "hangup", "write_failed",
		"provider_error", "model_not_found",
		"upstream_credential_invalid", "upstream_credential_revoked",
		"upstream_quota_periodic", "upstream_quota_permanent",
		"upstream_overloaded":
		return StageUpstreamRequest
	// Stream
	case "stream_error", "stream_timeout", "eof_without_done",
		"no_deltas", "invalid_first_chunk", "invalid_json",
		"eof_mid_tool_call", "first_byte_timeout":
		return StageStreamComplete
	case "client_cancel", "client_disconnected", "canceled":
		return StageRequestComplete
	default:
		return StageRequestComplete
	}
}
