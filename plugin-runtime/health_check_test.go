package pluginruntime

import (
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestPreciseHealthCheck_Healthy(t *testing.T) {
	socketPath := "/tmp/p8hc-ok.sock"
	os.Remove(socketPath)
	t.Cleanup(func() { os.Remove(socketPath) })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plugin/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"status":"ready"}`)
			return
		}
		http.NotFound(w, r)
	})}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()
	go srv.Serve(ln)

	check := PreciseHealthCheck(socketPath, "/plugin/healthz", 2*time.Second)
	if err := check(); err != nil {
		t.Fatalf("healthy plugin should pass, got %v", err)
	}
}

func TestPreciseHealthCheck_Unhealthy(t *testing.T) {
	socketPath := "/tmp/p8hc-bad.sock"
	os.Remove(socketPath)
	t.Cleanup(func() { os.Remove(socketPath) })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer srv.Close()
	go srv.Serve(ln)

	check := PreciseHealthCheck(socketPath, "/plugin/healthz", 2*time.Second)
	if err := check(); err == nil {
		t.Fatal("unhealthy plugin (500) should fail health check")
	}
}

func TestPreciseHealthCheck_Unreachable(t *testing.T) {
	check := PreciseHealthCheck("/tmp/p8hc-nonexistent.sock", "/plugin/healthz", 1*time.Second)
	if err := check(); err == nil {
		t.Fatal("unreachable socket should fail")
	}
}
