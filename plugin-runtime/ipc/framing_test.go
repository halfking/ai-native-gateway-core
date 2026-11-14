package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerReadsFragmentedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	server := NewServer(path)
	server.RegisterHandler(MessageTypeHealthCheck, func(_ context.Context, request []byte) ([]byte, error) { return request, nil })
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	first, second := []byte(`{"type":"health_`), []byte(`check","id":"fragmented"}`)
	if _, err := conn.Write(first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := conn.Write(second); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil || !bytes.Equal(bytes.TrimSpace(response), append(first, second...)) {
		t.Fatalf("fragmented request: response=%q err=%v", response, err)
	}
}

func TestMessageSizeBoundary(t *testing.T) {
	for _, size := range []int{MaxMessageSize, MaxMessageSize + 1} {
		message := []byte(`"` + strings.Repeat("a", size-2) + `"`)
		var encoded bytes.Buffer
		err := WriteMessage(&encoded, message)
		if size > MaxMessageSize {
			if !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("oversized write: %v", err)
			}
			if _, err := ReadMessage(bytes.NewReader(message)); !errors.Is(err, ErrMessageTooLarge) {
				t.Fatalf("oversized read: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := ReadMessage(&encoded)
		if err != nil || !bytes.Equal(decoded, message) {
			t.Fatalf("maximum supported message: got %d bytes, err=%v", len(decoded), err)
		}
	}
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(data []byte) (int, error) {
	return w.Buffer.Write(data[:min(3, len(data))])
}

func TestWriteMessageCompletesShortWrites(t *testing.T) {
	message := []byte(`{"healthy":true}`)
	writer := new(shortWriter)
	if err := WriteMessage(writer, message); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(writer.Bytes(), message) {
		t.Fatalf("short writer received incomplete message: %q", writer.Bytes())
	}
}

type messageWriterFunc func([]byte) (int, error)

func (f messageWriterFunc) Write(data []byte) (int, error) { return f(data) }

func TestWriteMessageRejectsInvalidJSONAndWriterFailures(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteMessage(&buffer, []byte(`{"type":`)); err == nil || buffer.Len() != 0 {
		t.Fatalf("invalid JSON was written: bytes=%d err=%v", buffer.Len(), err)
	}
	for _, failure := range []struct {
		writer messageWriterFunc
		want   error
	}{
		{func([]byte) (int, error) { return 0, nil }, io.ErrShortWrite},
		{func([]byte) (int, error) { return 0, io.ErrClosedPipe }, io.ErrClosedPipe},
	} {
		if err := WriteMessage(failure.writer, []byte(`{}`)); !errors.Is(err, failure.want) {
			t.Fatalf("writer error = %v, want %v", err, failure.want)
		}
	}
}

func TestScalarMessageDoesNotWaitForEOF(t *testing.T) {
	for _, message := range []string{`123`, `true`, `null`, `"hello"`} {
		reader, writer := net.Pipe()
		if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- WriteMessage(writer, []byte(message)) }()
		response, readErr := ReadMessage(reader)
		writeErr := <-done
		reader.Close()
		writer.Close()
		if readErr != nil || writeErr != nil || string(response) != message {
			t.Fatalf("scalar %s: response=%q read=%v write=%v", message, response, readErr, writeErr)
		}
	}
}

func TestClientReadsFragmentedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		var request json.RawMessage
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			done <- err
			return
		}
		if _, err := conn.Write([]byte(`{"healthy":`)); err != nil {
			done <- err
			return
		}
		time.Sleep(25 * time.Millisecond)
		_, err = conn.Write([]byte(`true}`))
		done <- err
	}()
	client := NewClient(path)
	defer client.Close()
	response, err := client.SendWithTimeout([]byte(`{"type":"health_check"}`), time.Second)
	if peerErr := <-done; peerErr != nil {
		t.Errorf("response writer: %v", peerErr)
	}
	if err != nil || !bytes.Equal(response, []byte(`{"healthy":true}`)) {
		t.Fatalf("fragmented response: response=%q err=%v", response, err)
	}
}
