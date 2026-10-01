package sanitize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Reserve every literal marker before processing any field. This also handles
// JSON unicode escapes and nested tool JSON strings, so allocation order cannot
// make a client-supplied marker alias a newly allocated credential.
func reserveInputPlaceholders(body []byte, offsets map[SensitiveType]int) error {
	return reserveInputPlaceholdersDepth(body, offsets, 0)
}

func reserveInputPlaceholdersDepth(body []byte, offsets map[SensitiveType]int, depth int) error {
	if depth > maxNativeToolInputDepth {
		return fmt.Errorf("%w: embedded JSON depth", errInvalidSanitizeInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: placeholder scan", errInvalidSanitizeInput)
		}
		if text, ok := token.(string); ok {
			document := bytes.TrimSpace([]byte(text))
			if len(document) > 0 && (document[0] == '{' || document[0] == '[') && json.Valid(document) {
				if err := reserveInputPlaceholdersDepth(document, offsets, depth+1); err != nil {
					return err
				}
			}
			for _, marker := range PlaceholderPattern.FindAllString(text, -1) {
				if p, ok := ParsePlaceholder(marker); ok && p.Index > offsets[p.Type] {
					offsets[p.Type] = p.Index
				}
			}
		}
	}
}

func sortedRawKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func credentialField(key string) bool {
	switch strings.ToLower(key) {
	case "password", "passwd", "pwd", "secret", "token", "api_key", "apikey", "access_key", "authorization", "account", "username", "user_name", "login", "帐号", "账号", "账户", "用户名", "密码", "口令":
		return true
	}
	return false
}

// Only call arguments/input are rewritten; names, IDs and schemas are opaque.
func (s *requestInputSanitizer) sanitizeToolCalls(raw json.RawMessage) (json.RawMessage, bool, error) {
	return s.sanitizeToolCallsDepth(raw, 0)
}

func (s *requestInputSanitizer) sanitizeToolCallsDepth(raw json.RawMessage, depth int) (json.RawMessage, bool, error) {
	if depth > maxNativeToolInputDepth {
		return nil, false, fmt.Errorf("%w: tool call depth", errInvalidSanitizeInput)
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var calls []json.RawMessage
		if err := json.Unmarshal(raw, &calls); err != nil {
			return nil, false, fmt.Errorf("%w: tool calls", errInvalidSanitizeInput)
		}
		changed := false
		for i, call := range calls {
			updated, didChange, err := s.sanitizeToolCallsDepth(call, depth+1)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				calls[i], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(calls)
		return out, true, err
	}
	var call map[string]json.RawMessage
	if err := json.Unmarshal(raw, &call); err != nil || call == nil {
		return nil, false, fmt.Errorf("%w: tool call", errInvalidSanitizeInput)
	}
	changed := false
	if fn, ok := call["function"]; ok {
		updated, didChange, err := s.sanitizeToolCallsDepth(fn, depth+1)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			call["function"], changed = updated, true
		}
	}
	if args, ok := call["arguments"]; ok {
		updated, didChange, err := s.sanitizeToolValue(args, 0)
		if err != nil {
			return nil, false, err
		}
		if didChange {
			call["arguments"], changed = updated, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(call)
	return out, true, err
}

func (s *requestInputSanitizer) sanitizeToolBlock(raw json.RawMessage, block map[string]json.RawMessage, field string, value json.RawMessage) (json.RawMessage, bool, error) {
	updated, changed, err := s.sanitizeToolValue(value, 0)
	if err != nil || !changed {
		return raw, false, err
	}
	block[field] = updated
	out, err := json.Marshal(block)
	return out, true, err
}

// Tool arguments can be structured JSON or a JSON document embedded in a string.
// Decoding and re-encoding string leaves preserves quotes, escapes and numbers.
func (s *requestInputSanitizer) sanitizeToolValue(raw json.RawMessage, depth int) (json.RawMessage, bool, error) {
	return s.sanitizeToolValueWithLabel(raw, depth, "")
}

func (s *requestInputSanitizer) sanitizeToolValueWithLabel(raw json.RawMessage, depth int, label string) (json.RawMessage, bool, error) {
	if depth > maxNativeToolInputDepth {
		return nil, false, fmt.Errorf("%w: tool input depth", errInvalidSanitizeInput)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, false, nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, false, err
		}
		_, marker := ParsePlaceholder(text)
		if credentialField(label) && text != "" && !marker {
			typ := TypeSecret
			switch strings.ToLower(label) {
			case "account", "username", "user_name", "login", "帐号", "账号", "账户", "用户名":
				typ = TypeAccount
			}
			token, err := allocateSensitivePlaceholder(typ, text, s.offset, s.existing)
			if err != nil {
				return nil, false, err
			}
			s.mapping[token] = text
			s.replacements++
			p, _ := ParsePlaceholder(token)
			if p.Index > s.usedCount[string(typ)] {
				s.usedCount[string(typ)] = p.Index
			}
			out, err := json.Marshal(token)
			return out, true, err
		}
		doc := bytes.TrimSpace([]byte(text))
		if len(doc) > 0 && (doc[0] == '{' || doc[0] == '[') && json.Valid(doc) {
			updated, changed, err := s.sanitizeToolValue(doc, depth+1)
			if err != nil || !changed {
				return raw, false, err
			}
			out, err := json.Marshal(string(updated))
			return out, true, err
		}
		return s.sanitizeText(raw)
	case '{':
		if credentialField(label) {
			return nil, false, fmt.Errorf("%w: credential object requires a string contract", errInvalidSanitizeInput)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, false, fmt.Errorf("%w: tool input JSON", errInvalidSanitizeInput)
		}
		changed := false
		// Map order must not affect placeholder allocation or cache fingerprints.
		for _, key := range sortedRawKeys(object) {
			value := object[key]
			updated, didChange, err := s.sanitizeToolValueWithLabel(value, depth+1, key)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				object[key], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(object)
		return out, true, err
	case '[':
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, false, fmt.Errorf("%w: tool input array", errInvalidSanitizeInput)
		}
		changed := false
		for i, value := range values {
			updated, didChange, err := s.sanitizeToolValueWithLabel(value, depth+1, label)
			if err != nil {
				return nil, false, err
			}
			if didChange {
				values[i], changed = updated, true
			}
		}
		if !changed {
			return raw, false, nil
		}
		out, err := json.Marshal(values)
		return out, true, err
	default:
		if credentialField(label) && !bytes.Equal(trimmed, []byte("null")) {
			return nil, false, fmt.Errorf("%w: credential requires string values", errInvalidSanitizeInput)
		}
		return raw, false, nil
	}
}
