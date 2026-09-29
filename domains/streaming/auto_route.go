package streaming

// Auto-route integration for v2.0.
//
// This file is the minimal wire-up that lets a client send
//   {"model": "auto"}
// and have the gateway pick the best model for them based on
//   - classified task type
//   - 5-min live index (success rate, p95 latency, pressure ratio)
//   - client profile preference (smart / speed_first / cost_first)
//
// Failure mode: if auto-route fails entirely (decider unset, index
// stale, LLM fallback down), the gateway falls back to the existing
// route-by-explicit-model path. The client sees a 502 with
// `code=auto_route_unavailable` rather than a wrong model.

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// autoFallbackModel returns the model used when decider fails or is
// unconfigured. Override via LLM_GATEWAY_AUTO_FALLBACK_MODEL env var.
// Defaults to "claude-sonnet-4.5" (canonical name).
func autoFallbackModel() string {
	if m := strings.TrimSpace(os.Getenv("LLM_GATEWAY_AUTO_FALLBACK_MODEL")); m != "" {
		return m
	}
	// 2026-09-14 O5 fix: the hardcoded default was "claude-sonnet-4.5" —
	// a premium model whose credentials are dead in production, so any
	// decider==nil instance answered every model="auto" request with a
	// guaranteed 503 no_candidate. The fallback default must be a model
	// that is actually routable: deepseek-v4-flash is the measured
	// cost-efficient workhorse (auto-matching audit §一) with live
	// credentials. Still env-overridable per deployment.
	return "deepseek-v4-flash"
}

// autoHeaderName is the response header carrying the decision JSON.
// Stable name (no x- prefix) so it's easy to grep in proxy logs.
const autoHeaderName = "X-Gw-Auto-Decision"

// autoProfileHeader is the per-request override header.
const autoProfileHeader = "X-Gw-Auto-Profile"

// autoTaskHintHeader is the optional client-provided task hint.
const autoTaskHintHeader = "X-Gw-Task-Hint"

// autoWorkTypeHeader carries the ACC / client work-type key (finer than L1 task_type).
const autoWorkTypeHeader = "X-Gw-Work-Type"

// autoIsAutoHeader marks a gateway-internal auto request (auto title /
// auto summary). Honored by the streaming handler to exclude it from
// title-gen chaining and to flag it as internal in request metrics.
const autoIsAutoHeader = "X-Gw-Is-Auto"

// autoParentRequestIDHeader (2026-08-06) carries the parent user request_id
// for gateway-internal loopback calls (auto title / auto summary). Handler
// entry reads it into logCtx.ParentRequestID, which flows into
// request_logs_hot.parent_request_id so operators can SQL JOIN the loopback
// row back to the parent user request that triggered it.
const autoParentRequestIDHeader = "X-Gw-Parent-Request-Id"

// autoSourceActorHeader (2026-08-06) names the emitting component for a
// gateway-internal loopback call (e.g. "auto-title-generator"). Handler
// entry reads it into logCtx.OriginActor, which flows into
// request_logs_hot.origin_actor so operators can SQL
// `WHERE origin_actor = 'auto-title-generator'` to find every auto-title row.
const autoSourceActorHeader = "X-Gw-Source-Actor"

// autoRequestMagic is the model name that triggers auto-route mode.
// Chosen to be OpenAI/Anthropic reserved-name-safe: "auto" is not a
// real model name (yet — Anthropic uses "auto" for tool_choice but
// that's a separate field), so no client will collide by accident.
const autoRequestMagic = "auto"

// maxWireDecisionBytes caps the JSON-serialised decision we put in
// both the X-Gw-Auto-Decision response header and request_logs.auto_decision.
// Without this cap, a cohort with many candidates could trigger a
// multi-MB decision. 16 KiB is enough for top-3 candidates with full
// per-dim scores.
//
// v2.0.3 audit fix #16.
const maxWireDecisionBytes = 16 * 1024

// autoTestModeHeader (2026-09-29) selects how a request interacts with the
// production upstream layer. The default (header absent) is "live": every
// stage of the handler pipeline runs and a real upstream LLM call is issued.
//
// Modes:
//
//	mock         — auto routing runs; the dispatch / upstream call is replaced
//	               by a synthetic protocol-correct response carrying the
//	               X-Gw-Auto-Decision header. Use this to test the auto
//	               matching pipeline against a live gateway without burning
//	               upstream tokens. Requires authorisation — see
//	               testModeAllowed().
//	auto-only    — alias of "mock" today; kept separate so future revisions
//	               can diverge (e.g. "auto-only" might exercise additional
//	               auto-route-side hooks that "mock" skips). Same runtime
//	               behaviour as mock.
//	other-only   — auto routing is SKIPPED: the body's explicit model name
//	               is honoured verbatim, and the rest of the pipeline
//	               (auth, policy, dispatch, upstream call) proceeds normally.
//	               Use this to isolate non-auto regressions.
//	live / full  — full production pipeline, including a real upstream call.
//	               Header absent and the two literal values are equivalent.
//
// Every mode honours the request's "stream" flag: a streaming request gets
// protocol-correct SSE framing ending in the protocol's terminator, never a
// bare JSON object.
//
// Authorisation is enforced once per request at handler entry (see
// testModeAllowed): it requires a shared secret presented in
// X-Gw-Test-Mode-Token, and is fail-closed when that secret is unconfigured.
// An unauthorised caller that names a non-empty mode is downgraded to
// TestModeLive silently, so a hostile probe can't toggle behaviour purely
// by setting a header.
const autoTestModeHeader = "X-Gw-Test-Mode"

