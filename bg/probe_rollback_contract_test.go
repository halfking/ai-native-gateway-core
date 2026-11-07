package bg

import (
	"strings"
	"testing"
	"time"
)

func TestProbeConfirmationClearsTentativeRestoreDeadline(t *testing.T) {
	if !strings.Contains(healthyBindingSQL(time.Now()), "probe_revert_at = NULL") {
		t.Fatal("confirmed successful probe must clear probe_revert_at")
	}
}
