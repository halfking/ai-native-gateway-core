package errorsx

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

type ErrorKind string

const (
	KindTransient    ErrorKind = "transient"
	KindTimeout      ErrorKind = "timeout"
	KindNetwork      ErrorKind = "network"
	KindRateLimit    ErrorKind = "rate_limit"
	KindAuth         ErrorKind = "auth"
	KindQuota        ErrorKind = "quota"
	KindUpstreamDown ErrorKind = "upstream_down"
	KindCanceled     ErrorKind = "canceled"
	// KindClientBug (2026-07-28 §5.6) covers client-side mistakes that
	// look like a KindCanceled from the upstream's perspective but
	// originate from the caller's protocol (e.g. echoing a stale
	// tool_call_id). Distinct from KindCanceled so the error_kind
	// taxonomy can keep cancel and bug separately countable.
	KindClientBug      ErrorKind = "client_bug"
	KindConcurrent     ErrorKind = "concurrent"
	KindAuthRevoked    ErrorKind = "auth_revoked"
	KindQuotaPeriodic  ErrorKind = "quota_periodic"
	KindQuotaBalance   ErrorKind = "quota_balance"
	KindQuotaPermanent ErrorKind = "quota_permanent"
	KindModelNotFound  ErrorKind = "model_not_found"
	KindStreamTimeout  ErrorKind = "stream_timeout"
	// KindToolCallIdMismatch: client echoed a tool_call_id that the
	// upstream did not recognise. The most common cause is the agent
	// framework losing the id field during streaming accumulation of
	// `delta.tool_calls` chunks, or generating a placeholder id when
	// the upstream response didn't carry one. Reported as a distinct
	// non-fatal, non-retryable kind so the gateway does not punish
	// the credential (this is a client bug, not an upstream failure).
	KindToolCallIdMismatch ErrorKind = "tool_call_id_mismatch"
	// KindContextLength: upstream rejected the request because the prompt
	// (or accumulated conversation history) exceeded the model's context
	// window. Distinct from KindTransient so the executor can:
	//   1. Surface a structured retry-after-trim path
	//      (routing/executor_chat.go attempts one trim+retry before bubbling
	//      the 4xx up to the client).
	//   2. Avoid the standard retryable-error fast path that would re-send
	//      the same oversized body to a different credential.
	// This matches minimax's behaviour: direct calls to api.minimaxi.com
	// silently slide-window trim a too-long conversation, but the proxy
	// path was historically returning the raw 400 because no client-side
	// trim was applied.
	KindContextLength      ErrorKind = "context_length_exceeded"
	KindUnsupportedFeature ErrorKind = "unsupported_feature"
	// KindModelDeprecated: upstream has permanently removed / end-of-lifed the
	// requested model. Distinct from KindModelNotFound (where the model name is
	// simply unknown / typo'd): deprecation is an authoritative upstream
	// statement that the model will NOT come back, so the gateway applies a
	// long (30-day) per-(credential,model) cooling and surfaces HTTP 410 Gone
	// to the client with the upstream's EOL message. Typical signals:
	//   - NVIDIA NIM: HTTP 410 {"detail":"...has reached its end of life..."}
	//   - OpenAI:     404/422 "model ... has been deprecated, use ..."
	//   - Anthropic:  404 "model is deprecated"
	// Routed via a dedicated executor/handler path (NOT IsClientBug, NOT
	// retryable) so the executor does not waste 22 attempts on a dead model.
	KindModelDeprecated ErrorKind = "model_deprecated"
	// KindContentFilter: upstream rejected the request based on content
	// moderation / safety policy (e.g. MiniMax 422 "new_sensitive (1026)",
	// OpenAI "content_filter", Anthropic content policy). This is
	// content-determined: the same prompt is rejected on every sibling
	// credential of the same provider, so the executor short-circuits
	// (does NOT retry other candidates) and the handler returns a 400 to
	// the client with the upstream reason + an actionable hint. The
	// credential is healthy — no cooling / circuit / state write.
	KindContentFilter ErrorKind = "content_filter"
	// KindEmptyResponse: upstream returned HTTP 200 with a well-formed stream
	// or JSON body that contains zero actual content — no delta.content, no
	// reasoning_content, no tool_calls, and no meaningful finish_reason. This
	// is the NIM (Provider 18) failure mode: the stream opens, sends ~1-3
	// chunks with empty choices, then [DONE], producing 0 completion tokens.
	//
	// Classification rationale:
	//   - NOT in IsCredentialFatal: a transient empty burst must not hard-exclude
	//     the credential — it may succeed on retry.
	//   - NOT in IsClientBug: the client's request is valid; the upstream is at
	//     fault.
	//   - NOT in freeCredentialsTolerateTransient: we WANT circuit
	//     RecordFailure to fire so recent_success_rate soft-demotes the
	//     credential, while the non-fatal kind keeps the availability machine
	//     from marking it 'unavailable'.
	//   - Handled by the dedicated stream-resumable failover path (content-gate
	//     returns Resumable=true → executor continues to next candidate), not
	//     the generic IsRetryable retry loop.
	KindEmptyResponse ErrorKind = "empty_response"
	// KindConversion marks a stream / body that the bridge or executor
	// emitted but whose serialization or shape could not be converted
	// (e.g. conversion_error in 2026-07-28 §5.8 taxonomy). Distinct
	// from KindUnsupportedFeature because conversion failure is
	// driven by payload shape, not by upstream capability — a
	// different credential against the same provider would also fail.
	KindConversion ErrorKind = "conversion_error"
	// KindUpstreamContextLoss: upstream returned HTTP 200 with a clean
	// stream ([DONE], stop_reason=end_turn), so the request looks
	// "successful" at the protocol layer, but the prompt_tokens the
	// upstream reported are a tiny fraction of what the request body
	// implies — the upstream silently dropped most of the context before
	// invoking the model (observed on third-party Claude relay
	// apiclaude.cc: a ~919KB body produced prompt_tokens=337 while
	// sibling requests of the same session reported 256K–305K).
	//
	// From the user's perspective this IS an error: the model answers a
	// stripped-down context and returns a near-useless short reply.
	//
	// Classification rationale (deliberately different from KindEmptyResponse):
	//   - NOT in IsClientBug: the client's request is valid.
	//   - NOT in IsCredentialFatal: a single occurrence must not hard-exclude
	//     the credential (it may be a transient relay hiccup).
	//   - NOT in IsRetryable: the response has already streamed to the
	//     client by the time we can detect the mismatch; retry would double
	//     bill. Detection is post-hoc, feeding the quality pipeline.
	//   - NOT skipped by credentialhealth (unlike KindEmptyResponse): this
	//     is a real upstream fault and MUST count toward degradation so the
	//     router can soft-demote / fail over the credential.
	KindUpstreamContextLoss ErrorKind = "upstream_context_loss"
)

