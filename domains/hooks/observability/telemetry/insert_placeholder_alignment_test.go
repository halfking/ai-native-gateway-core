package telemetry

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestInsertRequestLogPlaceholderAlignment guards insertRequestLog's
// column-list / VALUES / Go-arg triple alignment.
//
// 2026-08-25 incident (154/245): the INSERT column list gained token_band and
// customer_id while the VALUES segment and the Go arg list were renumbered
// independently. $66::text[] (quality_flags) ended up receiving
// jsonOrNull(OutboundMsgHashes) — every telemetry INSERT died with
// SQLSTATE 22P02 "malformed array literal" and request logs stopped saving.
// This test parses client.go itself and fails on any positional or semantic
// drift, so renumbering mistakes surface in CI instead of in production.
func TestInsertRequestLogPlaceholderAlignment(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	fn := extractFunc(string(src), "func (c *Client) insertRequestLog")
	if fn == "" {
		t.Fatalf("insertRequestLog not found")
	}

	sqlStart := strings.Index(fn, "INSERT INTO request_logs_hot (")
	sqlEnd := strings.Index(fn[sqlStart:], "`") + sqlStart
	sql := fn[sqlStart:sqlEnd]

	cols := parseIdentList(sql, sqlIndex(sql, "(", 0)+1, sqlIndex(sql, ") VALUES", 0))
	valsRaw := sql[sqlIndex(sql, ") VALUES (", 0)+len(") VALUES (") : sqlIndex(sql, "ON CONFLICT", 0)]
	valsRaw = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(valsRaw, "") // SQL comments
	valsRaw = strings.TrimSuffix(strings.TrimRight(valsRaw, " \t\n"), ")")
	exprs := parseIdentList(valsRaw, 0, len(valsRaw))

	args := extractCallArgs(fn[sqlEnd+1:])
	if len(cols) == 0 || len(exprs) == 0 || len(args) == 0 {
		t.Fatalf("parse failed: cols=%d exprs=%d args=%d", len(cols), len(exprs), len(args))
	}
	if len(exprs) != len(cols) {
		t.Fatalf("VALUES expression count %d != column count %d", len(exprs), len(cols))
	}
	if len(args) != len(cols)-1 { // ts is now(), every other column binds one arg
		t.Fatalf("Go arg count %d != columns-1 %d", len(args), len(cols)-1)
	}

	phRe := regexp.MustCompile(`^\$(\d+)((?:::[a-z\[\]]+)*)$`)
	used := map[int]bool{}
	for k, expr := range exprs {
		if k == 1 && expr == "now()" { // ts
			continue
		}
		m := phRe.FindStringSubmatch(expr)
		if m == nil {
			t.Fatalf("expression #%d unexpected form %q", k, expr)
		}
		n, _ := strconv.Atoi(m[1])
		if used[n] {
			t.Fatalf("placeholder $%d used more than once", n)
		}
		used[n] = true
		if n > len(args) {
			t.Fatalf("placeholder $%d beyond arg count %d", n, len(args))
		}
		col, arg := cols[k], args[n-1]
		if expect := expectedArgIdent(col); expect != "" && !strings.Contains(arg, expect) {
			t.Fatalf("misalignment at column %d %q <- %s bound to %q (expected ~%q)",
				k+1, col, expr, arg, expect)
		}
		// The 2026-08-25 incident also shipped semantically aligned columns
		// with MISPLACED casts ($66::text[] received the OutboundMsgHashes
		// JSON string). Verify each cast matches its column's wire format.
		if got, want := m[2], expectedCast(col); got != want {
			t.Fatalf("column %d %q <- %s: cast %q != expected %q",
				k+1, col, expr, got, want)
		}
	}
	for n := 1; n <= len(args); n++ {
		if !used[n] {
			t.Fatalf("Go arg #%d (%s) never referenced by any placeholder", n, args[n-1])
		}
	}
}

