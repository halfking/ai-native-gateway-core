package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

var errInvalidSanitizeInput = errors.New("invalid sanitizable request")

type authenticatedTenantContextKey struct{}

// WithAuthenticatedTenant binds the namespace supplied by a verified API key.
// Request headers are never accepted as a tenant authority by the sanitizer.
func WithAuthenticatedTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, authenticatedTenantContextKey{}, tenantID)
}

func authenticatedTenant(ctx context.Context) string {
	tenantID, _ := ctx.Value(authenticatedTenantContextKey{}).(string)
	return tenantID
}

// requestInputSanitizer changes only protocol text fields. RawMessage keeps
// opaque image, audio, document, tool schema and extension payloads intact.
type requestInputSanitizer struct {
	ctx       context.Context
	sanitizer *Sanitizer
	offset    map[SensitiveType]int
	mapping   SanitizeMap
	usedCount map[string]int
}

func (s *requestInputSanitizer) sanitizeEnvelope(body []byte, path string) ([]byte, []compression.SanitizedMessageRef, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return nil, nil, fmt.Errorf("%w: JSON object required: %v", errInvalidSanitizeInput, err)
	}
	changed := false
	var refs []compression.SanitizedMessageRef
	switch path {
	case "/v1/messages":
		if raw, ok := envelope["system"]; ok {
			updated, didChange, err := s.sanitizeContent(raw)
			if err != nil {
				return nil, nil, err
			}
			if didChange {
				envelope["system"], changed = updated, true
			}
		}
		var err error
		refs, changed, err = s.sanitizeMessageList(envelope, "messages", changed)
		if err != nil {
			return nil, nil, err
		}
	case "/v1/responses":
		if raw, ok := envelope["instructions"]; ok {
			updated, didChange, err := s.sanitizeText(raw)
			if err != nil {
				return nil, nil, err
			}
			if didChange {
				envelope["instructions"], changed = updated, true
			}
		}
		if raw, ok := envelope["input"]; ok {
			updated, inputRefs, didChange, err := s.sanitizeResponsesInput(raw)
			if err != nil {
				return nil, nil, err
			}
			refs = inputRefs
			if didChange {
				envelope["input"], changed = updated, true
			}
		}
	case "/v1/completions":
		if raw, ok := envelope["prompt"]; ok {
			updated, promptRefs, didChange, err := s.sanitizeCompletionPrompt(raw)
			if err != nil {
				return nil, nil, err
			}
			refs = promptRefs
			if didChange {
				envelope["prompt"], changed = updated, true
			}
		}
	default:
		var err error
		refs, changed, err = s.sanitizeMessageList(envelope, "messages", false)
		if err != nil {
			return nil, nil, err
		}
	}
	if !changed {
		return nil, nil, nil
	}
	updated, err := json.Marshal(envelope)
	return updated, refs, err
}

func (s *requestInputSanitizer) sanitizeCompletionPrompt(raw json.RawMessage) (json.RawMessage, []compression.SanitizedMessageRef, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, nil, false, nil
	}
	if trimmed[0] == '"' {
		before := len(s.mapping)
		updated, changed, err := s.sanitizeText(raw)
		return updated, []compression.SanitizedMessageRef{messageRef(0, raw, updated, changed, len(s.mapping)-before)}, changed, err
	}
	if trimmed[0] != '[' {
		return raw, nil, false, nil // Token-ID prompt, if supported by the upstream.
	}
	var prompts []json.RawMessage
	if err := json.Unmarshal(raw, &prompts); err != nil {
		return nil, nil, false, fmt.Errorf("%w: prompt: %v", errInvalidSanitizeInput, err)
	}
	refs := make([]compression.SanitizedMessageRef, 0, len(prompts))
	changed := false
	for i, prompt := range prompts {
		before := len(s.mapping)
		updated := prompt
		didChange := false
		if item := bytes.TrimSpace(prompt); len(item) > 0 && item[0] == '"' {
			var err error
			updated, didChange, err = s.sanitizeText(prompt)
			if err != nil {
				return nil, nil, false, err
			}
		}
		refs = append(refs, messageRef(i, prompt, updated, didChange, len(s.mapping)-before))
		if didChange {
			prompts[i], changed = updated, true
		}
	}
	if !changed {
		return raw, refs, false, nil
	}
	updated, err := json.Marshal(prompts)
	return updated, refs, true, err
}

