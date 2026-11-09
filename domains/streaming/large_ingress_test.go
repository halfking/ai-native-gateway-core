package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

type forbiddenRead struct{ read bool }

func (r *forbiddenRead) Read([]byte) (int, error) { r.read = true; return 0, io.EOF }
func TestLargeIngressAuthenticatesBeforeReading(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.staticDataPlaneKey = "correct"
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	rbody := &forbiddenRead{}
	r := httptest.NewRequest("POST", "/v1/chat/completions", rbody)
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unauthorized forwarded") })).ServeHTTP(w, r)
	if w.Code != 401 || rbody.read {
		t.Fatalf("status=%d read=%v", w.Code, rbody.read)
	}
}
func TestLargeIngressArchivesRawBeforeDownstreamAndKeepsSmallBody(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref, ok := LargeRequestArchive(r.Context())
		if !ok {
			t.Fatal("missing archive reference")
		}
		if err := store.Verify(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, body) {
			t.Fatalf("body changed: %s", got)
		}
		w.WriteHeader(200)
	})
	w := httptest.NewRecorder()
	ingress.Wrap(next).ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestLargeIngressSupports100MBUnknownLengthWithBoundedForward(t *testing.T) {
	if testing.Short() {
		t.Skip("100MB real disk integration")
	}
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	pre := `{"messages":[{"role":"user","content":"Maintain billing"},{"role":"assistant","content":"`
	post := `"},{"role":"user","content":"Find billing port"}],"model":"last-model"}`
	count := largebody.LimitBytes - int64(len(pre)+len(post))
	source := io.MultiReader(strings.NewReader(pre), io.LimitReader(&fillIngress{}, count), strings.NewReader(post))
	r := httptest.NewRequest("POST", "/v1/chat/completions", source)
	r.ContentLength = -1
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref, ok := LargeRequestArchive(r.Context())
		if !ok || ref.Size != largebody.LimitBytes {
			t.Fatalf("archive %+v", ref)
		}
		got, _ := io.ReadAll(r.Body)
		if len(got) > 1<<20 || !json.Valid(got) || !bytes.Contains(got, []byte("Find billing port")) {
			t.Fatalf("invalid workset %d", len(got))
		}
		if _, err := store.Recover(context.Background(), ref.Identity); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(200)
	})
	w := httptest.NewRecorder()
	ingress.Wrap(next).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestLargeIngressRejects100MBPlusOneBeforeForward(t *testing.T) {
	store, _ := largebody.NewStore(t.TempDir())
	defer store.Close()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	ingress := NewLargeIngress(h, store, LargeIngressConfig{})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("x"))
	r.ContentLength = largebody.LimitBytes + 1
	w := httptest.NewRecorder()
	ingress.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("oversized forwarded") })).ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("status %d", w.Code)
	}
}

type fillIngress struct{}

func (*fillIngress) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
