package checker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSource struct {
	rel *FoundRelease
	err error
}

func (f *fakeSource) Check(ctx context.Context) (*FoundRelease, error) {
	return f.rel, f.err
}

func TestCheckerFiresCallbackOnNewVersion(t *testing.T) {
	found := make(chan *FoundRelease, 1)
	c := New(Config{
		CurrentVersion: "v1.4.2",
		Interval:       50 * time.Millisecond,
		Source: &fakeSource{rel: &FoundRelease{
			Version: "v1.5.0", DownloadURL: "http://x", SHA256: "abc",
		}},
		OnUpdate: func(r *FoundRelease) { found <- r },
	})

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)

	select {
	case r := <-found:
		if r.Version != "v1.5.0" {
			t.Fatalf("expected v1.5.0, got %s", r.Version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: callback not fired")
	}
	
	// Cancel context before Stop to prevent deadlock from repeated callbacks
	cancel()
	c.Stop()
}

func TestCheckerNoUpdateNoCallback(t *testing.T) {
	fired := false
	c := New(Config{
		CurrentVersion: "v1.4.2",
		Interval:       50 * time.Millisecond,
		Source:         &fakeSource{rel: nil},
		OnUpdate:       func(r *FoundRelease) { fired = true },
	})

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	c.Stop()
	if fired {
		t.Fatal("callback should not fire when no update")
	}
}

func TestCheckerOfflineNoCrash(t *testing.T) {
	c := New(Config{
		CurrentVersion: "v1.4.2",
		Interval:       50 * time.Millisecond,
		Source:         &fakeSource{err: errors.New("offline")},
		OnUpdate:       func(r *FoundRelease) { t.Error("should not fire when offline") },
	})

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	c.Stop()
	// No panic, no callback — just logged at debug level
}

func TestCheckerSkipsSameVersion(t *testing.T) {
	fired := false
	c := New(Config{
		CurrentVersion: "v1.4.2",
		Interval:       50 * time.Millisecond,
		Source: &fakeSource{rel: &FoundRelease{Version: "v1.4.2"}},
		OnUpdate:       func(r *FoundRelease) { fired = true },
	})

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	c.Stop()
	if fired {
		t.Fatal("callback should not fire for same version")
	}
}