// contextLengthRe matches upstream error bodies that signal "prompt too
// long for the model's context window". The patterns are deliberately
// broad because each provider phrases the same condition differently:
//
//   - OpenAI:     "This model's maximum context length is 8192 tokens..."
//   - Anthropic:  "prompt is too long", "input is too long"
//   - minimax:    "context_length_exceeded", "max_tokens exceed"
//   - deepseek:   "context window exceeded"
//   - zhipu/glm:  "上下文长度超出限制", "tokens too long"
//
// We keep the CJK alternative because a few domestic providers localise
// the error string rather than returning the canonical English form.
var contextLengthRe = regexp.MustCompile(
	`(?i)(context[ _-]?length[ _-]?exceeded|` +
		`maximum context length|` +
		`context[ _-]?window[ _-]?(exceeded|is)|` +
		`context[ _-]?window.{0,30}(exceed|limit|maximum)|` +
		`prompt is too long|` +
		`input is too long|` +
		`input.{0,30}(exceed|context window|limit)|` +
		`too many (input )?tokens|` +
		`tokens? exceed|` +
		`reduce the length|` +
		`maximum number of tokens)`,
)
var contextLengthCJKRe = regexp.MustCompile(
	`上下文(长度)?(超出|超过|超限)|` +
		`tokens? (过多|超限|超过)|` +
		`输入(过长|太长)|` +
		`超出(模型)?(最大)?(上下文|长度|限制)`,
)

// modelNotFoundRe is intentionally narrow (P5 of 2026-06-18-model-match-and-404-plan.md).
//
// We previously used 50-char window regexes (e.g. `model.{0,40}(not found)`) which
// produced false positives on body strings like:
//
//	"Your previous model training run was not found, retry with a new id"
//	"Model is not available in your region"
//	"Model glm-5.1 has been deprecated, please use glm-5.2"
//
// Such cases are NOT model_not_found (they're region/quota/deprecation errors
// that deserve a different status code and a different code path) and the
// gateway used to silently swallow them, surfacing a generic 404 to the
// caller. The new pattern uses \b word boundaries and an explicit identifier
// token between the noun and the "not found" phrase, so the match requires
// the body to literally be talking about a specific model/endpoint name.
//
// Removed phrases and why:
//   - "model.{0,40}not available"     — over-matches region/tier restrictions.
//   - "model.{0,40}deprecated|retired|sunset" — deprecation has its own
//     semantics (a working model being scheduled for removal) and should
//     NOT short-circuit routing to a 404. Future work: a dedicated
//     KindDeprecated kind for telemetry.
var modelNotFoundRe = regexp.MustCompile(
	`(?i)(` +
		`\b(model|endpoint)[\s:]+['"]?[a-z0-9._\-/:]{1,80}['"]?\s+(does not exist|is not found|not found|is unknown|unknown)\b|` +
		`\b(no such|unknown)\s+model\b` +
		`)`,
)
var modelNotFoundCJKRe = regexp.MustCompile(
	`模型不存在|模型.{0,10}不存在|模型.{0,10}未找到`,
)

