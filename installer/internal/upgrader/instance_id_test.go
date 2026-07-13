package upgrader

import "testing"

func TestResolveInstanceIDFromEnv(t *testing.T) {
	t.Setenv("INSTANCE_ID", "test-instance-123")
	if got := resolveInstanceID(); got != "test-instance-123" {
		t.Fatalf("got %q", got)
	}
}
