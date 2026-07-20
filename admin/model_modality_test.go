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
	expected := []string{"text", "vision", "audio", "video", "multimodal", "embedding"}
	for _, m := range expected {
		if !validModalities[m] {
			t.Errorf("expected modality %q to be in validModalities", m)
		}
	}
}

func TestValidModalities_RejectsOthers(t *testing.T) {
	invalid := []string{"image", "Video", "", "TEXT", "Video_2", "speech"}
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
	// 'video' is now valid (migration 451, 2026-07-20). Removed from invalid list.
	invalidModalities := []string{"image", "TEXT", "", "Video_2", "speech"}
	for _, m := range invalidModalities {
		if validModalities[m] {
			t.Errorf("test invariant broken: %q should not be in validModalities", m)
		}
		_ = httptest.NewRequest(http.MethodPatch, "/api/models/1/modality", bytes.NewReader([]byte(`{"modality":"`+m+`"}`)))
	}
}