var unsupportedFeatureRe = regexp.MustCompile(
	`(?i)((does not|doesn'?t) support (coding plan|tool|function|tools|function call)|` +
		`(does not|doesn'?t) support (image|vision|image input|multimodal)|` +
		`cannot read .{0,80}(image\.[a-z0-9]+)|` +
		`(tool|function)[- _]?call(ing|s)? (is )?not supported|` +
		`unsupported (parameter|model|feature).{0,20}(tools?|function|tool_choice)|` +
		`当前模型不支持)`,
)

// modelDeprecatedRe matches upstream error bodies that signal the model has
// been permanently removed / end-of-lifed / deprecated by the provider.
//
// This is intentionally checked BEFORE modelNotFoundRe: a body like
// "model glm-5.1 has been deprecated, please use glm-5.2" historically fell
// through to KindTransient (modelNotFoundRe deliberately excludes
// "deprecated|retired|sunset" per its comment at L156-170, which explicitly
// called out "Future work: a dedicated KindDeprecated kind for telemetry").
// KindModelDeprecated is that dedicated kind.
//
// Matched vendor phrasings:
//   - NVIDIA NIM (HTTP 410): "has reached its end of life ... and is no
//     longer available"
//   - OpenAI: "model ... has been deprecated", "decommissioned"
//   - Anthropic: "model is deprecated"
//   - Generic: "retired", "sunset", "discontinued", "permanently removed",
//     "end of life", "end-of-life"
var modelDeprecatedRe = regexp.MustCompile(
	`(?i)(end[ _-]?of[ _-]?life|` +
		`no longer available|` +
		`has been deprecated|is deprecated|` +
		`has been (retired|decommissioned|discontinued|sunset|permanently removed)|` +
		`(retired|decommissioned|discontinued|sunset|permanently removed)|` +
		`已(下线|停用|废弃|停止服务)|` +
		`已(永久)?停用)`,
)

// budgetExceededRe detects permanent quota exhaustion (balance insufficient,
// budget exceeded, credits depleted). These are KindQuotaPermanent, not
// KindRateLimit, because they won't resolve until the user tops up their
// account — retrying or waiting is futile.
//
// 2026-07-16 P0 fix: Anthropic 429 with "Organization balance insufficient" +
// "budget_exceeded" was misclassified as KindRateLimit (transient), causing
// the executor to retry and trigger false-positive credential degradation.
// Direct API calls worked (different key with balance), but gateway kept
// trying the exhausted credential.
//
// 2026-07-19 P0 fix: 智谱AI 429 with "您已达到每周/每月使用上限" (code: 1310)
// was misclassified as KindRateLimit because Chinese quota messages weren't
// matched. Extended regex to support Chinese patterns.
var budgetExceededRe = regexp.MustCompile(
	`(?i)(budget[_ -]?exceeded|` +
		`balance[_ -]?insufficient|` +
		`insufficient[_ -]?(credit|balance|funds)|` +
		`credit[s]?[_ -]?(exhausted|depleted|insufficient)|` +
		`account[_ -]?balance[_ -]?(low|insufficient|exhausted)|` +
		`quota[_ -]?exceeded|` +
		`usage[_ -]?limit[_ -]?exceeded|` +
		// Chinese quota exhaustion patterns (智谱AI, etc.)
		`达到.{0,10}(每周|每月|每日|使用)?上限|` +
		`(配额|额度|余额).{0,10}(用尽|耗尽|不足|超限)|` +
		`(限额|使用量).{0,10}重置|` +
		`"code"\s*:\s*"1310")`, // 智谱AI specific code
)

// concurrentOverloadRe matches upstream error bodies that signal
// "service overloaded / too many concurrent requests". Providers like
// MiniMax surface concurrent-rate-limit problems as either:
//   - HTTP 429/503 with messages containing "concurrent", "too many",
//     "overloaded", "engine busy", "rpm/tpm", etc.
//   - SSE streams that close prematurely (EOF without [DONE]) under load.

