package dispatch

import (
	"errors"
	"testing"
)

func TestSnapshotStateConstantsAreStable(t *testing.T) {
	cases := []struct {
		got  SnapshotState
		want string
	}{
		{SnapshotStateReady, "ready"},
		{SnapshotStateQueueFull, "queue_full"},
		{SnapshotStateGovernorSaturated, "governor_saturated"},
		{SnapshotStateUnknown, "unknown"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Fatalf("snapshot state drift: got %q want %q", string(tc.got), tc.want)
		}
	}
}

func TestGovernorSnapshotPopulatesAllFields(t *testing.T) {
	snap := GovernorSnapshot{
		SpecRevision: 7,
		Backend:      string(BackendRedisEnforce),
		Mode:         ModeConcurrency,
		Limit:        16,
		Used:         4,
		InFlight:     4,
		QueueDepth:   1,
		State:        SnapshotStateReady,
		AgeMS:        100,
	}
	if snap.SpecRevision != 7 || snap.Backend != string(BackendRedisEnforce) ||
		snap.Mode != ModeConcurrency || snap.Limit != 16 || snap.Used != 4 ||
		snap.InFlight != 4 || snap.QueueDepth != 1 || snap.State != SnapshotStateReady ||
		snap.AgeMS != 100 || snap.BackendErr != nil {
		t.Fatalf("field round-trip drift: %+v", snap)
	}
}

// BackendErr is the only open-ended field; it carries a typed Go error.
// All other fields must remain closed-enum strings or numbers so the
// Prometheus label allowlist stays predictable.
func TestGovernorSnapshotBackendErrCarriesGoError(t *testing.T) {
	want := errors.New("redis: connection refused")
	snap := GovernorSnapshot{State: SnapshotStateUnknown, BackendErr: want}
	if !errors.Is(snap.BackendErr, want) {
		t.Fatalf("BackendErr not preserved: got %v want %v", snap.BackendErr, want)
	}
}

// ADR contract: State == SnapshotStateUnknown ⇔ BackendErr != nil.
// Both directions must hold so Stage C metrics can drop a row whose
// State is Unknown (it carries a backend fault, not a real state) and
// so a non-Unknown State can never mask a backend fault by accident.
func TestGovernorSnapshotInvariantUnknownImpliesBackendErr(t *testing.T) {
	// State=Unknown without BackendErr is a contract violation.
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("State=SnapshotStateUnknown without BackendErr should panic; it did not")
		}
	}()
	_ = GovernorSnapshot{State: SnapshotStateUnknown, BackendErr: nil}.Validate()
}

func TestGovernorSnapshotInvariantBackendErrImpliesUnknown(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("BackendErr != nil without State=SnapshotStateUnknown should panic; it did not")
		}
	}()
	_ = GovernorSnapshot{
		State:      SnapshotStateReady,
		BackendErr: errors.New("redis: down"),
	}.Validate()
}

func TestGovernorSnapshotValidStatesPassValidate(t *testing.T) {
	cases := []GovernorSnapshot{
		{State: SnapshotStateReady},
		{State: SnapshotStateQueueFull},
		{State: SnapshotStateGovernorSaturated},
		{State: SnapshotStateUnknown, BackendErr: errors.New("backend fault")},
	}
	for i, snap := range cases {
		if err := snap.Validate(); err != nil {
			t.Fatalf("case %d: unexpected Validate failure: %v", i, err)
		}
	}
}
