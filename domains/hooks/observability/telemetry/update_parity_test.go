package telemetry

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestUpdateRequestLogSQL_IncludesRoutingAndAttachments guards the same class
// of gap as t0..t9: emitTelemetry fills routing_attempts / routing_summary /
// attachments / canonical_model on the completion Update path, so the SQL must
// assign those columns (not only INSERT / ON CONFLICT).
func TestUpdateRequestLogSQL_IncludesRoutingAndAttachments(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "client.go"))
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "func (c *Client) updateRequestLog")
	if start < 0 {
		t.Fatal("updateRequestLog not found")
	}
	chunk := text[start:]
	end := strings.Index(chunk, "\nfunc (c *Client) ")
	if end > 0 {
		chunk = chunk[:end]
	}
	for _, needle := range []string{
		"routing_attempts",
		"routing_summary",
		"attachments",
		"canonical_model",
		"t0_arrived_at",
	} {
		if !strings.Contains(chunk, needle) {
			t.Fatalf("updateRequestLog SQL missing %q (INSERT/UPDATE parity)", needle)
		}
	}
	if !strings.Contains(text, "routing_attempts = CASE") {
		t.Fatal("ON CONFLICT / UPDATE must CASE-fill routing_attempts over JSON null")
	}
}
