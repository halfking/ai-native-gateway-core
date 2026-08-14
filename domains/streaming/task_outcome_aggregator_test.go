package streaming

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// SR-W1 task-level outcome aggregation (doc 18 §5.1 ExecuteAttempt / §10.3):
// the AttemptResult contract replaces "guess from the last error" with an
// exhaustive mapping from every errorsx.ErrorKind to a task action. Any
// unmapped kind must aggregate fail-closed — never silently retried.

// allErrorKinds enumerates every errorsx.ErrorKind constant as of
// errorsx/classify.go. When a new kind is added upstream it must be added
// here AND to the aggregator mapping; the exhaustive test below fails on
// drift because unclassified kinds aggregate to TaskActionFailClosed.
var allErrorKinds = []errorsx.ErrorKind{
	errorsx.KindTransient,
	errorsx.KindTimeout,
	errorsx.KindNetwork,
	errorsx.KindRateLimit,
	errorsx.KindAuth,
	errorsx.KindQuota,
	errorsx.KindUpstreamDown,
	errorsx.KindCanceled,
	errorsx.KindClientBug,
	errorsx.KindConcurrent,
	errorsx.KindAuthRevoked,
	errorsx.KindQuotaPeriodic,
	errorsx.KindQuotaBalance,
	errorsx.KindQuotaPermanent,
	errorsx.KindModelNotFound,
	errorsx.KindStreamTimeout,
	errorsx.KindToolCallIdMismatch,
	errorsx.KindContextLength,
	errorsx.KindUnsupportedFeature,
	errorsx.KindModelDeprecated,
	errorsx.KindContentFilter,
	errorsx.KindEmptyResponse,
	errorsx.KindConversion,
	errorsx.KindUpstreamContextLoss,
	errorsx.KindUpstreamOverloaded,
	errorsx.KindNoAvailableChannel,
}

func TestAggregateTaskOutcomeExhaustiveKindMatrix(t *testing.T) {
	cases := []struct {
		kind errorsx.ErrorKind
		want TaskAction
	}{
		// connection-internal immediate recovery (refresh candidates, retry)
		{errorsx.KindTransient, TaskActionRetryNow},
		{errorsx.KindTimeout, TaskActionRetryNow},
		{errorsx.KindNetwork, TaskActionRetryNow},
		{errorsx.KindConcurrent, TaskActionRetryNow},
		{errorsx.KindUpstreamOverloaded, TaskActionRetryNow},
		{errorsx.KindEmptyResponse, TaskActionRetryNow},
		{errorsx.KindAuth, TaskActionRetryNow},
		{errorsx.KindStreamTimeout, TaskActionRetryNow},
		// wait-for-recovery (quota window / provider down / no capacity)
		{errorsx.KindRateLimit, TaskActionWaitRecovery},
		{errorsx.KindQuota, TaskActionWaitRecovery},
		{errorsx.KindQuotaPeriodic, TaskActionWaitRecovery},
		{errorsx.KindQuotaBalance, TaskActionWaitRecovery},
		{errorsx.KindUpstreamDown, TaskActionWaitRecovery},
		{errorsx.KindNoAvailableChannel, TaskActionWaitRecovery},
		// terminal — no retry, surface to client
		{errorsx.KindAuthRevoked, TaskActionFailTerminal},
		{errorsx.KindQuotaPermanent, TaskActionFailTerminal},
		{errorsx.KindModelNotFound, TaskActionFailTerminal},
		{errorsx.KindModelDeprecated, TaskActionFailTerminal},
		{errorsx.KindContextLength, TaskActionFailTerminal},
		{errorsx.KindUnsupportedFeature, TaskActionFailTerminal},
		{errorsx.KindContentFilter, TaskActionFailTerminal},
		{errorsx.KindConversion, TaskActionFailTerminal},
		{errorsx.KindUpstreamContextLoss, TaskActionFailTerminal},
		{errorsx.KindToolCallIdMismatch, TaskActionFailTerminal},
		// client-side terminal
		{errorsx.KindCanceled, TaskActionFailTerminal},
		{errorsx.KindClientBug, TaskActionFailTerminal},
	}
	covered := map[errorsx.ErrorKind]bool{}
	for _, tc := range cases {
		covered[tc.kind] = true
		res := &AttemptResult{
			CandidateOutcomes: []CandidateOutcome{{CandidateID: "c1", Kind: tc.kind}},
			CommitState:       CommitStateNone,
		}
		got := AggregateTaskOutcome(res)
		if got.Action != tc.want {
			t.Errorf("kind %s: action = %v, want %v (reason %q)", tc.kind, got.Action, tc.want, got.Reason)
		}
	}
	for _, k := range allErrorKinds {
		if !covered[k] {
			t.Errorf("exhaustive matrix missing kind %s — add a row AND an aggregator mapping", k)
		}
	}
	if len(covered) != len(allErrorKinds) {
		t.Fatalf("matrix has %d kinds, allErrorKinds has %d — drift between test table and enumeration", len(covered), len(allErrorKinds))
	}
}

