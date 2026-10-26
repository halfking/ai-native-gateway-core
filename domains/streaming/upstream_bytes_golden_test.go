package streaming

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestUpstreamBodiesUnchanged(t *testing.T) {
	for _, client := range []string{"chat", "responses", "messages"} {
		for _, upstream := range []string{"chat", "responses", "messages"} {
			for _, streamed := range []bool{false, true} {
				upstreamBytesCase(t, client, upstream, streamed, false)
			}
		}
		for _, streamed := range []bool{false, true} {
			upstreamBytesCase(t, client, "responses", streamed, true)
		}
	}
}
func upstreamBytesCase(t *testing.T, client, upstream string, streamed, strict bool) {
	name := fmt.Sprintf("%s_to_%s_%s", client, upstream, map[bool]string{false: "json", true: "stream"}[streamed])
	if strict {
		name += "_vapeur"
	}
	t.Run(name, func(t *testing.T) {
		fake := &upstreamBytesFixture{protocol: upstream}
		server := httptest.NewServer(fake)
		defer server.Close()
		protocol := map[string]string{"chat": "openai-completions", "responses": "openai-responses", "messages": "anthropic-messages"}[upstream]
		catalog := "openai"
		if upstream == "messages" {
			catalog = "anthropic"
		}
		if strict {
			catalog = "vapeur"
		}
		cand := provider.Candidate{ProviderID: 11, CredentialID: 22, BaseURL: server.URL, Protocol: protocol, CatalogCode: catalog, RawModel: "m", OfferRawModel: "m", APIKey: "test-key", Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true, LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed"}
		if upstream == "responses" {
			cand.SupportsNativeResponses = true
			cand.SupportsNativeResponsesKnown = true
			cand.SupportsNativeResponsesStream = true
		}
		input := map[string]string{"chat": upstreamBytesChat, "responses": upstreamBytesResponses, "messages": upstreamBytesMessages}[client]
		// Appending only stream preserves the fixture's integer and escape lexemes.
		// Captured upstream bytes are never rewritten by this test.
		input = strings.TrimSuffix(input, "}") + fmt.Sprintf(",\"stream\":%t}", streamed)
		rec := upstreamBytesRequest(t, cand, client, input)
		attempts := fake.captured()
		if rec.Code != 200 || len(attempts) != 1 {
			t.Fatalf("exchange status=%d attempts=%d capture=%s client=%s", rec.Code, len(attempts), upstreamBytesAttemptsDescription(attempts), rec.Body.String())
		}
		for _, attempt := range attempts {
			if attempt.Rejection != "" {
				t.Fatalf("upstream rejected %s: %s; body=%s", attempt.Path, attempt.Rejection, attempt.Body)
			}
			if client == "messages" && upstream == "messages" && !bytes.Contains(attempt.Body, []byte(`"cache_control":{"type":"ephemeral"}`)) {
				t.Fatalf("native system cache prefix lost: %s", attempt.Body)
			}
			if err := upstreamBytesSemantics(attempt.Body, upstream); err != nil {
				t.Fatalf("independent semantics: %v; body=%s", err, attempt.Body)
			}
		}
		if err := upstreamBytesClientTerminal(rec, client, streamed); err != nil {
			t.Fatal(err)
		}
		// Only a complete, semantically valid exchange can reach the update gate.
		for i, attempt := range attempts {
			path := filepath.Join("testdata", "upstream_bodies", fmt.Sprintf("%s.attempt-%02d.json", name, i+1))
			if err := upstreamBytesGolden(path, attempt.Body, os.Getenv("UPDATE_UPSTREAM_BODIES") == "1"); err != nil {
				t.Fatal(err)
			}
		}
	})
}
func upstreamBytesGolden(path string, got []byte, update bool) error {
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return os.WriteFile(path, got, 0644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("missing golden (use UPDATE_UPSTREAM_BODIES=1 only after semantics pass): %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("upstream bytes changed for %s\n got %s\nwant %s", path, got, want)
	}
	return nil
}
func upstreamBytesSemantics(body []byte, protocol string) error {
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		return err
	}
	var want map[string]json.RawMessage
	_ = json.Unmarshal([]byte("{"+strings.TrimPrefix(upstreamBytesExtras, ",")+"}"), &want)
	keys := []string{"unknown_field", "unknown_array", "unknown_null"}
	// OpenAI cache identity has no Anthropic wire equivalent; the system cache
	// block is checked separately on the native Messages leg.
	if protocol != "messages" {
		keys = append(keys, "prompt_cache_key", "prompt_cache_retention")
	}
	for _, key := range keys {
		decode := func(raw json.RawMessage) any {
			var v any
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			_ = d.Decode(&v)
			return v
		}
		raw, exists := got[key]
		if !exists || !reflect.DeepEqual(decode(raw), decode(want[key])) {
			return fmt.Errorf("%s lost or changed: %s", key, raw)
		}
	}
	// Independently check the protocol's actual tool round and system payload,
	// rather than accepting marker strings in unrelated extension fields.
	conversation := got["messages"]
	system := got["system"]
	if protocol == "responses" {
		conversation = got["input"]
		system = got["instructions"]
	}
	if protocol == "chat" {
		system = conversation
	}
	if !bytes.Contains(system, []byte("system-prefix")) {
		return fmt.Errorf("system prefix lost: %s", system)
	}
	if !bytes.Contains(got["tools"], []byte(`"Read"`)) {
		return fmt.Errorf("tool definition lost: %s", got["tools"])
	}
	var decoded any
	d := json.NewDecoder(bytes.NewReader(conversation))
	d.UseNumber()
	if err := d.Decode(&decoded); err != nil {
		return err
	}
	flat, _ := json.Marshal(decoded)
	for _, marker := range []string{"call_1", "Read", "9007199254740993", "tool-result", "follow-up"} {
		if !bytes.Contains(flat, []byte(marker)) {
			return fmt.Errorf("tool round lost %s: %s", marker, flat)
		}
	}
	switch protocol {
	case "chat":
		if !bytes.Contains(flat, []byte(`"tool_calls"`)) || !bytes.Contains(flat, []byte(`"tool_call_id"`)) {
			return fmt.Errorf("chat tool call/result missing")
		}
	case "responses":
		if !bytes.Contains(flat, []byte(`"function_call"`)) || !bytes.Contains(flat, []byte(`"function_call_output"`)) {
			return fmt.Errorf("responses tool call/result missing")
		}
	case "messages":
		if !bytes.Contains(flat, []byte(`"tool_use"`)) || !bytes.Contains(flat, []byte(`"tool_result"`)) {
			return fmt.Errorf("messages tool call/result missing")
		}
	}
	return upstreamBytesToolRound(body, protocol)
}
