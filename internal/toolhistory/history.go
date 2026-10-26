// Package toolhistory repairs interrupted tool exchanges without rebuilding valid requests.
package toolhistory

import (
	"encoding/json"
	"fmt"
)

// Options contains dialect decisions from the provider field registry.
// Only newly synthesized Messages results use this field; real results stay raw.
type Options struct{ ResultIDField string }

// Repair completes local tool-call batches in the actual outbound dialect.
// Complete exchanges are returned byte-for-byte, including unordered answers.
// Repaired arrays reuse raw calls/results; all other top-level bytes stay intact.
func Repair(body []byte, protocol string, options ...Options) ([]byte, error) {
	switch protocol {
	case "openai-chat", "openai-completions":
		protocol = "chat"
	case "openai-responses":
		protocol = "responses"
	case "anthropic-messages":
		protocol = "messages"
	}
	if protocol != "chat" && protocol != "responses" && protocol != "messages" {
		return body, nil
	}
	resultIDField := "tool_use_id"
	if len(options) > 0 && options[0].ResultIDField == "tool_call_id" {
		resultIDField = "tool_call_id"
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body, nil
	}
	key := "messages"
	if protocol == "responses" {
		key = "input"
	}
	if duplicatedHistoryWithCalls(body, key, protocol) {
		return body, fmt.Errorf("%w: repeated top-level %q", ErrAmbiguousHistory, key)
	}
	var raw []json.RawMessage
	if json.Unmarshal(root[key], &raw) != nil {
		return body, nil
	}
	entries := make([]wireEntry, len(raw))
	hasCalls, ambiguousContent := false, false
	for i, r := range raw {
		entries[i] = readEntry(r, protocol)
		hasCalls = hasCalls || len(entries[i].calls) > 0
		ambiguousContent = ambiguousContent || entries[i].duplicateContent
	}
	if hasCalls && ambiguousContent {
		return body, fmt.Errorf("%w: repeated Messages content", ErrAmbiguousHistory)
	}
	var out []json.RawMessage
	changed := false
	for i := 0; i < len(entries); {
		if len(entries[i].calls) == 0 {
			out = append(out, entries[i].raw)
			i++
			continue
		}
		endCalls := i + 1
		for endCalls < len(entries) && len(entries[endCalls].calls) > 0 {
			endCalls++
		}
		end := endCalls
		for end < len(entries) && entries[end].role != "assistant" && len(entries[end].calls) == 0 {
			end++
		}
		var calls []wireCall
		for _, e := range entries[i:endCalls] {
			calls = append(calls, e.calls...)
		}
		callIDs := make([]string, len(calls))
		known := make(map[string]bool, len(calls))
		for n, c := range calls {
			callIDs[n] = c.id
			known[c.id] = true
		}
		var answers []json.RawMessage
		var answerIDs []string
		var rest []json.RawMessage
		restSeen, interleaved := false, false
		for _, e := range entries[endCalls:end] {
			var remaining []json.RawMessage
			for _, part := range e.parts {
				if part.result && known[part.id] {
					if restSeen {
						interleaved = true
					}
					answers = append(answers, part.raw)
					answerIDs = append(answerIDs, part.id)
				} else {
					remaining = append(remaining, part.raw)
					restSeen = true
				}
			}
			if len(e.parts) == 0 {
				rest = append(rest, e.raw)
				restSeen = true
				continue
			}
			if len(remaining) > 0 {
				if protocol == "messages" {
					updated, err := replaceField(e.raw, "content", rawArray(remaining))
					if err != nil {
						return body, err
					}
					rest = append(rest, updated)
				} else {
					rest = append(rest, e.raw)
				}
			}
		}
		plan, err := Pair(callIDs, answerIDs)
		if err != nil {
			return body, err
		}
		missing := false
		for _, n := range plan {
			if n < 0 {
				missing = true
			}
		}
		// Several Chat/Messages assistant records with pending calls cannot sit
		// next to one another upstream. Keep each actual record and place its own
		// answers immediately after it, rather than discarding/merging metadata.
		split := protocol != "responses" && endCalls-i > 1
		if !missing && !interleaved && !split {
			out = append(out, raw[i:end]...)
			i = end
			continue
		}
		changed = true
		var ordered []json.RawMessage
		for n, c := range calls {
			if plan[n] >= 0 {
				ordered = append(ordered, answers[plan[n]])
			} else {
				ordered = append(ordered, synthetic(c, protocol, resultIDField))
			}
		}
		if split {
			offset := 0
			for _, e := range entries[i:endCalls] {
				out = append(out, e.raw)
				out = appendAnswers(out, ordered[offset:offset+len(e.calls)], protocol)
				offset += len(e.calls)
			}
		} else {
			out = append(out, raw[i:endCalls]...)
			out = appendAnswers(out, ordered, protocol)
		}
		out = append(out, rest...)
		i = end
	}
	if !changed {
		return body, nil
	}
	fixed, err := replaceField(body, key, rawArray(out))
	if err != nil {
		return body, err
	}
	return fixed, nil
}
