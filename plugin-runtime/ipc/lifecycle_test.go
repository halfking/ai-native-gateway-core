package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func TestServerSocketIsOwnerOnly(t *testing.T) {
	path := testSocketPath(t)
	server := NewServer(path)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket permissions = %04o, want 0600", got)
	}
}

func TestClientCloseRejectsNewAndInterruptsActiveRequests(t *testing.T) {
	path := testSocketPath(t)
	server := NewServer(path)
	entered, release := make(chan struct{}), make(chan struct{})
	server.RegisterHandler(MessageTypeHealthCheck, func(context.Context, []byte) ([]byte, error) {
		close(entered)
		<-release
		return []byte(`{"healthy":true}`), nil
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	defer close(release)
	client := NewClient(path)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Send(ctx, []byte(`{"type":"health_check"}`)); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("active request error after Close = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("Close left active request waiting")
	}
	server.RegisterHandler(MessageTypeHealthCheck, func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"healthy":true}`), nil
	})
	if _, err := client.Send(context.Background(), []byte(`{"type":"health_check"}`)); !errors.Is(err, ErrClientClosed) {
		t.Errorf("request error after Close = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientCloseConcurrentSend(t *testing.T) {
	path := testSocketPath(t)
	server := NewServer(path)
	server.RegisterHandler(MessageTypeHealthCheck, func(ctx context.Context, _ []byte) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	client := NewClient(path)
	defer client.Close()
	start := make(chan struct{})
	var requests sync.WaitGroup
	for range 100 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			_, err := client.Send(context.Background(), []byte(`{"type":"health_check"}`))
			if !errors.Is(err, ErrClientClosed) {
				t.Errorf("concurrent Send/Close error = %v", err)
			}
		}()
	}
	close(start)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	requests.Wait()
}

func TestServerStopCancelsAndWaitsForHandler(t *testing.T) {
	path := testSocketPath(t)
	server := NewServer(path)
	entered, canceled, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer server.Stop()
	defer func() { cancel(); close(release) }()
	server.RegisterHandler(MessageTypeHealthCheck, func(ctx context.Context, _ []byte) ([]byte, error) {
		defer close(exited)
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	})
	if err := server.Start(parent); err != nil {
		t.Fatal(err)
	}
	client := NewClient(path)
	defer client.Close()
	requestDone := make(chan error, 1)
	go func() {
		_, err := client.Send(context.Background(), []byte(`{"type":"health_check"}`))
		requestDone <- err
	}()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- server.Stop() }()
	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not cancel active handler")
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before active handler exited")
	default:
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Error("active connection succeeded after Stop")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not close active connection")
	}
	// A second Stop must also wait for the same handler, without holding its lock.
	secondStop := make(chan error, 1)
	go func() { secondStop <- server.Stop() }()
	select {
	case <-secondStop:
		t.Error("second Stop returned before active handler exited")
	case <-time.After(25 * time.Millisecond):
	}
	// The deferred close releases the handler after this assertion phase.
	t.Cleanup(func() {
		for _, done := range []<-chan error{stopped, secondStop} {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(time.Second):
				t.Error("Stop did not finish after handler exit")
			}
		}
		select {
		case <-exited:
		default:
			t.Error("handler still running after Stop")
		}
	})
}

func TestServerRejectsDuplicateStart(t *testing.T) {
	path := testSocketPath(t)
	server := NewServer(path)
	defer server.Stop()
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstListener := server.listener
	defer firstListener.Close()
	if err := server.Start(context.Background()); err == nil {
		t.Error("duplicate Start succeeded")
	}
}

func TestServerStopBeforeStart(t *testing.T) {
	server := NewServer(testSocketPath(t))
	defer server.Stop()
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err == nil {
		t.Error("Start after Stop succeeded")
		server.listener.Close()
	}
}

func TestServerParentCancellationClosesIdleConnection(t *testing.T) {
	path := testSocketPath(t)
	server := NewServer(path)
	defer server.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	if timeout, ok := err.(net.Error); err == nil || (ok && timeout.Timeout()) {
		t.Fatalf("parent cancellation left idle connection open: %v", err)
	}
}
