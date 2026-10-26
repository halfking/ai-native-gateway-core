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
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本门**改指**到新写入路径
// （INSERT INTO usage_ledger_hot）。
//
// 旧门锚在 "INSERT INTO _request_logs_hot (" / "ON CONFLICT" 两处字面量上，
// 而这两个锚点随 v1 族一起消失 ⇒ 旧实现会走到 `fn[-1:]` 而 panic，
// **panic 会中止整个测试二进制**，把本包其余所有判据的结果一并吞掉
// （教训：门自己崩掉时，「没跑」和「跑过」长得一模一样）。
//
// 改指后本门保住的东西：
//
//	① 仍然解析 client.go 源码（不是对着测试文件里抄的 SQL 字面量断言）；
//	② 仍然核对「列清单 ↔ VALUES 表达式 ↔ Go 绑定参数」三者一一对齐；
//	③ 仍然逐列核对「占位符绑定的 Go 标识符 ↔ 该列的语义」；
//	④ 仍然逐列核对「占位符上的 cast ↔ 该列的 wire 格式」。
//
// 与旧门的两处形态差异（都是新写入路径的真实形态，不是放宽）：
//
//	★ 新 INSERT **没有 ON CONFLICT**（它不是 UPSERT）⇒ VALUES 段的结束位置
//	  改为「SQL 字面量的最后一个右括号」，不再按 ON CONFLICT 定位。
//	★ rate_multiplier 用 `COALESCE($19, 1.0)` 而不是裸占位符 ⇒ 只有这一列
//	  允许非裸形态，且该形态**必须**落在 rate_multiplier 列上（否则判据变红）。
func TestInsertRequestLogPlaceholderAlignment(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	fn := extractFunc(string(src), "func (c *Client) insertRequestLog")
	if fn == "" {
		t.Fatalf("insertRequestLog not found")
	}

	sqlStart := strings.Index(fn, "INSERT INTO usage_ledger_hot (")
	if sqlStart < 0 {
		t.Fatalf("INSERT INTO usage_ledger_hot not found in insertRequestLog")
	}
	sqlEnd := strings.Index(fn[sqlStart:], "`") + sqlStart
	if sqlEnd < sqlStart {
		t.Fatalf("unterminated SQL literal in insertRequestLog")
	}
	sql := fn[sqlStart:sqlEnd]

	cols := parseIdentList(sql, sqlIndex(sql, "(", 0)+1, sqlIndex(sql, ") VALUES", 0))
	// ★ 新写入路径没有 ON CONFLICT ⇒ VALUES 段到字面量末尾的最后一个右括号为止。
	valsStart := sqlIndex(sql, ") VALUES (", 0) + len(") VALUES (")
	valsEnd := strings.LastIndex(sql, ")")
	if valsStart < 0 || valsEnd < valsStart {
		t.Fatalf("VALUES segment not found in insertRequestLog")
	}
	valsRaw := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(sql[valsStart:valsEnd], "") // SQL comments
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
	// 新写入路径唯一允许的「非裸占位符」形态：rate_multiplier 的默认值兜底。
	coalesceRe := regexp.MustCompile(`^COALESCE\(\$(\d+),\s*1\.0\)$`)
	used := map[int]bool{}
	for k, expr := range exprs {
		if k == 1 && expr == "now()" { // ts
			continue
		}
		var n int
		var cast string
		switch {
		case phRe.MatchString(expr):
			m := phRe.FindStringSubmatch(expr)
			n, _ = strconv.Atoi(m[1])
			cast = m[2]
		case coalesceRe.MatchString(expr):
			if cols[k] != "rate_multiplier" {
				t.Fatalf("expression #%d (%q) uses the COALESCE default form — only rate_multiplier may", k, expr)
			}
			m := coalesceRe.FindStringSubmatch(expr)
			n, _ = strconv.Atoi(m[1])
		default:
			t.Fatalf("expression #%d unexpected form %q", k, expr)
		}
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
		if got, want := cast, expectedCast(col); got != want {
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
//
// ★ 2026-10-20（迁移 890，v1 族退役）：本门**改指**到新写入路径，且从「只看
// 第一条 UPDATE」升级为「**逐个变体**都查」——新写入路径有两条 UPDATE 变体
// （带真实 token 的 10 参版 / 不带的 5 参版），只查第一条会让第二条无人看管。
func TestUpdateRequestLogPlaceholderAlignment(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	fn := extractFunc(string(src), "func (c *Client) updateRequestLog")
	if fn == "" {
		t.Fatalf("updateRequestLog not found")
	}

	variants := extractUpdateVariants(fn, "UPDATE usage_ledger_hot")
	// 新写入路径只有两个变体：10 参（含真实 token）与 5 参（无 token）。
	// 多出第三条 = 有人加了新变体而没让本门看 ⇒ 直接判红。
	if len(variants) != 2 {
		t.Fatalf("expected exactly 2 UPDATE usage_ledger_hot variants in updateRequestLog, got %d", len(variants))
	}

	for _, v := range variants {
		sql := v.sql
		args := v.args
		if len(args) == 0 {
			t.Fatalf("no args parsed for updateRequestLog variant")
		}

		setStart := strings.Index(sql, "SET")
		whereStart := strings.Index(sql, "WHERE request_id")
		if setStart < 0 || whereStart < 0 || whereStart <= setStart {
			t.Fatalf("SET / WHERE request_id not found in variant SQL:\n%s", sql)
		}
		setSec := sql[setStart+3 : whereStart]

		// column = <rhs containing $N>. CASE expressions (usage_source / success /
		// error_kind / routing_attempts / attachments) reference several $N; a
		// placeholder belongs to a column only when the arg semantics match, so
		// CASE-internal references to other columns' placeholders don't misbind.
		setSec = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(setSec, "")
		assignSplit := regexp.MustCompile(`\n\s*,?\s*([a-z_0-9]+)\s*=\s*`)
		parts := assignSplit.Split("\n"+setSec, -1)
		names := assignSplit.FindAllStringSubmatch("\n"+setSec, -1)
		if len(names) == 0 {
			t.Fatalf("no SET assignment parsed in variant SQL:\n%s", sql)
		}
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
}

// updateVariant 是一条 UPDATE 语句字面量 + 它自己的 Go 绑定参数。
type updateVariant struct {
	sql  string
	args []string
}

// extractUpdateVariants 从函数体里抽出所有包含 anchor 的 UPDATE 语句字面量，
// 各自配上**紧随其后**那次 tx.Exec 的绑定参数。
//
// ⚠ 不能只取第一条：新写入路径有两条变体（10 参 / 5 参），只查第一条会让
// 第二条无人看管——而「变体选错」正是本门要抓的漂移。
func extractUpdateVariants(fn, anchor string) []updateVariant {
	var out []updateVariant
	for offset := 0; ; {
		rel := strings.Index(fn[offset:], anchor)
		if rel < 0 {
			break
		}
		abs := offset + rel
		// 字面量的起始反引号：本锚点之前最近的一个反引号。
		open := strings.LastIndex(fn[:abs], "`")
		closeIdx := strings.Index(fn[abs:], "`") + abs
		if open < 0 || closeIdx < abs {
			break
		}
		out = append(out, updateVariant{
			sql:  fn[open+1 : closeIdx],
			args: extractCallArgs(fn[closeIdx+1:]),
		})
		offset = closeIdx + 1
	}
	return out
}

// expectedCast returns the cast suffix the VALUES expression must carry for a
// column (pgx binary-protocol fix for JSONB / text[] params).
func expectedCast(col string) string {
	switch col {
	case "quality_flags":
		return "::text[]"
	case "auto_decision", "compression_meta", "outbound_msg_hashes", "quality_fix_actions",
		"tool_calls", "attachments", "routing_attempts", "discard_events", "request_body", "response_body", "outbound_body":
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

// expectedArgIdent maps a write-target column to the Go identifier fragment
// that must appear in the arg bound to it. Empty means "skip semantic check".
//
// ★ 2026-10-20（迁移 890，v1 族退役）：新写入路径的列清单（usage_ledger_hot）
// 补进 raw_model_name / rate_multiplier 两条；v1 族的列名保留在表里，是因为
// 读侧（会话族镜像、admin 视图）仍按那些名字讨论同一批语义。
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
		"success":     "Success",
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
		"client_protocol": "ClientProtocol", "upstream_protocol": "UpstreamProtocol",
		"protocol_conversion": "ProtocolConversion", "virtual_client_id": "VirtualClientID",
		"discard_events": "DiscardEvents", "customer_id": "CustomerID",
		"request_class": "RequestClass", "due_at": "DueAt",
		"credits_rate_multiplier": "RateMultiplier", // Wave 3 B1 (migration 736)
		// ★ 新写入路径（usage_ledger_hot）专属：
		"raw_model_name":  "OutboundModel", // 实际绑定 firstString(OutboundModel, ClientModel)
		"rate_multiplier": "RateMultiplier",
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