func TestAggregateTaskOutcomeUnknownKindFailsClosed(t *testing.T) {
	res := &AttemptResult{
		CandidateOutcomes: []CandidateOutcome{{CandidateID: "c1", Kind: errorsx.ErrorKind("brand_new_kind")}},
	}
	got := AggregateTaskOutcome(res)
	if got.Action != TaskActionFailClosed {
		t.Fatalf("unknown kind: action = %v, want TaskActionFailClosed", got.Action)
	}
}

func TestAggregateTaskOutcomeSuccess(t *testing.T) {
	res := &AttemptResult{Success: true, CommitState: CommitStateTerminal}
	got := AggregateTaskOutcome(res)
	if got.Action != TaskActionSucceed {
		t.Fatalf("success: action = %v, want TaskActionSucceed", got.Action)
	}
}

func TestAggregateTaskOutcomeCommittedOutputBlocksTransparentRetry(t *testing.T) {
	// A recoverable kind after content/tool_call has committed: restarting
	// would duplicate client-visible output — doc 18 §10.3 fail closed.
	res := &AttemptResult{
		CandidateOutcomes: []CandidateOutcome{{CandidateID: "c1", Kind: errorsx.KindRateLimit}},
		CommitState:       CommitStateContent,
	}
	if got := AggregateTaskOutcome(res); got.Action != TaskActionResumeBlocked {
		t.Fatalf("committed + rate_limit: action = %v, want TaskActionResumeBlocked", got.Action)
	}
	res.CommitState = CommitStateToolCall
	if got := AggregateTaskOutcome(res); got.Action != TaskActionResumeBlocked {
		t.Fatalf("committed tool_call + rate_limit: action = %v, want TaskActionResumeBlocked", got.Action)
	}
	// Terminal kinds stay terminal regardless of commit state.
	res.CandidateOutcomes[0].Kind = errorsx.KindContentFilter
	if got := AggregateTaskOutcome(res); got.Action != TaskActionFailTerminal {
		t.Fatalf("committed + content_filter: action = %v, want TaskActionFailTerminal", got.Action)
	}
}

func TestAggregateTaskOutcomeMixedCandidates(t *testing.T) {
	// retry-now beats wait-recovery: one transient candidate means refreshed
	// candidates can be retried immediately.
	res := &AttemptResult{
		CandidateOutcomes: []CandidateOutcome{
			{CandidateID: "c1", Kind: errorsx.KindRateLimit, RetryAfter: 30 * time.Second},
			{CandidateID: "c2", Kind: errorsx.KindTransient},
		},
	}
	got := AggregateTaskOutcome(res)
	if got.Action != TaskActionRetryNow {
		t.Fatalf("mixed rate_limit+transient: action = %v, want TaskActionRetryNow", got.Action)
	}

	// terminal beats everything: one content_filter candidate fails the task.
	res.CandidateOutcomes = []CandidateOutcome{
		{CandidateID: "c1", Kind: errorsx.KindQuota},
		{CandidateID: "c2", Kind: errorsx.KindContentFilter},
	}
	if got := AggregateTaskOutcome(res); got.Action != TaskActionFailTerminal {
		t.Fatalf("mixed quota+content_filter: action = %v, want TaskActionFailTerminal", got.Action)
	}

	// wait-recovery surfaces the max Retry-After across candidates.
	res.CandidateOutcomes = []CandidateOutcome{
		{CandidateID: "c1", Kind: errorsx.KindQuotaPeriodic, RetryAfter: 10 * time.Minute},
		{CandidateID: "c2", Kind: errorsx.KindRateLimit, RetryAfter: 2 * time.Second},
	}
	got = AggregateTaskOutcome(res)
	if got.Action != TaskActionWaitRecovery {
		t.Fatalf("quota mix: action = %v, want TaskActionWaitRecovery", got.Action)
	}
	if got.NextRetryAfter != 10*time.Minute {
		t.Fatalf("wait-recovery should carry max Retry-After: got %v, want 10m", got.NextRetryAfter)
	}
}
