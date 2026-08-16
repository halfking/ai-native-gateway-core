package streaming

// auto_route_nonchat.go — model=auto for /v1/messages (Anthropic) and
// /v1/responses (OpenAI Responses API).
//
// These endpoints share the same auto-route shape as /v1/chat/completions
// (see auto_route.go maybeResolveAuto): detect model=="auto", extract
// classification signals, call the Decider, rewrite the model field. The
// only difference is the request body shape, so each gets its own signal
// extractor.
//
// Embeddings (model=auto) is handled separately in embeddings.go because
// it bypasses task classification entirely (uses RecommendByModality).

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// extractSignalsForMessages builds ClassificationSignals from an Anthropic
// /v1/messages body. Anthropic messages have the same {role, content} shape
// as OpenAI chat, plus a top-level `system` string. Tools come from the
// `tools` array.
func extractSignalsForMessages(reqBody *messagesRequestBody, rawBody []byte) autoroute.ClassificationSignals {
	sigs := autoroute.ClassificationSignals{
		SystemPrompt:    reqBody.System,
		ToolCount:       countToolsInBody(rawBody),
		EstimatedTokens: estimateTokens(rawBody),
	}
	if len(reqBody.Messages) > 0 {
		sigs.MessageCount = countJSONArrayLen(reqBody.Messages)
	}
	// Walk messages for last user prompt + image parts + tool results.
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(reqBody.Messages, &msgs); err == nil {
		for _, m := range msgs {
			switch m.Role {
			case "user":
				// string content
				var s string
				if err := json.Unmarshal(m.Content, &s); err == nil {
					sigs.LastUserPrompt = s
					continue
				}
				// array of content blocks
				var blocks []struct {
					Type string          `json:"type"`
					Text string          `json:"text"`
					Src  json.RawMessage `json:"source"`
				}
				if err := json.Unmarshal(m.Content, &blocks); err == nil {
					var buf []byte
					for _, b := range blocks {
						switch b.Type {
						case "text", "":
							buf = append(buf, []byte(b.Text)...)
							buf = append(buf, ' ')
						case "image":
							sigs.HasImages = true
						}
					}
					sigs.LastUserPrompt = string(buf)
				}
			case "tool":
				sigs.HasToolResults = true
			}
		}
	}
	if bytes.Contains(rawBody, []byte("```")) {
		sigs.HasCodeBlock = true
	}
	sigs.Language = detectLanguage(sigs.LastUserPrompt + sigs.SystemPrompt)
	return sigs
}

// extractSignalsForResponses builds ClassificationSignals from an OpenAI
// /v1/responses body. `instructions` is the system prompt; `input` is a
// string or an array of input items.
func extractSignalsForResponses(reqBody *responsesRequestBody, rawBody []byte) autoroute.ClassificationSignals {
	sigs := autoroute.ClassificationSignals{
		SystemPrompt:    reqBody.Instructions,
		ToolCount:       countToolsInBody(rawBody),
		EstimatedTokens: estimateTokens(rawBody),
	}
	// input: string | array of {role, content}
	if len(reqBody.Input) > 0 {
		var s string
		if err := json.Unmarshal(reqBody.Input, &s); err == nil {
			sigs.LastUserPrompt = s
			sigs.MessageCount = 1
		} else {
			var items []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(reqBody.Input, &items); err == nil {
				sigs.MessageCount = len(items)
				for _, it := range items {
					if it.Role == "user" {
						sigs.LastUserPrompt = string(it.Content)
					}
				}
			}
		}
	}
	if bytes.Contains(rawBody, []byte("```")) {
		sigs.HasCodeBlock = true
	}
	sigs.Language = detectLanguage(sigs.LastUserPrompt + sigs.SystemPrompt)
	return sigs
}

// maybeResolveAutoForMessages is the /v1/messages counterpart of
// maybeResolveAuto. Returns (rewrittenBody, wireDecision, shouldFail).
// On non-auto requests returns (nil, nil, false).
func (h *MessagesHandler) maybeResolveAutoForMessages(reqBody *messagesRequestBody, rawBody []byte, r *http.Request, apiKeyID int) ([]byte, *autoRouteDecision, bool) {
	if reqBody.Model != autoRequestMagic {
		return nil, nil, false
	}
	// Flag gate: off → no-op (caller sees "not auto", model stays "auto").
	if flags := autoroute.GetFeatureFlags(); flags == nil || !flags.AutoOnMessages {
		return nil, nil, false
	}
	decider := h.chatHandler.decider
	if decider == nil {
		reqBody.Model = autoFallbackModel()
		return rewriteBodyWithModel(rawBody, autoFallbackModel()), nil, false
	}
	sigs := extractSignalsForMessages(reqBody, rawBody)
	sigs.ClientType = extractClientTypeWithPrompt(r, sigs.SystemPrompt)
	headerProfile := r.Header.Get(autoProfileHeader)
	taskHint := autoroute.TaskType(r.Header.Get(autoTaskHintHeader))
	sessionID := r.Header.Get("X-Gw-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-Id")
	}
	reqCtx := r.Context()
	if workType := strings.TrimSpace(r.Header.Get(autoWorkTypeHeader)); workType != "" {
		if l1, ok := decider.ResolveWorkType(workType); ok {
			taskHint = l1
			reqCtx = autoroute.WithWorkType(reqCtx, workType)
		}
	}
	decision, err := decider.DecideWithFeatureFlags(reqCtx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	if err != nil {
		return nil, nil, true // caller emits 502 auto_route_decider_failed
	}
	reqBody.Model = decision.ChosenModel
	return rewriteBodyWithModel(rawBody, decision.ChosenModel), decisionToWire(decision), false
}

// maybeResolveAutoForResponses is the /v1/responses counterpart.
func (h *ResponsesHandler) maybeResolveAutoForResponses(reqBody *responsesRequestBody, rawBody []byte, r *http.Request, apiKeyID int) ([]byte, *autoRouteDecision, bool) {
	if reqBody.Model != autoRequestMagic {
		return nil, nil, false
	}
	if flags := autoroute.GetFeatureFlags(); flags == nil || !flags.AutoOnResponses {
		return nil, nil, false
	}
	decider := h.chatHandler.decider
	if decider == nil {
		reqBody.Model = autoFallbackModel()
		return rewriteBodyWithModel(rawBody, autoFallbackModel()), nil, false
	}
	sigs := extractSignalsForResponses(reqBody, rawBody)
	sigs.ClientType = extractClientTypeWithPrompt(r, sigs.SystemPrompt)
	headerProfile := r.Header.Get(autoProfileHeader)
	taskHint := autoroute.TaskType(r.Header.Get(autoTaskHintHeader))
	sessionID := r.Header.Get("X-Gw-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-Id")
	}
	reqCtx := r.Context()
	if workType := strings.TrimSpace(r.Header.Get(autoWorkTypeHeader)); workType != "" {
		if l1, ok := decider.ResolveWorkType(workType); ok {
			taskHint = l1
			reqCtx = autoroute.WithWorkType(reqCtx, workType)
		}
	}
	decision, err := decider.DecideWithFeatureFlags(reqCtx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	if err != nil {
		return nil, nil, true
	}
	reqBody.Model = decision.ChosenModel
	return rewriteBodyWithModel(rawBody, decision.ChosenModel), decisionToWire(decision), false
}
