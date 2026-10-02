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
	ctx          context.Context
	sanitizer    *Sanitizer
	offset       map[SensitiveType]int
	mapping      SanitizeMap
	usedCount    map[string]int
	existing     sanitizeReuseIndex
	replacements int
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
		return nil, refs, nil
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
		before := s.replacements
		updated, changed, err := s.sanitizeText(raw)
		return updated, []compression.SanitizedMessageRef{messageRef(0, raw, updated, changed, s.replacements-before)}, changed, err
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
		before := s.replacements
		updated := prompt
		didChange := false
		if item := bytes.TrimSpace(prompt); len(item) > 0 && item[0] == '"' {
			var err error
			updated, didChange, err = s.sanitizeText(prompt)
			if err != nil {
				return nil, nil, false, err
			}
		}
		refs = append(refs, messageRef(i, prompt, updated, didChange, s.replacements-before))
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
		before := s.replacements
		updated, didChange, err := s.sanitizeMessage(original)
		if err != nil {
			return nil, false, err
		}
		ref := messageRef(i, original, updated, didChange, s.replacements-before)
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
	if role != "user" && role != "system" && role != "developer" && role != "assistant" && role != "tool" && role != "function" {
		return raw, false, nil
	}
	changed := false
	for _, field := range []string{"content", "reasoning_content"} {
		if content, ok := message[field]; ok {
			updated, didChange, err := s.sanitizeContent(content)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				message[field], changed = updated, true
			}
		}
	}
	for _, field := range []string{"tool_calls", "function_call"} {
		if value, ok := message[field]; ok {
			updated, didChange, err := s.sanitizeToolCalls(value)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				message[field], changed = updated, true
			}
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(message)
	return out, true, err
}