// TestUpdateRequestLogPlaceholderAlignment guards updateRequestLog's named
// assignments the same way. Each SET column names its placeholder explicitly,
// so the check is purely semantic: the Go arg bound to $N must match the
// column that references $N.
func TestUpdateRequestLogPlaceholderAlignment(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	fn := extractFunc(string(src), "func (c *Client) updateRequestLog")
	if fn == "" {
		t.Fatalf("updateRequestLog not found")
	}

	sqlStart := strings.Index(fn, "UPDATE request_logs_hot")
	sqlEnd := strings.Index(fn[sqlStart:], "`") + sqlStart
	sql := fn[sqlStart:sqlEnd]
	setSec := sql[strings.Index(sql, "SET")+3 : strings.Index(sql, "WHERE request_id")]

	args := extractCallArgs(fn[sqlEnd+1:])
	if len(args) == 0 {
		t.Fatalf("no args parsed for updateRequestLog")
	}

	// column = <rhs containing $N>. CASE expressions (usage_source / success /
	// error_kind / routing_attempts / attachments) reference several $N; a
	// placeholder belongs to a column only when the arg semantics match, so
	// CASE-internal references to other columns' placeholders don't misbind.
	setSec = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(setSec, "")
	assignSplit := regexp.MustCompile(`\n\s*,?\s*([a-z_0-9]+)\s*=\s*`)
	parts := assignSplit.Split("\n"+setSec, -1)
	names := assignSplit.FindAllStringSubmatch("\n"+setSec, -1)
	bindings := map[int]string{} // placeholder -> column
	for i, name := range names {
		col := name[1]
		rhs := parts[i+1]
		expect := expectedArgIdent(col)
		matched := false
		for _, pm := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(rhs, -1) {
			n, _ := strconv.Atoi(pm[1])
			if n < 1 || n > len(args) {
				t.Fatalf("column %q references $%d beyond arg count %d", col, n, len(args))
			}
			if expect != "" && strings.Contains(args[n-1], expect) {
				if prev, dup := bindings[n]; dup && prev != col {
					t.Fatalf("$%d bound to both %q and %q", n, prev, col)
				}
				bindings[n] = col
				matched = true
			}
		}
		if expect != "" && !matched {
			t.Fatalf("column %q: no placeholder in SET binds a semantically matching arg", col)
		}
	}
	for n := 1; n <= len(args); n++ {
		col, ok := bindings[n]
		if n == 1 && !ok {
			continue // $1 = request_id in the WHERE clause
		}
		if !ok {
			t.Fatalf("Go arg #%d (%s) not referenced by any SET column", n, args[n-1])
		}
		if expect := expectedArgIdent(col); expect != "" && !strings.Contains(args[n-1], expect) {
			t.Fatalf("misalignment: %q <- $%d bound to %q (expected ~%q)", col, n, args[n-1], expect)
		}
	}
}

// expectedCast returns the cast suffix the VALUES expression must carry for a
// column (pgx binary-protocol fix for JSONB / text[] params).
func expectedCast(col string) string {
	switch col {
	case "quality_flags":
		return "::text[]"
	case "auto_decision", "compression_meta", "outbound_msg_hashes", "quality_fix_actions",
		"tool_calls", "attachments", "routing_attempts", "discard_events":
		return "::text::jsonb"
	default:
		return ""
	}
}

func extractFunc(src, signature string) string {
	i := strings.Index(src, signature)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	j := strings.Index(rest[1:], "\nfunc ")
	if j < 0 {
		return rest
	}
	return rest[:j+1]
}

// parseIdentList splits a SQL identifier/placeholder list on top-level commas.
func parseIdentList(s string, start, end int) []string {
	if end > len(s) {
		end = len(s)
	}
	s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s[start:end], "")
	var out []string
	var cur strings.Builder
	depth := 0
	for _, ch := range s {
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
		}
		if ch == ',' && depth == 0 {
			if id := strings.TrimSpace(cur.String()); id != "" {
				out = append(out, id)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(ch)
	}
	if id := strings.TrimSpace(cur.String()); id != "" {
		out = append(out, id)
	}
	return out
}

func sqlIndex(s, sub string, from int) int {
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return from + i
}

