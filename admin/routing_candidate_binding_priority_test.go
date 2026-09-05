package admin

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRoutingCandidateBindingPriorityContract pins the PATCH contract for
// the priority flag: pointer-bool body field (so false is distinct from
// omitted), the $4 COALESCE slot, and the audit trail entry.
func TestRoutingCandidateBindingPriorityContract(t *testing.T) {
	src, err := os.ReadFile("routing.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{
		"Priority       *bool `json:\"priority\"`",
		"priority        = COALESCE($4::boolean, priority)",
		"WHERE id = $5",
		"\"priority\":        req.Priority",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("candidate-binding priority PATCH contract missing %q", want)
		}
	}
}

// TestRoutingCandidateBindingPriorityBodyIsPointer pins the pointer
// semantics with a real decode: false must survive as a non-nil pointer so
// an explicit disable is not mistaken for an omitted field.
func TestRoutingCandidateBindingPriorityBodyIsPointer(t *testing.T) {
	var withFalse struct {
		Priority *bool `json:"priority"`
	}
	if err := json.Unmarshal([]byte(`{"priority": false}`), &withFalse); err != nil {
		t.Fatal(err)
	}
	if withFalse.Priority == nil || *withFalse.Priority {
		t.Fatal("priority=false must decode to a non-nil false pointer")
	}

	var omitted struct {
		Priority *bool `json:"priority"`
	}
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Priority != nil {
		t.Fatal("omitted priority must decode to nil (keep current value)")
	}
}
