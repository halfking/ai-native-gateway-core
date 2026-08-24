package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type affinityRecorder struct {
	mu          sync.Mutex
	successes   []int
	invalidated []int
}

func (r *affinityRecorder) RecordSuccess(_ context.Context, _ string, credential CredentialRef, _ string) error {
	r.mu.Lock()
	r.successes = append(r.successes, credential.CredentialID)
	r.mu.Unlock()
	return nil
}

func (r *affinityRecorder) Invalidate(_ context.Context, _ string, credentialID int) error {
	r.mu.Lock()
	r.invalidated = append(r.invalidated, credentialID)
	r.mu.Unlock()
	return nil
}

func TestPriorityClusterSortPreservesOrderWithinCluster(t *testing.T) {
	refs := sortPriorityClusters([]CredentialRef{
		{CredentialID: 3, PriorityCluster: 2},
		{CredentialID: 1, PriorityCluster: 1},
		{CredentialID: 2, PriorityCluster: 1},
	})
	got := []int{refs[0].CredentialID, refs[1].CredentialID, refs[2].CredentialID}
	want := []int{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted credential ids = %v, want %v", got, want)
		}
	}
}

func TestPipelineUpdatesAffinityOnlyAfterSuccess(t *testing.T) {
	recorder := &affinityRecorder{}
	p := NewPipeline(Deps{SessionAffinitySink: recorder})
	qr := NewQueuedRequest("request", "tenant", "model", context.Background(), nil)
	qr.SessionID = "session"
	qr.SelectedCred = CredentialRef{CredentialID: 7}
	p.recordSessionAffinity(qr, ForwardOutcome{Result: "ok"})
	p.recordSessionAffinity(qr, ForwardOutcome{Err: errors.New("upstream failed")})
	if len(recorder.successes) != 1 || recorder.successes[0] != 7 {
		t.Fatalf("successes = %v, want [7]", recorder.successes)
	}
}

func TestPipelineInvalidatesAffinityOnCredentialFailover(t *testing.T) {
	recorder := &affinityRecorder{}
	p := NewPipeline(Deps{SessionAffinitySink: recorder})
	qr := NewQueuedRequest("request", "tenant", "model", context.Background(), nil)
	qr.SessionID = "session"
	p.invalidateSessionAffinity(qr, 9)
	if len(recorder.invalidated) != 1 || recorder.invalidated[0] != 9 {
		t.Fatalf("invalidated = %v, want [9]", recorder.invalidated)
	}
}
