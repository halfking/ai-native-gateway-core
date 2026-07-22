package executors

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func trimOneMessageFromBody(body []byte) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if raw, ok := req["messages"]; !ok || json.Unmarshal(raw, &messages) != nil {
		return body, nil
	}

	if len(messages) < 2 {
		return body, nil
	}
	last := len(messages) - 1
	if messages[last].Role == "user" {
		messages = messages[:last]
		last--
	}
	if last >= 0 && messages[last].Role == "assistant" {
		messages = messages[:last]
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	req["messages"] = encoded
	return json.Marshal(req)
}

func writeCachedResponse(w http.ResponseWriter, entry *pending.Response) {
	if w == nil || entry == nil {
		return
	}
	if entry.ContentType == "text/event-stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fmt.Fprint(w, entry.Body)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}

	contentType := entry.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	fmt.Fprint(w, entry.Body)
}

func candidateFromEntry(entry *pending.Response) provider.Candidate {
	return provider.Candidate{
		ProviderID:   entry.ProviderID,
		CredentialID: entry.CredentialID,
	}
}