// 2026-07-21 P0 fix: Distinguish periodic (recoverable) from permanent
// quota exhaustion. 智谱AI and similar providers return 429 + a "limit
// will reset at YYYY-MM-DD HH:MM:SS" message when the user hits a
// weekly/monthly cap. The previous single budgetExceededRe matched this
// as KindQuotaPermanent, so writer.go wrote quota_state='permanently_exhausted'
// with quota_recover_at=NULL and CredentialRecovery worker never picked it
// up — the credential stayed blocked for weeks/months even though the
// upstream had already reset the quota.
//
// quotaResetsRe matches recovery-time hints in the error body. If present,
// the upstream is signaling "wait until X then retry" — that's a periodic
// limit, not a permanent one. The {0,80} window covers cases like 智谱AI's
// "您的限额将在 2026-07-19 21:32:20 重置。" (限额→重置 spans ~24 chars, but
// we leave room for longer phrases including ISO timestamps).
var quotaResetsRe = regexp.MustCompile(
	`(?i)(reset[s]?[_ -]?(at|in|on)|` +
		`will[_ -]?reset|` +
		`retry[_ -]?after|` +
		`try[_ -]?again[_ -]?(at|in|after)|` +
		`available[_ -]?(at|in|from)|` +
		`recover[s]?[_ -]?(at|by|until)|` +
		// 2026-08-07 P0 fix: 智码(zhima) 的配额用尽报文用 window_type 标注
		// 窗口语义，而不是给 reset 时间戳。实测（154 生产 cred 34 zhima-1）：
		//   HTTP 429 {"error":"usage limit exceeded","window_type":"total"}
		// 旧逻辑把它落到 KindQuotaPermanent → recover_at=NULL → 永久卡死。
		// 但 window_type（无论 total/daily/weekly/monthly）都表明这是
		// 一个会按周期重置的用量窗口，应走 KindQuotaPeriodic 的恢复通道，
		// 由 balance_quota_probe 实测探活后自动翻回。
		`window[_ -]?type|"window_type"|` +
		// Chinese "重置" with up to 80 chars between the noun and 重置
		// (covers 智谱AI "...限额将在 YYYY-MM-DD HH:MM:SS 重置。").
		`(限额|使用量|配额|额度|余额).{0,80}重置|` +
		`重置.{0,80}(时间|日期|于|在)|` +
		// ISO timestamp / date-time pattern indicates a scheduled reset.
		// Use \s instead of a literal space to dodge Go RE2 character-class
		// edge cases at fragment boundaries.
		`\d{4}-\d{2}-\d{2}\s\d{2}:\d{2}:\d{2}|` +
		`\d{4}/\d{2}/\d{2}\s\d{2}:\d{2}:\d{2})`,
)

// Both patterns must be classified as KindConcurrent so the breaker can
// apply the 5-minute cooling policy and immediately route to the next
// candidate credential instead of retrying the same overloaded one.
var concurrentOverloadRe = regexp.MustCompile(
	`(?i)(concurrent.{0,30}(limit|exceed|over|too many|reach|max)|` +
		`too many (concurrent|requests|connections)|` +
		`(engine|server|service|api) (overloaded|too busy|busy)|` +
		`(server|service|upstream) (is )?(overload|under pressure)|` +
		`(rpm|tpm).{0,20}(limit|exceed|reach|over)|` +
		`request(ed|s)? too (fast|frequent|many)|` +
		`slow down|try again later|backoff|` +
		`available accounts|account (not|un)available|quota exhausted|insufficient credit)`,
)
var concurrentOverloadCJKRe = regexp.MustCompile(
	`并发.{0,15}(超限|过大|过高|达到上限|超过限制)|` +
		`请求.{0,10}(过快|频繁|太多)|` +
		`服务.{0,10}(繁忙|过载|压力|降级)|` +
		`稍后重试|限流`,
)

// eofWithoutDoneRe is a Go-level signal: the SSE stream closed before
// the [DONE] sentinel. When combined with provider-known overload (or
// repeated across the same credential) this is treated as concurrent
// overload — upstream silently dropping connections under load.
var eofWithoutDoneRe = regexp.MustCompile(
	`(?i)(eof without|eof_without_done|stream closed before|premature close|unexpected eof)`,
)

// toolCallIdMismatchRe matches the upstream error payload for the
// "client echoed a tool_call_id the upstream did not recognise"
// scenario. MiniMax surfaces this as a 4xx body with code 2013
// and a message containing "tool call id" / "tool id" /
// "tool_result's tool id". Anthropic's equivalent would be a
// 4xx with "tool_use_id" — the regex below is permissive about
// the exact noun so it can flag both vendors without future edits.
//
// 2026-07-11 expansion: also matches the OpenAI Responses API
// continuation-mismatch error "function_call_output requires
// item_reference ids matching each call_id" / "continuation
// requires previous_response_id or replayable tool-call context".
// These are the same class of client bug — the upstream has no
// context to associate the echoed tool output back to a prior
// call — and must NOT trip the transient-then-cooling path that
// would blacklist the credential for 5 minutes while the client
// retries with the same broken payload.
var toolCallIdMismatchRe = regexp.MustCompile(
	`(?i)(tool[_ ]?(call[_ ]?id|use[_ ]?id|result.*tool[_ ]?id).{0,40}(not found|not exist|invalid|unknown|unknown id|does not exist|unrecogn)|` +
		`(?:code|error[_ ]?code|status)["':= ]{0,8}2013|` +
		`message["']?\s*:\s*["']2013["']|` +
		`item[_ ]?reference.*(matching|each|call[_ ]?id)|previous[_ ]?response[_ ]?id|replayable[_ ]?tool[_ ]?call[_ ]?context)`,
)

