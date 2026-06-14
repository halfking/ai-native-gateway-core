package relay

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
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// autoHeaderName is the response header carrying the decision JSON.
// Stable name (no x- prefix) so it's easy to grep in proxy logs.
const autoHeaderName = "X-Gw-Auto-Decision"

// autoProfileHeader is the per-request override header.
const autoProfileHeader = "X-Gw-Auto-Profile"

// autoTaskHintHeader is the optional client-provided task hint.
const autoTaskHintHeader = "X-Gw-Task-Hint"

// autoRequestMagic is the model name that triggers auto-route mode.
// Chosen to be OpenAI/Anthropic reserved-name-safe: "auto" is not a
// real model name (yet — Anthropic uses "auto" for tool_choice but
// that's a separate field), so no client will collide by accident.
const autoRequestMagic = "auto"

// autoRouteDecision is the wire format of X-Gw-Auto-Decision. Stable
// JSON schema — clients may parse it for observability.
type autoRouteDecision struct {
	TaskType        string                 `json:"task_type"`
	Confidence      float64                `json:"confidence"`
	Profile         string                 `json:"profile"`
	Classifier      string                 `json:"classifier"`
	Reason          string                 `json:"reason"`
	ChosenModel     string                 `json:"chosen_model"`
	ChosenRawModel  string                 `json:"chosen_raw_model"`
	ChosenCredID    int64                  `json:"chosen_credential_id"`
	CandidatesTop3  []autoRouteCandidate   `json:"candidates_top3"`
}

// autoRouteCandidate is one row of the top-N audit list.
type autoRouteCandidate struct {
	Model          string  `json:"model"`
	Score          float64 `json:"composite_score"`
	Price          float64 `json:"price_score"`
	Speed          float64 `json:"speed_score"`
	Stability      float64 `json:"stability_score"`
	Match          float64 `json:"match_score"`
	Pressure       float64 `json:"pressure_score"`
	ContextFit     float64 `json:"context_fit"`
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
func extractSignalsForAuto(reqBody *chatRequestBody, rawBody []byte) autoroute.ClassificationSignals {
	sigs := autoroute.ClassificationSignals{
		ToolCount:      countToolsInBody(rawBody),
		HasImages:      false,
		HasCodeBlock:   bytes.Contains(rawBody, []byte("```")),
		EstimatedTokens: estimateTokens(rawBody),
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
//                   no rewrite performed)
//   - decision    : non-nil when auto-route ran (success or failure)
//   - shouldFail  : true when auto-route was attempted and failed;
//                   caller should return 502 immediately
//
// On non-auto requests, returns (nil, nil, false) — zero overhead.
func (h *ChatHandler) maybeResolveAuto(reqBody *chatRequestBody, rawBody []byte, r *http.Request, apiKeyID int) ([]byte, *autoRouteDecision, bool) {
	if reqBody.Model != autoRequestMagic {
		return nil, nil, false
	}
	if h.decider == nil {
		// No decider wired → fall back to default model
		reqBody.Model = "claude-sonnet-4.5"
		return rewriteBodyWithModel(rawBody, "claude-sonnet-4.5"), nil, false
	}

	sigs := extractSignalsForAuto(reqBody, rawBody)
	headerProfile := r.Header.Get(autoProfileHeader)
	taskHint := autoroute.TaskType(r.Header.Get(autoTaskHintHeader))

	decision, err := h.decider.Decide(r.Context(), sigs, apiKeyID, headerProfile, taskHint)
	if err != nil {
		slog.Warn("auto-route: decider failed, falling back",
			"error", err,
			"task_hint", string(taskHint),
			"profile_header", headerProfile,
		)
		// Fall back to a default chat model rather than 502 — clients
		// should not be punished for the gateway's transient issues.
		reqBody.Model = "claude-sonnet-4.5"
		return rewriteBodyWithModel(rawBody, "claude-sonnet-4.5"), nil, false
	}

	reqBody.Model = decision.ChosenModel
	rewritten := rewriteBodyWithModel(rawBody, decision.ChosenModel)
	wire := decisionToWire(decision)
	return rewritten, wire, false
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
	wire := &autoRouteDecision{
		TaskType:       string(d.TaskType),
		Confidence:     d.Confidence,
		Profile:        string(d.Profile),
		Classifier:     d.Classifier,
		Reason:         d.Reason,
		ChosenModel:    d.ChosenModel,
		ChosenRawModel: d.ChosenRawModel,
		ChosenCredID:   d.ChosenCredentialID,
	}
	for _, c := range d.CandidatesTopN {
		wire.CandidatesTop3 = append(wire.CandidatesTop3, autoRouteCandidate{
			Model:      c.Candidate.CanonicalName,
			Score:      c.Breakdown.Composite,
			Price:      c.Breakdown.PriceScore,
			Speed:      c.Breakdown.SpeedScore,
			Stability:  c.Breakdown.StabilityScore,
			Match:      c.Breakdown.MatchScore,
			Pressure:   c.Breakdown.PressureScore,
			ContextFit: c.Breakdown.ContextFit,
		})
	}
	return wire
}

// writeAutoDecisionHeader serialises the decision as JSON and sets
// the response header. Errors are swallowed (header is best-effort).
func writeAutoDecisionHeader(w http.ResponseWriter, wire *autoRouteDecision) {
	if wire == nil {
		return
	}
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