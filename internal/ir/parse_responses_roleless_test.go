package ir

import "testing"

func TestParseResponses_RolelessTextItemsBecomeUserMessages(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"type":"input_text","text":"first"},{"type":"text","text":"second"},{"type":"message","content":"third"}]}`)
	req, err := ParseResponses(body)
	if err != nil {
		t.Fatalf("ParseResponses: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(req.Messages))
	}
	for i, want := range []string{"first", "second", "third"} {
		if req.Messages[i].Role != "user" {
			t.Errorf("Messages[%d].Role = %q, want user", i, req.Messages[i].Role)
		}
		if len(req.Messages[i].Content) != 1 || req.Messages[i].Content[0].Text != want {
			t.Errorf("Messages[%d].Content = %+v, want text %q", i, req.Messages[i].Content, want)
		}
	}
}
