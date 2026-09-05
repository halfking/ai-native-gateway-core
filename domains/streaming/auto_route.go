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
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"

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
	return "claude-sonnet-4.5"
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

// autoRouteDecision is the wire format of X-Gw-Auto-Decision. Stable
// JSON schema — clients may parse it for observability.
type autoRouteDecision struct {
	TaskType                  string               `json:"task_type"`
	Confidence                float64              `json:"confidence"`
	Profile                   string               `json:"profile"`
	Classifier                string               `json:"classifier"`
	Reason                    string               `json:"reason"`
	ChosenModel               string               `json:"chosen_model"`
	ChosenRawModel            string               `json:"chosen_raw_model"`
	ChosenCredID              int64                `json:"chosen_credential_id"`
	EnabledFeatures           []string             `json:"enabled_features,omitempty"`
	FilterReasons             []string             `json:"filter_reasons,omitempty"`
	CacheReused               bool                 `json:"cache_reused"`
	FallbackUsed              bool                 `json:"fallback_used"`
	ExperimentID              string               `json:"experiment,omitempty"`
	Treatment                 autoroute.Treatment  `json:"treatment,omitempty"`
	AssignmentVersion         string               `json:"assignment_version,omitempty"`
	AssignmentKeyHash         string               `json:"assignment_key_hash,omitempty"`
	EmbeddingShadowTask       string               `json:"embedding_shadow_task,omitempty"`
	EmbeddingShadowSimilarity *float64             `json:"embedding_shadow_similarity,omitempty"`
	CandidatesTop3            []autoRouteCandidate `json:"candidates_top3"`

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

// estimateTokens uses a conservative heuristic: 4 chars per token for
// latin, 1.5 per CJK rune. Returns 0 for empty input.
func estimateTokens(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	tokens := 0
	for i := 0; i < len(b); {
		r, size := decodeRune(b[i:])
		if r >= 0x4E00 && r <= 0x9FFF {
			tokens += 2 // 1 CJK char ≈ 1.5-2 tokens
		} else {
			tokens++ // 1 ascii byte ≈ 0.25 token, so 4 bytes ≈ 1 token
			// But we count per byte, so adjust by counting 4-byte groups
		}
		i += size
	}
	// Adjust: the per-byte count for ASCII underweights, so divide by 4
	// for ASCII portion. Cheap approximation; accuracy is ~±30%.
	return tokens / 4
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
	recordAutoSelection(r, sessionID, decision)

	return rewritten, wire, false
}

// recordAutoSelection enqueues one auto_route_selections row from a Decision.
//
// MEDIUM-6 fix: the Explore flag is recorded here (deterministically, by
// hashing the same requestID with the same ratio the scoring path used) so
// the P3 acceptance criterion — applied-group reward vs explore-group reward —
// has an observable explore arm. Without this, llmgw_autoroute_explore_total is
// flat-zero and the shadow/rollout split is invisible.
//
// canonical_id and tenant_id are not set here — the settle worker backfills
// them from request_logs_hot (see CRITICAL-2 Fix A). Storing the canonical
// *name* now keeps the row useful even if the id is never resolved.
func recordAutoSelection(r *http.Request, sessionID string, decision *autoroute.Decision) {
	if decision == nil {
		return
	}
	recordAutoSelectionFromWire(r, sessionID, decisionToWire(decision))
}

// recordAutoSelectionFromWire is the protocol-neutral selection sink used by
// non-chat handlers after their final gateway session has been resolved. The
// wire carries the same IDs, decision snapshot, and process-local winner
// metrics as the originating Decision, without retaining prompt content.
func recordAutoSelectionFromWire(r *http.Request, sessionID string, wire *autoRouteDecision) {
	if r == nil || wire == nil {
		return
	}

	// Extract structured features v1 from signals (non-reversible, privacy-safe)
	features := autoroute.ExtractStructuredFeatures(wire.signals, wire.Profile)

	telemetry.WriteAutoSelection(telemetry.AutoSelection{
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
	})
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
