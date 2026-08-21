package dispatch

import (
	"context"
	"errors"
	"testing"
)

// fakeBackend is the minimum impl needed to assert the GovernorBackend
// contract. Stage B replaces it with real backends; Stage A only needs to
// pin the lifecycle signature so future additions don't drift.
type fakeBackend struct {
	kind     GovernorBackendKind
	name     string
	openErr  error
	closeErr error
	notifs   []uint64

	// newResult controls what New returns in tests that exercise it.
	newGov   Governor
	newErr   error
	newCalls int
}

func (f *fakeBackend) Kind() GovernorBackendKind       { return f.kind }
func (f *fakeBackend) Name() string                    { return f.name }
func (f *fakeBackend) Open(_ context.Context) error    { return f.openErr }
func (f *fakeBackend) Close(_ context.Context) error   { return f.closeErr }
func (f *fakeBackend) NotifyRevisions(_ context.Context, rev uint64) error {
	f.notifs = append(f.notifs, rev)
	return nil
}
func (f *fakeBackend) New(_ context.Context, _ GovernorSpec) (Governor, error) {
	f.newCalls++
	if f.newErr != nil {
		return nil, f.newErr
	}
	if f.newGov != nil {
		return f.newGov, nil
	}
	return newNoopGovernor(), nil
}

// Compile-time guarantee that fakeBackend satisfies the contract; a future
// signature change on GovernorBackend will break the build before review.
var _ GovernorBackend = (*fakeBackend)(nil)

func TestGovernorBackendKindConstantsAreStable(t *testing.T) {
	// These string values feed Stage C metric label values; changing them
	// is a coordinate-breaking operation for any dashboards/alerts that
	// already match the closed-enum strings.
	cases := []struct {
		got  GovernorBackendKind
		want string
	}{
		{BackendLocal, "local"},
		{BackendRedisEnforce, "redis_enforce"},
		{BackendRedisShadow, "redis_shadow"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Fatalf("backend kind drift: got %q want %q", string(tc.got), tc.want)
		}
	}
}

func TestGovernorBackendLifecycleRoundTrip(t *testing.T) {
	b := &fakeBackend{kind: BackendLocal, name: "unit"}
	ctx := context.Background()
	if err := b.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestGovernorBackendLifecycleErrorsPropagate(t *testing.T) {
	want := errors.New("boom")
	b := &fakeBackend{kind: BackendRedisEnforce, name: "unit", openErr: want, closeErr: want}
	if err := b.Open(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Open error propagation: got %v want %v", err, want)
	}
	if err := b.Close(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Close error propagation: got %v want %v", err, want)
	}
}

func TestGovernorBackendNotifyRevisionsRecords(t *testing.T) {
	b := &fakeBackend{kind: BackendLocal, name: "unit"}
	for _, r := range []uint64{1, 2, 3, 7} {
		if err := b.NotifyRevisions(context.Background(), r); err != nil {
			t.Fatalf("NotifyRevisions(%d): %v", r, err)
		}
	}
	if got, want := len(b.notifs), 4; got != want {
		t.Fatalf("notif count: got %d want %d", got, want)
	}
	if b.notifs[3] != 7 {
		t.Fatalf("last revision: got %d want 7", b.notifs[3])
	}
}