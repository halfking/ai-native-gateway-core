package fault

import (
	"context"
	"strings"
	"testing"
)

func TestRunScriptDisabledByDefault(t *testing.T) {
	t.Setenv("LLM_GATEWAY_FAULT_SCRIPT_DIR", "")

	_, err := NewActionExecutor().Execute(context.Background(), ActionScript, map[string]interface{}{
		"script": "/bin/true",
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestRunScriptRejectsPathOutsideAllowedDirectory(t *testing.T) {
	t.Setenv("LLM_GATEWAY_FAULT_SCRIPT_DIR", "/opt/llm-gateway/scripts")

	_, err := NewActionExecutor().Execute(context.Background(), ActionScript, map[string]interface{}{
		"script": "/bin/true",
	})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected outside directory error, got %v", err)
	}
}
