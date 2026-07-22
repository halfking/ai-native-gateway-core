package executors

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func trimOneMessageFromBody(body []byte) ([]byte, error) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	msgs := req.Messages
	if len(msgs) < 2 {
		return body, nil
	}

	last := msgs[len(msgs)-1]
	secondLast := msgs[len(msgs)-2]

	if last.Role == "assistant" {
		msgs = msgs[:len(msgs)-1]
	}
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
		msgs = msgs[:len(msgs)-1]
	}

	if last.Role == "assistant" && secondLast.Role == "user" {
		req.Messages = msgs
	} else if last.Role == "assistant" {
		req.Messages = append(msgs, secondLast)
	} else {
		req.Messages = msgs
	}
	return json.Marshal(req)
}

func writeCachedResponse(w http.ResponseWriter, entry *pending.Response) {
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

	w.Header().Set("Content-Type", entry.ContentType)
	fmt.Fprint(w, entry.Body)
}

func candidateFromEntry(entry *pending.Response) provider.Candidate {
	return provider.Candidate{
		ProviderID:   entry.ProviderID,
		CredentialID: entry.CredentialID,
	}
}
