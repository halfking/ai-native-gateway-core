package dispatch

import (
	"context"
	"testing"
)

func TestAttemptIdentityIsUniqueAcrossRetries(t *testing.T) {
	qr := NewQueuedRequest("request-contract", "tenant-contract", "model-contract", context.Background(), nil)
	firstReserved := qr.reserveAttempt(cred(1, ModeConcurrency, 1))
	first, ok := qr.commitReservedAttempt(firstReserved.AttemptID)
	if !ok {
		t.Fatal("first attempt reservation was not committed")
	}
	secondReserved := qr.reserveAttempt(cred(2, ModeConcurrency, 1))
	second, ok := qr.commitReservedAttempt(secondReserved.AttemptID)
	if !ok {
		t.Fatal("second attempt reservation was not committed")
	}
	if first.AttemptID == "" || second.AttemptID == "" {
		t.Fatal("attempt ids must be generated")
	}
	if first.AttemptID == second.AttemptID {
		t.Fatalf("retry reused attempt id %q", first.AttemptID)
	}
	if first.AttemptNo != 1 || second.AttemptNo != 2 {
		t.Fatalf("attempt numbers = %d, %d; want 1, 2", first.AttemptNo, second.AttemptNo)
	}
}
