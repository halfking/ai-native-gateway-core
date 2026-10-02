package executors

// responses_mode_bridge.go — the chat→responses direction of the protocol
// adapter.
//
// Background (2026-10-02, vapEUR / credential 126 hxt-local): the gateway has
// long had the responses→chat fallback (tryUnsupportedResponsesFallback in
// executor_chat.go) for providers that reject the Responses API. The INVERSE —
// a model that exists ONLY on /responses and 400s on /chat/completions — had no
// fallback at all. Measured on vapEUR:
//
//	gpt-5.3-codex   /v1/responses → 200     /v1/chat/completions → 400 {"code":4006,
//	               "message":"The requested operation is unsupported."}
//
// The node probe walks /v1/responses (providercap.Resolve maps
// openai-responses → EpResponses), so it saw 200 and called the node healthy,
// while every chat-shaped data-plane request failed. reqprobe even classifies
// this shape and sets SuggestMode:"responses", but internal/reqprobe/diagnose.go
// documents that the executor "不自动切换" — the suggestion was never acted on.
//
// This file supplies the two pure conversions that close the loop on top of the
// existing IR (the same machinery the OpenAI↔Anthropic bridge already uses):
//
//	chatBodyToResponsesBody : ir.ParseOpenAI → ir.SerializeResponsesRequest
//	responsesBodyToChatBody : ir.ParseResponsesResponse → ir.SerializeOpenAIResponse
//
// Both are pure functions over bytes so they are unit-testable without a
// gateway, an upstream, or a database.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/providercap"
)

// chatUnsupportedStatuses are the HTTP statuses under which a
// /v1/chat/completions rejection can carry the relay's "this model is only
// served by the Responses API" verdict.
//
// Observed in the wild (vapeur, 2026-10-02): 400 with body
// {"code":4006,"message":"The requested operation is unsupported."}.
// The remaining codes are the nearby endpoint/verb shapes that other relays
// use for the same verdict.
var chatUnsupportedStatuses = map[int]bool{
	400: true, 404: true, 405: true, 415: true, 422: true, 501: true, 502: true,
}

// chatRequiresResponsesPhrases are the body substrings that positively state
// the model is only reachable through the Responses API.
//
// The set deliberately does NOT include vapEUR's bare "The requested operation
// is unsupported." on its own — see chatRequiresResponses for why that verdict
// is accepted under an extra condition instead.
var chatRequiresResponsesPhrases = []string{
	"use the responses api",
	"use /v1/responses",
	"requires the responses api",
	"only supported via the responses api",
	"this model only supports the responses",
	"must use the responses api",
	"only supports the responses api",
	// CJK relays (vapeur's Claude family answers 该供应商不支持 Responses API on
	// the responses leg) state the same verdict in Chinese. Without these the
	// Chinese-wording half of the fleet would never be recognised.
	"仅支持 responses api",
	"只支持 responses api",
	"需要使用 responses api",
	"请使用 responses api",
	"不支持该接口",
}

// chatUnsupportedGenericPhrases are the shape-agnostic "not supported" verdicts
// that relays emit for responses-only models without naming the API. They are
// only honoured for openai-responses providers (the caller gates on protocol),
// because for any other provider "unsupported" means something else entirely
// (unknown model, disabled feature, …) and guessing would burn a doomed retry.
var chatUnsupportedGenericPhrases = []string{
	"the requested operation is unsupported",
	"requested operation is not supported",
	"operation unsupported",
	"不支持该操作",
	"不支持此操作",
}

// chatRequiresResponses reports whether a failed /v1/chat/completions response
// is the upstream's "this model is Responses-only" verdict — a per-model
// protocol capability gap on a relay that is itself declared
// openai-responses, NOT a dead node and NOT a dead model.
//
// Two accepted shapes:
//
//  1. Explicit: the body names the Responses API (English or CJK 不支持 …
//     Responses API). Unambiguous on any provider.
//
//  2. Generic, provider-gated: the body is a bare "operation is unsupported"
//     verdict. vapEUR's code 4006 is exactly this and never mentions the API,
//     yet re-probing cannot heal it — only the other wire format can. Callers
//     MUST gate this branch on the candidate's protocol being
//     openai-responses; without that gate it would fire on unrelated 4xx.
//
// Explicitly NOT matched: parameter-shaped rejections ("unsupported parameter:
// 'messages'", CJK 不支持该参数). Those are request-shape bugs — a chat-shaped
// request is simply malformed — and switching wire format would mask the bug
// and hide it from the probe. Mirrors providercap.ResponsesUnsupportedError,
// which makes the same carve-out for the opposite direction.
func chatRequiresResponses(httpStatus int, body string) bool {
	if !chatUnsupportedStatuses[httpStatus] {
		return false
	}
	b := strings.ToLower(strings.TrimSpace(body))
	if b == "" {
		return false
	}
	// Parameter-shaped rejections are probe/request-shape bugs in both
	// directions (providercap.ResponsesUnsupportedError applies the same guard
	// for the responses leg).
	paramShaped := strings.Contains(b, "parameter") || strings.Contains(b, "参数") ||
		strings.Contains(b, "argument")
	if paramShaped {
		return false
	}
	for _, p := range chatRequiresResponsesPhrases {
		if strings.Contains(b, p) {
			return true
		}
	}
	for _, p := range chatUnsupportedGenericPhrases {
		if strings.Contains(b, p) {
			return true
		}
	}
	return false
}

