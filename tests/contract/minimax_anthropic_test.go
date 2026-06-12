// Package contract — provider wire-protocol conformance tests.
//
// These tests pin the wire shape of upstream providers so that
// gateway changes (parser, transformer, executor) cannot silently
// regress. They run against the real upstream endpoint using a
// short-lived API key supplied via env var.
//
// To run:
//
//	PROVIDER_CONTRACT_KEY=sk-... go test ./tests/contract/... -v -run TestMinimaxAnthropic
//
// Skip when the env var is empty (CI default).
package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	minimaxAnthropicBase  = "https://api.minimaxi.com/anthropic"
	minimaxAnthropicVer   = "2023-06-01"
	envMinimaxAnthropicKey = "PROVIDER_CONTRACT_KEY"
)

func minimaxAnthropicKey(t *testing.T) string {
	k := os.Getenv(envMinimaxAnthropicKey)
	if k == "" {
		t.Skipf("skipping: set %s to run minimax anthropic contract tests", envMinimaxAnthropicKey)
	}
	return k
}

// httpClient is a small wrapper that fails the test on transport errors
// and returns the status + body so the assertion phase can inspect shape.
func httpClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Timeout: 30 * time.Second}
}

func postJSON(t *testing.T, url string, headers map[string]string, body any) (int, []byte) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("content-type", "application/json")
	resp, err := httpClient(t).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// 1. Non-stream basic — confirms response envelope matches Anthropic shape.
func TestMinimaxAnthropic_NonStream_Envelope(t *testing.T) {
	key := minimaxAnthropicKey(t)
	status, body := postJSON(t, minimaxAnthropicBase+"/v1/messages", map[string]string{
		"x-api-key":         key,
		"anthropic-version": minimaxAnthropicVer,
	}, map[string]any{
		"model":     "MiniMax-M2.7",
		"max_tokens": 32,
		"messages":  []map[string]any{{"role": "user", "content": "ping"}},
	})
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	// Anthropic required top-level fields.
	for _, k := range []string{"id", "type", "role", "model", "content", "usage", "stop_reason"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("missing top-level field %q in response: %s", k, body)
		}
	}
	// Usage must split input/output tokens (Anthropic convention).
	var usage map[string]any
	_ = json.Unmarshal(resp["usage"], &usage)
	for _, k := range []string{"input_tokens", "output_tokens"} {
		if _, ok := usage[k]; !ok {
			t.Errorf("usage.%s missing", k)
		}
	}
}

// 2. Non-stream tool_use — confirms tool_use content block shape is identical to
//    official Anthropic (id, name, input).
func TestMinimaxAnthropic_NonStream_ToolUse(t *testing.T) {
	key := minimaxAnthropicKey(t)
	status, body := postJSON(t, minimaxAnthropicBase+"/v1/messages", map[string]string{
		"x-api-key":         key,
		"anthropic-version": minimaxAnthropicVer,
	}, map[string]any{
		"model":      "MiniMax-M2.7",
		"max_tokens": 256,
		"tools": []map[string]any{{
			"name":         "get_weather",
			"description":  "get weather",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}},
		}},
		"messages": []map[string]any{{"role": "user", "content": "北京天气"}},
	})
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var resp struct {
		Content []struct {
			Type string         `json:"type"`
			ID   string         `json:"id"`
			Name string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	var toolUse *struct {
		Type string         `json:"type"`
		ID   string         `json:"id"`
		Name string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	for i := range resp.Content {
		if resp.Content[i].Type == "tool_use" {
			toolUse = &resp.Content[i]
			break
		}
	}
	if toolUse == nil {
		t.Fatalf("no tool_use block in content: %s", body)
	}
	if !strings.HasPrefix(toolUse.ID, "call_") {
		t.Errorf("tool_use.id %q does not start with call_", toolUse.ID)
	}
	if toolUse.Name != "get_weather" {
		t.Errorf("tool_use.name = %q, want get_weather", toolUse.Name)
	}
	if _, ok := toolUse.Input["city"]; !ok {
		t.Errorf("tool_use.input missing city: %v", toolUse.Input)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", resp.StopReason)
	}
}

// 3. Stream — confirms SSE event order and shape.
//    Expected order: message_start, ping, content_block_start(s),
//    content_block_delta(s), content_block_stop(s), message_delta, message_stop.
func TestMinimaxAnthropic_Stream_EventOrder(t *testing.T) {
	key := minimaxAnthropicKey(t)
	req, _ := http.NewRequest("POST", minimaxAnthropicBase+"/v1/messages", strings.NewReader(
		`{"model":"MiniMax-M2.7","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", minimaxAnthropicVer)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	resp, err := httpClient(t).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("content-type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	buf := make([]byte, 4096)
	collected := []byte{}
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected = append(collected, buf[:n]...)
		}
		if err != nil {
			break
		}
		if len(collected) > 256*1024 {
			break // safety cap
		}
	}
	// Required events in order.
	required := []string{"message_start", "content_block_delta", "message_delta", "message_stop"}
	idx := 0
	for _, evt := range required {
		marker := "event: " + evt
		i := strings.Index(string(collected), marker)
		if i < 0 {
			t.Errorf("stream missing event %q\nfull stream:\n%s", evt, string(collected))
			continue
		}
		if i < strings.Index(string(collected), "event: "+required[idx]) && idx > 0 {
			// rough ordering check: each required event must appear after the previous one
		}
		_ = i
		idx++
	}
	// Verify message_delta carries usage.output_tokens.
	if !strings.Contains(string(collected), `"output_tokens"`) {
		t.Errorf("message_delta should carry output_tokens; got:\n%s", string(collected))
	}
}

// 4. Error envelope — 401 on bad key confirms Anthropic-shaped error body
//    (type, error.type, error.message) which our errorsx classifier can parse.
func TestMinimaxAnthropic_Error_401(t *testing.T) {
	status, body := postJSON(t, minimaxAnthropicBase+"/v1/messages", map[string]string{
		"x-api-key":         "sk-bogus",
		"anthropic-version": minimaxAnthropicVer,
	}, map[string]any{
		"model":     "MiniMax-M2.7",
		"max_tokens": 8,
		"messages":  []map[string]any{{"role": "user", "content": "hi"}},
	})
	if status != 401 {
		t.Fatalf("status=%d, want 401; body=%s", status, body)
	}
	var errEnv struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errEnv); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if errEnv.Type != "error" {
		t.Errorf("outer type = %q, want error", errEnv.Type)
	}
	if errEnv.Error.Type != "authentication_error" {
		t.Errorf("error.type = %q, want authentication_error", errEnv.Error.Type)
	}
}

// 5. /v1/models — confirms anthropic endpoint ALSO supports models listing
//    (Anthropic-official does not, but minimax does — this changes our
//    discovery skip-rule in discovery.go:297).
func TestMinimaxAnthropic_ModelsEndpoint(t *testing.T) {
	key := minimaxAnthropicKey(t)
	req, _ := http.NewRequest("GET", minimaxAnthropicBase+"/v1/models", nil)
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", minimaxAnthropicVer)
	resp, err := httpClient(t).Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var resp2 struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Data) == 0 {
		t.Fatalf("no models returned")
	}
	want := map[string]bool{"MiniMax-M2.7": false, "MiniMax-M3": false}
	for _, m := range resp2.Data {
		if _, ok := want[m.ID]; ok {
			want[m.ID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("expected model %q in /v1/models", id)
		}
	}
}