func (s *requestInputSanitizer) sanitizeResponsesInput(raw json.RawMessage) (json.RawMessage, []compression.SanitizedMessageRef, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, nil, false, nil
	}
	if trimmed[0] == '"' {
		before := s.replacements
		updated, changed, err := s.sanitizeText(raw)
		return updated, []compression.SanitizedMessageRef{messageRef(0, raw, updated, changed, s.replacements-before)}, changed, err
	}
	if trimmed[0] == '{' {
		before := s.replacements
		updated, changed, err := s.sanitizeResponsesItem(raw)
		return updated, []compression.SanitizedMessageRef{messageRef(0, raw, updated, changed, s.replacements-before)}, changed, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false, fmt.Errorf("%w: input: %v", errInvalidSanitizeInput, err)
	}
	refs := make([]compression.SanitizedMessageRef, 0, len(items))
	changed := false
	for i, item := range items {
		before := s.replacements
		updated, didChange, err := s.sanitizeResponsesItem(item)
		if err != nil {
			return nil, nil, false, err
		}
		refs = append(refs, messageRef(i, item, updated, didChange, s.replacements-before))
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
	var kind string
	_ = json.Unmarshal(item["type"], &kind)
	field := ""
	switch kind {
	case "function_call_output":
		field = "output"
	case "input_text", "text":
		field = "text"
	case "function_call":
		field = "arguments"
	case "custom_tool_call":
		field = "input"
	case "custom_tool_call_output":
		// Sibling of function_call_output: a custom tool's plaintext return
		// value the client replays on the next turn (24h 审计第二十八轮 A1,
		// 2026-10-02).
		field = "output"
	case "", "message":
		field = "content"
	case "reasoning":
		// summary[].text is plaintext the client echoes back next to the
		// opaque encrypted_content; passing it through unsanitized was a
		// verified leak (12h 审计第二十七轮 N1, 2026-10-02). encrypted_content
		// is provider ciphertext and is not inspected.
		return s.sanitizeReasoningItem(raw, item)
	case "mcp_call":
		// arguments (JSON tool payload) + output (plaintext tool result) both
		// carry client-echoed text; the rest is provider metadata.
		return s.sanitizeMcpCallItem(raw, item)
	case "web_search_call":
		// action.query is the replayed search text.
		return s.sanitizeWebSearchCallItem(raw, item)
	case "file_search_call":
		// queries[] and results[].text are replayed plaintext.
		return s.sanitizeFileSearchCallItem(raw, item)
	case "local_shell_call_output":
		// Codex family: shell stdout/stderr replayed as a JSON string
		// (12h 审计第二十九轮, 2026-10-02).
		field = "output"
	case "apply_patch_call_output":
		// Codex family: patch application log replayed as plaintext.
		field = "output"
	case "shell_call_output":
		// Codex family: outputs[] are {text} containers without a type field,
		// so the typed-block walk in sanitizeContent would skip them.
		return s.sanitizeShellCallOutputItem(raw, item)
	case "code_interpreter_call":
		// code + outputs[].logs are model/tool plaintext; container_id and
		// image outputs are metadata.
		return s.sanitizeCodeInterpreterCallItem(raw, item)
	case "apply_patch_call":
		// action.content (create/update patch payload) is plaintext;
		// action.type / action.path are metadata.
		return s.sanitizeApplyPatchCallItem(raw, item)
	default:
		// Unknown item types still pass through (allowlist design); each new
		// plaintext-bearing type must be added here. The families above are
		// the known output/code bearers but the list is not exhaustive
		// (tool_search_call.arguments etc. remain future work);
		// item_reference (id-only) is harmless.
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
	} else if field == "arguments" || field == "input" {
		return s.sanitizeToolBlock(raw, item, field, value)
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

// reasoning item 携带 summary（summary_text 块数组，明文）与可选 content；
// 两者都走 sanitizeContent 的块机制清洗，encrypted_content 不动。
func (s *requestInputSanitizer) sanitizeReasoningItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	changed := false
	for _, field := range []string{"summary", "content"} {
		value, ok := item[field]
		if !ok {
			continue
		}
		updated, didChange, err := s.sanitizeContent(value)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			item[field] = updated
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(item)
	return out, changed, err
}

// mcp_call 携带 arguments（JSON 字符串或结构化工具入参）与 output（工具返回
// 明文），都是客户端回传的正文；approval_request_id / server_label 是元数据不动。
func (s *requestInputSanitizer) sanitizeMcpCallItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	changed := false
	if value, ok := item["arguments"]; ok {
		updated, didChange, err := s.sanitizeToolValue(value, 0)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			item["arguments"], changed = updated, true
		}
	}
	if value, ok := item["output"]; ok {
		updated, didChange, err := s.sanitizeContent(value)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			item["output"], changed = updated, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(item)
	return out, changed, err
}

// web_search_call 的 action.query 是客户端回传的检索明文；action 其余字段与
// item 级 id/status 是元数据不动。
func (s *requestInputSanitizer) sanitizeWebSearchCallItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	actionRaw, ok := item["action"]
	if !ok {
		return raw, false, nil
	}
	var action map[string]json.RawMessage
	if err := json.Unmarshal(actionRaw, &action); err != nil || action == nil {
		// 显式 "action": null 与字段缺失同义直通（错误拒单与缺失直通不一致，
		// R29 审计）；只有非对象形态才拒。
		if actionRaw != nil && string(bytes.TrimSpace(actionRaw)) == "null" {
			return raw, false, nil
		}
		return nil, false, fmt.Errorf("%w: web_search_call action object required: %v", errInvalidSanitizeInput, err)
	}
	query, ok := action["query"]
	if !ok {
		return raw, false, nil
	}
	updated, didChange, err := s.sanitizeText(query)
	if err != nil {
		return nil, false, err
	}
	if !didChange {
		return raw, false, nil
	}
	action["query"] = updated
	actionOut, err := json.Marshal(action)
	if err != nil {
		return nil, false, err
	}
	item["action"] = actionOut
	out, err := json.Marshal(item)
	return out, true, err
}

// file_search_call 回传检索参数与命中结果：queries[]（字符串数组）与
// results[].text（明文）入洗；file_id / attributes / score 等元数据不动。
func (s *requestInputSanitizer) sanitizeFileSearchCallItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	changed := false
	if value, ok := item["queries"]; ok {
		var queries []json.RawMessage
		if err := json.Unmarshal(value, &queries); err != nil {
			return nil, false, fmt.Errorf("%w: file_search_call queries array: %v", errInvalidSanitizeInput, err)
		}
		qChanged := false
		for i, q := range queries {
			updated, didChange, err := s.sanitizeText(q)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				queries[i], qChanged = updated, true
			}
		}
		if qChanged {
			out, err := json.Marshal(queries)
			if err != nil {
				return nil, false, err
			}
			item["queries"], changed = out, true
		}
	}
	if value, ok := item["results"]; ok {
		var results []map[string]json.RawMessage
		if err := json.Unmarshal(value, &results); err != nil {
			return nil, false, fmt.Errorf("%w: file_search_call results array: %v", errInvalidSanitizeInput, err)
		}
		rChanged := false
		for _, result := range results {
			text, ok := result["text"]
			if !ok {
				continue
			}
			updated, didChange, err := s.sanitizeText(text)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				result["text"], rChanged = updated, true
			}
		}
		if rChanged {
			out, err := json.Marshal(results)
			if err != nil {
				return nil, false, err
			}
			item["results"], changed = out, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(item)
	return out, changed, err
}