// TestMode is the parsed value of the X-Gw-Test-Mode header. The zero value
// (TestModeLive) is the production default — see autoTestModeHeader doc.
type TestMode string

const (
	// TestModeLive runs the full production pipeline. Header absent and
	// "live" / "full" are equivalent.
	TestModeLive TestMode = ""
	// TestModeMock runs auto routing, then short-circuits with a synthetic
	// OpenAI-compatible response. No upstream LLM call is made.
	TestModeMock TestMode = "mock"
	// TestModeAutoOnly is a future-proofed alias of TestModeMock. The two
	// share runtime semantics today; the names are kept distinct so the
	// reports can tell whether the caller asked for "whole-pipeline mock"
	// or "auto-only mock" once their semantics diverge.
	TestModeAutoOnly TestMode = "auto-only"
	// TestModeOtherOnly skips auto routing entirely; the explicit model name
	// is honoured and the request proceeds through the production pipeline.
	// Use to isolate non-auto regressions.
	TestModeOtherOnly TestMode = "other-only"
	// TestModeFull is the explicit "do everything normally" alias.
	TestModeFull TestMode = "full"
)

// ParseTestMode reads and validates the X-Gw-Test-Mode header. Returns
// (mode, true) when the header is present AND the value is recognised AND
// the caller is authorised to use non-live modes; (TestModeLive, false) when
// the header is absent, malformed, or unauthorised.
//
// Authorisation is gated by testModeAllowed(); an unauthorised client that
// sets X-Gw-Test-Mode gets the live behaviour silently so a probe can't
// toggle the production code path by guessing a header value.
func ParseTestMode(r *http.Request, allowed bool) (TestMode, bool) {
	if r == nil {
		return TestModeLive, false
	}
	raw := strings.TrimSpace(r.Header.Get(autoTestModeHeader))
	if raw == "" {
		return TestModeLive, false
	}
	switch TestMode(strings.ToLower(raw)) {
	case TestModeMock:
		if !allowed {
			return TestModeLive, false
		}
		return TestModeMock, true
	case TestModeAutoOnly:
		if !allowed {
			return TestModeLive, false
		}
		return TestModeAutoOnly, true
	case TestModeOtherOnly:
		if !allowed {
			return TestModeLive, false
		}
		return TestModeOtherOnly, true
	case TestModeFull, TestModeLive:
		return TestModeLive, true
	default:
		// Unrecognised mode — treat as live (and report "not set" so the
		// log row stays at the default test_mode value rather than recording
		// an unparseable user-supplied string).
		return TestModeLive, false
	}
}

// IsMockMode reports whether mode is one of the upstream-bypassing modes
// (mock / auto-only). Use this to decide whether to short-circuit before
// dispatch.
func (m TestMode) IsMockMode() bool {
	return m == TestModeMock || m == TestModeAutoOnly
}

// SkipsAutoRoute reports whether mode is TestModeOtherOnly.
func (m TestMode) SkipsAutoRoute() bool {
	return m == TestModeOtherOnly
}

// String returns the canonical lowercase mode name; empty string for live.
func (m TestMode) String() string {
	if m == "" {
		return "live"
	}
	return string(m)
}

// testModeTokenHeader carries the shared secret that authorises the
// non-live test modes for callers that do NOT originate inside this
// process (cmd/autoroute-e2e-audit, manual curl from an operator
// workstation, CI jobs).
//
// Why a dedicated secret instead of X-Gw-Source-Actor (2026-09-29 audit):
// X-Gw-Source-Actor is listed in loopback.CorrelationHeaders, so
// middleware.StripUntrustedCorrelationHeaders DELETES it from every
// request that lacks this process's per-boot loopback token
// (internal/loopback/token.go). By the time an external audit tool's
// request reaches the handler, the actor header is already gone — the
// original actor-based gate therefore evaluated to false for exactly
// the callers it was written for, silently downgrading mock to live
// and re-introducing the real upstream call the mode exists to avoid.
//
// The secret is configured out-of-band (LLM_GW_TEST_MODE_TOKEN) and
// compared in constant time. An unset env var means the external path
// is unavailable (fail-closed), NOT that every caller is authorised.
const testModeTokenHeader = "X-Gw-Test-Mode-Token"

// testModeTokenEnv names the env var holding the shared secret.
const testModeTokenEnv = "LLM_GW_TEST_MODE_TOKEN"

// testModeActors are the in-process loopback actors that may enable test
// modes without presenting the shared secret. Every one of them is a
// gateway-internal caller that already survived the loopback-token
// check, so this list is an allowlist of already-trusted inners, not an
// authentication mechanism. It is intentionally empty-by-default: none
// of the production loopbacks (auto-title / auto-summary / session-summary)
// have any use for mock mode, and listing them would mean a future
// caller could silence a real upstream call. Test-mode requests from
// outside this process must use the shared secret.
var testModeActors = map[string]bool{}

