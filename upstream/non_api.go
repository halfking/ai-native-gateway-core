package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// nonAPIReply catches relay sign-in/challenge pages before any response bytes
// commit. Keep the sniff bounded and replay every byte on the success path;
// audio, XML and compressed replies are also carried by this shared client.
func nonAPIReply(resp *http.Response, req *http.Request) *Error {
	if resp == nil || resp.Body == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 ||
		resp.StatusCode == http.StatusNoContent || req.Method == http.MethodHead {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	// The stream reader owns first-token/idle deadlines and empty-SSE recovery.
	// Do not wait for SSE bytes here before that reader has armed its timers.
	if streamingReplyType(ct) {
		return nil
	}
	if !strings.HasPrefix(ct, "text/html") &&
		(streamingReplyType(strings.ToLower(req.Header.Get("Accept"))) ||
			strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "image/") ||
			strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "application/octet-stream")) {
		return nil
	}
	reason := ""
	if strings.HasPrefix(ct, "text/html") {
		reason = "HTML page"
	} else if ce := resp.Header.Get("Content-Encoding"); ce == "" || strings.EqualFold(ce, "identity") {
		original := resp.Body
		br := bufio.NewReaderSize(original, maxErrorBodyBytes)
		resp.Body = &bufferedReplyBody{Reader: br, Closer: original}
		if head, err := peekReplyPrefix(br); err == io.EOF && len(head) == 0 {
			reason = "empty body"
		} else if err == nil {
			switch {
			case !strings.Contains(ct, "xml") && peekHTMLReply(br, head):
				reason = "HTML page"
			case strings.Contains(ct, "json") && len(head) > 0 && head[0] != '{' && head[0] != '[':
				reason = "invalid JSON prefix"
			}
		}
	}
	if reason == "" {
		return nil
	}
	// Yield immediately to the executor's candidate recovery, avoiding the
	// transport's repeated calls to the same broken relay. Never echo page bytes.
	_ = resp.Body.Close()
	message := "upstream returned " + reason + ", not an API reply"
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "bad_gateway", "message": message}})
	resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
	resp.Header = resp.Header.Clone()
	for _, key := range []string{"Content-Encoding", "Content-Length", "Transfer-Encoding"} {
		resp.Header.Del(key)
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return &Error{Kind: KindUpstreamDown, StatusCode: http.StatusBadGateway, Message: message, Body: body}
}

func peekReplyPrefix(br *bufio.Reader) ([]byte, error) {
	for {
		_, err := br.Peek(br.Buffered() + 1)
		head, _ := br.Peek(br.Buffered())
		head = bytes.TrimLeft(head, " \t\r\n\ufeff")
		if len(head) > 0 || err != nil || br.Buffered() >= maxErrorBodyBytes {
			return head, err
		}
	}
}

// A relay can split even the opening tag across reads. Only extend a prefix
// that could still become an HTML tag; JSON/SSE/binary payloads never wait for
// a full sniff buffer. All inspected bytes remain in br for the real reader.
func peekHTMLReply(br *bufio.Reader, head []byte) bool {
	for {
		lower := strings.ToLower(string(head))
		if htmlReplyPrefix(lower) {
			return true
		}
		possible := false
		for _, tag := range []string{"<html", "<head", "<body", "<!doctype html"} {
			possible = possible || (len(lower) > 0 && strings.HasPrefix(tag, lower))
		}
		if !possible || len(head) >= 32 || br.Buffered() >= maxErrorBodyBytes {
			return false
		}
		if _, err := br.Peek(br.Buffered() + 1); err != nil {
			return false
		}
		head, _ = br.Peek(br.Buffered())
		head = bytes.TrimLeft(head, " \t\r\n\ufeff")
	}
}

func streamingReplyType(value string) bool {
	return strings.Contains(value, "text/event-stream") || strings.Contains(value, "application/x-ndjson") ||
		strings.Contains(value, "application/ndjson")
}

func htmlReplyPrefix(head string) bool {
	for _, tag := range []string{"<html", "<head", "<body", "<!doctype html"} {
		if strings.HasPrefix(head, tag) && len(head) > len(tag) {
			switch head[len(tag)] {
			case ' ', '\t', '\r', '\n', '>':
				return true
			}
		}
	}
	return false
}

type bufferedReplyBody struct {
	io.Reader
	io.Closer
}
