package sanitizerules

import (
	"bytes"
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisWithheldSubscribeACKHasDeadline(t *testing.T) {
	server := miniredis.RunT(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sockets []net.Conn
	accepted := make(chan struct{})
	seen := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(accepted)
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", server.Addr())
			if err != nil {
				_ = client.Close()
				continue
			}
			mu.Lock()
			sockets = append(sockets, client, upstream)
			mu.Unlock()
			var mute atomic.Bool
			go func() {
				buffer := make([]byte, 65536)
				pending := []byte{}
				for {
					n, err := client.Read(buffer)
					if n > 0 {
						pending = append(pending, buffer[:n]...)
						if bytes.Contains(bytes.ToLower(pending), []byte("\r\nsubscribe\r\n")) {
							mute.Store(true)
							once.Do(func() { close(seen) })
						}
						if len(pending) > 65536 {
							pending = nil
						}
						if _, writeErr := upstream.Write(buffer[:n]); writeErr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			go func() {
				buffer := make([]byte, 65536)
				for {
					n, err := upstream.Read(buffer)
					if n > 0 && !mute.Load() {
						if _, writeErr := client.Write(buffer[:n]); writeErr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	defer func() {
		_ = listener.Close()
		<-accepted
		mu.Lock()
		defer mu.Unlock()
		for _, socket := range sockets {
			_ = socket.Close()
		}
	}()
	client := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), Protocol: 2, DisableIdentity: true, ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond, MaxRetries: -1})
	defer client.Close()
	notifications := NewRedisNotifications(client, "ack-test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := notifications.Subscribe(ctx); done <- err }()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("SUBSCRIBE never reached proxy")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing ACK accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("withheld ACK blocked subscription indefinitely")
	}
}
