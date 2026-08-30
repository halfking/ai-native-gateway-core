package errorsx

import "testing"

func TestProjectRecoveryKeepsGenericRetrySemanticsSeparate(t *testing.T) {
	if IsRetryable(KindEmptyResponse) || IsRetryable(KindNoAvailableChannel) {
		t.Fatal("dedicated failover kinds must remain outside IsRetryable")
	}

	tests := []struct {
		kind        ErrorKind
		generic     bool
		failover    bool
		action      string
		reason      string
		transparent bool
	}{
		{KindEmptyResponse, false, true, RecoveryActionCandidateFailover, "empty_response_candidate_failover", true},
		{KindNoAvailableChannel, false, true, RecoveryActionCandidateFailover, "no_available_channel_candidate_failover", true},
		{KindTimeout, true, false, RecoveryActionGenericRetry, "generic_retryable_upstream_failure", true},
		{KindAuth, false, false, RecoveryActionTerminal, "not_recoverable", false},
	}
	for _, tt := range tests {
		got := ProjectRecovery(tt.kind)
		if got.GenericRetryable != tt.generic || got.CandidateFailover != tt.failover ||
			got.TransparentResume != tt.transparent || got.EffectiveAction != tt.action || got.Reason != tt.reason {
			t.Errorf("ProjectRecovery(%q) = %#v", tt.kind, got)
		}
	}
}