// extractCallArgs parses the argument list of the tx.Exec call that follows
// the SQL string: strips comments, splits on depth-0 commas.
func extractCallArgs(s string) []string {
	// Trim to the first "if err != nil" which terminates the call.
	if i := strings.Index(s, "if err != nil"); i >= 0 {
		s = s[:i]
	}
	s = regexp.MustCompile(`//[^\n]*`).ReplaceAllString(s, "") // Go line comments in the arg list
	s = strings.TrimRight(s, " \t\n")
	s = strings.TrimSuffix(s, ")") // closing paren of Exec(
	var args []string
	var cur strings.Builder
	depth := 0
	for _, ch := range s {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		}
		if ch == ',' && depth == 0 {
			if a := strings.TrimSpace(cur.String()); a != "" {
				args = append(args, a)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(ch)
	}
	if a := strings.TrimSpace(cur.String()); a != "" {
		args = append(args, a)
	}
	return args
}

// expectedArgIdent maps a request_logs column to the Go identifier fragment
// that must appear in the arg bound to it. Empty means "skip semantic check".
func expectedArgIdent(col string) string {
	aliases := map[string]string{
		"request_id": "RequestID", "tenant_id": "TenantID", "application_id": "ApplicationID",
		"api_key_id": "APIKeyID", "end_user_id": "EndUserID", "client_model": "ClientModel",
		"outbound_model": "OutboundModel", "canonical_model": "CanonicalModel", "canonical_id": "CanonicalID",
		"credential_id": "CredentialID", "provider_id": "ProviderID",
		"client_profile": "ClientProfile", "request_mode": "RequestMode", "affinity_hit": "AffinityHit",
		"prompt_tokens": "PromptTokens", "completion_tokens": "CompletionTokens",
		"cache_read_tokens": "CacheReadTokens", "cache_write_tokens": "CacheWriteTokens",
		"reasoning_tokens": "ReasoningTokens", "image_tokens": "ImageTokens", "audio_tokens": "AudioTokens",
		"video_tokens": "VideoTokens", "provider_tokens": "ProviderTokens", "total_tokens": "totalTokens",
		"cost_usd": "CostUSD", "cost_display": "CostDisplay", "cost_currency": "CostCurrency",
		"latency_ms": "LatencyMs", "request_status": "RequestStatus", "error_kind": "ErrorKind",
		"success": "Success",
		"search_text": "searchText", "identity_hash": "IdentityHash", "response_checksum": "ResponseChecksum",
		"transform_rule_id": "TransformRuleID", "egress_protocol": "EgressProtocol", "failure_stage": "FailureStage",
		"failure_detail_code": "FailureDetailCode", "request_preview": "RequestPreview",
		"transform_summary": "TransformSummary", "response_preview": "ResponsePreview",
		"stream_first_chunk_ms": "StreamFirstChunkMs", "stream_chunk_count": "StreamChunkCount",
		"stream_done_received": "StreamDoneReceived", "stream_interrupted": "StreamInterrupted",
		"usage_source": "UsageSource", "gw_session_id": "GwSessionID", "gw_task_id": "GwTaskID",
		"api_key_prefix": "APIKeyPrefix", "api_key_owner_user": "APIKeyOwnerUser",
		"application_code": "ApplicationCode", "is_auto_request": "IsAutoRequest", "task_type": "TaskType",
		"auto_profile": "AutoProfile", "auto_decision": "AutoDecision", "auto_confidence": "AutoConfidence",
		"work_type": "WorkType", "credits_charged": "CreditsCharged", "parent_request_id": "ParentRequestID",
		"compression_reason": "CompressionReason", "compression_strategy": "CompressionStrategy",
		"compression_meta": "CompressionMeta", "token_band": "TokenBand",
		"outbound_msg_count": "OutboundMsgCount", "outbound_token_est": "OutboundTokenEst",
		"outbound_msg_hashes": "OutboundMsgHashes", "quality_flags": "QualityFlags",
		"quality_fix_actions": "QualityFixActions", "quality_score": "QualityScore",
		"upstream_finish_reason": "UpstreamFinishReason", "tool_calls": "ToolCalls",
		"client_request_id": "ClientRequestID", "upstream_status_code": "UpstreamStatusCode",
		"client_timeout": "ClientTimeout", "client_endpoint": "ClientEndpoint",
		"stream_chunk_errors": "StreamChunkErrors", "stream_chunks_sent": "StreamChunksSent",
		"attachments": "Attachments", "client_ip": "ClientIP", "client_forwarded_for": "ClientForwardedFor",
		"origin_stage": "OriginStage", "origin_actor": "OriginActor", "routing_attempts": "RoutingAttempts",
		"routing_summary": "RoutingSummary", "agent_name": "AgentName", "agent_type": "AgentType",
		"client_protocol": "ClientProtocol", "virtual_client_id": "VirtualClientID",
		"discard_events": "DiscardEvents", "customer_id": "CustomerID",
	}
	if v, ok := aliases[col]; ok {
		return v
	}
	m := regexp.MustCompile(`^t(\d)_(.+)$`).FindStringSubmatch(col)
	if m != nil {
		parts := strings.Split(m[2], "_")
		for i, p := range parts {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
		return "T" + m[1] + strings.Join(parts, "")
	}
	return ""
}
