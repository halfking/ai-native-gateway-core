package streaming

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectionMonitorCancelsOnCloseNotify(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	monCtx, mon := NewConnectionMonitor(r.Context(), w, WithConnectionMonitorInterval(time.Millisecond))
	mon.Stop()
	select {
	case <-monCtx.Done():
	default:
		t.Fatal("monitor context should be canceled on Stop")
	}
}

func TestConnectionMonitorProbeCancelsAfterIdle(t *testing.T) {
	var probes atomic.Int32
	monCtx, mon := NewConnectionMonitor(context.Background(), httptest.NewRecorder(),
		WithConnectionMonitorInterval(time.Millisecond),
		WithConnectionMonitorIdleTimeout(time.Millisecond),
		WithConnectionMonitorProbe(func() bool { probes.Add(1); return false }),
	)
	defer mon.Stop()
	select {
	case <-monCtx.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("monitor did not cancel after failed probe")
	}
	if probes.Load() == 0 {
		t.Fatal("expected probe invocation")
	}
}

func TestConnectionMonitorTouchPreventsProbe(t *testing.T) {
	var probes atomic.Int32
	monCtx, mon := NewConnectionMonitor(context.Background(), httptest.NewRecorder(),
		WithConnectionMonitorInterval(time.Millisecond),
		WithConnectionMonitorIdleTimeout(20*time.Millisecond),
		WithConnectionMonitorProbe(func() bool { probes.Add(1); return false }),
	)
	defer mon.Stop()
	for i := 0; i < 10; i++ {
		mon.Touch()
		time.Sleep(2 * time.Millisecond)
	}
	if probes.Load() != 0 {
		t.Fatalf("probe ran while monitor was active: %d", probes.Load())
	}
	select {
	case <-monCtx.Done():
		t.Fatal("monitor canceled during active stream")
	default:
	}
}

func TestMonitoredResponseWriterTouchesOnlySuccessfulOperations(t *testing.T) {
	w := httptest.NewRecorder()
	monCtx, mon := NewConnectionMonitor(context.Background(), w)
	defer mon.Stop()
	mw := &monitoredResponseWriter{delegate: w, monitor: mon}
	if _, err := mw.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-monCtx.Done():
		t.Fatal("unexpected cancellation")
	default:
	}
}