// chatBodyToResponsesBody converts an OpenAI Chat Completions request body into
// an OpenAI Responses request body via the shared IR.
//
// outboundModel overrides the model name with the candidate's raw model (the
// same rewrite the native path applies via
// transformation.RewriteResponsesModel), because the IR parser copies the
// client-supplied model verbatim and the dispatch loop has already decided
// which concrete upstream model this attempt targets.
//
// Returns ok=false when the chat body cannot be parsed, so the caller can leave
// the original chat attempt to fail on its own rather than masking it.
func chatBodyToResponsesBody(chatBody []byte, outboundModel string) (out []byte, ok bool) {
	if len(strings.TrimSpace(string(chatBody))) == 0 {
		return nil, false
	}
	parsed, err := ir.ParseOpenAI(chatBody)
	if err != nil || parsed == nil {
		return nil, false
	}
	if m := strings.TrimSpace(outboundModel); m != "" {
		parsed.Model = m
	}
	serialized, err := ir.SerializeResponsesRequest(parsed)
	if err != nil || len(serialized) == 0 {
		return nil, false
	}
	return serialized, true
}

// responsesBodyToChatBody converts a non-streaming OpenAI Responses response
// body into the Chat Completions response body a chat-shaped client expects.
//
// clientModel is echoed back in the rendered `model` field, matching what
// convertChatResponseToResponses does in the opposite direction (the gateway
// reports the model the client asked for, not the upstream alias).
//
// The shape guard below is load-bearing, not defensive noise. Without it,
// ir.ParseResponsesResponse happily accepts ANY JSON object (e.g. an upstream
// error envelope, or a relay's `{"unrelated":true}`) and hands back an empty
// InternalResponse, which SerializeOpenAIResponse then renders as a perfectly
// valid but content-less chat completion. A chat-shaped client would then see
// an empty 200 answer where the upstream had actually failed — the error would
// be swallowed rather than surfaced. So the input must positively identify
// itself as a Responses object before we convert.
//
// Returns ok=false when the body is not a Responses object, cannot be parsed,
// or cannot be converted; the caller must then surface the upstream body
// unchanged rather than writing a half-rendered envelope to the client.
func responsesBodyToChatBody(respBody []byte, clientModel string) (out []byte, ok bool) {
	trimmed := strings.TrimSpace(string(respBody))
	if trimmed == "" {
		return nil, false
	}
	var probe struct {
		Object   string          `json:"object"`
		ID       string          `json:"id"`
		Output   json.RawMessage `json:"output"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, false
	}
	// An error envelope is not a completion — never launder it into an empty one.
	if len(probe.Error) > 0 && string(probe.Error) != "null" {
		return nil, false
	}
	if probe.Object != "response" {
		return nil, false
	}
	parsed, err := ir.ParseResponsesResponse([]byte(trimmed))
	if err != nil || parsed == nil {
		return nil, false
	}
	converted, err := ir.SerializeOpenAIResponse(parsed, clientModel)
	if err != nil || len(converted) == 0 {
		return nil, false
	}
	// A conversion that lost every output item would silently answer the client
	// with an empty completion. The gateway already has a contract for that
	// shape (isNonStreamEmptyResponse → candidate failover); refuse it here so
	// the caller reaches that path instead of laundering an empty answer into a
	// 200. A tool-call-only completion counts as content.
	var rendered struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(converted, &rendered); err != nil {
		return nil, false
	}
	if rendered.Object != "chat.completion" || len(rendered.Choices) == 0 {
		return nil, false
	}
	if !chatMessageCarriesContent(rendered.Choices[0].Message) {
		return nil, false
	}
	return converted, true
}

// chatMessageCarriesContent reports whether a rendered chat message would give
// the client anything. Blank-but-present strings count as empty: whitespace and
// a bare newline are what a truncated upstream returns, and forwarding them as
// a successful completion is exactly the silent-failure shape to avoid.
func chatMessageCarriesContent(msg struct {
	Content          json.RawMessage `json:"content"`
	ToolCalls        json.RawMessage `json:"tool_calls"`
	ReasoningContent json.RawMessage `json:"reasoning_content"`
}) bool {
	nonBlank := func(raw json.RawMessage) bool {
		s := strings.TrimSpace(string(raw))
		if s == "" || s == "null" {
			return false
		}
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			return strings.TrimSpace(str) != ""
		}
		// Non-string payload: a non-empty array/object counts as content.
		return s != "[]" && s != "{}"
	}
	return nonBlank(msg.Content) || nonBlank(msg.ToolCalls) || nonBlank(msg.ReasoningContent)
}

// responsesCapabilityUnsupportedBody reports whether a Responses-leg rejection
// still looks like the "unsupported API" verdict under the shared detector.
// Re-exported as a thin wrapper so executor_chat.go keeps one import surface
// for both directions of the same contract.
func responsesCapabilityUnsupportedBody(status int, body string) bool {
	return providercap.ResponsesUnsupportedError(status, body)
}

// describeBridgeOutcome is a small helper for log lines that must not leak the
// request body. Keeps the fallback logs uniform without importing a logger here.
func describeBridgeOutcome(converted bool) string {
	if converted {
		return "converted"
	}
	return fmt.Sprintf("%s", "passthrough")
}