// contentFilterRe matches upstream error bodies that signal a
// content-moderation / safety-policy rejection. Scoped to avoid false
// positives on legitimate "sensitive" tokens (e.g. "case-sensitive"):
//   - new_sensitive: MiniMax-specific moderation code token.
//   - content_filter / content_policy / content_moderation: OpenAI + others.
//   - policy_violation: generic policy term.
//   - sensitive followed by a 2-5 digit code in optional parens: the
//     MiniMax body shape "input new_sensitive (1026)" / "sensitive (2013)".
//   - prohibited/forbidden content/input/material: generic phrasing.
//   - CJK equivalents for domestic providers.
var contentFilterRe = regexp.MustCompile(
	`(?i)(new_sensitive|` +
		`content[_ -]?(filter|policy|moderation)|` +
		`policy[_ -]?violation|` +
		`sensitive[_ ]?\(?\d{2,5}\)?|` +
		`(prohibited|forbidden).{0,30}(content|input|material)|` +
		`安全(审查|策略)|内容(违规|敏感|不合规)|包含敏感)`,
)

func ClassifyError(err error, resp *http.Response) ErrorKind {
	if err != nil {
		// Check timeout BEFORE cancel: if the error wraps DeadlineExceeded
		// the upstream DID exceed the deadline; classifying it as Canceled
		// (when the ctx tree shadows DeadlineExceeded with the parent's
		// Canceled) would lose the timeout signal and skip failover.
		// The cancel-check below catches the remaining pure-cancel cases.
		if errors.Is(err, context.DeadlineExceeded) {
			return KindTimeout
		}
		if errors.Is(err, context.Canceled) {
			return KindCanceled
		}
		msg := err.Error()
		// Order matters: overload and EOF-without-done are checked
		// before generic timeouts because upstream-reported overload
		// messages often include words like "timeout" or "connection"
		// that would otherwise be mis-classified.
		if concurrentOverloadRe.MatchString(msg) || concurrentOverloadCJKRe.MatchString(msg) {
			return KindConcurrent
		}
		if eofWithoutDoneRe.MatchString(msg) {
			// EOF without [DONE] is most often a benign provider quirk
			// (e.g. MiniMax omits the [DONE] sentinel on successful
			// streams). Treat as KindStreamTimeout, which is retryable
			// and short-cooling — the executor's chunk-count heuristic
			// already distinguishes benign eof (with chunks sent) from
			// a real overload.
			return KindStreamTimeout
		}
		if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
			return KindTimeout
		}
		if strings.Contains(msg, "connection") || strings.Contains(msg, "refused") ||
			strings.Contains(msg, "no such host") || strings.Contains(msg, "reset") {
			return KindNetwork
		}
		if modelDeprecatedRe.MatchString(msg) {
			return KindModelDeprecated
		}
		if modelNotFoundRe.MatchString(msg) {
			return KindModelNotFound
		}
		if unsupportedFeatureRe.MatchString(msg) {
			return KindUnsupportedFeature
		}
		// 2026-07-03 (Bug #N, defense in depth): the body-driven kinds
		// below were previously only checked in ClassifyErrorWithBody /
		// ClassifyResponseBody, which require the raw body bytes. When
		// an upstream.Error is re-wrapped via fmt.Errorf (the legacy
		// `fmt.Errorf("upstream %d: %s", status, body)` pattern that
		// the executor's tryCandidate used to emit), the body is
		// collapsed into err.Error() and ClassifyError(err, nil) was
		// returning KindTransient for these cases. That broke the
		// IsClientBug / IsRetryable / writeCredentialStateOnError
		// branches downstream and polluted request_logs.error_kind
		// with "transient" for what is actually a client-side bug.
		//
		// Running the same regexes on the err.Error() string closes
		// the gap for any caller (current or future) that loses the
		// typed *upstream.Error while still keeping the body text in
		// the wrapped message. The patterns are model-agnostic on
		// purpose — they match OpenAI, Anthropic, MiniMax, deepseek,
		// and zhipu bodies alike.
		if contextLengthRe.MatchString(msg) || contextLengthCJKRe.MatchString(msg) {
			return KindContextLength
		}
		if toolCallIdMismatchRe.MatchString(msg) {
			return KindToolCallIdMismatch
		}
		return KindTransient
	}
	if resp == nil {
		return KindUpstreamDown
	}
	return ClassifyResponseStatus(resp)
}

