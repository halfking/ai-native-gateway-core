package admin

import (
	"os"
	"strings"
	"testing"
)

func TestRoutingCandidateBindingPriorityContract(t *testing.T) {
	src, err := os.ReadFile("routing.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{
		`Priority       *bool ` + "`json:\"priority\"`",
		"priority        = COALESCE($4::boolean, priority)",
		"WHERE id = $5",
		`"priority":        req.Priority`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("candidate-binding priority PATCH contract missing %q", want)
		}
	}
}

func TestRoutingCandidateBindingPriorityBooleanIsPointer(t *testing.T) {
	src, err := os.ReadFile("routing.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, `Priority       *bool `+"`json:\"priority\"`") {
		t.Fatal("priority must be a pointer bool so false can be PATCHed explicitly")
	}
}
