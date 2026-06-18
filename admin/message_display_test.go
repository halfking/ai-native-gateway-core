package admin

import "testing"

func TestExtractLatestUserFromRequestJSON(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"user","content":"first"},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":"second turn"}
		]
	}`)
	got := extractLatestUserFromRequestJSON(body)
	if got != "second turn" {
		t.Fatalf("got %q want second turn", got)
	}
}

func TestLatestUserFromPreview(t *testing.T) {
	preview := "user: hello | assistant: hi | user: deploy please"
	got := latestUserFromPreview(preview)
	if got != "deploy please" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractAssistantFromResponseJSON(t *testing.T) {
	body := []byte(`{
		"choices":[{"message":{
			"role":"assistant",
			"content":"done",
			"tool_calls":[{"function":{"name":"read_file"}}]
		}}]
	}`)
	text, tools := extractAssistantFromResponseJSON(body)
	if text != "done" {
		t.Fatalf("text=%q", text)
	}
	if tools == "" {
		t.Fatal("expected tool summary")
	}
}

func TestParseNoTopicVirtualTaskID(t *testing.T) {
	h, ok := parseNoTopicVirtualTaskID("notopic:a7269902f8a04055:2026-06-18T17:00:00Z")
	if !ok || h != "2026-06-18T17:00:00Z" {
		t.Fatalf("got %q ok=%v", h, ok)
	}
}
