package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
)

// Wave 3 B14: the 300s lease hard cap. The decisive semantics — Released()
// gating and the Unlimited/disabled arms — are pinned here; the timer
// plumbing is a thin time.AfterFunc wrapper around them.

func TestFpSlotLeaseDeadline_ReleasedLeaseIsNoOp(t *testing.T) {
	// A nil lease always reads as released; the executors-side test covers
	// the acquired→released flip in credentialfpslot.
	var nilLease *credentialfpslot.Lease
	if !nilLease.Released() {
		t.Fatal("nil lease must read as released")
	}
	// Must not panic and must not force-release anything.
	enforceFpSlotLeaseDeadline(nil, nil)
	enforceFpSlotLeaseDeadline(nil, nilLease)
}

func TestFpSlotLeaseDeadline_ArmSkipsIneligible(t *testing.T) {
	// Disabled manager, Unlimited lease and non-positive caps must all be
	// silently skipped (no timer, no panic).
	armFpSlotLeaseDeadlineFor(nil, &credentialfpslot.Lease{}, fpSlotLeaseHardCap)
	m := credentialfpslot.New(credentialfpslot.Config{Enabled: false, DefaultLimit: 2}, nil)
	armFpSlotLeaseDeadlineFor(m, &credentialfpslot.Lease{}, fpSlotLeaseHardCap)
	armFpSlotLeaseDeadlineFor(m, &credentialfpslot.Lease{Unlimited: true}, fpSlotLeaseHardCap)
	armFpSlotLeaseDeadlineFor(m, &credentialfpslot.Lease{}, 0)
}
