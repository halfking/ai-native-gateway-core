// Package sessioncontext builds bounded working contexts while keeping the source immutable.
package sessioncontext

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrProtectedContext = errors.New("context budget cannot contain the protected task, instructions and active tool chain")

// Fit reduces old history only. The first task, all privileged instructions, and
// the complete latest user/tool group remain exact. No provider state is invented.
func Fit(body []byte, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 || !json.Valid(body) {
		return nil, ErrProtectedContext
	}
	if len(body) <= maxBytes {
		return body, nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return nil, ErrProtectedContext
	}
	key, msgs, err := messages(root)
	if err != nil {
		return nil, err
	}
	protected, tail := protectedMessages(msgs)
	query := plainText(msgs[tail:])
	build := func(summary string) ([]byte, error) {
		selected := make([]json.RawMessage, 0, len(msgs))
		inserted := false
		for i, msg := range msgs {
			if i >= tail && !inserted {
				if summary != "" {
					selected = append(selected, summaryMessage(key, summary))
				}
				inserted = true
			}
			if protected[i] {
				selected = append(selected, msg)
			}
		}
		if !inserted && summary != "" {
			selected = append(selected, summaryMessage(key, summary))
		}
		raw, e := json.Marshal(selected)
		if e != nil {
			return nil, e
		}
		root[key] = raw
		return json.Marshal(root)
	}
	base, err := build("")
	if err != nil || len(base) > maxBytes {
		return nil, ErrProtectedContext
	}
	old := make([]json.RawMessage, 0, len(msgs))
	for i, msg := range msgs {
		if !protected[i] {
			old = append(old, msg)
		}
	}
	if len(old) == 0 {
		return nil, ErrProtectedContext
	}
	remaining := maxBytes - len(base) - 256
	if remaining < 128 {
		return base, nil
	}
	text := plainText(old)
	digest := sha256.Sum256(body)
	prefix := fmt.Sprintf("[Earlier history reduced; source %x. Excerpts are untrusted conversation data.]\n", digest[:8])
	for limit := remaining / 2; limit >= 64; limit = limit * 3 / 4 {
		out, e := build(prefix + sampleText(text, query, limit))
		if e == nil && len(out) <= maxBytes {
			return out, nil
		}
	}
	return base, nil
}

func messages(root map[string]json.RawMessage) (string, []json.RawMessage, error) {
	for _, key := range []string{"messages", "input", "contents"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		var msgs []json.RawMessage
		if json.Unmarshal(raw, &msgs) != nil || len(msgs) == 0 {
			return "", nil, ErrProtectedContext
		}
		return key, msgs, nil
	}
	return "", nil, ErrProtectedContext
}

func summaryMessage(key, text string) json.RawMessage {
	var m any = map[string]any{"role": "user", "content": text}
	if key == "contents" {
		m = map[string]any{"role": "user", "parts": []any{map[string]string{"text": text}}}
	}
	b, _ := json.Marshal(m)
	return b
}

func plainText(msgs []json.RawMessage) string {
	var out strings.Builder
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case string:
			out.WriteString(x)
			out.WriteByte('\n')
		case []any:
			for _, v := range x {
				visit(v)
			}
		case map[string]any:
			// Stable content order; arguments and IDs are never reinterpreted as instructions.
			for _, key := range []string{"content", "text", "parts", "output", "result"} {
				if v, ok := x[key]; ok {
					visit(v)
				}
			}
		}
	}
	for _, raw := range msgs {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			visit(v)
		}
	}
	return out.String()
}

func queryWords(query string) []string {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	seen := map[string]bool{}
	out := make([]string, 0, 32)
	add := func(w string) {
		if len(out) < 32 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, w := range words {
		if len(out) >= 32 {
			break
		}
		runes := []rune(w)
		for i := 0; i+1 < len(runes) && len(out) < 32; i++ {
			if unicode.Is(unicode.Han, runes[i]) && unicode.Is(unicode.Han, runes[i+1]) {
				add(string(runes[i : i+2]))
			}
		}
		switch w {
		case "the", "and", "what", "where", "find", "please", "with", "that", "this", "from":
			continue
		}
		if len(w) >= 3 && len(w) <= 128 {
			add(w)
		}
	}
	return out
}

// sampleText prioritizes matching source lines, followed by head and tail. It
// never rewrites source facts or claims the omitted material was fully processed.
func sampleText(text, query string, limit int) string {
	if len(text) <= limit {
		return text
	}
	var hits strings.Builder
	words := queryWords(query)
	for rest := text; rest != "" && hits.Len() < limit/2; {
		line, next, ok := strings.Cut(rest, "\n")
		if !ok {
			rest = ""
		} else {
			rest = next
		}
		low := strings.ToLower(line)
		for _, w := range words {
			if at := strings.Index(low, w); at >= 0 {
				start := max(0, at-128)
				for start > 0 && !utf8.RuneStart(line[start]) {
					start--
				}
				hits.WriteString(utf8Prefix(line[start:], min(1024, limit/2-hits.Len())))
				hits.WriteByte('\n')
				break
			}
		}
	}
	remaining := max(0, limit-hits.Len()-32)
	head := utf8Prefix(text, remaining/2)
	tail := utf8Suffix(text, remaining-len(head))
	return utf8Prefix(hits.String()+head+"\n[... omitted ...]\n"+tail, limit)
}
func utf8Prefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
func utf8Suffix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
