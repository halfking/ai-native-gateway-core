package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSocketPath(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(dir, "s.sock")
}

func TestSendCancellationInterruptsWrite(t *testing.T) {
	path := testSocketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	writing, release := make(chan error, 1), make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			writing <- err
			return
		}
		defer conn.Close()
		_, err = conn.Read(make([]byte, 1))
		writing <- err
		// The remaining valid payload is larger than the socket send buffer.
		<-release
	}()
	client := NewClient(path)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := []byte(`{"type":"health_check","data":"` + strings.Repeat("a", 10<<20) + `"}`)
	done := make(chan error, 1)
	go func() { _, err := client.Send(ctx, request); done <- err }()
	select {
	case err := <-writing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client never started writing")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled write: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation did not interrupt blocked write")
	}
}

func TestSendCanceledBeforeDial(t *testing.T) {
	client := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Send(ctx, []byte(`{"type":"health_check"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send with canceled context = %v", err)
	}
}

func TestSendUnavailablePeer(t *testing.T) {
	client := NewClient(testSocketPath(t))
	defer client.Close()
	_, err := client.Send(context.Background(), []byte(`{"type":"health_check"}`))
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrClientClosed) {
		t.Fatalf("unavailable peer error = %v", err)
	}
}

func TestSendCancellationInterruptsRead(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
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
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 75*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := client.Send(ctx, []byte(`{"type":"health_check"}`)); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("handler did not start")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("Send error = %v, want %v", err, want)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("Send did not stop promptly after cancellation")
			}
		})
	}
}
