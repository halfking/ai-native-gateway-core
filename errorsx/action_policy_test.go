package errorsx

import (
	"testing"
	"time"
)

func TestDecideNextActionEmptyResponseRetriesBeforeSwitching(t *testing.T) {
	base := DecisionContext{
		RequestID:          "req-1",
		TenantID:           "tenant-a",
		SessionID:          "sess-1",
		ClientProtocol:     "anthropic",
		Phase:              PhaseStreaming,
		AttemptNo:          1,
		SameNodeRetryCount: 0,
		MaxSameNodeRetries: 1,
		RemainingAttempts:  4,
		ProviderID:         10,
		CredentialID:       20,
		ResolvedModel:      "claude-sonnet",
		HasAlternateNode:   true,
		Kind:               KindEmptyResponse,
	}

	got := DecideNextAction(base)
	if got.Action != ActionRetrySameNode {
		t.Fatalf("first empty response action = %s, want retry_same_node", got.Action)
	}
	if !got.PreserveClientConnection || !got.RequiresUncommittedOutput {
		t.Fatalf("first empty response decision = %+v, want connection-preserving uncommitted retry", got)
	}
	if got.RecordCredentialFailure || got.RecordCredentialState {
		t.Fatalf("empty response must not punish credential: %+v", got)
	}

	base.SameNodeRetryCount = 1
	got = DecideNextAction(base)
	if got.Action != ActionSwitchNode {
		t.Fatalf("exhausted empty response action = %s, want switch_node", got.Action)
	}
	if !got.RecordProviderFailure || got.RecordCredentialState {
		t.Fatalf("switch decision = %+v, want provider feedback without credential state mutation", got)
	}
}

func TestDecideNextActionCommittedAndCanceledPrecedence(t *testing.T) {
	committed := DecisionContext{Kind: KindNetwork, CommitState: ActionCommitContent, SemanticOutputVisible: true, HasAlternateNode: true}
	got := DecideNextAction(committed)
	if got.Action != ActionResumeBlocked || got.PreserveClientConnection {
		t.Fatalf("committed network decision = %+v, want resume_blocked", got)
	}

	canceled := DecisionContext{Kind: KindNetwork, ClientDisconnected: true, HasAlternateNode: true}
	got = DecideNextAction(canceled)
	if got.Action != ActionClientCanceled || got.RecordProviderFailure || got.RecordCredentialFailure {
		t.Fatalf("client disconnect decision = %+v, want client_canceled without provider side effects", got)
	}
}

func TestDecideNextActionNetworkAndWaitPolicies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ctx   DecisionContext
		want  NextAction
		wait  time.Duration
		probe bool
	}{
		{
			name: "network retries on same node first",
			ctx:  DecisionContext{Kind: KindNetwork, MaxSameNodeRetries: 2, RemainingAttempts: 3},
			want: ActionRetrySameNode, probe: true,
		},
		{
			name: "rate limit waits",
			ctx:  DecisionContext{Kind: KindRateLimit, RetryAfter: 7 * time.Second, RemainingAttempts: 3},
			want: ActionWaitRecovery, wait: 7 * time.Second, probe: true,
		},
		{
			name: "terminal request error",
			ctx:  DecisionContext{Kind: KindContentFilter, HasAlternateNode: true, RemainingAttempts: 3},
			want: ActionFailTerminal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideNextAction(tc.ctx)
			if got.Action != tc.want {
				t.Fatalf("action = %s, want %s (%+v)", got.Action, tc.want, got)
			}
			if tc.wait > 0 && got.RetryAfter != tc.wait {
				t.Fatalf("retry_after = %s, want %s", got.RetryAfter, tc.wait)
			}
			if got.EnqueueProbe != tc.probe {
				t.Fatalf("enqueue_probe = %v, want %v", got.EnqueueProbe, tc.probe)
			}
		})
	}
}

func TestDecideNextActionPreservesCorrelationAndFailsClosedUnknown(t *testing.T) {
	ctx := DecisionContext{
		RequestID: "req-42", TenantID: "tenant-x", SessionID: "sess-x", ClientProtocol: "responses",
		Phase: PhaseSettlement, AttemptNo: 3, ProviderID: 4, CredentialID: 5, ResolvedModel: "m",
		Kind: ErrorKind("future_kind"),
	}
	got := DecideNextAction(ctx)
	if got.Action != ActionFailClosed || got.ReasonCode != "unmapped_kind:future_kind" {
		t.Fatalf("unknown decision = %+v, want fail closed", got)
	}
	if got.RequestID != ctx.RequestID || got.TenantID != ctx.TenantID || got.SessionID != ctx.SessionID || got.ProviderID != ctx.ProviderID || got.CredentialID != ctx.CredentialID || got.ResolvedModel != ctx.ResolvedModel || got.AttemptNo != ctx.AttemptNo {
		t.Fatalf("decision lost correlation fields: %+v", got)
	}
}