// testModeAllowed (2026-09-29) is the access-control gate for the
// non-live test modes. It returns true only when the caller proves
// authorisation by one of two means:
//
//  1. It is an in-process loopback actor listed in testModeActors
//     (already trusted: such requests carry the per-boot loopback
//     token and thus survived header stripping).
//  2. It presents X-Gw-Test-Mode-Token matching LLM_GW_TEST_MODE_TOKEN
//     (constant-time compare, same posture as middleware.AdminToken).
//
// Fail-closed in every other case: an unauthorised caller that names a
// non-live mode is downgraded to TestModeLive, so a hostile probe
// cannot toggle the production code path by guessing header values.
// When the env var is unset the secret path is unavailable entirely.
func testModeAllowed(r *http.Request) bool {
	if r == nil {
		return false
	}
	if actor := strings.TrimSpace(r.Header.Get(autoSourceActorHeader)); actor != "" {
		if testModeActors[actor] {
			return true
		}
		// A non-empty actor that is not allowlisted is still a loopback
		// caller; fall through to the secret check rather than failing
		// early, so the same policy applies to every caller shape.
	}
	expected := strings.TrimSpace(os.Getenv(testModeTokenEnv))
	if expected == "" {
		return false
	}
	provided := r.Header.Get(testModeTokenHeader)
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// autoRouteDecision is the wire format of X-Gw-Auto-Decision. Stable
// JSON schema — clients may parse it for observability.
type autoRouteDecision struct {
	TaskType                  string              `json:"task_type"`
	Confidence                float64             `json:"confidence"`
	Profile                   string              `json:"profile"`
	Classifier                string              `json:"classifier"`
	Reason                    string              `json:"reason"`
	ChosenModel               string              `json:"chosen_model"`
	ChosenRawModel            string              `json:"chosen_raw_model"`
	ChosenCredID              int64               `json:"chosen_credential_id"`
	EnabledFeatures           []string            `json:"enabled_features,omitempty"`
	FilterReasons             []string            `json:"filter_reasons,omitempty"`
	CacheReused               bool                `json:"cache_reused"`
	FallbackUsed              bool                `json:"fallback_used"`
	ExperimentID              string              `json:"experiment,omitempty"`
	Treatment                 autoroute.Treatment `json:"treatment,omitempty"`
	AssignmentVersion         string              `json:"assignment_version,omitempty"`
	AssignmentKeyHash         string              `json:"assignment_key_hash,omitempty"`
	EmbeddingShadowTask       string              `json:"embedding_shadow_task,omitempty"`
	EmbeddingShadowSimilarity *float64            `json:"embedding_shadow_similarity,omitempty"`
	// R48 (2026-09-20) role 路由审计字段。omitempty + 仅在值非空时映射：
	// flag-off / 无角色头时序列化字节与本特性加入前完全一致。
	SessionRole string `json:"session_role,omitempty"`
	TaskKind    string `json:"task_kind,omitempty"`
	// RoutingSource 仅在 role_route 命中时映射到 wire（刻意不全量透出：
	// 其余来源值 V1/V2 早已落库，全量透出会改变 flag-off 字节流）。
	RoutingSource  string               `json:"routing_source,omitempty"`
	CandidatesTop3 []autoRouteCandidate `json:"candidates_top3"`

	// Process-local inputs for pre-first-byte dispatch recovery. They are not
	// serialized into the response header or audit JSON.
	failoverModels []string
	signals        autoroute.ClassificationSignals

	// Process-local selection snapshot fields. The wire candidate list is an
	// intentionally small audit view; these values preserve the exact winner
	// metrics needed by auto_route_selections without retaining the full
	// autoroute.Decision in protocol handlers.
	selectionCandidateRank   int
	selectionCompositeScore  float64
	selectionAffinityScore   float64
	selectionAffinityApplied bool
	selectionExplore         bool
}

// autoRouteCandidate is one row of the top-N audit list.
//
// CHANNEL_QUALITY_ROUTING（2026-06-28）新增 ChannelQuality / Reliability：
// 4 维评分（intent 0.4 + price 0.2 + channel 0.3 + reliability 0.1）下，
// 这两个维度是"质量优先于价格"策略的核心载体，必须对客户端可见
// （通过 X-Gw-Auto-Decision header）。
type autoRouteCandidate struct {
	Model          string  `json:"model"`
	Score          float64 `json:"composite_score"`
	Price          float64 `json:"price_score"`
	Speed          float64 `json:"speed_score"`
	Stability      float64 `json:"stability_score"`
	Match          float64 `json:"match_score"`
	Pressure       float64 `json:"pressure_score"`
	ContextFit     float64 `json:"context_fit"`
	ChannelQuality float64 `json:"channel_quality,omitempty"` // 0-100，越高越可靠
	Reliability    float64 `json:"reliability,omitempty"`     // 0-100，由 success_rate+p95_latency 推导
	RouteTier      string  `json:"route_tier,omitempty"`
}

// extractSignalsForAuto builds the ClassificationSignals from the
// parsed request body. Conservative estimation — undercounting tokens
// is OK (long_context false negative is acceptable); overcounting is
// also OK (false positive just routes to a long-context-friendly model).
//
// Image detection: scans the messages array for type="image_url" or
// type="image" content parts. Cheap pass; doesn't decode the URL.
//
// Tool detection: counts the tools array length.
//
// Code block detection: looks for ``` (triple-backtick fence) in any
// message content.
//
// Token estimation: ~4 chars per token (English / mixed), CJK chars
// count as ~1.5 tokens each (more efficient packing).
//
// 新增（需求 #1）：IDE 客户端指纹提取（ClientType）
func extractSignalsForAuto(reqBody *chatRequestBody, rawBody []byte) autoroute.ClassificationSignals {
	sigs := autoroute.ClassificationSignals{
		ToolCount:       countToolsInBody(rawBody),
		HasImages:       false,
		HasCodeBlock:    bytes.Contains(rawBody, []byte("```")),
		EstimatedTokens: estimateTokens(rawBody),
		ClientType:      "", // 新增字段，稍后从 HTTP 头提取
	}

	if len(reqBody.Messages) > 0 {
		sigs.MessageCount = countJSONArrayLen(reqBody.Messages)
	}

	// Walk messages to extract system prompt + last user prompt + image parts.
	// Defensive against arbitrary JSON shape; falls back silently on errors.
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(reqBody.Messages, &msgs); err == nil {
		for _, m := range msgs {
			switch strings.ToLower(m.Role) {
			case "system":
				sigs.SystemPrompt += string(m.Content) + "\n"
			case "tool":
				sigs.HasToolResults = true
			case "user":
				// Try string content first; fall back to array of parts
				var s string
				if err := json.Unmarshal(m.Content, &s); err == nil {
					sigs.LastUserPrompt = s
					continue
				}
				var parts []struct {
					Type     string          `json:"type"`
					Text     string          `json:"text"`
					ImageURL json.RawMessage `json:"image_url"`
				}
				if err := json.Unmarshal(m.Content, &parts); err == nil {
					var buf strings.Builder
					for _, p := range parts {
						switch strings.ToLower(p.Type) {
						case "image_url", "image":
							sigs.HasImages = true
						case "text", "":
							buf.WriteString(p.Text)
							buf.WriteByte(' ')
						}
					}
					sigs.LastUserPrompt = buf.String()
				}
			}
		}
	}
	sigs.Language = detectLanguage(sigs.LastUserPrompt + sigs.SystemPrompt)
	return sigs
}

// estimateTokens uses a conservative heuristic: 4 bytes per token for
// latin/ASCII, 2 tokens per CJK rune (comment contract: 1 CJK char ≈ 1.5-2).
// Returns 0 for empty input.
//
// H-5 (audit round2): the former single accumulator divided CJK counts by 4
// too, valuing one CJK char at ~0.5 tokens and never tripping the
// long_context route gate for Chinese requests.
func estimateTokens(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	asciiBytes := 0
	cjkRunes := 0
	for i := 0; i < len(b); {
		r, size := decodeRune(b[i:])
		if r >= 0x4E00 && r <= 0x9FFF {
			cjkRunes++
		} else {
			asciiBytes += size
		}
		i += size
	}
	// ASCII: ~4 bytes per token. CJK: ~2 tokens per rune (conservative side
	// of 1.5-2; overestimating is the safe direction for long-context gating).
	return asciiBytes/4 + cjkRunes*2
}

// decodeRune decodes one UTF-8 rune from b. Returns (r, n). On invalid
// input returns (U+FFFD, 1) — best-effort, never panics.
func decodeRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	switch {
	case c < 0x80:
		return rune(c), 1
	case c < 0xC0:
		return 0xFFFD, 1
	case c < 0xE0:
		if len(b) < 2 {
			return 0xFFFD, 1
		}
		return rune(c&0x1F)<<6 | rune(b[1]&0x3F), 2
	case c < 0xF0:
		if len(b) < 3 {
			return 0xFFFD, 1
		}
		return rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F), 3
	default:
		if len(b) < 4 {
			return 0xFFFD, 1
		}
		return rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F), 4
	}
}

