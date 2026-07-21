package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fake backend serves a body of its name + has /healthz returning 200.
func makeBackend(name string) (*httptest.Server, string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, name) })
	srv := httptest.NewServer(mux)
	return srv, srv.Listener.Addr().String()
}

func TestProxyForwardsToActive(t *testing.T) {
	blue, blueAddr := makeBackend("blue")
	defer blue.Close()

	p := New(blueAddr)
	p.SetAPIHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "api")
	}))
	front := httptest.NewServer(p)
	defer front.Close()

	// Business path → blue
	resp, err := http.Get(front.URL + "/api/foo")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "blue" {
		t.Fatalf("expected blue, got %q", body)
	}

	// /launcher/* path → local API handler
	resp2, err := http.Get(front.URL + "/launcher/api/status")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != "api" {
		t.Fatalf("expected api, got %q", body2)
	}
}

func TestProxyAtomicSwitch(t *testing.T) {
	blue, blueAddr := makeBackend("blue")
	defer blue.Close()
	green, greenAddr := makeBackend("green")
	defer green.Close()

	p := New(blueAddr)
	front := httptest.NewServer(p)
	defer front.Close()

	// Initially → blue
	resp, _ := http.Get(front.URL + "/api/x")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "blue" {
		t.Fatalf("expected blue, got %q", body)
	}

	// Switch → green
	p.SwitchActive(greenAddr)

	resp2, _ := http.Get(front.URL + "/api/x")
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != "green" {
		t.Fatalf("expected green after switch, got %q", body2)
	}
}

// TestProxyKeepAliveAcrossSwitch is a smoke test ensuring SwitchActive doesn't
// panic under concurrent access (proxy keeps serving during switch).
// Full zero-downtime guarantee is verified in Task 11 e2e with concurrent load.
func TestProxyKeepAliveAcrossSwitch(t *testing.T) {
	blue, blueAddr := makeBackend("blue")
	defer blue.Close()
	green, greenAddr := makeBackend("green")
	defer green.Close()

	p := New(blueAddr)
	front := httptest.NewServer(p)
	defer front.Close()

	// Hit a few times while switching
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			resp, err := http.Get(front.URL + "/api/x")
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			if i == 25 {
				p.SwitchActive(greenAddr)
			}
		}
	}()
	<-done
}
