package sanitizerules

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestManagerRecordDoesNotCallBlockingAuditSettings(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	m, err := NewManager(NewMemoryStore(), testConfig(), Options{
		InstanceID: "async-audit-setting",
		AuditEnabled: func() bool {
			enterOnce.Do(func() { close(entered) })
			<-release
			return true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}
	assertPrompt := func() {
		t.Helper()
		done := make(chan bool, 1)
		go func() { done <- m.Record(context.Background(), event) }()
		select {
		case accepted := <-done:
			if !accepted {
				t.Error("valid event was not accepted into queue")
			}
		case <-time.After(100 * time.Millisecond):
			releaseOnce.Do(func() { close(release) })
			<-done
			t.Fatal("request-path Record waited for the audit setting database callback")
		}
	}
	assertPrompt()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { releaseOnce.Do(func() { close(release) }); cancel(); <-done }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background writer never consulted the live audit setting")
	}
	assertPrompt()
}
