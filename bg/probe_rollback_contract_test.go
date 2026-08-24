package bg

import (
	"os"
	"strings"
	"testing"
)

func TestProbeConfirmationClearsTentativeRestoreDeadline(t *testing.T) {
	source, err := os.ReadFile("node_probe.go")
	if err != nil {
		t.Fatalf("read node_probe.go: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (w *NodeProbeWorker) updateBindingAvailability")
	if start < 0 {
		t.Fatal("updateBindingAvailability not found")
	}
	successBranch := text[start:]
	if !strings.Contains(successBranch, "probe_revert_at = NULL") {
		t.Fatal("confirmed successful probe must clear probe_revert_at")
	}
}
