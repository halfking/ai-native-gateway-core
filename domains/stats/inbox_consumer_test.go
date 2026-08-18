package stats

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestInboxConsumerDefaultsAndNilSafety(t *testing.T) {
	var nilConsumer *InboxConsumer
	nilConsumer.Start(context.Background())
	nilConsumer.Stop()
	if got := nilConsumer.Stats(); got != (InboxStats{}) {
		t.Fatalf("nil consumer stats = %+v, want zero", got)
	}

	consumer := NewInboxConsumerWithDB(nil, InboxConfig{})
	if consumer.cfg.BatchSize != defaultInboxBatchSize || consumer.cfg.Interval != defaultInboxInterval ||
		consumer.cfg.Lease != defaultInboxLease || consumer.cfg.MaxAttempts != defaultInboxMaxTries {
		t.Fatalf("unexpected defaults: %+v", consumer.cfg)
	}
	consumer.Start(context.Background())
	consumer.Stop()
	if err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatalf("nil DB RunOnce = %v, want nil", err)
	}
}

func TestInboxConsumerSQLContracts(t *testing.T) {
	for _, needle := range []string{
		"FOR UPDATE SKIP LOCKED",
		"processing_status IN ('pending', 'retryable')",
		"lease_until IS NULL OR lease_until < now()",
		"fencing_token = event.fencing_token + 1",
		"process_attempts = event.process_attempts + 1",
	} {
		if !strings.Contains(claimSQL, needle) {
			t.Errorf("claim SQL missing %q", needle)
		}
	}
	for _, needle := range []string{
		"processing_status = 'processing'",
		"processing_owner = $3 AND fencing_token = $4",
	} {
		if !strings.Contains(markProcessedSQL, needle) {
			t.Errorf("processed SQL missing fenced condition %q", needle)
		}
	}
	if !strings.Contains(projectLeaseSQL, "lease_until > now()") {
		t.Error("projection lease SQL must reject an expired claim")
	}
	for _, needle := range []string{
		"processing_status = 'dead_letter'",
		"process_attempts = 0",
		"dead_letter_reason = NULL",
	} {
		if !strings.Contains(replaySQL, needle) {
			t.Errorf("replay SQL missing %q", needle)
		}
	}
}

func TestReplayFilterNilConversion(t *testing.T) {
	if got := nullableString(" "); got != nil {
		t.Fatalf("blank string = %#v, want nil", got)
	}
	if got := nullableTime(time.Time{}); got != nil {
		t.Fatalf("zero time = %#v, want nil", got)
	}
}