// ClassifyResponseStatus maps an upstream HTTP response status code to
// an ErrorKind. Status-only (no body peek) so it's safe to call on a
// response whose body is still owned by another reader. Use
// ClassifyErrorWithBody when you also have the body bytes available
// and want overload/model-not-found signals from the payload.
func ClassifyResponseStatus(resp *http.Response) ErrorKind {
	switch {
	case resp.StatusCode == 429:
		// 429 with overload-shaped body is upgraded to KindConcurrent by
		// ClassifyErrorWithBody; a plain 429 stays as quota-style rate limit.
		return KindRateLimit
	case resp.StatusCode == 503, resp.StatusCode == 529:
		// 503 Service Unavailable and 529 (Anthropic-style Site Overloaded)
		// are commonly used to signal concurrent load.
		return KindConcurrent
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return KindAuth
	case resp.StatusCode == 402:
		return KindQuota
	case resp.StatusCode >= 500:
		return KindUpstreamDown
	default:
		return KindTransient
	}
}

// ClassifyErrorWithBody maps a (status, body) pair to an ErrorKind. It
// first inspects the body for concurrent-overload and model-not-found
// signals (which can be conveyed via JSON/SSE payloads rather than
// status codes), then falls back to status-based classification.
//
// Callers should pass the body bytes they've already read (or nil if
// the body was unreadable). The body is NOT consumed here; ownership
// stays with the caller.
func ClassifyErrorWithBody(status int, body []byte) ErrorKind {
	if len(body) > 0 {
		// 2026-07-04 V20 fix: exclude generic web-server 404 bodies from
		// model_not_found classification. Patterns like "404 page not found",
		// "404 Not Found", "The page you requested was not found" come from
		// Nginx, Apache, Caddy, Traefik, or cloud load balancers when the
		// upstream service is unreachable or misconfigured — not from LLM
		// providers. Without this guard, such responses are mis-classified as
		// KindModelNotFound, causing the router to skip cross-credential retry
		// and surface a 404 to the client when the real problem is routing or
		// infrastructure (should be KindUpstreamDown or KindEmpty).
		//
		// Exclusion heuristic: if body contains HTTP status line artifacts
		// ("404 Not Found", "404 page not found") OR generic web phrases
		// ("page you requested", "resource not found", "page not found")
		// within the first 512 bytes, do NOT apply model-specific patterns.
		bodyLower := strings.ToLower(string(body))
		if len(bodyLower) > 512 {
			bodyLower = bodyLower[:512]
		}
		isGenericWebError := strings.Contains(bodyLower, "404 not found") ||
			strings.Contains(bodyLower, "404 page not found") ||
			strings.Contains(bodyLower, "page you requested") ||
			strings.Contains(bodyLower, "page not found") ||
			strings.Contains(bodyLower, "resource not found")

		if concurrentOverloadRe.Match(body) || concurrentOverloadCJKRe.Match(body) {
			return KindConcurrent
		}
		// KindModelDeprecated (2026-08-05 P0): upstream permanently removed /
		// end-of-lifed the model. Checked BEFORE model_not_found because a
		// deprecation body ("has been deprecated, please use X") deliberately
		// does NOT match modelNotFoundRe (see its L156-170 comment), and would
		// otherwise fall through to KindTransient. Status gate includes 410
		// (Gone) — the canonical EOL status (NVIDIA NIM, OpenAI deprecation
		// proxies) — in addition to 400/404/422.
		if !isGenericWebError &&
			(status == 400 || status == 404 || status == 410 || status == 422) &&
			modelDeprecatedRe.Match(body) {
			return KindModelDeprecated
		}
		// P5 (2026-06-18): model_not_found only on 400/404/422, matching
		// ClassifyResponseBody's status gate. A 5xx body that mentions
		// "model not found" (e.g. a misconfigured proxy returning 502 with
		// a downstream 404 embedded) must be KindUpstreamDown, not
		// KindModelNotFound — the failure is connectivity, not model existence.
		//
		// V20 (2026-07-04): additionally gate on !isGenericWebError. If the
		// body looks like a generic web server 404 (Nginx/Apache/LB), do NOT
		// classify as KindModelNotFound even if modelNotFoundRe matches.
		if !isGenericWebError &&
			(status == 400 || status == 404 || status == 422) &&
			(modelNotFoundRe.Match(body) || modelNotFoundCJKRe.Match(body)) {
			return KindModelNotFound
		}
		if unsupportedFeatureRe.Match(body) {
			return KindUnsupportedFeature
		}
		// Content-moderation / safety-policy rejection (MiniMax 422
		// "new_sensitive (1026)", OpenAI "content_filter", etc). Gate on
		// the typical moderation status codes so a 200 body that happens
		// to contain "sensitive" (e.g. a benign word) is not mis-flagged.
		if (status == 400 || status == 403 || status == 422 || status == 451) &&
			contentFilterRe.Match(body) {
			return KindContentFilter
		}
		if (status == 400 || status == 413 || status == 422) &&
			(contextLengthRe.Match(body) || contextLengthCJKRe.Match(body)) {
			return KindContextLength
		}
		if toolCallIdMismatchRe.Match(body) {
			return KindToolCallIdMismatch
		}
		// 2026-07-16 P0 fix: budget_exceeded / balance insufficient on 429.
		// These are permanent quota exhaustion (KindQuotaPermanent), not
		// transient rate limits. Anthropic returns:
		//   429 {"error":{"message":"Organization balance insufficient",
		//        "type":"rate_limit_error","code":"budget_exceeded"}}
		// Without this check, such errors are classified as KindRateLimit
		// (transient), causing retries, probes, and false-positive degradation
		// even though the credential is permanently unusable until top-up.
		// 2026-07-21 P0 fix: when the body carries a reset timestamp, route
		// to KindQuotaPeriodic instead so the credential can recover.
		// 智谱AI 1310 returns "...限额将在 YYYY-MM-DD HH:MM:SS 重置" which
		// signals "wait for the quota window to reset" — periodic, not
		// permanent.
		if status == 429 && budgetExceededRe.Match(body) {
			if quotaResetsRe.Match(body) {
				return KindQuotaPeriodic
			}
			return KindQuotaPermanent
		}
	}
	// 2026-06-13: protocol/shape 4xx codes (e.g. 405 Method Not Allowed,
	// 406 Not Acceptable, 415 Unsupported Media Type) are NOT transient —
	// retrying the same payload on a different credential would just
	// bounce off the same upstream rule. Map them to KindUnsupportedFeature
	// so IsClientBug returns true, the executor skips cross-redential
	// retry, and the credential is NOT cooled. 408 (Request Timeout) is
	// mapped to KindTimeout so the network-retry path still fires.
	//
	// 2026-07-08: REMOVED 422 from this list. 422 "Unprocessable Entity"
	// has far broader semantics than the protocol-shape codes above — it
	// is how MiniMax surfaces content-moderation rejections
	// ("new_sensitive (1026)"), how some providers surface context-length
	// errors, and how clients surface malformed-tool bodies. Each of those
	// is classified above via a body-pattern match (contentFilterRe,
	// contextLengthRe, unsupportedFeatureRe). A 422 that matches none of
	// those patterns is ambiguous and should fall through to
	// ClassifyResponseStatus (KindTransient by default) rather than being
	// force-cast to KindUnsupportedFeature, which previously caused
	// content-filter rejections to masquerade as "unsupported_feature"
	// and be retried across every credential.
	//
	// 2026-08-05: REMOVED 410 (Gone) from this list. 410's semantics is
	// "resource permanently removed", which for an LLM gateway almost always
	// means the upstream model has been end-of-lifed. The body-pattern path
	// above now classifies 410+EOL bodies as KindModelDeprecated; a bare 410
	// with no recognizable body falls through to ClassifyResponseStatus
	// (KindTransient) so the executor can retry a sibling credential — the
	// previous mapping to KindUnsupportedFeature (IsClientBug=true) caused the
	// executor to skip cross-credential retry and hammer the dead model 22x.
	switch status {
	case 408:
		return KindTimeout
	case 405, 406, 409, 411, 412, 415, 416, 417, 418, 421, 423, 424, 425, 426, 428, 431:
		return KindUnsupportedFeature
	}
	return ClassifyResponseStatus(&http.Response{StatusCode: status})
}

