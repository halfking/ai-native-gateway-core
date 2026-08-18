package migration

import (
	"context"
	"strings"
	"testing"
)

func TestPromotionValidationRejectsUnsafeEdgesAndEvidence(t *testing.T) {
	valid := PromotionRequest{
		LedgerID: "ledger", Owner: "owner", ExpectedCheckpoint: CheckpointObserve, ExpectedEpoch: 3,
		Checkpoint: CheckpointCleanup, Mode: ModeCanonical, Actor: "owner", ApprovedBy: "reviewer",
		EvidenceSHA256: strings.Repeat("a", 64), EvidenceRef: "evidence://immutable/observe",
	}
	if err := validPromotion(valid); err != nil {
		t.Fatalf("valid promotion: %v", err)
	}
	for _, mutate := range []func(*PromotionRequest){
		func(r *PromotionRequest) { r.Checkpoint = CheckpointCleanup; r.ExpectedCheckpoint = CheckpointCopy },
		func(r *PromotionRequest) { r.Mode = ModeDual },
		func(r *PromotionRequest) { r.ApprovedBy = "" },
		func(r *PromotionRequest) { r.EvidenceSHA256 = "not-a-sha" },
		func(r *PromotionRequest) { r.Checkpoint = CheckpointDual },
		func(r *PromotionRequest) {
			r.ExpectedCheckpoint = CheckpointCoverage
			r.Checkpoint = CheckpointDual
			r.Mode = ModeCanonical
		},
		func(r *PromotionRequest) {
			r.ExpectedCheckpoint = CheckpointDual
			r.Checkpoint = CheckpointObserve
			r.Mode = ModeCanonical
		},
	} {
		request := valid
		mutate(&request)
		if err := validPromotion(request); err == nil {
			t.Fatalf("unsafe promotion accepted: %+v", request)
		}
	}
}

type deniedApprover struct{}

func (deniedApprover) ApprovePromotion(context.Context, PromotionRequest) error {
	return context.Canceled
}

func TestPromotionCoordinatorFailsClosedWithoutDependenciesOrApproval(t *testing.T) {
	request := PromotionRequest{}
	if err := (&PromotionCoordinator{}).Promote(context.Background(), request); err == nil {
		t.Fatal("promotion without dependencies and approver must fail closed")
	}
}
