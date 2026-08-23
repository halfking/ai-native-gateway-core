package dispatch

import (
	"context"
	"testing"
	"time"
)

func TestParseCredentialsRevision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    uint64
		ok      bool
	}{
		{"valid", "42", 42, true},
		{"trimmed", " 7 ", 7, true},
		{"empty", "", 0, false},
		{"zero", "0", 0, false},
		{"invalid", "abc", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCredentialsRevision(tc.payload)
			if tc.ok && err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected parse error")
			}
			if got != tc.want {
				t.Fatalf("revision = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPolicyPublisherStopBeforeStartIsSafe(t *testing.T) {
	p := NewPolicyPublisher(nil, nil)
	p.Stop()
	p.Stop()
}

func TestPolicyPublisherStartWithoutDependenciesIsNoop(t *testing.T) {
	p := NewPolicyPublisher(nil, nil)
	p.Start(context.Background())
	p.Stop()
}

func TestSleepContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepContext(ctx, time.Hour) {
		t.Fatal("sleepContext returned true after cancellation")
	}
}
