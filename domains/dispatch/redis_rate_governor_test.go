package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRedisRateGovernorRPMUsesAtomicBucket(t *testing.T) {
	client, _ := newRedisBackendTestClient(t)
	backend := NewRedisEnforceBackend(client, "rate-test")

	gov, err := backend.New(context.Background(), GovernorSpec{
		CredentialID: 7,
		ProviderID:   42,
		Mode:         ModeRPM,
		RPMLimit:     2,
		Backend:      BackendRedisEnforce,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	qr := &QueuedRequest{ID: "rpm-1"}
	for i := 0; i < 2; i++ {
		if err := gov.Acquire(context.Background(), qr, time.Time{}); err != nil {
			t.Fatalf("Acquire(%d): %v", i, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gov.Acquire(ctx, &QueuedRequest{ID: "rpm-3"}, time.Now().Add(time.Second)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third acquire error = %v, want context deadline", err)
	}

}

func TestRedisRateGovernorTPMChargesEstimatedTokens(t *testing.T) {
	client, _ := newRedisBackendTestClient(t)
	backend := NewRedisEnforceBackend(client, "rate-test")

	gov, err := backend.New(context.Background(), GovernorSpec{
		CredentialID: 8,
		ProviderID:   42,
		Mode:         ModeTPM,
		TPMLimit:     100,
		Backend:      BackendRedisEnforce,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := gov.Acquire(context.Background(), &QueuedRequest{ID: "tpm-1", EstimatedTokens: 75}, time.Time{}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gov.Acquire(ctx, &QueuedRequest{ID: "tpm-2", EstimatedTokens: 30}, time.Now().Add(time.Second)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error = %v, want context deadline", err)
	}
}

func TestRedisRateGovernorRejectsCostAboveTPMLimit(t *testing.T) {
	client, _ := newRedisBackendTestClient(t)
	backend := NewRedisEnforceBackend(client, "rate-test")

	gov, err := backend.New(context.Background(), GovernorSpec{
		CredentialID: 9,
		ProviderID:   42,
		Mode:         ModeTPM,
		TPMLimit:     100,
		Backend:      BackendRedisEnforce,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = gov.Acquire(context.Background(), &QueuedRequest{ID: "tpm-too-large", EstimatedTokens: 101}, time.Now())
	if !errors.Is(err, ErrGovernorUnavailable) {
		t.Fatalf("Acquire error = %v, want ErrGovernorUnavailable", err)
	}
}
