package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

type upstreamBytesAttempt struct {
	Path      string
	Body      []byte
	Rejection string
}
type upstreamBytesFixture struct {
	mu            sync.Mutex
	protocol      string
	rejectedField string
	attempts      []upstreamBytesAttempt
}

func (f *upstreamBytesFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	rejection := ""
	if err != nil {
		rejection = "body read failed"
	} else {
		rejection = upstreamBytesWireError(f.protocol, r.URL.Path, body)
	}
	if rejection == "" && f.rejectedField != "" {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(body, &fields)
		if _, exists := fields[f.rejectedField]; exists {
			rejection = fmt.Sprintf("Unknown parameter: '%s'.", f.rejectedField)
		}
	}
	f.mu.Lock()
	f.attempts = append(f.attempts, upstreamBytesAttempt{r.URL.Path, bytes.Clone(body), rejection})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if rejection != "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": rejection, "type": "invalid_request_error"}})
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	streamed := bytes.Equal(fields["stream"], []byte("true"))
	reply := map[string]string{"chat": upstreamBytesChatReply, "responses": upstreamBytesResponsesReply, "messages": upstreamBytesMessagesReply}[f.protocol]
	if streamed {
		w.Header().Set("Content-Type", "text/event-stream")
		reply = map[string]string{"chat": upstreamBytesChatSSE, "responses": upstreamBytesResponsesSSE, "messages": upstreamBytesMessagesSSE}[f.protocol]
	}
	_, _ = io.WriteString(w, reply)
}
func (f *upstreamBytesFixture) captured() []upstreamBytesAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upstreamBytesAttempt(nil), f.attempts...)
}

// Decode only the top-level envelope. Nested names such as "messages" in tool
// schemas are never mistaken for wrong-dialect request parameters.
func upstreamBytesWireError(protocol, path string, body []byte) string {
	if path != upstreamBytesPath(protocol) {
		return fmt.Sprintf("unexpected path %s for %s", path, protocol)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return "invalid JSON"
	}
	required := map[string]string{"chat": "messages", "responses": "input", "messages": "messages"}[protocol]
	if len(fields[required]) == 0 {
		return "missing " + required
	}
	forbidden := map[string][]string{
		"chat":      {"input", "instructions", "max_output_tokens"},
		"responses": {"messages", "system", "max_tokens", "max_completion_tokens", "stream_options", "reasoning_effort", "response_format", "logprobs", "top_logprobs"},
		"messages":  {"input", "instructions", "stream_options", "max_completion_tokens", "response_format"},
	}
	for _, key := range forbidden[protocol] {
		if _, ok := fields[key]; ok {
			return "Unknown parameter: " + key
		}
	}
	return ""
}

func upstreamBytesAttemptsDescription(attempts []upstreamBytesAttempt) string {
	var out bytes.Buffer
	for i, a := range attempts {
		fmt.Fprintf(&out, "[%d path=%s rejection=%s body=%s]", i+1, a.Path, a.Rejection, a.Body)
	}
	return out.String()
}