// detectLanguage returns "zh", "en", or "mixed" based on CJK char density.
func detectLanguage(s string) string {
	cjk, total := 0, 0
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		total++
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		}
	}
	if total == 0 {
		return "en"
	}
	ratio := float64(cjk) / float64(total)
	switch {
	case ratio > 0.5:
		return "zh"
	case ratio > 0.15:
		return "mixed"
	default:
		return "en"
	}
}

// countJSONArrayLen counts the number of top-level elements in a JSON array.
// Returns 0 if the input is not a valid JSON array.
func countJSONArrayLen(raw json.RawMessage) int {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// maybeResolveAuto inspects the parsed request and, if it's an auto-route
// request (model == "auto"), runs the decider and rewrites the body so
// the chosen model is what gets sent upstream.
//
// Parameters:
//   - reqBody    : the parsed chat request
//   - rawBody    : the original request bytes (used for token estimation)
//   - r          : the HTTP request (for X-Gw-Auto-Profile / X-Gw-Task-Hint)
//   - apiKeyID   : the resolved API key ID (0 for unauthenticated)
//
// Returns:
//   - newBody     : possibly-rewritten body bytes (nil if not auto or
//     no rewrite performed)
//   - decision    : non-nil when auto-route ran (success or failure)
//   - shouldFail  : true when auto-route was attempted and failed;
//     caller should return 502 immediately
//
// On non-auto requests, returns (nil, nil, false) — zero overhead.
func (h *ChatHandler) maybeResolveAuto(reqBody *chatRequestBody, rawBody []byte, r *http.Request, apiKeyID int) ([]byte, *autoRouteDecision, bool) {
	if reqBody.Model != autoRequestMagic {
		return nil, nil, false
	}
	if h.decider == nil {
		// No decider wired → fall back to default model
		reqBody.Model = autoFallbackModel()
		return rewriteBodyWithModel(rawBody, autoFallbackModel()), nil, false
	}

	sigs := extractSignalsForAuto(reqBody, rawBody)
	// 从 HTTP 头 + 系统提示词语义匹配提取客户端/智能体类型
	sigs.ClientType = extractClientTypeWithPrompt(r, sigs.SystemPrompt)
	// R48 (2026-09-20): 会话角色识别——X-Gw-Agent-Role 声明为主，
	// 网关内部 loopback 的 X-Gw-Source-Actor 推断为辅（该头已被 R35-R1
	// 中间件按 loopback 令牌剥离，到达此处只可能可信）。
	sigs.AgentRole = autoroute.ResolveAgentRoleFromHeaders(
		r.Header.Get(autoroute.AgentRoleHeader),
		r.Header.Get(autoSourceActorHeader),
	)

	headerProfile := r.Header.Get(autoProfileHeader)
	taskHint := autoroute.TaskType(r.Header.Get(autoTaskHintHeader))

	// Extract session ID for intent caching.
	// v2.0.4: if session ID present, Decider reuses the cached intent
	// (10min TTL) instead of reclassifying on every request.
	sessionID := r.Header.Get("X-Gw-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-Id")
	}

	// Carry the request id into the Decider so the affinity explore bucket can
	// hash on it (deterministic per-request sampling). X-Request-Id is stamped
	// upstream before this handler runs; if absent, affinity explore is skipped
	// and the request is scored with affinity applied normally.
	reqCtx := r.Context()
	if rid := r.Header.Get("X-Request-Id"); rid != "" {
		reqCtx = autoroute.WithRequestID(reqCtx, rid)
	}
	// R37 (R35-R2): carry the caller actor so recordFeedbackAsync can skip
	// gateway-synthetic rounds (goal-% shadow rounds, internal loopbacks).
	if actor := r.Header.Get(autoSourceActorHeader); actor != "" {
		reqCtx = autoroute.WithOriginActor(reqCtx, actor)
	}

	if workType := strings.TrimSpace(r.Header.Get(autoWorkTypeHeader)); workType != "" {
		if l1, ok := h.decider.ResolveWorkType(workType); ok {
			taskHint = l1
			reqCtx = autoroute.WithWorkType(reqCtx, workType)
		}
	}

	decision, err := h.decider.DecideWithFeatureFlags(reqCtx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	if err != nil {
		// 2026-07-01 P1: surface the real failure instead of masking it.
		//
		// The previous behaviour ("log warn + silently rewrite to fallback
		// model") made DB / Redis / feature-flag outages look like normal
		// auto-route selections in request_logs. Operators saw streams of
		// "auto-route fell back to chat-default" with no signal that the
		// routing data layer was actually broken, which is exactly the
		// class of misleading telemetry the routing-error-transparency
		// work is fighting against
		// (docs/2026-07-01-unknown-error-root-cause.md).
		//
		// Behaviour contract going forward:
		//   1. Always emit ERROR (not WARN) — alerts can fire.
		//   2. Return shouldFail=true so the handler emits 502 + a
		//      transparent error_kind rather than writing a request_logs
		//      row that looks like a successful auto-route selection.
		//
		// The fallback model rewrite is no longer applied here. Clients
		// retrying with model="..." explicitly continue to work because
		// auto-route resolution is opt-in via the magic "auto" model name.
		slog.Error("auto-route: decider failed - routing data query error",
			"error", err,
			"task_hint", string(taskHint),
			"profile_header", headerProfile,
		)
		return nil, nil, true
	}

	reqBody.Model = decision.ChosenModel
	rewritten := rewriteBodyWithModel(rawBody, decision.ChosenModel)
	wire := decisionToWire(decision)
	if wire != nil {
		wire.failoverModels = append([]string(nil), decision.TierFailoverModels...)
		wire.signals = sigs
	}

	// Record the selection for the feedback loop. IDs and numbers only — no
	// prompt or conversation content (see telemetry.AutoSelection). Best-effort,
	// non-blocking: the async writer drops on a full queue rather than stalling.
	//
	// Pass the wire we already built (it carries wire.signals) — re-deriving a
	// fresh wire here loses the signals, and ExtractStructuredFeatures would
	// then compute every bucket from empty prompts (language=unknown, xs,
	// constant content_hash), leaving the training columns useless.
	recordAutoSelectionFromWire(r, sessionID, wire)

	return rewritten, wire, false
}

// recordAutoSelectionFromWire is the protocol-neutral selection sink used by
// non-chat handlers after their final gateway session has been resolved. The
// wire carries the same IDs, decision snapshot, and process-local winner
// metrics as the originating Decision, without retaining prompt content.
func recordAutoSelectionFromWire(r *http.Request, sessionID string, wire *autoRouteDecision) {
	if r == nil || wire == nil {
		return
	}
	telemetry.WriteAutoSelection(buildAutoSelection(r, sessionID, wire))
}

// buildAutoSelection translates the resolved wire into a telemetry row. The
// structured features MUST be derived from wire.signals — the signals the
// decider actually saw. Deriving them from a fresh/zero-value signals struct
// yields degenerate training columns (language=unknown, xs buckets, a single
// content_hash for every request; the 2026-09-07 dataset defect).
func buildAutoSelection(r *http.Request, sessionID string, wire *autoRouteDecision) telemetry.AutoSelection {
	// Extract structured features v1 from signals (non-reversible, privacy-safe)
	features := autoroute.ExtractStructuredFeatures(wire.signals, wire.Profile)

	return telemetry.AutoSelection{
		RequestID:         r.Header.Get("X-Request-Id"),
		SessionID:         sessionID,
		TaskID:            sanitizeRequestCorrelationID(r.Header.Get("X-Gw-Task-Id")),
		TaskType:          wire.TaskType,
		Profile:           wire.Profile,
		Classifier:        wire.Classifier,
		Confidence:        wire.Confidence,
		ChosenModel:       wire.ChosenModel,
		CandidateRank:     maxInt(wire.selectionCandidateRank, 1),
		CompositeScore:    wire.selectionCompositeScore,
		AffinityScore:     wire.selectionAffinityScore,
		AffinityApplied:   wire.selectionAffinityApplied,
		Explore:           wire.selectionExplore,
		FallbackUsed:      wire.FallbackUsed,
		ExperimentID:      wire.ExperimentID,
		Treatment:         string(wire.Treatment),
		AssignmentVersion: wire.AssignmentVersion,
		AssignmentKeyHash: wire.AssignmentKeyHash,
		// Structured features v1 (privacy-safe, non-reversible)
		DetectedLanguage:       features.DetectedLanguage,
		PromptLengthBucket:     features.PromptLengthBucket,
		ContextLengthBucket:    features.ContextLengthBucket,
		TurnCountBucket:        features.TurnCountBucket,
		HasCodeIndicator:       features.HasCodeIndicator,
		HasMathIndicator:       features.HasMathIndicator,
		HasTableIndicator:      features.HasTableIndicator,
		HasMultimediaIndicator: features.HasMultimediaIndicator,
		IntentCategory:         features.IntentCategory,
		DomainHint:             features.DomainHint,
		ComplexityBucket:       features.ComplexityBucket,
		LatencySensitive:       features.LatencySensitive,
		CostSensitive:          features.CostSensitive,
		FeatureVersion:         features.FeatureVersion,
		ContentHash:            features.ContentHash,
		// R50 (migration 731): role-route attribution — flag-off 时三值皆空
		//（wire 只在 flag on 时透出 SessionRole/TaskKind，RoutingSource 仅
		// role_route 命中时非空），旧行为字节不变。
		SessionRole:   wire.SessionRole,
		TaskKind:      wire.TaskKind,
		RoutingSource: wire.RoutingSource,
	}
}

func maxInt(value, fallback int) int {
	if value < 1 {
		return fallback
	}
	return value
}

// rewriteBodyWithModel produces a copy of the body with the model field
// replaced. Returns the original bytes on parse failure (best-effort —
// upstream provider will see the literal string "auto" and may 400).
func rewriteBodyWithModel(body []byte, newModel string) []byte {
	if len(body) == 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	m["model"] = newModel
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// decisionToWire converts an autoroute.Decision to the wire format.
func decisionToWire(d *autoroute.Decision) *autoRouteDecision {
	if d == nil {
		return nil
	}
	wire := &autoRouteDecision{
		TaskType:            string(d.TaskType),
		Confidence:          d.Confidence,
		Profile:             string(d.Profile),
		Classifier:          d.Classifier,
		Reason:              d.Reason,
		ChosenModel:         d.ChosenModel,
		ChosenRawModel:      d.ChosenRawModel,
		ChosenCredID:        d.ChosenCredentialID,
		EnabledFeatures:     d.EnabledFeatures,
		FilterReasons:       d.FilterReasons,
		CacheReused:         d.CacheReused,
		FallbackUsed:        d.FallbackUsed,
		ExperimentID:        d.ExperimentID,
		Treatment:           d.Treatment,
		AssignmentVersion:   d.AssignmentVersion,
		AssignmentKeyHash:   d.AssignmentKeyHash,
		EmbeddingShadowTask: d.EmbeddingShadowTask,
		// R48: 仅非空时透出，flag-off wire 字节不变。
		SessionRole: d.SessionRole,
		TaskKind:    d.TaskKind,
	}
	// R48: routing_source 仅 role_route 命中时出现在 wire（见字段注释）。
	if d.RoutingSource == "role_route" {
		wire.RoutingSource = d.RoutingSource
	}
	if d.EmbeddingShadowTask != "" {
		similarity := d.EmbeddingShadowSimilarity
		wire.EmbeddingShadowSimilarity = &similarity
	}
	for i, c := range d.CandidatesTopN {
		wire.CandidatesTop3 = append(wire.CandidatesTop3, autoRouteCandidate{
			Model:          c.Candidate.CanonicalName,
			Score:          c.Breakdown.Composite,
			Price:          c.Breakdown.PriceScore,
			Speed:          c.Breakdown.SpeedScore,
			Stability:      c.Breakdown.StabilityScore,
			Match:          c.Breakdown.MatchScore,
			Pressure:       c.Breakdown.PressureScore,
			ContextFit:     c.Breakdown.ContextFit,
			ChannelQuality: c.Breakdown.ChannelQuality,
			Reliability:    c.Breakdown.Reliability,
			RouteTier:      c.Breakdown.RouteTier,
		})
		if c.Candidate.CanonicalName == d.ChosenModel {
			wire.selectionCandidateRank = i + 1
			wire.selectionCompositeScore = c.Breakdown.Composite
			wire.selectionAffinityScore = c.Breakdown.Affinity
			wire.selectionAffinityApplied = c.Breakdown.AffinityApplied
			wire.selectionExplore = c.Breakdown.Explore
		}
	}
	return wire
}

// capDecisionSize truncates wire.CandidatesTop3 from the tail until
// serialisation fits in maxWireDecisionBytes. Always keeps the chosen
// model (top-1) intact. Returns the (possibly truncated) wire.
func capDecisionSize(wire *autoRouteDecision) *autoRouteDecision {
	if wire == nil || len(wire.CandidatesTop3) == 0 {
		return wire
	}
	for len(wire.CandidatesTop3) > 0 {
		b, err := json.Marshal(wire)
		if err != nil {
			return wire
		}
		if len(b) <= maxWireDecisionBytes {
			return wire
		}
		wire.CandidatesTop3 = wire.CandidatesTop3[:len(wire.CandidatesTop3)-1]
	}
	return wire
}

// writeAutoDecisionHeader serialises the decision as JSON and sets
// the response header. Errors are swallowed (header is best-effort).
func writeAutoDecisionHeader(w http.ResponseWriter, wire *autoRouteDecision) {
	if wire == nil {
		return
	}
	wire = capDecisionSize(wire)
	b, err := json.Marshal(wire)
	if err != nil {
		return
	}
	w.Header().Set(autoHeaderName, string(b))
}

// autoDecisionTrace renders a compact decision_trace projection of the
// auto-route decision wire, for routing_decision_log.decision_trace on
// auto-route successes (the executor Trace is only built on routing
// failures, which left the column an empty object in production and made
// fallback_used/task_type unobservable via SQL — 2026-09-14 audit O2).
// Returns nil when the wire JSON does not parse; the column then stays NULL.
func autoDecisionTrace(wireJSON []byte) json.RawMessage {
	var wire autoRouteDecision
	if err := json.Unmarshal(wireJSON, &wire); err != nil {
		return nil
	}
	b, err := json.Marshal(map[string]any{
		"source":               "auto_route",
		"task_type":            wire.TaskType,
		"fallback_used":        wire.FallbackUsed,
		"confidence":           wire.Confidence,
		"classifier":           wire.Classifier,
		"chosen_model":         wire.ChosenModel,
		"chosen_raw_model":     wire.ChosenRawModel,
		"chosen_credential_id": wire.ChosenCredID,
	})
	if err != nil {
		return nil
	}
	return b
}

// SetAutoRoute wires the autoroute decider. Call from main.go at startup.
// Passing nil disables auto-route (the handler falls back to default model
// for any model="auto" request).
func (h *ChatHandler) SetAutoRoute(d *autoroute.Decider) {
	h.decider = d
}

// countToolsInBody returns the length of the top-level "tools" array.
func countToolsInBody(body []byte) int {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return 0
	}
	toolsRaw, ok := obj["tools"]
	if !ok || len(toolsRaw) == 0 || string(toolsRaw) == "null" {
		return 0
	}
	return countJSONArrayLen(toolsRaw)
}

// mockChatResponse is the OpenAI-compatible body shape we emit when an
// authorised caller asks for a mock / auto-only response. The model field is
// the post-decider chosen model so the client can verify the auto routing
// actually rewrote model="auto" to the expected canonical name; the choice
// stays in X-Gw-Auto-Decision and the body model MUST match it so a downstream
// log scraper can JOIN the two without a lookup table.
type mockChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []mockChoice   `json:"choices"`
	Usage   map[string]any `json:"usage"`
	// mock_marker surfaces the test mode and the x-gw-test-mode value back
	// to the caller so a test runner can grep its own response and verify
	// the requested mode was honoured. It is NOT a standard OpenAI field;
	// documented as a test-only extension under X-Gw-Mock-Marker.
	MockMarker string `json:"mock_marker,omitempty"`
}