// ClassifyResponseBody inspects an error body fragment (e.g. SSE error
// chunk) for upstream-error signals. Returns the matched ErrorKind or
// "" if the body doesn't match any of the known patterns (caller should
// fall through to other classification).
//
// P5 of 2026-06-18-model-match-and-404-plan.md: model_not_found is only
// returned when the HTTP status is one of 400, 404, or 422. A 5xx body
// that happens to mention "model not found" (e.g. a misconfigured proxy
// returning a 502 with a downstream 404 embedded) is treated as
// KindUpstreamDown instead, because the failure is the gateway's
// connectivity, not the model's existence.
func ClassifyResponseBody(status int, body []byte) ErrorKind {
	if len(body) > 0 {
		if concurrentOverloadRe.Match(body) || concurrentOverloadCJKRe.Match(body) {
			return KindConcurrent
		}
		// KindModelDeprecated: see ClassifyErrorWithBody. Checked before
		// model_not_found for the same reason (deprecation bodies must not
		// fall through to transient). Includes 410 in the status gate.
		if (status == 400 || status == 404 || status == 410 || status == 422) &&
			modelDeprecatedRe.Match(body) {
			return KindModelDeprecated
		}
		if (status == 400 || status == 404 || status == 422) &&
			(modelNotFoundRe.Match(body) || modelNotFoundCJKRe.Match(body)) {
			return KindModelNotFound
		}
		if unsupportedFeatureRe.Match(body) {
			return KindUnsupportedFeature
		}
		if eofWithoutDoneRe.Match(body) {
			return KindStreamTimeout
		}
		if toolCallIdMismatchRe.Match(body) {
			return KindToolCallIdMismatch
		}
		if (status == 400 || status == 403 || status == 422 || status == 451) &&
			contentFilterRe.Match(body) {
			return KindContentFilter
		}
		if contextLengthRe.Match(body) || contextLengthCJKRe.Match(body) {
			return KindContextLength
		}
		// 2026-07-16 P0 fix: budget_exceeded on 429 → KindQuotaPermanent.
		// 2026-07-21 P0 fix: when the body carries a reset timestamp, route
		// to KindQuotaPeriodic instead so the credential can recover.
		if status == 429 && budgetExceededRe.Match(body) {
			if quotaResetsRe.Match(body) {
				return KindQuotaPeriodic
			}
			return KindQuotaPermanent
		}
	}
	return ""
}