func (s *requestInputSanitizer) sanitizeMessageList(envelope map[string]json.RawMessage, key string, changed bool) ([]compression.SanitizedMessageRef, bool, error) {
	raw, ok := envelope[key]
	if !ok {
		return nil, changed, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, false, fmt.Errorf("%w: %s: %v", errInvalidSanitizeInput, key, err)
	}
	refs := make([]compression.SanitizedMessageRef, 0, len(messages))
	for i, original := range messages {
		before := len(s.mapping)
		updated, didChange, err := s.sanitizeMessage(original)
		if err != nil {
			return nil, false, err
		}
		ref := messageRef(i, original, updated, didChange, len(s.mapping)-before)
		refs = append(refs, ref)
		if didChange {
			messages[i], changed = updated, true
		}
	}
	if changed {
		updated, err := json.Marshal(messages)
		if err != nil {
			return nil, false, err
		}
		envelope[key] = updated
	}
	return refs, changed, nil
}

func messageRef(index int, original, updated json.RawMessage, changed bool, placeholderCount int) compression.SanitizedMessageRef {
	if !changed {
		updated = original
	}
	return compression.SanitizedMessageRef{
		RawIndex: index, SanitizedIndex: index,
		RawHash:       fingerprintRawMessage(original),
		SanitizedHash: fingerprintRawMessage(updated),
		Changed:       changed, PlaceholderCount: placeholderCount,
	}
}

func fingerprintRawMessage(raw json.RawMessage) string {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		return compression.MessageFingerprint(compact.Bytes())
	}
	return compression.MessageFingerprint(raw)
}

func (s *requestInputSanitizer) sanitizeMessage(raw json.RawMessage) (json.RawMessage, bool, error) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return nil, false, fmt.Errorf("%w: message object required: %v", errInvalidSanitizeInput, err)
	}
	var role string
	_ = json.Unmarshal(message["role"], &role)
	// Preserve historical chat semantics for assistant/tool turns. Anthropic
	// tool_result blocks inside a user turn and Responses function_call_output
	// items are handled by their protocol-specific paths.
	if role != "user" && role != "system" && role != "developer" {
		return raw, false, nil
	}
	content, ok := message["content"]
	if !ok {
		return raw, false, nil
	}
	updated, changed, err := s.sanitizeContent(content)
	if err != nil || !changed {
		return raw, false, err
	}
	message["content"] = updated
	out, err := json.Marshal(message)
	return out, true, err
}

func (s *requestInputSanitizer) sanitizeResponsesInput(raw json.RawMessage) (json.RawMessage, []compression.SanitizedMessageRef, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, nil, false, nil
	}
	if trimmed[0] == '"' {
		before := len(s.mapping)
		updated, changed, err := s.sanitizeText(raw)
		return updated, []compression.SanitizedMessageRef{messageRef(0, raw, updated, changed, len(s.mapping)-before)}, changed, err
	}
	if trimmed[0] == '{' {
		before := len(s.mapping)
		updated, changed, err := s.sanitizeResponsesItem(raw)
		return updated, []compression.SanitizedMessageRef{messageRef(0, raw, updated, changed, len(s.mapping)-before)}, changed, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false, fmt.Errorf("%w: input: %v", errInvalidSanitizeInput, err)
	}
	refs := make([]compression.SanitizedMessageRef, 0, len(items))
	changed := false
	for i, item := range items {
		before := len(s.mapping)
		updated, didChange, err := s.sanitizeResponsesItem(item)
		if err != nil {
			return nil, nil, false, err
		}
		refs = append(refs, messageRef(i, item, updated, didChange, len(s.mapping)-before))
		if didChange {
			items[i], changed = updated, true
		}
	}
	if !changed {
		return raw, refs, false, nil
	}
	updated, err := json.Marshal(items)
	return updated, refs, true, err
}

