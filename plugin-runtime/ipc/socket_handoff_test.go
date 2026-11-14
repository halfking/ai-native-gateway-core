package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStopDoesNotUnlinkReplacement(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ipc-handoff-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ipc.sock")
	entered, release := make(chan struct{}), make(chan struct{})
	old := NewServer(path)
	old.RegisterHandler("health_check", func(ctx context.Context, _ []byte) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return nil, ctx.Err()
	})
	if err := old.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := NewClient(path)
	defer client.Close()
	requestDone := make(chan struct{})
	go func() { defer close(requestDone); client.Send(context.Background(), []byte(`{"type":"health_check"}`)) }()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- old.Stop() }()
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			<-stopped
			t.Fatal("listener did not close")
		}
		time.Sleep(time.Millisecond)
	}
	replacement := NewServer(path)
	if err := replacement.Start(context.Background()); err != nil {
		close(release)
		<-stopped
		t.Fatal(err)
	}
	defer replacement.Stop()
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	<-requestDone
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("old Stop removed replacement socket: %v", err)
	}
	conn.Close()
}