// IsConcurrentOverload returns true when the error reason matches known
// upstream concurrent-overload signals (rate-limit body text, etc.).
// NOTE: eof_without_done is NOT treated as concurrent overload here —
// many providers (MiniMax in particular) simply omit the [DONE] sentinel
// on otherwise successful streams. The executor handles eof_without_done
// separately, using chunk-count heuristics to distinguish a genuinely
// truncated stream from a benign missing-sentinel.
func IsConcurrentOverload(reason string) bool {
	if reason == "" {
		return false
	}
	return concurrentOverloadRe.MatchString(reason) || concurrentOverloadCJKRe.MatchString(reason)
}

func IsModelNotFound(kind ErrorKind) bool {
	return kind == KindModelNotFound
}

func IsRetryable(kind ErrorKind) bool {
	switch kind {
	case KindTransient, KindTimeout, KindNetwork, KindUpstreamDown, KindConcurrent, KindStreamTimeout:
		return true
	// KindContextLength is intentionally NOT in this list. The executor
	// handles context-length 4xx via a dedicated path: it tries one
	// client-side trim+retry (executor_chat.go around the 4xx handler) and
	// then surfaces the 4xx to the caller. Adding it here would route it
	// into the generic retry loop with the same oversized body, which
	// would just bounce off the same upstream limit.
	default:
		return false
	}
}

// IsContextLength returns true if the kind is a context-window exceeded
// signal. Callers use this to decide whether to attempt a one-shot
// client-side trim before bubbling the 4xx up.
func IsContextLength(kind ErrorKind) bool {
	return kind == KindContextLength
}

// IsContentFilter returns true if the kind signals an upstream content-
// moderation / safety-policy rejection. The executor uses this to
// short-circuit the candidate loop (the same content is rejected on
// every sibling credential) and the handler uses it to render a 400
// with the upstream reason + actionable hint. Intentionally NOT in
// IsClientBug (which retries the next candidate), NOT in IsRetryable,
// and NOT in shouldWriteCredentialState (the credential is healthy).
func IsContentFilter(kind ErrorKind) bool {
	return kind == KindContentFilter
}

func IsCredentialFatal(kind ErrorKind) bool {
	switch kind {
	case KindAuth, KindAuthRevoked, KindQuota, KindQuotaPeriodic, KindQuotaBalance, KindQuotaPermanent:
		return true
	default:
		return false
	}
}

// IsClientBug returns true when the kind signals a client-side bug
// (e.g. KindToolCallIdMismatch) where writing credential cooling state
// would only punish the credential for the caller's mistake. The
// gateway should still surface the error to the caller and log the
// detail, but must NOT open a circuit or write availability_recover_at
// in the credentials table.
func IsClientBug(kind ErrorKind) bool {
	// 2026-07-03: Removed KindModelNotFound from this list.
	// model_not_found is typically a provider issue (model removed/renamed
	// upstream), not a client bug. It should trigger binding unavailability
	// via the dedicated mnf branch in executor.go, not skip state writes.
	switch kind {
	case KindToolCallIdMismatch, KindUnsupportedFeature, KindCanceled:
		return true
	default:
		return false
	}
}
