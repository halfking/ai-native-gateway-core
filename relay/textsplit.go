package relay

import "strings"

// splitLeadingThinkBlock extracts a leading `<think>...</think>` segment
// from s, returning the inner thinking text, the remainder of s with the
// segment stripped (leading newlines trimmed), and ok=true if the
// prefix was found. Otherwise it returns s unchanged and ok=false.
//
// Lives in the relay package so both the non-stream converter
// (relay/messages.go) and the SSE stream forwarder
// (relay/anthropic_passthrough_stream.go) can share one implementation.
// The routing package uses its own copy because routing already imports
// relay and an inverted import would form a cycle.
func splitLeadingThinkBlock(s string) (think, rest string, ok bool) {
	const openTag = "<think>"
	const closeTag = "</think>"
	if !strings.HasPrefix(s, openTag) {
		return "", s, false
	}
	end := strings.Index(s, closeTag)
	if end < 0 {
		return "", s, false
	}
	think = s[len(openTag):end]
	rest = strings.TrimLeft(s[end+len(closeTag):], "\n")
	return think, rest, true
}