type mockChoice struct {
	Index        int            `json:"index"`
	Message      map[string]any `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

// mockAutoResponseContent is the assistant message we return for a mock
// chat completion. It is intentionally short and self-describing so a human
// inspecting the response can tell the answer did not come from a real LLM.
const mockAutoResponseContent = "[auto-test mock] routing decision verified; no upstream call was made."

// mockMarkerHeaders sets the two response headers every mock reply carries.
//
//	X-Gw-Mock-Marker  — the test mode that fired; lets a runner assert the
//	                    mode was honoured without parsing the body.
//	X-Gw-Test-Mode    — the request-time mode, echoed for symmetry with
//	                    other X-Gw-* request/response pairs.
func mockMarkerHeaders(w http.ResponseWriter, mode TestMode) {
	w.Header().Set("X-Gw-Mock-Marker", mode.String())
	w.Header().Set("X-Gw-Test-Mode", mode.String())
}

// writeMockChatResponse emits a synthetic OpenAI-format chat completion that
// echoes the auto-routing decision back to the caller and short-circuits the
// dispatch / upstream call. The header set:
//
//	X-Gw-Auto-Decision    — the same wire the live path emits
//	X-Gw-Mock-Marker      — human-readable marker of the test mode that fired
//
// mockModel is the chosen model (post-decider). mode is the parsed TestMode.
// requestID is propagated into the mock body id so a test can correlate
// request_logs rows with the response.
//
// stream selects the SSE framing. A client that sent "stream": true is
// holding an SSE connection open and will block until the stream is
// terminated by a [DONE] sentinel; replying with a single JSON object
// would leave such a client hanging until its own timeout, and an SDK
// would fail to parse it. See writeMockChatStream.
func writeMockChatResponse(w http.ResponseWriter, mode TestMode, requestID, mockModel string, stream bool) {
	if stream {
		writeMockChatStream(w, mode, requestID, mockModel)
		return
	}
	body := mockChatResponse{
		ID:      "chatcmpl-mock-" + requestID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   mockModel,
		Choices: []mockChoice{{
			Index: 0,
			Message: map[string]any{
				"role":    "assistant",
				"content": mockAutoResponseContent,
			},
			FinishReason: "stop",
		}},
		Usage: map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
			"mock":              true,
		},
		MockMarker: mode.String(),
	}
	w.Header().Set("Content-Type", "application/json")
	mockMarkerHeaders(w, mode)
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck // best-effort; HTTP write error is terminal for the response anyway
	_ = json.NewEncoder(w).Encode(body)
}

// writeSSEFrame writes one `data: <json>` SSE frame plus the blank line
// separator the SSE grammar requires. Shared by all three protocol
// mock stream writers.
func writeSSEFrame(w http.ResponseWriter, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	//nolint:errcheck // best-effort; client disconnect ends the response anyway
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

// writeSSEEvent writes one named SSE event (`event: <name>` + data),
// required by the Anthropic and Responses stream grammars.
func writeSSEEvent(w http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	//nolint:errcheck
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

// writeMockChatStream emits the OpenAI chat-completions SSE framing:
// a role delta, a content delta carrying the marker text, a finish
// chunk, and the terminating `data: [DONE]` sentinel. Without the
// sentinel an SSE reader never sees end-of-stream.
func writeMockChatStream(w http.ResponseWriter, mode TestMode, requestID, mockModel string) {
	id := "chatcmpl-mock-" + requestID
	created := time.Now().Unix()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	mockMarkerHeaders(w, mode)
	w.WriteHeader(http.StatusOK)

	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   mockModel,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
	}
	writeSSEFrame(w, chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	writeSSEFrame(w, chunk(map[string]any{"content": mockAutoResponseContent}, nil))
	writeSSEFrame(w, chunk(map[string]any{}, "stop"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	//nolint:errcheck
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeMockMessagesResponse emits a synthetic Anthropic-format /v1/messages
// response. Mirror of writeMockChatResponse for the Anthropic protocol —
// same shape (content blocks), same MockMarker, same X-Gw-Mock-Marker
// response header so test runners can find the mark regardless of endpoint.
func writeMockMessagesResponse(w http.ResponseWriter, mode TestMode, requestID, mockModel string, stream bool) {
	if stream {
		writeMockMessagesStream(w, mode, requestID, mockModel)
		return
	}
	body := map[string]any{
		"id":            "msg-mock-" + requestID,
		"type":          "message",
		"role":          "assistant",
		"model":         mockModel,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []map[string]any{
			{"type": "text", "text": mockAutoResponseContent},
		},
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"mock":          true,
		},
		"mock_marker": mode.String(),
	}
	w.Header().Set("Content-Type", "application/json")
	mockMarkerHeaders(w, mode)
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck
	_ = json.NewEncoder(w).Encode(body)
}

// writeMockMessagesStream emits the Anthropic Messages SSE grammar:
// message_start → content_block_start → content_block_delta →
// content_block_stop → message_delta → message_stop. The event names and
// their order are part of the protocol contract; an SDK that sees them
// out of order (or missing message_stop) treats the turn as truncated.
func writeMockMessagesStream(w http.ResponseWriter, mode TestMode, requestID, mockModel string) {
	id := "msg-mock-" + requestID

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	mockMarkerHeaders(w, mode)
	w.WriteHeader(http.StatusOK)

	writeSSEEvent(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         mockModel,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})
	writeSSEEvent(w, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	writeSSEEvent(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": mockAutoResponseContent},
	})
	writeSSEEvent(w, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})
	writeSSEEvent(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 0},
	})
	writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeMockResponsesResponse emits a synthetic OpenAI Responses API
// (/v1/responses) shape. Mirror of writeMockChatResponse for the Responses
// protocol.
func writeMockResponsesResponse(w http.ResponseWriter, mode TestMode, requestID, mockModel string, stream bool) {
	if stream {
		writeMockResponsesStream(w, mode, requestID, mockModel)
		return
	}
	body := map[string]any{
		"id":         "resp-mock-" + requestID,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      mockModel,
		"status":     "completed",
		"output": []map[string]any{
			{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": mockAutoResponseContent}},
			},
		},
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"mock":          true,
		},
		"mock_marker": mode.String(),
	}
	w.Header().Set("Content-Type", "application/json")
	mockMarkerHeaders(w, mode)
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck
	_ = json.NewEncoder(w).Encode(body)
}

// writeMockResponsesStream emits the OpenAI Responses SSE grammar:
// response.created → response.output_item.added →
// response.output_text.delta → response.output_item.done →
// response.completed. The terminal response.completed event is what a
// Responses SDK waits for; without it the client blocks.
func writeMockResponsesStream(w http.ResponseWriter, mode TestMode, requestID, mockModel string) {
	id := "resp-mock-" + requestID
	created := time.Now().Unix()

	shell := func(status string) map[string]any {
		return map[string]any{
			"id":          id,
			"object":      "response",
			"created_at":  created,
			"model":       mockModel,
			"status":      status,
			"output":      []any{},
			"usage":       nil,
			"mock_marker": mode.String(),
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	mockMarkerHeaders(w, mode)
	w.WriteHeader(http.StatusOK)

	writeSSEEvent(w, "response.created", map[string]any{
		"type": "response.created", "response": shell("in_progress"),
	})
	writeSSEEvent(w, "response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]any{
			"type": "message", "id": id + "-msg", "role": "assistant", "content": []any{},
		},
	})
	writeSSEEvent(w, "response.output_text.delta", map[string]any{
		"type":         "response.output_text.delta",
		"output_index": 0,
		"item_id":      id + "-msg",
		"delta":        mockAutoResponseContent,
	})
	writeSSEEvent(w, "response.output_text.done", map[string]any{
		"type":         "response.output_text.done",
		"output_index": 0,
		"item_id":      id + "-msg",
		"text":         mockAutoResponseContent,
	})
	writeSSEEvent(w, "response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item": map[string]any{
			"type": "message", "id": id + "-msg", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": mockAutoResponseContent}},
		},
	})
	completed := shell("completed")
	completed["output"] = []map[string]any{{
		"type":    "message",
		"id":      id + "-msg",
		"role":    "assistant",
		"content": []map[string]any{{"type": "output_text", "text": mockAutoResponseContent}},
	}}
	completed["usage"] = map[string]any{
		"input_tokens": 0, "output_tokens": 0, "mock": true,
	}
	writeSSEEvent(w, "response.completed", map[string]any{
		"type": "response.completed", "response": completed,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
