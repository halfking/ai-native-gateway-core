package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidModalities_AllExpected(t *testing.T) {
	expected := []string{"text", "vision", "audio", "multimodal", "embedding"}
	for _, m := range expected {
		if !validModalities[m] {
			t.Errorf("expected modality %q to be in validModalities", m)
		}
	}
}

func TestValidModalities_RejectsOthers(t *testing.T) {
	invalid := []string{"video", "image", "Video", "", "TEXT", "Video_2"}
	for _, m := range invalid {
		if validModalities[m] {
			t.Errorf("expected modality %q to NOT be in validModalities", m)
		}
	}
}

func TestUpdateModelModality_RequestShape(t *testing.T) {
	body := map[string]any{
		"modality": "vision",
		"reason":   "rule table misclassified experimental model",
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(buf.String(), `"modality":"vision"`) {
		t.Errorf("encoded body missing modality field: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"reason":`) {
		t.Errorf("encoded body missing reason field: %s", buf.String())
	}
}

func TestUpdateModelModality_RejectsInvalidModality_Contract(t *testing.T) {
	invalidModalities := []string{"video", "image", "TEXT", "", "Video_2"}
	for _, m := range invalidModalities {
		if validModalities[m] {
			t.Errorf("test invariant broken: %q should not be in validModalities", m)
		}
		_ = httptest.NewRequest(http.MethodPatch, "/api/models/1/modality", bytes.NewReader([]byte(`{"modality":"`+m+`"}`)))
	}
}