// shell_call_output 回传 shell 执行产物：outputs[] 是无 type 字段的 {text}
// 容器（sanitizeContent 的 typed-block 行走会跳过），逐元素清洗 text。
func (s *requestInputSanitizer) sanitizeShellCallOutputItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	value, ok := item["outputs"]
	if !ok {
		return raw, false, nil
	}
	var outputs []map[string]json.RawMessage
	if err := json.Unmarshal(value, &outputs); err != nil {
		return nil, false, fmt.Errorf("%w: shell_call_output outputs array: %v", errInvalidSanitizeInput, err)
	}
	changed := false
	for _, output := range outputs {
		text, ok := output["text"]
		if !ok {
			continue
		}
		updated, didChange, err := s.sanitizeText(text)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			output["text"], changed = updated, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	outArr, err := json.Marshal(outputs)
	if err != nil {
		return nil, false, err
	}
	item["outputs"] = outArr
	out, err := json.Marshal(item)
	return out, true, err
}

// code_interpreter_call 回传模型代码与执行输出：code（明文）与 outputs[].logs
//（stdout/stderr 明文）入洗；container_id 与 image 类输出不动。
func (s *requestInputSanitizer) sanitizeCodeInterpreterCallItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	changed := false
	if value, ok := item["code"]; ok {
		updated, didChange, err := s.sanitizeText(value)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			item["code"], changed = updated, true
		}
	}
	if value, ok := item["outputs"]; ok {
		var outputs []map[string]json.RawMessage
		if err := json.Unmarshal(value, &outputs); err != nil {
			return nil, false, fmt.Errorf("%w: code_interpreter_call outputs array: %v", errInvalidSanitizeInput, err)
		}
		oChanged := false
		for _, output := range outputs {
			logs, ok := output["logs"]
			if !ok {
				continue
			}
			updated, didChange, err := s.sanitizeText(logs)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				output["logs"] = updated
				oChanged = true
			}
		}
		if oChanged {
			outArr, err := json.Marshal(outputs)
			if err != nil {
				return nil, false, err
			}
			item["outputs"], changed = outArr, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(item)
	return out, changed, err
}

// apply_patch_call 回传补丁动作：action.content（create/update 的文件内容）
// 是明文；action.type / action.path 与 call_id 是元数据不动。
func (s *requestInputSanitizer) sanitizeApplyPatchCallItem(raw json.RawMessage, item map[string]json.RawMessage) (json.RawMessage, bool, error) {
	actionRaw, ok := item["action"]
	if !ok {
		return raw, false, nil
	}
	var action map[string]json.RawMessage
	if err := json.Unmarshal(actionRaw, &action); err != nil || action == nil {
		return nil, false, fmt.Errorf("%w: apply_patch_call action object required: %v", errInvalidSanitizeInput, err)
	}
	content, ok := action["content"]
	if !ok {
		return raw, false, nil
	}
	updated, didChange, err := s.sanitizeText(content)
	if err != nil {
		return nil, false, err
	}
	if !didChange {
		return raw, false, nil
	}
	action["content"] = updated
	actionOut, err := json.Marshal(action)
	if err != nil {
		return nil, false, err
	}
	item["action"] = actionOut
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
	case "text", "input_text", "output_text", "reasoning_text", "summary_text", "":
		field = "text"
	case "refusal":
		// Replayed assistant refusal plaintext lives in the refusal field;
		// restore side mirrors both refusal and text fields.
		field = "refusal"
	case "tool_result":
		field = "content"
	case "tool_use":
		field = "input"
	case "thinking":
		field = "thinking"
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
		if field == "input" {
			return s.sanitizeToolBlock(raw, block, field, value)
		}
		updated, changed, err = s.sanitizeContent(value)
	}
	if err != nil || !changed {
		return raw, false, err
	}
	if kind == "thinking" {
		var signature string
		_ = json.Unmarshal(block["signature"], &signature)
		if signature != "" {
			return nil, false, fmt.Errorf("%w: sensitive signed thinking", errInvalidSanitizeInput)
		}
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
	result, err := s.sanitizer.sanitizeInputStable(s.ctx, value, s.offset, s.existing)
	if err != nil {
		return nil, false, err
	}
	if len(result.SanitizeMap) == 0 {
		return raw, false, nil
	}
	s.replacements += len(result.Fragments)
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
