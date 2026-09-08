package bg

import (
	"strings"
	"testing"
)

func TestProbeConfirmationClearsTentativeRestoreDeadline(t *testing.T) {
	if !strings.Contains(healthyBindingSQL(), "probe_revert_at = NULL") {
		t.Fatal("confirmed successful probe must clear probe_revert_at")
	}
}
