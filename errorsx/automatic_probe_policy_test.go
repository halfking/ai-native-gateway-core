package errorsx

import (
	"testing"
	"time"
)

func TestAutomaticProbePolicyFor(t *testing.T) {
	tests := []struct {
		kind  ErrorKind
		on    bool
		scope AutomaticProbeScope
		wait  time.Duration
		fixed bool
	}{
		{KindTransient, true, AutomaticProbeModel, 5 * time.Second, false},
		{KindEmptyResponse, true, AutomaticProbeModel, 5 * time.Second, false},
		{KindUpstreamContextLoss, true, AutomaticProbeModel, 5 * time.Second, false},
		{KindRateLimit, true, AutomaticProbeModel, 3 * time.Minute, false},
		{KindConcurrent, true, AutomaticProbeModel, 5 * time.Minute, false},
		{KindNoAvailableChannel, true, AutomaticProbeModel, 15 * time.Minute, false},
		{KindQuotaPeriodic, true, AutomaticProbeCredential, 5 * time.Minute, true},
		{KindQuotaBalance, true, AutomaticProbeCredential, 2 * time.Minute, true},
		{KindQuotaPermanent, true, AutomaticProbeCredential, 2 * time.Minute, true},
		{KindAuth, false, AutomaticProbeNone, 0, false},
		{KindConversion, false, AutomaticProbeNone, 0, false},
		{KindContentFilter, false, AutomaticProbeNone, 0, false},
	}
	for _, tt := range tests {
		got := AutomaticProbePolicyFor(tt.kind)
		if got.Enabled != tt.on || got.Scope != tt.scope || got.Interval != tt.wait || got.Fixed != tt.fixed {
			t.Errorf("%s: %+v", tt.kind, got)
		}
	}
}