func (s *requestInputSanitizer) sanitizeResponsesItem(raw json.RawMessage) (json.RawMessage, bool, error) {
	var item map[string]json.RawMessage
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return nil, false, fmt.Errorf("%w: input item object required: %v", errInvalidSanitizeInput, err)
	}
	var kind, role string
	_ = json.Unmarshal(item["type"], &kind)
	_ = json.Unmarshal(item["role"], &role)
	field := ""
	switch kind {
	case "function_call_output":
		field = "output"
	case "input_text", "text":
		field = "text"
	case "", "message":
		if role == "assistant" {
			return raw, false, nil
		}
		field = "content"
	default:
		return raw, false, nil
	}
	value, ok := item[field]
	if !ok {
		return raw, false, nil
	}
	var updated json.RawMessage
	var changed bool
	var err error
	if field == "text" {
		updated, changed, err = s.sanitizeText(value)
	} else {
		updated, changed, err = s.sanitizeContent(value)
	}
	if err != nil || !changed {
		return raw, false, err
	}
	item[field] = updated
	out, err := json.Marshal(item)
	return out, true, err
}

func (s *requestInputSanitizer) sanitizeContent(raw json.RawMessage) (json.RawMessage, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, false, nil
	}
	switch trimmed[0] {
	case '"':
		return s.sanitizeText(raw)
	case '{':
		return s.sanitizeContentBlock(raw)
	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return nil, false, fmt.Errorf("%w: content blocks: %v", errInvalidSanitizeInput, err)
		}
		changed := false
		for i, block := range blocks {
			var updated json.RawMessage
			var didChange bool
			var err error
			if trimmedBlock := bytes.TrimSpace(block); len(trimmedBlock) > 0 && trimmedBlock[0] == '"' {
				updated, didChange, err = s.sanitizeText(block)
			} else {
				updated, didChange, err = s.sanitizeContentBlock(block)
			}
			if err != nil {
				return nil, false, err
			}
			if didChange {
				blocks[i], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		updated, err := json.Marshal(blocks)
		return updated, true, err
	default:
		return nil, false, fmt.Errorf("%w: content must be text or content blocks", errInvalidSanitizeInput)
	}
}

func (s *requestInputSanitizer) sanitizeContentBlock(raw json.RawMessage) (json.RawMessage, bool, error) {
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil || block == nil {
		return nil, false, fmt.Errorf("%w: content block object required: %v", errInvalidSanitizeInput, err)
	}
	var kind string
	_ = json.Unmarshal(block["type"], &kind)
	field := ""
	switch kind {
	case "text", "input_text", "output_text", "":
		field = "text"
	case "tool_result":
		field = "content"
	default:
		// In particular, do not inspect image/audio/document source.data.
		return raw, false, nil
	}
	value, ok := block[field]
	if !ok {
		return raw, false, nil
	}
	var updated json.RawMessage
	var changed bool
	var err error
	if field == "text" {
		updated, changed, err = s.sanitizeText(value)
	} else {
		updated, changed, err = s.sanitizeContent(value)
	}
	if err != nil || !changed {
		return raw, false, err
	}
	block[field] = updated
	out, err := json.Marshal(block)
	return out, true, err
}

func (s *requestInputSanitizer) sanitizeText(raw json.RawMessage) (json.RawMessage, bool, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, fmt.Errorf("%w: text field must be a string: %v", errInvalidSanitizeInput, err)
	}
	result, err := s.sanitizer.sanitizeInput(s.ctx, value, s.offset)
	if err != nil {
		return nil, false, err
	}
	if len(result.SanitizeMap) == 0 {
		return raw, false, nil
	}
	for placeholder, original := range result.SanitizeMap {
		s.mapping[placeholder] = original
		if p, ok := ParsePlaceholder(placeholder); ok {
			if p.Index > s.offset[p.Type] {
				s.offset[p.Type] = p.Index
			}
			if p.Index > s.usedCount[string(p.Type)] {
				s.usedCount[string(p.Type)] = p.Index
			}
		}
	}
	updated, err := json.Marshal(result.SanitizedText)
	return updated, true, err
}

func writeSanitizeFailure(w http.ResponseWriter, r *http.Request, status int) {
	message := "Request sanitization unavailable"
	kind := "server_error"
	if status == http.StatusBadRequest {
		message, kind = "Invalid request body", "invalid_request"
	} else if status == http.StatusRequestEntityTooLarge {
		message, kind = "Request body too large", "invalid_request"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if r != nil && r.URL != nil && r.URL.Path == "/v1/messages" {
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": kind, "message": message, "code": "sanitization_failed"}})
}
